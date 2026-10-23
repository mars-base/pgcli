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
		c.Flags().Bool("postgrest", false, "select the PostgREST addon (use with -i for a local one, --pg-name for a remote one)")
		c.Flags().String("pg-name", "", "name of a remote PgBouncer or PostgREST (top-level addons)")
		c.Flags().Bool("etcd", false, "select an etcd member (top-level addons)")
		c.Flags().String("name", "", "name of the etcd member, pgdog proxy, haproxy, minio, silo, rustfs, garage, seaweedfs, redis, predixy, pgadmin, grafana, prometheus, alertmanager, victoriametrics, vmcluster, vlcluster, victorialogs or vlagent instance, gitea, beegfs, juicefs, zerofs or nginx instance, or Patroni member")
		c.Flags().Bool("pgdog", false, "select a PgDog proxy (top-level addons)")
		c.Flags().Bool("ha", false, "select Patroni HA members (top-level addons; requires --scope)")
		c.Flags().Bool("haproxy", false, "select an HAProxy instance (top-level addons)")
		c.Flags().Bool("minio", false, "select a MinIO instance (top-level addons)")
		c.Flags().Bool("silo", false, "select a silo instance (top-level addons)")
		c.Flags().Bool("rustfs", false, "select a rustfs instance (top-level addons)")
		c.Flags().Bool("garage", false, "select a garage instance (top-level addons)")
		c.Flags().Bool("seaweedfs", false, "select a seaweedfs instance (top-level addons)")
		c.Flags().Bool("redis", false, "select a redis instance (top-level addons)")
		c.Flags().Bool("predixy", false, "select a predixy proxy instance (top-level addons)")
		c.Flags().Bool("pgadmin", false, "select a pgAdmin instance (top-level addons)")
		c.Flags().Bool("grafana", false, "select a Grafana instance (top-level addons)")
		c.Flags().Bool("prometheus", false, "select a Prometheus instance (top-level addons)")
		c.Flags().Bool("alertmanager", false, "select an Alertmanager instance (top-level addons)")
		c.Flags().Bool("victoriametrics", false, "select a VictoriaMetrics instance (top-level addons)")
		c.Flags().Bool("vmcluster", false, "select a VMCluster instance (top-level addons)")
		c.Flags().Bool("vlcluster", false, "select a VLCluster instance (top-level addons)")
		c.Flags().Bool("victorialogs", false, "select a VictoriaLogs instance (top-level addons)")
		c.Flags().Bool("vlagent", false, "select a vlagent instance (top-level addons)")
		c.Flags().Bool("gitea", false, "select a Gitea instance (top-level addons)")
		c.Flags().Bool("beegfs", false, "select a BeeGFS filesystem (top-level addons; autostart starts the whole 3-container cluster)")
		c.Flags().Bool("juicefs", false, "select a JuiceFS volume (top-level host-binary addons; autostart re-mounts it)")
		c.Flags().Bool("zerofs", false, "select a ZeroFS volume (top-level host-binary addons; autostart starts the 9P server and FUSE client)")
		c.Flags().Bool("nginx", false, "select an nginx instance (top-level addons)")
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
	Short: "Enable auto-start for an instance, the backup container, a PgBouncer, a PostgREST, an etcd member, a PgDog proxy, an HAProxy instance, a MinIO/silo/rustfs/garage/seaweedfs/redis/predixy/pgadmin/grafana/prometheus/alertmanager/victoriametrics/vmcluster/vlcluster/victorialogs/vlagent/gitea/beegfs/juicefs/zerofs instance, or a Patroni member",
	Long: `Enable auto-start on boot for exactly one target:

  pg autostart enable -i myinst          # instance
  pg autostart enable --backup           # shared backup container
  pg autostart enable --pgbouncer -i myinst      # local PgBouncer for an instance
  pg autostart enable --pgbouncer --pg-name x    # remote PgBouncer
  pg autostart enable --postgrest -i myinst      # local PostgREST for an instance
  pg autostart enable --postgrest --pg-name x    # remote PostgREST
  pg autostart enable --etcd --name m1           # etcd member (default name "etcd")
  pg autostart enable --pgdog --name proxy       # PgDog proxy (default name "pgdog")
  pg autostart enable --haproxy --name lb        # HAProxy instance (default name "haproxy")
  pg autostart enable --minio --name store       # MinIO instance (default name "minio")
  pg autostart enable --silo --name store         # silo instance (default name "silo")
  pg autostart enable --rustfs --name store       # rustfs instance (default name "rustfs")
  pg autostart enable --garage --name store       # garage instance (default name "garage")
  pg autostart enable --seaweedfs --name store    # seaweedfs instance (default name "seaweedfs")
  pg autostart enable --redis --name cache       # redis instance (default name "redis")
  pg autostart enable --predixy --name proxy       # predixy proxy (default name "predixy")
  pg autostart enable --pgadmin --name ui            # pgAdmin (default name "pgadmin")
  pg autostart enable --grafana --name ui            # Grafana (default name "grafana")
  pg autostart enable --prometheus --name p1         # Prometheus (default name "prometheus")
  pg autostart enable --alertmanager --name am1      # Alertmanager (default name "alertmanager")
  pg autostart enable --victoriametrics --name vm1   # VictoriaMetrics (default name "victoriametrics")
  pg autostart enable --vmcluster --name vmc         # VMCluster — starts this host's role containers (default name "vmcluster")
  pg autostart enable --vlcluster --name vlc         # VLCluster — starts this host's role containers (default name "vlc")
  pg autostart enable --victorialogs --name vl        # VictoriaLogs (default name "victorialogs")
  pg autostart enable --vlagent --name va             # vlagent (default name "vlagent")
  pg autostart enable --gitea --name git1            # Gitea (default name "gitea")
  pg autostart enable --beegfs --name fs1            # BeeGFS filesystem — starts the whole mgmtd+meta+storage cluster (default name "beegfs")
  pg autostart enable --juicefs --name vol1          # JuiceFS volume — remounts it (default name "juicefs")
  pg autostart enable --zerofs --name z1             # ZeroFS volume — 9P server + FUSE client (default name "zerofs")
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
install minio). silo autostart behaves identically (pg addon install silo).
rustfs autostart behaves identically (pg addon install rustfs). rustfs's fixed
container uid is handled inside pgcli's wrapper image, so a boot-time start needs
no special host privileges beyond running podman itself.
garage autostart behaves identically (pg addon install garage): it re-renders
garage.toml and starts the existing container; Garage runs as root in the
upstream image, so no host privileges beyond podman are needed.
seaweedfs autostart likewise re-renders s3.json/filer.toml and starts the
existing container (a cluster member rejoin needs no orchestration — Raft
recovers from its own -data dir). A lone rebooted member simply waits for its
peers; no special host privileges beyond podman are needed.
Redis autostart likewise only starts the existing container (pg addon install
redis); it replays the RDB snapshot from its data dir, so the dataset survives
the reboot.
Predixy autostart likewise only starts the existing container (pg addon install
predixy), reading the predixy.conf already on disk. The boot service starts
redis before predixy, so a proxied cluster is up before its proxy.
PostgREST autostart likewise only starts the existing container, recreating it
from the config if it was removed — install it first (pg addon install
postgrest).
pgAdmin autostart behaves the same way (pg addon install pgadmin). It starts its
container, whose entrypoint re-chowns the data dir to its own uid and drops
privileges, so a boot-time start needs no special host privileges; pgAdmin's
config/session DB persists in that dir across reboots.
Grafana autostart likewise only starts the existing container (pg addon install
grafana). Its data dir was already pre-chowned to the image's uid 472 at install
time (the image itself does no ownership work), so a boot-time start needs no
special host privileges either; dashboards and settings persist in grafana.db.
Prometheus autostart likewise only starts the existing container (pg addon install
prometheus), reading the managed prometheus.yml already on disk. Its TSDB data dir
was pre-chowned to the image's uid/gid 65534 (nobody) at install time (the image
does no ownership work), so a boot-time start needs no special host privileges;
collected metrics persist in the TSDB.
Alertmanager autostart likewise only starts the existing container (pg addon install
alertmanager), reading the managed alertmanager.yml already on disk. Its state dir
was pre-chowned to the image's uid/gid 65534 (nobody) at install time (the image
does no ownership work), so a boot-time start needs no special host privileges;
silences and notification state persist in the data dir.
VictoriaMetrics autostart likewise only starts the existing container (pg addon
install victoriametrics), reading the flags already in the config. The image runs
as root, so a boot-time start needs no special host privileges; the TSDB data dir
persists across reboots.
VMCluster autostart brings THIS host's role containers up in dependency order
(storage → insert → select, pg addon install vmcluster first) and never
re-creates running services — boot must be quick. Each host starts only the
roles it runs; remote vmstorage peers are the operator's reachability concern.
VLCluster autostart does the same for vlstorage → vlinsert → vlselect (pg addon
install vlcluster first): only THIS host's roles start, and remote vlstorage
peers are again the operator's reachability concern.
VictoriaLogs autostart likewise only starts the existing container (pg addon
install victorialogs), reading the flags already in the config. The image runs
as root, so a boot-time start needs no special host privileges; the log data dir
persists across reboots.
vlagent autostart likewise only starts the existing container (pg addon install
vlagent), reading the flags already in the config. It starts after VictoriaLogs
in the boot sequence so its remote-write target is listening first; the file
checkpoints and remote-write buffers persist in the data dir.
Gitea autostart likewise only starts the existing container (pg addon install
gitea), reading the app.ini already rendered into its /data volume. The data dir
was pre-chowned to the image's git user uid/gid 1000 at install time (the image
su-execs down to that user but does no ownership work on an existing mount), so
a boot-time start needs no special host privileges; repos, the app.ini, and the
login/settings DB persist across reboots. With --dsn, the external PostgreSQL
must already be running (or be reachable at its own autostart time) for the
first web request to succeed.
BeeGFS autostart is the only cluster-granular one: it starts the filesystem's
THREE existing containers in dependency order (mgmtd → meta → storage, pg
addon install beegfs first). The data dirs are owned by the root-running images
from creation, so a boot-time start needs no special host privileges; the mgmtd
registry and both targets persist across reboots, and clients re-establish
mounts on their own once the cluster is back.
JuiceFS and ZeroFS autostart are the only host-binary (non-container) ones: they
re-run the mount at boot rather than start a container. JuiceFS remounts the
volume (the metadata engine and bucket already exist — it never re-formats);
ZeroFS brings up its 9P server and then the FUSE client. Both run after the
object-storage addons in the boot sequence, so the S3 backend is listening
before the filesystem reaches for it. They mount as the user the boot service
runs as (root here), same as a manual "pg addon ... mount".
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
	prSel, _ := cmd.Flags().GetBool("postgrest")
	etcdSel, _ := cmd.Flags().GetBool("etcd")
	pgdogSel, _ := cmd.Flags().GetBool("pgdog")
	haSel, _ := cmd.Flags().GetBool("ha")
	haproxySel, _ := cmd.Flags().GetBool("haproxy")
	minioSel, _ := cmd.Flags().GetBool("minio")
	siloSel, _ := cmd.Flags().GetBool("silo")
	rustfsSel, _ := cmd.Flags().GetBool("rustfs")
	garageSel, _ := cmd.Flags().GetBool("garage")
	seaweedfsSel, _ := cmd.Flags().GetBool("seaweedfs")
	redisSel, _ := cmd.Flags().GetBool("redis")
	predixySel, _ := cmd.Flags().GetBool("predixy")
	pgadminSel, _ := cmd.Flags().GetBool("pgadmin")
	grafanaSel, _ := cmd.Flags().GetBool("grafana")
	prometheusSel, _ := cmd.Flags().GetBool("prometheus")
	alertmanagerSel, _ := cmd.Flags().GetBool("alertmanager")
	victoriaMetricsSel, _ := cmd.Flags().GetBool("victoriametrics")
	vmclusterSel, _ := cmd.Flags().GetBool("vmcluster")
	vlclusterSel, _ := cmd.Flags().GetBool("vlcluster")
	victoriaLogsSel, _ := cmd.Flags().GetBool("victorialogs")
	vlagentSel, _ := cmd.Flags().GetBool("vlagent")
	giteaSel, _ := cmd.Flags().GetBool("gitea")
	beegfsSel, _ := cmd.Flags().GetBool("beegfs")
	nginxSel, _ := cmd.Flags().GetBool("nginx")
	juicefsSel, _ := cmd.Flags().GetBool("juicefs")
	zerofsSel, _ := cmd.Flags().GetBool("zerofs")
	pgName, _ := cmd.Flags().GetString("pg-name")
	addonName, _ := cmd.Flags().GetString("name")
	scope, _ := cmd.Flags().GetString("scope")
	instChanged := cmd.Flags().Changed("instance")

	// Exactly one selector required.
	sel := 0
	if instChanged && !pgbSel && !prSel {
		sel++
	}
	if backupSel {
		sel++
	}
	if pgbSel {
		sel++
	}
	if prSel {
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
	if siloSel {
		sel++
	}
	if rustfsSel {
		sel++
	}
	if garageSel {
		sel++
	}
	if seaweedfsSel {
		sel++
	}
	if redisSel {
		sel++
	}
	if predixySel {
		sel++
	}
	if pgadminSel {
		sel++
	}
	if grafanaSel {
		sel++
	}
	if prometheusSel {
		sel++
	}
	if alertmanagerSel {
		sel++
	}
	if victoriaMetricsSel {
		sel++
	}
	if vmclusterSel {
		sel++
	}
	if vlclusterSel {
		sel++
	}
	if victoriaLogsSel {
		sel++
	}
	if vlagentSel {
		sel++
	}
	if giteaSel {
		sel++
	}
	if beegfsSel {
		sel++
	}
	if juicefsSel {
		sel++
	}
	if zerofsSel {
		sel++
	}
	if nginxSel {
		sel++
	}
	if sel != 1 {
		return fmt.Errorf("select exactly one target: -i <name>, --backup, --pgbouncer, --postgrest, --etcd, --pgdog, --ha, --haproxy, --minio, --silo, --rustfs, --garage, --seaweedfs, --redis, --predixy, --pgadmin, --grafana, --prometheus, --alertmanager, --victoriametrics, --vmcluster, --vlcluster, --victorialogs, --vlagent, --gitea, --beegfs, --juicefs, --zerofs, or --nginx")
	}
	if pgName != "" && !pgbSel && !prSel {
		return fmt.Errorf("--pg-name requires --pgbouncer or --postgrest")
	}
	if addonName != "" && !etcdSel && !pgdogSel && !haSel && !haproxySel && !minioSel && !siloSel && !rustfsSel && !garageSel && !seaweedfsSel && !redisSel && !predixySel && !pgadminSel && !grafanaSel && !prometheusSel && !alertmanagerSel && !victoriaMetricsSel && !vmclusterSel && !vlclusterSel && !victoriaLogsSel && !vlagentSel && !giteaSel && !beegfsSel && !juicefsSel && !zerofsSel && !nginxSel {
		return fmt.Errorf("--name requires --etcd, --pgdog, --ha, --haproxy, --minio, --silo, --rustfs, --garage, --seaweedfs, --redis, --predixy, --pgadmin, --grafana, --prometheus, --alertmanager, --victoriametrics, --vmcluster, --vlcluster, --victorialogs, --vlagent, --gitea, --beegfs, --juicefs, --zerofs, or --nginx")
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
	case siloSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "silo"
		}
		sc, ok := cfg.Addons.Silo[proxyName]
		if !ok {
			return fmt.Errorf("silo instance %q not found in config", proxyName)
		}
		sc.Autostart = enable
		cfg.Addons.Silo[proxyName] = sc
		targetDesc = fmt.Sprintf("silo instance %q", proxyName)
	case rustfsSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "rustfs"
		}
		rc, ok := cfg.Addons.Rustfs[proxyName]
		if !ok {
			return fmt.Errorf("rustfs instance %q not found in config", proxyName)
		}
		rc.Autostart = enable
		cfg.Addons.Rustfs[proxyName] = rc
		targetDesc = fmt.Sprintf("rustfs instance %q", proxyName)
	case garageSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "garage"
		}
		gc, ok := cfg.Addons.Garage[proxyName]
		if !ok {
			return fmt.Errorf("garage instance %q not found in config", proxyName)
		}
		gc.Autostart = enable
		cfg.Addons.Garage[proxyName] = gc
		targetDesc = fmt.Sprintf("garage instance %q", proxyName)
	case seaweedfsSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "seaweedfs"
		}
		wc, ok := cfg.Addons.Seaweedfs[proxyName]
		if !ok {
			return fmt.Errorf("seaweedfs instance %q not found in config", proxyName)
		}
		wc.Autostart = enable
		cfg.Addons.Seaweedfs[proxyName] = wc
		targetDesc = fmt.Sprintf("seaweedfs instance %q", proxyName)
	case redisSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "redis"
		}
		rc, ok := cfg.Addons.Redis[proxyName]
		if !ok {
			return fmt.Errorf("redis instance %q not found in config", proxyName)
		}
		rc.Autostart = enable
		cfg.Addons.Redis[proxyName] = rc
		targetDesc = fmt.Sprintf("redis instance %q", proxyName)
	case predixySel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "predixy"
		}
		pc, ok := cfg.Addons.Predixy[proxyName]
		if !ok {
			return fmt.Errorf("predixy instance %q not found in config", proxyName)
		}
		pc.Autostart = enable
		cfg.Addons.Predixy[proxyName] = pc
		targetDesc = fmt.Sprintf("predixy instance %q", proxyName)
	case pgadminSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "pgadmin"
		}
		ac, ok := cfg.Addons.PgAdmin[proxyName]
		if !ok {
			return fmt.Errorf("pgadmin instance %q not found in config", proxyName)
		}
		ac.Autostart = enable
		cfg.Addons.PgAdmin[proxyName] = ac
		targetDesc = fmt.Sprintf("pgadmin instance %q", proxyName)
	case grafanaSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "grafana"
		}
		gc, ok := cfg.Addons.Grafana[proxyName]
		if !ok {
			return fmt.Errorf("grafana instance %q not found in config", proxyName)
		}
		gc.Autostart = enable
		cfg.Addons.Grafana[proxyName] = gc
		targetDesc = fmt.Sprintf("grafana instance %q", proxyName)
	case prometheusSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "prometheus"
		}
		pmc, ok := cfg.Addons.Prometheus[proxyName]
		if !ok {
			return fmt.Errorf("prometheus instance %q not found in config", proxyName)
		}
		pmc.Autostart = enable
		cfg.Addons.Prometheus[proxyName] = pmc
		targetDesc = fmt.Sprintf("prometheus instance %q", proxyName)
	case alertmanagerSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "alertmanager"
		}
		amc, ok := cfg.Addons.Alertmanager[proxyName]
		if !ok {
			return fmt.Errorf("alertmanager instance %q not found in config", proxyName)
		}
		amc.Autostart = enable
		cfg.Addons.Alertmanager[proxyName] = amc
		targetDesc = fmt.Sprintf("alertmanager instance %q", proxyName)
	case victoriaMetricsSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "victoriametrics"
		}
		vc, ok := cfg.Addons.VictoriaMetrics[proxyName]
		if !ok {
			return fmt.Errorf("victoriametrics instance %q not found in config", proxyName)
		}
		vc.Autostart = enable
		cfg.Addons.VictoriaMetrics[proxyName] = vc
		targetDesc = fmt.Sprintf("victoriametrics instance %q", proxyName)
	case vmclusterSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "vmcluster"
		}
		vc, ok := cfg.Addons.VMCluster[proxyName]
		if !ok {
			return fmt.Errorf("vmcluster instance %q not found in config", proxyName)
		}
		vc.Autostart = enable
		cfg.Addons.VMCluster[proxyName] = vc
		targetDesc = fmt.Sprintf("vmcluster instance %q", proxyName)
	case vlclusterSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "vlc"
		}
		vc, ok := cfg.Addons.VLCluster[proxyName]
		if !ok {
			return fmt.Errorf("vlcluster instance %q not found in config", proxyName)
		}
		vc.Autostart = enable
		cfg.Addons.VLCluster[proxyName] = vc
		targetDesc = fmt.Sprintf("vlcluster instance %q", proxyName)
	case victoriaLogsSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "victorialogs"
		}
		lc, ok := cfg.Addons.VictoriaLogs[proxyName]
		if !ok {
			return fmt.Errorf("victorialogs instance %q not found in config", proxyName)
		}
		lc.Autostart = enable
		cfg.Addons.VictoriaLogs[proxyName] = lc
		targetDesc = fmt.Sprintf("victorialogs instance %q", proxyName)
	case vlagentSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "vlagent"
		}
		vc, ok := cfg.Addons.Vlagent[proxyName]
		if !ok {
			return fmt.Errorf("vlagent instance %q not found in config", proxyName)
		}
		vc.Autostart = enable
		cfg.Addons.Vlagent[proxyName] = vc
		targetDesc = fmt.Sprintf("vlagent instance %q", proxyName)
	case giteaSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "gitea"
		}
		gtc, ok := cfg.Addons.Gitea[proxyName]
		if !ok {
			return fmt.Errorf("gitea instance %q not found in config", proxyName)
		}
		gtc.Autostart = enable
		cfg.Addons.Gitea[proxyName] = gtc
		targetDesc = fmt.Sprintf("gitea instance %q", proxyName)
	case beegfsSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "beegfs"
		}
		bc, ok := cfg.Addons.BeeGFS[proxyName]
		if !ok {
			return fmt.Errorf("beegfs filesystem %q not found in config", proxyName)
		}
		bc.Autostart = enable
		cfg.Addons.BeeGFS[proxyName] = bc
		targetDesc = fmt.Sprintf("beegfs filesystem %q", proxyName)
	case juicefsSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "juicefs"
		}
		jc, ok := cfg.Addons.Juicefs[proxyName]
		if !ok {
			return fmt.Errorf("juicefs volume %q not found in config", proxyName)
		}
		jc.Autostart = enable
		cfg.Addons.Juicefs[proxyName] = jc
		targetDesc = fmt.Sprintf("juicefs volume %q", proxyName)
	case zerofsSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "zerofs"
		}
		zc, ok := cfg.Addons.Zerofs[proxyName]
		if !ok {
			return fmt.Errorf("zerofs volume %q not found in config", proxyName)
		}
		zc.Autostart = enable
		cfg.Addons.Zerofs[proxyName] = zc
		targetDesc = fmt.Sprintf("zerofs volume %q", proxyName)
	case nginxSel:
		proxyName := addonName
		if proxyName == "" {
			proxyName = "nginx"
		}
		nc, ok := cfg.Addons.Nginx[proxyName]
		if !ok {
			return fmt.Errorf("nginx instance %q not found in config", proxyName)
		}
		nc.Autostart = enable
		cfg.Addons.Nginx[proxyName] = nc
		targetDesc = fmt.Sprintf("nginx instance %q", proxyName)
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
	case prSel && pgName != "":
		pr, ok := cfg.Addons.Postgrest[pgName]
		if !ok {
			return fmt.Errorf("remote PostgREST %q not found in config", pgName)
		}
		pr.Autostart = enable
		cfg.Addons.Postgrest[pgName] = pr
		targetDesc = fmt.Sprintf("remote PostgREST %q", pgName)
	case prSel:
		inst, ok := cfg.Instances[cfgInstance]
		if !ok {
			return fmt.Errorf("instance %q not found in config", cfgInstance)
		}
		if inst.Addons.Postgrest == nil {
			return fmt.Errorf("instance %q has no PostgREST installed (run 'pg addon install postgrest -i %s')", cfgInstance, cfgInstance)
		}
		inst.Addons.Postgrest.Autostart = enable
		cfg.Instances[cfgInstance] = inst
		targetDesc = fmt.Sprintf("PostgREST of instance %q", cfgInstance)
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
		if inst.Addons.Postgrest != nil && inst.Addons.Postgrest.Autostart {
			n++
		}
	}
	for _, pb := range c.Addons.PgBouncer {
		if pb.Autostart {
			n++
		}
	}
	for _, pr := range c.Addons.Postgrest {
		if pr.Autostart {
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
	for _, sc := range c.Addons.Silo {
		if sc.Autostart {
			n++
		}
	}
	for _, rc := range c.Addons.Rustfs {
		if rc.Autostart {
			n++
		}
	}
	for _, gc := range c.Addons.Garage {
		if gc.Autostart {
			n++
		}
	}
	for _, rc := range c.Addons.Redis {
		if rc.Autostart {
			n++
		}
	}
	for _, pc := range c.Addons.Predixy {
		if pc.Autostart {
			n++
		}
	}
	for _, ac := range c.Addons.PgAdmin {
		if ac.Autostart {
			n++
		}
	}
	for _, gc := range c.Addons.Grafana {
		if gc.Autostart {
			n++
		}
	}
	for _, pmc := range c.Addons.Prometheus {
		if pmc.Autostart {
			n++
		}
	}
	for _, amc := range c.Addons.Alertmanager {
		if amc.Autostart {
			n++
		}
	}
	for _, vc := range c.Addons.VictoriaMetrics {
		if vc.Autostart {
			n++
		}
	}
	for _, vc := range c.Addons.VMCluster {
		if vc.Autostart {
			n++
		}
	}
	for _, vc := range c.Addons.VLCluster {
		if vc.Autostart {
			n++
		}
	}
	for _, lc := range c.Addons.VictoriaLogs {
		if lc.Autostart {
			n++
		}
	}
	for _, vc := range c.Addons.Vlagent {
		if vc.Autostart {
			n++
		}
	}
	for _, gtc := range c.Addons.Gitea {
		if gtc.Autostart {
			n++
		}
	}
	for _, bc := range c.Addons.BeeGFS {
		if bc.Autostart {
			n++
		}
	}
	for _, nc := range c.Addons.Nginx {
		if nc.Autostart {
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
		if inst.Addons.Postgrest != nil {
			fmt.Printf("    postgrest %-10s %s\n", name, onOff(inst.Addons.Postgrest.Autostart))
		}
	}
	fmt.Printf("  backup %-15s %s\n", "", onOff(cfg.Backup.Autostart))
	for name, pb := range cfg.Addons.PgBouncer {
		fmt.Printf("  pgbouncer %-13s %s (remote)\n", name, onOff(pb.Autostart))
	}
	for name, pr := range cfg.Addons.Postgrest {
		fmt.Printf("  postgrest %-12s %s (remote)\n", name, onOff(pr.Autostart))
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
	for name, sc := range cfg.Addons.Silo {
		fmt.Printf("  silo %-16s %s\n", name, onOff(sc.Autostart))
	}
	for name, rc := range cfg.Addons.Rustfs {
		fmt.Printf("  rustfs %-14s %s\n", name, onOff(rc.Autostart))
	}
	for name, gc := range cfg.Addons.Garage {
		fmt.Printf("  garage %-14s %s\n", name, onOff(gc.Autostart))
	}
	for name, wc := range cfg.Addons.Seaweedfs {
		fmt.Printf("  seaweedfs %-12s %s\n", name, onOff(wc.Autostart))
	}
	for name, rc := range cfg.Addons.Redis {
		fmt.Printf("  redis %-15s %s\n", name, onOff(rc.Autostart))
	}
	for name, pc := range cfg.Addons.Predixy {
		fmt.Printf("  predixy %-13s %s\n", name, onOff(pc.Autostart))
	}
	for name, ac := range cfg.Addons.PgAdmin {
		fmt.Printf("  pgadmin %-13s %s\n", name, onOff(ac.Autostart))
	}
	for name, gc := range cfg.Addons.Grafana {
		fmt.Printf("  grafana %-12s %s\n", name, onOff(gc.Autostart))
	}
	for name, pc := range cfg.Addons.Prometheus {
		fmt.Printf("  prometheus %-8s %s\n", name, onOff(pc.Autostart))
	}
	for name, ac := range cfg.Addons.Alertmanager {
		fmt.Printf("  alertmanager %-5s %s\n", name, onOff(ac.Autostart))
	}
	for name, vc := range cfg.Addons.VictoriaMetrics {
		fmt.Printf("  victoriametrics %-2s %s\n", name, onOff(vc.Autostart))
	}
	for name, vc := range cfg.Addons.VMCluster {
		fmt.Printf("  vmcluster %-8s %s\n", name, onOff(vc.Autostart))
	}
	for name, vc := range cfg.Addons.VLCluster {
		fmt.Printf("  vlcluster %-8s %s\n", name, onOff(vc.Autostart))
	}
	for name, lc := range cfg.Addons.VictoriaLogs {
		fmt.Printf("  victorialogs %-4s %s\n", name, onOff(lc.Autostart))
	}
	for name, vc := range cfg.Addons.Vlagent {
		fmt.Printf("  vlagent %-10s %s\n", name, onOff(vc.Autostart))
	}
	for name, gtc := range cfg.Addons.Gitea {
		fmt.Printf("  gitea %-14s %s\n", name, onOff(gtc.Autostart))
	}
	for name, bc := range cfg.Addons.BeeGFS {
		fmt.Printf("  beegfs %-13s %s\n", name, onOff(bc.Autostart))
	}
	for name, jc := range cfg.Addons.Juicefs {
		fmt.Printf("  juicefs %-12s %s\n", name, onOff(jc.Autostart))
	}
	for name, zc := range cfg.Addons.Zerofs {
		fmt.Printf("  zerofs %-13s %s\n", name, onOff(zc.Autostart))
	}
	for name, nc := range cfg.Addons.Nginx {
		fmt.Printf("  nginx %-15s %s\n", name, onOff(nc.Autostart))
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
