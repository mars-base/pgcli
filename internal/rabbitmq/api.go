package rabbitmq

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"time"

	"github.com/mars-base/pgcli/internal/config"
)

// APIHost is the address the host-side management API client dials. An
// unspecified bind (0.0.0.0 or ::) is not a dialable destination, so it maps
// to the matching loopback address.
func APIHost(c config.RabbitMQConfig) string {
	if c.Listen == "" {
		return "127.0.0.1"
	}
	if ip := net.ParseIP(c.Listen); ip != nil && ip.IsUnspecified() {
		if ip.To4() == nil {
			return "::1"
		}
		return "127.0.0.1"
	}
	return c.Listen
}

// APIBaseURL is the plaintext management endpoint. TLS is additive, so the
// HTTP listener exists even when --tls is on.
func APIBaseURL(c config.RabbitMQConfig) string {
	return "http://" + net.JoinHostPort(APIHost(c), strconv.Itoa(c.ManagementPort))
}

// APIClient talks to RabbitMQ's management HTTP API as the configured admin.
type APIClient struct {
	BaseURL  string
	User     string
	Password string
	HTTP     *http.Client
}

// NewAPIClient builds a client for an instance's management API.
func NewAPIClient(c config.RabbitMQConfig) *APIClient {
	return &APIClient{
		BaseURL:  APIBaseURL(c),
		User:     c.AdminUser,
		Password: c.AdminPassword,
		HTTP:     &http.Client{Timeout: 30 * time.Second},
	}
}

func apiEscape(s string) string { return url.PathEscape(s) }

// QueueAPIPath is the management API path for one queue.
func QueueAPIPath(vhost, name string) string {
	return "/api/queues/" + apiEscape(vhost) + "/" + apiEscape(name)
}

func exchangeAPIPath(vhost, exchange string) string {
	return "/api/exchanges/" + apiEscape(vhost) + "/" + apiEscape(exchange)
}

func bindingAPIPath(vhost, exchange, queue string) string {
	return "/api/bindings/" + apiEscape(vhost) + "/e/" + apiEscape(exchange) + "/q/" + apiEscape(queue)
}

func (a *APIClient) do(method, path string, body, out any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding request: %w", err)
		}
		rdr = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, a.BaseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(a.User, a.Password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := a.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling management API %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("reading management API response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, fmt.Errorf("management API %s %s: HTTP %d: %s", method, path, resp.StatusCode, apiErrorText(data))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return data, fmt.Errorf("decoding management API response: %w", err)
		}
	}
	return data, nil
}

func apiErrorText(data []byte) string {
	var body struct {
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(data, &body); err == nil && (body.Error != "" || body.Reason != "") {
		switch {
		case body.Error != "" && body.Reason != "":
			return body.Error + ": " + body.Reason
		case body.Error != "":
			return body.Error
		default:
			return body.Reason
		}
	}
	text := string(bytes.TrimSpace(data))
	if text == "" {
		return "(empty response)"
	}
	return text
}

// QueueDeclareOptions mirrors the fields the management API accepts for a
// queue declaration.
type QueueDeclareOptions struct {
	Durable    bool
	AutoDelete bool
	Arguments  map[string]any
}

// DeclareQueue creates a queue (or verifies an identical existing one).
func (a *APIClient) DeclareQueue(vhost, name string, opts QueueDeclareOptions) error {
	body := map[string]any{
		"durable":     opts.Durable,
		"auto_delete": opts.AutoDelete,
	}
	if len(opts.Arguments) > 0 {
		body["arguments"] = opts.Arguments
	}
	_, err := a.do(http.MethodPut, QueueAPIPath(vhost, name), body, nil)
	return err
}

// QueueInfo returns the decoded queue document and the raw JSON.
func (a *APIClient) QueueInfo(vhost, name string) (map[string]any, []byte, error) {
	var out map[string]any
	raw, err := a.do(http.MethodGet, QueueAPIPath(vhost, name), nil, &out)
	return out, raw, err
}

// Publish sends one message through an exchange. The default exchange is
// "amq.default"; with it the routing key selects the queue.
func (a *APIClient) Publish(vhost, exchange, routingKey, payload string, properties map[string]any) (bool, error) {
	if properties == nil {
		properties = map[string]any{}
	}
	body := map[string]any{
		"properties":       properties,
		"routing_key":      routingKey,
		"payload":          payload,
		"payload_encoding": "string",
	}
	var out struct {
		Routed bool `json:"routed"`
	}
	_, err := a.do(http.MethodPost, exchangeAPIPath(vhost, exchange)+"/publish", body, &out)
	return out.Routed, err
}

// MessageProperties accepts the management API's empty-property encodings:
// messages without properties come back as [] rather than {}.
type MessageProperties map[string]any

func (p *MessageProperties) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || string(data) == "[]" {
		*p = nil
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	*p = m
	return nil
}

// Message is one message returned by the get endpoint.
type Message struct {
	Payload         string            `json:"payload"`
	PayloadEncoding string            `json:"payload_encoding"`
	Exchange        string            `json:"exchange"`
	RoutingKey      string            `json:"routing_key"`
	Redelivered     bool              `json:"redelivered"`
	Properties      MessageProperties `json:"properties"`
	MessageCount    int               `json:"message_count"`
}

// Get fetches messages from a queue. ackmode is one of ack_requeue_true
// (peek), ack_requeue_false (consume) or reject_requeue_*.
func (a *APIClient) Get(vhost, name string, count int, ackmode string) ([]Message, []byte, error) {
	body := map[string]any{
		"count":    count,
		"ackmode":  ackmode,
		"encoding": "auto",
		"truncate": 50000,
	}
	var out []Message
	raw, err := a.do(http.MethodPost, QueueAPIPath(vhost, name)+"/get", body, &out)
	return out, raw, err
}

// Binding is one binding attached to a queue.
type Binding struct {
	Source          string         `json:"source"`
	Destination     string         `json:"destination"`
	DestinationType string         `json:"destination_type"`
	RoutingKey      string         `json:"routing_key"`
	Arguments       map[string]any `json:"arguments"`
	PropertiesKey   string         `json:"properties_key"`
}

// QueueBindings lists the bindings that deliver to a queue.
func (a *APIClient) QueueBindings(vhost, name string) ([]Binding, []byte, error) {
	var out []Binding
	raw, err := a.do(http.MethodGet, QueueAPIPath(vhost, name)+"/bindings", nil, &out)
	return out, raw, err
}

// Bind creates a binding from an exchange to a queue.
func (a *APIClient) Bind(vhost, queue, exchange, routingKey string, args map[string]any) error {
	body := map[string]any{"routing_key": routingKey}
	if len(args) > 0 {
		body["arguments"] = args
	}
	_, err := a.do(http.MethodPost, bindingAPIPath(vhost, exchange, queue), body, nil)
	return err
}

// Unbind deletes every binding from exchange to queue with the given routing
// key (and, when args is non-nil, exactly those arguments). It returns the
// number of bindings removed.
func (a *APIClient) Unbind(vhost, queue, exchange, routingKey string, args map[string]any) (int, error) {
	bindings, _, err := a.QueueBindings(vhost, queue)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, b := range bindings {
		if b.Source != exchange || b.RoutingKey != routingKey {
			continue
		}
		if args != nil && !reflect.DeepEqual(b.Arguments, args) {
			continue
		}
		props := b.PropertiesKey
		if props == "" {
			props = "~"
		}
		path := bindingAPIPath(vhost, exchange, queue) + "/" + apiEscape(props)
		if _, err := a.do(http.MethodDelete, path, nil, nil); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
