package podman

import (
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// TestVictoriaLogsArgsAndEnv pins the `podman run` argv for VictoriaLogs across
// both networking shapes. VictoriaLogs is configured entirely by CLI flags
// AFTER the image name (no config file, no env vars), so this function is the
// whole contract between a VictoriaLogsConfig and the container. The shape is
// the exact mirror of TestVictoriaMetricsArgsAndEnv except for retention:
// pgcli does not default it (an empty Retention omits -retentionPeriod so the
// binary's own default stands), which the subtests pin.
func TestVictoriaLogsArgsAndEnv(t *testing.T) {
	base := func() *config.VictoriaLogsConfig {
		return &config.VictoriaLogsConfig{
			ContainerName: "pgcli-victorialogs-l1",
			Name:          "l1",
			HostPort:      9428,
			Listen:        "0.0.0.0",
			ImageTag:      config.DefaultVictoriaLogsImageTag,
			Retention:     "30d",
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
	// flag parsing stops and the victoria-logs binary's own trailing flags begin.
	imageIdx := func(args []string) int {
		return slices.Index(args, config.DefaultVictoriaLogsImageTag)
	}

	t.Run("linux host networking", func(t *testing.T) {
		lc := base()
		args := victoriaLogsArgsAndEnv(lc, false, "pgcli-net", "/home/u/.pgcli/addon/victorialogs/l1/data")

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
		if !hasMount(args, "/home/u/.pgcli/addon/victorialogs/l1/data:/victoria-logs-data:z") {
			t.Errorf("data dir mount missing: %v", args)
		}
		// No --user flag: the image runs as root, no pre-chown needed.
		if slices.Contains(args, "--user") {
			t.Errorf("VictoriaLogs must not pass --user (the image runs as root): %v", args)
		}

		idx := imageIdx(args)
		if idx == -1 {
			t.Fatal("image tag not found in args")
		}
		if idx == len(args)-1 {
			t.Fatal("image tag must not be last — victoria-logs needs trailing CLI flags after the image name")
		}
		trailing := args[idx+1:]
		if !slices.Contains(trailing, "-storageDataPath=/victoria-logs-data") {
			t.Errorf("-storageDataPath missing from trailing flags: %v", trailing)
		}
		if !slices.Contains(trailing, "-httpListenAddr=0.0.0.0:9428") {
			t.Errorf("-httpListenAddr missing from trailing flags: %v", trailing)
		}
		if !slices.Contains(trailing, "-retentionPeriod=30d") {
			t.Errorf("-retentionPeriod missing from trailing flags: %v", trailing)
		}
	})

	t.Run("macOS bridge networking publishes port and keeps a non-loopback bind", func(t *testing.T) {
		lc := base()
		lc.Listen = "127.0.0.1" // even a loopback listen is widened under bridge
		args := victoriaLogsArgsAndEnv(lc, true, "pgcli-net", "/data")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "pgcli-net") {
			t.Errorf("expected --network pgcli-net, got %v", args)
		}
		if !slices.Contains(args, "-p") || !slices.Contains(args, "9428:9428") {
			t.Errorf("bridge must publish 9428:9428, got %v", args)
		}
		// The published port can't reach a loopback-only bind.
		if !slices.Contains(args, "-httpListenAddr=0.0.0.0:9428") {
			t.Errorf("bridge should widen loopback to 0.0.0.0, got %v", args)
		}
		if slices.Contains(args, "-httpListenAddr=127.0.0.1:9428") {
			t.Errorf("bridge must not keep the loopback bind: %v", args)
		}
	})

	t.Run("explicit non-loopback listen passes through", func(t *testing.T) {
		lc := base()
		lc.Listen = "10.0.0.5"
		args := victoriaLogsArgsAndEnv(lc, true, "pgcli-net", "/data")
		if !slices.Contains(args, "-httpListenAddr=10.0.0.5:9428") {
			t.Errorf("explicit bind should pass through under bridge: %v", args)
		}
	})

	t.Run("empty retention omits the flag", func(t *testing.T) {
		lc := base()
		lc.Retention = ""
		args := victoriaLogsArgsAndEnv(lc, false, "pgcli-net", "/data")
		idx := imageIdx(args)
		for _, a := range args[idx+1:] {
			if strings.HasPrefix(a, "-retentionPeriod") {
				t.Errorf("empty retention should omit the flag entirely (binary default stands), got %q", a)
			}
		}
	})

	t.Run("extra args appear last after image flags", func(t *testing.T) {
		lc := base()
		lc.ExtraArgs = []string{"-memory.allowedPercent=80", "-search.maxQueryDuration=60s"}
		args := victoriaLogsArgsAndEnv(lc, false, "pgcli-net", "/data")

		idx := imageIdx(args)
		trailing := args[idx+1:]
		if len(trailing) < 2 {
			t.Fatalf("trailing too short: %v", trailing)
		}
		last2 := trailing[len(trailing)-2:]
		if !slices.Contains(last2, "-memory.allowedPercent=80") ||
			!slices.Contains(last2, "-search.maxQueryDuration=60s") {
			t.Errorf("ExtraArgs must be last: trailing=%v, last2=%v", trailing, last2)
		}
	})

	t.Run("extra args can override earlier flags via last-wins", func(t *testing.T) {
		lc := base()
		lc.ExtraArgs = []string{"-retentionPeriod=90d"}
		args := victoriaLogsArgsAndEnv(lc, false, "pgcli-net", "/data")

		idx := imageIdx(args)
		trailing := args[idx+1:]
		counts := map[string]int{}
		for _, a := range trailing {
			if strings.HasPrefix(a, "-retentionPeriod") {
				counts[a]++
			}
		}
		if counts["-retentionPeriod=30d"] != 1 || counts["-retentionPeriod=90d"] != 1 {
			t.Errorf("expected both retention values (set + override), got %v in %v", counts, trailing)
		}
		if trailing[len(trailing)-1] != "-retentionPeriod=90d" {
			t.Errorf("override must be last: %v", trailing)
		}
	})

	t.Run("no retention no extra args produces clean argv", func(t *testing.T) {
		lc := base()
		lc.Retention = ""
		args := victoriaLogsArgsAndEnv(lc, false, "pgcli-net", "/data")
		idx := imageIdx(args)
		trailing := args[idx+1:]
		if len(trailing) != 2 {
			t.Errorf("empty retention: want 2 trailing flags (storageDataPath, httpListenAddr), got %d: %v", len(trailing), trailing)
		}
	})
}
