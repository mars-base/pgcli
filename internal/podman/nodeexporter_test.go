package podman

import (
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// TestNodeExporterArgs pins the `podman run` argv for node_exporter across both
// networking shapes. node_exporter is configured entirely by CLI flags AFTER the
// image name (no config file, no env), and the whole point of this addon is the
// three read-only host mounts + --path.* redirection that make it report HOST
// metrics rather than container metrics. A missing --path.* or a stray :z here
// would silently produce a wrong exporter, so this is the load-bearing contract.
func TestNodeExporterArgs(t *testing.T) {
	base := func() *config.NodeExporterConfig {
		return &config.NodeExporterConfig{
			ContainerName: "pgcli-nodeexporter-ne1",
			Name:          "ne1",
			HostPort:      9100,
			Listen:        "0.0.0.0",
			ImageTag:      config.DefaultNodeExporterImageTag,
		}
	}

	// hasMount reports whether some `-v <spec>` entry equals the exact spec.
	hasMount := func(args []string, spec string) bool {
		for i, a := range args {
			if a == "-v" && i+1 < len(args) && args[i+1] == spec {
				return true
			}
		}
		return false
	}
	// imageIdx is where podman flag parsing stops and node_exporter's own flags begin.
	imageIdx := func(args []string) int {
		return slices.Index(args, config.DefaultNodeExporterImageTag)
	}

	t.Run("linux host networking with all three read-only host mounts", func(t *testing.T) {
		nc := base()
		args := nodeExporterArgs(nc, false, "pgcli-net")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "host") {
			t.Errorf("expected --network host, got %v", args)
		}
		if slices.Contains(args, "-p") {
			t.Errorf("host networking must not publish ports, got %v", args)
		}
		if !slices.Contains(args, "--http-proxy=false") {
			t.Errorf("--http-proxy=false missing: %v", args)
		}
		if !slices.Contains(args, "--restart") || !slices.Contains(args, "unless-stopped") {
			t.Errorf("--restart unless-stopped missing: %v", args)
		}

		// The three mandatory host mounts, each read-only (:ro, never :z).
		for _, spec := range []string{
			"/proc:/host/proc:ro",
			"/sys:/host/sys:ro",
			"/:/rootfs:ro",
		} {
			if !hasMount(args, spec) {
				t.Errorf("missing read-only host mount %q: %v", spec, args)
			}
		}
		// No :z relabeling anywhere — it would relabel the host root fs.
		for i, a := range args {
			if a == "-v" && i+1 < len(args) && strings.Contains(args[i+1], ":z") {
				t.Errorf("host mount must never use :z (relabels host fs), got %q", args[i+1])
			}
		}

		idx := imageIdx(args)
		if idx == -1 {
			t.Fatal("image tag not found in args")
		}
		if idx == len(args)-1 {
			t.Fatal("image tag must not be last — node_exporter needs trailing CLI flags")
		}
		trailing := args[idx+1:]
		for _, want := range []string{
			"--web.listen-address=0.0.0.0:9100",
			"--path.procfs=/host/proc",
			"--path.sysfs=/host/sys",
			"--path.rootfs=/rootfs",
			"--collector.filesystem.mount-points-exclude=^/(sys|proc|dev|host|etc)($|/)",
		} {
			if !slices.Contains(trailing, want) {
				t.Errorf("missing trailing flag %q: %v", want, trailing)
			}
		}
		// No --user flag: the image runs unprivileged and the read-only mounts
		// need nothing prepared host-side.
		if slices.Contains(args, "--user") {
			t.Errorf("node_exporter must not pass --user: %v", args)
		}
	})

	t.Run("macOS bridge networking publishes port and widens bind", func(t *testing.T) {
		nc := base()
		nc.Listen = "127.0.0.1" // even a loopback listen is widened under bridge
		args := nodeExporterArgs(nc, true, "pgcli-net")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "pgcli-net") {
			t.Errorf("expected --network pgcli-net, got %v", args)
		}
		if !slices.Contains(args, "-p") || !slices.Contains(args, "9100:9100") {
			t.Errorf("bridge must publish 9100:9100, got %v", args)
		}
		if !slices.Contains(args, "--web.listen-address=0.0.0.0:9100") {
			t.Errorf("bridge should widen loopback to 0.0.0.0, got %v", args)
		}
		if slices.Contains(args, "--web.listen-address=127.0.0.1:9100") {
			t.Errorf("bridge must not keep the loopback bind: %v", args)
		}
	})

	t.Run("extra args appear last (override via last-wins)", func(t *testing.T) {
		nc := base()
		nc.ExtraArgs = []string{"--no-collector.cpufreq", "--collector.netdev"}
		args := nodeExporterArgs(nc, false, "pgcli-net")

		idx := imageIdx(args)
		trailing := args[idx+1:]
		if len(trailing) < 2 {
			t.Fatalf("trailing too short: %v", trailing)
		}
		last2 := trailing[len(trailing)-2:]
		if !slices.Contains(last2, "--no-collector.cpufreq") || !slices.Contains(last2, "--collector.netdev") {
			t.Errorf("ExtraArgs must be last: trailing=%v last2=%v", trailing, last2)
		}
	})
}
