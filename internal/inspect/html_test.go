package inspect

import (
	"strings"
	"testing"
)

func TestHTMLIsSelfContained(t *testing.T) {
	c := sampleContext()
	doc := HTML(c)
	for _, bad := range []string{"http://", "https://", "<script", "@import", "<link"} {
		if strings.Contains(strings.ToLower(doc), bad) {
			t.Errorf("HTML must not reference %q (no external requests / no JS)", bad)
		}
	}
	for _, want := range []string{"<!DOCTYPE html>", "/100", "buffer cache", "Findings"} {
		if !strings.Contains(doc, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
}

// TestHTMLEscapesXSS is the security assertion: object names come from user
// data and must render as text, never as markup. If a table or index is named
// "<script>…", the report must contain no live <script> tag and no raw markup.
func TestHTMLEscapesXSS(t *testing.T) {
	c := sampleContext()
	c.Target = `<img src=x onerror=alert(1)>`
	c.Tables = []TableRow{{Schema: "s", Table: `<script>alert(1)</script>`, LiveTuples: 1, DeadTuples: 0, TotalBytes: 2048}}
	c.Indexes = []IndexRow{{Schema: "s", Table: "t", Index: `<b>bold</b>`, Bytes: 4 << 20}}
	c.Settings = []SettingRow{{Name: `x" onmouseover="alert(2)`, Value: "1", Source: "conf"}}

	doc := HTML(c)
	if strings.Contains(doc, "<script>") || strings.Contains(doc, "<b>bold") || strings.Contains(doc, "<img src=x") {
		t.Error("XSS payload escaped into markup — raw tags present in output")
	}
	for _, want := range []string{"&lt;script&gt;", "&lt;b&gt;bold", "&lt;img src=x"} {
		if !strings.Contains(doc, want) {
			t.Errorf("expected escaped form %q in HTML", want)
		}
	}
}

func TestHTMLScoresAndVerdict(t *testing.T) {
	doc := HTML(sampleContext())
	if !strings.Contains(doc, "/100") {
		t.Error("score banner missing")
	}
}

// sampleContext is a small fully-populated report used by several render tests.
// Findings are computed so the summary/HTML paths have real content to render.
func sampleContext() *Context {
	c := &Context{
		Server:   "postgres 16.3",
		Version:  160003,
		Target:   `instance "demo" (pgcli-demo)`,
		Interval: 1,
		Health:   &HealthRow{NumBackends: 5, XactCommit: 1000, StatsAgeS: 3600},
		Limits:   &LimitsRow{ConnUsed: 5, ConnMax: 100, MaxXidAge: 40_000_000},
		Rates:    &Rates{CommitsPerSec: 12, CacheHitRatio: f64(0.85), CacheBlocks: 20_000, WindowSec: 1},
		Queries: []QueryRow{
			{Query: "SELECT * FROM big", Calls: 3, TotalMs: 120, MeanMs: 40, TotalExecAll: 200},
		},
		Indexes:  []IndexRow{{Schema: "public", Table: "big", Index: "big_junk_idx", Bytes: 8 << 20}},
		Tables:   []TableRow{{Schema: "public", Table: "big", TotalBytes: 50 << 20, LiveTuples: 100_000, DeadTuples: 30_000, LastAnalyzeAgeS: 3600}},
		Settings: []SettingRow{{Name: "work_mem", Value: "4", Unit: "MB", Source: "default"}},
		Activity: &Activity{Total: 5, Active: 1, Idle: 3, IdleInTransaction: 1, LongestXactSec: 45},
	}
	c.Findings = Analyze(c, nil)
	return c
}
