package influxdb

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
)

// Container paths are fixed inside the pgcli-influxdb image: the config file
// is bind-mounted read-only at ContainerConfigPath and the data directory is
// bind-mounted at ContainerDataDir. The backup/restore RPC listens on the
// host-network loopback at the instance's RPCPort (no authentication upstream,
// so it is never published).
const (
	ContainerConfigPath = "/etc/influxdb/influxdb.conf"
	ContainerDataDir    = "/var/lib/influxdb"
)

type configOverride struct {
	key, value string
}

// groupOverrides splits the stored extra-config entries by table, preserving
// the order of first appearance for tables and insertion order within a table.
func groupOverrides(entries []string) (top []configOverride, sections map[string][]configOverride, order []string, err error) {
	sections = make(map[string][]configOverride)
	for _, opt := range entries {
		section, key, value, err := config.ParseInfluxDBConfigOption(opt)
		if err != nil {
			return nil, nil, nil, err
		}
		if section == "" {
			top = append(top, configOverride{key, value})
			continue
		}
		if _, ok := sections[section]; !ok {
			order = append(order, section)
		}
		sections[section] = append(sections[section], configOverride{key, value})
	}
	return top, sections, order, nil
}

// RenderConfig returns the minimal influxdb.conf pgcli manages. Everything
// else keeps InfluxDB 1.13.1's built-in defaults. Paths are the container
// mount points, not host paths — the file is bind-mounted into the container
// at ContainerConfigPath. The top-level bind-address is the RPC service
// (backup/restore), which upstream has no authentication for, so it is always
// container loopback. HTTP is authenticated and binds Listen:HostPort (the
// container uses host networking, so that is the host listener too).
//
// ExtraConfig entries (from --config-option / `pg addon influxdb config set`)
// are merged into their table so no TOML key is ever defined twice; unknown
// tables are appended at the end.
func RenderConfig(ic config.InfluxDBConfig) (string, error) {
	top, sections, order, err := groupOverrides(ic.ExtraConfig)
	if err != nil {
		return "", err
	}
	metaDir := filepath.Join(ContainerDataDir, "meta")
	dataDir := filepath.Join(ContainerDataDir, "data")
	walDir := filepath.Join(ContainerDataDir, "wal")

	var b strings.Builder
	b.WriteString("reporting-disabled = true\n")
	fmt.Fprintf(&b, "bind-address = %q\n", net.JoinHostPort("127.0.0.1", strconv.Itoa(ic.RPCPort)))
	for _, o := range top {
		fmt.Fprintf(&b, "%s = %s\n", o.key, o.value)
	}

	writeSection := func(name string, managed []configOverride) {
		fmt.Fprintf(&b, "\n[%s]\n", name)
		for _, o := range managed {
			fmt.Fprintf(&b, "  %s = %s\n", o.key, o.value)
		}
		for _, o := range sections[name] {
			fmt.Fprintf(&b, "  %s = %s\n", o.key, o.value)
		}
	}
	writeSection("meta", []configOverride{{"dir", strconv.Quote(metaDir)}})
	writeSection("data", []configOverride{
		{"dir", strconv.Quote(dataDir)},
		{"wal-dir", strconv.Quote(walDir)},
	})
	writeSection("http", []configOverride{
		{"bind-address", strconv.Quote(net.JoinHostPort(ic.Listen, strconv.Itoa(ic.HostPort)))},
		{"auth-enabled", "true"},
	})
	for _, name := range order {
		switch name {
		case "meta", "data", "http":
			continue // already rendered above
		}
		writeSection(name, nil)
	}
	return b.String(), nil
}

// WriteConfig renders ic and writes it atomically with mode 0644 (the file
// contains no credentials).
func WriteConfig(ic config.InfluxDBConfig) error {
	content, err := RenderConfig(ic)
	if err != nil {
		return fmt.Errorf("rendering config: %w", err)
	}
	dir := filepath.Dir(ic.ConfigPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating config dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".influxdb.conf-*")
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
	if err := os.Rename(name, ic.ConfigPath); err != nil {
		return fmt.Errorf("installing %s: %w", ic.ConfigPath, err)
	}
	return nil
}

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

// PingURL is the unauthenticated health endpoint (`/ping` returns 204 unless
// ping-auth-enabled is set, which pgcli does not set).
func PingURL(ic config.InfluxDBConfig) string {
	return "http://" + net.JoinHostPort(ConnectHost(ic.Listen), strconv.Itoa(ic.HostPort)) + "/ping"
}

// InfluxQLString quotes s as an InfluxQL string literal.
func InfluxQLString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
}

// InfluxQLIdent quotes s as an InfluxQL identifier.
func InfluxQLIdent(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
