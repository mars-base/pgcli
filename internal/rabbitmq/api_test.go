package rabbitmq

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func TestAPIHostAndBaseURL(t *testing.T) {
	cases := []struct {
		listen string
		host   string
		base   string
	}{
		{"", "127.0.0.1", "http://127.0.0.1:15672"},
		{"127.0.0.1", "127.0.0.1", "http://127.0.0.1:15672"},
		{"0.0.0.0", "127.0.0.1", "http://127.0.0.1:15672"},
		{"::", "::1", "http://[::1]:15672"},
		{"10.1.2.3", "10.1.2.3", "http://10.1.2.3:15672"},
	}
	for _, tt := range cases {
		c := config.RabbitMQConfig{Listen: tt.listen, ManagementPort: 15672}
		if got := APIHost(c); got != tt.host {
			t.Errorf("APIHost(%q) = %q, want %q", tt.listen, got, tt.host)
		}
		if got := APIBaseURL(c); got != tt.base {
			t.Errorf("APIBaseURL(%q) = %q, want %q", tt.listen, got, tt.base)
		}
	}
}

func TestQueueAPIPathEscapesSegments(t *testing.T) {
	if got := QueueAPIPath("/", "orders"); got != "/api/queues/%2F/orders" {
		t.Errorf("QueueAPIPath = %q", got)
	}
	if got := QueueAPIPath("prod env", "a/b"); got != "/api/queues/prod%20env/a%2Fb" {
		t.Errorf("QueueAPIPath = %q", got)
	}
	if got := bindingAPIPath("/", "events", "orders"); got != "/api/bindings/%2F/e/events/q/orders" {
		t.Errorf("bindingAPIPath = %q", got)
	}
}

func TestAPIClientRequests(t *testing.T) {
	type recorded struct {
		method string
		path   string
		auth   bool
		body   map[string]any
	}
	var got []recorded
	responses := map[string]string{
		"/api/queues/%2F/orders":                 `{"type":"classic","messages":3}`,
		"/api/queues/%2F/orders/bindings":        `[{"source":"events","destination":"orders","routing_key":"rk"}]`,
		"/api/exchanges/%2F/amq.default/publish": `{"routed":true}`,
		"/api/queues/%2F/orders/get":             `[{"payload":"hi","exchange":"amq.default","routing_key":"orders"}]`,
		"/api/bindings/%2F/e/events/q/orders":    `{"ok":true}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recorded{method: r.Method, path: r.URL.EscapedPath()}
		user, pass, ok := r.BasicAuth()
		rec.auth = ok && user == "admin" && pass == "secret"
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&rec.body)
		}
		got = append(got, rec)
		if body, ok := responses[r.URL.EscapedPath()]; ok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	api := &APIClient{BaseURL: srv.URL, User: "admin", Password: "secret"}

	if err := api.DeclareQueue("/", "orders", QueueDeclareOptions{
		Durable:   true,
		Arguments: map[string]any{"x-queue-type": "quorum"},
	}); err != nil {
		t.Fatalf("DeclareQueue: %v", err)
	}
	info, _, err := api.QueueInfo("/", "orders")
	if err != nil || info["type"] != "classic" {
		t.Fatalf("QueueInfo = %v, %v", info, err)
	}
	routed, err := api.Publish("/", "amq.default", "orders", "hello", map[string]any{"delivery_mode": float64(2)})
	if err != nil || !routed {
		t.Fatalf("Publish = %v, %v", routed, err)
	}
	msgs, _, err := api.Get("/", "orders", 5, "ack_requeue_true")
	if err != nil || len(msgs) != 1 || msgs[0].Payload != "hi" {
		t.Fatalf("Get = %+v, %v", msgs, err)
	}
	bindings, _, err := api.QueueBindings("/", "orders")
	if err != nil || len(bindings) != 1 || bindings[0].Source != "events" {
		t.Fatalf("QueueBindings = %+v, %v", bindings, err)
	}
	if err := api.Bind("/", "orders", "events", "rk", map[string]any{"x-match": "all"}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	want := []recorded{
		{method: http.MethodPut, path: "/api/queues/%2F/orders", body: map[string]any{"durable": true, "auto_delete": false, "arguments": map[string]any{"x-queue-type": "quorum"}}},
		{method: http.MethodGet, path: "/api/queues/%2F/orders"},
		{method: http.MethodPost, path: "/api/exchanges/%2F/amq.default/publish", body: map[string]any{"payload": "hello", "payload_encoding": "string", "routing_key": "orders", "properties": map[string]any{"delivery_mode": float64(2)}}},
		{method: http.MethodPost, path: "/api/queues/%2F/orders/get", body: map[string]any{"count": float64(5), "ackmode": "ack_requeue_true", "encoding": "auto", "truncate": float64(50000)}},
		{method: http.MethodGet, path: "/api/queues/%2F/orders/bindings"},
		{method: http.MethodPost, path: "/api/bindings/%2F/e/events/q/orders", body: map[string]any{"routing_key": "rk", "arguments": map[string]any{"x-match": "all"}}},
	}
	if len(got) != len(want) {
		t.Fatalf("recorded %d requests, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.method != w.method || g.path != w.path {
			t.Errorf("request %d = %s %s, want %s %s", i, g.method, g.path, w.method, w.path)
		}
		if !g.auth {
			t.Errorf("request %d missing basic auth", i)
		}
		if w.body != nil {
			gb, _ := json.Marshal(g.body)
			wb, _ := json.Marshal(w.body)
			if string(gb) != string(wb) {
				t.Errorf("request %d body = %s, want %s", i, gb, wb)
			}
		}
	}
}

func TestAPIErrorIncludesStatusAndReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Object Not Found","reason":"no queue 'nope' in vhost '/'"}`))
	}))
	defer srv.Close()

	api := &APIClient{BaseURL: srv.URL, User: "admin", Password: "secret"}
	_, _, err := api.QueueInfo("/", "nope")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"HTTP 404", "Object Not Found", "no queue 'nope'"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestUnbindMatchesBindings(t *testing.T) {
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/bindings"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"source":"events","destination":"orders","routing_key":"rk","arguments":{"x-match":"all"},"properties_key":"rk"},
				{"source":"events","destination":"orders","routing_key":"rk","arguments":{"x-match":"any"},"properties_key":"rk-2"},
				{"source":"other","destination":"orders","routing_key":"rk","properties_key":"rk"},
				{"source":"events","destination":"orders","routing_key":"","properties_key":"~"}
			]`))
		case r.Method == http.MethodDelete:
			deleted = append(deleted, r.URL.EscapedPath())
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
		}
	}))
	defer srv.Close()

	api := &APIClient{BaseURL: srv.URL, User: "admin", Password: "secret"}

	removed, err := api.Unbind("/", "orders", "events", "rk", nil)
	if err != nil || removed != 2 {
		t.Fatalf("Unbind all = %d, %v; want 2", removed, err)
	}
	want := []string{
		"/api/bindings/%2F/e/events/q/orders/rk",
		"/api/bindings/%2F/e/events/q/orders/rk-2",
	}
	if len(deleted) != len(want) || deleted[0] != want[0] || deleted[1] != want[1] {
		t.Fatalf("deleted = %v, want %v", deleted, want)
	}

	deleted = nil
	removed, err = api.Unbind("/", "orders", "events", "rk", map[string]any{"x-match": "any"})
	if err != nil || removed != 1 {
		t.Fatalf("Unbind with args = %d, %v; want 1", removed, err)
	}
	if len(deleted) != 1 || deleted[0] != "/api/bindings/%2F/e/events/q/orders/rk-2" {
		t.Fatalf("deleted = %v", deleted)
	}

	deleted = nil
	removed, err = api.Unbind("/", "orders", "events", "", nil)
	if err != nil || removed != 1 {
		t.Fatalf("Unbind empty routing key = %d, %v; want 1", removed, err)
	}
	if len(deleted) != 1 || deleted[0] != "/api/bindings/%2F/e/events/q/orders/~" {
		t.Fatalf("deleted = %v", deleted)
	}
}
