package inspect

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestSummaryText(t *testing.T) {
	var buf bytes.Buffer
	Summary(&buf, sampleContext())
	out := buf.String()
	for _, want := range []string{"HEALTH SCORE", "/100", "PostgreSQL health report", "[WARNING]", "buffer cache"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q\n---\n%s", want, out)
		}
	}
}

func TestFullHasAllSections(t *testing.T) {
	var buf bytes.Buffer
	Full(&buf, sampleContext())
	out := buf.String()
	for _, want := range []string{"Queries (top by total time)", "Indexes", "Vacuum / Tables", "Settings", "Sessions", "Cache & rates"} {
		if !strings.Contains(out, want) {
			t.Errorf("full report missing section %q", want)
		}
	}
}

func TestAgeOrRenders(t *testing.T) {
	if got := ageOr(-1, -1); got != "never" {
		t.Errorf("both-never → %q", got)
	}
	if got := ageOr(7200, 3600); got != "60m" { // 3600s renders via the minutes branch
		t.Errorf("younger age should win, got %q", got)
	}
	if got := ageOr(-1, 120); !strings.HasSuffix(got, "m") {
		t.Errorf("ignored -1 should fall to the real age, got %q", got)
	}
}

func TestJSONRoundTrips(t *testing.T) {
	c := sampleContext()
	var buf bytes.Buffer
	if err := JSON(&buf, c); err != nil {
		t.Fatal(err)
	}
	var back Context
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("JSON not parseable: %v", err)
	}
	if back.Server != c.Server || len(back.Findings) != len(c.Findings) {
		t.Errorf("JSON round-trip lost fields: %+v", back)
	}
}
