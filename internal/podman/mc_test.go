package podman

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMCRunArgs(t *testing.T) {
	const image = "ghcr.io/mars-base/pgcli/pgcli-mc:testtag"
	const cfgDir = "/home/tester/.mc"

	tests := []struct {
		name        string
		mounts      []string
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
			name:       "file-operand mounts published at their own path",
			mounts:     []string{"/data/src", "/data/dst"},
			useHostNet: true,
			args:       []string{"cp", "/data/src/a.pglz", "/data/dst/"},
			want: []string{
				"run", "--rm", "-i=false", "--network", "host",
				"--http-proxy=false",
				"-v", "/home/tester/.mc:/data/.mc:z",
				"-v", "/data/src:/data/src:z",
				"-v", "/data/dst:/data/dst:z",
				image, "cp", "/data/src/a.pglz", "/data/dst/",
			},
		},
		{
			name:        "MC_HOST_* envs forwarded after the mounts, before the image",
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
			got := mcRunArgs(image, cfgDir, tt.mounts, tt.useHostNet, tt.interactive, tt.envs, tt.args)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("mcRunArgs() =\n  %v\nwant\n  %v", got, tt.want)
			}
		})
	}
}

func TestMCRunArgsMountsConfigDir(t *testing.T) {
	got := strings.Join(mcRunArgs("img", "/x/.mc", nil, true, true, nil, []string{"version"}), " ")
	if !strings.Contains(got, "-v /x/.mc:/data/.mc:z") {
		t.Errorf("mount of the host ~/.mc into the container HOME is missing from: %s", got)
	}
}

func TestMCLocalFiles(t *testing.T) {
	aliases := map[string]bool{"store": true, "gcs": true}

	// an existing file operand is mounted at its own absolute path, resolved
	// like realpath (macOS temp dirs live under /private/var).
	dir := t.TempDir()
	file := filepath.Join(dir, "up.pglz")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	realFile, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatal(err)
	}
	newArgs, mounts := mcLocalFiles([]string{"cp", file, "store/backups/"}, aliases)
	if !reflect.DeepEqual(mounts, []string{realFile}) {
		t.Errorf("existing-file mount = %v, want the file itself %v", mounts, realFile)
	}
	wantArgs := []string{"cp", realFile, "store/backups/"}
	if !reflect.DeepEqual(newArgs, wantArgs) {
		t.Errorf("rewritten args = %v, want %v", newArgs, wantArgs)
	}

	// a not-yet-existing destination operand mounts its nearest existing
	// ancestor, keeping the full target path for mc to write into.
	realDir := filepath.Dir(realFile)
	dlDir := filepath.Join(realDir, "dl")
	newArgs, mounts = mcLocalFiles([]string{"cp", "store/backups/x.pglz", dlDir + "/y.pglz"}, aliases)
	if !reflect.DeepEqual(mounts, []string{realDir}) {
		t.Errorf("missing-file mount = %v, want %v", mounts, realDir)
	}
	if newArgs[2] != dlDir+"/y.pglz" {
		t.Errorf("download arg = %q, want unchanged absolute %q", newArgs[2], dlDir+"/y.pglz")
	}

	// non-file subcommands never get local-path treatment, and remote
	// operands of a file command pass through untouched.
	for _, args := range [][]string{
		{"ls", "./not-a-file-cp"},
		{"version"},
		{"cp", "store/a", "gcs/b"},
	} {
		newArgs, mounts := mcLocalFiles(args, aliases)
		if len(mounts) != 0 || !reflect.DeepEqual(newArgs, args) {
			t.Errorf("mcLocalFiles(%v) = (%v, %v), want untouched", args, newArgs, mounts)
		}
	}

	// relative operands resolve against the cwd (hostMountPath → Abs),
	// flags and -- passthrough survive.
	wd, wdErr := os.Getwd()
	if wdErr != nil {
		t.Fatal(wdErr)
	}
	newArgs, mounts = mcLocalFiles([]string{"mirror", "./out", "store/b", "--overwrite", "--json"}, aliases)
	if len(mounts) != 1 || mounts[0] != wd {
		t.Errorf("relative mount = %v, want [%s]", mounts, wd)
	}
	wantRelArgs := []string{"mirror", filepath.Join(wd, "out"), "store/b", "--overwrite", "--json"}
	if !reflect.DeepEqual(newArgs, wantRelArgs) {
		t.Errorf("mirror args = %v, want %v", newArgs, wantRelArgs)
	}
}

func TestMCKnownAliases(t *testing.T) {
	dir := t.TempDir()
	// mc's real config.json shape: aliases keyed by name.
	if err := os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"version":"10","aliases":{"store":{"url":"http://h:9000","accessKey":"a"},"gcs":{"url":"https://storage.googleapis.com"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	names := mcKnownAliases(dir, []string{"MC_HOST_e2e=http://admin:pass@host:9000"})
	for _, want := range []string{"store", "gcs", "e2e"} {
		if !names[want] {
			t.Errorf("alias %q not detected (names=%v)", want, names)
		}
	}
	if names["./file"] {
		t.Error("a path must never be an alias")
	}

	// a missing file simply yields the env aliases.
	names = mcKnownAliases(filepath.Join(dir, "nonexistent"), nil)
	if len(names) != 0 {
		t.Errorf("missing config = %v, want empty", names)
	}
}

func TestIsMCLocalOperand(t *testing.T) {
	aliases := map[string]bool{"store": true}
	tests := []struct {
		arg  string
		want bool
	}{
		{"./file", true},
		{"../file", true},
		{"/abs/file", true},
		{"~/file", true},
		{"file", true},   // bare name, not an alias → local
		{"store", false}, // known alias
		{"store/bucket/f", false},
		{"s3://bucket/f", false},
		{"https://e/b/f", false},
		{"--overwrite", false},
		{"", false},
		{"unknown", true}, // not a known alias → treated as a local path, like mc does
	}
	for _, tt := range tests {
		if got := isMCLocalOperand(tt.arg, aliases); got != tt.want {
			t.Errorf("isMCLocalOperand(%q) = %v, want %v", tt.arg, got, tt.want)
		}
	}
}
