package podman

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// TestGiteaArgsAndEnv pins the `podman run` argv for Gitea across both
// networking shapes and the SSH/DSN toggles. Gitea is env-only config plus one
// bind mount, so this function is the whole contract between a GiteaConfig and
// the container.
func TestGiteaArgsAndEnv(t *testing.T) {
	base := func() *config.GiteaConfig {
		return &config.GiteaConfig{
			ContainerName: "pgcli-gitea-git1",
			Name:          "git1",
			HostPort:      38080,
			// Every instance now gets a pool port to park the image's
			// always-running sshd on, even SSH-off — reflect that here.
			SSHPort:   32222,
			Listen:    "0.0.0.0",
			AdminUser: "gitea",
			Password:  "web-login-pw",
			ImageTag:  config.DefaultGiteaImageTag,
		}
	}

	hasEnv := func(args []string, kv string) bool {
		return slices.Contains(args, "-e") && slices.Contains(args, kv)
	}
	// hasMount reports whether some `-v <spec>` entry contains substr.
	hasMount := func(args []string, substr string) bool {
		for i, a := range args {
			if a == "-v" && i+1 < len(args) && strings.Contains(args[i+1], substr) {
				return true
			}
		}
		return false
	}
	// hasFlag reports whether flag appears immediately followed by value.
	hasFlag := func(args []string, flag, value string) bool {
		for i, a := range args {
			if a == flag && i+1 < len(args) && args[i+1] == value {
				return true
			}
		}
		return false
	}
	// count returns how many args equal s.
	count := func(args []string, s string) int {
		n := 0
		for _, a := range args {
			if a == s {
				n++
			}
		}
		return n
	}

	t.Run("linux host networking, defaults (ssh off, sqlite)", func(t *testing.T) {
		gc := base()
		args := giteaArgsAndEnv(gc, false, "pgcli-net", "/home/u/.pgcli/addon/gitea/git1/data")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "host") {
			t.Errorf("expected --network host, got %v", args)
		}
		if slices.Contains(args, "-p") {
			t.Errorf("host networking must not publish ports, got %v", args)
		}
		// The web listener is pinned to the assigned host port — without it
		// every instance defaults to 3000 and host networking collides.
		if !hasEnv(args, "GITEA__server__HTTP_PORT=38080") {
			t.Errorf("GITEA__server__HTTP_PORT=38080 missing: %v", args)
		}
		// Non-loopback default bind passes verbatim through on Linux.
		if !hasEnv(args, "GITEA__server__HTTP_ADDR=0.0.0.0") {
			t.Errorf("GITEA__server__HTTP_ADDR=0.0.0.0 missing: %v", args)
		}
		// The wildcard bind must not leak into displayed clone URLs.
		if !hasEnv(args, "GITEA__server__ROOT_URL=http://localhost:38080/") {
			t.Errorf("ROOT_URL should collapse the wildcard to localhost: %v", args)
		}
		// The wizard skip and the closed self-registration gate.
		if !hasEnv(args, "GITEA__security__INSTALL_LOCK=true") {
			t.Errorf("INSTALL_LOCK missing — the web wizard would block a headless install: %v", args)
		}
		if !hasEnv(args, "GITEA__service__DISABLE_REGISTRATION=true") {
			t.Errorf("self-registration should be closed: %v", args)
		}
		// The ownership contract with the host-side pre-chown: the image's
		// su-exec target must be pinned to the uid the data dir gets chowned
		// to, and there must be NO --user flag — forcing uid 0 would run the
		// daemon as root and write root-owned files into /data (the pgAdmin
		// trick is as wrong here as it is for Grafana).
		if !hasEnv(args, "USER_UID=1000") || !hasEnv(args, "USER_GID=1000") {
			t.Errorf("USER_UID/USER_GID must pin the git uid to 1000: %v", args)
		}
		if slices.Contains(args, "--user") {
			t.Errorf("Gitea must not pass --user (the image su-execs to git/1000): %v", args)
		}
		// The crypto-materials rule: pgcli never injects secrets that would
		// re-key a surviving store on reinstall (see GiteaManager doc).
		for _, a := range args {
			if strings.Contains(a, "SECRET_KEY") || strings.Contains(a, "INTERNAL_TOKEN") {
				t.Errorf("crypto secrets must never be injected, found %q", a)
			}
		}
		// SSH off: Gitea's own SSH layer is disabled, so no clone URLs and no
		// authorized_keys. But the image's s6 sshd always runs, so the BARE
		// SSH_LISTEN_PORT/SSH_PORT (the keys it actually binds, not the
		// GITEA__server__ copy) must park it off 22.
		if !hasEnv(args, "GITEA__server__DISABLE_SSH=true") {
			t.Errorf("DISABLE_SSH=true missing: %v", args)
		}
		for _, a := range args {
			if strings.HasPrefix(a, "GITEA__server__START_SSH_LISTENER") || strings.HasPrefix(a, "GITEA__server__SSH_") {
				t.Errorf("ssh-disabled instance must not carry GITEA__server__SSH_ env, found %q", a)
			}
		}
		if !hasEnv(args, "SSH_LISTEN_PORT=32222") || !hasEnv(args, "SSH_PORT=32222") {
			t.Errorf("ssh-off instance must still park the always-on sshd off 22 via bare SSH_LISTEN_PORT/SSH_PORT: %v", args)
		}
		// No DSN: no database env at all (embedded SQLite is the render).
		for _, a := range args {
			if strings.HasPrefix(a, "GITEA__database__") {
				t.Errorf("dsn-less instance must not carry database env, found %q", a)
			}
		}
		if !slices.Contains(args, "--http-proxy=false") {
			t.Errorf("--http-proxy=false missing: %v", args)
		}
		if !hasFlag(args, "--restart", "unless-stopped") {
			t.Errorf("--restart unless-stopped missing: %v", args)
		}
		if !hasMount(args, "/home/u/.pgcli/addon/gitea/git1/data:/data:z") {
			t.Errorf("data dir mount missing: %v", args)
		}
		if args[len(args)-1] != config.DefaultGiteaImageTag {
			t.Errorf("image = %q, want last arg %q", args[len(args)-1], config.DefaultGiteaImageTag)
		}
	})

	t.Run("macOS bridge networking publishes port and keeps a non-loopback bind", func(t *testing.T) {
		gc := base()
		gc.Listen = "127.0.0.1" // even a loopback listen is widened under bridge
		args := giteaArgsAndEnv(gc, true, "pgcli-net", "/data")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "pgcli-net") {
			t.Errorf("expected --network pgcli-net, got %v", args)
		}
		if !slices.Contains(args, "-p") || !slices.Contains(args, "38080:38080") {
			t.Errorf("bridge must publish 38080:38080, got %v", args)
		}
		// The published port can't reach a loopback-only bind.
		if !hasEnv(args, "GITEA__server__HTTP_ADDR=0.0.0.0") {
			t.Errorf("bridge should widen loopback to 0.0.0.0, got %v", args)
		}
		if hasEnv(args, "GITEA__server__HTTP_ADDR=127.0.0.1") {
			t.Errorf("bridge must not keep the loopback bind: %v", args)
		}
	})

	t.Run("explicit non-loopback listen passes through", func(t *testing.T) {
		gc := base()
		gc.Listen = "10.0.0.5"
		args := giteaArgsAndEnv(gc, true, "pgcli-net", "/data")
		if !hasEnv(args, "GITEA__server__HTTP_ADDR=10.0.0.5") {
			t.Errorf("explicit bind should pass through under bridge: %v", args)
		}
		// The explicit host is also what clone URLs must show.
		if !hasEnv(args, "GITEA__server__ROOT_URL=http://10.0.0.5:38080/") {
			t.Errorf("ROOT_URL should use the explicit listen host: %v", args)
		}
	})

	t.Run("ssh enabled adds the listener env and a second published port under bridge", func(t *testing.T) {
		gc := base()
		gc.SSH = true
		gc.SSHPort = 32222
		args := giteaArgsAndEnv(gc, true, "pgcli-net", "/data")

		if hasEnv(args, "GITEA__server__DISABLE_SSH=true") {
			t.Errorf("ssh-enabled instance must not disable ssh (DISABLE_SSH=false gates authorized_keys): %v", args)
		}
		if !hasEnv(args, "GITEA__server__DISABLE_SSH=false") {
			t.Errorf("DISABLE_SSH=false missing — Gitea must write the git user's authorized_keys: %v", args)
		}
		// git-over-ssh rides the image's s6 sshd (bare SSH_LISTEN_PORT/SSH_PORT
		// below), so Gitea's OWN built-in Go listener is pinned off — under host
		// networking both would bind the same port in the same netns.
		if !hasEnv(args, "GITEA__server__START_SSH_LISTENER=false") {
			t.Errorf("START_SSH_LISTENER=false missing (avoid double-bind): %v", args)
		}
		if hasEnv(args, "GITEA__server__START_SSH_LISTENER=true") {
			t.Errorf("built-in listener must stay off, image sshd carries ssh: %v", args)
		}
		// The BARE env is what the always-on sshd actually binds; the
		// GITEA__server__SSH_* copy drives clone URLs. Same pool number in both,
		// or sshd would listen somewhere the clone URLs don't point.
		if !hasEnv(args, "SSH_LISTEN_PORT=32222") || !hasEnv(args, "SSH_PORT=32222") {
			t.Errorf("bare sshd park env missing or mismatched: %v", args)
		}
		if !hasEnv(args, "GITEA__server__SSH_LISTEN_PORT=32222") || !hasEnv(args, "GITEA__server__SSH_PORT=32222") {
			t.Errorf("clone-URL ssh port env missing or mismatched: %v", args)
		}
		if !hasEnv(args, "GITEA__server__SSH_DOMAIN=localhost") {
			t.Errorf("SSH_DOMAIN should mirror the display host: %v", args)
		}
		// netFlags only knows the web port; the SSH block publishes its own.
		if count(args, "-p") != 2 || !slices.Contains(args, "32222:32222") {
			t.Errorf("bridge+ssh must publish both 38080 and 32222, got %v", args)
		}

		// Under host networking the listener binds the host port directly —
		// no publish flags at all.
		args = giteaArgsAndEnv(gc, false, "pgcli-net", "/data")
		if slices.Contains(args, "-p") {
			t.Errorf("host networking must not publish the ssh port either, got %v", args)
		}
		if !hasEnv(args, "GITEA__server__SSH_LISTEN_PORT=32222") {
			t.Errorf("ssh env must survive on the host network too: %v", args)
		}
	})

	t.Run("dsn renders the postgres database env", func(t *testing.T) {
		gc := base()
		gc.Dsn = "postgres://gitea:pw@db.example.com:5433/gitea_db"
		args := giteaArgsAndEnv(gc, false, "pgcli-net", "/data")

		if !hasEnv(args, "GITEA__database__DB_TYPE=postgres") {
			t.Errorf("DB_TYPE=postgres missing: %v", args)
		}
		if !hasEnv(args, "GITEA__database__HOST=db.example.com:5433") {
			t.Errorf("HOST missing (net.JoinHostPort shape): %v", args)
		}
		if !hasEnv(args, "GITEA__database__NAME=gitea_db") {
			t.Errorf("NAME missing: %v", args)
		}
		if !hasEnv(args, "GITEA__database__USER=gitea") || !hasEnv(args, "GITEA__database__PASSWD=pw") {
			t.Errorf("USER/PASSWD missing: %v", args)
		}
	})
}

// TestGiteaDisplayHost pins the clone-URL host collapse: wildcard binds are not
// connectable addresses and must echo as localhost, everything else verbatim.
func TestGiteaDisplayHost(t *testing.T) {
	for _, w := range []string{"", "0.0.0.0", "::"} {
		if got := GiteaDisplayHost(w); got != "localhost" {
			t.Errorf("GiteaDisplayHost(%q) = %q, want localhost", w, got)
		}
	}
	if got := GiteaDisplayHost("10.0.0.5"); got != "10.0.0.5" {
		t.Errorf("GiteaDisplayHost(10.0.0.5) = %q, want it verbatim", got)
	}
}

// TestGiteaStoreExists pins the revive check: the SQLite store lives one level
// below the data dir root (/data/gitea/gitea.db), and only its presence marks
// a revival — the NOTE path in install (the authoritative admin gate is the
// post-boot AdminExists exec, which also covers --dsn mode).
func TestGiteaStoreExists(t *testing.T) {
	dir := t.TempDir()
	if GiteaStoreExists(dir) {
		t.Fatal("empty dir should not count as an existing store")
	}
	// A stray file at the root (e.g. an ssh key dir skeleton) is not a store.
	if err := os.Mkdir(filepath.Join(dir, "lfs"), 0755); err != nil {
		t.Fatal(err)
	}
	if GiteaStoreExists(dir) {
		t.Fatal("unrelated subdir should not count as an existing store")
	}
	if err := os.Mkdir(filepath.Join(dir, "gitea"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Create(filepath.Join(dir, "gitea", "gitea.db")); err != nil {
		t.Fatal(err)
	}
	if !GiteaStoreExists(dir) {
		t.Fatal("dir holding gitea/gitea.db should count as an existing store")
	}
	if GiteaStoreExists("") {
		t.Fatal("empty path is never an existing store")
	}
}

// hasFlagVal reports whether flag appears immediately followed by value.
func hasFlagVal(args []string, flag, value string) bool {
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == value {
			return true
		}
	}
	return false
}

// TestGiteaUserCreateArgs pins the argv the `gitea admin user create` wrapper
// builds — the crux being that must-change-password is ALWAYS sent explicitly.
// Its Gitea default is true for every account after the first, and a forced
// change also blocks HTTP clone/push and API basic-auth with 403 until the user
// changes it; an omitted flag is exactly the trap, so a default-false member
// must emit --must-change-password=false, never nothing.
func TestGiteaUserCreateArgs(t *testing.T) {
	member := giteaUserCreateArgs(GiteaUserCreateSpec{
		Username: "alice", Email: "alice@corp", Password: "pw123",
		MustChangePassword: false,
	})
	if member[0] != "admin" || member[1] != "user" || member[2] != "create" {
		t.Fatalf("args must lead with admin user create, got %v", member[:3])
	}
	for _, kv := range [][2]string{
		{"--username", "alice"}, {"--email", "alice@corp"}, {"--password", "pw123"},
		{"--must-change-password=false", ""},
	} {
		if kv[1] == "" {
			if !slices.Contains(member, kv[0]) {
				t.Errorf("member args missing %s: %v", kv[0], member)
			}
		} else if !hasFlagVal(member, kv[0], kv[1]) {
			t.Errorf("member args missing %s %s: %v", kv[0], kv[1], member)
		}
	}
	if slices.Contains(member, "--admin") {
		t.Errorf("a member must not carry --admin: %v", member)
	}

	admin := giteaUserCreateArgs(GiteaUserCreateSpec{
		Username: "bob", Email: "bob@corp", Password: "pw",
		Admin: true, MustChangePassword: true,
	})
	if !slices.Contains(admin, "--admin") {
		t.Errorf("admin args missing --admin: %v", admin)
	}
	if !slices.Contains(admin, "--must-change-password=true") {
		t.Errorf("must-change=true args missing explicit true: %v", admin)
	}

	// Empty email is omitted entirely (the CLI layer fills the default; the
	// manager must not pass an empty --email that Gitea would reject).
	noEmail := giteaUserCreateArgs(GiteaUserCreateSpec{Username: "u", Password: "p"})
	if slices.Contains(noEmail, "--email") {
		t.Errorf("empty email must be omitted, not passed as --email \"\": %v", noEmail)
	}
}

// TestGiteaUserDeleteArgs pins the delete argv and the --purge toggle.
func TestGiteaUserDeleteArgs(t *testing.T) {
	plain := giteaUserDeleteArgs("alice", false)
	if !hasFlagVal(plain, "--username", "alice") {
		t.Fatalf("delete args missing --username alice: %v", plain)
	}
	if slices.Contains(plain, "--purge") {
		t.Errorf("plain delete must not purge: %v", plain)
	}
	purged := giteaUserDeleteArgs("alice", true)
	if !slices.Contains(purged, "--purge") {
		t.Errorf("purge delete missing --purge: %v", purged)
	}
}

// TestGiteaUserInList pins the table match behind UserExists: a username is
// found as a whitespace-delimited field, not a loose substring, and the empty
// username never matches (Fields of a header line could otherwise contain "").
func TestGiteaUserInList(t *testing.T) {
	const table = `ID   Username Email             IsActive IsAdmin 2FA
1    gitea    gitea@localhost   true     true    false
3    alice    alice@example.com true     false   false
7    gitea-bot giteabot@x        true     false   false`
	if !giteaUserInList(table, "alice") {
		t.Error("alice should be found")
	}
	if !giteaUserInList(table, "gitea-bot") {
		t.Error("gitea-bot should be found")
	}
	if giteaUserInList(table, "gitea-backup") {
		t.Error("gitea-backup must NOT match the gitea/gitea-bot rows as a substring")
	}
	if giteaUserInList(table, "") {
		t.Error("empty username must never match")
	}
}

// TestGiteaErr pins the failure-message collapse: Gitea's CLI floods combined
// output with storage-init INFO lines and ends with the real reason as
// `Command error: …` (or `Fatal: …`). The wrapper must surface just that line,
// and never swallow a failure that carries neither marker (fall back to err).
func TestGiteaErr(t *testing.T) {
	base := errors.New("podman exec … : exit status 1")
	ownErr := errors.New("own failure")

	noisy := "2026-10-16 storage init INFO one\n2026-10-16 storage init INFO two\nCommand error: user still has ownership of repositories [uid: 4]"
	if got := giteaErr(noisy, base); got.Error() != "user still has ownership of repositories [uid: 4]" {
		t.Errorf("Command error line not extracted cleanly: %q", got)
	}
	fatal := "some INFO\nFatal: bad flag"
	if got := giteaErr(fatal, base); got.Error() != "bad flag" {
		t.Errorf("Fatal line not extracted: %q", got)
	}
	// No Gitea marker → fall back to the original error (don't lose podman
	// failures like a missing container).
	if got := giteaErr("random noise with no marker", ownErr); got != ownErr {
		t.Errorf("no-marker case must return the original err, got %q", got)
	}
	if giteaErr("anything", nil) != nil {
		t.Error("a nil error must stay nil")
	}
}
