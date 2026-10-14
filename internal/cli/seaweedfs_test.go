package cli

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// --- weed shell script building --------------------------------------------

func TestWeedShellScriptSingleLine(t *testing.T) {
	sc := &config.SeaweedfsConfig{Port: 8400, FilerPort: 8402}
	script, err := weedShellScript(sc, []string{"volume.list"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// One line, single-quoted, piped into weed shell wired to loopback master +
	// filer at THIS instance's ports (never the advertised/listen address —
	// weed dials from inside the container).
	for _, want := range []string{
		`printf '%s\n' 'volume.list'`,
		"| /usr/bin/weed shell",
		"-master=127.0.0.1:8400",
		"-filer=127.0.0.1:8402",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}
}

func TestWeedShellScriptMultipleLines(t *testing.T) {
	sc := &config.SeaweedfsConfig{Port: 9333, FilerPort: 8888}
	script, err := weedShellScript(sc, []string{"cluster.status", "s3.bucket.list"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Each line is its own printf argument — the shell reads them as separate
	// commands, not one mashed-together line.
	if !strings.Contains(script, `'cluster.status' 's3.bucket.list'`) {
		t.Errorf("lines not passed as separate quoted args:\n%s", script)
	}
	if !strings.Contains(script, "-master=127.0.0.1:9333") || !strings.Contains(script, "-filer=127.0.0.1:8888") {
		t.Errorf("wrong ports wired:\n%s", script)
	}
}

func TestWeedShellScriptRejectsSingleQuote(t *testing.T) {
	// A line carrying ' would break out of the single-quoted argument inside the
	// surrounding sh -c — rejected loudly, never silently mis-quoted.
	sc := &config.SeaweedfsConfig{Port: 8400, FilerPort: 8402}
	if _, err := weedShellScript(sc, []string{`fs.ls '/weird`}); err == nil {
		t.Fatal("single-quoted line accepted")
	}
}

func TestWeedShellScriptMetacharactersInert(t *testing.T) {
	// Inside single quotes every other shell metacharacter is literal — the
	// quoting is what makes that safe, so a $() or ; line must pass through
	// verbatim into weed's stdin rather than erroring.
	sc := &config.SeaweedfsConfig{Port: 8400, FilerPort: 8402}
	line := `fs.mkfile /a;b=$(whoami)`
	script, err := weedShellScript(sc, []string{line})
	if err != nil {
		t.Fatalf("metacharacter line rejected: %v", err)
	}
	if !strings.Contains(script, "'"+line+"'") {
		t.Errorf("line not embedded verbatim single-quoted:\n%s", script)
	}
}

// --- target resolution ------------------------------------------------------

func TestResolveSeaweedfsTarget(t *testing.T) {
	// No addons configured at all.
	if _, err := resolveSeaweedfsTarget(&config.Config{}, ""); err == nil {
		t.Error("empty config accepted")
	}

	c := &config.Config{}
	c.Addons.Seaweedfs = map[string]config.SeaweedfsConfig{
		"store": {Name: "store", ContainerName: "pgcli-seaweedfs-store"},
	}
	// --name absent falls back to the only instance.
	sc, err := resolveSeaweedfsTarget(c, "")
	if err != nil {
		t.Fatalf("fallback resolution failed: %v", err)
	}
	if sc.Name != "store" {
		t.Errorf("fallback picked %q, want store", sc.Name)
	}
	// Explicit --name.
	if _, err := resolveSeaweedfsTarget(c, "store"); err != nil {
		t.Errorf("named resolution failed: %v", err)
	}
	// Unknown --name must list what exists.
	if _, err := resolveSeaweedfsTarget(c, "nope"); err == nil {
		t.Error("unknown name accepted")
	}
}
