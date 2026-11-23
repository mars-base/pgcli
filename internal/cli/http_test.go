package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/mars-base/pgcli/internal/config"
)

func TestHTTPAllowDeleteFlag(t *testing.T) {
	start := httpStartCmd.Flags().Lookup("allow-delete")
	if start == nil || start.DefValue != "false" {
		t.Fatalf("start: --allow-delete = %+v, want a flag defaulting to false", start)
	}
	serve := httpServeCmd.Flags().Lookup("allow-delete")
	if serve == nil || serve.DefValue != "false" {
		t.Fatalf("serve: --allow-delete = %+v, want a flag defaulting to false", serve)
	}
}

func TestHTTPAllowUploadDirFlags(t *testing.T) {
	for _, name := range []string{"allow-upload", "allow-dir"} {
		start := httpStartCmd.Flags().Lookup(name)
		if start == nil || start.DefValue != "false" {
			t.Errorf("start: --%s = %+v, want a flag defaulting to false", name, start)
		}
		serve := httpServeCmd.Flags().Lookup(name)
		if serve == nil || serve.DefValue != "false" {
			t.Errorf("serve: --%s = %+v, want a flag defaulting to false", name, serve)
		}
	}
	if f := httpStartCmd.Flags().Lookup("max-upload"); f == nil || f.DefValue != "1G" {
		t.Errorf("start: --max-upload = %+v, want default 1G", f)
	}
	if f := httpServeCmd.Flags().Lookup("max-upload"); f == nil || f.DefValue != "1G" {
		t.Errorf("serve: --max-upload = %+v, want default 1G", f)
	}
	if f := httpStartCmd.Flags().Lookup("admin-ip"); f == nil || f.DefValue != "" {
		t.Errorf("start: --admin-ip = %+v, want an empty default (no restriction)", f)
	}
	if f := httpServeCmd.Flags().Lookup("admin-ip"); f == nil || f.DefValue != "" {
		t.Errorf("serve: --admin-ip = %+v, want an empty default", f)
	}
}

func TestParseAdminIPs(t *testing.T) {
	if ips, err := parseAdminIPs(""); err != nil || len(ips) != 0 {
		t.Errorf("empty value = %v, %v; want no restriction", ips, err)
	}
	ips, err := parseAdminIPs(" 10.0.0.5 , 2001:db8::7 ,::ffff:10.0.0.5,10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.5", "2001:db8::7"}
	if got := adminIPStrings(ips); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("parsed = %v, want %v (trimmed, unmapped, deduped)", got, want)
	}
	for _, bad := range []string{"nope", "10.0.0.5,", ",10.0.0.5", "10.0.0.5, bad", "10.0.0.0/24"} {
		if _, err := parseAdminIPs(bad); err == nil {
			t.Errorf("parseAdminIPs(%q) accepted an invalid list", bad)
		}
	}
}

func TestFormatUploadLimit(t *testing.T) {
	for in, want := range map[int64]string{
		1 << 30:   "1GiB",
		512 << 20: "512MiB",
		12345:     "12345B",
	} {
		if got := formatUploadLimit(in); got != want {
			t.Errorf("formatUploadLimit(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestHTTPConfigAllowDeleteRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")
	c := config.Default()
	c.HTTP = map[string]config.HTTPServerConfig{
		"docs": {Dir: "/srv/docs", AllowDelete: true, AllowUpload: true, AllowDir: true, MaxUploadBytes: 1 << 30, AdminIPs: []string{"10.0.0.5", "2001:db8::7"}, Auth: map[string]string{"alice": "$2a$10$abcdefghijklmnopqrstuv"}, Autostart: true},
		"ro":   {Dir: "/srv/ro"},
	}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"allow_delete: true", "allow_upload: true", "allow_dir: true", "max_upload_bytes: 1073741824", "admin_ips:", "- 10.0.0.5", "- 2001:db8::7", "auth:", "alice: $2a$10$abcdefghijklmnopqrstuv"} {
		if !strings.Contains(string(data), key) {
			t.Errorf("%s not persisted:\n%s", key, data)
		}
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HTTP["docs"].AllowDelete || !got.HTTP["docs"].AllowUpload || !got.HTTP["docs"].AllowDir {
		t.Error("docs write modes lost on load")
	}
	if ips := got.HTTP["docs"].AdminIPs; len(ips) != 2 || ips[0] != "10.0.0.5" || ips[1] != "2001:db8::7" {
		t.Errorf("docs admin IPs = %v, want the two recorded addresses", ips)
	}
	if auth := got.HTTP["docs"].Auth; len(auth) != 1 || auth["alice"] != "$2a$10$abcdefghijklmnopqrstuv" {
		t.Errorf("docs auth = %v, want the recorded hash", auth)
	}
	if got.HTTP["ro"].AllowDelete || got.HTTP["ro"].AllowUpload || got.HTTP["ro"].AllowDir || got.HTTP["ro"].MaxUploadBytes != 0 || len(got.HTTP["ro"].AdminIPs) != 0 || len(got.HTTP["ro"].Auth) != 0 {
		t.Error("ro write modes should stay false")
	}
}

func TestHTTPListenLoopback(t *testing.T) {
	for _, listen := range []string{"", "localhost", "127.0.0.1", "::1", "[::1]"} {
		if !httpListenLoopback(listen) {
			t.Errorf("%q should count as loopback", listen)
		}
	}
	for _, listen := range []string{"0.0.0.0", "::", "10.0.0.5", "example.test"} {
		if httpListenLoopback(listen) {
			t.Errorf("%q should not count as loopback", listen)
		}
	}
}

func TestValidHTTPName(t *testing.T) {
	for _, name := range []string{"docs", "http-18080", "a.b_c-d"} {
		if !validHTTPName(name) {
			t.Errorf("%q should be valid", name)
		}
	}
	for _, name := range []string{"", ".", "..", "a/b", "a b", "../x"} {
		if validHTTPName(name) {
			t.Errorf("%q should be invalid", name)
		}
	}
}

func TestEnsureHTTPCertReuseAndRenewal(t *testing.T) {
	dir := t.TempDir()
	hosts := []string{"localhost", "127.0.0.1"}

	certPath, keyPath, notAfter, err := ensureHTTPCert(dir, hosts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !notAfter.After(time.Now().Add(30 * 24 * time.Hour)) {
		t.Errorf("generated cert expires too soon: %s", notAfter)
	}
	first, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if keyPath == "" || certPath == "" {
		t.Fatal("empty cert paths")
	}

	certPath2, _, _, err := ensureHTTPCert(dir, hosts)
	if err != nil {
		t.Fatalf("reuse: %v", err)
	}
	second, _ := os.ReadFile(certPath2)
	if string(first) != string(second) {
		t.Error("valid certificate was regenerated instead of reused")
	}

	// A new SAN forces a re-sign.
	if _, _, _, err := ensureHTTPCert(dir, append(hosts, "example.test")); err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	third, _ := os.ReadFile(certPath)
	if string(first) == string(third) {
		t.Error("certificate was not regenerated when a host was added")
	}
}

func TestHTTPStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := httpServerState{
		Name:           "docs",
		PID:            os.Getpid(),
		Port:           18080,
		Listen:         "127.0.0.1",
		Dir:            "/srv/share",
		TLS:            true,
		CertFile:       "/tmp/cert.pem",
		KeyFile:        "/tmp/key.pem",
		AllowDelete:    true,
		AllowUpload:    true,
		AllowDir:       true,
		MaxUploadBytes: 2 << 30,
		AdminIPs:       []string{"10.0.0.5", "2001:db8::7"},
		Auth:           map[string]string{"alice": "$2a$10$abcdefghijklmnopqrstuv"},
		LogFile:        "/tmp/http.log",
		StartedAt:      time.Unix(1700000000, 0).UTC(),
	}
	if err := writeHTTPState(dir, st); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "docs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("state file mode = %o, want 600 (it holds bcrypt hashes)", perm)
	}
	got, err := readHTTPState(filepath.Join(dir, "docs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != st.Name || got.Port != st.Port || !got.TLS || got.Dir != st.Dir ||
		!got.AllowDelete || !got.AllowUpload || !got.AllowDir || got.MaxUploadBytes != st.MaxUploadBytes {
		t.Errorf("round-trip = %+v, want %+v", got, st)
	}
	if ips := got.AdminIPs; len(ips) != 2 || ips[0] != "10.0.0.5" || ips[1] != "2001:db8::7" {
		t.Errorf("admin IPs round-trip = %v", ips)
	}
	if auth := got.Auth; len(auth) != 1 || auth["alice"] != "$2a$10$abcdefghijklmnopqrstuv" {
		t.Errorf("auth round-trip = %v", auth)
	}

	ro := httpServerState{Name: "ro", PID: 1, LogFile: "/tmp/ro.log"}
	if err := writeHTTPState(dir, ro); err != nil {
		t.Fatal(err)
	}
	if _, err := readHTTPState(filepath.Join(dir, "ro.json")); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "ro.json"))
	for _, key := range []string{"allow_delete", "allow_upload", "allow_dir", "max_upload_bytes", "admin_ips", `"auth"`} {
		if strings.Contains(string(raw), key) {
			t.Errorf("%s should be omitted when off: %s", key, raw)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tmp.json.tmp"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	states := loadHTTPStates(dir)
	if len(states) != 2 {
		t.Errorf("loadHTTPStates = %+v, want the two valid entries", states)
	}
}

func TestHTTPURL(t *testing.T) {
	st := &httpServerState{Name: "s", Port: 18081, Listen: "127.0.0.1"}
	if got := httpURL(st); got != "http://127.0.0.1:18081/" {
		t.Errorf("plain URL = %q", got)
	}
	st.TLS = true
	st.Listen = "0.0.0.0"
	if got := httpURL(st); got != "https://0.0.0.0:18081/" {
		t.Errorf("TLS URL = %q", got)
	}
}

func TestSelectHTTPTargets(t *testing.T) {
	states := []httpServerState{{Name: "a"}, {Name: "b"}}

	if got := selectHTTPTargets(states, nil, false); len(got) != 2 {
		t.Errorf("no names = %+v, want every server", got)
	}
	if got := selectHTTPTargets(states, nil, true); len(got) != 2 {
		t.Errorf("--all = %+v, want every server", got)
	}
	got := selectHTTPTargets(states, []string{"b", "missing"}, false)
	if len(got) != 1 || got[0].Name != "b" {
		t.Errorf("named = %+v, want only b", got)
	}
}

func TestHTTPAutostartCounting(t *testing.T) {
	c := &config.Config{}
	if hasAutostartHTTP(c) || countAutostartTargets(c) != 0 {
		t.Fatal("empty config should have no autostart targets")
	}
	c.HTTP = map[string]config.HTTPServerConfig{
		"docs":  {Dir: "/srv/docs", Autostart: true},
		"other": {Dir: "/srv/other"},
	}
	if !hasAutostartHTTP(c) {
		t.Error("hasAutostartHTTP = false, want true")
	}
	if n := countAutostartTargets(c); n != 1 {
		t.Errorf("countAutostartTargets = %d, want 1", n)
	}
}

func TestHTTPAutostartNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")
	data := "http:\n  docs:\n    dir: /srv/docs\n    autostart: true\n  other:\n    dir: /srv/other\n"
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	old := cfgPath
	cfgPath = path
	defer func() { cfgPath = old }()

	autos := httpAutostartNames()
	if len(autos) != 1 || !autos["docs"] {
		t.Errorf("httpAutostartNames = %v, want only docs", autos)
	}
}

func TestHTTPCertHostsWildcardCoverLoopback(t *testing.T) {
	hosts := httpCertHosts("0.0.0.0")
	joined := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		joined[h] = true
	}
	for _, want := range []string{"localhost", "127.0.0.1", "::1"} {
		if !joined[want] {
			t.Errorf("wildcard SANs missing %s: %v", want, hosts)
		}
	}
	specific := httpCertHosts("10.0.0.5")
	found := false
	for _, h := range specific {
		if h == "10.0.0.5" {
			found = true
		}
	}
	if !found {
		t.Errorf("specific listen address missing from SANs: %v", specific)
	}
}

func TestHTTPAuthFlags(t *testing.T) {
	for _, name := range []string{"auth", "auth-env", "auth-file"} {
		if f := httpStartCmd.Flags().Lookup(name); f == nil || f.DefValue != "[]" {
			t.Errorf("start: --%s = %+v, want an empty string array", name, f)
		}
	}
	if f := httpServeCmd.Flags().Lookup("auth-hash"); f == nil || f.DefValue != "[]" {
		t.Errorf("serve: --auth-hash = %+v, want an empty string array", f)
	}
	if f := httpHtpasswdCmd.Flags().Lookup("file"); f == nil || f.DefValue != "" {
		t.Errorf("htpasswd: --file = %+v, want an empty default (base dir)", f)
	}
}

func TestParseAuthEntries(t *testing.T) {
	hashes, err := parseAuthEntries([]string{"alice:s3:cret", "bob:hunter2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hashes) != 2 {
		t.Fatalf("parsed %d users, want 2", len(hashes))
	}
	for user, pass := range map[string]string{"alice": "s3:cret", "bob": "hunter2"} {
		if err := bcrypt.CompareHashAndPassword([]byte(hashes[user]), []byte(pass)); err != nil {
			t.Errorf("%s: hash does not match %q: %v (the password may contain ':')", user, pass, err)
		}
		if strings.Contains(hashes[user], pass) {
			t.Errorf("%s: hash %q contains the plain password", user, hashes[user])
		}
	}

	for _, bad := range [][]string{
		{"alice"},
		{"alice:"},
		{":pw"},
		{"alice:a", "alice:b"},
		{"alice:" + strings.Repeat("x", 73)},
	} {
		if _, err := parseAuthEntries(bad); err == nil {
			t.Errorf("parseAuthEntries(%q) accepted an invalid entry", bad)
		}
	}
	if _, err := parseAuthEntries([]string{"alice:" + strings.Repeat("x", 73)}); err == nil || !strings.Contains(err.Error(), "72") {
		t.Errorf("long password error = %v, want the 72-byte bcrypt limit", err)
	}
}

func TestParseAuthEnv(t *testing.T) {
	t.Setenv("PGCLI_TEST_AUTH", "carol:pw")
	hashes, err := parseAuthEnv([]string{"PGCLI_TEST_AUTH"})
	if err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hashes["carol"]), []byte("pw")); err != nil {
		t.Errorf("carol hash does not match: %v", err)
	}

	if _, err := parseAuthEnv([]string{"PGCLI_TEST_AUTH_UNSET"}); err == nil {
		t.Error("unset variable was accepted")
	}
	t.Setenv("PGCLI_TEST_AUTH_EMPTY", "  ")
	if _, err := parseAuthEnv([]string{"PGCLI_TEST_AUTH_EMPTY"}); err == nil {
		t.Error("blank variable was accepted")
	}
}

func TestMergeAuthRejectsDuplicateUsers(t *testing.T) {
	if _, err := mergeAuth(map[string]string{"alice": "$2a$x"}, map[string]string{"alice": "$2a$y"}); err == nil {
		t.Error("duplicate user across --auth and --auth-env was accepted")
	}
	if _, err := mergeAuth(map[string]string{"alice": "$2a$x"}, nil, map[string]string{"alice": "$2a$y"}); err == nil {
		t.Error("duplicate user across --auth and --auth-file was accepted")
	}
	merged, err := mergeAuth(map[string]string{"alice": "$2a$x"}, map[string]string{"bob": "$2a$y"})
	if err != nil || len(merged) != 2 {
		t.Errorf("mergeAuth = %v, %v; want both users", merged, err)
	}
	if merged, err := mergeAuth(nil, nil); err != nil || merged != nil {
		t.Errorf("mergeAuth(nil, nil) = %v, %v; want no auth", merged, err)
	}
}

func TestParseAuthFile(t *testing.T) {
	hashA, err := bcrypt.GenerateFromPassword([]byte("a-pass"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := bcrypt.GenerateFromPassword([]byte("b-pass"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	path := filepath.Join(dir, "htpasswd")
	content := "# managed by hand\n\nalice:" + string(hashA) + "\r\nbob:" + string(hashB) + "\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	users, err := parseAuthFile([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("parsed %d users, want 2", len(users))
	}
	for user, pass := range map[string]string{"alice": "a-pass", "bob": "b-pass"} {
		if err := bcrypt.CompareHashAndPassword([]byte(users[user]), []byte(pass)); err != nil {
			t.Errorf("%s: stored hash does not match: %v", user, err)
		}
	}

	// $2y$ entries, as written by Apache's htpasswd -B, are accepted.
	yPath := filepath.Join(dir, "apache")
	yHash := strings.Replace(string(hashA), "$2a$", "$2y$", 1)
	if err := os.WriteFile(yPath, []byte("carol:"+yHash+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	yUsers, err := parseAuthFile([]string{yPath})
	if err != nil {
		t.Fatalf("$2y$ entry rejected: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(yUsers["carol"]), []byte("a-pass")); err != nil {
		t.Errorf("$2y$ entry does not verify: %v", err)
	}

	for name, body := range map[string]string{
		"nocolon":   "alice\n",
		"emptyuser": ":" + string(hashA) + "\n",
		"apr1":      "dave:$apr1$abcdefgh$0123456789abcdefghijkl\n",
		"sha":       "dave:{SHA}qUqP5cyxm6YcTAhz05Hph5gvu9M=\n",
		"dup":       "alice:" + string(hashA) + "\nalice:" + string(hashB) + "\n",
		"onlycmts":  "# nobody here\n\n",
	} {
		badPath := filepath.Join(dir, name)
		if err := os.WriteFile(badPath, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := parseAuthFile([]string{badPath}); err == nil {
			t.Errorf("%s: accepted an invalid htpasswd file", name)
		}
	}
	if _, err := parseAuthFile([]string{filepath.Join(dir, "missing")}); err == nil {
		t.Error("missing file was accepted")
	}
	if _, err := parseAuthFile([]string{path, yPath}); err != nil {
		t.Errorf("multi-file parse with distinct users failed: %v", err)
	}
	dupPath := filepath.Join(dir, "dupfiles")
	if err := os.WriteFile(dupPath, []byte("alice:"+string(hashB)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseAuthFile([]string{path, dupPath}); err == nil {
		t.Error("duplicate user across two files was accepted")
	}
}

func TestHtpasswdUpsert(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "htpasswd")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# team share\n"), 0600); err != nil {
		t.Fatal(err)
	}
	hashA, err := bcrypt.GenerateFromPassword([]byte("a-pass"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := bcrypt.GenerateFromPassword([]byte("b-pass"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}

	if replaced, err := upsertHtpasswd(path, "alice", string(hashA)); err != nil || replaced {
		t.Fatalf("add alice = replaced %v, %v; want a new entry", replaced, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("mode = %o, want 600", perm)
	}
	users, err := parseAuthFile([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users["alice"] != string(hashA) {
		t.Errorf("after add = %v, want only alice", users)
	}

	if _, err := upsertHtpasswd(path, "bob", string(hashA)); err != nil {
		t.Fatal(err)
	}
	if replaced, err := upsertHtpasswd(path, "alice", string(hashB)); err != nil || !replaced {
		t.Fatalf("update alice = replaced %v, %v; want a replacement", replaced, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "# team share") {
		t.Errorf("comment lost:\n%s", raw)
	}
	users, err = parseAuthFile([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || users["alice"] != string(hashB) || users["bob"] != string(hashA) {
		t.Errorf("after update = %v, want the new alice hash and bob intact", users)
	}
}

func TestHTTPConfigFromState(t *testing.T) {
	st := httpServerState{
		Dir:            "/srv/share",
		Listen:         "127.0.0.1",
		Port:           18080,
		AllowUpload:    true,
		MaxUploadBytes: 1 << 30,
		AdminIPs:       []string{"10.0.0.5"},
		Auth:           map[string]string{"alice": "$2a$10$x"},
	}
	hc := httpConfigFromState(&st)
	if hc.Dir != st.Dir || hc.Port != st.Port || !hc.AllowUpload || hc.MaxUploadBytes != st.MaxUploadBytes {
		t.Errorf("snapshot = %+v, want the state settings", hc)
	}
	if len(hc.AdminIPs) != 1 || hc.AdminIPs[0] != "10.0.0.5" {
		t.Errorf("snapshot admin IPs = %v, want the recorded address", hc.AdminIPs)
	}
	if len(hc.Auth) != 1 || hc.Auth["alice"] != "$2a$10$x" {
		t.Errorf("snapshot auth = %v, want the recorded hash", hc.Auth)
	}
	if hc.Autostart {
		t.Error("snapshot should not enable autostart on its own")
	}

	st.AdminIPs[0] = "192.0.2.1"
	st.Auth["alice"] = "changed"
	if hc.AdminIPs[0] != "10.0.0.5" || hc.Auth["alice"] != "$2a$10$x" {
		t.Error("snapshot aliases the state maps")
	}
}

func TestParseAuthHashesAndChildArgs(t *testing.T) {
	hashes, err := parseAuthHashes([]string{"bob:$2a$10$b", "alice:$2b$10$a"})
	if err != nil {
		t.Fatal(err)
	}
	args := authHashArgs(hashes)
	want := []string{"--auth-hash", "alice:$2b$10$a", "--auth-hash", "bob:$2a$10$b"}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q (users sorted)", i, args[i], want[i])
		}
	}

	for _, bad := range [][]string{
		{"alice"},
		{"alice:plaintext"},
		{":$2a$x"},
		{"a:$2a$x", "a:$2a$y"},
	} {
		if _, err := parseAuthHashes(bad); err == nil {
			t.Errorf("parseAuthHashes(%q) accepted an invalid entry", bad)
		}
	}
}

// The child command and the state file only ever see bcrypt hashes: the
// plain-text password must not leak into the serve arguments.
func TestAuthChildArgsContainNoPlaintext(t *testing.T) {
	hashes, err := parseAuthEntries([]string{"alice:s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(authHashArgs(hashes), " ")
	if strings.Contains(joined, "s3cret") {
		t.Errorf("child args leak the plain password: %s", joined)
	}
	if !strings.Contains(joined, "--auth-hash alice:$2") {
		t.Errorf("child args missing the hashed credential: %s", joined)
	}
}
