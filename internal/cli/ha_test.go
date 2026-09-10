package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"

	"github.com/mars-base/pgcli/internal/config"
)

func TestCheckHAName(t *testing.T) {
	ok := []string{"app", "node1", "app-prod", "App_1", "a", "a1"}
	for _, n := range ok {
		if err := checkHAName("scope", n); err != nil {
			t.Errorf("checkHAName(%q) = %v, want nil", n, err)
		}
	}
	bad := []string{"", "-app", "app.x", "app x", "app/x", "app:8008", strings.Repeat("a", 33)}
	for _, n := range bad {
		if err := checkHAName("scope", n); err == nil {
			t.Errorf("checkHAName(%q) = nil, want error", n)
		}
	}
}

// PatroniScope bakes the namespace suffix into the DCS prefix (Patroni has no
// other namespace concept), so the same scope key in two pgcli namespaces lands
// under distinct prefixes and cannot cross-talk.
func TestPatroniScopeNamespacing(t *testing.T) {
	c := config.Default()
	c.Namespace = "x"
	if got := c.PatroniScope("app"); got != "app-x" {
		t.Errorf("PatroniScope(app) ns=x = %q, want app-x", got)
	}
	prod := config.Default()
	prod.Namespace = "prod"
	if a, b := prod.PatroniScope("app"), prod.PatroniScope("other"); a == b {
		t.Errorf("two different scopes must not share a DCS prefix: %q", a)
	}
}

func TestPgConnectHost(t *testing.T) {
	if h := pgConnectHost(config.PatroniMemberConfig{}); h != "127.0.0.1" {
		t.Errorf("loopback member: got %q", h)
	}
	mb := config.PatroniMemberConfig{AdvertiseHost: "10.0.0.11"}
	if h := pgConnectHost(mb); h != "10.0.0.11" {
		t.Errorf("advertised member: got %q", h)
	}
}

func TestPatroniYMLPathUsesNsScope(t *testing.T) {
	dir := t.TempDir()
	c := config.Default()
	c.BaseDir = dir
	c.Namespace = "prod"
	nsScope := c.PatroniScope("app") // "app-prod"
	got := patroniYMLPath(c, nsScope, "node1")
	want := filepath.Join(dir, "addon", "patroni", "app-prod", "node1", "patroni.yml")
	if filepath.ToSlash(got) != filepath.ToSlash(want) {
		t.Errorf("path = %q, want %q (must be keyed by nsScope, not raw scope)", got, want)
	}
}

func TestLoadPasswordsFile(t *testing.T) {
	dir := t.TempDir()

	// A well-formed passwords file.
	good := filepath.Join(dir, "p.yml")
	os.WriteFile(good, []byte("superuser: su\nreplication: re\nrewind: rw\nrestapi_password: rp\n"), 0600)
	var p config.PatroniPasswords
	if err := loadPasswordsFile(&p, good); err != nil {
		t.Fatalf("loadPasswordsFile: %v", err)
	}
	if p.Superuser != "su" || p.Replication != "re" || p.Rewind != "rw" || p.RestapiPasswd != "rp" {
		t.Errorf("parsed wrong: %+v", p)
	}
	if p.RestapiUser != "postgres" {
		t.Errorf("restapi_user should default to postgres, got %q", p.RestapiUser)
	}

	// Missing required keys must be rejected (a silent empty password would
	// brick cross-host auth).
	missing := filepath.Join(dir, "m.yml")
	os.WriteFile(missing, []byte("superuser: su\n"), 0600)
	var p2 config.PatroniPasswords
	if err := loadPasswordsFile(&p2, missing); err == nil {
		t.Error("expected error when restapi_password missing")
	}

	// Nonexistent file.
	var p3 config.PatroniPasswords
	if err := loadPasswordsFile(&p3, filepath.Join(dir, "nope.yml")); err == nil {
		t.Error("expected error for missing file")
	}
}

// TestPasswordsRoundTrip pins the contract of `pg ha passwords`: the YAML it
// marshals from a stored set must load back losslessly via loadPasswordsFile —
// the exporter's output format and --passwords-file's input format are the
// same keys, and a drift (e.g. a renamed yaml tag) would silently brick
// cross-host auth.
func TestPasswordsRoundTrip(t *testing.T) {
	src := config.PatroniPasswords{
		Superuser:     "su-pass",
		Replication:   "re-pass",
		Rewind:        "rw-pass",
		RestapiUser:   "apiuser",
		RestapiPasswd: "api-pass",
	}
	b, err := yaml.Marshal(&src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	file := filepath.Join(t.TempDir(), "p.yml")
	if err := os.WriteFile(file, b, 0600); err != nil {
		t.Fatal(err)
	}
	var got config.PatroniPasswords
	if err := loadPasswordsFile(&got, file); err != nil {
		t.Fatalf("loadPasswordsFile(marshalled): %v", err)
	}
	if got != src {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, src)
	}
}
