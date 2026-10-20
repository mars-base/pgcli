package inspect

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Terminal renders the report to plain text. Default is the triage summary
// (score + findings grouped by severity); Full appends the raw section tables,
// and JSON emits the whole Context for scripts. Columns align via
// text/tabwriter — no styling dependency.

const (
	labelCol  = "  "                  // findings indent
	bodyCol   = labelCol + "        " // continuation aligns under the body column
	wrapWidth = 56
)

// Summary writes the health score and findings, CRITICAL→GOOD. Caveats and
// evidence render inline under each finding.
func Summary(w io.Writer, c *Context) {
	score := HealthScore(c.Findings)
	fmt.Fprintf(w, "PostgreSQL health report — %s\n", c.Target)
	fmt.Fprintf(w, "server %s · sampled over %.0fs · %s\n\n",
		orNA(c.Server), c.Interval, verdict(score))
	fmt.Fprintf(w, "  HEALTH SCORE  %d/100\n", int(score))
	counts := map[Severity]int{}
	for _, f := range c.Findings {
		counts[f.Severity]++
	}
	fmt.Fprintf(w, "  %d critical · %d warning · %d note · %d good\n\n",
		counts[SeverityCritical], counts[SeverityWarn], counts[SeverityInfo], counts[SeverityGood])

	if len(c.Findings) == 0 {
		fmt.Fprintln(w, "No findings.")
		return
	}
	for _, sev := range []Severity{SeverityCritical, SeverityWarn, SeverityInfo, SeverityGood} {
		group := findingsAt(c.Findings, sev)
		if len(group) == 0 {
			continue
		}
		fmt.Fprintf(w, "%s\n", strings.Repeat("─", 64))
		for _, f := range group {
			fmt.Fprintf(w, "  [%s] %s\n", f.Severity, f.Title)
			if f.Detail != "" {
				printBlock(w, "", f.Detail)
			}
			if f.Remediation != "" {
				printBlock(w, "→ ", f.Remediation)
			}
			for _, e := range f.Evidence {
				fmt.Fprintf(w, "     · %s\n", e)
			}
			for _, cv := range f.Caveats {
				fmt.Fprintf(w, "     ! but %s\n", cv)
			}
			fmt.Fprintln(w)
		}
	}
}

// Full appends the raw section tables after the summary.
func Full(w io.Writer, c *Context) {
	Summary(w, c)
	section(w, "Queries (top by total time)", queryTable(c.Queries))
	section(w, "Indexes", indexTable(c.Indexes))
	section(w, "Vacuum / Tables", vacuumTable(c.Tables))
	section(w, "Settings", settingTable(c.Settings))
	section(w, "Sessions", activityTable(c.Activity))
	section(w, "Cache & rates", rateTable(c.Rates))
}

// JSON writes the machine-readable Context.
func JSON(w io.Writer, c *Context) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(c)
}

// printBlock wraps a finding's info/remediation text, aligned under the body
// column; prefix marks a remediation ("→ ").
func printBlock(w io.Writer, prefix, text string) {
	lines := strings.Split(wrap(prefix+text, wrapWidth), "\n")
	for _, l := range lines {
		fmt.Fprintf(w, "%s%s\n", bodyCol, l)
	}
}

// wrap folds a long single-line string into lines no wider than width.
func wrap(s string, width int) string {
	if width <= 0 {
		width = 40
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return s
	}
	var lines []string
	line := words[0]
	for _, wd := range words[1:] {
		if len(line)+1+len(wd) > width {
			lines = append(lines, line)
			line = wd
			continue
		}
		line += " " + wd
	}
	lines = append(lines, line)
	return strings.Join(lines, "\n")
}

// render helpers -------------------------------------------------------------

func section(w io.Writer, title string, body string) {
	if strings.TrimSpace(body) == "" {
		body = "  (not sampled)\n"
	}
	fmt.Fprintf(w, "\n%s\n%s\n%s", strings.Repeat("═", 64), title, body)
}

func table(rows [][]string) string {
	var sb strings.Builder
	tw := tabwriter.NewWriter(&sb, 0, 4, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
	return sb.String()
}

func queryTable(qs []QueryRow) string {
	if len(qs) == 0 {
		return ""
	}
	rows := [][]string{{"TOTAL", "MEAN", "CALLS", "SHARE", "QUERY"}}
	for _, q := range qs {
		share := "—"
		if s := q.Share(); s != nil {
			share = pct1(*s)
		}
		text := strings.Join(strings.Fields(q.Query), " ")
		if len(text) > 60 {
			text = text[:57] + "..."
		}
		rows = append(rows, []string{humanMS(q.TotalMs), humanMS(q.MeanMs), humanInt(q.Calls), share, text})
	}
	return table(rows)
}

func indexTable(ix []IndexRow) string {
	if len(ix) == 0 {
		return ""
	}
	rows := [][]string{{"SIZE", "SCANS", "INDEX", "TABLE", "FLAGS"}}
	for _, i := range ix {
		var flags []string
		if i.IsPrimary {
			flags = append(flags, "pk")
		}
		if i.IsUnique {
			flags = append(flags, "uq")
		}
		if i.IsPartial {
			flags = append(flags, "partial")
		}
		if i.IsExpression {
			flags = append(flags, "expr")
		}
		if i.IsReplIdent {
			flags = append(flags, "replident")
		}
		f := "—"
		if len(flags) > 0 {
			f = strings.Join(flags, ",")
		}
		rows = append(rows, []string{humanBytes(i.Bytes), humanInt(i.Scans), i.Index, i.Schema + "." + i.Table, f})
	}
	return table(rows)
}

func vacuumTable(ts []TableRow) string {
	if len(ts) == 0 {
		return ""
	}
	rows := [][]string{{"SIZE", "LIVE", "DEAD%", "LAST VAC", "LAST ANL", "TABLE"}}
	for _, t := range ts {
		deadPct := "0%"
		if d := t.LiveTuples + t.DeadTuples; d > 0 {
			deadPct = fmt.Sprintf("%.0f%%", float64(t.DeadTuples)/float64(d)*100)
		}
		rows = append(rows, []string{
			humanBytes(t.TotalBytes),
			humanInt(t.LiveTuples),
			deadPct,
			ageOr(t.LastVacuumAgeS, t.LastAutovacuumAgeS),
			ageOr(t.LastAnalyzeAgeS, t.LastAutoanalyzeAgeS),
			t.Schema + "." + t.Table,
		})
	}
	return table(rows)
}

func settingTable(ss []SettingRow) string {
	if len(ss) == 0 {
		return ""
	}
	rows := [][]string{{"NAME", "VALUE", "SOURCE"}}
	for _, s := range ss {
		v := s.Value
		if s.Unit != "" {
			v += " " + s.Unit
		}
		rows = append(rows, []string{s.Name, v, s.Source})
	}
	return table(rows)
}

func activityTable(a *Activity) string {
	if a == nil {
		return ""
	}
	return table([][]string{
		{"TOTAL", "ACTIVE", "IDLE", "IDLE-TXN", "LONGEST TXN", "LONGEST QUERY"},
		{fmt.Sprint(a.Total), fmt.Sprint(a.Active), fmt.Sprint(a.Idle), fmt.Sprint(a.IdleInTransaction),
			humanSec(a.LongestXactSec), humanSec(a.LongestActiveSec)},
	})
}

func rateTable(r *Rates) string {
	if r == nil {
		return ""
	}
	cache := "—"
	if r.CacheHitRatio != nil {
		cache = pct1(*r.CacheHitRatio)
	}
	roll := "—"
	if r.RollbackRatio != nil {
		roll = pct1(*r.RollbackRatio)
	}
	return table([][]string{
		{"COMMIT/s", "ROLLBACK/s", "CACHE HIT", "ROLLBACK %", "DEADLOCKS", "TEMP SPILL"},
		{strconv2f(r.CommitsPerSec), strconv2f(r.RollbacksPerSec), cache, roll,
			humanInt(r.DeadlocksDelta), humanBytes(r.TempBytesDelta)},
	})
}

// ageOr renders the younger of a manual/automatic timestamp age (-1 = never),
// or "never" when both are absent.
func ageOr(a, b int64) string {
	best := a
	if best < 0 || (b >= 0 && b < best) {
		best = b
	}
	if best < 0 {
		return "never"
	}
	return humanSec(float64(best))
}

func findingsAt(fs []Finding, sev Severity) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Severity == sev {
			out = append(out, f)
		}
	}
	return out
}

func verdict(score float64) string {
	switch {
	case score >= 90:
		return "healthy"
	case score >= 70:
		return "attention"
	case score >= 40:
		return "at risk"
	default:
		return "critical"
	}
}

func orNA(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func strconv2f(f float64) string { return fmt.Sprintf("%.1f", f) }
