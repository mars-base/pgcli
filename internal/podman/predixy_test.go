package podman

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func TestRenderPredixyCfg(t *testing.T) {
	p := &config.PredixyConfig{
		ContainerName: "pgcli-predixy-proxy",
		Name:          "proxy",
		Listen:        "0.0.0.0",
		Port:          7617,
		Workers:       4,
		Password:      "s3cretPW",
		Backend:       []string{"127.0.0.1:6379", "127.0.0.1:6380", "10.0.0.12:6379"},
	}
	cfg, err := RenderPredixyCfg(p)
	if err != nil {
		t.Fatalf("RenderPredixyCfg: %v", err)
	}

	for _, want := range []string{
		"Name pgcli-predixy-proxy\n",
		"Bind 0.0.0.0:7617\n",
		"WorkerThreads 4\n",
		"Include license.conf\n",
		"Authority {\n",
		"    Auth \"s3cretPW\" {\n",
		"        Mode admin\n",
		"ClusterServerPool {\n",
		"    Password \"s3cretPW\"\n",
		"    Servers {\n",
		"        + 127.0.0.1:6379\n",
		"        + 127.0.0.1:6380\n",
		"        + 10.0.0.12:6379\n",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("cfg missing %q\ngot:\n%s", want, cfg)
		}
	}

	// Braces must sit on the block-name line: Predixy 7.0.1 rejects a lone
	// opening brace with "unmatched end scope".
	for i, line := range strings.Split(cfg, "\n") {
		if strings.TrimSpace(line) == "{" {
			t.Errorf("line %d is a lone opening brace: %q", i+1, line)
		}
	}

	// One password, two placements: the client-facing Authority and the
	// backend-facing ClusterServerPool.
	if got := strings.Count(cfg, `"s3cretPW"`); got != 2 {
		t.Errorf("password should appear exactly twice, got %d\n%s", got, cfg)
	}
}

// TestRenderPredixyCfgDefaults pins the values the renderer supplies when the
// config leaves them empty. ApplyDefaults normally fills these, but a
// hand-written pg.yaml must still render a valid file.
func TestRenderPredixyCfgDefaults(t *testing.T) {
	p := &config.PredixyConfig{
		Name:     "proxy",
		Port:     7617,
		Password: "pw",
		Backend:  []string{"127.0.0.1:6379"},
	}
	cfg, err := RenderPredixyCfg(p)
	if err != nil {
		t.Fatalf("RenderPredixyCfg: %v", err)
	}
	if !strings.Contains(cfg, "Bind 0.0.0.0:7617\n") {
		t.Errorf("empty Listen should render 0.0.0.0\n%s", cfg)
	}
	if !strings.Contains(cfg, "WorkerThreads 1\n") {
		t.Errorf("zero Workers should render the factory default 1\n%s", cfg)
	}
	// No ContainerName: fall back to a generic Name rather than emitting a blank
	// "Name " line.
	if !strings.Contains(cfg, "Name pgcli-predixy\n") {
		t.Errorf("empty ContainerName should render the generic fallback name\n%s", cfg)
	}
}

func TestRenderPredixyCfgErrors(t *testing.T) {
	base := func() *config.PredixyConfig {
		return &config.PredixyConfig{
			Name: "proxy", Port: 7617, Password: "pw",
			Backend: []string{"127.0.0.1:6379"},
		}
	}
	t.Run("no backends", func(t *testing.T) {
		p := base()
		p.Backend = nil
		if _, err := RenderPredixyCfg(p); err == nil {
			t.Fatal("expected an error for an empty backend list")
		}
	})
	t.Run("no password", func(t *testing.T) {
		p := base()
		p.Password = ""
		if _, err := RenderPredixyCfg(p); err == nil {
			t.Fatal("expected an error for an empty password")
		}
	})
}
