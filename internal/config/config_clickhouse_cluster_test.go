package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func clusteredClickHouseConfig() *Config {
	cfg := Default()
	cfg.BaseDir = "/srv/pg"
	cfg.Addons.ClickHouse = map[string]ClickHouseConfig{
		"clickhouse": {
			Cluster:       "chcluster",
			Shard:         1,
			Replica:       1,
			AdvertiseHost: "10.241.20.147",
			Listen:        "0.0.0.0",
			Peers:         []string{"1/2@10.241.20.148:9000", "2/1@10.241.20.153:9000", "2/2@10.241.20.154:9000"},
			Keepers:       []string{"10.241.20.147:9181", "10.241.20.148:9181", "10.241.20.153:9181"},
			AdminPassword: "secret",
			HTTPPort:      18123,
			TCPPort:       19000,
		},
	}
	cfg.ApplyDefaults()
	return cfg
}

func TestClickHouseClusterApplyDefaults(t *testing.T) {
	cfg := clusteredClickHouseConfig()
	cc := cfg.Addons.ClickHouse["clickhouse"]
	if cc.InterserverPort != DefaultClickHouseInterserverPort {
		t.Errorf("InterserverPort = %d, want %d", cc.InterserverPort, DefaultClickHouseInterserverPort)
	}
	if !cc.Clustered() {
		t.Error("Clustered() = false, want true")
	}

	single := Default()
	single.Addons.ClickHouse = map[string]ClickHouseConfig{"a": {}}
	single.ApplyDefaults()
	if got := single.Addons.ClickHouse["a"].InterserverPort; got != 0 {
		t.Errorf("standalone InterserverPort = %d, want 0", got)
	}
}

func TestClickHousePortPoolsSkipFixedClusterPorts(t *testing.T) {
	cfg := Default()
	cfg.ClickHouseHTTPStartPort = 9180
	cfg.ClickHouseTCPStartPort = 9232
	cfg.Addons.ClickHouse = map[string]ClickHouseConfig{
		"a": {
			Cluster:       "c",
			Shard:         1,
			Replica:       1,
			AdvertiseHost: "10.0.0.1",
			Listen:        "0.0.0.0",
			Peers:         []string{"1/2@10.0.0.2:9000"},
			Keepers:       []string{"10.0.0.1:9181"},
		},
	}
	cfg.Addons.ClickHouseKeeper = map[string]ClickHouseKeeperConfig{"keeper": {}}
	cfg.ApplyDefaults()

	cc := cfg.Addons.ClickHouse["a"]
	for _, p := range []int{cc.HTTPPort, cc.TCPPort} {
		switch p {
		case DefaultClickHouseKeeperClientPort, DefaultClickHouseKeeperRaftPort, cc.InterserverPort:
			t.Errorf("ClickHouse allocated fixed port %d", p)
		}
	}
}

func TestParseClickHousePeer(t *testing.T) {
	valid := []struct {
		raw  string
		want ClickHousePeer
	}{
		{"1/2@10.0.0.2:9000", ClickHousePeer{Shard: 1, Replica: 2, Host: "10.0.0.2", Port: 9000}},
		{"3/4@192.168.1.10:19000", ClickHousePeer{Shard: 3, Replica: 4, Host: "192.168.1.10", Port: 19000}},
	}
	for _, tt := range valid {
		got, err := ParseClickHousePeer(tt.raw)
		if err != nil {
			t.Errorf("ParseClickHousePeer(%q): %v", tt.raw, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseClickHousePeer(%q) = %+v, want %+v", tt.raw, got, tt.want)
		}
	}

	bad := []struct {
		raw     string
		wantSub string
	}{
		{"1@10.0.0.2:9000", "SHARD/REPLICA"},
		{"1/2@10.0.0.2", "HOST:PORT"},
		{"0/2@10.0.0.2:9000", "shard"},
		{"1/0@10.0.0.2:9000", "replica"},
		{"1/2@host:9000", "IP address"},
		{"1/2@0.0.0.0:9000", "wildcard"},
		{"1/2@127.0.0.1:9000", "loopback"},
		{"1/2@10.0.0.2:70000", "port"},
	}
	for _, tt := range bad {
		_, err := ParseClickHousePeer(tt.raw)
		if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
			t.Errorf("ParseClickHousePeer(%q) error = %v, want %q", tt.raw, err, tt.wantSub)
		}
	}
}

func TestClickHouseTopology(t *testing.T) {
	cc := ClickHouseConfig{
		Cluster:       "c",
		Shard:         1,
		Replica:       1,
		AdvertiseHost: "10.0.0.1",
		TCPPort:       9000,
		Peers: []string{
			"2/2@10.0.0.4:9000",
			"1/2@10.0.0.2:9000",
			"2/1@10.0.0.3:9000",
		},
	}
	topology, err := ClickHouseTopology(cc)
	if err != nil {
		t.Fatalf("ClickHouseTopology: %v", err)
	}
	want := []string{
		"1/1@10.0.0.1:9000",
		"1/2@10.0.0.2:9000",
		"2/1@10.0.0.3:9000",
		"2/2@10.0.0.4:9000",
	}
	if len(topology) != len(want) {
		t.Fatalf("topology = %v, want %v", topology, want)
	}
	for i, peer := range topology {
		if peer.String() != want[i] {
			t.Errorf("topology[%d] = %s, want %s", i, peer, want[i])
		}
	}

	bad := []struct {
		name    string
		mutate  func(*ClickHouseConfig)
		wantSub string
	}{
		{"self repeated", func(c *ClickHouseConfig) {
			c.Peers = append(c.Peers, "1/1@10.0.0.9:9000")
		}, "repeats this node"},
		{"duplicate endpoint", func(c *ClickHouseConfig) {
			c.Peers = append(c.Peers, "3/1@10.0.0.2:9000")
		}, "share the endpoint"},
		{"non-uniform shards", func(c *ClickHouseConfig) {
			c.Peers = []string{"1/2@10.0.0.2:9000", "2/1@10.0.0.3:9000"}
		}, "same number of replicas"},
		{"replica gap", func(c *ClickHouseConfig) {
			c.Peers = []string{"1/3@10.0.0.2:9000", "2/1@10.0.0.3:9000", "2/2@10.0.0.4:9000"}
		}, "numbered 1..N"},
		{"no peers", func(c *ClickHouseConfig) {
			c.Peers = nil
		}, "at least one --peer"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			c := cc
			c.Peers = append([]string(nil), cc.Peers...)
			tt.mutate(&c)
			_, err := ClickHouseTopology(c)
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("error = %v, want %q", err, tt.wantSub)
			}
		})
	}
}

func TestClickHouseClusterValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{name: "valid"},
		{"invalid cluster name", func(c *Config) {
			cc := c.Addons.ClickHouse["clickhouse"]
			cc.Cluster = "1bad"
			c.Addons.ClickHouse["clickhouse"] = cc
		}, "cluster"},
		{"shard zero", func(c *Config) {
			cc := c.Addons.ClickHouse["clickhouse"]
			cc.Shard = 0
			c.Addons.ClickHouse["clickhouse"] = cc
		}, "shard and replica"},
		{"loopback advertise", func(c *Config) {
			cc := c.Addons.ClickHouse["clickhouse"]
			cc.AdvertiseHost = "127.0.0.1"
			c.Addons.ClickHouse["clickhouse"] = cc
		}, "advertise_host"},
		{"loopback listen", func(c *Config) {
			cc := c.Addons.ClickHouse["clickhouse"]
			cc.Listen = "127.0.0.1"
			c.Addons.ClickHouse["clickhouse"] = cc
		}, "must not be loopback"},
		{"missing keepers", func(c *Config) {
			cc := c.Addons.ClickHouse["clickhouse"]
			cc.Keepers = nil
			c.Addons.ClickHouse["clickhouse"] = cc
		}, "at least one --keeper"},
		{"keeper loopback", func(c *Config) {
			cc := c.Addons.ClickHouse["clickhouse"]
			cc.Keepers = []string{"127.0.0.1:9181"}
			c.Addons.ClickHouse["clickhouse"] = cc
		}, "loopback"},
		{"non-uniform topology", func(c *Config) {
			cc := c.Addons.ClickHouse["clickhouse"]
			cc.Peers = []string{"1/2@10.241.20.148:9000", "2/1@10.241.20.153:9000"}
			c.Addons.ClickHouse["clickhouse"] = cc
		}, "same number of replicas"},
		{"interserver equals tcp", func(c *Config) {
			cc := c.Addons.ClickHouse["clickhouse"]
			cc.InterserverPort = cc.TCPPort
			c.Addons.ClickHouse["clickhouse"] = cc
		}, "must differ"},
		{"half cluster flags", func(c *Config) {
			cc := c.Addons.ClickHouse["clickhouse"]
			cc.Cluster = ""
			cc.Peers = nil
			cc.Keepers = nil
			cc.Shard = 0
			cc.Replica = 0
			cc.AdvertiseHost = ""
			cc.InterserverPort = 0
			cc.Shard = 1
			c.Addons.ClickHouse["clickhouse"] = cc
			c.ApplyDefaults()
		}, "require cluster mode"},
		{"cluster managed setting", func(c *Config) {
			cc := c.Addons.ClickHouse["clickhouse"]
			cc.Settings = map[string]string{"remote_servers.chcluster.shard": "x"}
			c.Addons.ClickHouse["clickhouse"] = cc
		}, "managed by pgcli in cluster mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := clusteredClickHouseConfig()
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

func TestClickHouseClusterSaveLoadRoundTrip(t *testing.T) {
	cfg := clusteredClickHouseConfig()
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
	cc := got.Addons.ClickHouse["clickhouse"]
	if cc.Cluster != "chcluster" || cc.Shard != 1 || cc.Replica != 1 {
		t.Errorf("cluster identity lost: %+v", cc)
	}
	if cc.AdvertiseHost != "10.241.20.147" || len(cc.Peers) != 3 || len(cc.Keepers) != 3 {
		t.Errorf("cluster membership lost: %+v", cc)
	}
	if cc.InterserverPort != DefaultClickHouseInterserverPort {
		t.Errorf("InterserverPort = %d, want %d", cc.InterserverPort, DefaultClickHouseInterserverPort)
	}
}
