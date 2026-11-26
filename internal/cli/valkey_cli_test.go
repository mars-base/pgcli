package cli

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func valkeyTargetCfg() *config.Config {
	cfg := config.Default()
	cfg.Addons.Valkey = map[string]config.ValkeyConfig{
		"cache":  {Name: "cache", ImageTag: "docker.io/valkey/valkey:9.2.0", Password: "pw-cache", Port: 6380},
		"legacy": {Name: "legacy", ImageTag: "docker.io/valkey/valkey:7.2.16", Password: "pw-legacy", Port: 6381},
	}
	return cfg
}

func TestResolveValkeyCLITargetLocalDefault(t *testing.T) {
	target, err := resolveValkeyCLITarget(valkeyTargetCfg(), "", "", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.addonName != "cache" || target.imageTag != "docker.io/valkey/valkey:9.2.0" ||
		target.password != "pw-cache" || target.host != "" || target.port != 6380 {
		t.Errorf("unexpected target: %+v", target)
	}
}

func TestResolveValkeyCLITargetLocalNamed(t *testing.T) {
	target, err := resolveValkeyCLITarget(valkeyTargetCfg(), "legacy", "", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.addonName != "legacy" || target.imageTag != "docker.io/valkey/valkey:7.2.16" ||
		target.password != "pw-legacy" || target.host != "" || target.port != 6381 {
		t.Errorf("unexpected target: %+v", target)
	}
}

func TestResolveValkeyCLITargetLocalPortOverride(t *testing.T) {
	target, err := resolveValkeyCLITarget(valkeyTargetCfg(), "cache", "", 7000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.host != "" || target.port != 7000 {
		t.Errorf("port override wrong: host=%q port=%d", target.host, target.port)
	}
}

func TestResolveValkeyCLITargetRemoteWithAddon(t *testing.T) {
	target, err := resolveValkeyCLITarget(valkeyTargetCfg(), "cache", "10.10.0.158", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.host != "10.10.0.158" || target.port != 6380 ||
		target.imageTag != "docker.io/valkey/valkey:9.2.0" || target.password != "pw-cache" {
		t.Errorf("unexpected remote+addon target: %+v", target)
	}
}

func TestResolveValkeyCLITargetRemoteNoName(t *testing.T) {
	target, err := resolveValkeyCLITarget(valkeyTargetCfg(), "", "10.10.0.158", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.addonName != "cache" || target.host != "10.10.0.158" || target.port != 6380 ||
		target.password != "pw-cache" {
		t.Errorf("unexpected remote-no-name target: %+v", target)
	}
}

// --host with NO local valkey addon: the default major's image, no stored
// password (auth via REDISCLI_AUTH / a forwarded -a), port falls back to
// Valkey's 6380 — even when a redis addon exists, whose map must not leak in.
func TestResolveValkeyCLITargetPureRemote(t *testing.T) {
	cfg := config.Default()
	cfg.Addons.Valkey = nil
	cfg.Addons.Redis = map[string]config.RedisConfig{
		"rcache": {Name: "rcache", ImageTag: "docker.io/library/redis:8.10.2", Password: "pw-redis", Port: 6379},
	}
	target, err := resolveValkeyCLITarget(cfg, "", "203.0.113.9", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantTag, _ := config.ValkeyImageTagForMajor(config.DefaultValkeyMajor)
	if target.imageTag != wantTag || target.password != "" || target.host != "203.0.113.9" ||
		target.port != config.DefaultValkeyPort || target.addonName != "" {
		t.Errorf("unexpected pure-remote target: %+v", target)
	}
}

func TestResolveValkeyCLITargetNoAddonNoHost(t *testing.T) {
	cfg := config.Default()
	cfg.Addons.Valkey = nil
	_, err := resolveValkeyCLITarget(cfg, "", "", 0)
	if err == nil {
		t.Fatal("must error with no addon and no --host")
	}
	if !strings.Contains(err.Error(), "--host") || !strings.Contains(err.Error(), "valkey") {
		t.Errorf("error should point at --host and name valkey: %v", err)
	}
}

func TestResolveValkeyCLITargetUnknownNamed(t *testing.T) {
	// A redis addon with the same name must not satisfy a valkey lookup.
	cfg := valkeyTargetCfg()
	cfg.Addons.Redis = map[string]config.RedisConfig{
		"ghost": {Name: "ghost", Port: 6379},
	}
	_, err := resolveValkeyCLITarget(cfg, "ghost", "", 0)
	if err == nil {
		t.Fatal("unknown --name must error even when a redis addon shares the name")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error: %v", err)
	}
}
