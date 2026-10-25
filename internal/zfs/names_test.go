package zfs

import (
	"strings"
	"testing"
)

func TestValidatePoolName(t *testing.T) {
	valid := []string{"tank", "Tank-1", "a.b_c", "z"}
	for _, name := range valid {
		if err := ValidatePoolName(name); err != nil {
			t.Errorf("ValidatePoolName(%q): %v", name, err)
		}
	}
	invalid := []string{"", "1tank", "tank/child", "tank name", "tank@snap", "-tank"}
	for _, name := range invalid {
		if err := ValidatePoolName(name); err == nil {
			t.Errorf("ValidatePoolName(%q) should fail", name)
		}
	}
}

func TestValidateDatasetName(t *testing.T) {
	valid := []string{"tank", "tank/data", "tank/data/postgres", "tank/data@snap", "tank/data@snap-2026.01"}
	for _, name := range valid {
		if err := ValidateDatasetName(name); err != nil {
			t.Errorf("ValidateDatasetName(%q): %v", name, err)
		}
	}
	invalid := []string{"", "1tank/data", "tank/", "tank//data", "tank/data@", "tank/data@a/b"}
	for _, name := range invalid {
		if err := ValidateDatasetName(name); err == nil {
			t.Errorf("ValidateDatasetName(%q) should fail", name)
		}
	}
}

func TestSplitSnapshot(t *testing.T) {
	dataset, snap, err := SplitSnapshot("tank/data@before-upgrade")
	if err != nil {
		t.Fatalf("SplitSnapshot: %v", err)
	}
	if dataset != "tank/data" || snap != "before-upgrade" {
		t.Errorf("got %q, %q", dataset, snap)
	}
	for _, bad := range []string{"tank/data", "@snap", "tank/data@", "tank/data@a/b"} {
		if _, _, err := SplitSnapshot(bad); err == nil {
			t.Errorf("SplitSnapshot(%q) should fail", bad)
		}
	}
}

func TestNormalizeDeviceList(t *testing.T) {
	got := NormalizeDeviceList([]string{"/dev/vdb, /dev/vdc", "", " /dev/vdd ", "/dev/vde,"})
	want := []string{"/dev/vdb", "/dev/vdc", "/dev/vdd", "/dev/vde"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("NormalizeDeviceList = %v, want %v", got, want)
	}
}
