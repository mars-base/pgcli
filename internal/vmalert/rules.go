// Package vmalert holds the host-side pieces of the vmalert addon that do not
// depend on podman: seeding the rules/templates directories and talking to
// vmalert's HTTP API.
package vmalert

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const seedRulesFile = "alert.rules.yaml"

const seedRulesContent = `# vmalert rules for this instance.
# Only *.yaml files in this directory are loaded (*.yml is ignored).
# Edit this file, then run: pg addon vmalert reload
groups: []
`

const seedTemplateFile = "default.tpl"

const seedTemplateContent = "{{/* Add notification templates here. Only *.tpl files are loaded. */}}\n"

// SeedRulesDir creates dir when missing and writes an empty rules file only
// when the directory contains no *.yaml file. Existing files are never touched.
func SeedRulesDir(dir string) error {
	return seedDir(dir, "*.yaml", seedRulesFile, seedRulesContent, "rules")
}

// SeedTemplatesDir creates dir when missing and writes a default template only
// when the directory contains no *.tpl file. Existing files are never touched.
func SeedTemplatesDir(dir string) error {
	return seedDir(dir, "*.tpl", seedTemplateFile, seedTemplateContent, "templates")
}

func seedDir(dir, pattern, fileName, content, kind string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating vmalert %s dir %s: %w", kind, dir, err)
	}
	found, err := hasMatchingFile(dir, pattern)
	if err != nil {
		return fmt.Errorf("scanning vmalert %s dir %s: %w", kind, dir, err)
	}
	if found {
		return nil
	}
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing vmalert seed %s: %w", path, err)
	}
	return nil
}

func hasMatchingFile(dir, pattern string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	suffix := strings.TrimPrefix(pattern, "*")
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), suffix) {
			return true, nil
		}
	}
	return false, nil
}

// CountRuleFiles returns the number of top-level *.yaml files vmalert loads.
func CountRuleFiles(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".yaml") {
			n++
		}
	}
	return n, nil
}
