package podman

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// mcContainerConfigDir is where the mc image keeps its config: the image pins
// HOME=/data (embed/mc.Containerfile), and mc resolves its config as
// $HOME/.mc, so the host ~/.mc directory is mounted there — one shared
// config.json on Linux and macOS, the same default path as native mc.
const mcContainerConfigDir = "/data/.mc"

// MCRunner runs the MinIO client from the pre-built pgcli-mc image in a
// short-lived container, passing arguments straight to mc (the image's
// ENTRYPOINT). mc's alias configuration persists on the host at ~/.mc — the
// native default — via a bind mount, so `mc alias set` once and every later
// `pg mc` invocation (and a native mc on Linux) sees the same aliases.
type MCRunner struct {
	podman     string // podman binary path
	imageTag   string
	configDir  string // host ~/.mc
	useHostNet bool   // Linux: --network host reaches a loopback-bound MinIO; macOS: host networking binds the VM loopback, so use the default bridge
}

// NewMCRunner creates an MCRunner. Unlike the minio addon manager it works on
// macOS: mc is a client, so the bridge network's outbound connectivity is all
// a remote endpoint needs (see the platform note in the command help).
func NewMCRunner() (*MCRunner, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home directory: %w", err)
	}
	ensurePodmanStateReady(path)
	return &MCRunner{
		podman:     path,
		imageTag:   config.DefaultMCImageTag,
		configDir:  filepath.Join(home, ".mc"),
		useHostNet: platform.Detect() != platform.MacOS,
	}, nil
}

// Run executes mc in a temporary container with os.Args passthrough. Exit
// status is propagated: a failing mc command returns an error.
func (m *MCRunner) Run(args []string) error {
	// Create ~/.mc before mounting, or podman creates the mountpoint root-owned.
	if err := os.MkdirAll(m.configDir, 0700); err != nil {
		return fmt.Errorf("creating mc config dir %s: %w", m.configDir, err)
	}
	if err := m.EnsureImage(); err != nil {
		return err
	}

	envs := forwardMCHostEnv()
	aliases := mcKnownAliases(m.configDir, envs)

	// The container cannot see the host filesystem except through mounts, so a
	// local-path operand of `cp` / `mirror` / `diff` would fail with
	// "Requested path not found". Resolve each such operand to its absolute
	// path and mount that exact path (realpath semantics: same path inside the
	// container), rewriting the argument to match. The whole home directory is
	// not an option — on macOS its .Trash is TCC-protected, and mounting $HOME
	// fails outright.
	args, mounts := mcLocalFiles(args, aliases)

	// macOS: the podman machine shares only the home tree over virtiofs, so a
	// mount outside it fails with an opaque error — drop it with a clear note.
	if !m.useHostNet && len(mounts) > 0 {
		home, err := os.UserHomeDir()
		if err != nil {
			slog.Debug("mc: home for mount filter unavailable", "err", err)
		} else {
			kept := mounts[:0]
			for _, dir := range mounts {
				if mcWithin(home, dir) {
					kept = append(kept, dir)
					continue
				}
				fmt.Fprintf(os.Stderr, "[!] pg mc cannot publish %s on macOS: the podman machine shares only %s — keep local files under your home directory\n", dir, home)
			}
			mounts = kept
		}
	}

	runArgs := mcRunArgs(m.imageTag, m.configDir, mounts, m.useHostNet, isTerminal(os.Stdin), envs, args)
	slog.Debug("podman mc", "args", runArgs)
	cmd := podmanCommand(m.podman, runArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running mc: %w", err)
	}
	return nil
}

// EnsureImage pulls the mc image if it is not present locally (pull-only:
// the tag is published to the public ghcr repo; pgcli never builds it).
func (m *MCRunner) EnsureImage() error {
	exists, err := m.imageExists(m.imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", m.imageTag)
	if err := m.runInteractive("pull", m.imageTag); err != nil {
		return fmt.Errorf("pulling mc image %s (is ghcr.io/mars-base/pgcli reachable?): %w", m.imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// forwardMCHostEnv collects the stateless-alias variables (MC_HOST_<name>,
// mc's native env-based aliases) from the caller's environment so they keep
// working through `pg mc`. MC_CONFIG_DIR is deliberately not forwarded: it is
// a host path, and the container has its own HOME pinned to /data.
func forwardMCHostEnv() []string {
	var envs []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "MC_HOST_") {
			envs = append(envs, kv)
		}
	}
	return envs
}

// mcFileCommands are the mc subcommands that take host filesystem operands
// (every other subcommand's arguments are alias/bucket URLs only, or flags).
var mcFileCommands = map[string]bool{"cp": true, "mirror": true, "diff": true}

// mcKnownAliases returns the set of names mc already treats as remote — the
// ones stored in ~/.mc/config.json plus MC_HOST_* env aliases — so an operand
// like "store/backups" is left alone while "./LICENSE" is a local path.
func mcKnownAliases(configDir string, envs []string) map[string]bool {
	names := map[string]bool{}

	if data, err := os.ReadFile(filepath.Join(configDir, "config.json")); err == nil {
		var cfg struct {
			Aliases []struct {
				Name string `json:"name"`
			} `json:"aliases"`
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			slog.Debug("mc: parsing config.json for alias detection, treating all bare names as local", "err", err)
		}
		for _, a := range cfg.Aliases {
			if a.Name != "" {
				names[a.Name] = true
			}
		}
	}

	for _, kv := range envs {
		if name := strings.TrimPrefix(kv[:strings.Index(kv, "=")], "MC_HOST_"); name != "" {
			names[name] = true
		}
	}
	return names
}

// mcLocalFiles rewrites the host-path operands of a file-taking mc subcommand
// (cp/mirror/diff) to absolute paths and returns the mount sources needed to
// make them visible in the container. A token is a host path when it is not a
// URL (no scheme) and its first segment is not a known mc alias — the same
// test mc itself applies to decide local vs remote.
func mcLocalFiles(args []string, aliases map[string]bool) (newArgs, mounts []string) {
	seen := map[string]bool{}
	addMount := func(dir string) {
		if seen[dir] {
			return
		}
		seen[dir] = true
		mounts = append(mounts, dir)
	}

	inFileCmd := false
	newArgs = make([]string, 0, len(args))
	for _, arg := range args {
		switch {
		case !inFileCmd && !strings.HasPrefix(arg, "-") && mcFileCommands[arg]:
			inFileCmd = true
			newArgs = append(newArgs, arg)
		case inFileCmd && !isMCLocalOperand(arg, aliases):
			newArgs = append(newArgs, arg)
		case inFileCmd:
			abs := hostMountPath(mcExpandHome(arg))
			mount, rewritten := mcOperandMount(abs)
			addMount(mount)
			newArgs = append(newArgs, rewritten)
		default:
			newArgs = append(newArgs, arg)
		}
	}
	return newArgs, mounts
}

// isMCLocalOperand reports whether an operand of a file-taking mc subcommand
// names a host path rather than an alias/bucket URL.
func isMCLocalOperand(arg string, aliases map[string]bool) bool {
	if arg == "" || strings.HasPrefix(arg, "-") {
		return false
	}
	if strings.HasPrefix(arg, "~") || strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../") {
		return true
	}
	if i := strings.Index(arg, "://"); i >= 0 && i <= 8 {
		return false // s3://, https://, file:// (explicit URL form mc also accepts)
	}
	first := arg
	if j := strings.Index(arg, "/"); j >= 0 {
		first = arg[:j]
	}
	return !aliases[first]
}

// mcExpandHome resolves a leading ~ (the shell does this for unquoted paths;
// a quoted "~/file" reaches us verbatim, and mc itself understands it, but the
// bind mount needs a real host path).
func mcExpandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		slog.Debug("mc: home for ~ expansion unavailable", "err", err)
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

// mcOperandMount resolves one local-path operand to (mountSource, arg) with
// realpath semantics: a file that exists is mounted at its own absolute path;
// a path that does not exist yet (a download target — mc has not written it
// yet, or cp'ing to a fresh filename) mounts its nearest existing ancestor
// directory instead, so the file mc creates lands back on the host too.
func mcOperandMount(abs string) (mount, arg string) {
	// EvalSymlinks resolves a symlinked path to its real target, which is what
	// the bind mount needs to expose; a nonexistent path just fails here.
	real := abs
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		real = r
	}
	if _, err := os.Stat(real); err == nil {
		return real, real
	}
	for dir := filepath.Dir(abs); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(dir); err == nil {
			return dir, abs
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs, abs // reached the root without finding an existing ancestor (unreachable in practice)
		}
	}
}

// mcWithin reports whether path is at or below root.
func mcWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// mcRunArgs builds the `podman run` argv for a one-shot mc invocation. It is a
// pure function driven by useHostNet and interactive because platform.Detect()
// reads runtime.GOOS and os.Stdin is not controllable in unit tests that run
// on Linux.
func mcRunArgs(imageTag, configDir string, mounts []string, useHostNet, interactive bool, envs, args []string) []string {
	runArgs := []string{"run", "--rm"}
	if interactive {
		runArgs = append(runArgs, "-it")
	} else {
		runArgs = append(runArgs, "-i=false")
	}
	// macOS: no --network flag — the default bridge gives outbound access,
	// while host networking would only reach the VM's loopback.
	if useHostNet {
		// Linux: host networking so aliases to a loopback-bound MinIO
		// (127.0.0.1, the addon default) resolve to the host's loopback.
		runArgs = append(runArgs, "--network", "host")
	}
	// podman would otherwise inject the host's HTTP(S)_PROXY into the
	// container and mc's HTTPS transport could hijack LAN/loopback requests.
	runArgs = append(runArgs,
		"--http-proxy=false",
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(configDir), mcContainerConfigDir),
	)
	// Host-path operands mount at their own absolute path (see mcLocalFiles),
	// so the rewritten argument resolves identically inside and outside the
	// container.
	for _, dir := range mounts {
		runArgs = append(runArgs, "-v", fmt.Sprintf("%s:%s:z", hostMountPath(dir), hostMountPath(dir)))
	}
	for _, kv := range envs {
		runArgs = append(runArgs, "-e", kv)
	}
	runArgs = append(runArgs, imageTag)
	return append(runArgs, args...)
}

// --- internal helpers ---------------------------------------------------

func (m *MCRunner) run(args ...string) (string, error) {
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

func (m *MCRunner) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *MCRunner) imageExists(tag string) (bool, error) {
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
