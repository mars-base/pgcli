// Package clickhouse renders the managed ClickHouse server configuration and
// provides the pure SQL/URL helpers used by the podman add-on manager.
package clickhouse

import (
	"encoding/xml"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
)

// Container paths are fixed by the official clickhouse-server image. The
// config directory replaces the image's config.d (which would otherwise bind
// all interfaces), the data/log/backup directories are instance bind mounts,
// and the admin password is mounted as a file read by the entrypoint.
const (
	ContainerConfigDir  = "/etc/clickhouse-server/config.d"
	ContainerConfigPath = ContainerConfigDir + "/zz-pgcli.xml"
	ContainerDataDir    = "/var/lib/clickhouse"
	ContainerLogDir     = "/var/log/clickhouse-server"
	ContainerBackupDir  = "/var/lib/clickhouse-backups"
	ContainerSecretPath = "/run/secrets/admin_password"

	// StartTimeout covers first-boot system table creation on a slow host.
	StartTimeout = 60 * time.Second
)

var (
	databaseNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	userNameRe     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@-]{0,127}$`)
	engineRe       = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
	privilegeRe    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\([A-Za-z0-9_,]+\))?$`)
	hostNameRe     = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,253}[A-Za-z0-9])?$`)
)

// ValidDatabaseName reports whether name is a safe ClickHouse database name.
func ValidDatabaseName(name string) bool { return databaseNameRe.MatchString(name) }

// ValidUserName reports whether name is a safe ClickHouse user name.
func ValidUserName(name string) bool { return userNameRe.MatchString(name) }

// ValidEngine reports whether engine is a bare ClickHouse database engine
// name (no arguments; this CLI does not expose engine parameters).
func ValidEngine(engine string) bool { return engineRe.MatchString(engine) }

// --- XML rendering ------------------------------------------------------

type xmlNode struct {
	name     string
	text     string
	hasText  bool
	children map[string]*xmlNode
}

func newXMLNode(name string) *xmlNode {
	return &xmlNode{name: name, children: map[string]*xmlNode{}}
}

func (n *xmlNode) set(path []string, value string) error {
	if len(path) == 0 {
		return fmt.Errorf("empty setting path")
	}
	child, ok := n.children[path[0]]
	if !ok {
		child = newXMLNode(path[0])
		n.children[path[0]] = child
	}
	if len(path) == 1 {
		if child.hasText || len(child.children) > 0 {
			return fmt.Errorf("setting %s is already defined", strings.Join(path, "."))
		}
		child.text = value
		child.hasText = true
		return nil
	}
	if child.hasText {
		return fmt.Errorf("cannot set %s below scalar key %s", strings.Join(path, "."), path[0])
	}
	return child.set(path[1:], value)
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func writeXMLChildren(b *strings.Builder, n *xmlNode, depth int) {
	names := make([]string, 0, len(n.children))
	for name := range n.children {
		names = append(names, name)
	}
	sort.Strings(names)
	indent := strings.Repeat("    ", depth)
	for _, name := range names {
		child := n.children[name]
		if child.hasText {
			fmt.Fprintf(b, "%s<%s>%s</%s>\n", indent, name, xmlEscape(child.text), name)
			continue
		}
		fmt.Fprintf(b, "%s<%s>\n", indent, name)
		writeXMLChildren(b, child, depth+1)
		fmt.Fprintf(b, "%s</%s>\n", indent, name)
	}
}

func add(parent *xmlNode, path []string, value string) error {
	if err := parent.set(path, value); err != nil {
		return fmt.Errorf("rendering config: %w", err)
	}
	return nil
}

// listenHosts returns the bind addresses for the generated config. Loopback is
// always included unless a wildcard is requested, so the CLI's in-container
// clickhouse-client can reach the server regardless of --listen.
func listenHosts(listen string) []string {
	listen = strings.TrimSpace(listen)
	switch listen {
	case "0.0.0.0":
		return []string{"0.0.0.0"}
	case "::", "[::]":
		return []string{"::", "0.0.0.0"}
	case "", "127.0.0.1":
		return []string{"127.0.0.1"}
	default:
		hosts := []string{listen}
		if listen != "::1" {
			hosts = append(hosts, "127.0.0.1")
		}
		return hosts
	}
}

// RenderConfig returns the config.d override for one instance. It replaces
// the image's config.d directory, so listen_host is exactly what pgcli chose
// (the stock image would otherwise bind every interface).
func RenderConfig(cc config.ClickHouseConfig) (string, error) {
	root := newXMLNode("clickhouse")

	level := "information"
	if v, ok := cc.Settings["logger.level"]; ok && v != "" {
		level = v
	}
	for path, value := range map[string]string{
		"logger.level":    level,
		"logger.log":      ContainerLogDir + "/clickhouse-server.log",
		"logger.errorlog": ContainerLogDir + "/clickhouse-server.err.log",
		"logger.console":  "1",
		"logger.size":     "100M",
		"logger.count":    "3",
		"http_port":       strconv.Itoa(cc.HTTPPort),
		"tcp_port":        strconv.Itoa(cc.TCPPort),
	} {
		if err := add(root, strings.Split(path, "."), value); err != nil {
			return "", err
		}
	}
	for path, value := range map[string]string{
		"storage_configuration.disks.backups.type": "local",
		"storage_configuration.disks.backups.path": ContainerBackupDir + "/",
		"backups.allowed_disk":                     "backups",
		"backups.allowed_path":                     ContainerBackupDir + "/",
	} {
		if err := add(root, strings.Split(path, "."), value); err != nil {
			return "", err
		}
	}
	keys := make([]string, 0, len(cc.Settings))
	for key := range cc.Settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key == "logger.level" {
			continue // folded into the managed logger block above
		}
		if err := add(root, strings.Split(key, "."), cc.Settings[key]); err != nil {
			return "", err
		}
	}

	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\"?>\n")
	b.WriteString("<clickhouse>\n")
	for _, host := range listenHosts(cc.Listen) {
		fmt.Fprintf(&b, "    <listen_host>%s</listen_host>\n", xmlEscape(host))
	}
	b.WriteString("    <listen_try>1</listen_try>\n")
	writeXMLChildren(&b, root, 1)
	b.WriteString("</clickhouse>\n")
	return b.String(), nil
}

// WriteConfig atomically writes the generated config with mode 0644 (it
// contains no credentials).
func WriteConfig(cc config.ClickHouseConfig) error {
	content, err := RenderConfig(cc)
	if err != nil {
		return err
	}
	dir := filepath.Dir(cc.ConfigPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating config dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".zz-pgcli.xml-*")
	if err != nil {
		return fmt.Errorf("creating temp config: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("writing config: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("setting config mode: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing config: %w", err)
	}
	if err := os.Rename(name, cc.ConfigPath); err != nil {
		return fmt.Errorf("installing %s: %w", cc.ConfigPath, err)
	}
	return nil
}

// WriteSecret writes the admin password as a 0600 file mounted into the
// container. The parent directory stays host-owned so pgcli can replace the
// file after a password change; only the file itself is read by the
// container entrypoint (which starts as root).
func WriteSecret(cc config.ClickHouseConfig) error {
	dir := filepath.Dir(cc.SecretPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating secret dir %s: %w", dir, err)
	}
	current, err := os.ReadFile(cc.SecretPath)
	if err == nil && string(current) == cc.AdminPassword {
		return nil
	}
	if err := os.Remove(cc.SecretPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing old secret %s: %w", cc.SecretPath, err)
	}
	tmp, err := os.CreateTemp(dir, ".admin_password-*")
	if err != nil {
		return fmt.Errorf("creating temp secret: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.WriteString(cc.AdminPassword); err != nil {
		tmp.Close()
		return fmt.Errorf("writing secret: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("setting secret mode: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing secret: %w", err)
	}
	if err := os.Rename(name, cc.SecretPath); err != nil {
		return fmt.Errorf("installing %s: %w", cc.SecretPath, err)
	}
	return nil
}

// --- Endpoints / health -------------------------------------------------

// ConnectHost is the address clients use to reach this instance. A wildcard
// bind is dialled through loopback.
func ConnectHost(listen string) string {
	switch listen {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	default:
		return listen
	}
}

// HTTPURL is the unauthenticated health endpoint base URL. /ping returns
// "Ok." once the server is accepting HTTP requests.
func HTTPURL(cc config.ClickHouseConfig) string {
	return "http://" + net.JoinHostPort(ConnectHost(cc.Listen), strconv.Itoa(cc.HTTPPort))
}

// TCPAddr is the host:port for native clients.
func TCPAddr(cc config.ClickHouseConfig) string {
	return net.JoinHostPort(ConnectHost(cc.Listen), strconv.Itoa(cc.TCPPort))
}

// ClientArgs returns the clickhouse-client connection arguments. The server
// always listens on loopback inside the container, so the in-container client
// uses 127.0.0.1 even when --listen is a LAN address.
func ClientArgs(cc config.ClickHouseConfig, database string) []string {
	args := []string{
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(cc.TCPPort),
	}
	if database != "" {
		args = append(args, "--database", database)
	}
	return args
}

// Ping checks /ping.
func Ping(cc config.ClickHouseConfig, timeout time.Duration) error {
	client := &http.Client{
		// Loopback only; never route health checks through an HTTP proxy.
		Transport: &http.Transport{Proxy: nil},
		Timeout:   timeout,
	}
	url := HTTPURL(cc) + "/ping"
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned HTTP %s", url, resp.Status)
	}
	var body [16]byte
	n, _ := resp.Body.Read(body[:])
	if strings.TrimSpace(string(body[:n])) != "Ok." {
		return fmt.Errorf("%s returned an unexpected body", url)
	}
	return nil
}

// WaitForPing polls /ping until it answers or the timeout expires.
func WaitForPing(cc config.ClickHouseConfig, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := Ping(cc, 2*time.Second); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// --- SQL builders -------------------------------------------------------

// Ident quotes a ClickHouse identifier with backticks.
func Ident(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "`", "\\`")
	return "`" + s + "`"
}

// StringLiteral quotes a ClickHouse string literal.
func StringLiteral(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
}

// CreateDatabaseSQL builds CREATE DATABASE.
func CreateDatabaseSQL(name, engine, comment string, ifNotExists bool) (string, error) {
	if !ValidDatabaseName(name) {
		return "", fmt.Errorf("invalid database name %q", name)
	}
	if engine == "" {
		engine = "Atomic"
	}
	if !ValidEngine(engine) {
		return "", fmt.Errorf("invalid database engine %q", engine)
	}
	sql := "CREATE DATABASE "
	if ifNotExists {
		sql += "IF NOT EXISTS "
	}
	sql += Ident(name) + " ENGINE = " + engine
	if comment != "" {
		sql += " COMMENT " + StringLiteral(comment)
	}
	return sql, nil
}

// DropDatabaseSQL builds DROP DATABASE.
func DropDatabaseSQL(name string, ifExists bool) (string, error) {
	if !ValidDatabaseName(name) {
		return "", fmt.Errorf("invalid database name %q", name)
	}
	sql := "DROP DATABASE "
	if ifExists {
		sql += "IF EXISTS "
	}
	return sql + Ident(name), nil
}

// ListDatabasesSQL lists user-visible databases.
func ListDatabasesSQL() string {
	return "SELECT name, engine, data_path FROM system.databases ORDER BY name"
}

// CreateUserSQL builds CREATE USER with an explicit SHA256 password and host
// restrictions. hostClauses are raw ClickHouse HOST expressions such as
// "ANY", "LOCAL", "IP '10.0.0.0/8'" or "NAME 'app'"; callers validate them.
func CreateUserSQL(name, password, defaultDatabase string, hostClauses []string, readonly, ifNotExists bool) (string, error) {
	if !ValidUserName(name) {
		return "", fmt.Errorf("invalid user name %q", name)
	}
	if password == "" {
		return "", fmt.Errorf("password must not be empty")
	}
	if strings.ContainsAny(password, "\x00\r\n") {
		return "", fmt.Errorf("password must not contain control characters")
	}
	sql := "CREATE USER "
	if ifNotExists {
		sql += "IF NOT EXISTS "
	}
	sql += Ident(name) + " IDENTIFIED WITH sha256_password BY " + StringLiteral(password)
	if len(hostClauses) == 0 {
		hostClauses = []string{"ANY"}
	}
	sql += " HOST " + strings.Join(hostClauses, ", ")
	if defaultDatabase != "" {
		if !ValidDatabaseName(defaultDatabase) {
			return "", fmt.Errorf("invalid default database %q", defaultDatabase)
		}
		sql += " DEFAULT DATABASE " + Ident(defaultDatabase)
	}
	if readonly {
		sql += " SETTINGS readonly = 1"
	}
	return sql, nil
}

// HostIPClause validates an IP or CIDR and returns a ClickHouse HOST clause.
func HostIPClause(value string) (string, error) {
	if ip := net.ParseIP(value); ip != nil {
		return "IP " + StringLiteral(ip.String()), nil
	}
	if _, _, err := net.ParseCIDR(value); err == nil {
		return "IP " + StringLiteral(value), nil
	}
	return "", fmt.Errorf("invalid IP or CIDR %q", value)
}

// HostNameClause validates a host name and returns a ClickHouse HOST clause.
func HostNameClause(value string) (string, error) {
	if !hostNameRe.MatchString(value) {
		return "", fmt.Errorf("invalid host name %q", value)
	}
	return "NAME " + StringLiteral(value), nil
}

// DropUserSQL builds DROP USER.
func DropUserSQL(name string, ifExists bool) (string, error) {
	if !ValidUserName(name) {
		return "", fmt.Errorf("invalid user name %q", name)
	}
	sql := "DROP USER "
	if ifExists {
		sql += "IF EXISTS "
	}
	return sql + Ident(name), nil
}

// ListUsersSQL lists SQL-managed users (the XML admin user is included by
// system.users as well).
func ListUsersSQL() string {
	return "SELECT name, default_database FROM system.users ORDER BY name"
}

// ShowGrantsSQL builds SHOW GRANTS FOR.
func ShowGrantsSQL(name string) (string, error) {
	if !ValidUserName(name) {
		return "", fmt.Errorf("invalid user name %q", name)
	}
	return "SHOW GRANTS FOR " + Ident(name), nil
}

// NormalizePrivileges validates and uppercases a comma-separated privilege
// list.
func NormalizePrivileges(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !privilegeRe.MatchString(part) {
			return nil, fmt.Errorf("invalid privilege %q", part)
		}
		out = append(out, strings.ToUpper(part))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one privilege is required")
	}
	return out, nil
}

// GrantSQL builds GRANT or REVOKE. database/table may be "*" for wildcards;
// table requires database.
func GrantSQL(user string, privileges []string, database, table string, revoke, withGrantOption bool) (string, error) {
	if !ValidUserName(user) {
		return "", fmt.Errorf("invalid user name %q", user)
	}
	if len(privileges) == 0 {
		return "", fmt.Errorf("at least one privilege is required")
	}
	for i, p := range privileges {
		if !privilegeRe.MatchString(p) {
			return "", fmt.Errorf("invalid privilege %q", p)
		}
		privileges[i] = strings.ToUpper(p)
	}
	if table != "" && table != "*" {
		if database == "" || database == "*" {
			return "", fmt.Errorf("table requires an explicit database")
		}
		if !ValidDatabaseName(database) {
			return "", fmt.Errorf("invalid database name %q", database)
		}
	}
	target := "*"
	switch {
	case database != "" && database != "*":
		if !ValidDatabaseName(database) {
			return "", fmt.Errorf("invalid database name %q", database)
		}
		target = Ident(database) + ".*"
		if table != "" && table != "*" {
			target = Ident(database) + "." + Ident(table)
		}
	case table != "" && table != "*":
		return "", fmt.Errorf("table requires an explicit database")
	}
	verb, preposition := "GRANT", "TO"
	if revoke {
		verb, preposition = "REVOKE", "FROM"
	}
	sql := fmt.Sprintf("%s %s ON %s %s %s", verb, strings.Join(privileges, ", "), target, preposition, Ident(user))
	if !revoke && withGrantOption {
		sql += " WITH GRANT OPTION"
	}
	return sql, nil
}

// BackupDatabaseSQL builds BACKUP DATABASE to the managed backup disk.
func BackupDatabaseSQL(database, diskPath string) (string, error) {
	if !ValidDatabaseName(database) {
		return "", fmt.Errorf("invalid database name %q", database)
	}
	if diskPath == "" || strings.ContainsAny(diskPath, "\x00\r\n") {
		return "", fmt.Errorf("invalid backup path %q", diskPath)
	}
	return fmt.Sprintf("BACKUP DATABASE %s TO Disk('backups', %s)", Ident(database), StringLiteral(diskPath)), nil
}

// RestoreDatabaseSQL builds RESTORE DATABASE from the managed backup disk.
func RestoreDatabaseSQL(database, newName, diskPath string) (string, error) {
	if !ValidDatabaseName(database) {
		return "", fmt.Errorf("invalid database name %q", database)
	}
	if newName != "" && !ValidDatabaseName(newName) {
		return "", fmt.Errorf("invalid restore target database %q", newName)
	}
	if diskPath == "" || strings.ContainsAny(diskPath, "\x00\r\n") {
		return "", fmt.Errorf("invalid backup path %q", diskPath)
	}
	sql := "RESTORE DATABASE " + Ident(database)
	if newName != "" {
		sql += " AS " + Ident(newName)
	}
	return sql + " FROM Disk('backups', " + StringLiteral(diskPath) + ")", nil
}

// BackupTarget resolves --out to a backup-disk-relative path and the matching
// host path. Relative paths are rooted at the instance backup dir; absolute
// paths must stay inside it. The default name is <db>-<UTC timestamp>.zip.
func BackupTarget(backupDir, database, out string, now time.Time) (diskPath, hostPath string, err error) {
	if out == "" {
		out = fmt.Sprintf("%s-%s.zip", database, now.UTC().Format("20060102-150405"))
	}
	hostPath = out
	if !filepath.IsAbs(hostPath) {
		hostPath = filepath.Join(backupDir, hostPath)
	}
	hostPath = filepath.Clean(hostPath)
	rel, err := filepath.Rel(filepath.Clean(backupDir), hostPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("backup path %q must be inside %s", out, backupDir)
	}
	if filepath.Ext(rel) == "" {
		rel += ".zip"
		hostPath += ".zip"
	}
	if !strings.EqualFold(filepath.Ext(rel), ".zip") {
		return "", "", fmt.Errorf("backup path %q must end in .zip", out)
	}
	return filepath.ToSlash(rel), hostPath, nil
}

// RestoreSource resolves a user-supplied backup file to a backup-disk-relative
// path and validates that it exists.
func RestoreSource(backupDir, file string) (diskPath, hostPath string, err error) {
	if file == "" {
		return "", "", fmt.Errorf("backup file is required")
	}
	hostPath = file
	if !filepath.IsAbs(hostPath) {
		hostPath = filepath.Join(backupDir, hostPath)
	}
	hostPath = filepath.Clean(hostPath)
	rel, err := filepath.Rel(filepath.Clean(backupDir), hostPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("backup file %q must be inside %s", file, backupDir)
	}
	if !strings.EqualFold(filepath.Ext(rel), ".zip") {
		return "", "", fmt.Errorf("backup file %q must be a .zip archive", file)
	}
	info, err := os.Stat(hostPath)
	if err != nil {
		return "", "", fmt.Errorf("backup file %s: %w", hostPath, err)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("backup file %s is a directory", hostPath)
	}
	return filepath.ToSlash(rel), hostPath, nil
}

// ListBackupFiles returns *.zip files in the backup dir, newest first.
func ListBackupFiles(backupDir string) ([]string, error) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	type backupFile struct {
		name string
		mod  time.Time
	}
	files := make([]backupFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".zip") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		files = append(files, backupFile{name: entry.Name(), mod: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].mod.Equal(files[j].mod) {
			return files[i].name > files[j].name
		}
		return files[i].mod.After(files[j].mod)
	})
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.name
	}
	return out, nil
}
