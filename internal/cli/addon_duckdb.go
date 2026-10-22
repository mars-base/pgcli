package cli

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/hostbin"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// DuckDB — embedded OLAP database installed as a HOST BINARY
// ---------------------------------------------------------------------------
//
// DuckDB is a C++ columnar OLAP engine ("SQLite for analytics"). It queries
// Parquet/CSV/JSON files directly and supports extensions (httpfs, parquet,
// json, etc.). This addon downloads the CLI binary from GitHub releases,
// installs common extensions, and persists the config entry.
//
// Unlike juicefs/zerofs (FUSE filesystems), DuckDB is a standalone CLI tool —
// no mount, no daemon, no S3 backend. It is purely an analytics binary.

// duckdbFetch downloads url to a file at dest, verifying SHA256 when set.
// This is the same pattern as hostbin.fetch but for zip assets (hostbin.Ensure
// only supports tar.gz).
func duckdbFetch(url, dest, wantSHA string) error {
	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("creating %s: %w", dest, err)
	}
	defer f.Close()

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, addr)
			},
		},
		Timeout: 300 * time.Second,
	}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: HTTP %s", url, resp.Status)
	}

	h := sha256.New()
	w := io.MultiWriter(f, h)
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("writing %s: %w", dest, err)
	}

	if wantSHA != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(got, wantSHA) {
			return fmt.Errorf("checksum mismatch for %s\n  got  %s\n  want %s", url, got, wantSHA)
		}
	}
	return nil
}

// duckdbExtractZip extracts the duckdb binary from a zip archive into dest.
func duckdbExtractZip(zipPath, dest string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("opening zip: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		if filepath.Base(f.Name) != "duckdb" || f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("opening zip entry %s: %w", f.Name, err)
		}
		defer rc.Close()

		out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
		if err != nil {
			return fmt.Errorf("creating %s: %w", dest, err)
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			return fmt.Errorf("extracting %s: %w", f.Name, err)
		}
		return out.Close()
	}
	return fmt.Errorf("duckdb binary not found in %s", zipPath)
}

// ensureDuckdbBinary locates or downloads the DuckDB CLI binary.
// Returns the binary path plus a user-facing note (empty unless the installer
// had to fall back off /usr/local/bin).
func ensureDuckdbBinary(version, installDir string) (string, string, error) {
	if runtime.GOOS != "linux" {
		return "", "", fmt.Errorf("duckdb addon supports Linux only")
	}
	if runtime.GOARCH != "amd64" {
		return "", "", fmt.Errorf("duckdb addon supports amd64 only (got %s)", runtime.GOARCH)
	}

	// Detect existing binary on PATH
	if p, ok := hostbin.Detect("duckdb"); ok {
		return p, "", nil
	}

	dir, note, err := hostbin.InstallDir(installDir)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", fmt.Errorf("creating install dir %s: %w", dir, err)
	}

	v := strings.TrimPrefix(version, "v")
	sha := config.DuckdbSHA256("amd64")
	url := fmt.Sprintf("https://github.com/duckdb/duckdb/releases/download/v%s/duckdb_cli-linux-amd64.zip", v)

	// Download to temp file
	tmpDir, err := os.MkdirTemp("", "duckdb-install-")
	if err != nil {
		return "", "", fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	zipPath := filepath.Join(tmpDir, "duckdb.zip")
	fmt.Printf("-> Downloading duckdb %s (linux/amd64)...\n", version)
	if err := duckdbFetch(url, zipPath, sha); err != nil {
		return "", "", err
	}

	// Extract to temp name, then atomic rename
	exPath := filepath.Join(dir, ".duckdb.extracting")
	defer os.Remove(exPath)
	if err := duckdbExtractZip(zipPath, exPath); err != nil {
		return "", "", err
	}

	final := filepath.Join(dir, "duckdb")
	if err := os.Rename(exPath, final); err != nil {
		return "", "", fmt.Errorf("installing %s: %w", final, err)
	}
	return final, note, nil
}

// installDuckdbExtensions installs common extensions via `INSTALL 'ext'`.
// The current process's HTTP(S) proxy environment is forwarded to the duckdb
// subprocess so that extensions can be downloaded behind a proxy.
func installDuckdbExtensions(bin string, extensions []string) error {
	for _, ext := range extensions {
		cmd := exec.Command(bin, "-c", fmt.Sprintf("INSTALL '%s'", ext))
		// Forward proxy settings — DuckDB reads http_proxy / https_proxy
		// (lowercase) as well as the uppercase variants.
		cmd.Env = append(os.Environ(),
			fmt.Sprintf("http_proxy=%s", proxyURL("HTTP_PROXY")),
			fmt.Sprintf("https_proxy=%s", proxyURL("HTTPS_PROXY")),
		)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("installing extension %s: %w", ext, err)
		}
	}
	return nil
}

// proxyURL returns the proxy URL for the given env var name, checking both
// uppercase and lowercase variants.
func proxyURL(upperKey string) string {
	if v := os.Getenv(upperKey); v != "" {
		return v
	}
	return os.Getenv(strings.ToLower(upperKey))
}

// extractDuckdbVersion parses "v1.5.6 ..." from `duckdb --version` output.
func extractDuckdbVersion(output string) string {
	s := strings.TrimSpace(output)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, " \t\n-"); i > 0 {
		s = s[:i]
	}
	return s
}

// runAddonInstallDuckdb implements `pg addon install duckdb`.
func runAddonInstallDuckdb(cmd *cobra.Command) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("the duckdb add-on is a host binary and is Linux-only")
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "duckdb"
	}
	fmt.Printf("=== pg addon install duckdb (%s) ===\n", name)

	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s -- run \"pg config init\" first", path)
	}
	c, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if c.Addons.Duckdb == nil {
		c.Addons.Duckdb = make(map[string]config.DuckdbConfig)
	}
	dc, present := c.Addons.Duckdb[name]
	if present {
		force, _ := cmd.Flags().GetBool("force")
		if !force {
			fmt.Printf("✓ duckdb %q already installed (re-install with --force to rebuild the entry from flags)\n", name)
			return nil
		}
		dc = config.DuckdbConfig{}
	}
	dc.Name = name

	fs := cmd.Flags()
	if fs.Changed("version") {
		dc.Version, _ = fs.GetString("version")
	}
	if dc.Version == "" {
		dc.Version = config.DefaultDuckdbVersion
	}
	if v, _ := fs.GetString("data-dir"); v != "" {
		dc.DataDir = v
	}

	// Apply defaults
	c.Addons.Duckdb[name] = dc
	c.ApplyDefaults()
	dc = c.Addons.Duckdb[name]

	installDir, _ := fs.GetString("install-dir")
	noExtensions, _ := fs.GetBool("no-extensions")

	// --- 1. binary ---------------------------------------------------------
	bin, note, err := ensureDuckdbBinary(dc.Version, installDir)
	if err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	dc.BinaryPath = bin
	fmt.Printf("  [OK] duckdb binary: %s\n", bin)
	if note != "" {
		fmt.Printf("  [!] %s\n", note)
	}

	// --- 2. data dir -------------------------------------------------------
	if err := os.MkdirAll(dc.DataDir, 0755); err != nil {
		return fmt.Errorf("  [X] creating data dir %s: %w", dc.DataDir, err)
	}
	fmt.Printf("  [OK] data dir: %s\n", dc.DataDir)

	// --- 3. extensions -----------------------------------------------------
	// Only httpfs needs a network download; parquet and json are statically
	// linked into the DuckDB CLI binary (install_mode = STATICALLY_LINKED).
	extensions := []string{"httpfs"}
	if !noExtensions {
		if err := installDuckdbExtensions(bin, extensions); err != nil {
			return fmt.Errorf("  [X] %v", err)
		}
		fmt.Printf("  [OK] extensions: %s installed (parquet, json are bundled)\n", strings.Join(extensions, ", "))
	}

	// --- 4. verify ---------------------------------------------------------
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return fmt.Errorf("  [X] verification failed: %w", err)
	}
	ver := extractDuckdbVersion(string(out))
	fmt.Printf("  [OK] version: v%s\n", ver)

	// --- 5. persist config -------------------------------------------------
	c.Addons.Duckdb[name] = dc
	if err := c.Save(path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	fmt.Println()
	fmt.Printf("✓ DuckDB installed\n")
	fmt.Printf("  Version:  v%s\n", ver)
	fmt.Printf("  Binary:   %s\n", bin)
	fmt.Printf("  Data dir: %s\n", dc.DataDir)
	fmt.Println()
	fmt.Printf("  Try: duckdb\n")
	fmt.Printf("  Try: duckdb %s/analytics.duckdb\n", dc.DataDir)
	fmt.Printf("  Try: duckdb -c \"SELECT * FROM read_parquet('s3://bucket/data.parquet')\"\n")
	fmt.Println()
	fmt.Printf("  Manage: pg addon duckdb status --name %s\n", name)
	fmt.Printf("  Shell:  pg addon duckdb shell --name %s\n", name)
	fmt.Printf("  Remove: pg addon remove duckdb --name %s\n", name)
	return nil
}

// runAddonRemoveDuckdb tears down the config entry and optionally cleans data.
func runAddonRemoveDuckdb(cmd *cobra.Command) error {
	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "duckdb"
	}
	cleanData, _ := cmd.Flags().GetBool("clean-data")
	if _, ok := c.Addons.Duckdb[name]; !ok {
		return fmt.Errorf("duckdb %q not found (see 'pg addon list')", name)
	}
	c.ApplyDefaults()
	dc := c.Addons.Duckdb[name]
	fmt.Printf("=== pg addon remove duckdb (%s) ===\n", name)

	delete(c.Addons.Duckdb, name)
	if len(c.Addons.Duckdb) == 0 {
		c.Addons.Duckdb = nil
	}
	if err := c.Save(path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Printf("  [OK] config entry removed (%s)\n", path)

	if cleanData && dc.DataDir != "" {
		if err := os.RemoveAll(dc.DataDir); err != nil {
			return fmt.Errorf("cleaning %s: %w", dc.DataDir, err)
		}
		fmt.Printf("  [OK] cleaned %s (database files)\n", dc.DataDir)
	} else if dc.DataDir != "" {
		fmt.Printf("  [i] kept %s (database files) — pass --clean-data to drop\n", dc.DataDir)
	}
	if dc.BinaryPath != "" {
		fmt.Printf("  [i] kept %s (binary) — shared across instances\n", dc.BinaryPath)
	}

	fmt.Printf("✓ DuckDB %q removed\n", name)
	return nil
}

// --- nested `pg addon duckdb` tree -----------------------------------------

func runAddonDuckdbStatus(cmd *cobra.Command) error {
	c, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "duckdb"
	}
	c.ApplyDefaults()
	dc, ok := c.Addons.Duckdb[name]
	if !ok {
		return fmt.Errorf("duckdb %q not found (see 'pg addon list')", name)
	}

	state := "not found"
	if _, err := os.Stat(dc.BinaryPath); err == nil {
		out, err := exec.Command(dc.BinaryPath, "--version").Output()
		if err == nil {
			state = fmt.Sprintf("v%s", extractDuckdbVersion(string(out)))
		} else {
			state = "error (cannot execute binary)"
		}
	}
	fmt.Printf("duckdb %q — %s\n", name, state)
	fmt.Printf("  Binary:    %s\n", dc.BinaryPath)
	fmt.Printf("  Version:   %s (configured)\n", dc.Version)
	fmt.Printf("  Data dir:  %s\n", dc.DataDir)
	return nil
}

func runAddonDuckdbShell(cmd *cobra.Command, args []string) error {
	c, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "duckdb"
	}
	c.ApplyDefaults()
	dc, ok := c.Addons.Duckdb[name]
	if !ok {
		return fmt.Errorf("duckdb %q not found (see 'pg addon list')", name)
	}
	if _, err := os.Stat(dc.BinaryPath); err != nil {
		return fmt.Errorf("duckdb binary not found at %s — run 'pg addon install duckdb'", dc.BinaryPath)
	}

	// Launch duckdb with any user-supplied args
	shellCmd := exec.Command(dc.BinaryPath, args...)
	shellCmd.Stdin = os.Stdin
	shellCmd.Stdout = os.Stdout
	shellCmd.Stderr = os.Stderr
	return shellCmd.Run()
}

var addonDuckdbCmd = &cobra.Command{
	Use:   "duckdb",
	Short: "Manage DuckDB host-installed CLI",
	Long: `Manage DuckDB add-on instances: an embedded OLAP database for analytical queries.

Subcommands:
  status    Show binary version, path and data directory
  shell     Launch an interactive DuckDB shell (args passed to duckdb)

Examples:
  pg addon install duckdb
  pg addon install duckdb --version 1.5.6
  pg addon install duckdb --version 2.0.0
  pg addon duckdb status --name duckdb
  pg addon duckdb shell -- --help
  pg addon remove duckdb --clean-data`,
}

var addonDuckdbStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show DuckDB installation status",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonDuckdbStatus(cmd) },
}

var addonDuckdbShellCmd = &cobra.Command{
	Use:   "shell [-- duckdb args...]",
	Short: "Launch interactive DuckDB shell",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonDuckdbShell(cmd, args)
	},
}

func init() {
	addonCmd.AddCommand(addonDuckdbCmd)
	addonDuckdbCmd.AddCommand(addonDuckdbStatusCmd, addonDuckdbShellCmd)
	for _, c := range []*cobra.Command{addonDuckdbStatusCmd, addonDuckdbShellCmd} {
		c.Flags().String("name", "", "duckdb instance name (default \"duckdb\")")
	}

	// DuckDB-specific install flags (--data-dir is already registered in addon.go for all addons)
	addonInstallCmd.Flags().Bool("no-extensions", false, "duckdb: skip installing common extensions (httpfs, parquet, json)")
}
