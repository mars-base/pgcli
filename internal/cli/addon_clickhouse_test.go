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
		"status", "cluster", "shell", "query",
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
		"cluster", "shard", "replica", "advertise-host", "peer", "keeper",
		"interserver-port",
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

func TestValidateClickHouseClusterInstall(t *testing.T) {
	opts := clickHouseInstallOptions{
		cluster: "chcluster", clusterSet: true,
		shard: 1, shardSet: true,
		replica: 1, replicaSet: true,
		advertiseHost: "10.0.0.11", advertiseHostSet: true,
		peers: []string{"1/2@10.0.0.12:9000"}, peersSet: true,
		keepers: []string{"10.0.0.11:9181"}, keepersSet: true,
		listen: "0.0.0.0", listenSet: true,
		adminPassword: "same-pass", adminPasswordSet: true,
	}
	cc := config.ClickHouseConfig{
		Name: "ch1", Cluster: "chcluster", Shard: 1, Replica: 1,
		AdvertiseHost: "10.0.0.11", Peers: opts.peers, Keepers: opts.keepers,
		Listen: "0.0.0.0", AdminPassword: "same-pass",
	}
	if err := validateClickHouseClusterInstall(cc, opts, false); err != nil {
		t.Fatalf("complete fresh cluster install should pass: %v", err)
	}

	standalone := config.ClickHouseConfig{Name: "ch1", Listen: "127.0.0.1"}
	if err := validateClickHouseClusterInstall(standalone, clickHouseInstallOptions{peersSet: true}, false); err == nil {
		t.Error("--peer without --cluster must fail")
	}

	noPass := opts
	noPass.adminPasswordSet = false
	noPass.adminPassword = ""
	noPassCC := cc
	noPassCC.AdminPassword = ""
	if err := validateClickHouseClusterInstall(noPassCC, noPass, false); err == nil {
		t.Error("fresh cluster install without an explicit --admin-password must fail")
	}

	loopback := cc
	loopback.Listen = "127.0.0.1"
	if err := validateClickHouseClusterInstall(loopback, opts, true); err == nil {
		t.Error("cluster install with loopback listen must fail")
	}

	missing := opts
	missing.keepersSet = false
	missing.clusterSet = true
	missing.keepers = nil
	missingCC := cc
	missingCC.Keepers = nil
	if err := validateClickHouseClusterInstall(missingCC, missing, false); err == nil ||
		!strings.Contains(err.Error(), "--keeper") {
		t.Errorf("fresh cluster install must require --keeper, got %v", err)
	}
}

func TestCheckClickHouseTopologyImmutable(t *testing.T) {
	stored := config.ClickHouseConfig{
		Name: "ch1", Cluster: "chcluster", Shard: 1, Replica: 1,
		AdvertiseHost: "10.0.0.11", TCPPort: 9000, InterserverPort: 9009,
		Peers: []string{"1/2@10.0.0.12:9000", "2/1@10.0.0.13:9000"}, Keepers: []string{"10.0.0.11:9181"},
	}
	if err := checkClickHouseTopologyImmutable(stored, stored); err != nil {
		t.Fatalf("identical topology should be allowed: %v", err)
	}
	reordered := stored
	reordered.Peers = []string{"2/1@10.0.0.13:9000", "1/2@10.0.0.12:9000"}
	if err := checkClickHouseTopologyImmutable(stored, reordered); err != nil {
		t.Errorf("normalized peers should compare equal: %v", err)
	}
	changes := map[string]func(*config.ClickHouseConfig){
		"cluster":          func(c *config.ClickHouseConfig) { c.Cluster = "other" },
		"shard":            func(c *config.ClickHouseConfig) { c.Shard = 2 },
		"replica":          func(c *config.ClickHouseConfig) { c.Replica = 2 },
		"advertise-host":   func(c *config.ClickHouseConfig) { c.AdvertiseHost = "10.0.0.99" },
		"tcp-port":         func(c *config.ClickHouseConfig) { c.TCPPort = 19000 },
		"interserver-port": func(c *config.ClickHouseConfig) { c.InterserverPort = 19009 },
		"peer":             func(c *config.ClickHouseConfig) { c.Peers = []string{"1/2@10.0.0.13:9000"} },
		"keeper":           func(c *config.ClickHouseConfig) { c.Keepers = []string{"10.0.0.12:9181"} },
	}
	for name, change := range changes {
		next := stored
		change(&next)
		if err := checkClickHouseTopologyImmutable(stored, next); err == nil {
			t.Errorf("changing %s must be rejected", name)
		}
	}
}

func TestCheckClickHouseNoOpFlags(t *testing.T) {
	stored := config.ClickHouseConfig{
		Name: "ch1", Cluster: "chcluster", Shard: 1, Replica: 1,
		AdvertiseHost: "10.0.0.11",
		Peers:         []string{"1/2@10.0.0.12:9000", "2/1@10.0.0.13:9000"},
		Keepers:       []string{"10.0.0.11:9181", "10.0.0.12"},
		Settings:      map[string]string{"logger.level": "warning"},
	}
	same := clickHouseInstallOptions{
		cluster: "chcluster", clusterSet: true,
		shard: 1, shardSet: true, replica: 1, replicaSet: true,
		advertiseHost: "10.0.0.11", advertiseHostSet: true,
		peers: []string{"2/1@10.0.0.13:9000", "1/2@10.0.0.12:9000"}, peersSet: true,
		keepers: []string{"10.0.0.12:9181", "10.0.0.11:9181"}, keepersSet: true,
		configOptions: []string{"logger.level=warning"}, configOptionSet: true,
	}
	if err := checkClickHouseNoOpFlags(stored, same); err != nil {
		t.Fatalf("reordered peers/keepers should be a no-op: %v", err)
	}
	drift := same
	drift.shard = 2
	if err := checkClickHouseNoOpFlags(stored, drift); err == nil ||
		!strings.Contains(err.Error(), "--shard") {
		t.Errorf("changed --shard must be reported, got %v", err)
	}
}

func TestClickHouseClusterStatusParsers(t *testing.T) {
	members, err := parseClickHouseClusterMembers("1\t1\t10.0.0.11\t9000\t1\n1\t2\t10.0.0.12\t9000\t0\n")
	if err != nil {
		t.Fatalf("parse members: %v", err)
	}
	if len(members) != 2 || !members[0].Local || members[1].Local {
		t.Errorf("unexpected members: %+v", members)
	}
	if _, err := parseClickHouseClusterMembers("1\t1\t10.0.0.11\n"); err == nil {
		t.Error("short system.clusters rows must fail")
	}

	replicas, err := parseClickHouseReplicaStates("default\tevents\tch1-1\t0\t0\t0\ndefault\tevents\tch1-2\t1\t3\t7\n")
	if err != nil {
		t.Fatalf("parse replicas: %v", err)
	}
	if len(replicas) != 2 || replicas[1].Readonly != true || replicas[1].QueueSize != 7 {
		t.Errorf("unexpected replicas: %+v", replicas)
	}
	if _, err := parseClickHouseReplicaStates("default\tevents\tch1-1\t0\tx\t0\n"); err == nil {
		t.Error("invalid absolute_delay must fail")
	}
}
