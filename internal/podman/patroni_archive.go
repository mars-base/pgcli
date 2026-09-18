package podman

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	yaml "gopkg.in/yaml.v3"

	"github.com/mars-base/pgcli/internal/config"
)

// PatroniStanzaTarget names a Patroni cluster's pgBackRest stanza.
type PatroniStanzaTarget struct {
	Scope  string
	Stanza string
}

// PatroniStanzaNames lists every cluster stanza in the generated
// backup/archive configs (same source as WritePgbackrestConf).
func (m *BackupManager) PatroniStanzaNames() []PatroniStanzaTarget {
	ts := m.patroniBackupTargets()
	seen := map[string]bool{}
	var out []PatroniStanzaTarget
	for _, t := range ts {
		if seen[t.scope] {
			continue
		}
		seen[t.scope] = true
		out = append(out, PatroniStanzaTarget{Scope: t.scope, Stanza: patroniStanzaName(m.cfg.PatroniScope(t.scope))})
	}
	return out
}

// BackupProvisioningWarning checks the host-side backup provisioning a new
// member container depends on, and returns a warning when it is missing
// ("" when the host is ready). The dependency is creation-time-frozen:
// createMemberContainer mounts the member-local pgbackrest-archive.conf only
// when the file exists, and renderPatroniYML injects the archive parameters
// only when an S3 repo is configured. A member created before `pg backup
// setup` therefore ships with no WAL archiving — until a later setup flags it
// stale (MemberArchiveReady) and recreates it inside a pause window. Not a
// hard error: an HA cluster with no backup repo at all is a supported mode.
func (m *PatroniManager) BackupProvisioningWarning() string {
	if _, err := os.Stat(filepath.Join(m.dataDir, "pgbackrest-archive.conf")); err == nil {
		return ""
	}
	if m.cfg.Backup.Repo.S3 == nil {
		return "!  No S3 backup repo configured on this host (backup.repo.s3): the member will be created WITHOUT WAL archiving, so pg ha snapshot/restore cannot cover it. Configure a repo and run `pg backup setup` to wire archiving in (it recreates members as needed).\n"
	}
	return "!  An S3 backup repo is configured but `pg backup setup` has not run on this host (no pgbackrest-archive.conf): the member would be created WITHOUT WAL archiving and flagged stale by the next setup, which recreates it inside a pause window. Run `pg backup setup` first for a backup-ready create.\n"
}

// MemberArchiveReady reports whether a local Patroni member container is ready
// to push WAL archives under the current backup configuration. It must mount
// the member-local pgbackrest-archive.conf — the shared pgbackrest.conf names
// pg1-host, which makes archive-push abort with [072] "must be run on the
// PostgreSQL host" — and (once archiving is configured) its rendered
// patroni.yml must carry the archive parameters. Older containers that predate
// either are "stale" and need a recreate. When no archive view was ever
// generated, every container is considered ready (archiving is simply off).
func (m *PatroniManager) MemberArchiveReady(cluster *config.PatroniClusterConfig, member string) (ready bool, reason string, err error) {
	mb, ok := cluster.Members[member]
	if !ok || mb.RemoteHost != "" || mb.ContainerName == "" {
		return true, "", nil // nothing local to check
	}
	archivePath := filepath.Join(m.dataDir, "pgbackrest-archive.conf")
	if _, statErr := os.Stat(archivePath); os.IsNotExist(statErr) {
		return true, "", nil
	}

	out, err := m.run("inspect", "--format", "{{range .Mounts}}{{.Source}}\t{{.Destination}}\n{{end}}", mb.ContainerName)
	if err != nil {
		return false, "", fmt.Errorf("inspecting member container %s: %w", mb.ContainerName, err)
	}
	mountedArchive := false
	mountedClusterKeys := false
	for line := range strings.SplitSeq(out, "\n") {
		src, dst, _ := strings.Cut(line, "\t")
		switch strings.TrimSpace(dst) {
		case "/etc/pgbackrest.conf":
			if strings.TrimSpace(src) == hostMountPath(archivePath) {
				mountedArchive = true
			}
		case "/run/pgcli/authorized_keys_cluster":
			mountedClusterKeys = true
		}
	}
	if !mountedArchive {
		return false, "mounts the backup-side pgbackrest.conf (pg1-host breaks archive-push)", nil
	}
	if !mountedClusterKeys {
		// Predates cross-host backup trust: without the merged cluster
		// authorized_keys mount this member's sshd only trusts its own host's
		// backup key, so a peer host's full-backup SSH probe is refused.
		return false, "predates the cluster authorized_keys mount (cross-host backup)", nil
	}

	if m.cfg.Backup.Repo.S3 != nil {
		yml := filepath.Join(m.memberConfigDir(m.cfg.PatroniScope(cluster.Name), member), "patroni.yml")
		data, err := os.ReadFile(yml)
		if err != nil {
			return false, "patroni.yml predates the archive parameters", nil
		}
		// Parse rather than substring-match: yaml.Marshal folds long lines, and
		// the exact stanza must match so a rename (per-member → cluster-wide)
		// re-triggers the recreate.
		var doc struct {
			PostgreSQL struct {
				Parameters struct {
					ArchiveMode    string `yaml:"archive_mode"`
					ArchiveCommand string `yaml:"archive_command"`
				} `yaml:"parameters"`
			} `yaml:"postgresql"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return false, fmt.Sprintf("cannot parse patroni.yml: %v", err), nil
		}
		if doc.PostgreSQL.Parameters.ArchiveMode != "on" {
			return false, "patroni.yml predates the archive parameters", nil
		}
		want := fmt.Sprintf(`pgbackrest --stanza=%s archive-push "%%p"`, patroniStanzaName(m.cfg.PatroniScope(cluster.Name)))
		if doc.PostgreSQL.Parameters.ArchiveCommand != want {
			return false, fmt.Sprintf("patroni.yml archive_command is stale (want %s)", want), nil
		}
	}
	return true, "", nil
}
