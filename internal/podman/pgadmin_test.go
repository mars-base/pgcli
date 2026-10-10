package podman

import (
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// TestPgAdminArgsAndEnv pins the `podman run` argv for pgAdmin across both
// networking shapes and the optional servers.json seed. pgAdmin is env-only
// config (plus one bind mount), so this function is the whole contract between
// a PgAdminConfig and the container.
func TestPgAdminArgsAndEnv(t *testing.T) {
	base := func() *config.PgAdminConfig {
		return &config.PgAdminConfig{
			ContainerName: "pgcli-pgadmin-ui",
			Name:          "ui",
			HostPort:      38500,
			Listen:        "127.0.0.1",
			Email:         "admin@pgcli.lan",
			Password:      "web-login-pw",
			ImageTag:      config.DefaultPgAdminImageTag,
		}
	}

	hasEnv := func(args []string, kv string) bool {
		return slices.Contains(args, "-e") && slices.Contains(args, kv)
	}
	hasEnvKey := func(args []string, key string) bool {
		for i, a := range args {
			if a == "-e" && i+1 < len(args) && strings.HasPrefix(args[i+1], key+"=") {
				return true
			}
		}
		return false
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
		ac := base()
		args := pgadminArgsAndEnv(ac, false, "pgcli-net", "/home/u/.pgcli/addon/pgadmin/ui/data", "", "")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "host") {
			t.Errorf("expected --network host, got %v", args)
		}
		if slices.Contains(args, "-p") {
			t.Errorf("host networking must not publish ports, got %v", args)
		}
		// The two required web-login credentials, emitted verbatim.
		if !hasEnv(args, "PGADMIN_DEFAULT_EMAIL=admin@pgcli.lan") {
			t.Errorf("PGADMIN_DEFAULT_EMAIL missing: %v", args)
		}
		if !hasEnv(args, "PGADMIN_DEFAULT_PASSWORD=web-login-pw") {
			t.Errorf("PGADMIN_DEFAULT_PASSWORD missing: %v", args)
		}
		// The listen port is pinned to the assigned host port — without it the
		// image binds 80 (or 8080 in a restricted context) and host networking
		// would collide with anything else on the box.
		if !hasEnv(args, "PGADMIN_LISTEN_PORT=38500") {
			t.Errorf("PGADMIN_LISTEN_PORT=38500 missing: %v", args)
		}
		// Loopback bind kept verbatim on Linux.
		if !hasEnv(args, "PGADMIN_LISTEN_ADDRESS=127.0.0.1") {
			t.Errorf("PGADMIN_LISTEN_ADDRESS=127.0.0.1 missing: %v", args)
		}
		if !hasEnv(args, "PGADMIN_DISABLE_POSTFIX=1") {
			t.Errorf("PGADMIN_DISABLE_POSTFIX=1 missing: %v", args)
		}
		// --user 0 is the ownership mechanism: it makes the image's entrypoint
		// chown /var/lib/pgadmin to 5050 and su-exec down, so pgcli never chowns
		// on the host and needs no wrapper image.
		if !hasFlag(args, "--user", "0") {
			t.Errorf("--user 0 missing: %v", args)
		}
		if !slices.Contains(args, "--http-proxy=false") {
			t.Errorf("--http-proxy=false missing: %v", args)
		}
		if !hasFlag(args, "--restart", "unless-stopped") {
			t.Errorf("--restart unless-stopped missing: %v", args)
		}
		// The data dir mount is always present.
		if !hasMount(args, "/home/u/.pgcli/addon/pgadmin/ui/data:/var/lib/pgadmin:z") {
			t.Errorf("data dir mount missing: %v", args)
		}
		// No DSN seed: neither the servers.json mount nor the replace flag.
		if hasEnvKey(args, "PGADMIN_REPLACE_SERVERS_ON_STARTUP") {
			t.Errorf("PGADMIN_REPLACE_SERVERS_ON_STARTUP should be absent without a seed: %v", args)
		}
		if hasMount(args, "servers.json") {
			t.Errorf("servers.json mount should be absent without a seed: %v", args)
		}
		if args[len(args)-1] != config.DefaultPgAdminImageTag {
			t.Errorf("image = %q, want last arg %q", args[len(args)-1], config.DefaultPgAdminImageTag)
		}
	})

	t.Run("macOS bridge networking widens loopback bind and publishes port", func(t *testing.T) {
		ac := base()
		args := pgadminArgsAndEnv(ac, true, "pgcli-net", "/data", "", "")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "pgcli-net") {
			t.Errorf("expected --network pgcli-net, got %v", args)
		}
		if !slices.Contains(args, "-p") || !slices.Contains(args, "38500:38500") {
			t.Errorf("bridge must publish 38500:38500, got %v", args)
		}
		// The published port can't reach a loopback-only bind.
		if !hasEnv(args, "PGADMIN_LISTEN_ADDRESS=0.0.0.0") {
			t.Errorf("bridge should widen loopback to 0.0.0.0, got %v", args)
		}
		if hasEnv(args, "PGADMIN_LISTEN_ADDRESS=127.0.0.1") {
			t.Errorf("bridge must not keep the loopback bind: %v", args)
		}
	})

	t.Run("explicit non-loopback listen passes through", func(t *testing.T) {
		ac := base()
		ac.Listen = "10.0.0.5"
		args := pgadminArgsAndEnv(ac, true, "pgcli-net", "/data", "", "")
		if !hasEnv(args, "PGADMIN_LISTEN_ADDRESS=10.0.0.5") {
			t.Errorf("explicit bind should pass through under bridge: %v", args)
		}
	})

	// The seed is declarative: PGADMIN_REPLACE_SERVERS_ON_STARTUP makes pgAdmin
	// re-load servers.json on every start (not just the first), so a reinstall
	// that points --pg-name/--dsn somewhere else actually takes effect.
	t.Run("servers.json seed mounts read-only and enables replace", func(t *testing.T) {
		ac := base()
		args := pgadminArgsAndEnv(ac, false, "pgcli-net", "/data", "/home/u/.pgcli/addon/pgadmin/ui/servers.json", "")

		if !hasMount(args, "/home/u/.pgcli/addon/pgadmin/ui/servers.json:/pgadmin4/servers.json:ro,z") {
			t.Errorf("servers.json read-only mount missing: %v", args)
		}
		if !hasEnv(args, "PGADMIN_REPLACE_SERVERS_ON_STARTUP=True") {
			t.Errorf("PGADMIN_REPLACE_SERVERS_ON_STARTUP=True missing: %v", args)
		}
	})

	// The seed passfile mounts INSIDE /var/lib/pgadmin so the image's
	// entrypoint chown -R reaches it (libpq refuses a passfile not owned by the
	// connecting uid) — and therefore must NOT be read-only, or that chown
	// fails with EROFS.
	t.Run("seed passfile mounts writable inside the chowned tree", func(t *testing.T) {
		ac := base()
		args := pgadminArgsAndEnv(ac, false, "pgcli-net", "/data",
			"/home/u/.pgcli/addon/pgadmin/ui/servers.json",
			"/home/u/.pgcli/addon/pgadmin/ui/pgpass")

		if !hasMount(args, "/home/u/.pgcli/addon/pgadmin/ui/pgpass:/var/lib/pgadmin/pgpass:z") {
			t.Errorf("pgpass mount missing: %v", args)
		}
		if hasMount(args, "/var/lib/pgadmin/pgpass:ro") {
			t.Errorf("pgpass must NOT be :ro (the entrypoint chowns it to 5050): %v", args)
		}
		// No PGPASSFILE env: pgAdmin takes the path from
		// servers.json's ConnectionParameters.passfile instead.
		if hasEnvKey(args, "PGPASSFILE") {
			t.Errorf("PGPASSFILE should be absent (passfile is a per-server param): %v", args)
		}
	})

	t.Run("no passfile mount without a seed password", func(t *testing.T) {
		ac := base()
		args := pgadminArgsAndEnv(ac, false, "pgcli-net", "/data", "", "")
		if hasMount(args, "pgpass") {
			t.Errorf("pgpass mount should be absent without a seed password: %v", args)
		}
	})
}
