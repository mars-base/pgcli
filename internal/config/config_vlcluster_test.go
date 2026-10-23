package config

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestVLClusterDefaultStartPort(t *testing.T) {
	cfg := Default()
	if cfg.VLClusterStartPort != 9430 {
		t.Errorf("VLClusterStartPort = %d, want 9430", cfg.VLClusterStartPort)
	}

	cfg.VLClusterStartPort = 0
	cfg.ApplyDefaults()
	if cfg.VLClusterStartPort != 9430 {
		t.Errorf("VLClusterStartPort after ApplyDefaults = %d, want 9430", cfg.VLClusterStartPort)
	}
}

func TestVLClusterDefaults(t *testing.T) {
	cfg := Default()
	cfg.VLClusterStartPort = 49430
	cfg.Addons.VLCluster = map[string]VLClusterConfig{
		"vlc": {},
	}

	cfg.ApplyDefaults()

	vc, ok := cfg.Addons.VLCluster["vlc"]
	if !ok {
		t.Fatal("vlcluster addon missing after ApplyDefaults")
	}
	if vc.Name != "vlc" {
		t.Errorf("name = %q, want vlc", vc.Name)
	}
	if vc.ContainerPrefix != "pgcli-vlcluster-vlc" {
		t.Errorf("container prefix = %q, want pgcli-vlcluster-vlc", vc.ContainerPrefix)
	}
	if vc.ImageTag != DefaultVictoriaLogsImageTag {
		t.Errorf("image = %q, want %q (one image for all roles)", vc.ImageTag, DefaultVictoriaLogsImageTag)
	}
	if vc.Listen != "0.0.0.0" {
		t.Errorf("listen = %q, want 0.0.0.0", vc.Listen)
	}
	if got := vc.RolesInOrder(); !slices.Equal(got, []string{"storage", "insert", "select"}) {
		t.Errorf("roles = %v, want [storage insert select]", got)
	}
	// Deliberately not defaulted: empty means the victoria-logs binary default.
	if vc.Retention != "" {
		t.Errorf("retention = %q, want empty (binary default)", vc.Retention)
	}
	if len(vc.StorageNodes) != 0 {
		t.Errorf("storage nodes = %v, want empty (auto-derived when converged)", vc.StorageNodes)
	}
	if vc.DataDir != "" {
		t.Errorf("data dir = %q, want empty (manager resolves the default layout)", vc.DataDir)
	}

	if vc.InsertPort != 49430 || vc.SelectPort != 49431 || vc.StoragePort != 49432 {
		t.Errorf("ports = %d/%d/%d, want 49430/49431/49432",
			vc.InsertPort, vc.SelectPort, vc.StoragePort)
	}
	if vc.ContainerName(VLClusterRoleStorage) != "pgcli-vlcluster-vlc-storage" {
		t.Errorf("container name = %q", vc.ContainerName(VLClusterRoleStorage))
	}
}

// VLCluster instances get 3 consecutive ports each, names sorted, and every
// instance gets all three even when this host runs only the storage role (a
// storage node's address must be predictable across hosts).
func TestVLClusterPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.VLClusterStartPort = 49430
	cfg.Addons.VLCluster = map[string]VLClusterConfig{
		"b": {Roles: []string{"storage"}},
		"a": {},
	}

	cfg.ApplyDefaults()

	a := cfg.Addons.VLCluster["a"]
	if a.InsertPort != 49430 || a.SelectPort != 49431 || a.StoragePort != 49432 {
		t.Errorf("a ports = %d/%d/%d, want 49430/49431/49432", a.InsertPort, a.SelectPort, a.StoragePort)
	}
	b := cfg.Addons.VLCluster["b"]
	if b.InsertPort != 49433 || b.SelectPort != 49434 || b.StoragePort != 49435 {
		t.Errorf("b ports = %d/%d/%d, want 49433/49434/49435", b.InsertPort, b.SelectPort, b.StoragePort)
	}

	// Second pass must be a no-op (ApplyDefaults is called on every load).
	cfg.ApplyDefaults()
	a2 := cfg.Addons.VLCluster["a"]
	b2 := cfg.Addons.VLCluster["b"]
	if !reflect.DeepEqual(a2, a) || !reflect.DeepEqual(b2, b) {
		t.Errorf("ApplyDefaults is not idempotent:\nfirst  %+v\nsecond %+v", []VLClusterConfig{a, b}, []VLClusterConfig{a2, b2})
	}
}

// Explicit ports are preserved; zero ports are filled without colliding with
// the explicit ones.
func TestVLClusterExplicitPortPreserved(t *testing.T) {
	cfg := Default()
	cfg.VLClusterStartPort = 49430
	cfg.Addons.VLCluster = map[string]VLClusterConfig{
		"a": {InsertPort: 49500},
	}

	cfg.ApplyDefaults()

	a := cfg.Addons.VLCluster["a"]
	if a.InsertPort != 49500 {
		t.Errorf("explicit insert port changed: %d", a.InsertPort)
	}
	if a.SelectPort == 0 || a.StoragePort == 0 {
		t.Fatalf("zero ports not filled: %+v", a)
	}
	seen := map[int]bool{}
	for _, p := range []int{a.InsertPort, a.SelectPort, a.StoragePort} {
		if p != 0 && seen[p] {
			t.Errorf("duplicate port %d in %+v", p, a)
		}
		seen[p] = true
	}
	if a.SelectPort >= 49500 {
		t.Errorf("zero ports should be filled from the base, got select=%d storage=%d", a.SelectPort, a.StoragePort)
	}
}

// The VictoriaLogs / vlagent pools can grow up into VLCluster's 9430+ range;
// the three pools must never collide within one ApplyDefaults pass.
func TestVLClusterCrossPoolNonCollision(t *testing.T) {
	cfg := Default()
	cfg.VictoriaLogsStartPort = 49428
	cfg.VlagentStartPort = 49429
	cfg.VLClusterStartPort = 49430

	cfg.Addons.VictoriaLogs = map[string]VictoriaLogsConfig{
		"vl": {}, // takes 49428
	}
	cfg.Addons.Vlagent = map[string]VlagentConfig{
		"va": {}, // takes 49429
	}
	cfg.Addons.VLCluster = map[string]VLClusterConfig{
		"vlc": {},
	}

	cfg.ApplyDefaults()

	vl := cfg.Addons.VictoriaLogs["vl"]
	va := cfg.Addons.Vlagent["va"]
	vc := cfg.Addons.VLCluster["vlc"]

	seen := map[int]string{}
	for name, p := range map[string]int{
		"victorialogs": vl.HostPort,
		"vlagent":      va.HostPort,
		"vlc/insert":   vc.InsertPort,
		"vlc/select":   vc.SelectPort,
		"vlc/storage":  vc.StoragePort,
	} {
		if p == 0 {
			t.Fatalf("%s port not assigned", name)
		}
		if other, dup := seen[p]; dup {
			t.Errorf("%s and %s both use port %d", other, name, p)
		}
		seen[p] = name
	}
}

// A vlcluster instance with explicit ports must also push the other single-port
// pools off those ports, whichever pool is allocated first.
func TestVLClusterCrossPoolPinsVictoriaLogs(t *testing.T) {
	cfg := Default()
	cfg.VictoriaLogsStartPort = 49428
	cfg.VlagentStartPort = 49429
	cfg.VLClusterStartPort = 49430

	cfg.Addons.VLCluster = map[string]VLClusterConfig{
		"vlc": {InsertPort: 49430, SelectPort: 49431, StoragePort: 49432},
	}
	cfg.Addons.VictoriaLogs = map[string]VictoriaLogsConfig{
		"extra": {}, // base pool grows; must skip 49430-49432
	}

	cfg.ApplyDefaults()

	vl := cfg.Addons.VictoriaLogs["extra"]
	if vl.HostPort == 0 {
		t.Fatal("victorialogs port not assigned")
	}
	for _, p := range []int{49430, 49431, 49432} {
		if vl.HostPort == p {
			t.Errorf("victorialogs took vlcluster port %d", p)
		}
	}
}

func TestVLClusterSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")

	cfg := Default()
	cfg.VLClusterStartPort = 49430
	cfg.Addons.VLCluster = map[string]VLClusterConfig{
		"vlc": {
			ContainerPrefix:  "pgcli-vlcluster-vlc",
			Name:             "vlc",
			ImageTag:         DefaultVictoriaLogsImageTag,
			Listen:           "10.0.0.3",
			InsertPort:       49430,
			SelectPort:       49431,
			StoragePort:      49432,
			Roles:            []string{"insert", "select"},
			StorageNodes:     []string{"10.0.0.1:49432", "10.0.0.2:49432"},
			Retention:        "30d",
			InsertExtraArgs:  []string{"-memory.allowedPercent=80"},
			SelectExtraArgs:  []string{"-search.maxQueryDuration=30s"},
			StorageExtraArgs: []string{"-futureRetention=7d"},
			Autostart:        true,
		},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	vc, ok := got.Addons.VLCluster["vlc"]
	if !ok {
		t.Fatal("vlcluster addon not persisted")
	}
	if vc.ImageTag != DefaultVictoriaLogsImageTag {
		t.Errorf("image = %q", vc.ImageTag)
	}
	if !slices.Equal(vc.StorageNodes, []string{"10.0.0.1:49432", "10.0.0.2:49432"}) {
		t.Errorf("storage nodes = %v", vc.StorageNodes)
	}
	if !slices.Equal(vc.RolesInOrder(), []string{"insert", "select"}) {
		t.Errorf("roles = %v", vc.RolesInOrder())
	}
	if vc.Retention != "30d" {
		t.Errorf("retention = %q", vc.Retention)
	}
	if !slices.Equal(vc.InsertExtraArgs, []string{"-memory.allowedPercent=80"}) ||
		!slices.Equal(vc.SelectExtraArgs, []string{"-search.maxQueryDuration=30s"}) ||
		!slices.Equal(vc.StorageExtraArgs, []string{"-futureRetention=7d"}) {
		t.Errorf("extra args lost: %+v", vc)
	}
	if !vc.Autostart {
		t.Error("autostart lost")
	}
	if got.VLClusterStartPort != 49430 {
		t.Errorf("vlcluster_start_port = %d", got.VLClusterStartPort)
	}
}
