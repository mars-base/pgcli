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
