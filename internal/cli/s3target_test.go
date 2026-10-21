package cli

import (
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func s3TestConfig() *config.Config {
	c := config.Default()
	c.Addons.Minio = map[string]config.MinioConfig{
		"ms3": {Listen: "127.0.0.1", APIPort: 0, RootUser: "admin", RootPassword: "pw"},
	}
	c.Addons.Silo = map[string]config.SiloConfig{
		"ss3": {Listen: "127.0.0.1", APIPort: 0, RootUser: "admin", RootPassword: "spw", TLS: true},
	}
	return c
}

// startProbeListener binds a random loopback port and hands the number over.
func startProbeListener(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// closedLoopbackPort returns a port that is (momentarily) definitely not
// listening: bound, then released.
func closedLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func TestResolveS3Target(t *testing.T) {
	cfg := s3TestConfig()
	port := startProbeListener(t)
	mutateMinio(cfg, "ms3", func(m *config.MinioConfig) { m.APIPort = port })

	tgt, err := resolveS3Target(cfg, "minio", "ms3")
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("http://127.0.0.1:%d", port); tgt.BaseURL != want {
		t.Errorf("base url = %q, want %q", tgt.BaseURL, want)
	}
	if tgt.User != "admin" || tgt.Password != "pw" {
		t.Errorf("creds = %q/%q", tgt.User, tgt.Password)
	}

	mutated := s3TestConfig()
	mutated.Addons.Silo["ss3"] = config.SiloConfig{Listen: "127.0.0.1", APIPort: port, TLS: true}
	tgt, err = resolveS3Target(mutated, "silo", "ss3")
	if err != nil {
		t.Fatal(err)
	}
	if tgt.BaseURL != fmt.Sprintf("https://127.0.0.1:%d", port) {
		t.Errorf("tls silo base url = %q", tgt.BaseURL)
	}
}

func TestResolveS3TargetErrors(t *testing.T) {
	cfg := s3TestConfig()
	if _, err := resolveS3Target(cfg, "minio", "nope"); err == nil ||
		!strings.Contains(err.Error(), "not installed") {
		t.Errorf("missing addon: %v", err)
	}
	if _, err := resolveS3Target(cfg, "garage", "x"); err == nil ||
		!strings.Contains(err.Error(), "minio or silo") {
		t.Errorf("bad kind: %v", err)
	}
	// Configured but nobody listening: a released loopback port refuses.
	closed := closedLoopbackPort(t)
	mut := s3TestConfig()
	mut.Addons.Minio = map[string]config.MinioConfig{"ms3": {Listen: "127.0.0.1", APIPort: closed}}
	if _, err := resolveS3Target(mut, "minio", "ms3"); err == nil ||
		!strings.Contains(err.Error(), "start it first") {
		t.Errorf("down addon should hint at pg addon start, got %v", err)
	}
}

// mutator helpers (map values are not addressable).
func mutateMinio(c *config.Config, name string, fn func(*config.MinioConfig)) {
	v := c.Addons.Minio[name]
	fn(&v)
	c.Addons.Minio[name] = v
}
