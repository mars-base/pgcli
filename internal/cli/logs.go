package cli

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/podman"
)

// ---------------------------------------------------------------------------
// Parent command: pg logs (default behavior = show PG instance logs)
// ---------------------------------------------------------------------------

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "View PostgreSQL instance logs",
	Long: `View PostgreSQL instance console output logs.

By default, shows the PostgreSQL server logs for the current instance
(without -i, defaults to "default").
Use "pg logs addon" to view addon console output logs.

Examples:
  pg logs                              # PG logs (default instance), last 50 lines
  pg logs -f                           # Follow PG logs
  pg logs -n 200                       # Last 200 lines
  pg logs -i myinst                    # Specific instance
  pg logs addon pgbouncer -i myinst    # Local PgBouncer logs for myinst
  pg logs addon pgbouncer --pg-name my-pool   # Remote PgBouncer logs
  pg logs addon pgbouncer --pg-name my-pool -f
  pg logs addon etcd --name m1         # etcd member logs
  pg logs addon pgdog --name proxy     # PgDog proxy logs
  pg logs addon postgrest --pg-name my-api  # Remote PostgREST logs
  pg logs addon haproxy --name lb      # HAProxy instance logs
  pg logs addon minio --name store     # MinIO instance logs
  pg logs addon silo --name store      # silo instance logs
  pg logs addon predixy --name proxy   # Predixy proxy logs
  pg logs addon pgadmin                # pgAdmin web UI logs
  pg logs addon grafana                # Grafana dashboard logs
  pg logs addon prometheus             # Prometheus server logs
  pg logs addon alertmanager           # Alertmanager server logs
  pg logs addon gitea                  # Gitea server logs
  pg logs addon beegfs --service meta  # BeeGFS metadata-daemon logs (default service: mgmtd)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		follow, _ := cmd.Flags().GetBool("follow")
		tail, _ := cmd.Flags().GetInt("tail")

		if err := loadConfig(); err != nil {
			return err
		}

		return runPodmanLogs(cfg.Podman.ContainerName, tail, follow)
	},
}

// ---------------------------------------------------------------------------
// Subcommand: pg logs addon <type> --pg-name <name>
// ---------------------------------------------------------------------------

var logsAddonCmd = &cobra.Command{
	Use:   "addon <addon-type>",
	Short: "Show addon console output logs",
	Long: `Show addon console output logs.

Requires the addon type (pgbouncer, postgrest, etcd, pgdog, haproxy, minio, silo, rustfs, garage, seaweedfs, redis, predixy, pgadmin, grafana, prometheus, alertmanager, gitea, beegfs, vmcluster, vlcluster, juicefs, zerofs, influxdb, influx2tsdb, clickhouse, kafka, rocketmq, patroni).
Use -i for local instance addons, --pg-name for remote addons,
--name for an etcd member, pgdog proxy, haproxy instance, minio, silo, rustfs, garage,
seaweedfs, redis, predixy, pgadmin, grafana, prometheus, alertmanager, gitea, beegfs, vmcluster, vlcluster, juicefs, zerofs, influxdb, influx2tsdb, clickhouse, kafka, rocketmq or Patroni member (default
"etcd"/"pgdog"/"haproxy"/"minio"/"silo"/"rustfs"/"garage"/"seaweedfs"/"redis"/"predixy"/"pgadmin"/"grafana"/"prometheus"/"alertmanager"/"gitea"/"beegfs"/"vmc"/"vlc"/"juicefs"/"zerofs"/"influxdb"/"influx2tsdb"/"clickhouse"/"kafka"/"rocketmq"; Patroni members have
no default and also require --scope).

juicefs and zerofs are host-binary addons, so their logs are plain
files, not containers: juicefs tails the mount daemon's log; zerofs tails the
server log by default and the FUSE client's with --service mount. influxdb,
influx2tsdb, clickhouse, kafka and rocketmq run as containers, so their logs come from the
container runtime and accept the same -n/-f flags as the other container addons.
RocketMQ runs three containers per instance and defaults to the broker log;
--service namesrv|broker|proxy picks another role.

Examples:
  pg logs addon pgbouncer -i myinst
  pg logs addon pgbouncer -i myinst -f
  pg logs addon pgbouncer --pg-name my-pool
  pg logs addon pgbouncer --pg-name my-pool -f
  pg logs addon pgbouncer --pg-name my-pool -n 200
  pg logs addon postgrest -i myinst
  pg logs addon postgrest --pg-name my-api
  pg logs addon postgrest --pg-name my-api -f
  pg logs addon etcd --name m1
  pg logs addon etcd --name m1 -f
  pg logs addon etcd --name m2 -n 200
  pg logs addon pgdog --name proxy
  pg logs addon pgdog --name proxy -f
  pg logs addon haproxy --name lb
  pg logs addon haproxy --name lb -f
  pg logs addon minio --name store
  pg logs addon minio --name store -f
  pg logs addon silo --name store
  pg logs addon silo --name store -f
  pg logs addon rustfs --name store
  pg logs addon rustfs --name store -f
  pg logs addon garage --name store -f
  pg logs addon seaweedfs --name store -f
  pg logs addon redis --name cache
  pg logs addon redis --name cache -f
  pg logs addon predixy --name proxy
  pg logs addon predixy --name proxy -f
  pg logs addon pgadmin
  pg logs addon pgadmin -f
  pg logs addon grafana                # Grafana dashboard logs
  pg logs addon grafana -f
  pg logs addon prometheus             # Prometheus server logs
  pg logs addon prometheus --name p1 -f
  pg logs addon alertmanager           # Alertmanager server logs
  pg logs addon gitea                  # Gitea server logs
  pg logs addon beegfs                 # BeeGFS mgmtd logs (the registry; default service)
  pg logs addon beegfs --service meta  # the metadata daemon's logs
  pg logs addon beegfs --name fs1 --service storage -f
  pg logs addon vmcluster              # VMCluster storage logs (default service)
  pg logs addon vmcluster --service insert  # the vminsert logs
  pg logs addon vmcluster --service select  # the vmselect logs
  pg logs addon vlcluster              # VLCluster storage logs (default service)
  pg logs addon vlcluster --service insert  # the vlinsert logs
  pg logs addon vlcluster --service select  # the vlselect logs
  pg logs addon juicefs --name vol1            # JuiceFS mount daemon log
  pg logs addon juicefs --name vol1 -f
  pg logs addon zerofs --name z1               # ZeroFS server log (default service)
  pg logs addon zerofs --name z1 --service mount -f  # the FUSE client log
  pg logs addon influxdb --name metrics        # influxd daemon log
  pg logs addon influx2tsdb --name influx2tsdb # proxy container log
  pg logs addon clickhouse --name ch1         # clickhouse-server container log
  pg logs addon patroni --scope app --name node1
  pg logs addon patroni --scope app --name node1 -f`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		follow, _ := cmd.Flags().GetBool("follow")
		tail, _ := cmd.Flags().GetInt("tail")
		pgName, _ := cmd.Flags().GetString("pg-name")
		etcdName, _ := cmd.Flags().GetString("name")
		scope, _ := cmd.Flags().GetString("scope")
		service, _ := cmd.Flags().GetString("service")

		addonType := args[0]

		// loadConfigForDSN, not loadConfig: `pg logs addon ...` only needs the
		// addon section of the config, and requiring a valid *instance* here
		// would make etcd/pgdog/patroni logs unusable on a config that has
		// addons but no PG instance yet. pgbouncer's local branch below reads
		// cfgInstance (the raw -i flag) rather than cfg.Instance, which only
		// SetInstance populates.
		if err := loadConfigForDSN(); err != nil {
			return err
		}
		if scope != "" && addonType != "patroni" {
			return fmt.Errorf("--scope only applies to the patroni addon type")
		}
		if service != "" && addonType != "beegfs" && addonType != "zerofs" && addonType != "vmcluster" && addonType != "vlcluster" && addonType != "rocketmq" {
			return fmt.Errorf("--service only applies to the beegfs, zerofs, vmcluster, vlcluster and rocketmq addon types")
		}

		var containerName string

		switch addonType {
		case "patroni":
			if pgName != "" {
				return fmt.Errorf("--pg-name selects a remote PgBouncer or PostgREST; use --scope/--name for a Patroni member")
			}
			if scope == "" {
				return fmt.Errorf("--scope is required for Patroni members, e.g. pg logs addon patroni --scope app --name node1")
			}
			if etcdName == "" {
				return fmt.Errorf("--name is required: the Patroni member, e.g. pg logs addon patroni --scope %s --name node1", scope)
			}
			var err error
			containerName, err = resolvePatroniContainer(scope, etcdName)
			if err != nil {
				return err
			}
		case "etcd", "pgdog", "haproxy", "minio", "silo", "rustfs", "garage", "seaweedfs", "redis", "predixy", "pgadmin", "grafana", "prometheus", "alertmanager", "gitea", "beegfs", "nginx", "influxdb", "influx2tsdb", "clickhouse", "kafka", "rabbitmq", "rocketmq", "vmcluster", "vlcluster":
			// (patroni is handled above; it shares --name but requires --scope.)
			if pgName != "" {
				return fmt.Errorf("--pg-name selects a remote PgBouncer or PostgREST; use --name for an %s", addonType)
			}
			deflt := addonType
			switch addonType {
			case "vmcluster":
				deflt = "vmc"
			case "vlcluster":
				deflt = "vlc"
			}
			name := etcdName
			if name == "" {
				name = deflt
			}
			switch addonType {
			case "etcd":
				if cfg.Addons.Etcd == nil {
					return fmt.Errorf("no etcd addons configured")
				}
				ec, ok := cfg.Addons.Etcd[name]
				if !ok {
					return fmt.Errorf("etcd member %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = ec.ContainerName
			case "pgdog":
				if cfg.Addons.PgDog == nil {
					return fmt.Errorf("no pgdog addons configured")
				}
				pd, ok := cfg.Addons.PgDog[name]
				if !ok {
					return fmt.Errorf("pgdog proxy %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = pd.ContainerName
			case "haproxy":
				if cfg.Addons.HAProxy == nil {
					return fmt.Errorf("no haproxy addons configured")
				}
				hc, ok := cfg.Addons.HAProxy[name]
				if !ok {
					return fmt.Errorf("haproxy instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = hc.ContainerName
			case "minio":
				if cfg.Addons.Minio == nil {
					return fmt.Errorf("no minio addons configured")
				}
				mc, ok := cfg.Addons.Minio[name]
				if !ok {
					return fmt.Errorf("minio instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = mc.ContainerName
			case "silo":
				if cfg.Addons.Silo == nil {
					return fmt.Errorf("no silo addons configured")
				}
				sc, ok := cfg.Addons.Silo[name]
				if !ok {
					return fmt.Errorf("silo instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = sc.ContainerName
			case "rustfs":
				if cfg.Addons.Rustfs == nil {
					return fmt.Errorf("no rustfs addons configured")
				}
				rc, ok := cfg.Addons.Rustfs[name]
				if !ok {
					return fmt.Errorf("rustfs instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = rc.ContainerName
			case "garage":
				if cfg.Addons.Garage == nil {
					return fmt.Errorf("no garage addons configured")
				}
				gc, ok := cfg.Addons.Garage[name]
				if !ok {
					return fmt.Errorf("garage instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = gc.ContainerName
			case "seaweedfs":
				if cfg.Addons.Seaweedfs == nil {
					return fmt.Errorf("no seaweedfs addons configured")
				}
				sw, ok := cfg.Addons.Seaweedfs[name]
				if !ok {
					return fmt.Errorf("seaweedfs instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = sw.ContainerName
			case "redis":
				if cfg.Addons.Redis == nil {
					return fmt.Errorf("no redis addons configured")
				}
				rc, ok := cfg.Addons.Redis[name]
				if !ok {
					return fmt.Errorf("redis instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = rc.ContainerName
			case "predixy":
				if cfg.Addons.Predixy == nil {
					return fmt.Errorf("no predixy addons configured")
				}
				pc, ok := cfg.Addons.Predixy[name]
				if !ok {
					return fmt.Errorf("predixy instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = pc.ContainerName
			case "pgadmin":
				if cfg.Addons.PgAdmin == nil {
					return fmt.Errorf("no pgadmin addons configured")
				}
				ac, ok := cfg.Addons.PgAdmin[name]
				if !ok {
					return fmt.Errorf("pgadmin instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = ac.ContainerName
			case "grafana":
				if cfg.Addons.Grafana == nil {
					return fmt.Errorf("no grafana addons configured")
				}
				gc, ok := cfg.Addons.Grafana[name]
				if !ok {
					return fmt.Errorf("grafana instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = gc.ContainerName
			case "prometheus":
				if cfg.Addons.Prometheus == nil {
					return fmt.Errorf("no prometheus addons configured")
				}
				pc, ok := cfg.Addons.Prometheus[name]
				if !ok {
					return fmt.Errorf("prometheus instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = pc.ContainerName
			case "alertmanager":
				if cfg.Addons.Alertmanager == nil {
					return fmt.Errorf("no alertmanager addons configured")
				}
				ac, ok := cfg.Addons.Alertmanager[name]
				if !ok {
					return fmt.Errorf("alertmanager instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = ac.ContainerName
			case "gitea":
				if cfg.Addons.Gitea == nil {
					return fmt.Errorf("no gitea addons configured")
				}
				gtc, ok := cfg.Addons.Gitea[name]
				if !ok {
					return fmt.Errorf("gitea instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = gtc.ContainerName
			case "beegfs":
				if cfg.Addons.BeeGFS == nil {
					return fmt.Errorf("no beegfs addons configured")
				}
				bc, ok := cfg.Addons.BeeGFS[name]
				if !ok {
					return fmt.Errorf("beegfs filesystem %q not found (use 'pg addon list' to see available)", name)
				}
				// One filesystem = three containers; --service picks which
				// daemon's logs to read (default mgmtd, the registry).
				svc := service
				if svc == "" {
					svc = "mgmtd"
				}
				switch svc {
				case "mgmtd", "meta", "storage": // podman.BeeGFSService* roles
				default:
					return fmt.Errorf("--service must be mgmtd, meta or storage, got %q", svc)
				}
				containerName = bc.ContainerName(svc)
			case "influxdb":
				if cfg.Addons.InfluxDB == nil {
					return fmt.Errorf("no influxdb addons configured")
				}
				ic, ok := cfg.Addons.InfluxDB[name]
				if !ok {
					return fmt.Errorf("influxdb instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = ic.ContainerName
			case "influx2tsdb":
				if cfg.Addons.Influx2TSDB == nil {
					return fmt.Errorf("no influx2tsdb addons configured")
				}
				ic, ok := cfg.Addons.Influx2TSDB[name]
				if !ok {
					return fmt.Errorf("influx2tsdb instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = ic.ContainerName
			case "clickhouse":
				if cfg.Addons.ClickHouse == nil {
					return fmt.Errorf("no clickhouse addons configured")
				}
				cc, ok := cfg.Addons.ClickHouse[name]
				if !ok {
					return fmt.Errorf("clickhouse instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = cc.ContainerName
			case "kafka":
				if cfg.Addons.Kafka == nil {
					return fmt.Errorf("no kafka addons configured")
				}
				kc, ok := cfg.Addons.Kafka[name]
				if !ok {
					return fmt.Errorf("kafka instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = kc.ContainerName
			case "rabbitmq":
				if cfg.Addons.RabbitMQ == nil {
					return fmt.Errorf("no rabbitmq addons configured")
				}
				rc, ok := cfg.Addons.RabbitMQ[name]
				if !ok {
					return fmt.Errorf("rabbitmq instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = rc.ContainerName
			case "rocketmq":
				if cfg.Addons.RocketMQ == nil {
					return fmt.Errorf("no rocketmq addons configured")
				}
				rc, ok := cfg.Addons.RocketMQ[name]
				if !ok {
					return fmt.Errorf("rocketmq instance %q not found (use 'pg addon list' to see available)", name)
				}
				// One instance = three containers; --service picks which role's
				// logs to read (default broker, the data node).
				svc := service
				if svc == "" {
					svc = podman.RocketMQRoleBroker
				}
				switch svc {
				case podman.RocketMQRoleNamesrv, podman.RocketMQRoleBroker, podman.RocketMQRoleProxy:
				default:
					return fmt.Errorf("--service must be namesrv, broker or proxy, got %q", svc)
				}
				containerName = config.RocketMQContainerName(rc, svc)
			case "nginx":
				if cfg.Addons.Nginx == nil {
					return fmt.Errorf("no nginx addons configured")
				}
				nc, ok := cfg.Addons.Nginx[name]
				if !ok {
					return fmt.Errorf("nginx instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = nc.ContainerName
			case "vmcluster":
				if cfg.Addons.VMCluster == nil {
					return fmt.Errorf("no vmcluster addons configured")
				}
				vc, ok := cfg.Addons.VMCluster[name]
				if !ok {
					return fmt.Errorf("vmcluster instance %q not found (use 'pg addon list' to see available)", name)
				}
				// One cluster = three containers; --service picks which
				// component's logs to read (default storage, the data node).
				svc := service
				if svc == "" {
					svc = "storage"
				}
				switch svc {
				case "storage", "insert", "select": // vmcluster role names
				default:
					return fmt.Errorf("--service must be storage, insert or select, got %q", svc)
				}
				containerName = vc.ContainerName(svc)
			case "vlcluster":
				if cfg.Addons.VLCluster == nil {
					return fmt.Errorf("no vlcluster addons configured")
				}
				vc, ok := cfg.Addons.VLCluster[name]
				if !ok {
					return fmt.Errorf("vlcluster instance %q not found (use 'pg addon list' to see available)", name)
				}
				// One cluster = three containers; --service picks which
				// component's logs to read (default storage, the data node).
				svc := service
				if svc == "" {
					svc = "storage"
				}
				switch svc {
				case "storage", "insert", "select": // vlcluster role names
				default:
					return fmt.Errorf("--service must be storage, insert or select, got %q", svc)
				}
				if !vc.RunsRole(svc) {
					return fmt.Errorf("vlcluster instance %q does not run the %s role on this host (roles: %s)", name, svc, strings.Join(vc.RolesInOrder(), ", "))
				}
				containerName = vc.ContainerName(svc)
			}
		case "pgbouncer":
			if etcdName != "" {
				return fmt.Errorf("--name selects an etcd member, pgdog proxy, haproxy instance, minio, silo, rustfs, garage, seaweedfs, redis, predixy, pgadmin, grafana, prometheus, alertmanager, gitea, beegfs, vmcluster, vlcluster instance, or Patroni member; use -i or --pg-name for PgBouncer")
			}
			if pgName != "" {
				// Remote mode
				if cfg.Addons.PgBouncer == nil {
					return fmt.Errorf("no remote PgBouncer addons configured")
				}
				pb, ok := cfg.Addons.PgBouncer[pgName]
				if !ok {
					return fmt.Errorf("remote PgBouncer %q not found (use 'pg addon list' to see available)", pgName)
				}
				containerName = pb.ContainerName
			} else {
				// Local mode: -i. Uses cfgInstance (the raw flag) since this
				// path loads config without SetInstance (see comment above).
				inst, ok := cfg.Instances[cfgInstance]
				if !ok {
					return fmt.Errorf("instance %q not found", cfgInstance)
				}
				if inst.Addons.PgBouncer == nil {
					return fmt.Errorf("no PgBouncer addon configured for instance %q", cfgInstance)
				}
				containerName = inst.Addons.PgBouncer.ContainerName
			}
		case "postgrest":
			if etcdName != "" {
				return fmt.Errorf("--name selects an etcd member, pgdog proxy, haproxy instance, minio, silo, rustfs, garage, seaweedfs, redis, predixy, pgadmin, grafana, prometheus, alertmanager, gitea, beegfs, vmcluster, vlcluster instance, or Patroni member; use -i or --pg-name for PostgREST")
			}
			if pgName != "" {
				// Remote mode
				if cfg.Addons.Postgrest == nil {
					return fmt.Errorf("no remote PostgREST addons configured")
				}
				pr, ok := cfg.Addons.Postgrest[pgName]
				if !ok {
					return fmt.Errorf("remote PostgREST %q not found (use 'pg addon list' to see available)", pgName)
				}
				containerName = pr.ContainerName
			} else {
				// Local mode: -i
				inst, ok := cfg.Instances[cfgInstance]
				if !ok {
					return fmt.Errorf("instance %q not found", cfgInstance)
				}
				if inst.Addons.Postgrest == nil {
					return fmt.Errorf("no PostgREST addon configured for instance %q", cfgInstance)
				}
				containerName = inst.Addons.Postgrest.ContainerName
			}
		case "juicefs", "zerofs":
			// Host-binary addons: logs are plain files on the host, not a
			// container. juicefs has one mount log; zerofs has a server log
			// (default) and a mount log (--service mount).
			jname := etcdName
			if jname == "" {
				jname = addonType
			}
			m, err := hostAddonLogfile(addonType, jname, service, cfg)
			if err != nil {
				return err
			}
			return tailHostLog(m, tail, follow)
		default:
			return fmt.Errorf("unknown addon: %s (available: pgbouncer, etcd, pgdog, haproxy, minio, silo, rustfs, garage, seaweedfs, redis, predixy, postgrest, pgadmin, grafana, prometheus, alertmanager, gitea, beegfs, vmcluster, vlcluster, juicefs, zerofs, influxdb, influx2tsdb, clickhouse, kafka, rocketmq, nginx, patroni)", addonType)
		}

		return runPodmanLogs(containerName, tail, follow)
	},
}

// ---------------------------------------------------------------------------
// Subcommand: pg logs ha <scope> --member <name>
// ---------------------------------------------------------------------------

var logsHaCmd = &cobra.Command{
	Use:   "ha <scope>",
	Short: "Show Patroni HA cluster member logs",
	Long: `View console output logs for a member of a Patroni HA cluster (scope).

This is shorthand for "pg logs addon patroni --scope <scope> --name <member>"
(Patroni is managed by "pg ha", not "pg addon", so this is the natural fit
next to the rest of the "pg ha" command family).

Examples:
  pg logs ha app --member node1     # node1's logs (scope app)
  pg logs ha app -m node1 -f        # Follow node1
  pg logs ha app -m node1 -n 200    # Last 200 lines`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		follow, _ := cmd.Flags().GetBool("follow")
		tail, _ := cmd.Flags().GetInt("tail")
		member, _ := cmd.Flags().GetString("member")

		if err := loadConfigForDSN(); err != nil {
			return err
		}
		containerName, err := resolvePatroniContainer(args[0], member)
		if err != nil {
			return err
		}
		return runPodmanLogs(containerName, tail, follow)
	},
}

// resolvePatroniContainer validates a scope + member name against the loaded
// cfg (must be populated via loadConfigForDSN first) and returns the member's
// container name. Shared by `pg logs addon patroni` and the `pg logs ha`
// shorthand so the two can't drift apart on validation or lookup.
func resolvePatroniContainer(scope, member string) (string, error) {
	if cfg.Addons.Patroni == nil {
		return "", fmt.Errorf("no Patroni HA clusters configured")
	}
	cluster, ok := cfg.Addons.Patroni[scope]
	if !ok {
		return "", fmt.Errorf("Patroni cluster %q not found (use 'pg ha status' to list scopes)", scope)
	}
	if member == "" {
		return "", fmt.Errorf("--member is required: the Patroni member, e.g. pg logs ha %s --member node1", scope)
	}
	mb, ok := cluster.Members[member]
	if !ok {
		return "", fmt.Errorf("Patroni member %q not found in cluster %q (this host's view: %s)", member, scope, patroniMemberNames(cluster))
	}
	return mb.ContainerName, nil
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

func init() {
	// Flags on parent (pg logs)
	logsCmd.Flags().BoolP("follow", "f", false, "Stream logs continuously")
	logsCmd.Flags().IntP("tail", "n", 50, "Number of lines to show (0 = all)")

	// Flags on addon subcommand
	logsAddonCmd.Flags().BoolP("follow", "f", false, "Stream logs continuously")
	logsAddonCmd.Flags().IntP("tail", "n", 50, "Number of lines to show (0 = all)")
	logsAddonCmd.Flags().String("pg-name", "", "Remote addon name (for remote PgBouncer or PostgREST)")
	logsAddonCmd.Flags().String("name", "", "etcd member name, pgdog proxy name, haproxy instance name, minio/silo/rustfs/garage/redis/predixy/pgadmin/grafana/prometheus/alertmanager/gitea/influxdb/influx2tsdb/clickhouse/kafka/rocketmq/beegfs/vmcluster/vlcluster instance name, or Patroni member name")
	logsAddonCmd.Flags().String("scope", "", "Patroni cluster scope (required only with addon type patroni)")
	logsAddonCmd.Flags().String("service", "", "beegfs: which container of the filesystem to read — mgmtd, meta or storage (default \"mgmtd\"); zerofs: server or mount (the FUSE client) (default \"server\"); vmcluster/vlcluster: storage, insert or select (default \"storage\"); rocketmq: namesrv, broker or proxy (default \"broker\")")

	// Flags on ha subcommand
	logsHaCmd.Flags().BoolP("follow", "f", false, "Stream logs continuously")
	logsHaCmd.Flags().IntP("tail", "n", 50, "Number of lines to show (0 = all)")
	logsHaCmd.Flags().StringP("member", "m", "", "Patroni member name")

	rootCmd.AddCommand(logsCmd)
	logsCmd.AddCommand(logsAddonCmd)
	logsCmd.AddCommand(logsHaCmd)
}

// ---------------------------------------------------------------------------
// log output helpers
// ---------------------------------------------------------------------------

// patroniMemberNames lists a cluster's members (this host's config view), for
// a "member not found" error message.
func patroniMemberNames(cluster config.PatroniClusterConfig) string {
	names := make([]string, 0, len(cluster.Members))
	for name := range cluster.Members {
		names = append(names, name)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// runPodmanLogs runs podman logs directly, output goes to os.Stdout.
func runPodmanLogs(containerName string, tail int, follow bool) error {
	args := []string{"logs"}

	if follow {
		args = append(args, "-f")
	}

	if tail > 0 {
		args = append(args, "--tail", fmt.Sprintf("%d", tail))
	} else if !follow {
		args = append(args, "--tail", "all")
	}

	args = append(args, containerName)

	cmd := exec.Command("podman", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// ---------------------------------------------------------------------------
// Host-binary add-on logs (juicefs, zerofs) — plain files, not containers
// ---------------------------------------------------------------------------

// hostAddonLogfile resolves the on-disk log path for a host-binary add-on
// instance. juicefs has a single mount daemon (MountLogFile); zerofs runs a
// server plus a FUSE client, so --service {server|mount} picks between
// LogFile (the server, the default) and the derived mount log.
func hostAddonLogfile(addonType, name, service string, cfg *config.Config) (string, error) {
	switch addonType {
	case "juicefs":
		if service != "" {
			return "", fmt.Errorf("--service does not apply to juicefs (one mount daemon, one log)")
		}
		jc, ok := cfg.Addons.Juicefs[name]
		if !ok {
			return "", fmt.Errorf("juicefs volume %q not found (use 'pg addon list' to see available)", name)
		}
		if jc.MountLogFile == "" {
			return "", fmt.Errorf("juicefs %q has no recorded mount log path", name)
		}
		return jc.MountLogFile, nil
	case "zerofs":
		zc, ok := cfg.Addons.Zerofs[name]
		if !ok {
			return "", fmt.Errorf("zerofs volume %q not found (use 'pg addon list' to see available)", name)
		}
		svc := service
		if svc == "" {
			svc = "server"
		}
		switch svc {
		case "server":
			return zc.LogFile, nil
		case "mount":
			return zerofsMountLog(&zc), nil
		default:
			return "", fmt.Errorf("--service must be server or mount, got %q", svc)
		}
	case "influxdb":
		return "", fmt.Errorf("influxdb runs as a container; use the container log path")
	}
	return "", fmt.Errorf("not a host-binary add-on: %s", addonType)
}

// tailHostLog prints the last `tail` lines of a host log file, or follows it
// with `tail -f` (matching runPodmanLogs' semantics: tail>0 → -n, follow →
// stream). The file is small and daemon-appended, so reading it whole is fine.
func tailHostLog(path string, tail int, follow bool) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("log file %s: %w", path, err)
	}
	if follow {
		args := []string{"-n", fmt.Sprintf("%d", max(tail, 10)), "-f", path}
		return followFile(args...)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if tail > 0 && len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	for _, l := range lines {
		fmt.Println(l)
	}
	return nil
}

// followFile runs `tail -f` with the file's parent dir made group-readable-safe
// and forwards interrupt/TERM so Ctrl-C detaches cleanly (reusing the repo's
// stopSignal/killSelf convention from ha_restore).
func followFile(args ...string) error {
	cmd := exec.Command("tail", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting tail: %w", err)
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, stopSignal)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		signal.Stop(sigs)
		return err
	case sig := <-sigs:
		_ = cmd.Process.Signal(syscall.SIGTERM)
		<-done
		signal.Stop(sigs)
		killSelf(sig)
		return nil
	}
}
