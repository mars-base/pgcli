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

// validateRedisKnobs runs on the merged config, so the requirements fire on
// stored values too (a hand-edited policy without a cap is caught at the next
// install, before anything is pulled).

func TestValidateRedisKnobs(t *testing.T) {
	cases := []struct {
		name    string
		rc      config.RedisConfig
		wantErr string // substring; empty means must pass
	}{
		{"empty knobs pass", config.RedisConfig{Name: "cache"}, ""},
		{"policy without cap", config.RedisConfig{Name: "c", MaxMemoryPolicy: "noeviction"}, "--maxmemory-policy requires --maxmemory"},
		{"policy with cap", config.RedisConfig{Name: "c", MaxMemory: "1gb", MaxMemoryPolicy: "noeviction"}, ""},
		{"unknown policy", config.RedisConfig{Name: "c", MaxMemory: "1gb", MaxMemoryPolicy: "allkeys-fifo"}, "unknown --maxmemory-policy"},
		{"fsync without aof", config.RedisConfig{Name: "c", AppendFsync: "always"}, "--appendfsync requires --aof"},
		{"fsync with aof", config.RedisConfig{Name: "c", AOF: true, AppendFsync: "always"}, ""},
		{"unknown fsync", config.RedisConfig{Name: "c", AOF: true, AppendFsync: "sometimes"}, "unknown --appendfsync"},
		{"save no", config.RedisConfig{Name: "c", SaveSchedule: "no"}, ""},
		{"save pairs", config.RedisConfig{Name: "c", SaveSchedule: "900 1 300 10"}, ""},
		{"save odd fields", config.RedisConfig{Name: "c", SaveSchedule: "900 1 300"}, "malformed --save"},
		{"save non-integer", config.RedisConfig{Name: "c", SaveSchedule: "900 one"}, "not a non-negative integer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRedisKnobs(tc.rc)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestRedisPersistenceSummary(t *testing.T) {
	cases := []struct {
		rc   config.RedisConfig
		want string
	}{
		{config.RedisConfig{}, "rdb snapshots (default save schedule)"},
		{config.RedisConfig{SaveSchedule: "900 1"}, "rdb snapshots (save 900 1)"},
		{config.RedisConfig{AOF: true}, "rdb + aof (default save schedule, appendonly yes)"},
		{config.RedisConfig{AOF: true, AppendFsync: "always"}, "rdb + aof (default save schedule, appendonly yes, appendfsync always)"},
		{config.RedisConfig{AOF: true, SaveSchedule: "no"}, "aof only (appendonly yes, snapshots off)"},
		{config.RedisConfig{AOF: true, SaveSchedule: "no", AppendFsync: "everysec"}, "aof only (appendonly yes, appendfsync everysec, snapshots off)"},
		{config.RedisConfig{SaveSchedule: "no"}, "none (snapshots off, no aof)"},
	}
	for _, tc := range cases {
		if got := redisPersistenceSummary(tc.rc); got != tc.want {
			t.Errorf("redisPersistenceSummary(%+v) = %q, want %q", tc.rc, got, tc.want)
		}
	}
}
