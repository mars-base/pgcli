package cli

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/autostart"
	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

func init() {
	rootCmd.AddCommand(autostartCmd)
	autostartCmd.AddCommand(autostartEnableCmd)
	autostartCmd.AddCommand(autostartDisableCmd)
	autostartCmd.AddCommand(autostartStatusCmd)
	autostartCmd.AddCommand(autostartListCmd)

	for _, c := range []*cobra.Command{autostartEnableCmd, autostartDisableCmd} {
		c.Flags().Bool("backup", false, "select the shared backup container")
		c.Flags().Bool("pgbouncer", false, "select the PgBouncer addon (use with -i for a local pooler, --pg-name for a remote one)")
		c.Flags().String("pg-name", "", "name of a remote PgBouncer (top-level addons)")
		c.Flags().Bool("etcd", false, "select an etcd member (top-level addons)")
		c.Flags().String("name", "", "name of the etcd member, pgdog proxy, haproxy instance, minio instance, or Patroni member")
		c.Flags().Bool("pgdog", false, "select a PgDog proxy (top-level addons)")
		c.Flags().Bool("ha", false, "select Patroni HA members (top-level addons; requires --scope)")
		c.Flags().Bool("haproxy", false, "select an HAProxy instance (top-level addons)")
		c.Flags().Bool("minio", false, "select a MinIO instance (top-level addons)")
		c.Flags().String("scope", "", "Patroni cluster scope (required only with --ha)")
	}
}

var autostartCmd = &cobra.Command{
	Use:   "autostart",
	Short: "Manage auto-start on host boot",
	Long: `autostart controls which instances and addons are started automatically
after a host reboot, via a systemd user unit (Linux) or a launchd LaunchAgent
(macOS) that runs 'pg start --autostart' at boot/login.

Note: 'pg stop' does NOT affect autostart — autostart is purely config-driven.
Use 'pg autostart disable' to opt out.`,
}

var autostartEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Enable auto-start for an instance, the backup container, a PgBouncer, an etcd member, a PgDog proxy, an HAProxy instance, a MinIO instance, or a Patroni member",
	Long: `Enable auto-start on boot for exactly one target:

  pg autostart enable -i myinst          # instance
  pg autostart enable --backup           # shared backup container
  pg autostart enable --pgbouncer -i myinst      # local PgBouncer for an instance
  pg autostart enable --pgbouncer --pg-name x    # remote PgBouncer
  pg autostart enable --etcd --name m1           # etcd member (default name "etcd")
  pg autostart enable --pgdog --name proxy       # PgDog proxy (default name "pgdog")
  pg autostart enable --haproxy --name lb        # HAProxy instance (default name "haproxy")
  pg autostart enable --minio --name store       # MinIO instance (default name "minio")
  pg autostart enable --ha --scope app --name node1  # Patroni member (one at a time)

Etcd autostart only starts the member's existing container at boot; it never
re-registers membership (a data-dir-initialized etcd rejoins on plain start).
PgDog autostart likewise only starts the existing container, reading the
pgdog.toml/users.toml already on disk — install the proxy first.
HAProxy autostart likewise only starts the existing container, reading the
haproxy.cfg already on disk — install the instance first (pg addon install
haproxy).
MinIO autostart only starts the existing container, recreating it from the
config if the container was removed — install the instance first (pg addon
install minio).
Patroni member autostart only brings that member's existing container up,
reading the patroni.yml already on disk — create the member first (pg ha
create). Start order relative to the DCS doesn't matter: Patroni retries until
etcd answers, then re-elects normally.

On Linux the boot service is a systemd --user unit. True boot-time start
(without a login) requires loginctl enable-linger — attempted automatically,
with a hint printed if it fails. On macOS a LaunchAgent runs at GUI login.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAutostartToggle(cmd, true)
	},
}

var autostartDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Disable auto-start for a target",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAutostartToggle(cmd, false)
	},
}

var autostartStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show auto-start configuration and boot service state",
	Args:  cobra.NoArgs,
	RunE:  runAutostartStatus,
}

var autostartListCmd = &cobra.Command{
	Use:    "list",
	Short:  "List auto-start targets (alias of status)",
	Args:   cobra.NoArgs,
	Hidden: true,
	RunE:   runAutostartStatus,
}

// autostartConfigPath resolves the config path without requiring a full load.
func autostartConfigPath() string {
	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	return path
}

// runAutostartToggle flips the autostart field for exactly one target and
// keeps the boot service in sync with the config.
func runAutostartToggle(cmd *cobra.Command, enable bool) error {
	path := autostartConfigPath()
	// loadConfigForDSN, not loadConfig: an --ha/--etcd/--pgdog toggle must
	// work before any PG instance exists (e.g. Patroni-only or etcd-only
	// hosts). The instance/pgbouncer cases below check cfg.Instances[cfgInstance]
	// directly, so the same "instance not found" error still surfaces where it
	// actually matters.
	if err := loadConfigForDSN(); err != nil {
		return err
	}

	backupSel, _ := cmd.Flags().GetBool("backup")
	pgbSel, _ := cmd.Flags().GetBool("pgbouncer")
	etcdSel, _ := cmd.Flags().GetBool("etcd")
	pgdogSel, _ := cmd.Flags().GetBool("pgdog")
	haSel, _ := cmd.Flags().GetBool("ha")
	haproxySel, _ := cmd.Flags().GetBool("haproxy")
	minioSel, _ := cmd.Flags().GetBool("minio")
	pgName, _ := cmd.Flags().GetString("pg-name")
	addonName, _ := cmd.Flags().GetString("name")
	scope, _ := cmd.Flags().GetString("scope")
	instChanged := cmd.Flags().Changed("instance")

	// Exactly one selector required.
	sel := 0
	if instChanged && !pgbSel {
		sel++
	}
	if backupSel {
		sel++
	}
	if pgbSel {
		sel++
	}
	if etcdSel {
		sel++
	}
	if pgdogSel {
		sel++
	}
	if haSel {
		sel++
	}
	if haproxySel {
		sel++
	}
	if minioSel {
		sel++
	}
	if sel != 1 {
		return fmt.Errorf("select exactly one target: -i <name>, --backup, --pgbouncer, --etcd, --pgdog, --ha, --haproxy, or --minio")
	}
	if pgName != "" && !pgbSel {
		return fmt.Errorf("--pg-name requires --pgbouncer")
	}
	if addonName != "" && !etcdSel && !pgdogSel && !haSel && !haproxySel && !minioSel {
		return fmt.Errorf("--name requires --etcd, --pgdog, --ha, --haproxy, or --minio")
	}
	if scope != "" && !haSel {
		return fmt.Errorf("--scope requires --ha")
	}
	if haSel && scope == "" {
		return fmt.Errorf("--ha requires --scope <cluster-scope>")
	}
	if haSel && addonName == "" {
		return fmt.Errorf("--ha requires --name <member> (one member at a time)")
	}

	var targetDesc string
	switch {
	case haproxySel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "haproxy"
		}
		hc, ok := cfg.Addons.HAProxy[proxyName]
		if !ok {
			return fmt.Errorf("haproxy instance %q not found in config", proxyName)
		}
		hc.Autostart = enable
		cfg.Addons.HAProxy[proxyName] = hc
		targetDesc = fmt.Sprintf("haproxy instance %q", proxyName)
	case minioSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "minio"
		}
		mc, ok := cfg.Addons.Minio[proxyName]
		if !ok {
			return fmt.Errorf("minio instance %q not found in config", proxyName)
		}
		mc.Autostart = enable
		cfg.Addons.Minio[proxyName] = mc
		targetDesc = fmt.Sprintf("minio instance %q", proxyName)
	case haSel:
		cluster, ok := cfg.Addons.Patroni[scope]
		if !ok {
			return fmt.Errorf("Patroni cluster %q not found in config", scope)
		}
		mb, ok := cluster.Members[addonName]
		if !ok {
			return fmt.Errorf("Patroni member %q not found in cluster %q", addonName, scope)
		}
		mb.Autostart = enable
		cluster.Members[addonName] = mb
		cfg.Addons.Patroni[scope] = cluster
		targetDesc = fmt.Sprintf("Patroni member %q of cluster %q", addonName, scope)
	case pgdogSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "pgdog"
		}
		pd, ok := cfg.Addons.PgDog[proxyName]
		if !ok {
			return fmt.Errorf("pgdog proxy %q not found in config", proxyName)
		}
		pd.Autostart = enable
		cfg.Addons.PgDog[proxyName] = pd
		targetDesc = fmt.Sprintf("pgdog proxy %q", proxyName)
	case etcdSel:
		if addonName == "" {
			addonName = "etcd"
		}
		ec, ok := cfg.Addons.Etcd[addonName]
		if !ok {
			return fmt.Errorf("etcd member %q not found in config", addonName)
		}
		ec.Autostart = enable
		cfg.Addons.Etcd[addonName] = ec
		targetDesc = fmt.Sprintf("etcd member %q", addonName)
	case backupSel:
		cfg.Backup.Autostart = enable
		targetDesc = "backup container"
	case pgbSel && pgName != "":
		pb, ok := cfg.Addons.PgBouncer[pgName]
		if !ok {
			return fmt.Errorf("remote PgBouncer %q not found in config", pgName)
		}
		pb.Autostart = enable
		cfg.Addons.PgBouncer[pgName] = pb
		targetDesc = fmt.Sprintf("remote PgBouncer %q", pgName)
	case pgbSel:
		inst, ok := cfg.Instances[cfgInstance]
		if !ok {
			return fmt.Errorf("instance %q not found in config", cfgInstance)
		}
		if inst.Addons.PgBouncer == nil {
			return fmt.Errorf("instance %q has no PgBouncer installed (run 'pg addon install pgbouncer -i %s')", cfgInstance, cfgInstance)
		}
		inst.Addons.PgBouncer.Autostart = enable
		cfg.Instances[cfgInstance] = inst
		targetDesc = fmt.Sprintf("PgBouncer of instance %q", cfgInstance)
	default:
		inst, ok := cfg.Instances[cfgInstance]
		if !ok {
			return fmt.Errorf("instance %q not found in config", cfgInstance)
		}
		inst.Autostart = enable
		cfg.Instances[cfgInstance] = inst
		targetDesc = fmt.Sprintf("instance %q", cfgInstance)
	}

	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	action := "disabled"
	if enable {
		action = "enabled"
	}
	fmt.Printf("  [OK] Auto-start %s for %s\n", action, targetDesc)

	return syncBootService(path)
}

// syncBootService installs/refreshes the boot service when at least one
// target is enabled, and uninstalls it when none are.
func syncBootService(path string) error {
	svc, err := autostart.For(path)
	if err != nil {
		return err
	}
	inst := autostart.NewInstaller()

	if countAutostartTargets(cfg) == 0 {
		fmt.Printf("-> No autostart targets left; removing boot service %s\n", svc.UnitName)
		if err := inst.Uninstall(svc); err != nil {
			return fmt.Errorf("removing boot service: %w", err)
		}
		fmt.Printf("  [OK] Boot service removed\n")
		return nil
	}

	if err := inst.Install(svc); err != nil {
		return fmt.Errorf("installing boot service: %w", err)
	}
	fmt.Printf("  [OK] Boot service %s installed (runs 'pg -c %s start --autostart')\n", svc.UnitName, svc.ConfigPath)
	return nil
}

func countAutostartTargets(c *config.Config) int {
	n := 0
	if c.Backup.Autostart {
		n++
	}
	for _, inst := range c.Instances {
		if inst.Autostart {
			n++
		}
		if inst.Addons.PgBouncer != nil && inst.Addons.PgBouncer.Autostart {
			n++
		}
	}
	for _, pb := range c.Addons.PgBouncer {
		if pb.Autostart {
			n++
		}
	}
	for _, ec := range c.Addons.Etcd {
		if ec.Autostart {
			n++
		}
	}
	for _, pd := range c.Addons.PgDog {
		if pd.Autostart {
			n++
		}
	}
	for _, cluster := range c.Addons.Patroni {
		for _, mb := range cluster.Members {
			if mb.Autostart {
				n++
			}
		}
	}
	for _, hc := range c.Addons.HAProxy {
		if hc.Autostart {
			n++
		}
	}
	for _, mc := range c.Addons.Minio {
		if mc.Autostart {
			n++
		}
	}
	return n
}

func runAutostartStatus(cmd *cobra.Command, args []string) error {
	path := autostartConfigPath()
	// loadConfigForDSN: listing autostart targets must work on configs that
	// have addons but no PG instances (e.g. patroni-only or etcd-only hosts).
	if err := loadConfigForDSN(); err != nil {
		return err
	}

	fmt.Println("=== Auto-start targets ===")
	for name, inst := range cfg.Instances {
		fmt.Printf("  instance %-15s %s\n", name, onOff(inst.Autostart))
		if inst.Addons.PgBouncer != nil {
			fmt.Printf("    pgbouncer %-11s %s\n", name, onOff(inst.Addons.PgBouncer.Autostart))
		}
	}
	fmt.Printf("  backup %-15s %s\n", "", onOff(cfg.Backup.Autostart))
	for name, pb := range cfg.Addons.PgBouncer {
		fmt.Printf("  pgbouncer %-13s %s (remote)\n", name, onOff(pb.Autostart))
	}
	for name, ec := range cfg.Addons.Etcd {
		fmt.Printf("  etcd %-17s %s\n", name, onOff(ec.Autostart))
	}
	for name, pd := range cfg.Addons.PgDog {
		fmt.Printf("  pgdog %-16s %s\n", name, onOff(pd.Autostart))
	}
	for scope, cluster := range cfg.Addons.Patroni {
		for name, mb := range cluster.Members {
			fmt.Printf("  ha %-19s %s\n", scope+"/"+name, onOff(mb.Autostart))
		}
	}
	for name, hc := range cfg.Addons.HAProxy {
		fmt.Printf("  haproxy %-14s %s\n", name, onOff(hc.Autostart))
	}
	for name, mc := range cfg.Addons.Minio {
		fmt.Printf("  minio %-15s %s\n", name, onOff(mc.Autostart))
	}

	fmt.Println("\n=== Boot service ===")
	svc, err := autostart.For(path)
	if err != nil {
		return err
	}
	info, err := autostart.NewInstaller().Status(svc)
	if err != nil {
		return fmt.Errorf("checking boot service: %w", err)
	}
	fmt.Printf("  Unit:      %s\n", info.UnitName)
	fmt.Printf("  Installed: %s\n", yesNo(info.Installed))
	fmt.Printf("  Enabled:   %s\n", onOff(info.Enabled))
	fmt.Printf("  Running:   %s\n", onOff(info.Running))
	if info.Linger != "" {
		fmt.Printf("  Linger:   %s\n", info.Linger)
	}
	if info.Stale != "" {
		fmt.Printf("  [!] %s\n", info.Stale)
	}

	if info.Installed {
		fmt.Printf("\nBoot command: %s -c %s start --autostart\n", svc.Binary, svc.ConfigPath)
		// Show detailed service status from systemctl
		// systemctl returns non-zero exit code when service is not active, so use CombinedOutput
		if out, _ := exec.Command("systemctl", "--user", "status", info.UnitName).CombinedOutput(); len(out) > 0 {
			fmt.Println("\n=== Service status ===")
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if line != "" {
					fmt.Printf("  %s\n", line)
				}
			}
		}
	}
	fmt.Println("\nNote: 'pg stop' does not affect auto-start; use 'pg autostart disable' to opt out.")
	return nil
}

func onOff(v bool) string {
	if v {
		return "enabled"
	}
	return "disabled"
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
