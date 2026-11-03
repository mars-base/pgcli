package podman

import (
	"reflect"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func testVmalertConfig() *config.VmalertConfig {
	return &config.VmalertConfig{
		Name:            "vm1",
		ContainerName:   "pgcli-vmalert-vm1",
		ImageTag:        config.DefaultVmalertImageTag,
		HostPort:        8880,
		Listen:          "127.0.0.1",
		DatasourceURL:   "http://127.0.0.1:9428",
		NotifierURLs:    []string{"http://127.0.0.1:9093", "https://am.example.com"},
		RemoteWriteURLs: []string{"http://127.0.0.1:8428/api/v1/write"},
		RemoteReadURLs:  []string{"http://127.0.0.1:8428/api/v1/read"},
		ExternalLabels:  []string{"replica=a", "env=prod"},
		ExternalURL:     "http://192.0.2.10:8880",
		ExtraArgs:       []string{"-evaluationInterval=15s"},
	}
}

func TestVmalertArgsAndEnvLinux(t *testing.T) {
	vc := testVmalertConfig()
	want := []string{
		"run", "-d", "--name", "pgcli-vmalert-vm1",
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"-v", "/srv/vmalert/rules:/etc/vmalert/rules:ro,z",
		"-v", "/srv/vmalert/templates:/etc/vmalert/templates:ro,z",
		config.DefaultVmalertImageTag,
		"-datasource.url=http://127.0.0.1:9428",
		"-rule=/etc/vmalert/rules/*.yaml",
		"-rule.templates=/etc/vmalert/templates/*.tpl",
		"-notifier.url=http://127.0.0.1:9093",
		"-notifier.url=https://am.example.com",
		"-remoteWrite.url=http://127.0.0.1:8428/api/v1/write",
		"-remoteRead.url=http://127.0.0.1:8428/api/v1/read",
		"-external.url=http://192.0.2.10:8880",
		"-external.label=replica=a",
		"-external.label=env=prod",
		"-httpListenAddr=127.0.0.1:8880",
		"-evaluationInterval=15s",
	}
	got := vmalertArgsAndEnv(vc, false, "pgcli-net", "/srv/vmalert/rules", "/srv/vmalert/templates")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args mismatch\n got: %#v\nwant: %#v", got, want)
	}
	for _, a := range got {
		if a == "--user" {
			t.Fatal("vmalert must run as the image's root user (no --user)")
		}
	}
}

func TestVmalertArgsAndEnvMacBridge(t *testing.T) {
	vc := testVmalertConfig()
	got := vmalertArgsAndEnv(vc, true, "pgcli-net", "/srv/vmalert/rules", "/srv/vmalert/templates")
	wantPrefix := []string{
		"run", "-d", "--name", "pgcli-vmalert-vm1",
		"--network", "pgcli-net", "-p", "8880:8880",
		"--http-proxy=false",
		"--restart", "unless-stopped",
	}
	if !reflect.DeepEqual(got[:len(wantPrefix)], wantPrefix) {
		t.Errorf("prefix = %#v, want %#v", got[:len(wantPrefix)], wantPrefix)
	}
	// The published port is unreachable if the process binds loopback only.
	if last := got[len(got)-2]; last != "-httpListenAddr=0.0.0.0:8880" {
		t.Errorf("listen flag = %q, want -httpListenAddr=0.0.0.0:8880", last)
	}
}

func TestVmalertArgsOmitEmptySections(t *testing.T) {
	vc := &config.VmalertConfig{
		Name:          "vm1",
		ContainerName: "pgcli-vmalert-vm1",
		ImageTag:      config.DefaultVmalertImageTag,
		HostPort:      8880,
		Listen:        "0.0.0.0",
		DatasourceURL: "http://127.0.0.1:9428",
	}
	got := vmalertArgsAndEnv(vc, false, "pgcli-net", "/srv/vmalert/rules", "/srv/vmalert/templates")
	for _, a := range got {
		switch a {
		case "-notifier.url=", "-remoteWrite.url=", "-remoteRead.url=", "-external.url=":
			t.Errorf("empty optional flag emitted: %q", a)
		}
	}
	if last := got[len(got)-1]; last != "-httpListenAddr=0.0.0.0:8880" {
		t.Errorf("last flag = %q, want -httpListenAddr=0.0.0.0:8880", last)
	}
}

func TestVmalertDirResolution(t *testing.T) {
	m := &VmalertManager{dataDir: "/base"}
	vc := &config.VmalertConfig{Name: "vm1"}
	if got := m.RulesDir(vc); got != "/base/addon/vmalert/vm1/rules" {
		t.Errorf("default rules dir = %q", got)
	}
	if got := m.TemplatesDir(vc); got != "/base/addon/vmalert/vm1/templates" {
		t.Errorf("default templates dir = %q", got)
	}
	vc.RulesDir = "/custom/rules"
	vc.TemplatesDir = "/custom/templates"
	if got := m.RulesDir(vc); got != "/custom/rules" {
		t.Errorf("explicit rules dir = %q", got)
	}
	if got := m.TemplatesDir(vc); got != "/custom/templates" {
		t.Errorf("explicit templates dir = %q", got)
	}
}

func TestVmalertEndpoint(t *testing.T) {
	m := &VmalertManager{}
	tests := []struct {
		listen string
		want   string
	}{
		{"", "http://127.0.0.1:8880"},
		{"0.0.0.0", "http://127.0.0.1:8880"},
		{"::", "http://127.0.0.1:8880"},
		{"127.0.0.1", "http://127.0.0.1:8880"},
		{"192.0.2.10", "http://192.0.2.10:8880"},
	}
	for _, tt := range tests {
		vc := &config.VmalertConfig{HostPort: 8880, Listen: tt.listen}
		if got := m.Endpoint(vc); got != tt.want {
			t.Errorf("Endpoint(%q) = %q, want %q", tt.listen, got, tt.want)
		}
	}
}

func TestVmalertDirsToClean(t *testing.T) {
	tests := []struct {
		name string
		vc   config.VmalertConfig
		want []string
	}{
		{"both default", config.VmalertConfig{}, []string{"/base/rules", "/base/templates"}},
		{"explicit rules", config.VmalertConfig{RulesDir: "/custom/rules"}, []string{"/base/templates"}},
		{"explicit templates", config.VmalertConfig{TemplatesDir: "/custom/templates"}, []string{"/base/rules"}},
		{"both explicit", config.VmalertConfig{RulesDir: "/custom/rules", TemplatesDir: "/custom/templates"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vmalertDirsToClean(&tt.vc, "/base/rules", "/base/templates")
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("dirs = %#v, want %#v", got, tt.want)
			}
		})
	}
}
