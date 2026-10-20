package podman

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func TestBuildTuningLinesStaticOnly(t *testing.T) {
	m := &Manager{cfg: &config.Config{Instance: "demo",
		Instances: map[string]config.InstanceConfig{"demo": {}}}}
	lines := m.buildTuningLines()
	if len(lines) != len(pgTuningParams) {
		t.Fatalf("no tune recorded: got %d lines, want %d", len(lines), len(pgTuningParams))
	}
	for i, want := range pgTuningParams {
		if lines[i] != want {
			t.Errorf("line %d = %q, want %q", i, lines[i], want)
		}
	}
}

func TestBuildTuningLinesMergeOverride(t *testing.T) {
	m := &Manager{cfg: &config.Config{Instance: "demo",
		Instances: map[string]config.InstanceConfig{"demo": {Tune: &config.TuneConfig{
			Params: map[string]string{
				"shared_buffers": "4GB",           // overrides a static param
				"work_mem":       "19418kB",       // new param
				"jit":            "off",
			},
		}}}}}
	lines := m.buildTuningLines()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "shared_buffers = 4GB") {
		t.Errorf("tune value must override static default:\n%s", joined)
	}
	if strings.Contains(joined, "shared_buffers = 512MB") {
		t.Errorf("static shared_buffers must be gone:\n%s", joined)
	}
	if !strings.Contains(joined, "work_mem = 19418kB") || !strings.Contains(joined, "jit = off") {
		t.Errorf("new tune params missing:\n%s", joined)
	}
	// Order: overridden param keeps its static position; new params append.
	sb := -1
	synchronous := -1
	wm := -1
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "shared_buffers"):
			sb = i
		case strings.HasPrefix(l, "synchronous_commit"):
			synchronous = i
		case strings.HasPrefix(l, "work_mem"):
			wm = i
		}
	}
	if sb <= synchronous || wm <= sb {
		t.Errorf("unexpected order: synchronous=%d shared=%d work=%d", synchronous, sb, wm)
	}
	if len(lines) != len(pgTuningParams)+2 {
		t.Errorf("line count = %d, want %d", len(lines), len(pgTuningParams)+2)
	}
}

func TestBuildTuningLinesDeterministic(t *testing.T) {
	m := &Manager{cfg: &config.Config{Instance: "demo",
		Instances: map[string]config.InstanceConfig{"demo": {Tune: &config.TuneConfig{
			Params: map[string]string{"zz": "1", "aa": "2", "mm": "3", "shared_buffers": "4GB"},
		}}}}}
	first := strings.Join(m.buildTuningLines(), "\n")
	for range 20 {
		if got := strings.Join(m.buildTuningLines(), "\n"); got != first {
			t.Fatalf("repeated merges differ — the managed block would flap on every start")
		}
	}
	// appended tune params must follow sorted-name order
	tail := first[strings.Index(first, "aa = 2"):]
	if strings.Index(tail, "aa") > strings.Index(tail, "mm") ||
		strings.Index(tail, "mm") > strings.Index(tail, "zz") {
		t.Errorf("appended params not sorted:\n%s", tail)
	}
}

func TestBuildTuningLinesUnknownInstance(t *testing.T) {
	m := &Manager{cfg: &config.Config{Instance: "ghost"}}
	if lines := m.buildTuningLines(); len(lines) != len(pgTuningParams) {
		t.Errorf("missing instance should fall back to static, got %d lines", len(lines))
	}
}
