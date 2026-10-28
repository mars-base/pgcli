package podman

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/rabbitmq"
)

func testRabbitMQNode(t *testing.T) config.RabbitMQConfig {
	t.Helper()
	dir := t.TempDir()
	return config.RabbitMQConfig{
		ContainerName:     "pgcli-rabbitmq-test",
		Name:              "test",
		Version:           config.DefaultRabbitMQVersion,
		ImageTag:          config.DefaultRabbitMQImageTag,
		ConfigPath:        filepath.Join(dir, "config", "rabbitmq.conf"),
		DataDir:           filepath.Join(dir, "data"),
		SecretPath:        filepath.Join(dir, "secrets", "admin_password"),
		Listen:            "127.0.0.1",
		AMQPPort:          5672,
		AMQPTLSPort:       5671,
		ManagementPort:    15672,
		ManagementTLSPort: 15671,
		EPMDPort:          4369,
		DistPort:          25672,
		AdminUser:         "admin",
		AdminPassword:     "secret",
	}
}

func TestRabbitMQCreateArgs(t *testing.T) {
	rc := testRabbitMQNode(t)
	got := rabbitmqCreateArgs(rc)
	want := []string{
		"run", "-d",
		"--name", "pgcli-rabbitmq-test",
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--stop-timeout", "30",
		"-v", rc.DataDir + ":" + rabbitmq.ContainerDataDir + ":z",
		"-v", rc.ConfigPath + ":" + rabbitmq.ContainerConfigFile + ":ro,z",
		"-v", filepath.Join(filepath.Dir(rc.ConfigPath), "enabled_plugins") + ":" + rabbitmq.ContainerPluginsFile + ":ro,z",
		"-e", "RABBITMQ_NODENAME=rabbit@localhost",
		"-e", "ERL_EPMD_ADDRESS=127.0.0.1",
		"-e", "ERL_EPMD_PORT=4369",
		"-e", "RABBITMQ_DIST_PORT=25672",
		"-e", "RABBITMQ_DEFAULT_USER=admin",
		"-e", "RABBITMQ_DEFAULT_PASS=secret",
		rc.ImageTag,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rabbitmqCreateArgs:\n got %q\nwant %q", got, want)
	}
}

func TestRabbitMQCreateArgsNoPublishedPorts(t *testing.T) {
	got := rabbitmqCreateArgs(testRabbitMQNode(t))
	for _, arg := range got {
		if arg == "-p" || arg == "--publish" {
			t.Fatalf("host networking must not publish ports: %q", got)
		}
	}
}

func TestRabbitMQCreateArgsTLSMount(t *testing.T) {
	rc := testRabbitMQNode(t)
	rc.TLS = true
	got := rabbitmqCreateArgs(rc)
	want := filepath.Join(filepath.Dir(rc.ConfigPath), "tls") + ":" + rabbitmq.ContainerTLSDir + ":ro,z"
	for i := 0; i+1 < len(got); i++ {
		if got[i] == "-v" && got[i+1] == want {
			return
		}
	}
	t.Errorf("rabbitmqCreateArgs does not mount the TLS dir %q: %q", want, got)
}

func TestRabbitMQCreateArgsUsesStoredPaths(t *testing.T) {
	rc := testRabbitMQNode(t)
	outside := filepath.Join(t.TempDir(), "custom-data")
	rc.DataDir = outside
	got := rabbitmqCreateArgs(rc)
	for i := 0; i+1 < len(got); i++ {
		if got[i] == "-v" && got[i+1] == outside+":"+rabbitmq.ContainerDataDir+":z" {
			return
		}
	}
	t.Errorf("rabbitmqCreateArgs does not mount the configured data dir %q: %q", outside, got)
}

func TestRabbitMQExecArgsTTY(t *testing.T) {
	got := rabbitMQExecArgs("c", true, true, "/bin/bash")
	if want := []string{"exec", "-it", "c", "/bin/bash"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tty argv = %q, want %q", got, want)
	}
	got = rabbitMQExecArgs("c", true, false, "true")
	if want := []string{"exec", "-i", "c", "true"}; !reflect.DeepEqual(got, want) {
		t.Errorf("non-tty argv = %q, want %q", got, want)
	}
	got = rabbitMQExecArgs("c", false, true, "true")
	if want := []string{"exec", "c", "true"}; !reflect.DeepEqual(got, want) {
		t.Errorf("non-interactive argv = %q, want %q", got, want)
	}
}

func TestQueueCtlArgs(t *testing.T) {
	got := QueueCtlArgs("/", "list_queues", "name", "messages")
	want := []string{"-q", "--vhost", "/", "list_queues", "name", "messages"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("QueueCtlArgs = %q, want %q", got, want)
	}
	got = QueueCtlArgs("orders", "delete_queue", "q1", "--if-empty")
	want = []string{"-q", "--vhost", "orders", "delete_queue", "q1", "--if-empty"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("QueueCtlArgs = %q, want %q", got, want)
	}
}
