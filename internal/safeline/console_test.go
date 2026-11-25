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
	"strconv"
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

func TestParseMode(t *testing.T) {
	for name, want := range map[string]int{"defense": ModeDefense, "Dryrun": ModeDryrun, "offline": ModeOffline} {
		got, err := ParseMode(name)
		if err != nil || got != want {
			t.Errorf("ParseMode(%q) = (%d, %v), want %d", name, got, err, want)
		}
		if back := ModeName(want); !strings.EqualFold(back, name) {
			t.Errorf("ModeName(%d) = %q, round-trip of %q", want, back, name)
		}
	}
	if _, err := ParseMode("strict"); err == nil {
		t.Error("unknown mode accepted")
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
	t           *testing.T
	key         string
	password    string
	loginBody   map[string]string
	siteList    any
	rawSite     map[string]any
	waiting     any
	aclRules    any
	globalMode  any
	siteMode    any
	policy      any
	globalProxy any
	siteProxy   any
	requests    []*http.Request
	lastBodies  map[string]map[string]any
	createResp  any
	msg         string
	health      map[string]string
	certs       any
	certID      int
	certDelErr  string
	certDelMsg  string
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
	mux.HandleFunc("/api/open/global/mode", auth(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, map[string]any{"data": f.globalMode, "err": nil, "msg": ""})
		case http.MethodPut:
			var body map[string]any
			decodeBody(f.t, r, &body)
			f.lastBodies["PUT /api/open/global/mode"] = body
			f.globalMode = body
			writeJSON(w, map[string]any{"data": nil, "err": nil, "msg": ""})
		}
	}))
	mux.HandleFunc("/api/open/global/proxy", auth(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			data := f.globalProxy
			if data == nil {
				data = map[string]any{
					"ip_source": map[string]any{"value": "socket_ip", "global": true},
					"ip_value":  map[string]any{"value": "", "global": true},
				}
			}
			writeJSON(w, map[string]any{"data": data, "err": nil, "msg": ""})
		case http.MethodPut:
			var body map[string]any
			decodeBody(f.t, r, &body)
			f.lastBodies["PUT /api/open/global/proxy"] = body
			f.globalProxy = body
			writeJSON(w, map[string]any{"data": nil, "err": nil, "msg": ""})
		}
	}))
	mux.HandleFunc("/api/open/policy/detail", auth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": f.policy, "err": nil, "msg": ""})
	}))
	mux.HandleFunc("/api/open/site/", auth(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/challenge") {
			var body map[string]any
			decodeBody(f.t, r, &body)
			f.lastBodies["PUT /api/open/site/challenge"] = body
			if f.rawSite == nil {
				f.rawSite = map[string]any{}
			}
			enable, _ := body["enable"].(bool)
			f.rawSite["challenge_is_enabled"] = enable
			if enable {
				if id, _ := f.rawSite["challenge_id"].(float64); id == 0 {
					f.rawSite["challenge_id"] = 9
				}
			}
			writeJSON(w, map[string]any{"data": nil, "err": nil, "msg": ""})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/proxy") {
			switch r.Method {
			case http.MethodGet:
				data := f.siteProxy
				if data == nil {
					data = map[string]any{
						"ip_source": map[string]any{"value": "socket_ip", "global": true},
						"ip_value":  map[string]any{"value": "", "global": true},
					}
				}
				writeJSON(w, map[string]any{"data": data, "err": nil, "msg": ""})
			case http.MethodPut:
				var body map[string]any
				decodeBody(f.t, r, &body)
				f.lastBodies["PUT "+r.URL.Path] = body
				f.siteProxy = body
				writeJSON(w, map[string]any{"data": nil, "err": nil, "msg": ""})
			}
			return
		}
		if strings.HasSuffix(r.URL.Path, "/semantics") {
			switch r.Method {
			case http.MethodGet:
				writeJSON(w, map[string]any{"data": f.siteMode, "err": nil, "msg": ""})
			case http.MethodPut:
				var body map[string]any
				decodeBody(f.t, r, &body)
				f.lastBodies["PUT "+r.URL.Path] = body
				if useGlobal, _ := body["use_global"].(bool); useGlobal {
					f.siteMode = nil
				} else {
					f.siteMode = map[string]any{"semantics": body["semantics"], "site_id": 4, "id": 2}
				}
				writeJSON(w, map[string]any{"data": nil, "err": nil, "msg": ""})
			}
			return
		}
		if strings.HasSuffix(r.URL.Path, "/mode") {
			var body map[string]any
			decodeBody(f.t, r, &body)
			f.lastBodies["PUT /api/open/site/mode"] = body
			writeJSON(w, map[string]any{"data": nil, "err": nil, "msg": ""})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/healthcheck") {
			var body map[string]any
			decodeBody(f.t, r, &body)
			f.lastBodies["POST /api/open/site/healthcheck"] = body
			writeJSON(w, map[string]any{"data": f.health, "err": nil, "msg": ""})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/waiting") {
			switch r.Method {
			case http.MethodGet:
				writeJSON(w, map[string]any{"data": f.waiting, "err": nil, "msg": ""})
			case http.MethodPost:
				var body map[string]any
				decodeBody(f.t, r, &body)
				f.lastBodies["POST "+r.URL.Path] = body
				writeJSON(w, map[string]any{"data": nil, "err": nil, "msg": ""})
			}
			return
		}
		if i := strings.Index(r.URL.Path, "/acl"); i >= 0 {
			rest := r.URL.Path[i:]
			switch {
			case rest == "/acl" && r.Method == http.MethodGet:
				writeJSON(w, map[string]any{"data": f.aclRules, "err": nil, "msg": ""})
			case rest == "/acl" && r.Method == http.MethodPut:
				var body map[string]any
				decodeBody(f.t, r, &body)
				f.lastBodies["PUT "+r.URL.Path] = body
				writeJSON(w, map[string]any{"data": nil, "err": nil, "msg": ""})
			case strings.HasPrefix(rest, "/acl/") && r.Method == http.MethodPut:
				var body map[string]any
				decodeBody(f.t, r, &body)
				f.lastBodies["PUT "+r.URL.Path] = body
				id, _ := strconv.Atoi(rest[len("/acl/"):])
				writeJSON(w, map[string]any{"data": id, "err": nil, "msg": ""})
			}
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

func TestSetSiteMode(t *testing.T) {
	fc := newFakeConsole(t, "pw")
	c := newTestClient(t, fc)
	if err := c.SetSiteMode([]int{4, 5}, ModeDryrun); err != nil {
		t.Fatal(err)
	}
	body := fc.lastBodies["PUT /api/open/site/mode"]
	ids, _ := body["ids"].([]any)
	if len(ids) != 2 || ids[0] != float64(4) || ids[1] != float64(5) {
		t.Errorf("ids = %v", body["ids"])
	}
	if body["mode"] != float64(ModeDryrun) {
		t.Errorf("mode = %v, want %d", body["mode"], ModeDryrun)
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

func TestWaitingRoomEndpoints(t *testing.T) {
	fc := newFakeConsole(t, "pw")
	fc.waiting = map[string]any{
		"id": 1, "name": "website_waiting_room_4", "is_enabled": true,
		"max_concurrent": 7, "max_waiting": 0, "session_timeout": 4,
		"website_id": 4,
		"created_at": "2026-11-25T11:45:36.808774+08:00",
		"updated_at": "2026-11-25T11:45:36.808774+08:00",
	}
	c := newTestClient(t, fc)
	wr, err := c.GetWaitingRoom(4)
	if err != nil {
		t.Fatal(err)
	}
	if wr.ID != 1 || !wr.IsEnabled || wr.MaxConcurrent != 7 || wr.SessionTimeout != 4 ||
		wr.WebsiteID != 4 || !strings.HasPrefix(wr.CreatedAt, "2026-11-25") {
		t.Errorf("waiting room = %+v", wr)
	}

	if err := c.SetWaitingRoom(4, WaitingRoom{IsEnabled: true, MaxConcurrent: 7, SessionTimeout: 4}); err != nil {
		t.Fatal(err)
	}
	body := fc.lastBodies["POST /api/open/site/4/waiting"]
	if len(body) != 3 || body["is_enabled"] != true ||
		body["max_concurrent"] != float64(7) || body["session_timeout"] != float64(4) {
		t.Errorf("POST body = %v", body)
	}
}

func TestRateLimitEndpoints(t *testing.T) {
	fc := newFakeConsole(t, "pw")
	fc.aclRules = []map[string]any{
		{"id": 14, "site_id": 4, "type": "attack", "priority": 2, "enabled": false,
			"period": 60, "count": 10, "block_min": 30, "action": "block", "use_global": true},
		{"id": 15, "site_id": 4, "type": "error", "priority": 1, "enabled": false,
			"period": 10, "count": 10, "block_min": 30, "action": "block", "use_global": true,
			"conditions": []any{map[string]any{
				"strategy":  "status_code",
				"condition": map[string]any{"field": "", "operator": "eq", "value": []string{"403", "404"}},
			}}},
		{"id": 13, "site_id": 4, "type": "req", "priority": 3, "enabled": true,
			"period": 10, "count": 100, "block_min": 60, "action": "challenge_v1", "use_global": true},
	}
	c := newTestClient(t, fc)
	rules, err := c.ListRateLimits(4)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 3 || rules[0].Type != "attack" || rules[2].Action != "challenge_v1" {
		t.Fatalf("rules = %+v", rules)
	}
	cond := rules[1].Conditions
	if len(cond) != 1 || cond[0].Strategy != "status_code" || cond[0].Condition.Operator != "eq" ||
		len(cond[0].Condition.Value) != 2 || cond[0].Condition.Value[1] != "404" {
		t.Errorf("conditions = %+v", cond)
	}
	if !RateLimitsFollowGlobal(rules) {
		t.Error("global rule set not detected")
	}
	rules[0].UseGlobal, rules[1].UseGlobal, rules[2].UseGlobal = false, false, false
	if RateLimitsFollowGlobal(rules) {
		t.Error("custom rule set detected as global")
	}

	if err := c.UseGlobalRateLimits(4); err != nil {
		t.Fatal(err)
	}
	body := fc.lastBodies["PUT /api/open/site/4/acl"]
	if len(body) != 1 || body["use_global"] != true {
		t.Errorf("use-global body = %v", body)
	}

	custom := []RateLimitRule{{ID: 19, SiteID: 4, Type: "req", Period: 10, Count: 50,
		Action: "block", BlockMin: 5, Enabled: true}}
	if err := c.SetCustomRateLimits(4, custom); err != nil {
		t.Fatal(err)
	}
	body = fc.lastBodies["PUT /api/open/site/4/acl"]
	if body["use_global"] != false {
		t.Errorf("custom use_global = %v", body["use_global"])
	}
	sent, _ := body["rules"].([]any)
	if len(sent) != 1 {
		t.Fatalf("custom rules = %v", body["rules"])
	}
	if rule, _ := sent[0].(map[string]any); rule["count"] != float64(50) || rule["action"] != "block" {
		t.Errorf("custom rule = %v", sent[0])
	}

	rule := rules[0]
	rule.Count = 11
	if err := c.UpdateRateLimit(0, rule); err != nil {
		t.Fatal(err)
	}
	body = fc.lastBodies["PUT /api/open/site/0/acl/14"]
	if body["count"] != float64(11) || body["type"] != "attack" {
		t.Errorf("single-rule body = %v", body)
	}
}

func TestParseSemanticsMode(t *testing.T) {
	for name, want := range map[string]string{
		"disable": "disable", "Observe": "dry_run", "balance": "default", "STRICT": "strict",
	} {
		got, err := ParseSemanticsMode(name)
		if err != nil || got != want {
			t.Errorf("ParseSemanticsMode(%q) = (%q, %v), want %q", name, got, err, want)
		}
		if back := SemanticsLevelName(want); !strings.EqualFold(back, name) {
			t.Errorf("SemanticsLevelName(%q) = %q, round-trip of %q", want, back, name)
		}
	}
	if _, err := ParseSemanticsMode("default"); err == nil {
		t.Error("raw stored value accepted as a CLI mode")
	}
	if _, err := ParseSemanticsMode("off"); err == nil {
		t.Error("unknown semantics mode accepted")
	}
}

func TestBatchSemantics(t *testing.T) {
	cur := map[string]string{"m_sqli": "strict", "m_rule": "default", "m_scanner": "disable", "m_response": "disable"}
	got := BatchSemantics(cur, "dry_run")
	for _, m := range SemanticsModules {
		if got[m] != "dry_run" {
			t.Errorf("%s = %q, want dry_run", m, got[m])
		}
	}
	for _, keep := range []string{"m_rule", "m_scanner", "m_response"} {
		if got[keep] != cur[keep] {
			t.Errorf("%s = %q, want preserved %q", keep, got[keep], cur[keep])
		}
	}
	if cur["m_sqli"] != "strict" {
		t.Errorf("input map mutated: %v", cur)
	}
	if miss := SemanticsMismatch(got, "dry_run"); len(miss) != 0 {
		t.Errorf("mismatch = %v", miss)
	}
	got["m_xss"] = "disable"
	if miss := SemanticsMismatch(got, "dry_run"); len(miss) != 1 || miss[0] != "m_xss" {
		t.Errorf("mismatch = %v, want [m_xss]", miss)
	}
}

func TestSemanticsEndpoints(t *testing.T) {
	fc := newFakeConsole(t, "pw")
	fc.globalMode = map[string]any{"semantics": map[string]any{"m_sqli": "strict", "m_scanner": "disable"}}
	c := newTestClient(t, fc)
	levels, err := c.GetGlobalSemantics()
	if err != nil {
		t.Fatal(err)
	}
	if levels["m_sqli"] != "strict" || levels["m_scanner"] != "disable" {
		t.Fatalf("global levels = %v", levels)
	}
	if err := c.SetGlobalSemantics(map[string]string{"m_sqli": "dry_run"}); err != nil {
		t.Fatal(err)
	}
	body := fc.lastBodies["PUT /api/open/global/mode"]
	sent, _ := body["semantics"].(map[string]any)
	if sent["m_sqli"] != "dry_run" || len(body) != 1 {
		t.Errorf("global PUT body = %v", body)
	}

	// No row: the site follows the global configuration.
	own, err := c.GetSiteSemantics(4)
	if err != nil || own != nil {
		t.Fatalf("GetSiteSemantics(no row) = (%+v, %v), want nil", own, err)
	}
	fc.siteMode = map[string]any{"id": 2, "site_id": 4, "semantics": map[string]any{"m_sqli": "disable"}}
	own, err = c.GetSiteSemantics(4)
	if err != nil || own == nil || own.Semantics["m_sqli"] != "disable" || own.SiteID != 4 {
		t.Fatalf("GetSiteSemantics = (%+v, %v)", own, err)
	}
	if err := c.SetSiteSemantics(4, false, map[string]string{"m_sqli": "strict"}); err != nil {
		t.Fatal(err)
	}
	body = fc.lastBodies["PUT /api/open/site/4/semantics"]
	if body["use_global"] != false {
		t.Errorf("custom use_global = %v", body["use_global"])
	}
	sent, _ = body["semantics"].(map[string]any)
	if sent["m_sqli"] != "strict" {
		t.Errorf("custom semantics = %v", body["semantics"])
	}
	if err := c.SetSiteSemantics(4, true, map[string]string{"m_sqli": "default"}); err != nil {
		t.Fatal(err)
	}
	body = fc.lastBodies["PUT /api/open/site/4/semantics"]
	if body["use_global"] != true {
		t.Errorf("use-global body = %v", body)
	}
	// The console rejects the request without levels, so they go out too.
	if sent, _ = body["semantics"].(map[string]any); sent["m_sqli"] != "default" {
		t.Errorf("use-global semantics = %v", body["semantics"])
	}
	own, err = c.GetSiteSemantics(4)
	if err != nil || own != nil {
		t.Errorf("GetSiteSemantics after use-global = (%+v, %v), want nil", own, err)
	}
}

func TestBotEndpoints(t *testing.T) {
	fc := newFakeConsole(t, "pw")
	fc.rawSite = map[string]any{
		"id": 4, "server_names": []string{"www.example.com"},
		"challenge_id": 0, "challenge_is_enabled": false,
	}
	c := newTestClient(t, fc)

	// A site that never configured BOT protection reports the defaults.
	cfg, err := c.GetSiteBot(4)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled || cfg.Level != BotLevelInvisible || cfg.Expire != 3600 || cfg.PolicyID != 0 || len(cfg.Pattern) != 0 {
		t.Fatalf("defaults = %+v", cfg)
	}

	fc.policy = map[string]any{
		"id": 9, "action": 2, "is_enabled": true, "level": 1, "expire": 7200,
		"replay": false, "negate": true,
		"pattern": [][]map[string]any{{{"k": "uri", "op": "prefix", "v": []string{"/admin"}, "sub_k": ""}}},
	}
	if err := c.SetSiteBot(4, true, 7200, true, [][]BotCondition{{{Key: "uri", Operator: "prefix", Value: []string{"/admin"}}}}); err != nil {
		t.Fatal(err)
	}
	body := fc.lastBodies["PUT /api/open/site/challenge"]
	if body["enable"] != true || body["id"] != float64(4) || body["level"] != float64(BotLevelInvisible) ||
		body["expire"] != float64(7200) || body["negate"] != true || body["replay"] != false {
		t.Errorf("challenge PUT body = %v", body)
	}
	if groups, _ := body["pattern"].([]any); len(groups) != 1 {
		t.Errorf("challenge PUT pattern = %v", body["pattern"])
	}

	got, err := c.GetSiteBot(4)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.PolicyID != 9 || got.Expire != 7200 || !got.Negate || len(got.Pattern) != 1 ||
		got.Pattern[0][0].Key != "uri" || got.Pattern[0][0].Value[0] != "/admin" {
		t.Fatalf("GetSiteBot after enable = %+v", got)
	}

	// Disabling keeps the stored policy and its settings.
	if err := c.SetSiteBot(4, false, got.Expire, got.Negate, got.Pattern); err != nil {
		t.Fatal(err)
	}
	body = fc.lastBodies["PUT /api/open/site/challenge"]
	if body["enable"] != false || body["expire"] != float64(7200) || body["negate"] != true {
		t.Errorf("disable body = %v", body)
	}
	got, err = c.GetSiteBot(4)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.PolicyID != 9 || len(got.Pattern) != 1 {
		t.Fatalf("GetSiteBot after disable = %+v", got)
	}

	// A nil pattern goes out as an empty list, not null.
	if err := c.SetSiteBot(4, true, 3600, false, nil); err != nil {
		t.Fatal(err)
	}
	body = fc.lastBodies["PUT /api/open/site/challenge"]
	if _, ok := body["pattern"].([]any); !ok {
		t.Errorf("nil pattern sent as %v, want []", body["pattern"])
	}
}

func TestValidateBotCondition(t *testing.T) {
	for _, ok := range []BotCondition{
		{Key: "uri", Operator: "prefix", Value: []string{"/admin"}},
		{Key: "src_ip", Operator: "cidr", Value: []string{"10.0.0.0/8"}},
		{Key: "src_ip", Operator: "not_cidr", Value: []string{"10.0.0.0/8"}},
		{Key: "method", Operator: "eq", Value: []string{"POST"}},
		{Key: "req_header", Operator: "has", SubKey: "User-Agent", Value: []string{"curl"}},
		{Key: "get_param", Operator: "exist", SubKey: "q"},
	} {
		if err := ValidateBotCondition(ok); err != nil {
			t.Errorf("ValidateBotCondition(%+v) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []BotCondition{
		{Key: "nope", Operator: "eq", Value: []string{"x"}},
		{Key: "uri", Operator: "cidr", Value: []string{"10.0.0.0/8"}},
		{Key: "uri", Operator: "eq"},
		{Key: "uri", Operator: "exist", Value: []string{"x"}},
		{Key: "req_header", Operator: "has", Value: []string{"curl"}},
		{Key: "post_param", Operator: "eq", Value: []string{"x"}},
		// Pro-only operators on src_ip and the console-only in/ja4_in
		// selectors are rejected before they reach the console.
		{Key: "src_ip", Operator: "geo_eq", Value: []string{"CN"}},
		{Key: "src_ip", Operator: "geo_not_eq", Value: []string{"CN"}},
		{Key: "src_ip", Operator: "asn_eq", Value: []string{"64512"}},
		{Key: "src_ip", Operator: "asn_not_eq", Value: []string{"64512"}},
		{Key: "src_ip", Operator: "in", Value: []string{"1"}},
		{Key: "src_ip", Operator: "not_in", Value: []string{"1"}},
		{Key: "ja4", Operator: "ja4_in", Value: []string{"t13d1516h2"}},
	} {
		if err := ValidateBotCondition(bad); err == nil {
			t.Errorf("ValidateBotCondition(%+v) accepted an invalid condition", bad)
		}
	}
}

func TestValidateWaitingRoom(t *testing.T) {
	if err := ValidateWaitingRoom(1, 1); err != nil {
		t.Errorf("minimum rejected: %v", err)
	}
	if err := ValidateWaitingRoom(5000, 30); err != nil {
		t.Errorf("maximum rejected: %v", err)
	}
	for _, tc := range [][2]int{{0, 3}, {5001, 3}, {5, 0}, {5, 31}} {
		if err := ValidateWaitingRoom(tc[0], tc[1]); err == nil {
			t.Errorf("ValidateWaitingRoom(%d, %d) accepted", tc[0], tc[1])
		}
	}
}

func TestValidateRateLimitRule(t *testing.T) {
	valid := RateLimitRule{Type: "req", Period: 10, Count: 1, Action: "block", BlockMin: 1}
	if err := ValidateRateLimitRule(valid); err != nil {
		t.Errorf("valid rule rejected: %v", err)
	}
	for _, mutate := range []func(*RateLimitRule){
		func(r *RateLimitRule) { r.Type = "other" },
		func(r *RateLimitRule) { r.Period = 9 },
		func(r *RateLimitRule) { r.Period = 601 },
		func(r *RateLimitRule) { r.Count = 0 },
		func(r *RateLimitRule) { r.Action = "allow" },
		func(r *RateLimitRule) { r.BlockMin = 0 },
		func(r *RateLimitRule) { r.BlockMin = 1441 },
	} {
		r := valid
		mutate(&r)
		if err := ValidateRateLimitRule(r); err == nil {
			t.Errorf("rule %+v accepted", r)
		}
	}
	if err := ValidateRateLimitAction("challenge_v2"); err != nil {
		t.Errorf("challenge_v2 rejected: %v", err)
	}
}

func TestNormalizeSourceIPMode(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"socket", SourceIPSocket},
		{"socket_ip", SourceIPSocket},
		{"socket-ip", SourceIPSocket},
		{"xff-first", SourceIPXFFFirst},
		{"xff_first", SourceIPXFFFirst},
		{"XFF-Second", SourceIPXFFSecond},
		{"xff-third", SourceIPXFFThird},
		{"xff-dynamic", SourceIPXFFDynamic},
		{"header", SourceIPHTTPHeader},
		{"http-header", SourceIPHTTPHeader},
		{"proxy-protocol", SourceIPProxyProtocol},
		{"proxy", SourceIPProxyProtocol},
	} {
		got, err := NormalizeSourceIPMode(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("NormalizeSourceIPMode(%q) = (%q, %v), want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "xff", "xff-fourth", "socket-ipv6"} {
		if _, err := NormalizeSourceIPMode(bad); err == nil {
			t.Errorf("NormalizeSourceIPMode(%q) accepted", bad)
		}
	}
}

func TestSourceIPValidation(t *testing.T) {
	if err := ValidateSourceIPHeader("X-Real-IP"); err != nil {
		t.Errorf("X-Real-IP rejected: %v", err)
	}
	for _, bad := range []string{"", "X Real IP", "X-Real-IP:", "头"} {
		if err := ValidateSourceIPHeader(bad); err == nil {
			t.Errorf("header %q accepted", bad)
		}
	}

	got, err := NormalizeTrustedProxies([]string{"10.0.0.0/8", " 192.168.1.1 ", ""})
	if err != nil || got != "10.0.0.0/8,192.168.1.1" {
		t.Errorf("NormalizeTrustedProxies = (%q, %v)", got, err)
	}
	for _, bad := range [][]string{nil, {""}, {"not-an-ip"}, {"300.1.1.1"}} {
		if _, err := NormalizeTrustedProxies(bad); err == nil {
			t.Errorf("trusted proxies %v accepted", bad)
		}
	}

	if got, err := BuildXFFSelector("from-end", 1); err != nil || got != `{"index":1,"position":"from_end"}` {
		t.Errorf("BuildXFFSelector(from-end, 1) = (%q, %v)", got, err)
	}
	if got, err := BuildXFFSelector("from_start", 100); err != nil || got != `{"index":100,"position":"from_start"}` {
		t.Errorf("BuildXFFSelector(from_start, 100) = (%q, %v)", got, err)
	}
	for _, tc := range []struct {
		position string
		index    int
	}{{"middle", 1}, {"from-end", 0}, {"from-end", 101}} {
		if _, err := BuildXFFSelector(tc.position, tc.index); err == nil {
			t.Errorf("BuildXFFSelector(%q, %d) accepted", tc.position, tc.index)
		}
	}
}

func TestSourceIPEndpoints(t *testing.T) {
	fc := newFakeConsole(t, "pw")
	c := newTestClient(t, fc)

	global, err := c.GetSourceIP(0)
	if err != nil {
		t.Fatal(err)
	}
	if global.Mode != SourceIPSocket || global.Value != "" || !global.FollowGlobal {
		t.Fatalf("global source IP = %+v", global)
	}
	if err := c.SetSourceIP(0, ProxyIPSource{Mode: SourceIPXFFFirst}); err != nil {
		t.Fatal(err)
	}
	body := fc.lastBodies["PUT /api/open/global/proxy"]
	if len(body) != 2 {
		t.Errorf("global PUT body = %v, want ip_source and ip_value only", body)
	}
	src, _ := body["ip_source"].(map[string]any)
	if src["value"] != SourceIPXFFFirst || src["global"] != false {
		t.Errorf("global ip_source = %v", body["ip_source"])
	}
	val, _ := body["ip_value"].(map[string]any)
	if val["value"] != "" || val["global"] != false {
		t.Errorf("global ip_value = %v", body["ip_value"])
	}

	site, err := c.GetSourceIP(4)
	if err != nil {
		t.Fatal(err)
	}
	if !site.FollowGlobal {
		t.Fatalf("site source IP = %+v, want follow-global", site)
	}
	if err := c.SetSourceIP(4, ProxyIPSource{Mode: SourceIPHTTPHeader, Value: "X-Real-IP", FollowGlobal: true}); err != nil {
		t.Fatal(err)
	}
	body = fc.lastBodies["PUT /api/open/site/4/proxy"]
	src, _ = body["ip_source"].(map[string]any)
	if src["value"] != SourceIPHTTPHeader || src["global"] != true {
		t.Errorf("site ip_source = %v", body["ip_source"])
	}
	val, _ = body["ip_value"].(map[string]any)
	if val["value"] != "X-Real-IP" {
		t.Errorf("site ip_value = %v", body["ip_value"])
	}
}
