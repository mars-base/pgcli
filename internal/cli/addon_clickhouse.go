package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/clickhouse"
	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/podman"
)

// resolveClickHouseInstance loads the config, applies defaults and returns the
// named instance.
func resolveClickHouseInstance(name string) (*config.Config, string, config.ClickHouseConfig, error) {
	c, path, err := loadAddonCfg()
	if err != nil {
		return nil, "", config.ClickHouseConfig{}, err
	}
	c.ApplyDefaults()
	cc, ok := c.Addons.ClickHouse[name]
	if !ok {
		return nil, "", config.ClickHouseConfig{}, fmt.Errorf("clickhouse %q not found (see 'pg addon list')", name)
	}
	return c, path, cc, nil
}

func clickHouseFlagName(cmd *cobra.Command) string {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "clickhouse"
	}
	return name
}

// clickHouseAdminPassword reuses a kept secret (same-name reinstall after
// `remove` without --clean-data), otherwise generates a fresh password.
func clickHouseAdminPassword(secretPath string) (string, error) {
	data, err := os.ReadFile(secretPath)
	switch {
	case err == nil:
		if pw := strings.TrimSpace(string(data)); pw != "" {
			return pw, nil
		}
	case !os.IsNotExist(err):
		return "", err
	}
	return generatePassword(20)
}

// --- install / lifecycle ----------------------------------------------------

func runAddonInstallClickHouse(cmd *cobra.Command) error {
	name := clickHouseFlagName(cmd)
	if !config.ValidClickHouseName(name) {
		return fmt.Errorf("invalid ClickHouse instance name %q: use letters, digits, '_' and '-', starting with a letter or digit (max 48 chars)", name)
	}
	fmt.Printf("=== pg addon install clickhouse (%s) ===\n", name)

	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if c.Addons.ClickHouse == nil {
		c.Addons.ClickHouse = make(map[string]config.ClickHouseConfig)
	}
	cc, present := c.Addons.ClickHouse[name]
	force, _ := cmd.Flags().GetBool("force")
	if present && !force {
		c.ApplyDefaults()
		cc = c.Addons.ClickHouse[name]
		state := "stopped"
		if m, err := podman.NewClickHouseManager(c); err == nil {
			if running, _ := m.ContainerRunning(cc.ContainerName); running {
				state = "running"
			}
		}
		fmt.Printf("✓ clickhouse %q already installed (%s, HTTP %s:%d, TCP %s:%d, admin %s) — re-install with --force to rewrite the config and recreate the container\n",
			name, state, cc.Listen, cc.HTTPPort, cc.Listen, cc.TCPPort, cc.AdminUser)
		fmt.Printf("  Password: pg addon password clickhouse --name %s\n", name)
		return nil
	}
	if !present {
		cc = config.ClickHouseConfig{Name: name}
	}
	cc.Name = name

	fs := cmd.Flags()
	if fs.Changed("version") {
		cc.Version, _ = fs.GetString("version")
	}
	if cc.Version == "" {
		cc.Version = config.DefaultClickHouseVersion
	}
	imageTag, ok := config.ClickHouseImageTagForVersion(cc.Version)
	if !ok {
		return fmt.Errorf("ClickHouse %q is not available (supported: %s; pass the exact upstream tag)", cc.Version, config.DefaultClickHouseVersion)
	}
	cc.ImageTag = imageTag
	if v, _ := fs.GetString("admin-password"); v != "" {
		cc.AdminPassword = v
	}
	if v, _ := fs.GetString("admin-user"); v != "" {
		cc.AdminUser = v
	}
	if v, _ := fs.GetString("listen"); v != "" {
		cc.Listen = v
	}
	if fs.Changed("http-port") {
		cc.HTTPPort, _ = fs.GetInt("http-port")
	}
	if fs.Changed("tcp-port") {
		cc.TCPPort, _ = fs.GetInt("tcp-port")
	}
	if v, _ := fs.GetString("data-dir"); v != "" {
		cc.DataDir = v
	}
	if fs.Changed("config-option") {
		options, _ := fs.GetStringArray("config-option")
		settings := make(map[string]string, len(options))
		for _, opt := range options {
			key, value, ok := strings.Cut(opt, "=")
			if !ok || strings.TrimSpace(key) == "" {
				return fmt.Errorf("invalid --config-option %q: use KEY=VALUE with a dotted XML path, e.g. max_server_memory_usage=2147483648", opt)
			}
			settings[strings.TrimSpace(key)] = value
		}
		cc.Settings = settings
	}

	c.Addons.ClickHouse[name] = cc
	c.ApplyDefaults()
	cc = c.Addons.ClickHouse[name]
	if cc.AdminPassword == "" {
		pw, err := clickHouseAdminPassword(cc.SecretPath)
		if err != nil {
			return fmt.Errorf("resolving admin password: %w", err)
		}
		cc.AdminPassword = pw
		c.Addons.ClickHouse[name] = cc
	}
	if err := c.Validate(); err != nil {
		return err
	}

	m, err := podman.NewClickHouseManager(c)
	if err != nil {
		return err
	}
	if err := m.EnsureImage(cc.ImageTag); err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] image: %s\n", cc.ImageTag)
	if err := clickhouse.WriteConfig(cc); err != nil {
		return fmt.Errorf("  [X] writing config: %w", err)
	}
	if err := clickhouse.WriteSecret(cc); err != nil {
		return fmt.Errorf("  [X] writing password file: %w", err)
	}
	fmt.Printf("  [OK] config:  %s\n", cc.ConfigPath)
	fmt.Printf("  [OK] data:    %s\n", cc.DataDir)

	// Persist before starting so a failed boot is still inspectable and the
	// generated admin password is not lost.
	c.Addons.ClickHouse[name] = cc
	if err := c.Save(path); err != nil {
		return fmt.Errorf("  [X] saving config: %w", err)
	}

	if err := m.EnsureContainer(&cc); err != nil {
		m.LogsTail(cc.ContainerName, 20)
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] container: %s\n", cc.ContainerName)
	fmt.Printf("  [OK] HTTP:      %s\n", clickhouse.HTTPURL(cc))
	fmt.Printf("  [OK] TCP:       %s\n", clickhouse.TCPAddr(cc))
	if err := m.ExecClient(&cc, clickhouse.ClientArgs(cc, ""), "SELECT version()", false); err != nil {
		m.LogsTail(cc.ContainerName, 20)
		return fmt.Errorf("  [X] admin login failed: %w", err)
	}

	fmt.Println()
	fmt.Printf("✓ ClickHouse %q installed\n", name)
	fmt.Printf("  Version:   %s\n", cc.Version)
	fmt.Printf("  Image:     %s\n", cc.ImageTag)
	fmt.Printf("  Container: %s\n", cc.ContainerName)
	fmt.Printf("  HTTP:      %s\n", clickhouse.HTTPURL(cc))
	fmt.Printf("  TCP:       %s\n", clickhouse.TCPAddr(cc))
	fmt.Printf("  Admin:     %s\n", cc.AdminUser)
	fmt.Printf("  Password:  %s\n", cc.AdminPassword)
	fmt.Printf("  Data dir:  %s\n", cc.DataDir)
	fmt.Printf("  Backups:   %s\n", cc.BackupDir)
	fmt.Printf("  Config:    %s\n", cc.ConfigPath)
	fmt.Println()
	fmt.Printf("  Password: pg addon password clickhouse --name %s\n", name)
	fmt.Printf("  Status:   pg addon clickhouse status --name %s\n", name)
	fmt.Printf("  Query:    pg addon clickhouse query --name %s 'SELECT 1'\n", name)
	fmt.Printf("  Shell:    pg addon clickhouse shell --name %s\n", name)
	return nil
}

func runAddonRemoveClickHouse(cmd *cobra.Command) error {
	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	name := clickHouseFlagName(cmd)
	cleanData, _ := cmd.Flags().GetBool("clean-data")
	c.ApplyDefaults()
	cc, ok := c.Addons.ClickHouse[name]
	if !ok {
		return fmt.Errorf("clickhouse %q not found (see 'pg addon list')", name)
	}
	fmt.Printf("=== pg addon remove clickhouse (%s) ===\n", name)

	m, err := podman.NewClickHouseManager(c)
	if err != nil {
		return err
	}
	if err := m.RemoveContainer(&cc); err != nil {
		return err
	}
	delete(c.Addons.ClickHouse, name)
	if len(c.Addons.ClickHouse) == 0 {
		c.Addons.ClickHouse = nil
	}
	if err := c.Save(path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Printf("  [OK] config entry removed (%s)\n", path)

	if cleanData {
		if err := m.CleanData(&cc); err != nil {
			return err
		}
	} else {
		fmt.Printf("  [i] kept %s (data, config, logs, backups) — pass --clean-data to drop\n", filepath.Dir(filepath.Dir(cc.ConfigPath)))
	}
	fmt.Printf("✓ ClickHouse %q removed\n", name)
	return nil
}

func runAddonStartClickHouse(cmd *cobra.Command) error {
	name := clickHouseFlagName(cmd)
	c, _, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewClickHouseManager(c)
	if err != nil {
		return err
	}
	already, err := m.StartContainer(&cc)
	if err != nil {
		m.LogsTail(cc.ContainerName, 20)
		return err
	}
	if already {
		fmt.Printf("✓ clickhouse %q already running (HTTP %s, TCP %s)\n", name, clickhouse.HTTPURL(cc), clickhouse.TCPAddr(cc))
		return nil
	}
	fmt.Printf("✓ clickhouse %q started (HTTP %s, TCP %s)\n", name, clickhouse.HTTPURL(cc), clickhouse.TCPAddr(cc))
	return nil
}

func runAddonStopClickHouse(cmd *cobra.Command) error {
	name := clickHouseFlagName(cmd)
	c, _, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewClickHouseManager(c)
	if err != nil {
		return err
	}
	wasRunning, err := m.Stop(cc.ContainerName)
	if err != nil {
		return err
	}
	if !wasRunning {
		fmt.Printf("✓ clickhouse %q already stopped\n", name)
		return nil
	}
	fmt.Printf("✓ clickhouse %q stopped\n", name)
	return nil
}

// requireClickHouseRunning returns a manager for a running instance.
func requireClickHouseRunning(cmd *cobra.Command) (*podman.ClickHouseManager, config.ClickHouseConfig, error) {
	name := clickHouseFlagName(cmd)
	c, _, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return nil, cc, err
	}
	m, err := podman.NewClickHouseManager(c)
	if err != nil {
		return nil, cc, err
	}
	if running, _ := m.ContainerRunning(cc.ContainerName); !running {
		return nil, cc, fmt.Errorf("clickhouse %q is not running — 'pg addon start clickhouse --name %s' first", name, name)
	}
	return m, cc, nil
}

// --- status / shell / query ------------------------------------------------

func runAddonClickHouseStatus(cmd *cobra.Command) error {
	name := clickHouseFlagName(cmd)
	c, _, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewClickHouseManager(c)
	if err != nil {
		return err
	}
	state := "stopped"
	if running, _ := m.ContainerRunning(cc.ContainerName); running {
		if err := clickhouse.Ping(cc, 2*time.Second); err != nil {
			state = fmt.Sprintf("running (ping failed: %v)", err)
		} else {
			state = "running (ping ok)"
		}
	}
	fmt.Printf("clickhouse %q — %s\n", name, state)
	fmt.Printf("  Version:    %s\n", cc.Version)
	fmt.Printf("  Image:      %s\n", cc.ImageTag)
	fmt.Printf("  Container:  %s\n", cc.ContainerName)
	fmt.Printf("  HTTP:       %s\n", clickhouse.HTTPURL(cc))
	fmt.Printf("  TCP:        %s\n", clickhouse.TCPAddr(cc))
	fmt.Printf("  Listen:     %s\n", cc.Listen)
	fmt.Printf("  Admin user: %s\n", cc.AdminUser)
	fmt.Printf("  Data dir:   %s\n", cc.DataDir)
	fmt.Printf("  Logs dir:   %s\n", cc.LogDir)
	fmt.Printf("  Backups:    %s\n", cc.BackupDir)
	fmt.Printf("  Config:     %s\n", cc.ConfigPath)
	fmt.Printf("  Settings:   %d override(s)\n", len(cc.Settings))
	fmt.Printf("  Logs:       pg logs addon clickhouse --name %s\n", name)
	return nil
}

func runAddonClickHouseShell(cmd *cobra.Command) error {
	name := clickHouseFlagName(cmd)
	c, _, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewClickHouseManager(c)
	if err != nil {
		return err
	}
	if running, _ := m.ContainerRunning(cc.ContainerName); !running {
		fmt.Fprintf(os.Stderr, "[!] clickhouse %q is not running — start it with: pg addon start clickhouse --name %s\n", name, name)
	}
	return m.ExecShell(&cc)
}

func runAddonClickHouseQuery(cmd *cobra.Command, args []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	db, _ := cmd.Flags().GetString("database")
	file, _ := cmd.Flags().GetString("file")

	var sql string
	switch {
	case file != "" || (len(args) == 1 && args[0] == "-"):
		if file == "" {
			file = "-"
		}
		if file == "-" {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("reading SQL from stdin: %w", err)
			}
			sql = string(data)
		} else {
			data, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("reading SQL file %s: %w", file, err)
			}
			sql = string(data)
		}
	case len(args) > 0:
		sql = strings.Join(args, " ")
	default:
		return fmt.Errorf("pass SQL as arguments, with --file FILE, or pipe it on stdin (SQL... | ... -)")
	}
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("empty SQL")
	}
	return m.ExecClient(&cc, clickhouse.ClientArgs(cc, db), sql, false)
}

// --- database / user / grant ----------------------------------------------

func runAddonClickHouseDatabaseCreate(cmd *cobra.Command, args []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	engine, _ := cmd.Flags().GetString("engine")
	comment, _ := cmd.Flags().GetString("comment")
	ifNotExists, _ := cmd.Flags().GetBool("if-not-exists")
	sql, err := clickhouse.CreateDatabaseSQL(args[0], engine, comment, ifNotExists)
	if err != nil {
		return err
	}
	if err := m.ExecClient(&cc, clickhouse.ClientArgs(cc, ""), sql, false); err != nil {
		return err
	}
	fmt.Printf("✓ database %q ready on clickhouse %q\n", args[0], cc.Name)
	return nil
}

func runAddonClickHouseDatabaseList(cmd *cobra.Command, _ []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	out, err := m.ExecClientOutput(&cc, clickhouse.ClientArgs(cc, ""), clickhouse.ListDatabasesSQL())
	if err != nil {
		return err
	}
	fmt.Print(string(out))
	return nil
}

func runAddonClickHouseDatabaseDrop(cmd *cobra.Command, args []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	ifExists, _ := cmd.Flags().GetBool("if-exists")
	yes, _ := cmd.Flags().GetBool("yes")
	if !yes && !confirmPrompt(fmt.Sprintf("Drop database %q on clickhouse %q? All of its data is deleted. [y/N] ", args[0], cc.Name)) {
		fmt.Println("aborted")
		return nil
	}
	sql, err := clickhouse.DropDatabaseSQL(args[0], ifExists)
	if err != nil {
		return err
	}
	if err := m.ExecClient(&cc, clickhouse.ClientArgs(cc, ""), sql, false); err != nil {
		return err
	}
	fmt.Printf("✓ database %q dropped\n", args[0])
	return nil
}

func clickHouseHostClauses(cmd *cobra.Command) ([]string, error) {
	anyHost, _ := cmd.Flags().GetBool("host-any")
	local, _ := cmd.Flags().GetBool("host-local")
	ips, _ := cmd.Flags().GetStringArray("host-ip")
	names, _ := cmd.Flags().GetStringArray("host-name")

	var clauses []string
	if anyHost || (len(ips) == 0 && len(names) == 0 && !local) {
		clauses = append(clauses, "ANY")
	}
	if local {
		clauses = append(clauses, "LOCAL")
	}
	for _, value := range ips {
		clause, err := clickhouse.HostIPClause(value)
		if err != nil {
			return nil, err
		}
		clauses = append(clauses, clause)
	}
	for _, value := range names {
		clause, err := clickhouse.HostNameClause(value)
		if err != nil {
			return nil, err
		}
		clauses = append(clauses, clause)
	}
	return clauses, nil
}

func runAddonClickHouseUserCreate(cmd *cobra.Command, args []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	password, _ := cmd.Flags().GetString("password")
	generated := false
	if password == "" {
		password, err = generatePassword(20)
		if err != nil {
			return fmt.Errorf("generating password: %w", err)
		}
		generated = true
	}
	defaultDB, _ := cmd.Flags().GetString("default-database")
	readonly, _ := cmd.Flags().GetBool("readonly")
	ifNotExists, _ := cmd.Flags().GetBool("if-not-exists")
	clauses, err := clickHouseHostClauses(cmd)
	if err != nil {
		return err
	}
	sql, err := clickhouse.CreateUserSQL(args[0], password, defaultDB, clauses, readonly, ifNotExists)
	if err != nil {
		return err
	}
	if err := m.ExecClient(&cc, clickhouse.ClientArgs(cc, ""), sql, false); err != nil {
		return err
	}
	fmt.Printf("✓ user %q ready on clickhouse %q\n", args[0], cc.Name)
	if generated {
		fmt.Printf("  Password: %s\n", password)
	}
	return nil
}

func runAddonClickHouseUserList(cmd *cobra.Command, _ []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	out, err := m.ExecClientOutput(&cc, clickhouse.ClientArgs(cc, ""), clickhouse.ListUsersSQL())
	if err != nil {
		return err
	}
	fmt.Print(string(out))
	return nil
}

func runAddonClickHouseUserDrop(cmd *cobra.Command, args []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	ifExists, _ := cmd.Flags().GetBool("if-exists")
	yes, _ := cmd.Flags().GetBool("yes")
	if !yes && !confirmPrompt(fmt.Sprintf("Drop user %q on clickhouse %q? [y/N] ", args[0], cc.Name)) {
		fmt.Println("aborted")
		return nil
	}
	sql, err := clickhouse.DropUserSQL(args[0], ifExists)
	if err != nil {
		return err
	}
	if err := m.ExecClient(&cc, clickhouse.ClientArgs(cc, ""), sql, false); err != nil {
		return err
	}
	fmt.Printf("✓ user %q dropped\n", args[0])
	return nil
}

func clickHouseGrantPrivileges(cmd *cobra.Command) ([]string, error) {
	var privileges []string
	add := func(p string) {
		for _, existing := range privileges {
			if existing == p {
				return
			}
		}
		privileges = append(privileges, p)
	}
	if read, _ := cmd.Flags().GetBool("read"); read {
		add("SELECT")
	}
	if write, _ := cmd.Flags().GetBool("write"); write {
		add("INSERT")
	}
	if ddl, _ := cmd.Flags().GetBool("ddl"); ddl {
		for _, p := range []string{"CREATE", "ALTER", "DROP", "TRUNCATE", "OPTIMIZE"} {
			add(p)
		}
	}
	if all, _ := cmd.Flags().GetBool("all"); all {
		add("ALL")
	}
	if raw, _ := cmd.Flags().GetString("privileges"); raw != "" {
		parsed, err := clickhouse.NormalizePrivileges(raw)
		if err != nil {
			return nil, err
		}
		for _, p := range parsed {
			add(p)
		}
	}
	if len(privileges) == 0 {
		return nil, fmt.Errorf("no privileges selected: pass --read, --write, --ddl, --all and/or --privileges")
	}
	return privileges, nil
}

func runAddonClickHouseGrant(cmd *cobra.Command, args []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	privileges, err := clickHouseGrantPrivileges(cmd)
	if err != nil {
		return err
	}
	database, _ := cmd.Flags().GetString("database")
	table, _ := cmd.Flags().GetString("table")
	revoke, _ := cmd.Flags().GetBool("revoke")
	withGrantOption, _ := cmd.Flags().GetBool("with-grant-option")
	sql, err := clickhouse.GrantSQL(args[0], privileges, database, table, revoke, withGrantOption)
	if err != nil {
		return err
	}
	if err := m.ExecClient(&cc, clickhouse.ClientArgs(cc, ""), sql, false); err != nil {
		return err
	}
	verb := "granted to"
	if revoke {
		verb = "revoked from"
	}
	fmt.Printf("✓ %s %s user %q\n", strings.Join(privileges, ", "), verb, args[0])
	return nil
}

func runAddonClickHouseGrantList(cmd *cobra.Command, args []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	sql, err := clickhouse.ShowGrantsSQL(args[0])
	if err != nil {
		return err
	}
	out, err := m.ExecClientOutput(&cc, clickhouse.ClientArgs(cc, ""), sql)
	if err != nil {
		return err
	}
	fmt.Print(string(out))
	return nil
}

// --- backup / restore ------------------------------------------------------

func runAddonClickHouseBackup(cmd *cobra.Command) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	database, _ := cmd.Flags().GetString("database")
	if database == "" {
		return fmt.Errorf("--database is required")
	}
	out, _ := cmd.Flags().GetString("out")
	diskPath, hostPath, err := clickhouse.BackupTarget(cc.BackupDir, database, out, time.Now())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cc.BackupDir, 0o755); err != nil {
		return fmt.Errorf("creating backup dir %s: %w", cc.BackupDir, err)
	}
	sql, err := clickhouse.BackupDatabaseSQL(database, diskPath)
	if err != nil {
		return err
	}
	fmt.Printf("-> Backing up database %q to %s...\n", database, hostPath)
	if err := m.ExecClient(&cc, clickhouse.ClientArgs(cc, ""), sql, false); err != nil {
		return err
	}
	if info, err := os.Stat(hostPath); err == nil {
		fmt.Printf("✓ backup written: %s (%d bytes)\n", hostPath, info.Size())
	} else {
		fmt.Printf("✓ backup reported complete: %s\n", hostPath)
	}
	return nil
}

func runAddonClickHouseRestore(cmd *cobra.Command, args []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	database, _ := cmd.Flags().GetString("database")
	if database == "" {
		return fmt.Errorf("--database is required (the database name recorded in the backup)")
	}
	newName, _ := cmd.Flags().GetString("as")
	diskPath, hostPath, err := clickhouse.RestoreSource(cc.BackupDir, args[0])
	if err != nil {
		return err
	}
	sql, err := clickhouse.RestoreDatabaseSQL(database, newName, diskPath)
	if err != nil {
		return err
	}
	fmt.Printf("-> Restoring %s...\n", hostPath)
	if err := m.ExecClient(&cc, clickhouse.ClientArgs(cc, ""), sql, false); err != nil {
		return err
	}
	target := database
	if newName != "" {
		target = newName
	}
	fmt.Printf("✓ database %q restored on clickhouse %q\n", target, cc.Name)
	return nil
}

func runAddonClickHouseBackupList(cmd *cobra.Command) error {
	name := clickHouseFlagName(cmd)
	_, _, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	files, err := clickhouse.ListBackupFiles(cc.BackupDir)
	if err != nil {
		return fmt.Errorf("listing %s: %w", cc.BackupDir, err)
	}
	if len(files) == 0 {
		fmt.Printf("no backups in %s\n", cc.BackupDir)
		return nil
	}
	for _, file := range files {
		info, err := os.Stat(filepath.Join(cc.BackupDir, file))
		if err != nil {
			fmt.Printf("  %s\n", file)
			continue
		}
		fmt.Printf("  %-48s %10d bytes  %s\n", file, info.Size(), info.ModTime().UTC().Format("2006-01-02 15:04:05 UTC"))
	}
	return nil
}

// --- config ----------------------------------------------------------------

func clickHouseManagedSetting(cc config.ClickHouseConfig, key string) (string, bool) {
	switch key {
	case "listen_host":
		return cc.Listen, true
	case "listen_try":
		return "1", true
	case "http_port":
		return fmt.Sprint(cc.HTTPPort), true
	case "tcp_port":
		return fmt.Sprint(cc.TCPPort), true
	case "logger.level":
		if v, ok := cc.Settings["logger.level"]; ok && v != "" {
			return v, true
		}
		return "information", true
	case "logger.log":
		return clickhouse.ContainerLogDir + "/clickhouse-server.log", true
	case "logger.errorlog":
		return clickhouse.ContainerLogDir + "/clickhouse-server.err.log", true
	case "backups.allowed_disk":
		return "backups", true
	case "backups.allowed_path":
		return clickhouse.ContainerBackupDir + "/", true
	}
	return "", false
}

func runAddonClickHouseConfigList(cmd *cobra.Command) error {
	name := clickHouseFlagName(cmd)
	_, _, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	fmt.Printf("clickhouse %q effective managed settings:\n", name)
	for _, key := range []string{"listen_host", "http_port", "tcp_port", "logger.level", "logger.log", "logger.errorlog", "backups.allowed_disk", "backups.allowed_path"} {
		if value, ok := clickHouseManagedSetting(cc, key); ok {
			fmt.Printf("  %-24s %s\n", key, value)
		}
	}
	if len(cc.Settings) == 0 {
		fmt.Println("custom overrides: none")
		return nil
	}
	fmt.Println("custom overrides:")
	keys := make([]string, 0, len(cc.Settings))
	for key := range cc.Settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Printf("  %-24s %s\n", key, cc.Settings[key])
	}
	return nil
}

func runAddonClickHouseConfigGet(cmd *cobra.Command, args []string) error {
	name := clickHouseFlagName(cmd)
	_, _, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	key := args[0]
	if value, ok := cc.Settings[key]; ok {
		fmt.Println(value)
		return nil
	}
	if value, ok := clickHouseManagedSetting(cc, key); ok {
		fmt.Println(value)
		return nil
	}
	return fmt.Errorf("setting %q is not overridden (see 'pg addon clickhouse config list')", key)
}

// persistClickHouseConfig rewrites the generated XML, stores the entry and,
// when restart is true and the container is running, recreates it so the change
// takes effect immediately.
func persistClickHouseConfig(c *config.Config, path string, cc config.ClickHouseConfig, restart bool) error {
	if err := config.ValidateClickHouseSettings(cc.Settings); err != nil {
		return err
	}
	if err := clickhouse.WriteConfig(cc); err != nil {
		return err
	}
	c.Addons.ClickHouse[cc.Name] = cc
	if err := c.Validate(); err != nil {
		return err
	}
	if err := c.Save(path); err != nil {
		return err
	}
	fmt.Printf("  [OK] wrote %s\n", cc.ConfigPath)
	fmt.Printf("  [OK] stored in %s\n", path)

	m, err := podman.NewClickHouseManager(c)
	if err != nil {
		return err
	}
	running, _ := m.ContainerRunning(cc.ContainerName)
	if !restart {
		if running {
			fmt.Printf("  [i] takes effect on the next start: pg addon stop clickhouse --name %s && pg addon start clickhouse --name %s\n", cc.Name, cc.Name)
		}
		return nil
	}
	if !running {
		fmt.Printf("  [i] instance is stopped; the change applies on the next start\n")
		return nil
	}
	if _, err := m.Stop(cc.ContainerName); err != nil {
		return fmt.Errorf("stopping clickhouse %q: %w", cc.Name, err)
	}
	if _, err := m.StartContainer(&cc); err != nil {
		m.LogsTail(cc.ContainerName, 20)
		return err
	}
	fmt.Printf("✓ clickhouse %q restarted with the new config\n", cc.Name)
	return nil
}

func runAddonClickHouseConfigSet(cmd *cobra.Command, args []string) error {
	name := clickHouseFlagName(cmd)
	c, path, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	noRestart, _ := cmd.Flags().GetBool("no-restart")
	if cc.Settings == nil {
		cc.Settings = map[string]string{}
	}
	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return fmt.Errorf("invalid setting %q: use KEY=VALUE with a dotted XML path, e.g. max_server_memory_usage=2147483648", arg)
		}
		cc.Settings[strings.TrimSpace(key)] = value
	}
	if err := config.ValidateClickHouseSettings(cc.Settings); err != nil {
		return err
	}
	return persistClickHouseConfig(c, path, cc, !noRestart)
}

func runAddonClickHouseConfigUnset(cmd *cobra.Command, args []string) error {
	name := clickHouseFlagName(cmd)
	c, path, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	noRestart, _ := cmd.Flags().GetBool("no-restart")
	for _, key := range args {
		if _, ok := cc.Settings[key]; !ok {
			return fmt.Errorf("setting %q is not overridden", key)
		}
		delete(cc.Settings, key)
	}
	if len(cc.Settings) == 0 {
		cc.Settings = nil
	}
	return persistClickHouseConfig(c, path, cc, !noRestart)
}

func runAddonClickHouseConfigShow(cmd *cobra.Command) error {
	name := clickHouseFlagName(cmd)
	_, _, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	content, err := clickhouse.RenderConfig(cc)
	if err != nil {
		return err
	}
	fmt.Print(content)
	return nil
}

// --- command tree ----------------------------------------------------------

var addonClickHouseCmd = &cobra.Command{
	Use:   "clickhouse",
	Short: "Manage ClickHouse container instances",
	Long: `Manage ClickHouse add-on instances (official clickhouse-server image).

pgcli runs ClickHouse with host networking and a generated config.d override
that pins the HTTP/native ports, loopback binding, the log files and a local
"backups" disk. The admin user is created by the image entrypoint from
CLICKHOUSE_USER and a password file; "pg addon password clickhouse" reads it
back.

Subcommands:
  status    Show container state, endpoints, version and paths
  shell     Open a bash shell in the container
  query     Run SQL with clickhouse-client (args, --file or stdin)
  db        Create, list or drop databases
  user      Create, list or drop users
  grant     Grant/revoke privileges, or list a user's grants
  backup    BACKUP DATABASE to the managed backups disk
  restore   RESTORE DATABASE from a .zip in the backups disk
  backups   List local .zip backups
  config    Inspect, change and render server settings

Config settings use dotted XML paths, e.g.
  max_server_memory_usage=2147483648
  logger.level=debug
  query_log.flush_interval_milliseconds=1000
pgcli-managed keys (paths, listen/ports, auth, backups, user_directories) are
rejected. "config set" restarts the running container by default so the value
takes effect immediately; pass --no-restart to defer it.

Examples:
  pg addon install clickhouse --name ch1
  pg addon install clickhouse --listen 0.0.0.0 --http-port 8123 --tcp-port 9000
  pg addon clickhouse query --name ch1 'SELECT version()'
  pg addon clickhouse db create demo --name ch1
  pg addon clickhouse user create app --password secret --default-database demo
  pg addon clickhouse grant app --read --write --database demo
  pg addon clickhouse grant list app --name ch1
  pg addon clickhouse backup --name ch1 --database demo
  pg addon clickhouse restore demo-20261027-120000.zip --name ch1 --database demo --as demo_restored
  pg addon clickhouse config set max_server_memory_usage=2147483648 --name ch1
  pg addon clickhouse config show --name ch1
  pg logs addon clickhouse --name ch1 -f
  pg addon remove clickhouse --name ch1 --clean-data`,
}

var addonClickHouseStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show ClickHouse container state, endpoints and paths",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonClickHouseStatus(cmd) },
}

var addonClickHouseShellCmd = &cobra.Command{
	Use:   "shell",
	Short: "Open a bash shell in the ClickHouse container",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonClickHouseShell(cmd) },
}

var addonClickHouseQueryCmd = &cobra.Command{
	Use:   "query [SQL...]",
	Short: "Run SQL with clickhouse-client (args, --file or stdin)",
	Long: `Run a SQL statement with clickhouse-client inside the container.

Pass the statement as arguments, read it from --file, or pipe it on stdin and
pass "-". Query output is written to stdout, so it can be redirected or piped.
The client authenticates as the admin user with a password read from the
environment, never from argv.`,
	Args: cobra.ArbitraryArgs,
	RunE: runAddonClickHouseQuery,
}

var addonClickHouseDatabaseCmd = &cobra.Command{
	Use:     "database",
	Aliases: []string{"db"},
	Short:   "Create, list and drop databases",
}

var addonClickHouseDatabaseCreateCmd = &cobra.Command{
	Use:   "create NAME",
	Short: "Create a database",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonClickHouseDatabaseCreate,
}

var addonClickHouseDatabaseListCmd = &cobra.Command{
	Use:   "list",
	Short: "List databases",
	RunE:  runAddonClickHouseDatabaseList,
}

var addonClickHouseDatabaseDropCmd = &cobra.Command{
	Use:   "drop NAME",
	Short: "Drop a database and all of its data",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonClickHouseDatabaseDrop,
}

var addonClickHouseUserCmd = &cobra.Command{
	Use:   "user",
	Short: "Create, list and drop users",
}

var addonClickHouseUserCreateCmd = &cobra.Command{
	Use:   "create NAME",
	Short: "Create a user (generates a password when --password is omitted)",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonClickHouseUserCreate,
}

var addonClickHouseUserListCmd = &cobra.Command{
	Use:   "list",
	Short: "List users",
	RunE:  runAddonClickHouseUserList,
}

var addonClickHouseUserDropCmd = &cobra.Command{
	Use:   "drop NAME",
	Short: "Drop a user",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonClickHouseUserDrop,
}

var addonClickHouseGrantCmd = &cobra.Command{
	Use:   "grant USER",
	Short: "Grant or revoke privileges on a database/table",
	Long: `Grant privileges to a user. Select privileges with --read (SELECT),
--write (INSERT), --ddl (CREATE/ALTER/DROP/TRUNCATE/OPTIMIZE), --all, or an
explicit comma-separated list with --privileges. The scope is
[database].[table]; with no --database the grant is global (*.*). --revoke
issues REVOKE instead of GRANT.`,
	Args: cobra.ExactArgs(1),
	RunE: runAddonClickHouseGrant,
}

var addonClickHouseGrantListCmd = &cobra.Command{
	Use:   "list USER",
	Short: "Show a user's grants",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonClickHouseGrantList,
}

var addonClickHouseBackupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Back up a database to the managed backups disk",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonClickHouseBackup(cmd) },
}

var addonClickHouseRestoreCmd = &cobra.Command{
	Use:   "restore FILE",
	Short: "Restore a .zip backup from the managed backups disk",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonClickHouseRestore,
}

var addonClickHouseBackupsCmd = &cobra.Command{
	Use:   "backups",
	Short: "List .zip backups in the managed backups disk",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonClickHouseBackupList(cmd) },
}

var addonClickHouseConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Inspect, change and render server settings",
	Long: `Inspect and change ClickHouse server settings stored in pg.yaml and rendered
into the generated config.d XML. Keys are dotted XML paths whose text value is
the setting:

  pg addon clickhouse config set max_server_memory_usage=2147483648
  pg addon clickhouse config set logger.level=debug
  pg addon clickhouse config set query_log.flush_interval_milliseconds=1000

pgcli-managed keys (paths, listen/ports, backups, user_directories, profiles,
users, storage disks/policies) are rejected. "set"/"unset" restart a running
container by default; pass --no-restart to apply the change on the next start.
"show" prints the full generated XML.`,
}

var addonClickHouseConfigListCmd = &cobra.Command{
	Use:   "list",
	Short: "List managed and custom settings",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonClickHouseConfigList(cmd) },
}

var addonClickHouseConfigGetCmd = &cobra.Command{
	Use:   "get KEY",
	Short: "Print one setting's effective value",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonClickHouseConfigGet,
}

var addonClickHouseConfigSetCmd = &cobra.Command{
	Use:   "set KEY=VALUE...",
	Short: "Set settings and restart the container",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runAddonClickHouseConfigSet,
}

var addonClickHouseConfigUnsetCmd = &cobra.Command{
	Use:   "unset KEY...",
	Short: "Remove settings and restart the container",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runAddonClickHouseConfigUnset,
}

var addonClickHouseConfigShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the generated config.d XML",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonClickHouseConfigShow(cmd) },
}

func init() {
	addonCmd.AddCommand(addonClickHouseCmd)
	addonClickHouseCmd.AddCommand(
		addonClickHouseStatusCmd, addonClickHouseShellCmd, addonClickHouseQueryCmd,
		addonClickHouseDatabaseCmd, addonClickHouseUserCmd, addonClickHouseGrantCmd,
		addonClickHouseBackupCmd, addonClickHouseRestoreCmd, addonClickHouseBackupsCmd,
		addonClickHouseConfigCmd,
	)
	addonClickHouseDatabaseCmd.AddCommand(addonClickHouseDatabaseCreateCmd, addonClickHouseDatabaseListCmd, addonClickHouseDatabaseDropCmd)
	addonClickHouseUserCmd.AddCommand(addonClickHouseUserCreateCmd, addonClickHouseUserListCmd, addonClickHouseUserDropCmd)
	addonClickHouseGrantCmd.AddCommand(addonClickHouseGrantListCmd)
	addonClickHouseConfigCmd.AddCommand(addonClickHouseConfigListCmd, addonClickHouseConfigGetCmd, addonClickHouseConfigSetCmd, addonClickHouseConfigUnsetCmd, addonClickHouseConfigShowCmd)

	for _, sub := range []*cobra.Command{
		addonClickHouseStatusCmd, addonClickHouseShellCmd, addonClickHouseQueryCmd,
		addonClickHouseDatabaseCreateCmd, addonClickHouseDatabaseListCmd, addonClickHouseDatabaseDropCmd,
		addonClickHouseUserCreateCmd, addonClickHouseUserListCmd, addonClickHouseUserDropCmd,
		addonClickHouseGrantCmd, addonClickHouseGrantListCmd,
		addonClickHouseBackupCmd, addonClickHouseRestoreCmd, addonClickHouseBackupsCmd,
		addonClickHouseConfigListCmd, addonClickHouseConfigGetCmd, addonClickHouseConfigSetCmd,
		addonClickHouseConfigUnsetCmd, addonClickHouseConfigShowCmd,
	} {
		sub.Flags().String("name", "", "clickhouse instance name (default \"clickhouse\")")
	}

	addonClickHouseQueryCmd.Flags().String("database", "", "default database (--database)")
	addonClickHouseQueryCmd.Flags().String("file", "", "read SQL from this file (\"-\" = stdin)")
	addonClickHouseDatabaseCreateCmd.Flags().String("engine", "", "database engine (default Atomic)")
	addonClickHouseDatabaseCreateCmd.Flags().String("comment", "", "database comment")
	addonClickHouseDatabaseCreateCmd.Flags().Bool("if-not-exists", false, "do nothing if the database already exists")
	addonClickHouseDatabaseDropCmd.Flags().Bool("if-exists", false, "do nothing if the database does not exist")
	addonClickHouseDatabaseDropCmd.Flags().Bool("yes", false, "skip the confirmation prompt")

	addonClickHouseUserCreateCmd.Flags().String("password", "", "user password (generated and printed when omitted)")
	addonClickHouseUserCreateCmd.Flags().String("default-database", "", "default database for the user")
	addonClickHouseUserCreateCmd.Flags().Bool("host-any", false, "allow connections from any host (default when no host flag is given)")
	addonClickHouseUserCreateCmd.Flags().Bool("host-local", false, "allow local (loopback) connections only")
	addonClickHouseUserCreateCmd.Flags().StringArray("host-ip", nil, "allow this IP or CIDR (repeatable)")
	addonClickHouseUserCreateCmd.Flags().StringArray("host-name", nil, "allow this host name (repeatable)")
	addonClickHouseUserCreateCmd.Flags().Bool("readonly", false, "create the user with SETTINGS readonly = 1")
	addonClickHouseUserCreateCmd.Flags().Bool("if-not-exists", false, "do nothing if the user already exists")
	addonClickHouseUserDropCmd.Flags().Bool("if-exists", false, "do nothing if the user does not exist")
	addonClickHouseUserDropCmd.Flags().Bool("yes", false, "skip the confirmation prompt")

	addonClickHouseGrantCmd.Flags().Bool("read", false, "grant SELECT")
	addonClickHouseGrantCmd.Flags().Bool("write", false, "grant INSERT")
	addonClickHouseGrantCmd.Flags().Bool("ddl", false, "grant CREATE, ALTER, DROP, TRUNCATE and OPTIMIZE")
	addonClickHouseGrantCmd.Flags().Bool("all", false, "grant ALL")
	addonClickHouseGrantCmd.Flags().String("privileges", "", "comma-separated explicit privilege list (e.g. SELECT,INSERT)")
	addonClickHouseGrantCmd.Flags().String("database", "", "database scope (default: all databases)")
	addonClickHouseGrantCmd.Flags().String("table", "", "table scope (requires --database)")
	addonClickHouseGrantCmd.Flags().Bool("revoke", false, "revoke instead of grant")
	addonClickHouseGrantCmd.Flags().Bool("with-grant-option", false, "add WITH GRANT OPTION")

	addonClickHouseBackupCmd.Flags().String("database", "", "database to back up (required)")
	addonClickHouseBackupCmd.Flags().String("out", "", "output file inside the backups dir (default <db>-<UTC timestamp>.zip)")
	addonClickHouseRestoreCmd.Flags().String("database", "", "database name recorded in the backup (required)")
	addonClickHouseRestoreCmd.Flags().String("as", "", "restore under a new database name")

	for _, sub := range []*cobra.Command{addonClickHouseConfigSetCmd, addonClickHouseConfigUnsetCmd} {
		sub.Flags().Bool("no-restart", false, "apply on the next start instead of restarting the container now")
	}

	// ClickHouse-specific install flags. --name/--data-dir/--listen/
	// --http-port/--admin-user/--admin-password/--version/--force/
	// --config-option are shared.
	addonInstallCmd.Flags().Int("tcp-port", 0, "clickhouse native-protocol host port (0=auto-assign from clickhouse_tcp_start_port, default 9000; the S3 API pool shares this range)")
}
