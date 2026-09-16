// PatroniManager manages Patroni HA members. Patroni owns the postmaster
// lifecycle (initdb, start/stop, replication, failover) — pgcli owns only the
// container, image, per-member patroni.yml, ports and DCS wiring, and drives
// the cluster through patronictl in short-lived containers. This is a distinct
// mode from the plain `pg` instance path (which manages PG directly via
// docker-entrypoint + standby.signal) and shares no instance lifecycle code.
package podman

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	res "github.com/mars-base/pgcli/embed"
	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	yaml "gopkg.in/yaml.v3"
)

// DefaultPatroniImageTag is the built-in Patroni image when a member does not
// override it. Tag is dual-segment (PG version - Patroni version), mirroring
// pgcli-pg's `18-2.58.0`.
const DefaultPatroniImageTag = "ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5"

type PatroniManager struct {
	cfg     *config.Config
	podman  string
	dataDir string
}

func NewPatroniManager(cfg *config.Config) (*PatroniManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &PatroniManager{cfg: cfg, podman: path, dataDir: dataDir}, nil
}

// memberConfigDir is the host directory holding a member's patroni.yml. The
// path is keyed by the namespaced scope so two pgcli namespaces sharing one
// host's base dir do not collide, mirroring the DCS key layout.
func (m *PatroniManager) memberConfigDir(nsScope, member string) string {
	return filepath.Join(m.dataDir, "addon", "patroni", nsScope, member)
}

// dcsEndpoints resolves the etcd3.hosts list: explicit EtcdEndpoints win
// (external / cross-host DCS); otherwise EtcdMembers are looked up in the
// etcd addon and reduced to host:client-port. Patroni's etcd3.hosts takes a
// comma-joined host:port list with no scheme (unlike etcdctl's URL env).
func (m *PatroniManager) dcsEndpoints(cluster *config.PatroniClusterConfig) (string, error) {
	if len(cluster.EtcdEndpoints) > 0 {
		return strings.Join(cluster.EtcdEndpoints, ","), nil
	}
	if len(cluster.EtcdMembers) == 0 {
		return "", fmt.Errorf("cluster %q has no DCS: set --etcd <members> or --etcd-endpoints <host:port,...>", cluster.Name)
	}
	parts := make([]string, 0, len(cluster.EtcdMembers))
	for _, name := range cluster.EtcdMembers {
		ec, ok := m.cfg.Addons.Etcd[name]
		if !ok {
			return "", fmt.Errorf("etcd member %q referenced by patroni cluster %q is not installed (run `pg addon install etcd --name %s`)", name, cluster.Name, name)
		}
		parts = append(parts, fmt.Sprintf("%s:%d", ec.AdvertiseAddr(), ec.ClientPort))
	}
	return strings.Join(parts, ","), nil
}

// renderPatroniYML builds the member's patroni.yml value. It is the single
// place the shape is defined so both WriteMemberConfig (on-disk, 0600) and
// Patronictl's ephemeral config (with connect_address forced to loopback)
// reuse it. member carries the resolved ports/advertise host; nsScope is the
// namespaced scope that goes into the DCS.
func (m *PatroniManager) renderPatroniYML(cluster *config.PatroniClusterConfig, member string, mb config.PatroniMemberConfig, nsScope, endpoints string) (map[string]any, error) {
	pgListen := "127.0.0.1"
	pgConnect := "127.0.0.1"
	restListen := "127.0.0.1"
	restConnect := "127.0.0.1"
	if mb.AdvertiseHost != "" {
		pgListen = "0.0.0.0"
		pgConnect = mb.AdvertiseHost
		restListen = "0.0.0.0"
		restConnect = mb.AdvertiseHost
	}

	// Paths below are IN-CONTAINER: the member's host config dir is bind-mounted
	// at /patroni, and data dir at /var/lib/postgresql (so PGDATA is
	// /var/lib/postgresql/data). Patroni resolves data_dir/bin_dir/pgpass inside
	// the container, so a host path here would not exist at runtime. pgpass
	// lands next to patroni.yml (which is why that mount is rw, not :ro).
	pgPassPath := "/patroni/.pgpass"

	doc := map[string]any{
		"scope":     nsScope,
		"namespace": "/service/",
		"name":      member,
		"restapi": map[string]any{
			"listen":          fmt.Sprintf("%s:%d", restListen, mb.RestapiPort),
			"connect_address": fmt.Sprintf("%s:%d", restConnect, mb.RestapiPort),
			"authentication": map[string]any{
				"username": cluster.Passwords.RestapiUser,
				"password": cluster.Passwords.RestapiPasswd,
			},
		},
		"etcd3": map[string]any{
			"hosts": endpoints,
			// Patroni talks to etcd v3; protocol default is already http.
			"protocol": "http",
		},
		"bootstrap": map[string]any{
			"dcs": map[string]any{
				"ttl":                     30,
				"loop_wait":               10,
				"retry_timeout":           10,
				"maximum_lag_on_failover": 1048576,
				"postgresql": map[string]any{
					"use_pg_rewind": true,
					"use_slots":     true,
					"parameters": map[string]any{
						"wal_level":   "replica",
						"hot_standby": "on",
					},
				},
			},
			// initdb options mix value flags (encoding=UTF8) and boolean flags.
			// A boolean must be a BARE list item: Patroni's process_user_options
			// renders {"data-checksums": ""} as `--data-checksums=`, which
			// PG 18's initdb rejects ("doesn't allow an argument"), while the
			// bare string renders flag-only.
			"initdb": []any{
				map[string]string{"encoding": "UTF8"},
				"data-checksums",
			},
			// pg_hba is rendered into the running PG every DCS cycle, so any
			// hand-edit is lost — this list is the source of truth. "all" for
			// the address because rootless podman's pasta rewrites loopback
			// connections to arrive from 192.168.10.1; a 127.0.0.1/32-only
			// rule would lock Patroni out of its own postmaster.
			"pg_hba": []string{
				"host all all all scram-sha-256",
				"host replication all all scram-sha-256",
			},
		},
		"postgresql": map[string]any{
			"listen":          fmt.Sprintf("%s:%d", pgListen, mb.HostPort),
			"connect_address": fmt.Sprintf("%s:%d", pgConnect, mb.HostPort),
			"data_dir":        "/var/lib/postgresql/data",
			"bin_dir":         "/usr/lib/postgresql/18/bin",
			"pgpass":          pgPassPath,
			"authentication": map[string]any{
				"superuser": map[string]string{
					"username": "postgres",
					"password": cluster.Passwords.Superuser,
				},
				"replication": map[string]string{
					"username": "replicator",
					"password": cluster.Passwords.Replication,
				},
				"rewind": map[string]string{
					"username": "rewind_user",
					"password": cluster.Passwords.Rewind,
				},
			},
			"parameters": map[string]any{
				"unix_socket_directories": "/var/lib/postgresql",
			},
		},
	}
	return doc, nil
}

// WriteMemberConfig renders patroni.yml for one member and writes it mode 0600
// (it embeds the superuser/replication/rewind/restapi passwords). The file is
// bind-mounted read-only into the member container and is also what
// `patronictl -c` reads.
func (m *PatroniManager) WriteMemberConfig(cluster *config.PatroniClusterConfig, member string) (string, error) {
	mb, ok := cluster.Members[member]
	if !ok {
		return "", fmt.Errorf("member %q is not part of cluster %q", member, cluster.Name)
	}
	endpoints, err := m.dcsEndpoints(cluster)
	if err != nil {
		return "", err
	}
	nsScope := m.cfg.PatroniScope(cluster.Name)
	doc, err := m.renderPatroniYML(cluster, member, mb, nsScope, endpoints)
	if err != nil {
		return "", err
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("rendering patroni.yml: %w", err)
	}
	dir := m.memberConfigDir(nsScope, member)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating patroni member dir: %w", err)
	}
	path := filepath.Join(dir, "patroni.yml")
	if err := os.WriteFile(path, out, 0600); err != nil {
		return "", fmt.Errorf("writing patroni.yml: %w", err)
	}

	// When running as root with root podman, the container runs as postgres
	// (uid 999) which cannot read files owned by root. Chown the config file
	// and data directory to postgres uid so the container can access them.
	if os.Geteuid() == 0 {
		if err := os.Chown(path, 999, 999); err != nil {
			return "", fmt.Errorf("chown patroni.yml to postgres: %w", err)
		}
		// Also chown the data directory if it exists
		dataDir := mb.DataDir
		if dataDir != "" {
			if err := os.MkdirAll(dataDir, 0755); err != nil {
				return "", fmt.Errorf("creating data dir: %w", err)
			}
			if err := os.Chown(dataDir, 999, 999); err != nil {
				return "", fmt.Errorf("chown data dir to postgres: %w", err)
			}
		}
	}
	return path, nil
}

// --- image ------------------------------------------------------------

// EnsurePatroniImage makes the Patroni image available: try the registry, then
// build from the embedded Containerfile. Mirrors EnsureBackupImage's
// pull→build fallback with a patroni-specific build context dir.
func (m *PatroniManager) EnsurePatroniImage(tag string) error {
	exists, err := m.imageExists(tag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling Patroni image %s...\n", tag)
	if _, err := m.run("pull", tag); err == nil {
		fmt.Println("  [OK] Patroni image pulled")
		return nil
	}
	fmt.Println("  Pull failed, falling back to local build...")
	return m.buildPatroniImage(tag)
}

func (m *PatroniManager) buildPatroniImage(tag string) error {
	fmt.Println("-> Building pgcli-patroni image...")
	buildDir := filepath.Join(m.dataDir, "patroni-build")
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return fmt.Errorf("creating patroni build dir: %w", err)
	}
	containerfile := filepath.Join(buildDir, "Containerfile")
	if err := os.WriteFile(containerfile, []byte(res.PatroniContainerfile), 0644); err != nil {
		return fmt.Errorf("writing patroni Containerfile: %w", err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "patroni-entrypoint.sh"), []byte(res.PatroniEntrypointShell), 0755); err != nil {
		return fmt.Errorf("writing patroni entrypoint: %w", err)
	}
	// Context must be the directory (patroni-entrypoint.sh is COPY'd from it),
	// with the Containerfile named via -f — same shape as buildBackupImage.
	if err := m.runInteractive("build", "--http-proxy=false", "-t", tag, "-f", containerfile, buildDir); err != nil {
		return fmt.Errorf("podman build patroni image: %w", err)
	}
	fmt.Println("  [OK] Patroni image built:", tag)
	return nil
}

// --- container lifecycle ---------------------------------------------

// createMemberContainer runs the Patroni member container. Patroni is PID 1
// (the image entrypoint execs `patroni /patroni/patroni.yml`). Config and
// pgpass live under the member dir which is bind-mounted at /patroni (rw —
// Patroni writes its pgpass there); the data dir is mounted at
// /var/lib/postgresql so PGDATA=/var/lib/postgresql/data matches the yml.
//
// For backup support, the backup container's public key is bind-mounted at
// /run/pgcli/backup_id_rsa.pub so the entrypoint can install it for sshd.
// generateContainerPasswd creates /etc/passwd and /etc/group files that map
// the postgres user to the host UID. This is needed for non-root users:
// --user <uid>:<gid> overrides the image's USER postgres (uid 999), making
// all processes run as the host user. But /etc/passwd still says postgres
// is uid 999, so sshd can't authenticate SSH connections as "postgres".
// The custom files fix this by mapping postgres to the actual container UID.
//
// Files are written to the member's config dir and bind-mounted read-only
// into the container at /etc/passwd and /etc/group.
func (m *PatroniManager) generateContainerPasswd(nsScope, member string) error {
	if os.Geteuid() == 0 {
		return nil // root: no mapping needed
	}
	uid := os.Getuid()
	gid := os.Getgid()
	dir := m.memberConfigDir(nsScope, member)

	passwd := fmt.Sprintf(`root:x:0:0:root:/root:/bin/sh
postgres:x:%d:%d:PostgreSQL user:/var/lib/postgresql:/bin/bash
nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin
_apt:x:42:65534::/nonexistent:/usr/sbin/nologin
`, uid, gid)

	group := fmt.Sprintf(`root:x:0:
postgres:x:%d:
nobody:x:65534:
_apt:x:42:
ssl-cert:x:101:
`, gid)

	if err := os.WriteFile(filepath.Join(dir, "passwd"), []byte(passwd), 0644); err != nil {
		return fmt.Errorf("writing container passwd: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "group"), []byte(group), 0644); err != nil {
		return fmt.Errorf("writing container group: %w", err)
	}
	return nil
}

func (m *PatroniManager) createMemberContainer(cluster *config.PatroniClusterConfig, member string) error {
	mb, ok := cluster.Members[member]
	if !ok {
		return fmt.Errorf("member %q is not part of cluster %q", member, cluster.Name)
	}
	if mb.RemoteHost != "" {
		return fmt.Errorf("member %q is remote (on %s); no local container to create", member, mb.RemoteHost)
	}
	nsScope := m.cfg.PatroniScope(cluster.Name)
	dir := m.memberConfigDir(nsScope, member)

	args := []string{
		"run", "-d",
		"--name", mb.ContainerName,
		"--hostname", member,
	}
	args = append(args, patroniUserFlags()...)
	args = append(args,
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"-e", fmt.Sprintf("PGCLI_SSH_PORT=%d", mb.SSHPort),
		"-v", fmt.Sprintf("%s:/var/lib/postgresql:z", hostMountPath(mb.DataDir)),
		"-v", fmt.Sprintf("%s:/patroni:z", hostMountPath(dir)),
	)

	// Non-root: mount custom /etc/passwd and /etc/group that map postgres
	// to the host UID. This makes sshd authenticate correctly when the
	// container runs with --user <uid>:<gid> (host user's UID).
	if os.Geteuid() != 0 {
		passwdPath := filepath.Join(dir, "passwd")
		groupPath := filepath.Join(dir, "group")
		if err := m.generateContainerPasswd(nsScope, member); err != nil {
			return fmt.Errorf("generating container passwd: %w", err)
		}
		args = append(args,
			"-v", fmt.Sprintf("%s:/etc/passwd:ro,z", hostMountPath(passwdPath)),
			"-v", fmt.Sprintf("%s:/etc/group:ro,z", hostMountPath(groupPath)),
		)
	}

	// Mount backup container's public key for SSH-based backup support.
	// The entrypoint script installs it in sshd's authorized_keys.
	bm, err := NewBackupManager(m.cfg)
	if err == nil {
		keys, err := bm.EnsureSSHKey()
		if err == nil {
			args = append(args,
				"-v", fmt.Sprintf("%s:/run/pgcli/backup_id_rsa.pub:ro,z", hostMountPath(keys.Public)),
			)
		}
		// Mount pgbackrest.conf so pgbackrest can run in this container.
		confPath := filepath.Join(m.dataDir, "pgbackrest.conf")
		if _, err := os.Stat(confPath); err == nil {
			args = append(args,
				"-v", fmt.Sprintf("%s:/etc/pgbackrest.conf:ro,z", hostMountPath(confPath)),
			)
		}
	}

	args = append(args, mb.ImageTag)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating Patroni member container %s: %w", member, err)
	}
	return nil
}

// patroniUserFlags returns podman flags for Patroni member containers.
// - Root users: no flags needed, root can access any file.
// - Non-root users: --userns=keep-id + --user <uid>:<gid> maps the container
//   process to the host user's UID so bind-mounted 0600 config files are
//   readable. This overrides the image's USER postgres, so a custom /etc/passwd
//   with a pgbackrest user at the host UID must be bind-mounted for SSH auth.
func patroniUserFlags() []string {
	if os.Geteuid() == 0 {
		return []string{}
	}
	return []string{
		"--userns", "keep-id",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
	}
}

// EnsureMemberContainer is install semantics: recreate the member container
// (stop+rm first, then run) from the freshly-written patroni.yml. Because
// Patroni is PID 1, recreating a member takes that node fully offline and, if
// it was the leader, triggers a failover — this is why callers must treat it
// as a destructive re-install, not a reload. `edit-config` must NOT go
// through here.
func (m *PatroniManager) EnsureMemberContainer(cluster *config.PatroniClusterConfig, member string) error {
	mb := cluster.Members[member]
	name := mb.ContainerName

	if running, err := m.containerRunning(name); err != nil {
		return err
	} else if running {
		fmt.Printf("-> Patroni member %s is running; recreating will offline it%s\n", member, " (and may trigger failover if it is the leader)")
		if _, err := m.run("stop", name); err != nil {
			return fmt.Errorf("stopping Patroni member %s: %w", member, err)
		}
	}
	if exists, err := m.containerExists(name); err != nil {
		return err
	} else if exists {
		if _, err := m.run("rm", "-f", name); err != nil {
			return fmt.Errorf("removing stale Patroni member %s: %w", member, err)
		}
	}
	if err := m.createMemberContainer(cluster, member); err != nil {
		return err
	}
	fmt.Printf("  [OK] Patroni member %s started\n", member)
	return nil
}

// StartMemberContainer is boot-only semantics (autostart): start the existing
// container, never re-render config. Running → no-op; exists-but-stopped →
// start (recreate on improper state); absent → error pointing at
// `pg ha create`, because creating from scratch at boot would risk a failover.
func (m *PatroniManager) StartMemberContainer(cluster *config.PatroniClusterConfig, member string) error {
	mb := cluster.Members[member]
	name := mb.ContainerName

	running, err := m.containerRunning(name)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] Patroni member %s already running\n", name)
		return nil
	}
	if exists, err := m.containerExists(name); err != nil {
		return err
	} else if exists {
		if _, err := m.run("start", name); err == nil {
			fmt.Printf("  [OK] Patroni member %s started\n", name)
			return nil
		}
		return fmt.Errorf("Patroni member %s is in an unrecoverable state — re-create it with `pg ha create %s --member %s`", name, cluster.Name, member)
	}
	return fmt.Errorf("no container for Patroni member %s — run `pg ha create %s --member %s` first", name, cluster.Name, member)
}

func (m *PatroniManager) StopMemberContainer(name string) (string, error) {
	return m.run("stop", name)
}

// RemoveMemberContainer stops and removes one member's container and deletes
// only pgcli's own files under the member dir. The dir itself — and the
// PostgreSQL data dir nested inside it — is left in place: the member dir
// doubles as the host data dir (one bind mount surfaced at two container
// paths), so wiping it here would destroy the data the CLI only removes with
// --clean-data.
func (m *PatroniManager) RemoveMemberContainer(cluster *config.PatroniClusterConfig, member string) error {
	mb, ok := cluster.Members[member]
	if !ok {
		return nil
	}
	m.run("stop", mb.ContainerName)
	if _, err := m.run("rm", "-f", mb.ContainerName); err != nil {
		return fmt.Errorf("removing Patroni member %s: %w", mb.ContainerName, err)
	}
	nsScope := m.cfg.PatroniScope(cluster.Name)
	dir := m.memberConfigDir(nsScope, member)
	// patroni.yml (written by pgcli, 0600, embeds passwords) and .pgpass
	// (written by the daemon) are the secret-bearing files pgcli owns; every
	// other entry under the dir is PG data or ephemeral runtime state and
	// stays for the --clean-data decision.
	for _, f := range []string{"patroni.yml", ".pgpass"} {
		p := filepath.Join(dir, f)
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			fmt.Printf("  [!] warning: removing %s: %v\n", p, err)
		}
	}
	fmt.Printf("  [OK] Removed Patroni member config: %s\n", dir)
	return nil
}

// ContainerRunning reports whether the named container is running.
func (m *PatroniManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// --- patronictl passthrough ------------------------------------------

// pickMemberYML returns a rendered patroni.yml usable as `patronictl -c`:
// prefer any local member of the cluster, and force its restapi connect_address
// to loopback so patronictl talks to whichever member's REST API the yml points
// at without needing cross-host reachability. With no local member we still
// render a throwaway config (needs reachable DCS endpoints).
func (m *PatroniManager) pickMemberYML(cluster *config.PatroniClusterConfig) (ymlPath string, cleanup func(), imageTag string, err error) {
	endpoints, err := m.dcsEndpoints(cluster)
	if err != nil {
		return "", nil, "", err
	}
	nsScope := m.cfg.PatroniScope(cluster.Name)

	// Sort member names so a stable pick is made across map iteration.
	members := make([]string, 0, len(cluster.Members))
	for k := range cluster.Members {
		members = append(members, k)
	}
	slices.Sort(members)

	// Prefer any member whose patroni.yml already exists on disk; that config
	// reflects how the member was actually created (correct ports, advertise
	// host) so patronictl talks to the cluster the same way the daemon does.
	for _, name := range members {
		p := filepath.Join(m.memberConfigDir(nsScope, name), "patroni.yml")
		if _, statErr := os.Stat(p); statErr == nil {
			mb := cluster.Members[name]
			tag := mb.ImageTag
			if tag == "" {
				tag = DefaultPatroniImageTag
			}
			return p, func() {}, tag, nil
		}
	}

	// No local member config (e.g. `status` run before `create`, or a purely
	// remote-first cluster). Synthesize a transient one from the first declared
	// member and force its REST connect_address to loopback so patronictl can
	// reach whichever member the config points at via host networking.
	if len(cluster.Members) == 0 {
		return "", nil, "", fmt.Errorf("cluster %q has no members — run `pg ha create %s --member <m>` first", cluster.Name, cluster.Name)
	}
	// Prefer a local member for the synthetic config — its REST/PG ports are
	// meaningful on this host. A remote member has no local container, so a
	// 127.0.0.1 connect_address pointing at it would be unreachable.
	chosen := ""
	for _, name := range members {
		if cluster.Members[name].RemoteHost == "" {
			chosen = name
			break
		}
	}
	if chosen == "" {
		chosen = members[0] // all remote: nothing better to point patronictl at
	}
	mb := cluster.Members[chosen]
	imageTag = mb.ImageTag
	if imageTag == "" {
		imageTag = DefaultPatroniImageTag
	}
	doc, err2 := m.renderPatroniYML(cluster, chosen, mb, nsScope, endpoints)
	if err2 != nil {
		return "", nil, "", err2
	}
	if r, ok := doc["restapi"].(map[string]any); ok {
		r["connect_address"] = fmt.Sprintf("127.0.0.1:%d", mb.RestapiPort)
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", nil, "", fmt.Errorf("rendering ephemeral patroni.yml: %w", err)
	}
	tmp, err := os.CreateTemp("", "pgcli-patroni-ctl-*.yml")
	if err != nil {
		return "", nil, "", fmt.Errorf("creating ephemeral patroni.yml: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", nil, "", fmt.Errorf("writing ephemeral patroni.yml: %w", err)
	}
	tmp.Close()
	os.Chmod(tmpName, 0600)
	return tmpName, func() { os.Remove(tmpName) }, imageTag, nil
}

// runPatronictl is the shared engine behind the patronictl entry points.
// stdioFlags is the `podman run` stdio selection: "-it" (terminal —
// confirmation prompts and $EDITOR work naturally), "-i" (stdin attached
// without a tty — scripted input) or "-i=false" (nothing attached — pure
// capture). stdin/stdout/stderr are wired verbatim into the podman process,
// nil meaning detached.
func (m *PatroniManager) runPatronictl(cluster *config.PatroniClusterConfig, stdioFlags string, stdin io.Reader, stdout, stderr io.Writer, args []string) error {
	ymlPath, cleanup, imageTag, err := m.pickMemberYML(cluster)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := m.EnsurePatroniImage(imageTag); err != nil {
		return err
	}

	// --entrypoint patronictl is REQUIRED: the image's ENTRYPOINT runs the
	// Patroni daemon, so trailing run-arguments would be ignored by the
	// entrypoint script — without the override a control command would boot a
	// full member (and could even bootstrap a cluster into the live DCS).
	// Only the config FILE is mounted, read-only: patronictl just reads it
	// (writing .pgpass is the daemon's job), so we never expose a whole
	// directory — the no-on-disk-config path renders an ephemeral yml under
	// /tmp. The config file is 0600 and host-owned, so the container must run
	// as us (see patroniUserFlags), not the image's baked-in postgres uid.
	runArgs := []string{"run", "--rm", stdioFlags}
	runArgs = append(runArgs, patroniUserFlags()...)
	runArgs = append(runArgs,
		"--entrypoint", "patronictl",
		"--network", "host",
		"--http-proxy=false",
		"-v", fmt.Sprintf("%s:/patroni/patroni.yml:ro,z", hostMountPath(ymlPath)),
		imageTag,
		"-c", "/patroni/patroni.yml",
	)
	runArgs = append(runArgs, args...)

	slog.Debug("podman patronictl", "cluster", cluster.Name, "args", args)
	cmd := podmanCommand(m.podman, runArgs...)
	cmd.Stdin = stdin
	if stdout != nil {
		cmd.Stdout = stdout
	}
	if stderr != nil {
		cmd.Stderr = stderr
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("patronictl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// Patronictl runs a patronictl subcommand from an ephemeral container (no
// long-lived control container exists in this design). The cluster is
// discovered via the DCS, so this works even when no member container is
// running locally. Stdio is attached (patronictl's tabular list and
// confirmation prompts are meant for the terminal).
func (m *PatroniManager) Patronictl(cluster *config.PatroniClusterConfig, args ...string) error {
	stdio := "-i"
	if isTerminal(os.Stdin) {
		stdio = "-it"
	}
	return m.runPatronictl(cluster, stdio, os.Stdin, os.Stdout, os.Stderr, args)
}

// PatronictlCapture runs a patronictl subcommand and returns its stdout,
// for callers that parse output. Nothing is attached to stdio.
func (m *PatroniManager) PatronictlCapture(cluster *config.PatroniClusterConfig, args ...string) (string, error) {
	var out, errBuf strings.Builder
	err := m.runPatronictl(cluster, "-i=false", nil, &out, &errBuf, args)
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return out.String(), nil
}

// RemoveScopeFromDCS runs `patronictl remove <nsScope>` to completion.
// patronictl has no --force for remove: it asks to re-type the cluster name,
// then a literal "Yes I am aware", and — when the cluster is still healthy —
// the leader's member name as a final confirmation. The answers are scripted
// on stdin; the leader is discovered first via a captured `list -f json`
// (proven: the health prompt needs the exact leader name, and the blank
// answer is harmless when the scope is already gone — a nonexistent-scope
// remove is a clean no-op, verified against patronictl 4.1.5).
func (m *PatroniManager) RemoveScopeFromDCS(cluster *config.PatroniClusterConfig) error {
	nsScope := m.cfg.PatroniScope(cluster.Name)

	leader := ""
	if out, err := m.PatronictlCapture(cluster, "list", nsScope, "-f", "json"); err == nil {
		leader = PatroniLeaderFromListJSON(out)
	}

	script := strings.Join([]string{nsScope, "Yes I am aware", leader}, "\n") + "\n"
	// patronictl's own progress/failure text (including a third-prompt abort)
	// stays visible on stderr.
	return m.runPatronictl(cluster, "-i", strings.NewReader(script), os.Stderr, os.Stderr, []string{"remove", nsScope})
}

// PatroniLeaderFromListJSON extracts the leader member name from
// `patronictl list -f json` output. Values are `any`, not string: the rows
// mix types ("TL" is a number), and a strict map[string]string unmarshal
// would fail the whole array and silently lose the leader.
func PatroniLeaderFromListJSON(out string) string {
	var members []map[string]any
	if json.Unmarshal([]byte(out), &members) != nil {
		return ""
	}
	for _, mm := range members {
		role, _ := mm["Role"].(string)
		name, _ := mm["Member"].(string)
		role = strings.ToLower(role)
		if name != "" && (strings.Contains(role, "leader") || role == "primary") {
			return name
		}
	}
	return ""
}

// IsClusterPaused checks whether the Patroni cluster is currently paused by
// reading the DCS config via patronictl show-config. When a cluster is paused,
// Patroni writes a top-level pause key into the DCS config, which appears in
// show-config output as a non-indented line ("pause: true" or "pause: false").
// Returns false if the cluster is not paused or if the DCS cannot be reached
// (callers should treat errors as "assume not paused" and let the explicit
// pause/resume commands surface the real problem).
// Idempotent: safe to call before every patronictl pause to avoid redundant
// pause attempts in cross-host install flows.
func (m *PatroniManager) IsClusterPaused(cluster *config.PatroniClusterConfig, nsScope string) bool {
	out, err := m.PatronictlCapture(cluster, "show-config", nsScope)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		// Top-level YAML keys start at column 0. Indented lines (nested
		// mappings) are skipped — those are sub-keys of other top-level
		// entries and cannot be the pause key itself.
		if len(line) == 0 || line[0] == ' ' || line[0] == '	' {
			continue
		}
		if !strings.HasPrefix(line, "pause") {
			continue
		}
		// Ensure the match is the key name "pause" exactly, not a prefix
		// of a longer key name (e.g. "pause_timeout"). After "pause" we
		// expect ':', ' ', or end-of-line.
		rest := line[len("pause"):]
		if len(rest) == 0 || rest[0] == ':' || rest[0] == ' ' {
			return true
		}
		// If the next character is a letter, this is a different key
		// (e.g. "paused_since") — not the pause flag.
		if len(rest) > 0 && unicode.IsLetter(rune(rest[0])) {
			continue
		}
		return true
	}
	return false
}

// CountClusterMembers returns the total number of members in the cluster as
// reported by the DCS via patronictl list -f json. This is used to distinguish
// single-host clusters (all members are local) from cross-host clusters (some
// members live on remote hosts and are not in the local pg.yaml).
// Returns -1 on error so callers can fall back to assuming single-host (the
// conservative choice: auto-apply rather than print instructions that may
// never be followed).
func (m *PatroniManager) CountClusterMembers(cluster *config.PatroniClusterConfig, nsScope string) int {
	out, err := m.PatronictlCapture(cluster, "list", nsScope, "-f", "json")
	if err != nil {
		return -1
	}
	var members []map[string]any
	if json.Unmarshal([]byte(out), &members) != nil {
		return -1
	}
	return len(members)
}

// --- shared low-level helpers (mirror pgdog/etcd manager internals) -----

func (m *PatroniManager) run(args ...string) (string, error) {
	cmd := podmanCommand(m.podman, args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("podman %s: %s", strings.Join(args, " "), string(exitErr.Stderr))
		}
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (m *PatroniManager) runInteractive(args ...string) error {
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (m *PatroniManager) imageExists(tag string) (bool, error) {
	out, err := m.run("image", "exists", tag)
	if err != nil {
		// `image exists` exits non-zero for a missing image; treat any
		// command that errors as "does not exist" unless it's clearly a
		// daemon failure.
		return false, nil
	}
	_ = out
	return true, nil
}

func (m *PatroniManager) containerExists(name string) (bool, error) {
	out, err := m.run("ps", "-a", "--filter", "name="+name, "--format", "{{.Names}}")
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}
	return false, nil
}

func (m *PatroniManager) containerRunning(name string) (bool, error) {
	out, err := m.run("ps", "--filter", "name="+name, "--filter", "status=running", "--format", "{{.Names}}")
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}
	return false, nil
}

// --- extension support -------------------------------------------------

// BuildExtensionImage builds a derived Patroni image that layers Pigsty DEB
// packages on top of the member's base image. Delegates to the shared
// extImageBuilder. pigstyRepo is required (the patroni base image does NOT
// have Pigsty repo pre-configured).
func (m *PatroniManager) BuildExtensionImage(fromTag string, pkgList []string, pigstyRepo string) (string, error) {
	b := &extImageBuilder{
		dataDir:        m.dataDir,
		runInteractive: m.runInteractive,
		imageExists:    m.imageExists,
		run:            m.run,
	}
	return b.build(fromTag, pkgList, pigstyRepo)
}

// DiscoverLeader queries the cluster via patronictl list -f json and returns
// the leader member's name, host, and port. If the leader is not found or
// the cluster is unhealthy, returns an error.
func (m *PatroniManager) DiscoverLeader(cluster *config.PatroniClusterConfig) (memberName, host string, port int, err error) {
	nsScope := m.cfg.PatroniScope(cluster.Name)
	out, err := m.PatronictlCapture(cluster, "list", nsScope, "-f", "json")
	if err != nil {
		return "", "", 0, fmt.Errorf("querying cluster state: %w", err)
	}

	var members []map[string]any
	if err := json.Unmarshal([]byte(out), &members); err != nil {
		return "", "", 0, fmt.Errorf("parsing patronictl output: %w", err)
	}

	for _, mm := range members {
		role, _ := mm["Role"].(string)
		name, _ := mm["Member"].(string)

		role = strings.ToLower(role)
		if name == "" || (!strings.Contains(role, "leader") && role != "primary") {
			continue
		}

		// patronictl list JSON puts host:port in the "Host" field (e.g.
		// "10.0.0.1:35532"); there is no separate "Port" key. Split it.
		hostField, _ := mm["Host"].(string)
		h, pStr, splitErr := net.SplitHostPort(hostField)
		if splitErr != nil {
			return name, hostField, 0, nil
		}
		p, parseErr := strconv.Atoi(pStr)
		if parseErr != nil {
			return name, h, 0, nil
		}
		return name, h, p, nil
	}
	return "", "", 0, fmt.Errorf("no leader found in cluster %q", cluster.Name)
}

// ExecLeaderQuery runs a SQL query against the cluster leader via a temporary
// container with host networking. Used for CREATE EXTENSION on the leader,
// which may be on a remote host. The DSN is built from the leader's advertised
// host:port and the cluster's superuser password.
func (m *PatroniManager) ExecLeaderQuery(cluster *config.PatroniClusterConfig, database, sql string) (string, error) {
	_, host, port, err := m.DiscoverLeader(cluster)
	if err != nil {
		return "", err
	}

	dsn := fmt.Sprintf("postgres://postgres:%s@%s:%d/%s",
		url.QueryEscape(cluster.Passwords.Superuser),
		host, port, database)

	// Use any local member's image tag for the throwaway container
	imageTag := ""
	for _, mb := range cluster.Members {
		if mb.ImageTag == "" {
			continue // remote members carry no local image tag
		}
		imageTag = mb.ImageTag
		break
	}
	if imageTag == "" {
		imageTag = DefaultPatroniImageTag
	}

	containerName := fmt.Sprintf("pgcli-ha-query-%d", time.Now().UnixNano())
	defer m.run("rm", "-f", containerName)

	podmanArgs := []string{
		"run", "--rm", "--name", containerName,
		"--network", "host",
		"--entrypoint", "psql",
		imageTag,
		"--dbname=" + dsn, "-t", "-A", "-c", sql,
	}
	cmd := exec.Command(m.podman, podmanArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("querying leader: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// RecreateMemberWithImage updates a member's image tag and recreates its
// container. This is DESTRUCTIVE: the member goes offline, and if it was the
// leader, failover will occur (unless cluster is paused). Callers must pause
// the cluster first if they want to avoid failover.
func (m *PatroniManager) RecreateMemberWithImage(cluster *config.PatroniClusterConfig, member, newImageTag string) error {
	mb, ok := cluster.Members[member]
	if !ok {
		return fmt.Errorf("member %q not found in cluster %q", member, cluster.Name)
	}

	// Update the member's image tag
	mb.ImageTag = newImageTag
	cluster.Members[member] = mb

	// Re-render patroni.yml (references the new image)
	if _, err := m.WriteMemberConfig(cluster, member); err != nil {
		return fmt.Errorf("writing patroni.yml: %w", err)
	}

	// Recreate the container (stop+rm+create)
	if err := m.EnsureMemberContainer(cluster, member); err != nil {
		return fmt.Errorf("recreating member container: %w", err)
	}

	return nil
}
