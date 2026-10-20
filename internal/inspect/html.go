package inspect

import (
	"fmt"
	"html"
	"strings"
)

// HTML renders the same Context into one self-contained document — inline CSS,
// zero external requests, every value passed through html.EscapeString so a
// malicious object name (a table called "<script>…") can only ever be text. This
// mirrors pgbot's report/html.go philosophy: a file you can email or open
// offline that never phones home and never executes attacker data.

// HTML returns the full document.
func HTML(c *Context) string {
	score := HealthScore(c.Findings)
	var b strings.Builder
	b.WriteString(doctype)
	b.WriteString(head(orNA(c.Server)))
	b.WriteString(banner(c, score))
	b.WriteString(nav())
	b.WriteString(findingsSection(c.Findings))
	b.WriteString(sectionTables(c))
	b.WriteString(footer(c))
	b.WriteString("</body></html>")
	return b.String()
}

const doctype = `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
`

func head(server string) string {
	return fmt.Sprintf(`<title>pg inspect — %s</title><style>%s</style></head><body>`,
		esc(server), css)
}

func banner(c *Context, score float64) string {
	class := scoreClass(score)
	counts := map[Severity]int{}
	for _, f := range c.Findings {
		counts[f.Severity]++
	}
	return fmt.Sprintf(
		`<header class="banner %s"><div class="score"><span class="num">%d</span><span class="denom">/100</span></div>`+
			`<div class="meta"><h1>%s</h1><p>%s · server %s · %ds window</p>`+
			`<p class="pills"><span class="pill crit">%d critical</span><span class="pill warn">%d warning</span>`+
			`<span class="pill note">%d note</span><span class="pill good">%d good</span></p></div></header>`,
		class, int(score), esc(verdict(score)), esc(c.Target), esc(orNA(c.Server)),
		int(c.Interval), counts[SeverityCritical], counts[SeverityWarn], counts[SeverityInfo], counts[SeverityGood])
}

func nav() string {
	ids := []struct{ id, label string }{
		{"findings", "Findings"}, {"queries", "Queries"}, {"indexes", "Indexes"},
		{"vacuum", "Vacuum/Tables"}, {"settings", "Settings"}, {"sessions", "Sessions"},
	}
	var sb strings.Builder
	sb.WriteString(`<nav class="topnav">`)
	for _, i := range ids {
		fmt.Fprintf(&sb, `<a href="#%s">%s</a>`, i.id, i.label)
	}
	sb.WriteString(`</nav>`)
	return sb.String()
}

func findingsSection(fs []Finding) string {
	var sb strings.Builder
	sb.WriteString(`<section id="findings"><h2>Findings</h2>`)
	if len(fs) == 0 {
		sb.WriteString(`<p class="empty">No findings.</p>`)
	}
	for _, f := range fs {
		sb.WriteString(findingCard(f))
	}
	sb.WriteString(`</section>`)
	return sb.String()
}

func findingCard(f Finding) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`<article class="finding sev-%s">`, strings.ToLower(string(f.Severity))))
	sb.WriteString(fmt.Sprintf(`<div class="fhead"><span class="sev">%s</span><span class="ftitle">%s</span></div>`,
		esc(string(f.Severity)), esc(f.Title)))
	if f.Detail != "" {
		sb.WriteString(fmt.Sprintf(`<p class="fdetail">%s</p>`, esc(f.Detail)))
	}
	if f.Remediation != "" {
		sb.WriteString(fmt.Sprintf(`<p class="frem"><span class="arrow">→</span> %s</p>`, esc(f.Remediation)))
	}
	if len(f.Evidence) > 0 {
		sb.WriteString(`<ul class="evidence">`)
		for _, e := range f.Evidence {
			sb.WriteString(fmt.Sprintf(`<li>%s</li>`, esc(e)))
		}
		sb.WriteString(`</ul>`)
	}
	if len(f.Caveats) > 0 {
		sb.WriteString(`<ul class="caveats">`)
		for _, cv := range f.Caveats {
			sb.WriteString(fmt.Sprintf(`<li>but %s</li>`, esc(cv)))
		}
		sb.WriteString(`</ul>`)
	}
	sb.WriteString(`</article>`)
	return sb.String()
}

func sectionTables(c *Context) string {
	var sb strings.Builder
	sb.WriteString(htmlTable("queries", "Queries (top by total time)",
		[]string{"Total", "Mean", "Calls", "Share", "Query"}, queryRowsHTML(c.Queries)))
	sb.WriteString(htmlTable("indexes", "Indexes",
		[]string{"Size", "Scans", "Index", "Table", "Flags"}, indexRowsHTML(c.Indexes)))
	sb.WriteString(htmlTable("vacuum", "Vacuum / Tables",
		[]string{"Size", "Live", "Dead%", "Last vacuum", "Last analyze", "Table"}, vacuumRowsHTML(c.Tables)))
	sb.WriteString(htmlTable("settings", "Settings",
		[]string{"Name", "Value", "Source"}, settingRowsHTML(c.Settings)))
	sb.WriteString(htmlTable("sessions", "Sessions",
		[]string{"Total", "Active", "Idle", "Idle-txn", "Longest txn", "Longest query"}, activityRowsHTML(c.Activity)))
	return sb.String()
}

func htmlTable(id, title string, head []string, rows [][]string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `<section id="%s"><h2>%s</h2>`, id, esc(title))
	if len(rows) == 0 {
		sb.WriteString(`<p class="empty">Not sampled / no rows.</p></section>`)
		return sb.String()
	}
	sb.WriteString(`<table><thead><tr>`)
	for _, h := range head {
		sb.WriteString(fmt.Sprintf(`<th>%s</th>`, esc(h)))
	}
	sb.WriteString(`</tr></thead><tbody>`)
	for _, r := range rows {
		sb.WriteString(`<tr>`)
		for _, cell := range r {
			sb.WriteString(fmt.Sprintf(`<td>%s</td>`, esc(cell)))
		}
		sb.WriteString(`</tr>`)
	}
	sb.WriteString(`</tbody></table></section>`)
	return sb.String()
}

func queryRowsHTML(qs []QueryRow) [][]string {
	var out [][]string
	for _, q := range qs {
		share := "—"
		if s := q.Share(); s != nil {
			share = pct1(*s)
		}
		out = append(out, []string{humanMS(q.TotalMs), humanMS(q.MeanMs), humanInt(q.Calls), share, q.Query})
	}
	return out
}

func indexRowsHTML(ix []IndexRow) [][]string {
	var out [][]string
	for _, i := range ix {
		var flags []string
		for _, f := range []struct {
			on bool
			s  string
		}{{i.IsPrimary, "pk"}, {i.IsUnique, "uq"}, {i.IsPartial, "partial"}, {i.IsExpression, "expr"}, {i.IsReplIdent, "replident"}} {
			if f.on {
				flags = append(flags, f.s)
			}
		}
		fl := "—"
		if len(flags) > 0 {
			fl = strings.Join(flags, ", ")
		}
		out = append(out, []string{humanBytes(i.Bytes), humanInt(i.Scans), i.Index, i.Schema + "." + i.Table, fl})
	}
	return out
}

func vacuumRowsHTML(ts []TableRow) [][]string {
	var out [][]string
	for _, t := range ts {
		deadPct := "0%"
		if d := t.LiveTuples + t.DeadTuples; d > 0 {
			deadPct = fmt.Sprintf("%.0f%%", float64(t.DeadTuples)/float64(d)*100)
		}
		out = append(out, []string{
			humanBytes(t.TotalBytes), humanInt(t.LiveTuples), deadPct,
			ageOr(t.LastVacuumAgeS, t.LastAutovacuumAgeS), ageOr(t.LastAnalyzeAgeS, t.LastAutoanalyzeAgeS),
			t.Schema + "." + t.Table,
		})
	}
	return out
}

func settingRowsHTML(ss []SettingRow) [][]string {
	var out [][]string
	for _, s := range ss {
		v := s.Value
		if s.Unit != "" {
			v += " " + s.Unit
		}
		out = append(out, []string{s.Name, v, s.Source})
	}
	return out
}

func activityRowsHTML(a *Activity) [][]string {
	if a == nil {
		return nil
	}
	return [][]string{{
		fmt.Sprint(a.Total), fmt.Sprint(a.Active), fmt.Sprint(a.Idle), fmt.Sprint(a.IdleInTransaction),
		humanSec(a.LongestXactSec), humanSec(a.LongestActiveSec),
	}}
}

func footer(c *Context) string {
	return fmt.Sprintf(`<footer>pg inspect · %d findings · generated from a single snapshot</footer>`, len(c.Findings))
}

func scoreClass(score float64) string {
	switch {
	case score >= 90:
		return "s-good"
	case score >= 70:
		return "s-warn"
	case score >= 40:
		return "s-atrisk"
	default:
		return "s-crit"
	}
}

// esc escapes for HTML text and attribute contexts. Applied to every dynamic
// string; the only unescaped writes are our own structural tags and numbers.
func esc(s string) string { return html.EscapeString(s) }

// css is inlined; no @import, no external fonts, zero network requests. The
// whole document is a single file safe to open offline or forward by email.
const css = `
:root{--bg:#0f1216;--fg:#e6e9ee;--muted:#95a0b0;--line:#232a33;--card:#161b22;
--crit:#f85149;--warn:#e3b341;--note:#58a6ff;--good:#3fb950}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
h1{font-size:20px;margin:0}h2{font-size:16px;margin:0 0 12px;color:var(--muted);text-transform:uppercase;letter-spacing:.05em}
a{color:var(--note);text-decoration:none}
.banner{display:flex;gap:20px;align-items:center;padding:24px 28px;border-bottom:1px solid var(--line)}
.banner .num{font-size:48px;font-weight:700}
.banner .denom{color:var(--muted);font-size:20px}
.banner .meta p{margin:4px 0;color:var(--muted)}
.score{min-width:120px;text-align:center}
.s-good .num{color:var(--good)}.s-warn .num{color:var(--warn)}.s-atrisk .num{color:var(--warn)}.s-crit .num{color:var(--crit)}
.pills{display:flex;gap:8px;flex-wrap:wrap}
.pill{padding:2px 8px;border-radius:10px;font-size:12px;border:1px solid var(--line)}
.pill.crit{color:var(--crit)}.pill.warn{color:var(--warn)}.pill.note{color:var(--note)}.pill.good{color:var(--good)}
.topnav{position:sticky;top:0;background:var(--bg);border-bottom:1px solid var(--line);padding:10px 28px;display:flex;gap:18px;font-size:13px}
section{padding:22px 28px;border-bottom:1px solid var(--line)}
.finding{background:var(--card);border:1px solid var(--line);border-left-width:4px;border-radius:8px;padding:12px 16px;margin-bottom:12px}
.finding.sev-critical{border-left-color:var(--crit)}.finding.sev-warning{border-left-color:var(--warn)}
.finding.sev-note{border-left-color:var(--note)}.finding.sev-good{border-left-color:var(--good)}
.fhead{display:flex;gap:10px;align-items:baseline}.sev{font-size:11px;font-weight:700;letter-spacing:.05em}
.sev-critical .sev{color:var(--crit)}.sev-warning .sev{color:var(--warn)}.sev-note .sev{color:var(--note)}.sev-good .sev{color:var(--good)}
.ftitle{font-size:15px}
.fdetail{color:var(--muted);margin:8px 0}.frem{margin:8px 0}.arrow{color:var(--good)}
.evidence,.caveats{margin:8px 0;padding-left:20px;color:var(--muted);font-size:13px}
.caveats li{color:var(--warn)}
table{width:100%;border-collapse:collapse;font-size:13px}
th,td{text-align:left;padding:6px 10px;border-bottom:1px solid var(--line);vertical-align:top}
th{color:var(--muted);position:sticky;top:44px;background:var(--bg)}
td:last-child{white-space:normal;word-break:break-word}
.empty{color:var(--muted);font-style:italic}
footer{padding:16px 28px;color:var(--muted);font-size:12px}
`
