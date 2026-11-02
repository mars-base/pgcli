package cli

import (
	"net/url"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func TestParseDocumentDBRemoteURI(t *testing.T) {
	r, err := parseDocumentDBRemoteURI("mongodb://docdb_admin:secret@10.0.0.5:10260/app?retryWrites=true", "", "")
	if err != nil {
		t.Fatalf("parseDocumentDBRemoteURI: %v", err)
	}
	if r.host != "10.0.0.5" || r.port != 10260 || r.username != "docdb_admin" || r.database != "app" {
		t.Fatalf("unexpected remote: %+v", r)
	}

	u := mustParseMongoURI(t, r.uriString("", false))
	if u.Path != "/app" {
		t.Errorf("path = %q, want /app", u.Path)
	}
	q := u.Query()
	wantQuery := map[string]string{
		"authSource":               "admin",
		"directConnection":         "true",
		"serverSelectionTimeoutMS": "10000",
		"retryWrites":              "true",
	}
	for key, want := range wantQuery {
		if got := q.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if pw, _ := u.User.Password(); pw != "secret" {
		t.Errorf("password = %q, want secret", pw)
	}

	override := mustParseMongoURI(t, r.uriString("other", true))
	if override.Path != "/other" {
		t.Errorf("override path = %q, want /other", override.Path)
	}
	oq := override.Query()
	if oq.Get("tls") != "true" || oq.Get("tlsAllowInvalidCertificates") != "true" {
		t.Errorf("--tls did not add TLS parameters: %q", override.RawQuery)
	}
}

func TestParseDocumentDBRemoteURIEnvCredentials(t *testing.T) {
	r, err := parseDocumentDBRemoteURI("mongodb://10.0.0.5:10260/app", "envuser", "envpass")
	if err != nil {
		t.Fatalf("parse with env credentials: %v", err)
	}
	if r.username != "envuser" {
		t.Errorf("username = %q, want envuser", r.username)
	}
	u := mustParseMongoURI(t, r.uriString("", false))
	if pw, ok := u.User.Password(); !ok || pw != "envpass" {
		t.Errorf("env password not applied: %v", u.User)
	}

	r, err = parseDocumentDBRemoteURI("mongodb://envuser@10.0.0.5:10260/app", "", "envpass")
	if err != nil {
		t.Fatalf("parse with env password: %v", err)
	}
	u = mustParseMongoURI(t, r.uriString("", false))
	if pw, ok := u.User.Password(); !ok || pw != "envpass" {
		t.Errorf("env password not added to URI userinfo: %v", u.User)
	}
}

func TestParseDocumentDBRemoteURIErrors(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want string
	}{
		{"scheme", "http://host:10260/app", "unsupported MongoDB URI scheme"},
		{"no host", "mongodb://", "no host"},
		{"invalid port", "mongodb://host:99999/app", "invalid port"},
		{"multiple hosts", "mongodb://h1:10260,h2:10260/app", "single host"},
		{"database path", "mongodb://host:10260/a/b", "single database name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseDocumentDBRemoteURI(tt.uri, "", "")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestDocumentDBRemoteHostURI(t *testing.T) {
	r := &documentDBRemote{host: "10.0.0.5", port: 10260, username: "docdb_admin", password: "p@ss"}
	got := r.uriString("app", true)
	want := "mongodb://docdb_admin:p%40ss@10.0.0.5:10260/app?authSource=admin&directConnection=true&serverSelectionTimeoutMS=10000&tls=true&tlsAllowInvalidCertificates=true"
	if got != want {
		t.Errorf("uriString:\n got %q\nwant %q", got, want)
	}
}

func TestDocumentDBMongoClientResolveDatabase(t *testing.T) {
	local := &documentDBMongoClient{dc: config.DocumentDBConfig{Database: "demo"}}
	if got, err := local.resolveDatabase("", true); err != nil || got != "demo" {
		t.Errorf("local default = %q, %v; want demo", got, err)
	}
	if got, err := local.resolveDatabase("other", true); err != nil || got != "other" {
		t.Errorf("local override = %q, %v; want other", got, err)
	}

	remote := &documentDBMongoClient{remote: &documentDBRemote{database: "app"}}
	if got, err := remote.resolveDatabase("", true); err != nil || got != "app" {
		t.Errorf("remote URI default = %q, %v; want app", got, err)
	}
	noDB := &documentDBMongoClient{remote: &documentDBRemote{host: "10.0.0.5"}}
	if got, err := noDB.resolveDatabase("", false); err != nil || got != "" {
		t.Errorf("optional database = %q, %v; want empty", got, err)
	}
	if _, err := noDB.resolveDatabase("", true); err == nil {
		t.Error("expected an error when a required database is missing")
	}
}

func TestDocumentDBMongoClientDisplay(t *testing.T) {
	c := &documentDBMongoClient{remote: &documentDBRemote{host: "::1", port: 10260, username: "u", database: "app"}}
	if got, want := c.display("app"), "u@[::1]:10260/app"; got != want {
		t.Errorf("display = %q, want %q", got, want)
	}
}

func mustParseMongoURI(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func TestDocumentDBListCollectionsJS(t *testing.T) {
	got := documentDBListCollectionsJS(`my"db`)
	if !strings.Contains(got, `getSiblingDB("my\"db")`) {
		t.Errorf("database literal not escaped: %s", got)
	}
	if !strings.Contains(got, "getCollectionNames()") {
		t.Errorf("missing getCollectionNames: %s", got)
	}
}

func TestDocumentDBListIndexesJS(t *testing.T) {
	got := documentDBListIndexesJS("app", `coll\name`)
	if !strings.Contains(got, `getSiblingDB("app")`) {
		t.Errorf("missing database: %s", got)
	}
	if !strings.Contains(got, `getCollection("coll\\name")`) {
		t.Errorf("collection literal not escaped: %s", got)
	}
	if !strings.Contains(got, "getIndexes()") || !strings.Contains(got, "JSON.stringify") {
		t.Errorf("unexpected index script: %s", got)
	}
}

func TestDocumentDBListDatabasesJS(t *testing.T) {
	got := documentDBListDatabasesJS()
	if !strings.Contains(got, "listDatabases") || !strings.Contains(got, "databases.map") {
		t.Errorf("unexpected databases script: %s", got)
	}
}
