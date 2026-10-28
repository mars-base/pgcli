package podman

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/kafka"
)

func testKafkaNode(t *testing.T) config.KafkaConfig {
	t.Helper()
	dir := t.TempDir()
	return config.KafkaConfig{
		ContainerName:  "pgcli-kafka-test",
		Name:           "test",
		Version:        config.DefaultKafkaVersion,
		ImageTag:       config.DefaultKafkaImageTag,
		ConfigPath:     filepath.Join(dir, "config", "server.properties"),
		DataDir:        filepath.Join(dir, "data"),
		Listen:         "127.0.0.1",
		AdvertiseHost:  "127.0.0.1",
		BrokerPort:     9092,
		ControllerPort: 19093,
		NodeID:         1,
		ClusterID:      "aaaaaaaaaaaaaaaaaaaaaa",
	}
}

func TestKafkaCreateArgs(t *testing.T) {
	kc := testKafkaNode(t)
	got := kafkaCreateArgs(kc)
	want := []string{
		"run", "-d",
		"--name", "pgcli-kafka-test",
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--stop-timeout", "30",
		"-v", kc.DataDir + ":" + kafka.ContainerDataDir + ":z",
		"-v", filepath.Dir(kc.ConfigPath) + ":" + kafka.ContainerConfigDir + ":ro,z",
		"-e", "CLUSTER_ID=" + kc.ClusterID,
		kc.ImageTag,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("kafkaCreateArgs:\n got %q\nwant %q", got, want)
	}
}

func TestKafkaCreateArgsUsesStoredPaths(t *testing.T) {
	kc := testKafkaNode(t)
	outside := filepath.Join(t.TempDir(), "custom-data")
	kc.DataDir = outside
	got := kafkaCreateArgs(kc)
	for i := range len(got) {
		if got[i] == "-v" && i+1 < len(got) && got[i+1] == outside+":"+kafka.ContainerDataDir+":z" {
			return
		}
	}
	t.Errorf("kafkaCreateArgs does not mount the configured data dir %q: %q", outside, got)
}

func TestKafkaExecArgsTTY(t *testing.T) {
	got := kafkaExecArgs("c", true, true, "bash")
	if want := []string{"exec", "-it", "c", "bash"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tty argv = %q, want %q", got, want)
	}
	// Without a terminal, -t makes podman hang before starting the command.
	got = kafkaExecArgs("c", true, false, "bash", "-lc", "true")
	if want := []string{"exec", "-i", "c", "bash", "-lc", "true"}; !reflect.DeepEqual(got, want) {
		t.Errorf("non-tty argv = %q, want %q", got, want)
	}
	got = kafkaExecArgs("c", false, true, "true")
	if want := []string{"exec", "c", "true"}; !reflect.DeepEqual(got, want) {
		t.Errorf("non-interactive argv = %q, want %q", got, want)
	}
}
