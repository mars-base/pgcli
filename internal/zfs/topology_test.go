package zfs

import (
	"strings"
	"testing"
)

func TestAutoType(t *testing.T) {
	cases := map[int]PoolType{
		0: TypeStripe, 1: TypeStripe, 2: TypeMirror, 3: TypeRaidz1, 4: TypeRaidz2, 8: TypeRaidz2,
	}
	for count, want := range cases {
		if got := AutoType(count); got != want {
			t.Errorf("AutoType(%d) = %s, want %s", count, got, want)
		}
	}
}

func TestParsePoolType(t *testing.T) {
	cases := map[string]PoolType{
		"stripe": TypeStripe, "raid0": TypeStripe,
		"mirror": TypeMirror, "raid1": TypeMirror,
		"raidz": TypeRaidz1, "raidz1": TypeRaidz1, "raid5": TypeRaidz1,
		"raidz2": TypeRaidz2, "raid2": TypeRaidz2, "raid6": TypeRaidz2,
		"raidz3": TypeRaidz3, "raid3": TypeRaidz3,
	}
	for input, want := range cases {
		got, err := ParsePoolType(input)
		if err != nil {
			t.Errorf("ParsePoolType(%q): %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("ParsePoolType(%q) = %s, want %s", input, got, want)
		}
	}
	if _, err := ParsePoolType("bogus"); err == nil {
		t.Error("ParsePoolType(bogus) should fail")
	}
}

func TestBuildTopologyAuto(t *testing.T) {
	devices := []string{"/dev/vdb", "/dev/vdc", "/dev/vdd", "/dev/vde", "/dev/vdf", "/dev/vdg", "/dev/vdh", "/dev/vdi"}
	cases := []struct {
		count int
		want  PoolType
	}{
		{1, TypeStripe}, {2, TypeMirror}, {3, TypeRaidz1}, {4, TypeRaidz2}, {5, TypeRaidz2}, {8, TypeRaidz2},
	}
	for _, tc := range cases {
		topo, _, err := BuildTopology(devices[:tc.count], BuildOptions{})
		if err != nil {
			t.Errorf("BuildTopology(%d): %v", tc.count, err)
			continue
		}
		if len(topo.Data) != 1 || topo.Data[0].Type != tc.want {
			t.Errorf("BuildTopology(%d) = %+v, want one %s vdev", tc.count, topo.Data, tc.want)
		}
	}
}

func TestBuildTopologyExplicit(t *testing.T) {
	topo, warnings, err := BuildTopology(
		[]string{"/dev/vdb", "/dev/vdc", "/dev/vdd", "/dev/vde"},
		BuildOptions{Type: TypeRaidz3, TypeExplicit: true},
	)
	if err != nil {
		t.Fatalf("BuildTopology: %v", err)
	}
	if topo.Data[0].Type != TypeRaidz3 {
		t.Errorf("type = %s", topo.Data[0].Type)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "recommended") {
		t.Errorf("expected recommended-device warning, got %v", warnings)
	}
}

func TestBuildTopologyMinimums(t *testing.T) {
	cases := []struct {
		poolType PoolType
		devices  []string
	}{
		{TypeMirror, []string{"/dev/vdb"}},
		{TypeRaidz2, []string{"/dev/vdb", "/dev/vdc"}},
		{TypeRaidz3, []string{"/dev/vdb", "/dev/vdc", "/dev/vdd"}},
	}
	for _, tc := range cases {
		_, _, err := BuildTopology(tc.devices, BuildOptions{Type: tc.poolType, TypeExplicit: true})
		if err == nil {
			t.Errorf("%s with %d devices should fail", tc.poolType, len(tc.devices))
		}
	}
}

func TestBuildTopologyMultipleDataGroups(t *testing.T) {
	topo, _, err := BuildTopology(nil, BuildOptions{
		Type:         TypeMirror,
		TypeExplicit: true,
		Data:         []string{"/dev/vdb,/dev/vdc", "/dev/vdd,/dev/vde"},
	})
	if err != nil {
		t.Fatalf("BuildTopology: %v", err)
	}
	if len(topo.Data) != 2 {
		t.Errorf("data vdevs = %d, want 2", len(topo.Data))
	}
}

func TestBuildTopologyRequiresTypeForDataAndSpecial(t *testing.T) {
	if _, _, err := BuildTopology(nil, BuildOptions{Data: []string{"/dev/vdb,/dev/vdc"}}); err == nil {
		t.Error("--data without --type should fail")
	}
	if _, _, err := BuildTopology(
		[]string{"/dev/vdb", "/dev/vdc"},
		BuildOptions{Special: []string{"/dev/vdd,/dev/vde"}},
	); err == nil {
		t.Error("--special without --type should fail")
	}
}

func TestBuildTopologyRejectsDuplicateAcrossClasses(t *testing.T) {
	_, _, err := BuildTopology(
		[]string{"/dev/vdb", "/dev/vdc"},
		BuildOptions{
			Type:         TypeMirror,
			TypeExplicit: true,
			Special:      []string{"/dev/vdc,/dev/vdd"},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestBuildTopologyLogLayout(t *testing.T) {
	topo, warnings, err := BuildTopology(
		[]string{"/dev/vdb", "/dev/vdc"},
		BuildOptions{Log: []string{"/dev/vdd"}},
	)
	if err != nil {
		t.Fatalf("BuildTopology: %v", err)
	}
	if len(topo.Log) != 1 || topo.Log[0].Type != TypeStripe {
		t.Errorf("single log = %+v", topo.Log)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "no redundancy") {
		t.Errorf("expected log warning, got %v", warnings)
	}

	topo, _, err = BuildTopology(
		[]string{"/dev/vdb", "/dev/vdc"},
		BuildOptions{Log: []string{"/dev/vdd,/dev/vde"}},
	)
	if err != nil {
		t.Fatalf("BuildTopology: %v", err)
	}
	if len(topo.Log) != 1 || topo.Log[0].Type != TypeMirror {
		t.Errorf("paired log = %+v", topo.Log)
	}
}

func TestCreateArgs(t *testing.T) {
	topo := Topology{
		Data:    []VDev{{Type: TypeMirror, Devices: []string{"/dev/vdb", "/dev/vdc"}}},
		Special: []VDev{{Type: TypeMirror, Devices: []string{"/dev/vdd", "/dev/vde"}}},
		Log:     []VDev{{Type: TypeMirror, Devices: []string{"/dev/vdf", "/dev/vdg"}}},
		Cache:   []string{"/dev/vdh"},
		Spare:   []string{"/dev/vdi"},
	}
	args := CreateArgs(
		"tank",
		topo,
		map[string]string{"autoexpand": "on", "ashift": "12"},
		map[string]string{"compression": "zstd", "atime": "off"},
	)
	want := strings.Join([]string{
		"create",
		"-o", "ashift=12", "-o", "autoexpand=on",
		"-O", "atime=off", "-O", "compression=zstd",
		"tank",
		"mirror", "/dev/vdb", "/dev/vdc",
		"special", "mirror", "/dev/vdd", "/dev/vde",
		"log", "mirror", "/dev/vdf", "/dev/vdg",
		"cache", "/dev/vdh",
		"spare", "/dev/vdi",
	}, " ")
	if got := strings.Join(args, " "); got != want {
		t.Errorf("CreateArgs =\n  %s\nwant\n  %s", got, want)
	}

	dry := WithDryRun(args)
	if len(dry) < 2 || dry[1] != "-n" || dry[0] != "create" {
		t.Errorf("WithDryRun = %v", dry)
	}
}

func TestBuildAddTopology(t *testing.T) {
	topo, _, err := BuildAddTopology([]string{"/dev/vdb", "/dev/vdc"}, ClassData, BuildOptions{})
	if err != nil || topo.Data[0].Type != TypeMirror {
		t.Fatalf("data add: %+v, %v", topo, err)
	}

	if _, _, err := BuildAddTopology([]string{"/dev/vdb", "/dev/vdc"}, ClassSpecial, BuildOptions{}); err == nil {
		t.Error("special without --type should fail")
	}
	topo, _, err = BuildAddTopology([]string{"/dev/vdb", "/dev/vdc"}, ClassLog, BuildOptions{})
	if err != nil || topo.Log[0].Type != TypeMirror {
		t.Fatalf("log add: %+v, %v", topo, err)
	}
	if _, _, err := BuildAddTopology([]string{"/dev/vdb"}, ClassCache, BuildOptions{Type: TypeMirror}); err == nil {
		t.Error("--type with cache should fail")
	}
	if _, _, err := BuildAddTopology([]string{"/dev/vdb"}, ClassCache, BuildOptions{Data: []string{"/dev/vdc"}}); err == nil {
		t.Error("--data with non-data class should fail")
	}

	addArgs, err := AddArgs("tank", topo, ClassLog)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(addArgs, " "); got != "add tank log mirror /dev/vdb /dev/vdc" {
		t.Errorf("AddArgs = %q", got)
	}
}
