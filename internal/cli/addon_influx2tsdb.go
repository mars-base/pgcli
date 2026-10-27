package cli

import (
	"fmt"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/influx2tsdb"
	"github.com/mars-base/pgcli/internal/podman"
)

const influx2TSDBDefaultName = "influx2tsdb"

// resolveInflux2TSDBInstance loads the config and returns the named proxy
// deployment with defaults applied.
func resolveInflux2TSDBInstance(name string) (*config.Config, string, config.Influx2TSDBConfig, error) {
	c, path, err := loadAddonCfg()
	if err != nil {
		return nil, "", config.Influx2TSDBConfig{}, err
	}
	c.ApplyDefaults()
	ic, ok := c.Addons.Influx2TSDB[name]
	if !ok {
		return nil, "", config.Influx2TSDBConfig{}, fmt.Errorf("influx2tsdb %q not found (see 'pg addon list')", name)
	}
	return c, path, ic, nil
}

// influx2TSDBTargetDSN resolves the stored target to a connection string. A
// local instance's DSN is derived now, so a changed port or password in
// pg.yaml takes effect on the next proxy (re)start.
func influx2TSDBTargetDSN(c *config.Config, ic config.Influx2TSDBConfig) (string, error) {
	if ic.TargetInstance != "" {
		inst, ok := c.Instances[ic.TargetInstance]
		if !ok {
			return "", fmt.Errorf("target instance %q not found in config", ic.TargetInstance)
		}
		return influx2tsdb.LocalDSN(inst)
	}
	if ic.DSN != "" {
		return ic.DSN, nil
	}
	return "", fmt.Errorf("influx2tsdb %q has neither target_instance nor dsn", ic.Name)
}

// checkInflux2TSDBTarget validates a local target before the container starts:
// the proxy exits immediately when PostgreSQL is unreachable, so a stopped
// instance only produces a confusing log tail.
func checkInflux2TSDBTarget(c *config.Config, ic config.Influx2TSDBConfig) error {
	if ic.TargetInstance == "" {
		return nil
	}
	inst, ok := c.Instances[ic.TargetInstance]
	if !ok {
		return fmt.Errorf("target instance %q not found in config", ic.TargetInstance)
	}
	pm, err := podman.New(c)
	if err != nil {
		return fmt.Errorf("podman: %w", err)
	}
	running, err := pm.ContainerRunning(inst.Podman.ContainerName)
	if err != nil {
		return fmt.Errorf("checking target instance %q: %w", ic.TargetInstance, err)
	}
	if !running {
		return fmt.Errorf("target instance %q is not running — start it first: pg start -i %s", ic.TargetInstance, ic.TargetInstance)
	}
	if !hasTimescaleDB(inst.Extensions) {
		fmt.Printf("  [!] instance %q has no timescaledb extension recorded; the proxy will try CREATE EXTENSION on startup\n", ic.TargetInstance)
		fmt.Printf("      install it first if that fails: pg extension install timescaledb -i %s --auto-restart\n", ic.TargetInstance)
	}
	return nil
}

// runAddonInstallInflux2TSDB implements `pg addon install influx2tsdb`.
func runAddonInstallInflux2TSDB(cmd *cobra.Command) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return fmt.Errorf("the influx2tsdb add-on runs a linux/amd64 container image and is Linux x86 only")
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = influx2TSDBDefaultName
	}
	if !config.ValidInflux2TSDBName(name) {
		return fmt.Errorf("invalid influx2tsdb name %q: use letters, digits, '_' and '-', starting with a letter or digit (max 48 chars)", name)
	}
	fmt.Printf("=== pg addon install influx2tsdb (%s) ===\n", name)

	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if c.Addons.Influx2TSDB == nil {
		c.Addons.Influx2TSDB = make(map[string]config.Influx2TSDBConfig)
	}
	ic, present := c.Addons.Influx2TSDB[name]
	force, _ := cmd.Flags().GetBool("force")
	if present && !force {
		c.ApplyDefaults()
		ic = c.Addons.Influx2TSDB[name]
		state := "stopped"
		if m, err := podman.NewInflux2TSDBManager(c); err == nil {
			if running, _ := m.ContainerRunning(ic.ContainerName); running {
				state = "running"
			}
		}
		fmt.Printf("✓ influx2tsdb %q already installed (%s, HTTP port %d, target %s) — re-install with --force to rewrite the config and recreate the container\n", name, state, ic.Port, influx2tsdb.TargetSummary(ic))
		fmt.Printf("  Status: pg addon influx2tsdb status --name %s\n", name)
		return nil
	}
	if !present {
		ic = config.Influx2TSDBConfig{Name: name}
	}
	ic.Name = name

	fs := cmd.Flags()
	// Target: --pg-name selects a local instance, --dsn an external server.
	pgName, _ := fs.GetString("pg-name")
	dsn, _ := fs.GetString("dsn")
	switch {
	case pgName != "" && dsn != "":
		return fmt.Errorf("--pg-name and --dsn are mutually exclusive")
	case pgName != "":
		ic.TargetInstance, ic.DSN = pgName, ""
	case dsn != "":
		ic.DSN, ic.TargetInstance = dsn, ""
	case ic.TargetInstance == "" && ic.DSN == "":
		return fmt.Errorf("a target is required: pass --pg-name <local-instance> or --dsn postgres://...")
	}

	if fs.Changed("version") {
		ic.Version, _ = fs.GetString("version")
	}
	if ic.Version == "" {
		ic.Version = config.DefaultInflux2TSDBVersion
	}
	imageTag, ok := config.Influx2TSDBImageTagForVersion(ic.Version)
	if !ok {
		return fmt.Errorf("influx2tsdb %q is not available as a pgcli image (supported: %s)", ic.Version, config.DefaultInflux2TSDBVersion)
	}
	ic.ImageTag = imageTag
	if fs.Changed("port") {
		ic.Port, _ = fs.GetInt("port")
	}
	if fs.Changed("database") {
		ic.Database, _ = fs.GetString("database")
	}
	if fs.Changed("pool-size") {
		ic.PoolSize, _ = fs.GetInt("pool-size")
	}
	if fs.Changed("verbose") {
		ic.Verbose, _ = fs.GetBool("verbose")
	}

	// Resolve defaults (container name, pool size, port) and validate before
	// touching podman.
	c.Addons.Influx2TSDB[name] = ic
	c.ApplyDefaults()
	ic = c.Addons.Influx2TSDB[name]
	if err := c.Validate(); err != nil {
		return err
	}
	if err := checkInflux2TSDBTarget(c, ic); err != nil {
		return err
	}
	targetDSN, err := influx2TSDBTargetDSN(c, ic)
	if err != nil {
		return err
	}

	m, err := podman.NewInflux2TSDBManager(c)
	if err != nil {
		return err
	}

	// --- 1. image ----------------------------------------------------------
	if err := m.EnsureImage(ic.ImageTag); err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] image: %s\n", ic.ImageTag)

	// Persist before starting, so a failed boot is still inspectable and the
	// assigned port survives.
	c.Addons.Influx2TSDB[name] = ic
	if err := c.Save(path); err != nil {
		return fmt.Errorf("  [X] saving config: %w", err)
	}

	// --- 2. container ------------------------------------------------------
	if err := m.EnsureContainer(&ic, targetDSN); err != nil {
		m.LogsTail(ic.ContainerName, 30)
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] container: %s\n", ic.ContainerName)

	fmt.Println()
	fmt.Printf("✓ influx2tsdb %q installed and running\n", name)
	fmt.Printf("  Version:   %s\n", ic.Version)
	fmt.Printf("  Image:     %s\n", ic.ImageTag)
	fmt.Printf("  Container: %s\n", ic.ContainerName)
	fmt.Printf("  Target:    %s\n", influx2tsdb.TargetSummary(ic))
	fmt.Printf("  InfluxDB database: %s\n", influx2tsdb.DatabaseName(ic, targetDSN))
	fmt.Printf("  Endpoint:  http://<host>:%d (InfluxDB 1.x HTTP API; binds all interfaces, no auth)\n", ic.Port)
	fmt.Printf("  Pool:      %d connections\n", ic.PoolSize)
	fmt.Println()
	fmt.Printf("  Status:    pg addon influx2tsdb status --name %s\n", name)
	fmt.Printf("  Logs:      pg logs addon influx2tsdb --name %s -f\n", name)
	fmt.Printf("  Write:     curl -XPOST 'http://127.0.0.1:%d/write?db=%s' --data-binary 'cpu,host=a value=1i'\n", ic.Port, influx2tsdb.DatabaseName(ic, targetDSN))
	fmt.Printf("  Query:     curl -G 'http://127.0.0.1:%d/query?db=%s' --data-urlencode 'q=SELECT * FROM cpu'\n", ic.Port, influx2tsdb.DatabaseName(ic, targetDSN))
	fmt.Printf("  Autostart: pg autostart enable --influx2tsdb --name %s\n", name)
	return nil
}

func hasTimescaleDB(extensions []string) bool {
	for _, e := range extensions {
		if e == "timescaledb" {
			return true
		}
	}
	return false
}

// runAddonRemoveInflux2TSDB removes the container and drops the config entry.
func runAddonRemoveInflux2TSDB(cmd *cobra.Command) error {
	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = influx2TSDBDefaultName
	}
	c.ApplyDefaults()
	ic, ok := c.Addons.Influx2TSDB[name]
	if !ok {
		return fmt.Errorf("influx2tsdb %q not found (see 'pg addon list')", name)
	}
	fmt.Printf("=== pg addon remove influx2tsdb (%s) ===\n", name)

	m, err := podman.NewInflux2TSDBManager(c)
	if err != nil {
		return err
	}
	if err := m.RemoveContainer(&ic); err != nil {
		return err
	}

	delete(c.Addons.Influx2TSDB, name)
	if len(c.Addons.Influx2TSDB) == 0 {
		c.Addons.Influx2TSDB = nil
	}
	if err := c.Save(path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Printf("  [OK] config entry removed (%s)\n", path)
	// The addon has no local data; --clean-data is accepted for consistency
	// with the other addons and has nothing to delete.
	fmt.Printf("✓ influx2tsdb %q removed\n", name)
	return nil
}

// runAddonStartInflux2TSDB implements `pg addon start influx2tsdb`.
func runAddonStartInflux2TSDB(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = influx2TSDBDefaultName
	}
	c, _, ic, err := resolveInflux2TSDBInstance(name)
	if err != nil {
		return err
	}
	if err := checkInflux2TSDBTarget(c, ic); err != nil {
		return err
	}
	targetDSN, err := influx2TSDBTargetDSN(c, ic)
	if err != nil {
		return err
	}
	m, err := podman.NewInflux2TSDBManager(c)
	if err != nil {
		return err
	}
	already, err := m.StartContainer(&ic, targetDSN)
	if err != nil {
		m.LogsTail(ic.ContainerName, 30)
		return err
	}
	if already {
		fmt.Printf("✓ influx2tsdb %q already running (HTTP port %d)\n", name, ic.Port)
		return nil
	}
	fmt.Printf("✓ influx2tsdb %q started (HTTP port %d, target %s)\n", name, ic.Port, influx2tsdb.TargetSummary(ic))
	return nil
}

// runAddonStopInflux2TSDB implements `pg addon stop influx2tsdb`.
func runAddonStopInflux2TSDB(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = influx2TSDBDefaultName
	}
	c, _, ic, err := resolveInflux2TSDBInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewInflux2TSDBManager(c)
	if err != nil {
		return err
	}
	wasRunning, err := m.Stop(ic.ContainerName)
	if err != nil {
		return err
	}
	if !wasRunning {
		fmt.Printf("✓ influx2tsdb %q already stopped\n", name)
		return nil
	}
	fmt.Printf("✓ influx2tsdb %q stopped\n", name)
	return nil
}

// --- nested `pg addon influx2tsdb` tree ------------------------------------

func runAddonInflux2TSDBStatus(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = influx2TSDBDefaultName
	}
	c, _, ic, err := resolveInflux2TSDBInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewInflux2TSDBManager(c)
	if err != nil {
		return err
	}

	state := "stopped"
	if running, _ := m.ContainerRunning(ic.ContainerName); running {
		if err := influx2tsdb.Ping(ic.Port, 2*time.Second); err != nil {
			state = fmt.Sprintf("running (ping failed: %v)", err)
		} else {
			state = "running (ping ok)"
		}
	}
	fmt.Printf("influx2tsdb %q — %s\n", name, state)
	fmt.Printf("  Version:    %s\n", ic.Version)
	fmt.Printf("  Image:      %s\n", ic.ImageTag)
	fmt.Printf("  Container:  %s\n", ic.ContainerName)
	fmt.Printf("  Target:     %s\n", influx2tsdb.TargetSummary(ic))
	dsn, dsnErr := influx2TSDBTargetDSN(c, ic)
	if dsnErr == nil {
		fmt.Printf("  DSN:        %s\n", influx2tsdb.RedactDSN(dsn))
		fmt.Printf("  InfluxDB database: %s\n", influx2tsdb.DatabaseName(ic, dsn))
	} else {
		fmt.Printf("  DSN:        [X] %v\n", dsnErr)
	}
	fmt.Printf("  Endpoint:   http://<host>:%d (binds all interfaces, no auth)\n", ic.Port)
	if ic.TargetInstance != "" {
		if inst, ok := c.Instances[ic.TargetInstance]; ok && !hasTimescaleDB(inst.Extensions) {
			fmt.Printf("  [!] target instance %q has no timescaledb extension recorded\n", ic.TargetInstance)
		}
	}
	fmt.Printf("  Pool:       %d\n", ic.PoolSize)
	fmt.Printf("  Verbose:    %v\n", ic.Verbose)
	fmt.Printf("  Logs:       pg logs addon influx2tsdb --name %s\n", name)
	return nil
}

var addonInflux2TSDBCmd = &cobra.Command{
	Use:   "influx2tsdb",
	Short: "Manage the InfluxDB 1.x protocol proxy in front of TimescaleDB",
	Long: `Manage influx2tsdb-proxy: a Linux/amd64 container that accepts the InfluxDB
1.x HTTP API (Line Protocol writes and InfluxQL queries) and stores the data
in a PostgreSQL database with the TimescaleDB extension.

The proxy is stateless — PostgreSQL holds the data — so the container is
recreated on every start. Each InfluxDB "database" maps to a PostgreSQL
schema; the default database follows the target's database name unless -db is
set. The proxy uses host networking, binds all interfaces and has NO
authentication, so expose it only behind nginx or a trusted network boundary;
writes and queries otherwise bypass PostgreSQL authentication.

A local target (--pg-name) is resolved from pg.yaml on every start, so its
host port and password stay in sync. Use --dsn only for an unmanaged/remote
server; note that the DSN (including the password) is stored in pg.yaml and in
the container's command line (visible via podman inspect).

Subcommands:
  status    Show container state, endpoint, target and database

Examples:
  pg addon install influx2tsdb --pg-name tsdb
  pg addon install influx2tsdb --pg-name tsdb --port 8087 --pool-size 20
  pg addon install influx2tsdb --dsn 'postgres://user:pass@db:5432/metrics?sslmode=disable' --database metrics
  pg addon influx2tsdb status --name influx2tsdb
  pg logs addon influx2tsdb --name influx2tsdb -f
  pg autostart enable --influx2tsdb --name influx2tsdb
  pg addon remove influx2tsdb --name influx2tsdb`,
}

var addonInflux2TSDBStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show container state, endpoint, target and database",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonInflux2TSDBStatus(cmd) },
}

func init() {
	addonCmd.AddCommand(addonInflux2TSDBCmd)
	addonInflux2TSDBCmd.AddCommand(addonInflux2TSDBStatusCmd)
	addonInflux2TSDBStatusCmd.Flags().String("name", "", "influx2tsdb instance name (default \"influx2tsdb\")")

	// influx2tsdb-specific install flags. --name/--pg-name/--dsn/--port/
	// --version/--force/--clean-data are registered by the parent addon flags.
	addonInstallCmd.Flags().Int("pool-size", 0, "influx2tsdb: PostgreSQL connection pool size (-pool; default 10)")
	addonInstallCmd.Flags().String("database", "", "influx2tsdb: default InfluxDB database name (-db; default: the target's PostgreSQL database name)")
	addonInstallCmd.Flags().Bool("verbose", false, "influx2tsdb: log translated SQL and query details (-verbose)")
}
