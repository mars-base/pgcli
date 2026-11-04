package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/certgen"
	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/httpd"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/tlsca"
)

const (
	httpDefaultPort   = 18080
	httpDefaultListen = "127.0.0.1"
	httpPortTries     = 100
	httpReadyTimeout  = 3 * time.Second
	httpStopGrace     = 5 * time.Second
)

func init() {
	rootCmd.AddCommand(httpCmd)
	httpCmd.AddCommand(httpStartCmd)
	httpCmd.AddCommand(httpStopCmd)
	httpCmd.AddCommand(httpRestartCmd)
	httpCmd.AddCommand(httpStatusCmd)
	httpCmd.AddCommand(httpServeCmd)

	httpStartCmd.Flags().String("name", "", "server name used by status/stop (default \"http-<requested-port>\"; a running server with the same name gets a -2/-3 suffix)")
	httpStartCmd.Flags().Int("port", httpDefaultPort, "listen port; if it is taken, the next free port is tried automatically (up to 100)")
	httpStartCmd.Flags().String("listen", httpDefaultListen, "listen address (default 127.0.0.1; use 0.0.0.0 to share on the network)")
	httpStartCmd.Flags().String("dir", ".", "directory to serve (made absolute at start)")
	httpStartCmd.Flags().Bool("tls", false, "serve HTTPS with a generated self-signed certificate under <base_dir>/http/tls/<name>/")
	httpStartCmd.Flags().String("tls-cert", "", "use this certificate file instead of generating one (implies --tls)")
	httpStartCmd.Flags().String("tls-key", "", "private key for --tls-cert")

	httpStopCmd.Flags().Bool("all", false, "stop every pg http server")
	httpStopCmd.Flags().Duration("timeout", httpStopGrace, "how long to wait for a graceful shutdown before SIGKILL")

	httpRestartCmd.Flags().Bool("all", false, "restart every pg http server")
	httpRestartCmd.Flags().Duration("timeout", httpStopGrace, "how long to wait for a graceful shutdown before SIGKILL")

	httpServeCmd.Flags().String("name", "", "server name (for status/stop bookkeeping)")
	httpServeCmd.Flags().Int("port", httpDefaultPort, "listen port")
	httpServeCmd.Flags().String("listen", httpDefaultListen, "listen address")
	httpServeCmd.Flags().String("dir", ".", "directory to serve")
	httpServeCmd.Flags().String("tls-cert", "", "certificate file")
	httpServeCmd.Flags().String("tls-key", "", "private key file")
	httpServeCmd.Flags().String("ready-file", "", "write listen/port JSON here once the socket is bound, then delete on exit")
	httpServeCmd.Hidden = true
}

var httpCmd = &cobra.Command{
	Use:   "http",
	Short: "Serve a directory over HTTP(S) for quick file sharing",
	Long: `http runs a small static file server on the host (no container). Directories
render an nginx-style listing with a copy button for each file; files are served
with Range/HEAD support.

Servers are detached processes tracked under <base_dir>/http/servers/, so they
survive the terminal that started them. They do not start at boot — after a
reboot, run ` + "`pg http start`" + ` again.

The default is 127.0.0.1:18080 serving the current directory. If the requested
port is busy the next free one is used automatically (up to 100 attempts), and
multiple servers may run at once. There is no authentication: keep the default
loopback listener, or expose it only on a trusted network. --tls encrypts
traffic but does not authenticate clients.

Examples:
  pg http start
  pg http start --listen 0.0.0.0 --port 18080 --dir /srv/share
  pg http start --name docs --tls --dir ~/docs
  pg http status
  pg http restart docs
  pg http stop docs
  pg http stop --all`,
}

var httpStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start a detached HTTP static file server",
	Args:  cobra.NoArgs,
	RunE:  runHTTPStart,
}

var httpStopCmd = &cobra.Command{
	Use:   "stop [NAME...]",
	Short: "Stop one or more pg http servers",
	RunE:  runHTTPStop,
}

var httpRestartCmd = &cobra.Command{
	Use:   "restart [NAME...]",
	Short: "Restart one or more pg http servers with their recorded settings",
	Long: `restart stops each server gracefully, then starts it again with the listen
address, directory, TLS certificate and port recorded in its state file.

If the original port was taken in the meantime, the next free port is used.
Generated certificates are renewed when they are close to expiry or no longer
cover the listen address.`,
	RunE: runHTTPRestart,
}

var httpStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "List pg http servers and whether they are running",
	Args:  cobra.NoArgs,
	RunE:  runHTTPStatus,
}

var httpServeCmd = &cobra.Command{
	Use:    "serve",
	Short:  "Run the HTTP server in the foreground (internal)",
	Args:   cobra.NoArgs,
	RunE:   runHTTPServe,
	Hidden: true,
}

type httpServerState struct {
	Name      string    `json:"name"`
	PID       int       `json:"pid"`
	Port      int       `json:"port"`
	Listen    string    `json:"listen"`
	Dir       string    `json:"dir"`
	TLS       bool      `json:"tls"`
	CertFile  string    `json:"cert_file,omitempty"`
	KeyFile   string    `json:"key_file,omitempty"`
	LogFile   string    `json:"log_file"`
	StartedAt time.Time `json:"started_at"`
}

type httpReady struct {
	Listen string `json:"listen"`
	Port   int    `json:"port"`
}

// httpBaseDir is the same base as the rest of pgcli: base_dir from pg.yaml
// when it exists, the default config dir otherwise. pg http works without a
// config file.
func httpBaseDir() string {
	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if c, err := config.Load(path); err == nil && c.BaseDir != "" {
		return c.BaseDir
	}
	return platform.DefaultConfigDir()
}

func httpServersDir(base string) string {
	return filepath.Join(base, "http", "servers")
}

func validHTTPName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

func writeHTTPState(serversDir string, st httpServerState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	final := filepath.Join(serversDir, st.Name+".json")
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

func readHTTPState(path string) (*httpServerState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st httpServerState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &st, nil
}

// loadHTTPStates returns every state file, sorted by name. Broken files are
// skipped rather than failing the whole listing.
func loadHTTPStates(serversDir string) []httpServerState {
	entries, err := os.ReadDir(serversDir)
	if err != nil {
		return nil
	}
	var out []httpServerState
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		st, err := readHTTPState(filepath.Join(serversDir, e.Name()))
		if err != nil || st.Name == "" || st.PID == 0 {
			continue
		}
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// httpServerAlive reports whether the recorded process still exists. On Linux
// the cmdline must still contain "http", "serve" and the server name, so a
// recycled pid running something else is treated as stale.
func httpServerAlive(st *httpServerState) bool {
	p, err := os.FindProcess(st.PID)
	if err != nil {
		return false
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", st.PID))
	if err != nil {
		return true
	}
	cmdline := strings.ReplaceAll(string(data), "\x00", " ")
	return strings.Contains(cmdline, "http") &&
		strings.Contains(cmdline, "serve") &&
		strings.Contains(cmdline, st.Name)
}

func uniqueHTTPName(base string, states []httpServerState) string {
	live := map[string]bool{}
	for _, st := range states {
		if httpServerAlive(&st) {
			live[st.Name] = true
		}
	}
	if !live[base] {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !live[candidate] {
			return candidate
		}
	}
}

func httpURL(st *httpServerState) string {
	scheme := "http"
	if st.TLS {
		scheme = "https"
	}
	host := st.Listen
	if host == "" {
		host = httpDefaultListen
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(st.Port)) + "/"
}

func runHTTPStart(cmd *cobra.Command, _ []string) error {
	port, _ := cmd.Flags().GetInt("port")
	if port <= 0 {
		port = httpDefaultPort
	}
	listen, _ := cmd.Flags().GetString("listen")
	if listen == "" {
		listen = httpDefaultListen
	}
	dir, _ := cmd.Flags().GetString("dir")
	name, _ := cmd.Flags().GetString("name")
	tlsEnabled, _ := cmd.Flags().GetBool("tls")
	certFile, _ := cmd.Flags().GetString("tls-cert")
	keyFile, _ := cmd.Flags().GetString("tls-key")

	if (certFile == "") != (keyFile == "") {
		return fmt.Errorf("--tls-cert and --tls-key must be set together")
	}
	if certFile != "" {
		tlsEnabled = true
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolving --dir: %w", err)
	}
	info, err := os.Stat(absDir)
	if err != nil {
		return fmt.Errorf("--dir %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--dir %s is not a directory", dir)
	}

	base := httpBaseDir()
	serversDir := httpServersDir(base)
	if err := os.MkdirAll(serversDir, 0755); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}

	states := loadHTTPStates(serversDir)
	if name == "" {
		name = fmt.Sprintf("http-%d", port)
	}
	if !validHTTPName(name) {
		return fmt.Errorf("invalid --name %q: use letters, digits, '.', '_' or '-'", name)
	}
	name = uniqueHTTPName(name, states)

	var certPath, keyPath string
	var certNotAfter time.Time
	if tlsEnabled {
		if certFile != "" {
			certPath, keyPath = certFile, keyFile
			certNotAfter, err = validateHTTPCertPair(certPath, keyPath)
			if err != nil {
				return err
			}
		} else {
			tlsDir := filepath.Join(base, "http", "tls", name)
			certPath, keyPath, certNotAfter, err = ensureHTTPCert(tlsDir, httpCertHosts(listen))
			if err != nil {
				return fmt.Errorf("preparing TLS certificate: %w", err)
			}
		}
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating the pg executable: %w", err)
	}
	logPath := filepath.Join(serversDir, name+".log")
	readyFile := filepath.Join(serversDir, name+".ready")

	startPort := port
	for p := startPort; p < startPort+httpPortTries; p++ {
		st, err := launchHTTPServer(self, name, listen, p, absDir, certPath, keyPath, tlsEnabled, logPath, readyFile)
		if err != nil {
			return err
		}
		if st == nil {
			continue // port busy; try the next one
		}
		if st.Port != startPort {
			fmt.Printf("  [!] port %d is in use, using %d\n", startPort, st.Port)
		}
		if err := writeHTTPState(serversDir, *st); err != nil {
			_ = syscall.Kill(st.PID, syscall.SIGTERM)
			return fmt.Errorf("writing state file: %w", err)
		}
		printHTTPStarted(st, certNotAfter)
		return nil
	}
	return fmt.Errorf("no free port in %d..%d", startPort, startPort+httpPortTries-1)
}

// launchHTTPServer forks `pg http serve` for one port. A nil state with a nil
// error means the child exited before binding (port taken); the caller tries
// the next port.
func launchHTTPServer(self, name, listen string, port int, dir, certPath, keyPath string, tlsEnabled bool, logPath, readyFile string) (*httpServerState, error) {
	_ = os.Remove(readyFile)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening log %s: %w", logPath, err)
	}
	defer logFile.Close()

	args := []string{
		"http", "serve",
		"--name", name,
		"--listen", listen,
		"--port", strconv.Itoa(port),
		"--dir", dir,
		"--ready-file", readyFile,
	}
	if tlsEnabled {
		args = append(args, "--tls-cert", certPath, "--tls-key", keyPath)
	}

	child := exec.Command(self, args...)
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	child.Stdout = logFile
	child.Stderr = logFile
	child.Env = append(os.Environ(), "NO_COLOR=1")
	if err := child.Start(); err != nil {
		return nil, fmt.Errorf("starting http server: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()

	deadline := time.NewTimer(httpReadyTimeout)
	defer deadline.Stop()
	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()

	for {
		select {
		case <-exited:
			_ = os.Remove(readyFile)
			return nil, nil
		case <-deadline.C:
			_ = child.Process.Kill()
			<-exited
			_ = os.Remove(readyFile)
			return nil, fmt.Errorf("http server did not become ready within %s (see %s)", httpReadyTimeout, logPath)
		case <-poll.C:
			data, err := os.ReadFile(readyFile)
			if err != nil || len(data) == 0 {
				continue
			}
			var ready httpReady
			if err := json.Unmarshal(data, &ready); err != nil || ready.Port == 0 {
				continue
			}
			_ = os.Remove(readyFile)
			return &httpServerState{
				Name:      name,
				PID:       child.Process.Pid,
				Port:      ready.Port,
				Listen:    ready.Listen,
				Dir:       dir,
				TLS:       tlsEnabled,
				CertFile:  certPath,
				KeyFile:   keyPath,
				LogFile:   logPath,
				StartedAt: time.Now(),
			}, nil
		}
	}
}

func printHTTPStarted(st *httpServerState, certNotAfter time.Time) {
	fmt.Println()
	fmt.Printf("✓ HTTP server started: %s\n", st.Name)
	fmt.Printf("  URL:  %s\n", httpURL(st))
	fmt.Printf("  Dir:  %s\n", st.Dir)
	fmt.Printf("  PID:  %d\n", st.PID)
	fmt.Printf("  Log:  %s\n", st.LogFile)
	if st.TLS {
		if certNotAfter.IsZero() {
			certNotAfter = certExpiry(st.CertFile)
		}
		fmt.Printf("  TLS:  cert %s (expires %s)\n", st.CertFile, certNotAfter.Format("2006-01-02"))
	}
	fmt.Println()
	fmt.Printf("  Stop with: pg http stop %s\n", st.Name)
}

func certExpiry(path string) time.Time {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}
	}
	blk, _ := pem.Decode(data)
	if blk == nil {
		return time.Time{}
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return time.Time{}
	}
	return cert.NotAfter
}

func runHTTPStop(cmd *cobra.Command, args []string) error {
	all, _ := cmd.Flags().GetBool("all")
	grace, _ := cmd.Flags().GetDuration("timeout")
	if grace <= 0 {
		grace = httpStopGrace
	}
	base := httpBaseDir()
	serversDir := httpServersDir(base)
	states := loadHTTPStates(serversDir)
	if len(states) == 0 {
		fmt.Println("No pg http servers configured.")
		return nil
	}

	for _, st := range selectHTTPTargets(states, args, all) {
		if err := stopHTTPServer(serversDir, &st, grace); err != nil {
			return err
		}
	}
	return nil
}

// selectHTTPTargets resolves names against the recorded servers; no names (or
// --all) selects every server, mirroring stop.
func selectHTTPTargets(states []httpServerState, names []string, all bool) []httpServerState {
	byName := make(map[string]httpServerState, len(states))
	for _, st := range states {
		byName[st.Name] = st
	}
	if all || len(names) == 0 {
		return states
	}
	var targets []httpServerState
	for _, n := range names {
		st, ok := byName[n]
		if !ok {
			fmt.Printf("  [!] no pg http server named %q\n", n)
			continue
		}
		targets = append(targets, st)
	}
	return targets
}

func stopHTTPServer(serversDir string, st *httpServerState, grace time.Duration) error {
	statePath := filepath.Join(serversDir, st.Name+".json")
	readyPath := filepath.Join(serversDir, st.Name+".ready")
	if !httpServerAlive(st) {
		os.Remove(statePath)
		os.Remove(readyPath)
		fmt.Printf("  [!] %s (pid %d) was not running — cleaned up its state\n", st.Name, st.PID)
		return nil
	}
	fmt.Printf("-> Stopping %s (pid %d)...\n", st.Name, st.PID)
	if err := syscall.Kill(st.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stopping %s: %w", st.Name, err)
	}
	deadline := time.Now().Add(grace)
	for httpServerAlive(st) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if httpServerAlive(st) {
		_ = syscall.Kill(st.PID, syscall.SIGKILL)
		time.Sleep(100 * time.Millisecond)
	}
	os.Remove(statePath)
	os.Remove(readyPath)
	fmt.Printf("✓ %s stopped\n", st.Name)
	return nil
}

func runHTTPRestart(cmd *cobra.Command, args []string) error {
	all, _ := cmd.Flags().GetBool("all")
	grace, _ := cmd.Flags().GetDuration("timeout")
	if grace <= 0 {
		grace = httpStopGrace
	}
	base := httpBaseDir()
	serversDir := httpServersDir(base)
	states := loadHTTPStates(serversDir)
	if len(states) == 0 {
		fmt.Println("No pg http servers configured.")
		return nil
	}

	for _, st := range selectHTTPTargets(states, args, all) {
		if err := stopHTTPServer(serversDir, &st, grace); err != nil {
			return err
		}
		newSt, certNotAfter, err := relaunchHTTPServer(base, st)
		if err != nil {
			return err
		}
		printHTTPStarted(newSt, certNotAfter)
	}
	return nil
}

// relaunchHTTPServer starts a stopped server from its recorded state. TLS
// material is re-validated (generated certificates are renewed when needed)
// before the child is launched.
func relaunchHTTPServer(base string, st httpServerState) (*httpServerState, time.Time, error) {
	info, err := os.Stat(st.Dir)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("--dir %s: %w", st.Dir, err)
	}
	if !info.IsDir() {
		return nil, time.Time{}, fmt.Errorf("--dir %s is not a directory", st.Dir)
	}
	listen := st.Listen
	if listen == "" {
		listen = httpDefaultListen
	}
	port := st.Port
	if port <= 0 {
		port = httpDefaultPort
	}

	serversDir := httpServersDir(base)
	certPath, keyPath := st.CertFile, st.KeyFile
	var certNotAfter time.Time
	if st.TLS {
		genDir := filepath.Join(base, "http", "tls", st.Name)
		if certPath == "" || filepath.Clean(filepath.Dir(certPath)) == filepath.Clean(genDir) {
			certPath, keyPath, certNotAfter, err = ensureHTTPCert(genDir, httpCertHosts(listen))
			if err != nil {
				return nil, time.Time{}, fmt.Errorf("preparing TLS certificate: %w", err)
			}
		} else {
			certNotAfter, err = validateHTTPCertPair(certPath, keyPath)
			if err != nil {
				return nil, time.Time{}, err
			}
		}
	}

	self, err := os.Executable()
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("locating the pg executable: %w", err)
	}
	logPath := filepath.Join(serversDir, st.Name+".log")
	readyFile := filepath.Join(serversDir, st.Name+".ready")
	for p := port; p < port+httpPortTries; p++ {
		newSt, err := launchHTTPServer(self, st.Name, listen, p, st.Dir, certPath, keyPath, st.TLS, logPath, readyFile)
		if err != nil {
			return nil, time.Time{}, err
		}
		if newSt == nil {
			continue // port busy; try the next one
		}
		if newSt.Port != port {
			fmt.Printf("  [!] port %d is in use, using %d\n", port, newSt.Port)
		}
		if err := writeHTTPState(serversDir, *newSt); err != nil {
			_ = syscall.Kill(newSt.PID, syscall.SIGTERM)
			return nil, time.Time{}, fmt.Errorf("writing state file: %w", err)
		}
		return newSt, certNotAfter, nil
	}
	return nil, time.Time{}, fmt.Errorf("no free port in %d..%d", port, port+httpPortTries-1)
}

func runHTTPStatus(cmd *cobra.Command, _ []string) error {
	base := httpBaseDir()
	states := loadHTTPStates(httpServersDir(base))
	if len(states) == 0 {
		fmt.Println("No pg http servers configured.")
		return nil
	}
	fmt.Printf("%-24s %-8s %-9s %-40s %s\n", "NAME", "PID", "STATUS", "URL", "DIR")
	for _, st := range states {
		status := "running"
		if !httpServerAlive(&st) {
			status = "stale"
		}
		fmt.Printf("%-24s %-8d %-9s %-40s %s\n", st.Name, st.PID, status, httpURL(&st), st.Dir)
	}
	return nil
}

// runHTTPServe is the detached child. It binds the socket, writes the ready
// file (parent then records the pid/port), and serves until SIGTERM/SIGINT.
func runHTTPServe(cmd *cobra.Command, _ []string) error {
	port, _ := cmd.Flags().GetInt("port")
	listen, _ := cmd.Flags().GetString("listen")
	dir, _ := cmd.Flags().GetString("dir")
	certFile, _ := cmd.Flags().GetString("tls-cert")
	keyFile, _ := cmd.Flags().GetString("tls-key")
	readyFile, _ := cmd.Flags().GetString("ready-file")
	if (certFile == "") != (keyFile == "") {
		return fmt.Errorf("--tls-cert and --tls-key must be set together")
	}

	ln, err := net.Listen("tcp", net.JoinHostPort(listen, strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("listening on %s:%d: %w", listen, port, err)
	}
	actual := ln.Addr().(*net.TCPAddr)

	if readyFile != "" {
		data, _ := json.Marshal(httpReady{Listen: listen, Port: actual.Port})
		tmp := readyFile + ".tmp"
		if err := os.WriteFile(tmp, data, 0644); err != nil {
			ln.Close()
			return fmt.Errorf("writing ready file: %w", err)
		}
		if err := os.Rename(tmp, readyFile); err != nil {
			ln.Close()
			return fmt.Errorf("writing ready file: %w", err)
		}
	}

	srv := &http.Server{
		Handler:           httpd.Handler(dir),
		ReadHeaderTimeout: 10 * time.Second,
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		ctx, cancel := context.WithTimeout(context.Background(), httpStopGrace)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	if certFile != "" {
		err = srv.ServeTLS(ln, certFile, keyFile)
	} else {
		err = srv.Serve(ln)
	}
	if readyFile != "" {
		os.Remove(readyFile)
	}
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// httpCertHosts picks the SANs for a generated certificate: every local
// address when listening on a wildcard, otherwise the loopback names plus the
// specific listen address.
func httpCertHosts(listen string) []string {
	switch listen {
	case "", "0.0.0.0", "::", "[::]":
		return tlsca.LocalHosts()
	default:
		return tlsca.LocalHosts(listen)
	}
}

// ensureHTTPCert reuses dir's pair while it is valid for more than 30 days and
// covers every host, regenerating otherwise (pg cert itself is one-shot; a
// long-lived server needs the reuse check).
func ensureHTTPCert(dir string, hosts []string) (string, string, time.Time, error) {
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if cert := loadHTTPLeaf(certPath); cert != nil {
		if time.Until(cert.NotAfter) > 30*24*time.Hour && hostsCovered(cert, hosts) {
			return certPath, keyPath, cert.NotAfter, nil
		}
	}
	res, err := certgen.Generate(certgen.Options{Hosts: hosts})
	if err != nil {
		return "", "", time.Time{}, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", "", time.Time{}, err
	}
	if err := os.WriteFile(certPath, res.CertPEM, 0644); err != nil {
		return "", "", time.Time{}, err
	}
	if err := os.WriteFile(keyPath, res.KeyPEM, 0600); err != nil {
		return "", "", time.Time{}, err
	}
	return certPath, keyPath, res.NotAfter, nil
}

func loadHTTPLeaf(certPath string) *x509.Certificate {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return nil
	}
	blk, _ := pem.Decode(data)
	if blk == nil {
		return nil
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil
	}
	return cert
}

func hostsCovered(cert *x509.Certificate, hosts []string) bool {
	for _, h := range hosts {
		if err := cert.VerifyHostname(h); err != nil {
			return false
		}
	}
	return true
}

func validateHTTPCertPair(certPath, keyPath string) (time.Time, error) {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return time.Time{}, fmt.Errorf("cert/key pair unusable: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing certificate: %w", err)
	}
	now := time.Now()
	if now.After(leaf.NotAfter) {
		return time.Time{}, fmt.Errorf("certificate expired %s", leaf.NotAfter.Format(time.RFC3339))
	}
	if now.Before(leaf.NotBefore) {
		return time.Time{}, fmt.Errorf("certificate not yet valid (starts %s)", leaf.NotBefore.Format(time.RFC3339))
	}
	return leaf.NotAfter, nil
}
