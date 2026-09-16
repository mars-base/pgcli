// Minimal etcd v3 client over the gRPC-gateway REST API. Only what the Patroni
// member registry needs: put, delete, prefix range, delete-prefix. Uses the
// stdlib so no new module dependency is added.
package podman

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type etcdClient struct {
	endpoints []string // host:port, scheme stripped
	hc        *http.Client
}

func newEtcdClient(endpoints []string) *etcdClient {
	eps := make([]string, 0, len(endpoints))
	for _, e := range endpoints {
		e = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(e, "http://"), "https://"))
		if e != "" {
			eps = append(eps, e)
		}
	}
	return &etcdClient{
		endpoints: eps,
		// Proxy: nil — etcd is a direct TCP peer; an HTTP(S)_PROXY env var
		// (e.g. a corporate privoxy) must never intercept these requests.
		hc: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &http.Transport{Proxy: nil},
		},
	}
}

type etcdRangeResponse struct {
	Kvs []struct {
		Key   string `json:"key"`   // base64
		Value string `json:"value"` // base64
	} `json:"kvs"`
}

// post tries each endpoint in order; returns the raw JSON response body.
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
		resp, err := e.hc.Post("http://"+ep+path, "application/json", bytes.NewReader(payload))
		if err != nil {
			lastErr = err
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("etcd %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(data)))
			continue
		}
		return data, nil
	}
	return nil, fmt.Errorf("etcd %s unreachable on all %d endpoints: %w", path, len(e.endpoints), lastErr)
}

func (e *etcdClient) put(key, value string) error {
	_, err := e.post("/v3/kv/put", map[string]string{
		"key":   base64.StdEncoding.EncodeToString([]byte(key)),
		"value": base64.StdEncoding.EncodeToString([]byte(value)),
	})
	return err
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
