package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func k3sTestConfig(kc K3sConfig) *Config {
	cfg := Default()
	cfg.BaseDir = "/srv/pg"
	if kc.TokenFile == "" {
		kc.TokenFile = "/etc/pgcli/k3s-token"
	}
	cfg.Addons.K3s = map[string]K3sConfig{"k3s": kc}
	cfg.ApplyDefaults()
	return cfg
}

func TestK3sApplyDefaults(t *testing.T) {
	cfg := k3sTestConfig(K3sConfig{})
	kc := cfg.Addons.K3s["k3s"]

	if kc.Name != "k3s" {
		t.Errorf("Name = %q, want k3s", kc.Name)
	}
	if kc.ContainerName != "pgcli-k3s-k3s" {
		t.Errorf("ContainerName = %q, want pgcli-k3s-k3s", kc.ContainerName)
	}
	if kc.Version != DefaultK3sVersion {
		t.Errorf("Version = %q, want %q", kc.Version, DefaultK3sVersion)
	}
	if kc.ImageTag != DefaultK3sImageTag {
		t.Errorf("ImageTag = %q, want %q", kc.ImageTag, DefaultK3sImageTag)
	}
	if kc.Role != "server" {
		t.Errorf("Role = %q, want server", kc.Role)
	}
	if kc.NodeName == "" {
		t.Error("NodeName is empty, want hostname default")
	}
	for field, got := range map[string]string{
		"DataDir":    kc.DataDir,
		"KubeletDir": kc.KubeletDir,
		"CNIDir":     kc.CNIDir,
		"LogDir":     kc.LogDir,
	} {
		want := filepath.Join("/srv/pg/addon/k3s/k3s", map[string]string{
			"DataDir": "data", "KubeletDir": "kubelet", "CNIDir": "cni", "LogDir": "log",
		}[field])
		if got != want {
			t.Errorf("%s = %q, want %q", field, got, want)
		}
	}
	if !kc.InitialServer() {
		t.Error("InitialServer() = false, want true for a standalone server")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestK3sVersionAndImageOverride(t *testing.T) {
	cfg := k3sTestConfig(K3sConfig{Version: "v1.36.0-k3s1"})
	if got := cfg.Addons.K3s["k3s"].ImageTag; got != "docker.io/rancher/k3s:v1.36.0-k3s1" {
		t.Errorf("ImageTag = %q", got)
	}

	cfg = k3sTestConfig(K3sConfig{ImageTag: "registry.example.com/k3s:custom"})
	kc := cfg.Addons.K3s["k3s"]
	if kc.ImageTag != "registry.example.com/k3s:custom" {
		t.Errorf("ImageTag = %q", kc.ImageTag)
	}
	if kc.Version != "" {
		t.Errorf("Version = %q, want empty when image_tag is set without version", kc.Version)
	}
}

func TestK3sValidateRoles(t *testing.T) {
	valid := []K3sConfig{
		{},
		{ClusterInit: true, AdvertiseAddress: "10.0.0.1", TLSSAN: []string{"10.0.0.1"}},
		{Role: "server", ServerURL: "https://10.0.0.1:6443"},
		{Role: "agent", ServerURL: "https://10.0.0.1:6443"},
	}
	for _, kc := range valid {
		if err := k3sTestConfig(kc).Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", kc, err)
		}
	}

	invalid := []struct {
		name string
		kc   K3sConfig
		want string
	}{
		{"bad role", K3sConfig{Role: "worker"}, "role must be server or agent"},
		{"agent without server", K3sConfig{Role: "agent"}, "must set server_url"},
		{"agent cluster init", K3sConfig{Role: "agent", ServerURL: "https://10.0.0.1:6443", ClusterInit: true}, "cluster_init is only valid"},
		{"agent advertise", K3sConfig{Role: "agent", ServerURL: "https://10.0.0.1:6443", AdvertiseAddress: "10.0.0.2"}, "server-only"},
		{"agent ca", K3sConfig{Role: "agent", ServerURL: "https://10.0.0.1:6443", CADir: "/ca"}, "ca_dir is only valid"},
		{"init and join", K3sConfig{ClusterInit: true, ServerURL: "https://10.0.0.1:6443"}, "mutually exclusive"},
		{"ca on joiner", K3sConfig{ServerURL: "https://10.0.0.1:6443", CADir: "/ca"}, "ca_dir is only valid"},
		{"bad server url", K3sConfig{ServerURL: "http://10.0.0.1:6443"}, "must be https"},
		{"mirror and file", K3sConfig{RegistriesFile: "/r.yaml", RegistryMirrors: map[string][]string{"docker.io": {"https://m"}}}, "mutually exclusive"},
		{"bad mirror endpoint", K3sConfig{RegistryMirrors: map[string][]string{"docker.io": {"ftp://m"}}}, "must be http(s)"},
		{"empty mirror endpoints", K3sConfig{RegistryMirrors: map[string][]string{"docker.io": {}}}, "at least one endpoint"},
		{"plus version", K3sConfig{Version: "v1.37.2+k3s1"}, "contains '+'"},
		{"plus image", K3sConfig{ImageTag: "docker.io/rancher/k3s:v1.37.2+k3s1"}, "contains '+'"},
		{"relative dir", K3sConfig{DataDir: "data"}, "must be an absolute path"},
	}
	for _, tc := range invalid {
		err := k3sTestConfig(tc.kc).Validate()
		if err == nil {
			t.Errorf("%s: Validate = nil, want error containing %q", tc.name, tc.want)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %q, want substring %q", tc.name, err, tc.want)
		}
	}
}

func TestK3sValidateTokenAndSingleNode(t *testing.T) {
	cfg := Default()
	cfg.BaseDir = "/srv/pg"
	cfg.Addons.K3s = map[string]K3sConfig{"k3s": {}}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "token_file is required") {
		t.Fatalf("Validate = %v, want token_file error", err)
	}

	cfg = Default()
	cfg.BaseDir = "/srv/pg"
	cfg.Addons.K3s = map[string]K3sConfig{
		"a": {TokenFile: "/token"},
		"b": {TokenFile: "/token", Role: "agent", ServerURL: "https://10.0.0.1:6443"},
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "only one node per host") {
		t.Fatalf("Validate = %v, want single-node error", err)
	}
}

func TestValidateK3sCADir(t *testing.T) {
	dir := t.TempDir()
	if err := ValidateK3sCADir(""); err != nil {
		t.Fatalf("empty dir: %v", err)
	}
	if err := ValidateK3sCADir(dir); err == nil {
		t.Fatal("empty CA dir should fail validation")
	}
	for _, rel := range K3sCertificateFiles {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateK3sCADir(dir); err != nil {
		t.Fatalf("complete CA dir: %v", err)
	}
}