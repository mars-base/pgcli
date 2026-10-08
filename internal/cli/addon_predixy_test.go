package cli

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// parsePredixyBackends is the pre-pull gate on --backend: every entry must be a
// host:port the proxy can dial, the list may arrive across repeated flags and/or
// one comma-separated run, and a node listed twice is rejected (the proxy dials
// each node once).
func TestParsePredixyBackends(t *testing.T) {
	cases := []struct {
		name  string
		specs []string
		want  []string
		bad   bool   // expect an error
		frag  string // substring the error must contain (empty = any error)
	}{
		{
			name:  "single",
			specs: []string{"127.0.0.1:6379"},
			want:  []string{"127.0.0.1:6379"},
		},
		{
			name:  "comma run in one value",
			specs: []string{"127.0.0.1:6379,127.0.0.1:6380,10.0.0.12:6379"},
			want:  []string{"127.0.0.1:6379", "127.0.0.1:6380", "10.0.0.12:6379"},
		},
		{
			name:  "repeated flags",
			specs: []string{"127.0.0.1:6379,127.0.0.1:6380", "10.0.0.12:6379"},
			want:  []string{"127.0.0.1:6379", "127.0.0.1:6380", "10.0.0.12:6379"},
		},
		{
			name:  "whitespace around entries is trimmed",
			specs: []string{" 127.0.0.1:6379 , 127.0.0.1:6380 "},
			want:  []string{"127.0.0.1:6379", "127.0.0.1:6380"},
		},
		{
			name:  "hostname not resolved, only shaped",
			specs: []string{"redis-1.example.com:6379"},
			want:  []string{"redis-1.example.com:6379"},
		},
		{
			name:  "ipv6 in brackets",
			specs: []string{"[::1]:6379"},
			want:  []string{"[::1]:6379"},
		},
		// An empty list is not parsePredixyBackends' error to raise — it returns
		// no entries and no error, and the install handler separately checks for
		// the required flag (rendering would fail too, but with a worse message).
		{name: "empty list is not an error here", specs: nil, want: nil},
		{name: "missing port", specs: []string{"127.0.0.1"}, bad: true, frag: "host:port"},
		{name: "empty host", specs: []string{":6379"}, bad: true, frag: "empty host"},
		{name: "non-numeric port", specs: []string{"127.0.0.1:redis"}, bad: true, frag: "invalid port"},
		{name: "port zero", specs: []string{"127.0.0.1:0"}, bad: true, frag: "invalid port"},
		{name: "port out of range", specs: []string{"127.0.0.1:70000"}, bad: true, frag: "invalid port"},
		{name: "empty entry from trailing comma", specs: []string{"127.0.0.1:6379,"}, bad: true, frag: "empty entry"},
		{name: "duplicate within one list", specs: []string{"127.0.0.1:6379,127.0.0.1:6379"}, bad: true, frag: "listed twice"},
		{name: "duplicate across flags", specs: []string{"127.0.0.1:6379", "127.0.0.1:6379"}, bad: true, frag: "listed twice"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePredixyBackends(tc.specs)
			if tc.bad {
				if err == nil {
					t.Fatalf("expected an error, got %v", got)
				}
				if tc.frag != "" && !strings.Contains(err.Error(), tc.frag) {
					t.Errorf("error %q missing %q", err.Error(), tc.frag)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// validatePredixyConfig runs on the merged config, so a hand-edited pg.yaml
// fails at the next install rather than at Predixy parse time. The password
// checks mirror what RenderPredixyCfg's quoted-string syntax can carry.
func TestValidatePredixyConfig(t *testing.T) {
	base := func() config.PredixyConfig {
		return config.PredixyConfig{
			Name:     "proxy",
			Password: "pw",
			Backend:  []string{"127.0.0.1:6379"},
		}
	}
	t.Run("valid", func(t *testing.T) {
		if err := validatePredixyConfig(base()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("no backends", func(t *testing.T) {
		pc := base()
		pc.Backend = nil
		if err := validatePredixyConfig(pc); err == nil {
			t.Fatal("expected an error for an empty backend list")
		}
	})
	t.Run("no password", func(t *testing.T) {
		pc := base()
		pc.Password = ""
		if err := validatePredixyConfig(pc); err == nil {
			t.Fatal("expected an error for an empty password")
		}
	})
	// The renderer wraps the password in double quotes with no escape; a quote
	// or newline would break the parse (or split one directive into two).
	t.Run("password with double quote", func(t *testing.T) {
		pc := base()
		pc.Password = `p"w`
		if err := validatePredixyConfig(pc); err == nil {
			t.Fatal("expected an error for a password containing a double quote")
		}
	})
	t.Run("password with newline", func(t *testing.T) {
		pc := base()
		pc.Password = "pw\n"
		if err := validatePredixyConfig(pc); err == nil {
			t.Fatal("expected an error for a password containing a newline")
		}
	})
	// The specials the redis generator produces (and typical operator passwords)
	// must pass — the quoting tolerates them.
	t.Run("password with shell specials", func(t *testing.T) {
		pc := base()
		pc.Password = `p@ss:w/rd!{}$x%y#z`
		if err := validatePredixyConfig(pc); err != nil {
			t.Fatalf("specials should be accepted: %v", err)
		}
	})
	t.Run("negative workers", func(t *testing.T) {
		pc := base()
		pc.Workers = -1
		if err := validatePredixyConfig(pc); err == nil {
			t.Fatal("expected an error for negative workers")
		}
	})
	// Zero is not rejected here: the install handler maps --workers 0 to
	// "leave the stored/default value", and ApplyDefaults/renderer turn 0 into 1.
	t.Run("zero workers", func(t *testing.T) {
		pc := base()
		pc.Workers = 0
		if err := validatePredixyConfig(pc); err != nil {
			t.Fatalf("zero workers should pass validation (renderer supplies the default): %v", err)
		}
	})
}
