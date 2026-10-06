package cli

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// resolveRedisVersion implements the plan's precedence: --image beats
// --version, --version must name a table major, and neither means
// DefaultRedisMajor.

func TestResolveRedisVersionDefault(t *testing.T) {
	rc := &config.RedisConfig{Name: "cache"}
	if err := resolveRedisVersion(rc, "", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantTag, _ := config.RedisImageTagForMajor(config.DefaultRedisMajor)
	if rc.Version != config.DefaultRedisMajor || rc.ImageTag != wantTag {
		t.Errorf("got version=%q tag=%q, want %q / %q", rc.Version, rc.ImageTag, config.DefaultRedisMajor, wantTag)
	}
}

func TestResolveRedisVersionExplicit(t *testing.T) {
	rc := &config.RedisConfig{Name: "legacy"}
	if err := resolveRedisVersion(rc, "7", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantTag, _ := config.RedisImageTagForMajor("7")
	if rc.Version != "7" || rc.ImageTag != wantTag {
		t.Errorf("got version=%q tag=%q, want 7 / %q", rc.Version, rc.ImageTag, wantTag)
	}
}

func TestResolveRedisVersionInvalid(t *testing.T) {
	rc := &config.RedisConfig{Name: "cache"}
	err := resolveRedisVersion(rc, "6", "")
	if err == nil {
		t.Fatal("unknown --version must error")
	}
	if !strings.Contains(err.Error(), `"6"`) {
		t.Errorf("error should name the bad version: %v", err)
	}
	// The message lists the valid majors so the operator can fix it inline.
	for _, major := range config.RedisMajors() {
		if !strings.Contains(err.Error(), major) {
			t.Errorf("error missing available major %q: %v", major, err)
		}
	}
}

func TestResolveRedisVersionImageBypass(t *testing.T) {
	// --image wins and reverse-parses the major for display...
	rc := &config.RedisConfig{Name: "cache"}
	if err := resolveRedisVersion(rc, "", "registry.local/redis:7.2.4"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.ImageTag != "registry.local/redis:7.2.4" || rc.Version != "7" {
		t.Errorf("got version=%q tag=%q, want 7 / registry.local/redis:7.2.4", rc.Version, rc.ImageTag)
	}

	// ...and an unparseable tag leaves Version empty (tag shown verbatim).
	rc2 := &config.RedisConfig{Name: "edge"}
	if err := resolveRedisVersion(rc2, "", "registry.local/redis:edge"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc2.Version != "" {
		t.Errorf("version = %q, want empty for unparseable tag", rc2.Version)
	}

	// --version + --image together: the image still decides the tag.
	rc3 := &config.RedisConfig{Name: "both"}
	if err := resolveRedisVersion(rc3, "8", "registry.local/redis:7.2.4"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc3.ImageTag != "registry.local/redis:7.2.4" {
		t.Errorf("--image must outrank --version, tag = %q", rc3.ImageTag)
	}
}

// A stored entry with a hand-edited unknown Version and no --image must error
// with a message suggesting --image, not silently pull a wrong tag.
func TestResolveRedisVersionStoredUnknown(t *testing.T) {
	rc := &config.RedisConfig{Name: "cache", Version: "9"}
	err := resolveRedisVersion(rc, "", "")
	if err == nil {
		t.Fatal("stored unknown version must error")
	}
	if !strings.Contains(err.Error(), "--image") {
		t.Errorf("error should suggest --image: %v", err)
	}
}
