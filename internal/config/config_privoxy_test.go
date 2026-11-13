package config

import (
	"net"
	"runtime"
	"testing"

	"github.com/mars-base/pgcli/internal/platform"
)

func TestPrivoxyDefaults(t *testing.T) {
	cfg := Default()
	cfg.PrivoxyStartPort = 48118
	cfg.Addons.Privoxy = map[string]PrivoxyConfig{
		"p1": {},
	}

	cfg.ApplyDefaults()

	pc, ok := cfg.Addons.Privoxy["p1"]
	if !ok {
		t.Fatal("privoxy addon missing after ApplyDefaults")
	}
	if pc.Name != "p1" {
		t.Errorf("name = %q, want p1", pc.Name)
	}
	if pc.ContainerName != "pgcli-privoxy-p1" {
		t.Errorf("container name = %q, want pgcli-privoxy-p1", pc.ContainerName)
	}
	if pc.ImageTag != DefaultPrivoxyImageTag {
		t.Errorf("image tag = %q, want %q", pc.ImageTag, DefaultPrivoxyImageTag)
	}
	if pc.Listen != "0.0.0.0" {
		t.Errorf("listen = %q, want 0.0.0.0", pc.Listen)
	}
	if pc.Port != 48118 {
		t.Errorf("port = %d, want 48118", pc.Port)
	}
}

func TestPrivoxyPortsIncrementAndSkipExplicit(t *testing.T) {
	cfg := Default()
	cfg.PrivoxyStartPort = 48120
	cfg.Addons.Privoxy = map[string]PrivoxyConfig{
		"z": {},
		"a": {},
		"m": {Port: 48125}, // explicit, kept as-is
	}

	cfg.ApplyDefaults()

	// Names are sorted before allocation: a→48120, then m's explicit 48125
	// moves the cursor past it, so z→48126.
	if got := cfg.Addons.Privoxy["a"].Port; got != 48120 {
		t.Errorf("a port = %d, want 48120", got)
	}
	if got := cfg.Addons.Privoxy["z"].Port; got != 48126 {
		t.Errorf("z port = %d, want 48126", got)
	}
	if got := cfg.Addons.Privoxy["m"].Port; got != 48125 {
		t.Errorf("m explicit port = %d, want 48125", got)
	}
}

func TestPrivoxyACLValuesAreMerged(t *testing.T) {
	cfg := Default()
	cfg.PrivoxyStartPort = 48130
	cfg.Addons.Privoxy = map[string]PrivoxyConfig{
		"p1": {
			AllowNetworks: []string{"10.0.0.0/8, 192.168.0.0/16", "", "10.0.0.0/8", "172.16.0.0/12"},
			DenyNetworks:  []string{"10.0.0.66/32,10.0.0.67/32"},
		},
	}

	cfg.ApplyDefaults()

	pc := cfg.Addons.Privoxy["p1"]
	wantAllow := []string{"10.0.0.0/8", "192.168.0.0/16", "172.16.0.0/12"}
	if len(pc.AllowNetworks) != len(wantAllow) {
		t.Fatalf("allow = %v, want %v", pc.AllowNetworks, wantAllow)
	}
	for i, want := range wantAllow {
		if pc.AllowNetworks[i] != want {
			t.Errorf("allow[%d] = %q, want %q", i, pc.AllowNetworks[i], want)
		}
	}
	wantDeny := []string{"10.0.0.66/32", "10.0.0.67/32"}
	if len(pc.DenyNetworks) != len(wantDeny) {
		t.Fatalf("deny = %v, want %v", pc.DenyNetworks, wantDeny)
	}
	for i, want := range wantDeny {
		if pc.DenyNetworks[i] != want {
			t.Errorf("deny[%d] = %q, want %q", i, pc.DenyNetworks[i], want)
		}
	}
}

// TestPrivoxyPortsSkipHostListeners verifies the occupancy check: a port that
// is already listening on the host is skipped when auto-assigning.
func TestPrivoxyPortsSkipHostListeners(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("host port detection via ss is Linux-only")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	occupied := ln.Addr().(*net.TCPAddr).Port
	if used := platform.GetUsedPorts(); !used[occupied] {
		t.Skipf("ss did not report the test listener on port %d", occupied)
	}

	cfg := Default()
	cfg.PrivoxyStartPort = occupied
	cfg.Addons.Privoxy = map[string]PrivoxyConfig{"p1": {}}

	cfg.ApplyDefaults()

	got := cfg.Addons.Privoxy["p1"].Port
	if got == occupied {
		t.Fatalf("port = %d, want a port other than the occupied one", got)
	}
	if got <= occupied {
		t.Errorf("port = %d, want > %d (next free port after the occupied one)", got, occupied)
	}
}
