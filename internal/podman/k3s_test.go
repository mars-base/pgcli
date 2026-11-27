package podman

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

func k3sTestConfig(t *testing.T, kc config.K3sConfig) config.K3sConfig {
	t.Helper()
	cfg := config.Default()
	cfg.BaseDir = t.TempDir()
	if kc.TokenFile == "" {
		kc.TokenFile = filepath.Join(t.TempDir(), "k3s-token")
		if err := os.WriteFile(kc.TokenFile, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Addons.K3s = map[string]config.K3sConfig{"k3s": kc}
	cfg.ApplyDefaults()
	return cfg.Addons.K3s["k3s"]
}

func TestK3sUnsupported(t *testing.T) {
	cases := []struct {
		name     string
		goos     platform.OS
		rootless bool
		wantSub  string
	}{
		{"macOS", platform.MacOS, false, "Linux"},
		{"rootless linux", platform.Linux, true, "rootful"},
		{"rootful linux", platform.Linux, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := k3sUnsupported(tc.goos, tc.rootless)
			if tc.wantSub == "" {
				if err != nil {
					t.Errorf("unexpected refusal: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("k3sUnsupported = %v, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestK3sRunArgsServer(t *testing.T) {
	kc := k3sTestConfig(t, config.K3sConfig{
		ClusterInit:      true,
		AdvertiseAddress: "10.0.0.5",
		TLSSAN:           []string{"10.0.0.5", "api.example.com"},
		Disable:          []string{"traefik", "servicelb"},
		ClusterCIDR:      "10.42.0.0/16",
		ServiceCIDR:      "10.43.0.0/16,2001:db8:43::/112",
		KubeProxyArgs:    []string{"proxy-mode=nftables"},
		LogFile:          filepath.Join(t.TempDir(), "k3s.log"),
		ContainerLogFile: filepath.Join(t.TempDir(), "container.log"),
	})
	args := k3sRunArgs(&kc, "/tmp/registries.yaml")

	if args[0] != "run" || args[1] != "-d" {
		t.Fatalf("argv starts with %q, want run -d", args[:2])
	}
	for flag, want := range map[string]string{
		"--name":       kc.ContainerName,
		"--hostname":   kc.NodeName,
		"--network":    "host",
		"--userns":     "host",
		"--cgroupns":   "host",
		"--restart":    "unless-stopped",
		"--log-driver": "k8s-file",
	} {
		got := argValue(args, flag)
		if flag == "--userns" || flag == "--cgroupns" {
			if !slices.Contains(args, flag+"="+want) {
				t.Errorf("missing %s=%s in %q", flag, want, args)
			}
			continue
		}
		if got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
	if !slices.Contains(args, "--privileged") {
		t.Error("missing --privileged")
	}
	if !slices.Contains(args, "path="+kc.ContainerLogFile) {
		t.Errorf("missing container log path %q", kc.ContainerLogFile)
	}
	for _, mount := range []string{
		kc.DataDir + ":/var/lib/rancher/k3s:z",
		kc.KubeletDir + ":/var/lib/kubelet:z",
		kc.CNIDir + ":/var/lib/cni:z",
		kc.LogDir + ":/var/log:z",
		kc.LogFile + ":/var/log/k3s.log:z",
		kc.TokenFile + ":/run/k3s-token:ro,z",
		"/tmp/registries.yaml:/run/k3s-registries.yaml:ro,z",
	} {
		if !slices.Contains(args, mount) {
			t.Errorf("missing mount %q", mount)
		}
	}

	idx := slices.Index(args, kc.ImageTag)
	if idx < 0 {
		t.Fatalf("image %q missing from argv", kc.ImageTag)
	}
	if args[idx+1] != "server" {
		t.Errorf("first arg after image = %q, want server", args[idx+1])
	}
	for _, flag := range []string{
		"--name", "--hostname", "--network", "--privileged", "--userns=host",
		"--cgroupns=host", "--restart", "--tmpfs", "--log-driver", "--log-opt",
		"-v", "--privileged",
	} {
		if pos := slices.Index(args, flag); pos > idx {
			t.Errorf("%s must precede the image at %d, found at %d", flag, idx, pos)
		}
	}
	serverArgs := args[idx+2:]
	for _, want := range []string{
		"--data-dir", "/var/lib/rancher/k3s",
		"--token-file", "/run/k3s-token",
		"--node-name", kc.NodeName,
		"--advertise-address", "10.0.0.5",
		"--tls-san", "10.0.0.5",
		"--tls-san", "api.example.com",
		"--disable", "traefik",
		"--disable", "servicelb",
		"--cluster-cidr", "10.42.0.0/16",
		"--service-cidr", "10.43.0.0/16,2001:db8:43::/112",
		"--kube-proxy-arg", "proxy-mode=nftables",
		"--log", "/var/log/k3s.log",
		"--alsologtostderr",
		"--private-registry", "/run/k3s-registries.yaml",
		"--cluster-init",
	} {
		if !slices.Contains(serverArgs, want) {
			t.Errorf("missing %q in server argv %q", want, serverArgs)
		}
	}
	if slices.Contains(serverArgs, "--server") {
		t.Error("cluster-init server must not also join a server")
	}
}

func TestK3sRunArgsAgent(t *testing.T) {
	kc := k3sTestConfig(t, config.K3sConfig{
		Role:             "agent",
		ServerURL:        "https://10.0.0.5:6443",
		AdvertiseAddress: "10.0.0.6",
		TLSSAN:           []string{"10.0.0.6"},
		Disable:          []string{"traefik"},
		ClusterCIDR:      "10.42.0.0/16",
		ServiceCIDR:      "10.43.0.0/16",
		KubeProxyArgs:    []string{"proxy-mode=nftables"},
	})
	args := k3sRunArgs(&kc, "")
	idx := slices.Index(args, kc.ImageTag)
	if idx < 0 {
		t.Fatal("image missing")
	}
	if args[idx+1] != "agent" {
		t.Errorf("first arg after image = %q, want agent", args[idx+1])
	}
	agentArgs := args[idx+2:]
	if slices.Contains(agentArgs, "--advertise-address") || slices.Contains(agentArgs, "--tls-san") || slices.Contains(agentArgs, "--disable") ||
		slices.Contains(agentArgs, "--cluster-cidr") || slices.Contains(agentArgs, "--service-cidr") {
		t.Errorf("agent argv carries server-only flags: %q", agentArgs)
	}
	if slices.Contains(agentArgs, "--cluster-init") {
		t.Error("agent argv must not cluster-init")
	}
	if !slices.Contains(agentArgs, "--server") || !slices.Contains(agentArgs, "https://10.0.0.5:6443") {
		t.Errorf("agent argv missing join flags: %q", agentArgs)
	}
	if idx := slices.Index(agentArgs, "--kube-proxy-arg"); idx < 0 || agentArgs[idx+1] != "proxy-mode=nftables" {
		t.Errorf("agent argv missing --kube-proxy-arg: %q", agentArgs)
	}
	if slices.Contains(args, "--private-registry") {
		t.Error("no registries file: --private-registry must be omitted")
	}
}

func TestK3sRunArgsMinimal(t *testing.T) {
	kc := k3sTestConfig(t, config.K3sConfig{})
	for _, flag := range []string{"--advertise-address", "--tls-san", "--disable", "--cluster-cidr", "--service-cidr", "--kube-proxy-arg", "--log", "--alsologtostderr", "--private-registry", "--cluster-init", "--server"} {
		if slices.Contains(k3sRunArgs(&kc, ""), flag) {
			t.Errorf("minimal server argv must omit %s", flag)
		}
	}
	if slices.Contains(k3sRunArgs(&kc, ""), "--log-driver") {
		t.Error("minimal server argv must omit --log-driver")
	}
}

func TestRenderK3sRegistries(t *testing.T) {
	got := renderK3sRegistries(map[string][]string{
		"registry.k8s.io": {"https://b.example.com", "https://a.example.com"},
		"docker.io":       {"https://mirror.example.com"},
	})
	want := `mirrors:
  "docker.io":
    endpoint:
      - "https://mirror.example.com"
  "registry.k8s.io":
    endpoint:
      - "https://a.example.com"
      - "https://b.example.com"
`
	if got != want {
		t.Errorf("renderK3sRegistries =\n%s\nwant\n%s", got, want)
	}
	if again := renderK3sRegistries(map[string][]string{
		"docker.io":       {"https://mirror.example.com"},
		"registry.k8s.io": {"https://a.example.com", "https://b.example.com"},
	}); again != got {
		t.Error("renderK3sRegistries is not deterministic across map order")
	}
}

func TestRewriteKubeconfigServer(t *testing.T) {
	in := "server: https://127.0.0.1:6443\n"
	if got := rewriteKubeconfigServer(in, "10.0.0.5"); got != "server: https://10.0.0.5:6443\n" {
		t.Errorf("rewrite = %q", got)
	}
	if got := rewriteKubeconfigServer(in, ""); got != in {
		t.Errorf("empty advertise address must keep loopback, got %q", got)
	}
	v6 := rewriteKubeconfigServer(in, "fd00::1")
	if !strings.Contains(v6, "https://[fd00::1]:6443") {
		t.Errorf("IPv6 advertise address not bracketed: %q", v6)
	}
	if got := rewriteKubeconfigServer("server: https://localhost:6443\n", "10.0.0.5"); !strings.Contains(got, "https://10.0.0.5:6443") {
		t.Errorf("localhost rewrite failed: %q", got)
	}
}

func TestK3sInjectCA(t *testing.T) {
	src := t.TempDir()
	for _, rel := range config.K3sCertificateFiles {
		path := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(rel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	kc := k3sTestConfig(t, config.K3sConfig{CADir: src})
	m := &K3sManager{}
	if err := m.injectCA(&kc); err != nil {
		t.Fatalf("injectCA: %v", err)
	}
	keyInfo, err := os.Stat(filepath.Join(kc.DataDir, "server", "tls", "server-ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := keyInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("key mode = %o, want 600", perm)
	}
	crtInfo, err := os.Stat(filepath.Join(kc.DataDir, "server", "tls", "server-ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := crtInfo.Mode().Perm(); perm != 0o644 {
		t.Errorf("cert mode = %o, want 644", perm)
	}
	if err := m.injectCA(&kc); err != nil {
		t.Errorf("re-injecting the same CA must be idempotent: %v", err)
	}

	other := t.TempDir()
	for _, rel := range config.K3sCertificateFiles {
		path := filepath.Join(other, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("rotated "+rel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	kc.CADir = other
	if err := m.injectCA(&kc); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Errorf("different CA = %v, want differs error", err)
	}
}

func TestK3sRemoveCleanData(t *testing.T) {
	kc := k3sTestConfig(t, config.K3sConfig{})
	m := &K3sManager{podman: "true"} // container stop/rm are no-ops in this test
	for _, dir := range []string{kc.DataDir, kc.KubeletDir, kc.CNIDir, kc.LogDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(kc.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(&kc, true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	for _, dir := range []string{kc.DataDir, kc.KubeletDir, kc.CNIDir, kc.LogDir} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s still exists after clean remove: %v", dir, err)
		}
	}
	if _, err := os.Stat(kc.TokenFile); err != nil {
		t.Errorf("user token file must be preserved: %v", err)
	}
	if got, _ := os.Stat(kc.TokenFile); got.Mode() != info.Mode() {
		t.Errorf("token file mode changed: %v", got.Mode())
	}
}
