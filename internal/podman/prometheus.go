package podman

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// PrometheusManager manages standalone Prometheus containers — the metrics
// collection backend (https://prometheus.io/). Like Grafana it is a top-level,
// persistent web-UI addon on one host port with a host data dir that outlives
// the container; the two are natural partners (Prometheus scrapes/stores,
// Grafana visualises) and share the same lifecycle shape.
//
// It diverges from Grafana in HOW it is configured. Grafana is env-only
// (GF_*); Prometheus reads a config FILE (prometheus.yml) and takes its
// runtime tuning as CLI flags placed AFTER the image name (--config.file /
// --web.listen-address / --storage.tsdb.path / --storage.tsdb.retention.time).
// So this manager fuses the Grafana lifecycle (Ensure/Start/Remove/…) with the
// nginx/garage/haproxy "render a config file → bind-mount it read-only → pass
// its in-container path as a flag" pattern.
//
// prometheus.yml is either RENDERED by pgcli (a self-scrape job plus one static
// job per --scrape JOB, folding that JOB's targets into it), or, in --conf-file
// mode, the operator's own file is COPIED verbatim into the managed path and
// mounted. Either way the mounted path is the same, and pgcli never rewrites the
// operator's original file.
//
// Ownership matches Grafana's divergence and reuses its helpers: the image is
// USER nobody (uid/gid 65534) and its entrypoint does NO chown, so pgcli
// pre-chowns the host data dir to 65534:65534 via ensureDataDirOwner
// (`podman unshare chown` under rootless; the shared package-level chownTree on
// a rootful host when pgcli runs as root). The corollary for teardown: files
// land owned by the mapped uid, so Remove reclaims them through removeHostDir
// (its `podman unshare rm` fallback).
//
// There is NO login/password: Prometheus ships no authentication. Its HTTP API
// runs arbitrary PromQL, so a remote-exposed instance is guarded only by the
// network boundary (firewall / --listen 127.0.0.1). This is why Reload uses
// SIGHUP via podman rather than the HTTP POST /-/reload endpoint: staying off
// `--web.enable-lifecycle` never opens that unauthenticated mutating surface.
type PrometheusManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
	bridge  bool   // macOS: serve on the pgcli-net bridge with published ports
}

const (
	// prometheusContainerDataDir is the TSDB location the image expects under
	// --storage.tsdb.path; pgcli bind-mounts the host data dir here.
	prometheusContainerDataDir = "/prometheus"
	// prometheusContainerConfigFile is where prometheus.yml is mounted
	// read-only; --config.file points at this in-container path.
	prometheusContainerConfigFile = "/etc/prometheus/prometheus.yml"
	// prometheusContainerUID/GID are the image's baked nobody ids (65534) that
	// ensureDataDirOwner chowns the host data dir to.
	prometheusContainerUID = 65534
	prometheusContainerGID = 65534
)

// NewPrometheusManager creates a PrometheusManager. It works on both platforms:
// Linux serves over host networking, macOS over the pgcli-net bridge with the
// port published (proxyBindHost widening makes it reachable through the VM),
// exactly like Grafana. The upstream image is multi-arch.
func NewPrometheusManager(cfg *config.Config) (*PrometheusManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &PrometheusManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
		bridge:  platform.Detect() == platform.MacOS,
	}, nil
}

// resolveDataDir returns the instance's host TSDB dir: the explicit DataDir
// override when set, otherwise <base>/addon/prometheus/<name>/data. It holds the
// metrics and outlives the container — `pg addon remove` keeps it unless
// --clean-data, so reinstalling under the same name revives history.
func (m *PrometheusManager) resolveDataDir(pc *config.PrometheusConfig) string {
	if pc.DataDir != "" {
		return pc.DataDir
	}
	return filepath.Join(prometheusConfigDir(m.dataDir, pc.Name), "data")
}

// DataDir returns the resolved host data directory (for display).
func (m *PrometheusManager) DataDir(pc *config.PrometheusConfig) string {
	return m.resolveDataDir(pc)
}

// prometheusConfigDir is the host dir holding an instance's prometheus.yml (and,
// by default, its data/ subdir).
func prometheusConfigDir(baseDir, name string) string {
	return filepath.Join(baseDir, "addon", "prometheus", name)
}

// ConfigPath returns the path of the prometheus.yml copy pgcli mounts for
// instance pc (rendered or copied from the operator's file — same path either
// way). Callers snapshot it before WriteConfig overwrites it.
func (m *PrometheusManager) ConfigPath(pc *config.PrometheusConfig) string {
	return filepath.Join(prometheusConfigDir(m.dataDir, pc.Name), "prometheus.yml")
}

// RulesDir is the host dir holding pgcli's copies of the instance's --rule-file
// originals; it is bind-mounted read-only at /etc/prometheus/rules.
func (m *PrometheusManager) RulesDir(pc *config.PrometheusConfig) string {
	return filepath.Join(prometheusConfigDir(m.dataDir, pc.Name), "rules")
}

// renderPrometheusConfig builds the prometheus.yml text for a rendered-mode
// instance. Pure function of the config so it can be unit-tested without
// podman. It always emits a self-scrape job (so the server monitors itself and
// is never idle), then one static_configs job per SCRAPE JOB: specs are
// "JOB=host:port[,host:port...]" and share a job_name, so groupScrapeSpecs folds
// them into one job per name carrying every listed target (the natural shape for
// a fleet of identical exporters). Retention is NOT rendered here — it is a CLI
// flag (--storage.tsdb.retention.time), see prometheusArgsAndEnv.
//
// Alertmanager, when set, folds in two things: an `alerting:` section listing
// the normalized host:port targets (Prometheus pushes fired alerts there), and
// a matching `alertmanager` scrape job so Alertmanager's own /metrics is watched
// too. RuleFiles, when set, render a `rule_files:` section pointing at the
// read-only managed rules dir pgcli mounts at /etc/prometheus/rules.
func renderPrometheusConfig(pc *config.PrometheusConfig) (string, error) {
	interval := pc.ScrapeInterval
	if interval == "" {
		interval = "15s"
	}

	jobs, err := groupScrapeSpecs(pc.Scrape)
	if err != nil {
		return "", err
	}
	amTargets, amScheme, err := AlertmanagerTargets(pc.Alertmanager)
	if err != nil {
		return "", err
	}
	ruleNames, err := normalizeRuleNames(pc.RuleFiles)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("# Generated by pgcli for the prometheus addon — do not edit in place.\n")
	b.WriteString("# Change the pg.yaml prometheus entry and re-run\n")
	b.WriteString("# `pg addon install prometheus` instead; this file is rewritten.\n")
	b.WriteString("\n")
	b.WriteString("global:\n")
	fmt.Fprintf(&b, "  scrape_interval: %s\n", interval)
	if len(ruleNames) > 0 {
		b.WriteString("\nrule_files:\n")
		for _, name := range ruleNames {
			fmt.Fprintf(&b, "  - %q\n", prometheusContainerRulesDir+"/"+name)
		}
	}
	if len(amTargets) > 0 {
		b.WriteString("\nalerting:\n")
		b.WriteString("  alertmanagers:\n")
		// One list entry carrying every target: alertmanagers entries are
		// load-balanced/split across by Prometheus only at the replica level, so a
		// single static_configs with N targets is the natural "send to all of
		// these" shape (mirrors the operator files in the wild).
		if amScheme == "https" {
			b.WriteString("    - scheme: https\n")
			b.WriteString("      static_configs:\n")
		} else {
			b.WriteString("    - static_configs:\n")
		}
		fmt.Fprintf(&b, "        - targets: [%s]\n", quoteTargets(amTargets))
	}
	b.WriteString("\nscrape_configs:\n")
	// Self job: scrape this server's own /metrics. Host networking on Linux makes
	// localhost:<port> the host port directly; under the macOS bridge it is the
	// container's own listener — either way a self-scrape always resolves. It is
	// emitted first and carries pgcli's own name "prometheus".
	b.WriteString("  - job_name: prometheus\n")
	b.WriteString("    static_configs:\n")
	fmt.Fprintf(&b, "      - targets: [%q]\n", fmt.Sprintf("localhost:%d", pc.HostPort))

	for _, j := range jobs {
		b.WriteString("  - job_name: " + j.Name + "\n")
		b.WriteString("    static_configs:\n")
		b.WriteString("      - targets: [" + quoteTargets(j.Targets) + "]\n")
	}
	if len(amTargets) > 0 {
		// Derived from the same --alertmanager-url list as the alerting section
		// above, so watching Alertmanager is never a separate opt-in.
		b.WriteString("  - job_name: alertmanager\n")
		if amScheme == "https" {
			b.WriteString("    scheme: https\n")
		}
		b.WriteString("    static_configs:\n")
		fmt.Fprintf(&b, "      - targets: [%s]\n", quoteTargets(amTargets))
	}

	if len(pc.RemoteWrite) > 0 {
		// remote_write is a top-level section (like alerting:/rule_files:), not a
		// scrape job: each entry pushes a copy of every sample out to a long-term
		// store. The local TSDB keeps its own retention regardless, so a dead
		// remote only stalls the queue — Prometheus surfaces that itself.
		//
		// queue_config tuning rides as --remote-write-queue entries applied
		// identically under every URL (VictoriaMetrics' docs recommend larger
		// batches under load): per-remote differentiation still needs --conf-file.
		b.WriteString("\nremote_write:\n")
		for _, u := range pc.RemoteWrite {
			fmt.Fprintf(&b, "  - url: %s\n", quoteTargets([]string{u}))
			if len(pc.RemoteWriteQueue) > 0 {
				b.WriteString("    queue_config:\n")
				for _, e := range pc.RemoteWriteQueue {
					key, val, _ := strings.Cut(e, "=")
					if slices.Contains(remoteWriteQueueDurations, key) {
						fmt.Fprintf(&b, "      %s: %s\n", key, quoteTargets([]string{val}))
					} else {
						fmt.Fprintf(&b, "      %s: %s\n", key, val)
					}
				}
			}
		}
	}
	return b.String(), nil
}

// scrapeJob is one rendered static_configs job: a name and its (already
// de-duplicated) target list.
type scrapeJob struct {
	Name    string
	Targets []string
}

// prometheusContainerRulesDir is where pgcli bind-mounts the per-instance rules
// dir (<managed config dir>/rules, holding copies of the operator's --rule-file
// originals). It is mounted only when the instance has rule files.
const prometheusContainerRulesDir = "/etc/prometheus/rules"

// AlertmanagerTargets validates and normalizes the --alertmanager-url list into
// scrape/forward targets: each entry is a bare host[:port] or an
// http(s)://host[:port][/path] URL, and the portless form gets Alertmanager's
// conventional 9093. Duplicate entries collapse. The second return value is the
// web scheme — "http" or "https"; a bare entry is the http default. Mixed
// schemes are rejected because Prometheus's `scheme:` keys the alerting block
// and a scrape job as a whole, so one rendered config cannot serve both. HTTPS
// carries into the render as `scheme: https` but without TLS knobs (CA,
// skip-verify), which need --conf-file. ValidateAlertmanagerURLs runs the same
// check before the CLI writes anything.
func AlertmanagerTargets(urls []string) ([]string, string, error) {
	var targets []string
	scheme := ""
	for _, u := range urls {
		target, s := u, "http"
		if strings.Contains(u, "://") {
			parsed, err := url.Parse(u)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				return nil, "", fmt.Errorf("alertmanager URL %q: want host[:port] or http(s)://host[:port][/path]", u)
			}
			target, s = parsed.Host, parsed.Scheme
		} else if u == "" || strings.ContainsAny(u, " \t/\\\\") {
			return nil, "", fmt.Errorf("alertmanager URL %q: want host[:port] or http(s)://host[:port][/path]", u)
		}
		if host, _, _ := strings.Cut(target, ":"); host == "" {
			return nil, "", fmt.Errorf("alertmanager URL %q: no host", u)
		}
		if _, port, _ := strings.Cut(target, ":"); port == "" {
			target += ":9093" // Alertmanager's conventional web port
		}
		if scheme == "" {
			scheme = s
		} else if s != scheme {
			return nil, "", fmt.Errorf("alertmanager URLs mix http and https (%q vs %q) — one rendered config carries a single scheme, give them all the same one", scheme, s)
		}
		if !slices.Contains(targets, target) {
			targets = append(targets, target)
		}
	}
	if scheme == "" {
		scheme = "http"
	}
	return targets, scheme, nil
}

// ValidateAlertmanagerURLs checks every --alertmanager-url entry (bare
// host[:port] or http/https URL) WITHOUT probing connectivity — Prometheus
// discovers dead alertmanagers through its own target health at alert time. A
// malformed entry returns an error naming it; the CLI treats that as
// abort-before-write.
func ValidateAlertmanagerURLs(urls []string) error {
	_, _, err := AlertmanagerTargets(urls)
	return err
}

// ValidateRemoteWriteURLs checks each --remote-write-url entry the same way
// ValidateAlertmanagerURLs guards --alertmanager-url: format only, no
// connectivity probe (Prometheus surfaces a stuck remote-write queue on its
// own /rules and in logs). Unlike Alertmanager targets, a remote_write URL is
// rendered verbatim and must be a complete http(s):// URL — the path carries
// the write endpoint (/api/v1/write, or /insert/0/prometheus/api/v1/write on
// the cluster edition), so there is no portless-default form to offer.
func ValidateRemoteWriteURLs(urls []string) error {
	for _, u := range urls {
		parsed, err := url.Parse(u)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("remote-write URL %q: want a full http(s)://host[:port]/path URL", u)
		}
	}
	return nil
}

// remoteWriteQueueInts / remoteWriteQueueDurations are the whitelisted
// --remote-write-queue keys: Prometheus's queue_config block has more fields
// (metadata config, enable_http2, tls_config…), but they are either rarely
// tuned or need more than a scalar value — an operator who needs those drops to
// --conf-file. Keys must be exactly lowercase (matching Prometheus's own
// config style), like every other render knob.
var (
	remoteWriteQueueInts      = []string{"capacity", "max_shards", "min_shards", "max_samples_per_send"}
	remoteWriteQueueDurations = []string{"batch_send_deadline", "min_backoff", "max_backoff"}
)

// ValidateRemoteWriteQueue checks every --remote-write-queue KEY=VALUE entry:
// the key must be one of the whitelisted queue_config scalars and the value an
// integer (queue sizes/shards) or a Prometheus-style duration (timeouts) —
// model.Duration grammar (a sum of `[0-9]+(\.[0-9]+)?(ms|s|m|h|d|w|y)` terms,
// e.g. "5s", "1m30s", "2y"). Duplicate keys and a bare --remote-write-queue
// without --remote-write-url are rejected the same way, all BEFORE anything is
// written, so a typo never reaches the rendered file.
func ValidateRemoteWriteQueue(entries []string) error {
	seen := map[string]bool{}
	for _, e := range entries {
		key, val, ok := strings.Cut(e, "=")
		if !ok || key == "" || val == "" {
			return fmt.Errorf("remote-write queue %q: want KEY=VALUE", e)
		}
		if seen[key] {
			return fmt.Errorf("remote-write queue key %q given twice", key)
		}
		seen[key] = true
		switch {
		case slices.Contains(remoteWriteQueueInts, key):
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return fmt.Errorf("remote-write queue %q: %s wants a positive integer", e, key)
			}
		case slices.Contains(remoteWriteQueueDurations, key):
			if !isPromDuration(val) {
				return fmt.Errorf("remote-write queue %q: %s wants a duration like 5s, 1m or 1m30s", e, key)
			}
		default:
			return fmt.Errorf("remote-write queue key %q: want one of %s", key,
				strings.Join(append(append([]string{}, remoteWriteQueueInts...), remoteWriteQueueDurations...), ", "))
		}
	}
	return nil
}

// isPromDuration matches Prometheus's model.Duration grammar: one or more
// decimal-plus-unit terms concatenated (ms|s|m|h|d|w|y). The whole string is
// anchored so a lone "5" or trailing junk fails.
func isPromDuration(s string) bool {
	for len(s) > 0 {
		i := 0
		for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
			i++
		}
		num := s[:i]
		if num == "" || num == "." || strings.Count(num, ".") > 1 {
			return false
		}
		s = s[i:]
		switch {
		case strings.HasPrefix(s, "ms"):
			s = s[2:]
		case len(s) > 0 && strings.ContainsRune("smhwdy", rune(s[0])):
			// model.Duration units beyond ms/s/m/h: d (days), w (weeks), y (years)
			// — the VM docs' load-tuning examples use 2y-style values for
			// max_backoff, and the render is verbatim either way.
			s = s[1:]
		default:
			return false
		}
	}
	return true
}

// normalizeRuleNames maps each --rule-file host path to its name inside the
// managed rules dir (a flat copy, so the basename is the name). Two source
// files sharing a basename would silently clobber each other, so the pair is
// rejected before anything is written; the returned slice keeps input order.
func normalizeRuleNames(paths []string) ([]string, error) {
	seen := make([]string, 0, len(paths))
	first := map[string]string{} // basename → first source path claiming it
	for _, p := range paths {
		base := filepath.Base(strings.TrimRight(p, string(filepath.Separator)))
		if p == "" || base == "." || base == ".." || base == string(filepath.Separator) {
			return nil, fmt.Errorf("rule file %q: cannot derive a file name from it", p)
		}
		if earlier, dup := first[base]; dup {
			return nil, fmt.Errorf("rule files %q and %q share the name %q — the managed copy would overwrite one with the other; rename one source file and retry", earlier, p, base)
		}
		first[base] = p
		seen = append(seen, base)
	}
	return seen, nil
}

// groupScrapeSpecs parses each "JOB=host:port[,host:port...]" spec and folds the
// results into one scrapeJob per distinct job name, preserving first-appearance
// order of both jobs and targets and de-duplicating a target repeated within a
// job (the same target across two jobs is NOT a duplicate — it is scraped twice,
// once per job). It is the single grouping used by both the renderer and the
// append merge, so a hand-edited pg.yaml that reuses a job name across two
// entries still renders as one job. A malformed spec (no name, empty target)
// returns an error and nothing partial.
func groupScrapeSpecs(specs []string) ([]scrapeJob, error) {
	var jobs []scrapeJob
	index := map[string]int{} // job name → position in jobs
	for _, spec := range specs {
		name, targets, err := parseScrapeSpec(spec)
		if err != nil {
			return nil, err
		}
		i, seen := index[name]
		if !seen {
			index[name] = len(jobs)
			i = len(jobs)
			jobs = append(jobs, scrapeJob{Name: name})
		}
		for _, t := range targets {
			if !slices.Contains(jobs[i].Targets, t) {
				jobs[i].Targets = append(jobs[i].Targets, t)
			}
		}
	}
	return jobs, nil
}

// quoteTargets renders a targets slice as the inline YAML flow-sequence body
// (the quoted, comma-joined elements between the brackets), so a multi-target
// job reads as one line: `targets: ["a:1", "b:2"]`.
func quoteTargets(targets []string) string {
	parts := make([]string, len(targets))
	for i, t := range targets {
		parts[i] = fmt.Sprintf("%q", t)
	}
	return strings.Join(parts, ", ")
}

// parseScrapeSpec turns one --scrape value into a job_name and its target list.
// The ONLY accepted form is "JOB=host:port[,host:port...]" — the job name is
// REQUIRED, and one spec may carry several comma-separated targets. The
// job_name is emitted as a YAML plain scalar, so a name with spaces or YAML
// metacharacters is the operator's responsibility — it is a label, not data
// pgcli interprets. Target de-duplication happens in groupScrapeSpecs, not here.
func parseScrapeSpec(spec string) (job string, targets []string, err error) {
	name, list, ok := strings.Cut(spec, "=")
	job = strings.TrimSpace(name)
	if !ok || job == "" {
		return "", nil, fmt.Errorf("scrape spec %q has no job name (want JOB=host:port[,host:port...])", spec)
	}
	for _, t := range strings.Split(list, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			return "", nil, fmt.Errorf("empty target in scrape spec %q", spec)
		}
		targets = append(targets, t)
	}
	if len(targets) == 0 {
		return "", nil, fmt.Errorf("scrape spec %q has no targets", spec)
	}
	return job, targets, nil
}

// MergeScrapeSpecs folds --scrape-append values into an instance's stored
// --scrape list. Both lists are parsed and validated FIRST (through
// groupScrapeSpecs), so a malformed value — e.g. a spec missing its job name —
// fails the whole merge before anything is written; the install path then leaves
// both pg.yaml and the running container untouched rather than half-applying a
// bad target list.
//
// The merge is job-aware: an appended job name folds its targets into the
// existing job of that name (de-duplicating targets already present), and an
// unseen job name appends at the end. The result is re-serialised canonically —
// one "JOB=t1,t2,..." spec per job — so a stored list built up over several
// appends stays tidy and an append that changes nothing reproduces the exact
// input bytes (keeping the unchanged-config check, and thus the reload skip,
// working).
func MergeScrapeSpecs(existing, appended []string) ([]string, error) {
	existingJobs, err := groupScrapeSpecs(existing)
	if err != nil {
		return nil, fmt.Errorf("existing scrape list: %w", err)
	}
	appendedJobs, err := groupScrapeSpecs(appended)
	if err != nil {
		return nil, fmt.Errorf("--scrape-append: %w", err)
	}

	// Seed with the existing jobs, then fold appended jobs in by name.
	merged := make([]scrapeJob, len(existingJobs))
	copy(merged, existingJobs)
	index := map[string]int{}
	for i, j := range merged {
		// Copy the target slice so appending a target to an existing job does not
		// mutate the shared backing array behind existingJobs.
		merged[i].Targets = append([]string(nil), j.Targets...)
		index[j.Name] = i
	}
	for _, a := range appendedJobs {
		i, seen := index[a.Name]
		if !seen {
			index[a.Name] = len(merged)
			merged = append(merged, scrapeJob{Name: a.Name, Targets: append([]string(nil), a.Targets...)})
			continue
		}
		for _, t := range a.Targets {
			if !slices.Contains(merged[i].Targets, t) {
				merged[i].Targets = append(merged[i].Targets, t)
			}
		}
	}

	out := make([]string, 0, len(merged))
	for _, j := range merged {
		out = append(out, j.Name+"="+strings.Join(j.Targets, ","))
	}
	return out, nil
}

// scrapeProbeTimeout bounds one /metrics GET. A target that is up answers in
// milliseconds; anything slower than this is effectively unreachable, and we do
// not want a hung exporter to stall the command.
const scrapeProbeTimeout = 3 * time.Second

// ProbeScrapeTargets GETs each rendered-mode scrape target's /metrics endpoint
// from the host and returns a human-readable, newline-separated list of the
// targets that are unreachable or do not answer 200 (empty when all are fine).
// This is deliberately NOT an error and the install path only WARNS with it:
// a target may be down transiently, or reachable from the container but not the
// host, and Prometheus itself already surfaces DOWN targets — so a non-responsive
// endpoint must not block an install that is otherwise correct.
//
// The dial is from the HOST, not the container. On Linux (host networking) that
// is the same namespace the container scrapes from, so the verdict matches what
// Prometheus will see. On the macOS bridge it is NOT — the container's
// 127.0.0.1 is its own loopback — so the caller skips this entirely there rather
// than warn about targets the container could actually reach.
//
// It reports all unreachable targets at once so an operator fixing a batch sees
// the whole list in one pass. A malformed --scrape spec IS returned as a second
// error value — that one is a hard error, since it means the rendered YAML would
// be wrong, not merely that an endpoint is down.
func ProbeScrapeTargets(scrape []string) (unreachable []string, err error) {
	if len(scrape) == 0 {
		return nil, nil
	}
	client := &http.Client{Timeout: scrapeProbeTimeout}
	for _, spec := range scrape {
		job, targets, perr := parseScrapeSpec(spec)
		if perr != nil {
			return nil, perr
		}
		for _, target := range targets {
			url := "http://" + target + "/metrics"
			resp, gerr := client.Get(url)
			if gerr != nil {
				unreachable = append(unreachable, fmt.Sprintf("%s [%s] — cannot connect (%v)", target, job, gerr))
				continue
			}
			// Drain and close so the connection can be reused; the body is discarded
			// either way — a 200 with no metric lines is still an exporter that
			// answered, which is all this probe claims to check.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				unreachable = append(unreachable, fmt.Sprintf("%s [%s] — GET /metrics returned %d, want 200", target, job, resp.StatusCode))
			}
		}
	}
	return unreachable, nil
}

// WriteConfig renders prometheus.yml (or copies the operator's --conf-file) into
// <base>/addon/prometheus/<name>/. In conf-file mode the user's file is copied
// verbatim into the managed path — pgcli never rewrites it, and it is the
// COPY (not the original) that gets mounted, because a single-file bind mount
// pins the source inode so an in-place edit of the original would not
// propagate. The file carries no secrets (only scrape targets and intervals),
// so 0644 — matching nginx/haproxy and keeping it readable by the container's
// nobody uid under rootless podman's mapping.
//
// RuleFiles are copied into <dir>/rules in BOTH modes (same inode-pinning
// rationale, and the mount lives at the fixed /etc/prometheus/rules): the
// rendered config references the copies automatically, and in conf-file mode
// the operator's file decides whether the dir is used at all — a stale glob
// is why the copy is pruned to exactly the current set on every write.
func (m *PrometheusManager) WriteConfig(pc *config.PrometheusConfig) (string, error) {
	dir := prometheusConfigDir(m.dataDir, pc.Name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating prometheus config dir: %w", err)
	}
	path := filepath.Join(dir, "prometheus.yml")

	// Read everything first so a bad source fails before any managed file moves.
	ruleNames, err := normalizeRuleNames(pc.RuleFiles)
	if err != nil {
		return "", err
	}
	ruleData := make([][]byte, len(pc.RuleFiles))
	for i, src := range pc.RuleFiles {
		data, err := os.ReadFile(src)
		if err != nil {
			return "", fmt.Errorf("reading rule file %s: %w", src, err)
		}
		if len(data) == 0 {
			return "", fmt.Errorf("rule file %s is empty", src)
		}
		ruleData[i] = data
	}
	var confData []byte
	if pc.ConfFile != "" {
		data, err := os.ReadFile(pc.ConfFile)
		if err != nil {
			return "", fmt.Errorf("reading user conf file %s: %w", pc.ConfFile, err)
		}
		if len(data) == 0 {
			return "", fmt.Errorf("user conf file %s is empty", pc.ConfFile)
		}
		confData = data
	}

	if err := m.writeRulesDir(pc, ruleNames, ruleData); err != nil {
		return "", err
	}

	if pc.ConfFile != "" {
		if err := os.WriteFile(path, confData, 0644); err != nil {
			return "", fmt.Errorf("writing prometheus.yml: %w", err)
		}
		return path, nil
	}

	cfg, err := renderPrometheusConfig(pc)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(cfg), 0644); err != nil {
		return "", fmt.Errorf("writing prometheus.yml: %w", err)
	}
	return path, nil
}

// writeRulesDir materializes the managed rules dir as EXACTLY ruleNames (paired
// by index with the already-read ruleData): write/overwrite each copy 0644,
// delete anything else in the dir. An empty set removes the dir outright —
// nothing mounts it then, and leaving a stale dir around would resurrect old
// rules the moment the operator re-adds --rule-file with a glob.
func (m *PrometheusManager) writeRulesDir(pc *config.PrometheusConfig, ruleNames []string, ruleData [][]byte) error {
	rulesDir := m.RulesDir(pc)
	if len(ruleNames) == 0 {
		if err := os.RemoveAll(rulesDir); err != nil {
			return fmt.Errorf("removing stale rules dir %s: %w", rulesDir, err)
		}
		return nil
	}
	if err := os.MkdirAll(rulesDir, 0755); err != nil {
		return fmt.Errorf("creating prometheus rules dir: %w", err)
	}
	keep := make(map[string]bool, len(ruleNames))
	for i, name := range ruleNames {
		keep[name] = true
		if err := os.WriteFile(filepath.Join(rulesDir, name), ruleData[i], 0644); err != nil {
			return fmt.Errorf("writing rule file %s: %w", name, err)
		}
	}
	entries, err := os.ReadDir(rulesDir)
	if err != nil {
		return fmt.Errorf("listing prometheus rules dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || keep[e.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(rulesDir, e.Name())); err != nil {
			return fmt.Errorf("removing stale rule copy %s: %w", e.Name(), err)
		}
	}
	return nil
}

// EnsureImage pulls the Prometheus image if it is not present locally (pull-only:
// public docker.io/prom tags; pgcli never builds them). Offline hosts pre-seed
// them with `podman load` like the core images.
func (m *PrometheusManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling Prometheus image %s (is docker.io reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or restarts the Prometheus container (install
// semantics: stop/rm/recreate so a re-rendered config or changed port takes
// effect). The data and config dirs are created; neither is deleted here.
func (m *PrometheusManager) EnsureContainer(pc *config.PrometheusConfig) error {
	containerName := pc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> Prometheus container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping Prometheus container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing Prometheus container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale Prometheus container: %w", err)
			}
		}
	}

	if err := m.createContainer(pc); err != nil {
		return err
	}
	fmt.Println("  [OK] Prometheus container started")
	return nil
}

// StartContainer starts the Prometheus container without regenerating config
// (autostart-on-boot path): running → no-op; exists but stopped → podman start,
// recreating on improper state; missing → create. Like nginx/haproxy it refuses
// to create from scratch when the config file is absent (install writes it).
func (m *PrometheusManager) StartContainer(pc *config.PrometheusConfig) error {
	containerName := pc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] Prometheus %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] Prometheus %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] Prometheus %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper Prometheus container: %w", err)
		}
	}

	if _, err := os.Stat(m.ConfigPath(pc)); err != nil {
		return fmt.Errorf("prometheus config not found -- run 'pg addon install prometheus' first")
	}

	if err := m.createContainer(pc); err != nil {
		return err
	}
	fmt.Printf("  [OK] Prometheus %s started\n", containerName)
	return nil
}

// prometheusArgsAndEnv builds the full `podman run` argv for a Prometheus
// instance. Pure function (no podman calls) so the mount + trailing-flag shape
// is unit-testable:
//   - NO --user flag: the image's baked nobody (65534) is what must run — the
//     data dir arrives pre-chowned by createContainer (see PrometheusManager).
//   - Two mounts, plus a third when the instance has rule files: the writable
//     data dir at /prometheus (:z), the rendered/copied config file read-only at
//     /etc/prometheus/prometheus.yml (:ro,z), and the managed rules dir read-only
//     at /etc/prometheus/rules (:ro,z). A directory mount (not a single-file pin)
//     is fine here because pgcli fully re-materializes that dir on every write.
//   - Everything after ImageTag is the `prometheus` binary's OWN flags — podman
//     stops parsing container flags at the image name, so these reach the
//     process. `--config.file` names the in-container mount path;
//     `--web.listen-address` pins the listener to the assigned host port (bind
//     widened through proxyBindHost for the macOS bridge);
//     `--storage.tsdb.path` points at the data mount; retention is appended only
//     when set. `--web.enable-lifecycle` is deliberately ABSENT: reload happens
//     via SIGHUP, so the unauthenticated HTTP reload endpoint is never exposed.
//
// dataDir, configPath and rulesDir are the caller's already-resolved host paths,
// so this stays a pure function of its arguments. rulesDir is mounted only when
// pc.RuleFiles is non-empty (WriteConfig keeps that dir in lockstep with the list).
func prometheusArgsAndEnv(pc *config.PrometheusConfig, bridge bool, network, dataDir, configPath, rulesDir string) []string {
	args := []string{"run", "-d", "--name", pc.ContainerName}
	args = append(args, netFlags(bridge, network, pc.HostPort)...)
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(dataDir), prometheusContainerDataDir),
		"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(configPath), prometheusContainerConfigFile),
	)
	if len(pc.RuleFiles) > 0 {
		args = append(args,
			"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(rulesDir), prometheusContainerRulesDir),
		)
	}
	args = append(args, pc.ImageTag)
	args = append(args,
		"--config.file="+prometheusContainerConfigFile,
		fmt.Sprintf("--web.listen-address=%s:%d", proxyBindHost(bridge, pc.Listen), pc.HostPort),
		"--storage.tsdb.path="+prometheusContainerDataDir,
	)
	if pc.Retention != "" {
		args = append(args, "--storage.tsdb.retention.time="+pc.Retention)
	}
	return args
}

// createContainer runs a Prometheus instance. It creates the data dir, writes
// (renders or copies) the config, chowns the data dir to the image's 65534, then
// starts the container. The config file is host-owned and mounted read-only, so
// it needs no chown; only the writable TSDB dir does.
func (m *PrometheusManager) createContainer(pc *config.PrometheusConfig) error {
	dataDir := m.resolveDataDir(pc)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("creating Prometheus data dir %s: %w", dataDir, err)
	}
	configPath, err := m.WriteConfig(pc)
	if err != nil {
		return err
	}
	if err := m.ensureDataDirOwner(dataDir); err != nil {
		return err
	}

	args := prometheusArgsAndEnv(pc, m.bridge, m.cfg.Podman.Network, dataDir, configPath, m.RulesDir(pc))
	slog.Debug("podman prometheus run", "args", args)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating Prometheus container: %w", err)
	}
	return nil
}

// ensureDataDirOwner chowns the host data dir to the image's baked 65534:65534.
// Rootless podman maps container uid 65534 to a subordinate host uid, so the
// chown goes through `podman unshare chown`; a plain host os.Chown would set the
// wrong id. Rootful podman (only reachable by running pgcli as root, since the
// manager shells out to the plain `podman` CLI) maps 65534 directly and refuses
// `podman unshare`, so retry with the shared chownTree. Same contract as
// GrafanaManager.ensureDataDirOwner — only the ids differ.
func (m *PrometheusManager) ensureDataDirOwner(dataDir string) error {
	err := podmanUnshareChown(m.podman, prometheusContainerUID, prometheusContainerGID, dataDir)
	if err == nil {
		return nil
	}
	if os.Geteuid() == 0 {
		if chownErr := chownTree(prometheusContainerUID, prometheusContainerGID, dataDir); chownErr == nil {
			return nil
		}
	}
	return fmt.Errorf("chowning Prometheus data dir %s to %d:%d (Prometheus's container user): %w — pgcli chowns this automatically under rootless podman, and directly when it runs as root on a rootful host; a manual `sudo chown -R %d:%d %s` alone does not clear this error, since pgcli re-checks ownership on every install attempt — run pgcli as root, or switch to rootless podman",
		dataDir, prometheusContainerUID, prometheusContainerGID, err, prometheusContainerUID, prometheusContainerGID, dataDir)
}

// Reload applies config changes to a running instance without recreating it.
// Prometheus reloads prometheus.yml on SIGHUP, so pgcli rewrites the mounted
// copy (re-render in rendered mode, re-copy the operator's file in conf-file
// mode) and signals the container — the equivalent of haproxy's SIGUSR2 reload.
// It uses podman signalling rather than the HTTP POST /-/reload endpoint so no
// unauthenticated mutating surface is exposed (see PrometheusManager).
//
// A rendered-mode instance's config is regenerated on every install, so reload
// is only meaningful once the file on disk differs from what the process last
// read; the common case is conf-file mode, where the operator edits their own
// prometheus.yml and reloads.
func (m *PrometheusManager) Reload(pc *config.PrometheusConfig) error {
	running, err := m.containerRunning(pc.ContainerName)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("prometheus container %s is not running — run 'pg addon start prometheus --name %s' first", pc.ContainerName, pc.Name)
	}

	if _, err := m.WriteConfig(pc); err != nil {
		return err
	}
	return m.SignalReload(pc)
}

// SignalReload sends SIGHUP to a running instance, which makes Prometheus reread
// the prometheus.yml already on disk. Assumes the file is in place; Reload and
// the install path both write it first. Refuses if the container isn't running.
func (m *PrometheusManager) SignalReload(pc *config.PrometheusConfig) error {
	running, err := m.containerRunning(pc.ContainerName)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("prometheus container %s is not running — cannot hot-reload (a fresh instance starts normally)", pc.ContainerName)
	}
	fmt.Println("-> Reloading Prometheus (SIGHUP)...")
	if _, err := m.run("kill", "--signal", "HUP", pc.ContainerName); err != nil {
		return fmt.Errorf("reloading Prometheus: %w", err)
	}
	fmt.Println("  [OK] Prometheus reloaded — reread prometheus.yml")
	return nil
}

// Remove stops and removes the Prometheus container and deletes the generated
// config file. The TSDB data dir is kept by default (so reinstalling under the
// same name revives history) unless cleanData; --conf-file's original lives
// wherever the operator put it and is never touched — only pgcli's copy goes
// away. Under rootless podman the mapped-uid data dir is reclaimed through
// removeHostDir's `podman unshare rm` fallback.
func (m *PrometheusManager) Remove(pc *config.PrometheusConfig, cleanData bool) error {
	containerName := pc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing Prometheus container: %w", err)
	}
	fmt.Println("  [OK] Prometheus container removed")

	// pgcli's prometheus.yml copy is a generated artifact — safe to delete.
	if cfgPath := m.ConfigPath(pc); cfgPath != "" {
		if err := os.Remove(cfgPath); err != nil && !os.IsNotExist(err) {
			fmt.Printf("  [!] Warning: removing config file %s: %v\n", cfgPath, err)
		}
	}
	// So are the rules/ copies (--rule-file originals live wherever the operator
	// put them and are never touched) — and unlike the data dir they carry no
	// state to revive, so they go away even without cleanData.
	if err := os.RemoveAll(m.RulesDir(pc)); err != nil {
		fmt.Printf("  [!] Warning: removing rules dir: %v\n", err)
	}

	dataDir := m.resolveDataDir(pc)
	if !cleanData {
		fmt.Printf("  [OK] Data directory kept: %s\n", dataDir)
		return nil
	}
	if isMountpoint(dataDir) {
		fmt.Printf("  [!] Refusing to delete %s: it is still a mount point — unmount it first if the data below is really disposable\n", dataDir)
		return nil
	}
	if err := removeHostDir(m.podman, pc.ImageTag, dataDir); err != nil && !os.IsNotExist(err) {
		fmt.Printf("  [!] Warning: removing data dir %s: %v\n", dataDir, err)
	} else {
		fmt.Printf("  [OK] Data directory removed: %s\n", dataDir)
	}
	if pc.DataDir == "" {
		// Default layout: also prune the <name> dir once data/ is gone. With an
		// override the parent belongs to the user — never touch it.
		_, _ = removeHostDirIfEmpty(m.podman, pc.ImageTag, prometheusConfigDir(m.dataDir, pc.Name))
	}
	return nil
}

// ContainerRunning reports whether the named container is currently running.
func (m *PrometheusManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether a container with the given name exists
// (running or not) — the install path uses it to skip recreation of a live
// instance.
func (m *PrometheusManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Stop stops a Prometheus container.
func (m *PrometheusManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ---------------------------------------------------

func (m *PrometheusManager) run(args ...string) (string, error) {
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

func (m *PrometheusManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *PrometheusManager) imageExists(tag string) (bool, error) {
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

func (m *PrometheusManager) containerExists(name string) (bool, error) {
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

func (m *PrometheusManager) containerRunning(name string) (bool, error) {
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
