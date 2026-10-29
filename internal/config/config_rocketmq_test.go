package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func rocketMQStandaloneConfig() *Config {
	cfg := Default()
	cfg.BaseDir = "/srv/pg"
	cfg.Addons.RocketMQ = map[string]RocketMQConfig{"rmq": {}}
	cfg.ApplyDefaults()
	return cfg
}

func rocketMQClusterConfig() *Config {
	cfg := Default()
	cfg.BaseDir = "/srv/pg"
	cfg.Addons.RocketMQ = map[string]RocketMQConfig{
		"rmq": {
			Cluster:       "lab",
			AdvertiseHost: "10.0.0.1",
			ControllerID:  "n0",
			Peers:         []string{"n2@10.0.0.3", "n1@10.0.0.2"},
		},
	}
	cfg.ApplyDefaults()
	return cfg
}

func TestRocketMQApplyDefaults(t *testing.T) {
	cfg := rocketMQStandaloneConfig()
	rc := cfg.Addons.RocketMQ["rmq"]

	if rc.Name != "rmq" {
		t.Errorf("Name = %q, want rmq", rc.Name)
	}
	if rc.Version != DefaultRocketMQVersion {
		t.Errorf("Version = %q, want %q", rc.Version, DefaultRocketMQVersion)
	}
	if rc.ImageTag != DefaultRocketMQImageTag {
		t.Errorf("ImageTag = %q, want %q", rc.ImageTag, DefaultRocketMQImageTag)
	}
	if rc.ContainerName != "pgcli-rocketmq-rmq" {
		t.Errorf("ContainerName = %q, want pgcli-rocketmq-rmq", rc.ContainerName)
	}
	if rc.AdvertiseHost != "127.0.0.1" {
		t.Errorf("AdvertiseHost = %q, want 127.0.0.1", rc.AdvertiseHost)
	}
	if rc.Heap != DefaultRocketMQHeap || rc.NamesrvHeap != DefaultRocketMQNamesrvHeap || rc.ProxyHeap != DefaultRocketMQProxyHeap {
		t.Errorf("heaps = %q/%q/%q", rc.Heap, rc.NamesrvHeap, rc.ProxyHeap)
	}
	if rc.FileReservedTime != DefaultRocketMQFileReservedTime {
		t.Errorf("FileReservedTime = %d, want %d", rc.FileReservedTime, DefaultRocketMQFileReservedTime)
	}
	if rc.DeleteWhen != DefaultRocketMQDeleteWhen {
		t.Errorf("DeleteWhen = %q, want %q", rc.DeleteWhen, DefaultRocketMQDeleteWhen)
	}
	if rc.DiskMaxUsedSpaceRatio != DefaultRocketMQDiskMaxUsedSpaceRatio {
		t.Errorf("DiskMaxUsedSpaceRatio = %d, want %d", rc.DiskMaxUsedSpaceRatio, DefaultRocketMQDiskMaxUsedSpaceRatio)
	}
	if got := RocketMQBrokerAddr(rc); got != "127.0.0.1:10911" {
		t.Errorf("RocketMQBrokerAddr = %q", got)
	}
	dir := filepath.Join("/srv/pg", "addon", "rocketmq", "rmq")
	if rc.ConfigDir != filepath.Join(dir, "config") {
		t.Errorf("ConfigDir = %q", rc.ConfigDir)
	}
	if rc.DataDir != filepath.Join(dir, "data") {
		t.Errorf("DataDir = %q", rc.DataDir)
	}
	if rc.NamesrvPort < DefaultRocketMQNamesrvPort || rc.BrokerPort < DefaultRocketMQBrokerPort ||
		rc.ProxyPort < DefaultRocketMQProxyPort || rc.GRPCPort < DefaultRocketMQGRPCPort {
		t.Errorf("ports below defaults: %+v", rc)
	}
	if rc.ControllerPort != 0 {
		t.Errorf("ControllerPort = %d, want 0 for standalone", rc.ControllerPort)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestRocketMQContainerNameFollowsNamespace(t *testing.T) {
	cfg := Default()
	cfg.Namespace = "dev"
	cfg.Addons.RocketMQ = map[string]RocketMQConfig{"rmq": {}}
	cfg.ApplyDefaults()

	if got := cfg.Addons.RocketMQ["rmq"].ContainerName; got != "pgcli-rocketmq-dev-rmq" {
		t.Errorf("ContainerName = %q, want pgcli-rocketmq-dev-rmq", got)
	}
	if got := RocketMQContainerName(cfg.Addons.RocketMQ["rmq"], "broker"); got != "pgcli-rocketmq-dev-rmq-broker" {
		t.Errorf("broker container = %q", got)
	}
}

func TestRocketMQClusterApplyDefaults(t *testing.T) {
	cfg := rocketMQClusterConfig()
	rc := cfg.Addons.RocketMQ["rmq"]

	if rc.NamesrvPort != DefaultRocketMQNamesrvPort || rc.BrokerPort != DefaultRocketMQBrokerPort ||
		rc.ProxyPort != DefaultRocketMQProxyPort || rc.GRPCPort != DefaultRocketMQGRPCPort ||
		rc.ControllerPort != DefaultRocketMQControllerPort {
		t.Errorf("cluster ports = %d/%d/%d/%d/%d",
			rc.NamesrvPort, rc.BrokerPort, rc.ProxyPort, rc.GRPCPort, rc.ControllerPort)
	}
	want := []string{"n0@10.0.0.1", "n1@10.0.0.2", "n2@10.0.0.3"}
	if strings.Join(rc.Peers, ",") != strings.Join(want, ",") {
		t.Errorf("Peers = %v, want %v", rc.Peers, want)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestRocketMQTwoInstancesGetDistinctPorts(t *testing.T) {
	cfg := Default()
	cfg.BaseDir = "/srv/pg"
	cfg.Addons.RocketMQ = map[string]RocketMQConfig{
		"a": {},
		"b": {},
	}
	cfg.ApplyDefaults()

	a := cfg.Addons.RocketMQ["a"]
	b := cfg.Addons.RocketMQ["b"]
	ports := []int{
		a.NamesrvPort, a.BrokerPort, a.ProxyPort, a.GRPCPort,
		RocketMQVIPPort(a), RocketMQHAPort(a),
		b.NamesrvPort, b.BrokerPort, b.ProxyPort, b.GRPCPort,
		RocketMQVIPPort(b), RocketMQHAPort(b),
	}
	seen := map[int]bool{}
	for _, port := range ports {
		if seen[port] {
			t.Errorf("duplicate port %d across instances: %+v / %+v", port, a, b)
		}
		seen[port] = true
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestRocketMQExplicitPortsPreserved(t *testing.T) {
	cfg := Default()
	cfg.BaseDir = "/srv/pg"
	cfg.Addons.RocketMQ = map[string]RocketMQConfig{
		"a": {NamesrvPort: 19876, BrokerPort: 20911, ProxyPort: 18080, GRPCPort: 18081},
		"b": {},
	}
	cfg.ApplyDefaults()

	a := cfg.Addons.RocketMQ["a"]
	if a.NamesrvPort != 19876 || a.BrokerPort != 20911 || a.ProxyPort != 18080 || a.GRPCPort != 18081 {
		t.Errorf("explicit ports overwritten: %+v", a)
	}
	b := cfg.Addons.RocketMQ["b"]
	all := []int{a.NamesrvPort, a.BrokerPort, a.ProxyPort, a.GRPCPort, RocketMQVIPPort(a), RocketMQHAPort(a)}
	for _, p := range all {
		if b.NamesrvPort == p || b.BrokerPort == p || b.ProxyPort == p || b.GRPCPort == p ||
			RocketMQVIPPort(b) == p || RocketMQHAPort(b) == p {
			t.Errorf("instance b reused explicit port %d: %+v", p, b)
		}
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestRocketMQValidate(t *testing.T) {
	cases := []struct {
		name string
		edit func(*RocketMQConfig)
		want string
	}{
		{"version 4", func(rc *RocketMQConfig) { rc.Version = "4.9.0" }, "5.x"},
		{"version bare", func(rc *RocketMQConfig) { rc.Version = "5" }, "5.x"},
		{"version suffix", func(rc *RocketMQConfig) { rc.Version = "5.5.0-rc1" }, "5.x"},
		{"image mismatch", func(rc *RocketMQConfig) { rc.ImageTag = "docker.io/apache/rocketmq:5.4.0" }, "does not match version"},
		{"relative data dir", func(rc *RocketMQConfig) { rc.DataDir = "data" }, "absolute path"},
		{"bad heap", func(rc *RocketMQConfig) { rc.Heap = "512" }, "heap"},
		{"bad namesrv heap", func(rc *RocketMQConfig) { rc.NamesrvHeap = "0m" }, "heap"},
		{"bad setting key", func(rc *RocketMQConfig) { rc.Settings = map[string]string{"bad key": "v"} }, "settings key"},
		{"managed setting", func(rc *RocketMQConfig) { rc.Settings = map[string]string{"brokerRole": "SYNC_MASTER"} }, "managed by pgcli"},
		{"controller setting", func(rc *RocketMQConfig) { rc.Settings = map[string]string{"controllerAddr": "10.0.0.1:9877"} }, "managed by pgcli"},
		{"setting control chars", func(rc *RocketMQConfig) { rc.Settings = map[string]string{"flushDiskType": "a\nb"} }, "control characters"},
		{"hostname advertise", func(rc *RocketMQConfig) { rc.AdvertiseHost = "node1" }, "IPv4"},
		{"ipv6 advertise", func(rc *RocketMQConfig) { rc.AdvertiseHost = "fd00::1" }, "IPv4"},
		{"wildcard advertise", func(rc *RocketMQConfig) { rc.AdvertiseHost = "0.0.0.0" }, "IPv4"},
		{"standalone peers", func(rc *RocketMQConfig) { rc.Peers = []string{"n1@10.0.0.2"} }, "require cluster mode"},
		{"standalone controller id", func(rc *RocketMQConfig) { rc.ControllerID = "n0" }, "requires cluster mode"},
		{"standalone controller port", func(rc *RocketMQConfig) { rc.ControllerPort = 9877 }, "requires cluster mode"},
		{"port range", func(rc *RocketMQConfig) { rc.NamesrvPort = 70000 }, "between 1 and 65535"},
		{"port duplicate", func(rc *RocketMQConfig) { rc.ProxyPort = rc.GRPCPort }, "must be unique"},
		{"vip collision", func(rc *RocketMQConfig) { rc.ProxyPort = RocketMQVIPPort(*rc) }, "must be unique"},
		{"ha collision", func(rc *RocketMQConfig) { rc.GRPCPort = RocketMQHAPort(*rc) }, "must be unique"},
		{"retention hours low", func(rc *RocketMQConfig) { rc.FileReservedTime = 0 }, "file_reserved_time"},
		{"retention hours high", func(rc *RocketMQConfig) { rc.FileReservedTime = 8761 }, "file_reserved_time"},
		{"delete when", func(rc *RocketMQConfig) { rc.DeleteWhen = "24" }, "delete_when"},
		{"delete when empty", func(rc *RocketMQConfig) { rc.DeleteWhen = "" }, "delete_when"},
		{"disk ratio low", func(rc *RocketMQConfig) { rc.DiskMaxUsedSpaceRatio = 0 }, "disk_max_used_space_ratio"},
		{"disk ratio high", func(rc *RocketMQConfig) { rc.DiskMaxUsedSpaceRatio = 101 }, "disk_max_used_space_ratio"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := rocketMQStandaloneConfig()
			rc := cfg.Addons.RocketMQ["rmq"]
			tc.edit(&rc)
			cfg.Addons.RocketMQ["rmq"] = rc
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestRocketMQValidateCluster(t *testing.T) {
	cases := []struct {
		name string
		edit func(*RocketMQConfig)
		want string
	}{
		{"loopback advertise", func(rc *RocketMQConfig) { rc.AdvertiseHost = "127.0.0.1" }, "loopback"},
		{"missing advertise", func(rc *RocketMQConfig) { rc.AdvertiseHost = "" }, "advertise_host"},
		{"bad controller id", func(rc *RocketMQConfig) { rc.ControllerID = "n-0" }, "controller_id"},
		{"no remote peer", func(rc *RocketMQConfig) { rc.Peers = nil }, "at least one remote peer"},
		{"peer with port", func(rc *RocketMQConfig) { rc.Peers = []string{"n1@10.0.0.2:9877"} }, "without a port"},
		{"peer hostname", func(rc *RocketMQConfig) { rc.Peers = []string{"n1@node2"} }, "IPv4"},
		{"peer loopback", func(rc *RocketMQConfig) { rc.Peers = []string{"n1@127.0.0.2"} }, "loopback"},
		{"peer duplicate id", func(rc *RocketMQConfig) { rc.Peers = []string{"n1@10.0.0.2", "n1@10.0.0.3"} }, "duplicated"},
		{"peer duplicate host", func(rc *RocketMQConfig) { rc.Peers = []string{"n1@10.0.0.2", "n2@10.0.0.2"} }, "duplicated"},
		{"local peer mismatch", func(rc *RocketMQConfig) { rc.Peers = []string{"n0@10.0.0.9", "n1@10.0.0.2"} }, "advertises"},
		{"namesrv port override", func(rc *RocketMQConfig) { rc.NamesrvPort = 19999 }, "must be 9876"},
		{"broker port override", func(rc *RocketMQConfig) { rc.BrokerPort = 20911 }, "must be 10911"},
		{"proxy port override", func(rc *RocketMQConfig) { rc.ProxyPort = 18080 }, "must be 8080"},
		{"grpc port override", func(rc *RocketMQConfig) { rc.GRPCPort = 18081 }, "must be 8081"},
		{"controller port override", func(rc *RocketMQConfig) { rc.ControllerPort = 19877 }, "must be 9877"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := rocketMQClusterConfig()
			rc := cfg.Addons.RocketMQ["rmq"]
			tc.edit(&rc)
			cfg.Addons.RocketMQ["rmq"] = rc
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestRocketMQClusteredInstancePerHost(t *testing.T) {
	cfg := Default()
	cfg.BaseDir = "/srv/pg"
	cfg.Addons.RocketMQ = map[string]RocketMQConfig{
		"a": {Cluster: "lab", AdvertiseHost: "10.0.0.1", ControllerID: "n0", Peers: []string{"n1@10.0.0.2"}},
		"b": {Cluster: "lab", AdvertiseHost: "10.0.0.2", ControllerID: "n1", Peers: []string{"n0@10.0.0.1"}},
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "at most one clustered") {
		t.Fatalf("Validate() = %v, want one-cluster error", err)
	}
}

func TestRocketMQClusterAddresses(t *testing.T) {
	cfg := rocketMQClusterConfig()
	rc := cfg.Addons.RocketMQ["rmq"]

	namesrv, err := RocketMQNamesrvAddrs(rc)
	if err != nil {
		t.Fatalf("RocketMQNamesrvAddrs: %v", err)
	}
	want := []string{"10.0.0.1:9876", "10.0.0.2:9876", "10.0.0.3:9876"}
	if strings.Join(namesrv, ";") != strings.Join(want, ";") {
		t.Errorf("namesrv addrs = %v, want %v", namesrv, want)
	}
	controller, err := RocketMQControllerAddrs(rc)
	if err != nil {
		t.Fatalf("RocketMQControllerAddrs: %v", err)
	}
	wantController := []string{"10.0.0.1:9877", "10.0.0.2:9877", "10.0.0.3:9877"}
	if strings.Join(controller, ";") != strings.Join(wantController, ";") {
		t.Errorf("controller addrs = %v, want %v", controller, wantController)
	}
	if got := RocketMQMinInSyncReplicas(rc); got != 2 {
		t.Errorf("MinInSyncReplicas = %d, want 2", got)
	}
}

func TestRocketMQStandaloneAddresses(t *testing.T) {
	cfg := rocketMQStandaloneConfig()
	rc := cfg.Addons.RocketMQ["rmq"]

	namesrv, err := RocketMQNamesrvAddrs(rc)
	if err != nil {
		t.Fatalf("RocketMQNamesrvAddrs: %v", err)
	}
	if len(namesrv) != 1 || !strings.HasPrefix(namesrv[0], "127.0.0.1:") {
		t.Errorf("namesrv addrs = %v", namesrv)
	}
	if _, err := RocketMQControllerAddrs(rc); err == nil {
		t.Errorf("RocketMQControllerAddrs succeeded for standalone")
	}
	if got := RocketMQVIPPort(rc); got != rc.BrokerPort-2 {
		t.Errorf("VIP port = %d", got)
	}
	if got := RocketMQHAPort(rc); got != rc.BrokerPort+1 {
		t.Errorf("HA port = %d", got)
	}
	if got := RocketMQMinInSyncReplicas(rc); got != 1 {
		t.Errorf("MinInSyncReplicas = %d, want 1", got)
	}
}

func TestNormalizeRocketMQPeers(t *testing.T) {
	got, err := NormalizeRocketMQPeers([]string{"n2@10.0.0.3"}, "n0", "10.0.0.1")
	if err != nil {
		t.Fatalf("NormalizeRocketMQPeers: %v", err)
	}
	if strings.Join(got, ",") != "n0@10.0.0.1,n2@10.0.0.3" {
		t.Errorf("peers = %v", got)
	}
	if _, err := NormalizeRocketMQPeers([]string{"n0@10.0.0.9"}, "n0", "10.0.0.1"); err == nil {
		t.Errorf("mismatched local peer accepted")
	}
	if _, err := ParseRocketMQPeer("n1-1@10.0.0.2"); err == nil {
		t.Errorf("hyphenated controller id accepted")
	}
}

func TestRocketMQVersionAndImage(t *testing.T) {
	for _, version := range []string{"5.5.0", "5.5", "5.10.1"} {
		if !ValidRocketMQVersion(version) {
			t.Errorf("ValidRocketMQVersion(%q) = false", version)
		}
		if tag, ok := RocketMQImageTagForVersion(version); !ok || tag != "docker.io/apache/rocketmq:"+version {
			t.Errorf("RocketMQImageTagForVersion(%q) = %q/%v", version, tag, ok)
		}
	}
	for _, version := range []string{"4.9.0", "5", "5.5.0-rc1", "latest", "6.0.0"} {
		if ValidRocketMQVersion(version) {
			t.Errorf("ValidRocketMQVersion(%q) = true", version)
		}
	}
}

func TestValidRocketMQDeleteWhen(t *testing.T) {
	for _, value := range []string{"0", "04", "4", "23", "04;16", "0;12;23"} {
		if !ValidRocketMQDeleteWhen(value) {
			t.Errorf("ValidRocketMQDeleteWhen(%q) = false", value)
		}
	}
	for _, value := range []string{"", "24", "-1", "04;", ";04", "04; 16", "4,16", "04:16"} {
		if ValidRocketMQDeleteWhen(value) {
			t.Errorf("ValidRocketMQDeleteWhen(%q) = true", value)
		}
	}
}
