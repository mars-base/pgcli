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

	out, err := m.run("inspect", "--format", "{{range .Mounts}}{{.Source}}\n{{end}}", mb.ContainerName)
	if err != nil {
		return false, "", fmt.Errorf("inspecting member container %s: %w", mb.ContainerName, err)
	}
	mounted := false
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == hostMountPath(archivePath) {
			mounted = true
			break
		}
	}
	if !mounted {
		return false, "mounts the backup-side pgbackrest.conf (pg1-host breaks archive-push)", nil
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
