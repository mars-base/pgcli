package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRabbitMQApplyDefaults(t *testing.T) {
	cfg := Default()
	cfg.BaseDir = "/srv/pg"
	cfg.Addons.RabbitMQ = map[string]RabbitMQConfig{
		"mq": {},
	}
	cfg.ApplyDefaults()

	rc := cfg.Addons.RabbitMQ["mq"]
	if rc.Name != "mq" {
		t.Errorf("Name = %q, want mq", rc.Name)
	}
	if rc.Version != DefaultRabbitMQVersion {
		t.Errorf("Version = %q, want %q", rc.Version, DefaultRabbitMQVersion)
	}
	if rc.ImageTag != DefaultRabbitMQImageTag {
		t.Errorf("ImageTag = %q, want %q", rc.ImageTag, DefaultRabbitMQImageTag)
	}
	if rc.ContainerName != "pgcli-rabbitmq-mq" {
		t.Errorf("ContainerName = %q, want pgcli-rabbitmq-mq", rc.ContainerName)
	}
	if rc.Listen != "127.0.0.1" {
		t.Errorf("Listen = %q, want 127.0.0.1", rc.Listen)
	}
	if rc.AdminUser != "admin" {
		t.Errorf("AdminUser = %q, want admin", rc.AdminUser)
	}
	dir := filepath.Join("/srv/pg", "addon", "rabbitmq", "mq")
	if rc.ConfigPath != filepath.Join(dir, "config", "rabbitmq.conf") {
		t.Errorf("ConfigPath = %q", rc.ConfigPath)
	}
	if rc.DataDir != filepath.Join(dir, "data") {
		t.Errorf("DataDir = %q", rc.DataDir)
	}
	if rc.SecretPath != filepath.Join(dir, "secrets", "admin_password") {
		t.Errorf("SecretPath = %q", rc.SecretPath)
	}
	if rc.AMQPPort < cfg.RabbitMQStartPort {
		t.Errorf("AMQPPort = %d, want >= %d", rc.AMQPPort, cfg.RabbitMQStartPort)
	}
	if rc.ManagementPort < cfg.RabbitMQMgmtStartPort {
		t.Errorf("ManagementPort = %d, want >= %d", rc.ManagementPort, cfg.RabbitMQMgmtStartPort)
	}
	if rc.EPMDPort < cfg.RabbitMQEPMDStartPort {
		t.Errorf("EPMDPort = %d, want >= %d", rc.EPMDPort, cfg.RabbitMQEPMDStartPort)
	}
	if rc.DistPort < cfg.RabbitMQDistStartPort {
		t.Errorf("DistPort = %d, want >= %d", rc.DistPort, cfg.RabbitMQDistStartPort)
	}
}

func TestRabbitMQContainerNameFollowsNamespace(t *testing.T) {
	cfg := Default()
	cfg.Namespace = "dev"
	cfg.Addons.RabbitMQ = map[string]RabbitMQConfig{"mq": {}}
	cfg.ApplyDefaults()

	if got := cfg.Addons.RabbitMQ["mq"].ContainerName; got != "pgcli-rabbitmq-dev-mq" {
		t.Errorf("ContainerName = %q, want pgcli-rabbitmq-dev-mq", got)
	}
}

func TestRabbitMQPortPoolsAreSeparate(t *testing.T) {
	cfg := Default()
	cfg.RabbitMQStartPort = 28000
	cfg.RabbitMQTLSStartPort = 28100
	cfg.RabbitMQMgmtStartPort = 28200
	cfg.RabbitMQMgmtTLSStartPort = 28300
	cfg.RabbitMQEPMDStartPort = 28400
	cfg.RabbitMQDistStartPort = 28500
	cfg.Addons.RabbitMQ = map[string]RabbitMQConfig{
		"b": {TLS: true},
		"a": {},
	}
	cfg.ApplyDefaults()

	a := cfg.Addons.RabbitMQ["a"]
	b := cfg.Addons.RabbitMQ["b"]
	// Names are allocated in sorted order: "a" then "b".
	if a.AMQPPort != 28000 || b.AMQPPort != 28001 {
		t.Errorf("AMQP ports = %d/%d, want 28000/28001", a.AMQPPort, b.AMQPPort)
	}
	if a.ManagementPort != 28200 || b.ManagementPort != 28201 {
		t.Errorf("management ports = %d/%d, want 28200/28201", a.ManagementPort, b.ManagementPort)
	}
	if a.EPMDPort != 28400 || b.EPMDPort != 28401 {
		t.Errorf("epmd ports = %d/%d, want 28400/28401", a.EPMDPort, b.EPMDPort)
	}
	if a.DistPort != 28500 || b.DistPort != 28501 {
		t.Errorf("dist ports = %d/%d, want 28500/28501", a.DistPort, b.DistPort)
	}
	if a.TLS || a.AMQPTLSPort != 0 || a.ManagementTLSPort != 0 {
		t.Errorf("non-TLS instance got TLS ports: %+v", a)
	}
	if !b.TLS || b.AMQPTLSPort != 28100 || b.ManagementTLSPort != 28300 {
		t.Errorf("TLS instance ports = %d/%d, want 28100/28300", b.AMQPTLSPort, b.ManagementTLSPort)
	}
}

func TestRabbitMQExplicitPortIsPreserved(t *testing.T) {
	cfg := Default()
	cfg.RabbitMQStartPort = 29000
	cfg.RabbitMQMgmtStartPort = 29100
	cfg.RabbitMQEPMDStartPort = 29200
	cfg.RabbitMQDistStartPort = 29300
	cfg.Addons.RabbitMQ = map[string]RabbitMQConfig{
		"a": {AMQPPort: 29000, ManagementPort: 29100, EPMDPort: 29200, DistPort: 29300},
		"b": {},
	}
	cfg.ApplyDefaults()

	a := cfg.Addons.RabbitMQ["a"]
	if a.AMQPPort != 29000 || a.ManagementPort != 29100 || a.EPMDPort != 29200 || a.DistPort != 29300 {
		t.Errorf("explicit ports overwritten: %+v", a)
	}
	b := cfg.Addons.RabbitMQ["b"]
	if b.AMQPPort != 29001 || b.ManagementPort != 29101 || b.EPMDPort != 29201 || b.DistPort != 29301 {
		t.Errorf("next instance did not skip explicit ports: %+v", b)
	}
}

func TestRabbitMQValidate(t *testing.T) {
	base := func() *Config {
		cfg := Default()
		cfg.BaseDir = "/srv/pg"
		cfg.Addons.RabbitMQ = map[string]RabbitMQConfig{
			"mq": {AdminPassword: "secret"},
		}
		cfg.ApplyDefaults()
		return cfg
	}

	cases := []struct {
		name string
		edit func(*RabbitMQConfig)
		want string
	}{
		{"bad version", func(rc *RabbitMQConfig) { rc.Version = "bad/version" }, "safe Docker image tag"},
		{"image mismatch", func(rc *RabbitMQConfig) { rc.ImageTag = "docker.io/library/rabbitmq:4.3.0-management" }, "does not match version"},
		{"bad admin user", func(rc *RabbitMQConfig) { rc.AdminUser = "bad user" }, "admin_user"},
		{"empty password", func(rc *RabbitMQConfig) { rc.AdminPassword = "" }, "admin_password must not be empty"},
		{"password control chars", func(rc *RabbitMQConfig) { rc.AdminPassword = "a\nb" }, "control characters"},
		{"relative path", func(rc *RabbitMQConfig) { rc.DataDir = "data" }, "absolute path"},
		{"bad listen", func(rc *RabbitMQConfig) { rc.Listen = "localhost" }, "must be an IP address"},
		{"cert without key", func(rc *RabbitMQConfig) { rc.TLS = true; rc.CertFile = "/tmp/x.crt" }, "must be set together"},
		{"cert without tls", func(rc *RabbitMQConfig) {
			rc.CertFile = "/tmp/x.crt"
			rc.KeyFile = "/tmp/x.key"
		}, "requires tls: true"},
		{"tls ports without tls", func(rc *RabbitMQConfig) { rc.AMQPTLSPort = 12345 }, "TLS ports require tls: true"},
		{"bad setting key", func(rc *RabbitMQConfig) { rc.Settings = map[string]string{"bad key": "v"} }, "settings key"},
		{"managed setting", func(rc *RabbitMQConfig) { rc.Settings = map[string]string{"listeners.tcp.default": "0.0.0.0:5672"} }, "managed by pgcli"},
		{"setting control chars", func(rc *RabbitMQConfig) { rc.Settings = map[string]string{"log.file.level": "a\nb"} }, "control characters"},
		{"duplicate port", func(rc *RabbitMQConfig) { rc.ManagementPort = rc.AMQPPort }, "must be unique"},
		{"port out of range", func(rc *RabbitMQConfig) { rc.AMQPPort = 70000 }, "between 1 and 65535"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			rc := cfg.Addons.RabbitMQ["mq"]
			tc.edit(&rc)
			cfg.Addons.RabbitMQ["mq"] = rc
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestRabbitMQValidateDuplicateAcrossInstances(t *testing.T) {
	cfg := Default()
	cfg.BaseDir = "/srv/pg"
	cfg.Addons.RabbitMQ = map[string]RabbitMQConfig{
		"a": {AdminPassword: "secret"},
		"b": {AdminPassword: "secret"},
	}
	cfg.ApplyDefaults()
	b := cfg.Addons.RabbitMQ["b"]
	b.AMQPPort = cfg.Addons.RabbitMQ["a"].AMQPPort
	cfg.Addons.RabbitMQ["b"] = b
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "must be unique") {
		t.Fatalf("Validate() = %v, want duplicate-port error", err)
	}
}

func TestRabbitMQClusterApplyDefaults(t *testing.T) {
	cfg := Default()
	cfg.BaseDir = "/srv/pg"
	cfg.RabbitMQStartPort = 30000
	cfg.RabbitMQMgmtStartPort = 30100
	cfg.Addons.RabbitMQ = map[string]RabbitMQConfig{
		"mq": {
			Cluster:       "lab",
			NodeName:      "rabbit@node1",
			EtcdEndpoints: []string{"10.0.0.1:2379"},
			ErlangCookie:  "shared-cookie",
			AdminPassword: "secret",
		},
	}
	cfg.ApplyDefaults()

	rc := cfg.Addons.RabbitMQ["mq"]
	if rc.Listen != "0.0.0.0" {
		t.Errorf("Listen = %q, want 0.0.0.0 in cluster mode", rc.Listen)
	}
	if rc.EPMDPort != DefaultRabbitMQClusteredEPMDPort {
		t.Errorf("EPMDPort = %d, want %d", rc.EPMDPort, DefaultRabbitMQClusteredEPMDPort)
	}
	if rc.DistPort != DefaultRabbitMQClusteredDistPort {
		t.Errorf("DistPort = %d, want %d", rc.DistPort, DefaultRabbitMQClusteredDistPort)
	}
	if rc.AMQPPort != 30000 || rc.ManagementPort != 30100 {
		t.Errorf("AMQP/management ports = %d/%d, want 30000/30100", rc.AMQPPort, rc.ManagementPort)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestParseRabbitMQNodeName(t *testing.T) {
	valid := []struct {
		node      string
		name      string
		host      string
		longNames bool
	}{
		{"rabbit@node1", "rabbit", "node1", false},
		{"rabbit@node.example.com", "rabbit", "node.example.com", true},
		{"mq-1@10.241.20.147", "mq-1", "10.241.20.147", true},
	}
	for _, tt := range valid {
		name, host, err := ParseRabbitMQNodeName(tt.node)
		if err != nil {
			t.Errorf("ParseRabbitMQNodeName(%q) = %v", tt.node, err)
			continue
		}
		if name != tt.name || host != tt.host {
			t.Errorf("ParseRabbitMQNodeName(%q) = %q, %q; want %q, %q", tt.node, name, host, tt.name, tt.host)
		}
		if got := RabbitMQNodeUsesLongName(host); got != tt.longNames {
			t.Errorf("RabbitMQNodeUsesLongName(%q) = %t, want %t", host, got, tt.longNames)
		}
	}

	invalid := map[string]string{
		"":                 "exactly one '@'",
		"rabbit":           "exactly one '@'",
		"rabbit@node@host": "exactly one '@'",
		"@node1":           "before '@'",
		"rabbit@":          "after '@'",
		"bad name@node1":   "before '@'",
		"rabbit@localhost": "loopback",
		"rabbit@127.0.0.1": "loopback",
		"rabbit@0.0.0.0":   "loopback or unspecified",
		"rabbit@::1":       "IPv4 address or hostname",
		"rabbit@fe80::1":   "IPv4 address or hostname",
		"rabbit@node/1":    "IPv4 address or hostname",
		"rabbit@node_name": "not a valid hostname",
	}
	for node, want := range invalid {
		if _, _, err := ParseRabbitMQNodeName(node); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseRabbitMQNodeName(%q) = %v, want %q", node, err, want)
		}
	}
}

func TestNormalizeRabbitMQEtcdEndpoints(t *testing.T) {
	got, err := NormalizeRabbitMQEtcdEndpoints([]string{
		" http://10.0.0.2:2379 ",
		"10.0.0.1:2379",
		"10.0.0.2:2379",
		"http://node.example.com:2379",
	})
	if err != nil {
		t.Fatalf("NormalizeRabbitMQEtcdEndpoints: %v", err)
	}
	want := []string{"10.0.0.1:2379", "10.0.0.2:2379", "node.example.com:2379"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("endpoints = %v, want %v", got, want)
	}

	bad := map[string]string{
		"https://10.0.0.1:2379":     "TLS (https://)",
		"user:pass@10.0.0.1:2379":   "authentication",
		"http://10.0.0.1:2379/path": "path",
		"10.0.0.1":                  "host:port",
		"10.0.0.1:0":                "invalid port",
		"10.0.0.1:70000":            "invalid port",
		"":                          "must not be empty",
	}
	for endpoint, wantSub := range bad {
		if _, err := NormalizeRabbitMQEtcdEndpoints([]string{endpoint}); err == nil || !strings.Contains(err.Error(), wantSub) {
			t.Errorf("NormalizeRabbitMQEtcdEndpoints(%q) = %v, want %q", endpoint, err, wantSub)
		}
	}
}

func TestValidateRabbitMQErlangCookie(t *testing.T) {
	for _, ok := range []string{"cookie", "A1b2C3d4E5", strings.Repeat("x", 255)} {
		if err := ValidateRabbitMQErlangCookie(ok); err != nil {
			t.Errorf("ValidateRabbitMQErlangCookie(%q) = %v", ok, err)
		}
	}
	bad := map[string]string{
		"":                       "must not be empty",
		"has space":              "whitespace",
		"line\nbreak":            "whitespace",
		strings.Repeat("x", 256): "at most 255",
	}
	for cookie, want := range bad {
		if err := ValidateRabbitMQErlangCookie(cookie); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ValidateRabbitMQErlangCookie(%q) = %v, want %q", cookie, err, want)
		}
	}
}

func TestRabbitMQClusterValidate(t *testing.T) {
	base := func() *Config {
		cfg := Default()
		cfg.BaseDir = "/srv/pg"
		cfg.RabbitMQStartPort = 31000
		cfg.RabbitMQMgmtStartPort = 31100
		cfg.Addons.RabbitMQ = map[string]RabbitMQConfig{
			"mq": {
				Cluster:       "lab",
				NodeName:      "rabbit@node1",
				EtcdEndpoints: []string{"10.0.0.1:2379"},
				ErlangCookie:  "shared-cookie",
				AdminPassword: "secret",
			},
		}
		cfg.ApplyDefaults()
		return cfg
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("valid cluster config: %v", err)
	}

	cases := []struct {
		name string
		edit func(*RabbitMQConfig)
		want string
	}{
		{"standalone with node name", func(rc *RabbitMQConfig) { rc.Cluster = "" }, "node_name requires cluster"},
		{"standalone with endpoints", func(rc *RabbitMQConfig) { rc.Cluster = ""; rc.NodeName = "" }, "etcd_endpoints requires cluster"},
		{"standalone with cookie", func(rc *RabbitMQConfig) { rc.Cluster = ""; rc.NodeName = ""; rc.EtcdEndpoints = nil }, "erlang_cookie requires cluster"},
		{"bad cluster name", func(rc *RabbitMQConfig) { rc.Cluster = "bad cluster" }, "cluster"},
		{"bad node name", func(rc *RabbitMQConfig) { rc.NodeName = "rabbit@localhost" }, "loopback"},
		{"missing endpoints", func(rc *RabbitMQConfig) { rc.EtcdEndpoints = nil }, "etcd_endpoints is required"},
		{"https endpoint", func(rc *RabbitMQConfig) { rc.EtcdEndpoints = []string{"https://10.0.0.1:2379"} }, "https://"},
		{"missing cookie", func(rc *RabbitMQConfig) { rc.ErlangCookie = "" }, "Erlang cookie must not be empty"},
		{"loopback listen", func(rc *RabbitMQConfig) { rc.Listen = "127.0.0.1" }, "must not be loopback"},
		{"wrong epmd port", func(rc *RabbitMQConfig) { rc.EPMDPort = 4400 }, "must be 4369"},
		{"wrong dist port", func(rc *RabbitMQConfig) { rc.DistPort = 26000 }, "must be 25672"},
		{"managed cluster setting", func(rc *RabbitMQConfig) {
			rc.Settings = map[string]string{"cluster_formation.peer_discovery_backend": "static"}
		}, "managed by pgcli"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			rc := cfg.Addons.RabbitMQ["mq"]
			tc.edit(&rc)
			cfg.Addons.RabbitMQ["mq"] = rc
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestRabbitMQClusteredInstancePerHost(t *testing.T) {
	cfg := Default()
	cfg.BaseDir = "/srv/pg"
	cfg.RabbitMQStartPort = 32000
	cfg.RabbitMQMgmtStartPort = 32100
	cluster := RabbitMQConfig{
		Cluster:       "lab",
		NodeName:      "rabbit@node1",
		EtcdEndpoints: []string{"10.0.0.1:2379"},
		ErlangCookie:  "shared-cookie",
		AdminPassword: "secret",
	}
	other := cluster
	other.NodeName = "rabbit@node2"
	cfg.Addons.RabbitMQ = map[string]RabbitMQConfig{"mq": cluster, "mq2": other}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "at most one clustered RabbitMQ instance") {
		t.Fatalf("Validate() = %v, want one-clustered-instance error", err)
	}
}
