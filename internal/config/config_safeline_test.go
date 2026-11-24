package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func safelineTestConfig() *Config {
	cfg := Default()
	cfg.BaseDir = "/srv/pg"
	cfg.Addons.Safeline = map[string]SafelineConfig{"waf1": {}}
	cfg.ApplyDefaults()
	return cfg
}

func TestSafelineDefaultStartPort(t *testing.T) {
	if got := Default().SafelineStartPort; got != DefaultSafelineConsolePort {
		t.Errorf("safeline_start_port = %d, want %d", got, DefaultSafelineConsolePort)
	}
	if DefaultSafelineConsoleContainerPort != 1443 {
		t.Errorf("console container port = %d, want 1443 (upstream mgt port)", DefaultSafelineConsoleContainerPort)
	}
}

func TestSafelineApplyDefaults(t *testing.T) {
	cfg := safelineTestConfig()
	sc := cfg.Addons.Safeline["waf1"]

	if sc.Name != "waf1" {
		t.Errorf("Name = %q, want waf1", sc.Name)
	}
	if sc.Version != DefaultSafelineVersion {
		t.Errorf("Version = %q, want %q", sc.Version, DefaultSafelineVersion)
	}
	if sc.PostgresVersion != DefaultSafelinePostgresVersion {
		t.Errorf("PostgresVersion = %q, want %q", sc.PostgresVersion, DefaultSafelinePostgresVersion)
	}
	if sc.ContainerPrefix != "pgcli-safeline-waf1" {
		t.Errorf("ContainerPrefix = %q, want pgcli-safeline-waf1", sc.ContainerPrefix)
	}
	if sc.NetworkName != sc.ContainerPrefix {
		t.Errorf("NetworkName = %q, want %q", sc.NetworkName, sc.ContainerPrefix)
	}
	if sc.SubnetPrefix != DefaultSafelineSubnetPrefix {
		t.Errorf("SubnetPrefix = %q, want %q", sc.SubnetPrefix, DefaultSafelineSubnetPrefix)
	}
	if sc.Listen != "0.0.0.0" {
		t.Errorf("Listen = %q, want 0.0.0.0 (upstream binds all interfaces)", sc.Listen)
	}
	if sc.AdminUser != "admin" {
		t.Errorf("AdminUser = %q, want admin", sc.AdminUser)
	}
	if want := filepath.Join("/srv/pg", "addon", "safeline", "waf1"); sc.DataDir != want {
		t.Errorf("DataDir = %q, want %q", sc.DataDir, want)
	}
	// Secrets are generated at install, never defaulted into the config.
	if sc.PostgresPassword != "" || sc.AdminPassword != "" {
		t.Errorf("secrets must stay empty: postgres=%q admin=%q", sc.PostgresPassword, sc.AdminPassword)
	}
	if sc.ConsolePort < cfg.SafelineStartPort {
		t.Errorf("ConsolePort = %d, below pool base %d", sc.ConsolePort, cfg.SafelineStartPort)
	}
}

// SafeLine owns its console-port pool (safeline_start_port). A high unused
// base keeps the live-listener scan out of the exact expectations, same
// convention as TestPgAdminPortAssignment.
func TestSafelinePortAssignment(t *testing.T) {
	cfg := Default()
	cfg.SafelineStartPort = 45901
	cfg.Addons.Safeline = map[string]SafelineConfig{
		"b": {},
		"a": {ConsolePort: 46500}, // explicit, above the base
		"c": {},
	}

	cfg.ApplyDefaults()

	if got := cfg.Addons.Safeline["a"]; got.ConsolePort != 46500 {
		t.Errorf("explicit port changed: %d", got.ConsolePort)
	}
	if got := cfg.Addons.Safeline["b"]; got.ConsolePort != 46501 {
		t.Errorf("b port = %d, want 46501 (cursor must skip the reserved port)", got.ConsolePort)
	}
	if got := cfg.Addons.Safeline["c"]; got.ConsolePort != 46502 {
		t.Errorf("c port = %d, want 46502", got.ConsolePort)
	}
}

func TestSafelineValidate(t *testing.T) {
	if err := safelineTestConfig().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{
			name:    "bad name",
			mutate:  func(c *Config) { sc := c.Addons.Safeline["waf1"]; sc.Name = "-bad"; c.Addons.Safeline["waf1"] = sc },
			wantSub: "must match",
		},
		{
			name: "bad version",
			mutate: func(c *Config) {
				sc := c.Addons.Safeline["waf1"]
				sc.Version = "9.4.7-rc1"
				c.Addons.Safeline["waf1"] = sc
			},
			wantSub: "version",
		},
		{
			name: "bad postgres version",
			mutate: func(c *Config) {
				sc := c.Addons.Safeline["waf1"]
				sc.PostgresVersion = "15"
				c.Addons.Safeline["waf1"] = sc
			},
			wantSub: "postgres_version",
		},
		{
			name: "console port range",
			mutate: func(c *Config) {
				sc := c.Addons.Safeline["waf1"]
				sc.ConsolePort = 70000
				c.Addons.Safeline["waf1"] = sc
			},
			wantSub: "console_port",
		},
		{
			name: "duplicate console port",
			mutate: func(c *Config) {
				sc := c.Addons.Safeline["waf1"]
				sc.Name = "waf2"
				sc.ContainerPrefix = "pgcli-safeline-waf2"
				sc.NetworkName = sc.ContainerPrefix
				sc.SubnetPrefix = "172.22.223"
				c.Addons.Safeline["waf2"] = sc
			},
			wantSub: "already used by",
		},
		{
			name: "bad subnet",
			mutate: func(c *Config) {
				sc := c.Addons.Safeline["waf1"]
				sc.SubnetPrefix = "172.22"
				c.Addons.Safeline["waf1"] = sc
			},
			wantSub: "subnet_prefix",
		},
		{
			name: "duplicate subnet",
			mutate: func(c *Config) {
				sc := c.Addons.Safeline["waf1"]
				sc.Name = "waf2"
				sc.ContainerPrefix = "pgcli-safeline-waf2"
				sc.NetworkName = sc.ContainerPrefix
				sc.ConsolePort++
				c.Addons.Safeline["waf2"] = sc
			},
			wantSub: "needs its own subnet",
		},
		{
			name: "empty container prefix",
			mutate: func(c *Config) {
				sc := c.Addons.Safeline["waf1"]
				sc.ContainerPrefix = ""
				c.Addons.Safeline["waf1"] = sc
			},
			wantSub: "container_prefix",
		},
		{
			name: "relative data dir",
			mutate: func(c *Config) {
				sc := c.Addons.Safeline["waf1"]
				sc.DataDir = "relative/path"
				c.Addons.Safeline["waf1"] = sc
			},
			wantSub: "absolute",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := safelineTestConfig()
			tc.mutate(cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("Validate = %v, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestSafelineSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	cfg := Default()
	cfg.BaseDir = dir
	cfg.SafelineStartPort = 45981
	cfg.Addons.Safeline = map[string]SafelineConfig{
		"waf1": {
			Version:          "9.5.0",
			PostgresVersion:  "15.18",
			SubnetPrefix:     "172.22.240",
			ConsolePort:      45981,
			Listen:           "10.0.0.5",
			PostgresPassword: "pgsecret",
			AdminUser:        "admin",
			AdminPassword:    "consolepw",
			Autostart:        true,
		},
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sc, ok := loaded.Addons.Safeline["waf1"]
	if !ok {
		t.Fatal("safeline entry lost in round trip")
	}
	if sc.Version != "9.5.0" || sc.PostgresVersion != "15.18" {
		t.Errorf("versions = %q/%q", sc.Version, sc.PostgresVersion)
	}
	if sc.SubnetPrefix != "172.22.240" || sc.ConsolePort != 45981 || sc.Listen != "10.0.0.5" {
		t.Errorf("network = %q/%d/%q", sc.SubnetPrefix, sc.ConsolePort, sc.Listen)
	}
	if sc.PostgresPassword != "pgsecret" || sc.AdminPassword != "consolepw" {
		t.Errorf("secrets = %q/%q", sc.PostgresPassword, sc.AdminPassword)
	}
	if !sc.Autostart {
		t.Error("autostart lost in round trip")
	}
	if want := filepath.Join(dir, "addon", "safeline", "waf1"); sc.DataDir != want {
		t.Errorf("DataDir = %q, want %q", sc.DataDir, want)
	}
}

func TestValidSafelineHelpers(t *testing.T) {
	names := map[string]bool{
		"waf1": true, "w": true, "1waf": true, "waf_1-x": true,
		"": false, "-waf": false, "_waf": false, "waf 1": false,
		strings.Repeat("a", 49): false,
	}
	for name, want := range names {
		if got := ValidSafelineName(name); got != want {
			t.Errorf("ValidSafelineName(%q) = %v, want %v", name, got, want)
		}
	}

	versions := map[string]bool{
		"9.4.7": true, "15.18": true, "1.2": true,
		"9": false, "9.4.7-rc1": false, "v9.4.7": false, "": false,
	}
	for v, want := range versions {
		if got := ValidSafelineVersion(v); got != want {
			t.Errorf("ValidSafelineVersion(%q) = %v, want %v", v, got, want)
		}
	}

	subnets := map[string]bool{
		"172.22.222": true, "10.0.0": true,
		"172.22": false, "172.22.222.1": false, "172.22.256": false, "172.22.02": false, "": false,
	}
	for s, want := range subnets {
		if got := ValidSafelineSubnetPrefix(s); got != want {
			t.Errorf("ValidSafelineSubnetPrefix(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestSafelineDerivedAddresses(t *testing.T) {
	sc := SafelineConfig{ContainerPrefix: "pgcli-safeline-waf1", SubnetPrefix: "172.22.222"}
	if got := SafelineContainerName(sc, "mgt"); got != "pgcli-safeline-waf1-mgt" {
		t.Errorf("container name = %q", got)
	}
	if got := SafelineSubnet(sc); got != "172.22.222.0/24" {
		t.Errorf("subnet = %q", got)
	}
	if got := SafelineGateway(sc); got != "172.22.222.1" {
		t.Errorf("gateway = %q", got)
	}
	if got := SafelineStaticIP(sc, 4); got != "172.22.222.4" {
		t.Errorf("static ip = %q", got)
	}
}
