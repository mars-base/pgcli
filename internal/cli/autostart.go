package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/autostart"
	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

func init() {
	rootCmd.AddCommand(autostartCmd)
	autostartCmd.AddCommand(autostartEnableCmd)
	autostartCmd.AddCommand(autostartDisableCmd)
	autostartCmd.AddCommand(autostartStatusCmd)
	autostartCmd.AddCommand(autostartListCmd)

	for _, c := range []*cobra.Command{autostartEnableCmd, autostartDisableCmd} {
		c.Flags().Bool("backup", false, "select the shared backup container")
		c.Flags().Bool("pgbouncer", false, "select the PgBouncer addon (use with -i for a local pooler, --pg-name for a remote one)")
		c.Flags().String("pg-name", "", "name of a remote PgBouncer (top-level addons)")
	}
}

var autostartCmd = &cobra.Command{
	Use:   "autostart",
	Short: "Manage auto-start on host boot",
	Long: `autostart controls which instances and addons are started automatically
after a host reboot, via a systemd user unit (Linux) or a launchd LaunchAgent
(macOS) that runs 'pg start --autostart' at boot/login.

Note: 'pg stop' does NOT affect autostart — autostart is purely config-driven.
Use 'pg autostart disable' to opt out.`,
}

var autostartEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Enable auto-start for an instance, the backup container, or a PgBouncer",
	Long: `Enable auto-start on boot for exactly one target:

  pg autostart enable -i myinst          # instance
  pg autostart enable --backup           # shared backup container
  pg autostart enable --pgbouncer -i myinst      # local PgBouncer for an instance
  pg autostart enable --pgbouncer --pg-name x    # remote PgBouncer

On Linux the boot service is a systemd --user unit. True boot-time start
(without a login) requires loginctl enable-linger — attempted automatically,
with a hint printed if it fails. On macOS a LaunchAgent runs at GUI login.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAutostartToggle(cmd, true)
	},
}

var autostartDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Disable auto-start for a target",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAutostartToggle(cmd, false)
	},
}

var autostartStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show auto-start configuration and boot service state",
	Args:  cobra.NoArgs,
	RunE:  runAutostartStatus,
}

var autostartListCmd = &cobra.Command{
	Use:    "list",
	Short:  "List auto-start targets (alias of status)",
	Args:   cobra.NoArgs,
	Hidden: true,
	RunE:   runAutostartStatus,
}

// autostartConfigPath resolves the config path without requiring a full load.
func autostartConfigPath() string {
	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	return path
}

// runAutostartToggle flips the autostart field for exactly one target and
// keeps the boot service in sync with the config.
func runAutostartToggle(cmd *cobra.Command, enable bool) error {
	path := autostartConfigPath()
	if err := loadConfig(); err != nil {
		return err
	}

	backupSel, _ := cmd.Flags().GetBool("backup")
	pgbSel, _ := cmd.Flags().GetBool("pgbouncer")
	pgName, _ := cmd.Flags().GetString("pg-name")
	instChanged := cmd.Flags().Changed("instance")

	// Exactly one selector required.
	sel := 0
	if instChanged && !pgbSel {
		sel++
	}
	if backupSel {
		sel++
	}
	if pgbSel {
		sel++
	}
	if sel != 1 {
		return fmt.Errorf("select exactly one target: -i <name>, --backup, or --pgbouncer")
	}
	if pgName != "" && !pgbSel {
		return fmt.Errorf("--pg-name requires --pgbouncer")
	}

	var targetDesc string
	switch {
	case backupSel:
		cfg.Backup.Autostart = enable
		targetDesc = "backup container"
	case pgbSel && pgName != "":
		pb, ok := cfg.Addons.PgBouncer[pgName]
		if !ok {
			return fmt.Errorf("remote PgBouncer %q not found in config", pgName)
		}
		pb.Autostart = enable
		cfg.Addons.PgBouncer[pgName] = pb
		targetDesc = fmt.Sprintf("remote PgBouncer %q", pgName)
	case pgbSel:
		inst, ok := cfg.Instances[cfgInstance]
		if !ok {
			return fmt.Errorf("instance %q not found in config", cfgInstance)
		}
		if inst.Addons.PgBouncer == nil {
			return fmt.Errorf("instance %q has no PgBouncer installed (run 'pg addon install pgbouncer -i %s')", cfgInstance, cfgInstance)
		}
		inst.Addons.PgBouncer.Autostart = enable
		cfg.Instances[cfgInstance] = inst
		targetDesc = fmt.Sprintf("PgBouncer of instance %q", cfgInstance)
	default:
		inst, ok := cfg.Instances[cfgInstance]
		if !ok {
			return fmt.Errorf("instance %q not found in config", cfgInstance)
		}
		inst.Autostart = enable
		cfg.Instances[cfgInstance] = inst
		targetDesc = fmt.Sprintf("instance %q", cfgInstance)
	}

	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	action := "disabled"
	if enable {
		action = "enabled"
	}
	fmt.Printf("  [OK] Auto-start %s for %s\n", action, targetDesc)

	return syncBootService(path)
}

// syncBootService installs/refreshes the boot service when at least one
// target is enabled, and uninstalls it when none are.
func syncBootService(path string) error {
	svc, err := autostart.For(path)
	if err != nil {
		return err
	}
	inst := autostart.NewInstaller()

	if countAutostartTargets(cfg) == 0 {
		fmt.Printf("-> No autostart targets left; removing boot service %s\n", svc.UnitName)
		if err := inst.Uninstall(svc); err != nil {
			return fmt.Errorf("removing boot service: %w", err)
		}
		fmt.Printf("  [OK] Boot service removed\n")
		return nil
	}

	if err := inst.Install(svc); err != nil {
		return fmt.Errorf("installing boot service: %w", err)
	}
	fmt.Printf("  [OK] Boot service %s installed (runs 'pg -c %s start --autostart')\n", svc.UnitName, svc.ConfigPath)
	return nil
}

func countAutostartTargets(c *config.Config) int {
	n := 0
	if c.Backup.Autostart {
		n++
	}
	for _, inst := range c.Instances {
		if inst.Autostart {
			n++
		}
		if inst.Addons.PgBouncer != nil && inst.Addons.PgBouncer.Autostart {
			n++
		}
	}
	for _, pb := range c.Addons.PgBouncer {
		if pb.Autostart {
			n++
		}
	}
	return n
}

func runAutostartStatus(cmd *cobra.Command, args []string) error {
	path := autostartConfigPath()
	if err := loadConfig(); err != nil {
		return err
	}

	fmt.Println("=== Auto-start targets ===")
	for name, inst := range cfg.Instances {
		fmt.Printf("  instance %-15s %s\n", name, onOff(inst.Autostart))
		if inst.Addons.PgBouncer != nil {
			fmt.Printf("    pgbouncer %-11s %s\n", name, onOff(inst.Addons.PgBouncer.Autostart))
		}
	}
	fmt.Printf("  backup %-15s %s\n", "", onOff(cfg.Backup.Autostart))
	for name, pb := range cfg.Addons.PgBouncer {
		fmt.Printf("  pgbouncer %-13s %s (remote)\n", name, onOff(pb.Autostart))
	}

	fmt.Println("\n=== Boot service ===")
	svc, err := autostart.For(path)
	if err != nil {
		return err
	}
	info, err := autostart.NewInstaller().Status(svc)
	if err != nil {
		return fmt.Errorf("checking boot service: %w", err)
	}
	fmt.Printf("  Unit:     %s\n", info.UnitName)
	fmt.Printf("  Installed: %s\n", onOff(info.Installed))
	fmt.Printf("  Enabled:  %s\n", onOff(info.Enabled))
	fmt.Printf("  Running:  %s\n", onOff(info.Running))
	if info.Linger != "" {
		fmt.Printf("  Linger:   %s\n", info.Linger)
	}
	if info.Stale != "" {
		fmt.Printf("  [!] %s\n", info.Stale)
	}

	if info.Installed {
		fmt.Printf("\nBoot command: %s -c %s start --autostart\n", svc.Binary, svc.ConfigPath)
	}
	fmt.Println("\nNote: 'pg stop' does not affect auto-start; use 'pg autostart disable' to opt out.")
	return nil
}

func onOff(v bool) string {
	if v {
		return "enabled"
	}
	return "disabled"
}
