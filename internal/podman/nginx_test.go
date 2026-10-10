package podman

import (
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// TestRenderNginxCfg pins the rendered nginx.conf across the common shapes:
// single backend, multiple backends, TLS enabled, and the error path (no
// backends). The output is a single-file config (no conf.d includes) with
// upstream and server blocks.
func TestRenderNginxCfg(t *testing.T) {
	tests := []struct {
		name    string
		nc      *config.NginxConfig
		wantErr bool
		check   func(t *testing.T, cfg string)
	}{
		{
			name: "single backend HTTP only",
			nc: &config.NginxConfig{
				Name:     "proxy",
				Listen:   "127.0.0.1",
				HTTPPort: 8080,
				Backends: []config.NginxBackend{
					{Name: "pgadmin", Path: "/admin", Backend: "127.0.0.1:5050"},
				},
			},
			check: func(t *testing.T, cfg string) {
				// Upstream block present
				if !strings.Contains(cfg, "upstream pgadmin {") {
					t.Errorf("missing upstream pgadmin block:\n%s", cfg)
				}
				if !strings.Contains(cfg, "server 127.0.0.1:5050;") {
					t.Errorf("missing upstream server line:\n%s", cfg)
				}
				// HTTP server block
				if !strings.Contains(cfg, "listen 127.0.0.1:8080;") {
					t.Errorf("missing HTTP listen:\n%s", cfg)
				}
				if !strings.Contains(cfg, "location /admin {") {
					t.Errorf("missing location /admin:\n%s", cfg)
				}
				if !strings.Contains(cfg, "proxy_pass http://pgadmin;") {
					t.Errorf("missing proxy_pass:\n%s", cfg)
				}
				// No TLS block
				if strings.Contains(cfg, "ssl") {
					t.Errorf("should not have ssl block without TLS:\n%s", cfg)
				}
			},
		},
		{
			name: "multiple backends with root path",
			nc: &config.NginxConfig{
				Name:     "proxy",
				Listen:   "0.0.0.0",
				HTTPPort: 8080,
				Backends: []config.NginxBackend{
					{Name: "pgadmin", Path: "/admin", Backend: "127.0.0.1:5050"},
					{Name: "postgrest", Path: "/api", Backend: "127.0.0.1:3500"},
					{Name: "root", Path: "/", Backend: "127.0.0.1:8000"},
				},
			},
			check: func(t *testing.T, cfg string) {
				// All three upstreams
				for _, name := range []string{"pgadmin", "postgrest", "root"} {
					if !strings.Contains(cfg, "upstream "+name+" {") {
						t.Errorf("missing upstream %s:\n%s", name, cfg)
					}
				}
				// All three locations
				for _, path := range []string{"/admin", "/api", "/"} {
					if !strings.Contains(cfg, "location "+path+" {") {
						t.Errorf("missing location %s:\n%s", path, cfg)
					}
				}
			},
		},
		{
			name: "TLS enabled with HTTPS listener",
			nc: &config.NginxConfig{
				Name:      "proxy",
				Listen:    "127.0.0.1",
				HTTPPort:  8080,
				HTTPSPort: 8443,
				TLS:       true,
				Backends: []config.NginxBackend{
					{Name: "pgadmin", Path: "/admin", Backend: "127.0.0.1:5050"},
				},
			},
			check: func(t *testing.T, cfg string) {
				// HTTP listener
				if !strings.Contains(cfg, "listen 127.0.0.1:8080;") {
					t.Errorf("missing HTTP listen:\n%s", cfg)
				}
				// HTTPS listener
				if !strings.Contains(cfg, "listen 127.0.0.1:8443 ssl;") {
					t.Errorf("missing HTTPS ssl listen:\n%s", cfg)
				}
				// TLS directives
				if !strings.Contains(cfg, "ssl_certificate") {
					t.Errorf("missing ssl_certificate:\n%s", cfg)
				}
				if !strings.Contains(cfg, "ssl_protocols TLSv1.2 TLSv1.3;") {
					t.Errorf("missing ssl_protocols:\n%s", cfg)
				}
				// Both server blocks have the location
				count := strings.Count(cfg, "location /admin {")
				if count != 2 {
					t.Errorf("expected 2 location blocks (HTTP+HTTPS), got %d:\n%s", count, cfg)
				}
			},
		},
		{
			name: "TLS without HTTPSPort skips HTTPS block",
			nc: &config.NginxConfig{
				Name:     "proxy",
				Listen:   "127.0.0.1",
				HTTPPort: 8080,
				TLS:      true,
				Backends: []config.NginxBackend{
					{Name: "pgadmin", Path: "/admin", Backend: "127.0.0.1:5050"},
				},
			},
			check: func(t *testing.T, cfg string) {
				// TLS enabled but no port assigned → no HTTPS block
				if strings.Contains(cfg, "ssl") {
					t.Errorf("should not have ssl block without HTTPSPort:\n%s", cfg)
				}
			},
		},
		{
			name: "backend with empty path defaults to /",
			nc: &config.NginxConfig{
				Name:     "proxy",
				HTTPPort: 8080,
				Backends: []config.NginxBackend{
					{Name: "root", Path: "", Backend: "127.0.0.1:8000"},
				},
			},
			check: func(t *testing.T, cfg string) {
				if !strings.Contains(cfg, "location / {") {
					t.Errorf("empty path should default to /:\n%s", cfg)
				}
			},
		},
		{
			name: "backend with empty name gets sanitized upstream name",
			nc: &config.NginxConfig{
				Name:     "proxy",
				HTTPPort: 8080,
				Backends: []config.NginxBackend{
					{Name: "", Path: "/admin", Backend: "127.0.0.1:5050"},
				},
			},
			check: func(t *testing.T, cfg string) {
				// Name derived from path: /admin → upstream_admin
				if !strings.Contains(cfg, "upstream upstream_admin {") {
					t.Errorf("empty backend name should derive from path:\n%s", cfg)
				}
			},
		},
		{
			name: "no backends errors",
			nc: &config.NginxConfig{
				Name:     "proxy",
				HTTPPort: 8080,
				Backends: nil,
			},
			wantErr: true,
		},
		{
			name: "empty listen defaults to 127.0.0.1",
			nc: &config.NginxConfig{
				Name:     "proxy",
				Listen:   "",
				HTTPPort: 8080,
				Backends: []config.NginxBackend{
					{Name: "test", Path: "/", Backend: "127.0.0.1:8000"},
				},
			},
			check: func(t *testing.T, cfg string) {
				if !strings.Contains(cfg, "listen 127.0.0.1:8080;") {
					t.Errorf("empty listen should default to 127.0.0.1:\n%s", cfg)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := RenderNginxCfg(tc.nc)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got:\n%s", cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("RenderNginxCfg: %v", err)
			}
			if tc.check != nil {
				tc.check(t, cfg)
			}
		})
	}
}

// TestNginxArgsAndEnv pins the `podman run` argv across both networking shapes
// and the optional TLS cert mount. The function is pure (no podman calls) so
// the mount set and networking flags are unit-testable.
func TestNginxArgsAndEnv(t *testing.T) {
	nc := func() *config.NginxConfig {
		return &config.NginxConfig{
			ContainerName: "pgcli-nginx-proxy",
			Name:          "proxy",
			ImageTag:      DefaultNginxImageTag,
			HTTPPort:      8080,
			Listen:        "127.0.0.1",
		}
	}

	hasMount := func(args []string, substr string) bool {
		for i, a := range args {
			if a == "-v" && i+1 < len(args) && strings.Contains(args[i+1], substr) {
				return true
			}
		}
		return false
	}

	t.Run("linux host networking HTTP only", func(t *testing.T) {
		args := nginxArgsAndEnv(nc(), false, "pgcli-net", "/home/u/.pgcli/addon/nginx/proxy")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "host") {
			t.Errorf("expected --network host, got %v", args)
		}
		if slices.Contains(args, "-p") {
			t.Errorf("host networking must not publish ports, got %v", args)
		}
		// nginx.conf mount (always present)
		if !hasMount(args, "nginx.conf:/etc/nginx/nginx.conf:ro") {
			t.Errorf("nginx.conf mount missing: %v", args)
		}
		// No TLS → no cert mount
		if hasMount(args, "/etc/nginx/certs") {
			t.Errorf("cert mount should be absent without TLS: %v", args)
		}
		if !slices.Contains(args, "--http-proxy=false") {
			t.Errorf("--http-proxy=false missing: %v", args)
		}
		if !slices.Contains(args, "--restart") || !slices.Contains(args, "unless-stopped") {
			t.Errorf("--restart unless-stopped missing: %v", args)
		}
		if args[len(args)-1] != "daemon off;" || args[len(args)-2] != "-g" || args[len(args)-3] != "nginx" {
			t.Errorf("image args should end with 'nginx -g daemon off;', got %v", args[len(args)-3:])
		}
		if args[len(args)-4] != DefaultNginxImageTag {
			t.Errorf("image = %q, want %q before nginx args", args[len(args)-4], DefaultNginxImageTag)
		}
	})

	t.Run("macOS bridge networking publishes ports", func(t *testing.T) {
		args := nginxArgsAndEnv(nc(), true, "pgcli-net", "/data")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "pgcli-net") {
			t.Errorf("expected --network pgcli-net, got %v", args)
		}
		if !slices.Contains(args, "-p") || !slices.Contains(args, "8080:8080") {
			t.Errorf("bridge must publish 8080:8080, got %v", args)
		}
	})

	t.Run("TLS adds cert mount and publishes HTTPS port", func(t *testing.T) {
		c := nc()
		c.TLS = true
		c.HTTPSPort = 8443
		args := nginxArgsAndEnv(c, false, "pgcli-net", "/home/u/.pgcli/addon/nginx/proxy")

		// HTTPS port published under bridge
		argsBridge := nginxArgsAndEnv(c, true, "pgcli-net", "/data")
		if !slices.Contains(argsBridge, "8443:8443") {
			t.Errorf("bridge must publish HTTPS port 8443:8443, got %v", argsBridge)
		}

		// Cert mount present
		if !hasMount(args, "/etc/nginx/certs:ro") {
			t.Errorf("cert mount missing with TLS: %v", args)
		}
		// nginx.conf still mounted
		if !hasMount(args, "nginx.conf:/etc/nginx/nginx.conf:ro") {
			t.Errorf("nginx.conf mount missing: %v", args)
		}
	})

	t.Run("no cert mount without TLS", func(t *testing.T) {
		args := nginxArgsAndEnv(nc(), false, "pgcli-net", "/data")
		if hasMount(args, "/etc/nginx/certs") {
			t.Errorf("cert mount should be absent without TLS: %v", args)
		}
	})
}

// TestSanitizeNginxName checks the upstream name derivation from a location
// path: leading slash stripped, invalid chars replaced with underscores.
func TestSanitizeNginxName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/admin", "admin"},
		{"/", "root"},
		{"", "root"},
		{"/api/v2", "api_v2"},
		{"/my-app.dev", "my_app_dev"},
		{"host:8080/path", "host_8080_path"},
	}
	for _, tc := range tests {
		got := sanitizeNginxName(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeNginxName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
