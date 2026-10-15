package podman

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// TestPrometheusArgsAndEnv pins the `podman run` argv for Prometheus across
// both networking shapes. Unlike Grafana (env-only), Prometheus is configured
// by a mounted config FILE plus trailing CLI flags AFTER the image name — so
// this function is the whole contract between a PrometheusConfig and the
// container, and the trailing-flag shape (nothing container-side may leak past
// the image name) is exactly what deserves pinning.
func TestPrometheusArgsAndEnv(t *testing.T) {
	base := func() *config.PrometheusConfig {
		return &config.PrometheusConfig{
			ContainerName: "pgcli-prometheus-p1",
			Name:          "p1",
			HostPort:      39090,
			Listen:        "0.0.0.0",
			ImageTag:      config.DefaultPrometheusImageTag,
			Retention:     "15d",
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
	// hasFlagValue reports whether "--flag=value" appears verbatim anywhere.
	hasFlagValue := func(args []string, want string) bool {
		return slices.Contains(args, want)
	}
	// imageIdx returns the position of the image tag, i.e. where podman's own
	// flag parsing stops and the prometheus binary's own trailing flags begin.
	imageIdx := func(args []string) int {
		return slices.Index(args, config.DefaultPrometheusImageTag)
	}

	t.Run("linux host networking", func(t *testing.T) {
		pc := base()
		args := prometheusArgsAndEnv(pc, false, "pgcli-net", "/home/u/.pgcli/addon/prometheus/p1/data", "/home/u/.pgcli/addon/prometheus/p1/prometheus.yml")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "host") {
			t.Errorf("expected --network host, got %v", args)
		}
		if slices.Contains(args, "-p") {
			t.Errorf("host networking must not publish ports, got %v", args)
		}
		if !slices.Contains(args, "--http-proxy=false") {
			t.Errorf("--http-proxy=false missing: %v", args)
		}
		if !hasFlagValue(args, "--restart") || !slices.Contains(args, "unless-stopped") {
			t.Errorf("--restart unless-stopped missing: %v", args)
		}
		if !hasMount(args, "/home/u/.pgcli/addon/prometheus/p1/data:/prometheus:z") {
			t.Errorf("data dir mount missing: %v", args)
		}
		if !hasMount(args, "/home/u/.pgcli/addon/prometheus/p1/prometheus.yml:/etc/prometheus/prometheus.yml:ro,z") {
			t.Errorf("config file mount missing (or not read-only): %v", args)
		}
		// Ownership contract with Grafana: NO --user flag — the image's baked
		// nobody (65534) must run, since the data dir arrives pre-chowned by
		// createContainer rather than being fixed up by the entrypoint.
		if slices.Contains(args, "--user") {
			t.Errorf("Prometheus must not pass --user (the image runs as 65534): %v", args)
		}

		idx := imageIdx(args)
		if idx == -1 {
			t.Fatal("image tag not found in args")
		}
		if idx == len(args)-1 {
			t.Fatal("image tag must not be last — prometheus needs trailing config flags after the image name")
		}
		trailing := args[idx+1:]
		if !hasFlagValue(trailing, "--config.file=/etc/prometheus/prometheus.yml") {
			t.Errorf("--config.file missing from trailing flags: %v", trailing)
		}
		if !hasFlagValue(trailing, "--web.listen-address=0.0.0.0:39090") {
			t.Errorf("--web.listen-address missing from trailing flags: %v", trailing)
		}
		if !hasFlagValue(trailing, "--storage.tsdb.path=/prometheus") {
			t.Errorf("--storage.tsdb.path missing from trailing flags: %v", trailing)
		}
		if !hasFlagValue(trailing, "--storage.tsdb.retention.time=15d") {
			t.Errorf("--storage.tsdb.retention.time missing from trailing flags: %v", trailing)
		}
		// THE security contract: reload happens via SIGHUP, so the
		// unauthenticated HTTP reload endpoint is never enabled.
		if slices.ContainsFunc(trailing, func(a string) bool {
			return strings.HasPrefix(a, "--web.enable-lifecycle")
		}) {
			t.Errorf("--web.enable-lifecycle must never be set (reload uses SIGHUP, not HTTP POST /-/reload): %v", trailing)
		}
	})

	t.Run("macOS bridge networking publishes port and keeps a non-loopback bind", func(t *testing.T) {
		pc := base()
		pc.Listen = "127.0.0.1" // even a loopback listen is widened under bridge
		args := prometheusArgsAndEnv(pc, true, "pgcli-net", "/data", "/cfg/prometheus.yml")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "pgcli-net") {
			t.Errorf("expected --network pgcli-net, got %v", args)
		}
		if !slices.Contains(args, "-p") || !slices.Contains(args, "39090:39090") {
			t.Errorf("bridge must publish 39090:39090, got %v", args)
		}
		// The published port can't reach a loopback-only bind.
		if !slices.Contains(args, "--web.listen-address=0.0.0.0:39090") {
			t.Errorf("bridge should widen loopback to 0.0.0.0, got %v", args)
		}
		if slices.Contains(args, "--web.listen-address=127.0.0.1:39090") {
			t.Errorf("bridge must not keep the loopback bind: %v", args)
		}
	})

	t.Run("explicit non-loopback listen passes through", func(t *testing.T) {
		pc := base()
		pc.Listen = "10.0.0.5"
		args := prometheusArgsAndEnv(pc, true, "pgcli-net", "/data", "/cfg/prometheus.yml")
		if !slices.Contains(args, "--web.listen-address=10.0.0.5:39090") {
			t.Errorf("explicit bind should pass through under bridge: %v", args)
		}
	})

	t.Run("empty retention omits the flag", func(t *testing.T) {
		pc := base()
		pc.Retention = ""
		args := prometheusArgsAndEnv(pc, false, "pgcli-net", "/data", "/cfg/prometheus.yml")
		idx := imageIdx(args)
		for _, a := range args[idx+1:] {
			if strings.HasPrefix(a, "--storage.tsdb.retention.time") {
				t.Errorf("empty retention should omit the flag entirely, got %q", a)
			}
		}
	})
}

func TestRenderPrometheusConfig(t *testing.T) {
	base := func() *config.PrometheusConfig {
		return &config.PrometheusConfig{
			Name:           "p1",
			HostPort:       9090,
			ScrapeInterval: "15s",
		}
	}

	t.Run("always-present self job with no extra scrape targets", func(t *testing.T) {
		pc := base()
		out, err := renderPrometheusConfig(pc)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "job_name: prometheus") {
			t.Errorf("self-monitoring job missing:\n%s", out)
		}
		if !strings.Contains(out, `"localhost:9090"`) {
			t.Errorf("self job target missing:\n%s", out)
		}
		if !strings.Contains(out, "scrape_interval: 15s") {
			t.Errorf("global scrape_interval missing:\n%s", out)
		}
	})

	t.Run("one job per spec, folding that spec's targets", func(t *testing.T) {
		pc := base()
		pc.Scrape = []string{"postgres-exporter=127.0.0.1:9187", "node=10.0.0.5:9100,10.0.0.6:9100"}
		out, err := renderPrometheusConfig(pc)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "job_name: postgres-exporter") {
			t.Errorf("named scrape job missing:\n%s", out)
		}
		if !strings.Contains(out, `"127.0.0.1:9187"`) {
			t.Errorf("single-target job body missing:\n%s", out)
		}
		// Multi-target spec renders as ONE job carrying both targets inline.
		if !strings.Contains(out, `job_name: node`) {
			t.Errorf("node job missing:\n%s", out)
		}
		if !strings.Contains(out, `targets: ["10.0.0.5:9100", "10.0.0.6:9100"]`) {
			t.Errorf("both node targets must share one inline targets list:\n%s", out)
		}
		if got := strings.Count(out, "job_name: node"); got != 1 {
			t.Errorf("want exactly one node job, got %d:\n%s", got, out)
		}
	})

	t.Run("same job name across specs folds into one job", func(t *testing.T) {
		pc := base()
		pc.Scrape = []string{"node=10.0.0.5:9100", "node=10.0.0.6:9100,10.0.0.5:9100"}
		out, err := renderPrometheusConfig(pc)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(out, "job_name: node"); got != 1 {
			t.Errorf("reused job name must fold into one job, got %d:\n%s", got, out)
		}
		// Both targets, first-appearance order, the repeat de-duplicated.
		if !strings.Contains(out, `targets: ["10.0.0.5:9100", "10.0.0.6:9100"]`) {
			t.Errorf("folded targets missing or mis-ordered (dedupe failed):\n%s", out)
		}
	})

	t.Run("malformed spec errors instead of rendering", func(t *testing.T) {
		pc := base()
		pc.Scrape = []string{"10.0.0.5:9100"} // no JOB= prefix
		if _, err := renderPrometheusConfig(pc); err == nil {
			t.Error("want an error for a spec with no job name, got nil")
		}
	})

	t.Run("empty scrape interval falls back to the default", func(t *testing.T) {
		pc := base()
		pc.ScrapeInterval = ""
		out, err := renderPrometheusConfig(pc)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "scrape_interval: 15s") {
			t.Errorf("want the 15s fallback, got:\n%s", out)
		}
	})
}

func TestParseScrapeSpec(t *testing.T) {
	cases := []struct {
		spec       string
		wantJob    string
		wantTarget []string
	}{
		{"node=10.0.0.5:9100", "node", []string{"10.0.0.5:9100"}},
		{"node=10.0.0.5:9100,10.0.0.6:9100", "node", []string{"10.0.0.5:9100", "10.0.0.6:9100"}},
		{" node-exporter = 10.0.0.5:9100 , 10.0.0.6:9100 ", "node-exporter", []string{"10.0.0.5:9100", "10.0.0.6:9100"}},
	}
	for _, c := range cases {
		job, targets, err := parseScrapeSpec(c.spec)
		if err != nil {
			t.Errorf("parseScrapeSpec(%q): %v", c.spec, err)
			continue
		}
		if job != c.wantJob || !slices.Equal(targets, c.wantTarget) {
			t.Errorf("parseScrapeSpec(%q) = (%q,%v), want (%q,%v)", c.spec, job, targets, c.wantJob, c.wantTarget)
		}
	}
	for _, bad := range []string{
		"10.0.0.5:9100",        // no job name at all
		"=10.0.0.5:9100",       // empty job name
		"node=",                // job but no targets
		"node= ,10.0.0.5:9100", // empty target between commas
	} {
		if _, _, err := parseScrapeSpec(bad); err == nil {
			t.Errorf("want an error for %q, got nil", bad)
		}
	}
}

func TestMergeScrapeSpecs(t *testing.T) {
	t.Run("folds appended targets into the same-named job", func(t *testing.T) {
		got, err := MergeScrapeSpecs(
			[]string{"node=10.0.0.5:9100,10.0.0.6:9100", "postgres-exporter=127.0.0.1:9187"},
			[]string{"node=10.0.0.7:9100"},
		)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"node=10.0.0.5:9100,10.0.0.6:9100,10.0.0.7:9100", "postgres-exporter=127.0.0.1:9187"}
		if !slices.Equal(got, want) {
			t.Errorf("merge = %v, want %v", got, want)
		}
	})

	t.Run("unseen job name appends at the end; targets dedupe", func(t *testing.T) {
		got, err := MergeScrapeSpecs(
			[]string{"node=10.0.0.5:9100"},
			[]string{"cache=10.0.0.9:9121", "node=10.0.0.5:9100"}, // the repeat must not double
		)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"node=10.0.0.5:9100", "cache=10.0.0.9:9121"}
		if !slices.Equal(got, want) {
			t.Errorf("merge = %v, want %v", got, want)
		}
	})

	t.Run("re-serialises a hand-split existing list canonically", func(t *testing.T) {
		// pg.yaml edited by hand into two specs sharing one job name: the merge
		// output must be the canonical one-spec-per-job form.
		got, err := MergeScrapeSpecs(
			[]string{"node=10.0.0.5:9100", "node=10.0.0.6:9100"},
			[]string{"node=10.0.0.7:9100"},
		)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"node=10.0.0.5:9100,10.0.0.6:9100,10.0.0.7:9100"}
		if !slices.Equal(got, want) {
			t.Errorf("merge = %v, want %v", got, want)
		}
	})

	t.Run("no-op append reproduces the stored bytes", func(t *testing.T) {
		// The install path compares the rendered config to skip a pointless
		// reload — a no-op append must therefore return the EXISTING canonical
		// list unchanged.
		existing := []string{"node=10.0.0.5:9100,10.0.0.6:9100", "pg=127.0.0.1:9187"}
		got, err := MergeScrapeSpecs(existing, []string{"node=10.0.0.5:9100"})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, existing) {
			t.Errorf("no-op append changed the list: %v, want %v", got, existing)
		}
	})

	t.Run("malformed inputs error before anything is written", func(t *testing.T) {
		if _, err := MergeScrapeSpecs([]string{"node=10.0.0.5:9100"}, []string{"10.0.0.9:9121"}); err == nil {
			t.Error("want an error for an appended spec with no job name, got nil")
		}
		if _, err := MergeScrapeSpecs([]string{"no-job-here"}, nil); err == nil {
			t.Error("want an error for a malformed EXISTING spec too (validate-first), got nil")
		}
	})
}

// TestProbeScrapeTargets exercises the host-side /metrics probe: a live
// exporter passes, a dead port is reported (not an error — the caller decides
// warn vs abort per flag), and a malformed spec is a hard error.
func TestProbeScrapeTargets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "up 1")
	}))
	defer srv.Close()
	live := strings.TrimPrefix(srv.URL, "http://")

	// A port that just closed refuses connections deterministically.
	deadSrv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := strings.TrimPrefix(deadSrv.URL, "http://")
	deadSrv.Close()

	unreachable, err := ProbeScrapeTargets([]string{"ok=" + live, "down=" + dead})
	if err != nil {
		t.Fatal(err)
	}
	if len(unreachable) != 1 || !strings.Contains(unreachable[0], dead) || !strings.Contains(unreachable[0], "down") {
		t.Errorf("want the dead target reported with its job, got %v", unreachable)
	}

	if _, err := ProbeScrapeTargets([]string{"no-job"}); err == nil {
		t.Error("want a hard error for a malformed spec, got nil")
	}
}

// TestWriteConfigRenderVsConfFile pins the core --conf-file contract: the
// mounted path is ALWAYS pgcli's managed copy, never the operator's original
// file (a single-file bind mount pins the source inode, so an in-place edit of
// the original would not propagate). Render mode writes the generated YAML;
// conf-file mode copies the user's file verbatim into that same managed path
// and must not touch the original.
func TestWriteConfigRenderVsConfFile(t *testing.T) {
	base := t.TempDir()
	m := &PrometheusManager{dataDir: base}

	t.Run("render mode writes the generated file", func(t *testing.T) {
		pc := &config.PrometheusConfig{
			Name:           "p1",
			HostPort:       9090,
			ScrapeInterval: "15s",
		}
		path, err := m.WriteConfig(pc)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(base, "addon", "prometheus", "p1", "prometheus.yml")
		if path != want {
			t.Errorf("path = %q, want %q", path, want)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "job_name: prometheus") {
			t.Errorf("rendered file missing the self job:\n%s", data)
		}
	})

	t.Run("conf-file mode copies verbatim into the managed path, original untouched", func(t *testing.T) {
		userFile := filepath.Join(t.TempDir(), "my.yml")
		userContent := "# operator's own file, must survive byte-for-byte\n" +
			"scrape_configs:\n  - job_name: only-the-operator-has-this\n"
		if err := os.WriteFile(userFile, []byte(userContent), 0644); err != nil {
			t.Fatal(err)
		}
		pc := &config.PrometheusConfig{Name: "p2", HostPort: 9090, ConfFile: userFile}

		path, err := m.WriteConfig(pc)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(base, "addon", "prometheus", "p2", "prometheus.yml")
		if path != want {
			t.Errorf("path = %q, want the managed copy %q (never the operator's original)", path, want)
		}
		if path == userFile {
			t.Fatal("conf-file mode must mount pgcli's copy, not the operator's original path")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != userContent {
			t.Errorf("managed copy differs from the operator's file:\n%s", data)
		}
		// The self-monitoring job must NOT appear — conf-file mode skips
		// rendering entirely, per the plan.
		if strings.Contains(string(data), "job_name: prometheus\n    static_configs") {
			t.Errorf("conf-file mode must not render pgcli's own jobs into the copy:\n%s", data)
		}

		orig, err := os.ReadFile(userFile)
		if err != nil {
			t.Fatal(err)
		}
		if string(orig) != userContent {
			t.Errorf("the operator's original file was modified:\n%s", orig)
		}
	})

	t.Run("conf-file mode rejects an empty file", func(t *testing.T) {
		emptyFile := filepath.Join(t.TempDir(), "empty.yml")
		if err := os.WriteFile(emptyFile, nil, 0644); err != nil {
			t.Fatal(err)
		}
		pc := &config.PrometheusConfig{Name: "p3", HostPort: 9090, ConfFile: emptyFile}
		if _, err := m.WriteConfig(pc); err == nil {
			t.Error("want an error for an empty conf-file, got nil")
		}
	})
}
