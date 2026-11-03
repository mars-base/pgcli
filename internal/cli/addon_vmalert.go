package cli

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
	"github.com/mars-base/pgcli/internal/vmalert"
)

var addonVmalertCmd = &cobra.Command{
	Use:   "vmalert",
	Short: "Manage vmalert rule-evaluation addon instances",
	Long: `Manage standalone vmalert instances — the rule-evaluation engine of the
VictoriaMetrics family. One instance evaluates rules against exactly one
datasource (VictoriaLogs or a metrics backend) and forwards firing alerts to
one or more Alertmanager-compatible notifiers.

Rules and templates are host directories bind-mounted read-only into the
container. Edit the YAML/*.tpl files directly, then reload.`,
}

var addonVmalertReloadCmd = &cobra.Command{
	Use:   "reload",
	Short: "Hot-reload rules/templates (asynchronous GET /-/reload)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonVmalertReload(cmd)
	},
}

var addonVmalertStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show stored config and live rule/alert state",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonVmalertStatus(cmd)
	},
}

// runAddonInstallVmalert installs a standalone vmalert container — the rule
// evaluation engine of the VictoriaMetrics family. It is a top-level addon
// (addons.vmalert.<name>, key defaults to "vmalert") with one host HTTP port.
//
// Rules and templates are host directories bind-mounted read-only, so the
// operator edits YAML/templates directly and asks for a reload. There is no
// rendered config file: every setting is a CLI flag after the image name.
// Install recreates the container when the effective config changed (or with
// --force); an identical reinstall is a no-op.
//
// Rules are loaded with the glob /etc/vmalert/rules/*.yaml and templates with
// /etc/vmalert/templates/*.tpl. Only those extensions match — a *.yml file is
// silently ignored.
//
// vmalert has NO authentication and NO TLS: the API and /-/reload are open to
// anyone who can reach the port. Default --listen is 0.0.0.0; use
// --listen 127.0.0.1 or a firewall on an untrusted network.
func runAddonInstallVmalert(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vmalert"
	}
	if cmd.Flags().Changed("data-dir") {
		return fmt.Errorf("--data-dir is not supported by vmalert: it keeps no local state; rules and templates are host directories (--rules-dir/--templates-dir)")
	}
	if cmd.Flags().Changed("conf-file") {
		return fmt.Errorf("--conf-file is not supported by vmalert: it has no config file; rules live in --rules-dir")
	}
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

	if cfg.Addons.Vmalert == nil {
		cfg.Addons.Vmalert = make(map[string]config.VmalertConfig)
	}
	existing, ok := cfg.Addons.Vmalert[name]
	if !ok {
		existing = config.VmalertConfig{
			ContainerName: "pgcli-vmalert" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	previous := existing

	vc := applyVmalertInstallFlags(existing, cmd)
	cfg.Addons.Vmalert[name] = vc

	cfg.ApplyDefaults()
	vc = cfg.Addons.Vmalert[name]

	if err := config.ValidateVmalertConfig(name, vc); err != nil {
		return err
	}

	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	vm, err := podman.NewVmalertManager(cfg)
	if err != nil {
		return fmt.Errorf("vmalert manager: %w", err)
	}

	running, err := vm.ContainerRunning(vc.ContainerName)
	if err != nil {
		return err
	}
	if running && !force && reflect.DeepEqual(previous, vc) {
		fmt.Println("  [OK] vmalert running, config unchanged")
		cfg.Addons.Vmalert[name] = vc
		if err := cfg.Save(path); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		printVmalertSummary(&vc, vm, name, false, false)
		return nil
	}

	fmt.Printf("-> Preparing vmalert image %s...\n", vc.ImageTag)
	if err := vm.EnsureImage(vc.ImageTag); err != nil {
		return err
	}

	fmt.Println("-> Starting vmalert container...")
	if err := vm.EnsureContainer(&vc); err != nil {
		return err
	}

	cfg.Addons.Vmalert[name] = vc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	printVmalertSummary(&vc, vm, name, true, force)
	return nil
}

// applyVmalertInstallFlags merges only the flags the operator actually passed
// into vc. Omitted flags keep their stored (or defaulted) values, so a
// reinstall without a value never silently resets the instance. Repeatable
// flags replace the stored list wholesale.
func applyVmalertInstallFlags(vc config.VmalertConfig, cmd *cobra.Command) config.VmalertConfig {
	if cmd.Flags().Changed("image") {
		if image, _ := cmd.Flags().GetString("image"); image != "" {
			vc.ImageTag = image
		}
	}
	if cmd.Flags().Changed("port") {
		vc.HostPort, _ = cmd.Flags().GetInt("port")
	}
	if cmd.Flags().Changed("listen") {
		vc.Listen, _ = cmd.Flags().GetString("listen")
	}
	if cmd.Flags().Changed("datasource-url") {
		vc.DatasourceURL, _ = cmd.Flags().GetString("datasource-url")
	}
	if cmd.Flags().Changed("notifier-url") {
		vc.NotifierURLs, _ = cmd.Flags().GetStringArray("notifier-url")
	}
	if cmd.Flags().Changed("rules-dir") {
		vc.RulesDir, _ = cmd.Flags().GetString("rules-dir")
	}
	if cmd.Flags().Changed("templates-dir") {
		vc.TemplatesDir, _ = cmd.Flags().GetString("templates-dir")
	}
	if cmd.Flags().Changed("remote-write-url") {
		vc.RemoteWriteURLs, _ = cmd.Flags().GetStringArray("remote-write-url")
	}
	if cmd.Flags().Changed("remote-read-url") {
		vc.RemoteReadURLs, _ = cmd.Flags().GetStringArray("remote-read-url")
	}
	if cmd.Flags().Changed("external-label") {
		vc.ExternalLabels, _ = cmd.Flags().GetStringArray("external-label")
	}
	if cmd.Flags().Changed("external-url") {
		vc.ExternalURL, _ = cmd.Flags().GetString("external-url")
	}
	if cmd.Flags().Changed("extra-arg") {
		vc.ExtraArgs, _ = cmd.Flags().GetStringArray("extra-arg")
	}
	return vc
}

func printVmalertSummary(vc *config.VmalertConfig, vm *podman.VmalertManager, name string, installed, force bool) {
	fmt.Println()
	switch {
	case installed && force:
		fmt.Printf("✓ vmalert %q recreated (--force)\n", name)
	case installed:
		fmt.Printf("✓ vmalert installed: %q\n", name)
	default:
		fmt.Printf("✓ vmalert %q (unchanged)\n", name)
	}
	fmt.Printf("  Container:     %s\n", vc.ContainerName)
	fmt.Printf("  Image:         %s\n", vc.ImageTag)
	fmt.Printf("  URL:           %s/\n", vm.Endpoint(vc))
	fmt.Printf("  Datasource:    %s\n", vc.DatasourceURL)
	for _, u := range vc.NotifierURLs {
		fmt.Printf("  Notifier:      %s\n", u)
	}
	fmt.Printf("  Rules dir:     %s (mounted read-only at /etc/vmalert/rules)\n", vm.RulesDir(vc))
	fmt.Printf("  Templates dir: %s (mounted read-only at /etc/vmalert/templates)\n", vm.TemplatesDir(vc))
	for _, u := range vc.RemoteWriteURLs {
		fmt.Printf("  Remote write:  %s\n", u)
	}
	for _, u := range vc.RemoteReadURLs {
		fmt.Printf("  Remote read:   %s\n", u)
	}
	if vc.ExternalURL != "" {
		fmt.Printf("  External URL:  %s\n", vc.ExternalURL)
	}
	for _, l := range vc.ExternalLabels {
		fmt.Printf("  External label: %s\n", l)
	}
	if len(vc.ExtraArgs) > 0 {
		fmt.Printf("  Extra args:    %s\n", strings.Join(vc.ExtraArgs, " "))
	}
	fmt.Println()
	fmt.Println("  Rules are loaded from /etc/vmalert/rules/*.yaml ONLY (*.yml is ignored).")
	fmt.Println("  Edit them on the host, then run: pg addon vmalert reload --name " + name)
	fmt.Println("  Reload is asynchronous: a 2xx response only means it was accepted; check")
	fmt.Println("  'pg addon vmalert status' or 'pg logs addon vmalert' for parse errors.")
	if vc.Listen == "0.0.0.0" {
		fmt.Println()
		fmt.Println("  NOTE: listening on every interface. vmalert has NO authentication and NO TLS —")
		fmt.Println("        anyone who reaches this port can read alerts and trigger reloads.")
		fmt.Println("        Use --listen 127.0.0.1, or restrict at the firewall.")
	}
}

func resolveVmalertConfig(cmd *cobra.Command) (*config.Config, config.VmalertConfig, error) {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vmalert"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return nil, config.VmalertConfig{}, err
	}
	if cfg.Addons.Vmalert == nil {
		return nil, config.VmalertConfig{}, fmt.Errorf("no vmalert add-ons configured (run 'pg addon install vmalert')")
	}
	vc, ok := cfg.Addons.Vmalert[name]
	if !ok {
		return nil, config.VmalertConfig{}, fmt.Errorf("vmalert %q not found (run 'pg addon install vmalert --name %s')", name, name)
	}
	return cfg, vc, nil
}

// runAddonVmalertReload asks the running instance to re-read rules/templates.
func runAddonVmalertReload(cmd *cobra.Command) error {
	cfg, vc, err := resolveVmalertConfig(cmd)
	if err != nil {
		return err
	}
	vm, err := podman.NewVmalertManager(cfg)
	if err != nil {
		return fmt.Errorf("vmalert manager: %w", err)
	}
	return vm.Reload(&vc)
}

// runAddonVmalertStatus prints the stored config plus the live rule state
// reported by /api/v1/rules.
func runAddonVmalertStatus(cmd *cobra.Command) error {
	cfg, vc, err := resolveVmalertConfig(cmd)
	if err != nil {
		return err
	}
	vm, err := podman.NewVmalertManager(cfg)
	if err != nil {
		return fmt.Errorf("vmalert manager: %w", err)
	}
	running, err := vm.ContainerRunning(vc.ContainerName)
	if err != nil {
		return err
	}
	status := "stopped"
	if running {
		status = "running"
	}

	name := vc.Name
	fmt.Printf("vmalert %q\n", name)
	fmt.Printf("  Status:        %s\n", status)
	fmt.Printf("  URL:           %s/\n", vm.Endpoint(&vc))
	fmt.Printf("  Datasource:    %s\n", vc.DatasourceURL)
	for _, u := range vc.NotifierURLs {
		fmt.Printf("  Notifier:      %s\n", u)
	}
	fmt.Printf("  Rules dir:     %s\n", vm.RulesDir(&vc))
	fmt.Printf("  Templates dir: %s\n", vm.TemplatesDir(&vc))
	fmt.Printf("  Image:         %s\n", vc.ImageTag)
	fmt.Printf("  Container:     %s\n", vc.ContainerName)

	if !running {
		return nil
	}
	summary, err := vmalert.NewClient(vm.Endpoint(&vc)).RulesSummary(context.Background())
	if err != nil {
		return fmt.Errorf("querying vmalert rules: %w", err)
	}
	fmt.Printf("  Groups:        %d\n", summary.Groups)
	fmt.Printf("  Rules:         %d (firing %d, pending %d)\n", summary.Rules, summary.Firing, summary.Pending)
	for _, g := range summary.GroupList {
		fmt.Printf("    - %s (%d rule(s)) %s\n", g.Name, g.Rules, g.File)
	}
	return nil
}

// runAddonRemoveVmalert removes the container. Rules and templates are kept by
// default; --clean-data deletes only pgcli-managed directories, never an
// explicit --rules-dir/--templates-dir.
func runAddonRemoveVmalert(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vmalert"
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
	if cfg.Addons.Vmalert == nil {
		return fmt.Errorf("no vmalert add-ons configured")
	}
	vc, ok := cfg.Addons.Vmalert[name]
	if !ok {
		return fmt.Errorf("vmalert %q not found", name)
	}

	vm, err := podman.NewVmalertManager(cfg)
	if err != nil {
		return fmt.Errorf("vmalert manager: %w", err)
	}

	fmt.Printf("-> Removing vmalert %q...\n", name)
	if err := vm.Remove(&vc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.Vmalert, name)
	if len(cfg.Addons.Vmalert) == 0 {
		cfg.Addons.Vmalert = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ vmalert %q removed\n", name)
	if !cleanData {
		fmt.Println("  (rules and templates kept — --clean-data deletes only pgcli-managed dirs)")
	}
	return nil
}

func runAddonStartVmalert(cmd *cobra.Command) error {
	cfg, vc, err := resolveVmalertConfig(cmd)
	if err != nil {
		return err
	}
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}
	vm, err := podman.NewVmalertManager(cfg)
	if err != nil {
		return fmt.Errorf("vmalert manager: %w", err)
	}
	fmt.Printf("-> Starting vmalert %q...\n", vc.Name)
	return vm.StartContainer(&vc)
}

func runAddonStopVmalert(cmd *cobra.Command) error {
	cfg, vc, err := resolveVmalertConfig(cmd)
	if err != nil {
		return err
	}
	vm, err := podman.NewVmalertManager(cfg)
	if err != nil {
		return fmt.Errorf("vmalert manager: %w", err)
	}
	running, _ := vm.ContainerRunning(vc.ContainerName)
	if !running {
		fmt.Printf("vmalert %q is not running\n", vc.Name)
		return nil
	}
	fmt.Printf("-> Stopping vmalert %q...\n", vc.Name)
	if _, err := vm.Stop(vc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ vmalert %q stopped\n", vc.Name)
	return nil
}
