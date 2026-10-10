package podman

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// pgpass pre-registration for a seeded server. servers.json cannot carry a
// password ("Password fields cannot be imported or exported"), but pgAdmin
// supports libpq's per-server passfile through ConnectionParameters.passfile
// (an absolute path is honoured; see the pgAdmin 9.18 Import/Export Servers
// example). With it, connect() skips the password prompt and psycopg passes
// passfile through to libpq, which reads the secret from this file instead.
//
// The file is mounted at /var/lib/pgadmin/pgpass (INSIDE the dir the image's
// entrypoint chowns on every start), mode 0600 — libpq refuses a passfile that
// is not owned by the connecting uid and not private to them, and the
// entrypoint's chown to 5050 fixes the ownership for free.

// pgAdminStoragePgPass is the pgpass file's name INSIDE the user's storage
// directory. pgAdmin runs in SERVER_MODE, and get_complete_file_path() only
// resolves files under /var/lib/pgadmin/storage/<email_dir>/ — paths outside
// that tree are silently rejected (returns None). We therefore mount the
// passfile there and reference it by bare name in ConnectionParameters.passfile.
const pgAdminStoragePgPass = "pgpass"

// errNoPgPassPassword is the sentinel when a DSN carries no password — the
// seed then renders servers.json only, and pgAdmin prompts on first connect as
// it always did. Callers treat this as a non-error skip.
var errNoPgPassPassword = errors.New("DSN carries no password")

// DSNHasPassword reports whether the DSN carries a password, i.e. whether
// seeding will also configure a passfile. Used by the CLI to say up front
// whether the first connect will prompt. Pure function (no I/O).
func DSNHasPassword(dsn string) bool {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		return false
	}
	pw, ok := u.User.Password()
	return ok && pw != ""
}

// RenderPgPass turns a postgres:// DSN into one libpq pgpass line
// (host:port:db:user:password), with `:` and `\` escaped in the fields. The
// database field is the wildcard `*` on purpose: pgAdmin connects once to the
// seeded MaintenanceDB and then to each database the user expands in the tree,
// and a maintenance-db-only entry would re-prompt on every other database of
// the same server (the password is the same server-level secret anyway).
// Returns errNoPgPassPassword when the DSN has no password. Pure function (no
// I/O) so it is unit-testable.
func RenderPgPass(dsn string) ([]byte, error) {
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
	password, ok := u.User.Password()
	if !ok || password == "" {
		return nil, errNoPgPassPassword
	}
	user := u.User.Username()

	line := strings.Join([]string{
		escapePgPassField(host),
		strconv.Itoa(port),
		"*", // any database on this server, see RenderPgPass doc
		escapePgPassField(user),
		escapePgPassField(password),
	}, ":") + "\n"
	return []byte(line), nil
}

// WritePgPass renders the DSN to a pgpass file at path, mode 0600 (libpq
// rejects anything looser). Any previous file is always unlinked first, for two
// reasons: a re-render must never leave a stale secret behind (including the
// errNoPgPassPassword case), and after a container run the image's entrypoint
// has chowned the file to the mapped 5050 uid, which the host user cannot
// truncate but still can unlink (deletion needs write on the owning directory,
// not on the file).
func WritePgPass(path, dsn string) error {
	data, err := RenderPgPass(dsn)
	if rerr := os.Remove(path); rerr != nil && !os.IsNotExist(rerr) {
		return fmt.Errorf("removing previous pgpass: %w", rerr)
	}
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing pgpass: %w", err)
	}
	return nil
}

// escapePgPassField escapes a pgpass field: `:` and `\` are field separators /
// escape characters there and must be backslash-escaped to survive verbatim.
func escapePgPassField(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `:`, `\:`)
}
