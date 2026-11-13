package podman

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// TestRenderPrivoxyConfig pins the rendered config: listener, stdout logging,
// and the ACL ordering rules (permits before denies, catch-all permit for a
// deny-only list, IPv6-aware catch-all).
func TestRenderPrivoxyConfig(t *testing.T) {
	tests := []struct {
		name    string
		pc      *config.PrivoxyConfig
		wantErr bool
		check   func(t *testing.T, cfg string)
	}{
		{
			name: "defaults and no ACLs",
			pc:   &config.PrivoxyConfig{Name: "p1", Listen: "0.0.0.0", Port: 8118},
			check: func(t *testing.T, cfg string) {
				for _, want := range []string{
					"listen-address 0.0.0.0:8118",
					"logdir /var/log/privoxy",
					"logfile /dev/stdout",
					"toggle 1",
					"enable-remote-toggle 0",
					"enable-edit-actions 0",
					"enforce-blocks 0",
				} {
					if !strings.Contains(cfg, want) {
						t.Errorf("missing %q:\n%s", want, cfg)
					}
				}
				if strings.Contains(cfg, "permit-access") || strings.Contains(cfg, "deny-access") {
					t.Errorf("unexpected ACL lines:\n%s", cfg)
				}
			},
		},
		{
			name: "allow only is a whitelist",
			pc: &config.PrivoxyConfig{
				Name: "p1", Listen: "0.0.0.0", Port: 8118,
				AllowNetworks: []string{"10.0.0.0/8", "192.168.0.0/16"},
			},
			check: func(t *testing.T, cfg string) {
				for _, want := range []string{
					"permit-access 10.0.0.0/8\n",
					"permit-access 192.168.0.0/16\n",
				} {
					if !strings.Contains(cfg, want) {
						t.Errorf("missing %q:\n%s", want, cfg)
					}
				}
				if strings.Contains(cfg, "deny-access") {
					t.Errorf("unexpected deny-access:\n%s", cfg)
				}
				if strings.Contains(cfg, "permit-access 0.0.0.0/0") {
					t.Errorf("whitelist mode must not emit a catch-all permit:\n%s", cfg)
				}
			},
		},
		{
			name: "deny only emits catch-all permit first",
			pc: &config.PrivoxyConfig{
				Name: "p1", Listen: "0.0.0.0", Port: 8118,
				DenyNetworks: []string{"10.0.0.66/32"},
			},
			check: func(t *testing.T, cfg string) {
				permit := strings.Index(cfg, "permit-access 0.0.0.0/0")
				deny := strings.Index(cfg, "deny-access 10.0.0.66/32")
				if permit < 0 || deny < 0 {
					t.Fatalf("missing catch-all permit or deny:\n%s", cfg)
				}
				if permit > deny {
					t.Errorf("catch-all permit must precede denies:\n%s", cfg)
				}
			},
		},
		{
			name: "allow and deny render permits first",
			pc: &config.PrivoxyConfig{
				Name: "p1", Listen: "0.0.0.0", Port: 8118,
				AllowNetworks: []string{"10.0.0.0/8"},
				DenyNetworks:  []string{"10.0.0.66/32"},
			},
			check: func(t *testing.T, cfg string) {
				permit := strings.Index(cfg, "permit-access 10.0.0.0/8")
				deny := strings.Index(cfg, "deny-access 10.0.0.66/32")
				if permit < 0 || deny < 0 {
					t.Fatalf("missing permit or deny:\n%s", cfg)
				}
				if permit > deny {
					t.Errorf("permits must precede denies:\n%s", cfg)
				}
				if strings.Contains(cfg, "permit-access 0.0.0.0/0") {
					t.Errorf("mixed mode must not emit a catch-all permit:\n%s", cfg)
				}
			},
		},
		{
			name: "comma-separated ACL values are merged",
			pc: &config.PrivoxyConfig{
				Name: "p1", Listen: "0.0.0.0", Port: 8118,
				AllowNetworks: []string{"10.0.0.0/8, 192.168.0.0/16", "172.16.0.0/12"},
			},
			check: func(t *testing.T, cfg string) {
				for _, want := range []string{
					"permit-access 10.0.0.0/8\n",
					"permit-access 192.168.0.0/16\n",
					"permit-access 172.16.0.0/12\n",
				} {
					if !strings.Contains(cfg, want) {
						t.Errorf("missing %q:\n%s", want, cfg)
					}
				}
			},
		},
		{
			name: "IPv6 listener uses IPv6 catch-all",
			pc: &config.PrivoxyConfig{
				Name: "p1", Listen: "::", Port: 8118,
				DenyNetworks: []string{"2001:db8::/32"},
			},
			check: func(t *testing.T, cfg string) {
				if !strings.Contains(cfg, "permit-access [::]/0\n") {
					t.Errorf("missing IPv6 catch-all permit:\n%s", cfg)
				}
				if strings.Contains(cfg, "permit-access 0.0.0.0/0") {
					t.Errorf("IPv6 listener must not emit the IPv4 catch-all:\n%s", cfg)
				}
			},
		},
		{
			name:    "missing port is an error",
			pc:      &config.PrivoxyConfig{Name: "p1", Listen: "0.0.0.0"},
			wantErr: true,
		},
		{
			name: "newline in ACL is rejected",
			pc: &config.PrivoxyConfig{
				Name: "p1", Listen: "0.0.0.0", Port: 8118,
				AllowNetworks: []string{"10.0.0.0/8\ndeny-access 0.0.0.0/0"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := RenderPrivoxyConfig(tt.pc)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got config:\n%s", cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("RenderPrivoxyConfig: %v", err)
			}
			tt.check(t, cfg)
		})
	}
}

// TestPrivoxyArgsAndEnv pins the container shape: host networking on Linux,
// bridge + published port on macOS, a read-only config mount, and the
// entrypoint override that avoids the image script rewriting the mount.
func TestPrivoxyArgsAndEnv(t *testing.T) {
	pc := &config.PrivoxyConfig{
		Name:          "p1",
		ContainerName: "pgcli-privoxy-p1",
		ImageTag:      "ghcr.io/mars-base/pgcli/pgcli-privoxy:4.2.0",
		Port:          8118,
	}

	t.Run("linux host network", func(t *testing.T) {
		args := privoxyArgsAndEnv(pc, false, "pgcli-net", "/tmp/privoxy")
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--network host") {
			t.Errorf("expected host network:\n%s", joined)
		}
		if strings.Contains(joined, " -p ") {
			t.Errorf("host networking must not publish ports:\n%s", joined)
		}
		for _, want := range []string{
			"run -d",
			"--name pgcli-privoxy-p1",
			"--http-proxy=false",
			"--restart unless-stopped",
			"--entrypoint /usr/sbin/privoxy",
			"-v /tmp/privoxy/config:/etc/privoxy/config:ro,z",
			"ghcr.io/mars-base/pgcli/pgcli-privoxy:4.2.0 --no-daemon --user privoxy /etc/privoxy/config",
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("missing %q:\n%s", want, joined)
			}
		}
	})

	t.Run("macOS bridge publishes the port", func(t *testing.T) {
		args := privoxyArgsAndEnv(pc, true, "pgcli-net", "/tmp/privoxy")
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--network pgcli-net") {
			t.Errorf("expected bridge network:\n%s", joined)
		}
		if !strings.Contains(joined, "-p 8118:8118") {
			t.Errorf("expected published port:\n%s", joined)
		}
	})
}
