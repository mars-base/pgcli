package podman

import (
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// TestVlagentArgsAndEnv pins the `podman run` argv for vlagent across both
// networking shapes. vlagent is configured entirely by CLI flags AFTER the
// image name (no config file, no env vars), so this function is the whole
// contract between a VlagentConfig and the container. The distinctive parts
// are the per-log-path read-only mounts (host path == container path) and the
// positional pairing of -fileCollector.glob with -fileCollector.excludeGlob.
func TestVlagentArgsAndEnv(t *testing.T) {
	base := func() *config.VlagentConfig {
		return &config.VlagentConfig{
			ContainerName:   "pgcli-vlagent-a1",
			Name:            "a1",
			HostPort:        9429,
			Listen:          "0.0.0.0",
			ImageTag:        config.DefaultVlagentImageTag,
			RemoteWriteURLs: []string{"http://127.0.0.1:9428/insert/native"},
		}
	}

	// mounts returns the -v specs of the argv.
	mounts := func(args []string) []string {
		var out []string
		for i, a := range args {
			if a == "-v" && i+1 < len(args) {
				out = append(out, args[i+1])
			}
		}
		return out
	}
	// imageIdx returns the position of the image tag, i.e. where podman's own
	// flag parsing stops and the vlagent binary's own trailing flags begin.
	imageIdx := func(args []string) int {
		return slices.Index(args, config.DefaultVlagentImageTag)
	}

	t.Run("linux host networking", func(t *testing.T) {
		vc := base()
		vc.LogPaths = []string{"/var/log/nginx", "/var/log/app/app.log"}
		vc.Includes = []string{"/var/log/nginx/*.log", "/var/log/app/app.log"}
		vc.Excludes = []string{"/var/log/nginx/*.gz", ""}
		args := vlagentArgsAndEnv(vc, false, "pgcli-net", "/home/u/.pgcli/addon/vlagent/a1/data")

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
		if slices.Contains(args, "--user") {
			t.Errorf("vlagent must not pass --user (the image runs as root): %v", args)
		}
		got := mounts(args)
		if !slices.Contains(got, "/home/u/.pgcli/addon/vlagent/a1/data:/var/lib/vlagent:z") {
			t.Errorf("data dir mount missing: %v", got)
		}
		// Log paths are mounted read-only at the IDENTICAL container path.
		if !slices.Contains(got, "/var/log/nginx:/var/log/nginx:ro") {
			t.Errorf("directory log path mount missing: %v", got)
		}
		if !slices.Contains(got, "/var/log/app/app.log:/var/log/app/app.log:ro") {
			t.Errorf("file log path mount missing: %v", got)
		}

		idx := imageIdx(args)
		if idx == -1 {
			t.Fatal("image tag not found in args")
		}
		if idx == len(args)-1 {
			t.Fatal("image tag must not be last — vlagent needs trailing CLI flags after the image name")
		}
		trailing := args[idx+1:]
		if !slices.Contains(trailing, "-tmpDataPath=/var/lib/vlagent") {
			t.Errorf("-tmpDataPath missing from trailing flags: %v", trailing)
		}
		if !slices.Contains(trailing, "-httpListenAddr=0.0.0.0:9429") {
			t.Errorf("-httpListenAddr missing from trailing flags: %v", trailing)
		}
		if slices.Index(trailing, "-remoteWrite.url=http://127.0.0.1:9428/insert/native") == -1 {
			t.Errorf("-remoteWrite.url missing from trailing flags: %v", trailing)
		}
		if !slices.Contains(trailing, "-fileCollector.glob=/var/log/nginx/*.log") ||
			!slices.Contains(trailing, "-fileCollector.glob=/var/log/app/app.log") {
			t.Errorf("-fileCollector.glob flags missing: %v", trailing)
		}
		// The empty exclude at position 2 is a placeholder: only the first
		// exclude is emitted.
		excludes := 0
		for _, a := range trailing {
			if strings.HasPrefix(a, "-fileCollector.excludeGlob=") {
				excludes++
			}
		}
		if excludes != 1 || !slices.Contains(trailing, "-fileCollector.excludeGlob=/var/log/nginx/*.gz") {
			t.Errorf("want exactly one excludeGlob /var/log/nginx/*.gz, got %v", trailing)
		}
	})

	t.Run("macOS bridge networking publishes port and widens a loopback bind", func(t *testing.T) {
		vc := base()
		vc.Listen = "127.0.0.1"
		args := vlagentArgsAndEnv(vc, true, "pgcli-net", "/data")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "pgcli-net") {
			t.Errorf("expected --network pgcli-net, got %v", args)
		}
		if !slices.Contains(args, "-p") || !slices.Contains(args, "9429:9429") {
			t.Errorf("bridge must publish 9429:9429, got %v", args)
		}
		if !slices.Contains(args, "-httpListenAddr=0.0.0.0:9429") {
			t.Errorf("bridge should widen loopback to 0.0.0.0, got %v", args)
		}
		if slices.Contains(args, "-httpListenAddr=127.0.0.1:9429") {
			t.Errorf("bridge must not keep the loopback bind: %v", args)
		}
	})

	t.Run("relay-only install has no file collector flags or log mounts", func(t *testing.T) {
		vc := base()
		args := vlagentArgsAndEnv(vc, false, "pgcli-net", "/data")

		for _, a := range args {
			if strings.HasPrefix(a, "-fileCollector.") {
				t.Errorf("relay-only install must not pass file collector flags, got %q", a)
			}
		}
		got := mounts(args)
		if len(got) != 1 || !strings.Contains(got[0], ":/var/lib/vlagent:z") {
			t.Errorf("relay-only install should mount only the data dir, got %v", got)
		}
	})

	t.Run("multiple remote write URLs replicate", func(t *testing.T) {
		vc := base()
		vc.RemoteWriteURLs = []string{
			"http://10.0.0.1:9428/insert/native",
			"http://10.0.0.2:9428/insert/native",
		}
		args := vlagentArgsAndEnv(vc, false, "pgcli-net", "/data")
		for _, u := range vc.RemoteWriteURLs {
			if !slices.Contains(args, "-remoteWrite.url="+u) {
				t.Errorf("missing -remoteWrite.url=%s in %v", u, args)
			}
		}
	})

	t.Run("empty first exclude is emitted as a positional placeholder", func(t *testing.T) {
		vc := base()
		vc.Includes = []string{"/var/log/a/*.log", "/var/log/b/*.log"}
		vc.Excludes = []string{"", "/var/log/b/*.gz"}
		args := vlagentArgsAndEnv(vc, false, "pgcli-net", "/data")

		var got []string
		for _, a := range args {
			if strings.HasPrefix(a, "-fileCollector.excludeGlob=") {
				got = append(got, strings.TrimPrefix(a, "-fileCollector.excludeGlob="))
			}
		}
		if len(got) != 2 || got[0] != "" || got[1] != "/var/log/b/*.gz" {
			t.Errorf("placeholders must preserve positions: got %q", got)
		}
	})

	t.Run("extra args appear last after image flags", func(t *testing.T) {
		vc := base()
		vc.ExtraArgs = []string{"-fileCollector.refreshInterval=5s", "-memory.allowedPercent=80"}
		args := vlagentArgsAndEnv(vc, false, "pgcli-net", "/data")

		idx := imageIdx(args)
		trailing := args[idx+1:]
		last2 := trailing[len(trailing)-2:]
		if !slices.Contains(last2, "-fileCollector.refreshInterval=5s") ||
			!slices.Contains(last2, "-memory.allowedPercent=80") {
			t.Errorf("ExtraArgs must be last: trailing=%v, last2=%v", trailing, last2)
		}
	})
}
