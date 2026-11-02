package podman

import (
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func TestDocumentDBListenAddr(t *testing.T) {
	tests := []struct {
		listen string
		bridge bool
		want   string
	}{
		{"", false, ":10260"},
		{"0.0.0.0", false, ":10260"},
		{"127.0.0.1", false, "127.0.0.1:10260"},
		{"::1", false, "[::1]:10260"},
		{"", true, ":10260"},
		{"127.0.0.1", true, ":10260"},
	}
	for _, tt := range tests {
		if got := documentdbListenAddr(tt.listen, 10260, tt.bridge); got != tt.want {
			t.Errorf("documentdbListenAddr(%q, bridge=%v) = %q, want %q", tt.listen, tt.bridge, got, tt.want)
		}
	}
}

func TestDocumentDBGatewayArgs(t *testing.T) {
	dc := &config.DocumentDBConfig{
		ContainerName: "pgcli-documentdb-ddb",
		Name:          "ddb",
		ImageTag:      "localhost/pgcli-documentdb-gateway:1.0.0",
		HostPort:      10260,
		Listen:        "127.0.0.1",
		TLSMode:       "allow",
	}

	args := documentdbGatewayArgs(dc, "pgcli-pg-sock-pgcli-pg-demo", "/h/pg-url", "/h/SetupConfiguration.json", "pgcli-documentdb-tls-ddb", false, "pgcli-net")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--name pgcli-documentdb-ddb",
		"--network host",
		"--restart unless-stopped",
		"--http-proxy=false",
		"-v pgcli-pg-sock-pgcli-pg-demo:/var/run/postgresql:ro,z",
		"-v /h/pg-url:/etc/documentdb/gateway/pg-url:ro,U,z",
		"-v /h/SetupConfiguration.json:/etc/documentdb/gateway/SetupConfiguration.json:ro,U,z",
		"-v pgcli-documentdb-tls-ddb:/var/lib/documentdb-gateway:U,z",
		"-e DOCUMENTDB_PG_URL_FILE=/etc/documentdb/gateway/pg-url",
		"-e DOCUMENTDB_LISTEN_ADDR=127.0.0.1:10260",
		"-e DOCUMENTDB_TLS_AUTO_GENERATE=true",
		"-e DOCUMENTDB_TLS_STATE_DIR=/var/lib/documentdb-gateway/tls",
		"-e DOCUMENTDB_LOG_LEVEL=info",
		"localhost/pgcli-documentdb-gateway:1.0.0 run",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q:\n%s", want, joined)
		}
	}
	if slices.Contains(args, "-p") {
		t.Errorf("Linux host networking must not publish ports:\n%s", joined)
	}

	bridgeArgs := documentdbGatewayArgs(dc, "sock", "/h/u", "/h/c", "tls", true, "pgcli-net")
	bridgeJoined := strings.Join(bridgeArgs, " ")
	for _, want := range []string{
		"--network pgcli-net",
		"-p 10260:10260",
		"-e DOCUMENTDB_LISTEN_ADDR=:10260",
	} {
		if !strings.Contains(bridgeJoined, want) {
			t.Errorf("bridge argv missing %q:\n%s", want, bridgeJoined)
		}
	}
	if strings.Contains(bridgeJoined, "--network host") {
		t.Errorf("bridge mode must not use host networking:\n%s", bridgeJoined)
	}
}

func TestDocumentDBPgURL(t *testing.T) {
	got := documentdbPgURL(35432, "postgres")
	want := "postgresql://docdb_gateway@/postgres?host=/var/run/postgresql&port=35432"
	if got != want {
		t.Errorf("documentdbPgURL = %q, want %q", got, want)
	}
	if strings.Contains(got, "docdb_gateway:") {
		t.Errorf("URL must not carry a password: %q", got)
	}
}

func TestRenderDocumentDBSetupConfiguration(t *testing.T) {
	allow, err := renderDocumentDBSetupConfiguration("allow")
	if err != nil {
		t.Fatalf("allow: %v", err)
	}
	if !strings.Contains(allow, `"EnforceTls": false`) {
		t.Errorf("allow config must set EnforceTls=false:\n%s", allow)
	}
	require, err := renderDocumentDBSetupConfiguration("require")
	if err != nil {
		t.Fatalf("require: %v", err)
	}
	if !strings.Contains(require, `"EnforceTls": true`) {
		t.Errorf("require config must set EnforceTls=true:\n%s", require)
	}
	for _, doc := range []string{allow, require} {
		if strings.Contains(doc, `"Enabled": true`) {
			t.Errorf("telemetry must stay disabled:\n%s", doc)
		}
		if !strings.Contains(doc, "BlockedRolePrefixes") {
			t.Errorf("blocked role prefixes missing:\n%s", doc)
		}
	}
}
