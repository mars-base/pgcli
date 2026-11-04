// Minimal etcd v3 client over the gRPC-gateway REST API. Only what the Patroni
// member registry needs: put, delete, prefix range, delete-prefix. Uses the
// stdlib so no new module dependency is added.
package podman

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// EtcdClientOpts configures TLS and username/password authentication for the
// REST client. Zero value keeps the historical plaintext, unauthenticated
// behaviour.
type EtcdClientOpts struct {
	CAFile             string // PEM CA bundle; empty = system roots
	InsecureSkipVerify bool   // TOFU bootstrap: fetch a cluster CA before trusting it
	Username           string
	Password           string
}

type etcdEndpoint struct {
	scheme   string
	hostport string
}

type etcdClient struct {
	endpoints []etcdEndpoint
	hc        *http.Client
	username  string
	password  string
	token     string // cached /v3/auth/authenticate token
}

func newEtcdClient(endpoints []string) *etcdClient {
	c, _ := newEtcdClientWithOpts(endpoints, EtcdClientOpts{})
	return c
}

func newEtcdClientWithOpts(endpoints []string, opts EtcdClientOpts) (*etcdClient, error) {
	defaultScheme := "http"
	if opts.CAFile != "" || opts.InsecureSkipVerify {
		defaultScheme = "https"
	}
	eps := make([]etcdEndpoint, 0, len(endpoints))
	for _, raw := range endpoints {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		scheme := defaultScheme
		switch {
		case strings.HasPrefix(raw, "https://"):
			scheme = "https"
			raw = strings.TrimPrefix(raw, "https://")
		case strings.HasPrefix(raw, "http://"):
			scheme = "http"
			raw = strings.TrimPrefix(raw, "http://")
		}
		raw = strings.TrimSuffix(raw, "/")
		eps = append(eps, etcdEndpoint{scheme: scheme, hostport: raw})
	}

	var transport *http.Transport
	if opts.CAFile != "" || opts.InsecureSkipVerify {
		tlsCfg := &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: opts.InsecureSkipVerify,
		}
		if opts.CAFile != "" {
			pemData, err := os.ReadFile(opts.CAFile)
			if err != nil {
				return nil, fmt.Errorf("reading etcd CA %s: %w", opts.CAFile, err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pemData) {
				return nil, fmt.Errorf("etcd CA %s contains no usable certificates", opts.CAFile)
			}
			tlsCfg.RootCAs = pool
		}
		transport = &http.Transport{Proxy: nil, TLSClientConfig: tlsCfg}
	} else {
		transport = &http.Transport{Proxy: nil}
	}

	return &etcdClient{
		endpoints: eps,
		// Proxy: nil — etcd is a direct TCP peer; an HTTP(S)_PROXY env var
		// (e.g. a corporate privoxy) must never intercept these requests.
		hc: &http.Client{
			Timeout:   5 * time.Second,
			Transport: transport,
		},
		username: opts.Username,
		password: opts.Password,
	}, nil
}

type etcdRangeResponse struct {
	Kvs []struct {
		Key   string `json:"key"`   // base64
		Value string `json:"value"` // base64
	} `json:"kvs"`
}

// post tries each endpoint in order; returns the raw JSON response body. With
// credentials configured it authenticates first, caches the token, and retries
// once after a 401 (an expired/invalidated token).
func (e *etcdClient) post(path string, body any) ([]byte, error) {
	if len(e.endpoints) == 0 {
		return nil, fmt.Errorf("no etcd endpoints configured")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, ep := range e.endpoints {
		for attempt := 0; attempt < 2; attempt++ {
			if e.username != "" && e.token == "" {
				if err := e.authenticate(); err != nil {
					lastErr = err
					break
				}
			}
			data, status, err := e.doPost(ep, path, payload)
			if err != nil {
				lastErr = err
				break
			}
			if status == http.StatusUnauthorized && e.username != "" && attempt == 0 {
				e.token = ""
				continue
			}
			if status != http.StatusOK {
				lastErr = fmt.Errorf("etcd %s: HTTP %d: %s", path, status, strings.TrimSpace(string(data)))
				break
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("etcd %s unreachable on all %d endpoints: %w", path, len(e.endpoints), lastErr)
}

func (e *etcdClient) doPost(ep etcdEndpoint, path string, payload []byte) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodPost, ep.scheme+"://"+ep.hostport+path, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.token != "" {
		req.Header.Set("Authorization", e.token)
	}
	resp, err := e.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, 0, err
	}
	return data, resp.StatusCode, nil
}

// authenticate exchanges the configured username/password for an etcd token.
func (e *etcdClient) authenticate() error {
	payload, err := json.Marshal(map[string]string{"name": e.username, "password": e.password})
	if err != nil {
		return err
	}
	var lastErr error
	for _, ep := range e.endpoints {
		data, status, err := e.doPost(ep, "/v3/auth/authenticate", payload)
		if err != nil {
			lastErr = err
			continue
		}
		if status != http.StatusOK {
			lastErr = fmt.Errorf("etcd auth: HTTP %d: %s", status, strings.TrimSpace(string(data)))
			continue
		}
		var resp struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			lastErr = fmt.Errorf("parsing etcd auth response: %w", err)
			continue
		}
		if resp.Token == "" {
			lastErr = fmt.Errorf("etcd auth returned an empty token")
			continue
		}
		e.token = resp.Token
		return nil
	}
	return fmt.Errorf("etcd authenticate unreachable on all %d endpoints: %w", len(e.endpoints), lastErr)
}

func (e *etcdClient) put(key, value string) error {
	_, err := e.post("/v3/kv/put", map[string]string{
		"key":   base64.StdEncoding.EncodeToString([]byte(key)),
		"value": base64.StdEncoding.EncodeToString([]byte(value)),
	})
	return err
}

// putMany atomically writes every key/value pair in one transaction. With no
// compare clause the transaction always commits, so either all writes land or
// the request fails as a whole.
func (e *etcdClient) putMany(values map[string]string) error {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ops := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		ops = append(ops, map[string]any{
			"request_put": map[string]string{
				"key":   base64.StdEncoding.EncodeToString([]byte(key)),
				"value": base64.StdEncoding.EncodeToString([]byte(values[key])),
			},
		})
	}
	_, err := e.post("/v3/kv/txn", map[string]any{"success": ops})
	return err
}

func (e *etcdClient) get(key string) (string, bool, error) {
	data, err := e.post("/v3/kv/range", map[string]string{
		"key": base64.StdEncoding.EncodeToString([]byte(key)),
	})
	if err != nil {
		return "", false, err
	}
	var resp etcdRangeResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", false, fmt.Errorf("parsing etcd range response: %w", err)
	}
	if len(resp.Kvs) == 0 {
		return "", false, nil
	}
	value, err := base64.StdEncoding.DecodeString(resp.Kvs[0].Value)
	if err != nil {
		return "", false, fmt.Errorf("decoding etcd value: %w", err)
	}
	return string(value), true, nil
}

func (e *etcdClient) delete(key string) error {
	_, err := e.post("/v3/kv/deleterange", map[string]string{
		"key": base64.StdEncoding.EncodeToString([]byte(key)),
	})
	return err
}

func (e *etcdClient) deletePrefix(prefix string) error {
	_, err := e.post("/v3/kv/deleterange", map[string]string{
		"key":       base64.StdEncoding.EncodeToString([]byte(prefix)),
		"range_end": base64.StdEncoding.EncodeToString(prefixEnd([]byte(prefix))),
	})
	return err
}

// getPrefix returns every key/value under prefix, keyed by the full key.
func (e *etcdClient) getPrefix(prefix string) (map[string]string, error) {
	data, err := e.post("/v3/kv/range", map[string]string{
		"key":       base64.StdEncoding.EncodeToString([]byte(prefix)),
		"range_end": base64.StdEncoding.EncodeToString(prefixEnd([]byte(prefix))),
	})
	if err != nil {
		return nil, err
	}
	var resp etcdRangeResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parsing etcd range response: %w", err)
	}
	out := make(map[string]string, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		k, err := base64.StdEncoding.DecodeString(kv.Key)
		if err != nil {
			continue
		}
		v, err := base64.StdEncoding.DecodeString(kv.Value)
		if err != nil {
			continue
		}
		out[string(k)] = string(v)
	}
	return out, nil
}

// EtcdGetValue reads a single key through the etcd v3 REST gateway. The
// endpoint list is host:port (an http:// prefix is tolerated) and is tried in
// order. found is false when the key does not exist.
func EtcdGetValue(endpoints []string, key string) (value string, found bool, err error) {
	return newEtcdClient(endpoints).get(key)
}

// EtcdGetValueWith is EtcdGetValue with TLS/auth options.
func EtcdGetValueWith(endpoints []string, key string, opts EtcdClientOpts) (value string, found bool, err error) {
	c, err := newEtcdClientWithOpts(endpoints, opts)
	if err != nil {
		return "", false, err
	}
	return c.get(key)
}

// EtcdPutValues writes key/value pairs atomically in one transaction.
func EtcdPutValues(endpoints []string, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	return newEtcdClient(endpoints).putMany(values)
}

// EtcdPutValuesWith is EtcdPutValues with TLS/auth options.
func EtcdPutValuesWith(endpoints []string, values map[string]string, opts EtcdClientOpts) error {
	if len(values) == 0 {
		return nil
	}
	c, err := newEtcdClientWithOpts(endpoints, opts)
	if err != nil {
		return err
	}
	return c.putMany(values)
}

// EtcdUserAdd creates an etcd user through the v3 REST gateway. Unlike
// `etcdctl user add name:password`, the password never appears in a process
// argument list. Only valid while authentication is disabled (or with root
// credentials supplied in opts when it is on).
func EtcdUserAdd(endpoints []string, name, password string, opts EtcdClientOpts) error {
	c, err := newEtcdClientWithOpts(endpoints, opts)
	if err != nil {
		return err
	}
	_, err = c.post("/v3/auth/user/add", map[string]string{
		"name":     name,
		"password": password,
	})
	return err
}

// EtcdAuthStatusWith reports whether authentication is enabled on the cluster
// reachable at endpoints. It needs no credentials while auth is off; once auth
// is on it fails with HTTP 401 unless opts carries root credentials.
func EtcdAuthStatusWith(endpoints []string, opts EtcdClientOpts) (bool, error) {
	c, err := newEtcdClientWithOpts(endpoints, opts)
	if err != nil {
		return false, err
	}
	data, err := c.post("/v3/auth/status", map[string]any{})
	if err != nil {
		return false, err
	}
	var resp struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return false, fmt.Errorf("parsing etcd auth status response: %w", err)
	}
	return resp.Enabled, nil
}

// EtcdUserChangePassword sets newPassword for name. opts must carry credentials
// that can manage users (the old ones, for a self-service rotation).
func EtcdUserChangePassword(endpoints []string, name, newPassword string, opts EtcdClientOpts) error {
	c, err := newEtcdClientWithOpts(endpoints, opts)
	if err != nil {
		return err
	}
	_, err = c.post("/v3/auth/user/changepw", map[string]string{
		"name":     name,
		"password": newPassword,
	})
	return err
}

// prefixEnd computes the exclusive upper bound for an etcd prefix range:
// increment the last non-0xff byte and truncate. All-0xff (or empty)
// degenerates to {0x00}, etcd's sentinel for "no upper bound".
func prefixEnd(prefix []byte) []byte {
	end := make([]byte, len(prefix))
	copy(end, prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xff {
			end[i]++
			return end[:i+1]
		}
	}
	return []byte{0}
}
