package cli

import (
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// addonPasswords indexes the five addons that carry a stored password; the key
// "<addon>:<name>" is what `pg addon password` resolves against, so a wrong key
// shape is a silent not-found for the user. Four of the five are pgcli-generated
// (redis/minio/silo/rustfs); predixy's is the operator-supplied copy of the
// proxied cluster's requirepass, but it's still what the proxy authenticates
// clients with, so it belongs in the same index.
func passwordCfg() *config.Config {
	cfg := config.Default()
	cfg.Addons.Redis = map[string]config.RedisConfig{
		"cache": {Name: "cache", Password: "pw-cache"},
	}
	cfg.Addons.Predixy = map[string]config.PredixyConfig{
		"proxy": {Name: "proxy", Password: "pw-predixy"},
	}
	cfg.Addons.Minio = map[string]config.MinioConfig{
		"store": {Name: "store", RootUser: "admin", RootPassword: "pw-minio"},
	}
	cfg.Addons.Silo = map[string]config.SiloConfig{
		"store": {Name: "store", RootUser: "admin", RootPassword: "pw-silo"},
	}
	cfg.Addons.Rustfs = map[string]config.RustfsConfig{
		"store": {Name: "store", RootUser: "admin", RootPassword: "pw-rustfs"},
	}
	return cfg
}

func TestAddonPasswordsKeysAndSecrets(t *testing.T) {
	all := addonPasswords(passwordCfg())
	cases := map[string]string{
		"redis:cache":   "pw-cache",
		"predixy:proxy": "pw-predixy",
		"minio:store":   "pw-minio",
		"silo:store":    "pw-silo",
		"rustfs:store":  "pw-rustfs",
	}
	for key, want := range cases {
		got, ok := all[key]
		if !ok {
			t.Fatalf("missing key %q (have %v)", key, keysOf(all))
		}
		if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// etcd/haproxy store no generated password; they must be absent from the index
// so the command reports "no stored password" rather than an empty success.
func TestAddonPasswordsExcludesNoSecretAddons(t *testing.T) {
	cfg := passwordCfg()
	cfg.Addons.Etcd = map[string]config.EtcdConfig{"ha": {Name: "ha"}}
	for key := range addonPasswords(cfg) {
		if len(key) >= 4 && key[:4] == "etcd" {
			t.Errorf("etcd should not appear in the password index: %q", key)
		}
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
