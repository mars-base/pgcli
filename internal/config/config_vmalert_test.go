package config

import (
	"path/filepath"
	"testing"
)

func TestVmalertDefaults(t *testing.T) {
	cfg := Default()
	cfg.Addons.Vmalert = map[string]VmalertConfig{
		"vmalert": {},
	}

	cfg.ApplyDefaults()

	vc, ok := cfg.Addons.Vmalert["vmalert"]
	if !ok {
		t.Fatal("vmalert addon missing after ApplyDefaults")
	}
	if vc.Name != "vmalert" {
		t.Errorf("name = %q, want vmalert", vc.Name)
	}
	if vc.ContainerName != "pgcli-vmalert-vmalert" {
		t.Errorf("container name = %q, want pgcli-vmalert-vmalert", vc.ContainerName)
	}
	if vc.ImageTag != DefaultVmalertImageTag {
		t.Errorf("image tag = %q, want %q", vc.ImageTag, DefaultVmalertImageTag)
	}
	if vc.Listen != "0.0.0.0" {
		t.Errorf("listen = %q, want 0.0.0.0", vc.Listen)
	}
	if vc.DatasourceURL != "http://127.0.0.1:9428" {
		t.Errorf("datasource = %q, want http://127.0.0.1:9428 (VictoriaLogs)", vc.DatasourceURL)
	}
	if len(vc.NotifierURLs) != 1 || vc.NotifierURLs[0] != "http://127.0.0.1:9093" {
		t.Errorf("notifiers = %v, want [http://127.0.0.1:9093]", vc.NotifierURLs)
	}
	if vc.RulesDir != "" || vc.TemplatesDir != "" {
		t.Errorf("rules/templates dirs must stay empty in config (resolved by the podman manager), got %q / %q", vc.RulesDir, vc.TemplatesDir)
	}
	if vc.HostPort < cfg.VmalertStartPort {
		t.Errorf("port %d below base %d", vc.HostPort, cfg.VmalertStartPort)
	}
}

func TestVmalertPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.VmalertStartPort = 39880
	cfg.Addons.Vmalert = map[string]VmalertConfig{
		"a": {HostPort: 39880},
		"b": {},
		"c": {},
	}

	cfg.ApplyDefaults()

	if got := cfg.Addons.Vmalert["a"]; got.HostPort != 39880 {
		t.Errorf("explicit port changed: %d", got.HostPort)
	}
	if got := cfg.Addons.Vmalert["b"]; got.HostPort != 39881 {
		t.Errorf("b port = %d, want 39881", got.HostPort)
	}
	if got := cfg.Addons.Vmalert["c"]; got.HostPort != 39882 {
		t.Errorf("c port = %d, want 39882", got.HostPort)
	}

	// A second ApplyDefaults must not move the assigned ports.
	cfg.ApplyDefaults()
	if got := cfg.Addons.Vmalert["b"].HostPort; got != 39881 {
		t.Errorf("second ApplyDefaults moved b to %d", got)
	}
}

func TestVmalertSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")

	cfg := Default()
	cfg.VmalertStartPort = 39880
	cfg.Addons.Vmalert = map[string]VmalertConfig{
		"vm1": {
			ContainerName:   "pgcli-vmalert-vm1",
			Name:            "vm1",
			ImageTag:        "docker.io/victoriametrics/vmalert:v1.133.0",
			HostPort:        39880,
			Listen:          "127.0.0.1",
			DatasourceURL:   "http://127.0.0.1:8428",
			NotifierURLs:    []string{"http://127.0.0.1:9093", "https://am.example.com"},
			RulesDir:        "/srv/vmalert/rules",
			TemplatesDir:    "/srv/vmalert/templates",
			RemoteWriteURLs: []string{"http://127.0.0.1:8428/api/v1/write"},
			RemoteReadURLs:  []string{"http://127.0.0.1:8428/api/v1/read"},
			ExternalLabels:  []string{"replica=a", "env=prod"},
			ExternalURL:     "http://192.0.2.10:8880",
			ExtraArgs:       []string{"-evaluationInterval=15s"},
			Autostart:       true,
		},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.VmalertStartPort != 39880 {
		t.Errorf("vmalert_start_port = %d, want 39880 (Display must carry it)", got.VmalertStartPort)
	}
	vc, ok := got.Addons.Vmalert["vm1"]
	if !ok {
		t.Fatal("vmalert addon not persisted")
	}
	if vc.Listen != "127.0.0.1" || vc.DatasourceURL != "http://127.0.0.1:8428" {
		t.Errorf("listen/datasource not persisted: %q / %q", vc.Listen, vc.DatasourceURL)
	}
	if len(vc.NotifierURLs) != 2 || vc.NotifierURLs[1] != "https://am.example.com" {
		t.Errorf("notifiers not persisted: %v", vc.NotifierURLs)
	}
	if vc.RulesDir != "/srv/vmalert/rules" || vc.TemplatesDir != "/srv/vmalert/templates" {
		t.Errorf("rules/templates dirs not persisted: %q / %q", vc.RulesDir, vc.TemplatesDir)
	}
	if len(vc.RemoteWriteURLs) != 1 || len(vc.RemoteReadURLs) != 1 {
		t.Errorf("remote write/read not persisted: %v / %v", vc.RemoteWriteURLs, vc.RemoteReadURLs)
	}
	if len(vc.ExternalLabels) != 2 || vc.ExternalURL != "http://192.0.2.10:8880" {
		t.Errorf("external labels/url not persisted: %v / %q", vc.ExternalLabels, vc.ExternalURL)
	}
	if len(vc.ExtraArgs) != 1 || !vc.Autostart {
		t.Errorf("extra args/autostart not persisted: %v / %v", vc.ExtraArgs, vc.Autostart)
	}
}

func TestValidateVmalertConfig(t *testing.T) {
	valid := VmalertConfig{
		DatasourceURL:   "http://127.0.0.1:9428",
		NotifierURLs:    []string{"http://127.0.0.1:9093"},
		RemoteWriteURLs: []string{"https://vm.example.com/api/v1/write"},
		RemoteReadURLs:  []string{"https://vm.example.com/api/v1/read"},
		ExternalURL:     "http://192.0.2.10:8880",
		ExternalLabels:  []string{"replica=a", "env=prod"},
		RulesDir:        "/srv/vmalert/rules",
		TemplatesDir:    "/srv/vmalert/templates",
		HostPort:        8880,
	}
	tests := []struct {
		name    string
		mutate  func(*VmalertConfig)
		wantErr bool
	}{
		{"valid", func(*VmalertConfig) {}, false},
		{"empty datasource", func(vc *VmalertConfig) { vc.DatasourceURL = "" }, true},
		{"relative datasource", func(vc *VmalertConfig) { vc.DatasourceURL = "127.0.0.1:9428" }, true},
		{"ftp datasource", func(vc *VmalertConfig) { vc.DatasourceURL = "ftp://127.0.0.1:9428" }, true},
		{"datasource without host", func(vc *VmalertConfig) { vc.DatasourceURL = "http:///api" }, true},
		{"bad notifier", func(vc *VmalertConfig) { vc.NotifierURLs = []string{"alertmanager:9093"} }, true},
		{"bad remote write", func(vc *VmalertConfig) { vc.RemoteWriteURLs = []string{"http://"} }, true},
		{"bad remote read", func(vc *VmalertConfig) { vc.RemoteReadURLs = []string{"http://"} }, true},
		{"bad external url", func(vc *VmalertConfig) { vc.ExternalURL = "192.0.2.10:8880" }, true},
		{"label without value", func(vc *VmalertConfig) { vc.ExternalLabels = []string{"replica="} }, true},
		{"label without separator", func(vc *VmalertConfig) { vc.ExternalLabels = []string{"replica"} }, true},
		{"label bad name", func(vc *VmalertConfig) { vc.ExternalLabels = []string{"1replica=a"} }, true},
		{"relative rules dir", func(vc *VmalertConfig) { vc.RulesDir = "rules" }, true},
		{"same dirs", func(vc *VmalertConfig) { vc.TemplatesDir = vc.RulesDir }, true},
		{"negative port", func(vc *VmalertConfig) { vc.HostPort = -1 }, true},
		{"port too high", func(vc *VmalertConfig) { vc.HostPort = 65536 }, true},
		{"zero port is auto", func(vc *VmalertConfig) { vc.HostPort = 0 }, false},
		{"no notifiers allowed", func(vc *VmalertConfig) { vc.NotifierURLs = nil }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vc := valid
			// Copy slices so mutations in one case cannot leak into another.
			vc.NotifierURLs = append([]string(nil), valid.NotifierURLs...)
			vc.RemoteWriteURLs = append([]string(nil), valid.RemoteWriteURLs...)
			vc.RemoteReadURLs = append([]string(nil), valid.RemoteReadURLs...)
			vc.ExternalLabels = append([]string(nil), valid.ExternalLabels...)
			tt.mutate(&vc)
			err := ValidateVmalertConfig("vm1", vc)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
