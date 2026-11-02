package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func TestDocumentDBUserSpec(t *testing.T) {
	action, raw, err := documentDBUserSpec("docdb_admin", "secret", false)
	if err != nil {
		t.Fatalf("create spec: %v", err)
	}
	if action != "create_user" {
		t.Errorf("action = %q, want create_user", action)
	}
	var create map[string]any
	if err := json.Unmarshal(raw, &create); err != nil {
		t.Fatalf("decode create spec: %v", err)
	}
	if create["createUser"] != "docdb_admin" || create["pwd"] != "secret" || create["$db"] != "admin" {
		t.Errorf("unexpected create spec: %s", raw)
	}
	roles, ok := create["roles"].([]any)
	if !ok || len(roles) != 2 {
		t.Fatalf("roles = %#v, want two entries", create["roles"])
	}
	got := map[string]bool{}
	for _, r := range roles {
		m := r.(map[string]any)
		got[m["role"].(string)] = true
		if m["db"] != "admin" {
			t.Errorf("role %v db = %v, want admin", m["role"], m["db"])
		}
	}
	if !got["readWriteAnyDatabase"] || !got["clusterAdmin"] {
		t.Errorf("roles = %v, want readWriteAnyDatabase + clusterAdmin", got)
	}

	action, raw, err = documentDBUserSpec("docdb_admin", "rotated", true)
	if err != nil {
		t.Fatalf("update spec: %v", err)
	}
	if action != "update_user" {
		t.Errorf("action = %q, want update_user", action)
	}
	var update map[string]any
	if err := json.Unmarshal(raw, &update); err != nil {
		t.Fatalf("decode update spec: %v", err)
	}
	if update["updateUser"] != "docdb_admin" || update["pwd"] != "rotated" {
		t.Errorf("unexpected update spec: %s", raw)
	}
	if _, ok := update["roles"]; ok {
		t.Errorf("update spec must not carry roles: %s", raw)
	}
}

func TestDocumentDBEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		listen   string
		tlsMode  string
		password string
		want     string
	}{
		{
			name:    "wildcard allow",
			listen:  "0.0.0.0",
			tlsMode: "allow",
			want:    "mongodb://docdb_admin@127.0.0.1:10260/app?directConnection=true",
		},
		{
			name:     "loopback require",
			listen:   "127.0.0.1",
			tlsMode:  "require",
			password: "p@ss",
			want:     "mongodb://docdb_admin:p%40ss@127.0.0.1:10260/app?directConnection=true&tls=true&tlsAllowInvalidCertificates=true",
		},
		{
			name:    "ipv6 loopback",
			listen:  "::1",
			tlsMode: "allow",
			want:    "mongodb://docdb_admin@[::1]:10260/app?directConnection=true",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := &config.DocumentDBConfig{
				ClientUser: "docdb_admin",
				HostPort:   10260,
				Listen:     tt.listen,
				Database:   "app",
				TLSMode:    tt.tlsMode,
			}
			if got := documentDBEndpoint(dc, tt.password); got != tt.want {
				t.Errorf("documentDBEndpoint = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGenerateDocumentDBPassword(t *testing.T) {
	seen := map[string]bool{}
	for range 20 {
		pw, err := generateDocumentDBPassword()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if len(pw) != 24 {
			t.Fatalf("password length = %d, want 24", len(pw))
		}
		if strings.ContainsAny(pw, "+/=") || strings.ContainsAny(pw, " \t\n") {
			t.Fatalf("password %q is not URL-safe plain text", pw)
		}
		if seen[pw] {
			t.Fatalf("duplicate password generated: %q", pw)
		}
		seen[pw] = true
	}
}
