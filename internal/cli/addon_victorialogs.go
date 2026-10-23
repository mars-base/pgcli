package cli

import (
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

// runAddonInstallVictoriaLogs installs a standalone VictoriaLogs container —
// the log database of the VictoriaMetrics family — from the official
// docker.io/victoriametrics/victoria-logs image. It is a top-level addon
// (addons.victorialogs.<name>, key defaults to "vl") on one host port with a
// persistent data dir.
//
// Like VictoriaMetrics it needs NO rendered config file: everything is passed
// as CLI flags after the image name. Install always recreates the container
// (--force semantics) since VictoriaLogs does not support hot config reload.
// The data dir outlives the container — remove+reinstall revives stored logs.
//
// Retention is optional: --retention accepts a duration ("30d", "3months",
// "1y") and is passed through verbatim; omitting it lets the binary's own
// default stand.
//
// There is NO login/password: VictoriaLogs ships no authentication by default.
// Its HTTP API can ingest and query arbitrary logs via LogsQL, so a
// remote-exposed instance is guarded only by the network boundary (firewall /
// --listen 127.0.0.1), same as VictoriaMetrics.
func runAddonInstallVictoriaLogs(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vl"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	port, _ := cmd.Flags().GetInt("port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	retention, _ := cmd.Flags().GetString("retention")
	extraArgs, _ := cmd.Flags().GetStringArray("extra-arg")
	force, _ := cmd.Flags().GetBool("force")

	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s -- run \"pg config init\" first", path)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if cfg.Addons.VictoriaLogs == nil {
		cfg.Addons.VictoriaLogs = make(map[string]config.VictoriaLogsConfig)
	}
	existing, ok := cfg.Addons.VictoriaLogs[name]
	if !ok {
		existing = config.VictoriaLogsConfig{
			ContainerName: "pgcli-victorialogs" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	previous := existing // stored config before this run's flags, for change detection
	if dataDir != "" {
		existing.DataDir = dataDir
	}
	if cmd.Flags().Changed("port") {
		existing.HostPort = port
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if cmd.Flags().Changed("retention") {
		existing.Retention = retention
	}
	if cmd.Flags().Changed("extra-arg") {
		existing.ExtraArgs = extraArgs
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	cfg.Addons.VictoriaLogs[name] = existing

	cfg.ApplyDefaults()
	lc := cfg.Addons.VictoriaLogs[name]

	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	vl, err := podman.NewVictoriaLogsManager(cfg)
	if err != nil {
		return fmt.Errorf("VictoriaLogs manager: %w", err)
	}

	// Recreate when the config actually changed (VictoriaLogs has no reload);
	// an idempotent reinstall with identical config is a no-op. --force always
	// recreates.
	running, err := vl.ContainerRunning(lc.ContainerName)
	if err != nil {
		return err
	}
	if running && !force && reflect.DeepEqual(previous, lc) {
		fmt.Println("  [OK] VictoriaLogs running, config unchanged")
		cfg.Addons.VictoriaLogs[name] = lc
		if err := cfg.Save(path); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		printVictoriaLogsSummary(&lc, vl, name, false, false)
		return nil
	}

	fmt.Printf("-> Preparing VictoriaLogs image %s...\n", lc.ImageTag)
	if err := vl.EnsureImage(lc.ImageTag); err != nil {
		return err
	}

	fmt.Println("-> Starting VictoriaLogs container...")
	if err := vl.EnsureContainer(&lc); err != nil {
		return err
	}

	cfg.Addons.VictoriaLogs[name] = lc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	printVictoriaLogsSummary(&lc, vl, name, true, force)
	return nil
}

func printVictoriaLogsSummary(lc *config.VictoriaLogsConfig, vl *podman.VictoriaLogsManager, name string, installed, force bool) {
	fmt.Println()
	switch {
	case installed && force:
		fmt.Printf("✓ VictoriaLogs %q recreated (--force)\n", name)
	case installed:
		fmt.Printf("✓ VictoriaLogs installed: %q\n", name)
	default:
		fmt.Printf("✓ VictoriaLogs %q (unchanged)\n", name)
	}
	fmt.Printf("  Container:  %s\n", lc.ContainerName)
	fmt.Printf("  Image:      %s\n", lc.ImageTag)
	fmt.Printf("  Data:       %s\n", vl.DataDir(lc))
	if lc.Retention != "" {
		fmt.Printf("  Retention:  %s\n", lc.Retention)
	} else {
		fmt.Printf("  Retention:  binary default (--retention to change)\n")
	}
	fmt.Printf("  URL:        http://%s:%d/\n", lc.Listen, lc.HostPort)
	if len(lc.ExtraArgs) > 0 {
		fmt.Printf("  Extra args: %s\n", strings.Join(lc.ExtraArgs, " "))
	}
	fmt.Println()
	if lc.Listen == "0.0.0.0" {
		fmt.Println("  NOTE: listening on every interface. VictoriaLogs has NO authentication —")
		fmt.Println("        its HTTP API ingests and queries arbitrary logs, so this endpoint is")
		fmt.Println("        guarded only by your firewall. Use --listen 127.0.0.1 for loopback-only.")
	}
}

// runAddonRemoveVictoriaLogs removes a VictoriaLogs container. The data dir is
// kept unless --clean-data, so remove+reinstall revives stored logs. VictoriaLogs
// has no rendered config file (unlike Prometheus), so there is nothing extra to
// clean up.
func runAddonRemoveVictoriaLogs(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vl"
	}
	cleanData, _ := cmd.Flags().GetBool("clean-data")

	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s -- run \"pg config init\" first", path)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if cfg.Addons.VictoriaLogs == nil {
		return fmt.Errorf("no VictoriaLogs add-ons configured")
	}
	lc, ok := cfg.Addons.VictoriaLogs[name]
	if !ok {
		return fmt.Errorf("VictoriaLogs %q not found", name)
	}

	vl, err := podman.NewVictoriaLogsManager(cfg)
	if err != nil {
		return fmt.Errorf("VictoriaLogs manager: %w", err)
	}

	fmt.Printf("-> Removing VictoriaLogs %q...\n", name)
	if err := vl.Remove(&lc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.VictoriaLogs, name)
	if len(cfg.Addons.VictoriaLogs) == 0 {
		cfg.Addons.VictoriaLogs = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ VictoriaLogs %q removed\n", name)
	if !cleanData {
		fmt.Printf("  (data dir kept — reinstall the same name revives stored logs; --clean-data to delete it)\n")
	}
	return nil
}

func runAddonStartVictoriaLogs(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vl"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.VictoriaLogs == nil {
		return fmt.Errorf("no VictoriaLogs add-ons configured (run 'pg addon install victorialogs')")
	}
	lc, ok := cfg.Addons.VictoriaLogs[name]
	if !ok {
		return fmt.Errorf("VictoriaLogs %q not found (run 'pg addon install victorialogs --name %s')", name, name)
	}
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}
	vl, err := podman.NewVictoriaLogsManager(cfg)
	if err != nil {
		return fmt.Errorf("VictoriaLogs manager: %w", err)
	}
	fmt.Printf("-> Starting VictoriaLogs %q...\n", name)
	return vl.StartContainer(&lc)
}

func runAddonStopVictoriaLogs(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vl"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	lc, ok := cfg.Addons.VictoriaLogs[name]
	if !ok {
		return fmt.Errorf("VictoriaLogs %q not found", name)
	}
	vl, err := podman.NewVictoriaLogsManager(cfg)
	if err != nil {
		return fmt.Errorf("VictoriaLogs manager: %w", err)
	}
	running, _ := vl.ContainerRunning(lc.ContainerName)
	if !running {
		fmt.Printf("VictoriaLogs %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping VictoriaLogs %q...\n", name)
	if _, err := vl.Stop(lc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ VictoriaLogs %q stopped\n", name)
	return nil
}
