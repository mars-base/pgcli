package safeline

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptPasswordRoundTrip(t *testing.T) {
	const key = "0123456789abcdef"
	for _, pw := range []string{"a", "secret123", "p@ss:w0rd-with-16ch", ""} {
		enc, err := EncryptPassword(pw, key)
		if err != nil {
			t.Fatalf("EncryptPassword(%q): %v", pw, err)
		}
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			t.Fatalf("base64: %v", err)
		}
		if len(raw) < 16 {
			t.Fatalf("ciphertext too short: %d bytes", len(raw))
		}
		saltHex := string(raw[:16])
		if len(saltHex) != 16 {
			t.Fatalf("salt = %q, want 16 ASCII hex chars", saltHex)
		}
		block, err := aes.NewCipher([]byte(key))
		if err != nil {
			t.Fatal(err)
		}
		pt := make([]byte, len(raw)-16)
		cipher.NewCBCDecrypter(block, []byte(saltHex)).CryptBlocks(pt, raw[16:])
		pad := int(pt[len(pt)-1])
		if pad < 1 || pad > aes.BlockSize {
			t.Fatalf("bad padding %d", pad)
		}
		if got := string(pt[:len(pt)-pad]); got != pw {
			t.Errorf("round-trip = %q, want %q", got, pw)
		}
	}
	if _, err := EncryptPassword("x", "short"); err == nil {
		t.Error("short key accepted")
	}
}

func TestParseBalance(t *testing.T) {
	for name, want := range map[string]int{"roundrobin": 1, "LeastConn": 2, "iphash": 3, "backup": 4} {
		got, err := ParseBalance(name)
		if err != nil || got != want {
			t.Errorf("ParseBalance(%q) = (%d, %v), want %d", name, got, err, want)
		}
		if back := BalanceName(want); !strings.EqualFold(back, name) {
			t.Errorf("BalanceName(%d) = %q, round-trip of %q", want, back, name)
		}
	}
	if _, err := ParseBalance("random"); err == nil {
		t.Error("unknown balance accepted")
	}
}

func TestNormalizeServerNames(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{nil, []string{"*"}},
		{[]string{"", "  "}, []string{"*"}},
		{[]string{" a.test ", "b.test", "a.test"}, []string{"a.test", "b.test"}},
		{[]string{"*"}, []string{"*"}},
	}
	for _, tc := range cases {
		got := NormalizeServerNames(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("NormalizeServerNames(%v) = %v, want %v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("NormalizeServerNames(%v) = %v, want %v", tc.in, got, tc.want)
			}
		}
	}
}

func TestValidateEntryPorts(t *testing.T) {
	if err := ValidateEntryPorts([]string{"80", "443_ssl", "18080"}); err != nil {
		t.Errorf("valid ports rejected: %v", err)
	}
	for _, bad := range [][]string{nil, {"0"}, {"65536"}, {"http"}, {"443_ssl_ssl"}, {"443_"}, {"443_tls"}} {
		if err := ValidateEntryPorts(bad); err == nil {
			t.Errorf("ports %v accepted", bad)
		}
	}
}

func TestValidateUpstreams(t *testing.T) {
	if err := ValidateUpstreams([]string{"http://10.0.0.5:8080", "https://svc.example"}); err != nil {
		t.Errorf("valid upstreams rejected: %v", err)
	}
	for _, bad := range [][]string{nil, {"10.0.0.5:8080"}, {"ftp://x"}, {"http://"}} {
		if err := ValidateUpstreams(bad); err == nil {
			t.Errorf("upstreams %v accepted", bad)
		}
	}
}

func TestValidateHealthCheck(t *testing.T) {
	if err := ValidateHealthCheck(10, "/"); err != nil {
		t.Errorf("valid health check rejected: %v", err)
	}
	if err := ValidateHealthCheck(3600, "/api/health"); err != nil {
		t.Errorf("valid health check rejected: %v", err)
	}
	for _, tc := range []struct {
		interval int
		path     string
	}{{9, "/"}, {3601, "/"}, {10, "health"}, {10, "/a b"}, {10, "/a?x=1"}, {10, "/.safeline/x"}} {
		if err := ValidateHealthCheck(tc.interval, tc.path); err == nil {
			t.Errorf("health check (%d, %q) accepted", tc.interval, tc.path)
		}
	}
}

// fakeConsole implements the console endpoints the client uses. It validates
// the encrypted login exactly like the real console and requires the bearer
// token afterwards.
type fakeConsole struct {
	t          *testing.T
	key        string
	password   string
	loginBody  map[string]string
	siteList   any
	rawSite    map[string]any
	requests   []*http.Request
	lastBodies map[string]map[string]any
	createResp any
	msg        string
	health     map[string]string
	certs      any
	certID     int
	certDelErr string
	certDelMsg string
}

func newFakeConsole(t *testing.T, password string) *fakeConsole {
	return &fakeConsole{
		t:          t,
		key:        "0123456789abcdef",
		password:   password,
		lastBodies: map[string]map[string]any{},
	}
}

func (f *fakeConsole) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/open/system/key", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": f.key, "err": nil, "msg": ""})
	})
	mux.HandleFunc("/api/open/auth/csrf", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": map[string]string{"csrf_token": "csrf-1"}, "err": nil, "msg": ""})
	})
	mux.HandleFunc("/api/open/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		decodeBody(f.t, r, &body)
		f.loginBody = body
		if body["csrf_token"] != "csrf-1" {
			writeJSON(w, map[string]any{"data": map[string]any{}, "err": nil, "msg": "bad csrf"})
			return
		}
		if got := f.decrypt(body["password"]); got != f.password {
			writeJSON(w, map[string]any{"data": map[string]any{}, "err": nil, "msg": "wrong password " + got})
			return
		}
		writeJSON(w, map[string]any{"data": map[string]string{"jwt": "jwt-1"}, "err": nil, "msg": ""})
	})
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.requests = append(f.requests, r)
			if r.Header.Get("Authorization") != "Bearer jwt-1" {
				writeJSON(w, map[string]any{"data": map[string]any{}, "err": "login-required", "msg": "Login required"})
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/api/open/site", auth(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, map[string]any{"data": map[string]any{"data": f.siteList, "total": 1, "syncing": false}, "err": nil, "msg": ""})
		case http.MethodPost:
			var body map[string]any
			decodeBody(f.t, r, &body)
			f.lastBodies["POST /api/open/site"] = body
			if f.msg != "" {
				writeJSON(w, map[string]any{"data": map[string]any{}, "err": nil, "msg": f.msg})
				return
			}
			writeJSON(w, map[string]any{"data": f.createResp, "err": nil, "msg": ""})
		case http.MethodPut:
			var body map[string]any
			decodeBody(f.t, r, &body)
			f.lastBodies["PUT /api/open/site"] = body
			f.rawSite = body
			writeJSON(w, map[string]any{"data": nil, "err": nil, "msg": ""})
		case http.MethodDelete:
			var body map[string]any
			decodeBody(f.t, r, &body)
			f.lastBodies["DELETE /api/open/site"] = body
			writeJSON(w, map[string]any{"data": nil, "err": nil, "msg": ""})
		}
	}))
	mux.HandleFunc("/api/open/cert", auth(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, map[string]any{"data": map[string]any{"nodes": f.certs, "total": 1}, "err": nil, "msg": ""})
		case http.MethodPost:
			var body map[string]any
			decodeBody(f.t, r, &body)
			if _, update := body["id"]; update {
				f.lastBodies["POST /api/open/cert (update)"] = body
				writeJSON(w, map[string]any{"data": nil, "err": nil, "msg": ""})
				return
			}
			f.lastBodies["POST /api/open/cert"] = body
			writeJSON(w, map[string]any{"data": f.certID, "err": nil, "msg": ""})
		}
	}))
	mux.HandleFunc("/api/open/cert/", auth(func(w http.ResponseWriter, r *http.Request) {
		var err any
		if f.certDelErr != "" {
			err = f.certDelErr
		}
		writeJSON(w, map[string]any{"data": nil, "err": err, "msg": f.certDelMsg})
	}))
	mux.HandleFunc("/api/open/site/", auth(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/healthcheck") {
			var body map[string]any
			decodeBody(f.t, r, &body)
			f.lastBodies["POST /api/open/site/healthcheck"] = body
			writeJSON(w, map[string]any{"data": f.health, "err": nil, "msg": ""})
			return
		}
		writeJSON(w, map[string]any{"data": f.rawSite, "err": nil, "msg": ""})
	}))
	return mux
}

func (f *fakeConsole) decrypt(enc string) string {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(raw) < 32 {
		return ""
	}
	block, _ := aes.NewCipher([]byte(f.key))
	pt := make([]byte, len(raw)-16)
	cipher.NewCBCDecrypter(block, raw[:16]).CryptBlocks(pt, raw[16:])
	pad := int(pt[len(pt)-1])
	if pad < 1 || pad > len(pt) {
		return ""
	}
	return string(pt[:len(pt)-pad])
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func decodeBody(t *testing.T, r *http.Request, v any) {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decoding body %q: %v", b, err)
	}
}

func newTestClient(t *testing.T, fc *fakeConsole) *Client {
	t.Helper()
	srv := httptest.NewServer(fc.handler())
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "admin", fc.password)
	if err := c.Login(); err != nil {
		t.Fatalf("login: %v", err)
	}
	return c
}

func TestClientLoginAndList(t *testing.T) {
	fc := newFakeConsole(t, "p@ss")
	fc.siteList = []map[string]any{{
		"id": 1, "server_names": []string{"a.test"}, "ports": []string{"18097"},
		"upstreams": []string{"http://10.0.0.5:8080"}, "comment": "demo",
		"health_check": true, "health_check_interval": 30, "health_check_path": "/healthz",
		"load_balance": map[string]any{"balance_type": 2},
	}}
	c := newTestClient(t, fc)
	sites, err := c.ListSites()
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 || sites[0].ID != 1 || !sites[0].HealthCheck || sites[0].HealthCheckInterval != 30 {
		t.Fatalf("sites = %+v", sites)
	}
	if got := sites[0].EffectiveUpstreams(); len(got) != 1 || got[0] != "http://10.0.0.5:8080" {
		t.Errorf("EffectiveUpstreams = %v", got)
	}
}

func TestClientLoginWrongPassword(t *testing.T) {
	fc := newFakeConsole(t, "right")
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()
	c := NewClient(srv.URL, "admin", "wrong")
	if err := c.Login(); err == nil || !strings.Contains(err.Error(), "wrong password") {
		t.Fatalf("Login with wrong password = %v", err)
	}
}

func TestCreateSitePayload(t *testing.T) {
	fc := newFakeConsole(t, "pw")
	fc.createResp = 7
	c := newTestClient(t, fc)
	id, err := c.CreateSite(SiteSpec{
		ServerNames:         []string{"a.test", "*"},
		Ports:               []string{"18097"},
		Upstreams:           []string{"http://10.0.0.5:8080"},
		Comment:             "e2e",
		BalanceType:         1,
		CertID:              5,
		HealthCheck:         true,
		HealthCheckInterval: 30,
		HealthCheckPath:     "/healthz",
	})
	if err != nil || id != 7 {
		t.Fatalf("CreateSite = (%d, %v)", id, err)
	}
	body := fc.lastBodies["POST /api/open/site"]
	if body["comment"] != "e2e" || body["health_check"] != true {
		t.Errorf("payload = %v", body)
	}
	if body["health_check_interval"] != float64(30) || body["health_check_path"] != "/healthz" {
		t.Errorf("health payload = %v", body)
	}
	if body["cert_id"] != float64(5) {
		t.Errorf("cert_id = %v, want 5", body["cert_id"])
	}
	lb, _ := body["load_balance"].(map[string]any)
	if lb["balance_type"] != float64(1) {
		t.Errorf("load_balance = %v", lb)
	}
	for _, key := range []string{"server_names", "ports", "upstreams", "email", "cert_id", "static_default", "type"} {
		if _, ok := body[key]; !ok {
			t.Errorf("payload missing %q: %v", key, body)
		}
	}

	// A disabled health check must not send interval/path.
	if _, err := c.CreateSite(SiteSpec{Ports: []string{"18098"}, Upstreams: []string{"http://10.0.0.5:8081"}}); err != nil {
		t.Fatal(err)
	}
	body = fc.lastBodies["POST /api/open/site"]
	if _, ok := body["health_check_interval"]; ok {
		t.Errorf("disabled health check sent interval: %v", body)
	}
}

func TestCreateSiteErrorSurfaced(t *testing.T) {
	fc := newFakeConsole(t, "pw")
	fc.msg = "listen tcp :18097: bind: address already in use"
	c := newTestClient(t, fc)
	_, err := c.CreateSite(SiteSpec{Ports: []string{"18097"}, Upstreams: []string{"http://10.0.0.5:8080"}})
	if err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("CreateSite error = %v", err)
	}
}

func TestPatchSiteHealthCheck(t *testing.T) {
	fc := newFakeConsole(t, "pw")
	fc.rawSite = map[string]any{
		"id": 3.0, "server_names": []any{"a.test"}, "ports": []any{"18097"},
		"upstreams": []any{"http://10.0.0.5:8080"}, "comment": "keep-me",
		"health_check": false, "health_check_interval": 0.0, "health_check_path": "/",
	}
	fc.rawSite["health_check"] = true
	c := newTestClient(t, fc)
	enabled := true
	interval := 45
	path := "/readyz"
	site, err := c.PatchSiteHealthCheck(3, HealthCheckPatch{Enabled: &enabled, Interval: &interval, Path: &path})
	if err != nil {
		t.Fatal(err)
	}
	put := fc.lastBodies["PUT /api/open/site"]
	if put["health_check"] != true || put["health_check_interval"] != float64(45) || put["health_check_path"] != "/readyz" {
		t.Errorf("PUT payload = %v", put)
	}
	if put["comment"] != "keep-me" {
		t.Errorf("PUT dropped unrelated fields: %v", put)
	}
	if site == nil || !site.HealthCheck || site.HealthCheckInterval != 45 {
		t.Errorf("returned site = %+v", site)
	}

	// Enabling without explicit interval defaults to the 10s minimum.
	fc.rawSite["health_check"] = false
	fc.rawSite["health_check_interval"] = 0.0
	enabled = true
	if _, err := c.PatchSiteHealthCheck(3, HealthCheckPatch{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	if got := fc.lastBodies["PUT /api/open/site"]["health_check_interval"]; got != float64(10) {
		t.Errorf("default interval = %v, want 10", got)
	}

	// Invalid explicit interval is refused before the PUT.
	fc.rawSite["health_check"] = true
	bad := 5
	if _, err := c.PatchSiteHealthCheck(3, HealthCheckPatch{Interval: &bad}); err == nil {
		t.Error("interval 5 accepted")
	}
}

func TestEnvelopeMessageOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]any{"data": map[string]any{}, "err": nil, "msg": "listen tcp :9443: bind: address already in use"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "admin", "pw")
	err := c.do(http.MethodPost, "/api/open/site", map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "address already in use") || strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("error = %v, want the envelope msg", err)
	}
}

func TestDeleteSites(t *testing.T) {
	fc := newFakeConsole(t, "pw")
	c := newTestClient(t, fc)
	if err := c.DeleteSites([]int{1, 2}); err != nil {
		t.Fatal(err)
	}
	body := fc.lastBodies["DELETE /api/open/site"]
	ids, _ := body["ids"].([]any)
	if len(ids) != 2 || ids[0] != float64(1) || ids[1] != float64(2) {
		t.Errorf("ids = %v", body["ids"])
	}
}

func TestCertEndpoints(t *testing.T) {
	fc := newFakeConsole(t, "pw")
	fc.certs = []map[string]any{{
		"id": 1, "domains": []string{"a.test"}, "issuer": "a.test",
		"type": 2, "valid_before": "2026-12-24T17:36:22+08:00", "related_sites": []string{"a.test"},
	}}
	fc.certID = 3
	c := newTestClient(t, fc)

	certs, err := c.ListCerts()
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 1 || certs[0].ID != 1 || certs[0].Domains[0] != "a.test" || certs[0].RelatedSites[0] != "a.test" {
		t.Fatalf("certs = %+v", certs)
	}
	if got := CertTypeName(certs[0].Type); got != "uploaded" {
		t.Errorf("CertTypeName(2) = %q", got)
	}

	const crt = "-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----"
	const key = "-----BEGIN PRIVATE KEY-----\ny\n-----END PRIVATE KEY-----"
	id, err := c.UploadCert(crt, key)
	if err != nil || id != 3 {
		t.Fatalf("UploadCert = (%d, %v)", id, err)
	}
	body := fc.lastBodies["POST /api/open/cert"]
	if body["type"] != float64(2) {
		t.Errorf("type = %v", body["type"])
	}
	manual, _ := body["manual"].(map[string]any)
	if manual["crt"] != crt || manual["key"] != key {
		t.Errorf("manual = %v", manual)
	}

	if err := c.DeleteCert(1); err != nil {
		t.Fatalf("DeleteCert: %v", err)
	}
}

func TestUpdateCert(t *testing.T) {
	fc := newFakeConsole(t, "pw")
	c := newTestClient(t, fc)
	const crt = "-----BEGIN CERTIFICATE-----\nnew\n-----END CERTIFICATE-----"
	const key = "-----BEGIN PRIVATE KEY-----\nnew\n-----END PRIVATE KEY-----"
	if err := c.UpdateCert(4, crt, key); err != nil {
		t.Fatal(err)
	}
	body := fc.lastBodies["POST /api/open/cert (update)"]
	if body["id"] != float64(4) || body["type"] != float64(2) {
		t.Errorf("payload = %v", body)
	}
	manual, _ := body["manual"].(map[string]any)
	if manual["crt"] != crt || manual["key"] != key {
		t.Errorf("manual = %v", manual)
	}
}

func TestSetSiteCertID(t *testing.T) {
	fc := newFakeConsole(t, "pw")
	fc.rawSite = map[string]any{
		"id": 3.0, "server_names": []any{"a.test"}, "ports": []any{"19444_ssl"},
		"upstreams": []any{"http://10.0.0.5:8080"}, "comment": "keep-me",
		"cert_id": 0.0, "health_check": true, "health_check_interval": 30.0,
	}
	c := newTestClient(t, fc)
	site, err := c.SetSiteCertID(3, 9)
	if err != nil {
		t.Fatal(err)
	}
	put := fc.lastBodies["PUT /api/open/site"]
	if put["cert_id"] != float64(9) {
		t.Errorf("cert_id = %v, want 9", put["cert_id"])
	}
	if put["comment"] != "keep-me" || put["health_check"] != true {
		t.Errorf("PUT dropped unrelated fields: %v", put)
	}
	if site == nil || site.CertID != 9 {
		t.Errorf("returned site = %+v", site)
	}
}

func TestMergeUpstreams(t *testing.T) {
	cur := []string{"http://a:1", "http://b:2"}
	got, err := MergeUpstreams(cur, []string{"http://c:3", "http://a:1"}, []string{"http://b:2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "http://a:1" || got[1] != "http://c:3" {
		t.Errorf("merged = %v", got)
	}
	if _, err := MergeUpstreams(cur, nil, []string{"http://x:9"}); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("removing unknown upstream: %v", err)
	}
	if got, err := MergeUpstreams(cur, nil, nil); err != nil || len(got) != 2 {
		t.Errorf("no-op = (%v, %v)", got, err)
	}
	if got, err := MergeUpstreams(cur[:1], nil, cur[:1]); err != nil || len(got) != 0 {
		t.Errorf("removing the last upstream = (%v, %v)", got, err)
	}
}

func TestSetSiteUpstreams(t *testing.T) {
	fc := newFakeConsole(t, "pw")
	fc.rawSite = map[string]any{
		"id": 3.0, "server_names": []any{"a.test"}, "ports": []any{"18097"},
		"upstreams": []any{"http://10.0.0.5:8080"}, "comment": "keep-me",
		"cert_id": 0.0, "health_check": true, "health_check_interval": 30.0,
		"load_balance": map[string]any{
			"balance_type": 1.0,
			"upstreams": []any{
				map[string]any{"address": "http://10.0.0.5:8080", "weight": 5.0, "role": "primary"},
			},
		},
	}
	c := newTestClient(t, fc)
	want := []string{"http://10.0.0.5:8080", "http://10.0.0.6:8080"}
	site, err := c.SetSiteUpstreams(3, want)
	if err != nil {
		t.Fatal(err)
	}
	put := fc.lastBodies["PUT /api/open/site"]
	ups, _ := put["upstreams"].([]any)
	if len(ups) != 2 || ups[0] != want[0] || ups[1] != want[1] {
		t.Errorf("PUT upstreams = %v", put["upstreams"])
	}
	if put["comment"] != "keep-me" || put["health_check"] != true {
		t.Errorf("PUT dropped unrelated fields: %v", put)
	}
	lb, _ := put["load_balance"].(map[string]any)
	lbUps, _ := lb["upstreams"].([]any)
	if len(lbUps) != 2 {
		t.Fatalf("load_balance.upstreams = %v", lb["upstreams"])
	}
	kept, _ := lbUps[0].(map[string]any)
	if kept["address"] != want[0] || kept["weight"] != float64(5) || kept["role"] != "primary" {
		t.Errorf("kept load-balance entry = %v", kept)
	}
	added, _ := lbUps[1].(map[string]any)
	if added["address"] != want[1] || added["role"] != "primary" || added["weight"] != float64(1) {
		t.Errorf("added load-balance entry = %v", added)
	}
	if site == nil || len(site.Upstreams) != 2 || site.Upstreams[1] != want[1] {
		t.Errorf("returned site = %+v", site)
	}
}

func TestDeleteCertBoundSurfacesMessage(t *testing.T) {
	fc := newFakeConsole(t, "pw")
	fc.certDelErr = "cannot-delete"
	fc.certDelMsg = "cert Is Used On Website"
	c := newTestClient(t, fc)
	err := c.DeleteCert(1)
	if err == nil || !strings.Contains(err.Error(), "cert Is Used On Website") {
		t.Fatalf("DeleteCert error = %v, want the console message", err)
	}
}

func TestLoadCertificate(t *testing.T) {
	dir := t.TempDir()
	crtPath := filepath.Join(dir, "crt.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(crtPath, []byte("-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("-----BEGIN PRIVATE KEY-----\ny\n-----END PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCertificate(crtPath, keyPath); err != nil {
		t.Fatalf("LoadCertificate: %v", err)
	}
	if _, _, err := LoadCertificate(keyPath, crtPath); err == nil {
		t.Error("swapped files accepted")
	}
	if _, _, err := LoadCertificate(filepath.Join(dir, "missing.pem"), keyPath); err == nil {
		t.Error("missing file accepted")
	}
}

func TestHealthCheck(t *testing.T) {
	fc := newFakeConsole(t, "pw")
	fc.health = map[string]string{"http://10.0.0.5:8080": "", "http://10.0.0.6:8080": "unreachable"}
	c := newTestClient(t, fc)
	res, err := c.HealthCheck([]string{"http://10.0.0.5:8080", "http://10.0.0.6:8080"}, []string{"a.test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res["http://10.0.0.5:8080"] != "" || res["http://10.0.0.6:8080"] != "unreachable" {
		t.Errorf("health = %v", res)
	}
	body := fc.lastBodies["POST /api/open/site/healthcheck"]
	hosts, _ := body["hosts"].([]any)
	if len(hosts) != 1 || hosts[0] != "a.test" {
		t.Errorf("hosts = %v", body["hosts"])
	}
}
