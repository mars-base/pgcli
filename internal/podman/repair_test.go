package podman

import "testing"

func TestNeedsMigrate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{
			name: "logrus-style pause reset hint",
			in:   `time="2026-08-28T14:25:33+08:00" level=error msg="invalid internal status, try resetting the pause process with /home/diwen/.local/share/podman-static/bin/podman system migrate: could not find any running process: no such process"`,
			want: true,
		},
		{
			name: "short migrate hint",
			in:   "Error: need podman system migrate",
			want: true,
		},
		{
			name: "unrelated podman error",
			in:   "Error: no container with name or ID foo found",
			want: false,
		},
		{
			name: "empty output",
			in:   "",
			want: false,
		},
	}
	for _, c := range cases {
		if got := needsMigrate(c.in); got != c.want {
			t.Errorf("%s: needsMigrate(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}
