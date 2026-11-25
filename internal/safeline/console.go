// Package safeline talks to a SafeLine (雷池) console's web API. It wraps the
// same /api/open endpoints the console UI uses: AES-encrypted admin login with
// a CSRF token, then site CRUD and upstream health checks.
package safeline

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Client is a logged-in (or about-to-log-in) console API client.
type Client struct {
	baseURL  string
	username string
	password string
	http     *http.Client
	token    string
}

// NewClient builds a client for a console reached at consoleURL (for example
// https://127.0.0.1:9443). The console serves HTTPS with a self-signed
// certificate, so verification is skipped; the proxy is disabled because the
// console is a local listener.
func NewClient(consoleURL, username, password string) *Client {
	return &Client{
		baseURL:  strings.TrimRight(consoleURL, "/"),
		username: username,
		password: password,
		http: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				Proxy:           nil,
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

// apiEnvelope is the console's uniform response wrapper. Success carries the
// payload in data with an empty msg; failures carry a message (and sometimes a
// non-null err) while data is an empty object.
type apiEnvelope struct {
	Data json.RawMessage `json:"data"`
	Err  any             `json:"err"`
	Msg  string          `json:"msg"`
}

func (c *Client) do(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	// The console reports business failures either as HTTP 200 or 500 with the
	// same envelope; prefer the envelope's human message over the status code,
	// and fall back to the machine-readable err when no message is set.
	var env apiEnvelope
	if json.Unmarshal(data, &env) == nil {
		if env.Msg != "" {
			return errors.New(env.Msg)
		}
		if env.Err != nil {
			b, _ := json.Marshal(env.Err)
			return fmt.Errorf("console API error: %s", string(b))
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)
		}
		if out != nil {
			if err := json.Unmarshal(env.Data, out); err != nil {
				return fmt.Errorf("%s %s: decoding data: %w", method, path, err)
			}
		}
		return nil
	}
	return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
}

// Login performs the encrypted admin login: fetch the AES key and a CSRF
// token, encrypt the password, and keep the JWT. The client's timeout bounds
// the whole exchange.
func (c *Client) Login() error {
	if c.password == "" {
		return fmt.Errorf("console admin password is not recorded — run 'pg addon start safeline --name <name>' to capture it, or 'pg addon password safeline --name <name> --reset' to rotate it")
	}
	var key string
	if err := c.do(http.MethodGet, "/api/open/system/key", nil, &key); err != nil {
		return err
	}
	var csrf struct {
		Token string `json:"csrf_token"`
	}
	if err := c.do(http.MethodGet, "/api/open/auth/csrf", nil, &csrf); err != nil {
		return err
	}
	enc, err := EncryptPassword(c.password, key)
	if err != nil {
		return err
	}
	var login struct {
		JWT string `json:"jwt"`
	}
	if err := c.do(http.MethodPost, "/api/open/auth/login", map[string]string{
		"username":   c.username,
		"password":   enc,
		"csrf_token": csrf.Token,
	}, &login); err != nil {
		return err
	}
	if login.JWT == "" {
		return fmt.Errorf("console login returned no token")
	}
	c.token = login.JWT
	return nil
}

// EncryptPassword reproduces the console UI's login encryption:
// base64(salt_hex || AES-128-CBC-PKCS7(password, key, IV = salt_hex ASCII)),
// where salt_hex is 16 hex characters from 8 random bytes and key is the
// 16-character string from /api/open/system/key.
func EncryptPassword(password, key string) (string, error) {
	if len(key) != 16 {
		return "", fmt.Errorf("console returned a %d-byte encryption key, want 16", len(key))
	}
	salt := make([]byte, 8)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	saltHex := hex.EncodeToString(salt)
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return "", err
	}
	pad := aes.BlockSize - len(password)%aes.BlockSize
	plain := make([]byte, len(password)+pad)
	copy(plain, password)
	for i := len(password); i < len(plain); i++ {
		plain[i] = byte(pad)
	}
	ct := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, []byte(saltHex)).CryptBlocks(ct, plain)
	return base64.StdEncoding.EncodeToString(append([]byte(saltHex), ct...)), nil
}

// Site is one protected application as returned by the console.
type Site struct {
	ID                  int                    `json:"id"`
	ServerNames         []string               `json:"server_names"`
	Ports               []string               `json:"ports"`
	Upstreams           []string               `json:"upstreams"`
	Comment             string                 `json:"comment"`
	IsEnabled           bool                   `json:"is_enabled"`
	Mode                int                    `json:"mode"`
	CertID              int                    `json:"cert_id"`
	HealthCheck         bool                   `json:"health_check"`
	HealthCheckInterval int                    `json:"health_check_interval"`
	HealthCheckPath     string                 `json:"health_check_path"`
	HealthState         map[string]HealthState `json:"health_state"`
	LoadBalance         *LoadBalance           `json:"load_balance,omitempty"`
}

// HealthState is the console's cached health view for one upstream.
type HealthState struct {
	State int    `json:"state"`
	Error string `json:"error"`
}

// LoadBalance is the site's balancing configuration.
type LoadBalance struct {
	BalanceType int          `json:"balance_type"`
	Upstreams   []LBUpstream `json:"upstreams,omitempty"`
}

// LBUpstream is one weighted upstream entry.
type LBUpstream struct {
	Address string `json:"address"`
	Weight  int    `json:"weight,omitempty"`
	Role    string `json:"role,omitempty"`
}

// EffectiveUpstreams returns the upstream addresses actually serving traffic:
// the load_balance entries when present, the plain upstreams otherwise.
func (s Site) EffectiveUpstreams() []string {
	if s.LoadBalance != nil && len(s.LoadBalance.Upstreams) > 0 {
		out := make([]string, 0, len(s.LoadBalance.Upstreams))
		for _, u := range s.LoadBalance.Upstreams {
			out = append(out, u.Address)
		}
		return out
	}
	return s.Upstreams
}

// ModeName maps the site run mode to its CLI name.
func ModeName(mode int) string {
	switch mode {
	case 0:
		return "defense"
	case 1:
		return "offline"
	case 2:
		return "dryrun"
	default:
		return strconv.Itoa(mode)
	}
}

var balanceTypes = map[string]int{
	"roundrobin": 1,
	"leastconn":  2,
	"iphash":     3,
	"backup":     4,
}

// ParseBalance maps a CLI balance name to the console enum.
func ParseBalance(name string) (int, error) {
	if t, ok := balanceTypes[strings.ToLower(name)]; ok {
		return t, nil
	}
	return 0, fmt.Errorf("unknown balance %q: use roundrobin, leastconn, iphash or backup", name)
}

// BalanceName maps the console enum to its CLI name.
func BalanceName(t int) string {
	for name, v := range balanceTypes {
		if v == t {
			return name
		}
	}
	return strconv.Itoa(t)
}

// NormalizeServerNames trims and de-duplicates names; an empty result means
// match-all and becomes "*" (the console behaves the same way).
func NormalizeServerNames(names []string) []string {
	out := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	if len(out) == 0 {
		return []string{"*"}
	}
	return out
}

// ValidateEntryPorts checks entry ports in the console's on-the-wire form:
// a TCP port, optionally suffixed _ssl for an HTTPS entry port.
func ValidateEntryPorts(ports []string) error {
	if len(ports) == 0 {
		return fmt.Errorf("at least one --port is required")
	}
	for _, p := range ports {
		raw := strings.TrimSuffix(p, "_ssl")
		if raw == p && strings.Contains(p, "_") {
			return fmt.Errorf("invalid port %q: use <port> or <port>_ssl", p)
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("invalid port %q: use a number between 1 and 65535, optionally suffixed _ssl", p)
		}
	}
	return nil
}

// ValidateUpstreams checks that every upstream is an absolute http(s) URL —
// tengine rejects anything else.
func ValidateUpstreams(upstreams []string) error {
	if len(upstreams) == 0 {
		return fmt.Errorf("at least one --upstream is required")
	}
	for _, u := range upstreams {
		parsed, err := url.Parse(u)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("invalid upstream %q: use an http:// or https:// URL with a host", u)
		}
	}
	return nil
}

// MergeUpstreams applies incremental upstream changes: every remove entry
// must currently be configured, and add entries already present are ignored.
// Order is preserved, removals first.
func MergeUpstreams(current, add, remove []string) ([]string, error) {
	out := make([]string, 0, len(current)+len(add))
	out = append(out, current...)
	for _, u := range remove {
		idx := slices.Index(out, u)
		if idx < 0 {
			return nil, fmt.Errorf("upstream %q is not configured", u)
		}
		out = append(out[:idx], out[idx+1:]...)
	}
	for _, u := range add {
		if !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	return out, nil
}

// ValidateHealthCheck mirrors the console form's rules for an enabled health
// check: interval 10-3600 seconds, path starting with /, no spaces or query
// parameters, and not under the console's internal /.safeline prefix.
func ValidateHealthCheck(interval int, path string) error {
	if interval < 10 || interval > 3600 {
		return fmt.Errorf("health check interval must be between 10 and 3600 seconds, got %d", interval)
	}
	return ValidateHealthCheckPath(path)
}

// ValidateHealthCheckPath checks the probe path syntax on its own.
func ValidateHealthCheckPath(path string) error {
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("health check path %q must start with /", path)
	}
	if strings.ContainsAny(path, " ?") {
		return fmt.Errorf("health check path %q must not contain spaces or query parameters", path)
	}
	if strings.HasPrefix(path, "/.safeline") {
		return fmt.Errorf("health check path %q must not start with /.safeline", path)
	}
	return nil
}

// SiteSpec is the create request.
type SiteSpec struct {
	ServerNames         []string
	Ports               []string
	Upstreams           []string
	Comment             string
	BalanceType         int
	CertID              int
	HealthCheck         bool
	HealthCheckInterval int
	HealthCheckPath     string
}

// ListSites returns every protected site.
func (c *Client) ListSites() ([]Site, error) {
	var l struct {
		Data []Site `json:"data"`
	}
	if err := c.do(http.MethodGet, "/api/open/site", nil, &l); err != nil {
		return nil, err
	}
	return l.Data, nil
}

// CreateSite registers a protected site and returns its id.
func (c *Client) CreateSite(spec SiteSpec) (int, error) {
	payload := map[string]any{
		"server_names":   spec.ServerNames,
		"ports":          spec.Ports,
		"upstreams":      spec.Upstreams,
		"comment":        spec.Comment,
		"email":          "",
		"cert_id":        spec.CertID,
		"load_balance":   map[string]any{"balance_type": spec.BalanceType},
		"health_check":   spec.HealthCheck,
		"index":          "",
		"static_default": 1,
		"type":           0,
	}
	if spec.HealthCheck {
		payload["health_check_interval"] = spec.HealthCheckInterval
		payload["health_check_path"] = spec.HealthCheckPath
	}
	var id int
	if err := c.do(http.MethodPost, "/api/open/site", payload, &id); err != nil {
		return 0, err
	}
	return id, nil
}

// DeleteSites removes sites by id.
func (c *Client) DeleteSites(ids []int) error {
	return c.do(http.MethodDelete, "/api/open/site", map[string]any{"ids": ids}, nil)
}

// HealthCheckPatch changes only the fields set to non-nil.
type HealthCheckPatch struct {
	Enabled  *bool
	Interval *int
	Path     *string
}

// PatchSiteHealthCheck updates a site's health check settings. The console
// replaces the whole site on PUT, so this fetches the current object, applies
// the patch and writes it back — the way the UI edits a site.
func (c *Client) PatchSiteHealthCheck(id int, p HealthCheckPatch) (*Site, error) {
	var raw map[string]any
	if err := c.do(http.MethodGet, fmt.Sprintf("/api/open/site/%d", id), nil, &raw); err != nil {
		return nil, err
	}
	if p.Enabled != nil {
		raw["health_check"] = *p.Enabled
	}
	if p.Interval != nil {
		raw["health_check_interval"] = *p.Interval
	}
	if p.Path != nil {
		raw["health_check_path"] = *p.Path
	}
	enabled, _ := raw["health_check"].(bool)
	if enabled {
		if interval := jsonInt(raw["health_check_interval"]); interval == 0 {
			raw["health_check_interval"] = 10
		}
		if path, _ := raw["health_check_path"].(string); path == "" {
			raw["health_check_path"] = "/"
		}
		if err := ValidateHealthCheck(jsonInt(raw["health_check_interval"]), fmt.Sprint(raw["health_check_path"])); err != nil {
			return nil, err
		}
	} else {
		raw["health_check_interval"] = 0
	}
	if err := c.do(http.MethodPut, "/api/open/site", raw, nil); err != nil {
		return nil, err
	}
	var updated Site
	if err := c.do(http.MethodGet, fmt.Sprintf("/api/open/site/%d", id), nil, &updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

func jsonInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

// Cert is one TLS certificate stored in the console. Type 2 certificates are
// uploaded PEM pairs, type 3 are ACME-issued.
type Cert struct {
	ID            int      `json:"id"`
	Domains       []string `json:"domains"`
	Issuer        string   `json:"issuer"`
	SelfSignature bool     `json:"self_signature"`
	Trusted       bool     `json:"trusted"`
	Revoked       bool     `json:"revoked"`
	Expired       bool     `json:"expired"`
	Type          int      `json:"type"`
	ACMEMessage   string   `json:"acme_message"`
	ValidBefore   string   `json:"valid_before"`
	RelatedSites  []string `json:"related_sites"`
}

// CertTypeName maps the certificate type enum to its CLI name.
func CertTypeName(t int) string {
	switch t {
	case 2:
		return "uploaded"
	case 3:
		return "acme"
	default:
		return strconv.Itoa(t)
	}
}

// LoadCertificate reads a PEM certificate/key pair and sanity-checks it before
// it is sent to the console.
func LoadCertificate(certPath, keyPath string) (crt, key string, err error) {
	crtB, err := os.ReadFile(certPath)
	if err != nil {
		return "", "", err
	}
	keyB, err := os.ReadFile(keyPath)
	if err != nil {
		return "", "", err
	}
	crt, key = string(crtB), string(keyB)
	if !strings.Contains(crt, "-----BEGIN CERTIFICATE-----") {
		return "", "", fmt.Errorf("%s does not contain a PEM certificate (-----BEGIN CERTIFICATE-----)", certPath)
	}
	if !strings.Contains(key, "PRIVATE KEY-----") {
		return "", "", fmt.Errorf("%s does not contain a PEM private key", keyPath)
	}
	return crt, key, nil
}

// ListCerts returns every certificate stored in the console.
func (c *Client) ListCerts() ([]Cert, error) {
	var l struct {
		Nodes []Cert `json:"nodes"`
	}
	if err := c.do(http.MethodGet, "/api/open/cert", nil, &l); err != nil {
		return nil, err
	}
	return l.Nodes, nil
}

// UploadCert stores an uploaded PEM certificate/key pair and returns its id.
func (c *Client) UploadCert(crtPEM, keyPEM string) (int, error) {
	payload := map[string]any{
		"type":   2,
		"manual": map[string]string{"crt": crtPEM, "key": keyPEM},
	}
	var id int
	if err := c.do(http.MethodPost, "/api/open/cert", payload, &id); err != nil {
		return 0, err
	}
	return id, nil
}

// DeleteCert removes a certificate; the console refuses while a site uses it.
func (c *Client) DeleteCert(id int) error {
	return c.do(http.MethodDelete, fmt.Sprintf("/api/open/cert/%d", id), nil, nil)
}

// UpdateCert replaces the PEM pair of an existing certificate in place. Every
// site bound to the certificate serves the new one after the console publishes
// its config, which is the renewal path: no site needs to be touched. The
// console exposes the update as a POST to the same collection as the upload,
// distinguished by the id in the body.
func (c *Client) UpdateCert(id int, crtPEM, keyPEM string) error {
	payload := map[string]any{
		"id":     id,
		"type":   2,
		"manual": map[string]string{"crt": crtPEM, "key": keyPEM},
	}
	return c.do(http.MethodPost, "/api/open/cert", payload, nil)
}

// GetSite fetches one protected site.
func (c *Client) GetSite(id int) (*Site, error) {
	var s Site
	if err := c.do(http.MethodGet, fmt.Sprintf("/api/open/site/%d", id), nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// SetSiteUpstreams replaces a site's upstream list. Like the health check
// patch this rewrites the whole site object, keeping every other field. When
// the site carries weighted load-balance entries the console serves those
// instead of the plain list, so their addresses are rewritten in step,
// keeping weight and role for addresses that stay.
func (c *Client) SetSiteUpstreams(id int, upstreams []string) (*Site, error) {
	var raw map[string]any
	if err := c.do(http.MethodGet, fmt.Sprintf("/api/open/site/%d", id), nil, &raw); err != nil {
		return nil, err
	}
	raw["upstreams"] = upstreams
	syncLoadBalanceUpstreams(raw, upstreams)
	if err := c.do(http.MethodPut, "/api/open/site", raw, nil); err != nil {
		return nil, err
	}
	var updated Site
	if err := c.do(http.MethodGet, fmt.Sprintf("/api/open/site/%d", id), nil, &updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

func syncLoadBalanceUpstreams(raw map[string]any, upstreams []string) {
	lb, _ := raw["load_balance"].(map[string]any)
	if lb == nil {
		return
	}
	old, _ := lb["upstreams"].([]any)
	if len(old) == 0 {
		return
	}
	byAddr := make(map[string]map[string]any, len(old))
	for _, e := range old {
		entry, _ := e.(map[string]any)
		if addr, _ := entry["address"].(string); addr != "" {
			byAddr[addr] = entry
		}
	}
	out := make([]any, 0, len(upstreams))
	for _, u := range upstreams {
		if entry, ok := byAddr[u]; ok {
			out = append(out, entry)
			continue
		}
		out = append(out, map[string]any{"address": u, "role": "primary", "weight": 1})
	}
	lb["upstreams"] = out
}

// SetSiteCertID binds another certificate to a site. Like the health check
// patch this rewrites the whole site object, keeping every other field.
func (c *Client) SetSiteCertID(id, certID int) (*Site, error) {
	var raw map[string]any
	if err := c.do(http.MethodGet, fmt.Sprintf("/api/open/site/%d", id), nil, &raw); err != nil {
		return nil, err
	}
	raw["cert_id"] = certID
	if err := c.do(http.MethodPut, "/api/open/site", raw, nil); err != nil {
		return nil, err
	}
	var updated Site
	if err := c.do(http.MethodGet, fmt.Sprintf("/api/open/site/%d", id), nil, &updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

// HealthCheck probes the given upstreams through the console. The result maps
// each upstream to "" when healthy or to an error string such as
// "unreachable". hosts are the Host headers used for the probe (the site's
// server names).
func (c *Client) HealthCheck(upstreams, hosts []string) (map[string]string, error) {
	var out map[string]string
	if err := c.do(http.MethodPost, "/api/open/site/healthcheck", map[string]any{
		"upstreams": upstreams,
		"hosts":     hosts,
	}, &out); err != nil {
		return nil, err
	}
	return out, nil
}
