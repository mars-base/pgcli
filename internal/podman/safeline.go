package podman

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// SafeLine (雷池 WAF Community Edition) runs as seven containers per instance:
// a bundled PostgreSQL, the mgt console, the detector engine, the tengine
// traffic proxy, and the luigi/fvm/chaos support services. The upstream
// compose contract pins every bridge container to a static address inside a
// private /24, so this addon is host-network-free but rootful-only: rootless
// podman cannot assign static IPs. tengine still uses the host network because
// it must see real client addresses and bind the protected sites' ports
// directly.
const (
	SafelineRolePostgres = "postgres"
	SafelineRoleMgt      = "mgt"
	SafelineRoleDetector = "detector"
	SafelineRoleTengine  = "tengine"
	SafelineRoleLuigi    = "luigi"
	SafelineRoleFvm      = "fvm"
	SafelineRoleChaos    = "chaos"

	// The five SQL-visible component tags share one release; postgres is the
	// stack's own pinned database image.
	safelineImageRepo         = "docker.io/chaitin"
	safelinePostgresImageRepo = "docker.io/chaitin/safeline-postgres"

	// Static host octets inside the stack's /24, matching upstream compose.
	safelinePostgresHost = 2
	safelineMgtHost      = 4
	safelineDetectorHost = 5
	safelineLuigiHost    = 7
	safelineFvmHost      = 8
	safelineChaosHost    = 10

	safelineDBUser = "safeline-ce"
	safelineDBName = "safeline-ce"
)

// SafelineRoles lists the stack's roles in start order (stop/remove walk it
// in reverse). Exported so the CLI can validate --service and print the
// container set without duplicating the list.
func SafelineRoles() []string { return safelineRoles() }

// safelineRoles is the start order; stop/remove walk it in reverse. mgt comes
// right after postgres (it runs the schema migrations), the console is waited
// on before detector/tengine so their management callbacks have an API, and
// tengine starts last because it pulls its site config from mgt.
func safelineRoles() []string {
	return []string{
		SafelineRolePostgres,
		SafelineRoleMgt,
		SafelineRoleDetector,
		SafelineRoleFvm,
		SafelineRoleChaos,
		SafelineRoleLuigi,
		SafelineRoleTengine,
	}
}

// SafelineManager manages the seven-container SafeLine stack.
type SafelineManager struct {
	cfg     *config.Config
	podman  string
	dataDir string
}

// NewSafelineManager creates a SafelineManager, refusing hosts the stack
// cannot run on.
func NewSafelineManager(cfg *config.Config) (*SafelineManager, error) {
	if err := safelineUnsupported(platform.Detect(), platform.Rootless()); err != nil {
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
	ensurePodmanStateReady(path)
	return &SafelineManager{cfg: cfg, podman: path, dataDir: dataDir}, nil
}

// safelineUnsupported is the single refusal point for unsupported hosts,
// pure so tests can drive it without a rootless podman.
func safelineUnsupported(goos platform.OS, rootless bool) error {
	if goos != platform.Linux {
		return errors.New("SafeLine requires Linux: its stack pins containers to a static-IP bridge network and runs tengine on host networking, which podman only provides natively on Linux")
	}
	if rootless {
		return errors.New("SafeLine requires rootful podman: rootless cannot assign the stack's static container addresses or bind ports 80/443 for protected traffic")
	}
	return nil
}

// safelineImageRef returns the image reference for one stack container.
func safelineImageRef(sc config.SafelineConfig, role string) string {
	if role == SafelineRolePostgres {
		return safelinePostgresImageRepo + ":" + sc.PostgresVersion
	}
	return safelineImageRepo + "/safeline-" + role + ":" + sc.Version
}

// safelineImages lists every image the stack pulls, in role order.
func safelineImages(sc config.SafelineConfig) []string {
	images := make([]string, 0, len(safelineRoles()))
	for _, role := range safelineRoles() {
		images = append(images, safelineImageRef(sc, role))
	}
	return images
}

// safelineDSN is the connection string the console and its satellites use for
// the bundled PostgreSQL. The password is URL-escaped because generated
// passwords may contain DSN delimiters.
func safelineDSN(sc config.SafelineConfig) string {
	userinfo := url.UserPassword(safelineDBUser, sc.PostgresPassword).String()
	host := config.SafelineStaticIP(sc, safelinePostgresHost)
	return fmt.Sprintf("postgres://%s@%s/%s?sslmode=disable", userinfo, host, safelineDBName)
}

// safelineStaticHost maps a role to its fixed host octet; 0 means host
// networking (tengine).
func safelineStaticHost(role string) int {
	switch role {
	case SafelineRolePostgres:
		return safelinePostgresHost
	case SafelineRoleMgt:
		return safelineMgtHost
	case SafelineRoleDetector:
		return safelineDetectorHost
	case SafelineRoleLuigi:
		return safelineLuigiHost
	case SafelineRoleFvm:
		return safelineFvmHost
	case SafelineRoleChaos:
		return safelineChaosHost
	}
	return 0
}

// safelineDirs returns every host directory the stack bind-mounts.
func safelineDirs(sc config.SafelineConfig) []string {
	resources := filepath.Join(sc.DataDir, "resources")
	logs := filepath.Join(sc.DataDir, "logs")
	dirs := []string{resources, logs}
	for _, sub := range []string{"postgres/data", "mgt", "detector", "nginx", "chaos", "cache", "sock", "run", "luigi"} {
		dirs = append(dirs, filepath.Join(resources, sub))
	}
	for _, sub := range []string{"nginx", "detector"} {
		dirs = append(dirs, filepath.Join(logs, sub))
	}
	return dirs
}

// EnsureImages pulls each missing stack image.
func (m *SafelineManager) EnsureImages(sc *config.SafelineConfig) error {
	for _, image := range safelineImages(*sc) {
		exists, err := m.imageExists(image)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		fmt.Printf("-> Pulling image %s...\n", image)
		if err := m.runInteractive("pull", image); err != nil {
			return fmt.Errorf("pulling SafeLine image %s: %w", image, err)
		}
		fmt.Println("  [OK] Image pulled")
	}
	return nil
}

// EnsureNetwork creates the stack's dedicated bridge network, or validates an
// existing one still matches the configured subnet and owner label.
func (m *SafelineManager) EnsureNetwork(sc *config.SafelineConfig) error {
	exists, err := m.networkExists(sc.NetworkName)
	if err != nil {
		return err
	}
	want := config.SafelineSubnet(*sc)
	if exists {
		subnet, err := m.networkSubnet(sc.NetworkName)
		if err != nil {
			return err
		}
		if subnet != "" && subnet != want {
			return fmt.Errorf("network %s already exists with subnet %s, expected %s — remove it or install with another --subnet-prefix", sc.NetworkName, subnet, want)
		}
		labels, err := m.networkLabels(sc.NetworkName)
		if err != nil {
			return err
		}
		if !strings.Contains(labels, "pgcli.safeline:"+sc.Name) {
			return fmt.Errorf("network %s exists but is not owned by SafeLine instance %q (label pgcli.safeline=%s) — remove it or pick another --name", sc.NetworkName, sc.Name, sc.Name)
		}
		return nil
	}
	if _, err := m.run("network", "create",
		"--driver", "bridge",
		"--subnet", want,
		"--gateway", config.SafelineGateway(*sc),
		"--label", "pgcli.safeline="+sc.Name,
		sc.NetworkName,
	); err != nil {
		return fmt.Errorf("creating SafeLine network %s: %w", sc.NetworkName, err)
	}
	fmt.Printf("  [OK] network %s created (%s)\n", sc.NetworkName, want)
	return nil
}

// RemoveNetworkIfUnused deletes the stack network once no containers remain
// attached to it. A foreign container still on the network is reported instead
// of being disconnected.
func (m *SafelineManager) RemoveNetworkIfUnused(sc *config.SafelineConfig) error {
	exists, err := m.networkExists(sc.NetworkName)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	out, err := m.run("ps", "-a", "--filter", "network="+sc.NetworkName, "--format", "{{.Names}}")
	if err != nil {
		return err
	}
	if attached := strings.Fields(out); len(attached) > 0 {
		return fmt.Errorf("network %s still has containers attached: %s", sc.NetworkName, strings.Join(attached, ", "))
	}
	if _, err := m.run("network", "rm", sc.NetworkName); err != nil {
		return fmt.Errorf("removing SafeLine network %s: %w", sc.NetworkName, err)
	}
	fmt.Printf("  [OK] network %s removed\n", sc.NetworkName)
	return nil
}

// EnsureContainers recreates the whole stack (install/config-change
// semantics). The bridge roles are created in start order (postgres is waited
// on before mgt runs its migrations), the console is waited on once they all
// exist — mgt loads rules from fvm and restarts itself until that container is
// up — and tengine is created last because it fetches its site config from
// the console. Data is never deleted here.
func (m *SafelineManager) EnsureContainers(sc *config.SafelineConfig) error {
	if err := m.ensureFiles(sc); err != nil {
		return err
	}
	if err := m.EnsureImages(sc); err != nil {
		return err
	}
	for _, role := range safelineRoles() {
		if role == SafelineRoleTengine {
			continue
		}
		if err := m.recreateContainer(sc, role); err != nil {
			return err
		}
		if err := m.waitRoleDependencies(sc, role); err != nil {
			return err
		}
	}
	if err := m.WaitConsole(sc); err != nil {
		return err
	}
	return m.recreateContainer(sc, SafelineRoleTengine)
}

// recreateContainer removes the role's container when it exists and creates it
// again.
func (m *SafelineManager) recreateContainer(sc *config.SafelineConfig, role string) error {
	name := config.SafelineContainerName(*sc, role)
	exists, err := m.containerExists(name)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("rm", "-f", name); err != nil {
			return fmt.Errorf("removing SafeLine %s container: %w", role, err)
		}
	}
	return m.createContainer(sc, role)
}

// StartContainers starts the stack in role order without recreating existing
// containers (start/autostart path). Missing containers are created. The
// bridge roles start first, the console is waited on, and tengine follows so
// it can fetch its site config from a live console. Returns true when every
// container was already running.
func (m *SafelineManager) StartContainers(sc *config.SafelineConfig) (bool, error) {
	if err := m.ensureFiles(sc); err != nil {
		return false, err
	}
	allRunning := true
	for _, role := range safelineRoles() {
		if role == SafelineRoleTengine {
			continue
		}
		wasRunning, err := m.ensureRoleRunning(sc, role)
		if err != nil {
			return false, err
		}
		if !wasRunning {
			allRunning = false
		}
		if err := m.waitRoleDependencies(sc, role); err != nil {
			return false, err
		}
	}
	if !allRunning {
		if err := m.WaitConsole(sc); err != nil {
			return false, err
		}
	}
	wasRunning, err := m.ensureRoleRunning(sc, SafelineRoleTengine)
	if err != nil {
		return false, err
	}
	if !wasRunning {
		allRunning = false
	}
	return allRunning, nil
}

// ensureRoleRunning reports whether the role's container was already running.
// Stopped containers are started; stale container records are recreated.
func (m *SafelineManager) ensureRoleRunning(sc *config.SafelineConfig, role string) (bool, error) {
	name := config.SafelineContainerName(*sc, role)
	running, err := m.containerRunning(name)
	if err != nil {
		return false, err
	}
	if running {
		fmt.Printf("  [OK] SafeLine %s container %s already running\n", role, name)
		return true, nil
	}
	exists, err := m.containerExists(name)
	if err != nil {
		return false, err
	}
	if exists {
		if _, err := m.run("start", name); err == nil {
			// `podman start` exits 0 on a stale record even though
			// nothing started, so re-check process liveness.
			if running, checkErr := m.containerRunning(name); checkErr == nil && running {
				fmt.Printf("  [OK] SafeLine %s container %s started\n", role, name)
				return false, nil
			}
		}
		fmt.Printf("  [!] SafeLine %s container %s in improper state, recreating...\n", role, name)
		if _, err := m.run("rm", "-f", name); err != nil {
			return false, fmt.Errorf("removing improper SafeLine %s container: %w", role, err)
		}
	}
	if err := m.EnsureImages(sc); err != nil {
		return false, err
	}
	if err := m.createContainer(sc, role); err != nil {
		return false, err
	}
	return false, nil
}

// StopContainers stops the stack in reverse order. Containers that are
// already stopped or absent are reported and skipped.
func (m *SafelineManager) StopContainers(sc *config.SafelineConfig) error {
	for _, role := range slices.Backward(safelineRoles()) {
		name := config.SafelineContainerName(*sc, role)
		running, err := m.containerRunning(name)
		if err != nil {
			return err
		}
		if !running {
			fmt.Printf("  [OK] SafeLine %s container %s already stopped\n", role, name)
			continue
		}
		if _, err := m.run("stop", "-t", "30", name); err != nil {
			return fmt.Errorf("stopping SafeLine %s container: %w", role, err)
		}
		fmt.Printf("  [OK] SafeLine %s container %s stopped\n", role, name)
	}
	return nil
}

// RemoveContainers removes the stack in reverse order, keeping data unless
// CleanData is called separately.
func (m *SafelineManager) RemoveContainers(sc *config.SafelineConfig) error {
	for _, role := range slices.Backward(safelineRoles()) {
		name := config.SafelineContainerName(*sc, role)
		exists, err := m.containerExists(name)
		if err != nil {
			return err
		}
		if !exists {
			fmt.Printf("  [OK] SafeLine %s container %s already absent\n", role, name)
			continue
		}
		if _, err := m.run("rm", "-f", name); err != nil {
			return fmt.Errorf("removing SafeLine %s container: %w", role, err)
		}
		fmt.Printf("  [OK] SafeLine %s container %s removed\n", role, name)
	}
	return nil
}

// CleanData deletes the stack's data root (bundled database, console state,
// nginx config, logs).
func (m *SafelineManager) CleanData(sc *config.SafelineConfig) error {
	if err := removeHostDir(m.podman, safelineImageRef(*sc, SafelineRolePostgres), sc.DataDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cleaning %s: %w", sc.DataDir, err)
	}
	fmt.Printf("  [OK] cleaned %s\n", sc.DataDir)
	return nil
}

// ContainerStatus reports whether one role container is running and whether
// it exists at all.
func (m *SafelineManager) ContainerStatus(sc *config.SafelineConfig, role string) (bool, bool, error) {
	name := config.SafelineContainerName(*sc, role)
	exists, err := m.containerExists(name)
	if err != nil {
		return false, false, err
	}
	if !exists {
		return false, false, nil
	}
	running, err := m.containerRunning(name)
	if err != nil {
		return false, true, err
	}
	return running, true, nil
}

// RunningCount reports how many of the seven role containers are running.
func (m *SafelineManager) RunningCount(sc *config.SafelineConfig) (int, error) {
	count := 0
	for _, role := range safelineRoles() {
		running, exists, err := m.ContainerStatus(sc, role)
		if err != nil {
			return 0, err
		}
		if exists && running {
			count++
		}
	}
	return count, nil
}

// waitRoleDependencies waits for postgres to accept connections before the
// remaining roles run. The console is deliberately not waited on here: mgt
// fetches its rule feed from fvm at startup and crash-loops until the later
// containers exist, so callers wait for it once the bridge roles are created.
func (m *SafelineManager) waitRoleDependencies(sc *config.SafelineConfig, role string) error {
	if role == SafelineRolePostgres {
		return m.WaitPostgres(sc)
	}
	return nil
}

// WaitPostgres waits until the bundled PostgreSQL accepts connections.
func (m *SafelineManager) WaitPostgres(sc *config.SafelineConfig) error {
	name := config.SafelineContainerName(*sc, SafelineRolePostgres)
	deadline := time.Now().Add(120 * time.Second)
	var lastOut string
	for {
		out, err := execIdleKilled(m.podman, 30*time.Second, 5*time.Second,
			"exec", name, "pg_isready", "-h", "127.0.0.1", "-U", safelineDBUser, "-d", safelineDBName)
		if err == nil {
			return nil
		}
		lastOut = strings.TrimSpace(out)
		if time.Now().After(deadline) {
			return fmt.Errorf("SafeLine PostgreSQL did not accept connections within 120s (%s); check `pg addon logs safeline --service postgres`", lastOut)
		}
		time.Sleep(2 * time.Second)
	}
}

// ConsoleURL is the console address as printed to users. A wildcard listen is
// shown as localhost.
func ConsoleURL(sc *config.SafelineConfig) string {
	host := sc.Listen
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("https://%s:%d", host, sc.ConsolePort)
}

// safelineProbeHost is the address used to reach the console locally: the
// wildcard bind is probed via loopback, an explicit bind via itself.
func safelineProbeHost(sc *config.SafelineConfig) string {
	if sc.Listen == "" || sc.Listen == "0.0.0.0" || sc.Listen == "::" {
		return "127.0.0.1"
	}
	return sc.Listen
}

// consoleHTTPClient trusts the console's self-signed certificate and never
// uses an environment proxy: the probe talks to a local listener.
func consoleHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy:           nil,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

// WaitConsole waits until the mgt console's health endpoint answers. The
// first boot runs schema migrations and can take a while, so the timeout is
// generous.
func (m *SafelineManager) WaitConsole(sc *config.SafelineConfig) error {
	url := fmt.Sprintf("https://%s:%d/api/open/health", safelineProbeHost(sc), sc.ConsolePort)
	client := consoleHTTPClient()
	deadline := time.Now().Add(180 * time.Second)
	var lastErr error
	for {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("health endpoint returned %s", resp.Status)
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("SafeLine console did not become healthy at %s within 180s (%v); check `pg addon logs safeline --service mgt`", url, lastErr)
		}
		time.Sleep(2 * time.Second)
	}
}

var (
	safelineResetAdminUserRe     = regexp.MustCompile(`(?mi)Initial username\s*[:：]\s*(\S+)`)
	safelineResetAdminPasswordRe = regexp.MustCompile(`(?mi)Initial password\s*[:：]\s*(\S+)`)
	// resetadmin colorizes its banner; the reset sequence follows the password
	// with no whitespace, so it must be stripped or it lands in the secret.
	safelineANSIEscapeRe = regexp.MustCompile("\x1b\\[[0-9;]*m")
)

// ParseSafelineResetAdmin extracts the console credentials from the mgt
// container's `resetadmin` output. Both ASCII and full-width colons are
// accepted because the tool localizes its banner.
func ParseSafelineResetAdmin(output string) (user, password string) {
	output = safelineANSIEscapeRe.ReplaceAllString(output, "")
	if m := safelineResetAdminUserRe.FindStringSubmatch(output); m != nil {
		user = m[1]
	}
	if m := safelineResetAdminPasswordRe.FindStringSubmatch(output); m != nil {
		password = m[1]
	}
	return user, password
}

// ResetAdmin runs the mgt container's resetadmin bootstrap and returns the
// console credentials it printed. The tool rotates the admin password on
// every call, so callers run it only when no password is recorded.
func (m *SafelineManager) ResetAdmin(sc *config.SafelineConfig) (string, string, error) {
	name := config.SafelineContainerName(*sc, SafelineRoleMgt)
	out, err := execIdleKilled(m.podman, 60*time.Second, 5*time.Second, "exec", name, "resetadmin")
	if err != nil {
		return "", "", fmt.Errorf("running resetadmin in %s: %w", name, err)
	}
	user, password := ParseSafelineResetAdmin(out)
	if user == "" {
		user = sc.AdminUser
	}
	if password == "" {
		return user, "", fmt.Errorf("resetadmin printed no password; its output was:\n%s", strings.TrimSpace(out))
	}
	return user, password, nil
}

// ReloadTengine tests and gracefully reloads the nginx config inside the
// tengine container. Site upstreams named by host (e.g. a load-balancer
// domain) are resolved from DNS when that config loads, so a reload is how a
// DNS change on the upstream takes effect without restarting the stack.
func (m *SafelineManager) ReloadTengine(sc *config.SafelineConfig) error {
	name := config.SafelineContainerName(*sc, SafelineRoleTengine)
	out, err := execIdleKilled(m.podman, 30*time.Second, 5*time.Second,
		"exec", name, "sh", "-c", "nginx -t && nginx -s reload")
	if err != nil {
		if out = strings.TrimSpace(out); out != "" {
			return fmt.Errorf("reloading tengine in %s: %w\n%s", name, err, out)
		}
		return fmt.Errorf("reloading tengine in %s: %w", name, err)
	}
	return nil
}

// safelineBundledPasswordFile is where the bundled database password lives.
// PostgreSQL bakes the password into the cluster at first init, so the file
// must outlive the config entry: `pg addon remove` (without --clean-data)
// followed by a re-install reattaches to the surviving cluster only with the
// original password. It sits next to, never inside, the mounted data dir.
func safelineBundledPasswordFile(sc config.SafelineConfig) string {
	return filepath.Join(sc.DataDir, "resources", "postgres", "password")
}

// ReadBundledPassword returns the persisted bundled PostgreSQL password, or ""
// when none has been recorded yet.
func (m *SafelineManager) ReadBundledPassword(sc *config.SafelineConfig) (string, error) {
	b, err := os.ReadFile(safelineBundledPasswordFile(*sc))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading the bundled PostgreSQL password: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// WriteBundledPassword persists the bundled PostgreSQL password for re-installs.
func (m *SafelineManager) WriteBundledPassword(sc *config.SafelineConfig, password string) error {
	path := safelineBundledPasswordFile(*sc)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(password+"\n"), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// ensureFiles creates every bind-mounted directory. SafeLine's images run as
// root inside the container (upstream compose sets no user:), so rootful
// root-owned directories are sufficient.
func (m *SafelineManager) ensureFiles(sc *config.SafelineConfig) error {
	for _, dir := range safelineDirs(*sc) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating SafeLine directory %s: %w", dir, err)
		}
	}
	return nil
}

func (m *SafelineManager) createContainer(sc *config.SafelineConfig, role string) error {
	args, err := safelineCreateArgs(*sc, role)
	if err != nil {
		return err
	}
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating SafeLine %s container: %w", role, err)
	}
	fmt.Printf("  [OK] SafeLine %s container %s created\n", role, config.SafelineContainerName(*sc, role))
	return nil
}

// safelineCreateArgs builds the `podman run` argv for one role. Pure so tests
// can pin mounts, static addresses and the upstream environment contract
// without podman.
func safelineCreateArgs(sc config.SafelineConfig, role string) ([]string, error) {
	if !slices.Contains(safelineRoles(), role) {
		return nil, fmt.Errorf("unknown SafeLine role %q", role)
	}
	resources := func(sub string) string { return filepath.Join(sc.DataDir, "resources", sub) }
	logs := func(sub string) string { return filepath.Join(sc.DataDir, "logs", sub) }

	args := []string{
		"run", "-d",
		"--name", config.SafelineContainerName(sc, role),
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--stop-timeout", "30",
	}
	if role == SafelineRoleTengine {
		args = append(args, "--network", "host")
	} else {
		args = append(args, "--network",
			fmt.Sprintf("%s:ip=%s", sc.NetworkName, config.SafelineStaticIP(sc, safelineStaticHost(role))))
	}
	args = append(args, "-v", "/etc/localtime:/etc/localtime:ro")
	// Upstream drops NET_RAW and forbids privilege escalation on the bridge
	// containers; tengine (host network) only keeps the latter and adds it in
	// its own branch. Everything here must come BEFORE the image reference:
	// podman treats whatever follows the image as the container command.
	if role != SafelineRoleTengine {
		args = append(args,
			"--cap-drop", "NET_RAW",
			"--security-opt", "no-new-privileges:true",
		)
		// The compose services address each other by service name (mgt, for
		// one, panics unless http://safeline-fvm/skynetinfo resolves). Network
		// DNS is not guaranteed on target hosts, so pin every service name to
		// its static address.
		for _, r := range []string{
			SafelineRolePostgres, SafelineRoleMgt, SafelineRoleDetector,
			SafelineRoleLuigi, SafelineRoleFvm, SafelineRoleChaos,
		} {
			args = append(args, "--add-host",
				"safeline-"+r+":"+config.SafelineStaticIP(sc, safelineStaticHost(r)))
		}
	}

	switch role {
	case SafelineRolePostgres:
		args = append(args,
			"-e", "POSTGRES_USER="+safelineDBUser,
			"-e", "POSTGRES_PASSWORD="+sc.PostgresPassword,
			"-v", fmt.Sprintf("%s:/var/lib/postgresql/data:z", resources("postgres/data")),
			safelineImageRef(sc, role),
			"postgres", "-c", "max_connections=600",
		)
	case SafelineRoleMgt:
		args = append(args,
			"-e", "MGT_PG="+safelineDSN(sc),
			"-e", "MGT_PROXY=0",
			"-e", "SUBNET_PREFIX="+sc.SubnetPrefix,
			"-p", fmt.Sprintf("%s:%d:%d", sc.Listen, sc.ConsolePort, config.DefaultSafelineConsoleContainerPort),
			"-v", fmt.Sprintf("%s:/app/data:z", resources("mgt")),
			"-v", fmt.Sprintf("%s:/app/log/nginx:z", logs("nginx")),
			"-v", fmt.Sprintf("%s:/app/sock", resources("sock")),
			"-v", fmt.Sprintf("%s:/app/run", resources("run")),
			safelineImageRef(sc, role),
		)
	case SafelineRoleDetector:
		args = append(args,
			"-e", "LOG_DIR=/logs/detector",
			"-e", "DETECTOR_MGMT_AUTH=on",
			"-e", "DETECTOR_UPDATE_VERIFY=on",
			"-v", fmt.Sprintf("%s:/resources/detector:z", resources("detector")),
			"-v", fmt.Sprintf("%s:/logs/detector:z", logs("detector")),
			safelineImageRef(sc, role),
		)
	case SafelineRoleTengine:
		args = append(args,
			"-e", "TCD_MGT_API=https://"+config.SafelineStaticIP(sc, safelineMgtHost)+":1443/api/open/publish/server",
			"-e", "SUBNET_PREFIX="+sc.SubnetPrefix,
			"-e", "TCD_SNSERVER="+config.SafelineStaticIP(sc, safelineDetectorHost)+":8000",
			"-e", "SNSERVER_ADDR="+config.SafelineStaticIP(sc, safelineDetectorHost)+":8000",
			"-e", "CHAOS_ADDR="+config.SafelineStaticIP(sc, safelineChaosHost),
			"-v", "/etc/resolv.conf:/etc/resolv.conf:ro",
			"-v", "/dev/null:/etc/ld.so.preload:ro",
			"-v", fmt.Sprintf("%s:/etc/nginx:z", resources("nginx")),
			"-v", fmt.Sprintf("%s:/resources/detector:ro", resources("detector")),
			"-v", fmt.Sprintf("%s:/resources/chaos:z", resources("chaos")),
			"-v", fmt.Sprintf("%s:/var/log/nginx:z", logs("nginx")),
			"-v", fmt.Sprintf("%s:/usr/local/nginx/cache:z", resources("cache")),
			"-v", fmt.Sprintf("%s:/app/sock", resources("sock")),
			"--ulimit", "nofile=131072:131072",
			"--security-opt", "no-new-privileges:true",
			safelineImageRef(sc, role),
		)
	case SafelineRoleLuigi:
		args = append(args,
			"-e", "MGT_IP="+config.SafelineStaticIP(sc, safelineMgtHost),
			"-e", "LUIGI_PG="+safelineDSN(sc),
			"-e", "ENTITLEMENT_CONSUMER_VERIFY=off",
			"-v", fmt.Sprintf("%s:/app/data:z", resources("luigi")),
			safelineImageRef(sc, role),
		)
	case SafelineRoleFvm:
		args = append(args, safelineImageRef(sc, role))
	case SafelineRoleChaos:
		args = append(args,
			"-e", "DB_ADDR="+safelineDSN(sc),
			"-e", "ENTITLEMENT_CONSUMER_VERIFY=off",
			"-v", fmt.Sprintf("%s:/app/sock", resources("sock")),
			"-v", fmt.Sprintf("%s:/app/chaos:z", resources("chaos")),
			safelineImageRef(sc, role),
		)
	}
	return args, nil
}

// --- internal helpers ---------------------------------------------------

func (m *SafelineManager) run(args ...string) (string, error) {
	cmd := podmanCommand(m.podman, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (m *SafelineManager) runInteractive(args ...string) error {
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (m *SafelineManager) imageExists(tag string) (bool, error) {
	out, err := m.run("images", "--format", "{{.Repository}}:{{.Tag}}")
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == tag {
			return true, nil
		}
	}
	return false, nil
}

func (m *SafelineManager) containerExists(name string) (bool, error) {
	out, err := m.run("ps", "-a", "--format", "{{.Names}}")
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

func (m *SafelineManager) containerRunning(name string) (bool, error) {
	out, err := m.run("ps", "--format", "{{.Names}}")
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == name {
			return containerProcessAlive(m.podman, name), nil
		}
	}
	return false, nil
}

func (m *SafelineManager) networkExists(name string) (bool, error) {
	out, err := m.run("network", "ls", "--format", "{{.Name}}")
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

func (m *SafelineManager) networkSubnet(name string) (string, error) {
	out, err := m.run("network", "inspect", "--format", "{{range .Subnets}}{{.Subnet}} {{end}}", name)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

func (m *SafelineManager) networkLabels(name string) (string, error) {
	out, err := m.run("network", "inspect", "--format", "{{index .Labels}}", name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
