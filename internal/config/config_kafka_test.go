package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestKafkaApplyDefaults(t *testing.T) {
	cfg := Default()
	cfg.BaseDir = "/srv/pg"
	cfg.Addons.Kafka = map[string]KafkaConfig{
		"analytics": {},
	}
	cfg.ApplyDefaults()

	kc := cfg.Addons.Kafka["analytics"]
	if kc.Name != "analytics" {
		t.Errorf("Name = %q, want analytics", kc.Name)
	}
	if kc.Version != DefaultKafkaVersion {
		t.Errorf("Version = %q, want %q", kc.Version, DefaultKafkaVersion)
	}
	if kc.ImageTag != DefaultKafkaImageTag {
		t.Errorf("ImageTag = %q, want %q", kc.ImageTag, DefaultKafkaImageTag)
	}
	if kc.ContainerName != "pgcli-kafka-analytics" {
		t.Errorf("ContainerName = %q, want pgcli-kafka-analytics", kc.ContainerName)
	}
	if kc.Listen != "127.0.0.1" || kc.AdvertiseHost != "127.0.0.1" {
		t.Errorf("Listen/AdvertiseHost = %q/%q, want 127.0.0.1/127.0.0.1", kc.Listen, kc.AdvertiseHost)
	}
	if kc.NodeID != 1 {
		t.Errorf("NodeID = %d, want 1", kc.NodeID)
	}
	dir := filepath.Join("/srv/pg", "addon", "kafka", "analytics")
	if kc.ConfigPath != filepath.Join(dir, "config", "server.properties") {
		t.Errorf("ConfigPath = %q", kc.ConfigPath)
	}
	if kc.DataDir != filepath.Join(dir, "data") {
		t.Errorf("DataDir = %q", kc.DataDir)
	}
	if kc.BrokerPort < cfg.KafkaBrokerStartPort {
		t.Errorf("BrokerPort = %d, want >= %d", kc.BrokerPort, cfg.KafkaBrokerStartPort)
	}
	if kc.ControllerPort < cfg.KafkaControllerStartPort {
		t.Errorf("ControllerPort = %d, want >= %d", kc.ControllerPort, cfg.KafkaControllerStartPort)
	}
}

func TestKafkaContainerNameFollowsNamespace(t *testing.T) {
	cfg := Default()
	cfg.Namespace = "dev"
	cfg.Addons.Kafka = map[string]KafkaConfig{"k1": {}}
	cfg.ApplyDefaults()

	if got := cfg.Addons.Kafka["k1"].ContainerName; got != "pgcli-kafka-dev-k1" {
		t.Errorf("ContainerName = %q, want pgcli-kafka-dev-k1", got)
	}
}

func TestKafkaClusterDefaultsDeriveClusterID(t *testing.T) {
	cfg := Default()
	cfg.Addons.Kafka = map[string]KafkaConfig{
		"k1": {
			Cluster:       "prod",
			Listen:        "0.0.0.0",
			AdvertiseHost: "10.241.20.148",
			NodeID:        2,
			Peers:         []string{"1@10.241.20.147:19093"},
		},
	}
	cfg.ApplyDefaults()

	kc := cfg.Addons.Kafka["k1"]
	if kc.ClusterID != DeriveKafkaClusterID("prod") {
		t.Errorf("ClusterID = %q, want derived %q", kc.ClusterID, DeriveKafkaClusterID("prod"))
	}
	if kc.NodeID != 2 {
		t.Errorf("NodeID = %d, want 2 (cluster mode must not default to 1)", kc.NodeID)
	}
}

func TestKafkaPortPoolsAreSeparate(t *testing.T) {
	cfg := Default()
	cfg.KafkaBrokerStartPort = 28092
	cfg.KafkaControllerStartPort = 29093
	cfg.Addons.Kafka = map[string]KafkaConfig{
		"b": {},
		"a": {},
	}
	cfg.ApplyDefaults()

	a := cfg.Addons.Kafka["a"]
	b := cfg.Addons.Kafka["b"]
	for name, port := range map[string]int{"a.broker": a.BrokerPort, "b.broker": b.BrokerPort} {
		if port < cfg.KafkaBrokerStartPort {
			t.Errorf("%s = %d, want >= %d", name, port, cfg.KafkaBrokerStartPort)
		}
	}
	for name, port := range map[string]int{"a.controller": a.ControllerPort, "b.controller": b.ControllerPort} {
		if port < cfg.KafkaControllerStartPort {
			t.Errorf("%s = %d, want >= %d", name, port, cfg.KafkaControllerStartPort)
		}
	}
	seen := map[int]bool{}
	for _, p := range []int{a.BrokerPort, a.ControllerPort, b.BrokerPort, b.ControllerPort} {
		if seen[p] {
			t.Errorf("port %d assigned twice", p)
		}
		seen[p] = true
	}
}

func TestKafkaPortPoolsShareAssignedSet(t *testing.T) {
	cfg := Default()
	cfg.KafkaBrokerStartPort = 28000
	cfg.KafkaControllerStartPort = 28005
	cfg.Addons.Kafka = map[string]KafkaConfig{
		// The explicit controller port sits on the broker pool's first port;
		// broker allocation must skip it.
		"a": {ControllerPort: 28000},
		"b": {},
	}
	cfg.ApplyDefaults()

	a := cfg.Addons.Kafka["a"]
	b := cfg.Addons.Kafka["b"]
	if a.BrokerPort == 28000 {
		t.Errorf("a.BrokerPort = 28000, collides with a.ControllerPort")
	}
	if b.BrokerPort == 28000 {
		t.Errorf("b.BrokerPort = 28000, collides with a.ControllerPort")
	}
	if b.ControllerPort < cfg.KafkaControllerStartPort {
		t.Errorf("b.ControllerPort = %d, want >= %d", b.ControllerPort, cfg.KafkaControllerStartPort)
	}
}

func TestKafkaSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	cfg := Default()
	cfg.BaseDir = dir
	cfg.KafkaBrokerStartPort = 29092
	cfg.KafkaControllerStartPort = 29093
	cfg.Addons.Kafka = map[string]KafkaConfig{
		"k1": {
			Listen:        "0.0.0.0",
			AdvertiseHost: "10.0.0.5",
			ClusterID:     DeriveKafkaClusterID("seed"),
			Settings:      map[string]string{"num.partitions": "3"},
			Autostart:     true,
		},
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.KafkaBrokerStartPort != 29092 || got.KafkaControllerStartPort != 29093 {
		t.Errorf("start ports = %d/%d, want 29092/29093", got.KafkaBrokerStartPort, got.KafkaControllerStartPort)
	}
	kc, ok := got.Addons.Kafka["k1"]
	if !ok {
		t.Fatal("k1 missing after round-trip")
	}
	if kc.ClusterID != DeriveKafkaClusterID("seed") || !kc.Autostart {
		t.Errorf("round-trip lost fields: %+v", kc)
	}
	if kc.Listen != "0.0.0.0" || kc.AdvertiseHost != "10.0.0.5" {
		t.Errorf("Listen/AdvertiseHost = %q/%q", kc.Listen, kc.AdvertiseHost)
	}
	if kc.Settings["num.partitions"] != "3" {
		t.Errorf("Settings = %v", kc.Settings)
	}
	want := cfg.Addons.Kafka["k1"]
	if kc.ConfigPath != want.ConfigPath || kc.DataDir != want.DataDir {
		t.Errorf("paths = %q/%q, want %q/%q", kc.ConfigPath, kc.DataDir, want.ConfigPath, want.DataDir)
	}
	if kc.BrokerPort != want.BrokerPort || kc.ControllerPort != want.ControllerPort {
		t.Errorf("ports = %d/%d, want %d/%d", kc.BrokerPort, kc.ControllerPort, want.BrokerPort, want.ControllerPort)
	}
}

func TestKafkaValidate(t *testing.T) {
	valid := func() *Config {
		cfg := Default()
		cfg.BaseDir = "/srv/pg"
		cfg.Addons.Kafka = map[string]KafkaConfig{
			"kafka": {ClusterID: DeriveKafkaClusterID("test")},
		}
		cfg.ApplyDefaults()
		return cfg
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{name: "valid"},
		{
			name: "valid cluster",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.Cluster = "prod"
				kc.ClusterID = DeriveKafkaClusterID("prod")
				kc.Listen = "0.0.0.0"
				kc.AdvertiseHost = "10.241.20.147"
				kc.NodeID = 1
				kc.Peers = []string{"2@10.241.20.148:19093", "3@10.241.20.153:19093"}
				c.Addons.Kafka["kafka"] = kc
			},
		},
		{
			name: "valid TLS",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.TLS = true
				kc.TLSPort = 39093
				c.Addons.Kafka["kafka"] = kc
			},
		},
		{
			name: "valid BYO TLS PEM",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.TLS = true
				kc.TLSPort = 39093
				kc.TLSPEM = "/etc/pgcli/kafka/kafka-server.pem"
				c.Addons.Kafka["kafka"] = kc
			},
		},
		{
			name: "tls_pem without tls",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.TLSPEM = "/etc/pgcli/kafka/kafka-server.pem"
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "requires tls: true",
		},
		{
			name: "tls_pem relative path",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.TLS = true
				kc.TLSPort = 39093
				kc.TLSPEM = "kafka-server.pem"
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "must be an absolute path",
		},
		{
			name: "invalid name",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.Name = "../evil"
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "must match",
		},
		{
			name: "unsafe version",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.Version = "4.4/../../evil"
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "safe Docker image tag",
		},
		{
			name: "image tag mismatch",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.ImageTag = "docker.io/apache/kafka:latest"
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "does not match version",
		},
		{
			name: "relative data dir",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.DataDir = "data"
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "absolute path",
		},
		{
			name: "invalid listen",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.Listen = "localhost"
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "must be an IP address",
		},
		{
			name: "wildcard advertise host",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.AdvertiseHost = "0.0.0.0"
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "concrete IP address",
		},
		{
			name: "negative node id",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.NodeID = -1
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "node_id must be >= 0",
		},
		{
			name: "invalid cluster id",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.ClusterID = "short"
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "22-character",
		},
		{
			name: "managed setting",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.Settings = map[string]string{"listeners": "PLAINTEXT://0.0.0.0:9092"}
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "managed by pgcli",
		},
		{
			name: "peers without cluster",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.Peers = []string{"2@10.0.0.2:19093"}
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "peers require cluster mode",
		},
		{
			name: "cluster loopback advertise",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.Cluster = "prod"
				kc.ClusterID = DeriveKafkaClusterID("prod")
				kc.Peers = []string{"2@10.0.0.2:19093"}
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "must not be loopback in cluster mode",
		},
		{
			name: "cluster without peers",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.Cluster = "prod"
				kc.ClusterID = DeriveKafkaClusterID("prod")
				kc.Listen = "0.0.0.0"
				kc.AdvertiseHost = "10.0.0.1"
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "at least one --peer",
		},
		{
			name: "valid two-voter cluster",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.Cluster = "pair"
				kc.ClusterID = DeriveKafkaClusterID("pair")
				kc.Listen = "0.0.0.0"
				kc.AdvertiseHost = "10.0.0.1"
				kc.NodeID = 1
				kc.Peers = []string{"2@10.0.0.2:19093"}
				c.Addons.Kafka["kafka"] = kc
			},
		},
		{
			name: "cluster node id zero",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.Cluster = "prod"
				kc.ClusterID = DeriveKafkaClusterID("prod")
				kc.Listen = "0.0.0.0"
				kc.AdvertiseHost = "10.0.0.1"
				kc.NodeID = 0
				kc.Peers = []string{"2@10.0.0.2:19093"}
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "must be >= 1 in cluster mode",
		},
		{
			name: "cluster id does not match derived id",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.Cluster = "prod"
				kc.ClusterID = DeriveKafkaClusterID("other")
				kc.Listen = "0.0.0.0"
				kc.AdvertiseHost = "10.0.0.1"
				kc.Peers = []string{"2@10.0.0.2:19093"}
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "does not match the id derived",
		},
		{
			name: "cluster concrete listen differs from advertise",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.Cluster = "prod"
				kc.ClusterID = DeriveKafkaClusterID("prod")
				kc.Listen = "10.0.0.9"
				kc.AdvertiseHost = "10.0.0.1"
				kc.Peers = []string{"2@10.0.0.2:19093"}
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "is not the advertised address",
		},
		{
			name: "same broker and controller port",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.ControllerPort = kc.BrokerPort
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "must differ",
		},
		{
			name: "duplicate port across instances",
			mutate: func(c *Config) {
				first := c.Addons.Kafka["kafka"]
				second := first
				second.Name = "second"
				second.ContainerName = "pgcli-kafka-second"
				second.BrokerPort = first.BrokerPort
				c.Addons.Kafka["second"] = second
			},
			wantSub: "already used",
		},
		{
			name: "tls port equals broker port",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.TLS = true
				kc.TLSPort = kc.BrokerPort
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "must differ from broker_port",
		},
		{
			name: "duplicate tls port across instances",
			mutate: func(c *Config) {
				first := c.Addons.Kafka["kafka"]
				first.TLS = true
				if first.TLSPort == 0 {
					first.TLSPort = first.BrokerPort + 100
				}
				c.Addons.Kafka["kafka"] = first
				second := first
				second.Name = "second"
				second.ContainerName = "pgcli-kafka-second"
				c.Addons.Kafka["second"] = second
			},
			wantSub: "already used",
		},
		{
			name: "port out of range",
			mutate: func(c *Config) {
				kc := c.Addons.Kafka["kafka"]
				kc.BrokerPort = 70000
				c.Addons.Kafka["kafka"] = kc
			},
			wantSub: "between 1 and 65535",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid()
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
				t.Fatalf("Validate error = %v, want substring %q", err, tt.wantSub)
			}
		})
	}
}

func TestValidateKafkaSettings(t *testing.T) {
	valid := []map[string]string{
		nil,
		{"num.partitions": "3"},
		{"log.retention.hours": "168"},
		{"message.max.bytes": "1048576"},
	}
	for _, settings := range valid {
		if err := ValidateKafkaSettings(settings); err != nil {
			t.Errorf("ValidateKafkaSettings(%v): %v", settings, err)
		}
	}

	bad := []struct {
		settings map[string]string
		wantSub  string
	}{
		{map[string]string{"not a key": "1"}, "must contain only"},
		{map[string]string{"listeners": "PLAINTEXT://0.0.0.0:9092"}, "managed by pgcli"},
		{map[string]string{"ssl.keystore.location": "/tmp/server.pem"}, "managed by pgcli"},
		{map[string]string{"ssl.client.auth": "required"}, "managed by pgcli"},
		{map[string]string{"log.dirs": "/tmp"}, "managed by pgcli"},
		{map[string]string{"zookeeper.connect": "localhost:2181"}, "managed by pgcli"},
		{map[string]string{"default.replication.factor": "1"}, "managed by pgcli"},
		{map[string]string{"num.partitions": "3\nnum.partitions=4"}, "control characters"},
	}
	for _, tt := range bad {
		err := ValidateKafkaSettings(tt.settings)
		if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
			t.Errorf("ValidateKafkaSettings(%v) = %v, want %q", tt.settings, err, tt.wantSub)
		}
	}
}

func TestKafkaClusterIDHelpers(t *testing.T) {
	first := DeriveKafkaClusterID("prod")
	again := DeriveKafkaClusterID("prod")
	other := DeriveKafkaClusterID("staging")
	if first != again {
		t.Errorf("DeriveKafkaClusterID is not deterministic: %q vs %q", first, again)
	}
	if first == other {
		t.Errorf("different cluster names produced the same id %q", first)
	}
	if len(first) != 22 || !ValidKafkaClusterID(first) {
		t.Errorf("derived id %q is not a valid 22-char cluster id", first)
	}
	for _, bad := range []string{"", "short", strings.Repeat("a", 23), "aaaaaaaaaaaaaaaaaaaaa=", "aaaaaaaaaaaaaaaaaaaa/+", "!aaaaaaaaaaaaaaaaaaaaa"} {
		if ValidKafkaClusterID(bad) {
			t.Errorf("ValidKafkaClusterID(%q) = true, want false", bad)
		}
	}
}

func TestKafkaVoters(t *testing.T) {
	single := KafkaConfig{NodeID: 1, AdvertiseHost: "127.0.0.1", ControllerPort: 19093}
	voters, err := KafkaVoters(single)
	if err != nil {
		t.Fatalf("single-node voters: %v", err)
	}
	if len(voters) != 1 || voters[0].String() != "1@127.0.0.1:19093" {
		t.Errorf("single-node voters = %v", voters)
	}

	cluster := KafkaConfig{
		Cluster:        "prod",
		NodeID:         2,
		AdvertiseHost:  "10.0.0.2",
		ControllerPort: 19093,
		Peers:          []string{"3@[fd00::3]:19093", "1@10.0.0.1:19093"},
	}
	voters, err = KafkaVoters(cluster)
	if err != nil {
		t.Fatalf("cluster voters: %v", err)
	}
	got := make([]string, 0, len(voters))
	for _, voter := range voters {
		got = append(got, voter.String())
	}
	want := []string{"1@10.0.0.1:19093", "2@10.0.0.2:19093", "3@[fd00::3]:19093"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("cluster voters = %v, want %v", got, want)
	}

	bad := []struct {
		name string
		k    KafkaConfig
		sub  string
	}{
		{"peers in single mode", KafkaConfig{NodeID: 1, AdvertiseHost: "127.0.0.1", ControllerPort: 19093, Peers: []string{"2@10.0.0.2:19093"}}, "require cluster mode"},
		{"wildcard advertise", KafkaConfig{Cluster: "p", NodeID: 1, AdvertiseHost: "0.0.0.0", ControllerPort: 19093, Peers: []string{"2@10.0.0.2:19093"}}, "concrete address"},
		{"no peers", KafkaConfig{Cluster: "p", NodeID: 1, AdvertiseHost: "10.0.0.1", ControllerPort: 19093}, "at least one --peer"},
		{"self id", KafkaConfig{Cluster: "p", NodeID: 1, AdvertiseHost: "10.0.0.1", ControllerPort: 19093, Peers: []string{"1@10.0.0.9:19093"}}, "repeats this node"},
		{"duplicate ids", KafkaConfig{Cluster: "p", NodeID: 1, AdvertiseHost: "10.0.0.1", ControllerPort: 19093, Peers: []string{"2@10.0.0.2:19093", "2@10.0.0.3:19093"}}, "used by both"},
		{"hostname peer", KafkaConfig{Cluster: "p", NodeID: 1, AdvertiseHost: "10.0.0.1", ControllerPort: 19093, Peers: []string{"2@broker2:19093"}}, "must be an IP address"},
		{"wildcard peer", KafkaConfig{Cluster: "p", NodeID: 1, AdvertiseHost: "10.0.0.1", ControllerPort: 19093, Peers: []string{"2@0.0.0.0:19093"}}, "wildcard address"},
		{"peer id zero", KafkaConfig{Cluster: "p", NodeID: 1, AdvertiseHost: "10.0.0.1", ControllerPort: 19093, Peers: []string{"0@10.0.0.2:19093"}}, "1-based"},
		{"duplicate endpoint", KafkaConfig{Cluster: "p", NodeID: 1, AdvertiseHost: "10.0.0.1", ControllerPort: 19093, Peers: []string{"2@10.0.0.2:19093", "3@10.0.0.2:19093"}}, "share the controller endpoint"},
		{"peer repeats local endpoint", KafkaConfig{Cluster: "p", NodeID: 1, AdvertiseHost: "10.0.0.1", ControllerPort: 19093, Peers: []string{"2@10.0.0.1:19093"}}, "share the controller endpoint"},
		{"bad port", KafkaConfig{Cluster: "p", NodeID: 1, AdvertiseHost: "10.0.0.1", ControllerPort: 19093, Peers: []string{"2@10.0.0.2:70000"}}, "invalid port"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := KafkaVoters(tt.k); err == nil || !strings.Contains(err.Error(), tt.sub) {
				t.Fatalf("KafkaVoters error = %v, want %q", err, tt.sub)
			}
		})
	}
}

func TestKafkaReplicationFactorAndMinISR(t *testing.T) {
	for voters, want := range map[int]int{0: 1, 1: 1, 2: 2, 3: 3, 5: 3} {
		if got := KafkaReplicationFactor(voters); got != want {
			t.Errorf("KafkaReplicationFactor(%d) = %d, want %d", voters, got, want)
		}
	}
	for rf, want := range map[int]int{0: 1, 1: 1, 2: 2, 3: 2} {
		if got := KafkaMinISR(rf); got != want {
			t.Errorf("KafkaMinISR(%d) = %d, want %d", rf, got, want)
		}
	}
}

func TestKafkaTLSPorts(t *testing.T) {
	cfg := Default()
	cfg.KafkaBrokerStartPort = 29092
	cfg.KafkaControllerStartPort = 29093
	cfg.KafkaTLSStartPort = 29094
	cfg.Addons.Kafka = map[string]KafkaConfig{
		"a": {TLS: true},
		"b": {TLS: true},
		"c": {},
	}
	cfg.ApplyDefaults()

	a := cfg.Addons.Kafka["a"]
	b := cfg.Addons.Kafka["b"]
	c := cfg.Addons.Kafka["c"]
	if a.TLSPort < cfg.KafkaTLSStartPort || b.TLSPort < cfg.KafkaTLSStartPort {
		t.Errorf("TLS ports = %d/%d, want >= %d", a.TLSPort, b.TLSPort, cfg.KafkaTLSStartPort)
	}
	if c.TLSPort != 0 {
		t.Errorf("TLS-disabled instance has TLSPort %d, want 0", c.TLSPort)
	}
	seen := map[int]string{}
	for name, kc := range cfg.Addons.Kafka {
		for field, port := range map[string]int{"broker": kc.BrokerPort, "controller": kc.ControllerPort, "tls": kc.TLSPort} {
			if port == 0 {
				continue
			}
			if prev, dup := seen[port]; dup {
				t.Errorf("port %d assigned to both %s and %s.%s", port, prev, name, field)
			}
			seen[port] = name + "." + field
		}
		kc.ClusterID = DeriveKafkaClusterID("tls-" + name)
		cfg.Addons.Kafka[name] = kc
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestKafkaTLSPortClearedWhenDisabled(t *testing.T) {
	cfg := Default()
	cfg.Addons.Kafka = map[string]KafkaConfig{
		"k1": {TLSPort: 39093},
	}
	cfg.ApplyDefaults()
	if got := cfg.Addons.Kafka["k1"].TLSPort; got != 0 {
		t.Errorf("TLSPort = %d without TLS, want 0", got)
	}
}

func TestKafkaTLSPortAvoidsAlertmanager(t *testing.T) {
	cfg := Default()
	cfg.KafkaBrokerStartPort = 29092
	cfg.KafkaControllerStartPort = 29093
	cfg.Addons.Alertmanager = map[string]AlertmanagerConfig{
		"am": {HostPort: 9093},
	}
	cfg.Addons.Kafka = map[string]KafkaConfig{
		"k1": {TLS: true},
	}
	cfg.ApplyDefaults()
	if got := cfg.Addons.Kafka["k1"].TLSPort; got == 9093 {
		t.Error("Kafka TLS port 9093 collides with an explicit Alertmanager port")
	}
}

func TestParseKafkaHeap(t *testing.T) {
	valid := map[string]string{"512m": "512m", "4g": "4g", " 2G ": "2g", "": ""}
	for in, want := range valid {
		got, err := ParseKafkaHeap(in)
		if err != nil || got != want {
			t.Errorf("ParseKafkaHeap(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"4", "4gb", "0g", "-1g", "1.5g"} {
		if _, err := ParseKafkaHeap(in); err == nil {
			t.Errorf("ParseKafkaHeap(%q) should fail", in)
		}
	}
}
