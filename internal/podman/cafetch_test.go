package podman

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mars-base/pgcli/internal/tlsca"
)

func TestNormalizeS3Endpoint(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"10.0.0.9:9000", "10.0.0.9:9000"},
		{"https://10.0.0.9:9000", "10.0.0.9:9000"},
		{"http://minio.internal:9000/", "minio.internal:9000"},
		{"minio.internal", "minio.internal:443"},
		{" 10.0.0.9:9000 ", "10.0.0.9:9000"},
		{"[fd00::9]:9000", "[fd00::9]:9000"},
		{"fd00::9", "[fd00::9]:443"}, // bare IPv6 gets brackets + default port
		{"10.0.0.9:", "10.0.0.9:443"},
	}
	for _, tc := range tests {
		if got := NormalizeS3Endpoint(tc.in); got != tc.want {
			t.Errorf("NormalizeS3Endpoint(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// End-to-end: a TLS server backed by real tlsca material (the same leaf+CA
// chain pgcli's MinIO serves) must hand its CA back over a plain dial.
func TestFetchRepoCAEndToEnd(t *testing.T) {
	dir := t.TempDir()
	caPath, err := tlsca.Generate(dir, []string{"127.0.0.1", "localhost"}, time.Hour)
	if err != nil {
		t.Fatalf("tlsca.Generate: %v", err)
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, tlsca.ServerCert), filepath.Join(dir, tlsca.ServerKey))
	if err != nil {
		t.Fatalf("LoadX509KeyPair (chain must load as one pair): %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()

	got, err := FetchRepoCA(strings.TrimPrefix(srv.URL, "https://"))
	if err != nil {
		t.Fatalf("FetchRepoCA: %v", err)
	}
	wantPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got) != strings.TrimSpace(string(wantPEM)) {
		t.Fatal("fetched CA differs from the generated ca.crt")
	}
}

// A server that presents only its leaf (pre-chain pgcli) must produce the
// actionable "upgrade the storage host" error, not a silent wrong answer.
func TestFetchRepoCALeafOnlyServer(t *testing.T) {
	dir := t.TempDir()
	if _, err := tlsca.Generate(dir, []string{"127.0.0.1", "localhost"}, time.Hour); err != nil {
		t.Fatalf("tlsca.Generate: %v", err)
	}
	// Strip the CA block back out of the chain, like a legacy public.crt.
	leafPEM, err := os.ReadFile(filepath.Join(dir, tlsca.ServerCert))
	if err != nil {
		t.Fatal(err)
	}
	legacy := leafPEM[:strings.Index(string(leafPEM), "-----END CERTIFICATE-----")+len("-----END CERTIFICATE-----\n")]
	if err := os.WriteFile(filepath.Join(dir, tlsca.ServerCert), legacy, 0644); err != nil {
		t.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, tlsca.ServerCert), filepath.Join(dir, tlsca.ServerKey))
	if err != nil {
		t.Fatalf("LoadX509KeyPair: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()

	_, err = FetchRepoCA(strings.TrimPrefix(srv.URL, "https://"))
	if err == nil {
		t.Fatal("FetchRepoCA succeeded against a leaf-only server, want error")
	}
	if !strings.Contains(err.Error(), "did not present its CA") {
		t.Fatalf("error = %v, want the pre-chain upgrade message", err)
	}
}

// Non-TLS listener: the dial error must be wrapped with the HTTPS-is-required
// explanation (that is the config mistake this command will most often meet).
func TestFetchRepoCAPlainHTTPTEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	_, err := FetchRepoCA(strings.TrimPrefix(srv.URL, "http://"))
	if err == nil {
		t.Fatal("FetchRepoCA succeeded against a plaintext endpoint, want error")
	}
	if !strings.Contains(err.Error(), "must serve HTTPS") {
		t.Fatalf("error = %v, want the HTTPS-required hint", err)
	}
}
