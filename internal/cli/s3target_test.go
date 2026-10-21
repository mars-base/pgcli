package cli

import (
	"fmt"
	"net"
	"strings"
	"testing"
)

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

func TestExternalS3Target(t *testing.T) {
	port := startProbeListener(t)

	tgt, err := externalS3Target(fmt.Sprintf("http://127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("http://127.0.0.1:%d", port); tgt.BaseURL != want {
		t.Errorf("base url = %q, want %q", tgt.BaseURL, want)
	}
	if tgt.TLS {
		t.Error("http scheme must not report TLS")
	}

	// The probe is a plain TCP dial, so the same reachability check serves an
	// https endpoint too (the scheme only flips the TLS flag, which is what
	// the mount hint and config rendering branch on).
	tgt, err = externalS3Target(fmt.Sprintf("https://127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	if !tgt.TLS {
		t.Error("https scheme must report TLS")
	}
}

func TestExternalS3TargetErrors(t *testing.T) {
	if _, err := externalS3Target(""); err == nil ||
		!strings.Contains(err.Error(), "no s3_endpoint") {
		t.Errorf("empty endpoint: %v", err)
	}

	// Shape errors are rejected before any dial happens.
	for _, bad := range []string{
		"ftp://host", "http://host/bucket", "https://u:***@host",
		"https://host?x=1", "minio:9000",
	} {
		if _, err := externalS3Target(bad); err == nil ||
			!strings.Contains(err.Error(), "s3_endpoint") {
			t.Errorf("bad shape %q accepted: %v", bad, err)
		}
	}

	// Well-formed but nobody listening: the TCP dial to a released port
	// refuses, and the error names the host:port it tried.
	closed := closedLoopbackPort(t)
	_, err := externalS3Target(fmt.Sprintf("http://127.0.0.1:%d", closed))
	if err == nil || !strings.Contains(err.Error(), "not answering") {
		t.Errorf("down endpoint should report not-answering, got %v", err)
	}
}
