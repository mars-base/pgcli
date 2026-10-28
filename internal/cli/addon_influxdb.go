package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/influxdb"
	"github.com/mars-base/pgcli/internal/podman"
)

const (
	influxdEntrypoint   = "/usr/local/bin/influxd"
	influxCLIEntrypoint = "/usr/local/bin/influx"
	influxInspectEntry  = "/usr/local/bin/influx_inspect"
)

// resolveInfluxDBInstance loads the config, applies defaults and returns the
// named instance, with the same "not found" hint the other addons use.
func resolveInfluxDBInstance(name string) (*config.Config, string, config.InfluxDBConfig, error) {
	c, path, err := loadAddonCfg()
	if err != nil {
		return nil, "", config.InfluxDBConfig{}, err
	}
	c.ApplyDefaults()
	ic, ok := c.Addons.InfluxDB[name]
	if !ok {
		return nil, "", config.InfluxDBConfig{}, fmt.Errorf("influxdb %q not found (see 'pg addon list')", name)
	}
	return c, path, ic, nil
}

// runAddonInstallInfluxdb implements `pg addon install influxdb`.
func runAddonInstallInfluxdb(cmd *cobra.Command) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("the influxdb add-on runs Linux containers and is Linux/amd64 only")
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "influxdb"
	}
	if !config.ValidInfluxDBName(name) {
		return fmt.Errorf("invalid influxdb instance name %q: use letters, digits, '_' and '-', starting with a letter or digit (max 48 chars)", name)
	}
	fmt.Printf("=== pg addon install influxdb (%s) ===\n", name)

	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if c.Addons.InfluxDB == nil {
		c.Addons.InfluxDB = make(map[string]config.InfluxDBConfig)
	}
	ic, present := c.Addons.InfluxDB[name]
	force, _ := cmd.Flags().GetBool("force")
	if present && !force {
		c.ApplyDefaults()
		ic = c.Addons.InfluxDB[name]
		state := "stopped"
		if m, err := podman.NewInfluxDBManager(c); err == nil {
			if running, _ := m.ContainerRunning(ic.ContainerName); running {
				state = "running"
			}
		}
		fmt.Printf("✓ influxdb %q already installed (%s, HTTP %s:%d, admin %s) — re-install with --force to rewrite the config and recreate the container\n", name, state, ic.Listen, ic.HostPort, ic.AdminUser)
		fmt.Printf("  Password: pg addon password influxdb --name %s\n", name)
		return nil
	}
	if !present {
		ic = config.InfluxDBConfig{Name: name}
	}
	ic.Name = name

	fs := cmd.Flags()
	if fs.Changed("version") {
		ic.Version, _ = fs.GetString("version")
	}
	if ic.Version == "" {
		ic.Version = config.DefaultInfluxDBVersion
	}
	imageTag, ok := config.InfluxDBImageTagForVersion(ic.Version)
	if !ok {
		return fmt.Errorf("InfluxDB %q is not available as a pgcli image (supported: %s)", ic.Version, config.DefaultInfluxDBVersion)
	}
	ic.ImageTag = imageTag
	// --force keeps the stored password unless the flag supplies a new one;
	// otherwise the existing data dir's admin user would remain unreachable.
	if v, _ := fs.GetString("admin-password"); v != "" {
		ic.AdminPassword = v
	}
	if ic.AdminPassword == "" {
		pw, err := generatePassword(20)
		if err != nil {
			return fmt.Errorf("generating admin password: %w", err)
		}
		ic.AdminPassword = pw
	}
	if v, _ := fs.GetString("admin-user"); v != "" {
		ic.AdminUser = v
	}
	if v, _ := fs.GetString("listen"); v != "" {
		ic.Listen = v
	}
	if fs.Changed("http-port") {
		ic.HostPort, _ = fs.GetInt("http-port")
	}
	if fs.Changed("rpc-port") {
		ic.RPCPort, _ = fs.GetInt("rpc-port")
	}
	if v, _ := fs.GetString("data-dir"); v != "" {
		ic.DataDir = v
	}
	// --config-option replaces the stored override list (same contract as
	// --backend/--scrape): pass it again on --force to change the set.
	if fs.Changed("config-option") {
		ic.ExtraConfig, _ = fs.GetStringArray("config-option")
	}

	// Resolve defaults (paths, ports) and validate before touching the host.
	c.Addons.InfluxDB[name] = ic
	c.ApplyDefaults()
	ic = c.Addons.InfluxDB[name]
	if err := c.Validate(); err != nil {
		return err
	}

	m, err := podman.NewInfluxDBManager(c)
	if err != nil {
		return err
	}

	// --- 1. image ----------------------------------------------------------
	if err := m.EnsureImage(ic.ImageTag); err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] image: %s\n", ic.ImageTag)

	// --- 2. data dir and config -------------------------------------------
	if err := os.MkdirAll(ic.DataDir, 0o755); err != nil {
		return fmt.Errorf("  [X] creating data dir %s: %w", ic.DataDir, err)
	}
	if err := influxdb.WriteConfig(ic); err != nil {
		return fmt.Errorf("  [X] writing config: %w", err)
	}
	fmt.Printf("  [OK] data dir: %s\n", ic.DataDir)
	fmt.Printf("  [OK] config:   %s\n", ic.ConfigPath)
	if len(ic.ExtraConfig) > 0 {
		fmt.Printf("  [OK] config overrides:\n")
		for _, opt := range ic.ExtraConfig {
			fmt.Printf("         %s\n", opt)
		}
	}

	// Persist the entry before starting, so a failed boot is still inspectable
	// (and the generated password is not lost).
	c.Addons.InfluxDB[name] = ic
	if err := c.Save(path); err != nil {
		return fmt.Errorf("  [X] saving config: %w", err)
	}

	// --- 3. start and bootstrap authentication ----------------------------
	if err := m.EnsureContainer(&ic); err != nil {
		m.LogsTail(ic.ContainerName, 20)
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] container: %s\n", ic.ContainerName)
	fmt.Printf("  [OK] listening: http://%s:%d (auth enabled)\n", ic.Listen, ic.HostPort)
	fmt.Printf("  [OK] RPC:       127.0.0.1:%d (backup/restore, loopback only)\n", ic.RPCPort)

	runCLI := func(env, args []string) ([]byte, error) {
		return m.CLIOutput(&ic, env, args)
	}
	if err := influxdb.BootstrapAuth(ic, runCLI); err != nil {
		m.LogsTail(ic.ContainerName, 20)
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] admin user %q authenticated\n", ic.AdminUser)

	// --- 4. verify ---------------------------------------------------------
	if out, err := m.Output(&ic, podman.InfluxDBRunSpec{Entrypoint: influxdEntrypoint, Args: []string{"version"}}); err == nil {
		fmt.Printf("  [OK] %s\n", strings.TrimSpace(string(out)))
	}

	fmt.Println()
	fmt.Printf("✓ InfluxDB %q installed\n", name)
	fmt.Printf("  Version:   %s\n", ic.Version)
	fmt.Printf("  Image:     %s\n", ic.ImageTag)
	fmt.Printf("  Container: %s\n", ic.ContainerName)
	fmt.Printf("  HTTP:      http://%s:%d (auth enabled)\n", ic.Listen, ic.HostPort)
	fmt.Printf("  RPC:       127.0.0.1:%d (backup/restore, loopback only)\n", ic.RPCPort)
	fmt.Printf("  Admin:     %s\n", ic.AdminUser)
	fmt.Printf("  Password:  %s\n", ic.AdminPassword)
	fmt.Printf("  Data dir:  %s\n", ic.DataDir)
	fmt.Printf("  Config:    %s\n", ic.ConfigPath)
	fmt.Println()
	fmt.Printf("  Password: pg addon password influxdb --name %s\n", name)
	fmt.Printf("  Status:   pg addon influxdb status --name %s\n", name)
	fmt.Printf("  Shell:    pg influx --name %s\n", name)
	fmt.Printf("  Backup:   pg addon influxdb backup --name %s\n", name)
	return nil
}

// runAddonRemoveInfluxdb removes the container, drops the config entry and
// optionally deletes all instance-owned state.
func runAddonRemoveInfluxdb(cmd *cobra.Command) error {
	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "influxdb"
	}
	cleanData, _ := cmd.Flags().GetBool("clean-data")
	c.ApplyDefaults()
	ic, ok := c.Addons.InfluxDB[name]
	if !ok {
		return fmt.Errorf("influxdb %q not found (see 'pg addon list')", name)
	}
	fmt.Printf("=== pg addon remove influxdb (%s) ===\n", name)

	m, err := podman.NewInfluxDBManager(c)
	if err != nil {
		return err
	}
	if err := m.RemoveContainer(&ic); err != nil {
		return err
	}

	delete(c.Addons.InfluxDB, name)
	if len(c.Addons.InfluxDB) == 0 {
		c.Addons.InfluxDB = nil
	}
	if err := c.Save(path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Printf("  [OK] config entry removed (%s)\n", path)

	if cleanData {
		if err := m.CleanData(&ic); err != nil {
			return err
		}
	} else {
		fmt.Printf("  [i] kept %s (data, config, local backups) — pass --clean-data to drop\n", filepath.Dir(ic.ConfigPath))
	}

	fmt.Printf("✓ InfluxDB %q removed\n", name)
	return nil
}

// runAddonStartInfluxdb implements `pg addon start influxdb`.
func runAddonStartInfluxdb(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "influxdb"
	}
	c, _, ic, err := resolveInfluxDBInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewInfluxDBManager(c)
	if err != nil {
		return err
	}
	already, err := m.StartContainer(&ic)
	if err != nil {
		m.LogsTail(ic.ContainerName, 20)
		return err
	}
	if already {
		fmt.Printf("✓ influxdb %q already running (HTTP %s:%d)\n", name, ic.Listen, ic.HostPort)
		return nil
	}
	fmt.Printf("✓ influxdb %q started (HTTP %s:%d, RPC 127.0.0.1:%d)\n", name, ic.Listen, ic.HostPort, ic.RPCPort)
	return nil
}

// runAddonStopInfluxdb implements `pg addon stop influxdb`.
func runAddonStopInfluxdb(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "influxdb"
	}
	c, _, ic, err := resolveInfluxDBInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewInfluxDBManager(c)
	if err != nil {
		return err
	}
	wasRunning, err := m.Stop(ic.ContainerName)
	if err != nil {
		return err
	}
	if !wasRunning {
		fmt.Printf("✓ influxdb %q already stopped\n", name)
		return nil
	}
	fmt.Printf("✓ influxdb %q stopped\n", name)
	return nil
}

// --- nested `pg addon influxdb` tree ---------------------------------------

func runAddonInfluxdbStatus(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "influxdb"
	}
	c, _, ic, err := resolveInfluxDBInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewInfluxDBManager(c)
	if err != nil {
		return err
	}

	state := "stopped"
	if running, _ := m.ContainerRunning(ic.ContainerName); running {
		if err := influxdb.Ping(ic, 2*time.Second); err != nil {
			state = fmt.Sprintf("running (ping failed: %v)", err)
		} else {
			state = "running (ping ok)"
		}
	}
	fmt.Printf("influxdb %q — %s\n", name, state)
	if out, err := m.Output(&ic, podman.InfluxDBRunSpec{Entrypoint: influxdEntrypoint, Args: []string{"version"}}); err == nil {
		fmt.Printf("  Version:    %s\n", strings.TrimSpace(string(out)))
	} else {
		fmt.Printf("  Version:    %s (configured)\n", ic.Version)
	}
	fmt.Printf("  Image:      %s\n", ic.ImageTag)
	fmt.Printf("  Container:  %s\n", ic.ContainerName)
	fmt.Printf("  HTTP:       http://%s:%d (auth enabled)\n", ic.Listen, ic.HostPort)
	fmt.Printf("  RPC:        127.0.0.1:%d (backup/restore, loopback only)\n", ic.RPCPort)
	fmt.Printf("  Admin user: %s\n", ic.AdminUser)
	fmt.Printf("  Data dir:   %s\n", ic.DataDir)
	fmt.Printf("  Config:     %s\n", ic.ConfigPath)
	fmt.Printf("  Logs:       pg logs addon influxdb --name %s\n", name)
	fmt.Printf("  CLI:        pg influx --name %s\n", name)
	return nil
}

func runAddonInfluxdbShell(cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "influxdb"
	}
	c, _, ic, err := resolveInfluxDBInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewInfluxDBManager(c)
	if err != nil {
		return err
	}
	if running, _ := m.ContainerRunning(ic.ContainerName); !running {
		fmt.Fprintf(os.Stderr, "[!] influxdb %q is not running — start it with: pg addon start influxdb --name %s\n", name, name)
	}
	shellArgs := []string{}
	if db, _ := cmd.Flags().GetString("database"); db != "" {
		shellArgs = append(shellArgs, "-database", db)
	}
	shellArgs = append(shellArgs, args...)
	return m.RunCLI(&ic, influxdb.CLIEnv(ic, true), shellArgs, stdinIsTerminal())
}

func runAddonInfluxdbBackup(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "influxdb"
	}
	c, _, ic, err := resolveInfluxDBInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewInfluxDBManager(c)
	if err != nil {
		return err
	}
	if running, _ := m.ContainerRunning(ic.ContainerName); !running {
		return fmt.Errorf("influxdb %q is not running — 'pg addon start influxdb --name %s' first (backup is online)", name, name)
	}

	out, _ := cmd.Flags().GetString("out")
	if out == "" {
		out = filepath.Join(filepath.Dir(ic.ConfigPath), "backups", time.Now().UTC().Format("20060102T150405Z"))
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return err
	}
	database, _ := cmd.Flags().GetString("database")
	since, _ := cmd.Flags().GetString("since")
	start, _ := cmd.Flags().GetString("start")
	end, _ := cmd.Flags().GetString("end")

	// The backup runs in a one-shot container and writes to a fixed mount; the
	// server's RPC is reachable because both share the host network namespace.
	args, err := influxdb.BackupArgs(ic, "/backup", influxdb.BackupOptions{
		Database: database,
		Since:    since,
		Start:    start,
		End:      end,
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return fmt.Errorf("creating backup dir %s: %w", out, err)
	}

	fmt.Printf("-> Backing up influxdb %q to %s...\n", name, out)
	err = m.Run(&ic, podman.InfluxDBRunSpec{
		Entrypoint: influxdEntrypoint,
		Mounts:     []string{out + ":/backup:z"},
		Args:       args,
	})
	if err != nil {
		return fmt.Errorf("influxd backup failed: %w", err)
	}
	fmt.Printf("✓ backup written to %s\n", out)
	return nil
}

func runAddonInfluxdbRestore(cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "influxdb"
	}
	c, _, ic, err := resolveInfluxDBInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewInfluxDBManager(c)
	if err != nil {
		return err
	}

	backupDir, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	if st, err := os.Stat(backupDir); err != nil || !st.IsDir() {
		if err == nil {
			err = fmt.Errorf("not a directory")
		}
		return fmt.Errorf("backup directory %s: %w", backupDir, err)
	}
	database, _ := cmd.Flags().GetString("database")
	newdb, _ := cmd.Flags().GetString("newdb")

	restoreArgs, err := influxdb.RestoreArgs(ic, "/backup", database, newdb)
	if err != nil {
		return err
	}

	// influxd restore is ONLINE: it sends the restore over the RPC endpoint.
	// Bring the instance up first and leave it running afterwards.
	if running, _ := m.ContainerRunning(ic.ContainerName); !running {
		if _, err := m.StartContainer(&ic); err != nil {
			m.LogsTail(ic.ContainerName, 20)
			return fmt.Errorf("restore needs a running influxdb and %q did not start: %w", name, err)
		}
		fmt.Printf("  [OK] started influxdb (restore is online)\n")
	}

	fmt.Printf("-> Restoring %s into influxdb %q...\n", backupDir, name)
	err = m.Run(&ic, podman.InfluxDBRunSpec{
		Entrypoint: influxdEntrypoint,
		Mounts:     []string{backupDir + ":/backup:ro,z"},
		Args:       restoreArgs,
	})
	if err != nil {
		return fmt.Errorf("influxd restore failed: %w", err)
	}

	fmt.Printf("✓ restore complete — influxdb %q running (HTTP %s:%d)\n", name, ic.Listen, ic.HostPort)
	return nil
}

// --- database / import / export --------------------------------------------

func requireInfluxDBRunning(m *podman.InfluxDBManager, ic config.InfluxDBConfig) error {
	if running, _ := m.ContainerRunning(ic.ContainerName); !running {
		return fmt.Errorf("influxdb %q is not running — 'pg addon start influxdb --name %s' first", ic.Name, ic.Name)
	}
	return nil
}

func runAddonInfluxdbDatabaseCreate(cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "influxdb"
	}
	c, _, ic, err := resolveInfluxDBInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewInfluxDBManager(c)
	if err != nil {
		return err
	}
	if err := requireInfluxDBRunning(m, ic); err != nil {
		return err
	}
	retention, _ := cmd.Flags().GetString("retention")
	shardDuration, _ := cmd.Flags().GetString("shard-duration")
	rp, _ := cmd.Flags().GetString("rp")
	query, err := influxDBCreateDatabaseQuery(args[0], retention, shardDuration, rp)
	if err != nil {
		return err
	}
	if err := m.RunCLI(&ic, influxdb.CLIEnv(ic, true), []string{"-execute", query}, false); err != nil {
		return err
	}
	fmt.Printf("✓ database %q ready on influxdb %q\n", args[0], name)
	return nil
}

func runAddonInfluxdbDatabaseList(cmd *cobra.Command, _ []string) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "influxdb"
	}
	c, _, ic, err := resolveInfluxDBInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewInfluxDBManager(c)
	if err != nil {
		return err
	}
	if err := requireInfluxDBRunning(m, ic); err != nil {
		return err
	}
	return m.RunCLI(&ic, influxdb.CLIEnv(ic, true), []string{"-execute", "SHOW DATABASES"}, stdinIsTerminal())
}

var influxDBDurationRE = regexp.MustCompile(`^(?:[0-9]+(?:ns|ms|u|µ|s|m|h|d|w))+$`)

func validateInfluxDBDuration(flag, d string) error {
	if d == "" {
		return fmt.Errorf("--%s is required", flag)
	}
	if d == "INF" || influxDBDurationRE.MatchString(d) {
		return nil
	}
	return fmt.Errorf("invalid --%s %q: use an InfluxQL duration such as 30d, 12h or 1h30m (INF allowed for retention duration)", flag, d)
}

func validateInfluxDBShardDuration(d string) error {
	if err := validateInfluxDBDuration("shard-duration", d); err != nil {
		return err
	}
	if d == "INF" {
		return fmt.Errorf("--shard-duration cannot be INF")
	}
	return nil
}

func influxDBCreateDatabaseQuery(db, retention, shardDuration, rp string) (string, error) {
	if strings.TrimSpace(db) == "" {
		return "", fmt.Errorf("database name must not be empty")
	}
	q := "CREATE DATABASE " + influxdb.InfluxQLIdent(db)
	if retention == "" {
		if shardDuration != "" || rp != "" {
			return "", fmt.Errorf("--shard-duration and --rp require --retention")
		}
		return q, nil
	}
	if err := validateInfluxDBDuration("retention", retention); err != nil {
		return "", err
	}
	q += " WITH DURATION " + retention + " REPLICATION 1"
	if shardDuration != "" {
		if err := validateInfluxDBShardDuration(shardDuration); err != nil {
			return "", err
		}
		q += " SHARD DURATION " + shardDuration
	}
	if rp != "" {
		q += " NAME " + influxdb.InfluxQLIdent(rp)
	}
	return q, nil
}

func influxDBRetentionCreateQuery(db, rp, duration, shardDuration string, makeDefault bool) (string, error) {
	if strings.TrimSpace(db) == "" {
		return "", fmt.Errorf("--database is required")
	}
	if strings.TrimSpace(rp) == "" {
		return "", fmt.Errorf("--rp is required")
	}
	if err := validateInfluxDBDuration("duration", duration); err != nil {
		return "", err
	}
	q := "CREATE RETENTION POLICY " + influxdb.InfluxQLIdent(rp) + " ON " + influxdb.InfluxQLIdent(db) +
		" DURATION " + duration + " REPLICATION 1"
	if shardDuration != "" {
		if err := validateInfluxDBShardDuration(shardDuration); err != nil {
			return "", err
		}
		q += " SHARD DURATION " + shardDuration
	}
	if makeDefault {
		q += " DEFAULT"
	}
	return q, nil
}

func influxDBRetentionAlterQuery(db, rp, duration, shardDuration string, makeDefault bool) (string, error) {
	if strings.TrimSpace(db) == "" {
		return "", fmt.Errorf("--database is required")
	}
	if strings.TrimSpace(rp) == "" {
		return "", fmt.Errorf("--rp is required")
	}
	if duration == "" && shardDuration == "" && !makeDefault {
		return "", fmt.Errorf("nothing to change: pass --duration, --shard-duration and/or --default")
	}
	q := "ALTER RETENTION POLICY " + influxdb.InfluxQLIdent(rp) + " ON " + influxdb.InfluxQLIdent(db)
	if duration != "" {
		if err := validateInfluxDBDuration("duration", duration); err != nil {
			return "", err
		}
		q += " DURATION " + duration
	}
	if shardDuration != "" {
		if err := validateInfluxDBShardDuration(shardDuration); err != nil {
			return "", err
		}
		q += " SHARD DURATION " + shardDuration
	}
	if makeDefault {
		q += " DEFAULT"
	}
	return q, nil
}

func influxDBManagerForCommand(cmd *cobra.Command) (*podman.InfluxDBManager, config.InfluxDBConfig, error) {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "influxdb"
	}
	c, _, ic, err := resolveInfluxDBInstance(name)
	if err != nil {
		return nil, ic, err
	}
	m, err := podman.NewInfluxDBManager(c)
	if err != nil {
		return nil, ic, err
	}
	if err := requireInfluxDBRunning(m, ic); err != nil {
		return nil, ic, err
	}
	return m, ic, nil
}

func runAddonInfluxdbDatabaseDrop(cmd *cobra.Command, args []string) error {
	m, ic, err := influxDBManagerForCommand(cmd)
	if err != nil {
		return err
	}
	db := args[0]
	if strings.TrimSpace(db) == "" {
		return fmt.Errorf("database name must not be empty")
	}
	yes, _ := cmd.Flags().GetBool("yes")
	if !yes && !confirmPrompt(fmt.Sprintf("Drop database %q on influxdb %q? All of its data is deleted. [y/N] ", db, ic.Name)) {
		fmt.Println("aborted")
		return nil
	}
	if err := m.RunCLI(&ic, influxdb.CLIEnv(ic, true), []string{"-execute", "DROP DATABASE " + influxdb.InfluxQLIdent(db)}, false); err != nil {
		return err
	}
	fmt.Printf("✓ database %q dropped\n", db)
	return nil
}

func runAddonInfluxdbRetentionList(cmd *cobra.Command, _ []string) error {
	m, ic, err := influxDBManagerForCommand(cmd)
	if err != nil {
		return err
	}
	db, _ := cmd.Flags().GetString("database")
	if strings.TrimSpace(db) == "" {
		return fmt.Errorf("--database is required")
	}
	return m.RunCLI(&ic, influxdb.CLIEnv(ic, true), []string{"-execute", "SHOW RETENTION POLICIES ON " + influxdb.InfluxQLIdent(db)}, stdinIsTerminal())
}

func runAddonInfluxdbRetentionCreate(cmd *cobra.Command, _ []string) error {
	m, ic, err := influxDBManagerForCommand(cmd)
	if err != nil {
		return err
	}
	db, _ := cmd.Flags().GetString("database")
	rp, _ := cmd.Flags().GetString("rp")
	duration, _ := cmd.Flags().GetString("duration")
	shardDuration, _ := cmd.Flags().GetString("shard-duration")
	makeDefault, _ := cmd.Flags().GetBool("default")
	query, err := influxDBRetentionCreateQuery(db, rp, duration, shardDuration, makeDefault)
	if err != nil {
		return err
	}
	if err := m.RunCLI(&ic, influxdb.CLIEnv(ic, true), []string{"-execute", query}, false); err != nil {
		return err
	}
	fmt.Printf("✓ retention policy %q created on %q\n", rp, db)
	return nil
}

func runAddonInfluxdbRetentionAlter(cmd *cobra.Command, _ []string) error {
	m, ic, err := influxDBManagerForCommand(cmd)
	if err != nil {
		return err
	}
	db, _ := cmd.Flags().GetString("database")
	rp, _ := cmd.Flags().GetString("rp")
	duration, _ := cmd.Flags().GetString("duration")
	shardDuration, _ := cmd.Flags().GetString("shard-duration")
	makeDefault, _ := cmd.Flags().GetBool("default")
	query, err := influxDBRetentionAlterQuery(db, rp, duration, shardDuration, makeDefault)
	if err != nil {
		return err
	}
	if err := m.RunCLI(&ic, influxdb.CLIEnv(ic, true), []string{"-execute", query}, false); err != nil {
		return err
	}
	fmt.Printf("✓ retention policy %q updated on %q\n", rp, db)
	return nil
}

func runAddonInfluxdbRetentionDrop(cmd *cobra.Command, _ []string) error {
	m, ic, err := influxDBManagerForCommand(cmd)
	if err != nil {
		return err
	}
	db, _ := cmd.Flags().GetString("database")
	rp, _ := cmd.Flags().GetString("rp")
	if strings.TrimSpace(db) == "" {
		return fmt.Errorf("--database is required")
	}
	if strings.TrimSpace(rp) == "" {
		return fmt.Errorf("--rp is required")
	}
	yes, _ := cmd.Flags().GetBool("yes")
	if !yes && !confirmPrompt(fmt.Sprintf("Drop retention policy %q on database %q? Its data is deleted. [y/N] ", rp, db)) {
		fmt.Println("aborted")
		return nil
	}
	query := "DROP RETENTION POLICY " + influxdb.InfluxQLIdent(rp) + " ON " + influxdb.InfluxQLIdent(db)
	if err := m.RunCLI(&ic, influxdb.CLIEnv(ic, true), []string{"-execute", query}, false); err != nil {
		return err
	}
	fmt.Printf("✓ retention policy %q dropped from %q\n", rp, db)
	return nil
}

func runAddonInfluxdbExport(cmd *cobra.Command, _ []string) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "influxdb"
	}
	c, _, ic, err := resolveInfluxDBInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewInfluxDBManager(c)
	if err != nil {
		return err
	}

	db, _ := cmd.Flags().GetString("database")
	out, _ := cmd.Flags().GetString("out")
	compress, _ := cmd.Flags().GetBool("compress")
	noStart, _ := cmd.Flags().GetBool("no-start")
	start, _ := cmd.Flags().GetString("start")
	end, _ := cmd.Flags().GetString("end")
	if (start == "") != (end == "") {
		return fmt.Errorf("--start and --end must be given together")
	}
	if out == "" {
		label := db
		if label == "" {
			label = "all"
		}
		out = filepath.Join(filepath.Dir(ic.ConfigPath), "exports", fmt.Sprintf("%s-%s.txt", label, time.Now().UTC().Format("20060102T150405Z")))
		if compress {
			out += ".gz"
		}
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("creating export dir %s: %w", filepath.Dir(out), err)
	}

	// influx_inspect reads the TSM files directly; stop the server first so the
	// files are consistent, and mount the data dir read-only.
	wasRunning, err := m.Stop(ic.ContainerName)
	if err != nil {
		return fmt.Errorf("stopping influxdb %q: %w", name, err)
	}
	if wasRunning {
		fmt.Printf("  [OK] stopped influxdb (export reads the data files directly)\n")
	}

	args := []string{
		"export",
		"-datadir", influxdb.ContainerDataDir + "/data",
		"-waldir", influxdb.ContainerDataDir + "/wal",
		"-out", "/export/" + filepath.Base(out),
	}
	if db != "" {
		args = append(args, "-database", db)
	}
	if start != "" {
		args = append(args, "-start", start, "-end", end)
	}
	if compress {
		args = append(args, "-compress")
	}
	fmt.Printf("-> Exporting influxdb %q to %s...\n", name, out)
	exportErr := m.Run(&ic, podman.InfluxDBRunSpec{
		Entrypoint: influxInspectEntry,
		Mounts: []string{
			ic.DataDir + ":" + influxdb.ContainerDataDir + ":ro,z",
			filepath.Dir(out) + ":/export:z",
		},
		Args: args,
	})

	if !noStart && wasRunning {
		if _, err := m.StartContainer(&ic); err != nil {
			m.LogsTail(ic.ContainerName, 20)
			if exportErr == nil {
				return fmt.Errorf("export succeeded but the instance did not restart: %w", err)
			}
			return fmt.Errorf("export failed (%v) and the instance did not restart: %w", exportErr, err)
		}
		fmt.Printf("  [OK] influxdb %q restarted\n", name)
	}
	if exportErr != nil {
		return fmt.Errorf("influx_inspect export failed: %w", exportErr)
	}
	fmt.Printf("✓ export written to %s\n", out)
	return nil
}

// rewriteImportContext copies src to dst with every "# CONTEXT-DATABASE:"
// directive pointed at target. The influx importer lets a file's context
// directive win over -database, so influx_inspect exports (which always carry
// one) would otherwise ignore --database.
func rewriteImportContext(src, target, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening import file %s: %w", src, err)
	}
	defer in.Close()

	tmp, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("creating temporary import file: %w", err)
	}
	reader := bufio.NewReader(in)
	for {
		line, readErr := reader.ReadString('\n')
		if strings.HasPrefix(line, "# CONTEXT-DATABASE:") {
			line = "# CONTEXT-DATABASE:" + target + "\n"
		}
		if line != "" {
			if _, err := tmp.WriteString(line); err != nil {
				tmp.Close()
				return fmt.Errorf("writing temporary import file: %w", err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			tmp.Close()
			return fmt.Errorf("reading import file %s: %w", src, readErr)
		}
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temporary import file: %w", err)
	}
	return nil
}

func runAddonInfluxdbImport(cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "influxdb"
	}
	c, _, ic, err := resolveInfluxDBInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewInfluxDBManager(c)
	if err != nil {
		return err
	}
	file, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	st, err := os.Stat(file)
	if err != nil {
		return fmt.Errorf("import file %s: %w", file, err)
	}
	if st.IsDir() {
		return fmt.Errorf("import path %s is a directory (expected an influx_inspect export file)", file)
	}
	if err := requireInfluxDBRunning(m, ic); err != nil {
		return err
	}
	db, _ := cmd.Flags().GetString("database")

	importName := filepath.Base(file)
	mountDir := filepath.Dir(file)
	cleanup := func() {}
	if db != "" {
		// Copy-and-rewrite under the instance dir so the file is visible in the
		// helper container without mounting a user-chosen directory.
		importsDir := filepath.Join(filepath.Dir(ic.ConfigPath), "imports")
		if err := os.MkdirAll(importsDir, 0o755); err != nil {
			return fmt.Errorf("creating import dir %s: %w", importsDir, err)
		}
		dir, err := os.MkdirTemp(importsDir, "import-*")
		if err != nil {
			return fmt.Errorf("creating temporary import dir: %w", err)
		}
		cleanup = func() { os.RemoveAll(dir) }
		importName = filepath.Base(file)
		if err := rewriteImportContext(file, db, filepath.Join(dir, importName)); err != nil {
			cleanup()
			return err
		}
		mountDir = dir
		fmt.Printf("  [i] import context rewritten to database %q\n", db)
	}
	defer cleanup()

	extra := []string{"-import", "-path", "/import/" + importName}
	if db != "" {
		extra = append(extra, "-database", db)
	}
	if prec, _ := cmd.Flags().GetString("precision"); prec != "" {
		extra = append(extra, "-precision", prec)
	}
	fmt.Printf("-> Importing %s into influxdb %q...\n", file, name)
	err = m.Run(&ic, podman.InfluxDBRunSpec{
		Entrypoint: influxCLIEntrypoint,
		Mounts:     []string{mountDir + ":/import:ro,z"},
		Env:        influxdb.CLIEnv(ic, true),
		Args:       append(influxdb.CLIArgs(ic), extra...),
	})
	if err != nil {
		return fmt.Errorf("influx -import failed: %w", err)
	}
	fmt.Printf("✓ import of %s complete\n", file)
	return nil
}

// --- `pg addon influxdb config` --------------------------------------------

// persistInfluxDBConfig renders the managed config file from ic (which now
// includes ExtraConfig), stores the entry in pg.yaml and optionally restarts
// the container so the change takes effect. Rewriting the file is what makes an
// override survive every later restart.
func persistInfluxDBConfig(c *config.Config, path string, ic config.InfluxDBConfig, restart bool) error {
	if err := influxdb.WriteConfig(ic); err != nil {
		return err
	}
	c.Addons.InfluxDB[ic.Name] = ic
	if err := c.Save(path); err != nil {
		return err
	}
	fmt.Printf("  [OK] wrote %s\n", ic.ConfigPath)
	fmt.Printf("  [OK] overrides stored in %s\n", path)
	m, err := podman.NewInfluxDBManager(c)
	if err != nil {
		return err
	}
	running, _ := m.ContainerRunning(ic.ContainerName)
	if !restart {
		if running {
			fmt.Printf("  [i] restart to apply: pg addon stop influxdb --name %s && pg addon start influxdb --name %s\n", ic.Name, ic.Name)
		}
		return nil
	}
	if !running {
		return nil
	}
	if _, err := m.Stop(ic.ContainerName); err != nil {
		return fmt.Errorf("stopping influxdb %q: %w", ic.Name, err)
	}
	if _, err := m.StartContainer(&ic); err != nil {
		m.LogsTail(ic.ContainerName, 20)
		return err
	}
	fmt.Printf("✓ influxdb %q restarted with the new config\n", ic.Name)
	return nil
}

func runAddonInfluxdbConfigList(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "influxdb"
	}
	_, _, ic, err := resolveInfluxDBInstance(name)
	if err != nil {
		return err
	}
	fmt.Printf("influxdb %q config: %s\n", name, ic.ConfigPath)
	if len(ic.ExtraConfig) == 0 {
		fmt.Println("  (no overrides — only pgcli-managed paths/listen/auth differ from upstream defaults)")
		return nil
	}
	for _, opt := range ic.ExtraConfig {
		fmt.Printf("  %s\n", opt)
	}
	return nil
}

func runAddonInfluxdbConfigSet(cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "influxdb"
	}
	c, path, ic, err := resolveInfluxDBInstance(name)
	if err != nil {
		return err
	}
	restart, _ := cmd.Flags().GetBool("restart")

	entries := append([]string(nil), ic.ExtraConfig...)
	for _, opt := range args {
		section, key, _, err := config.ParseInfluxDBConfigOption(opt)
		if err != nil {
			return err
		}
		replaced := false
		for i, existing := range entries {
			es, ek, _, err := config.ParseInfluxDBConfigOption(existing)
			if err != nil {
				continue
			}
			if es == section && ek == key {
				entries[i] = opt
				replaced = true
				break
			}
		}
		if !replaced {
			entries = append(entries, opt)
		}
	}
	if err := config.ValidateInfluxDBExtraConfig(entries); err != nil {
		return err
	}
	ic.ExtraConfig = entries
	return persistInfluxDBConfig(c, path, ic, restart)
}

func runAddonInfluxdbConfigUnset(cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "influxdb"
	}
	c, path, ic, err := resolveInfluxDBInstance(name)
	if err != nil {
		return err
	}
	restart, _ := cmd.Flags().GetBool("restart")

	entries := append([]string(nil), ic.ExtraConfig...)
	for _, keyArg := range args {
		// Parse the key form by borrowing the parse with a placeholder value.
		section, key, _, err := config.ParseInfluxDBConfigOption(keyArg + "=x")
		if err != nil {
			return fmt.Errorf("invalid config key %q: %w", keyArg, err)
		}
		kept := entries[:0]
		found := false
		for _, existing := range entries {
			es, ek, _, err := config.ParseInfluxDBConfigOption(existing)
			if err != nil {
				kept = append(kept, existing)
				continue
			}
			if es == section && ek == key {
				found = true
				continue
			}
			kept = append(kept, existing)
		}
		if !found {
			return fmt.Errorf("config key %q is not overridden", keyArg)
		}
		entries = kept
	}
	ic.ExtraConfig = entries
	return persistInfluxDBConfig(c, path, ic, restart)
}

var addonInfluxdbCmd = &cobra.Command{
	Use:   "influxdb",
	Short: "Manage InfluxDB 1.x container instances",
	Long: `Manage InfluxDB 1.x add-on instances (Linux/amd64 only).

InfluxDB 1.x is a single-node time-series database with TSM storage and
InfluxQL. pgcli runs influxd from the pgcli-influxdb image (the official
1.13.1 release packaged on Alpine) and generates a minimal config mount that
keeps every other upstream default. The container uses host networking so the
configured HTTP port and the loopback-only backup/restore RPC behave exactly as
a native install.

Authentication is always enabled. The admin password is generated at install,
stored in pg.yaml and retrievable with "pg addon password influxdb". HTTP
defaults to 0.0.0.0; the backup/restore RPC endpoint is loopback-only because
upstream has no authentication for it. For ad-hoc commands use "pg influx"
(local addon or any remote InfluxDB 1.x server).

Subcommands:
  status    Show container state, endpoints, version and paths
  shell     Launch the influx CLI in a temporary container (extra args after --)
  database  Create, list or drop databases
  retention Manage retention policies (list/create/alter/drop)
  backup    Online portable backup (instance must be running)
  restore   Online portable restore (starts the instance first if stopped)
  export    Offline line-protocol export via influx_inspect
  import    Import a line-protocol export via the influx CLI
  config    Persist arbitrary influxdb.conf overrides (list/set/unset)

Custom config parameters use KEY=VALUE (top-level) or SECTION.KEY=VALUE
(table) entries with raw TOML values, e.g.
  --config-option 'data.index-version="tsi1"'
  --config-option data.cache-max-memory-size=1000000000
Overrides are stored in pg.yaml AND written into the generated influxdb.conf,
so they survive restarts; "pg addon influxdb config set|unset" changes them on
a live install (add --restart to apply immediately).

Examples:
  pg addon install influxdb --name metrics
  pg addon install influxdb --data-dir /srv/influx-data
  pg addon install influxdb --config-option 'data.index-version="tsi1"'
  pg addon influxdb config set --name metrics 'data.cache-max-memory-size=1000000000' --restart
  pg addon influxdb config unset --name metrics data.cache-max-memory-size
  pg influx --name metrics -- -execute "CREATE DATABASE demo"
  pg addon influxdb database create demo --name metrics
  pg addon influxdb database create demo --retention 30d --shard-duration 1d --rp rp_30d
  pg addon influxdb database drop old_demo --name metrics
  pg addon influxdb retention list --name metrics --database demo
  pg addon influxdb retention create --name metrics --database demo --rp rp_7d --duration 7d --default
  pg addon influxdb retention alter --name metrics --database demo --rp rp_7d --duration 14d
  pg addon influxdb retention drop --name metrics --database demo --rp rp_7d
  pg addon influxdb export --name metrics --database demo
  pg addon influxdb import /tmp/demo.txt --name metrics --database demo_copy
  pg addon influxdb backup --name metrics --database demo
  pg addon influxdb restore /var/backups/influx --name metrics --database demo --newdb demo_restored
  pg logs addon influxdb --name metrics -f
  pg addon remove influxdb --name metrics --clean-data`,
}

var addonInfluxdbStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show InfluxDB container state, endpoints and paths",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonInfluxdbStatus(cmd) },
}

var addonInfluxdbShellCmd = &cobra.Command{
	Use:   "shell [-- influx args...]",
	Short: "Launch the influx CLI for an instance",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonInfluxdbShell(cmd, args)
	},
}

var addonInfluxdbBackupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Run an online portable backup",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonInfluxdbBackup(cmd) },
}

var addonInfluxdbRestoreCmd = &cobra.Command{
	Use:   "restore BACKUP_DIR",
	Short: "Restore a portable backup (online; starts the instance if needed)",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonInfluxdbRestore,
}

var addonInfluxdbDatabaseCmd = &cobra.Command{
	Use:     "database",
	Aliases: []string{"db"},
	Short:   "Create, list and drop databases",
}

var addonInfluxdbDatabaseCreateCmd = &cobra.Command{
	Use:   "create NAME",
	Short: "Create a database (idempotent: an existing name is kept)",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonInfluxdbDatabaseCreate,
}

var addonInfluxdbDatabaseListCmd = &cobra.Command{
	Use:   "list",
	Short: "List databases (SHOW DATABASES)",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonInfluxdbDatabaseList(cmd, nil) },
}

var addonInfluxdbDatabaseDropCmd = &cobra.Command{
	Use:   "drop NAME",
	Short: "Drop a database and all of its data",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonInfluxdbDatabaseDrop,
}

var addonInfluxdbRetentionCmd = &cobra.Command{
	Use:     "retention",
	Aliases: []string{"rp"},
	Short:   "Manage retention policies",
}

var addonInfluxdbRetentionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List retention policies of a database",
	RunE:  runAddonInfluxdbRetentionList,
}

var addonInfluxdbRetentionCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a retention policy",
	RunE:  runAddonInfluxdbRetentionCreate,
}

var addonInfluxdbRetentionAlterCmd = &cobra.Command{
	Use:   "alter",
	Short: "Change the duration, shard duration or default flag of a policy",
	RunE:  runAddonInfluxdbRetentionAlter,
}

var addonInfluxdbRetentionDropCmd = &cobra.Command{
	Use:   "drop",
	Short: "Drop a retention policy and its data",
	RunE:  runAddonInfluxdbRetentionDrop,
}

var addonInfluxdbExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export line protocol offline with influx_inspect",
	Long: `Export the instance's data as line protocol (with DDL) using
influx_inspect. The tool reads the TSM files directly, so the container is
stopped for the export and restarted afterwards (unless --no-start). A
one-shot container reads the data directory read-only and writes the file
through a mount.

The output can be loaded with "pg addon influxdb import" or
"influx -import". Without --out the file is written below the instance
directory as exports/<database|all>-<UTC timestamp>.txt[.gz].`,
	RunE: func(cmd *cobra.Command, _ []string) error { return runAddonInfluxdbExport(cmd, nil) },
}

var addonInfluxdbImportCmd = &cobra.Command{
	Use:   "import FILE",
	Short: "Import a line-protocol export (instance must be running)",
	Long: `Import a line-protocol file with "influx -import". The instance must be
running; the file can come from "pg addon influxdb export" or influx_inspect.
Use --database to target a different database than the one in the file.`,
	Args: cobra.ExactArgs(1),
	RunE: runAddonInfluxdbImport,
}

var addonInfluxdbConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage persistent influxdb.conf overrides",
	Long: `Manage the config overrides stored in pg.yaml and rendered into the
generated influxdb.conf. Changing them here rewrites the file on disk, so the
values survive every restart; pass --restart to apply them immediately.

Entry syntax: KEY=VALUE for top-level settings, SECTION.KEY=VALUE for table
settings. Values are raw TOML — quote strings yourself:
  pg addon influxdb config set 'data.index-version="tsi1"'
  pg addon influxdb config set data.cache-max-memory-size=1000000000
Settings pgcli manages (paths, listen addresses, auth, reporting) cannot be
overridden.`,
}

var addonInfluxdbConfigListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the stored config overrides",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonInfluxdbConfigList(cmd) },
}

var addonInfluxdbConfigSetCmd = &cobra.Command{
	Use:   "set KEY=VALUE...",
	Short: "Add or replace config overrides and rewrite influxdb.conf",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runAddonInfluxdbConfigSet,
}

var addonInfluxdbConfigUnsetCmd = &cobra.Command{
	Use:   "unset SECTION.KEY...",
	Short: "Remove config overrides and rewrite influxdb.conf",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runAddonInfluxdbConfigUnset,
}

func init() {
	addonCmd.AddCommand(addonInfluxdbCmd)
	addonInfluxdbCmd.AddCommand(addonInfluxdbStatusCmd, addonInfluxdbShellCmd, addonInfluxdbDatabaseCmd, addonInfluxdbRetentionCmd, addonInfluxdbBackupCmd, addonInfluxdbRestoreCmd, addonInfluxdbExportCmd, addonInfluxdbImportCmd, addonInfluxdbConfigCmd)
	addonInfluxdbDatabaseCmd.AddCommand(addonInfluxdbDatabaseCreateCmd, addonInfluxdbDatabaseListCmd, addonInfluxdbDatabaseDropCmd)
	addonInfluxdbRetentionCmd.AddCommand(addonInfluxdbRetentionListCmd, addonInfluxdbRetentionCreateCmd, addonInfluxdbRetentionAlterCmd, addonInfluxdbRetentionDropCmd)
	addonInfluxdbConfigCmd.AddCommand(addonInfluxdbConfigListCmd, addonInfluxdbConfigSetCmd, addonInfluxdbConfigUnsetCmd)
	for _, sub := range []*cobra.Command{addonInfluxdbStatusCmd, addonInfluxdbShellCmd, addonInfluxdbDatabaseCreateCmd, addonInfluxdbDatabaseListCmd, addonInfluxdbDatabaseDropCmd, addonInfluxdbRetentionListCmd, addonInfluxdbRetentionCreateCmd, addonInfluxdbRetentionAlterCmd, addonInfluxdbRetentionDropCmd, addonInfluxdbBackupCmd, addonInfluxdbRestoreCmd, addonInfluxdbExportCmd, addonInfluxdbImportCmd, addonInfluxdbConfigListCmd, addonInfluxdbConfigSetCmd, addonInfluxdbConfigUnsetCmd} {
		sub.Flags().String("name", "", "influxdb instance name (default \"influxdb\")")
	}
	for _, sub := range []*cobra.Command{addonInfluxdbConfigSetCmd, addonInfluxdbConfigUnsetCmd} {
		sub.Flags().Bool("restart", false, "restart the instance after rewriting the config")
	}
	addonInfluxdbDatabaseCreateCmd.Flags().String("retention", "", "create a bounded default retention policy with this duration (e.g. 30d)")
	addonInfluxdbDatabaseCreateCmd.Flags().String("shard-duration", "", "shard group duration for --retention (e.g. 1d)")
	addonInfluxdbDatabaseCreateCmd.Flags().String("rp", "", "retention policy name for --retention (default \"autogen\")")
	addonInfluxdbDatabaseDropCmd.Flags().Bool("yes", false, "skip the confirmation prompt")
	for _, sub := range []*cobra.Command{addonInfluxdbRetentionListCmd, addonInfluxdbRetentionCreateCmd, addonInfluxdbRetentionAlterCmd, addonInfluxdbRetentionDropCmd} {
		sub.Flags().String("database", "", "database name (required)")
	}
	for _, sub := range []*cobra.Command{addonInfluxdbRetentionCreateCmd, addonInfluxdbRetentionAlterCmd, addonInfluxdbRetentionDropCmd} {
		sub.Flags().String("rp", "", "retention policy name (required)")
	}
	for _, sub := range []*cobra.Command{addonInfluxdbRetentionCreateCmd, addonInfluxdbRetentionAlterCmd} {
		sub.Flags().String("duration", "", "retention duration (e.g. 30d, 12h, INF)")
		sub.Flags().String("shard-duration", "", "shard group duration (e.g. 1d)")
		sub.Flags().Bool("default", false, "make this the default retention policy")
	}
	addonInfluxdbRetentionDropCmd.Flags().Bool("yes", false, "skip the confirmation prompt")
	addonInfluxdbShellCmd.Flags().String("database", "", "database to select in the CLI (-database)")
	addonInfluxdbExportCmd.Flags().String("database", "", "export a single database (-database)")
	addonInfluxdbExportCmd.Flags().String("out", "", "export file (default <instance dir>/exports/<database|all>-<UTC timestamp>.txt[.gz])")
	addonInfluxdbExportCmd.Flags().Bool("compress", false, "gzip the export file (-compress)")
	addonInfluxdbExportCmd.Flags().String("start", "", "start of the time range (-start; requires --end)")
	addonInfluxdbExportCmd.Flags().String("end", "", "end of the time range (-end; requires --start)")
	addonInfluxdbExportCmd.Flags().Bool("no-start", false, "leave the instance stopped after the export")
	addonInfluxdbImportCmd.Flags().String("database", "", "target database for the import (-database)")
	addonInfluxdbImportCmd.Flags().String("precision", "", "timestamp precision for the import (-precision: ns, u, ms, s, m, h)")
	addonInfluxdbBackupCmd.Flags().String("out", "", "backup output directory (default <instance dir>/backups/<UTC timestamp>)")
	addonInfluxdbBackupCmd.Flags().String("database", "", "back up a single database (-db)")
	addonInfluxdbBackupCmd.Flags().String("since", "", "back up data since this RFC3339 timestamp (-since)")
	addonInfluxdbBackupCmd.Flags().String("start", "", "start of an RFC3339 time range (-start; requires --end)")
	addonInfluxdbBackupCmd.Flags().String("end", "", "end of the RFC3339 time range (-end; requires --start)")
	addonInfluxdbRestoreCmd.Flags().String("database", "", "restore a single database (-db)")
	addonInfluxdbRestoreCmd.Flags().String("newdb", "", "restore under a new database name (-newdb; requires --database)")

	// InfluxDB-specific install flags (--name/--data-dir/--listen/--http-port/
	// --admin-user/--version/--force are registered globally).
	addonInstallCmd.Flags().Int("rpc-port", 0, "influxdb: backup/restore RPC host port, loopback only (0=auto-assign from 8088)")
	addonInstallCmd.Flags().String("admin-password", "", "influxdb/clickhouse: admin password (generated on first install if omitted; on --force the stored password is kept unless this is set)")
	addonInstallCmd.Flags().StringArray("config-option", nil, "influxdb: extra influxdb.conf setting KEY=VALUE (top-level) or SECTION.KEY=VALUE (table) with a raw TOML value, e.g. 'data.index-version=\"tsi1\"' (repeatable; replaces the stored override list, persisted in pg.yaml and rendered into the config file); clickhouse: extra server setting as a dotted XML path KEY=VALUE, e.g. 'logger.level=information' or 'max_server_memory_usage=2000000000' (repeatable; replaces the stored override list, persisted in pg.yaml and merged into the rendered config.d XML); kafka: extra server.properties setting KEY=VALUE, e.g. 'num.partitions=3' (repeatable; replaces the stored override list; pgcli-managed keys such as listeners and log.dirs are rejected)")
}
