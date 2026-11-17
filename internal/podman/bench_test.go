package podman

import (
	"reflect"
	"strings"
	"testing"
)

func TestBenchContainerArgs(t *testing.T) {
	got := benchContainerArgs("pgcli-bench-1", BenchContainerOptions{
		Image:      "ghcr.io/mars-base/pgcli/pgcli-pg:18-2.59.3",
		Entrypoint: "pgbench",
		Args:       []string{"-c", "16", "-T", "60"},
		Env:        []string{"PGHOST=127.0.0.1", "PGPASSWORD=secret"},
		Mounts:     []string{"/host/bench.sql:/pgcli-bench/files/0-bench.sql:ro,Z"},
		WorkDir:    "/pgcli-bench/log",
	})
	want := []string{
		"run", "--name", "pgcli-bench-1",
		"--network", "host",
		"--http-proxy=false",
		"-i=false",
		"-v", "/host/bench.sql:/pgcli-bench/files/0-bench.sql:ro,Z",
		"-w", "/pgcli-bench/log",
		"-e", "PGHOST", "-e", "PGPASSWORD",
		"--entrypoint", "pgbench",
		"ghcr.io/mars-base/pgcli/pgcli-pg:18-2.59.3",
		"-c", "16", "-T", "60",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("benchContainerArgs:\n got %q\nwant %q", got, want)
	}
}

func TestBenchContainerArgsKeepSecretsOutOfArgv(t *testing.T) {
	const password = "s3cr3t-do-not-leak"
	args := benchContainerArgs("name", BenchContainerOptions{
		Image:      "img",
		Entrypoint: "psql",
		Env:        []string{"PGPASSWORD=" + password, "PGSSLMODE=require"},
	})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, password) {
		t.Errorf("podman argv leaks the password: %q", joined)
	}
	if !strings.Contains(joined, "-e PGPASSWORD") {
		t.Errorf("podman argv does not pass PGPASSWORD by name: %q", joined)
	}
}

func TestBenchContainerArgsMinimal(t *testing.T) {
	got := benchContainerArgs("name", BenchContainerOptions{Image: "img", Entrypoint: "psql"})
	want := []string{
		"run", "--name", "name",
		"--network", "host",
		"--http-proxy=false",
		"-i=false",
		"--entrypoint", "psql",
		"img",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("benchContainerArgs:\n got %q\nwant %q", got, want)
	}
}
