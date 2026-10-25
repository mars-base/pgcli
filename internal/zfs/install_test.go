package zfs

import (
	"strings"
	"testing"
)

func TestZFSReleaseURL(t *testing.T) {
	cases := []struct {
		family  DistroFamily
		distTag string
		want    string
	}{
		{FamilyFedora, ".fc44", "https://zfsonlinux.org/fedora/zfs-release-3-1.fc44.noarch.rpm"},
		{FamilyRHEL, ".el10", "https://zfsonlinux.org/epel/zfs-release-3-0.el10.noarch.rpm"},
	}
	for _, tc := range cases {
		got, err := ZFSReleaseURL(tc.family, tc.distTag)
		if err != nil {
			t.Errorf("ZFSReleaseURL(%s, %q): %v", tc.family, tc.distTag, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ZFSReleaseURL(%s, %q) = %q, want %q", tc.family, tc.distTag, got, tc.want)
		}
	}

	invalid := []struct {
		family  DistroFamily
		distTag string
	}{
		{FamilyFedora, ".el10"},
		{FamilyRHEL, ".fc44"},
		{FamilyFedora, "fc44"},
		{FamilyFedora, ""},
		{FamilyDebian, ".fc44"},
		{FamilyUnknown, ".fc44"},
	}
	for _, tc := range invalid {
		if _, err := ZFSReleaseURL(tc.family, tc.distTag); err == nil {
			t.Errorf("ZFSReleaseURL(%s, %q) should fail", tc.family, tc.distTag)
		}
	}
}

func TestAPTPackages(t *testing.T) {
	debian := Distro{Family: FamilyDebian}
	got := strings.Join(aptPackages(debian, "6.1.0-45-amd64"), " ")
	want := "build-essential linux-headers-6.1.0-45-amd64 zfs-dkms zfsutils-linux zfs-zed"
	if got != want {
		t.Errorf("Debian packages = %q, want %q", got, want)
	}

	ubuntu := Distro{Family: FamilyUbuntu}
	got = strings.Join(aptPackages(ubuntu, "6.8.0-45-generic"), " ")
	if got != "zfsutils-linux zfs-zed" {
		t.Errorf("Ubuntu packages = %q", got)
	}
	if aptPackages(Distro{Family: FamilyFedora}, "6.9") != nil {
		t.Error("Fedora must not use apt packages")
	}
}

func TestHealth(t *testing.T) {
	health := Health{ZpoolPath: "/sbin/zpool", ZFSPath: "/sbin/zfs", ModuleLoaded: true}
	if !health.Installed() {
		t.Error("Installed should be true")
	}
	if !health.Ready() {
		t.Error("Ready should be true")
	}
	if health.VersionMismatch() {
		t.Error("empty versions must not mismatch")
	}

	health.UserlandVersion = "zfs-2.1.11-1"
	health.ModuleVersion = "2.1.11"
	if health.VersionMismatch() {
		t.Error("same versions must not mismatch")
	}
	health.ModuleVersion = "2.2.2"
	if !health.VersionMismatch() {
		t.Error("different versions must mismatch")
	}

	health.ModuleLoaded = false
	if health.Ready() {
		t.Error("not loaded must not be ready")
	}
	if (Health{}).Installed() {
		t.Error("empty health must not be installed")
	}
}

func TestModuleLoadConfigured(t *testing.T) {
	if !moduleLoadConfigured("zfs\n") {
		t.Error("plain line should count")
	}
	if !moduleLoadConfigured("# comment\n  zfs  \n") {
		t.Error("leading spaces should count")
	}
	if moduleLoadConfigured("# zfs\n") || moduleLoadConfigured("") {
		t.Error("comments and empty content must not count")
	}
}
