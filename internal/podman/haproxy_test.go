package podman

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// Conf-file mode: the user's haproxy.cfg is copied verbatim into the config
// dir (that copy is what the container mounts), never rendered. An empty file
// is refused so the -c gate is not reached with nothing to check.
func TestHAProxyWriteConfigsConfFile(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "custom.cfg")
	const body = "global\n    maxconn 10\n\ndefaults\n    mode tcp\n\nfrontend fe\n    bind :6000\n    default_backend be\n\nbackend be\n    server s1 127.0.0.1:5432\n"
	if err := os.WriteFile(user, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	m := &HAProxyManager{dataDir: dir}
	h := &config.HAProxyConfig{Name: "lb", ConfFile: user}
	path, err := m.WriteConfigs(h)
	if err != nil {
		t.Fatalf("WriteConfigs: %v", err)
	}
	if want := filepath.Join(dir, "addon", "haproxy", "lb", "haproxy.cfg"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != body {
		t.Errorf("copied config differs from the user's file:\n%s", data)
	}

	// Empty user file → refused.
	empty := filepath.Join(dir, "empty.cfg")
	if err := os.WriteFile(empty, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.WriteConfigs(&config.HAProxyConfig{Name: "e", ConfFile: empty}); err == nil {
		t.Error("empty conf file must be refused")
	}
	// Missing user file → refused.
	if _, err := m.WriteConfigs(&config.HAProxyConfig{Name: "m", ConfFile: filepath.Join(dir, "nope.cfg")}); err == nil {
		t.Error("missing conf file must be refused")
	}
	// Rendered mode is unaffected: no ConfFile and no targets still errors.
	if _, err := m.WriteConfigs(&config.HAProxyConfig{Name: "r"}); err == nil {
		t.Error("render mode without targets must still error")
	}
}

func TestRenderHAProxyCfgUnified(t *testing.T) {
	h := &config.HAProxyConfig{
		Name:      "lb",
		Listen:    "127.0.0.1",
		Mode:      "unified",
		WritePort: 5000,
		StatsPort: 5001,
		Targets: []config.HAProxyTarget{
			{Name: "node1", Host: "10.0.0.11", PGPort: 35532, RestPort: 8008},
			{Name: "node2", Host: "10.0.0.11", PGPort: 35533, RestPort: 8009},
		},
	}
	cfg, err := RenderHAProxyCfg(h)
	if err != nil {
		t.Fatalf("RenderHAProxyCfg: %v", err)
	}

	for _, want := range []string{
		"listen lb\n",
		"bind 127.0.0.1:5000",
		"option httpchk GET /",
		"http-check expect status 200",
		"on-marked-down shutdown-sessions",
		"server node1 10.0.0.11:35532 maxconn 100 check port 8008",
		"server node2 10.0.0.11:35533 maxconn 100 check port 8009",
		"listen stats",
		"bind 127.0.0.1:5001",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("unified cfg missing %q\ngot:\n%s", want, cfg)
		}
	}
	// Unified mode has no separate read listener.
	if strings.Contains(cfg, "_ro") {
		t.Errorf("unified cfg should not contain a read-only listener\ngot:\n%s", cfg)
	}
}

func TestRenderHAProxyCfgSplit(t *testing.T) {
	h := &config.HAProxyConfig{
		Name:          "lb",
		Listen:        "127.0.0.1",
		Mode:          "split",
		WritePort:     5000,
		ReadPort:      5001,
		StatsPort:     5002,
		ReplicaMaxLag: "1MB",
		Targets: []config.HAProxyTarget{
			{Name: "node1", Host: "10.0.0.11", PGPort: 35532, RestPort: 8008},
		},
	}
	cfg, err := RenderHAProxyCfg(h)
	if err != nil {
		t.Fatalf("RenderHAProxyCfg: %v", err)
	}

	for _, want := range []string{
		"listen lb_rw",
		"bind 127.0.0.1:5000",
		"option httpchk GET /",
		"listen lb_ro",
		"bind 127.0.0.1:5001",
		"option httpchk GET /replica?lag=1MB",
		"server node1 10.0.0.11:35532 maxconn 100 check port 8008",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("split cfg missing %q\ngot:\n%s", want, cfg)
		}
	}
	// The read listener must NOT force-disconnect sessions on failover — that
	// behavior belongs to the write listener only.
	roIdx := strings.Index(cfg, "listen lb_ro")
	if roIdx < 0 {
		t.Fatalf("no read listener")
	}
	if strings.Contains(cfg[roIdx:], "shutdown-sessions") {
		t.Errorf("read listener should not use on-marked-down shutdown-sessions\ngot:\n%s", cfg[roIdx:])
	}
}

func TestRenderHAProxyCfgNoTargets(t *testing.T) {
	h := &config.HAProxyConfig{Name: "lb", Mode: "unified"}
	if _, err := RenderHAProxyCfg(h); err == nil {
		t.Fatal("expected an error when there are no backend targets")
	}
}
