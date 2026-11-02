package podman

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// MongoshRunner runs the mongosh client from the pgcli-mongosh image in a
// short-lived container, so pgcli itself stays free of a MongoDB driver.
//
// On Linux the container joins the host network and reaches a gateway bound to
// 127.0.0.1 exactly as a native mongosh would. On macOS the default bridge
// network reaches the host-published gateway through host.containers.internal
// (the DocumentDB add-on itself is Linux-only, matching the upstream gateway
// .deb's Unix-socket requirement).
type MongoshRunner struct {
	podman     string
	useHostNet bool
}

// NewMongoshRunner creates a MongoshRunner.
func NewMongoshRunner() (*MongoshRunner, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	ensurePodmanStateReady(path)
	return &MongoshRunner{
		podman:     path,
		useHostNet: platform.Detect() != platform.MacOS,
	}, nil
}

// Host is the address a container on this runner's network uses to reach a
// gateway published on the host. listen is the gateway's configured bind
// address; with host networking an IPv6-only loopback listener must be reached
// through ::1.
func (r *MongoshRunner) Host(listen string) string {
	if r.useHostNet {
		if listen == "::1" {
			return "::1"
		}
		return "127.0.0.1"
	}
	return "host.containers.internal"
}

// Run executes mongosh in a throwaway container. envs are passed by NAME only
// (`-e NAME`), so credential values stay out of the podman argv; they travel
// in the child process environment and are expanded by the image's entrypoint.
func (r *MongoshRunner) Run(imageTag string, envs []string, interactive bool, args []string) error {
	if err := r.ensureImage(imageTag); err != nil {
		return err
	}
	interactive = interactive && isTerminal(os.Stdin)
	names := envNames(envs)
	runArgs := mongoshRunArgs(imageTag, r.useHostNet, interactive, names, args)
	cmd := podmanCommand(r.podman, runArgs...)
	cmd.Env = withEnvOverrides(os.Environ(), envs)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running mongosh: %w", err)
	}
	return nil
}

// Capture runs a non-interactive mongosh command and returns its stdout, so
// callers can distinguish an empty result from a failed one. stderr is folded
// into the error when the command fails.
func (r *MongoshRunner) Capture(imageTag string, envs []string, args []string) (string, error) {
	if err := r.ensureImage(imageTag); err != nil {
		return "", err
	}
	runArgs := mongoshRunArgs(imageTag, r.useHostNet, false, envNames(envs), args)
	cmd := podmanCommand(r.podman, runArgs...)
	cmd.Env = withEnvOverrides(os.Environ(), envs)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("running mongosh: %w: %s", err, msg)
		}
		return "", fmt.Errorf("running mongosh: %w", err)
	}
	return string(out), nil
}

// withEnvOverrides replaces any inherited values of the names in envs, so a
// host PGCLI_DOCDB_URI cannot shadow the per-invocation connection string.
func withEnvOverrides(base, envs []string) []string {
	names := make(map[string]bool, len(envs))
	for _, kv := range envs {
		if name, _, ok := strings.Cut(kv, "="); ok && name != "" {
			names[name] = true
		}
	}
	out := make([]string, 0, len(base)+len(envs))
	for _, kv := range base {
		if name, _, ok := strings.Cut(kv, "="); ok && names[name] {
			continue
		}
		out = append(out, kv)
	}
	return append(out, envs...)
}

// ensureImage pulls the mongosh image if it is not present locally (pull-only:
// the tag is published to the public ghcr repo; pgcli never builds it here).
func (r *MongoshRunner) ensureImage(imageTag string) error {
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
		return fmt.Errorf("pulling mongosh image %s (is ghcr.io/mars-base/pgcli reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

func (r *MongoshRunner) output(args ...string) (string, error) {
	out, err := podmanCommand(r.podman, args...).Output()
	if err != nil {
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (r *MongoshRunner) interactive(args ...string) error {
	cmd := podmanCommand(r.podman, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// mongoshRunArgs builds the `podman run` argv for a one-shot mongosh
// invocation. Pure so tests can pin the flag order without podman.
func mongoshRunArgs(imageTag string, useHostNet, interactive bool, envNames, args []string) []string {
	runArgs := []string{"run", "--rm"}
	if interactive {
		runArgs = append(runArgs, "-it")
	} else {
		runArgs = append(runArgs, "-i=false")
	}
	if useHostNet {
		runArgs = append(runArgs, "--network", "host")
	}
	// A host HTTP(S)_PROXY must not hijack requests to a loopback gateway.
	runArgs = append(runArgs, "--http-proxy=false")
	for _, name := range envNames {
		runArgs = append(runArgs, "-e", name)
	}
	runArgs = append(runArgs, imageTag)
	return append(runArgs, args...)
}

// DocumentDBMongoshURI builds the connection string handed to mongosh. The
// client user lives in the admin database (documentdb_api.create_user), so
// authSource is always admin even when the URI selects another database.
func DocumentDBMongoshURI(dc *config.DocumentDBConfig, host string, tls bool, database string) string {
	u := url.URL{
		Scheme: "mongodb",
		User:   url.UserPassword(dc.ClientUser, dc.ClientPassword),
		Host:   net.JoinHostPort(host, strconv.Itoa(dc.HostPort)),
		Path:   "/",
	}
	if database != "" {
		u.Path = "/" + database
	}
	q := url.Values{}
	q.Set("directConnection", "true")
	q.Set("authSource", "admin")
	q.Set("serverSelectionTimeoutMS", "10000")
	if tls {
		q.Set("tls", "true")
		q.Set("tlsAllowInvalidCertificates", "true")
	}
	u.RawQuery = q.Encode()
	return u.String()
}
