package podman

import (
	"reflect"
	"strings"
	"testing"
)

func TestMCRunArgs(t *testing.T) {
	const image = "ghcr.io/mars-base/pgcli/pgcli-mc:testtag"
	const cfgDir = "/home/tester/.mc"

	tests := []struct {
		name        string
		useHostNet  bool
		interactive bool
		envs        []string
		args        []string
		want        []string
	}{
		{
			name:        "linux interactive: host network, tty",
			useHostNet:  true,
			interactive: true,
			args:        []string{"ls", "store"},
			want: []string{
				"run", "--rm", "-it", "--network", "host",
				"--http-proxy=false", "-v", "/home/tester/.mc:/data/.mc:z",
				image, "ls", "store",
			},
		},
		{
			name:        "macOS non-interactive: bridge default, -i=false",
			useHostNet:  false,
			interactive: false,
			args:        []string{"ls", "store"},
			want: []string{
				"run", "--rm", "-i=false",
				"--http-proxy=false", "-v", "/home/tester/.mc:/data/.mc:z",
				image, "ls", "store",
			},
		},
		{
			name:        "MC_HOST_* envs forwarded after the mount, before the image",
			useHostNet:  true,
			interactive: false,
			envs:        []string{"MC_HOST_store=http://admin:pass@127.0.0.1:9000"},
			args:        []string{"mb", "store/backups"},
			want: []string{
				"run", "--rm", "-i=false", "--network", "host",
				"--http-proxy=false", "-v", "/home/tester/.mc:/data/.mc:z",
				"-e", "MC_HOST_store=http://admin:pass@127.0.0.1:9000",
				image, "mb", "store/backups",
			},
		},
		{
			name:        "mc flags after -- pass through verbatim, in order",
			useHostNet:  false,
			interactive: true,
			args:        []string{"ls", "store", "--all", "--json"},
			want: []string{
				"run", "--rm", "-it",
				"--http-proxy=false", "-v", "/home/tester/.mc:/data/.mc:z",
				image, "ls", "store", "--all", "--json",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mcRunArgs(image, cfgDir, tt.useHostNet, tt.interactive, tt.envs, tt.args)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("mcRunArgs() =\n  %v\nwant\n  %v", got, tt.want)
			}
		})
	}
}

func TestMCRunArgsMountsConfigDir(t *testing.T) {
	got := strings.Join(mcRunArgs("img", "/x/.mc", true, true, nil, []string{"version"}), " ")
	if !strings.Contains(got, "-v /x/.mc:/data/.mc:z") {
		t.Errorf("mount of the host ~/.mc into the container HOME is missing from: %s", got)
	}
}
