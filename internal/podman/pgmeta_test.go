package podman

import "testing"

func TestMetaEngineURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"postgres://a:b@127.0.0.1:5432/db", "postgres://a:b@127.0.0.1:5432/db?sslmode=disable"},
		{"postgres://a:b@h/db?x=1", "postgres://a:b@h/db?x=1&sslmode=disable"},
		{"postgres://a:b@h/db?sslmode=require", "postgres://a:b@h/db?sslmode=require"},
	}
	for _, c := range cases {
		if got := MetaEngineURL(c.in); got != c.want {
			t.Errorf("MetaEngineURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEscapeSQLString(t *testing.T) {
	if got := escapeSQLString("o'brien"); got != "o''brien" {
		t.Errorf("escape = %q", got)
	}
}
