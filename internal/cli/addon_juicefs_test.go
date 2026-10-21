package cli

import "testing"

func TestDsnDisplay(t *testing.T) {
	cases := []struct{ in, want string }{
		// postgres goes through ParseDSN.
		{"postgres://admin:secret@127.0.0.1:5432/demo?sslmode=disable", "127.0.0.1:5432/demo"},
		// Password-only redis URLs must not leak the password.
		{"redis://:sup3rsecret@127.0.0.1:6379/0", "redis://127.0.0.1:6379/0"},
		// sqlite3 is a credential-free file path — shown verbatim.
		{"sqlite3:///root/.pgcli/addon/juicefs/t1/meta.db", "sqlite3:///root/.pgcli/addon/juicefs/t1/meta.db"},
		// Garbage stays hidden.
		{"not a dsn at all", "(unparseable dsn)"},
	}
	for _, c := range cases {
		if got := dsnDisplay(c.in); got != c.want {
			t.Errorf("dsnDisplay(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidJuicefsVolumeName(t *testing.T) {
	for _, ok := range []string{"abc", "t1fs", "my-volume-9", "juicefs"} {
		if !validJuicefsVolumeName(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"ab", "UPPER", "under_score", "", "a b", string(make([]byte, 64))} {
		if validJuicefsVolumeName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
