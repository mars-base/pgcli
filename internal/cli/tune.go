package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/inspect"
	"github.com/mars-base/pgcli/internal/podman"
	"github.com/mars-base/pgcli/internal/tune"
)

// pg tune ports the pgtune (le0pard/pgtune, MIT) recommendation model: given
// the host's hardware specs and a workload class it computes a coherent set of
// postgresql.conf parameters. By default it only prints the recommendation;
// --apply writes it through the same managed tuning block `pg start` already
// maintains (or, for a Patroni cluster, the DCS via edit-config).

func init() {
	tuneCmd.Flags().String("type", "", "workload class: web, oltp, dw, desktop, mixed (default mixed)")
	tuneCmd.Flags().String("hd", "", "storage class: ssd, hdd, san, nvme (default ssd)")
	tuneCmd.Flags().String("ram", "", "total RAM, e.g. 16G (default: probe this host)")
	tuneCmd.Flags().Int("cpus", 0, "CPU count (default: probe this host)")
	tuneCmd.Flags().Int("conns", 0, "max_connections override (default: workload preset)")
	tuneCmd.Flags().String("db-size", "", "database size vs RAM, e.g. 200G (<RAM=less, <=4xRAM=mid, else greater)")
	tuneCmd.Flags().Bool("apply", false, "write the recommendation (managed conf block / Patroni DCS)")
	tuneCmd.Flags().Bool("revert", false, "remove a previously applied tune and fall back to defaults")
	tuneCmd.Flags().String("apply-conf", "", "tuning file (key = value). Without --apply: save the recommendation here; with --apply: apply this file instead of the computed set")
	tuneCmd.Flags().String("dsn", "", "target a remote database (recommend only; --apply/--revert are refused)")
	tuneCmd.Flags().String("ha", "", "target a Patroni cluster by <scope> instead of a local instance")
	tuneCmd.Flags().Bool("auto-restart", false, "with --ha --apply/--revert, skip the rolling-restart confirmation")
	tuneCmd.Flags().String("web-tune", "", "write a self-contained interactive tuning site to this path and exit")
	tuneCmd.Flags().Bool("open", false, "after --web-tune, try to open the site in a browser (needs a GUI)")
	rootCmd.AddCommand(tuneCmd)
}

var tuneCmd = &cobra.Command{
	Use:   "tune",
	Short: "Recommend (and optionally apply) postgresql.conf tuning from hardware specs",
	Long: `tune computes a parameter set from the machine's hardware specs and a
workload class. The specs default to a probe of the host this command runs on;
override with --ram/--cpus for shared hosts or cross-host clusters.

Default output is a recommendation table only — nothing is written. --apply
persists it: for a managed instance into pgcli's tuning block in
postgresql.conf (re-applied on every ` + "`pg start`" + `, so it survives restarts); for a
Patroni cluster (--ha <scope>) into the DCS via edit-config, which triggers a
rolling restart. Either way --revert removes a previously applied tune.
postgresql.auto.conf (ALTER SYSTEM) always wins over the managed block, and
those parameters are skipped.

--apply-conf points at a key = value file: without --apply it saves the
recommendation there for hand-editing; with --apply it uses that file as the
parameter source instead of the computed set. In both apply paths every
parameter is validated against the target server's pg_settings first (unknown
name, bad bool/enum, out-of-range) and the apply aborts listing any offenders.

Examples:
  pg tune -i myinst
  pg tune -i myinst --type oltp --hd nvme --ram 16G --cpus 8
  pg tune -i myinst --apply
  pg tune -i myinst --apply-conf /tmp/tune.conf     # save, edit, then apply
  pg tune -i myinst --apply --apply-conf /tmp/tune.conf
  pg tune --ha app --apply --auto-restart
  pg tune --dsn postgres://user:pass@host:5432/db   # recommendations only
  pg tune -i myinst --revert`,
	RunE: func(cmd *cobra.Command, args []string) error {
		typ, _ := cmd.Flags().GetString("type")
		hd, _ := cmd.Flags().GetString("hd")
		ramStr, _ := cmd.Flags().GetString("ram")
		cpus, _ := cmd.Flags().GetInt("cpus")
		conns, _ := cmd.Flags().GetInt("conns")
		dbSizeStr, _ := cmd.Flags().GetString("db-size")
		doApply, _ := cmd.Flags().GetBool("apply")
		doRevert, _ := cmd.Flags().GetBool("revert")
		applyConf, _ := cmd.Flags().GetString("apply-conf")
		dsn, _ := cmd.Flags().GetString("dsn")
		haScope, _ := cmd.Flags().GetString("ha")
		autoRestart, _ := cmd.Flags().GetBool("auto-restart")
		webTune, _ := cmd.Flags().GetString("web-tune")
		doOpen, _ := cmd.Flags().GetBool("open")

		if webTune != "" {
			return runWebTune(webTune, doOpen, cmd.OutOrStdout())
		}
		if err := checkTuneTargets(cmd, dsn, haScope); err != nil {
			return err
		}
		if doApply && doRevert {
			return fmt.Errorf("--apply and --revert are mutually exclusive")
		}
		// --apply-conf without --apply is the save path; --revert never reads a
		// conf file, so that pairing is the only illegal combination.
		if doRevert && applyConf != "" {
			return fmt.Errorf("--apply-conf is not used with --revert (revert removes the applied tune; it takes no file)")
		}
		if dsn != "" && (doApply || doRevert) {
			return fmt.Errorf("--dsn is recommendation-only; --apply/--revert need a managed instance (-i) or Patroni cluster (--ha)")
		}

		t, err := loadTune(cmd, dsn, haScope)
		if err != nil {
			return err
		}
		if doRevert {
			return t.revert(autoRestart)
		}

		in, err := t.buildInputs(typ, hd, ramStr, cpus, conns, dbSizeStr)
		if err != nil {
			return err
		}
		params, err := tune.Recommend(in)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		printTuneHeader(out, in, t.target())

		if doApply {
			if applyConf != "" {
				return t.applyFromFile(applyConf, autoRestart)
			}
			return t.applyParams(params, in, autoRestart)
		}
		if applyConf != "" {
			return saveTuneConf(applyConf, in, params)
		}
		return renderRecommendation(out, t, in, params)
	},
}

// tuneTarget bundles the resolved connection to a recommendation target and the
// two apply strategies. It is set up by loadTune.
type tuneTarget struct {
	q        inspect.Querier
	pm       *podman.Manager
	scope    string // non-empty → Patroni cluster
	cluster  *config.PatroniClusterConfig
	cfgSave  func() error
	confInst *config.InstanceConfig // local instance record (for Tune persistence)
	instName string
}

func loadTune(cmd *cobra.Command, dsn, haScope string) (*tuneTarget, error) {
	if haScope != "" {
		return loadTuneHA(cmd, haScope)
	}
	if dsn != "" {
		if err := loadConfigForDSN(); err != nil {
			return nil, err
		}
		pm, err := newPodman()
		if err != nil {
			return nil, err
		}
		return &tuneTarget{q: &dsnQuerier{pm: pm, dsn: dsn}, pm: pm}, nil
	}
	if err := loadConfig(); err != nil {
		return nil, err
	}
	if err := ensureContainerRunning(); err != nil {
		return nil, err
	}
	pm, err := newPodman()
	if err != nil {
		return nil, err
	}
	inst := cfg.Instances[cfg.Instance]
	return &tuneTarget{
		q:        &localQuerier{pm: pm},
		pm:       pm,
		cfgSave:  func() error { return cfg.Save(cfgPath) },
		confInst: &inst,
		instName: cfg.Instance,
	}, nil
}

func loadTuneHA(cmd *cobra.Command, scope string) (*tuneTarget, error) {
	if err := loadConfig(); err != nil {
		return nil, err
	}
	cluster, ok := cfg.Addons.Patroni[scope]
	if !ok {
		return nil, fmt.Errorf("Patroni cluster %q not found in config", scope)
	}
	pm, err := newPodman()
	if err != nil {
		return nil, err
	}
	pmPatroni, err := podman.NewPatroniManager(cfg)
	if err != nil {
		return nil, err
	}
	db := "postgres"
	t := &tuneTarget{
		pm:      pm,
		scope:   scope,
		cluster: &cluster,
		cfgSave: func() error { return cfg.Save(cfgPath) },
	}
	t.q = &haQuerier{pm: pmPatroni, cluster: &cluster, database: db}
	return t, nil
}

// haQuerier runs read-only SQL against a Patroni cluster's leader.
type haQuerier struct {
	pm       *podman.PatroniManager
	cluster  *config.PatroniClusterConfig
	database string
}

func (h *haQuerier) Query(sql string) (string, error) {
	return h.pm.ExecLeaderQuery(h.cluster, h.database, readOnly(sql))
}

// buildInputs resolves flag values on top of a host probe and the target's
// live server_version_num.
func (t *tuneTarget) buildInputs(typ, hd, ramStr string, cpus, conns int, dbSizeStr string) (tune.Inputs, error) {
	in := tune.Inputs{CPUs: hostCPUs(), RAMBytes: hostRAM()}
	if cpus > 0 {
		in.CPUs = cpus
	}
	if ramStr != "" {
		b, err := parseRAM(ramStr)
		if err != nil {
			return in, err
		}
		in.RAMBytes = b
	}
	if in.RAMBytes <= 0 {
		return in, fmt.Errorf("could not probe host RAM (got %d bytes); pass --ram explicitly", in.RAMBytes)
	}
	in.OS = probeOS()
	if typ != "" {
		in.Type = tune.Workload(typ)
	}
	if hd != "" {
		in.Disk = tune.Disk(hd)
	}
	if conns > 0 {
		in.MaxConns = conns
	}
	if dbSizeStr != "" {
		b, err := parseRAM(dbSizeStr)
		if err != nil {
			return in, fmt.Errorf("--db-size: %w", err)
		}
		in.Size = dbSizeClass(b, in.RAMBytes)
	}
	v, err := t.serverVersion()
	if err != nil {
		return in, fmt.Errorf("read server version: %w", err)
	}
	in.DBVersion = v
	if err := in.Normalize(); err != nil {
		return in, err
	}
	return in, nil
}

func (t *tuneTarget) serverVersion() (int, error) {
	out, err := t.q.Query("SELECT current_setting('server_version_num')")
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("unexpected server_version_num %q", strings.TrimSpace(out))
	}
	return n, nil
}

func (t *tuneTarget) target() string {
	if t.scope != "" {
		return fmt.Sprintf("Patroni cluster %q", t.scope)
	}
	if t.instName != "" {
		return fmt.Sprintf("instance %q", t.instName)
	}
	return "remote database" // dsn set elsewhere; caller prints via header
}

// renderRecommendation prints the table (name | current | recommended | why).
func renderRecommendation(out io.Writer, t *tuneTarget, in tune.Inputs, params []tune.Param) error {
	names := make([]string, len(params))
	for i, p := range params {
		names[i] = p.Name
	}
	current, err := t.currentValues(names)
	if err != nil {
		// Not fatal — recommendations stand without the CURRENT column.
		fmt.Fprintf(out, "(could not read current values: %v)\n\n", err)
		current = map[string]string{}
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PARAM\tCURRENT\tRECOMMENDED\tWHY")
	for _, p := range params {
		why := p.Reason
		if p.Warning != "" {
			why = "! " + p.Warning
		}
		cur := current[p.Name]
		if cur == "" {
			cur = "-"
		}
		flag := ""
		if p.RequiresRestart {
			flag = " *"
		}
		fmt.Fprintf(w, "%s%s\t%s\t%s\t%s\n", p.Name, flag, cur, p.Value, why)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(out, "\n* requires a restart to apply")
	for _, warn := range tune.GlobalWarnings(in) {
		fmt.Fprintf(out, "! %s\n", warn)
	}
	return nil
}

// currentValues reads each parameter's live value via current_setting, quoting
// names defensively (they may come from a user-supplied conf file).
func (t *tuneTarget) currentValues(names []string) (map[string]string, error) {
	if len(names) == 0 {
		return map[string]string{}, nil
	}
	var sb strings.Builder
	sb.WriteString("SELECT name, coalesce(current_setting(name, true), '') FROM (VALUES ")
	for i, n := range names {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("('" + sqlQuote(n) + "')")
	}
	sb.WriteString(") AS t(name)")
	out, err := t.q.Query(sb.String())
	if err != nil {
		return nil, err
	}
	return parsePipeRows(out), nil
}

// validateParams checks every pair against the target's pg_settings.
func (t *tuneTarget) validateParams(ctx context.Context, kvs []tune.KV) error {
	fetch := func(_ context.Context, names []string) (map[string]tune.SettingRow, error) {
		return t.fetchSettings(names)
	}
	errs := tune.Validate(ctx, kvs, fetch)
	if len(errs) == 0 {
		return nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "refusing to apply %d invalid parameter(s):", len(errs))
	for _, e := range errs {
		sb.WriteString("\n  - " + e.Error())
	}
	return fmt.Errorf("%s", sb.String())
}

func (t *tuneTarget) fetchSettings(names []string) (map[string]tune.SettingRow, error) {
	if len(names) == 0 {
		return map[string]tune.SettingRow{}, nil
	}
	var sb strings.Builder
	sb.WriteString("SELECT name, vartype, coalesce(unit,''), coalesce(min_val,''), coalesce(max_val,''), coalesce(enumvals::text,'') FROM pg_settings WHERE name IN (")
	for i, n := range names {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("('" + sqlQuote(n) + "')")
	}
	sb.WriteString(")")
	out, err := t.q.Query(sb.String())
	if err != nil {
		return nil, err
	}
	settings := map[string]tune.SettingRow{}
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) < 6 {
			continue
		}
		settings[f[0]] = tune.SettingRow{
			Name: f[0], Vartype: f[1], Unit: f[2], MinVal: f[3], MaxVal: f[4], EnumVals: f[5],
		}
	}
	return settings, nil
}

// sqlQuote escapes a single-quoted SQL string literal.
func sqlQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }

// stdinIsTerminal reports whether an interactive prompt makes sense.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// hostRAM probes total physical memory: Linux via /proc/meminfo, macOS via
// sysctl hw.memsize. Zero means "no idea" — the caller must pass --ram.
func hostRAM() int64 {
	switch runtime.GOOS {
	case "linux":
		f, err := os.Open("/proc/meminfo")
		if err != nil {
			return 0
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			name, kbStr, ok := strings.Cut(sc.Text(), ":")
			if !ok || name != "MemTotal" {
				continue
			}
			kbStr = strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(kbStr, "kB")), " kB")
			n, err := strconv.ParseInt(strings.TrimSpace(kbStr), 10, 64)
			if err != nil {
				return 0
			}
			return n * 1024
		}
		return 0
	case "darwin":
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0
		}
		n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

func hostCPUs() int { return runtime.NumCPU() }

// probeOS maps runtime.GOOS onto the tune OS enum.
func probeOS() tune.OS {
	switch runtime.GOOS {
	case "darwin":
		return tune.OSMac
	case "windows":
		return tune.OSWindows
	default:
		return tune.OSLinux
	}
}

// parseRAM parses "16G"/"512M"/"2TB"/bytes count into bytes.
func parseRAM(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "TB"):
		mult, s = 1<<40, s[:len(s)-2]
	case strings.HasSuffix(s, "GB"):
		mult, s = 1<<30, s[:len(s)-2]
	case strings.HasSuffix(s, "G"):
		mult, s = 1<<30, s[:len(s)-1]
	case strings.HasSuffix(s, "MB"):
		mult, s = 1<<20, s[:len(s)-2]
	case strings.HasSuffix(s, "M"):
		mult, s = 1<<20, s[:len(s)-1]
	case strings.HasSuffix(s, "KB"):
		mult, s = 1<<10, s[:len(s)-2]
	case strings.HasSuffix(s, "K"):
		mult, s = 1<<10, s[:len(s)-1]
	case strings.HasSuffix(s, "B"):
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid size %q (use e.g. 16G, 512M, or a byte count)", strings.TrimSpace(s))
	}
	return int64(n * float64(mult)), nil
}

// dbSizeClass buckets a database size against RAM the way le0pard's three
// choices do: fits in RAM → less; up to 4× RAM → mid; beyond → greater.
func dbSizeClass(dbBytes, ramBytes int64) tune.DBSize {
	switch {
	case dbBytes < ramBytes:
		return tune.SizeLess
	case dbBytes <= 4*ramBytes:
		return tune.SizeMid
	default:
		return tune.SizeGreater
	}
}

// printTuneHeader echoes the resolved inputs so the table is reproducible.
func printTuneHeader(out io.Writer, in tune.Inputs, target string) {
	fmt.Fprintf(out, "target:   %s\n", target)
	fmt.Fprintf(out, "workload: %s  storage: %s  db-size: %s\n", in.Type, in.Disk, in.Size)
	fmt.Fprintf(out, "ram:      %s  cpus: %d  max-connections: %d\n",
		formatSize(in.RAMBytes), in.CPUs, presetOr(in.MaxConns, in.Type))
	// PG10+ packs the version as major*10000 + minor (180006 → 18.6).
	fmt.Fprintf(out, "server:   %d.%d (version_num %d)  os: %s\n\n",
		in.DBVersion/10000, in.DBVersion%10000, in.DBVersion, in.OS)
}

func presetOr(conns int, typ tune.Workload) int {
	if conns > 0 {
		return conns
	}
	return tune.MaxConnectionsPreset(typ)
}

// formatSize renders a byte count for display. Probed host RAM almost never
// lands on an exact MB, so the fallback is a floored, marked-approximate MB.
func formatSize(b int64) string {
	switch {
	case b%(1<<30) == 0:
		return fmt.Sprintf("%dGB", b/(1<<30))
	case b%(1<<20) == 0:
		return fmt.Sprintf("%dMB", b/(1<<20))
	default:
		return fmt.Sprintf("≈%dMB", b/(1<<20))
	}
}

// saveTuneConf writes the recommendation to a key = value file for editing.
func saveTuneConf(path string, in tune.Inputs, params []tune.Param) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating tune file: %w", err)
	}
	defer f.Close()
	if err := tune.FormatConf(f, in, tune.ParamsToKV(params)); err != nil {
		return err
	}
	fmt.Printf("-> recommendation saved to %s (edit it, then `pg tune ... --apply --apply-conf %s`)\n", path, path)
	return nil
}

// applyParams validates then persists the computed recommendation.
func (t *tuneTarget) applyParams(params []tune.Param, in tune.Inputs, autoRestart bool) error {
	return t.apply(tune.ParamsToKV(params), inputsToStrings(in), autoRestart)
}

// inputsToStrings snapshots the resolved inputs for display/reproducibility.
func inputsToStrings(in tune.Inputs) map[string]string {
	return map[string]string{
		"type": string(in.Type), "hd": string(in.Disk), "size": string(in.Size),
		"os": string(in.OS), "ram": formatSize(in.RAMBytes),
		"cpus": strconv.Itoa(in.CPUs), "version": strconv.Itoa(in.DBVersion),
		"conns": strconv.Itoa(presetOr(in.MaxConns, in.Type)),
	}
}

// applyFromFile reads a key = value conf file and applies it as-is.
func (t *tuneTarget) applyFromFile(path string, autoRestart bool) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening --apply-conf: %w", err)
	}
	defer f.Close()
	kvs, err := tune.ParseConf(f)
	if err != nil {
		return err
	}
	if len(kvs) == 0 {
		return fmt.Errorf("--apply-conf %s: no parameters found", path)
	}
	fmt.Printf("-> applying %d parameter(s) from %s\n", len(kvs), path)
	return t.apply(kvs, nil, autoRestart)
}

func (t *tuneTarget) apply(kvs []tune.KV, inputs map[string]string, autoRestart bool) error {
	ctx := context.Background()
	if err := t.validateParams(ctx, kvs); err != nil {
		return err
	}
	if t.scope != "" {
		return t.applyHA(kvs, inputs, autoRestart)
	}
	return t.applyLocal(kvs, inputs)
}

// applyLocal records the params on the instance config, then re-runs the
// managed-block application; restart-required changes ask before bouncing the
// container (non-TTY: just tell the user).
func (t *tuneTarget) applyLocal(kvs []tune.KV, inputs map[string]string) error {
	inst := cfg.Instances[t.instName]
	if inst.Tune == nil {
		inst.Tune = &config.TuneConfig{}
	}
	inst.Tune.Inputs = inputs
	inst.Tune.Params = tune.KVsToMap(kvs)
	cfg.Instances[t.instName] = inst
	if err := t.cfgSave(); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	needsRestart, err := t.pm.ApplyPGTuning()
	if err != nil {
		return err
	}
	if !needsRestart {
		if _, err := t.pm.Exec("psql", "-U", cfg.Postgres.User, "-d", cfg.Postgres.Database,
			"-q", "-c", "SELECT pg_reload_conf()"); err != nil {
			fmt.Printf("  [!] pg_reload_conf: %v (values land on next restart)\n", err)
		} else {
			fmt.Println("✓ tuning applied (reload only — no restart needed)")
		}
		return nil
	}
	fmt.Println("! some applied parameters require a PostgreSQL restart")
	if !stdinIsTerminal() {
		fmt.Printf("  non-interactive session: run `pg stop -i %s && pg start -i %s` to apply\n",
			t.instName, t.instName)
		return nil
	}
	if !confirmPrompt("  restart the instance now? [y/N]: ") {
		fmt.Printf("  skipped — run `pg stop -i %s && pg start -i %s` later\n", t.instName, t.instName)
		return nil
	}
	if err := t.pm.StopContainer(); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	if err := t.pm.StartContainer(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	fmt.Println("✓ instance restarted with the tuned configuration")
	return nil
}

// applyHA writes params to the DCS via edit-config (Patroni rolling-restarts
// members for postmaster-level changes). Empty value = remove the parameter.
func (t *tuneTarget) applyHA(kvs []tune.KV, inputs map[string]string, autoRestart bool) error {
	params := tune.KVsToMap(kvs)
	t.applyToConfig(params, inputs)

	if !autoRestart {
		fmt.Println("! applying parameters to a Patroni cluster triggers a rolling restart of all members")
		if !confirmPrompt("  proceed with rolling restart? [y/N]: ") {
			// Params were recorded but not pushed — roll the record back.
			t.applyToConfig(nil, nil)
			return fmt.Errorf("cancelled; nothing was written to the DCS")
		}
	}
	nsScope := cfg.PatroniScope(t.scope)
	editArgs := []string{"edit-config", nsScope, "--force"}
	for _, kv := range kvs { // keep file/argument order deterministic
		editArgs = append(editArgs, "-s", fmt.Sprintf("postgresql.parameters.%s=%s", kv.Name, kv.Value))
	}
	pmPatroni, err := podman.NewPatroniManager(cfg)
	if err != nil {
		return err
	}
	if err := pmPatroni.Patronictl(t.cluster, editArgs...); err != nil {
		return fmt.Errorf("edit-config: %w", err)
	}
	if err := t.cfgSave(); err != nil {
		return fmt.Errorf("saving config (params applied to DCS but not recorded): %w", err)
	}
	fmt.Printf("✓ %d parameter(s) applied to Patroni cluster %q via DCS\n", len(kvs), t.scope)
	return nil
}

// applyToConfig records/clears the Tune block for either target kind.
func (t *tuneTarget) applyToConfig(params map[string]string, inputs map[string]string) {
	if t.scope != "" {
		cluster := cfg.Addons.Patroni[t.scope]
		if len(params) == 0 {
			cluster.Tune = nil
		} else {
			if cluster.Tune == nil {
				cluster.Tune = &config.TuneConfig{}
			}
			cluster.Tune.Inputs = inputs
			cluster.Tune.Params = params
		}
		cfg.Addons.Patroni[t.scope] = cluster
		return
	}
	inst := cfg.Instances[t.instName]
	if len(params) == 0 {
		inst.Tune = nil
	} else {
		inst.Tune = &config.TuneConfig{Inputs: inputs, Params: params}
	}
	cfg.Instances[t.instName] = inst
}

// revert removes a previously applied tune from the target.
func (t *tuneTarget) revert(autoRestart bool) error {
	if t.scope != "" {
		return t.revertHA(autoRestart)
	}
	return t.revertLocal()
}

func (t *tuneTarget) revertLocal() error {
	inst, ok := cfg.Instances[t.instName]
	if !ok || inst.Tune == nil || len(inst.Tune.Params) == 0 {
		return fmt.Errorf("instance %q has no applied tune to revert", t.instName)
	}
	inst.Tune = nil
	cfg.Instances[t.instName] = inst
	if err := t.cfgSave(); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	needsRestart, err := t.pm.ApplyPGTuning() // rewrites the block to static defaults
	if err != nil {
		return err
	}
	fmt.Println("✓ tuning reverted to pgcli defaults")
	if needsRestart {
		fmt.Printf("  run `pg stop -i %s && pg start -i %s` to apply reverted restart-level params\n",
			t.instName, t.instName)
	}
	return nil
}

func (t *tuneTarget) revertHA(autoRestart bool) error {
	cluster, ok := cfg.Addons.Patroni[t.scope]
	if !ok || cluster.Tune == nil || len(cluster.Tune.Params) == 0 {
		return fmt.Errorf("Patroni cluster %q has no applied tune to revert", t.scope)
	}
	names := make([]string, 0, len(cluster.Tune.Params))
	for name := range cluster.Tune.Params {
		names = append(names, name)
	}
	sort.Strings(names)
	if !autoRestart && !confirmPrompt(fmt.Sprintf("  removing %d tune parameter(s) from the DCS triggers a rolling restart — proceed? [y/N]: ", len(names))) {
		return fmt.Errorf("cancelled")
	}
	nsScope := cfg.PatroniScope(t.scope)
	editArgs := []string{"edit-config", nsScope, "--force"}
	for _, name := range names {
		editArgs = append(editArgs, "-s", fmt.Sprintf("postgresql.parameters.%s=", name)) // empty = remove
	}
	pmPatroni, err := podman.NewPatroniManager(cfg)
	if err != nil {
		return err
	}
	if err := pmPatroni.Patronictl(t.cluster, editArgs...); err != nil {
		return fmt.Errorf("edit-config: %w", err)
	}
	cluster.Tune = nil
	cfg.Addons.Patroni[t.scope] = cluster
	if err := t.cfgSave(); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Printf("✓ tune parameters removed from Patroni cluster %q\n", t.scope)
	return nil
}

// parsePipeRows turns psql -tA `a|b` output into name→value.
func parsePipeRows(out string) map[string]string {
	m := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		if name, val, ok := strings.Cut(line, "|"); ok {
			m[name] = val
		}
	}
	return m
}

func checkTuneTargets(cmd *cobra.Command, dsn, haScope string) error {
	if haScope != "" {
		if dsn != "" {
			return fmt.Errorf("--ha and --dsn are mutually exclusive")
		}
		if cmd.Flags().Changed("instance") {
			return fmt.Errorf("--ha and --instance are mutually exclusive: --ha <scope> selects the target")
		}
	}
	if dsn != "" {
		return checkDSNInstanceConflict(cmd)
	}
	return nil
}
