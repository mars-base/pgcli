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

// runAddonInstallNodeExporter installs a node_exporter container — the official
// Prometheus host-metrics exporter — from the docker.io/prom/node-exporter
// image. It is a top-level addon (addons.nodeexporter.<name>, key defaults to
// "nodeexporter") on one host port.
//
// Like VictoriaMetrics it needs NO rendered config file: everything is a CLI
// flag after the image name. Install always recreates the container when
// --force is set (node_exporter has no hot reload). It is stateless — there is
// no data dir, so remove never deletes anything and reinstall always starts
// fresh.
//
// The one non-negotiable is HOW it is run: pgcli hard-wires read-only mounts of
// the host /proc, /sys and / and points the binary at them, so the exporter
// reports the HOST's metrics rather than the container's (see
// podman.nodeExporterArgs). Without those mounts the addon would silently be
// wrong, which is the classic container-deployment mistake.
//
// There is NO login/password and no TLS: node_exporter just serves /metrics. It
// defaults to 0.0.0.0 (a Prometheus scraping it is usually on another host), so
// a remote-exposed instance is guarded only by the network boundary.
func runAddonInstallNodeExporter(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "nodeexporter"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	port, _ := cmd.Flags().GetInt("port")
	listenAddr, _ := cmd.Flags().GetString("listen")
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

	if cfg.Addons.NodeExporter == nil {
		cfg.Addons.NodeExporter = make(map[string]config.NodeExporterConfig)
	}
	existing, ok := cfg.Addons.NodeExporter[name]
	if !ok {
		existing = config.NodeExporterConfig{
			ContainerName: "pgcli-nodeexporter" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if cmd.Flags().Changed("port") {
		existing.HostPort = port
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if cmd.Flags().Changed("extra-arg") {
		existing.ExtraArgs = extraArgs
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	cfg.Addons.NodeExporter[name] = existing

	cfg.ApplyDefaults()
	nc := cfg.Addons.NodeExporter[name]

	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	ne, err := podman.NewNodeExporterManager(cfg)
	if err != nil {
		return fmt.Errorf("node_exporter manager: %w", err)
	}

	// Skip recreate when already running and --force is not set. node_exporter
	// has no SIGHUP reload, so changes require recreation — but an idempotent
	// reinstall with identical config is a no-op.
	running, err := ne.ContainerRunning(nc.ContainerName)
	if err != nil {
		return err
	}
	if running && !force {
		fmt.Println("  [OK] node_exporter running, config unchanged")
		cfg.Addons.NodeExporter[name] = nc
		if err := cfg.Save(path); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		printNodeExporterSummary(&nc, name, false, false)
		return nil
	}

	fmt.Printf("-> Preparing node_exporter image %s...\n", nc.ImageTag)
	if err := ne.EnsureImage(nc.ImageTag); err != nil {
		return err
	}

	fmt.Println("-> Starting node_exporter container...")
	if err := ne.EnsureContainer(&nc); err != nil {
		return err
	}

	cfg.Addons.NodeExporter[name] = nc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	printNodeExporterSummary(&nc, name, true, force)
	return nil
}

func printNodeExporterSummary(nc *config.NodeExporterConfig, name string, installed, force bool) {
	fmt.Println()
	switch {
	case installed && force:
		fmt.Printf("✓ node_exporter %q recreated (--force)\n", name)
	case installed:
		fmt.Printf("✓ node_exporter installed: %q\n", name)
	default:
		fmt.Printf("✓ node_exporter %q (unchanged)\n", name)
	}
	fmt.Printf("  Container:  %s\n", nc.ContainerName)
	fmt.Printf("  Image:      %s\n", nc.ImageTag)
	fmt.Printf("  Metrics:    http://%s:%d/metrics\n", nc.Listen, nc.HostPort)
	fmt.Printf("  Host mounts: /proc, /sys, / (read-only) → /host/proc, /host/sys, /rootfs\n")
	if len(nc.ExtraArgs) > 0 {
		fmt.Printf("  Extra args: %s\n", strings.Join(nc.ExtraArgs, " "))
	}
	fmt.Println()
	if nc.Listen == "0.0.0.0" {
		fmt.Println("  NOTE: listening on every interface. node_exporter has NO authentication and")
		fmt.Println("        exposes detailed host metrics (hardware, mounts, network). Anyone who can")
		fmt.Println("        reach the port can scrape them — restrict it to your monitoring network.")
		fmt.Println("        Point Prometheus at it with:")
		fmt.Printf("          pg addon install prometheus --scrape node-exporter=%s:%d\n", hostOrSelf(nc.Listen), nc.HostPort)
		fmt.Println("        Or add it to a running Prometheus without dropping its other jobs:")
		fmt.Printf("          pg addon install prometheus --scrape-append node-exporter=%s:%d\n", hostOrSelf(nc.Listen), nc.HostPort)
	}
}

// hostOrSelf turns the wildcard bind 0.0.0.0 into a concrete address for the
// example scrape target (127.0.0.1 for same-host Prometheus), so the printed
// hint is copy-pasteable rather than literally http://0.0.0.0:9100.
func hostOrSelf(listen string) string {
	if listen == "0.0.0.0" || listen == "" {
		return "127.0.0.1"
	}
	return listen
}

// runAddonRemoveNodeExporter removes a node_exporter container. node_exporter is
// stateless — there is no data dir to keep or delete — so --clean-data has no
// effect beyond an informational note.
func runAddonRemoveNodeExporter(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "nodeexporter"
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

	if cfg.Addons.NodeExporter == nil {
		return fmt.Errorf("no nodeexporter add-ons configured")
	}
	nc, ok := cfg.Addons.NodeExporter[name]
	if !ok {
		return fmt.Errorf("node_exporter %q not found", name)
	}

	ne, err := podman.NewNodeExporterManager(cfg)
	if err != nil {
		return fmt.Errorf("node_exporter manager: %w", err)
	}

	fmt.Printf("-> Removing node_exporter %q...\n", name)
	if err := ne.Remove(&nc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.NodeExporter, name)
	if len(cfg.Addons.NodeExporter) == 0 {
		cfg.Addons.NodeExporter = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ node_exporter %q removed\n", name)
	return nil
}

func runAddonStartNodeExporter(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "nodeexporter"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.NodeExporter == nil {
		return fmt.Errorf("no nodeexporter add-ons configured (run 'pg addon install nodeexporter')")
	}
	nc, ok := cfg.Addons.NodeExporter[name]
	if !ok {
		return fmt.Errorf("node_exporter %q not found (run 'pg addon install nodeexporter --name %s')", name, name)
	}
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}
	ne, err := podman.NewNodeExporterManager(cfg)
	if err != nil {
		return fmt.Errorf("node_exporter manager: %w", err)
	}
	fmt.Printf("-> Starting node_exporter %q...\n", name)
	return ne.StartContainer(&nc)
}

func runAddonStopNodeExporter(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "nodeexporter"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	nc, ok := cfg.Addons.NodeExporter[name]
	if !ok {
		return fmt.Errorf("node_exporter %q not found", name)
	}
	ne, err := podman.NewNodeExporterManager(cfg)
	if err != nil {
		return fmt.Errorf("node_exporter manager: %w", err)
	}
	running, _ := ne.ContainerRunning(nc.ContainerName)
	if !running {
		fmt.Printf("node_exporter %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping node_exporter %q...\n", name)
	if _, err := ne.Stop(nc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ node_exporter %q stopped\n", name)
	return nil
}
