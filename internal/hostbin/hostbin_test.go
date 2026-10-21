package hostbin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakePayload(t *testing.T) []byte {
	t.Helper()
	return []byte("#!/bin/sh\necho fake-binary\n")
}

func newServer(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/bare", func(w http.ResponseWriter, r *http.Request) { w.Write(payload) })
	mux.HandleFunc("/tar", func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		hdr := &tar.Header{Name: "subdir/juicefs", Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		w.Write(buf.Bytes())
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	return httptest.NewServer(mux)
}

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestEnsureBareBinary(t *testing.T) {
	payload := fakePayload(t)
	srv := newServer(t, payload)
	defer srv.Close()
	dir := t.TempDir()

	got, err := Ensure(Download{Binary: "faketool", URL: srv.URL + "/bare", SHA256: hexSum(payload)}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "faketool") {
		t.Fatalf("path = %s", got)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, payload) {
		t.Error("content mismatch")
	}
	st, err := os.Stat(got)
	if err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Errorf("not executable: %v %v", st, err)
	}
	// No temp litter left behind.
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".faketool.download") {
			t.Errorf("leftover temp %s", e.Name())
		}
	}
}

func TestEnsureTarMember(t *testing.T) {
	payload := fakePayload(t)
	srv := newServer(t, payload)
	defer srv.Close()
	dir := t.TempDir()

	// The checksum covers the tarball as served (sidecar semantics), so
	// fetch it once to compute the expected digest.
	res, err := http.Get(srv.URL + "/tar")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(res.Body); err != nil {
		t.Fatal(err)
	}

	got, err := Ensure(Download{Binary: "juicefs", URL: srv.URL + "/tar", Member: "juicefs", SHA256: hexSum(buf.Bytes())}, dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, payload) {
		t.Error("extracted content mismatch")
	}
}

func TestEnsureChecksumMismatch(t *testing.T) {
	srv := newServer(t, fakePayload(t))
	defer srv.Close()
	dir := t.TempDir()

	_, err := Ensure(Download{Binary: "faketool", URL: srv.URL + "/bare", SHA256: strings.Repeat("ab", 32)}, dir)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want checksum mismatch, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "faketool")); !os.IsNotExist(err) {
		t.Error("bad checksum must not install the binary")
	}
}

func TestEnsureHTTPError(t *testing.T) {
	srv := newServer(t, fakePayload(t))
	defer srv.Close()

	_, err := Ensure(Download{Binary: "faketool", URL: srv.URL + "/missing"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("want 404 error, got %v", err)
	}
}

func TestEnsureMissingMember(t *testing.T) {
	srv := newServer(t, fakePayload(t))
	defer srv.Close()

	_, err := Ensure(Download{Binary: "juicefs", URL: srv.URL + "/tar", Member: "nonexistent"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want member-not-found error, got %v", err)
	}
}

func TestInstallDirPreferred(t *testing.T) {
	dir := t.TempDir()
	got, note, err := InstallDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir || note != "" {
		t.Fatalf("got %s/%q", got, note)
	}
}

func TestDetect(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "pgcli-detect-probe")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if p, ok := Detect("pgcli-detect-probe"); !ok || p != target {
		t.Fatalf("Detect = %s,%v", p, ok)
	}
	if _, ok := Detect("definitely-not-here-xyz"); ok {
		t.Error("Detect found a nonexistent binary")
	}
}
