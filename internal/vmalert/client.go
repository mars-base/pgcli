package vmalert

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to one vmalert HTTP endpoint.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient returns a client for baseURL (e.g. http://127.0.0.1:8880). The
// transport deliberately ignores HTTP_PROXY: vmalert normally runs on the same
// host, and a proxy would make a loopback request fail in confusing ways.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http: &http.Client{
			Timeout:   10 * time.Second,
			Transport: &http.Transport{Proxy: nil},
		},
	}
}

// Reload requests an asynchronous configuration reload (GET /-/reload).
// A 2xx response only means the signal was accepted; reload happens through
// SIGHUP, and parse errors are reported in vmalert's log while the previous
// rules keep running. Callers must not treat success as validation.
func (c *Client) Reload(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/-/reload", nil)
	if err != nil {
		return fmt.Errorf("creating vmalert reload request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("requesting vmalert reload at %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("vmalert reload returned HTTP %d: %s", resp.StatusCode, truncateBody(body))
	}
	return nil
}

// Group is one rule group as reported by /api/v1/rules.
type Group struct {
	Name  string
	File  string
	Rules int
}

// Summary is the aggregate vmalert reports for its loaded rules.
type Summary struct {
	Groups    int
	Rules     int
	Firing    int
	Pending   int
	GroupList []Group
}

type rulesResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Groups []struct {
			Name  string `json:"name"`
			File  string `json:"file"`
			Rules []struct {
				Name  string `json:"name"`
				State string `json:"state"`
			} `json:"rules"`
		} `json:"groups"`
	} `json:"data"`
}

// RulesSummary fetches /api/v1/rules and aggregates groups, rules and alert
// states. vmalert's response is tolerant to unknown fields so version bumps do
// not break pgcli status.
func (c *Client) RulesSummary(ctx context.Context) (*Summary, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/rules", nil)
	if err != nil {
		return nil, fmt.Errorf("creating vmalert rules request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("querying vmalert rules at %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading vmalert rules response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("vmalert rules returned HTTP %d: %s", resp.StatusCode, truncateBody(body))
	}
	var parsed rulesResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parsing vmalert rules response: %w", err)
	}
	if parsed.Status != "success" {
		msg := parsed.Error
		if msg == "" {
			msg = "status=" + parsed.Status
		}
		return nil, fmt.Errorf("vmalert rules API error: %s", msg)
	}
	summary := &Summary{GroupList: make([]Group, 0, len(parsed.Data.Groups))}
	for _, group := range parsed.Data.Groups {
		summary.Groups++
		summary.GroupList = append(summary.GroupList, Group{
			Name:  group.Name,
			File:  group.File,
			Rules: len(group.Rules),
		})
		summary.Rules += len(group.Rules)
		for _, rule := range group.Rules {
			switch {
			case strings.EqualFold(rule.State, "firing"):
				summary.Firing++
			case strings.EqualFold(rule.State, "pending"):
				summary.Pending++
			}
		}
	}
	return summary, nil
}

func truncateBody(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 400 {
		s = s[:400] + "..."
	}
	if s == "" {
		return "(empty body)"
	}
	return s
}
