package zfs

import (
	"strings"
	"testing"
)

func TestParsePoolList(t *testing.T) {
	out := "tank\t1073741824\t268435456\t805306368\tONLINE\t-\n" +
		"backup\t2147483648\t1024\t2147482624\tDEGRADED\t/mnt/alt\n"
	pools, err := ParsePoolList([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 2 {
		t.Fatalf("pools = %d", len(pools))
	}
	if pools[0].Name != "tank" || pools[0].Size != 1073741824 || pools[0].Free != 805306368 {
		t.Errorf("pool[0] = %+v", pools[0])
	}
	if pools[0].AltRoot != "" {
		t.Errorf("altroot = %q, want empty", pools[0].AltRoot)
	}
	if pools[1].AltRoot != "/mnt/alt" || pools[1].Health != "DEGRADED" {
		t.Errorf("pool[1] = %+v", pools[1])
	}
}

func TestParsePoolListRejectsShortRows(t *testing.T) {
	if _, err := ParsePoolList([]byte("tank\t1\t2\n")); err == nil {
		t.Error("expected error")
	}
}

func TestParseDatasetList(t *testing.T) {
	out := "tank\t24576\t1073741824\t24576\t/tank\n" +
		"tank/data\t1024\t1073740800\t1024\t/tank/data\n"
	rows, err := ParseDatasetList([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1].Name != "tank/data" || rows[1].Mountpoint != "/tank/data" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestParseSnapshotList(t *testing.T) {
	out := "tank/data@snap1\t0\t1024\t1700000000\n"
	rows, err := ParseSnapshotList([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Creation != 1700000000 || rows[0].Refer != 1024 {
		t.Errorf("rows = %+v", rows)
	}
	if _, err := ParseSnapshotList([]byte("tank/data@snap1\t0\t1024\tbad\n")); err == nil {
		t.Error("expected error for bad creation")
	}
}

func TestDatasetArgs(t *testing.T) {
	create := DatasetCreateArgs("tank/data", true, map[string]string{"quota": "100G", "compression": "lz4"})
	want := "create -p -o compression=lz4 -o quota=100G tank/data"
	if got := strings.Join(create, " "); got != want {
		t.Errorf("DatasetCreateArgs = %q", got)
	}

	set, err := DatasetSetArgs("tank/data", map[string]string{"quota": "100G", "recordsize": "128k"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(set, " "); got != "set quota=100G recordsize=128k tank/data" {
		t.Errorf("DatasetSetArgs = %q", got)
	}
	if _, err := DatasetSetArgs("tank/data", nil); err == nil {
		t.Error("empty set should fail")
	}

	clone := DatasetCloneArgs("tank/data@snap", "tank/copy", map[string]string{"mountpoint": "/tank/copy"})
	if got := strings.Join(clone, " "); got != "clone -o mountpoint=/tank/copy tank/data@snap tank/copy" {
		t.Errorf("DatasetCloneArgs = %q", got)
	}

	if got := strings.Join(DatasetDestroyArgs("tank/data", true), " "); got != "destroy -r tank/data" {
		t.Errorf("DatasetDestroyArgs = %q", got)
	}
}

func TestSnapshotArgs(t *testing.T) {
	if got := strings.Join(SnapshotCreateArgs("tank/data@snap", true), " "); got != "snapshot -r tank/data@snap" {
		t.Errorf("SnapshotCreateArgs = %q", got)
	}
	if got := strings.Join(SnapshotRollbackArgs("tank/data@snap", false), " "); got != "rollback tank/data@snap" {
		t.Errorf("SnapshotRollbackArgs = %q", got)
	}
	if got := strings.Join(SnapshotDestroyArgs("tank/data@snap", true), " "); got != "destroy -r tank/data@snap" {
		t.Errorf("SnapshotDestroyArgs = %q", got)
	}
}

func TestPoolCreateDryRunArgs(t *testing.T) {
	topo := Topology{Data: []VDev{{Type: TypeMirror, Devices: []string{"/dev/vdb", "/dev/vdc"}}}}
	args := PoolCreateDryRunArgs("tank", topo, map[string]string{"ashift": "12"}, nil)
	if got := strings.Join(args, " "); got != "create -n -o ashift=12 tank mirror /dev/vdb /dev/vdc" {
		t.Errorf("PoolCreateDryRunArgs = %q", got)
	}
}
