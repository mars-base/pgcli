package podman

import (
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// TestPGExporterArgs pins the `podman run` argv for pg_exporter across both
// networking shapes. The load-bearing contract is that the CONTAINER gets the
// MONITOR DSN via PG_EXPORTER_URL (never an admin URL) and that nothing else is
// mounted: pg_exporter is stateless, so a stray data mount or a --user flag here
// would mean the design silently drifted. ConfigFile is the only volume, and it
// is a user file the container reads (:ro,z).
func TestPGExporterArgs(t *testing.T) {
	const dsn = "postgres://pgexporter:pw@127.0.0.1:5432/demo?sslmode=disable"

	base := func() *config.PGExporterConfig {
		return &config.PGExporterConfig{
			ContainerName:  "pgcli-pgexporter-pe1",
			Name:           "pe1",
			HostPort:       9630,
			Listen:         "0.0.0.0",
			ImageTag:       config.DefaultPGExporterImageTag,
			TargetInstance: "demo",
		}
	}
	imageIdx := func(args []string) int {
		return slices.Index(args, config.DefaultPGExporterImageTag)
	}
	hasMount := func(args []string, spec string) bool {
		for i, a := range args {
			if a == "-v" && i+1 < len(args) && args[i+1] == spec {
				return true
			}
		}
		return false
	}

	t.Run("linux host networking injects the monitor DSN and nothing else", func(t *testing.T) {
		args := pgExporterArgs(base(), dsn, false, "pgcli-net")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "host") {
			t.Errorf("expected --network host, got %v", args)
		}
		if slices.Contains(args, "-p") {
			t.Errorf("host networking must not publish ports, got %v", args)
		}
		if !slices.Contains(args, "--http-proxy=false") {
			t.Errorf("--http-proxy=false missing: %v", args)
		}
		if !slices.Contains(args, "--restart") || !slices.Contains(args, "unless-stopped") {
			t.Errorf("--restart unless-stopped missing: %v", args)
		}
		if !slices.Contains(args, "PG_EXPORTER_URL="+dsn) {
			t.Errorf("monitor DSN must be injected via PG_EXPORTER_URL: %v", args)
		}

		// Stateless: no volume without a config file, no --user.
		if slices.Contains(args, "-v") {
			t.Errorf("no mount expected without --config-file: %v", args)
		}
		if slices.Contains(args, "--user") {
			t.Errorf("pg_exporter must not pass --user: %v", args)
		}

		idx := imageIdx(args)
		if idx == -1 {
			t.Fatal("image tag not found in args")
		}
		if idx == len(args)-1 {
			t.Fatal("image tag must not be last — pg_exporter needs its own flags")
		}
		if !slices.Contains(args[idx+1:], "--web.listen-address=0.0.0.0:9630") {
			t.Errorf("missing --web.listen-address: %v", args[idx+1:])
		}
	})

	t.Run("config file is mounted read-only and selected via env", func(t *testing.T) {
		pc := base()
		pc.ConfigFile = "/srv/collectors.yml"
		args := pgExporterArgs(pc, dsn, false, "pgcli-net")

		if !hasMount(args, "/srv/collectors.yml:/etc/pg_exporter.yml:ro,z") {
			t.Errorf("config file mount missing or wrong: %v", args)
		}
		if !slices.Contains(args, "PG_EXPORTER_CONFIG=/etc/pg_exporter.yml") {
			t.Errorf("PG_EXPORTER_CONFIG must point at the mount: %v", args)
		}
	})

	t.Run("macOS bridge networking publishes port and widens bind", func(t *testing.T) {
		pc := base()
		pc.Listen = "127.0.0.1" // even a loopback listen is widened under bridge
		args := pgExporterArgs(pc, dsn, true, "pgcli-net")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "pgcli-net") {
			t.Errorf("expected --network pgcli-net, got %v", args)
		}
		if !slices.Contains(args, "-p") || !slices.Contains(args, "9630:9630") {
			t.Errorf("bridge must publish 9630:9630, got %v", args)
		}
		if !slices.Contains(args, "--web.listen-address=0.0.0.0:9630") {
			t.Errorf("bridge should widen loopback to 0.0.0.0, got %v", args)
		}
		if slices.Contains(args, "--web.listen-address=127.0.0.1:9630") {
			t.Errorf("bridge must not keep the loopback bind: %v", args)
		}
	})

	t.Run("extra args appear last (override via last-wins)", func(t *testing.T) {
		pc := base()
		pc.ExtraArgs = []string{"--auto-discovery=false", "--tag=env=prod"}
		args := pgExporterArgs(pc, dsn, false, "pgcli-net")

		trailing := args[imageIdx(args)+1:]
		if len(trailing) < 3 {
			t.Fatalf("trailing too short: %v", trailing)
		}
		last2 := trailing[len(trailing)-2:]
		if !slices.Contains(last2, "--auto-discovery=false") || !slices.Contains(last2, "--tag=env=prod") {
			t.Errorf("ExtraArgs must be last: trailing=%v last2=%v", trailing, last2)
		}
	})
}

// TestRedactExporterArgs: the debug trace of the argv must never carry the
// monitor password.
func TestRedactExporterArgs(t *testing.T) {
	const dsn = "postgres://pgexporter:hunter2@127.0.0.1:5432/demo"
	args := pgExporterArgs(&config.PGExporterConfig{
		ContainerName: "pgcli-pgexporter-pe1",
		HostPort:      9630,
		Listen:        "0.0.0.0",
		ImageTag:      config.DefaultPGExporterImageTag,
	}, dsn, false, "pgcli-net")

	out := redactExporterArgs(args, dsn)
	if strings.Contains(strings.Join(out, " "), "hunter2") {
		t.Errorf("redacted argv still leaks the password: %v", out)
	}
	if !slices.Contains(out, "PG_EXPORTER_URL=<redacted>") {
		t.Errorf("expected the placeholder in its place: %v", out)
	}
	// The input slice must not be mutated in place.
	if !slices.Contains(args, "PG_EXPORTER_URL="+dsn) {
		t.Errorf("redactExporterArgs mutated its input: %v", args)
	}
}

// TestValidMonitorUser: the name is embedded in SQL, so anything that is not a
// plain identifier must be rejected.
func TestValidMonitorUser(t *testing.T) {
	ok := []string{"pgexporter", "_x", "a1", "monitor-user", "svc$read", "r"}
	bad := []string{"", `a"b`, "a'b", "a;b", "a b", "a.b", "1abc", strings.Repeat("a", 64)}

	for _, n := range ok {
		if !ValidMonitorUser(n) {
			t.Errorf("ValidMonitorUser(%q) = false, want true", n)
		}
	}
	for _, n := range bad {
		if ValidMonitorUser(n) {
			t.Errorf("ValidMonitorUser(%q) = true, want false", n)
		}
	}
}

// TestBuildMonitorDSN: only the credentials change — host, port, database and
// query params all survive. The target may also carry NO userinfo (the
// credential-free admin_dsn address), which must still rewrite cleanly.
func TestBuildMonitorDSN(t *testing.T) {
	got, err := BuildMonitorDSN("postgres://admin:adminpw@db.example.com:5433/app?sslmode=require", "pgexporter", "pw123")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := "postgres://pgexporter:pw123@db.example.com:5433/app?sslmode=require"
	if got != want {
		t.Errorf("monitor DSN = %q, want %q", got, want)
	}

	// Credential-free target (stored admin_dsn form): the output gains the
	// monitor user/password, everything else preserved.
	got, err = BuildMonitorDSN("postgres://db.example.com:5433/app?sslmode=require", "pgexporter", "pw123")
	if err != nil {
		t.Fatalf("build credential-free: %v", err)
	}
	if got != want {
		t.Errorf("credential-free monitor DSN = %q, want %q", got, want)
	}

	for _, dsn := range []string{"mysql://u@h/db", "not a url", "redis://h:6379"} {
		if _, err := BuildMonitorDSN(dsn, "pgexporter", "pw"); err == nil {
			t.Errorf("non-postgres DSN accepted: %q", dsn)
		}
	}
}
