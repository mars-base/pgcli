package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/mars-base/pgcli/internal/config"
)

func findChild(t *testing.T, parent *cobra.Command, name string) *cobra.Command {
	t.Helper()
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	t.Fatalf("%s has no subcommand %q", parent.Name(), name)
	return nil
}

func TestSQLiteCommandTree(t *testing.T) {
	root := findChild(t, rootCmd, "sqlite")
	for _, name := range []string{"install", "create", "list", "info", "shell", "exec", "remove", "replication"} {
		findChild(t, root, name)
	}
	repl := findChild(t, root, "replication")
	for _, name := range []string{"enable", "disable", "start", "stop", "status", "logs", "restore"} {
		findChild(t, repl, name)
	}
}

func TestSQLiteRawCommandsDisableFlagParsing(t *testing.T) {
	root := findChild(t, rootCmd, "sqlite")
	for _, name := range []string{"shell", "exec"} {
		c := findChild(t, root, name)
		if !c.DisableFlagParsing {
			t.Errorf("pg sqlite %s must disable flag parsing so SQL and sqlite3 options pass through", name)
		}
	}
}

func TestSQLiteFlagShapes(t *testing.T) {
	root := findChild(t, rootCmd, "sqlite")
	create := findChild(t, root, "create")
	if f := create.Flags().Lookup("path"); f == nil || f.Value.Type() != "string" {
		t.Error("pg sqlite create --path must be a string flag")
	}
	info := findChild(t, root, "info")
	if f := info.Flags().Lookup("integrity"); f == nil || f.DefValue != "quick" {
		t.Error("pg sqlite info --integrity must default to quick")
	}
	remove := findChild(t, root, "remove")
	for _, name := range []string{"clean-data", "force"} {
		if f := remove.Flags().Lookup(name); f == nil || f.Value.Type() != "bool" {
			t.Errorf("pg sqlite remove --%s must be a bool flag", name)
		}
	}

	repl := findChild(t, root, "replication")
	enable := findChild(t, repl, "enable")
	wantDefaults := map[string]string{
		"s3-bucket":         "",
		"s3-path":           "",
		"s3-region":         "",
		"s3-uri-style":      "path",
		"sync-interval":     "1s",
		"snapshot-interval": "24h",
		"retention":         "24h",
	}
	for name, def := range wantDefaults {
		f := enable.Flags().Lookup(name)
		if f == nil {
			t.Errorf("pg sqlite replication enable --%s is not registered", name)
			continue
		}
		if f.DefValue != def {
			t.Errorf("--%s default = %q, want %q", name, f.DefValue, def)
		}
	}
	for _, name := range []string{"s3-skip-verify", "no-start", "force"} {
		if f := enable.Flags().Lookup(name); f == nil || f.Value.Type() != "bool" {
			t.Errorf("--%s must be a bool flag", name)
		}
	}

	restore := findChild(t, repl, "restore")
	for _, name := range []string{"target", "timestamp", "txid"} {
		if f := restore.Flags().Lookup(name); f == nil || f.Value.Type() != "string" {
			t.Errorf("pg sqlite replication restore --%s must be a string flag", name)
		}
	}
	if f := restore.Flags().Lookup("integrity"); f == nil || f.DefValue != "quick" {
		t.Error("restore --integrity must default to quick")
	}
	for _, name := range []string{"force", "dry-run", "restart"} {
		if f := restore.Flags().Lookup(name); f == nil || f.Value.Type() != "bool" {
			t.Errorf("restore --%s must be a bool flag", name)
		}
	}

	logs := findChild(t, repl, "logs")
	if f := logs.Flags().Lookup("tail"); f == nil || f.DefValue != "50" || f.Shorthand != "n" {
		t.Error("logs --tail must be -n/--tail with default 50")
	}
	if f := logs.Flags().Lookup("follow"); f == nil || f.Shorthand != "f" {
		t.Error("logs --follow must be -f/--follow")
	}
}

func TestSQLiteNameAndArgs(t *testing.T) {
	tests := []struct {
		args []string
		name string
		rest []string
		ok   bool
	}{
		{[]string{"appdb"}, "appdb", nil, true},
		{[]string{"appdb", "--", "-header", "-column"}, "appdb", []string{"-header", "-column"}, true},
		{[]string{"--", "appdb", "-header"}, "appdb", []string{"-header"}, true},
		{[]string{"appdb", "--"}, "appdb", nil, true},
		{[]string{"appdb", "SELECT 1"}, "appdb", []string{"SELECT 1"}, true},
		{nil, "", nil, false},
		{[]string{"--"}, "", nil, false},
	}
	for _, tt := range tests {
		name, rest, ok := sqliteNameAndArgs(tt.args)
		if name != tt.name || ok != tt.ok {
			t.Errorf("sqliteNameAndArgs(%q) = (%q, %q, %v), want (%q, _, %v)", tt.args, name, rest, ok, tt.name, tt.ok)
			continue
		}
		if len(rest) != len(tt.rest) {
			t.Errorf("sqliteNameAndArgs(%q) rest = %q, want %q", tt.args, rest, tt.rest)
			continue
		}
		for i := range rest {
			if rest[i] != tt.rest[i] {
				t.Errorf("sqliteNameAndArgs(%q) rest = %q, want %q", tt.args, rest, tt.rest)
				break
			}
		}
	}
}

func TestSQLiteS3Prefix(t *testing.T) {
	tests := []struct{ flag, name, want string }{
		{"", "appdb", "sqlite/appdb"},
		{"/sqlite/appdb", "appdb", "sqlite/appdb"},
		{"//custom/prefix", "appdb", "custom/prefix"},
		{"  custom  ", "appdb", "custom"},
		{"/", "appdb", "sqlite/appdb"},
	}
	for _, tt := range tests {
		if got := sqliteS3Prefix(tt.flag, tt.name); got != tt.want {
			t.Errorf("sqliteS3Prefix(%q, %q) = %q, want %q", tt.flag, tt.name, got, tt.want)
		}
	}
}

func TestValidatePositiveDuration(t *testing.T) {
	for _, value := range []string{"1s", "24h", "1m30s"} {
		if err := validatePositiveDuration("--x", value); err != nil {
			t.Errorf("validatePositiveDuration(%q) = %v", value, err)
		}
	}
	for _, value := range []string{"", "0s", "-1s", "yesterday"} {
		if err := validatePositiveDuration("--x", value); err == nil {
			t.Errorf("validatePositiveDuration(%q) = nil, want error", value)
		}
	}
}

// --output/-o is a global pg flag used to write pg.yaml; the sqlite family
// reserves --target for restore output and must not shadow it locally.
func TestSQLiteFamilyHasNoOutputShorthand(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		c.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
			if f.Name == "output" || f.Shorthand == "o" {
				t.Errorf("%s registers -%s/--%s; use --target instead", c.CommandPath(), f.Shorthand, f.Name)
			}
		})
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(findChild(t, rootCmd, "sqlite"))
}

func TestAutostartSQLiteSelectorFlag(t *testing.T) {
	autostart := findChild(t, rootCmd, "autostart")
	for _, name := range []string{"enable", "disable"} {
		c := findChild(t, autostart, name)
		if f := c.Flags().Lookup("sqlite"); f == nil || f.Value.Type() != "bool" {
			t.Errorf("pg autostart %s --sqlite must be a bool flag", name)
		}
	}
}

func TestAutostartSQLiteTargets(t *testing.T) {
	c := &config.Config{
		Addons: config.TopAddonsConfig{
			SQLite: map[string]config.SQLiteConfig{
				"appdb": {
					Name:        "appdb",
					Path:        "/tmp/appdb.db",
					Autostart:   true,
					Replication: &config.SQLiteReplicationConfig{S3: &config.SQLiteS3Config{Bucket: "b"}},
				},
				"idle": {Name: "idle", Path: "/tmp/idle.db"},
			},
		},
	}
	if !hasAutostartSQLite(c) {
		t.Error("hasAutostartSQLite = false, want true")
	}
	if n := countAutostartTargets(c); n != 1 {
		t.Errorf("countAutostartTargets = %d, want 1", n)
	}
	c.Addons.SQLite["appdb"] = config.SQLiteConfig{Name: "appdb", Path: "/tmp/appdb.db"}
	if hasAutostartSQLite(c) {
		t.Error("hasAutostartSQLite = true after clearing autostart, want false")
	}
}
