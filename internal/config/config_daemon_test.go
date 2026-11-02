package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidDaemonName(t *testing.T) {
	valid := []string{"webhook", "w", "hook_1", "hook-1", strings.Repeat("a", 48)}
	for _, name := range valid {
		if !ValidDaemonName(name) {
			t.Errorf("ValidDaemonName(%q) = false, want true", name)
		}
	}
	invalid := []string{"", "_hook", "-hook", "hook.app", "hook/app", "hook app", strings.Repeat("a", 49)}
	for _, name := range invalid {
		if ValidDaemonName(name) {
			t.Errorf("ValidDaemonName(%q) = true, want false", name)
		}
	}
}

func TestDaemonSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	cfg := Default()
	cfg.Daemons = map[string]DaemonConfig{
		"webhook": {
			Command:   []string{"/opt/webhook/bin/uvicorn", "app:app", "--port", "18801"},
			Dir:       "/opt/webhook",
			LogFile:   "/var/log/webhook.log",
			Autostart: true,
		},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d, ok := loaded.Daemons["webhook"]
	if !ok {
		t.Fatalf("daemon webhook missing after reload: %+v", loaded.Daemons)
	}
	if len(d.Command) != 4 || d.Command[0] != "/opt/webhook/bin/uvicorn" {
		t.Errorf("Command = %v", d.Command)
	}
	if d.Dir != "/opt/webhook" || d.LogFile != "/var/log/webhook.log" || !d.Autostart {
		t.Errorf("daemon = %+v", d)
	}
}

func TestDaemonDisplayWithoutEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")
	if err := Default().Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(data), "daemons:") {
		t.Errorf("empty daemons should be omitted, got:\n%s", data)
	}
}
