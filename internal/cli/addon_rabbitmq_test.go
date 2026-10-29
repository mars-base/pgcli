package cli

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/certgen"
	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/rabbitmq"
)

func rabbitMQTestInstallCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "install"}
	cmd.Flags().String("name", "", "")
	cmd.Flags().String("version", "", "")
	cmd.Flags().String("data-dir", "", "")
	cmd.Flags().String("listen", "", "")
	cmd.Flags().Int("amqp-port", 0, "")
	cmd.Flags().Int("management-port", 0, "")
	cmd.Flags().Bool("tls", false, "")
	cmd.Flags().Int("tls-port", 0, "")
	cmd.Flags().String("tls-cert", "", "")
	cmd.Flags().String("tls-key", "", "")
	cmd.Flags().String("admin-user", "", "")
	cmd.Flags().String("admin-password", "", "")
	cmd.Flags().String("admin-password-file", "", "")
	cmd.Flags().String("cluster", "", "")
	cmd.Flags().String("node-name", "", "")
	cmd.Flags().StringSlice("etcd", nil, "")
	cmd.Flags().StringSlice("etcd-endpoints", nil, "")
	cmd.Flags().String("erlang-cookie-file", "", "")
	cmd.Flags().Bool("secrets-from-etcd", false, "")
	cmd.Flags().StringArray("config-option", nil, "")
	cmd.Flags().Bool("force", false, "")
	cmd.Flags().String("advertise-host", "", "")
	cmd.Flags().Int("node-id", 0, "")
	cmd.Flags().StringArray("peer", nil, "")
	cmd.Flags().String("join", "", "")
	return cmd
}

func writeRabbitMQTestConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pg.yaml")
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	oldPath := cfgPath
	cfgPath = path
	t.Cleanup(func() { cfgPath = oldPath })
}

func TestRabbitMQFlagName(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("name", "", "")
	if got := rabbitMQFlagName(cmd); got != "rabbitmq" {
		t.Errorf("default name = %q, want rabbitmq", got)
	}
	if err := cmd.Flags().Set("name", "rmq"); err != nil {
		t.Fatal(err)
	}
	if got := rabbitMQFlagName(cmd); got != "rmq" {
		t.Errorf("name = %q, want rmq", got)
	}
}

func TestParseRabbitMQConfigOptions(t *testing.T) {
	got, err := parseRabbitMQConfigOptions([]string{"log.file.level=debug", "heartbeat=60"})
	if err != nil {
		t.Fatalf("parseRabbitMQConfigOptions: %v", err)
	}
	if got["log.file.level"] != "debug" || got["heartbeat"] != "60" {
		t.Errorf("parsed = %v", got)
	}
	if _, err := parseRabbitMQConfigOptions(nil); err != nil {
		t.Errorf("empty options: %v", err)
	}

	bad := []struct {
		options []string
		sub     string
	}{
		{[]string{"heartbeat"}, "KEY=VALUE"},
		{[]string{"=60"}, "KEY=VALUE"},
		{[]string{"listeners.tcp.default=0.0.0.0:5672"}, "managed by pgcli"},
		{[]string{"ssl_options.verify=verify_peer"}, "managed by pgcli"},
		{[]string{"default_user=other"}, "managed by pgcli"},
		{[]string{"heartbeat=60\nconsumer_timeout=1"}, "control characters"},
	}
	for _, tt := range bad {
		if _, err := parseRabbitMQConfigOptions(tt.options); err == nil || !strings.Contains(err.Error(), tt.sub) {
			t.Errorf("parseRabbitMQConfigOptions(%v) = %v, want %q", tt.options, err, tt.sub)
		}
	}
}

func TestRabbitMQCommandTree(t *testing.T) {
	for _, name := range []string{"status", "shell", "queue", "cluster", "secret"} {
		if _, _, err := addonRabbitMQCmd.Find([]string{name}); err != nil {
			t.Errorf("addon rabbitmq %s: %v", name, err)
		}
	}
	generate, _, err := addonRabbitMQCmd.Find([]string{"secret", "generate"})
	if err != nil {
		t.Fatal(err)
	}
	for flag, def := range map[string]string{
		"cluster":             "",
		"cookie-file":         "/tmp/rmq.cookie",
		"admin-password-file": "/tmp/rmq.admin",
		"force":               "false",
		"keep-cookie":         "false",
	} {
		got := generate.Flags().Lookup(flag)
		if got == nil {
			t.Errorf("addon rabbitmq secret generate: --%s not registered", flag)
			continue
		}
		if got.DefValue != def {
			t.Errorf("addon rabbitmq secret generate: --%s default = %q, want %q", flag, got.DefValue, def)
		}
	}
	for _, flag := range []string{"etcd", "etcd-endpoints"} {
		if generate.Flags().Lookup(flag) == nil {
			t.Errorf("addon rabbitmq secret generate: --%s not registered", flag)
		}
	}
	for _, name := range []string{"status", "forget"} {
		cmd, _, err := addonRabbitMQCmd.Find([]string{"cluster", name})
		if err != nil {
			t.Errorf("addon rabbitmq cluster %s: %v", name, err)
			continue
		}
		if cmd.Flags().Lookup("name") == nil {
			t.Errorf("addon rabbitmq cluster %s: --name flag not registered", name)
		}
	}
	forget, _, err := addonRabbitMQCmd.Find([]string{"cluster", "forget"})
	if err != nil {
		t.Fatal(err)
	}
	if forget.Flags().Lookup("yes") == nil {
		t.Error("addon rabbitmq cluster forget: --yes flag not registered")
	}
	for _, name := range []string{"list", "declare", "info", "publish", "get", "bind", "unbind", "bindings", "purge", "delete"} {
		cmd, _, err := addonRabbitMQCmd.Find([]string{"queue", name})
		if err != nil {
			t.Errorf("addon rabbitmq queue %s: %v", name, err)
			continue
		}
		if cmd.Flags().Lookup("name") == nil {
			t.Errorf("addon rabbitmq queue %s: --name flag not registered", name)
		}
		if cmd.Flags().Lookup("vhost") == nil {
			t.Errorf("addon rabbitmq queue %s: --vhost flag not registered", name)
		}
	}
	wantFlags := map[string][]string{
		"declare":  {"durable", "auto-delete", "type", "message-ttl", "expires", "argument"},
		"info":     {"json"},
		"publish":  {"payload", "payload-file", "exchange", "routing-key", "property"},
		"get":      {"count", "consume", "json"},
		"bind":     {"exchange", "routing-key", "argument"},
		"unbind":   {"exchange", "routing-key", "argument"},
		"bindings": {"json"},
	}
	for name, flags := range wantFlags {
		cmd, _, err := addonRabbitMQCmd.Find([]string{"queue", name})
		if err != nil {
			continue
		}
		for _, flag := range flags {
			if cmd.Flags().Lookup(flag) == nil {
				t.Errorf("addon rabbitmq queue %s: --%s flag not registered", name, flag)
			}
		}
	}
	for _, name := range []string{"purge", "delete"} {
		cmd, _, err := addonRabbitMQCmd.Find([]string{"queue", name})
		if err != nil {
			continue
		}
		if cmd.Flags().Lookup("yes") == nil {
			t.Errorf("addon rabbitmq queue %s: --yes flag not registered", name)
		}
	}
	del, _, err := addonRabbitMQCmd.Find([]string{"queue", "delete"})
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"if-empty", "if-unused"} {
		if del.Flags().Lookup(flag) == nil {
			t.Errorf("queue delete: --%s flag not registered", flag)
		}
	}
}

func TestRabbitMQInstallFlagsRegistered(t *testing.T) {
	want := map[string]string{
		"amqp-port":           "int",
		"management-port":     "int",
		"tls":                 "bool",
		"tls-port":            "int",
		"tls-cert":            "string",
		"tls-key":             "string",
		"admin-user":          "string",
		"admin-password":      "string",
		"admin-password-file": "string",
		"cluster":             "string",
		"node-name":           "string",
		"etcd":                "stringSlice",
		"etcd-endpoints":      "stringSlice",
		"erlang-cookie-file":  "string",
		"secrets-from-etcd":   "bool",
		"config-option":       "stringArray",
		"listen":              "string",
		"version":             "string",
		"data-dir":            "string",
		"force":               "bool",
	}
	for name, typ := range want {
		f := addonInstallCmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("--%s is not registered", name)
		}
		if f.Value.Type() != typ {
			t.Errorf("--%s is %s, want %s", name, f.Value.Type(), typ)
		}
	}
	for _, name := range []string{"amqp-port", "management-port"} {
		if f := addonInstallCmd.Flags().Lookup(name); !strings.Contains(f.Usage, "RabbitMQ") {
			t.Errorf("--%s help does not mention RabbitMQ: %q", name, f.Usage)
		}
	}
}

func TestParseRabbitMQKeyValues(t *testing.T) {
	got, err := parseRabbitMQKeyValues("argument", []string{
		"x-message-ttl=60000",
		"x-queue-mode=lazy",
		"x-dead-letter-exchange=dlx",
		"x-max-priority=10",
		"x-truncate=false",
		`x-meta={"a":1}`,
	})
	if err != nil {
		t.Fatalf("parseRabbitMQKeyValues: %v", err)
	}
	if got["x-message-ttl"] != float64(60000) {
		t.Errorf("x-message-ttl = %#v", got["x-message-ttl"])
	}
	if got["x-queue-mode"] != "lazy" || got["x-dead-letter-exchange"] != "dlx" {
		t.Errorf("string values = %#v / %#v", got["x-queue-mode"], got["x-dead-letter-exchange"])
	}
	if got["x-max-priority"] != float64(10) || got["x-truncate"] != false {
		t.Errorf("typed values = %#v / %#v", got["x-max-priority"], got["x-truncate"])
	}
	meta, ok := got["x-meta"].(map[string]any)
	if !ok || meta["a"] != float64(1) {
		t.Errorf("x-meta = %#v", got["x-meta"])
	}
	if _, err := parseRabbitMQKeyValues("argument", []string{"novalue"}); err == nil || !strings.Contains(err.Error(), "KEY=VALUE") {
		t.Errorf("missing '=' error = %v", err)
	}
	if _, err := parseRabbitMQKeyValues("argument", []string{"=1"}); err == nil || !strings.Contains(err.Error(), "KEY=VALUE") {
		t.Errorf("empty key error = %v", err)
	}
}

func TestRabbitMQDurationMS(t *testing.T) {
	cases := map[string]int64{
		"0":      0,
		"60000":  60000,
		"30m":    1800000,
		"24h":    86400000,
		"1500ms": 1500,
	}
	for input, want := range cases {
		got, err := rabbitMQDurationMS("message-ttl", input)
		if err != nil || got != want {
			t.Errorf("rabbitMQDurationMS(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, bad := range []string{"-1", "soon", "10x"} {
		if _, err := rabbitMQDurationMS("message-ttl", bad); err == nil {
			t.Errorf("rabbitMQDurationMS(%q) succeeded, want error", bad)
		}
	}
}

func TestParseRabbitMQQueueDeclareFlags(t *testing.T) {
	newCmd := func() *cobra.Command {
		cmd := &cobra.Command{Use: "declare"}
		cmd.Flags().Bool("durable", true, "")
		cmd.Flags().Bool("auto-delete", false, "")
		cmd.Flags().String("type", "", "")
		cmd.Flags().String("message-ttl", "", "")
		cmd.Flags().String("expires", "", "")
		cmd.Flags().StringArray("argument", nil, "")
		return cmd
	}
	parse := func(defaultType string, flags map[string]string) (rabbitMQQueueDeclareOptions, error) {
		cmd := newCmd()
		for key, value := range flags {
			if err := cmd.Flags().Set(key, value); err != nil {
				t.Fatalf("set %s=%s: %v", key, value, err)
			}
		}
		return parseRabbitMQQueueDeclareFlags(cmd.Flags(), defaultType)
	}

	t.Run("defaults", func(t *testing.T) {
		opts, err := parse("classic", nil)
		if err != nil {
			t.Fatal(err)
		}
		if !opts.durable || opts.autoDelete || opts.queueType != "classic" {
			t.Errorf("defaults = %+v", opts)
		}
		if opts.arguments["x-queue-type"] != "classic" {
			t.Errorf("arguments = %v", opts.arguments)
		}
		if _, ok := opts.arguments["x-message-ttl"]; ok || opts.messageTTLSet {
			t.Errorf("message TTL set without flag: %+v", opts)
		}
		if _, ok := opts.arguments["x-expires"]; ok || opts.expiresSet {
			t.Errorf("expires set without flag: %+v", opts)
		}
	})

	t.Run("cluster default is quorum", func(t *testing.T) {
		opts, err := parse("quorum", nil)
		if err != nil {
			t.Fatal(err)
		}
		if opts.queueType != "quorum" || opts.arguments["x-queue-type"] != "quorum" {
			t.Errorf("defaults = %+v", opts)
		}
	})

	t.Run("explicit type wins over default", func(t *testing.T) {
		opts, err := parse("quorum", map[string]string{"type": "classic"})
		if err != nil {
			t.Fatal(err)
		}
		if opts.queueType != "classic" || opts.arguments["x-queue-type"] != "classic" {
			t.Errorf("explicit classic ignored: %+v", opts)
		}
	})

	t.Run("ttl, expiry and extra arguments", func(t *testing.T) {
		opts, err := parse("classic", map[string]string{
			"type":        "quorum",
			"message-ttl": "30m",
			"expires":     "24h",
			"argument":    "x-max-priority=10",
		})
		if err != nil {
			t.Fatal(err)
		}
		if opts.queueType != "quorum" || opts.arguments["x-queue-type"] != "quorum" {
			t.Errorf("queue type = %+v", opts)
		}
		if !opts.messageTTLSet || opts.messageTTL != 1800000 || opts.arguments["x-message-ttl"] != int64(1800000) {
			t.Errorf("message TTL = %+v", opts)
		}
		if !opts.expiresSet || opts.queueExpires != 86400000 || opts.arguments["x-expires"] != int64(86400000) {
			t.Errorf("expires = %+v", opts)
		}
		if opts.arguments["x-max-priority"] != float64(10) {
			t.Errorf("arguments = %v", opts.arguments)
		}
	})

	bad := []struct {
		name   string
		flags  map[string]string
		substr string
	}{
		{"invalid type", map[string]string{"type": "stream"}, "use classic or quorum"},
		{"transient queue", map[string]string{"durable": "false"}, "transient"},
		{"quorum auto-delete", map[string]string{"type": "quorum", "auto-delete": "true"}, "cannot be auto-delete"},
		{"bad message ttl", map[string]string{"message-ttl": "soon"}, "message-ttl"},
		{"bad expires", map[string]string{"expires": "-5"}, "expires"},
		{"zero expires", map[string]string{"expires": "0"}, "positive"},
		{"bad argument", map[string]string{"argument": "novalue"}, "KEY=VALUE"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parse("classic", tt.flags); err == nil || !strings.Contains(err.Error(), tt.substr) {
				t.Errorf("error = %v, want %q", err, tt.substr)
			}
		})
	}
}

func TestRabbitMQDefaultQueueType(t *testing.T) {
	if got := rabbitMQDefaultQueueType(config.RabbitMQConfig{}); got != "classic" {
		t.Errorf("standalone default = %q, want classic", got)
	}
	if got := rabbitMQDefaultQueueType(config.RabbitMQConfig{Cluster: "lab"}); got != "quorum" {
		t.Errorf("cluster default = %q, want quorum", got)
	}
}

func TestRabbitMQAdminPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin_password")
	if err := os.WriteFile(path, []byte("  reused-pw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := rabbitMQAdminPassword(path); err != nil || got != "reused-pw" {
		t.Errorf("rabbitMQAdminPassword = %q, %v; want reused-pw", got, err)
	}

	missing := filepath.Join(t.TempDir(), "absent")
	got, err := rabbitMQAdminPassword(missing)
	if err != nil {
		t.Fatalf("missing secret: %v", err)
	}
	if len(got) != 20 {
		t.Errorf("generated password length = %d, want 20", len(got))
	}
}

func TestApplyRabbitMQInstallFlags(t *testing.T) {
	base := func() config.RabbitMQConfig {
		return config.RabbitMQConfig{
			Name:              "rmq",
			Version:           config.DefaultRabbitMQVersion,
			ImageTag:          config.DefaultRabbitMQImageTag,
			Listen:            "127.0.0.1",
			AMQPPort:          5672,
			AMQPTLSPort:       5671,
			ManagementPort:    15672,
			ManagementTLSPort: 15671,
			EPMDPort:          4369,
			DistPort:          25672,
			AdminUser:         "admin",
			AdminPassword:     "secret",
			Settings:          map[string]string{"old": "1"},
		}
	}
	apply := func(flags map[string]string) (config.RabbitMQConfig, error) {
		cmd := rabbitMQTestInstallCmd()
		for key, value := range flags {
			if err := cmd.Flags().Set(key, value); err != nil {
				t.Fatalf("set %s: %v", key, err)
			}
		}
		rc := base()
		err := applyRabbitMQInstallFlags(&rc, cmd.Flags(), readRabbitMQInstallOptions(cmd.Flags()))
		return rc, err
	}

	t.Run("no flags preserves stored values", func(t *testing.T) {
		rc, err := apply(nil)
		if err != nil {
			t.Fatal(err)
		}
		if rc.Version != config.DefaultRabbitMQVersion || rc.ImageTag != config.DefaultRabbitMQImageTag {
			t.Errorf("version/image = %s/%s", rc.Version, rc.ImageTag)
		}
		if rc.AMQPPort != 5672 || rc.ManagementPort != 15672 {
			t.Errorf("ports changed: %+v", rc)
		}
		if rc.Settings["old"] != "1" {
			t.Errorf("settings changed: %v", rc.Settings)
		}
	})

	t.Run("version rewrites the image tag", func(t *testing.T) {
		rc, err := apply(map[string]string{"version": "4.3.1"})
		if err != nil {
			t.Fatal(err)
		}
		if rc.ImageTag != "docker.io/library/rabbitmq:4.3.1-management" {
			t.Errorf("ImageTag = %q", rc.ImageTag)
		}
	})

	t.Run("listen and ports", func(t *testing.T) {
		rc, err := apply(map[string]string{"listen": "0.0.0.0", "amqp-port": "6000", "management-port": "6001"})
		if err != nil {
			t.Fatal(err)
		}
		if rc.Listen != "0.0.0.0" || rc.AMQPPort != 6000 || rc.ManagementPort != 6001 {
			t.Errorf("applied = %+v", rc)
		}
	})

	t.Run("cert pair implies TLS", func(t *testing.T) {
		rc := base()
		rc.TLS = false
		rc.AMQPTLSPort = 0
		rc.ManagementTLSPort = 0
		cmd := rabbitMQTestInstallCmd()
		cmd.Flags().Set("tls-cert", "/tmp/c.pem")
		cmd.Flags().Set("tls-key", "/tmp/k.pem")
		if err := applyRabbitMQInstallFlags(&rc, cmd.Flags(), readRabbitMQInstallOptions(cmd.Flags())); err != nil {
			t.Fatal(err)
		}
		if !rc.TLS || rc.CertFile != "/tmp/c.pem" || rc.KeyFile != "/tmp/k.pem" {
			t.Errorf("TLS flags not applied: %+v", rc)
		}
	})

	t.Run("cert without key fails", func(t *testing.T) {
		_, err := apply(map[string]string{"tls-cert": "/tmp/c.pem"})
		if err == nil || !strings.Contains(err.Error(), "--tls-cert and --tls-key must be supplied together") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("tls=false clears certs and TLS ports", func(t *testing.T) {
		rc := base()
		rc.TLS = true
		rc.CertFile = "/tmp/c.pem"
		rc.KeyFile = "/tmp/k.pem"
		cmd := rabbitMQTestInstallCmd()
		cmd.Flags().Set("tls", "false")
		if err := applyRabbitMQInstallFlags(&rc, cmd.Flags(), readRabbitMQInstallOptions(cmd.Flags())); err != nil {
			t.Fatal(err)
		}
		if rc.TLS || rc.CertFile != "" || rc.KeyFile != "" || rc.AMQPTLSPort != 0 || rc.ManagementTLSPort != 0 {
			t.Errorf("TLS disable left state: %+v", rc)
		}
	})

	t.Run("tls-port requires TLS", func(t *testing.T) {
		rc := base()
		rc.TLS = false
		rc.AMQPTLSPort = 0
		rc.ManagementTLSPort = 0
		cmd := rabbitMQTestInstallCmd()
		cmd.Flags().Set("tls-port", "7000")
		if err := applyRabbitMQInstallFlags(&rc, cmd.Flags(), readRabbitMQInstallOptions(cmd.Flags())); err != nil {
			t.Fatal(err)
		}
		if rc.AMQPTLSPort != 0 {
			t.Errorf("AMQPTLSPort = %d, want 0 without TLS", rc.AMQPTLSPort)
		}
	})

	t.Run("config options replace stored settings", func(t *testing.T) {
		rc, err := apply(map[string]string{"config-option": "heartbeat=60"})
		if err != nil {
			t.Fatal(err)
		}
		if len(rc.Settings) != 1 || rc.Settings["heartbeat"] != "60" {
			t.Errorf("settings = %v, want replacement map", rc.Settings)
		}
	})

	t.Run("reserved config option fails", func(t *testing.T) {
		if _, err := apply(map[string]string{"config-option": "distribution.listener.interface=0.0.0.0"}); err == nil || !strings.Contains(err.Error(), "managed by pgcli") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestRabbitMQInstallRejectsBadInputBeforePodman(t *testing.T) {
	newConfig := func() {
		cfg := config.Default()
		cfg.BaseDir = t.TempDir()
		writeRabbitMQTestConfig(t, cfg)
	}

	t.Run("invalid instance name", func(t *testing.T) {
		cmd := rabbitMQTestInstallCmd()
		cmd.Flags().Set("name", "bad name")
		err := runAddonInstallRabbitMQ(cmd)
		if err == nil || !strings.Contains(err.Error(), "invalid RabbitMQ instance name") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("unsafe version", func(t *testing.T) {
		newConfig()
		cmd := rabbitMQTestInstallCmd()
		cmd.Flags().Set("version", "bad/version")
		err := runAddonInstallRabbitMQ(cmd)
		if err == nil || !strings.Contains(err.Error(), "not available") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("reserved config option", func(t *testing.T) {
		newConfig()
		cmd := rabbitMQTestInstallCmd()
		cmd.Flags().Set("config-option", "management.tcp.port=9999")
		err := runAddonInstallRabbitMQ(cmd)
		if err == nil || !strings.Contains(err.Error(), "managed by pgcli") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("tls cert without key", func(t *testing.T) {
		newConfig()
		cmd := rabbitMQTestInstallCmd()
		cmd.Flags().Set("tls-cert", "/tmp/c.pem")
		err := runAddonInstallRabbitMQ(cmd)
		if err == nil || !strings.Contains(err.Error(), "--tls-cert and --tls-key must be supplied together") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("certificate does not cover listen", func(t *testing.T) {
		newConfig()
		dir := t.TempDir()
		res, err := certgen.Generate(certgen.Options{Hosts: []string{"example.com"}, RSABits: 2048})
		if err != nil {
			t.Fatal(err)
		}
		certPath := filepath.Join(dir, "bad.crt")
		keyPath := filepath.Join(dir, "bad.key")
		if err := os.WriteFile(certPath, res.CertPEM, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyPath, res.KeyPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := rabbitMQTestInstallCmd()
		cmd.Flags().Set("name", "bad")
		cmd.Flags().Set("listen", "127.0.0.1")
		cmd.Flags().Set("tls-cert", certPath)
		cmd.Flags().Set("tls-key", keyPath)
		err = runAddonInstallRabbitMQ(cmd)
		if err == nil || !strings.Contains(err.Error(), "does not cover the listen address") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestAddonPasswordsIncludesRabbitMQ(t *testing.T) {
	cfg := config.Default()
	cfg.Addons.RabbitMQ = map[string]config.RabbitMQConfig{
		"rmq": {Name: "rmq", AdminPassword: "secret"},
	}
	if got := addonPasswords(cfg)["rabbitmq:rmq"]; got != "secret" {
		t.Errorf("rabbitmq:rmq password = %q, want secret", got)
	}
}

func TestReadRabbitMQSecretFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("pw with spaces \r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readRabbitMQSecretFile(path, "admin password")
	if err != nil || got != "pw with spaces " {
		t.Errorf("readRabbitMQSecretFile = %q, %v; want interior spaces kept and newline stripped", got, err)
	}

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRabbitMQSecretFile(empty, "admin password"); err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Errorf("empty file error = %v", err)
	}

	nul := filepath.Join(dir, "nul")
	if err := os.WriteFile(nul, []byte("a\x00b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRabbitMQSecretFile(nul, "admin password"); err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Errorf("NUL file error = %v", err)
	}

	if _, err := readRabbitMQSecretFile(filepath.Join(dir, "missing"), "admin password"); err == nil || !strings.Contains(err.Error(), "reading") {
		t.Errorf("missing file error = %v", err)
	}
}

func TestResolveRabbitMQEtcdEndpoints(t *testing.T) {
	cfg := config.Default()
	cfg.Addons.Etcd = map[string]config.EtcdConfig{
		"dcs": {Name: "dcs", AdvertiseHost: "10.0.0.5", ClientPort: 2379},
	}

	got, err := resolveRabbitMQEtcdEndpoints(cfg, []string{"dcs"}, nil)
	if err != nil {
		t.Fatalf("local member: %v", err)
	}
	if len(got) != 1 || got[0] != "10.0.0.5:2379" {
		t.Errorf("local member endpoints = %v, want [10.0.0.5:2379]", got)
	}

	got, err = resolveRabbitMQEtcdEndpoints(cfg, []string{"dcs"}, []string{"192.168.1.9:2379"})
	if err != nil {
		t.Fatalf("explicit endpoints: %v", err)
	}
	if len(got) != 1 || got[0] != "192.168.1.9:2379" {
		t.Errorf("explicit endpoints = %v, want explicit to take precedence", got)
	}

	if _, err := resolveRabbitMQEtcdEndpoints(cfg, []string{"missing"}, nil); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown member error = %v", err)
	}

	if got, err := resolveRabbitMQEtcdEndpoints(cfg, nil, nil); err != nil || len(got) != 0 {
		t.Errorf("no endpoints = %v, %v; want empty", got, err)
	}
}

func TestCheckRabbitMQTopologyImmutable(t *testing.T) {
	stored := config.RabbitMQConfig{
		Name:          "mq",
		Cluster:       "lab",
		NodeName:      "rabbit@node1",
		ErlangCookie:  "shared-cookie",
		EtcdEndpoints: []string{"10.0.0.1:2379"},
	}
	next := stored
	if err := checkRabbitMQTopologyImmutable(stored, next); err != nil {
		t.Errorf("unchanged topology = %v", err)
	}

	next = stored
	next.EtcdEndpoints = []string{"10.0.0.2:2379"}
	if err := checkRabbitMQTopologyImmutable(stored, next); err != nil {
		t.Errorf("etcd endpoints must be mutable: %v", err)
	}

	next = stored
	next.Cluster = "other"
	if err := checkRabbitMQTopologyImmutable(stored, next); err == nil || !strings.Contains(err.Error(), "changing --cluster") {
		t.Errorf("cluster change error = %v", err)
	}

	next = stored
	next.NodeName = "rabbit@node2"
	if err := checkRabbitMQTopologyImmutable(stored, next); err == nil || !strings.Contains(err.Error(), "changing --node-name") {
		t.Errorf("node name change error = %v", err)
	}

	next = stored
	next.ErlangCookie = "other-cookie"
	if err := checkRabbitMQTopologyImmutable(stored, next); err == nil || !strings.Contains(err.Error(), "stored Erlang cookie") {
		t.Errorf("cookie change error = %v", err)
	}

	standalone := config.RabbitMQConfig{Name: "mq"}
	next = config.RabbitMQConfig{Name: "mq", Cluster: "lab"}
	if err := checkRabbitMQTopologyImmutable(standalone, next); err == nil || !strings.Contains(err.Error(), "changing --cluster") {
		t.Errorf("standalone-to-cluster error = %v", err)
	}
}

func TestRabbitMQClusterInstallRejectsMissingInputs(t *testing.T) {
	newConfig := func() {
		cfg := config.Default()
		cfg.BaseDir = t.TempDir()
		cfg.RabbitMQStartPort = 33000
		cfg.RabbitMQMgmtStartPort = 33100
		writeRabbitMQTestConfig(t, cfg)
	}
	secretFile := func(content string) string {
		path := filepath.Join(t.TempDir(), "secret")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cookie := secretFile("shared-cookie\n")
	adminPw := secretFile("admin-secret\n")

	baseCluster := func(cmd *cobra.Command) {
		cmd.Flags().Set("cluster", "lab")
		cmd.Flags().Set("node-name", "rabbit@10.0.0.1")
		cmd.Flags().Set("etcd-endpoints", "10.0.0.1:2379")
		cmd.Flags().Set("erlang-cookie-file", cookie)
		cmd.Flags().Set("admin-password-file", adminPw)
	}

	t.Run("cluster fields without cluster", func(t *testing.T) {
		newConfig()
		cmd := rabbitMQTestInstallCmd()
		cmd.Flags().Set("node-name", "rabbit@10.0.0.1")
		err := runAddonInstallRabbitMQ(cmd)
		if err == nil || !strings.Contains(err.Error(), "require --cluster") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("missing node name", func(t *testing.T) {
		newConfig()
		cmd := rabbitMQTestInstallCmd()
		baseCluster(cmd)
		cmd.Flags().Set("node-name", "")
		err := runAddonInstallRabbitMQ(cmd)
		if err == nil || !strings.Contains(err.Error(), "requires --node-name") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("missing etcd endpoints", func(t *testing.T) {
		newConfig()
		cmd := rabbitMQTestInstallCmd()
		baseCluster(cmd)
		cmd.Flags().Set("etcd-endpoints", "")
		err := runAddonInstallRabbitMQ(cmd)
		if err == nil || !strings.Contains(err.Error(), "--etcd-endpoints") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("missing cookie file", func(t *testing.T) {
		newConfig()
		cmd := rabbitMQTestInstallCmd()
		cmd.Flags().Set("cluster", "lab")
		cmd.Flags().Set("node-name", "rabbit@10.0.0.1")
		cmd.Flags().Set("etcd-endpoints", "10.0.0.1:2379")
		cmd.Flags().Set("admin-password-file", adminPw)
		err := runAddonInstallRabbitMQ(cmd)
		if err == nil || !strings.Contains(err.Error(), "requires --erlang-cookie-file") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("missing admin password file", func(t *testing.T) {
		newConfig()
		cmd := rabbitMQTestInstallCmd()
		cmd.Flags().Set("cluster", "lab")
		cmd.Flags().Set("node-name", "rabbit@10.0.0.1")
		cmd.Flags().Set("etcd-endpoints", "10.0.0.1:2379")
		cmd.Flags().Set("erlang-cookie-file", cookie)
		err := runAddonInstallRabbitMQ(cmd)
		if err == nil || !strings.Contains(err.Error(), "requires --admin-password-file") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("plaintext admin password rejected", func(t *testing.T) {
		newConfig()
		cmd := rabbitMQTestInstallCmd()
		cmd.Flags().Set("cluster", "lab")
		cmd.Flags().Set("node-name", "rabbit@10.0.0.1")
		cmd.Flags().Set("etcd-endpoints", "10.0.0.1:2379")
		cmd.Flags().Set("erlang-cookie-file", cookie)
		cmd.Flags().Set("admin-password", "plaintext")
		err := runAddonInstallRabbitMQ(cmd)
		if err == nil || !strings.Contains(err.Error(), "cluster mode rejects --admin-password") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("unreachable etcd", func(t *testing.T) {
		newConfig()
		cmd := rabbitMQTestInstallCmd()
		baseCluster(cmd)
		cmd.Flags().Set("etcd-endpoints", "127.0.0.1:1")
		err := runAddonInstallRabbitMQ(cmd)
		if err == nil || !strings.Contains(err.Error(), "no etcd endpoint is reachable") {
			t.Fatalf("error = %v", err)
		}
	})

	for _, flag := range []string{"advertise-host", "node-id", "peer", "join"} {
		t.Run("rejects --"+flag, func(t *testing.T) {
			newConfig()
			cmd := rabbitMQTestInstallCmd()
			switch flag {
			case "node-id":
				cmd.Flags().Set(flag, "3")
			default:
				cmd.Flags().Set(flag, "value")
			}
			err := runAddonInstallRabbitMQ(cmd)
			if err == nil || !strings.Contains(err.Error(), "does not use --"+flag) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

type rabbitMQEtcdStub struct {
	mu sync.Mutex
	kv map[string]string
}

func newRabbitMQEtcdStub() *rabbitMQEtcdStub {
	return &rabbitMQEtcdStub{kv: make(map[string]string)}
}

func (s *rabbitMQEtcdStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.URL.Path {
	case "/v3/kv/range":
		var req struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		key, _ := base64.StdEncoding.DecodeString(req.Key)
		resp := map[string]any{}
		if value, ok := s.kv[string(key)]; ok {
			resp["kvs"] = []map[string]string{{
				"key":   req.Key,
				"value": base64.StdEncoding.EncodeToString([]byte(value)),
			}}
		}
		_ = json.NewEncoder(w).Encode(resp)
	case "/v3/kv/txn":
		var req struct {
			Success []struct {
				RequestPut struct {
					Key   string `json:"key"`
					Value string `json:"value"`
				} `json:"request_put"`
			} `json:"success"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, op := range req.Success {
			key, _ := base64.StdEncoding.DecodeString(op.RequestPut.Key)
			value, _ := base64.StdEncoding.DecodeString(op.RequestPut.Value)
			s.kv[string(key)] = string(value)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"succeeded": true})
	default:
		http.NotFound(w, r)
	}
}

func (s *rabbitMQEtcdStub) value(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.kv[key]
	return value, ok
}

func (s *rabbitMQEtcdStub) set(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kv[key] = value
}

func (s *rabbitMQEtcdStub) delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.kv, key)
}

func rabbitMQTestSecretGenerateCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "generate"}
	cmd.Flags().String("cluster", "", "")
	cmd.Flags().StringSlice("etcd", nil, "")
	cmd.Flags().StringSlice("etcd-endpoints", nil, "")
	cmd.Flags().String("cookie-file", "", "")
	cmd.Flags().String("admin-password-file", "", "")
	cmd.Flags().Bool("force", false, "")
	cmd.Flags().Bool("keep-cookie", false, "")
	return cmd
}

func TestRabbitMQSecretGeneratePublishesToEtcd(t *testing.T) {
	stub := newRabbitMQEtcdStub()
	srv := httptest.NewServer(stub)
	defer srv.Close()
	endpoint := strings.TrimPrefix(srv.URL, "http://")
	dir := t.TempDir()
	cookiePath := filepath.Join(dir, "rmq.cookie")
	passwordPath := filepath.Join(dir, "rmq.admin")

	cmd := rabbitMQTestSecretGenerateCmd()
	for flag, value := range map[string]string{
		"cluster":             "lab",
		"etcd-endpoints":      endpoint,
		"cookie-file":         cookiePath,
		"admin-password-file": passwordPath,
	} {
		if err := cmd.Flags().Set(flag, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := runAddonRabbitMQSecretGenerate(cmd); err != nil {
		t.Fatalf("generate: %v", err)
	}

	cookieData, err := os.ReadFile(cookiePath)
	if err != nil {
		t.Fatal(err)
	}
	cookie := strings.TrimSpace(string(cookieData))
	if len(cookie) != 64 {
		t.Errorf("cookie length = %d, want 64 hex chars", len(cookie))
	}
	if _, err := hex.DecodeString(cookie); err != nil {
		t.Errorf("cookie is not hex: %v", err)
	}
	passwordData, err := os.ReadFile(passwordPath)
	if err != nil {
		t.Fatal(err)
	}
	password := strings.TrimSpace(string(passwordData))
	if len(password) != 24 {
		t.Errorf("password length = %d, want 24", len(password))
	}
	for _, path := range []string{cookiePath, passwordPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 600", path, info.Mode().Perm())
		}
	}
	if got, ok := stub.value(rabbitmq.ErlangCookieKey("lab")); !ok || got != cookie {
		t.Errorf("etcd cookie = %q (found %v), want file content", got, ok)
	}
	if got, ok := stub.value(rabbitmq.AdminPasswordKey("lab")); !ok || got != password {
		t.Errorf("etcd password = %q (found %v), want file content", got, ok)
	}

	if err := runAddonRabbitMQSecretGenerate(cmd); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second generate without --force = %v, want exists error", err)
	}

	if err := cmd.Flags().Set("force", "true"); err != nil {
		t.Fatal(err)
	}
	if err := runAddonRabbitMQSecretGenerate(cmd); err != nil {
		t.Fatalf("generate --force: %v", err)
	}
	rotated, err := os.ReadFile(cookiePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(rotated)); got == cookie {
		t.Error("--force did not rotate the cookie")
	}
	if got, ok := stub.value(rabbitmq.ErlangCookieKey("lab")); !ok || got != strings.TrimSpace(string(rotated)) {
		t.Errorf("etcd cookie after --force = %q (found %v)", got, ok)
	}

	if err := cmd.Flags().Set("keep-cookie", "true"); err != nil {
		t.Fatal(err)
	}
	passwordAfterForce := readRabbitMQTestFile(t, passwordPath)
	if err := runAddonRabbitMQSecretGenerate(cmd); err != nil {
		t.Fatalf("generate --force --keep-cookie: %v", err)
	}
	if got := readRabbitMQTestFile(t, cookiePath); got != strings.TrimSpace(string(rotated)) {
		t.Errorf("--keep-cookie changed the cookie to %q", got)
	}
	rotatedPassword := readRabbitMQTestFile(t, passwordPath)
	if rotatedPassword == passwordAfterForce {
		t.Error("--keep-cookie did not rotate the admin password")
	}
	if got, ok := stub.value(rabbitmq.ErlangCookieKey("lab")); !ok || got != strings.TrimSpace(string(rotated)) {
		t.Errorf("etcd cookie after --keep-cookie = %q (found %v)", got, ok)
	}
	if got, ok := stub.value(rabbitmq.AdminPasswordKey("lab")); !ok || got != rotatedPassword {
		t.Errorf("etcd password after --keep-cookie = %q (found %v)", got, ok)
	}
}

func readRabbitMQTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func TestRabbitMQSecretGenerateRequiresEtcd(t *testing.T) {
	cmd := rabbitMQTestSecretGenerateCmd()
	cmd.Flags().Set("cluster", "lab")
	cmd.Flags().Set("cookie-file", filepath.Join(t.TempDir(), "c"))
	cmd.Flags().Set("admin-password-file", filepath.Join(t.TempDir(), "p"))
	err := runAddonRabbitMQSecretGenerate(cmd)
	if err == nil || !strings.Contains(err.Error(), "--etcd") {
		t.Fatalf("error = %v, want missing etcd endpoint error", err)
	}
}

func TestReadRabbitMQSecretsFromEtcd(t *testing.T) {
	stub := newRabbitMQEtcdStub()
	srv := httptest.NewServer(stub)
	defer srv.Close()
	endpoints := []string{strings.TrimPrefix(srv.URL, "http://")}
	stub.set(rabbitmq.ErlangCookieKey("lab"), "shared-cookie")
	stub.set(rabbitmq.AdminPasswordKey("lab"), "shared-password")

	cookie, password, err := readRabbitMQSecretsFromEtcd(endpoints, "lab")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if cookie != "shared-cookie" || password != "shared-password" {
		t.Errorf("read = %q / %q", cookie, password)
	}

	stub.delete(rabbitmq.AdminPasswordKey("lab"))
	if _, _, err := readRabbitMQSecretsFromEtcd(endpoints, "lab"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing key error = %v", err)
	}
}

func TestRabbitMQSecretsFromEtcdRejectsFileFlags(t *testing.T) {
	for _, flag := range []string{"erlang-cookie-file", "admin-password-file"} {
		cmd := rabbitMQTestInstallCmd()
		cmd.Flags().Set("secrets-from-etcd", "true")
		cmd.Flags().Set(flag, "value")
		opts := readRabbitMQInstallOptions(cmd.Flags())
		err := applyRabbitMQInstallFlags(&config.RabbitMQConfig{Name: "mq", Cluster: "lab"}, cmd.Flags(), opts)
		if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Errorf("%s with --secrets-from-etcd = %v, want conflict error", flag, err)
		}
	}
}
