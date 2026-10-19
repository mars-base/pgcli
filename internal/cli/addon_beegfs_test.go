package cli

import (
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// BeeGFS's entry in the password index is the connection-auth secret the three
// daemons (and every client's /etc/beegfs/conn.auth) share. `pg addon password
// beegfs --name <fs>` must resolve the "<addon>:<name>" key the CLI builds
// from the positional arg + --name. An empty ConnAuth (a revived store whose
// secret lives in the daemons, not the config) must still produce the key —
// presence/absence is rendered by the caller, not by dropping the entry.
func TestAddonPasswordsIncludesBeeGFS(t *testing.T) {
	cfg := config.Default()
	cfg.Addons.BeeGFS = map[string]config.BeeGFSConfig{
		"fs1": {Name: "fs1", ConnAuth: "secret-beegfs"},
		"fs2": {Name: "fs2"},
	}
	all := addonPasswords(cfg)
	if got, ok := all["beegfs:fs1"]; !ok {
		t.Fatalf("missing key beegfs:fs1 (have %v)", keysOf(all))
	} else if got != "secret-beegfs" {
		t.Errorf("beegfs:fs1 = %q, want secret-beegfs", got)
	}
	if got, ok := all["beegfs:fs2"]; !ok {
		t.Fatalf("beegfs:fs2 must key even with no secret (have %v)", keysOf(all))
	} else if got != "" {
		t.Errorf("beegfs:fs2 = %q, want empty", got)
	}
}

// The two mgmtd port overrides are the ONLY movable BeeGFS ports (meta 8005
// and storage 8003 cannot be relocated at all — verified on vm01), and they
// must be plain Ints: Changed-gating distinguishes "not passed" from "passed
// as 0", which a default-equal-to-0 scheme cannot.
func TestBeeGFSWiringFlagsRegistered(t *testing.T) {
	for _, name := range []string{"beegfs-mgmtd-port", "beegfs-mgmtd-grpc-port"} {
		f := addonInstallCmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("--%s is not registered", name)
		}
		if f.Value.Type() != "int" {
			t.Errorf("--%s is %s, want int", name, f.Value.Type())
		}
	}
}
