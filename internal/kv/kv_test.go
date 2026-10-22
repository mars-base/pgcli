package kv

import (
	"strings"
	"testing"
)

func TestSQLFilesEmbedded(t *testing.T) {
	files := []string{
		"sql/pg-kv-cache.sql",
		"sql/pg-kv-cache-json.sql",
	}
	for _, f := range files {
		data, err := SQLs.ReadFile(f)
		if err != nil {
			t.Errorf("failed to read %s: %v", f, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("%s is empty", f)
		}
	}
}

func TestByteaCacheFunctions(t *testing.T) {
	data, err := SQLs.ReadFile("sql/pg-kv-cache.sql")
	if err != nil {
		t.Fatalf("failed to read pg-kv-cache.sql: %v", err)
	}
	content := string(data)

	// Check table creation
	if !strings.Contains(content, "CREATE UNLOGGED TABLE IF NOT EXISTS kv_cache") {
		t.Error("missing kv_cache table creation")
	}

	// Check all 6 functions
	functions := []string{
		"kv_set",
		"kv_set_with_result",
		"kv_get",
		"kv_get_text",
		"kv_del",
		"kv_cleanup_batch",
	}
	for _, fn := range functions {
		if !strings.Contains(content, "CREATE OR REPLACE FUNCTION "+fn) {
			t.Errorf("missing function %s", fn)
		}
	}

	// Check no test data (no \d+, no \pset, no SELECT kv_set with test keys)
	if strings.Contains(content, "\\d+") {
		t.Error("pg-kv-cache.sql should not contain \\d+ (test artifact)")
	}
	if strings.Contains(content, "\\pset") {
		t.Error("pg-kv-cache.sql should not contain \\pset (test artifact)")
	}
	if strings.Contains(content, "kv_set('user:") {
		t.Error("pg-kv-cache.sql should not contain test data writes")
	}

	// Check no CONCURRENTLY (we removed it)
	if strings.Contains(content, "CREATE INDEX CONCURRENTLY") {
		t.Error("pg-kv-cache.sql should not use CREATE INDEX CONCURRENTLY")
	}
}

func TestJsonCacheFunctions(t *testing.T) {
	data, err := SQLs.ReadFile("sql/pg-kv-cache-json.sql")
	if err != nil {
		t.Fatalf("failed to read pg-kv-cache-json.sql: %v", err)
	}
	content := string(data)

	// Check table creation
	if !strings.Contains(content, "CREATE UNLOGGED TABLE IF NOT EXISTS kv_cache_json") {
		t.Error("missing kv_cache_json table creation")
	}

	// Check all 9 functions
	functions := []string{
		"json_set",
		"json_set_with_result",
		"json_get",
		"json_get_field",
		"json_get_field_json",
		"json_contains",
		"json_update_field",
		"json_del",
		"json_cleanup_batch",
	}
	for _, fn := range functions {
		if !strings.Contains(content, "CREATE OR REPLACE FUNCTION "+fn) {
			t.Errorf("missing function %s", fn)
		}
	}

	// Check no test data
	if strings.Contains(content, "\\d+") {
		t.Error("pg-kv-cache-json.sql should not contain \\d+ (test artifact)")
	}
	if strings.Contains(content, "json_set('user:") {
		t.Error("pg-kv-cache-json.sql should not contain test data writes")
	}

	// Check no CONCURRENTLY
	if strings.Contains(content, "CREATE INDEX CONCURRENTLY") {
		t.Error("pg-kv-cache-json.sql should not use CREATE INDEX CONCURRENTLY")
	}

	// Check json_update_field bug fixes
	if !strings.Contains(content, "IF p_new_value IS NULL THEN") {
		t.Error("json_update_field should reject SQL NULL to prevent accidental field deletion")
	}
	if !strings.Contains(content, "p_ttl_seconds INT DEFAULT NULL") {
		t.Error("json_update_field should have p_ttl_seconds DEFAULT NULL")
	}
}
