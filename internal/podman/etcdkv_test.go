package podman

import (
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// tlsTestServer starts an httptest TLS server and returns it plus the path to
// its self-signed certificate (usable as a CA file).
func tlsTestServer(t *testing.T, handler http.Handler) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	caPath := filepath.Join(t.TempDir(), "ca.crt")
	der := srv.Certificate().Raw
	pemData := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(caPath, pemData, 0644); err != nil {
		t.Fatal(err)
	}
	return srv, caPath
}

func TestEtcdGetValueWithTLS(t *testing.T) {
	srv, caPath := tlsTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/kv/range" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"kvs": []map[string]string{{
				"key":   base64.StdEncoding.EncodeToString([]byte("foo")),
				"value": base64.StdEncoding.EncodeToString([]byte("bar")),
			}},
		})
	}))

	value, found, err := EtcdGetValueWith([]string{srv.URL}, "foo", EtcdClientOpts{CAFile: caPath})
	if err != nil {
		t.Fatalf("EtcdGetValueWith: %v", err)
	}
	if !found || value != "bar" {
		t.Errorf("value=%q found=%v, want bar/true", value, found)
	}

	if _, _, err := EtcdGetValueWith([]string{srv.URL}, "foo", EtcdClientOpts{}); err == nil {
		t.Error("plaintext client unexpectedly trusted the TLS endpoint")
	}
	if _, _, err := EtcdGetValueWith([]string{srv.URL}, "foo", EtcdClientOpts{InsecureSkipVerify: true}); err != nil {
		t.Errorf("InsecureSkipVerify client: %v", err)
	}
}

func TestEtcdAuthTokenCachedAndReauthenticated(t *testing.T) {
	var authCalls atomic.Int32
	var kvCalls atomic.Int32
	srv, _ := tlsTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/auth/authenticate":
			authCalls.Add(1)
			json.NewEncoder(w).Encode(map[string]string{"token": "tok-1"})
		case "/v3/kv/range":
			n := kvCalls.Add(1)
			if r.Header.Get("Authorization") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			// The second data call sees an expired token: a 401 must trigger
			// exactly one re-authentication and a retry.
			if n == 2 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"kvs": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))

	c, err := newEtcdClientWithOpts([]string{srv.URL}, EtcdClientOpts{
		InsecureSkipVerify: true,
		Username:           "root",
		Password:           "pw",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.post("/v3/kv/range", map[string]string{}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if got := authCalls.Load(); got != 1 {
		t.Errorf("authenticate calls after first request = %d, want 1", got)
	}
	if _, err := c.post("/v3/kv/range", map[string]string{}); err != nil {
		t.Fatalf("second call after 401: %v", err)
	}
	if got := authCalls.Load(); got != 2 {
		t.Errorf("authenticate calls after 401 = %d, want 2", got)
	}
}

func TestEtcdAuthStatusWith(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		srv, caPath := tlsTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v3/auth/status" {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]bool{"enabled": enabled})
		}))
		got, err := EtcdAuthStatusWith([]string{srv.URL}, EtcdClientOpts{CAFile: caPath})
		if err != nil {
			t.Fatalf("enabled=%v: %v", enabled, err)
		}
		if got != enabled {
			t.Errorf("enabled=%v, got %v", enabled, got)
		}
	}
}

func TestEtcdUserAddSendsPasswordInBody(t *testing.T) {
	var got map[string]string
	srv, caPath := tlsTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/auth/user/add" {
			http.NotFound(w, r)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]any{})
	}))
	if err := EtcdUserAdd([]string{srv.URL}, "root", "s3cret", EtcdClientOpts{CAFile: caPath}); err != nil {
		t.Fatalf("EtcdUserAdd: %v", err)
	}
	if got["name"] != "root" || got["password"] != "s3cret" {
		t.Errorf("body = %v, want name/password", got)
	}
}

func TestEtcdClientPlaintextStaysHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"kvs": []any{}})
	}))
	defer srv.Close()
	if _, _, err := EtcdGetValue([]string{srv.URL}, "foo"); err != nil {
		t.Errorf("plaintext EtcdGetValue: %v", err)
	}
}
