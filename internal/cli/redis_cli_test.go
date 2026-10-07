package cli

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func redisTargetCfg() *config.Config {
	cfg := config.Default()
	cfg.Addons.Redis = map[string]config.RedisConfig{
		"cache": {Name: "cache", ImageTag: "docker.io/library/redis:8.10.2", Password: "pw-cache", Port: 6379},
		"legacy": {Name: "legacy", ImageTag: "docker.io/library/redis:7.4.11", Password: "pw-legacy", Port: 6380},
	}
	return cfg
}

func TestResolveRedisCLITargetLocalDefault(t *testing.T) {
	// No --host, no --name: the first addon by sorted name, local host (empty
	// host = manager default), its stored port.
	target, err := resolveRedisCLITarget(redisTargetCfg(), "", "", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.addonName != "cache" || target.imageTag != "docker.io/library/redis:8.10.2" ||
		target.password != "pw-cache" || target.host != "" || target.port != 6379 {
		t.Errorf("unexpected target: %+v", target)
	}
}

func TestResolveRedisCLITargetLocalNamed(t *testing.T) {
	target, err := resolveRedisCLITarget(redisTargetCfg(), "legacy", "", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.addonName != "legacy" || target.imageTag != "docker.io/library/redis:7.4.11" ||
		target.password != "pw-legacy" || target.host != "" || target.port != 6380 {
		t.Errorf("unexpected target: %+v", target)
	}
}

func TestResolveRedisCLITargetLocalPortOverride(t *testing.T) {
	// --port without --host overrides the port on the local (empty-host) target.
	target, err := resolveRedisCLITarget(redisTargetCfg(), "cache", "", 7000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.host != "" || target.port != 7000 {
		t.Errorf("port override wrong: host=%q port=%d", target.host, target.port)
	}
}

func TestResolveRedisCLITargetRemoteWithAddon(t *testing.T) {
	// --host + a named local addon: host overridden, image/password reused,
	// port defaults to the addon's when --port omitted.
	target, err := resolveRedisCLITarget(redisTargetCfg(), "cache", "10.10.0.158", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.host != "10.10.0.158" || target.port != 6379 || target.imageTag != "docker.io/library/redis:8.10.2" ||
		target.password != "pw-cache" {
		t.Errorf("unexpected remote+addon target: %+v", target)
	}
}

func TestResolveRedisCLITargetRemoteWithAddonPortOverride(t *testing.T) {
	target, err := resolveRedisCLITarget(redisTargetCfg(), "cache", "10.10.0.158", 6390)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.host != "10.10.0.158" || target.port != 6390 {
		t.Errorf("port override ignored: %+v", target)
	}
}

func TestResolveRedisCLITargetRemoteNoName(t *testing.T) {
	// --host with no --name but addons present: borrow the first addon's
	// image/password, override the host.
	target, err := resolveRedisCLITarget(redisTargetCfg(), "", "10.10.0.158", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.addonName != "cache" || target.host != "10.10.0.158" || target.port != 6379 ||
		target.password != "pw-cache" {
		t.Errorf("unexpected remote-no-name target: %+v", target)
	}
}

func TestResolveRedisCLITargetPureRemote(t *testing.T) {
	// --host with NO local addon: the default major's image, no stored password
	// (auth must come from REDISCLI_AUTH / a forwarded -a), port falls back to
	// Redis's standard 6379.
	cfg := config.Default()
	cfg.Addons.Redis = nil
	target, err := resolveRedisCLITarget(cfg, "", "203.0.113.9", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantTag, _ := config.RedisImageTagForMajor(config.DefaultRedisMajor)
	if target.imageTag != wantTag || target.password != "" || target.host != "203.0.113.9" ||
		target.port != config.DefaultRedisPort || target.addonName != "" {
		t.Errorf("unexpected pure-remote target: %+v", target)
	}
}

func TestResolveRedisCLITargetPureRemotePort(t *testing.T) {
	cfg := config.Default()
	cfg.Addons.Redis = nil
	target, err := resolveRedisCLITarget(cfg, "", "203.0.113.9", 6400)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.port != 6400 {
		t.Errorf("explicit remote port dropped: %+v", target)
	}
}

func TestResolveRedisCLITargetNoAddonNoHost(t *testing.T) {
	cfg := config.Default()
	cfg.Addons.Redis = nil
	_, err := resolveRedisCLITarget(cfg, "", "", 0)
	if err == nil {
		t.Fatal("must error with no addon and no --host")
	}
	if !strings.Contains(err.Error(), "--host") {
		t.Errorf("error should point at --host as the remote escape: %v", err)
	}
}

func TestResolveRedisCLITargetUnknownNamed(t *testing.T) {
	_, err := resolveRedisCLITarget(redisTargetCfg(), "ghost", "", 0)
	if err == nil {
		t.Fatal("unknown --name must error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveRedisCLITargetUnknownNamedWithHost(t *testing.T) {
	// An explicit --name that does not exist is an error even alongside --host.
	_, err := resolveRedisCLITarget(redisTargetCfg(), "ghost", "10.10.0.158", 0)
	if err == nil {
		t.Fatal("unknown --name must error even with --host")
	}
}
