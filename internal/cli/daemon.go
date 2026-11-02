package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/daemon"
	"github.com/mars-base/pgcli/internal/platform"
)

var (
	daemonAddDir      string
	daemonAddLogFile  string
	daemonAddForce    bool
	daemonStopTimeout time.Duration
	daemonLogsTail    int
	daemonLogsFollow  bool
	daemonRemoveClean bool
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run long-lived host commands in the background (Linux only)",
	Long: `Register and control long-running host processes without a container.

Daemons are declared with "pg daemon add", started explicitly with "pg daemon
start", and can be started at boot through the existing autostart service:

  pg autostart enable --daemon --name <name>

The command is resolved to an absolute path when it is added, so starting at
boot does not depend on PATH. State lives under <base>/daemon/<name>/ unless a
custom --log-file is used.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if platform.Detect() != platform.Linux {
			return fmt.Errorf("pg daemon requires Linux")
		}
		return nil
	},
}

var daemonAddCmd = &cobra.Command{
	Use:   "add <name> [--dir DIR] [--log-file PATH] [--force] -- <command> [args...]",
	Short: "Register a background command",
	Long: `Register a daemon without starting it.

The command must be separated from the daemon name by "--". Flags intended for
the command belong after "--"; flags before it configure pg daemon itself.

Examples:
  pg daemon add webhook --dir /opt/webhook -- /opt/webhook/.venv/bin/uvicorn app:app --port 18801
  pg daemon add tick -- /bin/sh -c 'while true; do echo tick; sleep 1; done'`,
	Args: cobra.MinimumNArgs(2),
	RunE: runDaemonAdd,
}

var daemonStartCmd = &cobra.Command{
	Use:   "start <name>",
	Short: "Start a registered daemon",
	Args:  cobra.ExactArgs(1),
	RunE:  runDaemonStart,
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop <name>",
	Short: "Stop a running daemon",
	Args:  cobra.ExactArgs(1),
	RunE:  runDaemonStop,
}

var daemonRestartCmd = &cobra.Command{
	Use:   "restart <name>",
	Short: "Restart a daemon",
	Args:  cobra.ExactArgs(1),
	RunE:  runDaemonRestart,
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status [<name>]",
	Short: "Show daemon status",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runDaemonStatus,
}

var daemonListCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered daemons",
	Args:  cobra.NoArgs,
	RunE:  runDaemonList,
}

var daemonLogsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "Show a daemon's log",
	Args:  cobra.ExactArgs(1),
	RunE:  runDaemonLogs,
}

var daemonRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Stop and unregister a daemon",
	Args:  cobra.ExactArgs(1),
	RunE:  runDaemonRemove,
}

func init() {
	rootCmd.AddCommand(daemonCmd)
	daemonCmd.AddCommand(daemonAddCmd, daemonStartCmd, daemonStopCmd, daemonRestartCmd,
		daemonStatusCmd, daemonListCmd, daemonLogsCmd, daemonRemoveCmd)

	daemonAddCmd.Flags().StringVar(&daemonAddDir, "dir", "", "working directory (default: current directory)")
	daemonAddCmd.Flags().StringVar(&daemonAddLogFile, "log-file", "", "log file (default: <base>/daemon/<name>/daemon.log; relative paths resolve against --dir)")
	daemonAddCmd.Flags().BoolVar(&daemonAddForce, "force", false, "replace an existing daemon (must be stopped)")
	daemonStopCmd.Flags().DurationVar(&daemonStopTimeout, "timeout", 10*time.Second, "grace period before SIGKILL")
	daemonLogsCmd.Flags().IntVarP(&daemonLogsTail, "tail", "n", 100, "number of lines to show")
	daemonLogsCmd.Flags().BoolVarP(&daemonLogsFollow, "follow", "f", false, "follow the log")
	daemonRemoveCmd.Flags().BoolVar(&daemonRemoveClean, "clean-log", false, "delete the daemon state directory (default log and pid)")
}

func runDaemonAdd(cmd *cobra.Command, args []string) error {
	if cmd.ArgsLenAtDash() != 1 {
		return fmt.Errorf(`separate the daemon name from its command with "--": pg daemon add webhook -- /path/to/command [args...]`)
	}
	name := args[0]
	command := args[1:]
	if !config.ValidDaemonName(name) {
		return fmt.Errorf("invalid daemon name %q: must match [A-Za-z0-9][A-Za-z0-9_-]{0,47}", name)
	}

	dir := daemonAddDir
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("getting current directory: %w", err)
		}
	}
	var err error
	dir, err = filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolving --dir: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("working directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("working directory %s is not a directory", dir)
	}

	bin := command[0]
	if strings.ContainsRune(bin, filepath.Separator) {
		if !filepath.IsAbs(bin) {
			bin = filepath.Join(dir, bin)
		}
		bin, err = filepath.Abs(bin)
		if err != nil {
			return fmt.Errorf("resolving command %q: %w", command[0], err)
		}
		binInfo, statErr := os.Stat(bin)
		if statErr != nil {
			return fmt.Errorf("command %q: %w", bin, statErr)
		}
		if binInfo.IsDir() || binInfo.Mode()&0o111 == 0 {
			return fmt.Errorf("command %q is not executable", bin)
		}
	} else {
		bin, err = exec.LookPath(bin)
		if err != nil {
			return fmt.Errorf("command %q not found in PATH: %w", command[0], err)
		}
		bin, err = filepath.Abs(bin)
		if err != nil {
			return fmt.Errorf("resolving command %q: %w", bin, err)
		}
	}

	logFile := daemonAddLogFile
	if logFile != "" {
		if !filepath.IsAbs(logFile) {
			logFile = filepath.Join(dir, logFile)
		}
		logFile, err = filepath.Abs(logFile)
		if err != nil {
			return fmt.Errorf("resolving --log-file: %w", err)
		}
	}

	if err := loadConfigForDSN(); err != nil {
		return err
	}
	old, exists := cfg.Daemons[name]
	if exists && !daemonAddForce {
		return fmt.Errorf("daemon %q already exists; use --force to replace it", name)
	}
	if exists && daemonAddForce && daemon.Running(cfg, name) {
		return fmt.Errorf("daemon %q is running; stop it before --force", name)
	}

	dc := config.DaemonConfig{
		Command: append([]string{bin}, command[1:]...),
		Dir:     dir,
		LogFile: logFile,
	}
	if exists {
		dc.Autostart = old.Autostart
	}
	if cfg.Daemons == nil {
		cfg.Daemons = make(map[string]config.DaemonConfig)
	}
	cfg.Daemons[name] = dc
	if err := cfg.Save(cfgPath); err != nil {
		return err
	}

	layout := daemon.NewLayout(cfg, name)
	logPath := layout.LogFile
	if dc.LogFile != "" {
		logPath = dc.LogFile
	}
	fmt.Printf("  [OK] daemon %q registered\n", name)
	fmt.Printf("  Command: %s\n", daemon.CommandLine(dc))
	fmt.Printf("  Dir:     %s\n", dc.Dir)
	fmt.Printf("  Log:     %s\n", logPath)
	fmt.Printf("\nStart it with: pg daemon start %s\n", name)
	return nil
}

func runDaemonStart(cmd *cobra.Command, args []string) error {
	if err := loadConfigForDSN(); err != nil {
		return err
	}
	name := args[0]
	dc, ok := cfg.Daemons[name]
	if !ok {
		return daemonNotFound(name)
	}
	already, err := daemon.Start(cfg, name)
	if err != nil {
		return err
	}
	if already {
		fmt.Printf("  [OK] daemon %q already running (pid %d)\n", name, daemon.PID(cfg, name))
		return nil
	}
	fmt.Printf("  [OK] daemon %q started (pid %d)\n", name, daemon.PID(cfg, name))
	fmt.Printf("  Log: %s\n", daemon.LogFile(cfg, name, dc))
	return nil
}

func runDaemonStop(cmd *cobra.Command, args []string) error {
	if err := loadConfigForDSN(); err != nil {
		return err
	}
	name := args[0]
	if _, ok := cfg.Daemons[name]; !ok {
		return daemonNotFound(name)
	}
	if !daemon.Running(cfg, name) {
		fmt.Printf("  [OK] daemon %q is not running\n", name)
		return nil
	}
	if err := daemon.Stop(cfg, name, daemonStopTimeout); err != nil {
		return err
	}
	fmt.Printf("  [OK] daemon %q stopped\n", name)
	return nil
}

func runDaemonRestart(cmd *cobra.Command, args []string) error {
	if err := loadConfigForDSN(); err != nil {
		return err
	}
	name := args[0]
	dc, ok := cfg.Daemons[name]
	if !ok {
		return daemonNotFound(name)
	}
	if daemon.Running(cfg, name) {
		if err := daemon.Stop(cfg, name, daemonStopTimeout); err != nil {
			return err
		}
	}
	already, err := daemon.Start(cfg, name)
	if err != nil {
		return err
	}
	if already {
		fmt.Printf("  [OK] daemon %q already running (pid %d)\n", name, daemon.PID(cfg, name))
		return nil
	}
	fmt.Printf("  [OK] daemon %q restarted (pid %d)\n", name, daemon.PID(cfg, name))
	fmt.Printf("  Log: %s\n", daemon.LogFile(cfg, name, dc))
	return nil
}

func runDaemonStatus(cmd *cobra.Command, args []string) error {
	if err := loadConfigForDSN(); err != nil {
		return err
	}
	var names []string
	if len(args) == 1 {
		if _, ok := cfg.Daemons[args[0]]; !ok {
			return daemonNotFound(args[0])
		}
		names = args[:1]
	} else {
		for name := range cfg.Daemons {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	if len(names) == 0 {
		fmt.Println("No daemons configured.")
		return nil
	}
	for i, name := range names {
		if i > 0 {
			fmt.Println()
		}
		printDaemonStatus(name, cfg.Daemons[name])
	}
	return nil
}

func printDaemonStatus(name string, dc config.DaemonConfig) {
	status := "stopped"
	if daemon.Running(cfg, name) {
		status = fmt.Sprintf("running (pid %d)", daemon.PID(cfg, name))
	}
	autostart := "off"
	if dc.Autostart {
		autostart = "on"
	}
	fmt.Printf("daemon %q\n", name)
	fmt.Printf("  Status:    %s\n", status)
	fmt.Printf("  Command:   %s\n", daemon.CommandLine(dc))
	fmt.Printf("  Dir:       %s\n", dc.Dir)
	fmt.Printf("  Log:       %s\n", daemon.LogFile(cfg, name, dc))
	fmt.Printf("  PID file:  %s\n", daemon.PIDFile(cfg, name))
	fmt.Printf("  Autostart: %s\n", autostart)
}

func runDaemonList(cmd *cobra.Command, args []string) error {
	if err := loadConfigForDSN(); err != nil {
		return err
	}
	if len(cfg.Daemons) == 0 {
		fmt.Println("No daemons configured.")
		return nil
	}
	names := make([]string, 0, len(cfg.Daemons))
	for name := range cfg.Daemons {
		names = append(names, name)
	}
	sort.Strings(names)

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATUS\tPID\tAUTOSTART\tCOMMAND")
	for _, name := range names {
		dc := cfg.Daemons[name]
		status := "stopped"
		pid := "-"
		if daemon.Running(cfg, name) {
			status = "running"
			pid = fmt.Sprintf("%d", daemon.PID(cfg, name))
		}
		autostart := "off"
		if dc.Autostart {
			autostart = "on"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, status, pid, autostart, daemon.CommandLine(dc))
	}
	return w.Flush()
}

func runDaemonLogs(cmd *cobra.Command, args []string) error {
	if err := loadConfigForDSN(); err != nil {
		return err
	}
	name := args[0]
	dc, ok := cfg.Daemons[name]
	if !ok {
		return daemonNotFound(name)
	}
	return tailHostLog(daemon.LogFile(cfg, name, dc), daemonLogsTail, daemonLogsFollow)
}

func runDaemonRemove(cmd *cobra.Command, args []string) error {
	if err := loadConfigForDSN(); err != nil {
		return err
	}
	name := args[0]
	dc, ok := cfg.Daemons[name]
	if !ok {
		return daemonNotFound(name)
	}
	if err := daemon.Stop(cfg, name, daemonStopTimeout); err != nil {
		return err
	}
	delete(cfg.Daemons, name)
	if err := cfg.Save(cfgPath); err != nil {
		return err
	}
	if dc.Autostart {
		if err := syncBootService(cfgPath); err != nil {
			return err
		}
	}
	if daemonRemoveClean {
		layout := daemon.NewLayout(cfg, name)
		if err := os.RemoveAll(layout.StateDir); err != nil {
			return fmt.Errorf("removing %s: %w", layout.StateDir, err)
		}
		if dc.LogFile != "" {
			fmt.Printf("  [!] custom log file left in place: %s\n", dc.LogFile)
		}
	}
	fmt.Printf("  [OK] daemon %q removed\n", name)
	return nil
}

func daemonNotFound(name string) error {
	return fmt.Errorf("daemon %q not found in config", name)
}
