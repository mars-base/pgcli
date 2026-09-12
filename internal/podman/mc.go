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

	runArgs := mcRunArgs(m.imageTag, m.configDir, m.useHostNet, isTerminal(os.Stdin), forwardMCHostEnv(), args)
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

// mcRunArgs builds the `podman run` argv for a one-shot mc invocation. It is a
// pure function driven by useHostNet and interactive because platform.Detect()
// reads runtime.GOOS and os.Stdin is not controllable in unit tests that run
// on Linux.
func mcRunArgs(imageTag, configDir string, useHostNet, interactive bool, envs, args []string) []string {
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
