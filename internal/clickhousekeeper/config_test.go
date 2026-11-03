package clickhousekeeper

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func keeperTestConfig(t *testing.T) config.ClickHouseKeeperConfig {
	t.Helper()
	dir := t.TempDir()
	return config.ClickHouseKeeperConfig{
		Name:          "keeper",
		ConfigPath:    filepath.Join(dir, "config", "keeper_config.xml"),
		DataDir:       filepath.Join(dir, "data"),
		LogDir:        filepath.Join(dir, "logs"),
		Listen:        "0.0.0.0",
		ServerID:      1,
		AdvertiseHost: "10.241.20.147",
		Peers:         []string{"2@10.241.20.148", "3@10.241.20.153"},
		ClientPort:    9181,
		RaftPort:      9234,
	}
}

func TestRenderConfig(t *testing.T) {
	kc := keeperTestConfig(t)
	out, err := RenderConfig(kc)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	for _, want := range []string{
		"<?xml version=\"1.0\"?>",
		"<clickhouse>",
		"<logger>",
		"<log>/var/log/clickhouse-keeper/clickhouse-keeper.log</log>",
		"<errorlog>/var/log/clickhouse-keeper/clickhouse-keeper.err.log</errorlog>",
		"<listen_host>0.0.0.0</listen_host>",
		"<keeper_server>",
		"<tcp_port>9181</tcp_port>",
		"<server_id>1</server_id>",
		"<log_storage_path>/var/lib/clickhouse-keeper/coordination/log</log_storage_path>",
		"<snapshot_storage_path>/var/lib/clickhouse-keeper/coordination/snapshots</snapshot_storage_path>",
		"<coordination_settings>",
		"<raft_configuration>",
		"<hostname>10.241.20.148</hostname>",
		"<port>9234</port>",
		"</keeper_server>",
		"</clickhouse>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("keeper config missing %q:\n%s", want, out)
		}
	}
	if got := strings.Count(out, "\n            <server>\n"); got != 3 {
		t.Errorf("raft servers = %d, want 3:\n%s", got, out)
	}
	if got := strings.Count(out, "<id>"); got != 3 {
		t.Errorf("raft server ids = %d, want 3", got)
	}

	var parsed struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("rendered keeper config is not well-formed XML: %v", err)
	}
	if parsed.XMLName.Local != "clickhouse" {
		t.Errorf("root element = %q, want clickhouse", parsed.XMLName.Local)
	}
}

func TestRenderConfigStandalone(t *testing.T) {
	kc := keeperTestConfig(t)
	kc.Peers = nil
	kc.AdvertiseHost = "127.0.0.1"
	kc.Listen = "127.0.0.1"
	out, err := RenderConfig(kc)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if got := strings.Count(out, "<server>"); got != 1 {
		t.Errorf("standalone raft servers = %d, want 1", got)
	}
	if !strings.Contains(out, "<hostname>127.0.0.1</hostname>") {
		t.Errorf("standalone advertise host missing:\n%s", out)
	}
}

func TestRenderConfigRejectsInvalidPeer(t *testing.T) {
	kc := keeperTestConfig(t)
	kc.Peers = []string{"2@host"}
	if _, err := RenderConfig(kc); err == nil || !strings.Contains(err.Error(), "IP address") {
		t.Fatalf("error = %v, want IP address error", err)
	}
}

func TestWriteConfig(t *testing.T) {
	kc := keeperTestConfig(t)
	if err := WriteConfig(kc); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	info, err := os.Stat(kc.ConfigPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %o, want 644", info.Mode().Perm())
	}
	body, err := os.ReadFile(kc.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "<server_id>1</server_id>") {
		t.Errorf("config content missing server id:\n%s", body)
	}
}

func TestLocalProbeHost(t *testing.T) {
	tests := map[string]string{
		"":          "127.0.0.1",
		"0.0.0.0":   "127.0.0.1",
		"::":        "127.0.0.1",
		"[::]":      "127.0.0.1",
		"127.0.0.1": "127.0.0.1",
		"10.0.0.5":  "10.0.0.5",
	}
	for listen, want := range tests {
		if got := LocalProbeHost(listen); got != want {
			t.Errorf("LocalProbeHost(%q) = %q, want %q", listen, got, want)
		}
	}
}
