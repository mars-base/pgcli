package podman

import (
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// TestVMStorageArgs pins the `podman run` argv for vmstorage. It is configured
// entirely by CLI flags after the image name (no config file, no env vars),
// same shape as the single-node VictoriaMetrics. The trailing-flag contract is
// the whole load-bearing surface.
func TestVMStorageArgs(t *testing.T) {
	vc := &config.VMClusterConfig{
		Name:               "vmc",
		ContainerPrefix:    "pgcli-vmcluster-default-vmc",
		StorageImageTag:    config.DefaultVMStorageImageTag,
		Listen:             "0.0.0.0",
		StoragePort:        8482,
		InternalInsertPort: 8483,
		InternalSelectPort: 8484,
		Retention:          "3",
	}

	args := vmstorageArgs(vc, "/home/u/.pgcli/addon/vmcluster/vmc/data")

	// Container name has the storage suffix.
	if !slices.Contains(args, "--name") {
		t.Fatal("--name missing")
	}
	nameIdx := slices.Index(args, "--name")
	if args[nameIdx+1] != "pgcli-vmcluster-default-vmc-storage" {
		t.Errorf("container name = %q, want pgcli-vmcluster-default-vmc-storage", args[nameIdx+1])
	}

	// Host networking.
	if !slices.Contains(args, "--network") || !slices.Contains(args, "host") {
		t.Errorf("expected --network host, got %v", args)
	}

	// Data dir bind mount.
	hasMount := false
	for i, a := range args {
		if a == "-v" && i+1 < len(args) && strings.Contains(args[i+1], "/home/u/.pgcli/addon/vmcluster/vmc/data:/storage:z") {
			hasMount = true
		}
	}
	if !hasMount {
		t.Errorf("data dir bind mount missing: %v", args)
	}

	// Image is in the argv.
	imgIdx := slices.Index(args, config.DefaultVMStorageImageTag)
	if imgIdx < 0 {
		t.Fatalf("storage image tag missing: %v", args)
	}

	// Trailing flags come AFTER the image.
	trailing := args[imgIdx+1:]
	wantFlags := []string{
		"-storageDataPath=/storage",
		"-httpListenAddr=0.0.0.0:8482",
		"-vminsertAddr=:8483",
		"-vmselectAddr=:8484",
		"-retentionPeriod=3",
	}
	for _, f := range wantFlags {
		if !slices.Contains(trailing, f) {
			t.Errorf("trailing flag %q missing: %v", f, trailing)
		}
	}
}

// TestVMinInsertArgs pins the argv for vminsert — stateless (no bind mount),
// with one -storageNode per vmstorage (the horizontal-scaling core).
func TestVMinInsertArgs(t *testing.T) {
	t.Run("converged single-node", func(t *testing.T) {
		vc := &config.VMClusterConfig{
			Name:               "vmc",
			ContainerPrefix:    "pgcli-vmcluster-default-vmc",
			InsertImageTag:     config.DefaultVMInsertImageTag,
			Listen:             "0.0.0.0",
			InsertPort:         8480,
			InternalInsertPort: 8483,
			InternalSelectPort: 8484,
			ReplicationFactor:  1,
			// Empty StorageNodes → EffectiveStorageNodes auto-derives 127.0.0.1.
		}

		args := vminsertArgs(vc)

		// No bind mount — vminsert is stateless.
		for i, a := range args {
			if a == "-v" {
				t.Errorf("stateless vminsert must not have bind mounts, got -v %s", args[i+1])
			}
		}

		imgIdx := slices.Index(args, config.DefaultVMInsertImageTag)
		if imgIdx < 0 {
			t.Fatalf("insert image tag missing: %v", args)
		}
		trailing := args[imgIdx+1:]

		if !slices.Contains(trailing, "-httpListenAddr=0.0.0.0:8480") {
			t.Errorf("-httpListenAddr missing: %v", trailing)
		}
		// Auto-derived storage node: 127.0.0.1:InternalInsertPort.
		if !slices.Contains(trailing, "-storageNode=127.0.0.1:8483") {
			t.Errorf("auto-derived -storageNode missing: %v", trailing)
		}
		// ReplicationFactor=1 → not emitted.
		for _, f := range trailing {
			if strings.HasPrefix(f, "-replicationFactor") {
				t.Errorf("replicationFactor=1 should not be emitted: %v", trailing)
			}
		}
	})

	t.Run("cross-host multi-node with replication", func(t *testing.T) {
		vc := &config.VMClusterConfig{
			Name:            "vmc",
			ContainerPrefix: "pgcli-vmcluster-default-vmc",
			InsertImageTag:  config.DefaultVMInsertImageTag,
			Listen:          "10.241.20.153",
			InsertPort:      8480,
			StorageNodes: []config.VMClusterStorageNode{
				{Address: "10.241.20.147:8483", SelectPort: 8484},
				{Address: "10.241.20.148:8483", SelectPort: 8484},
			},
			ReplicationFactor: 2,
		}

		args := vminsertArgs(vc)
		imgIdx := slices.Index(args, config.DefaultVMInsertImageTag)
		trailing := args[imgIdx+1:]

		// Both storage nodes present.
		if !slices.Contains(trailing, "-storageNode=10.241.20.147:8483") {
			t.Errorf("first -storageNode missing: %v", trailing)
		}
		if !slices.Contains(trailing, "-storageNode=10.241.20.148:8483") {
			t.Errorf("second -storageNode missing: %v", trailing)
		}
		if !slices.Contains(trailing, "-replicationFactor=2") {
			t.Errorf("-replicationFactor=2 missing: %v", trailing)
		}
	})

	t.Run("extra args last-wins", func(t *testing.T) {
		vc := &config.VMClusterConfig{
			Name:            "vmc",
			ContainerPrefix: "pgcli-vmcluster-default-vmc",
			InsertImageTag:  config.DefaultVMInsertImageTag,
			Listen:          "0.0.0.0",
			InsertPort:      8480,
			InsertExtraArgs: []string{"-search.maxUniqueTimeseries=1000000"},
		}

		args := vminsertArgs(vc)
		// Extra args must be last.
		last := args[len(args)-1]
		if last != "-search.maxUniqueTimeseries=1000000" {
			t.Errorf("extra args not last: got %q, want -search.maxUniqueTimeseries=1000000", last)
		}
	})
}

// TestVMSelectArgs pins the argv for vmselect — stateless, with -storageNode
// using host:SelectPort (the SELECT protocol port, different from vminsert's
// insert protocol port on the same vmstorage).
func TestVMSelectArgs(t *testing.T) {
	t.Run("converged single-node", func(t *testing.T) {
		vc := &config.VMClusterConfig{
			Name:               "vmc",
			ContainerPrefix:    "pgcli-vmcluster-default-vmc",
			SelectImageTag:     config.DefaultVMSelectImageTag,
			Listen:             "0.0.0.0",
			SelectPort:         8481,
			InternalInsertPort: 8483,
			InternalSelectPort: 8484,
		}

		args := vmselectArgs(vc)
		imgIdx := slices.Index(args, config.DefaultVMSelectImageTag)
		trailing := args[imgIdx+1:]

		if !slices.Contains(trailing, "-httpListenAddr=0.0.0.0:8481") {
			t.Errorf("-httpListenAddr missing: %v", trailing)
		}
		// Auto-derived: 127.0.0.1:InternalSelectPort (select protocol).
		if !slices.Contains(trailing, "-storageNode=127.0.0.1:8484") {
			t.Errorf("auto-derived -storageNode missing: %v", trailing)
		}
	})

	t.Run("cross-host multi-node", func(t *testing.T) {
		vc := &config.VMClusterConfig{
			Name:            "vmc",
			ContainerPrefix: "pgcli-vmcluster-default-vmc",
			SelectImageTag:  config.DefaultVMSelectImageTag,
			Listen:          "10.241.20.153",
			SelectPort:      8481,
			StorageNodes: []config.VMClusterStorageNode{
				{Address: "10.241.20.147:8483", SelectPort: 8484},
				{Address: "10.241.20.148:8483", SelectPort: 8484},
			},
		}

		args := vmselectArgs(vc)
		imgIdx := slices.Index(args, config.DefaultVMSelectImageTag)
		trailing := args[imgIdx+1:]

		// vmselect uses host:SelectPort (select protocol).
		if !slices.Contains(trailing, "-storageNode=10.241.20.147:8484") {
			t.Errorf("first -storageNode (select port) missing: %v", trailing)
		}
		if !slices.Contains(trailing, "-storageNode=10.241.20.148:8484") {
			t.Errorf("second -storageNode (select port) missing: %v", trailing)
		}
		// Must NOT use the insert protocol port.
		for _, f := range trailing {
			if strings.HasPrefix(f, "-storageNode=") && strings.HasSuffix(f, ":8483") {
				t.Errorf("vmselect must use select port (8484), not insert port (8483): %v", trailing)
			}
		}
	})
}

// TestVMClusterContainerName verifies the name-suffix convention the three
// builders rely on.
func TestVMClusterContainerName(t *testing.T) {
	vc := &config.VMClusterConfig{
		ContainerPrefix: "pgcli-vmcluster-default-vmc",
	}
	cases := map[string]string{
		config.VMClusterRoleStorage: "pgcli-vmcluster-default-vmc-storage",
		config.VMClusterRoleInsert:  "pgcli-vmcluster-default-vmc-insert",
		config.VMClusterRoleSelect:  "pgcli-vmcluster-default-vmc-select",
	}
	for role, want := range cases {
		if got := vc.ContainerName(role); got != want {
			t.Errorf("ContainerName(%q) = %q, want %q", role, got, want)
		}
	}
}

// TestVMClusterEffectiveStorageNodes verifies the auto-derivation for
// converged deployments (empty StorageNodes → 127.0.0.1:<internalPorts>).
func TestVMClusterEffectiveStorageNodes(t *testing.T) {
	t.Run("converged auto-derive", func(t *testing.T) {
		vc := &config.VMClusterConfig{
			Listen:             "0.0.0.0",
			InternalInsertPort: 8483,
			InternalSelectPort: 8484,
		}
		nodes := vc.EffectiveStorageNodes()
		if len(nodes) != 1 {
			t.Fatalf("expected 1 auto-derived node, got %d", len(nodes))
		}
		if nodes[0].Address != "127.0.0.1:8483" {
			t.Errorf("Address = %q, want 127.0.0.1:8483", nodes[0].Address)
		}
		if nodes[0].SelectPort != 8484 {
			t.Errorf("SelectPort = %d, want 8484", nodes[0].SelectPort)
		}
	})

	t.Run("explicit listen IP", func(t *testing.T) {
		vc := &config.VMClusterConfig{
			Listen:             "10.241.20.147",
			InternalInsertPort: 8483,
			InternalSelectPort: 8484,
		}
		nodes := vc.EffectiveStorageNodes()
		if nodes[0].Address != "10.241.20.147:8483" {
			t.Errorf("Address = %q, want 10.241.20.147:8483", nodes[0].Address)
		}
	})

	t.Run("explicit storage nodes override", func(t *testing.T) {
		vc := &config.VMClusterConfig{
			Listen: "10.241.20.153",
			StorageNodes: []config.VMClusterStorageNode{
				{Address: "10.241.20.147:8483", SelectPort: 8484},
				{Address: "10.241.20.148:8483", SelectPort: 8484},
			},
		}
		nodes := vc.EffectiveStorageNodes()
		if len(nodes) != 2 {
			t.Fatalf("expected 2 explicit nodes, got %d", len(nodes))
		}
	})
}

// TestVMClusterHostFromAddress verifies the host extraction from "host:port"
// strings — used by vmselectArgs to build host:SelectPort from the
// VMClusterStorageNode.Address (which is host:InsertInternalPort).
func TestVMClusterHostFromAddress(t *testing.T) {
	cases := []struct {
		addr string
		want string
	}{
		{"10.241.20.147:8483", "10.241.20.147"},
		{"127.0.0.1:8483", "127.0.0.1"},
		{"myhost:9000", "myhost"},
	}
	for _, c := range cases {
		if got := vmclusterHostFromAddress(c.addr); got != c.want {
			t.Errorf("vmclusterHostFromAddress(%q) = %q, want %q", c.addr, got, c.want)
		}
	}
}
