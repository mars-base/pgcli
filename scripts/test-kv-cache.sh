#!/usr/bin/env bash
# Test script for pg kv cache functions
# Usage: ./test-kv-cache.sh [instance]

INSTANCE="${1:-default}"
DATABASE="postgres"

PASS=0
FAIL=0

run_test() {
    local name="$1"
    local sql="$2"
    local expected="$3"

    result=$(pg exec -i "$INSTANCE" -- psql -U admin -d "$DATABASE" -t -A -c "$sql" 2>&1 || echo "ERROR")

    if [[ "$result" == "$expected" ]]; then
        echo "✓ $name"
        PASS=$((PASS + 1))
    else
        echo "✗ $name"
        echo "  Expected: $expected"
        echo "  Got:      $result"
        FAIL=$((FAIL + 1))
    fi
}

echo "=== Testing BYTEA KV cache ==="

# Test kv_set and kv_get (use dollar-quoting to avoid shell escaping)
run_test "kv_set returns void" \
    "SELECT kv_set('test:key1', \$\$hello\$\$::bytea, 60)" \
    ""

run_test "kv_get returns bytea" \
    "SELECT kv_get('test:key1')" \
    "\\x68656c6c6f"

run_test "kv_get_text returns text" \
    "SELECT kv_get_text('test:key1')" \
    "hello"

# Test kv_set_with_result
run_test "kv_set_with_result returns true" \
    "SELECT kv_set_with_result('test:key2', \$\$world\$\$::bytea, 60)" \
    "t"

# Test kv_del
run_test "kv_del removes entry" \
    "SELECT kv_del('test:key1')" \
    ""

run_test "kv_get returns NULL after delete" \
    "SELECT kv_get('test:key1')" \
    ""

# Test kv_cleanup_batch
run_test "kv_set with short TTL" \
    "SELECT kv_set('test:expire', \$\$data\$\$::bytea, 1)" \
    ""

run_test "sleep before kv cleanup" \
    "SELECT pg_sleep(1.5)" \
    ""

run_test "kv_cleanup_batch deletes expired" \
    "SELECT kv_cleanup_batch(100)" \
    "1"

echo ""
echo "=== Testing JSONB KV cache ==="

# Test json_set and json_get (dollar-quoting preserves JSON quotes)
run_test "json_set returns void" \
    "SELECT json_set('user:1', \$\${\"name\":\"Alice\",\"age\":30}\$\$::jsonb, 300)" \
    ""

run_test "json_get returns jsonb field" \
    "SELECT json_get('user:1')->>'name'" \
    "Alice"

run_test "json_set_with_result returns true" \
    "SELECT json_set_with_result('user:2', \$\${\"name\":\"Bob\"}\$\$::jsonb, 300)" \
    "t"

# Test json_get_field
run_test "json_get_field returns text" \
    "SELECT json_get_field('user:1', 'name')" \
    "Alice"

run_test "json_get_field returns NULL for missing field" \
    "SELECT json_get_field('user:1', 'email')" \
    ""

# Test json_get_field_json
run_test "json_get_field_json returns jsonb" \
    "SELECT json_get_field_json('user:1', 'age')::text" \
    "30"

# Test json_contains
run_test "json_contains returns true for matching subset" \
    "SELECT json_contains('user:1', \$\${\"age\":30}\$\$::jsonb)" \
    "t"

run_test "json_contains returns false for non-matching" \
    "SELECT json_contains('user:1', \$\${\"age\":25}\$\$::jsonb)" \
    "f"

# Test json_update_field
run_test "json_update_field updates existing field" \
    "SELECT json_update_field('user:1', 'age', '25')" \
    "t"

run_test "json_update_field new value persisted" \
    "SELECT json_get_field('user:1', 'age')" \
    "25"

run_test "json_update_field creates new field" \
    "SELECT json_update_field('user:1', 'email', 'alice@example.com')" \
    "t"

run_test "json_update_field new field persisted" \
    "SELECT json_get_field('user:1', 'email')" \
    "alice@example.com"

# Test bug fix: SQL NULL should return FALSE (not delete field)
run_test "json_update_field rejects SQL NULL" \
    "SELECT json_update_field('user:1', 'age', NULL)" \
    "f"

run_test "json_update_field NULL preserves field" \
    "SELECT json_get_field('user:1', 'age')" \
    "25"

# Test bug fix: NULL ttl preserves expires_at
run_test "json_set for TTL test" \
    "SELECT json_set('user:3', \$\${\"x\":1}\$\$::jsonb, 60)" \
    ""

run_test "json_update_field NULL ttl keeps expiration" \
    "SELECT json_update_field('user:3', 'x', '2', NULL)" \
    "t"

run_test "expiration preserved after NULL ttl update" \
    "SELECT COUNT(*) FROM kv_cache_json WHERE key='user:3' AND expires_at > NOW() + INTERVAL '55 seconds'" \
    "1"

# Test json_del
run_test "json_del removes entry" \
    "SELECT json_del('user:1')" \
    ""

run_test "json_get returns NULL after delete" \
    "SELECT json_get('user:1')" \
    ""

# Test json_cleanup_batch
run_test "json_set with short TTL" \
    "SELECT json_set('user:expire', \$\${\"x\":1}\$\$::jsonb, 1)" \
    ""

run_test "sleep before json cleanup" \
    "SELECT pg_sleep(1.5)" \
    ""

run_test "json_cleanup_batch deletes expired" \
    "SELECT json_cleanup_batch(100)" \
    "1"

echo ""
echo "=== Summary ==="
echo "Passed: $PASS"
echo "Failed: $FAIL"

if [[ $FAIL -eq 0 ]]; then
    echo "✓ All tests passed"
    exit 0
else
    echo "✗ Some tests failed"
    exit 1
fi
