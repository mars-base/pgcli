package cli

import (
	"maps"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
)

func TestRocketMQFlagName(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("name", "", "")
	if got := rocketMQFlagName(cmd); got != "rocketmq" {
		t.Errorf("default name = %q, want rocketmq", got)
	}
	if err := cmd.Flags().Set("name", "rmq"); err != nil {
		t.Fatal(err)
	}
	if got := rocketMQFlagName(cmd); got != "rmq" {
		t.Errorf("name = %q, want rmq", got)
	}
}

func TestParseRocketMQConfigOptions(t *testing.T) {
	got, err := parseRocketMQConfigOptions([]string{"flushDiskType=SYNC_FLUSH", "brokerPermission=4"})
	if err != nil {
		t.Fatalf("parseRocketMQConfigOptions: %v", err)
	}
	if got["flushDiskType"] != "SYNC_FLUSH" || got["brokerPermission"] != "4" {
		t.Errorf("parsed = %v", got)
	}
	if _, err := parseRocketMQConfigOptions(nil); err != nil {
		t.Errorf("empty options: %v", err)
	}

	bad := []struct {
		options []string
		sub     string
	}{
		{[]string{"flushDiskType"}, "KEY=VALUE"},
		{[]string{"=SYNC_FLUSH"}, "KEY=VALUE"},
		{[]string{"brokerRole=SYNC_MASTER"}, "managed by pgcli"},
		{[]string{"controllerAddr=10.0.0.1:9877"}, "managed by pgcli"},
		{[]string{"flushDiskType=SYNC\nFLUSH"}, "control characters"},
	}
	for _, tt := range bad {
		if _, err := parseRocketMQConfigOptions(tt.options); err == nil || !strings.Contains(err.Error(), tt.sub) {
			t.Errorf("parseRocketMQConfigOptions(%v) = %v, want %q", tt.options, err, tt.sub)
		}
	}
}

func TestRocketMQCommandTree(t *testing.T) {
	for _, name := range []string{"status", "shell", "topic", "send", "cluster"} {
		if _, _, err := addonRocketMQCmd.Find([]string{name}); err != nil {
			t.Errorf("addon rocketmq %s: %v", name, err)
		}
	}
	for _, name := range []string{"create", "list", "describe", "delete"} {
		cmd, _, err := addonRocketMQCmd.Find([]string{"topic", name})
		if err != nil {
			t.Errorf("addon rocketmq topic %s: %v", name, err)
			continue
		}
		if cmd.Flags().Lookup("name") == nil {
			t.Errorf("addon rocketmq topic %s: --name flag not registered", name)
		}
	}
	cmd, _, err := addonRocketMQCmd.Find([]string{"cluster", "status"})
	if err != nil {
		t.Fatalf("addon rocketmq cluster status: %v", err)
	}
	if cmd.Flags().Lookup("more") == nil {
		t.Error("rocketmq cluster status: --more flag not registered")
	}
	if cmd.Flags().Lookup("name") == nil {
		t.Error("rocketmq cluster status: --name flag not registered")
	}
	shell, _, err := addonRocketMQCmd.Find([]string{"shell"})
	if err != nil {
		t.Fatal(err)
	}
	if f := shell.Flags().Lookup("service"); f == nil || f.DefValue != "broker" {
		t.Errorf("rocketmq shell --service = %v, want default broker", f)
	}
	send, _, err := addonRocketMQCmd.Find([]string{"send"})
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"key", "tags"} {
		if send.Flags().Lookup(flag) == nil {
			t.Errorf("rocketmq send --%s not registered", flag)
		}
	}
}

func TestRocketMQInstallFlagsRegistered(t *testing.T) {
	want := map[string]string{
		"namesrv-port":    "int",
		"broker-port":     "int",
		"proxy-port":      "int",
		"grpc-port":       "int",
		"controller-port": "int",
		"controller-id":   "string",
		"namesrv-heap":    "string",
		"proxy-heap":      "string",
		"heap":            "string",
		"cluster":         "string",
		"peer":            "stringArray",
		"advertise-host":  "string",
		"config-option":   "stringArray",
		"version":         "string",
		"data-dir":        "string",
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
	for _, name := range []string{"controller-id", "namesrv-port", "proxy-port", "grpc-port", "namesrv-heap", "proxy-heap"} {
		if f := addonInstallCmd.Flags().Lookup(name); f != nil && !strings.Contains(f.Usage, "RocketMQ") {
			t.Errorf("--%s help does not mention RocketMQ: %q", name, f.Usage)
		}
	}
}

func TestRocketMQTopicName(t *testing.T) {
	for _, name := range []string{"demo", "my-topic_1", "A1", strings.Repeat("a", 127)} {
		if !validRocketMQTopicName(name) {
			t.Errorf("topic %q should be valid", name)
		}
	}
	for _, name := range []string{"", "my.topic", "has space", "slash/topic", "%RETRY%g", strings.Repeat("a", 128)} {
		if validRocketMQTopicName(name) {
			t.Errorf("topic %q should be invalid", name)
		}
	}
}

func TestRocketMQSystemTopic(t *testing.T) {
	system := []string{
		"%RETRY%demo", "%DLQ%demo", "TBW102", "SELF_TEST_TOPIC",
		"RMQ_SYS_TRANS_HALF_TOPIC", "RMQ_SYS_SCHEDULE_TOPIC",
		"RMQ_SYS_ROCKSDB_TRANS_HALF_TOPIC", "RMQ_SYS_ROCKSDB_TRANS_OP_HALF_TOPIC",
		"DefaultHeartBeatSyncerTopic", "rmq_sys_wheel_timer",
		"rmq_sys_REVIVE_LOG_rmq", "rmq_sys_SYNC_BROKER_MEMBER_rmq",
		"rmq_REPLY_TOPIC", "pgcli-lab_REPLY_TOPIC",
	}
	for _, topic := range system {
		if !rocketMQSystemTopic(topic) {
			t.Errorf("topic %q should be treated as system", topic)
		}
	}
	for _, topic := range []string{"demo", "surge", "RetryTopic", "sys_lab"} {
		if rocketMQSystemTopic(topic) {
			t.Errorf("topic %q should be user-visible", topic)
		}
	}
}

func TestRocketMQPeersEqual(t *testing.T) {
	a := []string{"n0@10.0.0.1", "n1@10.0.0.2", "n2@10.0.0.3"}
	b := []string{"n2@10.0.0.3", "n0@10.0.0.1", "n1@10.0.0.2"}
	if !rocketMQPeersEqual(a, b) {
		t.Error("peer sets with different order should be equal")
	}
	if rocketMQPeersEqual(a, []string{"n0@10.0.0.1", "n1@10.0.0.9", "n2@10.0.0.3"}) {
		t.Error("peer sets with a different host should differ")
	}
	if rocketMQPeersEqual(a, a[:2]) {
		t.Error("peer sets with a different size should differ")
	}
	if rocketMQPeersEqual([]string{"bad"}, []string{"bad"}) {
		t.Error("invalid peers should not compare equal")
	}
}

func TestRocketMQClusterSummary(t *testing.T) {
	output := `#Cluster Name           #Broker Name            #BID  #Addr                  #Version              #InTPS(LOAD)  #OutTPS(LOAD)
prod                     prod                    0     10.0.0.1:10911         V5_5_0                 0.00(0,0ms)   0.00(0,0ms)
prod                     prod                    1     10.0.0.2:10911         V5_5_0                 0.00(0,0ms)   0.00(0,0ms)
prod                     prod                    2     10.0.0.3:10911         V5_5_0                 0.00(0,0ms)   0.00(0,0ms)
other                    other                   0     10.0.0.9:10911         V5_5_0                 0.00(0,0ms)   0.00(0,0ms)
`
	brokers, masters := rocketMQClusterSummary(output, "prod")
	if brokers != 3 || masters != 1 {
		t.Errorf("summary = %d brokers/%d masters, want 3/1", brokers, masters)
	}
	legacy := `#ClusterName     BrokerName      BrokerId  InSyncBrokerNum  Address                    HA address
prod             prod            0         3                10.0.0.1:10911             10.0.0.1:10912
prod             prod            1         3                10.0.0.2:10911             10.0.0.2:10912
`
	if brokers, masters := rocketMQClusterSummary(legacy, "prod"); brokers != 2 || masters != 1 {
		t.Errorf("legacy summary = %d/%d, want 2/1", brokers, masters)
	}
	if brokers, masters := rocketMQClusterSummary("#ClusterName BrokerName\n", "prod"); brokers != 0 || masters != 0 {
		t.Errorf("header-only summary = %d/%d", brokers, masters)
	}
}

func rocketMQInstallCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "install"}
	cmd.Flags().String("name", "", "")
	cmd.Flags().String("version", "", "")
	cmd.Flags().String("data-dir", "", "")
	cmd.Flags().String("cluster", "", "")
	cmd.Flags().StringArray("peer", nil, "")
	cmd.Flags().String("advertise-host", "", "")
	cmd.Flags().String("controller-id", "", "")
	cmd.Flags().Int("namesrv-port", 0, "")
	cmd.Flags().Int("broker-port", 0, "")
	cmd.Flags().Int("proxy-port", 0, "")
	cmd.Flags().Int("grpc-port", 0, "")
	cmd.Flags().Int("controller-port", 0, "")
	cmd.Flags().String("heap", "", "")
	cmd.Flags().String("namesrv-heap", "", "")
	cmd.Flags().String("proxy-heap", "", "")
	cmd.Flags().StringArray("config-option", nil, "")
	cmd.Flags().Bool("force", false, "")
	// Shared flags that RocketMQ must reject.
	cmd.Flags().String("listen", "", "")
	cmd.Flags().Bool("tls", false, "")
	cmd.Flags().String("tls-pem", "", "")
	cmd.Flags().String("tls-cert", "", "")
	cmd.Flags().String("tls-key", "", "")
	cmd.Flags().Int("node-id", 0, "")
	cmd.Flags().String("cluster-id", "", "")
	cmd.Flags().String("join", "", "")
	cmd.Flags().String("image", "", "")
	return cmd
}

func writeRocketMQTestConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pg.yaml")
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	oldPath := cfgPath
	cfgPath = path
	t.Cleanup(func() { cfgPath = oldPath })
}

func runRocketMQInstallFlags(t *testing.T, flags map[string]string, peers []string) error {
	t.Helper()
	cmd := rocketMQInstallCmd()
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
	return runAddonInstallRocketMQ(cmd)
}

func rocketMQTestClusterInstance(name string) config.RocketMQConfig {
	return config.RocketMQConfig{
		Name:          name,
		Cluster:       "prod",
		AdvertiseHost: "10.0.0.1",
		ControllerID:  "n0",
		Peers:         []string{"n0@10.0.0.1", "n1@10.0.0.2", "n2@10.0.0.3"},
	}
}

func TestRocketMQInstallRejectsUnsupportedFlags(t *testing.T) {
	for _, flag := range []string{
		"listen", "tls", "tls-pem", "tls-cert", "tls-key",
		"node-id", "cluster-id", "join", "image",
	} {
		t.Run(flag, func(t *testing.T) {
			cfg := config.Default()
			cfg.BaseDir = t.TempDir()
			writeRocketMQTestConfig(t, cfg)
			value := "x"
			if flag == "tls" {
				value = "true"
			} else if flag == "node-id" {
				value = "1"
			}
			err := runRocketMQInstallFlags(t, map[string]string{flag: value}, nil)
			if err == nil || !strings.Contains(err.Error(), "--"+flag+" is not supported for RocketMQ") {
				t.Fatalf("install error = %v, want unsupported --%s", err, flag)
			}
		})
	}
}

func TestRocketMQInstallClusterFlagValidation(t *testing.T) {
	base := map[string]string{
		"cluster":         "prod",
		"controller-id":   "n0",
		"advertise-host":  "10.0.0.1",
		"controller-port": "9877",
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
	peer := []string{"n1@10.0.0.2"}
	tests := []struct {
		name  string
		flags map[string]string
		peers []string
		want  string
	}{
		{"missing peer", base, nil, "--cluster requires at least one --peer"},
		{"missing controller id", with(map[string]string{"controller-id": ""}), peer, "--cluster requires an explicit --controller-id"},
		{"missing advertise host", with(map[string]string{"advertise-host": ""}), peer, "--cluster requires an explicit --advertise-host"},
		{"loopback advertise host", with(map[string]string{"advertise-host": "127.0.0.1"}), peer, "loopback"},
		{"invalid controller id", with(map[string]string{"controller-id": "n-0"}), peer, "controller_id"},
		{"peer with port", base, []string{"n1@10.0.0.2:9877"}, "without a port"},
		{"peer hostname", base, []string{"n1@node2"}, "IPv4"},
		{"version 4", with(map[string]string{"version": "4.9.0"}), peer, "5.x"},
		{"fixed namesrv port", with(map[string]string{"namesrv-port": "19999"}), peer, "--namesrv-port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.BaseDir = t.TempDir()
			writeRocketMQTestConfig(t, cfg)
			err := runRocketMQInstallFlags(t, tt.flags, tt.peers)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("install error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRocketMQInstallSingleNodeRejectsClusterFlags(t *testing.T) {
	tests := []struct {
		name  string
		flags map[string]string
		peers []string
		want  string
	}{
		{"peer without cluster", nil, []string{"n1@10.0.0.2"}, "--peer requires --cluster"},
		{"controller id without cluster", map[string]string{"controller-id": "n0"}, nil, "--controller-id requires --cluster"},
		{"controller port without cluster", map[string]string{"controller-port": "9877"}, nil, "--controller-port requires --cluster"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.BaseDir = t.TempDir()
			writeRocketMQTestConfig(t, cfg)
			err := runRocketMQInstallFlags(t, tt.flags, tt.peers)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("install error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRocketMQInstallRejectsClusterIdentityChanges(t *testing.T) {
	tests := []struct {
		name  string
		flags map[string]string
		peers []string
		want  string
	}{
		{"cluster", map[string]string{"force": "true", "cluster": "other"}, nil, "changing --cluster is not supported"},
		{"controller id", map[string]string{"force": "true", "controller-id": "n9"}, nil, "changing --controller-id is not supported"},
		{"advertise host", map[string]string{"force": "true", "advertise-host": "10.0.0.99"}, nil, "changing --advertise-host"},
		{"peers", map[string]string{"force": "true"}, []string{"n1@10.0.0.9"}, "changing --peer is not supported"},
		{"namesrv port", map[string]string{"force": "true", "namesrv-port": "19999"}, nil, "--namesrv-port"},
		{"broker port", map[string]string{"force": "true", "broker-port": "20911"}, nil, "--broker-port"},
		{"proxy port", map[string]string{"force": "true", "proxy-port": "18080"}, nil, "--proxy-port"},
		{"grpc port", map[string]string{"force": "true", "grpc-port": "18081"}, nil, "--grpc-port"},
		{"controller port", map[string]string{"force": "true", "controller-port": "19877"}, nil, "--controller-port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.BaseDir = t.TempDir()
			cfg.Addons.RocketMQ = map[string]config.RocketMQConfig{"prod": rocketMQTestClusterInstance("prod")}
			cfg.ApplyDefaults()
			if err := cfg.Validate(); err != nil {
				t.Fatalf("seed Validate: %v", err)
			}
			writeRocketMQTestConfig(t, cfg)
			flags := map[string]string{"name": "prod"}
			maps.Copy(flags, tt.flags)
			err := runRocketMQInstallFlags(t, flags, tt.peers)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("install error = %v, want %q", err, tt.want)
			}
		})
	}
}

// Re-installing without --force must reject conflicting identity flags instead
// of silently ignoring them.
func TestRocketMQInstallNoOpRejectsIdentityChanges(t *testing.T) {
	tests := []struct {
		name  string
		flags map[string]string
		peers []string
		want  string
	}{
		{"cluster", map[string]string{"cluster": "other"}, nil, "changing --cluster is not supported"},
		{"controller id", map[string]string{"controller-id": "n9"}, nil, "changing --controller-id is not supported"},
		{"advertise host", map[string]string{"advertise-host": "10.0.0.99"}, nil, "changing --advertise-host"},
		{"peers", nil, []string{"n1@10.0.0.9"}, "changing --peer is not supported"},
		{"namesrv port", map[string]string{"namesrv-port": "19999"}, nil, "cluster mode fixes --namesrv-port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.BaseDir = t.TempDir()
			cfg.Addons.RocketMQ = map[string]config.RocketMQConfig{"prod": rocketMQTestClusterInstance("prod")}
			cfg.ApplyDefaults()
			if err := cfg.Validate(); err != nil {
				t.Fatalf("seed Validate: %v", err)
			}
			writeRocketMQTestConfig(t, cfg)
			flags := map[string]string{"name": "prod"}
			maps.Copy(flags, tt.flags)
			err := runRocketMQInstallFlags(t, flags, tt.peers)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("install error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRocketMQInstallRejectsTopologyConversion(t *testing.T) {
	t.Run("single node to cluster", func(t *testing.T) {
		cfg := config.Default()
		cfg.BaseDir = t.TempDir()
		cfg.Addons.RocketMQ = map[string]config.RocketMQConfig{"rmq": {}}
		cfg.ApplyDefaults()
		if err := cfg.Validate(); err != nil {
			t.Fatalf("seed Validate: %v", err)
		}
		writeRocketMQTestConfig(t, cfg)
		err := runRocketMQInstallFlags(t, map[string]string{
			"name": "rmq", "force": "true", "cluster": "prod",
			"controller-id": "n0", "advertise-host": "10.0.0.1",
		}, []string{"n1@10.0.0.2"})
		if err == nil || !strings.Contains(err.Error(), "not an in-place change") {
			t.Fatalf("install error = %v, want in-place conversion rejection", err)
		}
	})
	t.Run("cluster to single node", func(t *testing.T) {
		cfg := config.Default()
		cfg.BaseDir = t.TempDir()
		cfg.Addons.RocketMQ = map[string]config.RocketMQConfig{"prod": rocketMQTestClusterInstance("prod")}
		cfg.ApplyDefaults()
		if err := cfg.Validate(); err != nil {
			t.Fatalf("seed Validate: %v", err)
		}
		writeRocketMQTestConfig(t, cfg)
		err := runRocketMQInstallFlags(t, map[string]string{
			"name": "prod", "force": "true", "cluster": "",
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "changing --cluster is not supported") {
			t.Fatalf("install error = %v, want cluster removal rejection", err)
		}
	})
}
