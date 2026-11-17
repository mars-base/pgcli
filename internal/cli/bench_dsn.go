package cli

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
)

// benchTarget is the connection information for one pgbench/psql client run.
// Password is passed through PGPASSWORD, never through argv.
type benchTarget struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
	ExtraEnv []string // sorted PG* entries derived from DSN query parameters
	Mounts   []string // pre-formatted -v operands for TLS files
}

var benchDSNParamEnv = map[string]string{
	"sslmode":                   "PGSSLMODE",
	"sslrootcert":               "PGSSLROOTCERT",
	"sslcert":                   "PGSSLCERT",
	"sslkey":                    "PGSSLKEY",
	"sslcrl":                    "PGSSLCRL",
	"sslcrldir":                 "PGSSLCRLDIR",
	"sslpassword":               "PGSSLPASSWORD",
	"connect_timeout":           "PGCONNECT_TIMEOUT",
	"application_name":          "PGAPPNAME",
	"fallback_application_name": "PGFALLBACKAPPNAME",
	"options":                   "PGOPTIONS",
	"target_session_attrs":      "PGTARGETSESSIONATTRS",
	"channel_binding":           "PGCHANNELBINDING",
	"client_encoding":           "PGCLIENTENCODING",
	"gssencmode":                "PGGSSENCMODE",
	"sslnegotiation":            "PGSSLNEGOTIATION",
	"ssl_min_protocol_version":  "PGSSLMINPROTOCOLVERSION",
	"ssl_max_protocol_version":  "PGSSLMAXPROTOCOLVERSION",
}

var benchDSNTLSFiles = map[string]bool{
	"sslrootcert": true,
	"sslcert":     true,
	"sslkey":      true,
	"sslcrl":      true,
	"sslcrldir":   true,
}

var benchDSNForbiddenParams = map[string]bool{
	"host":        true,
	"hostaddr":    true,
	"port":        true,
	"user":        true,
	"password":    true,
	"dbname":      true,
	"service":     true,
	"passfile":    true,
	"replication": true,
}

func benchTargetFromConfig(c *config.Config) benchTarget {
	host := c.Postgres.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := c.Postgres.Port
	if port == 0 {
		port = 5432
	}
	database := c.Postgres.Database
	if database == "" {
		database = "postgres"
	}
	return benchTarget{
		Host:     host,
		Port:     port,
		User:     c.Postgres.User,
		Password: c.Postgres.Password,
		Database: database,
	}
}

func parseBenchDSN(raw string) (benchTarget, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return benchTarget{}, fmt.Errorf("invalid --dsn: %w", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return benchTarget{}, fmt.Errorf("invalid --dsn: scheme must be postgres:// or postgresql://, got %q", u.Scheme)
	}
	if u.Opaque != "" {
		return benchTarget{}, fmt.Errorf("invalid --dsn: expected postgres://user:pass@host:port/db")
	}
	if u.Fragment != "" {
		return benchTarget{}, fmt.Errorf("invalid --dsn: fragments are not supported")
	}
	if strings.Contains(u.Host, ",") {
		return benchTarget{}, fmt.Errorf("invalid --dsn: multiple hosts are not supported; pass one host")
	}
	host := u.Hostname()
	if host == "" {
		return benchTarget{}, fmt.Errorf("invalid --dsn: missing host")
	}
	port := 5432
	if p := u.Port(); p != "" {
		port, err = strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return benchTarget{}, fmt.Errorf("invalid --dsn: bad port %q", p)
		}
	}
	if u.User == nil {
		return benchTarget{}, fmt.Errorf("invalid --dsn: missing user")
	}
	user := u.User.Username()
	if user == "" {
		return benchTarget{}, fmt.Errorf("invalid --dsn: missing user")
	}
	password, _ := u.User.Password()
	database := strings.TrimPrefix(u.Path, "/")
	if database == "" {
		return benchTarget{}, fmt.Errorf("invalid --dsn: missing database")
	}
	if strings.Contains(database, "/") {
		return benchTarget{}, fmt.Errorf("invalid --dsn: database name must not contain %q", "/")
	}

	t := benchTarget{
		Host:     host,
		Port:     port,
		User:     user,
		Password: password,
		Database: database,
	}

	query := u.Query()
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	tlsIndex := 0
	for _, key := range keys {
		values := query[key]
		if len(values) != 1 {
			return benchTarget{}, fmt.Errorf("invalid --dsn: parameter %q must be specified once", key)
		}
		value := values[0]
		if benchDSNForbiddenParams[key] {
			return benchTarget{}, fmt.Errorf("invalid --dsn: parameter %q must be set in the URL authority/path, not in the query string", key)
		}
		envName, ok := benchDSNParamEnv[key]
		if !ok {
			return benchTarget{}, fmt.Errorf("invalid --dsn: unsupported parameter %q (supported: %s)", key, strings.Join(benchDSNParamKeys(), ", "))
		}
		if benchDSNTLSFiles[key] {
			if value == "" {
				return benchTarget{}, fmt.Errorf("invalid --dsn: parameter %q must not be empty", key)
			}
			if key == "sslrootcert" && value == "system" {
				t.ExtraEnv = append(t.ExtraEnv, envName+"=system")
				continue
			}
			hostPath, err := resolveBenchTLSPath(value, key == "sslcrldir")
			if err != nil {
				return benchTarget{}, err
			}
			containerPath := fmt.Sprintf("/pgcli-bench/tls/%d-%s", tlsIndex, sanitizeBenchMountName(filepath.Base(hostPath)))
			tlsIndex++
			t.ExtraEnv = append(t.ExtraEnv, envName+"="+containerPath)
			t.Mounts = append(t.Mounts, hostPath+":"+containerPath+":ro,Z")
			continue
		}
		t.ExtraEnv = append(t.ExtraEnv, envName+"="+value)
	}
	sort.Strings(t.ExtraEnv)
	return t, nil
}

func benchDSNParamKeys() []string {
	keys := make([]string, 0, len(benchDSNParamEnv))
	for key := range benchDSNParamEnv {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func resolveBenchTLSPath(value string, wantDir bool) (string, error) {
	path := value
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving TLS path %q: %w", value, err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("TLS path %q: %w", value, err)
	}
	if wantDir && !fi.IsDir() {
		return "", fmt.Errorf("TLS path %q must be a directory", value)
	}
	if !wantDir && !fi.Mode().IsRegular() {
		return "", fmt.Errorf("TLS path %q must be a regular file", value)
	}
	if strings.Contains(abs, ":") {
		return "", fmt.Errorf("TLS path %q must not contain ':'", value)
	}
	return abs, nil
}

func sanitizeBenchMountName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "file"
	}
	return b.String()
}

func (t benchTarget) withDatabase(database string) benchTarget {
	t.Database = database
	return t
}

func (t benchTarget) env() []string {
	env := []string{
		"PGHOST=" + t.Host,
		"PGPORT=" + strconv.Itoa(t.Port),
		"PGUSER=" + t.User,
		"PGDATABASE=" + t.Database,
	}
	if t.Password != "" {
		env = append(env, "PGPASSWORD="+t.Password)
	}
	env = append(env, t.ExtraEnv...)
	if !benchEnvHas(env, "PGAPPNAME") {
		env = append(env, "PGAPPNAME=pgcli-bench")
	}
	return env
}

func benchEnvHas(env []string, name string) bool {
	for _, kv := range env {
		if key, _, ok := strings.Cut(kv, "="); ok && key == name {
			return true
		}
	}
	return false
}

func (t benchTarget) display() string {
	return fmt.Sprintf("%s@%s/%s", t.User, net.JoinHostPort(t.Host, strconv.Itoa(t.Port)), t.Database)
}
