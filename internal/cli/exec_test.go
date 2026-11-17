package cli

import (
	"strings"
	"testing"
)

func TestExecDSNWithDatabase(t *testing.T) {
	tests := []struct {
		name     string
		dsn      string
		database string
		want     string
	}{
		{
			name:     "replaces database",
			dsn:      "postgres://user:pass@host:5432/appdb",
			database: "bench_db",
			want:     "postgres://user:pass@host:5432/bench_db",
		},
		{
			name:     "keeps query parameters",
			dsn:      "postgresql://user@host/db?sslmode=disable&connect_timeout=5",
			database: "bench_db",
			want:     "postgresql://user@host/bench_db?sslmode=disable&connect_timeout=5",
		},
		{
			name:     "escapes password",
			dsn:      "postgres://user:p%40ss@host:5433/appdb",
			database: "bench_db",
			want:     "postgres://user:p%40ss@host:5433/bench_db",
		},
		{
			name:     "escapes database",
			dsn:      "postgres://user:pass@host/appdb",
			database: "bench db",
			want:     "postgres://user:pass@host/bench%20db",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := execDSNWithDatabase(tt.dsn, tt.database)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("execDSNWithDatabase(%q, %q) = %q, want %q", tt.dsn, tt.database, got, tt.want)
			}
		})
	}
}

func TestExecDSNWithDatabaseInvalid(t *testing.T) {
	tests := []struct {
		dsn  string
		want string
	}{
		{"mysql://user:pass@host/db", "scheme"},
		{"postgres:///db", "missing host"},
		{"://bad", "invalid DSN"},
	}
	for _, tt := range tests {
		_, err := execDSNWithDatabase(tt.dsn, "bench_db")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("execDSNWithDatabase(%q) error = %v, want containing %q", tt.dsn, err, tt.want)
		}
	}
}
