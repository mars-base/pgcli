package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/hostbin"
	"github.com/mars-base/pgcli/internal/pitr"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
	"github.com/mars-base/pgcli/internal/sqlite"
)

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.Flags().BoolVar(&startAll, "all", false, "start all configured instances")
	startCmd.Flags().BoolVar(&startAutostartFlag, "autostart", false, "start only instances/addons with autostart enabled (used by the boot service)")
	startCmd.MarkFlagsMutuallyExclusive("all", "autostart")
}

var (
	startAll           bool
	startAutostartFlag bool
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start pg services (PostgreSQL + pgBackRest)",
	Long: `start initializes the runtime environment and launches the PostgreSQL container.

Steps:
  1. Check dependencies
  2. podman machine init/start (macOS only)
  3. Build PostgreSQL + pgBackRest image (if missing)
  4. Create data directories (if missing)
  5. Start PostgreSQL container
  6. Initialize pgBackRest stanza (if PITR enabled)

Use --all to start all instances configured in the current config file.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if startAutostartFlag {
			return startAutostart()
		}
		if startAll {
			return startAllInstances()
		}
		return startInstance()
	},
}

// startAutostart starts only the instances and addons that have autostart
// enabled in the config. Invoked by the boot service (systemd unit or
// launchd agent) after a host reboot. Returns nil when nothing is enabled
// so the boot unit always succeeds.
func startAutostart() error {
	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	c, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	cfgPath = path

	// Instances with autostart enabled, sorted for deterministic order.
	var names []string
	for name, inst := range c.Instances {
		if inst.Autostart {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	if len(names) == 0 && !c.Backup.Autostart && !hasAutostartPgbouncer(c) && !hasAutostartPostgrest(c) && !hasAutostartEtcd(c) && !hasAutostartPgDog(c) && !hasAutostartPatroni(c) && !hasAutostartHAProxy(c) && !hasAutostartMinio(c) && !hasAutostartSilo(c) && !hasAutostartRustfs(c) && !hasAutostartGarage(c) && !hasAutostartSeaweedfs(c) && !hasAutostartSQLite(c) && !hasAutostartRedis(c) && !hasAutostartPredixy(c) && !hasAutostartPgAdmin(c) && !hasAutostartGrafana(c) && !hasAutostartPrometheus(c) && !hasAutostartAlertmanager(c) && !hasAutostartVictoriaMetrics(c) && !hasAutostartVMCluster(c) && !hasAutostartVLCluster(c) && !hasAutostartVictoriaLogs(c) && !hasAutostartVlagent(c) && !hasAutostartGitea(c) && !hasAutostartBeeGFS(c) && !hasAutostartJuicefs(c) && !hasAutostartZerofs(c) && !hasAutostartNginx(c) {
		fmt.Println("-> No autostart targets configured (pg autostart enable -i <name> / --backup / --pgbouncer / --postgrest / --etcd / --pgdog / --ha / --haproxy / --minio / --silo / --rustfs / --garage / --seaweedfs / --sqlite / --redis / --predixy / --pgadmin / --grafana / --prometheus / --alertmanager / --victoriametrics / --vmcluster / --vlcluster / --victorialogs / --vlagent / --gitea / --beegfs / --juicefs / --zerofs / --nginx)")
		return nil
	}

	var firstErr error
	ok := 0
	for _, name := range names {
		fmt.Printf("\n>>> starting instance %q (autostart) <<<\n", name)
		if err := startSingle(c, name); err != nil {
			fmt.Printf("  [X] %s: %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		ok++
	}
	if len(names) > 0 {
		fmt.Printf("\n>>> started %d/%d autostart instances <<<\n", ok, len(names))
	}

	// Shared backup container. When PITR is enabled, doStart already ensured
	// it per instance; the explicit call covers configs without PITR.
	if c.Backup.Autostart {
		bm, err := podman.NewBackupManager(c)
		if err != nil {
			fmt.Printf("  [X] backup autostart: %v\n", err)
			if firstErr == nil {
				firstErr = err
			}
		} else if err := bm.EnsureBackupInfra(); err != nil {
			fmt.Printf("  [X] backup autostart: %v\n", err)
			if firstErr == nil {
				firstErr = err
			}
		} else {
			fmt.Println("  [OK] backup container running (autostart)")
		}
	}

	// PgBouncer containers (local per-instance + remote).
	if err := startAutostartPgbouncers(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// PostgREST containers (local per-instance + remote). Right after
	// pgbouncer: both front the PG instances started above, and PostgREST
	// only needs its backend reachable — no ordering constraint with the LBs.
	if err := startAutostartPostgrests(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// etcd members (top-level infra addon).
	if err := startAutostartEtcds(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// pgdog proxies (top-level infra addon).
	if err := startAutostartPgDogs(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// Patroni HA members (top-level infra addon).
	if err := startAutostartPatroni(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// HAProxy load balancers (top-level infra addon). Last among the
	// PostgreSQL-facing addons: they front the Patroni members above, so their
	// backends exist by the time the listeners come up (HAProxy tolerates
	// backends being down anyway).
	if err := startAutostartHAProxies(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// MinIO instances (top-level infra addon). Independent of the PostgreSQL
	// stack — typically the backup repository, so bringing it up last is fine.
	if err := startAutostartMinios(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// silo instances (top-level infra addon, Pigsty's MinIO fork). Same
	// position as minio: independent of the PostgreSQL stack, a backup repo.
	if err := startAutostartSilos(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// rustfs instances (top-level infra addon, a Rust S3 store). Same position
	// as minio/silo: independent of the PostgreSQL stack, a backup repo.
	if err := startAutostartRustfs(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// garage instances (top-level infra addon, a Rust S3 store). Same position
	// as minio/silo/rustfs: independent of the PostgreSQL stack, a backup repo.
	if err := startAutostartGarage(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// seaweedfs instances (top-level infra addon, a Go S3 store). Same position
	// as minio/silo/rustfs/garage: a cluster member's filer metadata lives in a
	// pgcli instance, which is already up earlier in this chain.
	if err := startAutostartSeaweedfs(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// SQLite Litestream replication. Strictly after every object-storage addon
	// above: Litestream uploads to one of them, so a same-host backend is
	// listening when it starts. Start-only — the database and litestream.yml
	// already exist on disk.
	if err := startAutostartSQLite(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// redis instances (top-level infra addon, a KV store). Independent of the
	// PostgreSQL stack like the object stores, so it goes last.
	if err := startAutostartRedis(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// predixy proxies (top-level infra addon). Strictly after redis: the proxy
	// dials its cluster backends at startup, so a same-host cluster is up by the
	// time the listener comes over (a cross-host backend set is the operator's
	// own reachability concern — ordering cannot help there).
	if err := startAutostartPredixies(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// pgAdmin instances (top-level web addon). Independent of the PostgreSQL
	// stack like the other non-database addons — it is a browser UI that dials
	// its servers itself, so there is no ordering constraint at all.
	if err := startAutostartPgAdmin(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// Grafana instances (top-level web addon). Same as pgAdmin: a browser UI
	// with no dependency on the PostgreSQL stack, but still ahead of nginx below
	// since a dashboard is a plausible reverse-proxy backend.
	if err := startAutostartGrafana(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// Prometheus instances (top-level web addon, the metrics backend Grafana
	// reads from). Started after Grafana and before nginx: it has no dependency
	// on the PostgreSQL stack, but is a plausible reverse-proxy backend and the
	// natural companion to the dashboard above.
	if err := startAutostartPrometheus(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// Alertmanager instances (top-level web addon, the alert routing frontend of
	// the Prometheus stack). Started right after Prometheus and before nginx: no
	// dependency on the PostgreSQL stack, a plausible reverse-proxy backend, and
	// the natural companion to the collector above (v1 does not wire the two
	// together, but boot order anticipates a future link-up).
	if err := startAutostartAlertmanager(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// VictoriaMetrics instances (top-level web addon, the long-term metrics
	// store Grafana can also read from). Started after Prometheus — same role,
	// no dependency on the PostgreSQL stack, a plausible reverse-proxy backend.
	if err := startAutostartVictoriaMetrics(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// VMCluster instances (top-level cluster addon). Brings THIS host's role
	// containers up in dependency order (storage → insert → select); each host
	// only starts the roles it runs. No dependency on the PostgreSQL stack.
	if err := startAutostartVMClusters(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// VLCluster instances (top-level cluster addon). Same cluster-granular
	// model as VMCluster: only THIS host's role containers start, in
	// dependency order (storage → insert → select).
	if err := startAutostartVLClusters(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// VictoriaLogs instances (top-level web addon, the log database). Before
	// vlagent below so the agents' remote-write targets are listening first.
	if err := startAutostartVictoriaLogs(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// vlagent instances (top-level log agents). Strictly after VictoriaLogs:
	// the agent warns about an unreachable remote-write target at startup
	// (it buffers and replays, but boot order avoids the noise).
	if err := startAutostartVlagents(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// Gitea instances (top-level web addon, self-hosted Git). Started after the
	// monitoring stack and before nginx: it is a plausible reverse-proxy
	// backend, and with --dsn its external PostgreSQL (a managed instance, or
	// any reachable server) comes up earlier in this pass anyway.
	if err := startAutostartGitea(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// BeeGFS filesystems (top-level infra addon — the only multi-container
	// autostart): each filesystem's mgmtd → meta → storage come up in order.
	// Placed after the web addons — nothing on this pass depends on the
	// filesystem, and a client mount on this host re-attaches by itself.
	if err := startAutostartBeeGFS(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// Host-binary filesystem addons (juicefs, zerofs). These are the only
	// non-container addons: their "start" is a mount (juicefs) or a 9P server +
	// FUSE client (zerofs), launched as detached host processes. Both helpers are
	// idempotent — a volume already mounted is left alone — and neither re-formats
	// or re-creates anything. They run after the S3 addons (minio/silo/etc. above)
	// so the object store is listening before the filesystem tries to reach it.
	if err := startAutostartJuicefs(c); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := startAutostartZerofs(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// nginx instances (top-level web addon). Strictly after pgAdmin and
	// PostgREST: the proxy dials its backend services at startup, so same-host
	// backends exist by the time the listener comes up (nginx tolerates
	// backends being down anyway).
	if err := startAutostartNginx(c); err != nil && firstErr == nil {
		firstErr = err
	}

	return firstErr
}

func hasAutostartPgbouncer(c *config.Config) bool {
	for _, inst := range c.Instances {
		if inst.Addons.PgBouncer != nil && inst.Addons.PgBouncer.Autostart {
			return true
		}
	}
	for _, pb := range c.Addons.PgBouncer {
		if pb.Autostart {
			return true
		}
	}
	return false
}

func startAutostartPgbouncers(c *config.Config) error {
	pbMgr, err := podman.NewPgBouncerManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for name, inst := range c.Instances {
		if inst.Addons.PgBouncer != nil && inst.Addons.PgBouncer.Autostart {
			if err := pbMgr.StartContainer(inst.Addons.PgBouncer, name); err != nil {
				fmt.Printf("  [X] pgbouncer autostart (%s): %v\n", name, err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	for name, pb := range c.Addons.PgBouncer {
		if pb.Autostart {
			if err := pbMgr.StartContainer(&pb, name); err != nil {
				fmt.Printf("  [X] pgbouncer autostart (%s): %v\n", name, err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

func hasAutostartPostgrest(c *config.Config) bool {
	for _, inst := range c.Instances {
		if inst.Addons.Postgrest != nil && inst.Addons.Postgrest.Autostart {
			return true
		}
	}
	for _, pr := range c.Addons.Postgrest {
		if pr.Autostart {
			return true
		}
	}
	return false
}

func startAutostartPostgrests(c *config.Config) error {
	prMgr, err := podman.NewPostgrestManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for name, inst := range c.Instances {
		if inst.Addons.Postgrest != nil && inst.Addons.Postgrest.Autostart {
			if err := prMgr.StartContainer(inst.Addons.Postgrest); err != nil {
				fmt.Printf("  [X] postgrest autostart (%s): %v\n", name, err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	for name, pr := range c.Addons.Postgrest {
		if pr.Autostart {
			if err := prMgr.StartContainer(&pr); err != nil {
				fmt.Printf("  [X] postgrest autostart (%s): %v\n", name, err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

func hasAutostartEtcd(c *config.Config) bool {
	for _, ec := range c.Addons.Etcd {
		if ec.Autostart {
			return true
		}
	}
	return false
}

// startAutostartEtcds starts each autostart-enabled etcd member. Order is
// sorted by member name for deterministic, readable boot logs.
func startAutostartEtcds(c *config.Config) error {
	var names []string
	for name, ec := range c.Addons.Etcd {
		if ec.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	em, err := podman.NewEtcdManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		ec := c.Addons.Etcd[name]
		if err := em.StartContainer(&ec); err != nil {
			fmt.Printf("  [X] etcd autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func hasAutostartPgDog(c *config.Config) bool {
	for _, pd := range c.Addons.PgDog {
		if pd.Autostart {
			return true
		}
	}
	return false
}

// startAutostartPgDogs starts each autostart-enabled PgDog proxy. Order is
// sorted by proxy name for deterministic, readable boot logs.
func startAutostartPgDogs(c *config.Config) error {
	var names []string
	for name, pd := range c.Addons.PgDog {
		if pd.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	dm, err := podman.NewPgDogManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		pd := c.Addons.PgDog[name]
		if err := dm.StartContainer(&pd); err != nil {
			fmt.Printf("  [X] pgdog autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func hasAutostartPatroni(c *config.Config) bool {
	for _, cluster := range c.Addons.Patroni {
		for _, mb := range cluster.Members {
			if mb.Autostart && mb.RemoteHost == "" {
				return true
			}
		}
	}
	return false
}

// startAutostartPatroni starts each autostart-enabled Patroni member. Like the
// other infra autostarts this only brings the existing container up — it never
// re-renders patroni.yml or re-creates anything. Members whose DCS (etcd) is
// also autostart-managed will simply rejoin and re-elect normally; Patroni
// tolerates the DCS not being up yet by retrying, so member ordering relative
// to etcd does not need special handling. Scope and member names are sorted
// for deterministic, readable boot logs.
func startAutostartPatroni(c *config.Config) error {
	var scopes []string
	for scope, cluster := range c.Addons.Patroni {
		for _, mb := range cluster.Members {
			if mb.Autostart && mb.RemoteHost == "" {
				scopes = append(scopes, scope)
				break
			}
		}
	}
	if len(scopes) == 0 {
		return nil
	}
	sort.Strings(scopes)

	pm, err := podman.NewPatroniManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, scope := range scopes {
		cluster := c.Addons.Patroni[scope]
		var members []string
		for name, mb := range cluster.Members {
			if mb.Autostart && mb.RemoteHost == "" {
				members = append(members, name)
			}
		}
		sort.Strings(members)
		for _, name := range members {
			if err := pm.StartMemberContainer(&cluster, name); err != nil {
				fmt.Printf("  [X] patroni autostart (%s/%s): %v\n", scope, name, err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

// hasAutostartHAProxy reports whether any HAProxy instance is autostart-enabled.
func hasAutostartHAProxy(c *config.Config) bool {
	for _, hc := range c.Addons.HAProxy {
		if hc.Autostart {
			return true
		}
	}
	return false
}

// startAutostartHAProxies starts each autostart-enabled HAProxy instance. Like
// the other infra autostarts this is start-only: the container comes up reading
// the haproxy.cfg already on disk; nothing is re-rendered or re-derived. Order
// is sorted by instance name for deterministic, readable boot logs.
func startAutostartHAProxies(c *config.Config) error {
	var names []string
	for name, hc := range c.Addons.HAProxy {
		if hc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	hm, err := podman.NewHAProxyManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		hc := c.Addons.HAProxy[name]
		if err := hm.StartContainer(&hc); err != nil {
			fmt.Printf("  [X] haproxy autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartMinio reports whether any MinIO instance is autostart-enabled.
func hasAutostartMinio(c *config.Config) bool {
	for _, mc := range c.Addons.Minio {
		if mc.Autostart {
			return true
		}
	}
	return false
}

// startAutostartMinios starts each autostart-enabled MinIO instance. Start-only
// semantics like the other infra autostarts: the container comes up with the
// ports/credentials already in the config, recreating it if the container was
// removed. Order is sorted by instance name for deterministic, readable boot
// logs.
func startAutostartMinios(c *config.Config) error {
	var names []string
	for name, mc := range c.Addons.Minio {
		if mc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	mm, err := podman.NewMinioManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		mc := c.Addons.Minio[name]
		if err := mm.StartContainer(&mc); err != nil {
			fmt.Printf("  [X] minio autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartSilo reports whether any silo instance is autostart-enabled.
func hasAutostartSilo(c *config.Config) bool {
	for _, sc := range c.Addons.Silo {
		if sc.Autostart {
			return true
		}
	}
	return false
}

// startAutostartSilos starts each autostart-enabled silo instance. Start-only
// semantics like the other infra autostarts: the container comes up with the
// ports/credentials already in the config, recreating it if the container was
// removed. Order is sorted by instance name for deterministic, readable boot
// logs.
func startAutostartSilos(c *config.Config) error {
	var names []string
	for name, sc := range c.Addons.Silo {
		if sc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	sm, err := podman.NewSiloManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		sc := c.Addons.Silo[name]
		if err := sm.StartContainer(&sc); err != nil {
			fmt.Printf("  [X] silo autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartRustfs reports whether any rustfs instance is autostart-enabled.
func hasAutostartRustfs(c *config.Config) bool {
	for _, rc := range c.Addons.Rustfs {
		if rc.Autostart {
			return true
		}
	}
	return false
}

// startAutostartRustfs starts each autostart-enabled rustfs instance. Start-only
// semantics like the other infra autostarts: the container comes up with the
// ports/credentials already in the config, recreating it if the container was
// removed. Order is sorted by instance name for deterministic, readable boot
// logs. StartContainer re-applies the uid-10001 ownership before creating, so
// the boot-time start needs the same root-or-rootless-podman access as install.
func startAutostartRustfs(c *config.Config) error {
	var names []string
	for name, rc := range c.Addons.Rustfs {
		if rc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	rm, err := podman.NewRustfsManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		rc := c.Addons.Rustfs[name]
		if err := rm.StartContainer(&rc); err != nil {
			fmt.Printf("  [X] rustfs autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartGarage reports whether any garage instance is autostart-enabled.
func hasAutostartGarage(c *config.Config) bool {
	for _, gc := range c.Addons.Garage {
		if gc.Autostart {
			return true
		}
	}
	return false
}

// startAutostartGarage starts each autostart-enabled garage instance. Start-only
// semantics like the other object-store autostarts: StartContainer recreates
// from the config if the container was removed, re-rendering garage.toml; the
// seeded access key and bucket live in metadata_dir and survive that. Garage
// runs as root in the upstream image, so the boot-time start needs no special
// host privileges beyond running podman itself.
func startAutostartGarage(c *config.Config) error {
	var names []string
	for name, gc := range c.Addons.Garage {
		if gc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	gm, err := podman.NewGarageManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		gc := c.Addons.Garage[name]
		if err := gm.StartContainer(&gc); err != nil {
			fmt.Printf("  [X] garage autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartSeaweedfs reports whether any seaweedfs instance is autostart-enabled.
func hasAutostartSeaweedfs(c *config.Config) bool {
	for _, sc := range c.Addons.Seaweedfs {
		if sc.Autostart {
			return true
		}
	}
	return false
}

// startAutostartSeaweedfs starts each autostart-enabled seaweedfs instance.
// Start-only semantics like the other object-store autostarts: StartContainer
// re-renders s3.json (and filer.toml when a shared metadata DSN is set) and
// recreates from the config if the container was removed. A cluster member
// needs no orchestration — Raft recovers from the master's own -dir data and
// re-joins its peers; a member that boots alone simply waits for quorum, and
// weed tolerates its metadata database still being mid-startup by retrying.
func startAutostartSeaweedfs(c *config.Config) error {
	var names []string
	for name, sc := range c.Addons.Seaweedfs {
		if sc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	sm, err := podman.NewSeaweedfsManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		sc := c.Addons.Seaweedfs[name]
		if err := sm.StartContainer(&sc); err != nil {
			fmt.Printf("  [X] seaweedfs autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartSQLite reports whether any host-local SQLite database restarts
// its Litestream replication on boot.
func hasAutostartSQLite(c *config.Config) bool {
	for _, db := range c.Addons.SQLite {
		if db.Autostart {
			return true
		}
	}
	return false
}

// startAutostartSQLite restarts Litestream for each autostart-enabled database.
// A plain SQLite file has nothing to start, so replication must already be
// configured; startSQLiteReplication is idempotent and reports a missing
// litestream.yml with a re-enable hint. Sorted by name for deterministic logs.
func startAutostartSQLite(c *config.Config) error {
	var names []string
	for name, db := range c.Addons.SQLite {
		if db.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	var firstErr error
	for _, name := range names {
		db := c.Addons.SQLite[name]
		if db.Replication == nil {
			err := fmt.Errorf("replication is not configured")
			fmt.Printf("  [X] sqlite autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := startSQLiteReplication(sqlite.NewLayout(sqlite.BaseDir(c), db), db); err != nil {
			fmt.Printf("  [X] sqlite autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartRedis reports whether any redis instance is autostart-enabled.
func hasAutostartRedis(c *config.Config) bool {
	for _, rc := range c.Addons.Redis {
		if rc.Autostart {
			return true
		}
	}
	return false
}

// startAutostartRedis starts each autostart-enabled redis instance. Start-only
// semantics like the other infra autostarts: the container comes up with the
// port/password already in the config, recreating it if the container was
// removed; Redis replays the RDB snapshot from its data dir, so the dataset
// survives the reboot. Order is sorted by instance name for deterministic,
// readable boot logs.
func startAutostartRedis(c *config.Config) error {
	var names []string
	for name, rc := range c.Addons.Redis {
		if rc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	rm, err := podman.NewRedisManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		rc := c.Addons.Redis[name]
		if err := rm.StartContainer(&rc); err != nil {
			fmt.Printf("  [X] redis autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartPredixy reports whether any predixy instance is autostart-enabled.
func hasAutostartPredixy(c *config.Config) bool {
	for _, pc := range c.Addons.Predixy {
		if pc.Autostart {
			return true
		}
	}
	return false
}

// startAutostartPredixies starts each autostart-enabled predixy instance.
// Start-only like the other config-file addons: the container comes up reading
// the predixy.conf already on disk, recreating it if the container was removed —
// install first. Order is sorted by instance name for deterministic boot logs.
func startAutostartPredixies(c *config.Config) error {
	var names []string
	for name, pc := range c.Addons.Predixy {
		if pc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	pm, err := podman.NewPredixyManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		pc := c.Addons.Predixy[name]
		if err := pm.StartContainer(&pc); err != nil {
			fmt.Printf("  [X] predixy autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartPgAdmin reports whether any pgAdmin instance is autostart-enabled.
func hasAutostartPgAdmin(c *config.Config) bool {
	for _, ac := range c.Addons.PgAdmin {
		if ac.Autostart {
			return true
		}
	}
	return false
}

// startAutostartPgAdmin starts each autostart-enabled pgAdmin instance. Start-only
// like the other infra autostarts: the container comes up with the port/login
// credentials already in the config, recreating it if the container was removed.
// Its entrypoint re-chowns the bound data dir on every launch, so the boot-time
// start needs the same podman access as install. Order is sorted by instance name
// for deterministic, readable boot logs.
func startAutostartPgAdmin(c *config.Config) error {
	var names []string
	for name, ac := range c.Addons.PgAdmin {
		if ac.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	am, err := podman.NewPgAdminManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		ac := c.Addons.PgAdmin[name]
		if err := am.StartContainer(&ac); err != nil {
			fmt.Printf("  [X] pgadmin autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartGrafana reports whether any Grafana instance is autostart-enabled.
func hasAutostartGrafana(c *config.Config) bool {
	for _, gc := range c.Addons.Grafana {
		if gc.Autostart {
			return true
		}
	}
	return false
}

// startAutostartGrafana starts each autostart-enabled Grafana instance.
// Start-only like pgAdmin's: the container comes up with the port/login
// credentials already in the config, recreating it if the container was removed.
// Unlike pgAdmin the image's entrypoint does no ownership work — the data dir
// was pre-chowned to uid 472 at create time (a plain `podman start` of an
// existing container needs none), so the boot-time start needs the same podman
// access as install. Order is sorted by instance name for deterministic,
// readable boot logs.
func startAutostartGrafana(c *config.Config) error {
	var names []string
	for name, gc := range c.Addons.Grafana {
		if gc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	gm, err := podman.NewGrafanaManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		gc := c.Addons.Grafana[name]
		if err := gm.StartContainer(&gc); err != nil {
			fmt.Printf("  [X] grafana autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartPrometheus reports whether any Prometheus instance is autostart-enabled.
func hasAutostartPrometheus(c *config.Config) bool {
	for _, pc := range c.Addons.Prometheus {
		if pc.Autostart {
			return true
		}
	}
	return false
}

// startAutostartPrometheus starts each autostart-enabled Prometheus instance.
// Start-only like Grafana's: the container comes up reading the managed
// prometheus.yml already on disk, recreating it if the container was removed.
// The TSDB data dir was pre-chowned to uid/gid 65534 (nobody) at create time (a
// plain `podman start` of an existing container needs none), so the boot-time
// start needs the same podman access as install. Order is sorted by instance name
// for deterministic, readable boot logs.
func startAutostartPrometheus(c *config.Config) error {
	var names []string
	for name, pc := range c.Addons.Prometheus {
		if pc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	pm, err := podman.NewPrometheusManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		pc := c.Addons.Prometheus[name]
		if err := pm.StartContainer(&pc); err != nil {
			fmt.Printf("  [X] prometheus autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartAlertmanager reports whether any Alertmanager instance is autostart-enabled.
func hasAutostartAlertmanager(c *config.Config) bool {
	for _, ac := range c.Addons.Alertmanager {
		if ac.Autostart {
			return true
		}
	}
	return false
}

// startAutostartAlertmanager starts each autostart-enabled Alertmanager instance.
// Start-only like Prometheus's: the container comes up reading the managed
// alertmanager.yml already on disk, recreating it if the container was removed.
// The state data dir was pre-chowned to uid/gid 65534 (nobody) at create time (a
// plain `podman start` of an existing container needs none), so the boot-time
// start needs the same podman access as install. Order is sorted by instance name
// for deterministic, readable boot logs.
func startAutostartAlertmanager(c *config.Config) error {
	var names []string
	for name, ac := range c.Addons.Alertmanager {
		if ac.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	am, err := podman.NewAlertmanagerManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		ac := c.Addons.Alertmanager[name]
		if err := am.StartContainer(&ac); err != nil {
			fmt.Printf("  [X] alertmanager autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartGitea reports whether any Gitea instance is autostart-enabled.
func hasAutostartGitea(c *config.Config) bool {
	for _, gc := range c.Addons.Gitea {
		if gc.Autostart {
			return true
		}
	}
	return false
}

// startAutostartGitea starts each autostart-enabled Gitea instance. Start-only
// like Grafana's: the container comes up reading the app.ini already rendered
// into its /data volume, recreating the container if it was removed — install
// first. The data dir was pre-chowned to the image's git uid/gid 1000 at create
// time (a plain `podman start` of an existing container needs none), so the
// boot-time start needs the same podman access as install. Order is sorted by
// instance name for deterministic boot logs.
func startAutostartGitea(c *config.Config) error {
	var names []string
	for name, gc := range c.Addons.Gitea {
		if gc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	gm, err := podman.NewGiteaManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		gc := c.Addons.Gitea[name]
		if err := gm.StartContainer(&gc); err != nil {
			fmt.Printf("  [X] gitea autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartBeeGFS reports whether any BeeGFS filesystem is autostart-enabled.
func hasAutostartBeeGFS(c *config.Config) bool {
	for _, bc := range c.Addons.BeeGFS {
		if bc.Autostart {
			return true
		}
	}
	return false
}

// startAutostartBeeGFS starts each autostart-enabled filesystem. The only
// CLUSTER-granular autostart in pgcli: StartCluster brings one filesystem's
// three containers up in dependency order (mgmtd → meta → storage), starting
// existing containers and recreating only what is missing or broken. The
// services retry their own registration, so a boot pass does not block on logs.
// Order is sorted by filesystem name for deterministic boot logs.
func startAutostartBeeGFS(c *config.Config) error {
	var names []string
	for name, bc := range c.Addons.BeeGFS {
		if bc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	bm, err := podman.NewBeeGFSManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		bc := c.Addons.BeeGFS[name]
		if err := bm.StartCluster(&bc); err != nil {
			fmt.Printf("  [X] beegfs autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartNginx reports whether any nginx instance is autostart-enabled.
func hasAutostartNginx(c *config.Config) bool {
	for _, nc := range c.Addons.Nginx {
		if nc.Autostart {
			return true
		}
	}
	return false
}

// hasAutostartJuicefs / hasAutostartZerofs report whether any host-binary
// filesystem volume is autostart-enabled.
func hasAutostartJuicefs(c *config.Config) bool {
	for _, jc := range c.Addons.Juicefs {
		if jc.Autostart {
			return true
		}
	}
	return false
}

func hasAutostartZerofs(c *config.Config) bool {
	for _, zc := range c.Addons.Zerofs {
		if zc.Autostart {
			return true
		}
	}
	return false
}

// startAutostartJuicefs re-mounts each autostart-enabled JuiceFS volume. It
// never re-formats: the volume already exists in the metadata engine (created
// at install), so a boot pass is just `juicefs mount`, which is idempotent
// against an already-mounted point. Sorted by name for deterministic boot logs.
func startAutostartJuicefs(c *config.Config) error {
	var names []string
	for name, jc := range c.Addons.Juicefs {
		if jc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	var firstErr error
	for _, name := range names {
		jc := c.Addons.Juicefs[name]
		if isMountPoint(jc.MountPoint) {
			fmt.Printf("  [OK] juicefs %q already mounted\n", name)
			continue
		}
		if err := juicefsMount(jc.BinaryPath, &jc); err != nil {
			fmt.Printf("  [X] juicefs autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		fmt.Printf("  [OK] juicefs %q mounted at %s\n", name, jc.MountPoint)
	}
	return firstErr
}

// startAutostartZerofs brings each autostart-enabled ZeroFS volume up: the 9P
// server first (if not already running), then the FUSE client. Both launch
// paths are idempotent and, unlike install, never write config.toml or touch
// the encryption password — the config file on disk is the source of truth.
func startAutostartZerofs(c *config.Config) error {
	var names []string
	for name, zc := range c.Addons.Zerofs {
		if zc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	raiseOpenFileLimit()
	var firstErr error
	for _, name := range names {
		zc := c.Addons.Zerofs[name]
		if !hostbin.Alive(zc.ServerPidFile) {
			if err := hostbin.StartDetached(zc.BinaryPath, []string{"run", "-c", zc.ConfigPath}, zc.LogFile, zc.ServerPidFile); err != nil {
				fmt.Printf("  [X] zerofs autostart (%s): server: %v\n", name, err)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if err := waitForTCPPort(zc.Listen, zc.ListenPort, 10*time.Second); err != nil {
				fmt.Printf("  [X] zerofs autostart (%s): server did not come up (check %s)\n", name, zc.LogFile)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
		}
		if err := zerofsMount(zc.BinaryPath, &zc); err != nil {
			fmt.Printf("  [X] zerofs autostart (%s): mount: %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		fmt.Printf("  [OK] zerofs %q up (9P %s:%d, mounted at %s)\n", name, zc.Listen, zc.ListenPort, zc.MountPoint)
	}
	return firstErr
}

// startAutostartNginx starts each autostart-enabled nginx instance. Start-only
// like the other config-file addons: the container comes up reading the
// nginx.conf already on disk, recreating it if the container was removed —
// install first. Order is sorted by instance name for deterministic boot logs.
func startAutostartNginx(c *config.Config) error {
	var names []string
	for name, nc := range c.Addons.Nginx {
		if nc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	nm, err := podman.NewNginxManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		nc := c.Addons.Nginx[name]
		if err := nm.StartContainer(&nc); err != nil {
			fmt.Printf("  [X] nginx autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartVictoriaMetrics reports whether any VictoriaMetrics instance is
// autostart-enabled.
func hasAutostartVictoriaMetrics(c *config.Config) bool {
	for _, vc := range c.Addons.VictoriaMetrics {
		if vc.Autostart {
			return true
		}
	}
	return false
}

// startAutostartVictoriaMetrics starts each autostart-enabled VictoriaMetrics
// instance. Start-only like the other web addons: the container comes up with
// the flags already in the config, recreating it if it was removed. The image
// runs as root, so no ownership work is needed. Order is sorted by instance
// name for deterministic boot logs.
func startAutostartVictoriaMetrics(c *config.Config) error {
	var names []string
	for name, vc := range c.Addons.VictoriaMetrics {
		if vc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	vm, err := podman.NewVictoriaMetricsManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		vc := c.Addons.VictoriaMetrics[name]
		if err := vm.StartContainer(&vc); err != nil {
			fmt.Printf("  [X] victoriametrics autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartVMCluster reports whether any VMCluster instance is
// autostart-enabled.
func hasAutostartVMCluster(c *config.Config) bool {
	for _, vc := range c.Addons.VMCluster {
		if vc.Autostart {
			return true
		}
	}
	return false
}

// startAutostartVMClusters starts each autostart-enabled VMCluster instance.
// Cluster-granular like BeeGFS: StartCluster brings up THIS host's role
// containers in dependency order (storage → insert → select), starting existing
// containers and recreating only what is missing. Order is sorted by instance
// name for deterministic boot logs.
func startAutostartVMClusters(c *config.Config) error {
	var names []string
	for name, vc := range c.Addons.VMCluster {
		if vc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	vm, err := podman.NewVMClusterManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		vc := c.Addons.VMCluster[name]
		if err := vm.StartCluster(&vc); err != nil {
			fmt.Printf("  [X] vmcluster autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartVLCluster reports whether any VLCluster instance is
// autostart-enabled.
func hasAutostartVLCluster(c *config.Config) bool {
	for _, vc := range c.Addons.VLCluster {
		if vc.Autostart {
			return true
		}
	}
	return false
}

// startAutostartVLClusters starts each autostart-enabled VLCluster instance.
// Like VMCluster, StartCluster brings up THIS host's role containers in
// dependency order (storage → insert → select), starting existing containers
// and recreating only what is missing. Order is sorted by instance name for
// deterministic boot logs.
func startAutostartVLClusters(c *config.Config) error {
	var names []string
	for name, vc := range c.Addons.VLCluster {
		if vc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	vlc, err := podman.NewVLClusterManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		vc := c.Addons.VLCluster[name]
		if err := vlc.StartCluster(&vc); err != nil {
			fmt.Printf("  [X] vlcluster autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartVictoriaLogs reports whether any VictoriaLogs instance is
// autostart-enabled.
func hasAutostartVictoriaLogs(c *config.Config) bool {
	for _, lc := range c.Addons.VictoriaLogs {
		if lc.Autostart {
			return true
		}
	}
	return false
}

// startAutostartVictoriaLogs starts each autostart-enabled VictoriaLogs
// instance. Start-only like the other web addons: the container comes up with
// the flags already in the config, recreating it if it was removed. The image
// runs as root, so no ownership work is needed. Order is sorted by instance
// name for deterministic boot logs.
func startAutostartVictoriaLogs(c *config.Config) error {
	var names []string
	for name, lc := range c.Addons.VictoriaLogs {
		if lc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	lm, err := podman.NewVictoriaLogsManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		lc := c.Addons.VictoriaLogs[name]
		if err := lm.StartContainer(&lc); err != nil {
			fmt.Printf("  [X] victorialogs autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// hasAutostartVlagent reports whether any vlagent instance is autostart-enabled.
func hasAutostartVlagent(c *config.Config) bool {
	for _, vc := range c.Addons.Vlagent {
		if vc.Autostart {
			return true
		}
	}
	return false
}

// startAutostartVlagents starts each autostart-enabled vlagent instance.
// Start-only like the other web addons: the container comes up with the flags
// already in the config, recreating it if it was removed. File-collector
// checkpoints and remote-write buffers live in the data dir, so a rebooted
// agent resumes where it stopped. The image runs as root, so no ownership work
// is needed. Order is sorted by instance name for deterministic boot logs.
func startAutostartVlagents(c *config.Config) error {
	var names []string
	for name, vc := range c.Addons.Vlagent {
		if vc.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	vm, err := podman.NewVlagentManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		vc := c.Addons.Vlagent[name]
		if err := vm.StartContainer(&vc); err != nil {
			fmt.Printf("  [X] vlagent autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// startAllInstances starts every instance listed in the config file.
func startAllInstances() error {
	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	c, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if len(c.Instances) == 0 {
		return fmt.Errorf("no instances configured in %s", path)
	}
	cfgPath = path

	var firstErr error
	ok := 0
	for name := range c.Instances {
		fmt.Printf("\n>>> starting instance %q <<<\n", name)
		if err := startSingle(c, name); err != nil {
			fmt.Printf("  [X] %s: %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		ok++
	}
	fmt.Printf("\n>>> started %d/%d instances <<<\n", ok, len(c.Instances))
	return firstErr
}

// startInstance starts the instance specified by -i (default "default").
func startInstance() error {
	if err := loadConfig(); err != nil {
		return err
	}
	return doStart(cfg)
}

// startSingle starts a specific instance from a shared config.
func startSingle(c *config.Config, name string) error {
	cfgInstance = name
	if err := c.SetInstance(name); err != nil {
		return err
	}
	return doStart(c)
}

// doStart performs the actual start workflow.
func doStart(c *config.Config) error {
	fmt.Println("=== pg start ===")
	fmt.Printf("Platform: %s\n", platform.Detect())

	// 1. Check dependencies
	fmt.Println("\n-> Checking dependencies...")
	missing := platform.MissingPrereqs()
	if len(missing) > 0 {
		for _, d := range missing {
			fmt.Printf("  [X] %s: %s\n", d.Name, d.Hint)
		}
		return fmt.Errorf("missing dependencies, please install them first")
	}
	fmt.Println("  [OK] podman available")

	// 2. Initialize podman manager
	pm, err := podman.New(c)
	if err != nil {
		return err
	}

	// 3. podman machine (macOS, no-op on Linux)
	if err := pm.EnsureMachine(); err != nil {
		return err
	}

	// 4. Build image (if missing)
	if err := pm.EnsureImage(); err != nil {
		return err
	}

	// 5. Create directories (if missing)
	if err := pm.EnsureDirs(); err != nil {
		return err
	}

	// 6. Ensure shared network
	if err := pm.EnsureNetwork(); err != nil {
		return err
	}

	// 7. Replica instances: initialize data via pg_basebackup before the
	// container is created (skips initdb). No-op for primaries and on
	// subsequent starts.
	if err := pm.EnsureReplica(); err != nil {
		return err
	}

	// 8. Create and start container
	if err := pm.EnsureContainer(); err != nil {
		return err
	}

	// 8a. EnsurePGPortProxy is a no-op on Unix platforms.
	pm.EnsurePGPortProxy()

	// Wait for PostgreSQL to finish initialization (initdb + init scripts).
	fmt.Println("-> Waiting for PostgreSQL to be ready...")
	for i := 0; i < 60; i++ {
		if ready, _ := pm.PGIsReady(); ready {
			break
		}
		if cs, _ := pm.Status(); cs != nil && !cs.Running && strings.HasPrefix(strings.ToLower(cs.Status), "exited") {
			return fmt.Errorf("PostgreSQL container exited during startup (status: %s); data directory may be corrupted -- run 'pg restore -i %s --time \"...\"' to recover", cs.Status, c.Instance)
		}
		time.Sleep(time.Second)
	}

	// 7a. Ensure .pgpass exists for the postgres OS user inside the container.
	// This is needed by pg_cron and other background workers that connect via
	// TCP as localhost. The file is idempotent — overwrites with current creds.
	if err := pm.EnsurePgpass(); err != nil {
		fmt.Printf("  [!] .pgpass warning: %v\n", err)
	}

	// 7b. Apply performance tuning to postgresql.conf inside the running
	// container.  Done here (after PG is ready) so podman exec works.
	// If restart-required params changed, restart the container once more and
	// wait for PG to come back up.
	if needsRestart, err := pm.ApplyPGTuning(); err != nil {
		fmt.Printf("  [!] pg_tuning warning: %v\n", err)
	} else if needsRestart {
		fmt.Println("-> Restarting PostgreSQL to apply shared_buffers / wal_buffers changes...")
		if err := pm.StopContainer(); err != nil {
			fmt.Printf("  [!] stop container for restart: %v\n", err)
		} else if err := pm.StartContainer(); err != nil {
			fmt.Printf("  [!] start container after restart: %v\n", err)
		} else {
			fmt.Println("-> Waiting for PostgreSQL to be ready (after restart)...")
			for i := 0; i < 60; i++ {
				if ready, _ := pm.PGIsReady(); ready {
					fmt.Println("  [OK] PostgreSQL ready")
					break
				}
				time.Sleep(time.Second)
			}
		}
	}

	// 7c. Apply extensions: install packages, manage shared_preload_libraries,
	// CREATE EXTENSION if needed. If shared_preload_libraries changed, restart
	// the container and wait for PG to come back up.
	if len(c.Instances[c.Instance].Extensions) > 0 {
		if err := applyExtensions(pm, c); err != nil {
			fmt.Printf("  [!] extensions warning: %v\n", err)
		}
	}

	// 8. Initialize pgBackRest stanza (via backup container)
	if c.PITR.Enabled {
		bm, err := podman.NewBackupManager(c)
		if err != nil {
			return fmt.Errorf("creating backup manager: %w", err)
		}

		if _, err := bm.EnsureSSHKey(); err != nil {
			return fmt.Errorf("backup ssh key: %w", err)
		}

		fmt.Println("-> Authorizing backup key on PostgreSQL container...")
		if err := bm.AuthorizeKeyOnInstance(); err != nil {
			fmt.Printf("  [!] backup key authorization warning: %v\n", err)
		}

		fmt.Println("-> Ensuring backup infrastructure is ready...")
		if err := bm.EnsureBackupInfra(); err != nil {
			return fmt.Errorf("backup infrastructure: %w", err)
		}

		pt := pitr.New(c, pm, bm)
		if err := pt.EnsureStanza(); err != nil {
			fmt.Printf("  [!] stanza create warning: %v\n", err)
		}

		stanza := c.PITR.PgBackRestStanza
		archiveCmd := fmt.Sprintf("pgbackrest --stanza=%s archive-push %%p", stanza)

		// archive_mode is a postmaster-level parameter — needs restart.
		// Set it together with archive_command, then restart if either changed.
		if _, err := pm.Exec("psql", "-U", c.Postgres.User, "-d", c.Postgres.Database, "-c",
			"ALTER SYSTEM SET archive_mode = on"); err != nil {
			fmt.Printf("  [!] setting archive_mode: %v\n", err)
		}
		if _, err := pm.Exec("psql", "-U", c.Postgres.User, "-d", c.Postgres.Database, "-c",
			fmt.Sprintf("ALTER SYSTEM SET archive_command TO '%s'", archiveCmd)); err != nil {
			fmt.Printf("  [!] setting archive_command: %v\n", err)
		} else {
			fmt.Println("-> archive_command configured")
		}

		// archive_mode requires a restart (not just reload). Check if it's
		// already active; if not, restart the container.
		modeOut, _ := pm.Exec("psql", "-U", c.Postgres.User, "-d", c.Postgres.Database,
			"-t", "-A", "-c", "SHOW archive_mode")
		if strings.TrimSpace(modeOut) != "on" {
			fmt.Println("-> Restarting PostgreSQL to enable archive_mode...")
			pm.StopContainer()
			pm.StartContainer()
			for i := 0; i < 60; i++ {
				if ready, _ := pm.PGIsReady(); ready {
					break
				}
				time.Sleep(time.Second)
			}
		} else {
			pm.Exec("psql", "-U", c.Postgres.User, "-d", c.Postgres.Database, "-c", "SELECT pg_reload_conf()")
		}
		pm.Exec("psql", "-U", c.Postgres.User, "-d", c.Postgres.Database, "-c", "SELECT pg_switch_wal()")

		if err := pt.CheckStanza(); err != nil {
			fmt.Printf("  [!] stanza check warning: %v\n", err)
		}

		fmt.Println("-> Waiting for WAL archiver to catch up...")
		for i := 0; i < 30; i++ {
			time.Sleep(2 * time.Second)
			out, err := pm.Exec("psql", "-U", c.Postgres.User, "-d", c.Postgres.Database, "-t", "-c",
				"SELECT count(*) FROM pg_ls_dir('pg_wal/archive_status') AS f WHERE f LIKE '%.ready'")
			if err == nil && strings.TrimSpace(out) == "0" {
				fmt.Println("  [OK] WAL archiver caught up")
				break
			}
		}
	}

	if err := c.Save(cfgPath); err != nil {
		fmt.Printf("  [!] failed to save config: %v\n", err)
	}

	fmt.Println("\nOK started")
	// Password redacted but the DSN shape kept: `start` runs under the boot
	// service, so anything printed here lands in the (persistent, readable)
	// systemd journal. A passwordless "user@host" URI would read as "no
	// password needed", so mask it as :***@ instead. Get the real DSN from
	// `pg status` or pg.yaml.
	fmt.Printf("  PostgreSQL: postgres://%s:***@localhost:%d/%s\n",
		c.Postgres.User, c.Postgres.Port, c.Postgres.Database)
	return nil
}

// applyExtensions manages shared_preload_libraries and runs CREATE EXTENSION
// for managed extensions. Extension packages are baked into the image by
// `pg extension install` (BuildExtensionImage), so start only needs to keep
// postgresql.conf and pg_extension in sync.
func applyExtensions(pm *podman.Manager, c *config.Config) error {
	inst := c.Instances[c.Instance]
	if len(inst.Extensions) == 0 {
		return nil
	}

	// Apply shared_preload_libraries (idempotent: no-op if already set)
	needsRestart, err := pm.ApplyExtensions(inst.Extensions)
	if err != nil {
		return fmt.Errorf("apply extensions: %w", err)
	}

	if needsRestart {
		fmt.Println("-> Restarting PostgreSQL to apply shared_preload_libraries changes...")
		if err := pm.StopContainer(); err != nil {
			return fmt.Errorf("stop container for restart: %w", err)
		}
		if err := pm.StartContainer(); err != nil {
			return fmt.Errorf("start container after restart: %w", err)
		}
		fmt.Println("-> Waiting for PostgreSQL to be ready (after restart)...")
		for i := 0; i < 60; i++ {
			if ready, _ := pm.PGIsReady(); ready {
				fmt.Println("  [OK] PostgreSQL ready")
				break
			}
			time.Sleep(time.Second)
		}
	}

	// CREATE EXTENSION IF NOT EXISTS for each managed extension.
	// Skip for replica instances: they are read-only and extensions are
	// replicated from the primary via pg_basebackup + WAL streaming.
	if !pm.IsReplica() {
		if err := pm.RunCreateExtensions(inst.Extensions); err != nil {
			return fmt.Errorf("create extensions: %w", err)
		}
	}

	return nil
}
