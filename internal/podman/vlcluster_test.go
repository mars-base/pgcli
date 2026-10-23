package podman

import (
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// TestVLStorageArgs pins the `podman run` argv for vlstorage: one image, one
// HTTP port for both /internal/insert and /internal/select/*, and the only
// role with a bind mount.
func TestVLStorageArgs(t *testing.T) {
	vc := &config.VLClusterConfig{
		Name:            "vlc",
		ContainerPrefix: "pgcli-vlcluster-default-vlc",
		ImageTag:        config.DefaultVictoriaLogsImageTag,
		Listen:          "0.0.0.0",
		StoragePort:     9432,
		Retention:       "30d",
	}

	args := vlstorageArgs(vc, "/home/u/.pgcli/addon/vlcluster/vlc/data")

	nameIdx := slices.Index(args, "--name")
	if nameIdx < 0 || args[nameIdx+1] != "pgcli-vlcluster-default-vlc-storage" {
		t.Errorf("container name missing or wrong: %v", args)
	}

	if !slices.Contains(args, "--network") || !slices.Contains(args, "host") {
		t.Errorf("expected --network host, got %v", args)
	}

	hasMount := false
	for i, a := range args {
		if a == "-v" && i+1 < len(args) && strings.Contains(args[i+1], "/home/u/.pgcli/addon/vlcluster/vlc/data:/victoria-logs-data:z") {
			hasMount = true
		}
	}
	if !hasMount {
		t.Errorf("data dir bind mount missing: %v", args)
	}

	imgIdx := slices.Index(args, config.DefaultVictoriaLogsImageTag)
	if imgIdx < 0 {
		t.Fatalf("image tag missing: %v", args)
	}
	trailing := args[imgIdx+1:]
	wantFlags := []string{
		"-storageDataPath=/victoria-logs-data",
		"-httpListenAddr=0.0.0.0:9432",
		"-retentionPeriod=30d",
	}
	for _, f := range wantFlags {
		if !slices.Contains(trailing, f) {
			t.Errorf("trailing flag %q missing: %v", f, trailing)
		}
	}
	// vlstorage takes no -storageNode and no role-disable flags.
	for _, f := range trailing {
		if strings.HasPrefix(f, "-storageNode") || f == "-select.disable" || f == "-insert.disable" {
			t.Errorf("vlstorage must not emit %q: %v", f, trailing)
		}
	}
}

// TestVLStorageArgsNoRetention verifies that an empty Retention is omitted, so
// the victoria-logs binary default applies.
func TestVLStorageArgsNoRetention(t *testing.T) {
	vc := &config.VLClusterConfig{
		ContainerPrefix: "pgcli-vlcluster-default-vlc",
		ImageTag:        config.DefaultVictoriaLogsImageTag,
		Listen:          "0.0.0.0",
		StoragePort:     9432,
	}
	args := vlstorageArgs(vc, "/data")
	for _, a := range args {
		if strings.HasPrefix(a, "-retentionPeriod") {
			t.Errorf("empty Retention must not emit -retentionPeriod: %v", args)
		}
	}
}

// TestVLInsertArgs pins the argv for vlinsert — stateless, one -storageNode
// per vlstorage, and -select.disable so it only ingests.
func TestVLInsertArgs(t *testing.T) {
	t.Run("converged single-node", func(t *testing.T) {
		vc := &config.VLClusterConfig{
			Name:            "vlc",
			ContainerPrefix: "pgcli-vlcluster-default-vlc",
			ImageTag:        config.DefaultVictoriaLogsImageTag,
			Listen:          "0.0.0.0",
			InsertPort:      9430,
			StoragePort:     9432,
			// Empty StorageNodes → EffectiveStorageNodes auto-derives 127.0.0.1.
		}

		args := vlinsertArgs(vc)

		for i, a := range args {
			if a == "-v" {
				t.Errorf("stateless vlinsert must not have bind mounts, got -v %s", args[i+1])
			}
		}

		imgIdx := slices.Index(args, config.DefaultVictoriaLogsImageTag)
		if imgIdx < 0 {
			t.Fatalf("image tag missing: %v", args)
		}
		trailing := args[imgIdx+1:]

		if !slices.Contains(trailing, "-httpListenAddr=0.0.0.0:9430") {
			t.Errorf("-httpListenAddr missing: %v", trailing)
		}
		if !slices.Contains(trailing, "-storageNode=127.0.0.1:9432") {
			t.Errorf("auto-derived -storageNode missing: %v", trailing)
		}
		if !slices.Contains(trailing, "-select.disable") {
			t.Errorf("-select.disable missing: %v", trailing)
		}
		if slices.Contains(trailing, "-insert.disable") {
			t.Errorf("vlinsert must not emit -insert.disable: %v", trailing)
		}
	})

	t.Run("cross-host multi-node", func(t *testing.T) {
		vc := &config.VLClusterConfig{
			Name:            "vlc",
			ContainerPrefix: "pgcli-vlcluster-default-vlc",
			ImageTag:        config.DefaultVictoriaLogsImageTag,
			Listen:          "10.0.0.3",
			InsertPort:      9430,
			StorageNodes:    []string{"10.0.0.1:9432", "10.0.0.2:9432"},
		}

		args := vlinsertArgs(vc)
		imgIdx := slices.Index(args, config.DefaultVictoriaLogsImageTag)
		trailing := args[imgIdx+1:]

		if !slices.Contains(trailing, "-storageNode=10.0.0.1:9432") {
			t.Errorf("first -storageNode missing: %v", trailing)
		}
		if !slices.Contains(trailing, "-storageNode=10.0.0.2:9432") {
			t.Errorf("second -storageNode missing: %v", trailing)
		}
		// Order must match the configured list.
		first := slices.Index(trailing, "-storageNode=10.0.0.1:9432")
		second := slices.Index(trailing, "-storageNode=10.0.0.2:9432")
		if first > second {
			t.Errorf("-storageNode order changed: %v", trailing)
		}
	})

	t.Run("extra args last-wins", func(t *testing.T) {
		vc := &config.VLClusterConfig{
			Name:            "vlc",
			ContainerPrefix: "pgcli-vlcluster-default-vlc",
			ImageTag:        config.DefaultVictoriaLogsImageTag,
			Listen:          "0.0.0.0",
			InsertPort:      9430,
			StoragePort:     9432,
			InsertExtraArgs: []string{"-memory.allowedPercent=80"},
		}
		args := vlinsertArgs(vc)
		if last := args[len(args)-1]; last != "-memory.allowedPercent=80" {
			t.Errorf("extra args not last: got %q", last)
		}
	})
}

// TestVLSelectArgs pins the argv for vlselect — stateless, same storage
// addresses as vlinsert (one port per node), and -insert.disable.
func TestVLSelectArgs(t *testing.T) {
	vc := &config.VLClusterConfig{
		Name:            "vlc",
		ContainerPrefix: "pgcli-vlcluster-default-vlc",
		ImageTag:        config.DefaultVictoriaLogsImageTag,
		Listen:          "0.0.0.0",
		SelectPort:      9431,
		StorageNodes:    []string{"10.0.0.1:9432", "10.0.0.2:9432"},
	}

	args := vlselectArgs(vc)
	imgIdx := slices.Index(args, config.DefaultVictoriaLogsImageTag)
	if imgIdx < 0 {
		t.Fatalf("image tag missing: %v", args)
	}
	trailing := args[imgIdx+1:]

	if !slices.Contains(trailing, "-httpListenAddr=0.0.0.0:9431") {
		t.Errorf("-httpListenAddr missing: %v", trailing)
	}
	if !slices.Contains(trailing, "-storageNode=10.0.0.1:9432") || !slices.Contains(trailing, "-storageNode=10.0.0.2:9432") {
		t.Errorf("-storageNode list missing: %v", trailing)
	}
	if !slices.Contains(trailing, "-insert.disable") {
		t.Errorf("-insert.disable missing: %v", trailing)
	}
	if slices.Contains(trailing, "-select.disable") {
		t.Errorf("vlselect must not emit -select.disable: %v", trailing)
	}
}

// TestVLClusterContainerName verifies the name-suffix convention the three
// builders rely on.
func TestVLClusterContainerName(t *testing.T) {
	vc := &config.VLClusterConfig{ContainerPrefix: "pgcli-vlcluster-default-vlc"}
	cases := map[string]string{
		config.VLClusterRoleStorage: "pgcli-vlcluster-default-vlc-storage",
		config.VLClusterRoleInsert:  "pgcli-vlcluster-default-vlc-insert",
		config.VLClusterRoleSelect:  "pgcli-vlcluster-default-vlc-select",
	}
	for role, want := range cases {
		if got := vc.ContainerName(role); got != want {
			t.Errorf("ContainerName(%q) = %q, want %q", role, got, want)
		}
	}
}

// TestVLClusterEffectiveStorageNodes verifies the auto-derivation for
// converged deployments (empty StorageNodes → 127.0.0.1:<StoragePort>).
func TestVLClusterEffectiveStorageNodes(t *testing.T) {
	t.Run("empty listen", func(t *testing.T) {
		vc := &config.VLClusterConfig{StoragePort: 9432}
		if got := vc.EffectiveStorageNodes(); len(got) != 1 || got[0] != "127.0.0.1:9432" {
			t.Errorf("EffectiveStorageNodes() = %v, want [127.0.0.1:9432]", got)
		}
	})

	t.Run("wildcard listen", func(t *testing.T) {
		vc := &config.VLClusterConfig{Listen: "0.0.0.0", StoragePort: 9432}
		if got := vc.EffectiveStorageNodes(); got[0] != "127.0.0.1:9432" {
			t.Errorf("EffectiveStorageNodes() = %v, want [127.0.0.1:9432]", got)
		}
	})

	t.Run("explicit IPv4 listen", func(t *testing.T) {
		vc := &config.VLClusterConfig{Listen: "10.0.0.1", StoragePort: 9432}
		if got := vc.EffectiveStorageNodes(); got[0] != "10.0.0.1:9432" {
			t.Errorf("EffectiveStorageNodes() = %v, want [10.0.0.1:9432]", got)
		}
	})

	t.Run("wildcard IPv6 listen", func(t *testing.T) {
		vc := &config.VLClusterConfig{Listen: "::", StoragePort: 9432}
		if got := vc.EffectiveStorageNodes(); got[0] != "127.0.0.1:9432" {
			t.Errorf("EffectiveStorageNodes() = %v, want [127.0.0.1:9432]", got)
		}
	})

	t.Run("explicit storage nodes override", func(t *testing.T) {
		vc := &config.VLClusterConfig{
			Listen:       "10.0.0.3",
			StorageNodes: []string{"10.0.0.1:9432", "10.0.0.2:9432"},
		}
		got := vc.EffectiveStorageNodes()
		if len(got) != 2 || got[0] != "10.0.0.1:9432" || got[1] != "10.0.0.2:9432" {
			t.Errorf("EffectiveStorageNodes() = %v", got)
		}
	})
}

// TestVLClusterRoles verifies canonical role ordering and RunsRole.
func TestVLClusterRoles(t *testing.T) {
	if got := (&config.VLClusterConfig{}).RolesInOrder(); !slices.Equal(got, []string{"storage", "insert", "select"}) {
		t.Errorf("default RolesInOrder() = %v", got)
	}

	vc := &config.VLClusterConfig{Roles: []string{"select", "insert", "select"}}
	if got := vc.RolesInOrder(); !slices.Equal(got, []string{"insert", "select"}) {
		t.Errorf("RolesInOrder() = %v, want [insert select]", got)
	}
	if vc.RunsRole("storage") {
		t.Error("RunsRole(storage) = true, want false")
	}
	if !vc.RunsRole("insert") {
		t.Error("RunsRole(insert) = false, want true")
	}
}
