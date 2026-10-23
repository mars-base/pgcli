package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

// runAddonInstallVlagent installs a standalone vlagent container — the official
// VictoriaLogs log agent — from the official docker.io/victoriametrics/vlagent
// image. It is a top-level addon (addons.vlagent.<name>, key defaults to "va")
// on one host port with a persistent data dir.
//
// vlagent needs NO rendered config file: everything is passed as CLI flags
// after the image name. Install always recreates the container (--force
// semantics) since vlagent does not support hot config reload. The data dir
// holds the remote-write buffer and file-collector checkpoints and outlives
// the container.
//
// File collection: each --log-path is a host file or directory bind-mounted
// read-only at the SAME absolute path inside the container; --include /
// --exclude are vlagent-native globs matched against those container paths
// (`*`, `/**/`, `?`, `[a-z]`, `{a,b}` — not regular expressions). Without
// --include, each directory gets "<dir>/**/*.log" and each file gets itself.
//
// At least one --remote-write-url is required and must end with
// /insert/native. Repeating it replicates every log to all targets.
//
// There is NO login/password on either side: vlagent's HTTP listener accepts
// arbitrary logs without authentication, so a remote-exposed instance is
// guarded only by the network boundary (firewall / --listen 127.0.0.1).
func runAddonInstallVlagent(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "va"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	port, _ := cmd.Flags().GetInt("port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	rwURLs, _ := cmd.Flags().GetStringArray("remote-write-url")
	logPaths, _ := cmd.Flags().GetStringArray("log-path")
	includes, _ := cmd.Flags().GetStringArray("include")
	excludes, _ := cmd.Flags().GetStringArray("exclude")
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

	if cfg.Addons.Vlagent == nil {
		cfg.Addons.Vlagent = make(map[string]config.VlagentConfig)
	}
	existing, ok := cfg.Addons.Vlagent[name]
	if !ok {
		existing = config.VlagentConfig{
			ContainerName: "pgcli-vlagent" + nsSuffixCLI(cfg.Namespace) + "-" + name,
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
	if cmd.Flags().Changed("remote-write-url") {
		existing.RemoteWriteURLs = rwURLs
	}
	if cmd.Flags().Changed("log-path") {
		existing.LogPaths = logPaths
		if !cmd.Flags().Changed("include") {
			// The default includes are derived from the log paths; a changed
			// path list invalidates them.
			existing.Includes = nil
		}
	}
	if cmd.Flags().Changed("include") {
		existing.Includes = includes
	}
	if cmd.Flags().Changed("exclude") {
		existing.Excludes = excludes
	}
	if cmd.Flags().Changed("extra-arg") {
		existing.ExtraArgs = extraArgs
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	cfg.Addons.Vlagent[name] = existing

	cfg.ApplyDefaults()
	vc := cfg.Addons.Vlagent[name]

	if err := validateVlagentConfig(&vc, cmd.Flags().Changed("exclude")); err != nil {
		return err
	}
	cfg.Addons.Vlagent[name] = vc

	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	va, err := podman.NewVlagentManager(cfg)
	if err != nil {
		return fmt.Errorf("vlagent manager: %w", err)
	}

	// Recreate when the config actually changed (vlagent has no reload); an
	// idempotent reinstall with identical config is a no-op. --force always
	// recreates.
	running, err := va.ContainerRunning(vc.ContainerName)
	if err != nil {
		return err
	}
	if running && !force && reflect.DeepEqual(previous, vc) {
		fmt.Println("  [OK] vlagent running, config unchanged")
		cfg.Addons.Vlagent[name] = vc
		if err := cfg.Save(path); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		printVlagentSummary(&vc, va, name, false, false)
		return nil
	}

	fmt.Printf("-> Preparing vlagent image %s...\n", vc.ImageTag)
	if err := va.EnsureImage(vc.ImageTag); err != nil {
		return err
	}

	fmt.Println("-> Starting vlagent container...")
	if err := va.EnsureContainer(&vc); err != nil {
		return err
	}

	cfg.Addons.Vlagent[name] = vc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	printVlagentSummary(&vc, va, name, true, force)
	return nil
}

// validateVlagentConfig checks a merged vlagent config and fills in the default
// include globs when none were given. explicitExclude reports whether the
// current run passed --exclude; it decides whether an over-long exclude list is
// an error (fresh user input) or stale stored entries to trim (e.g. after
// --log-path changed).
func validateVlagentConfig(vc *config.VlagentConfig, explicitExclude bool) error {
	if err := validateVlagentRemoteWriteURLs(vc.RemoteWriteURLs); err != nil {
		return err
	}
	if err := validateVlagentLogPaths(vc.LogPaths); err != nil {
		return err
	}
	if len(vc.Includes) > 0 && len(vc.LogPaths) == 0 {
		return fmt.Errorf("--include patterns are matched inside the container, so they need at least one --log-path to mount the host files first")
	}
	if err := validateVlagentPatterns("--include", vc.Includes); err != nil {
		return err
	}
	if err := validateVlagentPatterns("--exclude", vc.Excludes); err != nil {
		return err
	}

	// Default includes: a directory gets every *.log below it (/**/ also
	// matches files directly inside it), a single file gets itself.
	if len(vc.Includes) == 0 && len(vc.LogPaths) > 0 {
		for _, p := range vc.LogPaths {
			fi, err := os.Stat(p)
			if err != nil {
				return fmt.Errorf("--log-path %q: %w", p, err)
			}
			p = filepath.Clean(p)
			if fi.IsDir() {
				vc.Includes = append(vc.Includes, filepath.Join(p, "**", "*.log"))
			} else {
				vc.Includes = append(vc.Includes, p)
			}
		}
	}

	// Excludes pair positionally with includes. Extra stored entries are
	// trimmed; extra entries on the command line are a user error.
	if len(vc.Excludes) > len(vc.Includes) {
		if explicitExclude {
			return fmt.Errorf("got %d --exclude values but only %d --include patterns: vlagent pairs the Nth exclude with the Nth include, so there cannot be more excludes", len(vc.Excludes), len(vc.Includes))
		}
		vc.Excludes = vc.Excludes[:len(vc.Includes)]
	}
	return nil
}

func validateVlagentRemoteWriteURLs(urls []string) error {
	if len(urls) == 0 {
		return fmt.Errorf("at least one --remote-write-url is required: the VictoriaLogs endpoint vlagent forwards to, e.g. http://127.0.0.1:9428/insert/native")
	}
	for _, u := range urls {
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("--remote-write-url %q must be an http:// or https:// URL", u)
		}
		if !strings.HasSuffix(u, "/insert/native") {
			return fmt.Errorf("--remote-write-url %q must end with /insert/native — that is VictoriaLogs's native ingest endpoint (e.g. http://127.0.0.1:9428/insert/native)", u)
		}
	}
	return nil
}

func validateVlagentLogPaths(paths []string) error {
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("--log-path %q must be an absolute path (the host path is bind-mounted at the same path inside the container, where the globs are matched)", p)
		}
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("--log-path %q: %w", p, err)
		}
	}
	return nil
}

func validateVlagentPatterns(flagName string, pats []string) error {
	for _, p := range pats {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("%s %q must be an absolute container path", flagName, p)
		}
	}
	return nil
}

func printVlagentSummary(vc *config.VlagentConfig, va *podman.VlagentManager, name string, installed, force bool) {
	fmt.Println()
	switch {
	case installed && force:
		fmt.Printf("✓ vlagent %q recreated (--force)\n", name)
	case installed:
		fmt.Printf("✓ vlagent installed: %q\n", name)
	default:
		fmt.Printf("✓ vlagent %q (unchanged)\n", name)
	}
	fmt.Printf("  Container:  %s\n", vc.ContainerName)
	fmt.Printf("  Image:      %s\n", vc.ImageTag)
	fmt.Printf("  Data:       %s\n", va.DataDir(vc))
	fmt.Printf("  Listen:     http://%s:%d/ (HTTP ingest: jsonline, ES bulk, Loki, OTLP)\n", vc.Listen, vc.HostPort)
	for _, u := range vc.RemoteWriteURLs {
		fmt.Printf("  Remote:     %s\n", u)
	}
	if len(vc.LogPaths) == 0 {
		fmt.Printf("  Collect:    none (relay-only: only HTTP pushes on the listen port are forwarded)\n")
	} else {
		for _, p := range vc.LogPaths {
			fi, err := os.Stat(p)
			kind := "dir"
			if err == nil && !fi.IsDir() {
				kind = "file"
			}
			fmt.Printf("  Mount:      %s (%s, read-only, same path in the container)\n", p, kind)
		}
		for _, g := range vc.Includes {
			fmt.Printf("  Include:    %s\n", g)
		}
		for i, e := range vc.Excludes {
			if e != "" && i < len(vc.Includes) {
				fmt.Printf("  Exclude:    %s (from %s)\n", e, vc.Includes[i])
			}
		}
		fmt.Printf("  Checkpoint: %s under %s\n", "file-checkpoints.json", "/var/lib/vlagent")
	}
	if len(vc.ExtraArgs) > 0 {
		fmt.Printf("  Extra args: %s\n", strings.Join(vc.ExtraArgs, " "))
	}
	fmt.Println()
	if vc.Listen == "0.0.0.0" {
		fmt.Println("  NOTE: listening on every interface. vlagent has NO authentication —")
		fmt.Println("        anyone who can reach the port can push logs through it. Use")
		fmt.Println("        --listen 127.0.0.1 for loopback-only, or restrict at the firewall.")
	}
}

// runAddonRemoveVlagent removes a vlagent container. The data dir (buffers +
// file-collector checkpoints) is kept unless --clean-data, so remove+reinstall
// resumes where collection stopped.
func runAddonRemoveVlagent(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "va"
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

	if cfg.Addons.Vlagent == nil {
		return fmt.Errorf("no vlagent add-ons configured")
	}
	vc, ok := cfg.Addons.Vlagent[name]
	if !ok {
		return fmt.Errorf("vlagent %q not found", name)
	}

	va, err := podman.NewVlagentManager(cfg)
	if err != nil {
		return fmt.Errorf("vlagent manager: %w", err)
	}

	fmt.Printf("-> Removing vlagent %q...\n", name)
	if err := va.Remove(&vc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.Vlagent, name)
	if len(cfg.Addons.Vlagent) == 0 {
		cfg.Addons.Vlagent = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ vlagent %q removed\n", name)
	if !cleanData {
		fmt.Printf("  (data dir kept — reinstalling keeps buffered logs and file checkpoints; --clean-data to delete it)\n")
	}
	return nil
}

func runAddonStartVlagent(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "va"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.Vlagent == nil {
		return fmt.Errorf("no vlagent add-ons configured (run 'pg addon install vlagent')")
	}
	vc, ok := cfg.Addons.Vlagent[name]
	if !ok {
		return fmt.Errorf("vlagent %q not found (run 'pg addon install vlagent --name %s')", name, name)
	}
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}
	va, err := podman.NewVlagentManager(cfg)
	if err != nil {
		return fmt.Errorf("vlagent manager: %w", err)
	}
	fmt.Printf("-> Starting vlagent %q...\n", name)
	return va.StartContainer(&vc)
}

func runAddonStopVlagent(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "va"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	vc, ok := cfg.Addons.Vlagent[name]
	if !ok {
		return fmt.Errorf("vlagent %q not found", name)
	}
	va, err := podman.NewVlagentManager(cfg)
	if err != nil {
		return fmt.Errorf("vlagent manager: %w", err)
	}
	running, _ := va.ContainerRunning(vc.ContainerName)
	if !running {
		fmt.Printf("vlagent %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping vlagent %q...\n", name)
	if _, err := va.Stop(vc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ vlagent %q stopped\n", name)
	return nil
}
