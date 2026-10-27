package podman

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func TestInfluxRunArgs(t *testing.T) {
	got := influxRunArgs(
		"ghcr.io/mars-base/pgcli/pgcli-influxdb:1.13.1",
		nil,
		true, false,
		[]string{"INFLUX_USERNAME", "INFLUX_PASSWORD"},
		[]string{"-host", "127.0.0.1", "-execute", "SHOW DATABASES"},
	)
	want := []string{
		"run", "--rm", "-i=false",
		"--network", "host",
		"--http-proxy=false",
		"--entrypoint", "/usr/local/bin/influx",
		"-e", "INFLUX_USERNAME", "-e", "INFLUX_PASSWORD",
		"ghcr.io/mars-base/pgcli/pgcli-influxdb:1.13.1",
		"-host", "127.0.0.1", "-execute", "SHOW DATABASES",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("influxRunArgs:\n got %q\nwant %q", got, want)
	}
}

func TestInfluxRunArgsInteractiveNoHostNet(t *testing.T) {
	got := influxRunArgs("img", nil, false, true, nil, nil)
	want := []string{
		"run", "--rm", "-it",
		"--http-proxy=false",
		"--entrypoint", "/usr/local/bin/influx",
		"img",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("influxRunArgs:\n got %q\nwant %q", got, want)
	}
}

func TestInfluxRunArgsMounts(t *testing.T) {
	got := influxRunArgs("img", []string{"/host/f:/host/f:ro,z"}, true, false, nil, []string{"-import"})
	want := []string{
		"run", "--rm", "-i=false",
		"--network", "host",
		"--http-proxy=false",
		"--entrypoint", "/usr/local/bin/influx",
		"-v", "/host/f:/host/f:ro,z",
		"img", "-import",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("influxRunArgs:\n got %q\nwant %q", got, want)
	}
}

func TestInfluxImportMounts(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "demo.txt")
	if err := os.WriteFile(file, []byte("cpu value=1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	args, mounts := influxImportMounts([]string{"-import", "-path", file})
	if want := []string{"-import", "-path", file}; !reflect.DeepEqual(args, want) {
		t.Errorf("args = %q, want %q", args, want)
	}
	if want := []string{file + ":" + file + ":ro,z"}; !reflect.DeepEqual(mounts, want) {
		t.Errorf("mounts = %q, want %q", mounts, want)
	}

	args, mounts = influxImportMounts([]string{"-import", "-path=" + file})
	if want := []string{"-import", "-path=" + file}; !reflect.DeepEqual(args, want) {
		t.Errorf("-path= args = %q, want %q", args, want)
	}
	if len(mounts) != 1 {
		t.Errorf("-path= mounts = %q, want 1", mounts)
	}

	// Not an import: no mounts, args untouched.
	args, mounts = influxImportMounts([]string{"-execute", "SHOW DATABASES"})
	if mounts != nil || !reflect.DeepEqual(args, []string{"-execute", "SHOW DATABASES"}) {
		t.Errorf("non-import returned args=%q mounts=%q", args, mounts)
	}

	// Missing file: left for influx to report.
	missing := filepath.Join(dir, "missing.txt")
	args, mounts = influxImportMounts([]string{"-import", "-path", missing})
	if mounts != nil || !reflect.DeepEqual(args, []string{"-import", "-path", missing}) {
		t.Errorf("missing path returned args=%q mounts=%q", args, mounts)
	}
}

func TestInfluxDBCreateArgs(t *testing.T) {
	ic := config.InfluxDBConfig{
		ContainerName: "pgcli-influxdb-metrics",
		ImageTag:      "ghcr.io/mars-base/pgcli/pgcli-influxdb:1.13.1",
		ConfigPath:    "/srv/pg/addon/influxdb/metrics/influxdb.conf",
		DataDir:       "/srv/pg/addon/influxdb/metrics/data",
	}
	got := influxDBCreateArgs(ic)
	want := []string{
		"run", "-d",
		"--name", "pgcli-influxdb-metrics",
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--stop-timeout", "30",
		"-v", "/srv/pg/addon/influxdb/metrics/data:/var/lib/influxdb:z",
		"-v", "/srv/pg/addon/influxdb/metrics/influxdb.conf:/etc/influxdb/influxdb.conf:ro,z",
		"ghcr.io/mars-base/pgcli/pgcli-influxdb:1.13.1",
		"-config", "/etc/influxdb/influxdb.conf",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("influxDBCreateArgs:\n got %q\nwant %q", got, want)
	}
}
