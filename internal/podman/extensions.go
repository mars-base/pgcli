package podman

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// createNameAlias maps alternative user-facing extension names to the
// canonical SQL extension name used in Pigsty's catalog and CREATE EXTENSION.
// Only entries where the user-typed name differs from the SQL name are needed.
var createNameAlias = map[string]string{
	"pgvector": "vector", // user types "pgvector", SQL extension is "vector"
}

// ResolveExtName resolves a user-supplied extension name to the canonical
// SQL extension name used by Pigsty.  For example "pgvector" → "vector".
// Returns the resolved name.
func ResolveExtName(name string) string {
	if resolved, ok := createNameAlias[name]; ok {
		return resolved
	}
	return name
}

// IsExtensionKnown returns true if the extension name is recognized —
// either as a builtin (contrib) or in the Pigsty catalog.
func IsExtensionKnown(name string) bool {
	resolved := ResolveExtName(name)
	_, _, _, found := LookupExtension(resolved)
	return found
}

// ExtNeedsPreload returns true if the extension requires shared_preload_libraries.
func ExtNeedsPreload(name string) bool {
	resolved := ResolveExtName(name)
	_, needsPreload, _, found := LookupExtension(resolved)
	return found && needsPreload
}

// ExtIsBuiltin returns true if the extension is already in the base image.
func ExtIsBuiltin(name string) bool {
	resolved := ResolveExtName(name)
	_, _, builtin, found := LookupExtension(resolved)
	return found && builtin
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
		if !ExtIsBuiltin(name) {
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

	// Filter to non-builtin extensions only, resolve apt package names
	var aptPkgs []string
	for _, name := range pkgList {
		if ExtIsBuiltin(name) {
			continue // already in base image
		}
		resolved := ResolveExtName(name)
		pkg, _, _, found := LookupExtension(resolved)
		if found && pkg != "" {
			aptPkgs = append(aptPkgs, "postgresql-18-"+pkg)
		} else {
			// Fallback: use the resolved name as package suffix
			aptPkgs = append(aptPkgs, "postgresql-18-"+resolved)
		}
	}

	if len(aptPkgs) == 0 {
		fmt.Println("-> All extensions are built-in (contrib), no image build needed")
		return fromTag, nil
	}

	// Check if the -ext image already exists with all required packages.
	// Uses a temporary container to inspect installed packages — avoids
	// rebuilding when the image already contains everything we need.
	if exists, _ := m.imageExists(newTag); exists {
		if m.extImageHasPackages(newTag, aptPkgs) {
			fmt.Printf("-> Extension image %s already has all required packages\n", newTag)
			return newTag, nil
		}
		// Image exists but is missing packages — will rebuild below.
		fmt.Printf("-> Extension image %s is missing packages, rebuilding...\n", newTag)
	}

	baseTag := BaseImageTag(fromTag)
	fmt.Printf("-> Building extension image with %d package(s) (from %s)...\n", len(aptPkgs), baseTag)

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

// extImageHasPackages checks if the given image already contains all the
// specified apt packages by running a temporary container and checking
// with dpkg -s.
func (m *Manager) extImageHasPackages(imageTag string, packages []string) bool {
	var checkCmd strings.Builder
	checkCmd.WriteString("set -e; ")
	for _, pkg := range packages {
		fmt.Fprintf(&checkCmd, "dpkg -s %s >/dev/null 2>&1 || exit 1; ", pkg)
	}
	checkCmd.WriteString("echo all-installed")

	// Run a temporary container to check packages
	cmd := exec.Command(m.podman, "run", "--rm", imageTag, "sh", "-c", checkCmd.String())
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}

	return strings.Contains(string(output), "all-installed")
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
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// RunCreateExtensions runs CREATE EXTENSION IF NOT EXISTS for each extension.
// Uses the resolved SQL name (e.g., "vector" for pgvector).
func (m *Manager) RunCreateExtensions(extNames []string) error {
	for _, name := range extNames {
		resolved := ResolveExtName(name)
		sql := fmt.Sprintf("CREATE EXTENSION IF NOT EXISTS \"%s\"", resolved)
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
		if ExtNeedsPreload(name) {
			// Use the resolved SQL name for shared_preload_libraries
			resolved := ResolveExtName(name)
			preload = append(preload, resolved)
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
