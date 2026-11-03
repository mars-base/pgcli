// Package clickhousekeeper renders the managed ClickHouse Keeper
// configuration and probes Keeper members over the ZooKeeper 4lw protocol.
package clickhousekeeper

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
)

// Container paths are fixed by the official clickhouse-keeper image. The
// config directory is mounted read-only; data and logs are instance bind
// mounts that the image entrypoint chowns for the clickhouse user.
const (
	ContainerConfigDir  = "/etc/pgcli-clickhouse-keeper"
	ContainerConfigPath = ContainerConfigDir + "/keeper_config.xml"
	ContainerDataDir    = "/var/lib/clickhouse-keeper"
	ContainerLogDir     = "/var/log/clickhouse-keeper"
)

type keeperConfigXML struct {
	XMLName      xml.Name        `xml:"clickhouse"`
	Logger       loggerXML       `xml:"logger"`
	ListenHost   string          `xml:"listen_host"`
	KeeperServer keeperServerXML `xml:"keeper_server"`
}

type loggerXML struct {
	Level    string `xml:"level"`
	Log      string `xml:"log"`
	ErrorLog string `xml:"errorlog"`
	Size     string `xml:"size"`
	Count    int    `xml:"count"`
	Console  int    `xml:"console"`
}

type keeperServerXML struct {
	TCPPort             int                     `xml:"tcp_port"`
	ServerID            int                     `xml:"server_id"`
	LogStoragePath      string                  `xml:"log_storage_path"`
	SnapshotStoragePath string                  `xml:"snapshot_storage_path"`
	Coordination        coordinationSettingsXML `xml:"coordination_settings"`
	Raft                raftConfigurationXML    `xml:"raft_configuration"`
}

type coordinationSettingsXML struct {
	OperationTimeoutMS int    `xml:"operation_timeout_ms"`
	SessionTimeoutMS   int    `xml:"session_timeout_ms"`
	RaftLogsLevel      string `xml:"raft_logs_level"`
}

type raftConfigurationXML struct {
	Servers []raftServerXML `xml:"server"`
}

type raftServerXML struct {
	ID       int    `xml:"id"`
	Hostname string `xml:"hostname"`
	Port     int    `xml:"port"`
}

// RenderConfig returns the managed keeper_config.xml for one Keeper member.
func RenderConfig(kc config.ClickHouseKeeperConfig) (string, error) {
	if kc.ServerID < 1 {
		return "", fmt.Errorf("server_id must be >= 1, got %d", kc.ServerID)
	}
	if kc.ClientPort < 1 || kc.ClientPort > 65535 {
		return "", fmt.Errorf("client_port must be between 1 and 65535, got %d", kc.ClientPort)
	}
	voters, err := config.ClickHouseKeeperVoters(kc)
	if err != nil {
		return "", err
	}
	listen := strings.TrimSpace(kc.Listen)
	if listen == "" {
		listen = "127.0.0.1"
	}

	servers := make([]raftServerXML, 0, len(voters))
	for _, voter := range voters {
		servers = append(servers, raftServerXML{ID: voter.ID, Hostname: voter.Host, Port: voter.Port})
	}

	doc := keeperConfigXML{
		Logger: loggerXML{
			Level:    "information",
			Log:      ContainerLogDir + "/clickhouse-keeper.log",
			ErrorLog: ContainerLogDir + "/clickhouse-keeper.err.log",
			Size:     "100M",
			Count:    3,
			Console:  1,
		},
		ListenHost: listen,
		KeeperServer: keeperServerXML{
			TCPPort:             kc.ClientPort,
			ServerID:            kc.ServerID,
			LogStoragePath:      ContainerDataDir + "/coordination/log",
			SnapshotStoragePath: ContainerDataDir + "/coordination/snapshots",
			Coordination: coordinationSettingsXML{
				OperationTimeoutMS: 10000,
				SessionTimeoutMS:   30000,
				RaftLogsLevel:      "information",
			},
			Raft: raftConfigurationXML{Servers: servers},
		},
	}

	body, err := xml.MarshalIndent(doc, "", "    ")
	if err != nil {
		return "", fmt.Errorf("rendering keeper config: %w", err)
	}
	return "<?xml version=\"1.0\"?>\n" + string(body) + "\n", nil
}

// WriteConfig atomically writes keeper_config.xml with mode 0644 (it contains
// no credentials).
func WriteConfig(kc config.ClickHouseKeeperConfig) error {
	content, err := RenderConfig(kc)
	if err != nil {
		return err
	}
	dir := filepath.Dir(kc.ConfigPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating config dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".keeper_config.xml-*")
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
	if err := os.Rename(name, kc.ConfigPath); err != nil {
		return fmt.Errorf("installing %s: %w", kc.ConfigPath, err)
	}
	return nil
}

// LocalProbeHost maps a wildcard bind address to loopback so the local 4lw
// probe can connect to it.
func LocalProbeHost(listen string) string {
	switch strings.TrimSpace(listen) {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	default:
		return strings.TrimSpace(listen)
	}
}
