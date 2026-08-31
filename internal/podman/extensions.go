package podman

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Extension represents a PostgreSQL extension managed by pgcli.
type Extension struct {
	Name         string // extension name (e.g., pg_stat_statements)
	Package      string // apt package suffix (e.g., pg-stat-statements)
	NeedsPreload bool   // must be in shared_preload_libraries
	CreateInDB   bool   // run CREATE EXTENSION IF NOT EXISTS by default
	Builtin      bool   // already in the base image (contrib), no apt install needed
	CreateName   string // SQL extension name if different from Name (e.g., "vector" for pgvector)
}

// ExtensionCatalog contains well-known extensions available from the Pigsty DEB repo
// or already included in the base PostgreSQL image (contrib modules).
var ExtensionCatalog = []Extension{
	// --- Builtin (contrib, already in postgres:18 image) ---
	{Name: "pg_stat_statements", Package: "", NeedsPreload: true, CreateInDB: true, Builtin: true},
	{Name: "uuid-ossp", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "hstore", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "pgcrypto", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "tablefunc", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "btree_gist", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "btree_gin", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "pg_trgm", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "unaccent", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "fuzzystrmatch", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "intarray", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "isn", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "citext", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "ltree", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "cube", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "earthdistance", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "pg_buffercache", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "pg_freespacemap", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "pg_visibility", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "pgstattuple", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "pageinspect", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "pg_prewarm", Package: "", NeedsPreload: true, CreateInDB: true, Builtin: true},
	{Name: "pgrowlocks", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "bloom", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "amcheck", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "file_fdw", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "postgres_fdw", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "dblink", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "sslinfo", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "xml2", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "seg", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "dict_int", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "dict_xsyn", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "intagg", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "lo", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "autoinc", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "insert_username", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "moddatetime", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "refint", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "tsm_system_rows", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "tsm_system_time", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "pg_walinspect", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "pg_surgery", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "pg_logicalinspect", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},
	{Name: "tcn", Package: "", NeedsPreload: false, CreateInDB: true, Builtin: true},

	// --- Non-builtin (requires apt install from Pigsty/PGDG) ---
	{Name: "pg_hint_plan", Package: "pg-hint-plan", NeedsPreload: true, CreateInDB: true},
	{Name: "pg_cron", Package: "pg-cron", NeedsPreload: true, CreateInDB: true},
	{Name: "pg_stat_monitor", Package: "pg-stat-monitor", NeedsPreload: true, CreateInDB: true},
	{Name: "pg_qualstats", Package: "pg-qualstats", NeedsPreload: true, CreateInDB: true},
	{Name: "pg_stat_kcache", Package: "pg-stat-kcache", NeedsPreload: true, CreateInDB: true},
	{Name: "pg_wait_sampling", Package: "pg-wait-sampling", NeedsPreload: true, CreateInDB: true},
	{Name: "pg_track_settings", Package: "pg-track-settings", NeedsPreload: true, CreateInDB: true},
	{Name: "pg_repack", Package: "pg-repack", NeedsPreload: false, CreateInDB: true},
	{Name: "pg_squeeze", Package: "pg-squeeze", NeedsPreload: false, CreateInDB: true},
	{Name: "pg_partman", Package: "pg-partman", NeedsPreload: false, CreateInDB: true},
	{Name: "pgmq", Package: "pgmq", NeedsPreload: false, CreateInDB: true},
	{Name: "pgvector", Package: "pgvector", NeedsPreload: false, CreateInDB: true, CreateName: "vector"},
	{Name: "postgis", Package: "postgis-3", NeedsPreload: false, CreateInDB: true},
	{Name: "timescaledb", Package: "timescaledb", NeedsPreload: true, CreateInDB: true},
}

// GetExtension returns catalog entry for extName, or nil if not found.
func GetExtension(extName string) *Extension {
	for i := range ExtensionCatalog {
		if ExtensionCatalog[i].Name == extName {
			return &ExtensionCatalog[i]
		}
	}
	return nil
}

const (
	pgExtBegin = "# === pgcli extensions (managed — do not edit) ==="
	pgExtEnd   = "# === end pgcli extensions ==="
)

// ---------------------------------------------------------------------------
// Extension image build
// ---------------------------------------------------------------------------

// ExtensionImageTag derives a deterministic image tag from the base tag.
// All extension images share a single "-ext" suffix; the exact extension
// list is tracked in the config file, not encoded in the tag.
func ExtensionImageTag(baseTag string, _ []string) string {
	return BaseImageTag(baseTag) + "-ext"
}

// BaseImageTag extracts the original base image tag from a possibly-extension
// image tag.  Extension tags follow the pattern: <base>-ext-<ext1>-<ext2>-...
// If the tag is not an extension tag, it is returned as-is.
func BaseImageTag(tag string) string {
	if i := findExtSuffix(tag); i >= 0 {
		return tag[:i]
	}
	return tag
}

// findExtSuffix finds the position of "-ext-" suffix in the tag.
func findExtSuffix(tag string) int {
	for i := 0; i < len(tag)-4; i++ {
		if tag[i:i+5] == "-ext-" {
			return i
		}
	}
	return -1
}

// HasNonBuiltinExtensions returns true if any of the named extensions
// require an apt package (not already in the base image).
func HasNonBuiltinExtensions(extNames []string) bool {
	for _, name := range extNames {
		ext := GetExtension(name)
		if ext == nil || !ext.Builtin {
			return true
		}
	}
	return false
}

// BuildExtensionImage builds a derived image that layers Pigsty DEB source
// and non-builtin extension packages on top of fromTag.  Builtin (contrib)
// extensions are skipped — they are already in the base image.
//
// For install: fromTag is the current ImageTag (may be base or -ext),
//
//	pkgList contains ALL extensions (old + new).
//
// For remove:  fromTag is the base image tag (BaseImageTag),
//
//	pkgList contains the REMAINING extensions.
func (m *Manager) BuildExtensionImage(fromTag string, pkgList []string) (string, error) {
	newTag := ExtensionImageTag(BaseImageTag(fromTag), pkgList)

	if exists, _ := m.imageExists(newTag); exists {
		fmt.Printf("-> Extension image %s already exists\n", newTag)
		return newTag, nil
	}

	// Filter to non-builtin extensions only
	var aptPkgs []string
	for _, name := range pkgList {
		ext := GetExtension(name)
		if ext != nil && ext.Builtin {
			continue // already in base image
		}
		pkg := name
		if ext != nil && ext.Package != "" {
			pkg = ext.Package
		}
		aptPkgs = append(aptPkgs, "postgresql-18-"+pkg)
	}

	if len(aptPkgs) == 0 {
		fmt.Println("-> All extensions are built-in (contrib), no image build needed")
		return fromTag, nil
	}

	fmt.Printf("-> Building extension image with %d package(s) (from %s)...\n", len(aptPkgs), fromTag)

	var dockerfile string
	// If building from an existing -ext image, Pigsty repo is already configured.
	// Just install additional packages.
	if strings.HasSuffix(fromTag, "-ext") {
		dockerfile = fmt.Sprintf(`FROM %s

ENV http_proxy="" HTTP_PROXY="" https_proxy="" HTTPS_PROXY="" no_proxy="" NO_PROXY=""
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
    && apt-get install -y %s \
    && rm -rf /var/lib/apt/lists/*
`, fromTag, strings.Join(aptPkgs, " "))
	} else {
		// First-time ext build: set up Pigsty repo from scratch
		dockerfile = fmt.Sprintf(`FROM %s

ENV http_proxy="" HTTP_PROXY="" https_proxy="" HTTPS_PROXY="" no_proxy="" NO_PROXY=""
ENV DEBIAN_FRONTEND=noninteractive

# Pigsty DEB repository (https://pigsty.io/ext/) — 576+ PG extensions
RUN apt-get update && apt-get install -y curl gnupg2 lsb-release \
    && curl -fsSL https://repo.pigsty.io/key | gpg --dearmor -o /etc/apt/keyrings/pigsty.gpg \
    && . /etc/os-release \
    && echo "deb [signed-by=/etc/apt/keyrings/pigsty.gpg] https://repo.pigsty.io/apt/infra generic main" > /etc/apt/sources.list.d/pigsty.list \
    && echo "deb [signed-by=/etc/apt/keyrings/pigsty.gpg] https://repo.pigsty.io/apt/pgsql/${VERSION_CODENAME} ${VERSION_CODENAME} main" >> /etc/apt/sources.list.d/pigsty.list \
    && apt-get update \
    && apt-get install -y %s \
    && rm -rf /var/lib/apt/lists/* \
    && apt-get purge -y --auto-remove curl gnupg2 lsb-release
`, fromTag, strings.Join(aptPkgs, " "))
	}

	buildDir := filepath.Join(m.dataDir, "ext-build")
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return "", fmt.Errorf("creating ext build directory: %w", err)
	}

	cfPath := filepath.Join(buildDir, "Containerfile.ext")
	if err := os.WriteFile(cfPath, []byte(dockerfile), 0644); err != nil {
		return "", fmt.Errorf("writing extension Containerfile: %w", err)
	}

	if err := m.runInteractive("build", "-t", newTag, "-f", cfPath, buildDir); err != nil {
		return "", fmt.Errorf("building extension image: %w", err)
	}

	fmt.Printf("  [OK] Extension image built: %s\n", newTag)
	return newTag, nil
}

// NeedsRebuild returns true if the current image tag does not include all
// requested extensions (i.e. it doesn't match the expected extension tag).
func (m *Manager) NeedsRebuild(extNames []string) bool {
	if len(extNames) == 0 {
		return false
	}
	expected := ExtensionImageTag(m.cfg.Podman.ImageTag, extNames)
	current := m.cfg.Podman.ImageTag
	return current != expected
}

// ReplaceContainer stops and removes the current container, updates the
// image tag in config, and recreates the container from the new image.
// Data volumes are preserved (they live on the host).
func (m *Manager) ReplaceContainer(newImageTag string) error {
	fmt.Println("-> Replacing container with new extension image...")

	// Stop if running
	running, _ := m.containerRunning(m.cfg.Podman.ContainerName)
	if running {
		if err := m.StopContainer(); err != nil {
			fmt.Printf("  [!] stop container: %v\n", err)
		}
	}

	// Remove container (volumes on host are preserved)
	if _, err := m.run("rm", m.cfg.Podman.ContainerName); err != nil {
		return fmt.Errorf("removing container: %w", err)
	}

	// Update image tag in config
	oldTag := m.cfg.Podman.ImageTag
	m.cfg.Podman.ImageTag = newImageTag

	// Recreate container from new image
	if err := m.EnsureContainer(); err != nil {
		// Rollback image tag on failure
		m.cfg.Podman.ImageTag = oldTag
		return fmt.Errorf("recreating container: %w", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// shared_preload_libraries + CREATE EXTENSION
// ---------------------------------------------------------------------------

// ExecLong runs a command inside the container with a 5-minute timeout,
// suitable for long-running operations like CREATE EXTENSION on large datasets.
func (m *Manager) ExecLong(args ...string) (string, error) {
	podmanArgs := append([]string{"exec", "-i=false", m.cfg.Podman.ContainerName}, args...)
	return execWithTimeout(m.podman, podmanArgs, 5*time.Minute)
}

// GetInstalledExtensions queries pg_extension and returns installed extension names.
func (m *Manager) GetInstalledExtensions() ([]string, error) {
	sql := "SELECT extname FROM pg_extension WHERE extname != 'plpgsql' ORDER BY extname"
	out, err := m.Exec("psql", "-U", m.cfg.Postgres.User, "-d", m.cfg.Postgres.Database, "-t", "-A", "-c", sql)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// RunCreateExtensions runs CREATE EXTENSION IF NOT EXISTS for each extension.
func (m *Manager) RunCreateExtensions(extNames []string) error {
	for _, name := range extNames {
		ext := GetExtension(name)
		if ext != nil && !ext.CreateInDB {
			continue
		}
		// Use CreateName if specified (e.g., "vector" for pgvector)
		sqlName := name
		if ext != nil && ext.CreateName != "" {
			sqlName = ext.CreateName
		}
		sql := fmt.Sprintf("CREATE EXTENSION IF NOT EXISTS \"%s\"", sqlName)
		fmt.Printf("-> Running: %s\n", sql)
		out, err := m.ExecLong("psql", "-U", m.cfg.Postgres.User, "-d", m.cfg.Postgres.Database, "-c", sql)
		if err != nil {
			fmt.Printf("  [!] Warning: CREATE EXTENSION %s: %v\n", name, err)
			if out != "" {
				fmt.Printf("  Output: %s\n", out)
			}
		}
	}
	return nil
}

// ApplyExtensions manages shared_preload_libraries for extensions that need
// preloading.  It replaces (or appends) a sentinel block in postgresql.conf.
// Returns needsRestart=true if shared_preload_libraries changed.
func (m *Manager) ApplyExtensions(extNames []string) (needsRestart bool, err error) {
	// Collect extensions that need preloading
	var preload []string
	for _, name := range extNames {
		ext := GetExtension(name)
		if ext != nil && ext.NeedsPreload {
			preload = append(preload, name)
		}
	}

	// Read current postgresql.conf
	current, err := m.Exec("cat", pgConfPath)
	if err != nil {
		return false, fmt.Errorf("apply extensions: read postgresql.conf: %w", err)
	}

	// Build new block
	var lines []string
	lines = append(lines, pgExtBegin)
	if len(preload) > 0 {
		spl := fmt.Sprintf("shared_preload_libraries = '%s'", strings.Join(preload, ","))
		lines = append(lines, spl)
	}
	lines = append(lines, pgExtEnd)
	newBlock := "\n" + strings.Join(lines, "\n") + "\n"

	// Check if block exists and is identical
	if strings.Contains(current, pgExtBegin) {
		start := strings.Index(current, pgExtBegin)
		end := strings.Index(current[start:], pgExtEnd)
		if end >= 0 {
			existing := current[start : start+end+len(pgExtEnd)]
			if existing == strings.TrimPrefix(strings.TrimSuffix(newBlock, "\n"), "\n") {
				return false, nil // no change
			}
		}
	}

	// Detect if shared_preload_libraries is changing
	oldSPL := ""
	if idx := strings.Index(current, "shared_preload_libraries"); idx >= 0 {
		lineEnd := strings.Index(current[idx:], "\n")
		if lineEnd >= 0 {
			oldSPL = current[idx : idx+lineEnd]
		}
	}
	newSPL := ""
	if len(preload) > 0 {
		newSPL = fmt.Sprintf("shared_preload_libraries = '%s'", strings.Join(preload, ","))
	}
	if oldSPL != newSPL {
		needsRestart = true
	}

	// Build merged content: replace existing block or append
	content := current
	if idx := strings.Index(content, pgExtBegin); idx >= 0 {
		endIdx := strings.Index(content[idx:], pgExtEnd)
		if endIdx >= 0 {
			tail := idx + endIdx + len(pgExtEnd)
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

	// Write via podman cp
	tmp, err := os.CreateTemp("", "pgcli-pg-conf-*.conf")
	if err != nil {
		return false, fmt.Errorf("apply extensions: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return false, fmt.Errorf("apply extensions: write temp file: %w", err)
	}
	tmp.Close()

	if _, err := m.run("cp", tmpPath, m.cfg.Podman.ContainerName+":"+pgConfPath); err != nil {
		return false, fmt.Errorf("apply extensions: podman cp postgresql.conf: %w", err)
	}
	if err := m.chownDataFile(m.cfg.Podman.ContainerName, pgConfPath); err != nil {
		return false, fmt.Errorf("apply extensions: chown postgresql.conf: %w", err)
	}

	if len(preload) > 0 {
		fmt.Printf("-> Extensions configured: shared_preload_libraries = '%s'\n", strings.Join(preload, ","))
	}

	return needsRestart, nil
}
