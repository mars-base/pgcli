package rocketmq

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func standalone() config.RocketMQConfig {
	return config.RocketMQConfig{
		ContainerName: "pgcli-rocketmq-rmq",
		Name:          "rmq",
		Version:       config.DefaultRocketMQVersion,
		ImageTag:      config.DefaultRocketMQImageTag,
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

func clustered() config.RocketMQConfig {
	c := standalone()
	c.Name = "rmq"
	c.Cluster = "lab"
	c.AdvertiseHost = "10.0.0.1"
	c.ControllerID = "n0"
	c.Peers = []string{"n0@10.0.0.1", "n1@10.0.0.2", "n2@10.0.0.3"}
	c.ControllerPort = 9877
	return c
}

func TestRenderNamesrvConfigStandalone(t *testing.T) {
	got, err := RenderNamesrvConfig(standalone())
	if err != nil {
		t.Fatalf("RenderNamesrvConfig: %v", err)
	}
	if want := "listenPort=9876\n"; got != want {
		t.Errorf("namesrv config = %q, want %q", got, want)
	}
}

func TestRenderNamesrvConfigCluster(t *testing.T) {
	got, err := RenderNamesrvConfig(clustered())
	if err != nil {
		t.Fatalf("RenderNamesrvConfig: %v", err)
	}
	want := "listenPort=9876\n" +
		"enableControllerInNamesrv=true\n" +
		"controllerDLegerGroup=lab\n" +
		"controllerDLegerPeers=n0-10.0.0.1:9877;n1-10.0.0.2:9877;n2-10.0.0.3:9877\n" +
		"controllerDLegerSelfId=n0\n" +
		"controllerStorePath=/home/rocketmq/DledgerController\n" +
		"enableElectUncleanMaster=false\n" +
		"notifyBrokerRoleChanged=true\n"
	if got != want {
		t.Errorf("namesrv config =\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderNamesrvConfigClusterSortsPeers(t *testing.T) {
	c := clustered()
	c.Peers = []string{"n2@10.0.0.3", "n1@10.0.0.2"}
	got, err := RenderNamesrvConfig(c)
	if err != nil {
		t.Fatalf("RenderNamesrvConfig: %v", err)
	}
	if !strings.Contains(got, "controllerDLegerPeers=n0-10.0.0.1:9877;n1-10.0.0.2:9877;n2-10.0.0.3:9877\n") {
		t.Errorf("peers not folded in and sorted:\n%s", got)
	}
}

func TestRenderNamesrvConfigClusterRequiresPeer(t *testing.T) {
	c := clustered()
	c.Peers = nil
	if _, err := RenderNamesrvConfig(c); err == nil || !strings.Contains(err.Error(), "at least one remote peer") {
		t.Fatalf("RenderNamesrvConfig() = %v, want peer error", err)
	}
}

func TestRenderBrokerConfigStandalone(t *testing.T) {
	got, err := RenderBrokerConfig(standalone())
	if err != nil {
		t.Fatalf("RenderBrokerConfig: %v", err)
	}
	want := "brokerClusterName=rmq\n" +
		"brokerName=rmq\n" +
		"brokerId=0\n" +
		"brokerRole=ASYNC_MASTER\n" +
		"enableControllerMode=false\n" +
		"namesrvAddr=127.0.0.1:9876\n" +
		"brokerIP1=127.0.0.1\n" +
		"brokerIP2=127.0.0.1\n" +
		"listenPort=10911\n" +
		"haListenPort=10912\n" +
		"storePathRootDir=/home/rocketmq/store\n" +
		"storePathCommitLog=/home/rocketmq/store/commitlog\n" +
		"autoCreateTopicEnable=true\n"
	if got != want {
		t.Errorf("broker config =\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderBrokerConfigCluster(t *testing.T) {
	got, err := RenderBrokerConfig(clustered())
	if err != nil {
		t.Fatalf("RenderBrokerConfig: %v", err)
	}
	want := "brokerClusterName=lab\n" +
		"brokerName=lab\n" +
		"brokerId=-1\n" +
		"brokerRole=SLAVE\n" +
		"enableControllerMode=true\n" +
		"controllerAddr=10.0.0.1:9877;10.0.0.2:9877;10.0.0.3:9877\n" +
		"namesrvAddr=10.0.0.1:9876;10.0.0.2:9876;10.0.0.3:9876\n" +
		"brokerIP1=10.0.0.1\n" +
		"brokerIP2=10.0.0.1\n" +
		"listenPort=10911\n" +
		"haListenPort=10912\n" +
		"storePathRootDir=/home/rocketmq/store\n" +
		"storePathCommitLog=/home/rocketmq/store/commitlog\n" +
		"allAckInSyncStateSet=true\n" +
		"minInSyncReplicas=2\n" +
		"autoCreateTopicEnable=true\n"
	if got != want {
		t.Errorf("broker config =\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderBrokerConfigTwoMemberCluster(t *testing.T) {
	c := clustered()
	c.Peers = []string{"n0@10.0.0.1", "n1@10.0.0.2"}
	got, err := RenderBrokerConfig(c)
	if err != nil {
		t.Fatalf("RenderBrokerConfig: %v", err)
	}
	if !strings.Contains(got, "minInSyncReplicas=1\n") {
		t.Errorf("two-member cluster should use minInSyncReplicas=1:\n%s", got)
	}
}

func TestRenderBrokerConfigSettings(t *testing.T) {
	c := standalone()
	c.Settings = map[string]string{"flushDiskType": "SYNC_FLUSH", "brokerPermission": "4"}
	got, err := RenderBrokerConfig(c)
	if err != nil {
		t.Fatalf("RenderBrokerConfig: %v", err)
	}
	if !strings.Contains(got, "\nbrokerPermission=4\nflushDiskType=SYNC_FLUSH\n") {
		t.Errorf("settings not appended in sorted order:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("broker config does not end with newline")
	}
}

func TestRenderProxyConfig(t *testing.T) {
	got, err := RenderProxyConfig(clustered())
	if err != nil {
		t.Fatalf("RenderProxyConfig: %v", err)
	}
	var doc ProxyConfig
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("proxy config is not valid JSON: %v\n%s", err, got)
	}
	if doc.RocketMQClusterName != "lab" || doc.ProxyClusterName != "lab-proxy" {
		t.Errorf("cluster names = %q/%q", doc.RocketMQClusterName, doc.ProxyClusterName)
	}
	if doc.NamesrvAddr != "10.0.0.1:9876;10.0.0.2:9876;10.0.0.3:9876" {
		t.Errorf("namesrvAddr = %q", doc.NamesrvAddr)
	}
	if doc.ProxyMode != "CLUSTER" {
		t.Errorf("proxyMode = %q", doc.ProxyMode)
	}
	if doc.GRPCServerPort != 8081 || doc.RemotingListenPort != 8080 {
		t.Errorf("ports = %d/%d", doc.GRPCServerPort, doc.RemotingListenPort)
	}
	// Regression: RocketMQ appends the remoting port itself; a host:port here
	// breaks HostAndPort.fromParts.
	if doc.RemotingAccessAddr != "10.0.0.1" {
		t.Errorf("remotingAccessAddr = %q, want bare host", doc.RemotingAccessAddr)
	}
	if doc.LocalServeAddr != "10.0.0.1" {
		t.Errorf("localServeAddr = %q, want bare host", doc.LocalServeAddr)
	}
	if strings.Contains(doc.RemotingAccessAddr, ":") || strings.Contains(doc.LocalServeAddr, ":") {
		t.Errorf("proxy access addresses must not contain a port: %+v", doc)
	}
}

func TestRenderProxyConfigStandalone(t *testing.T) {
	got, err := RenderProxyConfig(standalone())
	if err != nil {
		t.Fatalf("RenderProxyConfig: %v", err)
	}
	var doc ProxyConfig
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("proxy config is not valid JSON: %v", err)
	}
	if doc.RocketMQClusterName != "rmq" || doc.ProxyClusterName != "rmq-proxy" {
		t.Errorf("cluster names = %q/%q", doc.RocketMQClusterName, doc.ProxyClusterName)
	}
	if doc.NamesrvAddr != "127.0.0.1:9876" {
		t.Errorf("namesrvAddr = %q", doc.NamesrvAddr)
	}
}

func TestWriteConfigs(t *testing.T) {
	dir := t.TempDir()
	c := clustered()
	c.ConfigDir = filepath.Join(dir, "config")
	if err := WriteConfigs(c); err != nil {
		t.Fatalf("WriteConfigs: %v", err)
	}
	for _, name := range []string{NamesrvConfigFile, BrokerConfigFile, ProxyConfigFile} {
		path := filepath.Join(c.ConfigDir, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Errorf("%s mode = %o, want 644", name, info.Mode().Perm())
		}
	}
	namesrv, err := os.ReadFile(NamesrvConfigPath(c))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(namesrv), "controllerDLegerSelfId=n0\n") {
		t.Errorf("namesrv config missing self id:\n%s", namesrv)
	}
}

func TestRocketMQPaths(t *testing.T) {
	c := clustered()
	if got := BrokerDataDir(c); got != "/srv/pg/addon/rocketmq/rmq/data/broker" {
		t.Errorf("BrokerDataDir = %q", got)
	}
	if got := ControllerDataDir(c); got != "/srv/pg/addon/rocketmq/rmq/data/controller" {
		t.Errorf("ControllerDataDir = %q", got)
	}
	if got := LogDir(c, "broker"); got != "/srv/pg/addon/rocketmq/rmq/logs/broker" {
		t.Errorf("LogDir = %q", got)
	}
}
