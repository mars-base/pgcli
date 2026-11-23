package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		"docs": {Dir: "/srv/docs", AllowDelete: true, AllowUpload: true, AllowDir: true, MaxUploadBytes: 1 << 30, Autostart: true},
		"ro":   {Dir: "/srv/ro"},
	}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"allow_delete: true", "allow_upload: true", "allow_dir: true", "max_upload_bytes: 1073741824"} {
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
	if got.HTTP["ro"].AllowDelete || got.HTTP["ro"].AllowUpload || got.HTTP["ro"].AllowDir || got.HTTP["ro"].MaxUploadBytes != 0 {
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
		LogFile:        "/tmp/http.log",
		StartedAt:      time.Unix(1700000000, 0).UTC(),
	}
	if err := writeHTTPState(dir, st); err != nil {
		t.Fatal(err)
	}
	got, err := readHTTPState(filepath.Join(dir, "docs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != st.Name || got.Port != st.Port || !got.TLS || got.Dir != st.Dir ||
		!got.AllowDelete || !got.AllowUpload || !got.AllowDir || got.MaxUploadBytes != st.MaxUploadBytes {
		t.Errorf("round-trip = %+v, want %+v", got, st)
	}

	ro := httpServerState{Name: "ro", PID: 1, LogFile: "/tmp/ro.log"}
	if err := writeHTTPState(dir, ro); err != nil {
		t.Fatal(err)
	}
	if _, err := readHTTPState(filepath.Join(dir, "ro.json")); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "ro.json"))
	for _, key := range []string{"allow_delete", "allow_upload", "allow_dir", "max_upload_bytes"} {
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
