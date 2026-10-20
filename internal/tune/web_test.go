package tune

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestWebHTMLSelfContained checks the generated site is complete: placeholders
// substituted, the embedded golden fixtures parse as JSON and match a fresh
// Recommend() for their inputs, and the prefill carries sane defaults.
func TestWebHTMLSelfContained(t *testing.T) {
	html, err := WebHTML(Inputs{RAMBytes: 16 * 1024 * 1024 * 1024, CPUs: 8, OS: OSLinux})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "__FIXTURES__") || strings.Contains(html, "__PREFILL__") {
		t.Error("unsubstituted placeholder left in the page")
	}
	if !strings.Contains(html, "<!DOCTYPE html>") || !strings.Contains(html, "</html>") {
		t.Error("page is not a full HTML document")
	}

	fixtures := mustFixtureJSON(t, html)
	if len(fixtures) != 3 {
		t.Fatalf("want 3 golden fixtures, got %d", len(fixtures))
	}
	for i, f := range fixtures {
		params, err := Recommend(fromWebInputs(f.Inputs))
		if err != nil {
			t.Fatalf("fixture %d: %v", i, err)
		}
		if len(params) != len(f.Params) {
			t.Fatalf("fixture %d: recommend gave %d params, embedded %d", i, len(params), len(f.Params))
		}
		for j, p := range params {
			if f.Params[j][0] != p.Name || f.Params[j][1] != p.Value {
				t.Errorf("fixture %d param %d = %v, want {%s,%s}", i, j, f.Params[j], p.Name, p.Value)
			}
		}
	}
}

// TestWebHTMLPrefillDefaults: a blank prefill still yields version 18, cpus 1,
// and the default enums — the standalone case where no host probe is available.
func TestWebHTMLPrefillDefaults(t *testing.T) {
	html, err := WebHTML(Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	var pre webInputs
	if err := json.Unmarshal([]byte(between(t, html, "var PREFILL = ", ";")), &pre); err != nil {
		t.Fatal(err)
	}
	if pre.Version != 180000 || pre.CPUs != 1 {
		t.Errorf("prefill defaults: version=%d cpus=%d, want 180000/1", pre.Version, pre.CPUs)
	}
	if pre.OS != "linux" || pre.Type != "mixed" || pre.HD != "ssd" || pre.Size != "mid_ram" {
		t.Errorf("prefill enums wrong: %+v", pre)
	}
	if pre.RAMBytes != 0 {
		t.Errorf("standalone prefill RAM should stay 0 (blank form), got %d", pre.RAMBytes)
	}
}

func mustFixtureJSON(t *testing.T, html string) []webFixture {
	t.Helper()
	raw := between(t, html, "var FIXTURES = ", ";")
	var fs []webFixture
	if err := json.Unmarshal([]byte(raw), &fs); err != nil {
		t.Fatalf("fixtures not valid JSON: %v\n%s", err, raw)
	}
	return fs
}

// between extracts the substring after the first `open` up to the next `close`.
func between(t *testing.T, s, open, close string) string {
	t.Helper()
	i := strings.Index(s, open)
	if i < 0 {
		t.Fatalf("marker %q not found", open)
	}
	s = s[i+len(open):]
	j := strings.Index(s, close)
	if j < 0 {
		t.Fatalf("end marker %q not found after %q", close, open)
	}
	return s[:j]
}

func fromWebInputs(w webInputs) Inputs {
	return Inputs{
		DBVersion: w.Version, OS: OS(w.OS), Type: Workload(w.Type),
		RAMBytes: w.RAMBytes, CPUs: w.CPUs, Disk: Disk(w.HD),
		Size: DBSize(w.Size), MaxConns: w.MaxConns,
	}
}
