package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

// runAddonInstallVictoriaMetrics installs a standalone VictoriaMetrics container —
// the fast, Prometheus-compatible time-series database — from the official
// docker.io/victoriametrics/victoria-metrics image. It is a top-level addon
// (addons.victoriametrics.<name>, key defaults to "vm") on one host port with a
// persistent data dir.
//
// Unlike Prometheus it needs NO rendered config file: everything is passed as
// CLI flags after the image name. Install always recreates the container
// (--force semantics) since VictoriaMetrics does not support hot config reload
// (SIGHUP). The data dir outlives the container — remove+reinstall revives
// stored time series.
//
// There is NO login/password: VictoriaMetrics ships no authentication by default.
// Its HTTP API can ingest and query arbitrary metrics via PromQL/MetricsQL, so a
// remote-exposed instance is guarded only by the network boundary (firewall /
// --listen 127.0.0.1), same as Prometheus.
func runAddonInstallVictoriaMetrics(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vm"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	port, _ := cmd.Flags().GetInt("port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	retentionMonths, _ := cmd.Flags().GetString("retention-months")
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

	if cfg.Addons.VictoriaMetrics == nil {
		cfg.Addons.VictoriaMetrics = make(map[string]config.VictoriaMetricsConfig)
	}
	existing, ok := cfg.Addons.VictoriaMetrics[name]
	if !ok {
		existing = config.VictoriaMetricsConfig{
			ContainerName: "pgcli-victoriametrics" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if dataDir != "" {
		existing.DataDir = dataDir
	}
	if cmd.Flags().Changed("port") {
		existing.HostPort = port
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if retentionMonths != "" {
		existing.Retention = retentionMonths
	}
	if cmd.Flags().Changed("extra-arg") {
		existing.ExtraArgs = extraArgs
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	cfg.Addons.VictoriaMetrics[name] = existing

	cfg.ApplyDefaults()
	vc := cfg.Addons.VictoriaMetrics[name]

	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	vm, err := podman.NewVictoriaMetricsManager(cfg)
	if err != nil {
		return fmt.Errorf("VictoriaMetrics manager: %w", err)
	}

	// Skip recreate when already running and --force is not set, unless the
	// config actually changed. VictoriaMetrics has no SIGHUP reload, so changes
	// require recreation — but an idempotent reinstall with identical config is
	// a no-op (just like Prometheus's "config unchanged" path).
	running, err := vm.ContainerRunning(vc.ContainerName)
	if err != nil {
		return err
	}
	if running && !force {
		// Config is already stored; a plain reinstall with the same flags is a
		// no-op. If the user wants to recreate, pass --force.
		fmt.Println("  [OK] VictoriaMetrics running, config unchanged")
		cfg.Addons.VictoriaMetrics[name] = vc
		if err := cfg.Save(path); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		printVictoriaMetricsSummary(&vc, vm, name, false, false)
		return nil
	}

	fmt.Printf("-> Preparing VictoriaMetrics image %s...\n", vc.ImageTag)
	if err := vm.EnsureImage(vc.ImageTag); err != nil {
		return err
	}

	fmt.Println("-> Starting VictoriaMetrics container...")
	if err := vm.EnsureContainer(&vc); err != nil {
		return err
	}

	cfg.Addons.VictoriaMetrics[name] = vc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	printVictoriaMetricsSummary(&vc, vm, name, true, force)
	return nil
}

func printVictoriaMetricsSummary(vc *config.VictoriaMetricsConfig, vm *podman.VictoriaMetricsManager, name string, installed, force bool) {
	fmt.Println()
	switch {
	case installed && force:
		fmt.Printf("✓ VictoriaMetrics %q recreated (--force)\n", name)
	case installed:
		fmt.Printf("✓ VictoriaMetrics installed: %q\n", name)
	default:
		fmt.Printf("✓ VictoriaMetrics %q (unchanged)\n", name)
	}
	fmt.Printf("  Container:  %s\n", vc.ContainerName)
	fmt.Printf("  Image:      %s\n", vc.ImageTag)
	fmt.Printf("  Data:       %s\n", vm.DataDir(vc))
	fmt.Printf("  Retention:  %s month(s)\n", vc.Retention)
	fmt.Printf("  URL:        http://%s:%d/\n", vc.Listen, vc.HostPort)
	if len(vc.ExtraArgs) > 0 {
		fmt.Printf("  Extra args: %s\n", strings.Join(vc.ExtraArgs, " "))
	}
	fmt.Println()
	if vc.Listen == "0.0.0.0" {
		fmt.Println("  NOTE: listening on every interface. VictoriaMetrics has NO authentication —")
		fmt.Println("        its HTTP API runs arbitrary MetricsQL, so this endpoint is guarded only")
		fmt.Println("        by your firewall. Use --listen 127.0.0.1 for loopback-only.")
	}
}

// resolveVictoriaMetricsConfig loads the config and returns the VictoriaMetricsConfig
// for the given --name (default "vm"), or an error if not found. Shared by
// the start/stop subcommands.
func resolveVictoriaMetricsConfig(cmd *cobra.Command) (*config.VictoriaMetricsConfig, error) {
	if err := loadConfigForDSN(); err != nil {
		return nil, err
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vm"
	}
	if cfg.Addons.VictoriaMetrics == nil {
		return nil, fmt.Errorf("no victoriametrics addons configured")
	}
	vc, ok := cfg.Addons.VictoriaMetrics[name]
	if !ok {
		return nil, fmt.Errorf("victoriametrics instance %q not found (use 'pg addon list' to see available)", name)
	}
	return &vc, nil
}

// runAddonRemoveVictoriaMetrics removes a VictoriaMetrics container. The data dir
// is kept unless --clean-data, so remove+reinstall revives stored time series.
// VictoriaMetrics has no rendered config file (unlike Prometheus), so there is
// nothing extra to clean up.
func runAddonRemoveVictoriaMetrics(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vm"
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

	if cfg.Addons.VictoriaMetrics == nil {
		return fmt.Errorf("no VictoriaMetrics add-ons configured")
	}
	vc, ok := cfg.Addons.VictoriaMetrics[name]
	if !ok {
		return fmt.Errorf("VictoriaMetrics %q not found", name)
	}

	vm, err := podman.NewVictoriaMetricsManager(cfg)
	if err != nil {
		return fmt.Errorf("VictoriaMetrics manager: %w", err)
	}

	fmt.Printf("-> Removing VictoriaMetrics %q...\n", name)
	if err := vm.Remove(&vc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.VictoriaMetrics, name)
	if len(cfg.Addons.VictoriaMetrics) == 0 {
		cfg.Addons.VictoriaMetrics = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ VictoriaMetrics %q removed\n", name)
	if !cleanData {
		fmt.Printf("  (data dir kept — reinstall the same name revives stored time series; --clean-data to delete it)\n")
	}
	return nil
}

func runAddonStartVictoriaMetrics(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vm"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.VictoriaMetrics == nil {
		return fmt.Errorf("no VictoriaMetrics add-ons configured (run 'pg addon install victoriametrics')")
	}
	vc, ok := cfg.Addons.VictoriaMetrics[name]
	if !ok {
		return fmt.Errorf("VictoriaMetrics %q not found (run 'pg addon install victoriametrics --name %s')", name, name)
	}
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}
	vm, err := podman.NewVictoriaMetricsManager(cfg)
	if err != nil {
		return fmt.Errorf("VictoriaMetrics manager: %w", err)
	}
	fmt.Printf("-> Starting VictoriaMetrics %q...\n", name)
	return vm.StartContainer(&vc)
}

func runAddonStopVictoriaMetrics(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vm"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	vc, ok := cfg.Addons.VictoriaMetrics[name]
	if !ok {
		return fmt.Errorf("VictoriaMetrics %q not found", name)
	}
	vm, err := podman.NewVictoriaMetricsManager(cfg)
	if err != nil {
		return fmt.Errorf("VictoriaMetrics manager: %w", err)
	}
	running, _ := vm.ContainerRunning(vc.ContainerName)
	if !running {
		fmt.Printf("VictoriaMetrics %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping VictoriaMetrics %q...\n", name)
	if _, err := vm.Stop(vc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ VictoriaMetrics %q stopped\n", name)
	return nil
}
