package podman

import (
	"fmt"
	"strings"
)

// MetaEngineURL normalises a pgcli instance DSN (GetPostgresURL after
// SetInstance) into a metadata-engine URL JuiceFS accepts: lib/pq requires
// sslmode explicitly, and a pgcli-managed local connection is plaintext
// loopback, so disable it unless the caller already chose a mode.
func MetaEngineURL(dsn string) string {
	if strings.Contains(dsn, "sslmode=") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "sslmode=disable"
}

// EnsureMetaDB creates the metadata database if it does not exist yet,
// idempotently: pgcli never creates databases, but JuiceFS's format refuses
// to run against a missing one, and reinstalling an addon must not fail on
// the second pass.
//
// The SELECT first is the fast path; the CREATE then runs as its own statement
// (a DO/PL-pgSQL block cannot host CREATE DATABASE — it refuses to run inside a
// transaction block), and a lost create race surfaces as "already exists",
// which is exactly the success we wanted.
//
// The dbname must already be a safe identifier (the CLI derives
// juicefs_<name> with hyphens converted); it is double-quoted so no reserved
// word can trip CREATE DATABASE.
func (m *Manager) EnsureMetaDB(dsn, dbname string) error {
	out, err := m.ExecDSNQuery(dsn, fmt.Sprintf("SELECT 1 FROM pg_database WHERE datname = '%s'", escapeSQLString(dbname)))
	if err != nil {
		return fmt.Errorf("checking database %q: %w", dbname, err)
	}
	if strings.TrimSpace(out) == "1" {
		return nil
	}
	if _, err := m.ExecDSNQuery(dsn, fmt.Sprintf(`CREATE DATABASE "%s"`, dbname)); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return fmt.Errorf("creating metadata database %q: %w", dbname, err)
	}
	return nil
}

// escapeSQLString doubles single quotes for safe embedding in a literal.
func escapeSQLString(s string) string { return strings.ReplaceAll(s, "'", "''") }
