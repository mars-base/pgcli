package cli

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

var extensionCmd = &cobra.Command{
	Use:   "extension",
	Short: "Manage PostgreSQL extensions",
	Long: `Manage PostgreSQL extensions (install, list, remove, available).

Extensions are installed from the Pigsty DEB repository (https://pigsty.cc/ext/),
which provides 576+ extensions for PostgreSQL 18.

Extensions are baked into a derived container image. When you install or remove
an extension, pgcli builds a new image layer on top of the base image, stops the
container, and recreates it from the new image. Data volumes on the host are
preserved across rebuilds.

Commands:
  pg extension install <ext> [ext...]     install extensions (builds new image)
  pg extension list                       list installed extensions
  pg extension remove <ext> [ext...]      remove extensions (rebuilds image)
  pg extension available                  list available extensions in catalog`,
}

var extensionInstallCmd = &cobra.Command{
	Use:   "install <extension> [extension...]",
	Short: "Install one or more PostgreSQL extensions",
	Long: `Install PostgreSQL extensions from the Pigsty DEB repository.

A new container image is built on top of the current base image with the
extension packages baked in. The container is then replaced (data preserved).

For extensions requiring shared_preload_libraries (e.g., pg_stat_statements,
pg_cron), PostgreSQL will be restarted to load the shared library.

Examples:
  pg extension install pg_stat_statements
  pg extension install pgmq uuid-ossp pg_stat_statements`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runExtensionInstall(args)
	},
}

var extensionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed extensions",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runExtensionList()
	},
}

var extensionRemoveCmd = &cobra.Command{
	Use:   "remove <extension> [extension...]",
	Short: "Remove one or more PostgreSQL extensions",
	Long: `Remove PostgreSQL extensions (DROP EXTENSION + rebuild image without them).

The container is replaced with a new image that excludes the removed extensions.
Data volumes on the host are preserved.

Examples:
  pg extension remove pgmq
  pg extension remove pg_stat_statements pg_cron`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runExtensionRemove(args)
	},
}

var extensionAvailableCmd = &cobra.Command{
	Use:   "available",
	Short: "List available extensions in the pgcli catalog",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runExtensionAvailable()
	},
}

func init() {
	rootCmd.AddCommand(extensionCmd)
	extensionCmd.AddCommand(extensionInstallCmd, extensionListCmd, extensionRemoveCmd, extensionAvailableCmd)
}

func runExtensionInstall(extNames []string) error {
	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s -- run \"pg config init\" first", path)
	}

	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if _, ok := cfg.Instances[cfgInstance]; !ok {
		return fmt.Errorf("instance %q not found in config", cfgInstance)
	}

	if err := cfg.SetInstance(cfgInstance); err != nil {
		return fmt.Errorf("loading instance config: %w", err)
	}

	pm, err := podman.New(cfg)
	if err != nil {
		return fmt.Errorf("podman: %w", err)
	}

	// Check for duplicates
	inst := cfg.Instances[cfgInstance]
	existing := make(map[string]bool)
	for _, e := range inst.Extensions {
		existing[e] = true
	}

	var toInstall []string
	for _, name := range extNames {
		if existing[name] {
			fmt.Printf("  [skip] %s (already installed)\n", name)
			continue
		}
		toInstall = append(toInstall, name)
	}

	if len(toInstall) == 0 {
		fmt.Println("All requested extensions already installed")
		return nil
	}

	// Pre-validate: warn about extensions not in the catalog.
	var unknown []string
	for _, name := range toInstall {
		if ext := podman.GetExtension(name); ext == nil {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		fmt.Printf("  [!] Extension(s) not in catalog: %v\n", unknown)
		fmt.Println("      These will be installed as postgresql-18-<name> from Pigsty/PGDG repos.")
		fmt.Println("      If the package does not exist, the build will fail.")
		fmt.Println("      Run `pg extension available` to see catalog extensions.")
		fmt.Println()
	}

	// Build new extension image with all managed extensions.
	// Install: FROM current ImageTag (may be base or -ext), add new packages.
	// If all extensions are builtin (contrib), BuildExtensionImage returns
	// the current tag unchanged and no image build or container replacement
	// is needed — we only need to configure shared_preload_libraries and
	// run CREATE EXTENSION.
	allExts := append(inst.Extensions, toInstall...)
	oldTag := cfg.Podman.ImageTag
	newTag, err := pm.BuildExtensionImage(cfg.Podman.ImageTag, allExts)
	if err != nil {
		return fmt.Errorf("building extension image: %w", err)
	}

	imageChanged := newTag != oldTag
	if imageChanged {
		// Replace container with new image (stop → rm → run with new image)
		if err := pm.ReplaceContainer(newTag); err != nil {
			return fmt.Errorf("replacing container: %w", err)
		}

		// Wait for PG to be ready
		fmt.Println("-> Waiting for PostgreSQL to be ready...")
		for i := 0; i < 60; i++ {
			if ready, _ := pm.PGIsReady(); ready {
				break
			}
			time.Sleep(time.Second)
		}
	}

	// Update config: extensions list + new image tag
	inst.Extensions = allExts
	if imageChanged {
		inst.Podman.ImageTag = newTag
	}
	cfg.Instances[cfgInstance] = inst
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Apply shared_preload_libraries (may trigger restart for NeedsPreload extensions)
	needsRestart, err := pm.ApplyExtensions(allExts)
	if err != nil {
		return fmt.Errorf("apply extensions: %w", err)
	}

	if needsRestart {
		fmt.Println("-> Restarting PostgreSQL to load shared_preload_libraries...")
		if err := pm.StopContainer(); err != nil {
			return fmt.Errorf("stop container for restart: %w", err)
		}
		if err := pm.StartContainer(); err != nil {
			return fmt.Errorf("start container after restart: %w", err)
		}
		fmt.Println("-> Waiting for PostgreSQL to be ready (after restart)...")
		for i := 0; i < 60; i++ {
			if ready, _ := pm.PGIsReady(); ready {
				break
			}
			time.Sleep(time.Second)
		}
	}

	// CREATE EXTENSION IF NOT EXISTS for newly added extensions
	if err := pm.RunCreateExtensions(toInstall); err != nil {
		return err
	}

	fmt.Printf("✓ Extensions installed: %v\n", toInstall)
	return nil
}

func runExtensionList() error {
	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s", path)
	}

	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if _, ok := cfg.Instances[cfgInstance]; !ok {
		return fmt.Errorf("instance %q not found in config", cfgInstance)
	}

	if err := cfg.SetInstance(cfgInstance); err != nil {
		return fmt.Errorf("loading instance config: %w", err)
	}

	pm, err := podman.New(cfg)
	if err != nil {
		return fmt.Errorf("podman: %w", err)
	}

	if err := pm.CheckContainerRunning(); err != nil {
		return err
	}

	installed, err := pm.GetInstalledExtensions()
	if err != nil {
		return fmt.Errorf("query extensions: %w", err)
	}

	managed := cfg.Instances[cfgInstance].Extensions
	managedSet := make(map[string]bool)
	for _, e := range managed {
		managedSet[e] = true
		// Also map CreateName → managed (e.g., "vector" → managed for pgvector)
		if ext := podman.GetExtension(e); ext != nil && ext.CreateName != "" {
			managedSet[ext.CreateName] = true
		}
	}

	fmt.Printf("Installed extensions in %q:\n", cfgInstance)
	if len(installed) == 0 {
		fmt.Println("  (none)")
		return nil
	}

	for _, name := range installed {
		status := "unmanaged"
		if managedSet[name] {
			status = "managed"
		}
		fmt.Printf("  %s (%s)\n", name, status)
	}

	return nil
}

func runExtensionRemove(extNames []string) error {
	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s", path)
	}

	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if _, ok := cfg.Instances[cfgInstance]; !ok {
		return fmt.Errorf("instance %q not found in config", cfgInstance)
	}

	if err := cfg.SetInstance(cfgInstance); err != nil {
		return fmt.Errorf("loading instance config: %w", err)
	}

	pm, err := podman.New(cfg)
	if err != nil {
		return fmt.Errorf("podman: %w", err)
	}

	if err := pm.CheckContainerRunning(); err != nil {
		return err
	}

	inst := cfg.Instances[cfgInstance]
	removeSet := make(map[string]bool)
	for _, name := range extNames {
		removeSet[name] = true
	}

	// DROP EXTENSION (before removing from image, so we can still access the files)
	for _, name := range extNames {
		sqlName := name
		if ext := podman.GetExtension(name); ext != nil && ext.CreateName != "" {
			sqlName = ext.CreateName
		}
		sql := fmt.Sprintf("DROP EXTENSION IF EXISTS \"%s\"", sqlName)
		fmt.Printf("-> Running: %s\n", sql)
		out, err := pm.Exec("psql", "-U", cfg.Postgres.User, "-d", cfg.Postgres.Database, "-c", sql)
		if err != nil {
			fmt.Printf("  [!] Warning: DROP EXTENSION %s: %v\n", name, err)
			if out != "" {
				fmt.Printf("  Output: %s\n", out)
			}
		}
	}

	// Compute remaining extensions
	var remaining []string
	for _, e := range inst.Extensions {
		if !removeSet[e] {
			remaining = append(remaining, e)
		}
	}

	// Determine new image tag based on remaining extensions.
	// If all remaining are builtin (or none remain), we can use the base image.
	// Otherwise, rebuild with only the non-builtin packages.
	oldTag := cfg.Podman.ImageTag
	baseTag := podman.BaseImageTag(oldTag)

	var newTag string
	if len(remaining) == 0 {
		newTag = baseTag
	} else if podman.HasNonBuiltinExtensions(remaining) {
		var err error
		newTag, err = pm.BuildExtensionImage(baseTag, remaining)
		if err != nil {
			return fmt.Errorf("building extension image: %w", err)
		}
	} else {
		// All remaining are builtin — use base image
		newTag = baseTag
	}

	if newTag != oldTag {
		if err := pm.ReplaceContainer(newTag); err != nil {
			return fmt.Errorf("replacing container: %w", err)
		}

		// Wait for PG to be ready
		fmt.Println("-> Waiting for PostgreSQL to be ready...")
		for i := 0; i < 60; i++ {
			if ready, _ := pm.PGIsReady(); ready {
				break
			}
			time.Sleep(time.Second)
		}
	}

	// Update config
	inst.Extensions = remaining
	if newTag != oldTag {
		inst.Podman.ImageTag = newTag
	}
	cfg.Instances[cfgInstance] = inst
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Update shared_preload_libraries
	needsRestart, err := pm.ApplyExtensions(remaining)
	if err != nil {
		return fmt.Errorf("apply extensions: %w", err)
	}

	if needsRestart {
		fmt.Println("-> Restarting PostgreSQL to unload shared_preload_libraries...")
		if err := pm.StopContainer(); err != nil {
			return fmt.Errorf("stop container for restart: %w", err)
		}
		if err := pm.StartContainer(); err != nil {
			return fmt.Errorf("start container after restart: %w", err)
		}
		fmt.Println("-> Waiting for PostgreSQL to be ready (after restart)...")
		for i := 0; i < 60; i++ {
			if ready, _ := pm.PGIsReady(); ready {
				break
			}
			time.Sleep(time.Second)
		}
	}

	fmt.Printf("✓ Extensions removed: %v\n", extNames)
	return nil
}

func runExtensionAvailable() error {
	fmt.Println("Available extensions in pgcli catalog:")
	fmt.Println()

	// Sort by category
	var needsPreload []podman.Extension
	var noPreload []podman.Extension
	for _, ext := range podman.ExtensionCatalog {
		if ext.NeedsPreload {
			needsPreload = append(needsPreload, ext)
		} else {
			noPreload = append(noPreload, ext)
		}
	}

	sort.Slice(needsPreload, func(i, j int) bool {
		return needsPreload[i].Name < needsPreload[j].Name
	})
	sort.Slice(noPreload, func(i, j int) bool {
		return noPreload[i].Name < noPreload[j].Name
	})

	fmt.Println("Extensions requiring shared_preload_libraries (container restart on install):")
	for _, ext := range needsPreload {
		fmt.Printf("  %s\n", ext.Name)
	}

	fmt.Println()
	fmt.Println("Extensions without preloading (no restart needed):")
	for _, ext := range noPreload {
		fmt.Printf("  %s\n", ext.Name)
	}

	fmt.Println()
	fmt.Println("Extensions not in the catalog can be installed by name:")
	fmt.Println("  pg extension install <name>")
	fmt.Println("  (attempts to install postgresql-18-<name> from Pigsty DEB repo)")
	fmt.Println()
	fmt.Println("Full catalog: https://pigsty.cc/ext/")

	return nil
}
