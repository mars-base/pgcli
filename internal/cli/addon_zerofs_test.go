package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// TestRenderZerofsConfig pins the golden shape of the pgcli-rendered
// config.toml: field names, order, quoting, and which keys appear per
// conditional-put mode. ZeroFS's config surface changed across releases —
// this test is the contract with the v2.3.5 binary we pin.
func TestRenderZerofsConfig(t *testing.T) {
	base := &config.ZerofsConfig{
		Name:               "z1",
		Bucket:             "zerofs-z1",
		EncryptionPassword: "enc-pw",
		AccessKey:          "ak",
		SecretKey:          "sk",
		CacheDir:           "/base/addon/zerofs/z1/cache",
		ServerCacheGB:      100,
		Listen:             "127.0.0.1",
		ListenPort:         5564,
		ConditionalPut:     config.ZerofsConditionalPutAuto,
	}
	tgt := &s3Target{BaseURL: "http://127.0.0.1:9000"}

	got := renderZerofsConfig(base, tgt)
	want := strings.Join([]string{
		"# pgcli-managed ZeroFS server config — do not edit by hand.",
		"# Regenerate with: pg addon install zerofs --name z1 --force",
		"",
		"[cache]",
		`dir = "/base/addon/zerofs/z1/cache"`,
		"disk_size_gb = 100.0",
		"",
		"[storage]",
		`url = "s3://zerofs-z1"`,
		`encryption_password = "enc-pw"`,
		"",
		"[aws]",
		`access_key_id = "ak"`,
		`secret_access_key = "sk"`,
		`endpoint = "http://127.0.0.1:9000"`,
		`allow_http = "true"`,
		"",
		"[servers.ninep]",
		`addresses = ["127.0.0.1:5564"]`,
		"",
	}, "\n")
	if got != want {
		t.Errorf("render mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	redisMode := *base
	redisMode.ConditionalPut = config.ZerofsConditionalPutRedis
	redisMode.ConditionalPutURL = "redis://:pw@127.0.0.1:6379"
	gotRedis := renderZerofsConfig(&redisMode, tgt)
	if !strings.Contains(gotRedis, `conditional_put = "redis://:pw@127.0.0.1:6379"`) {
		t.Errorf("redis mode missing conditional_put:\n%s", gotRedis)
	}

	none := *base
	none.ConditionalPut = config.ZerofsConditionalPutNone
	gotNone := renderZerofsConfig(&none, tgt)
	if strings.Contains(gotNone, "conditional_put") {
		t.Errorf("none mode must omit conditional_put:\n%s", gotNone)
	}

	// An explicit region is rendered into [aws]; the default (empty) config
	// above must NOT carry a region line — zerofs picks us-east-1 itself.
	region := *base
	region.S3Region = "us-west-2"
	gotRegion := renderZerofsConfig(&region, tgt)
	if !strings.Contains(gotRegion, `region = "us-west-2"`) {
		t.Errorf("region not rendered:\n%s", gotRegion)
	}
	if strings.Contains(got, `region =`) {
		t.Errorf("empty region must omit the key:\n%s", got)
	}

	tlsTgt := &s3Target{BaseURL: "https://127.0.0.1:9002", TLS: true}
	gotTLS := renderZerofsConfig(base, tlsTgt)
	if strings.Contains(gotTLS, "allow_http") {
		t.Errorf("TLS endpoint must not set allow_http:\n%s", gotTLS)
	}
	if !strings.Contains(gotTLS, `endpoint = "https://127.0.0.1:9002"`) {
		t.Errorf("TLS endpoint wrong:\n%s", gotTLS)
	}
}

func TestEnsureZerofsBindFree(t *testing.T) {
	// A port nothing listens on is free.
	closed := closedLoopbackPort(t)
	if err := ensureZerofsBindFree("127.0.0.1", closed, "/nonexistent/pid"); err != nil {
		t.Errorf("closed port reported busy: %v", err)
	}
	// A port that answers is refused, with actionable hints for both cases:
	// no pid file ⇒ some foreign listener, a LIVE pid file ⇒ this entry's own
	// stale server still holds the port.
	busy := startProbeListener(t)
	err := ensureZerofsBindFree("127.0.0.1", busy, "/nonexistent/pid")
	if err == nil || !strings.Contains(err.Error(), "already answering") {
		t.Fatalf("busy port accepted: %v", err)
	}
	if !strings.Contains(err.Error(), "--port") {
		t.Errorf("missing pick-a-port hint: %v", err)
	}

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "server.pid")
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sleep: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	// The pid file's first line is the pid, second the binary path — Alive
	// matches the process's argv[0] base against that path.
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n%s\n", cmd.Process.Pid, "/bin/sleep")), 0o600); err != nil {
		t.Fatal(err)
	}
	err = ensureZerofsBindFree("127.0.0.1", busy, pidFile)
	if err == nil || !strings.Contains(err.Error(), "this zerofs entry already has a live server") {
		t.Errorf("stale-server hint wrong: %v", err)
	}
}

func TestIsLoopbackBind(t *testing.T) {
	for _, ok := range []string{"127.0.0.1", "127.5.5.5", "localhost", "::1"} {
		if !isLoopbackBind(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"0.0.0.0", "::", "10.0.0.5", ""} {
		if isLoopbackBind(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
