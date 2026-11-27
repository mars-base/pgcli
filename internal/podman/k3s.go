package podman

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// K3sManager manages K3s server/agent nodes running as rootful Podman
// containers on the host network. Each host runs at most one node: the API,
// etcd and kubelet ports are fixed.
type K3sManager struct {
	cfg     *config.Config
	podman  string
	dataDir string
	ip      string
}

// NewK3sManager refuses hosts K3s cannot run on, then resolves podman.
func NewK3sManager(cfg *config.Config) (*K3sManager, error) {
	if err := k3sUnsupported(platform.Detect(), platform.Rootless()); err != nil {
		return nil, err
	}
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := ""
	if cfg != nil {
		dataDir = cfg.BaseDir
	}
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &K3sManager{cfg: cfg, podman: path, dataDir: dataDir, ip: findIP()}, nil
}

// findIP resolves the ip binary used for stale-CNI cleanup; empty when absent.
func findIP() string {
	path, err := exec.LookPath("ip")
	if err != nil {
		return ""
	}
	return path
}

// k3sUnsupported is the single refusal point for unsupported hosts, pure so
// tests can drive it without rootful podman.
func k3sUnsupported(goos platform.OS, rootless bool) error {
	if goos != platform.Linux {
		return errors.New("K3s requires Linux: its containers need host networking, privileged mode and the host cgroup namespace, which podman only provides natively on Linux")
	}
	if rootless {
		return errors.New("K3s requires rootful podman: a rootless container cannot join the host network/cgroup namespaces or mount the kubelet and CNI paths K3s needs")
	}
	return nil
}

// InstanceDir returns the per-node state directory (generated registries.yaml
// and default data/kubelet/cni/log parents).
func (m *K3sManager) InstanceDir(kc *config.K3sConfig) string {
	return filepath.Join(m.dataDir, "addon", "k3s", kc.Name)
}

// EnsureContainer creates the node container when it does not exist yet. It
// never touches an existing container: running nodes keep running and stopped
// ones are left for StartContainer. Use RecreateContainer to apply changed
// flags.
func (m *K3sManager) EnsureContainer(kc *config.K3sConfig) error {
	if err := m.prepare(kc); err != nil {
		return err
	}
	containerName := kc.ContainerName
	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	m.cleanupStaleCNI()
	if err := m.checkPorts(kc); err != nil {
		return err
	}
	if err := m.EnsureImage(kc.ImageTag); err != nil {
		return err
	}
	registries, err := m.registriesPath(kc)
	if err != nil {
		return err
	}
	if _, err := m.run(k3sRunArgs(kc, registries)...); err != nil {
		return fmt.Errorf("creating k3s container: %w", err)
	}
	return nil
}

// RecreateContainer removes the existing container and creates a fresh one
// from the current config. Host data directories are preserved, so an
// initialized node simply restarts; role/cluster switches still require
// clearing the data dir.
func (m *K3sManager) RecreateContainer(kc *config.K3sConfig) error {
	if err := m.prepare(kc); err != nil {
		return err
	}
	if _, err := m.run("rm", "-f", kc.ContainerName); err != nil {
		return fmt.Errorf("removing k3s container: %w", err)
	}
	m.cleanupStaleCNI()
	if err := m.checkPorts(kc); err != nil {
		return err
	}
	if err := m.EnsureImage(kc.ImageTag); err != nil {
		return err
	}
	registries, err := m.registriesPath(kc)
	if err != nil {
		return err
	}
	if _, err := m.run(k3sRunArgs(kc, registries)...); err != nil {
		return fmt.Errorf("recreating k3s container: %w", err)
	}
	return nil
}

// StartContainer starts an existing node container, recreating it when it is
// missing or stuck in an improper state.
func (m *K3sManager) StartContainer(kc *config.K3sConfig) error {
	running, err := m.ContainerRunning(kc.ContainerName)
	if err != nil {
		return err
	}
	if running {
		return nil
	}
	exists, err := m.containerExists(kc.ContainerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", kc.ContainerName); err == nil {
			return nil
		}
		fmt.Printf("  [!] k3s %s in improper state, recreating...\n", kc.ContainerName)
		return m.RecreateContainer(kc)
	}
	return m.EnsureContainer(kc)
}

// Stop stops a node container.
func (m *K3sManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// Remove deletes the container. With cleanData it also deletes the data,
// kubelet, CNI and log directories plus the generated registries.yaml; user
// supplied token/CA/registries files are never removed.
func (m *K3sManager) Remove(kc *config.K3sConfig, cleanData bool) error {
	m.run("stop", kc.ContainerName)
	if _, err := m.run("rm", "-f", kc.ContainerName); err != nil {
		return fmt.Errorf("removing k3s container: %w", err)
	}
	m.cleanupStaleCNI()
	fmt.Println("  [OK] k3s container removed")
	if !cleanData {
		return nil
	}
	for _, dir := range []string{kc.DataDir, kc.KubeletDir, kc.CNIDir, kc.LogDir} {
		if dir == "" {
			continue
		}
		if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
			fmt.Printf("  [!] Warning: removing %s: %v\n", dir, err)
		} else {
			fmt.Printf("  [OK] Removed %s\n", dir)
		}
	}
	os.Remove(filepath.Join(m.InstanceDir(kc), "registries.yaml"))
	os.Remove(m.InstanceDir(kc))
	return nil
}

// ContainerRunning reports whether the named container is running.
func (m *K3sManager) ContainerRunning(name string) (bool, error) {
	out, err := m.run("ps", "--filter", "name="+name, "--filter", "status=running", "--format", "{{.Names}}")
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}
	return false, nil
}

// EnsureImage pulls the node image when it is not already local.
func (m *K3sManager) EnsureImage(imageTag string) error {
	if _, err := m.run("image", "exists", imageTag); err == nil {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	cmd := podmanCommand(m.podman, "pull", imageTag)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pulling K3s image %s: %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// WaitReady blocks until a server answers /readyz or an agent has registered
// its node. timeout <= 0 uses 120s.
func (m *K3sManager) WaitReady(kc *config.K3sConfig, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		running, err := m.ContainerRunning(kc.ContainerName)
		if err != nil {
			return err
		}
		if !running {
			return fmt.Errorf("k3s container %s is not running", kc.ContainerName)
		}
		if kc.Role == "agent" {
			out, _ := m.run("logs", "--tail", "200", kc.ContainerName)
			if strings.Contains(out, "Successfully registered node") ||
				strings.Contains(out, "Fast updating node status as it just became ready") {
				return nil
			}
		} else if _, err := m.run("exec", kc.ContainerName, "kubectl", "get", "--raw=/readyz"); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("k3s %s did not become ready within %s", kc.Role, timeout)
		}
		time.Sleep(2 * time.Second)
	}
}

// Kubectl runs kubectl inside the node container. With terminal=true it
// attaches a TTY; stdin is always attached so `kubectl apply -f -` works.
func (m *K3sManager) Kubectl(kc *config.K3sConfig, args []string, terminal bool) error {
	podmanArgs := []string{"exec", "-i"}
	if terminal {
		podmanArgs = append(podmanArgs, "-t")
	}
	podmanArgs = append(podmanArgs, kc.ContainerName, "kubectl")
	podmanArgs = append(podmanArgs, args...)
	cmd := podmanCommand(m.podman, podmanArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// KubectlOutput runs kubectl inside the node container and returns stdout.
func (m *K3sManager) KubectlOutput(kc *config.K3sConfig, args ...string) (string, error) {
	podmanArgs := append([]string{"exec", kc.ContainerName, "kubectl"}, args...)
	return m.run(podmanArgs...)
}

// Kubeconfig returns the node's admin kubeconfig. When AdvertiseAddress is
// set, the loopback API server URL is rewritten to that address so the file
// works from other hosts.
func (m *K3sManager) Kubeconfig(kc *config.K3sConfig) (string, error) {
	out, err := m.KubectlOutput(kc, "config", "view", "--raw")
	if err != nil {
		return "", err
	}
	return rewriteKubeconfigServer(out, kc.AdvertiseAddress), nil
}

// rewriteKubeconfigServer points a loopback kubeconfig at the node's
// advertise address so it is usable from other hosts. An empty address keeps
// the file untouched.
func rewriteKubeconfigServer(kubeconfig, advertiseAddress string) string {
	if advertiseAddress == "" {
		return kubeconfig
	}
	addr := advertiseAddress
	if strings.Contains(addr, ":") && !strings.HasPrefix(addr, "[") {
		addr = "[" + addr + "]"
	}
	target := "https://" + addr + ":6443"
	kubeconfig = strings.ReplaceAll(kubeconfig, "https://127.0.0.1:6443", target)
	return strings.ReplaceAll(kubeconfig, "https://localhost:6443", target)
}

// --- Setup helpers ---------------------------------------------------

func (m *K3sManager) prepare(kc *config.K3sConfig) error {
	if kc.TokenFile == "" {
		return errors.New("k3s token_file is required")
	}
	if _, err := os.Stat(kc.TokenFile); err != nil {
		return fmt.Errorf("k3s token file: %w", err)
	}
	for _, dir := range []string{kc.DataDir, kc.KubeletDir, kc.CNIDir, kc.LogDir} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating k3s directory %s: %w", dir, err)
		}
	}
	for _, file := range []string{kc.LogFile, kc.ContainerLogFile} {
		if file == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return fmt.Errorf("creating parent of %s: %w", file, err)
		}
		f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("creating k3s log file %s: %w", file, err)
		}
		f.Close()
	}
	if kc.CADir != "" {
		if err := m.injectCA(kc); err != nil {
			return err
		}
	}
	return nil
}

// injectCA copies a custom CA into <data-dir>/server/tls before the initial
// server starts. On repeat installs the existing copy is kept when it matches
// the source; a different CA is refused because the datastore already holds
// the original trust material.
func (m *K3sManager) injectCA(kc *config.K3sConfig) error {
	if kc.CADir == "" {
		return nil
	}
	if !kc.InitialServer() {
		return fmt.Errorf("k3s ca_dir is only valid on the initial server")
	}
	if err := config.ValidateK3sCADir(kc.CADir); err != nil {
		return err
	}
	tlsDir := filepath.Join(kc.DataDir, "server", "tls")
	dstCA := filepath.Join(tlsDir, "server-ca.crt")
	if _, err := os.Stat(dstCA); err == nil {
		same, err := sameK3sCA(kc.CADir, tlsDir)
		if err != nil {
			return err
		}
		if same {
			return nil
		}
		return fmt.Errorf("custom CA in %s differs from the CA already initializing %s; clear the data directory or rotate the CA with `k3s certificate rotate-ca` instead of reinstalling", kc.CADir, kc.DataDir)
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, rel := range config.K3sCertificateFiles {
		src := filepath.Join(kc.CADir, rel)
		dst := filepath.Join(tlsDir, rel)
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("reading custom CA file: %w", err)
		}
		perm := os.FileMode(0o644)
		if strings.HasSuffix(rel, ".key") {
			perm = 0o600
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, perm); err != nil {
			return fmt.Errorf("writing custom CA file %s: %w", dst, err)
		}
	}
	fmt.Printf("  [OK] Custom CA injected into %s\n", tlsDir)
	return nil
}

func sameK3sCA(srcDir, dstDir string) (bool, error) {
	for _, rel := range config.K3sCertificateFiles {
		src, err := os.ReadFile(filepath.Join(srcDir, rel))
		if err != nil {
			return false, fmt.Errorf("reading custom CA file: %w", err)
		}
		dst, err := os.ReadFile(filepath.Join(dstDir, rel))
		if err != nil {
			return false, nil
		}
		if string(src) != string(dst) {
			return false, nil
		}
	}
	return true, nil
}

// registriesPath returns the host path to mount at
// /run/k3s-registries.yaml, or "" when neither mirrors nor a full file is
// configured.
func (m *K3sManager) registriesPath(kc *config.K3sConfig) (string, error) {
	if kc.RegistriesFile != "" {
		if _, err := os.Stat(kc.RegistriesFile); err != nil {
			return "", fmt.Errorf("k3s registries file: %w", err)
		}
		return kc.RegistriesFile, nil
	}
	if len(kc.RegistryMirrors) == 0 {
		return "", nil
	}
	path := filepath.Join(m.InstanceDir(kc), "registries.yaml")
	content := renderK3sRegistries(kc.RegistryMirrors)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("writing k3s registries.yaml: %w", err)
	}
	return path, nil
}

// renderK3sRegistries writes a deterministic registries.yaml: keys and
// endpoints are sorted so repeated installs do not rewrite the file.
func renderK3sRegistries(mirrors map[string][]string) string {
	registries := make([]string, 0, len(mirrors))
	for registry := range mirrors {
		registries = append(registries, registry)
	}
	sort.Strings(registries)
	var b strings.Builder
	b.WriteString("mirrors:\n")
	for _, registry := range registries {
		endpoints := append([]string(nil), mirrors[registry]...)
		sort.Strings(endpoints)
		fmt.Fprintf(&b, "  %s:\n    endpoint:\n", strconv.Quote(registry))
		for _, endpoint := range endpoints {
			fmt.Fprintf(&b, "      - %s\n", strconv.Quote(endpoint))
		}
	}
	return b.String()
}

// checkPorts refuses a fresh install when a required host-network port is
// already taken. Ports 80/443 are only warned about: K3s's bundled Traefik
// and ServiceLB claim them after startup, which conflicts with web addons.
func (m *K3sManager) checkPorts(kc *config.K3sConfig) error {
	ssPath, err := exec.LookPath("ss")
	if err != nil {
		return nil
	}
	out, err := exec.Command(ssPath, "-H", "-lntu").Output()
	if err != nil {
		return nil
	}
	usedTCP := map[int]string{}
	usedUDP := map[int]string{}
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		netID := fields[0]
		local := fields[4]
		idx := strings.LastIndex(local, ":")
		if idx < 0 {
			continue
		}
		port, err := strconv.Atoi(local[idx+1:])
		if err != nil {
			continue
		}
		if strings.HasPrefix(netID, "tcp") {
			usedTCP[port] = local
		} else if strings.HasPrefix(netID, "udp") {
			usedUDP[port] = local
		}
	}
	requiredTCP := []int{10250}
	requiredUDP := []int{8472}
	if kc.Role == "server" {
		requiredTCP = append([]int{6443, 2379, 2380}, requiredTCP...)
	}
	for _, port := range requiredTCP {
		if addr, ok := usedTCP[port]; ok {
			return fmt.Errorf("K3s %s needs TCP port %d, already listening on %s", kc.Role, port, addr)
		}
	}
	for _, port := range requiredUDP {
		if addr, ok := usedUDP[port]; ok {
			msg := fmt.Sprintf("K3s %s needs UDP port %d, already listening on %s", kc.Role, port, addr)
			if port == 8472 && m.staleFlannelPresent() {
				msg += " (held by the flannel.1 link of a previous K3s node; delete it with `ip link delete flannel.1` and `ip link delete cni0`)"
			}
			return errors.New(msg)
		}
	}
	for _, port := range []int{80, 443} {
		if addr, ok := usedTCP[port]; ok {
			fmt.Printf("  [!] TCP port %d is already in use (%s); K3s Traefik/ServiceLB will fail to bind it — pass --disable traefik (or --disable servicelb) if that is intended\n", port, addr)
		}
	}
	return nil
}

// staleFlannelPresent reports whether a leftover flannel.1 device exists on
// the host, which explains an otherwise invisible 8472/udp conflict: a VXLAN
// socket is held by the kernel and does not show up under `ss -lunp`.
func (m *K3sManager) staleFlannelPresent() bool {
	if m.ip == "" {
		return false
	}
	return exec.Command(m.ip, "link", "show", "flannel.1").Run() == nil
}

// cleanupStaleCNI deletes the flannel.1 and cni0 links a killed K3s
// container left in the host network namespace. Host-network K3s creates
// them outside the container lifetime, so `podman rm -f` leaves them behind
// and their VXLAN socket silently occupies 8472/udp, blocking the next
// install. It is a no-op while another pgcli K3s node is running.
func (m *K3sManager) cleanupStaleCNI() {
	if m.ip == "" {
		return
	}
	out, err := m.run("ps", "--filter", "status=running", "--format", "{{.Names}}")
	if err != nil {
		return
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "pgcli-k3s-") {
			return
		}
	}
	for _, link := range []string{"flannel.1", "cni0"} {
		if exec.Command(m.ip, "link", "show", link).Run() != nil {
			continue
		}
		if out, err := exec.Command(m.ip, "link", "delete", link).CombinedOutput(); err != nil {
			fmt.Printf("  [!] Could not delete stale K3s link %s: %v (%s)\n", link, err, strings.TrimSpace(string(out)))
			continue
		}
		fmt.Printf("  [OK] Removed stale K3s link %s left by a previous node\n", link)
	}
}

func (m *K3sManager) containerExists(name string) (bool, error) {
	out, err := m.run("ps", "-a", "--filter", "name="+name, "--format", "{{.Names}}")
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}
	return false, nil
}

func (m *K3sManager) run(args ...string) (string, error) {
	cmd := podmanCommand(m.podman, args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("podman %s: %s", strings.Join(args, " "), string(exitErr.Stderr))
		}
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// k3sRunArgs builds the podman run argv. Pure so the full flag matrix is
// unit-testable without podman.
func k3sRunArgs(kc *config.K3sConfig, registries string) []string {
	args := []string{
		"run", "-d",
		"--name", kc.ContainerName,
		"--hostname", kc.NodeName,
		"--network", "host",
		"--privileged",
		"--userns=host",
		"--cgroupns=host",
		"--restart", "unless-stopped",
		"--tmpfs", "/run",
		"--tmpfs", "/var/run",
	}
	if kc.ContainerLogFile != "" {
		args = append(args, "--log-driver", "k8s-file", "--log-opt", "path="+kc.ContainerLogFile)
	}
	args = append(args,
		"-v", fmt.Sprintf("%s:/var/lib/rancher/k3s:z", hostMountPath(kc.DataDir)),
		"-v", fmt.Sprintf("%s:/var/lib/kubelet:z", hostMountPath(kc.KubeletDir)),
		"-v", fmt.Sprintf("%s:/var/lib/cni:z", hostMountPath(kc.CNIDir)),
		"-v", fmt.Sprintf("%s:/var/log:z", hostMountPath(kc.LogDir)),
	)
	if kc.LogFile != "" {
		args = append(args, "-v", fmt.Sprintf("%s:/var/log/k3s.log:z", hostMountPath(kc.LogFile)))
	}
	args = append(args, "-v", fmt.Sprintf("%s:/run/k3s-token:ro,z", hostMountPath(kc.TokenFile)))
	if registries != "" {
		args = append(args, "-v", fmt.Sprintf("%s:/run/k3s-registries.yaml:ro,z", hostMountPath(registries)))
	}
	args = append(args,
		kc.ImageTag,
		kc.Role,
		"--data-dir", "/var/lib/rancher/k3s",
		"--token-file", "/run/k3s-token",
		"--node-name", kc.NodeName,
	)
	if kc.Role == "server" {
		if kc.AdvertiseAddress != "" {
			args = append(args, "--advertise-address", kc.AdvertiseAddress)
		}
		for _, san := range kc.TLSSAN {
			args = append(args, "--tls-san", san)
		}
		for _, disabled := range kc.Disable {
			args = append(args, "--disable", disabled)
		}
	}
	for _, proxyArg := range kc.KubeProxyArgs {
		args = append(args, "--kube-proxy-arg", proxyArg)
	}
	if kc.LogFile != "" {
		args = append(args, "--log", "/var/log/k3s.log", "--alsologtostderr")
	}
	if registries != "" {
		args = append(args, "--private-registry", "/run/k3s-registries.yaml")
	}
	if kc.ClusterInit {
		args = append(args, "--cluster-init")
	}
	if kc.ServerURL != "" {
		args = append(args, "--server", kc.ServerURL)
	}
	return args
}
