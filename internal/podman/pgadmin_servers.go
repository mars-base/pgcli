package podman

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
)

// servers.json pre-registration. pgAdmin's own entrypoint loads a file at
// /pgadmin4/servers.json on first launch (setup.py load-servers); pgcli renders
// a minimal one from a DSN when the operator installed with --dsn/--pg-name so
// the web UI opens with that server already listed. This is a one-time
// convenience, not a runtime coupling — pgAdmin manages its server list itself
// thereafter.
//
// The schema (verified against the pgAdmin 9.18 Import/Export docs): a
// top-level "Servers" object keyed by integer id, each entry needing
// Name/Group/Port/Username/MaintenanceDB + Host, with sslmode canonically under
// a ConnectionParameters object. Passwords CANNOT be carried in this file
// ("Password fields cannot be imported or exported") — the seed password lives
// in a separate pgpass file instead (see pgadmin_pgpass.go) and is referenced
// here via ConnectionParameters.passfile, so pgcli never writes a PostgreSQL
// password into servers.json itself.

type pgAdminServerEntry struct {
	Name             string         `json:"Name"`
	Group            string         `json:"Group"`
	Host             string         `json:"Host"`
	Port             int            `json:"Port"`
	MaintenanceDB    string         `json:"MaintenanceDB"`
	Username         string         `json:"Username"`
	SSLMode          string         `json:"SSLMode"` // legacy top-level form; harmless if ignored
	ConnectionParams map[string]any `json:"ConnectionParameters"`
}

type pgAdminServersFile struct {
	Servers map[string]pgAdminServerEntry `json:"Servers"`
}

// RenderServersJSON turns a postgres:// DSN into pgAdmin's servers.json bytes,
// registering one server. serverName is the UI display name; when empty the DSN
// host is used. pgpassContainerPath non-empty is the container-side path of the
// companion pgpass file (written by WritePgPass) and is recorded as the
// server's ConnectionParameters.passfile so the first connect authenticates
// without a prompt. Pure function (no I/O) so it is unit-testable. The DSN's
// password is deliberately parsed and DISCARDED here: pgAdmin cannot import
// passwords into this file, and it is bind-mounted into a container — leaking
// one here would be both useless and a secret exposure.
func RenderServersJSON(dsn, serverName, pgpassContainerPath string) ([]byte, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid DSN: %w", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return nil, fmt.Errorf("invalid DSN: scheme must be postgres://, got %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("invalid DSN: missing host")
	}
	port := 5432
	if p := u.Port(); p != "" {
		if port, err = strconv.Atoi(p); err != nil {
			return nil, fmt.Errorf("invalid DSN: bad port %q", p)
		}
	}
	user := u.User.Username()
	db := trimDSNPath(u.Path)
	if db == "" {
		db = "postgres" // pgAdmin needs a maintenance db; default the way its UI does
	}
	name := serverName
	if name == "" {
		name = host
	}

	connParams := map[string]any{"sslmode": "prefer"}
	if pgpassContainerPath != "" {
		connParams["passfile"] = pgpassContainerPath
	}
	doc := pgAdminServersFile{
		Servers: map[string]pgAdminServerEntry{
			"1": {
				Name:          name,
				Group:         "Servers",
				Host:          host,
				Port:          port,
				MaintenanceDB: db,
				Username:      user,
				SSLMode:       "prefer",
				ConnectionParams: connParams,
			},
		},
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling servers.json: %w", err)
	}
	return out, nil
}

// WriteServersJSON renders the DSN to JSON and writes it to path (mode 0644 so
// the rootless container's mapped 5050 user can read the :ro bind mount). The
// parent dir must already exist (createContainer mkdir's the data dir sibling).
// pgpassContainerPath is threaded through to the passfile reference ("" = no
// passfile, seed will prompt on first connect).
func WriteServersJSON(path, dsn, serverName, pgpassContainerPath string) error {
	data, err := RenderServersJSON(dsn, serverName, pgpassContainerPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing servers.json: %w", err)
	}
	return nil
}

// trimDSNPath returns the database name from a URL path ("/db" → "db").
func trimDSNPath(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}
