package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJuicefsApplyDefaults(t *testing.T) {
	c := Default()
	c.BaseDir = "/base"
	c.Addons.Juicefs = map[string]JuicefsConfig{"fs1": {}}
	c.ApplyDefaults()

	j := c.Addons.Juicefs["fs1"]
	if j.Name != "fs1" || j.Version != DefaultJuicefsVersion {
		t.Fatalf("name/version = %q/%q", j.Name, j.Version)
	}
	if j.MetaEngine != JuicefsMetaSQLite3 {
		t.Errorf("meta engine default = %q", j.MetaEngine)
	}
	if j.RedisName != "redis" || j.PgName != "default" {
		t.Errorf("addon name defaults = %q/%q", j.RedisName, j.PgName)
	}
	if j.PgDBName != "juicefs_fs1" {
		t.Errorf("pg db name = %q", j.PgDBName)
	}
	// Hyphenated instance names become valid identifiers.
	c.Addons.Juicefs["my-fs"] = JuicefsConfig{}
	c.ApplyDefaults()
	if got := c.Addons.Juicefs["my-fs"].PgDBName; got != "juicefs_my_fs" {
		t.Errorf("pg db name for my-fs = %q", got)
	}
	if j.MetaDBPath != "/base/addon/juicefs/fs1/meta.db" {
		t.Errorf("meta db = %q", j.MetaDBPath)
	}
	if j.S3Addon != "minio" || j.S3Name != "minio" {
		t.Errorf("s3 default = %q/%q", j.S3Addon, j.S3Name)
	}
	if j.Bucket != "juicefs-fs1" || j.AccessKey != "juicefs-fs1" {
		t.Errorf("bucket/access = %q/%q", j.Bucket, j.AccessKey)
	}
	if j.MountPoint != filepath.Join("/base", "addon", "juicefs", "fs1", "mnt") {
		t.Errorf("mount point = %q", j.MountPoint)
	}
	if j.MountPidFile == "" || j.MountLogFile == "" || j.CacheDir == "" {
		t.Errorf("host bookkeeping not defaulted: %+v", j)
	}
	// Secrets are never defaulted.
	if j.SecretKey != "" {
		t.Error("secret key must stay empty until install resolves it")
	}
}

func TestZerofsApplyDefaults(t *testing.T) {
	c := Default()
	c.BaseDir = "/base"
	c.Addons.Zerofs = map[string]ZerofsConfig{"z1": {}}
	c.ApplyDefaults()

	z := c.Addons.Zerofs["z1"]
	if z.Version != DefaultZerofsVersion || z.Listen != "127.0.0.1" || z.ListenPort != DefaultZerofsPort {
		t.Fatalf("defaults wrong: %+v", z)
	}
	if z.Bucket != "zerofs-z1" {
		t.Errorf("bucket = %q", z.Bucket)
	}
	if z.ConditionalPut != ZerofsConditionalPutAuto {
		t.Errorf("conditional put = %q", z.ConditionalPut)
	}
	if z.ConfigPath != "/base/addon/zerofs/z1/config.toml" {
		t.Errorf("config path = %q", z.ConfigPath)
	}
	if z.ServerCacheGB != 100 {
		t.Errorf("server cache gb = %d", z.ServerCacheGB)
	}
	if z.ServerPidFile == "" || z.MountPidFile == "" || z.LogFile == "" {
		t.Errorf("pid/log not defaulted: %+v", z)
	}
	if z.EncryptionPassword != "" {
		t.Error("encryption password must stay empty until install generates it")
	}
}

func mutateJuicefs(c *Config, name string, fn func(*JuicefsConfig)) {
	v := c.Addons.Juicefs[name]
	fn(&v)
	c.Addons.Juicefs[name] = v
}

func mutateZerofs(c *Config, name string, fn func(*ZerofsConfig)) {
	v := c.Addons.Zerofs[name]
	fn(&v)
	c.Addons.Zerofs[name] = v
}

func TestJuicefsValidate(t *testing.T) {
	base := func() *Config {
		c := Default()
		c.Addons.Juicefs = map[string]JuicefsConfig{"fs1": {
			MetaEngine: JuicefsMetaSQLite3, S3Addon: "minio", MountPoint: "/mnt/j",
		}}
		return c
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("clean config rejected: %v", err)
	}

	c := base()
	mutateJuicefs(c, "fs1", func(j *JuicefsConfig) { j.MountPoint = "relative/path" })
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("relative mount point accepted: %v", err)
	}

	c = base()
	mutateJuicefs(c, "fs1", func(j *JuicefsConfig) { j.MetaEngine = "mysql" })
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "meta_engine") {
		t.Errorf("bad meta engine accepted: %v", err)
	}

	c = base()
	mutateJuicefs(c, "fs1", func(j *JuicefsConfig) { j.S3Addon = "rustfs" })
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "s3_addon") {
		t.Errorf("bad s3 addon accepted: %v", err)
	}

	c = base()
	mutateJuicefs(c, "fs1", func(j *JuicefsConfig) {
		j.MetaEngine, j.MetaDSN = JuicefsMetaDSN, ""
	})
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("missing meta_dsn on dsn engine accepted: %v", err)
	}

	// Non-dsn engines freely carry the RESOLVED url (the installer writes it
	// back after format) — must not be rejected.
	c = base()
	mutateJuicefs(c, "fs1", func(j *JuicefsConfig) {
		j.MetaEngine, j.MetaDSN = JuicefsMetaSQLite3, "sqlite3:///base/meta.db"
	})
	if err := c.Validate(); err != nil {
		t.Errorf("resolved meta_dsn on sqlite3 engine rejected: %v", err)
	}

	c = base()
	mutateJuicefs(c, "fs1", func(j *JuicefsConfig) {
		j.MetaEngine, j.MetaDSN = JuicefsMetaDSN, "mysql://host/db"
	})
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "postgres://") {
		t.Errorf("unknown dsn scheme accepted: %v", err)
	}

	for _, ok := range []string{"postgres://u:p@h:5432/db", "redis://:pw@h:6379/0", "sqlite3:///x/y.db"} {
		c = base()
		mutateJuicefs(c, "fs1", func(j *JuicefsConfig) {
			j.MetaEngine, j.MetaDSN = JuicefsMetaDSN, ok
		})
		if err := c.Validate(); err != nil {
			t.Errorf("scheme %q rejected: %v", ok, err)
		}
	}
}

func TestZerofsValidate(t *testing.T) {
	base := func() *Config {
		c := Default()
		c.Addons.Zerofs = map[string]ZerofsConfig{"z1": {
			MountPoint: "/mnt/z", Listen: "127.0.0.1", ListenPort: 5564,
			ConditionalPut: ZerofsConditionalPutAuto, S3Addon: "minio",
		}}
		return c
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("clean config rejected: %v", err)
	}

	c := base()
	mutateZerofs(c, "z1", func(z *ZerofsConfig) { z.Listen = "0.0.0.0" })
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Errorf("non-loopback 9P listener accepted: %v", err)
	}

	c = base()
	mutateZerofs(c, "z1", func(z *ZerofsConfig) { z.Listen = "::1" })
	if err := c.Validate(); err != nil {
		t.Errorf("ipv6 loopback rejected: %v", err)
	}

	c = base()
	mutateZerofs(c, "z1", func(z *ZerofsConfig) { z.ConditionalPut = "etcd" })
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "conditional_put") {
		t.Errorf("bad conditional put accepted: %v", err)
	}

	c = base()
	c.Addons.Zerofs["z2"] = ZerofsConfig{MountPoint: "/mnt/z2", Listen: "localhost",
		ListenPort: 5564, ConditionalPut: "none", S3Addon: "silo"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "listen_port") {
		t.Errorf("duplicate 9P port accepted: %v", err)
	}

	c = base()
	mutateZerofs(c, "z1", func(z *ZerofsConfig) { z.MountPoint = "rel" })
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "mount_point") {
		t.Errorf("relative mount accepted: %v", err)
	}
}

func TestJuicefsZerofsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")
	c := Default()
	c.Addons.Juicefs = map[string]JuicefsConfig{"fs1": {SecretKey: "sk-1"}}
	c.Addons.Zerofs = map[string]ZerofsConfig{"z1": {EncryptionPassword: "ep-1"}}
	c.ApplyDefaults()
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Addons.Juicefs["fs1"].SecretKey; got != "sk-1" {
		t.Errorf("juicefs round trip lost secret: %q", got)
	}
	if got := loaded.Addons.Zerofs["z1"].EncryptionPassword; got != "ep-1" {
		t.Errorf("zerofs round trip lost password: %q", got)
	}
	if got := loaded.Addons.Juicefs["fs1"].MountPoint; !filepath.IsAbs(got) {
		t.Errorf("mount point lost: %q", got)
	}
	// The yaml keys are the lower-case family names.
	raw, _ := os.ReadFile(path)
	for _, want := range []string{"juicefs:", "zerofs:", "meta_engine:", "conditional_put:"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("saved yaml missing %q\n%s", want, raw)
		}
	}
}

func TestSHAConstants(t *testing.T) {
	if JuicefsSHA256("amd64") == "" || JuicefsSHA256("arm64") == "" {
		t.Error("juicefs sha constants missing")
	}
	if JuicefsSHA256("riscv") != "" {
		t.Error("unknown arch must have no pin")
	}
	if len(ZerofsTarballSHA256()) != 64 {
		t.Errorf("zerofs sha = %q", ZerofsTarballSHA256())
	}
}
