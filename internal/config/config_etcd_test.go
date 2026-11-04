package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEtcdDefaultsAndPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.Addons.Etcd = map[string]EtcdConfig{
		"ha": {Name: "ha"},
	}

	cfg.ApplyDefaults()

	ec, ok := cfg.Addons.Etcd["ha"]
	if !ok {
		t.Fatal("etcd addon missing after ApplyDefaults")
	}
	if ec.ContainerName != "pgcli-etcd-ha" {
		t.Errorf("container name = %q, want pgcli-etcd-ha", ec.ContainerName)
	}
	if ec.ImageTag != "quay.io/coreos/etcd:v3.5.30" {
		t.Errorf("image tag = %q, want quay.io/coreos/etcd:v3.5.30", ec.ImageTag)
	}
	if ec.ClientPort < cfg.EtcdStartPort {
		t.Errorf("client port %d below base %d", ec.ClientPort, cfg.EtcdStartPort)
	}
	if ec.PeerPort <= ec.ClientPort {
		t.Errorf("peer port %d must be above client port %d", ec.PeerPort, ec.ClientPort)
	}
}

func TestEtcdSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{"a": {}}
	cfg.Addons.Etcd = map[string]EtcdConfig{
		"ha": {ClientPort: 2379, PeerPort: 2380, AdvertiseHost: "10.0.0.1", Autostart: true},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ec, ok := got.Addons.Etcd["ha"]
	if !ok {
		t.Fatal("etcd addon not persisted")
	}
	if ec.ClientPort != 2379 || ec.PeerPort != 2380 {
		t.Errorf("ports not persisted: client=%d peer=%d", ec.ClientPort, ec.PeerPort)
	}
	if ec.AdvertiseHost != "10.0.0.1" {
		t.Errorf("advertise host not persisted: %q", ec.AdvertiseHost)
	}
	if !ec.Autostart {
		t.Error("autostart not persisted")
	}
}

func TestEtcdURLSchemeFollowsTLS(t *testing.T) {
	plain := EtcdConfig{ClientPort: 2379, PeerPort: 2380}
	if got := plain.ClientURL(); got != "http://127.0.0.1:2379" {
		t.Errorf("ClientURL = %q", got)
	}
	if got := plain.PeerURL(); got != "http://127.0.0.1:2380" {
		t.Errorf("PeerURL = %q", got)
	}
	if got := plain.ClientScheme(); got != "http" {
		t.Errorf("ClientScheme = %q", got)
	}

	secure := EtcdConfig{TLS: true, AdvertiseHost: "10.0.0.5", ClientPort: 2379, PeerPort: 2380}
	if got := secure.ClientURL(); got != "https://10.0.0.5:2379" {
		t.Errorf("TLS ClientURL = %q", got)
	}
	if got := secure.PeerURL(); got != "https://10.0.0.5:2380" {
		t.Errorf("TLS PeerURL = %q", got)
	}
	if got := secure.ClientScheme(); got != "https" {
		t.Errorf("TLS ClientScheme = %q", got)
	}
}

func TestEtcdTLSAndAuthRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")
	cfg := Default()
	cfg.Addons.Etcd = map[string]EtcdConfig{
		"secure": {
			ClientPort:   2379,
			PeerPort:     2380,
			TLS:          true,
			Auth:         true,
			AuthUser:     "root",
			AuthPassword: "s3cret",
		},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ec := got.Addons.Etcd["secure"]
	if !ec.TLS || !ec.Auth {
		t.Errorf("tls/auth not persisted: %+v", ec)
	}
	if ec.AuthUser != "root" || ec.AuthPassword != "s3cret" {
		t.Errorf("auth credentials not persisted: user=%q", ec.AuthUser)
	}
}

func TestValidateEtcdTLSAndAuth(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "cert.pem")
	key := filepath.Join(dir, "key.pem")
	for _, p := range []string{cert, key} {
		if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name string
		ec   EtcdConfig
		want string
	}{
		{"half pair", EtcdConfig{TLSCertFile: cert}, "must be set together"},
		{"missing cert", EtcdConfig{TLSCertFile: filepath.Join(dir, "nope"), TLSKeyFile: key}, "no such file"},
		{"auth without user", EtcdConfig{Auth: true, AuthPassword: "x"}, "auth_user"},
		{"auth without password", EtcdConfig{Auth: true, AuthUser: "root"}, "auth_password"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Addons.Etcd = map[string]EtcdConfig{"a": tc.ec}
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate = %v, want error containing %q", err, tc.want)
			}
		})
	}

	cfg := Default()
	cfg.Addons.Etcd = map[string]EtcdConfig{
		"ok": {TLS: true, TLSCertFile: cert, TLSKeyFile: key, Auth: true, AuthUser: "root", AuthPassword: "x"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid etcd TLS/auth rejected: %v", err)
	}

	// ApplyDefaults fills the default root user before Validate runs.
	cfg = Default()
	cfg.Addons.Etcd = map[string]EtcdConfig{"a": {Auth: true, AuthPassword: "x"}}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaulted auth user rejected: %v", err)
	}
}
