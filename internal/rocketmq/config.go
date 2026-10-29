// Package rocketmq renders the NameServer, broker and proxy configuration for
// a managed RocketMQ 5.x deployment.
package rocketmq

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
)

const (
	// ContainerConfigDir is where the generated files are mounted read-only.
	ContainerConfigDir = "/etc/rocketmq"
	// ContainerDataDir is the broker store root inside the container.
	ContainerDataDir = "/home/rocketmq/store"
	// ContainerControllerDir holds the embedded controller's DLedger state.
	ContainerControllerDir = "/home/rocketmq/DledgerController"
	// ContainerLogDir is the per-role log mount.
	ContainerLogDir = "/home/rocketmq/logs"

	NamesrvConfigFile = "namesrv.conf"
	BrokerConfigFile  = "broker.conf"
	ProxyConfigFile   = "rmq-proxy.json"
)

// NamesrvConfigPath returns the generated NameServer config path.
func NamesrvConfigPath(c config.RocketMQConfig) string {
	return filepath.Join(c.ConfigDir, NamesrvConfigFile)
}

// BrokerConfigPath returns the generated broker config path.
func BrokerConfigPath(c config.RocketMQConfig) string {
	return filepath.Join(c.ConfigDir, BrokerConfigFile)
}

// ProxyConfigPath returns the generated proxy JSON path.
func ProxyConfigPath(c config.RocketMQConfig) string {
	return filepath.Join(c.ConfigDir, ProxyConfigFile)
}

// BrokerDataDir returns the host directory mounted at the broker store root.
func BrokerDataDir(c config.RocketMQConfig) string {
	return filepath.Join(c.DataDir, "broker")
}

// ControllerDataDir returns the host directory mounted at the embedded
// controller's DLedger store.
func ControllerDataDir(c config.RocketMQConfig) string {
	return filepath.Join(c.DataDir, "controller")
}

// LogDir returns the host log directory for one container role.
func LogDir(c config.RocketMQConfig, role string) string {
	return filepath.Join(filepath.Dir(c.DataDir), "logs", role)
}

// RenderNamesrvConfig returns namesrv.conf. Cluster mode adds the embedded
// DLedger controller; standalone mode only declares the listener port.
func RenderNamesrvConfig(c config.RocketMQConfig) (string, error) {
	if c.NamesrvPort < 1 || c.NamesrvPort > 65535 {
		return "", fmt.Errorf("invalid nameserver port %d", c.NamesrvPort)
	}
	var b strings.Builder
	writeProperty(&b, "listenPort", strconv.Itoa(c.NamesrvPort))
	if !c.Clustered() {
		return b.String(), nil
	}
	if !config.ValidRocketMQName(c.Cluster) {
		return "", fmt.Errorf("invalid RocketMQ cluster name %q", c.Cluster)
	}
	peers, err := config.RocketMQPeers(c)
	if err != nil {
		return "", err
	}
	peerStrings := make([]string, len(peers))
	for i, peer := range peers {
		peerStrings[i] = fmt.Sprintf("%s-%s:%d", peer.ID, peer.Host, config.DefaultRocketMQControllerPort)
	}
	writeProperty(&b, "enableControllerInNamesrv", "true")
	writeProperty(&b, "controllerDLegerGroup", c.Cluster)
	writeProperty(&b, "controllerDLegerPeers", strings.Join(peerStrings, ";"))
	writeProperty(&b, "controllerDLegerSelfId", c.ControllerID)
	writeProperty(&b, "controllerStorePath", ContainerControllerDir)
	writeProperty(&b, "enableElectUncleanMaster", "false")
	writeProperty(&b, "notifyBrokerRoleChanged", "true")
	return b.String(), nil
}

// RenderBrokerConfig returns broker.conf. Cluster mode turns on controller
// election and the in-sync replication policy; standalone mode is a single
// async master with no replicas.
func RenderBrokerConfig(c config.RocketMQConfig) (string, error) {
	if err := config.ValidateRocketMQSettings(c.Settings); err != nil {
		return "", err
	}
	if c.BrokerPort < 1 || c.BrokerPort > 65535 || config.RocketMQHAPort(c) > 65535 {
		return "", fmt.Errorf("invalid broker port %d", c.BrokerPort)
	}
	clusterName := c.ClusterName()
	if clusterName == "" {
		return "", fmt.Errorf("RocketMQ instance name is empty")
	}
	var b strings.Builder
	writeProperty(&b, "brokerClusterName", clusterName)
	writeProperty(&b, "brokerName", clusterName)
	if c.Clustered() {
		namesrv, err := config.RocketMQNamesrvAddrs(c)
		if err != nil {
			return "", err
		}
		controller, err := config.RocketMQControllerAddrs(c)
		if err != nil {
			return "", err
		}
		writeProperty(&b, "brokerId", "-1")
		writeProperty(&b, "brokerRole", "SLAVE")
		writeProperty(&b, "enableControllerMode", "true")
		writeProperty(&b, "controllerAddr", strings.Join(controller, ";"))
		writeProperty(&b, "namesrvAddr", strings.Join(namesrv, ";"))
		writeProperty(&b, "brokerIP1", c.AdvertiseHost)
		writeProperty(&b, "brokerIP2", c.AdvertiseHost)
		writeProperty(&b, "listenPort", strconv.Itoa(c.BrokerPort))
		writeProperty(&b, "haListenPort", strconv.Itoa(config.RocketMQHAPort(c)))
		writeProperty(&b, "storePathRootDir", ContainerDataDir)
		writeProperty(&b, "storePathCommitLog", ContainerDataDir+"/commitlog")
		writeProperty(&b, "allAckInSyncStateSet", "true")
		writeProperty(&b, "minInSyncReplicas", strconv.Itoa(config.RocketMQMinInSyncReplicas(c)))
	} else {
		if c.AdvertiseHost == "" {
			return "", fmt.Errorf("broker advertise host is empty")
		}
		writeProperty(&b, "brokerId", "0")
		writeProperty(&b, "brokerRole", "ASYNC_MASTER")
		writeProperty(&b, "enableControllerMode", "false")
		writeProperty(&b, "namesrvAddr", fmt.Sprintf("127.0.0.1:%d", c.NamesrvPort))
		writeProperty(&b, "brokerIP1", c.AdvertiseHost)
		writeProperty(&b, "brokerIP2", c.AdvertiseHost)
		writeProperty(&b, "listenPort", strconv.Itoa(c.BrokerPort))
		writeProperty(&b, "haListenPort", strconv.Itoa(config.RocketMQHAPort(c)))
		writeProperty(&b, "storePathRootDir", ContainerDataDir)
		writeProperty(&b, "storePathCommitLog", ContainerDataDir+"/commitlog")
	}
	writeProperty(&b, "fileReservedTime", strconv.Itoa(c.FileReservedTime))
	writeProperty(&b, "deleteWhen", c.DeleteWhen)
	writeProperty(&b, "diskMaxUsedSpaceRatio", strconv.Itoa(c.DiskMaxUsedSpaceRatio))
	writeProperty(&b, "autoCreateTopicEnable", "true")
	if len(c.Settings) > 0 {
		b.WriteString("\n")
		keys := make([]string, 0, len(c.Settings))
		for key := range c.Settings {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			writeProperty(&b, key, c.Settings[key])
		}
	}
	return b.String(), nil
}

// ProxyConfig is the JSON document mqproxy reads with -pc.
type ProxyConfig struct {
	RocketMQClusterName string `json:"rocketMQClusterName"`
	ProxyClusterName    string `json:"proxyClusterName"`
	NamesrvAddr         string `json:"namesrvAddr"`
	ProxyMode           string `json:"proxyMode"`
	GRPCServerPort      int    `json:"grpcServerPort"`
	RemotingListenPort  int    `json:"remotingListenPort"`
	RemotingAccessAddr  string `json:"remotingAccessAddr"`
	LocalServeAddr      string `json:"localServeAddr"`
}

// RenderProxyConfig returns rmq-proxy.json. remotingAccessAddr and
// localServeAddr carry a bare host: RocketMQ appends the remoting port itself.
func RenderProxyConfig(c config.RocketMQConfig) (string, error) {
	if c.ProxyPort < 1 || c.ProxyPort > 65535 || c.GRPCPort < 1 || c.GRPCPort > 65535 {
		return "", fmt.Errorf("invalid proxy ports %d/%d", c.ProxyPort, c.GRPCPort)
	}
	if c.AdvertiseHost == "" {
		return "", fmt.Errorf("proxy advertise host is empty")
	}
	namesrv, err := config.RocketMQNamesrvAddrs(c)
	if err != nil {
		return "", err
	}
	doc := ProxyConfig{
		RocketMQClusterName: c.ClusterName(),
		ProxyClusterName:    c.ClusterName() + "-proxy",
		NamesrvAddr:         strings.Join(namesrv, ";"),
		ProxyMode:           "CLUSTER",
		GRPCServerPort:      c.GRPCPort,
		RemotingListenPort:  c.ProxyPort,
		RemotingAccessAddr:  c.AdvertiseHost,
		LocalServeAddr:      c.AdvertiseHost,
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encoding proxy config: %w", err)
	}
	return string(data) + "\n", nil
}

// WriteConfigs renders and atomically writes namesrv.conf, broker.conf and
// rmq-proxy.json into the instance config dir.
func WriteConfigs(c config.RocketMQConfig) error {
	if c.ConfigDir == "" {
		return fmt.Errorf("RocketMQ config dir is empty")
	}
	if err := os.MkdirAll(c.ConfigDir, 0o755); err != nil {
		return fmt.Errorf("creating config dir %s: %w", c.ConfigDir, err)
	}
	namesrv, err := RenderNamesrvConfig(c)
	if err != nil {
		return fmt.Errorf("rendering namesrv config: %w", err)
	}
	broker, err := RenderBrokerConfig(c)
	if err != nil {
		return fmt.Errorf("rendering broker config: %w", err)
	}
	proxy, err := RenderProxyConfig(c)
	if err != nil {
		return fmt.Errorf("rendering proxy config: %w", err)
	}
	for path, content := range map[string]string{
		NamesrvConfigPath(c): namesrv,
		BrokerConfigPath(c):  broker,
		ProxyConfigPath(c):   proxy,
	} {
		if err := writeFileAtomic(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func writeProperty(b *strings.Builder, key, value string) {
	b.WriteString(key)
	b.WriteByte('=')
	b.WriteString(value)
	b.WriteByte('\n')
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("setting mode on %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("installing %s: %w", path, err)
	}
	return nil
}
