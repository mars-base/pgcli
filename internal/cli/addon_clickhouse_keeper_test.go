package cli

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func TestClickHouseKeeperFlagName(t *testing.T) {
	cmd := addonClickHouseKeeperStatusCmd
	if got := clickHouseKeeperFlagName(cmd); got != "clickhouse-keeper" {
		t.Errorf("default name = %q, want clickhouse-keeper", got)
	}
	_ = cmd.Flags().Set("name", "keeper")
	t.Cleanup(func() { _ = cmd.Flags().Set("name", "") })
	if got := clickHouseKeeperFlagName(cmd); got != "keeper" {
		t.Errorf("name = %q, want keeper", got)
	}
}

func TestApplyClickHouseKeeperInstallFlags(t *testing.T) {
	dir := t.TempDir()
	kc := config.ClickHouseKeeperConfig{Name: "keeper"}
	opts := clickHouseKeeperInstallOptions{
		serverID: 2, serverIDSet: true,
		advertiseHost: "10.0.0.12", advertiseHostSet: true,
		peers: []string{"1@10.0.0.11", "3@10.0.0.13"}, peersSet: true,
		clientPort: 9181, clientPortSet: true,
		raftPort: 9234, raftPortSet: true,
		listen: "0.0.0.0", listenSet: true,
		dataDir: dir + "/data", dataDirSet: true,
		logDir: dir + "/logs", logDirSet: true,
		configDir: dir + "/config", configDirSet: true,
	}
	if err := applyClickHouseKeeperInstallFlags(&kc, opts); err != nil {
		t.Fatalf("applyClickHouseKeeperInstallFlags: %v", err)
	}
	if kc.Version != config.DefaultClickHouseKeeperVersion || kc.ImageTag != config.DefaultClickHouseKeeperImageTag {
		t.Errorf("version/image = %q/%q, want defaults", kc.Version, kc.ImageTag)
	}
	if kc.ServerID != 2 || kc.AdvertiseHost != "10.0.0.12" || kc.RaftPort != 9234 || kc.ClientPort != 9181 {
		t.Errorf("identity flags not applied: %+v", kc)
	}
	if kc.ConfigPath != dir+"/config/keeper_config.xml" {
		t.Errorf("config path = %q", kc.ConfigPath)
	}

	bad := clickHouseKeeperInstallOptions{version: "bad version", versionSet: true}
	if err := applyClickHouseKeeperInstallFlags(&config.ClickHouseKeeperConfig{Name: "k"}, bad); err == nil {
		t.Error("invalid version must error")
	}
}

func TestClickHouseKeeperPeerSetsEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want bool
	}{
		{"order insensitive", []string{"2@10.0.0.2:9234", "3@10.0.0.3"}, []string{"3@10.0.0.3:9234", "2@10.0.0.2"}, true},
		{"default raft port", []string{"2@10.0.0.2"}, []string{"2@10.0.0.2:9234"}, true},
		{"different id", []string{"2@10.0.0.2"}, []string{"3@10.0.0.2"}, false},
		{"different host", []string{"2@10.0.0.2"}, []string{"2@10.0.0.9"}, false},
		{"duplicate", []string{"2@10.0.0.2", "2@10.0.0.2"}, []string{"2@10.0.0.2"}, false},
		{"invalid", []string{"nope"}, []string{"2@10.0.0.2"}, false},
	}
	for _, tt := range tests {
		if got := clickHouseKeeperPeerSetsEqual(tt.a, tt.b); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestCheckClickHouseKeeperIdentityImmutable(t *testing.T) {
	stored := config.ClickHouseKeeperConfig{
		Name: "keeper", ServerID: 1, AdvertiseHost: "10.0.0.11",
		ClientPort: 9181, RaftPort: 9234,
		Peers: []string{"2@10.0.0.12:9234", "3@10.0.0.13:9234"},
	}
	if err := checkClickHouseKeeperIdentityImmutable(stored, stored); err != nil {
		t.Fatalf("identical config should be allowed: %v", err)
	}
	next := stored
	next.Peers = []string{"3@10.0.0.13", "2@10.0.0.12"}
	if err := checkClickHouseKeeperIdentityImmutable(stored, next); err != nil {
		t.Errorf("reordered peers should be allowed: %v", err)
	}
	changes := map[string]func(*config.ClickHouseKeeperConfig){
		"server-id":      func(c *config.ClickHouseKeeperConfig) { c.ServerID = 2 },
		"advertise-host": func(c *config.ClickHouseKeeperConfig) { c.AdvertiseHost = "10.0.0.99" },
		"client-port":    func(c *config.ClickHouseKeeperConfig) { c.ClientPort = 19181 },
		"raft-port":      func(c *config.ClickHouseKeeperConfig) { c.RaftPort = 19234 },
		"peer":           func(c *config.ClickHouseKeeperConfig) { c.Peers = []string{"2@10.0.0.12"} },
	}
	for name, change := range changes {
		next := stored
		change(&next)
		if err := checkClickHouseKeeperIdentityImmutable(stored, next); err == nil {
			t.Errorf("changing %s must be rejected", name)
		}
	}
}

func TestValidateClickHouseKeeperInstall(t *testing.T) {
	fresh := clickHouseKeeperInstallOptions{
		serverIDSet: true, advertiseHostSet: true,
	}
	single := config.ClickHouseKeeperConfig{
		Name: "keeper", ServerID: 1, AdvertiseHost: "10.0.0.11",
		Listen: "127.0.0.1", ClientPort: 9181, RaftPort: 9234,
		Peers: nil,
	}
	if err := validateClickHouseKeeperInstall(single, fresh, false); err != nil {
		t.Errorf("single-member fresh install should pass: %v", err)
	}

	if err := validateClickHouseKeeperInstall(single, clickHouseKeeperInstallOptions{}, false); err == nil ||
		!strings.Contains(err.Error(), "--server-id") || !strings.Contains(err.Error(), "--advertise-host") {
		t.Errorf("fresh install must require --server-id and --advertise-host, got %v", err)
	}

	cluster := single
	cluster.Listen = "0.0.0.0"
	cluster.Peers = []string{"2@10.0.0.12", "3@10.0.0.13"}
	if err := validateClickHouseKeeperInstall(cluster, fresh, false); err != nil {
		t.Errorf("multi-member install should pass: %v", err)
	}

	loopback := cluster
	loopback.Listen = "127.0.0.1"
	if err := validateClickHouseKeeperInstall(loopback, fresh, false); err == nil {
		t.Error("multi-member install with loopback listen must fail")
	}

	if err := validateClickHouseKeeperInstall(single, clickHouseKeeperInstallOptions{clusterSet: true}, false); err == nil {
		t.Error("--cluster must be rejected for clickhouse-keeper")
	}
}

func TestClickHouseKeeperCommandTree(t *testing.T) {
	for _, name := range []string{"status", "shell", "config"} {
		found := false
		for _, c := range addonClickHouseKeeperCmd.Commands() {
			if c.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("pg addon clickhouse-keeper is missing the %q subcommand", name)
		}
	}
	if addonClickHouseKeeperConfigCmd.Commands() == nil {
		t.Error("clickhouse-keeper config has no subcommands")
	}
	showFound := false
	for _, c := range addonClickHouseKeeperConfigCmd.Commands() {
		if c.Name() == "show" {
			showFound = true
			break
		}
	}
	if !showFound {
		t.Error("clickhouse-keeper config is missing 'show'")
	}

	required := []string{
		"name", "version", "server-id", "advertise-host", "peer",
		"client-port", "raft-port", "listen", "data-dir", "log-dir",
		"config-dir", "force",
	}
	for _, name := range required {
		if addonInstallCmd.Flags().Lookup(name) == nil {
			t.Errorf("addon install is missing --%s", name)
		}
	}
}
