package podman

import (
	"reflect"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func TestMongoshRunArgs(t *testing.T) {
	got := mongoshRunArgs(
		"ghcr.io/mars-base/pgcli/pgcli-mongosh:2.14.1",
		true, false,
		[]string{"PGCLI_DOCDB_URI"},
		[]string{"--quiet", "--eval", "print(1)"},
	)
	want := []string{
		"run", "--rm", "-i=false",
		"--network", "host",
		"--http-proxy=false",
		"-e", "PGCLI_DOCDB_URI",
		"ghcr.io/mars-base/pgcli/pgcli-mongosh:2.14.1",
		"--quiet", "--eval", "print(1)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mongoshRunArgs:\n got %q\nwant %q", got, want)
	}
}

func TestMongoshRunArgsInteractiveNoHostNet(t *testing.T) {
	got := mongoshRunArgs("img", false, true, []string{"PGCLI_DOCDB_URI", "TERM"}, nil)
	want := []string{
		"run", "--rm", "-it",
		"--http-proxy=false",
		"-e", "PGCLI_DOCDB_URI", "-e", "TERM",
		"img",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mongoshRunArgs:\n got %q\nwant %q", got, want)
	}
}

func TestMongoshRunnerHost(t *testing.T) {
	tests := []struct {
		name       string
		useHostNet bool
		listen     string
		want       string
	}{
		{"host network wildcard", true, "0.0.0.0", "127.0.0.1"},
		{"host network ipv4 loopback", true, "127.0.0.1", "127.0.0.1"},
		{"host network ipv6 loopback", true, "::1", "::1"},
		{"bridge", false, "0.0.0.0", "host.containers.internal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &MongoshRunner{useHostNet: tt.useHostNet}
			if got := r.Host(tt.listen); got != tt.want {
				t.Errorf("Host(%q) = %q, want %q", tt.listen, got, tt.want)
			}
		})
	}
}

func TestWithEnvOverrides(t *testing.T) {
	got := withEnvOverrides(
		[]string{"PATH=/bin", "PGCLI_DOCDB_URI=stale", "TERM=xterm"},
		[]string{"PGCLI_DOCDB_URI=fresh"},
	)
	want := []string{"PATH=/bin", "TERM=xterm", "PGCLI_DOCDB_URI=fresh"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("withEnvOverrides = %q, want %q", got, want)
	}
}

func TestDocumentDBMongoshURI(t *testing.T) {
	dc := &config.DocumentDBConfig{
		ClientUser:     "docdb_admin",
		ClientPassword: "p@ss:/?#",
		HostPort:       10260,
	}

	tests := []struct {
		name     string
		host     string
		tls      bool
		database string
		want     string
	}{
		{
			name:     "plaintext with database",
			host:     "127.0.0.1",
			database: "app",
			want:     "mongodb://docdb_admin:p%40ss%3A%2F%3F%23@127.0.0.1:10260/app?authSource=admin&directConnection=true&serverSelectionTimeoutMS=10000",
		},
		{
			name: "tls without database",
			host: "host.containers.internal",
			tls:  true,
			want: "mongodb://docdb_admin:p%40ss%3A%2F%3F%23@host.containers.internal:10260/?authSource=admin&directConnection=true&serverSelectionTimeoutMS=10000&tls=true&tlsAllowInvalidCertificates=true",
		},
		{
			name: "ipv6 host",
			host: "::1",
			want: "mongodb://docdb_admin:p%40ss%3A%2F%3F%23@[::1]:10260/?authSource=admin&directConnection=true&serverSelectionTimeoutMS=10000",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DocumentDBMongoshURI(dc, tt.host, tt.tls, tt.database)
			if got != tt.want {
				t.Errorf("DocumentDBMongoshURI:\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}
