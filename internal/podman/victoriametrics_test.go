package podman

import (
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// TestVictoriaMetricsArgsAndEnv pins the `podman run` argv for VictoriaMetrics
// across both networking shapes. VictoriaMetrics is configured entirely by CLI
// flags AFTER the image name (no config file, no env vars), so this function is
// the whole contract between a VictoriaMetricsConfig and the container.
// The trailing-flag shape (nothing container-side may leak past the image name)
// is exactly what deserves pinning.
func TestVictoriaMetricsArgsAndEnv(t *testing.T) {
	base := func() *config.VictoriaMetricsConfig {
		return &config.VictoriaMetricsConfig{
			ContainerName: "pgcli-victoriametrics-vm1",
			Name:          "vm1",
			HostPort:      8428,
			Listen:        "0.0.0.0",
			ImageTag:      config.DefaultVictoriaMetricsImageTag,
			Retention:     "1",
		}
	}

	// hasMount reports whether some `-v <spec>` entry contains substr.
	hasMount := func(args []string, substr string) bool {
		for i, a := range args {
			if a == "-v" && i+1 < len(args) && strings.Contains(args[i+1], substr) {
				return true
			}
		}
		return false
	}
	// imageIdx returns the position of the image tag, i.e. where podman's own
	// flag parsing stops and the victoria-metrics binary's own trailing flags begin.
	imageIdx := func(args []string) int {
		return slices.Index(args, config.DefaultVictoriaMetricsImageTag)
	}

	t.Run("linux host networking", func(t *testing.T) {
		vc := base()
		args := victoriaMetricsArgsAndEnv(vc, false, "pgcli-net", "/home/u/.pgcli/addon/victoriametrics/vm1/data")

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
		if !hasMount(args, "/home/u/.pgcli/addon/victoriametrics/vm1/data:/victoria-metrics-data:z") {
			t.Errorf("data dir mount missing: %v", args)
		}
		// No --user flag: the image runs as root, no pre-chown needed.
		if slices.Contains(args, "--user") {
			t.Errorf("VictoriaMetrics must not pass --user (the image runs as root): %v", args)
		}

		idx := imageIdx(args)
		if idx == -1 {
			t.Fatal("image tag not found in args")
		}
		if idx == len(args)-1 {
			t.Fatal("image tag must not be last — victoria-metrics needs trailing CLI flags after the image name")
		}
		trailing := args[idx+1:]
		if !slices.Contains(trailing, "-storageDataPath=/victoria-metrics-data") {
			t.Errorf("-storageDataPath missing from trailing flags: %v", trailing)
		}
		if !slices.Contains(trailing, "-httpListenAddr=0.0.0.0:8428") {
			t.Errorf("-httpListenAddr missing from trailing flags: %v", trailing)
		}
		if !slices.Contains(trailing, "-retentionPeriod=1") {
			t.Errorf("-retentionPeriod missing from trailing flags: %v", trailing)
		}
	})

	t.Run("macOS bridge networking publishes port and keeps a non-loopback bind", func(t *testing.T) {
		vc := base()
		vc.Listen = "127.0.0.1" // even a loopback listen is widened under bridge
		args := victoriaMetricsArgsAndEnv(vc, true, "pgcli-net", "/data")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "pgcli-net") {
			t.Errorf("expected --network pgcli-net, got %v", args)
		}
		if !slices.Contains(args, "-p") || !slices.Contains(args, "8428:8428") {
			t.Errorf("bridge must publish 8428:8428, got %v", args)
		}
		// The published port can't reach a loopback-only bind.
		if !slices.Contains(args, "-httpListenAddr=0.0.0.0:8428") {
			t.Errorf("bridge should widen loopback to 0.0.0.0, got %v", args)
		}
		if slices.Contains(args, "-httpListenAddr=127.0.0.1:8428") {
			t.Errorf("bridge must not keep the loopback bind: %v", args)
		}
	})

	t.Run("explicit non-loopback listen passes through", func(t *testing.T) {
		vc := base()
		vc.Listen = "10.0.0.5"
		args := victoriaMetricsArgsAndEnv(vc, true, "pgcli-net", "/data")
		if !slices.Contains(args, "-httpListenAddr=10.0.0.5:8428") {
			t.Errorf("explicit bind should pass through under bridge: %v", args)
		}
	})

	t.Run("empty retention omits the flag", func(t *testing.T) {
		vc := base()
		vc.Retention = ""
		args := victoriaMetricsArgsAndEnv(vc, false, "pgcli-net", "/data")
		idx := imageIdx(args)
		for _, a := range args[idx+1:] {
			if strings.HasPrefix(a, "-retentionPeriod") {
				t.Errorf("empty retention should omit the flag entirely, got %q", a)
			}
		}
	})

	t.Run("extra args appear last after image flags", func(t *testing.T) {
		vc := base()
		vc.ExtraArgs = []string{"-search.maxUniqueTimeseries=1000000", "-selfScrapeInterval=10s"}
		args := victoriaMetricsArgsAndEnv(vc, false, "pgcli-net", "/data")

		idx := imageIdx(args)
		trailing := args[idx+1:]
		// ExtraArgs must be the LAST entries.
		if len(trailing) < 2 {
			t.Fatalf("trailing too short: %v", trailing)
		}
		last2 := trailing[len(trailing)-2:]
		if !slices.Contains(last2, "-search.maxUniqueTimeseries=1000000") ||
			!slices.Contains(last2, "-selfScrapeInterval=10s") {
			t.Errorf("ExtraArgs must be last: trailing=%v, last2=%v", trailing, last2)
		}
	})

	t.Run("extra args can override earlier flags via last-wins", func(t *testing.T) {
		vc := base()
		vc.ExtraArgs = []string{"-retentionPeriod=6"}
		args := victoriaMetricsArgsAndEnv(vc, false, "pgcli-net", "/data")

		idx := imageIdx(args)
		trailing := args[idx+1:]
		// Both the original and the override appear; the override is last (wins).
		counts := map[string]int{}
		for _, a := range trailing {
			if strings.HasPrefix(a, "-retentionPeriod") {
				counts[a]++
			}
		}
		if counts["-retentionPeriod=1"] != 1 || counts["-retentionPeriod=6"] != 1 {
			t.Errorf("expected both retention values (default + override), got %v in %v", counts, trailing)
		}
		// Override must be LAST.
		if trailing[len(trailing)-1] != "-retentionPeriod=6" {
			t.Errorf("override must be last: %v", trailing)
		}
	})

	t.Run("no extra args produces clean argv", func(t *testing.T) {
		vc := base()
		args := victoriaMetricsArgsAndEnv(vc, false, "pgcli-net", "/data")
		idx := imageIdx(args)
		trailing := args[idx+1:]
		if len(trailing) != 3 {
			t.Errorf("no extra args: want 3 trailing flags (storageDataPath, httpListenAddr, retentionPeriod), got %d: %v", len(trailing), trailing)
		}
	})
}
