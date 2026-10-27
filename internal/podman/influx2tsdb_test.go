package podman

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func TestInflux2TSDBCreateArgs(t *testing.T) {
	ic := config.Influx2TSDBConfig{
		ContainerName: "pgcli-influx2tsdb-proxy",
		ImageTag:      "ghcr.io/mars-base/pgcli/pgcli-influx2tsdb:1.3.2",
		Port:          8087,
		PoolSize:      10,
	}
	got := strings.Join(influx2TSDBCreateArgs(ic, "postgres://u:p@127.0.0.1:5432/db"), " ")
	want := "run -d --name pgcli-influx2tsdb-proxy --network host --http-proxy=false " +
		"--restart unless-stopped --stop-timeout 30 " +
		"ghcr.io/mars-base/pgcli/pgcli-influx2tsdb:1.3.2 " +
		"-pg postgres://u:p@127.0.0.1:5432/db -port 8087 -pool 10"
	if got != want {
		t.Errorf("influx2TSDBCreateArgs:\n got %q\nwant %q", got, want)
	}
}

func TestInflux2TSDBCreateArgsOptionalFlags(t *testing.T) {
	ic := config.Influx2TSDBConfig{
		ContainerName: "pgcli-influx2tsdb-dev-proxy",
		ImageTag:      "img",
		Port:          9090,
		PoolSize:      25,
		Database:      "metrics",
		Verbose:       true,
	}
	got := strings.Join(influx2TSDBCreateArgs(ic, "dsn"), " ")
	if !strings.HasSuffix(got, "-pg dsn -port 9090 -pool 25 -db metrics -verbose") {
		t.Errorf("optional flags not appended in order: %q", got)
	}
}
