package podman

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// TestAlertmanagerArgsAndEnv pins the `podman run` argv for Alertmanager across
// both networking shapes. Same contract shape as Prometheus (mounted config
// FILE plus trailing CLI flags AFTER the image name) with one extra fact worth
// pinning: --cluster.listen-address= (empty) must always be present — it is
// what keeps a single instance from opening the 9094 HA gossip port (TCP and
// UDP) and from logging peer-bootstrap failures forever.
func TestAlertmanagerArgsAndEnv(t *testing.T) {
	base := func() *config.AlertmanagerConfig {
		return &config.AlertmanagerConfig{
			ContainerName: "pgcli-alertmanager-am1",
			Name:          "am1",
			HostPort:      39093,
			Listen:        "0.0.0.0",
			ImageTag:      config.DefaultAlertmanagerImageTag,
		}
	}

	hasMount := func(args []string, substr string) bool {
		for i, a := range args {
			if a == "-v" && i+1 < len(args) && strings.Contains(args[i+1], substr) {
				return true
			}
		}
		return false
	}
	hasFlagValue := func(args []string, want string) bool {
		return slices.Contains(args, want)
	}
	imageIdx := func(args []string) int {
		return slices.Index(args, config.DefaultAlertmanagerImageTag)
	}

	t.Run("linux host networking", func(t *testing.T) {
		ac := base()
		args := alertmanagerArgsAndEnv(ac, false, "pgcli-net", "/home/u/.pgcli/addon/alertmanager/am1/data", "/home/u/.pgcli/addon/alertmanager/am1/alertmanager.yml")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "host") {
			t.Errorf("expected --network host, got %v", args)
		}
		if slices.Contains(args, "-p") {
			t.Errorf("host networking must not publish ports, got %v", args)
		}
		if !slices.Contains(args, "--http-proxy=false") {
			t.Errorf("--http-proxy=false missing: %v", args)
		}
		if !hasFlagValue(args, "--restart") || !slices.Contains(args, "unless-stopped") {
			t.Errorf("--restart unless-stopped missing: %v", args)
		}
		if !hasMount(args, "/home/u/.pgcli/addon/alertmanager/am1/data:/alertmanager:z") {
			t.Errorf("data dir mount missing: %v", args)
		}
		if !hasMount(args, "/home/u/.pgcli/addon/alertmanager/am1/alertmanager.yml:/etc/alertmanager/alertmanager.yml:ro,z") {
			t.Errorf("config file mount missing (or not read-only): %v", args)
		}
		// Ownership contract with Prometheus: NO --user flag — the image's baked
		// nobody (65534) must run, since the data dir arrives pre-chowned by
		// createContainer rather than being fixed up by the entrypoint.
		if slices.Contains(args, "--user") {
			t.Errorf("Alertmanager must not pass --user (the image runs as 65534): %v", args)
		}

		idx := imageIdx(args)
		if idx == -1 {
			t.Fatal("image tag not found in args")
		}
		if idx == len(args)-1 {
			t.Fatal("image tag must not be last — alertmanager needs trailing config flags after the image name")
		}
		trailing := args[idx+1:]
		if !hasFlagValue(trailing, "--config.file=/etc/alertmanager/alertmanager.yml") {
			t.Errorf("--config.file missing from trailing flags: %v", trailing)
		}
		if !hasFlagValue(trailing, "--web.listen-address=0.0.0.0:39093") {
			t.Errorf("--web.listen-address missing from trailing flags: %v", trailing)
		}
		if !hasFlagValue(trailing, "--storage.path=/alertmanager") {
			t.Errorf("--storage.path missing from trailing flags: %v", trailing)
		}
		// THE single-instance contract: cluster gossip is explicitly DISABLED,
		// not left at its default (which would open TCP+UDP 9094 and never stop
		// trying to bootstrap peers that will never arrive).
		if !hasFlagValue(trailing, "--cluster.listen-address=") {
			t.Errorf("--cluster.listen-address= (empty, disabling 9094) missing from trailing flags: %v", trailing)
		}
	})

	t.Run("macOS bridge networking publishes port and keeps a non-loopback bind", func(t *testing.T) {
		ac := base()
		ac.Listen = "127.0.0.1" // even a loopback listen is widened under bridge
		args := alertmanagerArgsAndEnv(ac, true, "pgcli-net", "/data", "/cfg/alertmanager.yml")

		if !slices.Contains(args, "--network") || !slices.Contains(args, "pgcli-net") {
			t.Errorf("expected --network pgcli-net, got %v", args)
		}
		if !slices.Contains(args, "-p") || !slices.Contains(args, "39093:39093") {
			t.Errorf("bridge must publish 39093:39093, got %v", args)
		}
		// The published port can't reach a loopback-only bind.
		if !slices.Contains(args, "--web.listen-address=0.0.0.0:39093") {
			t.Errorf("bridge should widen loopback to 0.0.0.0, got %v", args)
		}
		if slices.Contains(args, "--web.listen-address=127.0.0.1:39093") {
			t.Errorf("bridge must not keep the loopback bind: %v", args)
		}
	})

	t.Run("explicit non-loopback listen passes through", func(t *testing.T) {
		ac := base()
		ac.Listen = "10.0.0.5"
		args := alertmanagerArgsAndEnv(ac, true, "pgcli-net", "/data", "/cfg/alertmanager.yml")
		if !slices.Contains(args, "--web.listen-address=10.0.0.5:39093") {
			t.Errorf("explicit bind should pass through under bridge: %v", args)
		}
	})
}

func TestRenderAlertmanagerConfig(t *testing.T) {
	base := func() *config.AlertmanagerConfig {
		return &config.AlertmanagerConfig{
			Name:     "am1",
			HostPort: 9093,
		}
	}

	t.Run("no webhook renders a no-op receiver", func(t *testing.T) {
		ac := base()
		out, err := renderAlertmanagerConfig(ac)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, `receiver: "no-op"`) {
			t.Errorf("root route must point at the no-op receiver when no webhook is set:\n%s", out)
		}
		if !strings.Contains(out, `- name: "no-op"`) {
			t.Errorf("the route's receiver must also be DEFINED (Alertmanager rejects a dangling reference):\n%s", out)
		}
		if strings.Contains(out, "webhook_configs") {
			t.Errorf("no-op receiver must not carry webhook_configs:\n%s", out)
		}
	})

	t.Run("webhook URLs fold into one web.hook receiver", func(t *testing.T) {
		ac := base()
		ac.Webhook = []string{"http://127.0.0.1:5001/", "https://hooks.example.com/alert?token=abc"}
		out, err := renderAlertmanagerConfig(ac)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, `receiver: "web.hook"`) {
			t.Errorf("root route must point at web.hook when webhooks are set:\n%s", out)
		}
		if !strings.Contains(out, `- name: "web.hook"`) {
			t.Errorf("web.hook receiver must be defined:\n%s", out)
		}
		if !strings.Contains(out, `webhook_configs:`) {
			t.Errorf("webhook_configs missing:\n%s", out)
		}
		if !strings.Contains(out, `- url: "http://127.0.0.1:5001/"`) {
			t.Errorf("first webhook URL missing:\n%s", out)
		}
		if !strings.Contains(out, `- url: "https://hooks.example.com/alert?token=abc"`) {
			t.Errorf("second webhook URL missing:\n%s", out)
		}
		// One receiver, several URLs — not one receiver per URL.
		if got := strings.Count(out, `- name: `); got != 1 {
			t.Errorf("want exactly one receiver, got %d:\n%s", got, out)
		}
		if got := strings.Count(out, "- url: "); got != 2 {
			t.Errorf("want exactly two webhook URLs under the one receiver, got %d:\n%s", got, out)
		}
	})

	t.Run("malformed webhook URL errors instead of rendering", func(t *testing.T) {
		for _, bad := range []string{"not a url", "://missing-scheme", "http://"} {
			ac := base()
			ac.Webhook = []string{bad}
			if _, err := renderAlertmanagerConfig(ac); err == nil {
				t.Errorf("want an error for webhook %q, got nil", bad)
			}
		}
	})
}

func TestValidateWebhooks(t *testing.T) {
	if err := ValidateWebhooks(nil); err != nil {
		t.Errorf("empty list must be valid (no-op receiver mode): %v", err)
	}
	if err := ValidateWebhooks([]string{"http://127.0.0.1:5001/", "https://example.com/hook"}); err != nil {
		t.Errorf("valid URLs rejected: %v", err)
	}
	for _, bad := range []string{"not a url", "://missing-scheme", "http://", "ftp://example.com/hook"} {
		if err := ValidateWebhooks([]string{bad}); err == nil {
			t.Errorf("want an error for %q, got nil", bad)
		}
	}
}

// TestWriteConfigAlertmanagerRenderVsConfFile pins the same core --conf-file
// contract as Prometheus: the mounted path is ALWAYS pgcli's managed copy,
// never the operator's original file, and conf-file mode must not touch the
// original.
func TestWriteConfigAlertmanagerRenderVsConfFile(t *testing.T) {
	base := t.TempDir()
	m := &AlertmanagerManager{dataDir: base}

	t.Run("render mode writes the generated file", func(t *testing.T) {
		ac := &config.AlertmanagerConfig{Name: "am1", HostPort: 9093}
		path, err := m.WriteConfig(ac)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(base, "addon", "alertmanager", "am1", "alertmanager.yml")
		if path != want {
			t.Errorf("path = %q, want %q", path, want)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `receiver: "no-op"`) {
			t.Errorf("rendered file missing the no-op route:\n%s", data)
		}
	})

	t.Run("conf-file mode copies verbatim into the managed path, original untouched", func(t *testing.T) {
		userFile := filepath.Join(t.TempDir(), "my-am.yml")
		userContent := "# operator's own file, must survive byte-for-byte\n" +
			"route:\n  receiver: only-the-operator-has-this\n"
		if err := os.WriteFile(userFile, []byte(userContent), 0644); err != nil {
			t.Fatal(err)
		}
		ac := &config.AlertmanagerConfig{Name: "am2", HostPort: 9093, ConfFile: userFile}

		path, err := m.WriteConfig(ac)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(base, "addon", "alertmanager", "am2", "alertmanager.yml")
		if path != want {
			t.Errorf("path = %q, want the managed copy %q (never the operator's original)", path, want)
		}
		if path == userFile {
			t.Fatal("conf-file mode must mount pgcli's copy, not the operator's original path")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != userContent {
			t.Errorf("managed copy differs from the operator's file:\n%s", data)
		}
		if strings.Contains(string(data), `receiver: "no-op"`) {
			t.Errorf("conf-file mode must not render pgcli's own route into the copy:\n%s", data)
		}

		orig, err := os.ReadFile(userFile)
		if err != nil {
			t.Fatal(err)
		}
		if string(orig) != userContent {
			t.Errorf("the operator's original file was modified:\n%s", orig)
		}
	})

	t.Run("conf-file mode rejects an empty file", func(t *testing.T) {
		emptyFile := filepath.Join(t.TempDir(), "empty.yml")
		if err := os.WriteFile(emptyFile, nil, 0644); err != nil {
			t.Fatal(err)
		}
		ac := &config.AlertmanagerConfig{Name: "am3", HostPort: 9093, ConfFile: emptyFile}
		if _, err := m.WriteConfig(ac); err == nil {
			t.Error("want an error for an empty conf-file, got nil")
		}
	})
}
