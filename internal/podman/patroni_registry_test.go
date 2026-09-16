package podman

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// The registry value gained backup_pubkey: old entries (no field) must decode,
// and a round trip must preserve the key line.
func TestMemberPortsPubKeyRoundTrip(t *testing.T) {
	in := memberPorts{SSHPort: 42301, RestapiPort: 8008, HostPort: 5432,
		BackupPubKey: "ssh-rsa AAAAB3Nza... pgcli-backup@host"}
	blob, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out memberPorts
	if err := json.Unmarshal(blob, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Errorf("round trip: got %+v, want %+v", out, in)
	}

	// A pre-feature entry (no backup_pubkey) must decode with an empty key.
	var legacy memberPorts
	if err := json.Unmarshal([]byte(`{"ssh_port":42301,"restapi_port":8008}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.SSHPort != 42301 || legacy.BackupPubKey != "" {
		t.Errorf("legacy entry decoded wrong: %+v", legacy)
	}

	// omitempty: no pubkey, no field in the JSON.
	blob, _ = json.Marshal(memberPorts{SSHPort: 1})
	if strings.Contains(string(blob), "backup_pubkey") {
		t.Errorf("empty pubkey must be omitted: %s", blob)
	}
}

// ".repo/ca" lives under the per-scope member prefix (so --scope-all removal
// clears it) yet can never read back as a member: its name contains "." and
// "/", both illegal in a member name.
func TestRepoCAKeyPathNoMemberCollision(t *testing.T) {
	const nsScope = "app-prod"
	got := repoCAKeyPath(nsScope)
	if want := "/pgcli/ha/app-prod/.repo/ca"; got != want {
		t.Fatalf("repo CA key = %q, want %q", got, want)
	}
	member := strings.TrimPrefix(got, registryKeyPath(nsScope))
	if member == "" || !strings.ContainsAny(member, "./") {
		t.Errorf("repo CA key would parse as member %q; memberPortsFromDCS must skip it", member)
	}
	// A real member key must NOT be skipped by that rule.
	if m := strings.TrimPrefix(registryKeyPath(nsScope)+"node1", registryKeyPath(nsScope)); m == "" || strings.ContainsAny(m, "./") {
		t.Errorf("member key node1 wrongly looks like a scope-level key: %q", m)
	}
}

func TestWriteClusterAuthKeys(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix paths")
	}
	m := &BackupManager{dataDir: t.TempDir()}
	const nsScope = "app"

	keys := []string{
		"ssh-rsa AAAAB3KEYC host3",
		"  ssh-rsa AAAAB3KEYA host1  ", // surrounding spaces trimmed
		"ssh-ed25519 AAAAC3KEYB host2",
		"ssh-rsa AAAAB3KEYC host3", // duplicate
		"",                         // empty
	}
	path, err := m.WriteClusterAuthKeys(nsScope, keys)
	if err != nil {
		t.Fatal(err)
	}
	if path != m.ClusterAuthKeysPath(nsScope) {
		t.Errorf("returned path %q != ClusterAuthKeysPath %q", path, m.ClusterAuthKeysPath(nsScope))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "ssh-ed25519 AAAAC3KEYB host2\nssh-rsa AAAAB3KEYA host1\nssh-rsa AAAAB3KEYC host3\n"
	if string(data) != want {
		t.Errorf("merged file =\n%q\nwant\n%q", data, want)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0644 {
		t.Errorf("file mode = %v, want 0644 (member containers run as postgres uid and must read this bind mount)", fi.Mode().Perm())
	}

	// Overwrite replaces content through the SAME inode: members bind-mount
	// this file, and a rename-replace would strand them on the old inode.
	before, _ := os.Stat(path)
	if _, err := m.WriteClusterAuthKeys(nsScope, []string{"ssh-rsa AAAAB3KEYA host1"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Error("rewrite changed the inode; live bind mounts would miss the update")
	}
	data, _ = os.ReadFile(path)
	if string(data) != "ssh-rsa AAAAB3KEYA host1\n" {
		t.Errorf("after rewrite = %q", data)
	}

	// All-empty input still yields a valid (empty) file for the bind mount.
	if _, err := m.WriteClusterAuthKeys("other", []string{"", " "}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(m.ClusterAuthKeysPath("other"))
	if len(data) != 1 || data[0] != '\n' {
		t.Errorf("empty merge wrote %q, want a single newline", data)
	}
}

// --- MemberArchiveReady cluster-keys mount check -------------------------

// fakePodman returns a shell script standing in for podman: `inspect` prints
// the mount lines given (source\tdestination, the format MemberArchiveReady
// asks for) and always succeeds.
func fakePodman(t *testing.T, mountLines ...string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "podman")
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *inspect*) printf '%s' ;;
esac
exit 0
`, strings.Join(mountLines, `\n`)+`\n`)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func archiveReadyFixture(t *testing.T, s3 *config.BackupRepoS3) (*PatroniManager, *config.PatroniClusterConfig, string) {
	t.Helper()
	dataDir := t.TempDir()
	cfg := config.Default()
	cfg.Backup.Repo.S3 = s3
	archivePath := filepath.Join(dataDir, "pgbackrest-archive.conf")
	if err := os.WriteFile(archivePath, []byte("[global]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cluster := testPatroniCluster()
	cluster.Members["node1"] = config.PatroniMemberConfig{
		HostPort: 35532, SSHPort: 42301, RestapiPort: 8008,
		ContainerName: "pgcli-patroni-app-node1",
	}
	m := &PatroniManager{cfg: cfg, dataDir: dataDir, podman: "podman"}
	return m, cluster, archivePath
}

func TestMemberArchiveReadyStaleWithoutClusterKeysMount(t *testing.T) {
	m, cluster, archivePath := archiveReadyFixture(t, testS3Repo())
	m.podman = fakePodman(t,
		hostMountPath(archivePath)+"\t/etc/pgbackrest.conf",
		"/host/id_rsa.pub\t/run/pgcli/backup_id_rsa.pub",
	)
	ready, reason, err := m.MemberArchiveReady(cluster, "node1")
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Fatal("member missing the authorized_keys_cluster mount must be stale")
	}
	if !strings.Contains(reason, "authorized_keys") {
		t.Errorf("reason = %q, want it to name the cluster authorized_keys mount", reason)
	}
}

func TestMemberArchiveReadyStaleArchiveMountStillFirst(t *testing.T) {
	m, cluster, _ := archiveReadyFixture(t, testS3Repo())
	m.podman = fakePodman(t, "/host/id_rsa.pub\t/run/pgcli/backup_id_rsa.pub") // neither mount
	ready, reason, err := m.MemberArchiveReady(cluster, "node1")
	if err != nil {
		t.Fatal(err)
	}
	if ready || !strings.Contains(reason, "pgbackrest") {
		t.Errorf("got (%v, %q), want stale on the pgbackrest.conf mount", ready, reason)
	}
}

func TestMemberArchiveReadyCurrentWithClusterKeysMount(t *testing.T) {
	m, cluster, archivePath := archiveReadyFixture(t, testS3Repo())
	m.podman = fakePodman(t,
		hostMountPath(archivePath)+"\t/etc/pgbackrest.conf",
		"/host/authorized_keys_app\t/run/pgcli/authorized_keys_cluster",
	)
	// With S3 configured the patroni.yml archive parameters are also checked;
	// render them so this case exercises "everything present => ready".
	nsScope := m.cfg.PatroniScope(cluster.Name)
	ymlDir := m.memberConfigDir(nsScope, "node1")
	if err := os.MkdirAll(ymlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stanza := patroniStanzaName(nsScope)
	yml := fmt.Sprintf("postgresql:\n  parameters:\n    archive_mode: on\n    archive_command: 'pgbackrest --stanza=%s archive-push \"%%p\"'\n", stanza)
	if err := os.WriteFile(filepath.Join(ymlDir, "patroni.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	ready, reason, err := m.MemberArchiveReady(cluster, "node1")
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Fatalf("fully current member reported stale: %q", reason)
	}
}

// Without an archive conf ever generated, archiving is off and the new mount
// requirement must not flag existing members.
func TestMemberArchiveReadyNoArchiveConfSkipsMountChecks(t *testing.T) {
	m, cluster, _ := archiveReadyFixture(t, nil)
	if err := os.Remove(filepath.Join(m.dataDir, "pgbackrest-archive.conf")); err != nil {
		t.Fatal(err)
	}
	m.podman = fakePodman(t) // no mounts at all
	ready, _, err := m.MemberArchiveReady(cluster, "node1")
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Error("members must be ready when no archive conf exists")
	}
}
