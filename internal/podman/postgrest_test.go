package podman

import (
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// TestPostgrestArgsAndEnv pins the `podman run` argv for PostgREST across both
// networking shapes and the optional env vars. PostgREST is env-only config, so
// this function is the entire contract between a PostgrestConfig and the
// container — everything the CLI can't verify at runtime lives here.
func TestPostgrestArgsAndEnv(t *testing.T) {
	base := func() *config.PostgrestConfig {
		return &config.PostgrestConfig{
			ContainerName: "pgcli-postgrest-test",
			HostPort:      3500,
			Listen:        "127.0.0.1",
			DSN:           "postgres://auth:pw@127.0.0.1:5000/appdb",
			ImageTag:      config.DefaultPostgrestImageTag,
		}
	}

	// hasEnv reports whether the argv contains the exact `-e KEY=VALUE` pair.
	hasEnv := func(args []string, kv string) bool {
		return slices.Contains(args, "-e") && slices.Contains(args, kv)
	}
	// hasEnvKey reports whether any `-e KEY=...` is present (regardless of value).
	hasEnvKey := func(args []string, key string) bool {
		for i, a := range args {
			if a == "-e" && i+1 < len(args) && strings.HasPrefix(args[i+1], key+"=") {
				return true
			}
		}
		return false
	}

	t.Run("linux host networking", func(t *testing.T) {
		pc := base()
		args := postgrestArgsAndEnv(pc, false, "pgcli-net")

		// Networking shape: --network host, no port publishing.
		if !slices.Contains(args, "--network") || !slices.Contains(args, "host") {
			t.Errorf("expected --network host, got %v", args)
		}
		if slices.Contains(args, "-p") {
			t.Errorf("host networking must not publish ports, got %v", args)
		}
		// DB_URI verbatim — never re-serialized, so params survive.
		if !hasEnv(args, "PGRST_DB_URI="+pc.DSN) {
			t.Errorf("PGRST_DB_URI not passed verbatim: %v", args)
		}
		// SERVER_PORT is always emitted (=HostPort): required under --net=host
		// because the container default is 3000.
		if !hasEnv(args, "PGRST_SERVER_PORT=3500") {
			t.Errorf("PGRST_SERVER_PORT=3500 missing: %v", args)
		}
		// Loopback bind kept verbatim on Linux.
		if !hasEnv(args, "PGRST_SERVER_HOST=127.0.0.1") {
			t.Errorf("PGRST_SERVER_HOST=127.0.0.1 missing: %v", args)
		}
		// Unset pool/schemas defer to PostgREST defaults — must be absent.
		if hasEnvKey(args, "PGRST_DB_POOL") {
			t.Errorf("PGRST_DB_POOL should be absent when DbPool=0: %v", args)
		}
		if hasEnvKey(args, "PGRST_DB_SCHEMAS") {
			t.Errorf("PGRST_DB_SCHEMAS should be absent when Schemas=\"\": %v", args)
		}
		// Unset anon-role means "anonymous access disabled" — env absent.
		if hasEnvKey(args, "PGRST_DB_ANON_ROLE") {
			t.Errorf("PGRST_DB_ANON_ROLE should be absent when AnonRole=\"\": %v", args)
		}
		// Image is the final positional argument.
		if args[len(args)-1] != config.DefaultPostgrestImageTag {
			t.Errorf("image = %q, want last arg %q", args[len(args)-1], config.DefaultPostgrestImageTag)
		}
	})

	t.Run("macOS bridge networking widens loopback bind and publishes port", func(t *testing.T) {
		pc := base()
		args := postgrestArgsAndEnv(pc, true, "pgcli-net")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "pgcli-net") {
			t.Errorf("expected --network pgcli-net, got %v", args)
		}
		if !slices.Contains(args, "-p") || !slices.Contains(args, "3500:3500") {
			t.Errorf("bridge must publish 3500:3500, got %v", args)
		}
		// The published port can't reach a loopback-only bind, so 127.0.0.1
		// widens to 0.0.0.0.
		if !hasEnv(args, "PGRST_SERVER_HOST=0.0.0.0") {
			t.Errorf("bridge should widen loopback to 0.0.0.0, got %v", args)
		}
		if hasEnv(args, "PGRST_SERVER_HOST=127.0.0.1") {
			t.Errorf("bridge must not keep the loopback bind: %v", args)
		}
	})

	t.Run("explicit non-loopback listen passes through", func(t *testing.T) {
		pc := base()
		pc.Listen = "10.0.0.5"
		args := postgrestArgsAndEnv(pc, true, "pgcli-net")
		if !hasEnv(args, "PGRST_SERVER_HOST=10.0.0.5") {
			t.Errorf("explicit bind should pass through under bridge: %v", args)
		}
	})

	t.Run("db-pool and schemas emitted when set", func(t *testing.T) {
		pc := base()
		pc.DbPool = 3
		pc.Schemas = "api,public"
		pc.AnonRole = "web_anon"
		args := postgrestArgsAndEnv(pc, false, "pgcli-net")
		if !hasEnv(args, "PGRST_DB_POOL=3") {
			t.Errorf("PGRST_DB_POOL=3 missing: %v", args)
		}
		if !hasEnv(args, "PGRST_DB_SCHEMAS=api,public") {
			t.Errorf("PGRST_DB_SCHEMAS missing: %v", args)
		}
		if !hasEnv(args, "PGRST_DB_ANON_ROLE=web_anon") {
			t.Errorf("PGRST_DB_ANON_ROLE=web_anon missing: %v", args)
		}
	})

	// The whole point of passing the DSN verbatim: sslmode and other URI params
	// must not be dropped by any re-parsing.
	t.Run("DSN with sslmode param survives verbatim", func(t *testing.T) {
		pc := base()
		pc.DSN = "postgres://auth:pw@db.example.com:5432/appdb?sslmode=require&options=-c%20role%3Dapi"
		args := postgrestArgsAndEnv(pc, false, "pgcli-net")
		if !hasEnv(args, "PGRST_DB_URI="+pc.DSN) {
			t.Errorf("DSN params were not preserved verbatim: %v", args)
		}
	})
}
