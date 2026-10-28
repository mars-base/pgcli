package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
)

func TestKafkaFlagName(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("name", "", "")
	if got := kafkaFlagName(cmd); got != "kafka" {
		t.Errorf("default name = %q, want kafka", got)
	}
	if err := cmd.Flags().Set("name", "k1"); err != nil {
		t.Fatal(err)
	}
	if got := kafkaFlagName(cmd); got != "k1" {
		t.Errorf("name = %q, want k1", got)
	}
}

func TestParseKafkaConfigOptions(t *testing.T) {
	got, err := parseKafkaConfigOptions([]string{"num.partitions=3", "log.retention.hours=168"})
	if err != nil {
		t.Fatalf("parseKafkaConfigOptions: %v", err)
	}
	if got["num.partitions"] != "3" || got["log.retention.hours"] != "168" {
		t.Errorf("parsed = %v", got)
	}
	if _, err := parseKafkaConfigOptions(nil); err != nil {
		t.Errorf("empty options: %v", err)
	}

	bad := []struct {
		options []string
		sub     string
	}{
		{[]string{"num.partitions"}, "KEY=VALUE"},
		{[]string{"=3"}, "KEY=VALUE"},
		{[]string{"listeners=PLAINTEXT://0.0.0.0:9092"}, "managed by pgcli"},
		{[]string{"num.partitions=3\nlog.dirs=/tmp"}, "control characters"},
	}
	for _, tt := range bad {
		if _, err := parseKafkaConfigOptions(tt.options); err == nil || !strings.Contains(err.Error(), tt.sub) {
			t.Errorf("parseKafkaConfigOptions(%v) = %v, want %q", tt.options, err, tt.sub)
		}
	}
}

func TestKafkaCommandTree(t *testing.T) {
	for _, name := range []string{"status", "shell", "topic", "cluster"} {
		if _, _, err := addonKafkaCmd.Find([]string{name}); err != nil {
			t.Errorf("addon kafka %s: %v", name, err)
		}
	}
	for _, name := range []string{"create", "list", "describe", "delete"} {
		if _, _, err := addonKafkaCmd.Find([]string{"topic", name}); err != nil {
			t.Errorf("addon kafka topic %s: %v", name, err)
		}
	}
	if _, _, err := addonKafkaCmd.Find([]string{"cluster", "status"}); err != nil {
		t.Errorf("addon kafka cluster status: %v", err)
	}
}

func TestKafkaTopicName(t *testing.T) {
	valid := []string{"demo", "my.topic_1-2", strings.Repeat("a", 249)}
	for _, name := range valid {
		if !validKafkaTopicName(name) {
			t.Errorf("topic %q should be valid", name)
		}
	}
	for _, name := range []string{"", "..", ".", "has space", "slash/topic", strings.Repeat("a", 250)} {
		if validKafkaTopicName(name) {
			t.Errorf("topic %q should be invalid", name)
		}
	}
}

func TestKafkaInstallFlagsRegistered(t *testing.T) {
	want := map[string]string{
		"broker-port":     "int",
		"controller-port": "int",
		"cluster-id":      "string",
		"advertise-host":  "string",
		"listen":          "string",
		"version":         "string",
		"config-option":   "stringArray",
		"force":           "bool",
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
}

func TestAddonPasswordsExcludesKafka(t *testing.T) {
	cfg := config.Default()
	cfg.Addons.Kafka = map[string]config.KafkaConfig{
		"k1": {Name: "k1"},
	}
	for key := range addonPasswords(cfg) {
		if strings.HasPrefix(key, "kafka:") {
			t.Errorf("kafka has no stored password and must not be indexed: %q", key)
		}
	}
}

func kafkaInstallCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "install"}
	cmd.Flags().String("name", "", "")
	cmd.Flags().String("version", "", "")
	cmd.Flags().String("data-dir", "", "")
	cmd.Flags().String("listen", "", "")
	cmd.Flags().String("advertise-host", "", "")
	cmd.Flags().Int("broker-port", 0, "")
	cmd.Flags().Int("controller-port", 0, "")
	cmd.Flags().String("cluster-id", "", "")
	cmd.Flags().StringArray("config-option", nil, "")
	cmd.Flags().String("cluster", "", "")
	cmd.Flags().StringArray("peer", nil, "")
	cmd.Flags().Int("node-id", 0, "")
	cmd.Flags().Bool("force", false, "")
	return cmd
}

// Phase 1 ships a single combined KRaft node. Cluster flags must be refused
// before any image pull or container work, even though the fields exist.
func TestKafkaInstallRejectsPhaseTwoFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")
	cfg := config.Default()
	cfg.BaseDir = t.TempDir()
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	oldPath := cfgPath
	cfgPath = path
	t.Cleanup(func() { cfgPath = oldPath })

	cases := []struct {
		name  string
		flags map[string]string
	}{
		{"cluster", map[string]string{"cluster": "prod"}},
		{"peer", map[string]string{"peer": "2@10.0.0.2:19093"}},
		{"node-id", map[string]string{"node-id": "2"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cmd := kafkaInstallCmd()
			for key, value := range tt.flags {
				if err := cmd.Flags().Set(key, value); err != nil {
					t.Fatal(err)
				}
			}
			err := runAddonInstallKafka(cmd)
			if err == nil || !strings.Contains(err.Error(), "not implemented yet") {
				t.Fatalf("want multi-node rejection, got %v", err)
			}
		})
	}
}

// Re-installing without --force is a no-op that must not rewrite the stored
// cluster id (nor create a container).
func TestKafkaInstallIdempotentKeepsConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")
	cfg := config.Default()
	cfg.BaseDir = t.TempDir()
	cfg.Addons.Kafka = map[string]config.KafkaConfig{
		"k1": {ClusterID: config.DeriveKafkaClusterID("seed")},
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	seed := cfg.Addons.Kafka["k1"]
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	oldPath := cfgPath
	cfgPath = path
	t.Cleanup(func() { cfgPath = oldPath })

	cmd := kafkaInstallCmd()
	if err := cmd.Flags().Set("name", "k1"); err != nil {
		t.Fatal(err)
	}
	if err := runAddonInstallKafka(cmd); err != nil {
		t.Fatalf("idempotent install: %v", err)
	}

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	kc, ok := got.Addons.Kafka["k1"]
	if !ok {
		t.Fatal("k1 disappeared")
	}
	if kc.ClusterID != seed.ClusterID {
		t.Errorf("ClusterID = %q, want %q", kc.ClusterID, seed.ClusterID)
	}
	if kc.ContainerName != seed.ContainerName || kc.BrokerPort != seed.BrokerPort || kc.ControllerPort != seed.ControllerPort {
		t.Errorf("idempotent install rewrote the instance: got %+v, want %+v", kc, seed)
	}
}
