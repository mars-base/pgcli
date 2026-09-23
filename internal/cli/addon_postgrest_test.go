package cli

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// postgrestTestCfg builds a config with one Patroni scope (member "node1" on
// 127.0.0.1:5432) and optionally an HAProxy listener fronting that scope.
func postgrestTestCfg(withLB bool) *config.Config {
	cfg := config.Default()
	cfg.Addons.Patroni = map[string]config.PatroniClusterConfig{
		"app": {
			Name: "app",
			Members: map[string]config.PatroniMemberConfig{
				"node1": {HostPort: 5432},
			},
		},
	}
	if withLB {
		cfg.Addons.HAProxy = map[string]config.HAProxyConfig{
			"lb": {Name: "lb", HAScope: "app", Listen: "127.0.0.1", WritePort: 5000},
		}
	}
	return cfg
}

func TestPostgrestPatroniMemberWarning(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *config.Config
		dsn     string
		wantSub []string // substrings the warning must contain (empty = no warning)
	}{
		{
			name: "member direct port with LB present suggests LB",
			cfg:  postgrestTestCfg(true),
			dsn:  "postgres://auth:pw@127.0.0.1:5432/appdb",
			wantSub: []string{
				"node1", "direct PG port", "127.0.0.1:5432", "scope \"app\"",
				"127.0.0.1:5000", // the suggested LB endpoint
			},
		},
		{
			name: "member direct port without LB suggests installing haproxy",
			cfg:  postgrestTestCfg(false),
			dsn:  "postgres://auth:pw@127.0.0.1:5432/appdb",
			wantSub: []string{
				"node1", "direct PG port", "pg addon install haproxy --ha app",
			},
		},
		{
			name:    "DSN already points at the LB port - no warning",
			cfg:     postgrestTestCfg(true),
			dsn:     "postgres://auth:pw@127.0.0.1:5000/appdb",
			wantSub: nil,
		},
		{
			name:    "non-Patroni backend (plain instance) - no warning",
			cfg:     postgrestTestCfg(true),
			dsn:     "postgres://auth:pw@10.9.9.9:5432/appdb",
			wantSub: nil,
		},
		{
			name:    "localhost host matches a 127.0.0.1 member",
			cfg:     postgrestTestCfg(true),
			dsn:     "postgres://auth:pw@localhost:5432/appdb",
			wantSub: []string{"node1", "direct PG port"},
		},
		{
			name:    "unparseable DSN yields no opinion",
			cfg:     postgrestTestCfg(true),
			dsn:     "not-a-dsn",
			wantSub: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := postgrestPatroniMemberWarning(tc.cfg, tc.dsn)
			if len(tc.wantSub) == 0 {
				if got != "" {
					t.Fatalf("expected no warning, got %q", got)
				}
				return
			}
			if got == "" {
				t.Fatal("expected a warning, got none")
			}
			for _, sub := range tc.wantSub {
				if !strings.Contains(got, sub) {
					t.Errorf("warning missing %q:\n%s", sub, got)
				}
			}
		})
	}
}

func TestPostgrestSchemasAndPoolDisplay(t *testing.T) {
	if got := postgrestSchemasDisplay(""); got != "public (PostgREST default)" {
		t.Errorf("empty schemas display = %q", got)
	}
	if got := postgrestSchemasDisplay("api"); got != "api" {
		t.Errorf("schemas display = %q, want api", got)
	}
	if got := postgrestPoolDisplay(0); got != "10 (PostgREST default)" {
		t.Errorf("empty pool display = %q", got)
	}
	if got := postgrestPoolDisplay(3); got != "3" {
		t.Errorf("pool display = %q, want 3", got)
	}
}
