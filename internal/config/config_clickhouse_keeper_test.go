package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func clusteredKeeperConfig() *Config {
	cfg := Default()
	cfg.BaseDir = "/srv/pg"
	cfg.Addons.ClickHouseKeeper = map[string]ClickHouseKeeperConfig{
		"keeper": {
			ServerID:      1,
			AdvertiseHost: "10.241.20.147",
			Listen:        "0.0.0.0",
			Peers:         []string{"2@10.241.20.148", "3@10.241.20.153"},
		},
	}
	cfg.ApplyDefaults()
	return cfg
}

func TestClickHouseKeeperApplyDefaults(t *testing.T) {
	cfg := clusteredKeeperConfig()
	kc := cfg.Addons.ClickHouseKeeper["keeper"]
	if kc.Version != DefaultClickHouseKeeperVersion {
		t.Errorf("Version = %q, want %q", kc.Version, DefaultClickHouseKeeperVersion)
	}
	if kc.ImageTag != DefaultClickHouseKeeperImageTag {
		t.Errorf("ImageTag = %q, want %q", kc.ImageTag, DefaultClickHouseKeeperImageTag)
	}
	if kc.ContainerName != "pgcli-clickhouse-keeper-keeper" {
		t.Errorf("ContainerName = %q", kc.ContainerName)
	}
	if kc.ClientPort != DefaultClickHouseKeeperClientPort || kc.RaftPort != DefaultClickHouseKeeperRaftPort {
		t.Errorf("ports = %d/%d", kc.ClientPort, kc.RaftPort)
	}
	dir := filepath.Join("/srv/pg", "addon", "clickhouse-keeper", "keeper")
	if kc.InstanceDir != dir {
		t.Errorf("InstanceDir = %q, want %q", kc.InstanceDir, dir)
	}
	if kc.ConfigPath != filepath.Join(dir, "config", "keeper_config.xml") {
		t.Errorf("ConfigPath = %q", kc.ConfigPath)
	}
	if kc.DataDir != filepath.Join(dir, "data") || kc.LogDir != filepath.Join(dir, "logs") {
		t.Errorf("data/log = %q/%q", kc.DataDir, kc.LogDir)
	}

	single := Default()
	single.Addons.ClickHouseKeeper = map[string]ClickHouseKeeperConfig{"solo": {}}
	single.ApplyDefaults()
	solo := single.Addons.ClickHouseKeeper["solo"]
	if solo.Listen != "127.0.0.1" || solo.AdvertiseHost != "127.0.0.1" {
		t.Errorf("standalone listen/advertise = %q/%q", solo.Listen, solo.AdvertiseHost)
	}
}

func TestClickHouseKeeperVoters(t *testing.T) {
	kc := ClickHouseKeeperConfig{
		ServerID:      2,
		AdvertiseHost: "10.0.0.2",
		RaftPort:      9234,
		Peers:         []string{"3@10.0.0.3", "1@10.0.0.1:9235"},
	}
	voters, err := ClickHouseKeeperVoters(kc)
	if err != nil {
		t.Fatalf("ClickHouseKeeperVoters: %v", err)
	}
	want := []string{"1@10.0.0.1:9235", "2@10.0.0.2:9234", "3@10.0.0.3:9234"}
	if len(voters) != len(want) {
		t.Fatalf("voters = %v, want %v", voters, want)
	}
	for i, voter := range voters {
		if voter.String() != want[i] {
			t.Errorf("voters[%d] = %s, want %s", i, voter, want[i])
		}
	}

	solo, err := ClickHouseKeeperVoters(ClickHouseKeeperConfig{
		ServerID:      1,
		AdvertiseHost: "127.0.0.1",
		RaftPort:      9234,
	})
	if err != nil {
		t.Fatalf("standalone: %v", err)
	}
	if len(solo) != 1 || solo[0].String() != "1@127.0.0.1:9234" {
		t.Errorf("standalone voters = %v", solo)
	}

	bad := []struct {
		name    string
		kc      ClickHouseKeeperConfig
		wantSub string
	}{
		{"self id", ClickHouseKeeperConfig{ServerID: 1, AdvertiseHost: "10.0.0.1", RaftPort: 9234, Peers: []string{"1@10.0.0.2"}}, "repeats this node"},
		{"duplicate id", ClickHouseKeeperConfig{ServerID: 1, AdvertiseHost: "10.0.0.1", RaftPort: 9234, Peers: []string{"2@10.0.0.2", "2@10.0.0.3"}}, "server id 2"},
		{"duplicate host", ClickHouseKeeperConfig{ServerID: 1, AdvertiseHost: "10.0.0.1", RaftPort: 9234, Peers: []string{"2@10.0.0.2", "3@10.0.0.2:9235"}}, "share host"},
		{"loopback advertise", ClickHouseKeeperConfig{ServerID: 1, AdvertiseHost: "127.0.0.1", RaftPort: 9234, Peers: []string{"2@10.0.0.2"}}, "non-loopback"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ClickHouseKeeperVoters(tt.kc)
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("error = %v, want %q", err, tt.wantSub)
			}
		})
	}
}

func TestParseClickHouseKeeperPeer(t *testing.T) {
	got, err := ParseClickHouseKeeperPeer("2@10.0.0.2", 9234)
	if err != nil {
		t.Fatalf("ParseClickHouseKeeperPeer: %v", err)
	}
	if got.ID != 2 || got.Host != "10.0.0.2" || got.Port != 9234 {
		t.Errorf("peer = %+v", got)
	}

	bad := []struct {
		raw     string
		wantSub string
	}{
		{"10.0.0.2", "ID@HOST"},
		{"0@10.0.0.2", "server id"},
		{"2@host", "IP address"},
		{"2@0.0.0.0", "wildcard"},
		{"2@127.0.0.1", "loopback"},
		{"2@10.0.0.2:0", "port"},
	}
	for _, tt := range bad {
		_, err := ParseClickHouseKeeperPeer(tt.raw, 9234)
		if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
			t.Errorf("ParseClickHouseKeeperPeer(%q) error = %v, want %q", tt.raw, err, tt.wantSub)
		}
	}
}

func TestClickHouseKeeperValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{name: "valid"},
		{"missing server id", func(c *Config) {
			kc := c.Addons.ClickHouseKeeper["keeper"]
			kc.ServerID = 0
			c.Addons.ClickHouseKeeper["keeper"] = kc
		}, "server_id"},
		{"duplicate peer id", func(c *Config) {
			kc := c.Addons.ClickHouseKeeper["keeper"]
			kc.Peers = append(kc.Peers, "2@10.241.20.154")
			c.Addons.ClickHouseKeeper["keeper"] = kc
		}, "server id 2"},
		{"loopback listen", func(c *Config) {
			kc := c.Addons.ClickHouseKeeper["keeper"]
			kc.Listen = "127.0.0.1"
			c.Addons.ClickHouseKeeper["keeper"] = kc
		}, "must not be loopback"},
		{"same client and raft port", func(c *Config) {
			kc := c.Addons.ClickHouseKeeper["keeper"]
			kc.RaftPort = kc.ClientPort
			c.Addons.ClickHouseKeeper["keeper"] = kc
		}, "must differ"},
		{"data log overlap", func(c *Config) {
			kc := c.Addons.ClickHouseKeeper["keeper"]
			kc.LogDir = kc.DataDir
			c.Addons.ClickHouseKeeper["keeper"] = kc
		}, "must be separate"},
		{"relative data dir", func(c *Config) {
			kc := c.Addons.ClickHouseKeeper["keeper"]
			kc.DataDir = "data"
			c.Addons.ClickHouseKeeper["keeper"] = kc
		}, "absolute path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := clusteredKeeperConfig()
			if tt.mutate != nil {
				tt.mutate(cfg)
			}
			err := cfg.Validate()
			if tt.wantSub == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("Validate error = %v, want %q", err, tt.wantSub)
			}
		})
	}
}

func TestClickHouseKeeperPathsMustNotOverlapClickHouse(t *testing.T) {
	cfg := clusteredKeeperConfig()
	cfg.Addons.ClickHouse = map[string]ClickHouseConfig{"ch": {AdminPassword: "secret"}}
	cfg.ApplyDefaults()

	ch := cfg.Addons.ClickHouse["ch"]
	kc := cfg.Addons.ClickHouseKeeper["keeper"]
	kc.DataDir = ch.DataDir
	cfg.Addons.ClickHouseKeeper["keeper"] = kc

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "must live in paths disjoint") {
		t.Fatalf("Validate error = %v, want path isolation error", err)
	}
}

func TestClickHouseKeeperPortMustNotCollideWithClickHouse(t *testing.T) {
	cfg := clusteredKeeperConfig()
	cfg.Addons.ClickHouse = map[string]ClickHouseConfig{"ch": {AdminPassword: "secret"}}
	cfg.ApplyDefaults()

	ch := cfg.Addons.ClickHouse["ch"]
	kc := cfg.Addons.ClickHouseKeeper["keeper"]
	kc.ClientPort = ch.HTTPPort
	cfg.Addons.ClickHouseKeeper["keeper"] = kc

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("Validate error = %v, want port collision error", err)
	}
}

func TestClickHouseKeeperSaveLoadRoundTrip(t *testing.T) {
	cfg := clusteredKeeperConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	path := filepath.Join(t.TempDir(), "pg.yaml")
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	kc, ok := got.Addons.ClickHouseKeeper["keeper"]
	if !ok {
		t.Fatal("keeper missing after round-trip")
	}
	if kc.ServerID != 1 || kc.AdvertiseHost != "10.241.20.147" || len(kc.Peers) != 2 {
		t.Errorf("identity lost: %+v", kc)
	}
	if kc.ClientPort != DefaultClickHouseKeeperClientPort || kc.RaftPort != DefaultClickHouseKeeperRaftPort {
		t.Errorf("ports lost: %+v", kc)
	}
	if kc.InstanceDir == "" || kc.ConfigPath == "" || kc.DataDir == "" || kc.LogDir == "" {
		t.Errorf("paths lost: %+v", kc)
	}
}
