package podman

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// TestGrafanaArgsAndEnv pins the `podman run` argv for Grafana across both
// networking shapes. Grafana is env-only config plus one bind mount, so this
// function is the whole contract between a GrafanaConfig and the container.
func TestGrafanaArgsAndEnv(t *testing.T) {
	base := func() *config.GrafanaConfig {
		return &config.GrafanaConfig{
			ContainerName: "pgcli-grafana-ui",
			Name:          "ui",
			HostPort:      38300,
			Listen:        "0.0.0.0",
			AdminUser:     "admin",
			Password:      "web-login-pw",
			ImageTag:      config.DefaultGrafanaImageTag,
		}
	}

	hasEnv := func(args []string, kv string) bool {
		return slices.Contains(args, "-e") && slices.Contains(args, kv)
	}
	// hasMount reports whether some `-v <spec>` entry contains substr.
	hasMount := func(args []string, substr string) bool {
		for i, a := range args {
			if a == "-v" && i+1 < len(args) && strings.Contains(args[i+1], substr) {
				return true
			}
		}
		return false
	}
	// hasFlag reports whether flag appears immediately followed by value.
	hasFlag := func(args []string, flag, value string) bool {
		for i, a := range args {
			if a == flag && i+1 < len(args) && args[i+1] == value {
				return true
			}
		}
		return false
	}

	t.Run("linux host networking", func(t *testing.T) {
		gc := base()
		args := grafanaArgsAndEnv(gc, false, "pgcli-net", "/home/u/.pgcli/addon/grafana/ui/data")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "host") {
			t.Errorf("expected --network host, got %v", args)
		}
		if slices.Contains(args, "-p") {
			t.Errorf("host networking must not publish ports, got %v", args)
		}
		// The two web-login credentials, emitted verbatim.
		if !hasEnv(args, "GF_SECURITY_ADMIN_USER=admin") {
			t.Errorf("GF_SECURITY_ADMIN_USER missing: %v", args)
		}
		if !hasEnv(args, "GF_SECURITY_ADMIN_PASSWORD=web-login-pw") {
			t.Errorf("GF_SECURITY_ADMIN_PASSWORD missing: %v", args)
		}
		// The listen port is pinned to the assigned host port — without it
		// every instance defaults to 3000 and host networking collides.
		if !hasEnv(args, "GF_SERVER_HTTP_PORT=38300") {
			t.Errorf("GF_SERVER_HTTP_PORT=38300 missing: %v", args)
		}
		// Non-loopback default bind passes verbatim through on Linux.
		if !hasEnv(args, "GF_SERVER_HTTP_ADDR=0.0.0.0") {
			t.Errorf("GF_SERVER_HTTP_ADDR=0.0.0.0 missing: %v", args)
		}
		if !hasEnv(args, "GF_ANALYTICS_REPORTING_ENABLED=false") {
			t.Errorf("analytics reporting should be disabled: %v", args)
		}
		if !hasEnv(args, "GF_USERS_ALLOW_SIGN_UP=false") {
			t.Errorf("self-signup should be closed: %v", args)
		}
		// The defining ownership contrast with pgAdmin: NO --user flag. The
		// image's baked USER 472 must run; --user 0 would run Grafana as root
		// and leave root-owned files in the data dir. Ownership is instead
		// handled on the host in createContainer.
		if slices.Contains(args, "--user") {
			t.Errorf("Grafana must not pass --user (the image runs as 472): %v", args)
		}
		if !slices.Contains(args, "--http-proxy=false") {
			t.Errorf("--http-proxy=false missing: %v", args)
		}
		if !hasFlag(args, "--restart", "unless-stopped") {
			t.Errorf("--restart unless-stopped missing: %v", args)
		}
		if !hasMount(args, "/home/u/.pgcli/addon/grafana/ui/data:/var/lib/grafana:z") {
			t.Errorf("data dir mount missing: %v", args)
		}
		if args[len(args)-1] != config.DefaultGrafanaImageTag {
			t.Errorf("image = %q, want last arg %q", args[len(args)-1], config.DefaultGrafanaImageTag)
		}
	})

	t.Run("macOS bridge networking publishes port and keeps a non-loopback bind", func(t *testing.T) {
		gc := base()
		gc.Listen = "127.0.0.1" // even a loopback listen is widened under bridge
		args := grafanaArgsAndEnv(gc, true, "pgcli-net", "/data")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "pgcli-net") {
			t.Errorf("expected --network pgcli-net, got %v", args)
		}
		if !slices.Contains(args, "-p") || !slices.Contains(args, "38300:38300") {
			t.Errorf("bridge must publish 38300:38300, got %v", args)
		}
		// The published port can't reach a loopback-only bind.
		if !hasEnv(args, "GF_SERVER_HTTP_ADDR=0.0.0.0") {
			t.Errorf("bridge should widen loopback to 0.0.0.0, got %v", args)
		}
		if hasEnv(args, "GF_SERVER_HTTP_ADDR=127.0.0.1") {
			t.Errorf("bridge must not keep the loopback bind: %v", args)
		}
	})

	t.Run("explicit non-loopback listen passes through", func(t *testing.T) {
		gc := base()
		gc.Listen = "10.0.0.5"
		args := grafanaArgsAndEnv(gc, true, "pgcli-net", "/data")
		if !hasEnv(args, "GF_SERVER_HTTP_ADDR=10.0.0.5") {
			t.Errorf("explicit bind should pass through under bridge: %v", args)
		}
	})
}

// TestGrafanaStoreExists pins the revive check: GF_SECURITY_ADMIN_* only
// applies to an EMPTY data dir, so install must be able to tell a first init
// from a revival of a kept grafana.db.
func TestGrafanaStoreExists(t *testing.T) {
	dir := t.TempDir()
	if GrafanaStoreExists(dir) {
		t.Fatal("empty dir should not count as an existing store")
	}
	if _, err := os.Create(filepath.Join(dir, "grafana.db")); err != nil {
		t.Fatal(err)
	}
	if !GrafanaStoreExists(dir) {
		t.Fatal("dir holding grafana.db should count as an existing store")
	}
	if GrafanaStoreExists("") {
		t.Fatal("empty path is never an existing store")
	}
}
