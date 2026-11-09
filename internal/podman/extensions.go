package podman

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/mirror"
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
	_, _, _, _, found := LookupExtension(resolved)
	return found
}

// ExtNeedsPreload returns true if the extension requires shared_preload_libraries.
func ExtNeedsPreload(name string) bool {
	resolved := ResolveExtName(name)
	_, needsPreload, _, _, found := LookupExtension(resolved)
	return found && needsPreload
}

// ExtIsBuiltin returns true if the extension is already in the base image.
func ExtIsBuiltin(name string) bool {
	resolved := ResolveExtName(name)
	_, _, _, builtin, found := LookupExtension(resolved)
	return found && builtin
}

// ExtMustBeFirst returns true if the extension must be at position 0 in
// shared_preload_libraries (FATAL if not, e.g. citus, timescaledb).
func ExtMustBeFirst(name string) bool {
	resolved := ResolveExtName(name)
	_, _, preloadFirst, _, found := LookupExtension(resolved)
	return found && preloadFirst
}

const (
	pgExtBegin = "# === pgcli extensions (managed — do not edit) ==="
	pgExtEnd   = "# === end pgcli extensions ==="
)

// ---------------------------------------------------------------------------
// Extension image build — shared builder (stateless)
// ---------------------------------------------------------------------------

// extImageBuilder is a stateless helper that builds extension images for any
// Debian-based PostgreSQL image (pgcli-pg, pgcli-patroni, etc.). It holds
// only the podman runner functions needed for build + package-check, not the
// full Manager or PatroniManager state. Both (*Manager).BuildExtensionImage
// and (*PatroniManager).BuildExtensionImage delegate to it.
type extImageBuilder struct {
	dataDir        string
	runInteractive func(args ...string) error
	imageExists    func(tag string) (bool, error)
	run            func(args ...string) (string, error)
}

// build is the shared implementation. fromTag is the base image (pgcli-pg:18-X
// or pgcli-patroni:18-4.1.5); pkgList is ALL extensions to include (install)
// or REMAINING extensions (remove). pigstyRepo is the Pigsty DEB repo URL.
// Returns the new image tag (fromTag-ext or fromTag if all builtin).
func (b *extImageBuilder) build(fromTag string, pkgList []string, pigstyRepo string) (string, error) {
	newTag := ExtensionImageTag(BaseImageTag(fromTag), pkgList)

	// Filter to non-builtin extensions only, resolve apt package names.
	// DocumentDB ships as an official vendor DEB rather than through the
	// Pigsty repo, so it is downloaded into the build context separately.
	var aptPkgs []string
	var checkPkgs []string
	needDocDB := false
	for _, name := range pkgList {
		if ExtIsBuiltin(name) {
			continue // already in base image
		}
		resolved := ResolveExtName(name)
		if resolved == "documentdb" {
			needDocDB = true
			checkPkgs = append(checkPkgs, "postgresql-18-documentdb", "libicu74")
			continue
		}
		pkg, _, _, _, found := LookupExtension(resolved)
		if found && pkg != "" {
			aptPkgs = append(aptPkgs, "postgresql-18-"+pkg)
			checkPkgs = append(checkPkgs, "postgresql-18-"+pkg)
		} else {
			// Fallback: use the resolved name as package suffix
			aptPkgs = append(aptPkgs, "postgresql-18-"+resolved)
			checkPkgs = append(checkPkgs, "postgresql-18-"+resolved)
		}
	}

	if len(checkPkgs) == 0 {
		fmt.Println("-> All extensions are built-in (contrib), no image build needed")
		return fromTag, nil
	}

	// The final image must keep the base image's runtime user: pgcli-pg runs
	// as root (its entrypoint starts sshd), pgcli-patroni runs as postgres.
	baseUser := b.imageUser(BaseImageTag(fromTag))

	// Decide the build base: if the -ext image already exists (even without
	// all required packages), build on top of it — Pigsty repo is already
	// configured and apt install is idempotent.  Only fall back to the plain
	// base image when no -ext image exists at all.
	buildFrom := newTag
	if exists, _ := b.imageExists(newTag); exists {
		if b.extImageHasPackages(newTag, checkPkgs) &&
			(!needDocDB || b.extImageHasDocDBPatch(newTag)) &&
			sameImageUser(b.imageUser(newTag), baseUser) {
			fmt.Printf("-> Extension image %s already has all required packages\n", newTag)
			return newTag, nil
		}
		// -ext image exists but missing packages or has a stale runtime user
		// (older builds forced USER postgres) — rebuild on top of it.
		fmt.Printf("-> Extension image %s exists, installing missing packages on top...\n", newTag)
	} else {
		// No -ext image at all — first-time build from the plain base image.
		buildFrom = BaseImageTag(fromTag)
		fmt.Printf("-> Building extension image with %d package(s) (from %s)...\n", len(checkPkgs), buildFrom)
	}

	buildDir := filepath.Join(b.dataDir, "ext-build")
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return "", fmt.Errorf("creating ext build directory: %w", err)
	}

	var localDebs []string
	if needDocDB {
		if err := ensureDocumentDBExtDeb(buildDir); err != nil {
			return "", err
		}
		if err := ensureDocumentDBIcuDeb(buildDir); err != nil {
			return "", err
		}
		localDebs = []string{documentdbDebFileName, documentdbIcuDebFileName}
	}

	dockerfile := extContainerfile(buildFrom, aptPkgs, localDebs, pigstyRepo, baseUser)

	cfPath := filepath.Join(buildDir, "Containerfile.ext")
	if err := os.WriteFile(cfPath, []byte(dockerfile), 0644); err != nil {
		return "", fmt.Errorf("writing extension Containerfile: %w", err)
	}

	if err := b.runInteractive("build", "-t", newTag, "-f", cfPath, buildDir); err != nil {
		return "", fmt.Errorf("building extension image: %w", err)
	}

	fmt.Printf("  [OK] Extension image built: %s\n", newTag)
	return newTag, nil
}

// extContainerfile renders the extension image build file. baseUser is the
// runtime user configured on the base image; an empty value means the image
// inherits root, so no trailing USER instruction is needed. localDebs are
// filenames in the build context (official vendor packages).
func extContainerfile(buildFrom string, aptPkgs, localDebs []string, pigstyRepo, baseUser string) string {
	userLine := ""
	if baseUser != "" {
		userLine = "USER " + baseUser + "\n"
	}

	// COPY lines and install arguments for vendor DEBs.
	var copies strings.Builder
	installs := append([]string(nil), aptPkgs...)
	patchDocDB := false
	for _, deb := range localDebs {
		fmt.Fprintf(&copies, "COPY %s /tmp/%s\n", deb, deb)
		installs = append(installs, "/tmp/"+deb)
		if deb == documentdbDebFileName {
			patchDocDB = true
		}
	}
	copyLines := copies.String()

	// DocumentDB's core library was built against ICU 74 but does not declare
	// it as a dependency; make the bundled ICU 74 runtime load with it.
	installStep := ""
	purgeStep := ""
	purgePkgs := ""
	if patchDocDB {
		installs = append(installs, "patchelf")
		installStep = " \\\n    && (patchelf --print-needed /usr/lib/postgresql/18/lib/pg_documentdb_core.so | grep -qx libicui18n.so.74 \\\n        || patchelf --add-needed libicui18n.so.74 /usr/lib/postgresql/18/lib/pg_documentdb_core.so)"
		purgeStep = " && apt-get purge -y patchelf"
		purgePkgs = " patchelf"
	}

	if strings.HasSuffix(buildFrom, "-ext") {
		// Building from an existing -ext image: Pigsty repo already configured,
		// just install additional packages (apt install is idempotent).
		return fmt.Sprintf(`FROM %s

ENV http_proxy="" HTTP_PROXY="" https_proxy="" HTTPS_PROXY="" no_proxy="" NO_PROXY=""
ENV DEBIAN_FRONTEND=noninteractive

USER root
%sRUN apt-get update \
    && apt-get install -y %s%s \
    && rm -rf /var/lib/apt/lists/*%s
%s`, buildFrom, copyLines, strings.Join(installs, " "), installStep, purgeStep, userLine)
	}

	// First-time ext build from the plain base image: set up Pigsty repo.
	return fmt.Sprintf(`FROM %s

ENV http_proxy="" HTTP_PROXY="" https_proxy="" HTTPS_PROXY="" no_proxy="" NO_PROXY=""
ENV DEBIAN_FRONTEND=noninteractive

USER root
%s# Pigsty DEB repository (https://pigsty.io/ext/) — 576+ PG extensions
RUN apt-get update && apt-get install -y curl gnupg2 lsb-release \
    && curl -fsSL %s/key | gpg --dearmor -o /etc/apt/keyrings/pigsty.gpg \
    && . /etc/os-release \
    && echo "deb [signed-by=/etc/apt/keyrings/pigsty.gpg] %s/apt/infra generic main" > /etc/apt/sources.list.d/pigsty.list \
    && echo "deb [signed-by=/etc/apt/keyrings/pigsty.gpg] %s/apt/pgsql/${VERSION_CODENAME} ${VERSION_CODENAME} main" >> /etc/apt/sources.list.d/pigsty.list \
    && apt-get update \
    && apt-get install -y %s%s \
    && rm -rf /var/lib/apt/lists/* \
    && apt-get purge -y --auto-remove curl gnupg2 lsb-release%s
%s`, buildFrom, copyLines, pigstyRepo, pigstyRepo, pigstyRepo, strings.Join(installs, " "), installStep, purgePkgs, userLine)
}

// ensureDocumentDBExtDeb downloads the official DocumentDB PG18 extension DEB
// for the current GOARCH into dir and verifies its pinned sha256. A cached
// file with a matching digest is reused.
func ensureDocumentDBExtDeb(dir string) error {
	url, want, err := documentdbExtDebURL(runtime.GOARCH)
	if err != nil {
		return err
	}
	return downloadVerifiedFile(dir, documentdbDebFileName, "DocumentDB extension", url, want)
}

// ensureDocumentDBIcuDeb downloads the Ubuntu 24.04 ICU 74 runtime that the
// official extension binaries were linked against (see ext_catalog.go).
func ensureDocumentDBIcuDeb(dir string) error {
	url, want, err := documentdbIcuDebURL(runtime.GOARCH)
	if err != nil {
		return err
	}
	return downloadVerifiedFile(dir, documentdbIcuDebFileName, "DocumentDB ICU 74 runtime", url, want)
}

// downloadVerifiedFile downloads url into dir/name unless a cached copy already
// matches want, and verifies the sha256 either way.
func downloadVerifiedFile(dir, name, label, url, want string) error {
	dest := filepath.Join(dir, name)
	if got, err := fileSHA256(dest); err == nil && strings.EqualFold(got, want) {
		fmt.Printf("-> Using cached %s package (%s)\n", label, name)
		return nil
	}

	fmt.Printf("-> Downloading %s package for %s...\n", label, runtime.GOARCH)
	url = mirror.Resolve(url)
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
		Timeout:   300 * time.Second,
	}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("documentdb: downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("documentdb: downloading %s: HTTP %s", url, resp.Status)
	}

	tmp, err := os.CreateTemp(dir, "."+name+".download-*")
	if err != nil {
		return fmt.Errorf("documentdb: creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		tmp.Close()
		return fmt.Errorf("documentdb: downloading %s: %w", url, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("documentdb: writing %s: %w", tmpName, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("documentdb: checksum mismatch for %s\n  got  %s\n  want %s", url, got, want)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return fmt.Errorf("documentdb: installing %s: %w", dest, err)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// imageUser returns the runtime user configured in an image, or "" when the
// image has no explicit user (root).
func (b *extImageBuilder) imageUser(tag string) string {
	out, err := b.run("image", "inspect", "--format", "{{.Config.User}}", tag)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// sameImageUser compares runtime users, treating an empty user and "root" as
// equivalent.
func sameImageUser(a, b string) bool {
	return normalizeImageUser(a) == normalizeImageUser(b)
}

func normalizeImageUser(user string) string {
	user = strings.TrimSpace(user)
	if user == "" || user == "root" || user == "0" || user == "0:0" {
		return "root"
	}
	return user
}

// extImageHasPackages checks if the given image already contains all the
// specified apt packages by running a temporary container and checking
// with dpkg -s.
func (b *extImageBuilder) extImageHasPackages(imageTag string, packages []string) bool {
	var checkCmd strings.Builder
	checkCmd.WriteString("set -e; ")
	for _, pkg := range packages {
		fmt.Fprintf(&checkCmd, "dpkg -s %s >/dev/null 2>&1 || exit 1; ", pkg)
	}
	checkCmd.WriteString("echo all-installed")

	// Run a temporary container to check packages
	out, err := b.run("run", "--rm", imageTag, "sh", "-c", checkCmd.String())
	if err != nil {
		return false
	}
	return strings.Contains(out, "all-installed")
}

// extImageHasDocDBPatch reports whether pg_documentdb_core.so carries the
// bundled ICU 74 dependency. The string appears in the dynamic string table
// only after patchelf --add-needed; patchelf itself is removed from the image.
func (b *extImageBuilder) extImageHasDocDBPatch(imageTag string) bool {
	_, err := b.run("run", "--rm", imageTag, "sh", "-c",
		"grep -aq libicui18n.so.74 /usr/lib/postgresql/18/lib/pg_documentdb_core.so")
	return err == nil
}

// ExtPreloadLibs returns the shared_preload_libraries entries contributed by
// one extension, in load order. Most extensions contribute their own name.
// documentdb is special: it needs pg_cron plus three DocumentDB libraries.
// The second return is true for extensions that must occupy position 0.
func ExtPreloadLibs(name string) (libs []string, preloadFirst bool) {
	resolved := ResolveExtName(name)
	if resolved == "documentdb" {
		return []string{
			"pg_cron",
			"pg_documentdb_core",
			"pg_documentdb",
			"pg_documentdb_extended_rum",
		}, false
	}
	if ExtNeedsPreload(name) {
		return []string{resolved}, ExtMustBeFirst(name)
	}
	return nil, false
}

// BuildPreloadCSV builds the shared_preload_libraries CSV from a list of
// extensions. Extensions with PreloadFirst are forced to position 0 (FATAL if not).
// Returns the CSV string and whether pg_cron is present (needs cron.database_name).
func BuildPreloadCSV(extNames []string) (csv string, hasCron bool) {
	libs, hasCron := collectPreloadLibs(extNames)
	return strings.Join(libs, ","), hasCron
}

// collectPreloadLibs merges ExtPreloadLibs across extensions, deduplicating
// libraries while keeping the first occurrence. PreloadFirst libraries are
// hoisted to the front in their original order.
func collectPreloadLibs(extNames []string) (libs []string, hasCron bool) {
	var preload, first []string
	seen := make(map[string]bool)
	for _, name := range extNames {
		extLibs, isFirst := ExtPreloadLibs(name)
		for _, lib := range extLibs {
			if seen[lib] {
				continue
			}
			seen[lib] = true
			if isFirst {
				first = append(first, lib)
			} else {
				preload = append(preload, lib)
			}
			if lib == "pg_cron" {
				hasCron = true
			}
		}
	}
	return append(first, preload...), hasCron
}

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

// findExtSuffix finds the position of "-ext" suffix in the tag.
// Handles both "-ext" at end and "-ext-" in the middle.
func findExtSuffix(tag string) int {
	// Check if tag ends with "-ext"
	if len(tag) >= 4 && tag[len(tag)-4:] == "-ext" {
		return len(tag) - 4
	}
	// Check for "-ext-" in the middle
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
// and non-builtin extension packages on top of fromTag.  Delegates to the
// shared extImageBuilder (zero behavior change).
//
// For install: fromTag is the current ImageTag (may be base or -ext),
//
//	pkgList contains ALL extensions (old + new).
//
// For remove:  fromTag is the base image tag (BaseImageTag),
//
//	pkgList contains the REMAINING extensions.
func (m *Manager) BuildExtensionImage(fromTag string, pkgList []string, pigstyRepo string) (string, error) {
	b := &extImageBuilder{
		dataDir:        m.dataDir,
		runInteractive: m.runInteractive,
		imageExists:    m.imageExists,
		run:            m.run,
	}
	return b.build(fromTag, pkgList, pigstyRepo)
}

// EnsurePgpass creates or updates the .pgpass file for the postgres OS user.
// This is needed by pg_cron and other background workers that connect via TCP.
func (m *Manager) EnsurePgpass() error {
	// Write .pgpass with current credentials
	pgpassContent := fmt.Sprintf("*:*:*:%s:%s\n", m.cfg.Postgres.User, m.cfg.Postgres.Password)
	execArgs := []string{"exec", m.cfg.Podman.ContainerName,
		"sh", "-c",
		fmt.Sprintf("echo '%s' > /var/lib/postgresql/.pgpass && chmod 600 /var/lib/postgresql/.pgpass && chown postgres:postgres /var/lib/postgresql/.pgpass",
			pgpassContent)}
	cmd := exec.Command(m.podman, execArgs...)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Cgroup fallback: retry via systemd-run
		if isCgroupPermErr(err) {
			slog.Debug("cgroup fallback via systemd-run (EnsurePgpass)")
			runArgs := append([]string{"--user", "--scope", "--quiet", m.podman}, execArgs...)
			cmd2 := exec.Command("systemd-run", runArgs...)
			if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
				return fmt.Errorf("creating .pgpass: %w (output: %s)", err2, string(out2))
			}
			return nil
		}
		return fmt.Errorf("creating .pgpass: %w (output: %s)", err, string(out))
	}
	return nil
}

// GetContainerImageID returns the image ID of the current container.
func (m *Manager) GetContainerImageID() (string, error) {
	cmd := exec.Command(m.podman, "inspect", m.cfg.Podman.ContainerName, "--format", "{{.Image}}")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// GetImageID returns the image ID for a given tag.
func (m *Manager) GetImageID(tag string) (string, error) {
	cmd := exec.Command(m.podman, "inspect", tag, "--format", "{{.Id}}")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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

// ExtensionsNeedCron reports whether any of the named extensions still
// requires pg_cron in shared_preload_libraries.
func ExtensionsNeedCron(extNames []string) bool {
	for _, name := range extNames {
		libs, _ := ExtPreloadLibs(name)
		for _, lib := range libs {
			if lib == "pg_cron" {
				return true
			}
		}
	}
	return false
}

// ExtCreateStatements returns the SQL statements that create one extension.
// documentdb needs pg_cron first and an explicit documentdb_extended_rum:
// CREATE EXTENSION documentdb CASCADE does not pull the RUM companion in.
func ExtCreateStatements(name string) []string {
	resolved := ResolveExtName(name)
	if resolved == "documentdb" {
		return []string{
			"CREATE EXTENSION IF NOT EXISTS pg_cron",
			`CREATE EXTENSION IF NOT EXISTS "documentdb" CASCADE`,
			`CREATE EXTENSION IF NOT EXISTS "documentdb_extended_rum"`,
		}
	}
	return []string{fmt.Sprintf(`CREATE EXTENSION IF NOT EXISTS "%s"`, resolved)}
}

// ExtDropStatements returns the SQL statements that drop one extension.
// documentdb is removed in dependency order, bottom-up. pg_cron is appended
// only when allowCronDrop is true — the caller must confirm nothing else needs
// it and that cron.job is empty.
func ExtDropStatements(name string, allowCronDrop bool) []string {
	resolved := ResolveExtName(name)
	if resolved != "documentdb" {
		return []string{fmt.Sprintf(`DROP EXTENSION IF EXISTS "%s"`, resolved)}
	}
	stmts := []string{
		`DROP EXTENSION IF EXISTS "documentdb_extended_rum"`,
		`DROP EXTENSION IF EXISTS "documentdb"`,
		`DROP EXTENSION IF EXISTS "documentdb_core"`,
	}
	if allowCronDrop {
		stmts = append(stmts, `DROP EXTENSION IF EXISTS pg_cron`)
	}
	return stmts
}

// RunCreateExtensions runs CREATE EXTENSION IF NOT EXISTS for each extension.
// Uses the resolved SQL name (e.g., "vector" for pgvector).
// DocumentDB failures are fatal: its three CREATE statements form one unit and
// a silent partial install would leave the gateway unusable.
func (m *Manager) RunCreateExtensions(extNames []string) error {
	hasDocDB := false
	ran := make(map[string]bool)
	for _, name := range extNames {
		isDocDB := ResolveExtName(name) == "documentdb"
		hasDocDB = hasDocDB || isDocDB
		for _, sql := range ExtCreateStatements(name) {
			key := sql
			if sql == "CREATE EXTENSION IF NOT EXISTS pg_cron" {
				key = "pg_cron" // shared by documentdb and an explicit pg_cron request
			}
			if ran[key] {
				continue
			}
			ran[key] = true
			fmt.Printf("-> Running: %s\n", sql)
			out, err := m.ExecLong("psql", "-U", m.cfg.Postgres.User, "-d", m.cfg.Postgres.Database, "-c", sql)
			if err == nil {
				continue
			}
			if isDocDB {
				return fmt.Errorf("creating DocumentDB extension: %w (output: %s)", err, strings.TrimSpace(out))
			}
			fmt.Printf("  [!] Warning: CREATE EXTENSION %s: %v\n", name, err)
			if out != "" {
				fmt.Printf("  Output: %s\n", out)
			}
		}
	}

	if hasDocDB {
		installed, err := m.GetInstalledExtensions()
		if err != nil {
			return fmt.Errorf("documentdb: verifying installed extensions: %w", err)
		}
		have := make(map[string]bool, len(installed))
		for _, e := range installed {
			have[e] = true
		}
		for _, want := range []string{"pg_cron", "documentdb", "documentdb_core", "documentdb_extended_rum"} {
			if !have[want] {
				return fmt.Errorf("documentdb: extension %q is missing after CREATE", want)
			}
		}
	}
	return nil
}

// renderExtConfBlock renders the pgcli-managed block in postgresql.conf.
// preload is the ordered, deduplicated shared_preload_libraries list.
// hasCron adds the pg_cron GUCs; docdb adds the DocumentDB GUCs, whose
// localhost_connection_string points back at the instance's host port.
func renderExtConfBlock(preload []string, hasCron, docdb bool, database string, hostPort int) string {
	var lines []string
	lines = append(lines, pgExtBegin)
	if len(preload) > 0 {
		lines = append(lines, fmt.Sprintf("shared_preload_libraries = '%s'", strings.Join(preload, ",")))
	}
	if hasCron {
		// cron.database_name is postmaster-level: CREATE EXTENSION pg_cron must
		// run in the same database the postmaster was told to use.
		lines = append(lines,
			fmt.Sprintf("cron.database_name = '%s'", database),
			"cron.use_background_workers = on",
		)
	}
	if docdb {
		lines = append(lines,
			"documentdb.enableBackgroundWorker = true",
			"documentdb.enableBackgroundWorkerJobs = true",
			"documentdb.indexBuildsScheduledOnBgWorker = false",
			fmt.Sprintf("documentdb.localhost_connection_string = 'host=/var/run/postgresql port=%d'", hostPort),
			"default_toast_compression = 'lz4'",
			"documentdb.rum_library_load_option = 'require_documentdb_extended_rum'",
			"documentdb.alternate_index_handler_name = 'extended_rum'",
		)
	}
	lines = append(lines, pgExtEnd)
	return "\n" + strings.Join(lines, "\n") + "\n"
}

// extBlockHasSettings reports whether a managed block carries any parameter
// line (as opposed to only the sentinel comments).
func extBlockHasSettings(block string) bool {
	for line := range strings.SplitSeq(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == pgExtBegin || line == pgExtEnd {
			continue
		}
		return true
	}
	return false
}

// ApplyExtensions manages shared_preload_libraries for extensions that need
// preloading.  It replaces (or appends) a sentinel block in postgresql.conf.
// For pg_cron (and documentdb, which depends on it), also sets
// cron.database_name to the configured database (postmaster-level).
// Returns needsRestart=true if any postmaster-level parameter changed.
//
// NOT for Patroni members: Patroni owns postgresql.conf and regenerates it
// from DCS every cycle, so a direct edit is clobbered. Use patronictl
// edit-config to set postgresql.parameters.shared_preload_libraries in the
// DCS instead — see internal/cli/ha_extension.go.
func (m *Manager) ApplyExtensions(extNames []string) (needsRestart bool, err error) {
	preload, hasCron := collectPreloadLibs(extNames)
	docdb := false
	for _, name := range extNames {
		if ResolveExtName(name) == "documentdb" {
			docdb = true
			break
		}
	}
	hasSettings := len(preload) > 0 || hasCron || docdb

	// Read current postgresql.conf
	current, err := m.Exec("cat", pgConfPath)
	if err != nil {
		return false, fmt.Errorf("apply extensions: read postgresql.conf: %w", err)
	}

	newBlock := renderExtConfBlock(preload, hasCron, docdb, m.cfg.Postgres.Database, m.cfg.Podman.HostPort)
	newBlockTrimmed := strings.Trim(newBlock, "\n")

	// Extract the existing managed block, if any.
	existing := ""
	hasBlock := false
	if start := strings.Index(current, pgExtBegin); start >= 0 {
		if end := strings.Index(current[start:], pgExtEnd); end >= 0 {
			hasBlock = true
			existing = current[start : start+end+len(pgExtEnd)]
		}
	}

	// Compare the entire managed block: any preload or DocumentDB GUC change
	// is postmaster-level and needs a restart.
	if hasBlock && existing == newBlockTrimmed {
		return false, nil
	}
	if hasSettings || extBlockHasSettings(existing) {
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
