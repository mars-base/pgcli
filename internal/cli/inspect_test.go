package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/inspect"
)

// findSub locates the inspect command registered on rootCmd.
func findSub(t *testing.T) *cobra.Command {
	t.Helper()
	for _, c := range rootCmd.Commands() {
		if c.Name() == "inspect" {
			return c
		}
	}
	t.Fatal("inspect command not registered on rootCmd")
	return nil
}

func TestInspectFlagsRegistered(t *testing.T) {
	c := findSub(t)
	want := map[string]string{
		"dsn": "string", "html": "string", "open": "bool",
		"full": "bool", "json": "bool", "interval": "duration", "top": "int",
	}
	for name, typ := range want {
		f := c.Flags().Lookup(name)
		if f == nil {
			t.Errorf("flag --%s not registered", name)
			continue
		}
		if f.Value.Type() != typ {
			t.Errorf("flag --%s type = %s, want %s", name, f.Value.Type(), typ)
		}
	}
	// -o is taken by the global persistent --output; inspect must not re-add it.
	if c.Flags().ShorthandLookup("o") != nil {
		t.Error("inspect must not claim the -o shorthand (reserved by global --output)")
	}
}

func TestInspectOpenNeedsHTML(t *testing.T) {
	c := findSub(t)
	// Reset flags between the two RunE invocations.
	_ = c.Flags().Set("html", "")
	_ = c.Flags().Set("open", "true")
	// No --dsn and no reachable container, but the --open/--html guard must
	// reject the combination before any podman/container work happens.
	err := c.RunE(c, []string{})
	if err == nil {
		t.Fatal("expected an error for --open without --html")
	}
	if want := "--open needs --html"; err.Error()[:len(want)] != want {
		t.Errorf("unexpected error: %v", err)
	}
	_ = c.Flags().Set("open", "false")
}

func TestReadOnlyWrapper(t *testing.T) {
	got := readOnly("SELECT 1;")
	want := "BEGIN TRANSACTION READ ONLY; SELECT 1; COMMIT;"
	if got != want {
		t.Errorf("readOnly() = %q, want %q", got, want)
	}
	// A statement without its own terminator must not let COMMIT parse as a
	// column alias (the pgss probe has no trailing semicolon).
	got = readOnly("SELECT 'x' WHERE 1 = 1")
	want = "BEGIN TRANSACTION READ ONLY; SELECT 'x' WHERE 1 = 1; COMMIT;"
	if got != want {
		t.Errorf("readOnly() = %q, want %q", got, want)
	}
}

func TestRedactDSN(t *testing.T) {
	cases := map[string]string{
		"postgres://u:secret@db.example:5433/app": "db.example:5433/app",
		"postgresql://u:s@h:6000/d":               "h:6000/d",
		"not-a-dsn":                               "remote database",
	}
	for in, want := range cases {
		if got := redactDSN(in); got != want {
			t.Errorf("redactDSN(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeQ returns canned psql -tA JSON for the two probe queries the runner
// issues before any section, and an empty result otherwise. It proves the CLI
// package can drive inspect.Run against a Querier without a live container.
type fakeQ struct{}

func (fakeQ) Query(sql string) (string, error) {
	if strings.Contains(sql, "server_version_num") {
		return "150004\n", nil
	}
	return "\n", nil
}

func TestInspectRunThroughQuerier(t *testing.T) {
	c, err := inspect.Run(fakeQ{}, "fake", inspect.Options{})
	if err != nil {
		t.Fatalf("inspect.Run: %v", err)
	}
	if c.Version != 150004 || c.Server != "postgres 15.4" {
		t.Errorf("version probe through the interface wrong: %+v", c)
	}
	// Every section degraded to empty but the run must still yield findings.
	if len(c.Findings) == 0 {
		t.Error("expected at least the degradation NOTE findings")
	}
}
