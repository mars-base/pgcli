package zfs

import (
	"strings"
	"testing"
)

func TestParseProps(t *testing.T) {
	props, err := ParseProps([]string{"ashift=12", "compression = zstd", "xattr=sa"})
	if err != nil {
		t.Fatalf("ParseProps: %v", err)
	}
	if props["ashift"] != "12" || props["compression"] != "zstd" || props["xattr"] != "sa" {
		t.Errorf("props = %v", props)
	}
	for _, bad := range []string{"ashift", "=12", "a b=12"} {
		if _, err := ParseProps([]string{bad}); err == nil {
			t.Errorf("ParseProps(%q) should fail", bad)
		}
	}
}

func TestMergeProps(t *testing.T) {
	defaults := map[string]string{"ashift": "12", "autoexpand": "on"}
	user := map[string]string{"ashift": "13"}
	merged := MergeProps(defaults, user, false)
	if merged["ashift"] != "13" || merged["autoexpand"] != "on" {
		t.Errorf("merged = %v", merged)
	}
	only := MergeProps(defaults, user, true)
	if len(only) != 1 || only["ashift"] != "13" {
		t.Errorf("no-defaults merged = %v", only)
	}
	// Defaults must not be mutated.
	if defaults["ashift"] != "12" {
		t.Errorf("defaults mutated: %v", defaults)
	}
}

func TestSortedProps(t *testing.T) {
	got := SortedProps(map[string]string{"z": "1", "a": "2", "m": "3"})
	if strings.Join(got, ",") != "a,m,z" {
		t.Errorf("SortedProps = %v", got)
	}
}

func TestAutotrimEligible(t *testing.T) {
	devices := []BlockDevice{
		{Name: "vdb", Path: "/dev/vdb", DiscardGran: 4096},
		{Name: "vdc", Path: "/dev/vdc", DiscardMax: 1 << 30},
		{Name: "vdd", Path: "/dev/vdd", Rotational: true, DiscardGran: 4096},
	}
	if !AutotrimEligible(devices, []string{"/dev/vdb", "/dev/vdc"}) {
		t.Error("expected eligible for non-rotational discard devices")
	}
	if AutotrimEligible(devices, []string{"/dev/vdb", "/dev/vdd"}) {
		t.Error("rotational device must disable autotrim")
	}
	if AutotrimEligible(devices, []string{"/dev/vdb", "/dev/vde"}) {
		t.Error("unknown device must disable autotrim")
	}
	if AutotrimEligible(devices, nil) {
		t.Error("no devices must not be eligible")
	}
}
