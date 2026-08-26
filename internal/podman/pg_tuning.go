package podman

import (
	"fmt"
	"os"
	"strings"
)

// pgTuningParams holds the performance parameters written to postgresql.conf.
// Values are intentionally conservative and work on machines with ≥4 GB RAM.
//
// The block is idempotent: repeated pgcli start calls replace the block
// in-place rather than appending duplicate sections.
var pgTuningParams = []string{
	// Disable synchronous WAL commit.  Transactions return as soon as WAL is
	// written to the kernel buffer — no fsync wait.  In the worst case (hard
	// OS crash) up to ~200 ms of committed data may be lost, which is
	// acceptable for a local database workload.
	"synchronous_commit = off",

	// Shared buffer pool pre-allocated at startup.  512 MB is a reasonable
	// default for a machine with ≥8 GB RAM.  Requires restart to change.
	"shared_buffers = 512MB",

	// WAL buffer in shared memory.  Lets multiple small transactions coalesce
	// WAL writes, reducing flush frequency.  Requires restart to change.
	"wal_buffers = 64MB",

	// Spread checkpoint dirty-page flushes evenly over 70 % of the interval.
	// 0.7 completes faster than the default 0.9, reducing peak dirty-page
	// accumulation and shortening post-bench I/O tails.
	"checkpoint_completion_target = 0.7",

	// Allow WAL to grow to 2 GB before forcing a checkpoint.
	"max_wal_size = 2GB",

	// Check every 5 min instead of 15 min.  Smaller checkpoint intervals mean
	// less dirty data per cycle and shorter recovery time; the tradeoff is
	// slightly more frequent background I/O.
	"checkpoint_timeout = 5min",

	// Disable autovacuum I/O throttling entirely so autovacuum runs at full
	// I/O speed, clearing dead rows quickly before they bloat tables.
	"autovacuum_vacuum_cost_delay = 0",
	"autovacuum_vacuum_cost_limit = 800",
}

// pgRestartParams is the set of parameter names that require a PostgreSQL
// restart (not just reload) to take effect.
var pgRestartParams = map[string]bool{
	"shared_buffers": true,
	"wal_buffers":    true,
}

const (
	pgTuningBegin = "# === pgcli performance tuning (managed — do not edit) ==="
	pgTuningEnd   = "# === end pgcli performance tuning ==="
	pgConfPath    = "/var/lib/postgresql/data/postgresql.conf"
)

// ApplyPGTuning writes (or replaces) the pgcli performance-tuning block inside
// the running PostgreSQL container, then reloads (or restarts) as needed.
//
// It is called by doStart after the container is running and PostgreSQL is
// ready, so it can use podman exec / podman cp to access the file with the
// correct in-container user permissions.
//
// The write is done via podman cp (host temp file → container path) rather
// than a shell heredoc, so it is not subject to OS command-line length limits.
//
// Behaviour:
//   - If the block is absent or differs, it is written.
//   - If any restart-required parameter (shared_buffers, wal_buffers) changed,
//     the return value needsRestart is true; the caller is responsible for
//     restarting the container.
func (m *Manager) ApplyPGTuning() (needsRestart bool, err error) {
	// Read current postgresql.conf from inside the container.
	current, err := m.Exec("cat", pgConfPath)
	if err != nil {
		return false, fmt.Errorf("pg_tuning: read postgresql.conf: %w", err)
	}

	// Build new block.
	lines := []string{pgTuningBegin}
	lines = append(lines, pgTuningParams...)
	lines = append(lines, pgTuningEnd)
	newBlock := "\n" + strings.Join(lines, "\n") + "\n"

	// Check whether the block already exists and is identical — skip if so.
	if strings.Contains(current, pgTuningBegin) {
		start := strings.Index(current, pgTuningBegin)
		end := strings.Index(current[start:], pgTuningEnd)
		if end >= 0 {
			existing := current[start : start+end+len(pgTuningEnd)]
			if existing == strings.TrimPrefix(strings.TrimSuffix(newBlock, "\n"), "\n") {
				return false, nil
			}
		}
	}

	// Detect whether any restart-required param is being newly set or changed.
	for _, param := range pgTuningParams {
		key := strings.SplitN(param, " ", 2)[0]
		if !pgRestartParams[key] {
			continue
		}
		if !strings.Contains(current, param) {
			needsRestart = true
			break
		}
	}

	// Build the merged content: replace existing block or append.
	content := current
	if idx := strings.Index(content, pgTuningBegin); idx >= 0 {
		endIdx := strings.Index(content[idx:], pgTuningEnd)
		if endIdx >= 0 {
			tail := idx + endIdx + len(pgTuningEnd)
			if tail < len(content) && content[tail] == '\n' {
				tail++
			}
			content = content[:idx] + newBlock + content[tail:]
		} else {
			content += newBlock
		}
	} else {
		content += newBlock
	}

	// Write via podman cp: write content to a host temp file, copy it into
	// the container, then remove the temp file.
	tmp, err := os.CreateTemp("", "pgcli-pg-conf-*.conf")
	if err != nil {
		return false, fmt.Errorf("pg_tuning: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return false, fmt.Errorf("pg_tuning: write temp file: %w", err)
	}
	tmp.Close()

	// podman cp <host-path> <container>:<container-path>
	if _, err := m.run("cp", tmpPath, m.cfg.Podman.ContainerName+":"+pgConfPath); err != nil {
		return false, fmt.Errorf("pg_tuning: podman cp postgresql.conf: %w", err)
	}

	fmt.Println("-> PostgreSQL performance tuning applied")

	return needsRestart, nil
}
