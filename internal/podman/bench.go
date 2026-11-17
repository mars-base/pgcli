package podman

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// BenchContainerOptions describes one disposable pgbench or psql container.
// Env entries are KEY=VALUE and are passed to podman as "-e KEY" only, so
// credential values stay out of the podman argv (and thus out of ps).
type BenchContainerOptions struct {
	Image      string
	Entrypoint string // "pgbench" or "psql"
	Args       []string
	Env        []string // KEY=VALUE
	Mounts     []string // pre-formatted -v operands
	WorkDir    string
}

// RunPgbench runs pgbench in a one-shot container with the host network.
func (m *Manager) RunPgbench(o BenchContainerOptions) error {
	_, err := m.execBenchContainer(o, false)
	return err
}

// QueryPsql runs psql in a one-shot container and returns its trimmed stdout.
// stderr is folded into the error when the command fails.
func (m *Manager) QueryPsql(o BenchContainerOptions) (string, error) {
	return m.execBenchContainer(o, true)
}

func (m *Manager) execBenchContainer(o BenchContainerOptions, capture bool) (string, error) {
	if o.Image == "" {
		return "", fmt.Errorf("bench container image must not be empty")
	}
	if o.Entrypoint == "" {
		return "", fmt.Errorf("bench container entrypoint must not be empty")
	}

	name := fmt.Sprintf("pgcli-bench-%d", time.Now().UnixNano())
	defer m.run("rm", "-f", name)

	cmd := podmanCommand(m.podman, benchContainerArgs(name, o)...)
	baseEnv := cmd.Env
	if baseEnv == nil {
		baseEnv = os.Environ()
	}
	cmd.Env = withEnvOverrides(baseEnv, o.Env)

	var stdout, stderr strings.Builder
	if capture {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	} else {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Run(); err != nil {
		msg := ""
		if capture {
			msg = strings.TrimSpace(stderr.String())
		}
		return "", benchExecError(o.Entrypoint, o.Image, err, msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// benchContainerArgs builds the podman run argv for a one-shot client
// container. Pure so tests can pin flag order without podman.
func benchContainerArgs(name string, o BenchContainerOptions) []string {
	args := []string{
		"run", "--rm", "--name", name,
		"--network", "host",
		"--http-proxy=false",
		"-i=false",
	}
	for _, mount := range o.Mounts {
		args = append(args, "-v", mount)
	}
	if o.WorkDir != "" {
		args = append(args, "-w", o.WorkDir)
	}
	for _, n := range envNames(o.Env) {
		args = append(args, "-e", n)
	}
	// pgcli-pg's entrypoint starts sshd before handing off to the official
	// postgres entrypoint; with --network host that can collide with the host
	// sshd. Select pgbench/psql directly and skip the entrypoint entirely.
	args = append(args, "--entrypoint", o.Entrypoint, o.Image)
	return append(args, o.Args...)
}

func benchExecError(entrypoint, image string, err error, stderr string) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && (exitErr.ExitCode() == 126 || exitErr.ExitCode() == 127) {
		return fmt.Errorf("%s is not available in image %q (exit %d); pass --image with an image that includes %s: %w",
			entrypoint, image, exitErr.ExitCode(), entrypoint, err)
	}
	if stderr != "" {
		return fmt.Errorf("running %s: %w: %s", entrypoint, err, stderr)
	}
	return fmt.Errorf("running %s: %w", entrypoint, err)
}
