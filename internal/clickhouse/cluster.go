package clickhouse

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mars-base/pgcli/internal/config"
)

// ClusterConfigFileName is the managed cluster override. It lives next to the
// base config in the mounted config.d directory and is only present for
// clustered instances.
const ClusterConfigFileName = "zz-pgcli-cluster.xml"

// AdminPasswordEnvVar is exported by the container entrypoint wrapper from the
// mounted secret. remote_servers references it with `from_env`, so Distributed
// queries authenticate as the admin user without writing the password into the
// 0644 cluster config.
const AdminPasswordEnvVar = "PGCLI_CLICKHOUSE_ADMIN_PASSWORD"

// ClusterConfigPath returns the host path of the cluster override for cc.
func ClusterConfigPath(cc config.ClickHouseConfig) string {
	return filepath.Join(filepath.Dir(cc.ConfigPath), ClusterConfigFileName)
}

type clusterConfigXML struct {
	XMLName             xml.Name          `xml:"clickhouse"`
	RemoteServers       remoteServersXML  `xml:"remote_servers"`
	Macros              macrosXML         `xml:"macros"`
	ZooKeeper           zooKeeperXML      `xml:"zookeeper"`
	DistributedDDL      distributedDDLXML `xml:"distributed_ddl"`
	InterserverHTTPHost string            `xml:"interserver_http_host"`
	InterserverHTTPPort int               `xml:"interserver_http_port"`
	DefaultReplicaPath  string            `xml:"default_replica_path"`
	DefaultReplicaName  string            `xml:"default_replica_name"`
}

type remoteServersXML struct {
	Cluster clusterEntryXML
}

// clusterEntryXML takes the user-chosen cluster name from XMLName so the
// <remote_servers> child cannot be expressed with a static struct tag.
type clusterEntryXML struct {
	XMLName xml.Name
	Shards  []shardXML `xml:"shard"`
}

type shardXML struct {
	InternalReplication bool         `xml:"internal_replication"`
	Replicas            []replicaXML `xml:"replica"`
}

type replicaXML struct {
	Host     string      `xml:"host"`
	Port     int         `xml:"port"`
	User     string      `xml:"user"`
	Password passwordXML `xml:"password"`
}

type passwordXML struct {
	FromEnv string `xml:"from_env,attr"`
}

type macrosXML struct {
	Cluster string `xml:"cluster"`
	Shard   int    `xml:"shard"`
	Replica int    `xml:"replica"`
}

type zooKeeperXML struct {
	Nodes              []keeperNodeXML `xml:"node"`
	SessionTimeoutMS   int             `xml:"session_timeout_ms"`
	OperationTimeoutMS int             `xml:"operation_timeout_ms"`
}

type keeperNodeXML struct {
	Host string `xml:"host"`
	Port int    `xml:"port"`
}

type distributedDDLXML struct {
	Path string `xml:"path"`
}

// RenderClusterConfig returns the managed remote_servers/macros/zookeeper
// override for one clustered instance. The cluster name is rendered as an
// XML element name, so it is validated again here.
func RenderClusterConfig(cc config.ClickHouseConfig) (string, error) {
	if !cc.Clustered() {
		return "", fmt.Errorf("cluster config requires cluster mode")
	}
	if !config.ValidClickHouseClusterName(cc.Cluster) {
		return "", fmt.Errorf("invalid cluster name %q", cc.Cluster)
	}
	if cc.InterserverPort < 1 || cc.InterserverPort > 65535 {
		return "", fmt.Errorf("interserver_port must be between 1 and 65535, got %d", cc.InterserverPort)
	}
	topology, err := config.ClickHouseTopology(cc)
	if err != nil {
		return "", err
	}
	keepers, err := config.ClickHouseKeeperEndpoints(cc)
	if err != nil {
		return "", err
	}

	var shards []shardXML
	for _, peer := range topology {
		if len(shards) == 0 || peer.Shard != len(shards) {
			shards = append(shards, shardXML{InternalReplication: true})
		}
		shards[len(shards)-1].Replicas = append(shards[len(shards)-1].Replicas, replicaXML{
			Host: peer.Host,
			Port: peer.Port,
			User: cc.AdminUser,
			// Distributed connections default to the `default` user, which
			// the image's entrypoint removes when a dedicated admin user is
			// configured.
			Password: passwordXML{FromEnv: AdminPasswordEnvVar},
		})
	}

	nodes := make([]keeperNodeXML, 0, len(keepers))
	for _, endpoint := range keepers {
		nodes = append(nodes, keeperNodeXML{Host: endpoint.Host, Port: endpoint.Port})
	}

	doc := clusterConfigXML{
		RemoteServers: remoteServersXML{Cluster: clusterEntryXML{
			XMLName: xml.Name{Local: cc.Cluster},
			Shards:  shards,
		}},
		Macros: macrosXML{
			Cluster: cc.Cluster,
			Shard:   cc.Shard,
			Replica: cc.Replica,
		},
		ZooKeeper: zooKeeperXML{
			Nodes:              nodes,
			SessionTimeoutMS:   30000,
			OperationTimeoutMS: 10000,
		},
		DistributedDDL: distributedDDLXML{
			Path: "/clickhouse/" + cc.Cluster + "/task_queue/ddl",
		},
		InterserverHTTPHost: cc.AdvertiseHost,
		InterserverHTTPPort: cc.InterserverPort,
		DefaultReplicaPath:  "/clickhouse/" + cc.Cluster + "/tables/{uuid}/{shard}",
		DefaultReplicaName:  "{replica}",
	}

	body, err := xml.MarshalIndent(doc, "", "    ")
	if err != nil {
		return "", fmt.Errorf("rendering cluster config: %w", err)
	}
	return "<?xml version=\"1.0\"?>\n" + string(body) + "\n", nil
}

// WriteClusterConfig atomically writes the cluster override with mode 0644. It
// contains only the environment variable name for the password, never the
// password itself.
func WriteClusterConfig(cc config.ClickHouseConfig) error {
	content, err := RenderClusterConfig(cc)
	if err != nil {
		return err
	}
	return writeFileAtomic(ClusterConfigPath(cc), content)
}

// RemoveClusterConfig deletes a stale cluster override. It is not an error if
// the file does not exist.
func RemoveClusterConfig(cc config.ClickHouseConfig) error {
	err := os.Remove(ClusterConfigPath(cc))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing %s: %w", ClusterConfigPath(cc), err)
	}
	return nil
}

// WriteGeneratedConfig writes the base config and either the cluster override
// (clustered) or removes any stale override left by an earlier clustered
// install (standalone).
func WriteGeneratedConfig(cc config.ClickHouseConfig) error {
	if err := WriteConfig(cc); err != nil {
		return err
	}
	if !cc.Clustered() {
		return RemoveClusterConfig(cc)
	}
	return WriteClusterConfig(cc)
}

func writeFileAtomic(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating config dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".zz-pgcli-cluster.xml-*")
	if err != nil {
		return fmt.Errorf("creating temp config: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("writing config: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("setting config mode: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing config: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("installing %s: %w", path, err)
	}
	return nil
}
