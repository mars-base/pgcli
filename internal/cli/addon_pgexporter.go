package cli

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/influx2tsdb"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

const pgExporterDefaultName = "pgexporter"

// stripDSNCredentials returns targetDSN with its userinfo (user and password)
// removed, keeping scheme/host/port/database/query. The --admin-dsn path stores
// only this form, so an admin-privileged URL is used once for CREATE ROLE and
// never persists — later starts substitute the monitor credentials into it.
func stripDSNCredentials(targetDSN string) (string, error) {
	u, err := url.Parse(targetDSN)
	if err != nil {
		return "", fmt.Errorf("parsing admin DSN: %w", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", fmt.Errorf("admin DSN must be a postgres:// or postgresql:// URL")
	}
	u.User = nil
	return u.String(), nil
}

// pgexporterTargetDSN resolves the monitored target's address right now — the
// URL whose host/port/database the exporter dials. In local mode it is
// re-derived from pg.yaml (so a rotated password or changed port never goes
// stale); in --admin-dsn mode it is the stored CREDENTIAL-FREE address; in
// dial-as-is (--dsn) mode it is the user's DSN verbatim (credentials included —
// that mode never builds a monitor role).
func pgexporterTargetDSN(c *config.Config, pc config.PGExporterConfig) (string, error) {
	switch {
	case pc.TargetInstance != "":
		inst, ok := c.Instances[pc.TargetInstance]
		if !ok {
			return "", fmt.Errorf("target instance %q not found in config", pc.TargetInstance)
		}
		return influx2tsdb.LocalDSN(inst)
	case pc.AdminDSN != "":
		return pc.AdminDSN, nil
	case pc.DSN != "":
		return pc.DSN, nil
	}
	return "", fmt.Errorf("pgexporter %q has no target (set target_instance, dsn, or admin_dsn)", pc.Name)
}

// pgexporterMonitorDSN resolves the URL handed to the pg_exporter container:
// the target address with the monitor role's credentials substituted. In
// dial-as-is mode (no monitor password was generated) the target DSN is used
// verbatim — the user's own credentials are the exporter's.
func pgexporterMonitorDSN(c *config.Config, pc config.PGExporterConfig) (string, error) {
	target, err := pgexporterTargetDSN(c, pc)
	if err != nil {
		return "", err
	}
	if pc.MonitorPassword == "" {
		return target, nil
	}
	return podman.BuildMonitorDSN(target, pc.MonitorUser, pc.MonitorPassword)
}

// pgexporterConnectParts returns the label and value for the "who connects"
// display line. In dial-as-is (--dsn) mode it is the DSN's own user (pgcli
// created nothing), otherwise the pgcli-managed monitor role — the label never
// lies by showing a monitor role that isn't in play. Each call site applies its
// own column padding.
func pgexporterConnectParts(pc config.PGExporterConfig) (string, string) {
	if pc.DSN != "" {
		user := ""
		if u, err := url.Parse(pc.DSN); err == nil && u.User != nil {
			user = u.User.Username()
		}
		return "Connects:", fmt.Sprintf("%s (given DSN's own user)", user)
	}
	return "Monitor as:", fmt.Sprintf("%s (pg_monitor)", pc.MonitorUser)
}

// pgexporterTargetSummary describes the monitored target for display without
// ever exposing a password.
func pgexporterTargetSummary(pc config.PGExporterConfig) string {
	switch {
	case pc.TargetInstance != "":
		return "instance " + pc.TargetInstance
	case pc.AdminDSN != "":
		return influx2tsdb.RedactDSN(pc.AdminDSN)
	default:
		return influx2tsdb.RedactDSN(pc.DSN)
	}
}

// runAddonInstallPGExporter installs a pg_exporter container — Pigsty's
// declarative PostgreSQL metrics exporter
// (https://pigsty.cc/docs/pg_exporter/) — monitoring EITHER a pgcli-managed
// instance (--pg-name) OR any reachable PostgreSQL server, so databases
// outside pgcli's management get collected by the same exporter.
//
// The remote target is given ONE of two mutually exclusive URLs:
//   - --admin-dsn: an ADMIN-privileged URL used once to create a low-privilege
//     pg_monitor role on the target, then discarded — only its credential-free
//     address is stored, and the exporter dials as the monitor role thereafter.
//   - --dsn: the URL the exporter dials AS GIVEN, with its own credentials —
//     pgcli runs NO SQL on the target (use it when a suitable role already
//     exists, or when pgcli must not touch the database at all).
//
// In local and --admin-dsn modes the admin URL is never persisted; the
// generated monitor password is printed ONCE and retrievable via
// `pg addon password pgexporter`.
//
// Like node_exporter the container is stateless and needs no rendered config
// (the default collector set is baked into the binary); a custom collector YAML
// can be passed through with --config-file and hot-reloaded via SIGHUP
// (`pg addon pgexporter reload`).
func runAddonInstallPGExporter(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = pgExporterDefaultName
	}
	imageTag, _ := cmd.Flags().GetString("image")
	port, _ := cmd.Flags().GetInt("port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	extraArgs, _ := cmd.Flags().GetStringArray("extra-arg")
	force, _ := cmd.Flags().GetBool("force")
	pgName, _ := cmd.Flags().GetString("pg-name")
	dsn, _ := cmd.Flags().GetString("dsn")
	adminDSN, _ := cmd.Flags().GetString("admin-dsn")
	monitorUser, _ := cmd.Flags().GetString("monitor-user")
	configFile, _ := cmd.Flags().GetString("config-file")

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

	if cfg.Addons.PGExporter == nil {
		cfg.Addons.PGExporter = make(map[string]config.PGExporterConfig)
	}
	existing, ok := cfg.Addons.PGExporter[name]
	if !ok {
		existing = config.PGExporterConfig{
			ContainerName: "pgcli-pgexporter" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}

	// Target: exactly one of --pg-name (a local instance), --admin-dsn (a
	// remote server, pgcli creates a monitor role on it), or --dsn (a remote
	// server, the exporter dials the URL as given). The whole point of the
	// remote modes is that non-pgcli databases get collected too; --dsn and
	// --admin-dsn never appear together — host/port/database come from whichever
	// one is passed.
	switch {
	case pgName != "" && (dsn != "" || adminDSN != ""):
		return fmt.Errorf("--pg-name is mutually exclusive with --dsn and --admin-dsn")
	case dsn != "" && adminDSN != "":
		return fmt.Errorf("--dsn and --admin-dsn are mutually exclusive — pass --admin-dsn to create a monitor role on the target, or --dsn to dial the URL as given")
	case pgName != "":
		existing.TargetInstance, existing.DSN, existing.AdminDSN = pgName, "", ""
	case adminDSN != "":
		addr, err := stripDSNCredentials(adminDSN)
		if err != nil {
			return err
		}
		existing.AdminDSN, existing.TargetInstance, existing.DSN = addr, "", ""
	case dsn != "":
		existing.DSN, existing.TargetInstance, existing.AdminDSN = dsn, "", ""
	case existing.TargetInstance == "" && existing.DSN == "" && existing.AdminDSN == "":
		return fmt.Errorf("a target is required: pass --pg-name <local-instance>, --admin-dsn postgres://... (creates a monitor role), or --dsn postgres://... (dial as given)")
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
	if monitorUser != "" {
		existing.MonitorUser = monitorUser
	}
	if configFile != "" {
		abs, err := filepath.Abs(configFile)
		if err != nil {
			return fmt.Errorf("resolving --config-file: %w", err)
		}
		if _, err := os.Stat(abs); err != nil {
			return fmt.Errorf("--config-file %s: %w", configFile, err)
		}
		existing.ConfigFile = abs
	}
	cfg.Addons.PGExporter[name] = existing

	cfg.ApplyDefaults()
	pc := cfg.Addons.PGExporter[name]
	if err := cfg.Validate(); err != nil {
		return err
	}

	// The container needs no proxy bridge knowledge, but the exporter dials its
	// target from inside it — a local instance's derived DSN is 127.0.0.1,
	// unreachable from a macOS bridge container. Warn rather than fail (the
	// remote --dsn path works fine there).
	if platform.Detect() == platform.MacOS && pc.TargetInstance != "" {
		fmt.Println("  [!] NOTE: on macOS the container cannot reach a local instance's 127.0.0.1 DSN — for Mac hosts point --dsn at a reachable address instead")
	}

	// A live container with --force absent is left alone (node_exporter
	// semantics: no SIGHUP for URL/port changes, so changes need --force).
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}
	pm, err := podman.NewPGExporterManager(cfg)
	if err != nil {
		return fmt.Errorf("pg_exporter manager: %w", err)
	}
	running, err := pm.ContainerRunning(pc.ContainerName)
	if err != nil {
		return err
	}
	if running && !force {
		fmt.Println("  [OK] pg_exporter running, config unchanged")
		cfg.Addons.PGExporter[name] = pc
		if err := cfg.Save(path); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		printPGExporterSummary(&pc, name, false, false, "")
		return nil
	}

	// --- monitor role ------------------------------------------------------
	// Three cases. --dsn is dial-as-is: NO SQL ever runs on the target and the
	// given credentials ARE the exporter's (any stale monitor secret is
	// dropped). The local and --admin-dsn modes create/rotate the role
	// idempotently — local with the re-derived admin DSN, remote with the
	// --admin-dsn the user just passed (the only moment pgcli asks for admin
	// rights, and those credentials are never stored). A --force reinstall of an
	// --admin-dsn instance WITHOUT a fresh --admin-dsn has no admin rights by
	// design, so it reuses the stored monitor password rather than rotating.
	passwordPrinted := ""
	switch {
	case pc.DSN != "":
		pc.MonitorPassword, pc.MonitorDSN = "", ""
		fmt.Println("-> --dsn: the exporter dials this URL as given; no monitor role is created on the target")
	case pc.TargetInstance != "" || adminDSN != "":
		setupDSN := adminDSN
		if pc.TargetInstance != "" {
			var err error
			if setupDSN, err = pgexporterTargetDSN(cfg, pc); err != nil {
				return err
			}
		}
		fmt.Printf("-> Creating monitor role %q on the target (pg_monitor)...\n", pc.MonitorUser)
		pw, err := pm.SetupMonitorRole(setupDSN, pc.MonitorUser)
		if err != nil {
			return err
		}
		pc.MonitorPassword = pw
		passwordPrinted = pw
		fmt.Println("  [OK] monitor role ready")
	default:
		if pc.MonitorPassword == "" {
			return fmt.Errorf("pgexporter %q has an admin_dsn target but no stored monitor password — re-pass --admin-dsn so pgcli can create the role", name)
		}
		fmt.Printf("-> Reusing the existing monitor role %q (pass --admin-dsn to re-run the setup SQL)\n", pc.MonitorUser)
	}

	monitorDSN, err := pgexporterMonitorDSN(cfg, pc)
	if err != nil {
		return err
	}
	pc.MonitorDSN = monitorDSN
	cfg.Addons.PGExporter[name] = pc

	fmt.Printf("-> Preparing pg_exporter image %s...\n", pc.ImageTag)
	if err := pm.EnsureImage(pc.ImageTag); err != nil {
		return err
	}

	fmt.Println("-> Starting pg_exporter container...")
	if err := pm.EnsureContainer(&pc, monitorDSN); err != nil {
		return err
	}

	cfg.Addons.PGExporter[name] = pc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	printPGExporterSummary(&pc, name, true, force, passwordPrinted)
	return nil
}

// printPGExporterSummary prints the install result. password is the freshly
// generated monitor password, printed exactly once (empty on the dial-as-is
// --dsn path and on no-op installs).
func printPGExporterSummary(pc *config.PGExporterConfig, name string, installed, force bool, password string) {
	fmt.Println()
	switch {
	case installed && force:
		fmt.Printf("✓ pg_exporter %q recreated (--force)\n", name)
	case installed:
		fmt.Printf("✓ pg_exporter installed: %q\n", name)
	default:
		fmt.Printf("✓ pg_exporter %q (unchanged)\n", name)
	}
	fmt.Printf("  Container:  %s\n", pc.ContainerName)
	fmt.Printf("  Image:      %s\n", pc.ImageTag)
	fmt.Printf("  Metrics:    http://%s:%d/metrics\n", pc.Listen, pc.HostPort)
	fmt.Printf("  Target:     %s\n", pgexporterTargetSummary(*pc))
	cl, cv := pgexporterConnectParts(*pc)
	fmt.Printf("  %-12s%s\n", cl, cv)
	if pc.ConfigFile != "" {
		fmt.Printf("  Config:     %s (mounted %s, reload with: pg addon pgexporter reload --name %s)\n", pc.ConfigFile, pgexporterConfigMountHint, name)
	}
	if len(pc.ExtraArgs) > 0 {
		fmt.Printf("  Extra args: %s\n", strings.Join(pc.ExtraArgs, " "))
	}
	if password != "" {
		fmt.Println()
		fmt.Printf("  Monitor password: %s\n", password)
		fmt.Printf("  (printed once — retrieve it later with `pg addon password pgexporter --name %s`)\n", name)
	}
	fmt.Println()
	fmt.Println("  Health probes: /up /primary /replica (also /read); collectors reload via SIGHUP.")
	if pc.Listen == "0.0.0.0" {
		fmt.Println("  NOTE: listening on every interface. pg_exporter has NO authentication and")
		fmt.Printf("        also serves admin endpoints (/reload, /explain, /stat) on %d. Anyone who\n", pc.HostPort)
		fmt.Println("        can reach the port can scrape metrics and trigger reloads — restrict it")
		fmt.Println("        to your monitoring network.")
	}
	fmt.Println("        Point Prometheus at it with:")
	fmt.Printf("          pg addon install prometheus --scrape pg=%s:%d\n", hostOrSelf(pc.Listen), pc.HostPort)
	fmt.Println("        Or add it to a running Prometheus without dropping its other jobs:")
	fmt.Printf("          pg addon install prometheus --scrape-append pg=%s:%d\n", hostOrSelf(pc.Listen), pc.HostPort)
}

// pgexporterConfigMountHint is display-only; mirrors the podman layer's mount
// point without importing a non-exported constant.
const pgexporterConfigMountHint = "/etc/pg_exporter.yml"

// runAddonRemovePGExporter removes a pg_exporter container. The exporter is
// stateless — no data dir — so --clean-data only yields an informational note.
// The monitor role created on the target is NOT dropped (it may be shared or
// hand-managed); the summary hint at install time documents the one-line
// `DROP ROLE` if the user wants it gone.
func runAddonRemovePGExporter(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = pgExporterDefaultName
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

	if cfg.Addons.PGExporter == nil {
		return fmt.Errorf("no pgexporter add-ons configured")
	}
	pc, ok := cfg.Addons.PGExporter[name]
	if !ok {
		return fmt.Errorf("pg_exporter %q not found", name)
	}

	pm, err := podman.NewPGExporterManager(cfg)
	if err != nil {
		return fmt.Errorf("pg_exporter manager: %w", err)
	}

	fmt.Printf("-> Removing pg_exporter %q...\n", name)
	if err := pm.Remove(&pc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.PGExporter, name)
	if len(cfg.Addons.PGExporter) == 0 {
		cfg.Addons.PGExporter = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ pg_exporter %q removed\n", name)
	if pc.MonitorPassword != "" {
		// The role also carries a per-database column grant on pg_subscription,
		// so a bare DROP ROLE fails until it's revoked in each database it was
		// granted in — say so in the hint (E2E-learned).
		fmt.Printf("  The monitor role %q on the target was kept; to drop it, first revoke its grant in every database it monitors:\n", pc.MonitorUser)
		fmt.Printf("    REVOKE SELECT (oid) ON pg_catalog.pg_subscription FROM \"%s\"; DROP ROLE \"%s\";\n", pc.MonitorUser, pc.MonitorUser)
	}
	return nil
}

// runAddonStartPGExporter starts a stored pg_exporter container, re-deriving
// the monitor DSN first (influx2tsdb semantics: a rotated instance password or
// changed port takes effect without a reinstall when the container is
// recreated; a plain `podman start` keeps the original env).
func runAddonStartPGExporter(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = pgExporterDefaultName
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.PGExporter == nil {
		return fmt.Errorf("no pgexporter add-ons configured (run 'pg addon install pgexporter')")
	}
	pc, ok := cfg.Addons.PGExporter[name]
	if !ok {
		return fmt.Errorf("pg_exporter %q not found (run 'pg addon install pgexporter --name %s')", name, name)
	}
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}
	monitorDSN, err := pgexporterMonitorDSN(cfg, pc)
	if err != nil {
		return err
	}
	pm, err := podman.NewPGExporterManager(cfg)
	if err != nil {
		return fmt.Errorf("pg_exporter manager: %w", err)
	}
	fmt.Printf("-> Starting pg_exporter %q...\n", name)
	return pm.StartContainer(&pc, monitorDSN)
}

func runAddonStopPGExporter(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = pgExporterDefaultName
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	pc, ok := cfg.Addons.PGExporter[name]
	if !ok {
		return fmt.Errorf("pg_exporter %q not found", name)
	}
	pm, err := podman.NewPGExporterManager(cfg)
	if err != nil {
		return fmt.Errorf("pg_exporter manager: %w", err)
	}
	running, _ := pm.ContainerRunning(pc.ContainerName)
	if !running {
		fmt.Printf("pg_exporter %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping pg_exporter %q...\n", name)
	if _, err := pm.Stop(pc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ pg_exporter %q stopped\n", name)
	return nil
}

// runAddonReloadPGExporter SIGHUPs the exporter to re-read its collector
// config. Without --config-file pgcli runs the binary's baked-in default
// config, so the signal is harmless but has nothing to reload — say so.
// URL/port/listen changes need `install --force` (container recreation).
func runAddonReloadPGExporter(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = pgExporterDefaultName
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	pc, ok := cfg.Addons.PGExporter[name]
	if !ok {
		return fmt.Errorf("pg_exporter %q not found", name)
	}
	pm, err := podman.NewPGExporterManager(cfg)
	if err != nil {
		return fmt.Errorf("pg_exporter manager: %w", err)
	}
	running, err := pm.ContainerRunning(pc.ContainerName)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("pg_exporter %q is not running — nothing to reload", name)
	}
	if err := pm.Reload(pc.ContainerName); err != nil {
		return err
	}
	if pc.ConfigFile == "" {
		fmt.Printf("✓ SIGHUP sent to pg_exporter %q — it uses the built-in default collectors, so nothing changed (mount a --config-file to make reload meaningful)\n", name)
	} else {
		fmt.Printf("✓ pg_exporter %q reloaded its collector config from %s\n", name, pc.ConfigFile)
	}
	return nil
}
