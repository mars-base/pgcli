package podman

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mars-base/pgcli/internal/platform"
)

// replicaSlotPrefix is the name prefix for the physical replication slot
// created on the primary for each replica.
const replicaSlotPrefix = "pgcli_r_"

// ReplicaSlotName returns the replication slot name for a replica. The same
// name is derived on both the primary and replica hosts, so both sides of a
// cross-network replica agree on the slot.
func ReplicaSlotName(name string) string {
	return replicaSlotPrefix + name
}

// isReplica reports whether the manager's current instance is a physical
// replica (streams WAL from a primary).
func (m *Manager) isReplica() bool {
	return m.replicaOf() != ""
}

// replicaOf returns the primary instance name, or "" if not a replica.
func (m *Manager) replicaOf() string {
	return m.cfg.ReplicaOf(m.cfg.Instance)
}

// primaryManager returns a Manager bound to the replica's primary instance.
func (m *Manager) primaryManager() (*Manager, error) {
	primary := m.replicaOf()
	if primary == "" {
		return nil, fmt.Errorf("instance %q is not a replica", m.cfg.Instance)
	}
	pc := *m.cfg
	if err := pc.SetInstance(primary); err != nil {
		return nil, fmt.Errorf("loading primary instance %q: %w", primary, err)
	}
	return New(&pc)
}

// EnsureReplica prepares a replica instance before its container is created.
// On first start the data directory is initialized with pg_basebackup so the
// container boots in standby mode instead of running initdb. On later starts
// the data already exists and this is a no-op.
func (m *Manager) EnsureReplica() error {
	if !m.isReplica() {
		return nil
	}

	// Data already initialized by a previous pg_basebackup run.
	if m.dataDirInitialized(m.PGHostDataDir()) {
		return nil
	}

	// Cross-network replica: the primary lives on another host and was
	// prepared with `pg replica create ... --replica-host`. Check the slot,
	// then pg_basebackup straight from the DSN.
	if m.cfg.Instances[m.cfg.Instance].PrimaryDSN != "" {
		return m.ensureRemoteReplica()
	}

	primaryPM, err := m.primaryManager()
	if err != nil {
		return err
	}
	if err := primaryPM.CheckContainerRunning(); err != nil {
		return fmt.Errorf("primary instance %q must be running to create a replica: %w", m.replicaOf(), err)
	}

	// Allow replication connections on the primary (idempotent).
	if err := primaryPM.EnsureReplicationHBA(); err != nil {
		return fmt.Errorf("configuring replication on primary: %w", err)
	}

	// Reserve WAL on the primary with a physical slot so the replica can
	// never be left behind by WAL recycling (idempotent).
	slot := ReplicaSlotName(m.cfg.Instance)
	if err := primaryPM.EnsureReplicationSlot(slot); err != nil {
		return fmt.Errorf("creating replication slot on primary: %w", err)
	}

	// Reach the primary: Linux host networking shares loopback, macOS bridge
	// networking resolves container names.
	host, port := "127.0.0.1", primaryPM.cfg.Podman.HostPort
	if platform.Detect() == platform.MacOS {
		host = primaryPM.cfg.Podman.ContainerName
	}

	fmt.Println("-> Initializing replica data directory via pg_basebackup...")
	if err := m.runBasebackup(host, port, primaryPM.cfg.Postgres.User, primaryPM.cfg.Postgres.Password, false); err != nil {
		// Remove the half-initialized data dir so a retry re-runs
		// pg_basebackup instead of booting a broken standby.
		if rmErr := removeHostDir(m.podman, m.cfg.Podman.ImageTag, m.PGHostDataDir()); rmErr != nil {
			fmt.Printf("  [!] warning: removing half-initialized replica data dir %s: %v\n", m.PGHostDataDir(), rmErr)
		}
		return err
	}
	return nil
}

// CheckPrimarySlotReady verifies the primary side of a cross-network replica
// has been prepared: the replication slot for <replicaName> exists and is
// reachable via <dsn>. A single query validates both connectivity and
// ordering, so running the replica side before the primary side fails with an
// actionable message instead of a pg_basebackup connection error.
func (m *Manager) CheckPrimarySlotReady(dsn, replicaName, primaryName string) error {
	slot := ReplicaSlotName(replicaName)
	esc := strings.ReplaceAll(slot, "'", "''")
	out, err := m.ExecDSNQuery(dsn, fmt.Sprintf("SELECT slot_name FROM pg_replication_slots WHERE slot_name = '%s'", esc))
	if err != nil {
		return fmt.Errorf("cannot verify replication slot %q on the primary: %w", slot, err)
	}
	if strings.TrimSpace(out) != slot {
		return fmt.Errorf("replication slot %q not found on the primary. Run on the primary host first:\n  pg replica create %s -i %s --replica-host <replica host ip>", slot, replicaName, primaryName)
	}
	return nil
}

// ensureRemoteReplica initializes a cross-network replica from a remote
// primary. The primary side must already have been prepared (pg_hba entry
// and replication slot) with `pg replica create ... --replica-host`.
func (m *Manager) ensureRemoteReplica() error {
	dsn := m.cfg.Instances[m.cfg.Instance].PrimaryDSN
	host, port, user, password, _, err := ParseDSN(dsn)
	if err != nil {
		return err
	}

	// Verify the primary side was prepared (also covers later starts after a
	// re-prepared primary, e.g. after the slot was dropped on the other side).
	if err := m.CheckPrimarySlotReady(dsn, m.cfg.Instance, m.replicaOf()); err != nil {
		return err
	}

	fmt.Println("-> Initializing replica data directory via pg_basebackup...")
	if err := m.runBasebackup(host, port, user, password, true); err != nil {
		// Remove the half-initialized data dir so a retry re-runs
		// pg_basebackup instead of booting a broken standby.
		if rmErr := removeHostDir(m.podman, m.cfg.Podman.ImageTag, m.PGHostDataDir()); rmErr != nil {
			fmt.Printf("  [!] warning: removing half-initialized replica data dir %s: %v\n", m.PGHostDataDir(), rmErr)
		}
		return err
	}
	return nil
}

// dataDirInitialized reports whether hostDir already holds a PostgreSQL data
// directory. Rootless podman owns container-written files as a subordinate
// UID the host user cannot read (os.Stat fails with EACCES), so such errors
// fall back to checking from inside a container where root can see the files.
func (m *Manager) dataDirInitialized(hostDir string) bool {
	if _, err := os.Stat(filepath.Join(hostDir, "PG_VERSION")); err == nil {
		return true
	} else if os.IsNotExist(err) {
		return false
	}
	parent, base := filepath.Dir(hostDir), filepath.Base(hostDir)
	cmd := podmanCommand(m.podman, "run", "--rm",
		"-v", fmt.Sprintf("%s:/target:z", hostMountPath(parent)),
		m.cfg.Podman.ImageTag, "sh", "-c",
		fmt.Sprintf("test -f /target/%s/PG_VERSION", base))
	return cmd.Run() == nil
}

// runBasebackup copies the primary's data directory into the replica's data
// dir with -R, which writes primary_conninfo and standby.signal so the replica
// starts in read-only standby mode. Runs in a throwaway sleep container
// because the official image entrypoint would run initdb on an empty PGDATA.
// hostNetwork forces host networking (used for cross-network replicas, where
// the primary is not reachable by container name).
func (m *Manager) runBasebackup(host string, port int, user, password string, hostNetwork bool) error {
	slot := ReplicaSlotName(m.cfg.Instance)

	tmpName := "pgcli-bb-" + m.cfg.Instance
	if m.cfg.Namespace != "" {
		tmpName = "pgcli-bb-" + m.cfg.Namespace + "-" + m.cfg.Instance
	}
	defer func() {
		_, _ = m.run("rm", "-f", tmpName)
	}()

	networkMode := "host"
	if platform.Detect() == platform.MacOS && !hostNetwork {
		networkMode = m.cfg.Podman.Network
	}
	args := []string{
		"run", "-d", "--name", tmpName,
		"--network", networkMode,
		"-v", fmt.Sprintf("%s:/var/lib/postgresql:z", hostMountPath(m.cfg.Podman.DataDir)),
		"-e", "PGDATA=/var/lib/postgresql/data",
		"--entrypoint", "sleep",
		m.cfg.Podman.ImageTag,
		"infinity",
	}
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating basebackup container: %w", err)
	}

	if _, err := m.run(
		"exec", "-e", "PGPASSWORD="+password,
		tmpName,
		"pg_basebackup",
		"-h", host, "-p", strconv.Itoa(port),
		"-U", user,
		"-D", "/var/lib/postgresql/data",
		"-S", slot, // reuse the pre-created slot
		"-R", // write primary_conninfo + standby.signal
		"-P",
	); err != nil {
		return fmt.Errorf("pg_basebackup from primary: %w", err)
	}
	fmt.Println("  [OK] base backup complete")

	// -R writes primary_conninfo without a password; also drop the archive
	// settings copied from the primary (a standby archives nothing, and
	// after a future promote the primary's stanza path would be stale).
	return m.fixupReplicaConfig(tmpName, password)
}

// fixupReplicaConfig adjusts postgresql.auto.conf in the freshly-restored
// data directory: adds the primary password to primary_conninfo and removes
// archive_mode/archive_command inherited from the primary.
func (m *Manager) fixupReplicaConfig(tmpContainer, primaryPassword string) error {
	const autoConf = "/var/lib/postgresql/data/postgresql.auto.conf"
	out, err := m.execOn(tmpContainer, "cat", autoConf)
	if err != nil {
		return fmt.Errorf("reading replica postgresql.auto.conf: %w", err)
	}

	var kept []string
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "archive_mode") || strings.HasPrefix(t, "archive_command") {
			continue
		}
		kept = append(kept, line)
	}
	content := addPasswordToPrimaryConnInfo(strings.Join(kept, "\n"), primaryPassword)

	tmp, err := os.CreateTemp("", "pgcli-replica-auto-*.conf")
	if err != nil {
		return fmt.Errorf("creating temp postgresql.auto.conf: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp postgresql.auto.conf: %w", err)
	}
	tmp.Close()

	if _, err := m.run("cp", tmpPath, tmpContainer+":"+autoConf); err != nil {
		return fmt.Errorf("writing replica postgresql.auto.conf: %w", err)
	}
	if err := m.chownDataFile(tmpContainer, autoConf); err != nil {
		return fmt.Errorf("chown replica postgresql.auto.conf: %w", err)
	}
	return nil
}

// addPasswordToPrimaryConnInfo rewrites the primary_conninfo line (written by
// pg_basebackup -R without a password) to include the primary password.
func addPasswordToPrimaryConnInfo(content, password string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "primary_conninfo") {
			continue
		}
		idx := strings.Index(line, "'")
		if idx < 0 {
			continue
		}
		rest := line[idx+1:]
		end := strings.Index(rest, "'")
		if end < 0 {
			continue
		}
		conn := rest[:end]
		parts := strings.Fields(conn)
		for _, p := range parts {
			if strings.HasPrefix(p, "password=") {
				return content
			}
		}
		parts = append(parts, "password="+password)
		lines[i] = line[:idx+1] + strings.Join(parts, " ") + line[idx+1+len(conn):]
		break
	}
	return strings.Join(lines, "\n")
}

// EnsureReplicationHBA allows replication connections to this instance by
// appending the managed host replication entries to pg_hba.conf, then reloads
// the configuration. Each managed line is added only if absent, so repeated
// runs (and upgrades that add new lines) stay idempotent.
//
// extraHosts are additional source addresses (IPs or hostnames) to allow for
// cross-network replicas. IPs get a /32 mask (/128 for IPv6), hostnames are
// written as-is.
func (m *Manager) EnsureReplicationHBA(extraHosts ...string) error {
	const hbaPath = "/var/lib/postgresql/data/pg_hba.conf"
	out, err := m.Exec("cat", hbaPath)
	if err != nil {
		return fmt.Errorf("reading pg_hba.conf: %w", err)
	}

	// Replica containers reach the primary via the host loopback, but under
	// rootless podman the connection source is NATed to a host interface
	// address (observed: the default-route interface IP) rather than
	// 127.0.0.1 — so allow replication from any RFC1918 range, not just
	// loopback. Replication requires the admin password (scram), and
	// replicas only exist on the same host as their primary.
	managed := []string{
		"host replication all 127.0.0.1/32 scram-sha-256",
		"host replication all ::1/128 scram-sha-256",
		"host replication all 10.0.0.0/8 scram-sha-256",
		"host replication all 172.16.0.0/12 scram-sha-256",
		"host replication all 192.168.0.0/16 scram-sha-256",
	}
	for _, h := range extraHosts {
		// An IP already covered by a managed CIDR (e.g. 10.241.20.x inside
		// 10.0.0.0/8) needs no extra line; hostnames and public IPs do.
		if ip := net.ParseIP(h); ip != nil && ipCoveredByCIDR(ip, managed) {
			continue
		}
		managed = append(managed, replicationHBALine(h))
	}
	var missing []string
	for _, want := range managed {
		if !pgHbaHasLine(out, want) {
			missing = append(missing, want)
		}
	}
	if len(missing) == 0 {
		// Lines already present, but podman cp may have left the file owned
		// by root — restore ownership so the postmaster can reload it.
		_ = m.chownDataFile(m.cfg.Podman.ContainerName, hbaPath)
		return nil
	}

	content := out
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += "# === pgcli replication (managed - do not edit) ===\n"
	content += strings.Join(missing, "\n") + "\n"

	tmp, err := os.CreateTemp("", "pgcli-hba-*.conf")
	if err != nil {
		return fmt.Errorf("creating temp pg_hba.conf: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp pg_hba.conf: %w", err)
	}
	tmp.Close()

	if _, err := m.run("cp", tmpPath, m.cfg.Podman.ContainerName+":"+hbaPath); err != nil {
		return fmt.Errorf("podman cp pg_hba.conf: %w", err)
	}
	if err := m.chownDataFile(m.cfg.Podman.ContainerName, hbaPath); err != nil {
		return fmt.Errorf("chown pg_hba.conf: %w", err)
	}
	fmt.Println("  [OK] replication access configured on primary")

	if _, err := m.Exec("psql", "-U", m.cfg.Postgres.User, "-d", m.cfg.Postgres.Database,
		"-c", "SELECT pg_reload_conf()"); err != nil {
		return fmt.Errorf("reloading pg_hba.conf: %w", err)
	}
	return nil
}

// pgHbaHasLine reports whether pg_hba.conf content contains the exact
// (non-comment) line <want>. Default pg_hba.conf contains "# replication
// privilege." comment lines, so only active lines count.
func pgHbaHasLine(content, want string) bool {
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if t == want {
			return true
		}
	}
	return false
}

// replicationHBALine builds a managed pg_hba replication line for a source
// address: IPs get a /32 (/128 for IPv6) mask, hostnames are written as-is.
func replicationHBALine(host string) string {
	ip := net.ParseIP(host)
	if ip == nil {
		return "host replication all " + host + " scram-sha-256"
	}
	mask := "/32"
	if ip.To4() == nil {
		mask = "/128"
	}
	return "host replication all " + host + mask + " scram-sha-256"
}

// ipCoveredByCIDR reports whether ip falls inside any CIDR-scoped managed hba
// line (e.g. 10.241.20.147 is inside 10.0.0.0/8). Hostname lines are skipped.
func ipCoveredByCIDR(ip net.IP, hbaLines []string) bool {
	for _, line := range hbaLines {
		fields := strings.Fields(line)
		if len(fields) != 5 {
			continue
		}
		_, cidr, err := net.ParseCIDR(fields[3])
		if err != nil {
			continue
		}
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// chownDataFile restores postgres ownership on a file inside a container's
// data directory after podman cp, which writes the file as container root.
// Without this the postmaster cannot read it (e.g. pg_hba.conf on reload),
// and the change silently fails to take effect.
func (m *Manager) chownDataFile(container, path string) error {
	_, err := m.run("exec", "-u", "0", container, "chown", "postgres:postgres", path)
	return err
}

// EnsureReplicationSlot creates the physical replication slot for a replica
// on this (primary) instance if it does not exist yet.
func (m *Manager) EnsureReplicationSlot(slot string) error {
	esc := strings.ReplaceAll(slot, "'", "''")
	exists, err := m.Exec("psql", "-U", m.cfg.Postgres.User, "-d", m.cfg.Postgres.Database,
		"-t", "-A", "-c", fmt.Sprintf("SELECT slot_name FROM pg_replication_slots WHERE slot_name = '%s'", esc))
	if err != nil {
		return err
	}
	if strings.TrimSpace(exists) != "" {
		return nil
	}
	if _, err := m.Exec("psql", "-U", m.cfg.Postgres.User, "-d", m.cfg.Postgres.Database,
		"-c", fmt.Sprintf("SELECT pg_create_physical_replication_slot('%s')", esc)); err != nil {
		return err
	}
	fmt.Printf("  [OK] replication slot %q created on primary\n", slot)
	return nil
}

// DropReplicationSlot removes a replica's physical replication slot from this
// (primary) instance. It is a no-op when the slot does not exist.
func (m *Manager) DropReplicationSlot(slot string) error {
	esc := strings.ReplaceAll(slot, "'", "''")
	exists, err := m.Exec("psql", "-U", m.cfg.Postgres.User, "-d", m.cfg.Postgres.Database,
		"-t", "-A", "-c", fmt.Sprintf("SELECT slot_name FROM pg_replication_slots WHERE slot_name = '%s'", esc))
	if err != nil {
		return err
	}
	if strings.TrimSpace(exists) == "" {
		return nil
	}
	_, err = m.Exec("psql", "-U", m.cfg.Postgres.User, "-d", m.cfg.Postgres.Database,
		"-c", fmt.Sprintf("SELECT pg_drop_replication_slot('%s')", esc))
	return err
}

// DropReplicaSlot removes the replication slot for replica <replicaName>
// from this (primary) instance. No-op when the slot does not exist.
func (m *Manager) DropReplicaSlot(replicaName string) error {
	return m.DropReplicationSlot(ReplicaSlotName(replicaName))
}

// execOn runs a command inside the named container (not necessarily the
// instance's own container) and returns stdout.
func (m *Manager) execOn(name string, args ...string) (string, error) {
	return m.run(append([]string{"exec", name}, args...)...)
}
