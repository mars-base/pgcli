package podman

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderServersJSON pins the pre-registration document pgAdmin loads from
// /pgadmin4/servers.json. The load-bearing assertion is the absence of the
// password: pgAdmin cannot import passwords at all, so carrying one would be
// both useless and a secret exposure through a container bind mount.
func TestRenderServersJSON(t *testing.T) {
	tests := []struct {
		name       string
		dsn        string
		serverName string
		wantErr    bool
		check      func(t *testing.T, doc pgAdminServersFile, raw string)
	}{
		{
			name:       "full DSN with explicit server name",
			dsn:        "postgres://app:sup3rs3cret@db.example.com:5433/appdb",
			serverName: "prod",
			check: func(t *testing.T, doc pgAdminServersFile, raw string) {
				s, ok := doc.Servers["1"]
				if !ok {
					t.Fatalf("no server under id 1: %v", doc.Servers)
				}
				if s.Name != "prod" {
					t.Errorf("Name = %q, want prod", s.Name)
				}
				if s.Group != "Servers" {
					t.Errorf("Group = %q, want Servers", s.Group)
				}
				if s.Host != "db.example.com" {
					t.Errorf("Host = %q, want db.example.com", s.Host)
				}
				if s.Port != 5433 {
					t.Errorf("Port = %d, want 5433", s.Port)
				}
				if s.MaintenanceDB != "appdb" {
					t.Errorf("MaintenanceDB = %q, want appdb", s.MaintenanceDB)
				}
				if s.Username != "app" {
					t.Errorf("Username = %q, want app", s.Username)
				}
				if s.SSLMode != "prefer" {
					t.Errorf("SSLMode = %q, want prefer", s.SSLMode)
				}
				if got := s.ConnectionParams["sslmode"]; got != "prefer" {
					t.Errorf("ConnectionParameters.sslmode = %v, want prefer", got)
				}
			},
		},
		{
			name: "password is parsed and discarded",
			dsn:  "postgres://app:sup3rs3cret@127.0.0.1:5432/appdb",
			check: func(t *testing.T, doc pgAdminServersFile, raw string) {
				if strings.Contains(raw, "sup3rs3cret") {
					t.Errorf("rendered servers.json leaked the DSN password:\n%s", raw)
				}
				for k := range doc.Servers["1"].ConnectionParams {
					if strings.Contains(strings.ToLower(k), "pass") {
						t.Errorf("ConnectionParameters carries a password key %q", k)
					}
				}
			},
		},
		{
			name:       "empty server name falls back to the host",
			dsn:        "postgres://app@10.0.0.9:5432/appdb",
			serverName: "",
			check: func(t *testing.T, doc pgAdminServersFile, raw string) {
				if got := doc.Servers["1"].Name; got != "10.0.0.9" {
					t.Errorf("Name = %q, want the DSN host 10.0.0.9", got)
				}
			},
		},
		{
			name: "port and maintenance db defaults",
			dsn:  "postgres://app@dbhost",
			check: func(t *testing.T, doc pgAdminServersFile, raw string) {
				s := doc.Servers["1"]
				if s.Port != 5432 {
					t.Errorf("Port = %d, want the 5432 default", s.Port)
				}
				if s.MaintenanceDB != "postgres" {
					t.Errorf("MaintenanceDB = %q, want the postgres default", s.MaintenanceDB)
				}
			},
		},
		// A URL query is not part of servers.json (pgAdmin has no free-form
		// params field here), but it must not corrupt the parsed host/db either.
		{
			name: "DSN with query params still parses host and db",
			dsn:  "postgres://app:pw@dbhost:5432/appdb?sslmode=require",
			check: func(t *testing.T, doc pgAdminServersFile, raw string) {
				s := doc.Servers["1"]
				if s.Host != "dbhost" || s.Port != 5432 || s.MaintenanceDB != "appdb" {
					t.Errorf("parsed %+v, want dbhost:5432/appdb", s)
				}
			},
		},
		{
			name:    "postgresql scheme accepted",
			dsn:     "postgresql://app@dbhost/appdb",
			wantErr: false,
			check:   func(t *testing.T, doc pgAdminServersFile, raw string) {},
		},
		{
			name:    "wrong scheme rejected",
			dsn:     "mysql://app@dbhost/appdb",
			wantErr: true,
		},
		{
			name:    "missing host rejected",
			dsn:     "postgres:///appdb",
			wantErr: true,
		},
		{
			name:    "garbage rejected",
			dsn:     "not a dsn at all",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := RenderServersJSON(tc.dsn, tc.serverName)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %s", raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("RenderServersJSON: %v", err)
			}
			var doc pgAdminServersFile
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("output is not valid JSON: %v\n%s", err, raw)
			}
			if len(doc.Servers) != 1 {
				t.Errorf("got %d servers, want exactly 1", len(doc.Servers))
			}
			if tc.check != nil {
				tc.check(t, doc, string(raw))
			}
		})
	}
}

// TestWriteServersJSON checks the file lands world-readable: under rootless
// podman the container's 5050 is a mapped uid, and a :ro bind mount of a
// 0600 file would not be readable inside.
func TestWriteServersJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.json")
	if err := WriteServersJSON(path, "postgres://app:pw@dbhost:5432/appdb", "seeded"); err != nil {
		t.Fatalf("WriteServersJSON: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0644 {
		t.Errorf("mode = %o, want 0644", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "pw") {
		t.Errorf("file leaked the DSN password:\n%s", data)
	}
	var doc pgAdminServersFile
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := doc.Servers["1"].Name; got != "seeded" {
		t.Errorf("Name = %q, want seeded", got)
	}
}
