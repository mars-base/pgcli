package podman

import (
	"strings"
	"testing"
)

// mcliRunArgs is mcRunArgs plus exactly two differences: the silo image needs
// an explicit --entrypoint mcli (its default ENTRYPOINT runs the server), and
// MC_CONFIG_DIR must be pinned to the mounted config dir (mcli's own default
// is $HOME/.mcli, and the image's HOME is not the /data pgcli mounts — so
// pgcli pins it to the host ~/.mc mount, sharing mc's alias file). Everything
// else — network, proxy, config mount, operand mounts, env, image, args — is
// identical to mc, so this pins only the delta and the ordering that matters
// (entrypoint/env/flags before the image, mcli args after it).
func TestMCLIRunArgsEntrypointAndConfigDir(t *testing.T) {
	got := mcliRunArgs("docker.io/pgsty/silo:TAG", "/home/u/.mc", []string{"/home/u/f"}, true, false, []string{"MC_HOST_store=x"}, []string{"ls", "store"})
	joined := strings.Join(got, " ")

	for _, want := range []string{
		"--entrypoint mcli",
		"-e MC_CONFIG_DIR=" + mcliContainerConfigDir,
		"-v /home/u/.mc:" + mcliContainerConfigDir + ":z",
		"--network host",
		"-e MC_HOST_store=x",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("mcliRunArgs missing %q:\n  %v", want, got)
		}
	}
	// The image must come after the flags and before the mcli subcommand.
	imgAt := indexOf(got, "docker.io/pgsty/silo:TAG")
	if imgAt < 0 {
		t.Fatalf("image not in args: %v", got)
	}
	if got[imgAt+1] != "ls" || got[imgAt+2] != "store" {
		t.Errorf("args must follow the image verbatim, got %v", got[imgAt:])
	}
	epIdx := indexOf(got, "--entrypoint")
	if epIdx < 0 || got[epIdx+1] != "mcli" {
		t.Errorf("--entrypoint mcli malformed: %v", got)
	}
}

func indexOf(hay []string, needle string) int {
	for i, s := range hay {
		if s == needle {
			return i
		}
	}
	return -1
}
