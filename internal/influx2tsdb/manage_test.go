package influx2tsdb

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func TestArgs(t *testing.T) {
	ic := config.Influx2TSDBConfig{Port: 8087, PoolSize: 10}
	got := strings.Join(Args(ic, "postgres://u:p@127.0.0.1:5432/db"), " ")
	want := "-pg postgres://u:p@127.0.0.1:5432/db -port 8087 -pool 10"
	if got != want {
		t.Errorf("Args = %q, want %q", got, want)
	}
}

func TestArgsOptionalFlags(t *testing.T) {
	ic := config.Influx2TSDBConfig{Port: 9090, PoolSize: 25, Database: "metrics", Verbose: true}
	got := strings.Join(Args(ic, "dsn"), " ")
	want := "-pg dsn -port 9090 -pool 25 -db metrics -verbose"
	if got != want {
		t.Errorf("Args = %q, want %q", got, want)
	}
}

func TestLocalDSN(t *testing.T) {
	inst := config.InstanceConfig{}
	inst.Podman.HostPort = 35432
	inst.Postgres.User = "admin"
	inst.Postgres.Password = "p@ss:w/rd"
	inst.Postgres.Database = "demo"

	got, err := LocalDSN(inst)
	if err != nil {
		t.Fatalf("LocalDSN: %v", err)
	}
	want := "postgres://admin:p%40ss%3Aw%2Frd@127.0.0.1:35432/demo?sslmode=disable"
	if got != want {
		t.Errorf("LocalDSN = %q, want %q", got, want)
	}
}

func TestLocalDSNErrors(t *testing.T) {
	inst := config.InstanceConfig{}
	if _, err := LocalDSN(inst); err == nil || !strings.Contains(err.Error(), "host port") {
		t.Errorf("no port: err = %v", err)
	}
	inst.Podman.HostPort = 5432
	if _, err := LocalDSN(inst); err == nil || !strings.Contains(err.Error(), "user") {
		t.Errorf("no user: err = %v", err)
	}
}

func TestRedactDSN(t *testing.T) {
	got := RedactDSN("postgres://admin:s3cret@db:5432/metrics?sslmode=disable")
	if strings.Contains(got, "s3cret") {
		t.Errorf("RedactDSN leaked the password: %q", got)
	}
	if !strings.Contains(got, "admin:***@") {
		t.Errorf("RedactDSN = %q, want admin:***@", got)
	}
	if got := RedactDSN("not a url"); got != "not a url" {
		t.Errorf("RedactDSN(plain) = %q", got)
	}
}

func TestDatabaseName(t *testing.T) {
	dsn := "postgres://u:p@db:5432/metrics?sslmode=disable"
	if got := DatabaseName(config.Influx2TSDBConfig{}, dsn); got != "metrics" {
		t.Errorf("DatabaseName(dsn) = %q, want metrics", got)
	}
	if got := DatabaseName(config.Influx2TSDBConfig{Database: "custom"}, dsn); got != "custom" {
		t.Errorf("DatabaseName(explicit) = %q, want custom", got)
	}
	if got := DatabaseName(config.Influx2TSDBConfig{}, "::bad::"); got != "" {
		t.Errorf("DatabaseName(bad) = %q, want empty", got)
	}
}

func TestTargetSummary(t *testing.T) {
	if got := TargetSummary(config.Influx2TSDBConfig{TargetInstance: "tsdb"}); got != "instance tsdb" {
		t.Errorf("TargetSummary(instance) = %q", got)
	}
	got := TargetSummary(config.Influx2TSDBConfig{DSN: "postgres://u:secret@db:5432/x"})
	if strings.Contains(got, "secret") {
		t.Errorf("TargetSummary leaked the password: %q", got)
	}
}
