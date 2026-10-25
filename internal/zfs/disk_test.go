package zfs

import (
	"strings"
	"testing"
)

func TestParseZFSUsage(t *testing.T) {
	fixture := `  pool: tank
 state: ONLINE
config:

        NAME        STATE     READ WRITE CKSUM
        tank        ONLINE       0     0     0
          mirror-0  ONLINE       0     0     0
            /dev/vdb  ONLINE       0     0     0
            /dev/vdc  ONLINE       0     0     0

errors: No known data errors

  pool: backup
 state: ONLINE
config:
        NAME        STATE
        backup      ONLINE
          /dev/vdd  ONLINE
`
	usage := ParseZFSUsage([]byte(fixture))
	if pools := usage["/dev/vdb"]; len(pools) != 1 || pools[0] != "tank" {
		t.Errorf("/dev/vdb = %v", pools)
	}
	if pools := usage["/dev/vdd"]; len(pools) != 1 || pools[0] != "backup" {
		t.Errorf("/dev/vdd = %v", pools)
	}
	if _, ok := usage["mirror-0"]; ok {
		t.Error("vdev names must not be treated as devices")
	}
}

func TestInspectSafeDisk(t *testing.T) {
	check := inspectDevice(BlockDevice{Name: "vdb", Path: "/dev/vdb", Type: "disk"}, nil, nil)
	if !check.Safe {
		t.Errorf("expected safe, got %q", check.Reason)
	}
}

func TestInspectRejectsUnsafeDisks(t *testing.T) {
	cases := []struct {
		name     string
		device   BlockDevice
		children []BlockDevice
		usage    map[string][]string
		want     string
	}{
		{
			name:   "partition node",
			device: BlockDevice{Name: "vdb1", Path: "/dev/vdb1", Type: "part"},
			want:   "not a whole disk",
		},
		{
			name:   "whole disk with partitions",
			device: BlockDevice{Name: "vdb", Path: "/dev/vdb", Type: "disk"},
			children: []BlockDevice{
				{Name: "vdb1", Path: "/dev/vdb1", Type: "part", PKName: "vdb"},
			},
			want: "contains partitions",
		},
		{
			name:   "mounted",
			device: BlockDevice{Name: "vdb", Path: "/dev/vdb", Type: "disk", Mountpoints: []string{"/data"}},
			want:   "is mounted",
		},
		{
			name:   "filesystem",
			device: BlockDevice{Name: "vdb", Path: "/dev/vdb", Type: "disk", FSType: "ext4"},
			want:   "contains a filesystem",
		},
		{
			name:   "zfs label",
			device: BlockDevice{Name: "vdb", Path: "/dev/vdb", Type: "disk", FSType: "zfs_member"},
			want:   "labelclear",
		},
		{
			name:   "used by pool",
			device: BlockDevice{Name: "vdb", Path: "/dev/vdb", Type: "disk"},
			usage:  map[string][]string{"/dev/vdb": {"tank"}},
			want:   "in use by pool",
		},
		{
			// Pool members always have ZFS partitions; report the pool,
			// never suggest wiping the partition table.
			name:   "used by pool with partitions",
			device: BlockDevice{Name: "vdb", Path: "/dev/vdb", Type: "disk"},
			children: []BlockDevice{
				{Name: "vdb1", Path: "/dev/vdb1", Type: "part", PKName: "vdb"},
			},
			usage: map[string][]string{"/dev/vdb": {"tank"}},
			want:  "in use by pool",
		},
		{
			name:   "mounted with partitions",
			device: BlockDevice{Name: "vdb", Path: "/dev/vdb", Type: "disk", Mountpoints: []string{"/data"}},
			children: []BlockDevice{
				{Name: "vdb1", Path: "/dev/vdb1", Type: "part", PKName: "vdb"},
			},
			want: "is mounted",
		},
		{
			name:   "read only",
			device: BlockDevice{Name: "vdb", Path: "/dev/vdb", Type: "disk", ReadOnly: true},
			want:   "read-only",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check := inspectDevice(tc.device, tc.children, tc.usage)
			if check.Safe {
				t.Fatal("expected unsafe")
			}
			if !strings.Contains(check.Reason, tc.want) {
				t.Errorf("reason = %q, want %q", check.Reason, tc.want)
			}
		})
	}
}

func TestInspectRootDisk(t *testing.T) {
	devices := []BlockDevice{{Name: "vdb", Path: "/dev/vdb", Type: "disk"}}
	set := NewDeviceSet(devices)
	set.rootDisk = "vdb"
	check := set.Inspect(&set.Devices[0], nil)
	if check.Safe || !strings.Contains(check.Reason, "root filesystem") {
		t.Errorf("check = %+v", check)
	}
}

func TestInspectMountTable(t *testing.T) {
	devices := []BlockDevice{{Name: "vdb", Path: "/dev/vdb", Type: "disk"}}
	set := NewDeviceSet(devices)
	set.mountTable = map[string][]string{"/dev/vdb": {"/srv"}}
	check := set.Inspect(&set.Devices[0], nil)
	if check.Safe || !strings.Contains(check.Reason, "/srv") {
		t.Errorf("check = %+v", check)
	}
}

func inspectDevice(device BlockDevice, children []BlockDevice, usage map[string][]string) DeviceCheck {
	devices := append([]BlockDevice{device}, children...)
	set := NewDeviceSet(devices)
	return set.Inspect(&set.Devices[0], usage)
}
