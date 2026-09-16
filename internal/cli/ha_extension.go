package cli

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

var haExtensionCmd = &cobra.Command{
	Use:   "extension",
	Short: "Manage PostgreSQL extensions in Patroni HA clusters",
	Long: `Manage PostgreSQL extensions in Patroni-managed HA clusters.

Unlike single-node instances (where pgcli edits postgresql.conf directly), Patroni
regenerates postgresql.conf from the DCS every cycle. Therefore:
  - shared_preload_libraries must be set via ` + "`patronictl edit-config`" + ` (DCS), not file edit
  - Container recreation (for new image packages) must be coordinated with pause/resume
  - CREATE EXTENSION runs on the leader (which may be on a remote host)

The install flow is:
  1. Build -ext image with Pigsty packages
  2. Pause cluster (disable auto-failover)
  3. Recreate each member container from new image (one at a time, wait rejoin)
  4. Resume cluster (re-enable failover)
  5. patronictl edit-config to set shared_preload_libraries (triggers rolling restart)
  6. CREATE EXTENSION on leader

Commands:
  pg ha extension install <scope> <ext>[,<ext>...]  install extensions (builds image, recreates, edit-config)
  pg ha extension remove <scope> <ext>[,<ext>...]   remove extensions (DROP EXTENSION + edit-config)
  pg ha extension list <scope>                      list installed extensions (from DCS + leader query)
  pg ha extension apply <scope>                     (advanced) manually trigger edit-config + CREATE EXTENSION`,
}

var haExtensionInstallCmd = &cobra.Command{
	Use:   "install <scope> <extension>[,<extension>...]",
	Short: "Install extensions in a Patroni cluster",
	Long: `Install PostgreSQL extensions in a Patroni-managed HA cluster.

Builds a derived image with Pigsty packages, recreates all member containers
(coordinated via pause/resume), sets shared_preload_libraries via patronictl
edit-config (triggers Patroni's rolling restart), and runs CREATE EXTENSION
on the leader.

For cross-host clusters: run this command on each host (each recreates only its
local members), then run ` + "`pg ha extension apply <scope>`" + ` once on any host to
trigger the edit-config + CREATE EXTENSION.

Examples:
  pg ha extension install app pg_stat_statements
  pg ha extension install app pg_cron,pg_stat_statements --database mydb
  pg ha extension install app pgvector --auto-restart`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		scope := args[0]
		extNames := splitExtArgs(args[1:])
		database, _ := cmd.Flags().GetString("database")
		autoRestart, _ := cmd.Flags().GetBool("auto-restart")
		return runHAExtensionInstall(scope, extNames, database, autoRestart)
	},
}

var haExtensionRemoveCmd = &cobra.Command{
	Use:   "remove <scope> <extension>[,<extension>...]",
	Short: "Remove extensions from a Patroni cluster",
	Long: `Remove PostgreSQL extensions from a Patroni-managed HA cluster.

Runs DROP EXTENSION on the leader, then updates shared_preload_libraries via
patronictl edit-config (triggers rolling restart). Does NOT rebuild the image
or recreate containers (the -ext image only grows; disk reclamation is rare).

Examples:
  pg ha extension remove app pg_cron
  pg ha extension remove app pg_stat_statements,pg_cron --auto-restart`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		scope := args[0]
		extNames := splitExtArgs(args[1:])
		database, _ := cmd.Flags().GetString("database")
		autoRestart, _ := cmd.Flags().GetBool("auto-restart")
		return runHAExtensionRemove(scope, extNames, database, autoRestart)
	},
}

var haExtensionListCmd = &cobra.Command{
	Use:   "list <scope>",
	Short: "List installed extensions in a Patroni cluster",
	Long: `List PostgreSQL extensions installed in a Patroni-managed HA cluster.

Shows the cluster-wide extension list from pg.yaml (config.Extensions), the
shared_preload_libraries from DCS (patronictl show-config), and the actual
extensions present in pg_extension on the leader.

Example:
  pg ha extension list app`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		scope := args[0]
		return runHAExtensionList(scope)
	},
}

var haExtensionApplyCmd = &cobra.Command{
	Use:   "apply <scope>",
	Short: "Manually trigger edit-config + CREATE EXTENSION (cross-host second step)",
	Long: `Manually trigger the DCS edit-config and CREATE EXTENSION steps after all
hosts have built the -ext image and recreated their local members.

Used in cross-host clusters: run ` + "`pg ha extension install`" + ` on each host first
(each builds the image and recreates its own members), then run this command
once on any host to resume the cluster, set shared_preload_libraries via
edit-config (triggers Patroni's rolling restart), and run CREATE EXTENSION
on the leader.

Example:
  pg ha extension apply app`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		scope := args[0]
		database, _ := cmd.Flags().GetString("database")
		autoRestart, _ := cmd.Flags().GetBool("auto-restart")
		return runHAExtensionApply(scope, database, autoRestart)
	},
}

func init() {
	haExtensionCmd.AddCommand(haExtensionInstallCmd, haExtensionRemoveCmd, haExtensionListCmd, haExtensionApplyCmd)

	// Shared flags
	for _, cmd := range []*cobra.Command{haExtensionInstallCmd, haExtensionRemoveCmd, haExtensionApplyCmd} {
		cmd.Flags().String("database", "postgres", "target database for CREATE EXTENSION (Patroni config has no 'database' concept)")
		cmd.Flags().Bool("auto-restart", false, "skip confirmation prompt for edit-config rolling restart")
	}
}

// splitExtArgs expands comma-separated extension names so
// "pg ha extension install app pg_cron,pg_stat_statements" works.
func splitExtArgs(args []string) []string {
	var out []string
	for _, a := range args {
		for _, s := range strings.Split(a, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// mergeWithDCSPreload reads the DCS's current shared_preload_libraries and
// merges them with the local extension list. This prevents cross-host install
// from overwriting extensions installed by other hosts — each host's pg.yaml
// only lists its own extensions, but DCS holds the cluster-wide union.
func mergeWithDCSPreload(pm *podman.PatroniManager, cluster *config.PatroniClusterConfig, nsScope string, localExts []string) []string {
	out, err := pm.PatronictlCapture(cluster, "show-config", nsScope)
	if err != nil {
		return localExts // can't read DCS, fall back to local only
	}

	// Find shared_preload_libraries line in show-config output
	idx := strings.Index(out, "shared_preload_libraries")
	if idx < 0 {
		return localExts // nothing in DCS yet
	}
	lineEnd := strings.Index(out[idx:], "\n")
	if lineEnd < 0 {
		lineEnd = len(out) - idx
	}
	line := out[idx : idx+lineEnd]

	// Parse "shared_preload_libraries: pg_cron,pg_stat_statements"
	// or "shared_preload_libraries: 'pg_cron,pg_stat_statements'"
	var dcsCSV string
	if colon := strings.Index(line, ":"); colon >= 0 {
		dcsCSV = strings.TrimSpace(line[colon+1:])
		dcsCSV = strings.Trim(dcsCSV, "'\"")
	}
	if dcsCSV == "" {
		return localExts
	}

	// Merge: union of local + DCS extensions (dedupe)
	seen := make(map[string]bool)
	for _, e := range localExts {
		seen[strings.TrimSpace(e)] = true
	}
	for _, e := range strings.Split(dcsCSV, ",") {
		e = strings.TrimSpace(e)
		if e != "" {
			// DCS stores resolved SQL names (e.g. "vector"); map back to user
			// names where possible so BuildPreloadCSV can re-resolve them.
			userName := dcsToUserName(e)
			if !seen[userName] {
				seen[userName] = true
			}
		}
	}
	merged := make([]string, 0, len(seen))
	for e := range seen {
		merged = append(merged, e)
	}
	slices.Sort(merged)
	return merged
}

// dcsToUserName maps a resolved SQL extension name (from DCS shared_preload_libraries)
// back to the user-facing name. For most extensions, SQL name == user name.
// The exception is pgvector: user types "pgvector", SQL name is "vector".
func dcsToUserName(sqlName string) string {
	if sqlName == "vector" {
		return "pgvector"
	}
	return sqlName
}

// replicasFirstOrder returns local member names sorted so that replicas are
// recreated before the leader. This minimizes unnecessary failovers during
// extension install — if the leader is recreated first, Patroni promotes a
// replica, and then we'd recreate the new leader again on the next host.
func replicasFirstOrder(pm *podman.PatroniManager, cluster *config.PatroniClusterConfig, nsScope string) []string {
	// Query current roles from patronictl list
	leader := ""
	if out, err := pm.PatronictlCapture(cluster, "list", nsScope, "-f", "json"); err == nil {
		leader = podman.PatroniLeaderFromListJSON(out)
	}

	var replicas []string
	var leaders []string
	for member, mb := range cluster.Members {
		if mb.RemoteHost != "" {
			continue // remote members are recreated on their own host
		}
		if member == leader {
			leaders = append(leaders, member)
		} else {
			replicas = append(replicas, member)
		}
	}
	// Stable order: replicas first, then leader
	slices.Sort(replicas)
	return append(replicas, leaders...)
}

// totalClusterMemberCount queries DCS (patronictl list) for the total number
// of cluster members. Returns 0 if DCS is unreachable. Used to distinguish
// single-host (all members local) from cross-host clusters.
func totalClusterMemberCount(pm *podman.PatroniManager, cluster *config.PatroniClusterConfig, nsScope string) int {
	out, err := pm.PatronictlCapture(cluster, "list", nsScope, "-f", "json")
	if err != nil {
		return 0
	}
	return strings.Count(out, `"Member":`)
}

// pauseCluster pauses the Patroni cluster, tolerating "already paused" (idempotent).
// In cross-host flows, multiple hosts may each call pause — the second host must
// not fail because the first already paused it.
func pauseCluster(pm *podman.PatroniManager, cluster *config.PatroniClusterConfig, nsScope string) error {
	if _, err := pm.PatronictlCapture(cluster, "pause", nsScope, "--wait"); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "already paused") || strings.Contains(msg, "is already paused") {
			fmt.Println("  (cluster is already paused — continuing)")
			return nil
		}
		return fmt.Errorf("pausing cluster: %w", err)
	}
	return nil
}

func runHAExtensionInstall(scope string, extNames []string, database string, autoRestart bool) error {
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
	cfgPath = path

	cluster, ok := cfg.Addons.Patroni[scope]
	if !ok {
		return fmt.Errorf("Patroni cluster %q not found in config", scope)
	}

	pm, err := podman.NewPatroniManager(cfg)
	if err != nil {
		return err
	}

	// Validate all extensions
	var unknown []string
	for _, name := range extNames {
		if !podman.IsExtensionKnown(name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		fmt.Printf("  [X] Unknown extension(s): %v\n", unknown)
		fmt.Println()
		fmt.Println("      These extensions are not in the Pigsty catalog or builtin contrib list.")
		fmt.Println("      Check available extensions: pg extension available")
		fmt.Println("      Full Pigsty catalog: https://pigsty.cc/ext/list/")
		return fmt.Errorf("unknown extensions: %v", unknown)
	}

	// Merge with existing extensions (dedupe)
	existingMap := make(map[string]bool)
	for _, e := range cluster.Extensions {
		existingMap[e] = true
	}
	var toInstall []string
	for _, name := range extNames {
		if !existingMap[name] {
			toInstall = append(toInstall, name)
			existingMap[name] = true
		}
	}

	// Merge with DCS's current shared_preload_libraries so the image build
	// includes ALL extension packages (not just this host's), and pg.yaml
	// stays consistent across hosts.
	nsScope := cfg.PatroniScope(scope)
	dcsMerged := mergeWithDCSPreload(pm, &cluster, nsScope, func() []string {
		out := make([]string, 0, len(existingMap))
		for e := range existingMap {
			out = append(out, e)
		}
		return out
	}())
	// Rebuild existingMap from the merged list
	existingMap = make(map[string]bool)
	for _, e := range dcsMerged {
		existingMap[e] = true
	}

	// Check if there's anything new to install
	if len(toInstall) == 0 {
		fmt.Println("All requested extensions already installed")
		// Even if nothing new, save merged list if DCS had extras
		if !slices.Equal(dcsMerged, cluster.Extensions) {
			cluster.Extensions = dcsMerged
			cfg.Addons.Patroni[scope] = cluster
			if err := cfg.Save(path); err != nil {
				fmt.Printf("  [!] Warning: could not save merged extensions to pg.yaml: %v\n", err)
			} else {
				fmt.Printf("  -> Synced local pg.yaml extensions from DCS: %v\n", dcsMerged)
			}
		}
		return nil
	}
	allExts := make([]string, 0, len(existingMap))
	for e := range existingMap {
		allExts = append(allExts, e)
	}
	slices.Sort(allExts)

	// Check if any are non-builtin (need image build + container recreate)
	hasNonBuiltin := podman.HasNonBuiltinExtensions(allExts)

	// Pause cluster (global, idempotent).
	// In cross-host flows, the first host pauses; subsequent hosts find it
	// already paused and continue. The cluster stays paused until the final
	// `pg ha extension apply` resumes it.
	fmt.Printf("-> Pausing cluster %q...\n", scope)
	if err := pauseCluster(pm, &cluster, nsScope); err != nil {
		return err
	}

	if hasNonBuiltin {
		// Build image
		fmt.Printf("-> Building extension image for cluster %q...\n", scope)
		// Pick any local member's base image tag (remote members carry none).
		baseTag := ""
		for _, mb := range cluster.Members {
			if mb.RemoteHost != "" {
				continue
			}
			baseTag = podman.BaseImageTag(mb.ImageTag)
			break
		}
		if baseTag == "" {
			baseTag = podman.DefaultPatroniImageTag
		}
		newTag, err := pm.BuildExtensionImage(baseTag, allExts, cfg.Pigsty.Repo)
		if err != nil {
			return fmt.Errorf("building extension image: %w", err)
		}

		// Determine recreate order: replicas first, leader last.
		// This avoids unnecessary failover — if we recreate the leader first,
		// Patroni promotes a replica, and then we'd recreate the new leader
		// again when we get to that host.
		recreateOrder := replicasFirstOrder(pm, &cluster, nsScope)

		fmt.Printf("-> Recreating %d local member(s) from new image...\n", len(recreateOrder))
		for _, member := range recreateOrder {
			fmt.Printf("  -> Recreating member %s...\n", member)
			if err := pm.RecreateMemberWithImage(&cluster, member, newTag); err != nil {
				return fmt.Errorf("recreating member %s: %w", member, err)
			}
			// Wait for member to rejoin (poll patronictl list)
			fmt.Printf("  -> Waiting for member %s to rejoin...\n", member)
			for i := 0; i < 24; i++ { // 2min timeout
				time.Sleep(5 * time.Second)
				if out, err := pm.PatronictlCapture(&cluster, "list", nsScope, "-f", "json"); err == nil {
					if strings.Contains(out, fmt.Sprintf(`"Member": "%s"`, member)) {
						fmt.Printf("  [OK] Member %s rejoined\n", member)
						break
					}
				}
			}
		}

		// Update config with new image tag + extensions
		for member, mb := range cluster.Members {
			mb.ImageTag = newTag
			cluster.Members[member] = mb
		}
	}

	// Update extensions list and save config
	cluster.Extensions = allExts
	cfg.Addons.Patroni[scope] = cluster
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	// Detect single-host vs cross-host by comparing local member count with
	// DCS total member count. cluster.Members may include remote member
	// declarations (no local container); only local members count here.
	// patronictl list shows ALL members across all hosts.
	localCount := 0
	for _, mb := range cluster.Members {
		if mb.RemoteHost == "" {
			localCount++
		}
	}
	totalMembers := totalClusterMemberCount(pm, &cluster, nsScope)
	singleHost := totalMembers > 0 && localCount >= totalMembers

	if singleHost {
		// Single-host: auto-apply (resume + edit-config + CREATE EXTENSION)
		if err := runHAExtensionApplyWithCluster(pm, cfg, &cluster, scope, database, autoRestart); err != nil {
			return err
		}
		fmt.Printf("✓ Extensions installed in cluster %q: %v\n", scope, toInstall)
	} else {
		// Cross-host: cluster stays paused — other hosts still need to install.
		// The last step (resume + edit-config + CREATE EXTENSION) runs via apply.
		fmt.Println()
		fmt.Printf("✓ Local members recreated on this host. Cluster is paused.\n")
		fmt.Printf("  Next steps:\n")
		fmt.Printf("  1. Run `pg ha extension install %s %s` on each remaining host\n", scope, strings.Join(extNames, ","))
		fmt.Printf("  2. Run `pg ha extension apply %s` once on any host to complete\n", scope)
	}

	return nil
}

func runHAExtensionRemove(scope string, extNames []string, database string, autoRestart bool) error {
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
	cfgPath = path

	cluster, ok := cfg.Addons.Patroni[scope]
	if !ok {
		return fmt.Errorf("Patroni cluster %q not found in config", scope)
	}

	pm, err := podman.NewPatroniManager(cfg)
	if err != nil {
		return err
	}

	// Check which extensions are actually installed
	existingMap := make(map[string]bool)
	for _, e := range cluster.Extensions {
		existingMap[e] = true
	}
	var toRemove []string
	for _, name := range extNames {
		if !existingMap[name] {
			fmt.Printf("  [skip] %s (not installed)\n", name)
			continue
		}
		toRemove = append(toRemove, name)
	}
	if len(toRemove) == 0 {
		fmt.Println("No extensions to remove")
		return nil
	}

	// DROP EXTENSION on leader first (before removing from shared_preload_libraries)
	fmt.Printf("-> Dropping extensions on leader...\n")
	for _, name := range toRemove {
		resolved := podman.ResolveExtName(name)
		sql := fmt.Sprintf("DROP EXTENSION IF EXISTS \"%s\"", resolved)
		fmt.Printf("  -> Running: %s\n", sql)
		if _, err := pm.ExecLeaderQuery(&cluster, database, sql); err != nil {
			fmt.Printf("  [!] Warning: DROP EXTENSION %s: %v\n", name, err)
		}
	}

	// Remove from extensions list
	remainingMap := make(map[string]bool)
	for _, e := range cluster.Extensions {
		remainingMap[e] = true
	}
	for _, name := range toRemove {
		delete(remainingMap, name)
	}
	remaining := make([]string, 0, len(remainingMap))
	for e := range remainingMap {
		remaining = append(remaining, e)
	}
	slices.Sort(remaining)

	// Update config
	cluster.Extensions = remaining
	cfg.Addons.Patroni[scope] = cluster
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	// Update shared_preload_libraries via edit-config (triggers rolling restart)
	csv, hasCron := podman.BuildPreloadCSV(remaining)
	nsScope := cfg.PatroniScope(scope)

	fmt.Printf("-> Updating shared_preload_libraries via patronictl edit-config...\n")
	editArgs := []string{"edit-config", nsScope, "--force"}
	if csv != "" {
		editArgs = append(editArgs, "-s", fmt.Sprintf("postgresql.parameters.shared_preload_libraries=%s", csv))
	} else {
		// Remove shared_preload_libraries entirely
		editArgs = append(editArgs, "-s", "postgresql.parameters.shared_preload_libraries=")
	}
	if hasCron {
		editArgs = append(editArgs, "-s", fmt.Sprintf("postgresql.parameters.cron.database_name=%s", database))
	} else if slices.Contains(toRemove, "pg_cron") {
		// Remove cron.database_name if pg_cron was removed
		editArgs = append(editArgs, "-s", "postgresql.parameters.cron.database_name=")
	}

	if err := pm.Patronictl(&cluster, editArgs...); err != nil {
		return fmt.Errorf("edit-config: %w", err)
	}

	fmt.Printf("✓ Extensions removed from cluster %q: %v\n", scope, toRemove)
	return nil
}

func runHAExtensionList(scope string) error {
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
	cfgPath = path

	cluster, ok := cfg.Addons.Patroni[scope]
	if !ok {
		return fmt.Errorf("Patroni cluster %q not found in config", scope)
	}

	pm, err := podman.NewPatroniManager(cfg)
	if err != nil {
		return err
	}

	fmt.Printf("Cluster %q extensions:\n", scope)
	fmt.Println()

	// Config-level list
	fmt.Printf("  Config (pg.yaml):  %v\n", cluster.Extensions)

	// DCS-level list (shared_preload_libraries from show-config)
	nsScope := cfg.PatroniScope(scope)
	if out, err := pm.PatronictlCapture(&cluster, "show-config", nsScope); err == nil {
		if idx := strings.Index(out, "shared_preload_libraries"); idx >= 0 {
			lineEnd := strings.Index(out[idx:], "\n")
			if lineEnd >= 0 {
				spl := strings.TrimSpace(out[idx : idx+lineEnd])
				fmt.Printf("  DCS (preload):     %s\n", spl)
			}
		}
	}

	// Leader query (actual pg_extension list)
	sql := "SELECT extname FROM pg_extension WHERE extname != 'plpgsql' ORDER BY extname"
	if out, err := pm.ExecLeaderQuery(&cluster, "postgres", sql); err == nil {
		var exts []string
		for line := range strings.SplitSeq(out, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				exts = append(exts, line)
			}
		}
		fmt.Printf("  Leader (installed): %v\n", exts)
	} else {
		fmt.Printf("  Leader (installed): (query failed: %v)\n", err)
	}

	return nil
}

func runHAExtensionApply(scope string, database string, autoRestart bool) error {
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
	cfgPath = path

	cluster, ok := cfg.Addons.Patroni[scope]
	if !ok {
		return fmt.Errorf("Patroni cluster %q not found in config", scope)
	}

	pm, err := podman.NewPatroniManager(cfg)
	if err != nil {
		return err
	}

	if err := runHAExtensionApplyWithCluster(pm, cfg, &cluster, scope, database, autoRestart); err != nil {
		return err
	}
	fmt.Printf("✓ Extensions applied to cluster %q\n", scope)
	return nil
}

// runHAExtensionApplyWithCluster is the shared apply logic: resume (idempotent),
// edit-config (triggers rolling restart), CREATE EXTENSION on leader.
// The cluster is expected to be paused when this is called (either from single-host
// install which pauses globally, or from cross-host where the last host applies).
func runHAExtensionApplyWithCluster(pm *podman.PatroniManager, cfg *config.Config, cluster *config.PatroniClusterConfig, scope, database string, autoRestart bool) error {
	nsScope := cfg.PatroniScope(scope)

	// Resume cluster (idempotent).
	// After the global-pause refactor, the cluster is always paused when apply
	// runs (either from single-host install or cross-host after all hosts have
	// recreated). Tolerate "not paused" for the standalone `pg ha extension apply`
	// command, which may be run when the cluster was already resumed manually.
	fmt.Printf("-> Resuming cluster %q...\n", scope)
	if _, err := pm.PatronictlCapture(cluster, "resume", nsScope, "--wait"); err != nil {
		if strings.Contains(err.Error(), "not paused") {
			fmt.Println("  (cluster is not paused — skipping resume)")
		} else {
			return fmt.Errorf("resuming cluster: %w", err)
		}
	}
	// Wait a moment for leader election to stabilize
	time.Sleep(3 * time.Second)

	// Build shared_preload_libraries CSV.
	// In cross-host clusters, each host's pg.yaml only lists its own extensions.
	// Read DCS's current shared_preload_libraries and merge so we don't drop
	// extensions installed by other hosts.
	mergedExts := mergeWithDCSPreload(pm, cluster, nsScope, cluster.Extensions)

	// Sync merged list back to local pg.yaml so all hosts stay consistent.
	if !slices.Equal(mergedExts, cluster.Extensions) {
		cluster.Extensions = mergedExts
		cfg.Addons.Patroni[scope] = *cluster
		if err := cfg.Save(cfgPath); err != nil {
			fmt.Printf("  [!] Warning: could not save merged extensions to pg.yaml: %v\n", err)
		} else {
			fmt.Printf("  -> Synced local pg.yaml extensions: %v\n", mergedExts)
		}
	}

	csv, hasCron := podman.BuildPreloadCSV(mergedExts)

	// Prompt for rolling restart confirmation
	if csv != "" || hasCron {
		fmt.Printf("\nApplying extensions will update shared_preload_libraries via patronictl edit-config,\n")
		fmt.Printf("which triggers a rolling restart (replicas first, then leader switchover).\n")
		if csv != "" {
			fmt.Printf("  shared_preload_libraries = %s\n", csv)
		}
		if hasCron {
			fmt.Printf("  cron.database_name = %s\n", database)
		}
		fmt.Println()
		if !autoRestart && !confirmPrompt("Proceed with rolling restart? [y/N]: ") {
			fmt.Println("Restart skipped. Run `pg ha extension apply` later to apply.")
			return nil
		}
	}

	// edit-config (triggers Patroni's rolling restart)
	fmt.Printf("-> Updating DCS via patronictl edit-config...\n")
	editArgs := []string{"edit-config", nsScope, "--force"}
	if csv != "" {
		editArgs = append(editArgs, "-s", fmt.Sprintf("postgresql.parameters.shared_preload_libraries=%s", csv))
	}
	if hasCron {
		editArgs = append(editArgs, "-s", fmt.Sprintf("postgresql.parameters.cron.database_name=%s", database))
	}

	if err := pm.Patronictl(cluster, editArgs...); err != nil {
		return fmt.Errorf("edit-config: %w", err)
	}

	// Wait for rolling restart to complete. After edit-config changes
	// shared_preload_libraries, Patroni marks each member with "pending_restart"
	// but does NOT automatically restart them — an explicit
	// `patronictl restart` is required to trigger the rolling restart.
	fmt.Println("-> Waiting for rolling restart to complete...")
	restartTriggered := false
	for i := 0; i < 60; i++ { // 5min timeout
		time.Sleep(5 * time.Second)
		out, err := pm.PatronictlCapture(cluster, "list", nsScope, "-f", "json")
		if err != nil {
			continue
		}
		hasPending := strings.Contains(out, `"Pending restart"`)
		if hasPending && !restartTriggered {
			// Patroni only marks members as pending; it won't restart them
			// automatically for shared_preload_libraries changes.
			// Trigger the rolling restart explicitly.
			fmt.Println("  -> Triggering rolling restart (patronictl restart)...")
			if rerr := pm.Patronictl(cluster, "restart", nsScope, "--force"); rerr != nil {
				fmt.Printf("  [!] Warning: patronictl restart: %v\n", rerr)
			}
			restartTriggered = true
			continue
		}
		// Check all members are running/streaming with no pending restart
		if !hasPending {
			if (strings.Contains(out, `"State": "running"`) || strings.Contains(out, `"State": "streaming"`)) {
				leader := podman.PatroniLeaderFromListJSON(out)
				if leader != "" {
					fmt.Printf("  [OK] Cluster healthy, leader: %s\n", leader)
					break
				}
			}
		}
	}

	// CREATE EXTENSION on leader
	fmt.Printf("-> Running CREATE EXTENSION on leader...\n")
	for _, name := range cluster.Extensions {
		resolved := podman.ResolveExtName(name)
		sql := fmt.Sprintf("CREATE EXTENSION IF NOT EXISTS \"%s\"", resolved)
		fmt.Printf("  -> Running: %s\n", sql)
		if out, err := pm.ExecLeaderQuery(cluster, database, sql); err != nil {
			fmt.Printf("  [!] Warning: CREATE EXTENSION %s: %v\n", name, err)
			if out != "" {
				fmt.Printf("  Output: %s\n", out)
			}
		}
	}

	return nil
}
