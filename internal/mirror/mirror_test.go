package mirror

import "testing"

func TestResolve(t *testing.T) {
	const raw = "https://github.com/o/r/releases/download/v1/tool-linux-amd64.tar.gz"
	tests := []struct {
		name string
		env  string
		in   string
		want string
	}{
		{"unset", "", raw, raw},
		{"base", "http://mirror:18080", raw, "http://mirror:18080/tool-linux-amd64.tar.gz"},
		{"trailing slash", "http://mirror:18080/", raw, "http://mirror:18080/tool-linux-amd64.tar.gz"},
		{"already mirrored", "http://mirror:18080", "http://mirror:18080/tool-linux-amd64.tar.gz", "http://mirror:18080/tool-linux-amd64.tar.gz"},
		{"non-http", "http://mirror:18080", "s3://bucket/tool.tar.gz", "s3://bucket/tool.tar.gz"},
		{"empty", "http://mirror:18080", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(Env, tt.env)
			if got := Resolve(tt.in); got != tt.want {
				t.Fatalf("Resolve(%q) with %s=%q = %q, want %q", tt.in, Env, tt.env, got, tt.want)
			}
		})
	}
}
