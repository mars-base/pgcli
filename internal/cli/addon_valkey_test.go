package cli

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// resolveValkeyVersion mirrors resolveRedisVersion's precedence (--image beats
// --version, bare --version must name a table major, neither means the default)
// but reads the Valkey table: Valkey majors (7|8|9) are its own numbering and
// must never be looked up in the Redis table.

func TestResolveValkeyVersionDefault(t *testing.T) {
	rc := &config.RedisConfig{Name: "vcache"}
	if err := resolveValkeyVersion(rc, "", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantTag, _ := config.ValkeyImageTagForMajor(config.DefaultValkeyMajor)
	if rc.Version != config.DefaultValkeyMajor || rc.ImageTag != wantTag {
		t.Errorf("got version=%q tag=%q, want %q / %q", rc.Version, rc.ImageTag, config.DefaultValkeyMajor, wantTag)
	}
}

func TestResolveValkeyVersionExplicit(t *testing.T) {
	for _, major := range config.ValkeyMajors() {
		rc := &config.RedisConfig{Name: "vcache"}
		if err := resolveValkeyVersion(rc, major, ""); err != nil {
			t.Fatalf("major %q: unexpected error: %v", major, err)
		}
		wantTag, _ := config.ValkeyImageTagForMajor(major)
		if rc.Version != major || rc.ImageTag != wantTag {
			t.Errorf("major %q: got version=%q tag=%q, want %q / %q", major, rc.Version, rc.ImageTag, major, wantTag)
		}
	}
}

// 9 is a Valkey major but never a Redis one: the valkey resolver must accept
// it without consulting the Redis table.
func TestResolveValkeyVersionNineIsValkeyOnly(t *testing.T) {
	if _, ok := config.RedisImageTagForMajor("9"); ok {
		t.Fatal("fixture assumption broken: 9 is expected to be a valkey-only major")
	}
	rc := &config.RedisConfig{Name: "vcache"}
	if err := resolveValkeyVersion(rc, "9", ""); err != nil {
		t.Fatalf("valkey --version 9 must be accepted: %v", err)
	}
}

func TestResolveValkeyVersionInvalid(t *testing.T) {
	rc := &config.RedisConfig{Name: "vcache"}
	err := resolveValkeyVersion(rc, "6", "")
	if err == nil {
		t.Fatal("unknown --version must error")
	}
	if !strings.Contains(err.Error(), `"6"`) || !strings.Contains(err.Error(), "valkey") {
		t.Errorf("error should name the bad valkey version: %v", err)
	}
	for _, major := range config.ValkeyMajors() {
		if !strings.Contains(err.Error(), major) {
			t.Errorf("error missing available major %q: %v", major, err)
		}
	}
}

func TestResolveValkeyVersionImageBypass(t *testing.T) {
	rc := &config.RedisConfig{Name: "vcache"}
	if err := resolveValkeyVersion(rc, "", "registry.local/valkey:8.1.12"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.ImageTag != "registry.local/valkey:8.1.12" || rc.Version != "8" {
		t.Errorf("got version=%q tag=%q, want 8 / registry.local/valkey:8.1.12", rc.Version, rc.ImageTag)
	}

	rc2 := &config.RedisConfig{Name: "edge"}
	if err := resolveValkeyVersion(rc2, "", "registry.local/valkey:unstable"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc2.Version != "" {
		t.Errorf("version = %q, want empty for unparseable tag", rc2.Version)
	}
}

func TestResolveValkeyVersionStoredUnknown(t *testing.T) {
	rc := &config.RedisConfig{Name: "vcache", Version: "10"}
	err := resolveValkeyVersion(rc, "", "")
	if err == nil {
		t.Fatal("stored unknown version must error")
	}
	if !strings.Contains(err.Error(), "--image") || !strings.Contains(err.Error(), "valkey") {
		t.Errorf("error should suggest --image and name valkey: %v", err)
	}
}
