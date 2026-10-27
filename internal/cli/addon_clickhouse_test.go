package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
)

func TestClickHouseFlagName(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("name", "", "")
	if got := clickHouseFlagName(cmd); got != "clickhouse" {
		t.Errorf("default name = %q, want clickhouse", got)
	}
	if err := cmd.Flags().Set("name", "ch1"); err != nil {
		t.Fatal(err)
	}
	if got := clickHouseFlagName(cmd); got != "ch1" {
		t.Errorf("name = %q, want ch1", got)
	}
}

func TestClickHouseAdminPasswordReuse(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "admin_password")
	if err := os.WriteFile(secret, []byte("kept-pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := clickHouseAdminPassword(secret)
	if err != nil {
		t.Fatalf("clickHouseAdminPassword: %v", err)
	}
	if got != "kept-pass" {
		t.Errorf("reused password = %q, want kept-pass", got)
	}

	generated, err := clickHouseAdminPassword(filepath.Join(dir, "missing"))
	if err != nil {
		t.Fatalf("clickHouseAdminPassword (missing): %v", err)
	}
	if len(generated) != 20 {
		t.Errorf("generated password length = %d, want 20", len(generated))
	}
	if generated == "kept-pass" {
		t.Error("missing secret should generate a fresh password")
	}

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	generated, err = clickHouseAdminPassword(empty)
	if err != nil {
		t.Fatalf("clickHouseAdminPassword (empty): %v", err)
	}
	if len(generated) != 20 {
		t.Errorf("empty secret should generate a password, got %q", generated)
	}
}

func TestClickHouseHostClauses(t *testing.T) {
	tests := []struct {
		name  string
		flags func(*cobra.Command)
		want  []string
	}{
		{
			name:  "default any",
			flags: func(c *cobra.Command) {},
			want:  []string{"ANY"},
		},
		{
			name: "local",
			flags: func(c *cobra.Command) {
				c.Flags().Bool("host-local", false, "")
				_ = c.Flags().Set("host-local", "true")
			},
			want: []string{"LOCAL"},
		},
		{
			name: "ip and name",
			flags: func(c *cobra.Command) {
				c.Flags().StringArray("host-ip", nil, "")
				c.Flags().StringArray("host-name", nil, "")
				_ = c.Flags().Set("host-ip", "10.0.0.0/8")
				_ = c.Flags().Set("host-name", "app.internal")
			},
			want: []string{"IP '10.0.0.0/8'", "NAME 'app.internal'"},
		},
		{
			name: "any plus local",
			flags: func(c *cobra.Command) {
				c.Flags().Bool("host-any", false, "")
				c.Flags().Bool("host-local", false, "")
				_ = c.Flags().Set("host-any", "true")
				_ = c.Flags().Set("host-local", "true")
			},
			want: []string{"ANY", "LOCAL"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "test"}
			tt.flags(cmd)
			got, err := clickHouseHostClauses(cmd)
			if err != nil {
				t.Fatalf("clickHouseHostClauses: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("clauses = %v, want %v", got, tt.want)
			}
		})
	}

	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().StringArray("host-ip", nil, "")
	_ = cmd.Flags().Set("host-ip", "not-an-ip")
	if _, err := clickHouseHostClauses(cmd); err == nil {
		t.Error("clickHouseHostClauses should reject an invalid IP")
	}
}

func TestClickHouseGrantPrivileges(t *testing.T) {
	tests := []struct {
		name  string
		flags func(*cobra.Command)
		want  []string
		err   bool
	}{
		{
			name: "read and write deduplicated",
			flags: func(c *cobra.Command) {
				c.Flags().Bool("read", false, "")
				c.Flags().Bool("write", false, "")
				_ = c.Flags().Set("read", "true")
				_ = c.Flags().Set("write", "true")
			},
			want: []string{"SELECT", "INSERT"},
		},
		{
			name: "ddl expansion",
			flags: func(c *cobra.Command) {
				c.Flags().Bool("ddl", false, "")
				_ = c.Flags().Set("ddl", "true")
			},
			want: []string{"CREATE", "ALTER", "DROP", "TRUNCATE", "OPTIMIZE"},
		},
		{
			name: "raw privileges merge",
			flags: func(c *cobra.Command) {
				c.Flags().Bool("read", false, "")
				c.Flags().String("privileges", "", "")
				_ = c.Flags().Set("read", "true")
				_ = c.Flags().Set("privileges", "select, insert")
			},
			want: []string{"SELECT", "INSERT"},
		},
		{
			name:  "none selected",
			flags: func(c *cobra.Command) {},
			err:   true,
		},
		{
			name: "invalid raw privilege",
			flags: func(c *cobra.Command) {
				c.Flags().String("privileges", "", "")
				_ = c.Flags().Set("privileges", "bad priv")
			},
			err: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "test"}
			tt.flags(cmd)
			got, err := clickHouseGrantPrivileges(cmd)
			if tt.err {
				if err == nil {
					t.Fatalf("expected an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("clickHouseGrantPrivileges: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("privileges = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClickHouseManagedSetting(t *testing.T) {
	cc := config.ClickHouseConfig{
		Listen:   "10.0.0.5",
		HTTPPort: 18123,
		TCPPort:  19000,
		Settings: map[string]string{"logger.level": "warning"},
	}
	tests := []struct {
		key, want string
		ok        bool
	}{
		{"listen_host", "10.0.0.5", true},
		{"listen_try", "1", true},
		{"http_port", "18123", true},
		{"tcp_port", "19000", true},
		{"logger.level", "warning", true},
		{"logger.log", "/var/log/clickhouse-server/clickhouse-server.log", true},
		{"backups.allowed_disk", "backups", true},
		{"max_server_memory_usage", "", false},
	}
	for _, tt := range tests {
		got, ok := clickHouseManagedSetting(cc, tt.key)
		if ok != tt.ok || got != tt.want {
			t.Errorf("clickHouseManagedSetting(%q) = %q, %v; want %q, %v", tt.key, got, ok, tt.want, tt.ok)
		}
	}

	cc.Settings = nil
	got, ok := clickHouseManagedSetting(cc, "logger.level")
	if !ok || got != "information" {
		t.Errorf("default logger.level = %q, %v", got, ok)
	}
}

func TestClickHouseCommandTree(t *testing.T) {
	for _, name := range []string{
		"status", "shell", "query",
		"database", "user", "grant",
		"backup", "restore", "backups", "config",
	} {
		found := false
		for _, c := range addonClickHouseCmd.Commands() {
			if c.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("pg addon clickhouse is missing the %q subcommand", name)
		}
	}
	for _, parent := range []*cobra.Command{
		addonClickHouseDatabaseCmd, addonClickHouseUserCmd, addonClickHouseGrantCmd, addonClickHouseConfigCmd,
	} {
		if len(parent.Commands()) == 0 {
			t.Errorf("%s has no subcommands", parent.Name())
		}
	}

	// The install command must accept the shared flags and clickhouse's own.
	required := []string{
		"name", "version", "data-dir", "listen", "http-port", "tcp-port",
		"admin-user", "admin-password", "config-option", "force",
	}
	for _, name := range required {
		if addonInstallCmd.Flags().Lookup(name) == nil {
			t.Errorf("addon install is missing --%s", name)
		}
	}
}

func TestClickHouseConfigSetArgumentParsing(t *testing.T) {
	// RunE needs a live config; assert the flag wiring that the parser relies on.
	for _, key := range []string{"no-restart"} {
		if addonClickHouseConfigSetCmd.Flags().Lookup(key) == nil {
			t.Errorf("config set is missing --%s", key)
		}
		if addonClickHouseConfigUnsetCmd.Flags().Lookup(key) == nil {
			t.Errorf("config unset is missing --%s", key)
		}
	}
	if got := addonClickHouseQueryCmd.Flags().Lookup("database"); got == nil || !strings.Contains(got.Usage, "default database") {
		t.Errorf("query --database flag = %v", got)
	}
}
