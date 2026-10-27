package podman

import (
	"fmt"
	"os"
	"strings"

	"github.com/mars-base/pgcli/internal/platform"
)

// InfluxRunner runs the `influx` CLI from the pre-built pgcli-influxdb image
// in a short-lived container, passing arguments straight through (the image's
// ENTRYPOINT is influxd, so the CLI is selected explicitly).
//
// On Linux the container joins the host network, so a loopback-bound instance
// (the addon default) is reachable as 127.0.0.1 exactly as it is from a native
// influx install; a remote server is reached the same way. On macOS the default
// bridge network gives outbound access to remote servers (the addon itself is
// Linux-only).
type InfluxRunner struct {
	podman     string
	useHostNet bool
}

// NewInfluxRunner creates an InfluxRunner.
func NewInfluxRunner() (*InfluxRunner, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	ensurePodmanStateReady(path)
	return &InfluxRunner{
		podman:     path,
		useHostNet: platform.Detect() != platform.MacOS,
	}, nil
}

// Run executes influx in a throwaway container. envs are passed by NAME only
// (`-e NAME`), so credential values stay out of the podman argv (and `ps`);
// they travel in the child process environment instead. A host file named by
// an -import -path operand is mounted at its absolute path (read-only) and the
// argument is rewritten to match.
func (r *InfluxRunner) Run(imageTag string, envs, args []string) error {
	if err := r.ensureImage(imageTag); err != nil {
		return err
	}
	args, mounts := influxImportMounts(args)
	names := envNames(envs)
	runArgs := influxRunArgs(imageTag, mounts, r.useHostNet, isTerminal(os.Stdin), names, args)
	cmd := podmanCommand(r.podman, runArgs...)
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, envs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running influx: %w", err)
	}
	return nil
}

func envNames(envs []string) []string {
	names := make([]string, 0, len(envs))
	for _, kv := range envs {
		if name, _, ok := strings.Cut(kv, "="); ok && name != "" {
			names = append(names, name)
		}
	}
	return names
}

// ensureImage pulls the influxdb image if it is not present locally (pull-only:
// the tag is published to the public ghcr repo; pgcli never builds it here).
func (r *InfluxRunner) ensureImage(imageTag string) error {
	out, err := r.output("images", "--format", "{{.Repository}}:{{.Tag}}")
	if err != nil {
		return err
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == imageTag {
			return nil
		}
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := r.interactive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling influxdb image %s (is ghcr.io/mars-base/pgcli reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

func (r *InfluxRunner) output(args ...string) (string, error) {
	out, err := podmanCommand(r.podman, args...).Output()
	if err != nil {
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (r *InfluxRunner) interactive(args ...string) error {
	cmd := podmanCommand(r.podman, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// influxImportMounts rewrites the -path operand of an `influx -import` run to
// its absolute host path and returns the bind mount that makes it visible in
// the container at the same path. Non-import invocations and missing paths are
// left untouched for influx itself to report.
func influxImportMounts(args []string) ([]string, []string) {
	if !containsArg(args, "-import") {
		return args, nil
	}
	mounts := []string{}
	index := -1
	for i, arg := range args {
		if arg == "-path" && i+1 < len(args) {
			index = i + 1
			break
		}
		if v, ok := strings.CutPrefix(arg, "-path="); ok {
			index = i
			_ = v
			break
		}
	}
	if index < 0 {
		return args, nil
	}
	raw := args[index]
	if v, ok := strings.CutPrefix(raw, "-path="); ok {
		raw = v
	}
	abs := hostMountPath(raw)
	if _, err := os.Stat(abs); err != nil {
		return args, nil
	}
	mounts = append(mounts, fmt.Sprintf("%s:%s:ro,z", abs, abs))
	if strings.HasPrefix(args[index], "-path=") {
		args[index] = "-path=" + abs
	} else {
		args[index] = abs
	}
	return args, mounts
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

// influxRunArgs builds the `podman run` argv for a one-shot influx invocation.
// Pure so tests can pin the flag order without podman.
func influxRunArgs(imageTag string, mounts []string, useHostNet, interactive bool, envNames, args []string) []string {
	runArgs := []string{"run", "--rm"}
	if interactive {
		runArgs = append(runArgs, "-it")
	} else {
		runArgs = append(runArgs, "-i=false")
	}
	if useHostNet {
		runArgs = append(runArgs, "--network", "host")
	}
	// The image runs influxd by default; select the CLI explicitly. Disable
	// podman's proxy injection so a host HTTP(S)_PROXY cannot hijack requests
	// to a loopback instance.
	runArgs = append(runArgs, "--http-proxy=false", "--entrypoint", "/usr/local/bin/influx")
	for _, mount := range mounts {
		runArgs = append(runArgs, "-v", mount)
	}
	for _, name := range envNames {
		runArgs = append(runArgs, "-e", name)
	}
	runArgs = append(runArgs, imageTag)
	return append(runArgs, args...)
}
