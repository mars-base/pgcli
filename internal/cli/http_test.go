package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
		Name:      "docs",
		PID:       os.Getpid(),
		Port:      18080,
		Listen:    "127.0.0.1",
		Dir:       "/srv/share",
		TLS:       true,
		CertFile:  "/tmp/cert.pem",
		KeyFile:   "/tmp/key.pem",
		LogFile:   "/tmp/http.log",
		StartedAt: time.Unix(1700000000, 0).UTC(),
	}
	if err := writeHTTPState(dir, st); err != nil {
		t.Fatal(err)
	}
	got, err := readHTTPState(filepath.Join(dir, "docs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != st.Name || got.Port != st.Port || !got.TLS || got.Dir != st.Dir {
		t.Errorf("round-trip = %+v, want %+v", got, st)
	}

	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tmp.json.tmp"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	states := loadHTTPStates(dir)
	if len(states) != 1 || states[0].Name != "docs" {
		t.Errorf("loadHTTPStates = %+v, want the one valid entry", states)
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
