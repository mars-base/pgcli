package cli

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func TestParseK3sRegistryMirrors(t *testing.T) {
	got, err := parseK3sRegistryMirrors([]string{
		"docker.io=https://mirror.example.com",
		"registry.k8s.io=https://k8s.example.com",
		"docker.io=https://backup.example.com",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !slices.Equal(got["docker.io"], []string{"https://mirror.example.com", "https://backup.example.com"}) {
		t.Errorf("docker.io = %v", got["docker.io"])
	}
	if !slices.Equal(got["registry.k8s.io"], []string{"https://k8s.example.com"}) {
		t.Errorf("registry.k8s.io = %v", got["registry.k8s.io"])
	}

	for _, bad := range []string{"docker.io", "=https://m", "docker.io=", " docker.io = "} {
		if _, err := parseK3sRegistryMirrors([]string{bad}); err == nil {
			t.Errorf("parse(%q) = nil error, want a REGISTRY=ENDPOINT error", bad)
		}
	}
}

func TestK3sAPIHost(t *testing.T) {
	if got := k3sAPIHost(config.K3sConfig{AdvertiseAddress: "10.0.0.5", NodeName: "n1"}); got != "10.0.0.5" {
		t.Errorf("advertise wins: got %q", got)
	}
	if got := k3sAPIHost(config.K3sConfig{NodeName: "n1"}); got != "n1" {
		t.Errorf("node name fallback: got %q", got)
	}
	if got := k3sAPIHost(config.K3sConfig{}); got != "127.0.0.1" {
		t.Errorf("loopback fallback: got %q", got)
	}
}

func TestSortK3sBootOrder(t *testing.T) {
	nodes := []config.K3sConfig{
		{Name: "agent1", Role: "agent"},
		{Name: "srv2", Role: "server", ServerURL: "https://10.0.0.1:6443"},
		{Name: "solo", Role: "server"},
		{Name: "srv1", Role: "server", ClusterInit: true},
		{Name: "agent2", Role: "agent"},
		{Name: "srv3", Role: "server", ServerURL: "https://10.0.0.1:6443"},
	}
	sortK3sBootOrder(nodes)
	var order []string
	for _, kc := range nodes {
		order = append(order, kc.Name)
	}
	want := []string{"srv1", "srv2", "srv3", "solo", "agent1", "agent2"}
	if !slices.Equal(order, want) {
		t.Errorf("boot order = %v, want %v", order, want)
	}
}

func writeK3sCLITestConfig(t *testing.T, nodes map[string]config.K3sConfig) {
	t.Helper()
	cfg := config.Default()
	cfg.Addons.K3s = nodes
	path := filepath.Join(t.TempDir(), "pg.yaml")
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	oldPath := cfgPath
	cfgPath = path
	t.Cleanup(func() { cfgPath = oldPath })
}

func TestRunRemoteKubectlMissingKubeconfig(t *testing.T) {
	if err := runRemoteKubectl(filepath.Join(t.TempDir(), "missing.yaml"), []string{"get", "nodes"}); err == nil || !strings.Contains(err.Error(), "kubeconfig") {
		t.Errorf("missing kubeconfig = %v, want a kubeconfig error", err)
	}
}

func TestK3sCLITarget(t *testing.T) {
	writeK3sCLITestConfig(t, map[string]config.K3sConfig{
		"k3s":     {Name: "k3s", Role: "server", TokenFile: "/token"},
		"worker1": {Name: "worker1", Role: "agent", ServerURL: "https://10.0.0.1:6443", TokenFile: "/token"},
	})

	if _, kc, err := k3sCLITarget("k3s"); err != nil || kc.Name != "k3s" {
		t.Errorf("--name k3s = %v, %v", kc.Name, err)
	}
	if _, _, err := k3sCLITarget("worker1"); err == nil || !strings.Contains(err.Error(), "agent") {
		t.Errorf("agent target = %v, want agent error", err)
	}
	if _, _, err := k3sCLITarget("missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing target = %v, want not-found error", err)
	}
	if _, _, err := k3sCLITarget(""); err == nil || !strings.Contains(err.Error(), "multiple k3s nodes") {
		t.Errorf("ambiguous target = %v, want multiple-nodes error", err)
	}

	writeK3sCLITestConfig(t, map[string]config.K3sConfig{
		"worker1": {Name: "worker1", Role: "agent", ServerURL: "https://10.0.0.1:6443", TokenFile: "/token"},
	})
	if _, _, err := k3sCLITarget(""); err == nil || !strings.Contains(err.Error(), "agent") {
		t.Errorf("sole agent target = %v, want agent error", err)
	}

	writeK3sCLITestConfig(t, nil)
	if _, _, err := k3sCLITarget(""); err == nil || !strings.Contains(err.Error(), "no k3s nodes") {
		t.Errorf("empty config = %v, want no-nodes error", err)
	}
}
