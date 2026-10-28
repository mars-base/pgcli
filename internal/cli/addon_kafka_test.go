package cli

import (
	"maps"
	"os"
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
	for _, name := range []string{"status", "shell", "topic", "group", "cluster"} {
		if _, _, err := addonKafkaCmd.Find([]string{name}); err != nil {
			t.Errorf("addon kafka %s: %v", name, err)
		}
	}
	for _, name := range []string{"create", "list", "describe", "delete", "retention"} {
		if _, _, err := addonKafkaCmd.Find([]string{"topic", name}); err != nil {
			t.Errorf("addon kafka topic %s: %v", name, err)
		}
	}
	for _, name := range []string{"list", "describe"} {
		cmd, _, err := addonKafkaCmd.Find([]string{"group", name})
		if err != nil {
			t.Errorf("addon kafka group %s: %v", name, err)
			continue
		}
		if cmd.Flags().Lookup("name") == nil {
			t.Errorf("addon kafka group %s: --name flag not registered", name)
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

func TestParseKafkaRetention(t *testing.T) {
	valid := map[string]int64{
		"604800000": 604800000,
		"250ms":     250,
		"30s":       30000,
		"15m":       900000,
		"12h":       43200000,
		"7d":        604800000,
		"4w":        2419200000,
		"-1":        -1,
		"forever":   -1,
		"INFINITE":  -1,
	}
	for value, want := range valid {
		got, err := parseKafkaRetention(value)
		if err != nil || got != want {
			t.Errorf("parseKafkaRetention(%q) = %d, %v; want %d", value, got, err, want)
		}
	}
	for _, value := range []string{"", "-2", "1.5h", "7 days", "d", "9999999999999999999w"} {
		if _, err := parseKafkaRetention(value); err == nil {
			t.Errorf("parseKafkaRetention(%q) should fail", value)
		}
	}
}

func TestKafkaInstallFlagsRegistered(t *testing.T) {
	want := map[string]string{
		"broker-port":     "int",
		"controller-port": "int",
		"tls":             "bool",
		"tls-port":        "int",
		"tls-pem":         "string",
		"cluster-id":      "string",
		"cluster":         "string",
		"peer":            "stringArray",
		"node-id":         "int",
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
	if f := addonInstallCmd.Flags().Lookup("tls"); !strings.Contains(f.Usage, "kafka") {
		t.Errorf("--tls help does not mention kafka: %q", f.Usage)
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
	cmd.Flags().Bool("tls", false, "")
	cmd.Flags().Int("tls-port", 0, "")
	cmd.Flags().String("tls-pem", "", "")
	cmd.Flags().String("cluster-id", "", "")
	cmd.Flags().StringArray("config-option", nil, "")
	cmd.Flags().String("cluster", "", "")
	cmd.Flags().StringArray("peer", nil, "")
	cmd.Flags().Int("node-id", 0, "")
	cmd.Flags().Bool("force", false, "")
	return cmd
}

// writeKafkaTestConfig saves cfg as the active pg.yaml for the duration of t.
func writeKafkaTestConfig(t *testing.T, cfg *config.Config) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pg.yaml")
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	oldPath := cfgPath
	cfgPath = path
	t.Cleanup(func() { cfgPath = oldPath })
	return path
}

// runKafkaInstallFlags runs the install with the given scalar flags and peers.
func runKafkaInstallFlags(t *testing.T, flags map[string]string, peers []string) error {
	t.Helper()
	cmd := kafkaInstallCmd()
	for key, value := range flags {
		if err := cmd.Flags().Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, peer := range peers {
		if err := cmd.Flags().Set("peer", peer); err != nil {
			t.Fatal(err)
		}
	}
	return runAddonInstallKafka(cmd)
}

// clusterTestKafka is a valid installed node 2 of cluster "prod".
func clusterTestKafka(name string) config.KafkaConfig {
	return config.KafkaConfig{
		Name:           name,
		Cluster:        "prod",
		NodeID:         2,
		Listen:         "0.0.0.0",
		AdvertiseHost:  "10.0.0.2",
		ControllerPort: 19093,
		Peers:          []string{"1@10.0.0.1:19093", "3@10.0.0.3:19093"},
		ClusterID:      config.DeriveKafkaClusterID("prod"),
	}
}

func TestKafkaInstallClusterFlagValidation(t *testing.T) {
	base := map[string]string{
		"cluster":         "prod",
		"node-id":         "1",
		"listen":          "0.0.0.0",
		"advertise-host":  "10.0.0.1",
		"controller-port": "19093",
	}
	with := func(changes map[string]string) map[string]string {
		out := make(map[string]string, len(base)+len(changes))
		maps.Copy(out, base)
		for k, v := range changes {
			if v == "" {
				delete(out, k)
				continue
			}
			out[k] = v
		}
		return out
	}
	peer := []string{"2@10.0.0.2:19093"}
	tests := []struct {
		name  string
		flags map[string]string
		peers []string
		want  string
	}{
		{"missing peer", base, nil, "--cluster requires at least one --peer"},
		{"missing node id", with(map[string]string{"node-id": ""}), peer, "cannot determine this node's Kafka cluster node id"},
		{"missing advertise host", with(map[string]string{"advertise-host": ""}), peer, "--cluster requires an explicit --advertise-host"},
		{"missing controller port", with(map[string]string{"controller-port": ""}), peer, "--cluster requires an explicit --controller-port"},
		{"loopback advertise host", with(map[string]string{"advertise-host": "127.0.0.1"}), peer, "--cluster requires an explicit --advertise-host"},
		{"loopback listen", with(map[string]string{"listen": "127.0.0.1"}), peer, "--cluster requires --listen 0.0.0.0"},
		{"mismatched cluster id", with(map[string]string{"cluster-id": config.DeriveKafkaClusterID("other")}), peer, "does not match the id derived"},
		{"invalid peer id", base, []string{"0@10.0.0.2:19093"}, "1-based"},
		{"self peer", base, []string{"1@10.0.0.9:19093"}, "repeats this node"},
		{"duplicate peer endpoint", base, []string{"2@10.0.0.2:19093", "3@10.0.0.2:19093"}, "share the controller endpoint"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.BaseDir = t.TempDir()
			writeKafkaTestConfig(t, cfg)
			err := runKafkaInstallFlags(t, tt.flags, tt.peers)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("install error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestKafkaInstallSingleNodeRejectsClusterFlags(t *testing.T) {
	tests := []struct {
		name  string
		flags map[string]string
		peers []string
		want  string
	}{
		{"peer without cluster", nil, []string{"2@10.0.0.2:19093"}, "--peer requires --cluster"},
		{"node id without cluster", map[string]string{"node-id": "2"}, nil, "--node-id requires --cluster"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.BaseDir = t.TempDir()
			writeKafkaTestConfig(t, cfg)
			err := runKafkaInstallFlags(t, tt.flags, tt.peers)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("install error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestKafkaInstallRejectsClusterIdentityChanges(t *testing.T) {
	tests := []struct {
		name  string
		flags map[string]string
		peers []string
		want  string
	}{
		{"cluster", map[string]string{"force": "true", "cluster": "other"}, nil, "changing --cluster is not supported"},
		{"node id", map[string]string{"force": "true", "node-id": "4"}, nil, "changing --node-id is not supported"},
		{"cluster id", map[string]string{"force": "true", "cluster-id": config.DeriveKafkaClusterID("other")}, nil, "changing --cluster-id is not supported"},
		{"peers", map[string]string{"force": "true"}, []string{"1@10.0.0.1:19093"}, "changing --peer is not supported"},
		{"advertise host", map[string]string{"force": "true", "advertise-host": "10.0.0.99"}, nil, "changing --advertise-host"},
		{"controller port", map[string]string{"force": "true", "controller-port": "19193"}, nil, "changing --controller-port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.BaseDir = t.TempDir()
			cfg.Addons.Kafka = map[string]config.KafkaConfig{"prod": clusterTestKafka("prod")}
			cfg.ApplyDefaults()
			if err := cfg.Validate(); err != nil {
				t.Fatalf("seed Validate: %v", err)
			}
			writeKafkaTestConfig(t, cfg)
			flags := map[string]string{"name": "prod"}
			maps.Copy(flags, tt.flags)
			err := runKafkaInstallFlags(t, flags, tt.peers)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("install error = %v, want %q", err, tt.want)
			}
		})
	}
}

// Re-installing without --force must reject conflicting identity flags instead
// of silently ignoring them.
func TestKafkaInstallNoOpRejectsIdentityChanges(t *testing.T) {
	tests := []struct {
		name  string
		flags map[string]string
		peers []string
		want  string
	}{
		{"cluster", map[string]string{"cluster": "other"}, nil, "changing --cluster is not supported"},
		{"node id", map[string]string{"node-id": "4"}, nil, "changing --node-id is not supported"},
		{"peers", nil, []string{"1@10.0.0.1:19093"}, "changing --peer is not supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.BaseDir = t.TempDir()
			cfg.Addons.Kafka = map[string]config.KafkaConfig{"prod": clusterTestKafka("prod")}
			cfg.ApplyDefaults()
			if err := cfg.Validate(); err != nil {
				t.Fatalf("seed Validate: %v", err)
			}
			writeKafkaTestConfig(t, cfg)
			flags := map[string]string{"name": "prod"}
			maps.Copy(flags, tt.flags)
			err := runKafkaInstallFlags(t, flags, tt.peers)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("install error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestResolveKafkaIdentityFreshCluster(t *testing.T) {
	cfg := config.Default()
	cfg.BaseDir = t.TempDir()
	kc := clusterTestKafka("prod")
	cfg.Addons.Kafka = map[string]config.KafkaConfig{"prod": kc}
	cfg.ApplyDefaults()
	kc = cfg.Addons.Kafka["prod"]

	opts := kafkaInstallOptions{clusterSet: true, nodeIDSet: true}
	if err := resolveKafkaIdentity(&kc, opts, false); err != nil {
		t.Fatalf("resolveKafkaIdentity: %v", err)
	}
	if want := config.DeriveKafkaClusterID("prod"); kc.ClusterID != want {
		t.Errorf("cluster id = %q, want the derived id %q", kc.ClusterID, want)
	}
	if kc.NodeID != 2 {
		t.Errorf("node id = %d, want 2", kc.NodeID)
	}
}

func TestResolveKafkaIdentityAdoptsMeta(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	clusterID := config.DeriveKafkaClusterID("prod")
	meta := "cluster.id=" + clusterID + "\nnode.id=3\n"
	if err := os.WriteFile(filepath.Join(dataDir, "meta.properties"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	kc := clusterTestKafka("prod")
	kc.NodeID = 0
	kc.DataDir = dataDir
	opts := kafkaInstallOptions{}
	if err := resolveKafkaIdentity(&kc, opts, true); err != nil {
		t.Fatalf("resolveKafkaIdentity: %v", err)
	}
	if kc.ClusterID != clusterID || kc.NodeID != 3 {
		t.Errorf("adopted ids = (%s, %d), want (%s, 3)", kc.ClusterID, kc.NodeID, clusterID)
	}

	// A node id > 1 must not silently become a single-node install.
	single := config.KafkaConfig{Name: "prod", DataDir: dataDir}
	if err := resolveKafkaIdentity(&single, opts, false); err == nil || !strings.Contains(err.Error(), "original --cluster and --node-id") {
		t.Fatalf("single-node adoption error = %v", err)
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

	// A clustered node keeps its identity too, and matching flags (in any peer
	// order) are accepted on a no-op re-install.
	clusterCfg := config.Default()
	clusterCfg.BaseDir = t.TempDir()
	clusterCfg.Addons.Kafka = map[string]config.KafkaConfig{"prod": clusterTestKafka("prod")}
	clusterCfg.ApplyDefaults()
	if err := clusterCfg.Validate(); err != nil {
		t.Fatalf("seed Validate: %v", err)
	}
	clusterPath := writeKafkaTestConfig(t, clusterCfg)
	clusterSeed := clusterCfg.Addons.Kafka["prod"]

	err = runKafkaInstallFlags(t,
		map[string]string{
			"name":            "prod",
			"cluster":         "prod",
			"node-id":         "2",
			"advertise-host":  "10.0.0.2",
			"controller-port": "19093",
		},
		[]string{"3@10.0.0.3:19093", "1@10.0.0.1:19093"},
	)
	if err != nil {
		t.Fatalf("idempotent cluster install: %v", err)
	}
	gotCluster, err := config.Load(clusterPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	clusterGot := gotCluster.Addons.Kafka["prod"]
	if clusterGot.ClusterID != clusterSeed.ClusterID || clusterGot.Cluster != clusterSeed.Cluster || clusterGot.NodeID != clusterSeed.NodeID {
		t.Errorf("idempotent cluster install rewrote identity: got %+v, want %+v", clusterGot, clusterSeed)
	}
	if !kafkaPeerSetsEqual(clusterGot.Peers, clusterSeed.Peers) {
		t.Errorf("idempotent cluster install rewrote peers: got %v, want %v", clusterGot.Peers, clusterSeed.Peers)
	}
}

func TestKafkaTLSPortRequiresTLS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")
	cfg := config.Default()
	cfg.BaseDir = t.TempDir()
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	oldPath := cfgPath
	cfgPath = path
	t.Cleanup(func() { cfgPath = oldPath })

	cmd := kafkaInstallCmd()
	if err := cmd.Flags().Set("tls-port", "9093"); err != nil {
		t.Fatal(err)
	}
	err := runAddonInstallKafka(cmd)
	if err == nil || !strings.Contains(err.Error(), "--tls-port requires --tls") {
		t.Fatalf("want --tls-port/--tls error, got %v", err)
	}
}

func TestKafkaTLSPEMFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")
	cfg := config.Default()
	cfg.BaseDir = t.TempDir()
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	oldPath := cfgPath
	cfgPath = path
	t.Cleanup(func() { cfgPath = oldPath })

	// A non-empty PEM cannot be combined with an explicit --tls=false.
	cmd := kafkaInstallCmd()
	if err := cmd.Flags().Set("tls", "false"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("tls-pem", "/tmp/kafka-server.pem"); err != nil {
		t.Fatal(err)
	}
	err := runAddonInstallKafka(cmd)
	if err == nil || !strings.Contains(err.Error(), "cannot be combined with --tls=false") {
		t.Fatalf("conflict error = %v", err)
	}

	// A missing PEM is reported by validation, proving --tls-pem implies TLS
	// without the caller also passing --tls.
	cmd = kafkaInstallCmd()
	if err := cmd.Flags().Set("tls-pem", filepath.Join(t.TempDir(), "missing.pem")); err != nil {
		t.Fatal(err)
	}
	err = runAddonInstallKafka(cmd)
	if err == nil || !strings.Contains(err.Error(), "--tls-pem: reading") {
		t.Fatalf("missing PEM error = %v", err)
	}
}
