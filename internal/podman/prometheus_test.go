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
		args := prometheusArgsAndEnv(pc, false, "pgcli-net", "/home/u/.pgcli/addon/prometheus/p1/data", "/home/u/.pgcli/addon/prometheus/p1/prometheus.yml", "/home/u/.pgcli/addon/prometheus/p1/rules")

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
		args := prometheusArgsAndEnv(pc, true, "pgcli-net", "/data", "/cfg/prometheus.yml", "/rules")

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
		args := prometheusArgsAndEnv(pc, true, "pgcli-net", "/data", "/cfg/prometheus.yml", "/rules")
		if !slices.Contains(args, "--web.listen-address=10.0.0.5:39090") {
			t.Errorf("explicit bind should pass through under bridge: %v", args)
		}
	})

	t.Run("empty retention omits the flag", func(t *testing.T) {
		pc := base()
		pc.Retention = ""
		args := prometheusArgsAndEnv(pc, false, "pgcli-net", "/data", "/cfg/prometheus.yml", "/rules")
		idx := imageIdx(args)
		for _, a := range args[idx+1:] {
			if strings.HasPrefix(a, "--storage.tsdb.retention.time") {
				t.Errorf("empty retention should omit the flag entirely, got %q", a)
			}
		}
	})

	t.Run("no rule files means no rules mount", func(t *testing.T) {
		pc := base()
		args := prometheusArgsAndEnv(pc, false, "pgcli-net", "/data", "/cfg/prometheus.yml", "/rules")
		if hasMount(args, "/rules:/etc/prometheus/rules:ro,z") {
			t.Errorf("rules dir mount must be absent when RuleFiles is empty: %v", args)
		}
	})

	t.Run("rule files mount the rules dir read-only", func(t *testing.T) {
		pc := base()
		pc.RuleFiles = []string{"/srv/rules/alert.rules.yml"}
		args := prometheusArgsAndEnv(pc, false, "pgcli-net", "/data", "/cfg/prometheus.yml", "/rules")
		if !hasMount(args, "/rules:/etc/prometheus/rules:ro,z") {
			t.Errorf("rules dir mount missing (or not read-only): %v", args)
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

	// No --alertmanager-url and no --rule-file must render NEITHER the alerting
	// block, the derived job, nor rule_files — an empty list means "not wired",
	// never an empty section (Prometheus rejects an alertmanagers list with no
	// targets and a rule_files glob that matches nothing it can't see).
	t.Run("no alertmanager no rule files means no extra sections", func(t *testing.T) {
		out, err := renderPrometheusConfig(base())
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"alerting:", "job_name: alertmanager", "rule_files:", "remote_write:"} {
			if strings.Contains(out, want) {
				t.Errorf("rendered config must not contain %q when unset:\n%s", want, out)
			}
		}
	})

	t.Run("remote-write-url renders a remote_write section verbatim", func(t *testing.T) {
		pc := base()
		pc.RemoteWrite = []string{
			"http://127.0.0.1:8480/insert/0/prometheus/api/v1/write",
			"http://vm2:8428/api/v1/write",
		}
		out, err := renderPrometheusConfig(pc)
		if err != nil {
			t.Fatal(err)
		}
		// Each URL is emitted quoted on its own `- url:` line, in input order,
		// as a top-level section (not a scrape job, not under alerting:).
		if !strings.Contains(out, "remote_write:\n  - url: \"http://127.0.0.1:8480/insert/0/prometheus/api/v1/write\"\n  - url: \"http://vm2:8428/api/v1/write\"\n") {
			t.Errorf("remote_write section malformed:\n%s", out)
		}
		// No queue entries means no queue_config block at all.
		if strings.Contains(out, "queue_config:") {
			t.Errorf("queue_config must not appear without --remote-write-queue:\n%s", out)
		}
	})

	t.Run("remote-write-queue renders queue_config under every URL", func(t *testing.T) {
		pc := base()
		pc.RemoteWrite = []string{
			"http://127.0.0.1:8428/api/v1/write",
			"http://vm2:8428/api/v1/write",
		}
		pc.RemoteWriteQueue = []string{
			"max_samples_per_send=10000",
			"capacity=20000",
			"max_shards=30",
			"batch_send_deadline=30s",
			"max_backoff=2y",
		}
		out, err := renderPrometheusConfig(pc)
		if err != nil {
			t.Fatal(err)
		}
		// The tuning rides under BOTH entries — one shared shape, indented
		// two levels below `- url:`. Integers render bare, durations quoted.
		want := "remote_write:\n" +
			"  - url: \"http://127.0.0.1:8428/api/v1/write\"\n" +
			"    queue_config:\n" +
			"      max_samples_per_send: 10000\n" +
			"      capacity: 20000\n" +
			"      max_shards: 30\n" +
			"      batch_send_deadline: \"30s\"\n" +
			"      max_backoff: \"2y\"\n" +
			"  - url: \"http://vm2:8428/api/v1/write\"\n" +
			"    queue_config:\n" +
			"      max_samples_per_send: 10000\n" +
			"      capacity: 20000\n" +
			"      max_shards: 30\n" +
			"      batch_send_deadline: \"30s\"\n" +
			"      max_backoff: \"2y\"\n"
		if !strings.Contains(out, want) {
			t.Errorf("remote_write queue_config malformed:\n%s", out)
		}
	})

	t.Run("alertmanager-url renders an alerting block and a derived scrape job", func(t *testing.T) {
		pc := base()
		pc.Alertmanager = []string{"http://127.0.0.1:9093/", "am2:9093"}
		out, err := renderPrometheusConfig(pc)
		if err != nil {
			t.Fatal(err)
		}
		// alerting block, bare host normalized alongside the URL form.
		if !strings.Contains(out, "alerting:\n  alertmanagers:\n    - static_configs:\n") {
			t.Errorf("alerting block malformed:\n%s", out)
		}
		if !strings.Contains(out, `targets: ["127.0.0.1:9093", "am2:9093"]`) {
			t.Errorf("alerting targets (normalized) missing:\n%s", out)
		}
		// Derived scrape job over the SAME targets — watching AM is not opt-in.
		if !strings.Contains(out, "  - job_name: alertmanager\n    static_configs:\n      - targets: [\"127.0.0.1:9093\", \"am2:9093\"]") {
			t.Errorf("derived alertmanager scrape job missing/mismatched:\n%s", out)
		}
	})

	t.Run("portless alertmanager-url gets 9093", func(t *testing.T) {
		pc := base()
		pc.Alertmanager = []string{"am1"}
		out, err := renderPrometheusConfig(pc)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, `"am1:9093"`) {
			t.Errorf("portless entry should default to 9093:\n%s", out)
		}
	})

	t.Run("https alertmanager-url sets scheme on both block and job", func(t *testing.T) {
		pc := base()
		pc.Alertmanager = []string{"https://am1:9093/"}
		out, err := renderPrometheusConfig(pc)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "    - scheme: https\n") {
			t.Errorf("alerting scheme: https missing:\n%s", out)
		}
		if !strings.Contains(out, "  - job_name: alertmanager\n    scheme: https\n") {
			t.Errorf("scrape job scheme: https missing:\n%s", out)
		}
	})

	t.Run("mixed http and https alertmanager-url errors", func(t *testing.T) {
		pc := base()
		pc.Alertmanager = []string{"http://a:9093/", "https://b:9093/"}
		if _, err := renderPrometheusConfig(pc); err == nil {
			t.Error("want an error for mixed schemes, got nil")
		}
	})

	t.Run("malformed alertmanager-url errors instead of rendering", func(t *testing.T) {
		pc := base()
		pc.Alertmanager = []string{"not a url"}
		if _, err := renderPrometheusConfig(pc); err == nil {
			t.Error("want an error for a malformed URL, got nil")
		}
	})

	t.Run("rule_files render pointing at the container rules dir", func(t *testing.T) {
		pc := base()
		pc.RuleFiles = []string{"/srv/rules/alert.rules.yml", "/other/disk.rules.yml"}
		out, err := renderPrometheusConfig(pc)
		if err != nil {
			t.Fatal(err)
		}
		// basename only — the host dirs are irrelevant once copied flat.
		if !strings.Contains(out, "rule_files:\n  - \"/etc/prometheus/rules/alert.rules.yml\"\n  - \"/etc/prometheus/rules/disk.rules.yml\"\n") {
			t.Errorf("rule_files section missing or mis-pathed:\n%s", out)
		}
	})

	t.Run("duplicate rule file basenames error instead of clobbering", func(t *testing.T) {
		pc := base()
		pc.RuleFiles = []string{"/a/alert.rules.yml", "/b/alert.rules.yml"}
		if _, err := renderPrometheusConfig(pc); err == nil {
			t.Error("want an error for colliding rule basenames, got nil")
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
func TestAlertmanagerTargets(t *testing.T) {
	cases := []struct {
		in      []string
		want    []string
		scheme  string
		wantErr bool
	}{
		{nil, nil, "http", false},
		{[]string{"am1"}, []string{"am1:9093"}, "http", false},
		{[]string{"am1:19093"}, []string{"am1:19093"}, "http", false},
		{[]string{"http://127.0.0.1:9093/"}, []string{"127.0.0.1:9093"}, "http", false},
		{[]string{"https://am1:9093/alerting/path"}, []string{"am1:9093"}, "https", false},
		// Duplicates collapse after normalization; order is first-appearance.
		{[]string{"am2:9093", "http://am2:9093/"}, []string{"am2:9093"}, "http", false},
		// A bare entry is the http default, so it coexists with explicit http.
		{[]string{"am1", "http://am2:9093/"}, []string{"am1:9093", "am2:9093"}, "http", false},
		// A bare entry does NOT resolve a mixed-scheme clash against explicit https.
		{[]string{"http://a:9093/", "https://b:9093/"}, nil, "", true},
		{[]string{""}, nil, "", true},                // empty entry, no host at all
		{[]string{"ftp://am1:9093/"}, nil, "", true}, // unsupported scheme
		{[]string{"http://"}, nil, "", true},         // no host
		{[]string{"two words"}, nil, "", true},       // whitespace in a bare entry
		{[]string{"host/path"}, nil, "", true},       // a path without scheme is nonsense
	}
	for _, c := range cases {
		got, scheme, err := AlertmanagerTargets(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("AlertmanagerTargets(%v): want error, got (%v, %q)", c.in, got, scheme)
			}
			continue
		}
		if err != nil {
			t.Errorf("AlertmanagerTargets(%v): %v", c.in, err)
			continue
		}
		if !slices.Equal(got, c.want) || scheme != c.scheme {
			t.Errorf("AlertmanagerTargets(%v) = (%v, %q), want (%v, %q)", c.in, got, scheme, c.want, c.scheme)
		}
		if err := ValidateAlertmanagerURLs(c.in); (err != nil) != c.wantErr {
			t.Errorf("ValidateAlertmanagerURLs(%v) err=%v, wantErr=%v", c.in, err, c.wantErr)
		}
	}
}

func TestValidateRemoteWriteURLs(t *testing.T) {
	cases := []struct {
		in      []string
		wantErr bool
	}{
		{nil, false},
		{[]string{}, false},
		{[]string{"http://127.0.0.1:8480/insert/0/prometheus/api/v1/write"}, false},
		{[]string{"https://vm.example.com/api/v1/write"}, false},
		// A bare host:port is rejected — unlike Alertmanager targets, a
		// remote_write URL has no default scheme/port to fall back to (the path
		// is the write endpoint).
		{[]string{"127.0.0.1:8480"}, true},
		{[]string{""}, true},
		{[]string{"ftp://vm:8428/api/v1/write"}, true}, // unsupported scheme
		{[]string{"http://"}, true},                    // no host
	}
	for _, c := range cases {
		if err := ValidateRemoteWriteURLs(c.in); (err != nil) != c.wantErr {
			t.Errorf("ValidateRemoteWriteURLs(%v) err=%v, wantErr=%v", c.in, err, c.wantErr)
		}
	}
}

func TestValidateRemoteWriteQueue(t *testing.T) {
	cases := []struct {
		in      []string
		wantErr bool
	}{
		{nil, false},
		{[]string{}, false},
		{[]string{"capacity=20000", "max_shards=30", "max_samples_per_send=10000", "min_shards=2"}, false},
		{[]string{"batch_send_deadline=30s", "min_backoff=1s", "max_backoff=2y"}, false},
		// Compound durations follow model.Duration grammar.
		{[]string{"max_backoff=1m30s"}, false},
		{[]string{"capacity=abc"}, true}, // integer key, junk value
		{[]string{"capacity=-5"}, true},  // queue sizes must be positive
		{[]string{"capacity=0"}, true},
		{[]string{"batch_send_deadline=30"}, true}, // duration key, unitless
		{[]string{"batch_send_deadline=30x"}, true},
		{[]string{"enable_http2=true"}, true}, // not whitelisted — needs --conf-file
		{[]string{"bogus=1"}, true},
		{[]string{"capacity"}, true},                 // no KEY=VALUE
		{[]string{"=20000"}, true},                   // empty key
		{[]string{"capacity="}, true},                // empty value
		{[]string{"Capacity=1"}, true},               // keys are lowercase-exact
		{[]string{"capacity=1", "capacity=2"}, true}, // duplicate key
	}
	for _, c := range cases {
		if err := ValidateRemoteWriteQueue(c.in); (err != nil) != c.wantErr {
			t.Errorf("ValidateRemoteWriteQueue(%v) err=%v, wantErr=%v", c.in, err, c.wantErr)
		}
	}
}

func TestNormalizeRuleNames(t *testing.T) {
	got, err := normalizeRuleNames([]string{"/srv/rules/a.rules.yml", "/other/b.rules.yml", "c.json"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a.rules.yml", "b.rules.yml", "c.json"}; !slices.Equal(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}
	// Trailing slashes must not turn the dir itself into the basename.
	got, err = normalizeRuleNames([]string{"/srv/a.rules.yml/"})
	if err != nil || !slices.Equal(got, []string{"a.rules.yml"}) {
		t.Errorf("trailing slash: got (%v, %v)", got, err)
	}
	for _, bad := range [][]string{
		{""},
		{"/srv/x.rules.yml", "/other/x.rules.yml"}, // colliding basenames
		{"/srv", "/srv"}, // dir-as-basename collision path
	} {
		if _, err := normalizeRuleNames(bad); err == nil {
			t.Errorf("want an error for %v, got nil", bad)
		}
	}
}

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

// Rule files are copied in BOTH modes (the mount lives at the fixed
// /etc/prometheus/rules regardless of who renders the config), the managed dir
// tracks the list exactly, and originals are never touched.
func TestWriteConfigRuleFiles(t *testing.T) {
	base := t.TempDir()
	m := &PrometheusManager{dataDir: base}

	srcDir := t.TempDir()
	writeSrc := func(name, content string) string {
		t.Helper()
		p := filepath.Join(srcDir, name)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	rulesPath := func(name string) string {
		return filepath.Join(base, "addon", "prometheus", name, "rules")
	}

	t.Run("render mode copies rules and references them in the config", func(t *testing.T) {
		a := writeSrc("alert.rules.yml", "groups:\n- name: g\n")
		b := writeSrc("disk.rules.yml", "groups:\n- name: d\n")
		pc := &config.PrometheusConfig{Name: "r1", HostPort: 9090, RuleFiles: []string{a, b}}

		path, err := m.WriteConfig(pc)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "- \"/etc/prometheus/rules/alert.rules.yml\"") {
			t.Errorf("rendered config must reference the container-side copy:\n%s", data)
		}
		for _, name := range []string{"alert.rules.yml", "disk.rules.yml"} {
			got, err := os.ReadFile(filepath.Join(rulesPath("r1"), name))
			if err != nil {
				t.Fatalf("copy of %s missing: %v", name, err)
			}
			if len(got) == 0 {
				t.Errorf("copy of %s is empty", name)
			}
		}
		// Host dirs are irrelevant in the copy: basename only.
		if strings.Contains(string(data), srcDir) {
			t.Errorf("rendered config must not leak host source paths:\n%s", data)
		}
	})

	t.Run("conf-file mode copies rules too, without touching the file", func(t *testing.T) {
		user := writeSrc("my.yml", "global:\n  scrape_interval: 15s\n")
		rule := writeSrc("alert.rules.yml", "groups:\n- name: g\n")
		pc := &config.PrometheusConfig{Name: "r2", HostPort: 9090, ConfFile: user, RuleFiles: []string{rule}}

		if _, err := m.WriteConfig(pc); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(rulesPath("r2"), "alert.rules.yml")); err != nil {
			t.Errorf("conf-file mode must still copy rule files for the mount: %v", err)
		}
		// The copy verbatim — pgcli adds no rule_files: section of its own.
		data, err := os.ReadFile(filepath.Join(base, "addon", "prometheus", "r2", "prometheus.yml"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "rule_files:") {
			t.Errorf("conf-file mode must not inject rule_files into the copy:\n%s", data)
		}
	})

	t.Run("managed dir prunes to exactly the current list", func(t *testing.T) {
		a := writeSrc("keep.rules.yml", "groups:\n- name: k\n")
		stale := writeSrc("stale.rules.yml", "groups:\n- name: s\n")
		pc := &config.PrometheusConfig{Name: "r3", HostPort: 9090, RuleFiles: []string{a, stale}}
		if _, err := m.WriteConfig(pc); err != nil {
			t.Fatal(err)
		}
		pc.RuleFiles = []string{a}
		if _, err := m.WriteConfig(pc); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(rulesPath("r3"), "stale.rules.yml")); !os.IsNotExist(err) {
			t.Errorf("dropping a rule file must delete its stale managed copy (err=%v)", err)
		}
		if _, err := os.Stat(filepath.Join(rulesPath("r3"), "keep.rules.yml")); err != nil {
			t.Errorf("kept rule copy vanished: %v", err)
		}
		// And an empty list removes the dir entirely.
		pc.RuleFiles = nil
		if _, err := m.WriteConfig(pc); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(rulesPath("r3")); !os.IsNotExist(err) {
			t.Errorf("empty rule list must remove the rules dir (err=%v)", err)
		}
	})

	t.Run("bad rule files fail before prometheus.yml is rewritten", func(t *testing.T) {
		pc := &config.PrometheusConfig{Name: "r4", HostPort: 9090}
		goodPath, err := m.WriteConfig(pc)
		if err != nil {
			t.Fatal(err)
		}
		good, err := os.ReadFile(goodPath)
		if err != nil {
			t.Fatal(err)
		}

		empty := writeSrc("empty.rules.yml", "")
		missing := filepath.Join(srcDir, "nope.rules.yml")
		dupA := writeSrc("dup.rules.yml", "groups:\n- name: a\n")
		dupB := writeSrc2(t, srcDir, "dup.rules.yml")
		for _, bad := range [][]string{{empty}, {missing}, {dupA, dupB}} {
			pc.RuleFiles = bad
			if _, err := m.WriteConfig(pc); err == nil {
				t.Errorf("want an error for rule files %v, got nil", bad)
			}
		}
		// Nothing moved: prometheus.yml byte-identical, rules dir never created.
		after, err := os.ReadFile(goodPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(good) {
			t.Errorf("failed WriteConfig rewrote prometheus.yml anyway:\n%s", after)
		}
		if _, err := os.Stat(rulesPath("r4")); !os.IsNotExist(err) {
			t.Errorf("failed WriteConfig created a rules dir (err=%v)", err)
		}
	})
}

// writeSrc2 exists so the duplicate-basename case can name a file that collides
// with an earlier entry while still living in a distinct directory.
func writeSrc2(t *testing.T, dir, name string) string {
	t.Helper()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(sub, name)
	if err := os.WriteFile(p, []byte("groups:\n- name: dup\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}
