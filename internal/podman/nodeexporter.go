package podman

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// NodeExporterManager manages node_exporter containers — the official
// Prometheus host-metrics exporter
// (https://github.com/prometheus/node_exporter). It is a top-level, single
// container addon on one host port, configured entirely by CLI flags (no
// rendered config file, no hot reload, no data — the same simple shape as
// VictoriaMetrics).
//
// The one difference from every other web addon: a container that reads its own
// /proc reports CONTAINER metrics, not host metrics. To expose the host, pgcli
// hard-wires three read-only bind mounts (host /proc, /sys and / at /host/proc,
// /host/sys and /rootfs) and points the binary at them with --path.procfs /
// --path.sysfs / --path.rootfs. The mounts are :ro only — never :z — because
// relabeling the host root filesystem or /proc is unacceptable (vlagent's log
// mounts set the same precedent).
//
// There is NO authentication or TLS: node_exporter just serves /metrics. The
// usual topology has Prometheus on a DIFFERENT host, so it defaults to
// 0.0.0.0 and is guarded only by the network boundary.
type NodeExporterManager struct {
	cfg    *config.Config
	podman string // podman binary path
	bridge bool   // macOS: serve on the pgcli-net bridge with published ports
}

// The fixed host→container redirect mounts and their --path.* counterparts.
// Identical string constants so the pure argv builder and its test agree.
const (
	neHostProc  = "/proc"
	neHostSys   = "/sys"
	neHostRoot  = "/"
	neProcMount = "/host/proc"
	neSysMount  = "/host/sys"
	neRootMount = "/rootfs"
	neFsExclude = `^/(sys|proc|dev|host|etc)($|/)`
)

// NewNodeExporterManager creates a NodeExporterManager. It works on both
// platforms: Linux serves over host networking (so the exporter binds the host
// port directly), macOS over the pgcli-net bridge with the port published
// (proxyBindHost widening makes it reachable through the VM), exactly like
// VictoriaMetrics. The upstream image is multi-arch.
func NewNodeExporterManager(cfg *config.Config) (*NodeExporterManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	ensurePodmanStateReady(path)
	return &NodeExporterManager{
		cfg:    cfg,
		podman: path,
		bridge: platform.Detect() == platform.MacOS,
	}, nil
}

// nodeExporterArgs builds the full `podman run` argv for a node_exporter
// instance. Pure function (no podman calls) so the mandatory mount + flag shape
// is unit-testable — a missing --path.* or a wrong :z here would silently make
// the exporter report container metrics, the classic container mistake:
//   - NO --user flag: the image runs as an unprivileged user but the read-only
//     host mounts do not need root to write, so no chown/mkdir is required.
//   - netFlags: --network host on Linux (so the exporter binds the host port),
//     bridge + -p on macOS.
//   - Three read-only mounts, host path → fixed container path (/proc→/host/proc,
//     /sys→/host/sys, /→/rootfs). :ro only, never :z.
//   - After ImageTag, node_exporter's OWN flags:
//     --web.listen-address=<bind>:<port> (widened via proxyBindHost on macOS),
//     --path.procfs/sysfs/rootfs pointing at the three mounts, and the default
//     --collector.filesystem.mount-points-exclude that drops the pseudo-
//     filesystems pgcli itself just mounted (otherwise node_filesystem_* is all
//     noise). ExtraArgs come LAST so a user can override any of them
//     (last-wins).
func nodeExporterArgs(nc *config.NodeExporterConfig, bridge bool, network string) []string {
	args := []string{"run", "-d", "--name", nc.ContainerName}
	args = append(args, netFlags(bridge, network, nc.HostPort)...)
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"-v", fmt.Sprintf("%s:%s:ro", neHostProc, neProcMount),
		"-v", fmt.Sprintf("%s:%s:ro", neHostSys, neSysMount),
		"-v", fmt.Sprintf("%s:%s:ro", neHostRoot, neRootMount),
	)
	args = append(args, nc.ImageTag)
	args = append(args,
		fmt.Sprintf("--web.listen-address=%s:%d", proxyBindHost(bridge, nc.Listen), nc.HostPort),
		"--path.procfs="+neProcMount,
		"--path.sysfs="+neSysMount,
		"--path.rootfs="+neRootMount,
		"--collector.filesystem.mount-points-exclude="+neFsExclude,
	)
	// ExtraArgs LAST — last-wins semantics for node_exporter CLI flags.
	args = append(args, nc.ExtraArgs...)
	return args
}

// createContainer runs a node_exporter instance. There is no data dir to create
// (the exporter is stateless) and no chown — the read-only host mounts need
// nothing prepared on the host side.
func (m *NodeExporterManager) createContainer(nc *config.NodeExporterConfig) error {
	args := nodeExporterArgs(nc, m.bridge, m.cfg.Podman.Network)
	slog.Debug("podman nodeexporter run", "args", args)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating node_exporter container: %w", err)
	}
	return nil
}

// EnsureImage pulls the node_exporter image if it is not present locally
// (pull-only: public docker.io/prom tags; pgcli never builds them). Offline
// hosts pre-seed it with `podman load` like the core images.
func (m *NodeExporterManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling node_exporter image %s (is docker.io reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or recreates the node_exporter container (install
// semantics: stop/rm/recreate so changed ports/listen/extra-args take effect).
func (m *NodeExporterManager) EnsureContainer(nc *config.NodeExporterConfig) error {
	containerName := nc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> node_exporter container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping node_exporter container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing node_exporter container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale node_exporter container: %w", err)
			}
		}
	}

	if err := m.createContainer(nc); err != nil {
		return err
	}
	fmt.Println("  [OK] node_exporter container started")
	return nil
}

// StartContainer starts the node_exporter container without regenerating config
// (autostart-on-boot path): running → no-op; exists but stopped → podman start,
// recreating on improper state; missing → create from the stored config. There
// is no config file to check (unlike Prometheus), so a missing container is
// created fresh.
func (m *NodeExporterManager) StartContainer(nc *config.NodeExporterConfig) error {
	containerName := nc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] node_exporter %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] node_exporter %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] node_exporter %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper node_exporter container: %w", err)
		}
	}

	if err := m.createContainer(nc); err != nil {
		return err
	}
	fmt.Printf("  [OK] node_exporter %s started\n", containerName)
	return nil
}

// Remove stops and removes the node_exporter container. There is no data dir to
// keep or clean — the exporter is stateless — so the cleanData flag only
// controls the "nothing to delete" notice (parity with the VictoriaMetrics
// manager's signature; reinstalling always starts fresh regardless).
func (m *NodeExporterManager) Remove(nc *config.NodeExporterConfig, cleanData bool) error {
	containerName := nc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing node_exporter container: %w", err)
	}
	fmt.Println("  [OK] node_exporter container removed")
	if cleanData {
		// No-op today (nothing persists); kept so the CLI flag has a stable
		// meaning if node_exporter ever grows a tsdb/wal in a future image.
		fmt.Println("  [OK] No data to clean (node_exporter is stateless)")
	}
	return nil
}

// ContainerRunning reports whether the named container is currently running.
func (m *NodeExporterManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether a container with the given name exists
// (running or not) — the install path uses it to skip recreation of a live
// instance.
func (m *NodeExporterManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Stop stops a node_exporter container.
func (m *NodeExporterManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ---------------------------------------------------

func (m *NodeExporterManager) run(args ...string) (string, error) {
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

func (m *NodeExporterManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *NodeExporterManager) imageExists(tag string) (bool, error) {
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

func (m *NodeExporterManager) containerExists(name string) (bool, error) {
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

func (m *NodeExporterManager) containerRunning(name string) (bool, error) {
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
