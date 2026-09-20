package podman

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// mcliContainerConfigDir is where `pg mcli` pins mcli's config inside the
// silo image: mcli resolves its config as $HOME/.mcli by default, but pgcli
// pins MC_CONFIG_DIR to this path and mounts the host ~/.mc there — the same
// file `pg mc` persists, so one `alias set` serves both clients. Pinning the
// path keeps the behaviour independent of whatever HOME the silo image happens
// to set (its server wants /data for storage).
const mcliContainerConfigDir = "/data/.mc"

// MCLIRunner runs silo's mcli client from the same docker.io/pgsty/silo image
// the silo addon uses, in a short-lived container. It is the twin of MCRunner:
// the alias parsing, local-file mounting, MC_HOST_* forwarding, and loopback
// --insecure logic are shared helpers with no binding to the mc binary, so only
// the image, the container config path, and the run args (mcli is selected via
// --entrypoint, and MC_CONFIG_DIR is pinned) differ. mcli's alias
// configuration persists on the host at ~/.mc — mc's file, shared deliberately
// so one `pg mc`/`pg mcli alias set` is visible to both clients — via a bind
// mount, so setting an alias once works everywhere.
type MCLIRunner struct {
	podman     string // podman binary path
	imageTag   string
	configDir  string // host ~/.mc (shared with MCRunner on purpose)
	useHostNet bool   // Linux: --network host reaches a loopback-bound silo; macOS: host networking binds the VM loopback, so use the default bridge
}

// NewMCLIRunner creates an MCLIRunner. Unlike the silo addon manager it works
// on macOS: mcli is a client, so the bridge network's outbound connectivity is
// all a remote endpoint needs (see the platform note in the command help).
func NewMCLIRunner() (*MCLIRunner, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home directory: %w", err)
	}
	ensurePodmanStateReady(path)
	return &MCLIRunner{
		podman:     path,
		imageTag:   config.DefaultSiloImageTag,
		configDir:  filepath.Join(home, ".mc"),
		useHostNet: platform.Detect() != platform.MacOS,
	}, nil
}

// Run executes mcli in a temporary container with os.Args passthrough. Exit
// status is propagated: a failing mcli command returns an error. The body
// mirrors MCRunner.Run — every step (alias detection, local-path rewriting,
// loopback --insecure, macOS home-tree mount filter) is shared logic.
func (m *MCLIRunner) Run(args []string) error {
	// Create ~/.mc before mounting, or podman creates the mountpoint root-owned.
	if err := os.MkdirAll(m.configDir, 0700); err != nil {
		return fmt.Errorf("creating mcli config dir %s: %w", m.configDir, err)
	}
	if err := m.EnsureImage(); err != nil {
		return err
	}

	envs := forwardMCHostEnv()
	aliasURLs := mcAliasURLs(m.configDir, envs)
	aliases := make(map[string]bool, len(aliasURLs))
	for name := range aliasURLs {
		aliases[name] = true
	}

	// The container cannot see the host filesystem except through mounts, so a
	// local-path operand of `cp` / `mirror` / `diff` would fail with
	// "Requested path not found". Resolve each such operand to its absolute
	// path and mount that exact path (realpath semantics: same path inside the
	// container), rewriting the argument to match.
	args, mounts := mcLocalFiles(args, aliases)

	// A self-signed pgcli silo on loopback needs --insecure (mcli cannot persist
	// CA trust); add it unless the user already passed one.
	if !containsInsecureFlag(args) && mcLocalInsecureHosts(args, aliasURLs) {
		args = append(args, "--insecure")
	}

	// macOS: the podman machine shares only the home tree over virtiofs, so a
	// mount outside it fails with an opaque error — drop it with a clear note.
	if !m.useHostNet && len(mounts) > 0 {
		home, err := os.UserHomeDir()
		if err != nil {
			slog.Debug("mcli: home for mount filter unavailable", "err", err)
		} else {
			kept := mounts[:0]
			for _, dir := range mounts {
				if mcWithin(home, dir) {
					kept = append(kept, dir)
					continue
				}
				fmt.Fprintf(os.Stderr, "[!] pg mcli cannot publish %s on macOS: the podman machine shares only %s — keep local files under your home directory\n", dir, home)
			}
			mounts = kept
		}
	}

	runArgs := mcliRunArgs(m.imageTag, m.configDir, mounts, m.useHostNet, isTerminal(os.Stdin), envs, args)
	slog.Debug("podman mcli", "args", runArgs)
	cmd := podmanCommand(m.podman, runArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running mcli: %w", err)
	}
	return nil
}

// EnsureImage pulls the silo image if it is not present locally (pull-only: the
// tag is published to the public docker.io/pgsty repo; pgcli never builds it).
func (m *MCLIRunner) EnsureImage() error {
	exists, err := m.imageExists(m.imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", m.imageTag)
	if err := m.runInteractive("pull", m.imageTag); err != nil {
		return fmt.Errorf("pulling mcli image %s (is docker.io/pgsty reachable?): %w", m.imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// mcliRunArgs builds the `podman run` argv for a one-shot mcli invocation. It
// is mcRunArgs plus the two differences mcli needs from the silo image: an
// explicit `--entrypoint mcli` (the image's ENTRYPOINT is its docker-entrypoint
// wrapper for the server) and a pinned MC_CONFIG_DIR pointing at the mounted
// config dir. Pure function driven by useHostNet and interactive for the same
// testability reason mcRunArgs documents.
func mcliRunArgs(imageTag, configDir string, mounts []string, useHostNet, interactive bool, envs, args []string) []string {
	runArgs := []string{"run", "--rm"}
	if interactive {
		runArgs = append(runArgs, "-it")
	} else {
		runArgs = append(runArgs, "-i=false")
	}
	// macOS: no --network flag — the default bridge gives outbound access,
	// while host networking would only reach the VM's loopback.
	if useHostNet {
		// Linux: host networking so aliases to a loopback-bound silo
		// (127.0.0.1, the addon default) resolve to the host's loopback.
		runArgs = append(runArgs, "--network", "host")
	}
	// podman would otherwise inject the host's HTTP(S)_PROXY into the
	// container and mcli's HTTPS transport could hijack LAN/loopback requests.
	runArgs = append(runArgs,
		"--http-proxy=false",
		"--entrypoint", "mcli",
		"-e", "MC_CONFIG_DIR="+mcliContainerConfigDir,
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(configDir), mcliContainerConfigDir),
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

func (m *MCLIRunner) run(args ...string) (string, error) {
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

func (m *MCLIRunner) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *MCLIRunner) imageExists(tag string) (bool, error) {
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
