package podman

import (
	"path/filepath"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// silo's data dir and TLS dir resolve under the silo-namespaced layout, not
// minio's — the one place a copy-paste between the two managers would go
// silently wrong (a silo instance writing to addons.minio). Pin it.
func TestSiloPathResolution(t *testing.T) {
	m := &SiloManager{dataDir: "/base"}
	sc := &config.SiloConfig{Name: "s1"}

	if got := m.resolveDataDir(sc); got != filepath.Join("/base", "addon", "silo", "s1", "data") {
		t.Errorf("data dir = %q, want .../addon/silo/s1/data", got)
	}
	if got := m.TLSDir(sc); got != filepath.Join("/base", "tls", "silo", "s1") {
		t.Errorf("tls dir = %q, want .../tls/silo/s1", got)
	}
	// Explicit DataDir override wins, same as minio.
	sc2 := &config.SiloConfig{Name: "s1", DataDir: "/srv/silo"}
	if got := m.resolveDataDir(sc2); got != "/srv/silo" {
		t.Errorf("override data dir = %q, want /srv/silo", got)
	}
}

// serverURL is the MINIO_SERVER_URL silo advertises (inherited env contract):
// scheme follows TLS, host is the listen on Linux. The macOS bridge override
// (127.0.0.1) is a platform branch not reachable in a unit test on Linux.
func TestSiloServerURL(t *testing.T) {
	m := &SiloManager{dataDir: "/base", bridge: false}
	plain := m.serverURL(&config.SiloConfig{Listen: "10.0.0.9", APIPort: 9000})
	if want := "http://10.0.0.9:9000"; plain != want {
		t.Errorf("serverURL = %q, want %q", plain, want)
	}
	tls := m.serverURL(&config.SiloConfig{Listen: "10.0.0.9", APIPort: 9000, TLS: true})
	if want := "https://10.0.0.9:9000"; tls != want {
		t.Errorf("TLS serverURL = %q, want %q", tls, want)
	}
}
