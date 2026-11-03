package vmalert

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSeedRulesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rules")
	if err := SeedRulesDir(dir); err != nil {
		t.Fatalf("seed: %v", err)
	}
	path := filepath.Join(dir, seedRulesFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	if string(data) != seedRulesContent {
		t.Errorf("seed content = %q, want %q", data, seedRulesContent)
	}
	if n, err := CountRuleFiles(dir); err != nil || n != 1 {
		t.Errorf("count = %d, err = %v, want 1, nil", n, err)
	}

	// User edits must survive a second seeding.
	if err := os.WriteFile(path, []byte("groups:\n  - name: custom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SeedRulesDir(dir); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	data, _ = os.ReadFile(path)
	if string(data) != "groups:\n  - name: custom\n" {
		t.Errorf("second seed overwrote user rules: %q", data)
	}
}

func TestSeedRulesDirIgnoresYmlAndExisting(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "old.yml"), []byte("groups: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SeedRulesDir(dir); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, seedRulesFile)); err != nil {
		t.Errorf("seed file missing when only *.yml existed: %v", err)
	}
	if n, _ := CountRuleFiles(dir); n != 1 {
		t.Errorf("count = %d, want 1 (*.yml must not count)", n)
	}
}

func TestSeedTemplatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "templates")
	if err := SeedTemplatesDir(dir); err != nil {
		t.Fatalf("seed: %v", err)
	}
	path := filepath.Join(dir, seedTemplateFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("seed template missing: %v", err)
	}
	custom := "{{ define \"x\" }}x{{ end }}\n"
	if err := os.WriteFile(path, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SeedTemplatesDir(dir); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != custom {
		t.Errorf("second seed overwrote user template: %q", data)
	}
}

func TestCountRuleFilesMissingDir(t *testing.T) {
	if _, err := CountRuleFiles(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected error for a missing directory")
	}
}
