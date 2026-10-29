package podman

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func rocketMQTestStandalone() config.RocketMQConfig {
	return config.RocketMQConfig{
		ContainerName: "pgcli-rocketmq-rmq",
		Name:          "rmq",
		Version:       "5.5.0",
		ImageTag:      "docker.io/apache/rocketmq:5.5.0",
		ConfigDir:     "/srv/pg/addon/rocketmq/rmq/config",
		DataDir:       "/srv/pg/addon/rocketmq/rmq/data",
		AdvertiseHost: "127.0.0.1",
		NamesrvPort:   9876,
		BrokerPort:    10911,
		ProxyPort:     8080,
		GRPCPort:      8081,
		Heap:          "512m",
		NamesrvHeap:   "256m",
		ProxyHeap:     "256m",
	}
}

func rocketMQTestCluster() config.RocketMQConfig {
	k := rocketMQTestStandalone()
	k.Cluster = "lab"
	k.AdvertiseHost = "10.0.0.1"
	k.ControllerID = "n0"
	k.Peers = []string{"n0@10.0.0.1", "n1@10.0.0.2", "n2@10.0.0.3"}
	k.ControllerPort = 9877
	return k
}

func TestRocketMQCreateArgsNamesrvStandalone(t *testing.T) {
	got, err := rocketMQCreateArgs(rocketMQTestStandalone(), RocketMQRoleNamesrv)
	if err != nil {
		t.Fatalf("rocketMQCreateArgs: %v", err)
	}
	want := []string{
		"run", "-d",
		"--name", "pgcli-rocketmq-rmq-namesrv",
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--stop-timeout", "30",
		"-v", "/srv/pg/addon/rocketmq/rmq/config:/etc/rocketmq:ro,z",
		"-v", "/srv/pg/addon/rocketmq/rmq/logs/namesrv:/home/rocketmq/logs:z",
		"-e", "JAVA_OPT_EXT=-Xms256m -Xmx256m",
		"docker.io/apache/rocketmq:5.5.0",
		"sh", "mqnamesrv", "-c", "/etc/rocketmq/namesrv.conf",
	}
	assertArgs(t, got, want)
}

func TestRocketMQCreateArgsBrokerStandalone(t *testing.T) {
	got, err := rocketMQCreateArgs(rocketMQTestStandalone(), RocketMQRoleBroker)
	if err != nil {
		t.Fatalf("rocketMQCreateArgs: %v", err)
	}
	want := []string{
		"run", "-d",
		"--name", "pgcli-rocketmq-rmq-broker",
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--stop-timeout", "30",
		"-v", "/srv/pg/addon/rocketmq/rmq/config:/etc/rocketmq:ro,z",
		"-v", "/srv/pg/addon/rocketmq/rmq/data/broker:/home/rocketmq/store:z",
		"-v", "/srv/pg/addon/rocketmq/rmq/logs/broker:/home/rocketmq/logs:z",
		"-e", "JAVA_OPT_EXT=-Xms512m -Xmx512m",
		"docker.io/apache/rocketmq:5.5.0",
		"sh", "mqbroker", "-c", "/etc/rocketmq/broker.conf",
	}
	assertArgs(t, got, want)
}

func TestRocketMQCreateArgsProxyStandalone(t *testing.T) {
	got, err := rocketMQCreateArgs(rocketMQTestStandalone(), RocketMQRoleProxy)
	if err != nil {
		t.Fatalf("rocketMQCreateArgs: %v", err)
	}
	want := []string{
		"run", "-d",
		"--name", "pgcli-rocketmq-rmq-proxy",
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--stop-timeout", "30",
		"-v", "/srv/pg/addon/rocketmq/rmq/config:/etc/rocketmq:ro,z",
		"-v", "/srv/pg/addon/rocketmq/rmq/logs/proxy:/home/rocketmq/logs:z",
		"-e", "JAVA_OPT_EXT=-Xms256m -Xmx256m",
		"-e", "NAMESRV_ADDR=127.0.0.1:9876",
		"docker.io/apache/rocketmq:5.5.0",
		"sh", "mqproxy", "-pc", "/etc/rocketmq/rmq-proxy.json", "-pm", "cluster",
	}
	assertArgs(t, got, want)
}

func TestRocketMQCreateArgsClusterNamesrvMountsController(t *testing.T) {
	got, err := rocketMQCreateArgs(rocketMQTestCluster(), RocketMQRoleNamesrv)
	if err != nil {
		t.Fatalf("rocketMQCreateArgs: %v", err)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "-v /srv/pg/addon/rocketmq/rmq/data/controller:/home/rocketmq/DledgerController:z") {
		t.Errorf("cluster namesrv missing controller mount:\n%s", joined)
	}
	if !strings.Contains(joined, "-v /srv/pg/addon/rocketmq/rmq/logs/namesrv:/home/rocketmq/logs:z") {
		t.Errorf("cluster namesrv missing log mount:\n%s", joined)
	}
}

func TestRocketMQCreateArgsClusterProxyNamesrvList(t *testing.T) {
	got, err := rocketMQCreateArgs(rocketMQTestCluster(), RocketMQRoleProxy)
	if err != nil {
		t.Fatalf("rocketMQCreateArgs: %v", err)
	}
	if !strings.Contains(strings.Join(got, " "), "-e NAMESRV_ADDR=10.0.0.1:9876;10.0.0.2:9876;10.0.0.3:9876") {
		t.Errorf("cluster proxy missing full nameserver list:\n%s", strings.Join(got, " "))
	}
}

func TestRocketMQCreateArgsFallbackHeap(t *testing.T) {
	k := rocketMQTestStandalone()
	k.NamesrvHeap = ""
	k.ProxyHeap = ""
	namesrv, err := rocketMQCreateArgs(k, RocketMQRoleNamesrv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(namesrv, " "), "JAVA_OPT_EXT=-Xms512m -Xmx512m") {
		t.Errorf("namesrv heap did not fall back to broker heap: %v", namesrv)
	}
	proxy, err := rocketMQCreateArgs(k, RocketMQRoleProxy)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(proxy, " "), "JAVA_OPT_EXT=-Xms512m -Xmx512m") {
		t.Errorf("proxy heap did not fall back to broker heap: %v", proxy)
	}
}

func TestRocketMQCreateArgsUnknownRole(t *testing.T) {
	if _, err := rocketMQCreateArgs(rocketMQTestStandalone(), "controller"); err == nil {
		t.Fatal("unknown role accepted")
	}
}

func TestRocketMQAdminArgsAddsNamesrv(t *testing.T) {
	got := rocketMQAdminArgs(rocketMQTestStandalone(), "clusterList")
	want := []string{
		"run", "--rm", "--network", "host", "--http-proxy=false",
		"-e", "JAVA_OPT_EXT=-Xms192m -Xmx192m",
		"docker.io/apache/rocketmq:5.5.0", "sh", "mqadmin",
		"clusterList", "-n", "127.0.0.1:9876",
	}
	assertArgs(t, got, want)

	got = rocketMQAdminArgs(rocketMQTestStandalone(), "topicList", "-n", "10.0.0.9:9876")
	count := 0
	for _, arg := range got {
		if arg == "-n" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("explicit -n duplicated: %v", got)
	}
}

func TestRocketMQBrokerRegistered(t *testing.T) {
	output := `#ClusterName     BrokerName      BrokerId  InSyncBrokerNum  Address                    HA address
pgcli-lab        pgcli-lab       0         3                10.241.20.147:10911        10.241.20.147:10912
pgcli-lab        pgcli-lab       1         3                10.241.20.148:10911        10.241.20.148:10912
`
	if !rocketMQBrokerRegistered(output, "pgcli-lab", 10911) {
		t.Error("registered broker not detected")
	}
	if rocketMQBrokerRegistered(output, "other", 10911) {
		t.Error("matched a different cluster")
	}
	if rocketMQBrokerRegistered(output, "pgcli-lab", 10912) {
		t.Error("matched the HA port instead of the broker port")
	}
	if rocketMQBrokerRegistered("#ClusterName BrokerName\n", "pgcli-lab", 10911) {
		t.Error("matched a header-only table")
	}
	if rocketMQBrokerRegistered("", "pgcli-lab", 10911) {
		t.Error("matched empty output")
	}
}

func TestRocketMQExecArgs(t *testing.T) {
	got := rocketMQExecArgs("pgcli-rocketmq-rmq-broker", false, false, "ls")
	assertArgs(t, got, []string{"exec", "pgcli-rocketmq-rmq-broker", "ls"})
	got = rocketMQExecArgs("pgcli-rocketmq-rmq-broker", true, false, "ls")
	assertArgs(t, got, []string{"exec", "-i", "pgcli-rocketmq-rmq-broker", "ls"})
	got = rocketMQExecArgs("pgcli-rocketmq-rmq-broker", true, true, "ls")
	assertArgs(t, got, []string{"exec", "-it", "pgcli-rocketmq-rmq-broker", "ls"})
}

func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q\nfull: %v", i, got[i], want[i], got)
		}
	}
}
