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
	if j.Bucket != "juicefs-fs1" {
		t.Errorf("bucket = %q", j.Bucket)
	}
	if j.MountPoint != filepath.Join("/base", "addon", "juicefs", "fs1", "mnt") {
		t.Errorf("mount point = %q", j.MountPoint)
	}
	if j.MountPidFile == "" || j.MountLogFile == "" || j.CacheDir == "" {
		t.Errorf("host bookkeeping not defaulted: %+v", j)
	}
	// The external S3 contract is never defaulted — endpoint, credentials and
	// the metadata DSN all come from explicit flags.
	if j.MetaDSN != "" || j.S3Endpoint != "" || j.AccessKey != "" || j.SecretKey != "" {
		t.Errorf("external backend must stay empty until install: %+v", j)
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
	// Secrets and the external backend are never defaulted.
	if z.EncryptionPassword != "" || z.S3Endpoint != "" || z.AccessKey != "" || z.SecretKey != "" {
		t.Errorf("must stay empty until install: %+v", z)
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
			MetaDSN:    "sqlite3:///base/meta.db",
			S3Endpoint: "https://s3.example.com",
			MountPoint: "/mnt/j",
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

	// The metadata engine is always an explicit DSN — missing or unknown
	// schemes are rejected.
	c = base()
	mutateJuicefs(c, "fs1", func(j *JuicefsConfig) { j.MetaDSN = "" })
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "meta_dsn is required") {
		t.Errorf("missing meta_dsn accepted: %v", err)
	}

	c = base()
	mutateJuicefs(c, "fs1", func(j *JuicefsConfig) { j.MetaDSN = "mysql://host/db" })
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "postgres://") {
		t.Errorf("unknown dsn scheme accepted: %v", err)
	}

	for _, ok := range []string{
		"postgres://u:p@h:5432/db", "postgresql://u:p@h:5432/db",
		"redis://:pw@h:6379/0", "rediss://h:6379/0", "sqlite3:///x/y.db",
	} {
		c = base()
		mutateJuicefs(c, "fs1", func(j *JuicefsConfig) { j.MetaDSN = ok })
		if err := c.Validate(); err != nil {
			t.Errorf("scheme %q rejected: %v", ok, err)
		}
	}

	// s3_endpoint shape: only bare http(s)://host[:port] is accepted (an empty
	// endpoint passes Validate — presence is enforced by the installer).
	for _, bad := range []string{
		"ftp://host", "http://host/bucket", "https://u:***@host",
		"https://host?x=1", "minio:9000",
	} {
		c = base()
		mutateJuicefs(c, "fs1", func(j *JuicefsConfig) { j.S3Endpoint = bad })
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "s3_endpoint") {
			t.Errorf("bad endpoint %q accepted: %v", bad, err)
		}
	}
	for _, ok := range []string{"http://host:9000", "https://s3.amazonaws.com", "http://10.0.0.5:9000"} {
		c = base()
		mutateJuicefs(c, "fs1", func(j *JuicefsConfig) { j.S3Endpoint = ok })
		if err := c.Validate(); err != nil {
			t.Errorf("endpoint %q rejected: %v", ok, err)
		}
	}
}

func TestZerofsValidate(t *testing.T) {
	base := func() *Config {
		c := Default()
		c.Addons.Zerofs = map[string]ZerofsConfig{"z1": {
			MountPoint: "/mnt/z", Listen: "127.0.0.1", ListenPort: 5564,
			ConditionalPut: ZerofsConditionalPutAuto,
			S3Endpoint:     "http://minio.example.com:9000",
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
		ListenPort: 5564, ConditionalPut: "none"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "listen_port") {
		t.Errorf("duplicate 9P port accepted: %v", err)
	}

	c = base()
	mutateZerofs(c, "z1", func(z *ZerofsConfig) { z.MountPoint = "rel" })
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "mount_point") {
		t.Errorf("relative mount accepted: %v", err)
	}

	c = base()
	mutateZerofs(c, "z1", func(z *ZerofsConfig) { z.S3Endpoint = "http://host/bucket" })
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "s3_endpoint") {
		t.Errorf("endpoint with a path accepted: %v", err)
	}
}

func TestJuicefsZerofsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")
	c := Default()
	c.Addons.Juicefs = map[string]JuicefsConfig{"fs1": {
		MetaDSN: "postgres://u:***@meta-host:5432/jfs", SecretKey: "sk-1",
		S3Endpoint: "https://s3.example.com", AccessKey: "ak-1",
	}}
	c.Addons.Zerofs = map[string]ZerofsConfig{"z1": {
		EncryptionPassword: "ep-1", S3Endpoint: "http://minio.example.com:9000",
		AccessKey: "ak-2", SecretKey: "sk-2", ConditionalPut: "redis",
		ConditionalPutURL: "redis://:***@fencer:6379",
	}}
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
	if got := loaded.Addons.Juicefs["fs1"].MetaDSN; got != "postgres://u:***@meta-host:5432/jfs" {
		t.Errorf("juicefs round trip lost meta dsn: %q", got)
	}
	if got := loaded.Addons.Zerofs["z1"].EncryptionPassword; got != "ep-1" {
		t.Errorf("zerofs round trip lost password: %q", got)
	}
	if got := loaded.Addons.Zerofs["z1"].ConditionalPutURL; got != "redis://:***@fencer:6379" {
		t.Errorf("zerofs round trip lost conditional put url: %q", got)
	}
	if got := loaded.Addons.Juicefs["fs1"].MountPoint; !filepath.IsAbs(got) {
		t.Errorf("mount point lost: %q", got)
	}
	// The yaml keys are the lower-case family names.
	raw, _ := os.ReadFile(path)
	for _, want := range []string{"juicefs:", "zerofs:", "meta_dsn:", "s3_endpoint:", "conditional_put_url:"} {
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
