package config

import (
	"path/filepath"
	"testing"
)

func TestGiteaDefaults(t *testing.T) {
	cfg := Default()
	cfg.Addons.Gitea = map[string]GiteaConfig{
		"git1": {},
	}

	cfg.ApplyDefaults()

	gc, ok := cfg.Addons.Gitea["git1"]
	if !ok {
		t.Fatal("gitea addon missing after ApplyDefaults")
	}
	if gc.Name != "git1" {
		t.Errorf("name = %q, want git1", gc.Name)
	}
	if gc.ContainerName != "pgcli-gitea-git1" {
		t.Errorf("container name = %q, want pgcli-gitea-git1", gc.ContainerName)
	}
	if gc.ImageTag != DefaultGiteaImageTag {
		t.Errorf("image tag = %q, want %q", gc.ImageTag, DefaultGiteaImageTag)
	}
	if gc.Listen != "0.0.0.0" {
		t.Errorf("listen = %q, want 0.0.0.0 (a git host is cloned from other machines; containers run on the host network)", gc.Listen)
	}
	// AdminUser defaults to "gitea", deliberately NOT "git": the container's
	// OS user git is what ssh clone authenticates AS; keeping the web admin
	// distinct avoids two different "git"s in every clone URL and error.
	if gc.AdminUser != "gitea" {
		t.Errorf("admin user = %q, want gitea", gc.AdminUser)
	}
	// Password is deliberately NOT defaulted: ApplyDefaults must stay
	// deterministic and a login password must never be a hardcoded literal.
	// The install handler generates one (same rule as Grafana).
	if gc.Password != "" {
		t.Errorf("password = %q, want empty (install-time generated)", gc.Password)
	}
	if gc.HostPort < cfg.GiteaStartPort {
		t.Errorf("port %d below base %d", gc.HostPort, cfg.GiteaStartPort)
	}
	// SSH defaults off (no *live* cloning), but an off instance still takes an
	// SSH-pool port: the image's s6 sshd always runs and must be parked off 22.
	if gc.SSH {
		t.Error("ssh = true, want false (live git-over-ssh off by default)")
	}
	if gc.SSHPort < cfg.GiteaSSHStartPort {
		t.Errorf("ssh port = %d, want >= base %d (SSH-off still parks the always-on sshd)", gc.SSHPort, cfg.GiteaSSHStartPort)
	}
	// Empty Dsn is the rendered configuration (embedded SQLite), not a gap.
	if gc.Dsn != "" {
		t.Errorf("dsn = %q, want empty (embedded SQLite default)", gc.Dsn)
	}
}

// Gitea owns TWO pools (gitea_start_port web, gitea_ssh_start_port OpenSSH).
// High unused bases keep the live-listener scan out of the exact expectations,
// same convention as the alertmanager test. The SSH pool now advances for EVERY
// instance — the image's s6 sshd always runs, so an SSH-off instance still needs
// a port to park it on — so with five instances sorted a..e the auto SSH ports
// are dense from the base (a=base, b=+1, c=+2, d=+3) until e's explicit port.
func TestGiteaPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.GiteaStartPort = 33080
	cfg.GiteaSSHStartPort = 32222
	cfg.Addons.Gitea = map[string]GiteaConfig{
		"b": {},
		"a": {HostPort: 33100},           // explicit web (above the base)
		"c": {SSH: true},                 // auto web + live ssh
		"d": {SSH: true},                 // auto web + live ssh
		"e": {SSH: true, SSHPort: 32230}, // explicit ssh (above the base), auto web
	}

	cfg.ApplyDefaults()

	if got := cfg.Addons.Gitea["a"]; got.HostPort != 33100 {
		t.Errorf("explicit web port changed: %d", got.HostPort)
	}
	// a's explicit 33100 bumps the shared cursor past it (processed first, by
	// sorted name), so b — the first auto instance — lands on 33101, and the
	// rest stay dense above it.
	if got := cfg.Addons.Gitea["b"]; got.HostPort != 33101 {
		t.Errorf("b web port = %d, want 33101 (cursor must skip the reserved port)", got.HostPort)
	}
	if got := cfg.Addons.Gitea["c"]; got.HostPort != 33102 {
		t.Errorf("c web port = %d, want 33102", got.HostPort)
	}
	if got := cfg.Addons.Gitea["d"]; got.HostPort != 33103 {
		t.Errorf("d web port = %d, want 33103", got.HostPort)
	}
	if got := cfg.Addons.Gitea["e"]; got.HostPort != 33104 {
		t.Errorf("e web port = %d, want 33104", got.HostPort)
	}

	// SSH pool: every instance takes one, dense from the base in name order
	// (a..d), regardless of the SSH flag; e keeps its explicit 32230. a/b have
	// SSH off but still get a park port for the always-running sshd.
	a := cfg.Addons.Gitea["a"]
	b := cfg.Addons.Gitea["b"]
	c := cfg.Addons.Gitea["c"]
	d := cfg.Addons.Gitea["d"]
	e := cfg.Addons.Gitea["e"]
	if a.SSHPort != 32222 {
		t.Errorf("a ssh port = %d, want 32222 (pool base; SSH-off still parks the sshd)", a.SSHPort)
	}
	if b.SSHPort != 32223 {
		t.Errorf("b ssh port = %d, want 32223 (next free after a, despite SSH off)", b.SSHPort)
	}
	if c.SSHPort != 32224 {
		t.Errorf("c ssh port = %d, want 32224 (next free after b)", c.SSHPort)
	}
	if d.SSHPort != 32225 {
		t.Errorf("d ssh port = %d, want 32225 (next free after c)", d.SSHPort)
	}
	if e.SSHPort != 32230 {
		t.Errorf("explicit ssh port changed: %d", e.SSHPort)
	}

	// The two Gitea pools must not collide with each other or with Grafana's
	// pool on the same host (bases 3080/2222/3000 are disjoint by design).
	cfg.Addons.Grafana = map[string]GrafanaConfig{"ui": {}}
	cfg.GrafanaStartPort = 33080
	cfg.ApplyDefaults()
	if g := cfg.Addons.Grafana["ui"]; g.HostPort == cfg.Addons.Gitea["e"].HostPort {
		t.Errorf("grafana port %d collided with gitea web port", g.HostPort)
	}
	if g := cfg.Addons.Gitea["e"]; g.SSHPort == g.HostPort {
		t.Errorf("an instance's own web and ssh ports collided: %d", g.HostPort)
	}
}

func TestGiteaSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")

	cfg := Default()
	cfg.Addons.Gitea = map[string]GiteaConfig{
		"git1": {
			ContainerName: "pgcli-gitea-git1",
			Name:          "git1",
			ImageTag:      DefaultGiteaImageTag,
			HostPort:      33080,
			Listen:        "127.0.0.1",
			AdminUser:     "ops",
			Password:      "web-login-pw",
			DataDir:       "/srv/gitea-data",
			SSH:           true,
			SSHPort:       32222,
			Dsn:           "postgres://gitea:pw@127.0.0.1:5432/gitea",
			Autostart:     true,
		},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	gc, ok := got.Addons.Gitea["git1"]
	if !ok {
		t.Fatal("gitea addon not persisted")
	}
	if gc.Listen != "127.0.0.1" {
		t.Errorf("listen not persisted: %q", gc.Listen)
	}
	if gc.HostPort != 33080 {
		t.Errorf("port not persisted: %d", gc.HostPort)
	}
	if gc.AdminUser != "ops" {
		t.Errorf("admin user not persisted: %q", gc.AdminUser)
	}
	if gc.Password != "web-login-pw" {
		t.Errorf("password not persisted: %q", gc.Password)
	}
	if gc.DataDir != "/srv/gitea-data" {
		t.Errorf("data dir not persisted: %q", gc.DataDir)
	}
	if !gc.SSH || gc.SSHPort != 32222 {
		t.Errorf("ssh not persisted: enabled=%v port=%d", gc.SSH, gc.SSHPort)
	}
	if gc.Dsn != "postgres://gitea:pw@127.0.0.1:5432/gitea" {
		t.Errorf("dsn not persisted: %q", gc.Dsn)
	}
	if !gc.Autostart {
		t.Error("autostart not persisted")
	}
	if gc.ImageTag != DefaultGiteaImageTag {
		t.Errorf("image tag not persisted: %q", gc.ImageTag)
	}

	// A second ApplyDefaults must not move either assigned port, rewrite the
	// admin user, touch the stored password, or flip SSH — every field is
	// already set.
	got.ApplyDefaults()
	again := got.Addons.Gitea["git1"]
	if again.HostPort != 33080 || again.SSHPort != 32222 || again.AdminUser != "ops" || again.Password != "web-login-pw" {
		t.Errorf("ApplyDefaults mutated stored values: web=%d ssh=%d admin=%q", again.HostPort, again.SSHPort, again.AdminUser)
	}
	if again.Listen != "127.0.0.1" {
		t.Errorf("ApplyDefaults overrode explicit listen: %q", again.Listen)
	}
	if again.DataDir != "/srv/gitea-data" {
		t.Errorf("ApplyDefaults dropped explicit data dir: %q", again.DataDir)
	}
	if !again.SSH || !again.Autostart {
		t.Errorf("ApplyDefaults dropped flags: ssh=%v autostart=%v", again.SSH, again.Autostart)
	}
	if again.Dsn != "postgres://gitea:pw@127.0.0.1:5432/gitea" {
		t.Errorf("ApplyDefaults dropped explicit dsn: %q", again.Dsn)
	}
}
