package vmalert

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReload(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"200", http.StatusOK, "", ""},
		{"204", http.StatusNoContent, "", ""},
		{"500", http.StatusInternalServerError, "boom", "HTTP 500"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/-/reload" {
					t.Errorf("path = %q, want /-/reload", r.URL.Path)
				}
				if r.Method != http.MethodGet {
					t.Errorf("method = %q, want GET", r.Method)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			err := NewClient(srv.URL + "/").Reload(context.Background())
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
			}
		})
	}
}

func TestReloadConnectionFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	if err := NewClient(url).Reload(context.Background()); err == nil {
		t.Fatal("expected connection error")
	}
}

func TestRulesSummary(t *testing.T) {
	const body = `{
	  "status": "success",
	  "data": {
	    "groups": [
	      {"name": "victoria-logs", "file": "/etc/vmalert/rules/alert.yaml",
	       "rules": [
	         {"name": "found-999-log", "state": "firing"},
	         {"name": "no-logs", "state": "pending"},
	         {"name": "quiet", "state": "inactive"}
	       ]},
	      {"name": "metrics", "file": "/etc/vmalert/rules/metrics.yaml",
	       "rules": [{"name": "up", "state": "firing"}]}
	    ]
	  },
	  "extra": {"ignored": true}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/rules" {
			t.Errorf("path = %q, want /api/v1/rules", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	got, err := NewClient(srv.URL).RulesSummary(context.Background())
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if got.Groups != 2 || got.Rules != 4 || got.Firing != 2 || got.Pending != 1 {
		t.Errorf("summary = %+v, want groups=2 rules=4 firing=2 pending=1", got)
	}
	if len(got.GroupList) != 2 || got.GroupList[0].Name != "victoria-logs" || got.GroupList[0].Rules != 3 {
		t.Errorf("group list = %+v", got.GroupList)
	}
}

func TestRulesSummaryEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"groups":[]}}`))
	}))
	defer srv.Close()
	got, err := NewClient(srv.URL).RulesSummary(context.Background())
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if got.Groups != 0 || got.Rules != 0 || len(got.GroupList) != 0 {
		t.Errorf("summary = %+v, want all zero", got)
	}
}

func TestRulesSummaryErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"api error", http.StatusOK, `{"status":"error","error":"cannot load rules"}`, "cannot load rules"},
		{"bad json", http.StatusOK, `{`, "parsing"},
		{"http error", http.StatusServiceUnavailable, `unavailable`, "HTTP 503"},
		{"error status without message", http.StatusOK, `{"status":"error"}`, "status=error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			_, err := NewClient(srv.URL).RulesSummary(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
