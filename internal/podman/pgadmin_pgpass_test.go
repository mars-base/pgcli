package podman

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderPgPass pins the libpq pgpass line pgcli renders for a seeded
// server: exact field order, `*` database wildcard, and `:`/`\` escaping (the
// format's own separators). The password is EXPECTED here — this is the one
// place the seed password is written, in the 0600 file libpq reads instead of
// prompting.
func TestRenderPgPass(t *testing.T) {
	tests := []struct {
		name    string
		dsn     string
		want    string
		wantErr error // errNoPgPassPassword or nil; other errors via wantErrNonNil
	}{
		{
			name: "full DSN",
			dsn:  "postgres://app:sup3rs3cret@db.example.com:5433/appdb",
			want: "db.example.com:5433:*:app:sup3rs3cret\n",
		},
		{
			name: "default port",
			dsn:  "postgres://app:pw@dbhost/appdb",
			want: "dbhost:5432:*:app:pw\n",
		},
		{
			name: "colon and backslash in password are escaped",
			dsn:  "postgres://app:p%40ss%3Aword%5Cx@dbhost:5432/appdb",
			want: "dbhost:5432:*:app:p@ss\\:word\\\\x\n",
		},
		{
			name: "colon in user is escaped",
			dsn:  "postgres://u%3Aser:pw@dbhost:5432/appdb",
			want: "dbhost:5432:*:u\\:ser:pw\n",
		},
		{
			name:    "no password is the sentinel",
			dsn:     "postgres://app@dbhost:5432/appdb",
			wantErr: errNoPgPassPassword,
		},
		{
			name:    "empty password is the sentinel too",
			dsn:     "postgres://app:@dbhost:5432/appdb",
			wantErr: errNoPgPassPassword,
		},
		{
			name: "database field is the wildcard, not the DSN path",
			dsn:  "postgres://app:pw@dbhost:5432/onlythis",
			want: "dbhost:5432:*:app:pw\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := RenderPgPass(tc.dsn)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("RenderPgPass: %v", err)
			}
			if string(out) != tc.want {
				t.Errorf("got %q, want %q", out, tc.want)
			}
		})
	}

	t.Run("wrong scheme rejected", func(t *testing.T) {
		if _, err := RenderPgPass("mysql://app:pw@dbhost/appdb"); err == nil {
			t.Fatal("expected an error for a non-postgres scheme")
		}
	})
	t.Run("missing host rejected", func(t *testing.T) {
		if _, err := RenderPgPass("postgres://app:pw@/appdb"); err == nil {
			t.Fatal("expected an error for a missing host")
		}
	})
}

// TestDSNHasPassword is the CLI-facing predicate that decides whether the
// install summary can promise a prompt-free first connect.
func TestDSNHasPassword(t *testing.T) {
	if !DSNHasPassword("postgres://app:pw@dbhost/appdb") {
		t.Error("DSN with a password should report true")
	}
	if DSNHasPassword("postgres://app@dbhost/appdb") {
		t.Error("DSN without a password should report false")
	}
	if DSNHasPassword("not a dsn") {
		t.Error("garbage should report false")
	}
}

// TestWritePgPass checks the file is 0600 (libpq rejects looser permissions)
// and that re-writing over an existing file works: after a container run the
// entrypoint has chowned it to the mapped 5050 uid, so the write path removes
// and recreates rather than truncating.
func TestWritePgPass(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pgpass")
	dsn := "postgres://app:pw@dbhost:5432/appdb"
	if err := WritePgPass(path, dsn); err != nil {
		t.Fatalf("WritePgPass: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("mode = %o, want 0600", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "pw") {
		t.Errorf("pgpass file should hold the password, got %q", data)
	}

	// Rewrite over the existing file (this is what --force reinstall does).
	if err := WritePgPass(path, "postgres://app:other@dbhost:5432/appdb"); err != nil {
		t.Fatalf("WritePgPass (rewrite): %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "other") || strings.Contains(string(data), ":pw\n") {
		t.Errorf("rewrite did not replace the content: %q", data)
	}

	// A password-less DSN must not leave a stale secret behind.
	if err := WritePgPass(path, "postgres://app@dbhost:5432/appdb"); !errors.Is(err, errNoPgPassPassword) {
		t.Fatalf("err = %v, want errNoPgPassPassword", err)
	}
}
