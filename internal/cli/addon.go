package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
	"github.com/mars-base/pgcli/internal/tlsca"
)

// ---------------------------------------------------------------------------
// Parent command
// ---------------------------------------------------------------------------

var addonCmd = &cobra.Command{
	Use:   "addon",
	Short: "Manage add-on components",
	Long: `Manage add-on components (connection poolers, etc.).

Add-ons are sidecar containers that provide additional capabilities
for PostgreSQL instances without modifying the database itself.

Two modes (pgbouncer, postgrest):
  Local:  pg addon install pgbouncer -i <instance>
          Stored under instances.<name>.addons in config.

  Remote: pg addon install pgbouncer --dsn <dsn> --pg-name <name>
          Stored under top-level addons in config.
          --pg-name is required to identify this remote addon.

Infra addons (shared, not tied to one instance):
  pg addon install etcd
          Stored under top-level addons.etcd in config.
  pg addon install pgdog
          Stored under top-level addons.pgdog in config.
  pg addon install haproxy
          Stored under top-level addons.haproxy in config (Linux only).
  pg addon install minio
          Stored under top-level addons.minio in config (Linux and macOS).
  pg addon install silo
          Stored under top-level addons.silo in config (Linux and macOS).
          Pigsty's MinIO fork — the same S3 store, from the public
          docker.io/pgsty/silo image (its own mcli client comes with it; see
          "pg mcli").
  pg addon install rustfs
          Stored under top-level addons.rustfs in config (Linux only).
          A Rust S3-compatible object store, run from pgcli's own wrapper image
          (ghcr.io/mars-base/pgcli/pgcli-rustfs). Upstream rustfs runs as a fixed
          uid 10001; the wrapper handles that inside the container (it chowns its
          own bind dirs and drops privileges), so pgcli never touches host file
          ownership. Three topologies — SNSD / SNMD / MNMD — but
          no multi-node single-drive mode, and every drive must sit on its own
          physical device.
  pg addon install redis
          Stored under top-level addons.redis in config (Linux and macOS).
          A standalone Redis (KV store for cache, session, ranking and
          atomic-counter data) run from the plain upstream docker.io/library/redis
          image — no wrapper, no pgcli build. --version 7|8 picks the major
          (each maps to a pinned patch tag); a requirepass password is generated
          on first install and persisted, so the default 0.0.0.0 bind is always
          authenticated. Data is an RDB snapshot under the addon's data dir, kept
          across remove/reinstall. Single-node only: sentinel/cluster are out of
          scope.
  pg addon install postgrest
          Exposes a PostgreSQL schema as a REST API (single stateless
          container, dual mode like pgbouncer): local -i fronting an
          instance, or remote --dsn fronting any PG endpoint (direct
          instance, PgBouncer, or a Patroni cluster behind its HAProxy
          listener). Stored under instances.<name>.addons.postgrest (local)
          or addons.postgrest.<name> (remote).

Commands:
  pg addon install <addon>   install an add-on
  pg addon list              list all installed add-ons
  pg addon start <addon>     start an installed but stopped add-on
  pg addon stop <addon>      stop a running add-on (keeps config)
  pg addon remove <addon>    remove an add-on`,
}

// ---------------------------------------------------------------------------
// install
// ---------------------------------------------------------------------------

var addonInstallCmd = &cobra.Command{
	Use:   "install <addon>",
	Short: "Install an add-on for a local or remote PostgreSQL instance",
	Long: `Install an add-on sidecar container for a PostgreSQL instance.

Currently supported add-ons:
  pgbouncer   connection pooler (transaction mode)
  etcd        standalone key-value store (HA cluster DCS)
  pgdog       Postgres proxy (pooling, load balancing, sharding)
  haproxy     TCP load balancer in front of a Patroni cluster (unified or read/write split)
  minio       single-node S3-compatible object storage (web console included; Linux host network, macOS bridge)
  silo        MinIO's Pigsty fork — same S3 object storage, from the public docker.io/pgsty/silo image (console + mcli client bundled; Linux host network, macOS bridge)
  rustfs      Rust S3-compatible object storage from pgcli's wrapper image, ghcr.io/mars-base/pgcli/pgcli-rustfs (console included; Linux only; the fixed upstream uid 10001 is handled inside the container, three topologies SNSD/SNMD/MNMD, each drive on its own physical device)
  redis       standalone KV store (cache/session/ranking/counters) from the plain upstream docker.io/library/redis image; --version 7|8 selects the major, requirepass auto-generated, RDB persistence, read replicas via --replica-of (no automatic failover)
  postgrest   stateless REST API in front of a PostgreSQL schema (single container; Linux host network, macOS bridge)

Two modes (pgbouncer, postgrest):
  Local:  pg addon install pgbouncer -i <instance>
  Remote: pg addon install pgbouncer --dsn <dsn> --pg-name <name>

Infra addon (etcd — shared, not tied to an instance):
  pg addon install etcd [--name ha] [--client-port N] [--peer-port N]
                        [--image ...] [--data-dir ...]

Infra addon (pgdog — shared Postgres proxy):
  pg addon install pgdog [--name proxy] [--backend NAME=HOST:PORT:DB[:SHARD[:ROLE]]]
                         [--user NAME:PASSWORD[:DBNAME]] [--sharded-table DB:TABLE:COLUMN:TYPE]
                         [--port N] [--pool-mode ...] [--workers N] [--default-pool-size N]

Infra addon (haproxy — TCP load balancer in front of a Patroni cluster, Linux only):
  pg addon install haproxy [--name lb] [--ha <scope>] [--node NAME=HOST:PGPORT:RESTPORT]
                           [--mode unified|split] [--rw-port N] [--ro-port N] [--stats-port N]
                           [--max-lag 1MB]
  Members are injected once via --node, or auto-derived from a Patroni scope via
  --ha. After adding or removing a member ("pg ha create" / "pg ha remove"),
  re-run install to re-sync the backend list.

Infra addon (minio — single-node S3-compatible object storage, Linux and macOS;
            silo — its Pigsty fork, identical flags, same shared port pool;
            rustfs — a Rust S3 store, Linux only, same shared port pool):
  pg addon install minio [--name store] [--api-port N] [--console-port N]
                         [--listen 127.0.0.1] [--root-user admin] [--data-dir ...] [--force]
  pg addon install silo  [--name store] [--api-port N] [--console-port N]
                         [--listen 127.0.0.1] [--root-user admin] [--data-dir ...] [--force]
  pg addon install rustfs [--name store] [--api-port N] [--console-port N]
                         [--listen 127.0.0.1] [--root-user admin] [--data-dir ...] [--force]
  Root credentials are generated on first install, printed once for the record,
  and stored in the config (root_user / root_password under
  addons.minio.<name> / addons.silo.<name> / addons.rustfs.<name>);
  the web console is at http://<listen>:<console-port>/ (on macOS the store
  serves on the bridge with the ports published, so the Mac reaches both on
  127.0.0.1). An already-present container is reused (a stopped one is
  started); pass --force to recreate it after changing ports, listen, or
  credentials. rustfs has three topologies — single-node single-drive (default
  --data-dir), single-node multi-drive (--drive, each on its own physical
  device), and multi-node multi-drive (--drive + one --endpoint per node); it
  has NO multi-node single-drive mode. It runs as uid 10001, so pgcli chowns
  its data and TLS dirs for that user (directly as root, or via "podman
  unshare chown" under a rootless setup).

Infra addon (redis — standalone KV store for cache/session/ranking/counters,
Linux and macOS; the first version-selectable addon):
  pg addon install redis [--name cache] [--version 7|8] [--port N]
                         [--listen 0.0.0.0] [--password ...] [--maxmemory 256mb]
                         [--maxmemory-policy noeviction] [--aof]
                         [--appendfsync always|everysec|no] [--save "900 1 300 10"|no]
                         [--replica-of <masterName>]
                         [--replica-of-host <ip> --replica-of-port <N> --password ...]
                         [--data-dir ...] [--image ...] [--force]
  --version chooses the major; each maps to a pinned upstream tag (see
  docs/images.md) and defaults to "8" when omitted. --image overrides the tag
  verbatim (and --version is then just a display label, reverse-parsed from the
  tag). The port is drawn from its own pool (redis_start_port, default 6379).
  A requirepass password is generated on first install, printed once, and stored
  under addons.redis.<name>; pass --password to pin it yourself. --maxmemory
  caps the dataset and defaults to allkeys-lru eviction (a real cache); pair it
  with --maxmemory-policy noeviction for a hard ceiling, or leave it empty for
  no cap. Persistence is the native RDB snapshot into --data-dir (default
  <base_dir>/addon/redis/<name>/data), so remove/reinstall revives the data;
  --aof adds the write-ahead log on top (--appendfsync tunes its flush), and
  --save overrides the snapshot schedule or "no" disables snapshots entirely.
  Listen defaults to 0.0.0.0 — requirepass is always on, so pass
  --listen 127.0.0.1 to keep it loopback-only.
  --replica-of <masterName> turns the instance into a read-only replica of a
  local master addon (same major required; it borrows the master's password and
  endpoint). For a cross-host master use --replica-of-host/--replica-of-port
  with --password equal to the master's. Replicas serve reads only — writes are
  rejected READONLY — and there is no automatic failover: promote with
  "pg redis-cli --name <r> replicaof no one".
  Talk to it with "pg redis-cli" (a short-lived redis-cli container wired to the
  first redis addon; see that command's help).

PostgREST (stateless REST API in front of a schema; dual mode like pgbouncer,
Linux and macOS):
  Local:  pg addon install postgrest -i <instance> [--schema api] [--db-pool N] [--anon-role r] [--jwt-secret s]
  Remote: pg addon install postgrest --dsn <dsn> --pg-name <name> [--schema api] [--anon-role r] [--jwt-secret s]
          The --dsn works against any PG endpoint — a direct instance, a
          PgBouncer pool, or a Patroni cluster behind its HAProxy listener
          (prefer the LB: a member's direct port loses writes on failover).
  PostgREST is env-configured (PGRST_*) and holds no data dir. It does NOT
  touch the database: the login role, the --anon-role/--jwt-secret roles, and
  their GRANTs are yours (or your migrations'). --anon-role serves
  unauthenticated requests; --jwt-secret enables Authorization: Bearer JWTs
  whose 'role' claim runs the request. Without either, every request is 401.
  After a schema change run NOTIFY pgrst, 'reload schema'.
  Re-running install reuses a live container; --force recreates it to apply a
  changed --dsn/--port/--listen/--db-pool/--schema/--anon-role/--jwt-secret.

Re-running install is idempotent — it re-syncs all users and passwords from
pg_shadow, regenerates config files and restarts the container.

Examples:
  pg addon install pgbouncer -i proj01
  pg addon install pgbouncer --dsn "postgres://admin:pass@host:35432/proj01_db" --pg-name remote-proj01
  pg addon install etcd
  pg addon install etcd --name ha --client-port 2379 --peer-port 2380
  pg addon install pgdog --backend app=127.0.0.1:5432:appdb
  pg addon install pgdog --backend app=127.0.0.1:5432:shard0:0 --backend app=127.0.0.1:5433:shard1:1 --user alice:s3cret:app
  pg addon install haproxy --ha app
  pg addon install haproxy --name lb --mode split --ha app --max-lag 1MB
  pg addon install haproxy --node node1=10.0.0.11:35532:8008 --node node2=10.0.0.11:35533:8009
  pg addon install minio --name store --data-dir /srv/minio
  pg addon install silo --name store --tls
  pg addon install rustfs --name store --tls
  pg addon install rustfs --name store --drive /mnt/rustfs/d0 --drive /mnt/rustfs/d1 --drive /mnt/rustfs/d2 --drive /mnt/rustfs/d3 --tls
  pg addon install redis
  pg addon install redis --name cache --version 7 --maxmemory 256mb
  pg addon install redis --name session --listen 127.0.0.1
  pg addon install redis --name cache-r --replica-of cache
  pg addon install redis --name remote-r --replica-of-host 10.0.0.9 --replica-of-port 6379 --password <master-pw>
  pg addon install postgrest -i proj01 --schema api --anon-role web_anon
  pg addon install postgrest --dsn "postgres://api:pass@127.0.0.1:5000/appdb" --pg-name app-api --schema api`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonInstall(args[0], cmd)
	},
}

// ---------------------------------------------------------------------------
// list
// ---------------------------------------------------------------------------

var addonListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all installed add-ons (local and remote)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonList()
	},
}

// ---------------------------------------------------------------------------
// remove
// ---------------------------------------------------------------------------

var addonRemoveCmd = &cobra.Command{
	Use:   "remove <addon>",
	Short: "Remove an add-on",
	Long: `Remove an add-on sidecar container and its configuration.

For local add-ons, use -i to specify the instance.
For remote add-ons, use --pg-name to specify the pooler name.

Infra add-ons (etcd/pgdog/haproxy/minio/silo/rustfs/redis) use --name.
Remove keeps the data directory; pass --clean-data for object-storage/KV data
to go too.

Examples:
  pg addon remove pgbouncer -i proj01
  pg addon remove pgbouncer --pg-name remote-proj01
  pg addon remove postgrest -i proj01
  pg addon remove postgrest --pg-name app-api
  pg addon remove redis --name cache
  pg addon remove redis --name cache --clean-data`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonRemove(args[0], cmd)
	},
}

// ---------------------------------------------------------------------------
// start / stop
// ---------------------------------------------------------------------------

var addonStartCmd = &cobra.Command{
	Use:   "start <addon>",
	Short: "Start an installed add-on container",
	Long: `Start an add-on container that is installed but not running (e.g. after a
host reboot or a manual stop). This brings the existing container up without
regenerating config or re-registering cluster members.

Supported add-ons:
  etcd       pg addon start etcd [--name m1]
  pgdog      pg addon start pgdog [--name proxy]
  haproxy    pg addon start haproxy [--name lb]
  minio      pg addon start minio [--name store]
  silo       pg addon start silo [--name store]
  rustfs     pg addon start rustfs [--name store]
  redis      pg addon start redis [--name cache]
  pgbouncer  pg addon start pgbouncer -i <instance>
             pg addon start pgbouncer --pg-name <remote-name>
  postgrest  pg addon start postgrest -i <instance>
             pg addon start postgrest --pg-name <remote-name>

Examples:
  pg addon start etcd --name m1
  pg addon start pgdog
  pg addon start haproxy --name lb
  pg addon start minio --name store
  pg addon start silo --name store
  pg addon start rustfs --name store
  pg addon start redis --name cache
  pg addon start pgbouncer -i proj01
  pg addon start postgrest --pg-name app-api`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonStart(args[0], cmd)
	},
}

var addonStopCmd = &cobra.Command{
	Use:   "stop <addon>",
	Short: "Stop a running add-on container",
	Long: `Stop an add-on container without removing it or its configuration. Bring it
back up later with 'pg addon start <addon>'.

Supported add-ons:
  etcd       pg addon stop etcd [--name m1]
  pgdog      pg addon stop pgdog [--name proxy]
  haproxy    pg addon stop haproxy [--name lb]
  minio      pg addon stop minio [--name store]
  silo       pg addon stop silo [--name store]
  rustfs     pg addon stop rustfs [--name store]
  redis      pg addon stop redis [--name cache]
  pgbouncer  pg addon stop pgbouncer -i <instance>
             pg addon stop pgbouncer --pg-name <remote-name>
  postgrest  pg addon stop postgrest -i <instance>
             pg addon stop postgrest --pg-name <remote-name>

Examples:
  pg addon stop etcd --name m1
  pg addon stop pgdog
  pg addon stop haproxy --name lb
  pg addon stop minio --name store
  pg addon stop silo --name store
  pg addon stop rustfs --name store
  pg addon stop pgbouncer -i proj01
  pg addon stop postgrest --pg-name app-api`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonStop(args[0], cmd)
	},
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

func init() {
	rootCmd.AddCommand(addonCmd)
	addonCmd.AddCommand(addonInstallCmd, addonListCmd, addonRemoveCmd, addonStartCmd, addonStopCmd)

	// Basic flags
	addonInstallCmd.Flags().String("dsn", "", "PG instance connection string for remote mode (postgres://user:pass@host:port/db)")
	addonInstallCmd.Flags().String("pg-name", "", "name to identify a remote PgBouncer or PostgREST (required with --dsn)")
	addonInstallCmd.Flags().Int("max-client-conn", 0, "maximum number of client connections allowed (default 100)")
	addonInstallCmd.Flags().Int("default-pool-size", 0, "number of server connections per user/database pair (default 20)")

	// Pool sizing
	addonInstallCmd.Flags().Int("min-pool-size", 0, "minimum number of server connections to maintain (default 0)")
	addonInstallCmd.Flags().Int("reserve-pool-size", 0, "extra connections for burst traffic (default 0)")
	addonInstallCmd.Flags().Int("reserve-pool-timeout", 0, "seconds before using reserve pool (default 5)")
	addonInstallCmd.Flags().Int("max-db-connections", 0, "max server connections per database (0=unlimited)")
	addonInstallCmd.Flags().Int("max-user-connections", 0, "max server connections per user (0=unlimited)")

	// Timeouts (in seconds)
	addonInstallCmd.Flags().Int("server-idle-timeout", 0, "close idle server connections after N seconds (default 600)")
	addonInstallCmd.Flags().Int("server-lifetime", 0, "max lifetime for server connections in seconds (default 3600)")
	addonInstallCmd.Flags().Int("server-connect-timeout", 0, "timeout for connecting to PG in seconds (default 15)")
	addonInstallCmd.Flags().Int("query-timeout", 0, "max query execution time in seconds (default 0=disabled)")
	addonInstallCmd.Flags().Int("query-wait-timeout", 0, "max time to wait for server connection in seconds (default 120)")
	addonInstallCmd.Flags().Int("idle-transaction-timeout", 0, "close idle transactions after N seconds (default 0=disabled)")
	addonInstallCmd.Flags().Int("transaction-timeout", 0, "max transaction duration in seconds (default 0=disabled)")

	// Admin access
	addonInstallCmd.Flags().String("admin-users", "", "comma-separated list of admin users for console access")
	addonInstallCmd.Flags().String("stats-users", "", "comma-separated list of users for read-only stats access")

	// Logging
	addonInstallCmd.Flags().Int("log-connections", 0, "log client connections (default 1=enabled)")
	addonInstallCmd.Flags().Int("log-disconnections", 0, "log client disconnections (default 1=enabled)")

	addonRemoveCmd.Flags().String("pg-name", "", "name of a remote PgBouncer or PostgREST to remove")

	// etcd flags (top-level shared-infrastructure addon)
	addonInstallCmd.Flags().String("name", "", "addon key/name for the etcd member, pgdog proxy, minio, silo, rustfs or redis instance (default \"etcd\"/\"pgdog\"/\"minio\"/\"silo\"/\"rustfs\"/\"redis\")")
	addonInstallCmd.Flags().Int("client-port", 0, "etcd client host port (0=auto-assign from etcd_start_port)")
	addonInstallCmd.Flags().Int("peer-port", 0, "etcd peer host port (0=auto-assign, next free port after client)")
	addonInstallCmd.Flags().String("image", "", "override the addon image tag verbatim (etcd default quay.io/coreos/etcd:v3.5.30; redis default resolved from --version, e.g. docker.io/library/redis:8.10.2)")
	addonInstallCmd.Flags().String("cluster", "", "etcd cluster name (--initial-cluster-token, default \"pgcli-etcd\")")
	addonInstallCmd.Flags().String("data-dir", "", "data directory for an etcd cluster root, a MinIO, silo, rustfs or redis instance, absolute or relative to base_dir (etcd default <base_dir>/addon/etcd, each member uses <root>/<name>/data; minio default <base_dir>/addon/minio/<name>/data; silo default <base_dir>/addon/silo/<name>/data; rustfs default <base_dir>/addon/rustfs/<name>/data; redis default <base_dir>/addon/redis/<name>/data)")
	addonInstallCmd.Flags().String("advertise-host", "", "host advertised in this member's peer/client URLs (empty=127.0.0.1 for single-host; set a LAN IP or FQDN for cross-host clusters)")
	addonInstallCmd.Flags().String("join", "", "client endpoint of an existing cluster member to join cross-host, e.g. http://10.0.0.12:2379 (implies --initial-cluster-state existing; requires --advertise-host)")
	addonRemoveCmd.Flags().String("name", "", "name of the etcd member, pgdog proxy, haproxy, minio, silo or rustfs instance to remove (default \"etcd\"/\"pgdog\"/\"haproxy\"/\"minio\"/\"silo\"/\"rustfs\")")
	addonRemoveCmd.Flags().Bool("clean-data", false, "also delete the MinIO/silo/rustfs/redis data directory (object storage / backup repository / key-value data)")

	// haproxy flags (top-level load balancer in front of a Patroni cluster)
	addonInstallCmd.Flags().String("mode", "", "HAProxy routing mode: unified (default, all traffic to the leader) or split (separate read listener for replicas)")
	addonInstallCmd.Flags().String("ha", "", "Patroni scope to derive backend members from (auto: every local member's pg + restapi port); mutually exclusive with --node")
	addonInstallCmd.Flags().StringArray("node", nil, "HAProxy backend node NAME=HOST:PGPORT:RESTPORT (repeatable; PGPORT is the routed backend, RESTPORT the health-check port)")
	addonInstallCmd.Flags().Int("rw-port", 0, "HAProxy read-write listener host port (0=auto-assign from haproxy_start_port)")
	addonInstallCmd.Flags().Int("ro-port", 0, "HAProxy read-only listener host port, split mode only (0=auto-assign, next free port)")
	addonInstallCmd.Flags().Int("stats-port", 0, "HAProxy stats page host port (0=auto-assign, next free port)")
	addonInstallCmd.Flags().String("max-lag", "", "replica lag threshold for the read listener, e.g. 1MB (split mode; empty=no filter)")

	// minio flags (top-level single-node S3-compatible object storage)
	addonInstallCmd.Flags().Int("api-port", 0, "MinIO/silo/rustfs S3 API host port (0=auto-assign from the shared minio_start_port pool)")
	addonInstallCmd.Flags().Int("console-port", 0, "MinIO/silo/rustfs web console host port (0=auto-assign, next free port)")
	addonInstallCmd.Flags().String("listen", "", "bind address for the haproxy listeners / MinIO, silo or rustfs server / PostgREST HTTP server (default \"127.0.0.1\"; 0.0.0.0 exposes them on the network) — Redis defaults to \"0.0.0.0\" instead, since its requirepass is always set; pass 127.0.0.1 to keep Redis loopback-only")
	addonInstallCmd.Flags().String("root-user", "", "MinIO/silo/rustfs root user (default \"admin\"; the root password is generated on first install, printed once, and stored in the config)")
	addonInstallCmd.Flags().String("root-password", "", "MinIO/silo/rustfs root password (generated on first install if omitted; pass the SAME value on every node of a distributed cluster so all pg.yaml files share one credential without copying it by hand)")
	addonInstallCmd.Flags().StringSlice("endpoint", nil, "MinIO/silo/rustfs distributed-mode endpoint(s), e.g. --endpoint http://10.0.0.1:9000/data (MinIO/silo: path is the in-container export dir — /data with a plain --data-dir node, or /data1../dataN on a --drive node (MNMD); repeat for every node's every drive. rustfs: one endpoint per node — scheme://host:port, no path; it has no multi-node single-drive mode so --drive is required, and it derives the /data/rustfs0../rustfsN suffix itself. The list AND root credentials must match every node's pg.yaml — enables cluster mode; omit for single-node)")
	addonInstallCmd.Flags().StringSlice("drive", nil, "MinIO/silo/rustfs multi-drive host dir(s), each on its own disk — e.g. --drive /mnt/minio/disk1 --drive /mnt/minio/disk2 ... (repeat for each drive; alone this is single-node multi-drive (SNMD); combined with --endpoint it is one node of a multi-node multi-drive cluster (MNMD), and each endpoint addresses this node's drive slots (/data1../dataN for MinIO/silo, /data/rustfs0../rustfsN for rustfs). Mutually exclusive with --data-dir. MinIO/silo reject a drive sharing the root device; rustfs hard-requires every drive on its own physical device)")
	addonInstallCmd.Flags().Bool("tls", false, "MinIO/silo/rustfs: serve HTTPS via pgcli's self-signed CA (certs generated under <base_dir>/tls/<minio|silo|rustfs>/<name>/; hand ca.crt to pgBackRest as backup.repo.s3.ca_file). Required for a store used as a pgBackRest S3 repo — pgBackRest refuses plaintext HTTP. Changing this needs --force to recreate")
	addonInstallCmd.Flags().String("tls-cert", "", "MinIO/silo/rustfs: serve HTTPS with THIS certificate file instead of the generated self-signed one (PEM leaf + any intermediate chain; mounted read-only as public.crt for MinIO/silo, rustfs_cert.pem for rustfs). Implies --tls. Renew by replacing the file then --force to recreate (a single-file mount pins the source inode). A public-CA cert needs no --s3-ca-file on clients; a private-CA one passes its chain/CA there")
	addonInstallCmd.Flags().String("tls-key", "", "MinIO/silo/rustfs: private key for --tls-cert (PEM; mounted read-only as private.key for MinIO/silo, rustfs_key.pem for rustfs). Must pair with the cert; both are required to enable BYO TLS")
	addonInstallCmd.Flags().Bool("force", false, "recreate the MinIO/silo/rustfs/redis/PostgREST container even if one already exists (to apply changed ports/listen/credentials, or a changed PostgREST --dsn/--db-pool/--schema)")

	// redis flags (top-level standalone KV store — the first version-selectable addon)
	addonInstallCmd.Flags().String("version", "", "Redis major version to install: 7 or 8 (default \"8\"); each maps to a pinned upstream docker.io/library/redis tag. Ignored for every other addon")
	addonInstallCmd.Flags().String("password", "", "Redis requirepass password (generated on first install if omitted; pass it explicitly to pin the value across reinstalls)")
	addonInstallCmd.Flags().String("maxmemory", "", "Redis memory cap, e.g. 256mb or 2gb — turns the instance into a real cache (allkeys-lru evicts keys at the cap); empty means no cap")
	addonInstallCmd.Flags().String("maxmemory-policy", "", "Redis eviction policy to pair with --maxmemory (noeviction | allkeys-lru | allkeys-lfu | volatile-lru | volatile-lfu | volatile-ttl; default allkeys-lru; requires --maxmemory)")
	addonInstallCmd.Flags().Bool("aof", false, "Redis: enable AOF (appendonly yes) — a write-ahead log on top of the RDB snapshot, for crash-safe no-eviction stores; off by default")
	addonInstallCmd.Flags().String("appendfsync", "", "Redis AOF flush strength: always (durability) | everysec (default) | no (throughput); requires --aof")
	addonInstallCmd.Flags().String("save", "", `Redis RDB snapshot schedule, e.g. "900 1 300 10" (empty keeps Redis's own default; "no" disables snapshots, e.g. for an AOF-only instance)`)
	addonInstallCmd.Flags().String("replica-of", "", "Redis: make this instance a read-only replica of the named local master addon (its host/port/password are resolved from the config; the master must already be installed and on the same major)")
	addonInstallCmd.Flags().String("replica-of-host", "", "Redis: address of a remote master to replicate (cross-host; pair with --replica-of-port and --password matching the master's; mutually exclusive with --replica-of)")
	addonInstallCmd.Flags().Int("replica-of-port", 0, "Redis: remote master's port, paired with --replica-of-host")

	// start / stop flags
	addonStartCmd.Flags().String("name", "", "name of the etcd member, pgdog proxy, haproxy, minio, silo, rustfs or redis instance to start (default \"etcd\"/\"pgdog\"/\"haproxy\"/\"minio\"/\"silo\"/\"rustfs\"/\"redis\")")
	addonStartCmd.Flags().String("pg-name", "", "name of a remote PgBouncer or PostgREST to start")
	addonStopCmd.Flags().String("name", "", "name of the etcd member, pgdog proxy, haproxy, minio, silo, rustfs or redis instance to stop (default \"etcd\"/\"pgdog\"/\"haproxy\"/\"minio\"/\"silo\"/\"rustfs\"/\"redis\")")
	addonStopCmd.Flags().String("pg-name", "", "name of a remote PgBouncer or PostgREST to stop")

	// pgdog flags (top-level shared Postgres proxy addon)
	addonInstallCmd.Flags().Int("port", 0, "PgDog client host port (0=auto-assign from pgdog_start_port; openmetrics takes the next free port) / PostgREST HTTP host port (0=auto-assign from postgrest_start_port) / Redis host port (0=auto-assign from redis_start_port, default 6379)")
	addonInstallCmd.Flags().String("host", "", "PgDog listen address (default 127.0.0.1)")
	addonInstallCmd.Flags().String("pool-mode", "", "PgDog pooler mode: transaction (default) or session")
	addonInstallCmd.Flags().Int("workers", 0, "PgDog worker threads (default 2)")
	addonInstallCmd.Flags().StringArray("backend", nil, "backend database NAME=HOST:PORT:DBNAME[:SHARD[:ROLE]] (repeatable)")
	addonInstallCmd.Flags().StringArray("user", nil, "proxy user NAME:PASSWORD[:DBNAME] (repeatable; DBNAME defaults to the first backend's name)")
	addonInstallCmd.Flags().StringArray("sharded-table", nil, "sharded table DBNAME:TABLE:COLUMN:DATA_TYPE (repeatable)")

	// postgrest flags (proxy-type addon: stateless REST API in front of any PG endpoint)
	addonInstallCmd.Flags().Int("db-pool", 0, "PostgREST: connections in its internal pool toward the backend (PGRST_DB_POOL; 0=PostgREST's own default 10) — total backend connections = instances × db-pool")
	addonInstallCmd.Flags().String("schema", "", "PostgREST: exposed schema(s), comma-separated (PGRST_DB_SCHEMAS; default \"public\") — which schema to serve as REST")
	addonInstallCmd.Flags().String("anon-role", "", "PostgREST: role unauthenticated requests run as (PGRST_DB_ANON_ROLE) — a NOINHERIT login role with GRANTs on the exposed schema; empty disables anonymous access (JWT only)")
	addonInstallCmd.Flags().String("jwt-secret", "", "PostgREST: JWT secret (PGRST_JWT_SECRET) — enables Authorization: Bearer requests whose 'role' claim SET ROLEs a NOINHERIT role with grants on the exposed schema; empty disables JWT auth")
}

// ---------------------------------------------------------------------------
// install logic
// ---------------------------------------------------------------------------

func runAddonInstall(addonName string, cmd *cobra.Command) error {
	switch addonName {
	case "etcd":
		return runAddonInstallEtcd(cmd)
	case "pgdog":
		return runAddonInstallPgDog(cmd)
	case "haproxy":
		return runAddonInstallHAProxy(cmd)
	case "minio":
		return runAddonInstallMinio(cmd)
	case "silo":
		return runAddonInstallSilo(cmd)
	case "rustfs":
		return runAddonInstallRustfs(cmd)
	case "redis":
		return runAddonInstallRedis(cmd)
	case "postgrest":
		return runAddonInstallPostgrest(cmd)
	case "pgbouncer":
		// falls through to the PgBouncer flow below
	default:
		return fmt.Errorf("unknown addon: %s (available: pgbouncer, etcd, pgdog, haproxy, minio, silo, rustfs, redis, postgrest)", addonName)
	}

	dsn, _ := cmd.Flags().GetString("dsn")
	pgName, _ := cmd.Flags().GetString("pg-name")

	// Validate mutual exclusivity: --dsn/--pg-name vs -i
	if dsn != "" || pgName != "" {
		if dsn == "" {
			return fmt.Errorf("--pg-name requires --dsn")
		}
		if pgName == "" {
			return fmt.Errorf("--dsn requires --pg-name to identify this remote pooler")
		}
		if err := checkDSNInstanceConflict(cmd); err != nil {
			return err
		}
	}

	// 1. Load config
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

	// 2. Determine DSN and storage key
	var instName string // name used for config storage and config file directory
	if dsn != "" {
		// Remote mode: --dsn + --pg-name
		instName = pgName
	} else {
		// Local mode: -i
		if _, ok := cfg.Instances[cfgInstance]; !ok {
			return fmt.Errorf("instance %q not found in config", cfgInstance)
		}
		cfg.SetInstance(cfgInstance)
		dsn = cfg.GetPostgresURL()
		instName = cfgInstance
	}

	// 3. Verify connectivity
	pm, err := podman.New(cfg)
	if err != nil {
		return fmt.Errorf("podman: %w", err)
	}
	// macOS: bring up the podman machine and the pgcli-net bridge that the
	// pgbouncer container joins (no-ops on Linux).
	if err := pm.EnsureMachine(); err != nil {
		return err
	}
	if err := pm.EnsureNetwork(); err != nil {
		return err
	}
	fmt.Println("-> Checking PG connectivity...")
	if err := pm.CheckDSNReachable(dsn); err != nil {
		return err
	}

	// 4. Setup auth_query user and function on PG
	pbMgr, err := podman.NewPgBouncerManager(cfg)
	if err != nil {
		return fmt.Errorf("pgbouncer manager: %w", err)
	}

	// 5. Get or allocate PgBouncer config (needed before SetupAuth for auth user name)
	// Read optional override flags
	maxClientConn, _ := cmd.Flags().GetInt("max-client-conn")
	defaultPoolSize, _ := cmd.Flags().GetInt("default-pool-size")
	maxClientConnChanged := cmd.Flags().Changed("max-client-conn")
	defaultPoolSizeChanged := cmd.Flags().Changed("default-pool-size")

	// Pool sizing flags
	minPoolSize, _ := cmd.Flags().GetInt("min-pool-size")
	reservePoolSize, _ := cmd.Flags().GetInt("reserve-pool-size")
	maxDBConnections, _ := cmd.Flags().GetInt("max-db-connections")
	maxUserConnections, _ := cmd.Flags().GetInt("max-user-connections")

	// Timeout flags
	serverIdleTimeout, _ := cmd.Flags().GetInt("server-idle-timeout")
	serverLifetime, _ := cmd.Flags().GetInt("server-lifetime")
	serverConnectTimeout, _ := cmd.Flags().GetInt("server-connect-timeout")
	queryTimeout, _ := cmd.Flags().GetInt("query-timeout")
	queryWaitTimeout, _ := cmd.Flags().GetInt("query-wait-timeout")
	idleTransactionTimeout, _ := cmd.Flags().GetInt("idle-transaction-timeout")
	transactionTimeout, _ := cmd.Flags().GetInt("transaction-timeout")

	// Admin and logging flags
	adminUsers, _ := cmd.Flags().GetString("admin-users")
	statsUsers, _ := cmd.Flags().GetString("stats-users")
	logConnections, _ := cmd.Flags().GetInt("log-connections")
	logDisconnections, _ := cmd.Flags().GetInt("log-disconnections")

	var pbConf config.PgBouncerConfig
	if dsn != "" && pgName != "" {
		// Remote mode: store in top-level addons.pgbouncer.<pgName>
		if cfg.Addons.PgBouncer == nil {
			cfg.Addons.PgBouncer = make(map[string]config.PgBouncerConfig)
		}
		existing, ok := cfg.Addons.PgBouncer[pgName]
		if ok {
			pbConf = existing
		} else {
			pbConf = config.PgBouncerConfig{
				ContainerName:   "pgcli-pgbouncer" + nsSuffixCLI(cfg.Namespace) + "-" + pgName,
				ImageTag:        config.DefaultPgBouncerImageTag,
				PoolMode:        "transaction",
				DSN:             dsn,
				MaxClientConn:   100,
				DefaultPoolSize: 20,
			}
		}
		// Apply overrides if flags were explicitly set
		if maxClientConnChanged {
			pbConf.MaxClientConn = maxClientConn
		}
		if defaultPoolSizeChanged {
			pbConf.DefaultPoolSize = defaultPoolSize
		}
		// Pool sizing
		if cmd.Flags().Changed("min-pool-size") {
			pbConf.MinPoolSize = minPoolSize
		}
		if cmd.Flags().Changed("reserve-pool-size") {
			pbConf.ReservePoolSize = reservePoolSize
		}
		if cmd.Flags().Changed("max-db-connections") {
			pbConf.MaxDBConnections = maxDBConnections
		}
		if cmd.Flags().Changed("max-user-connections") {
			pbConf.MaxUserConnections = maxUserConnections
		}
		// Timeouts
		if cmd.Flags().Changed("server-idle-timeout") {
			pbConf.ServerIdleTimeout = serverIdleTimeout
		}
		if cmd.Flags().Changed("server-lifetime") {
			pbConf.ServerLifetime = serverLifetime
		}
		if cmd.Flags().Changed("server-connect-timeout") {
			pbConf.ServerConnectTimeout = serverConnectTimeout
		}
		if cmd.Flags().Changed("query-timeout") {
			pbConf.QueryTimeout = queryTimeout
		}
		if cmd.Flags().Changed("query-wait-timeout") {
			pbConf.QueryWaitTimeout = queryWaitTimeout
		}
		if cmd.Flags().Changed("idle-transaction-timeout") {
			pbConf.IdleTransactionTimeout = idleTransactionTimeout
		}
		if cmd.Flags().Changed("transaction-timeout") {
			pbConf.TransactionTimeout = transactionTimeout
		}
		// Admin and logging
		if cmd.Flags().Changed("admin-users") {
			pbConf.AdminUsers = adminUsers
		}
		if cmd.Flags().Changed("stats-users") {
			pbConf.StatsUsers = statsUsers
		}
		if cmd.Flags().Changed("log-connections") {
			pbConf.LogConnections = logConnections
		}
		if cmd.Flags().Changed("log-disconnections") {
			pbConf.LogDisconnections = logDisconnections
		}
		cfg.Addons.PgBouncer[pgName] = pbConf
	} else {
		// Local mode: store in instances.<name>.addons.pgbouncer
		inst := cfg.Instances[instName]
		if inst.Addons.PgBouncer == nil {
			inst.Addons.PgBouncer = &config.PgBouncerConfig{
				ContainerName:   "pgcli-pgbouncer" + nsSuffixCLI(cfg.Namespace) + "-" + instName,
				ImageTag:        config.DefaultPgBouncerImageTag,
				PoolMode:        "transaction",
				MaxClientConn:   100,
				DefaultPoolSize: 20,
			}
		}
		// Apply overrides if flags were explicitly set
		if maxClientConnChanged {
			inst.Addons.PgBouncer.MaxClientConn = maxClientConn
		}
		if defaultPoolSizeChanged {
			inst.Addons.PgBouncer.DefaultPoolSize = defaultPoolSize
		}
		// Pool sizing
		if cmd.Flags().Changed("min-pool-size") {
			inst.Addons.PgBouncer.MinPoolSize = minPoolSize
		}
		if cmd.Flags().Changed("reserve-pool-size") {
			inst.Addons.PgBouncer.ReservePoolSize = reservePoolSize
		}
		if cmd.Flags().Changed("max-db-connections") {
			inst.Addons.PgBouncer.MaxDBConnections = maxDBConnections
		}
		if cmd.Flags().Changed("max-user-connections") {
			inst.Addons.PgBouncer.MaxUserConnections = maxUserConnections
		}
		// Timeouts
		if cmd.Flags().Changed("server-idle-timeout") {
			inst.Addons.PgBouncer.ServerIdleTimeout = serverIdleTimeout
		}
		if cmd.Flags().Changed("server-lifetime") {
			inst.Addons.PgBouncer.ServerLifetime = serverLifetime
		}
		if cmd.Flags().Changed("server-connect-timeout") {
			inst.Addons.PgBouncer.ServerConnectTimeout = serverConnectTimeout
		}
		if cmd.Flags().Changed("query-timeout") {
			inst.Addons.PgBouncer.QueryTimeout = queryTimeout
		}
		if cmd.Flags().Changed("query-wait-timeout") {
			inst.Addons.PgBouncer.QueryWaitTimeout = queryWaitTimeout
		}
		if cmd.Flags().Changed("idle-transaction-timeout") {
			inst.Addons.PgBouncer.IdleTransactionTimeout = idleTransactionTimeout
		}
		if cmd.Flags().Changed("transaction-timeout") {
			inst.Addons.PgBouncer.TransactionTimeout = transactionTimeout
		}
		// Admin and logging
		if cmd.Flags().Changed("admin-users") {
			inst.Addons.PgBouncer.AdminUsers = adminUsers
		}
		if cmd.Flags().Changed("stats-users") {
			inst.Addons.PgBouncer.StatsUsers = statsUsers
		}
		if cmd.Flags().Changed("log-connections") {
			inst.Addons.PgBouncer.LogConnections = logConnections
		}
		if cmd.Flags().Changed("log-disconnections") {
			inst.Addons.PgBouncer.LogDisconnections = logDisconnections
		}
		cfg.Instances[instName] = inst
	}

	// Set BackendHost from DSN
	if host, port, _, _, _, err := podman.ParseDSN(dsn); err == nil {
		if dsn != "" && pgName != "" {
			pbConf := cfg.Addons.PgBouncer[pgName]
			pbConf.BackendHost = fmt.Sprintf("%s:%d", host, port)
			cfg.Addons.PgBouncer[pgName] = pbConf
		} else {
			inst := cfg.Instances[instName]
			inst.Addons.PgBouncer.BackendHost = fmt.Sprintf("%s:%d", host, port)
			cfg.Instances[instName] = inst
		}
	}

	// Let config auto-assign port if zero
	cfg.ApplyDefaults()

	// Re-fetch pointer after ApplyDefaults (map values are copied)
	if dsn != "" && pgName != "" {
		pbConf = cfg.Addons.PgBouncer[pgName]
	} else {
		pbConf = *cfg.Instances[instName].Addons.PgBouncer
	}

	// 6. Setup auth_query user and function on PG (needs pbConf for auth user name)
	fmt.Println("-> Setting up PgBouncer auth_query...")
	authUser, err := pbMgr.SetupAuth(dsn, instName)
	if err != nil {
		return err
	}
	fmt.Printf("  [OK] auth user %q configured\n", authUser.User)

	// 7. Generate config files
	fmt.Println("-> Generating PgBouncer configuration...")
	iniPath, userListPath, err := pbMgr.WriteConfigs(&pbConf, authUser, dsn, instName)
	if err != nil {
		return err
	}
	fmt.Printf("  [OK] pgbouncer.ini: %s\n", iniPath)
	fmt.Printf("  [OK] userlist.txt:  %s\n", userListPath)

	// 7. Ensure container is running (create or restart)
	fmt.Println("-> Starting PgBouncer container...")
	if err := pbMgr.EnsureContainer(iniPath, userListPath, &pbConf); err != nil {
		return err
	}

	// 8. Save config
	if dsn != "" && pgName != "" {
		cfg.Addons.PgBouncer[pgName] = pbConf
	} else {
		inst := cfg.Instances[instName]
		inst.Addons.PgBouncer = &pbConf
		cfg.Instances[instName] = inst
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// 9. Print result
	fmt.Println()
	fmt.Printf("✓ PgBouncer installed for %q\n", instName)
	fmt.Printf("  PgBouncer port: %d\n", pbConf.HostPort)
	fmt.Printf("  Pool mode:      %s\n", pbConf.PoolMode)
	fmt.Printf("  Max clients:    %d\n", pbConf.MaxClientConn)
	fmt.Printf("  Pool size:      %d\n", pbConf.DefaultPoolSize)
	fmt.Println()
	fmt.Printf("  Connect via PgBouncer:\n")
	fmt.Printf("    postgres://user:pass@127.0.0.1:%d/<database>\n", pbConf.HostPort)
	return nil
}

// ---------------------------------------------------------------------------
// install logic — etcd
// ---------------------------------------------------------------------------

// runAddonInstallEtcd installs a standalone single-member etcd container as a
// top-level (shared infrastructure) addon. Unlike pgbouncer it is not tied to
// a specific instance and needs no DSN/auth setup.
func runAddonInstallEtcd(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "etcd"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	clientPort, _ := cmd.Flags().GetInt("client-port")
	peerPort, _ := cmd.Flags().GetInt("peer-port")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	cluster, _ := cmd.Flags().GetString("cluster")
	advertiseHost, _ := cmd.Flags().GetString("advertise-host")
	joinEndpoint, _ := cmd.Flags().GetString("join")

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

	if joinEndpoint != "" {
		if advertiseHost == "" {
			return fmt.Errorf("--join requires --advertise-host so existing members can reach this new member over the network")
		}
		if cluster == "" {
			return fmt.Errorf("--join requires --cluster (must match the remote cluster's --initial-cluster-token)")
		}
	}

	if cfg.Addons.Etcd == nil {
		cfg.Addons.Etcd = make(map[string]config.EtcdConfig)
	}
	existing, ok := cfg.Addons.Etcd[name]
	if !ok {
		existing = config.EtcdConfig{
			ContainerName: "pgcli-etcd" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	if clientPort != 0 {
		existing.ClientPort = clientPort
	}
	if peerPort != 0 {
		existing.PeerPort = peerPort
	}
	if dataDir != "" {
		base := cfg.BaseDir
		if base == "" {
			base = platform.DefaultConfigDir()
		}
		existing.DataDir = resolveUnderBase(base, dataDir)
	}
	if existing.Name == "" {
		existing.Name = name
	}
	if cluster != "" {
		existing.ClusterName = cluster
	}
	if advertiseHost != "" {
		existing.AdvertiseHost = advertiseHost
	}
	cfg.Addons.Etcd[name] = existing

	// Fill defaults and auto-assign any unset ports.
	cfg.ApplyDefaults()
	ec := cfg.Addons.Etcd[name]

	em, err := podman.NewEtcdManager(cfg)
	if err != nil {
		return fmt.Errorf("etcd manager: %w", err)
	}

	peerURL := ec.PeerURL()

	switch {
	case joinEndpoint != "":
		// Cross-host join: no coordinator container on this machine. Use
		// etcdctl from a short-lived container to register with the remote
		// coordinator, then start with the authoritative initial-cluster it
		// prints (already reflects every existing member's advertised URL,
		// plus this new one).
		initialCluster, err := joinViaEndpoint(em, ec, joinEndpoint, peerURL)
		if err != nil {
			return err
		}
		if err := em.EnsureContainer(&ec, initialCluster, "existing"); err != nil {
			return err
		}

	default:
		// Local-cluster path: peers are pgcli-managed members in the same
		// config. First member bootstraps; later ones join via a running
		// peer container.
		var peers []config.EtcdConfig
		for k, v := range cfg.Addons.Etcd {
			if k != name && v.ClusterName == ec.ClusterName {
				peers = append(peers, v)
			}
		}
		sort.Slice(peers, func(i, j int) bool { return peers[i].Name < peers[j].Name })

		initialCluster, state := bootstrapCluster(ec, peers)

		if len(peers) == 0 {
			fmt.Println("-> Bootstrapping etcd cluster...")
		} else {
			coord, ok := pickCoordinator(em, ec, peers)
			if !ok {
				return fmt.Errorf("cluster %q has no running member to join; start one of the existing etcd members first", ec.ClusterName)
			}
			registered, err := em.MemberExists(coord.ContainerName, coord.ClientPort, ec.Name)
			if err != nil {
				return fmt.Errorf("querying etcd members: %w", err)
			}
			if !registered {
				fmt.Println("-> Registering new member with the cluster...")
				if err := em.MemberAdd(coord.ContainerName, coord.ClientPort, ec.Name, peerURL); err != nil {
					return err
				}
			}
			fmt.Println("-> Starting etcd container (joining cluster)...")
		}

		if err := em.EnsureContainer(&ec, initialCluster, state); err != nil {
			return err
		}
	}

	cfg.Addons.Etcd[name] = ec
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	fmt.Printf("✓ etcd installed: %q\n", name)
	fmt.Printf("  Container:    %s\n", ec.ContainerName)
	fmt.Printf("  Cluster:      %s\n", ec.ClusterName)
	fmt.Printf("  Data dir:     %s\n", em.DataDir(&ec))
	fmt.Printf("  Client port:  %d\n", ec.ClientPort)
	fmt.Printf("  Peer port:    %d\n", ec.PeerPort)
	fmt.Printf("  Advertise:    %s\n", ec.AdvertiseAddr())
	fmt.Println()
	fmt.Printf("  Client URL: %s\n", ec.ClientURL())
	fmt.Printf("  Connect (etcdctl): ETCDCTL_ENDPOINTS=%s\n", ec.ClientURL())
	return nil
}

// joinViaEndpoint registers a new member with a coordinator reachable at
// coordinatorEndpoint (cross-host or a non-pgcli-managed peer) and returns the
// authoritative ETCD_INITIAL_CLUSTER from etcdctl's response, to be passed to
// the joining container. Idempotent in the other direction: it errors when the
// member name is already registered, since re-adding would fail and the caller
// should remove the stale member first.
func joinViaEndpoint(em *podman.EtcdManager, ec config.EtcdConfig, coordinatorEndpoint, peerURL string) (string, error) {
	registered, err := em.MemberExistsAt(ec.ImageTag, coordinatorEndpoint, ec.Name)
	if err != nil {
		return "", err
	}
	if registered {
		return "", fmt.Errorf("member %q is already registered with the cluster at %s — remove it first with `pg etcdctl member remove` against that endpoint", ec.Name, coordinatorEndpoint)
	}
	fmt.Printf("-> Registering member with the cluster at %s...\n", coordinatorEndpoint)
	out, err := em.MemberAddAt(ec.ImageTag, coordinatorEndpoint, ec.Name, peerURL)
	if err != nil {
		return "", err
	}
	initialCluster, ok := parseInitialCluster(out)
	if !ok {
		return "", fmt.Errorf("coordinator response did not contain ETCD_INITIAL_CLUSTER:\n%s", out)
	}
	fmt.Println("-> Starting etcd container (joining cluster)...")
	return initialCluster, nil
}

// parseInitialCluster extracts the ETCD_INITIAL_CLUSTER value from the
// `etcdctl member add` response, e.g.:
//
//	ETCD_INITIAL_CLUSTER="m1=http://10.0.0.1:2380,m2=http://10.0.0.2:2380"
func parseInitialCluster(out string) (string, bool) {
	const key = "ETCD_INITIAL_CLUSTER=\""
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, key); i >= 0 {
			rest := line[i+len(key):]
			if j := strings.IndexByte(rest, '"'); j >= 0 {
				return rest[:j], true
			}
		}
	}
	return "", false
}

// resolveUnderBase turns a user-supplied data dir into an absolute path:
// absolute values are returned cleaned as-is, relative values are joined onto
// base (the config's base_dir). This is what lets `--data-dir ./etcd-data`
// land under the pg config file's base directory.
func resolveUnderBase(base, dir string) string {
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Join(base, dir)
}

// bootstrapCluster returns the etcd --initial-cluster value and its state for
// a member, given the other members already in the same cluster. The first
// member of a cluster bootstraps with state "new" (single-member list);
// subsequent members join with state "existing" and the full peer list. Each
// entry uses that member's advertised peer URL, so a mixed host/network
// layout is represented correctly.
func bootstrapCluster(ec config.EtcdConfig, peers []config.EtcdConfig) (initialCluster, state string) {
	if len(peers) == 0 {
		return ec.Name + "=" + ec.PeerURL(), "new"
	}
	parts := make([]string, 0, len(peers)+1)
	for _, p := range peers {
		parts = append(parts, p.Name+"="+p.PeerURL())
	}
	parts = append(parts, ec.Name+"="+ec.PeerURL())
	return strings.Join(parts, ","), "existing"
}

// pickCoordinator returns a running member of the cluster to use for member
// registration, preferring the member's own already-running container (so a
// restart doesn't need a re-add), then any running peer.
func pickCoordinator(em *podman.EtcdManager, ec config.EtcdConfig, peers []config.EtcdConfig) (config.EtcdConfig, bool) {
	if running, err := em.ContainerRunning(ec.ContainerName); err == nil && running {
		return ec, true
	}
	for _, p := range peers {
		if running, err := em.ContainerRunning(p.ContainerName); err == nil && running {
			return p, true
		}
	}
	return config.EtcdConfig{}, false
}

// ---------------------------------------------------------------------------
// install logic — pgdog
// ---------------------------------------------------------------------------

// parsePgDogBackend parses a --backend value of the form
// NAME=HOST:PORT:DBNAME[:SHARD[:ROLE]]. NAME is the logical database name
// clients connect to; SHARD defaults to 0 and ROLE to empty (pgdog's default,
// primary). Used to build one [[databases]] entry in pgdog.toml.
func parsePgDogBackend(spec string) (config.PgDogBackend, error) {
	var b config.PgDogBackend
	name, rest, ok := strings.Cut(spec, "=")
	if !ok || name == "" {
		return b, fmt.Errorf("backend %q must be NAME=HOST:PORT:DBNAME[:SHARD[:ROLE]]", spec)
	}
	parts := strings.Split(rest, ":")
	if len(parts) < 3 || len(parts) > 5 {
		return b, fmt.Errorf("backend %q: expected HOST:PORT:DBNAME[:SHARD[:ROLE]] after the name", spec)
	}
	b.Name = name
	b.Host = parts[0]
	port, err := strconv.Atoi(parts[1])
	if err != nil {
		return b, fmt.Errorf("backend %q: invalid port %q", spec, parts[1])
	}
	b.Port = port
	b.DatabaseName = parts[2]
	if len(parts) >= 4 {
		shard, err := strconv.Atoi(parts[3])
		if err != nil {
			return b, fmt.Errorf("backend %q: invalid shard %q", spec, parts[3])
		}
		b.Shard = shard
	}
	if len(parts) == 5 {
		b.Role = parts[4]
	}
	if b.Host == "" || b.DatabaseName == "" {
		return b, fmt.Errorf("backend %q: host and database name must not be empty", spec)
	}
	return b, nil
}

// parsePgDogUser parses a --user value of the form NAME:PASSWORD[:DBNAME].
// TODO: also support the backend role decoupling pgdog offers — server_user /
// server_password per [[users]] (and user/password per [[databases]]) — so the
// role PgDog uses to reach the backends need not equal the client --user name.
// DBNAME (the logical database the user may reach) defaults to defaultDB, the
// first backend's name. Passwords may contain ':' — only the first two fields
// are split off, the remainder is the password+dbname tail; since DBNAME is
// optional we take the last ':'-segment as the database only when the spec has
// at least three fields. Returns the parsed user.
func parsePgDogUser(spec, defaultDB string) (config.PgDogUser, error) {
	var u config.PgDogUser
	name, rest, ok := strings.Cut(spec, ":")
	if !ok || name == "" {
		return u, fmt.Errorf("user %q must be NAME:PASSWORD[:DBNAME]", spec)
	}
	// The password is everything up to the final ':' segment when a DBNAME is
	// present, or the whole remainder otherwise. A DBNAME never contains ':',
	// so the last segment is unambiguously the database when there are >=2
	// remaining ':'-parts.
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		u.Password = rest[:i]
		u.Database = rest[i+1:]
	} else {
		u.Password = rest
		u.Database = defaultDB
	}
	if u.Password == "" {
		return u, fmt.Errorf("user %q: password must not be empty", spec)
	}
	if u.Database == "" {
		u.Database = defaultDB
	}
	u.Name = name
	return u, nil
}

// parsePgDogShardedTable parses a --sharded-table value of the form
// DBNAME:TABLE:COLUMN:DATA_TYPE into one [[sharded_tables]] entry.
func parsePgDogShardedTable(spec string) (config.PgDogShardedTable, error) {
	var t config.PgDogShardedTable
	parts := strings.Split(spec, ":")
	if len(parts) != 4 {
		return t, fmt.Errorf("sharded-table %q must be DBNAME:TABLE:COLUMN:DATA_TYPE", spec)
	}
	t.Database, t.Name, t.Column, t.DataType = parts[0], parts[1], parts[2], parts[3]
	for _, f := range []*string{&t.Database, &t.Name, &t.Column, &t.DataType} {
		if *f == "" {
			return t, fmt.Errorf("sharded-table %q: all fields must be non-empty", spec)
		}
	}
	return t, nil
}

// runAddonInstallPgDog installs a standalone PgDog proxy container as a
// top-level (shared infrastructure) addon. Its pgdog.toml/users.toml are
// generated entirely from the flags below — there is no config file editing.
// At least one --backend is required; --user entries authenticate clients.
func runAddonInstallPgDog(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "pgdog"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	port, _ := cmd.Flags().GetInt("port")
	host, _ := cmd.Flags().GetString("host")
	poolMode, _ := cmd.Flags().GetString("pool-mode")
	workers, _ := cmd.Flags().GetInt("workers")
	defaultPoolSize, _ := cmd.Flags().GetInt("default-pool-size")
	backendSpecs, _ := cmd.Flags().GetStringArray("backend")
	userSpecs, _ := cmd.Flags().GetStringArray("user")
	shardSpecs, _ := cmd.Flags().GetStringArray("sharded-table")

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

	if len(backendSpecs) == 0 {
		return fmt.Errorf("--backend is required (at least one NAME=HOST:PORT:DBNAME)")
	}
	if len(userSpecs) == 0 {
		return fmt.Errorf("--user is required (at least one NAME:PASSWORD[:DBNAME])")
	}

	backends := make([]config.PgDogBackend, 0, len(backendSpecs))
	for _, s := range backendSpecs {
		b, err := parsePgDogBackend(s)
		if err != nil {
			return err
		}
		backends = append(backends, b)
	}
	// --user DBNAME defaults to the first backend's logical database name.
	users := make([]config.PgDogUser, 0, len(userSpecs))
	for _, s := range userSpecs {
		u, err := parsePgDogUser(s, backends[0].Name)
		if err != nil {
			return err
		}
		// Every referenced database name must exist among the backends.
		found := false
		for _, b := range backends {
			if b.Name == u.Database {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("user %q references database %q, which no --backend defines", u.Name, u.Database)
		}
		users = append(users, u)
	}
	sharded := make([]config.PgDogShardedTable, 0, len(shardSpecs))
	for _, s := range shardSpecs {
		t, err := parsePgDogShardedTable(s)
		if err != nil {
			return err
		}
		sharded = append(sharded, t)
	}

	if cfg.Addons.PgDog == nil {
		cfg.Addons.PgDog = make(map[string]config.PgDogConfig)
	}
	existing, ok := cfg.Addons.PgDog[name]
	if !ok {
		existing = config.PgDogConfig{
			ContainerName: "pgcli-pgdog" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	if port != 0 {
		existing.HostPort = port
	}
	if host != "" {
		existing.Host = host
	}
	if poolMode != "" {
		existing.PoolerMode = poolMode
	}
	if workers != 0 {
		existing.Workers = workers
	}
	if defaultPoolSize != 0 {
		existing.DefaultPoolSize = defaultPoolSize
	}
	// Lists are fully replaced by the flags: pgdog.toml is generated solely
	// from this command's inputs, so re-running install is a clean re-render.
	existing.Backends = backends
	existing.Users = users
	existing.ShardedTables = sharded
	if existing.Name == "" {
		existing.Name = name
	}
	cfg.Addons.PgDog[name] = existing

	cfg.ApplyDefaults()
	pd := cfg.Addons.PgDog[name]

	dm, err := podman.NewPgDogManager(cfg)
	if err != nil {
		return fmt.Errorf("pgdog manager: %w", err)
	}

	tomlPath, usersPath, err := dm.WriteConfigs(&pd)
	if err != nil {
		return err
	}

	// macOS: bring up the podman machine and the pgcli-net bridge the container
	// joins (no-ops on Linux).
	pm, err := podman.New(cfg)
	if err != nil {
		return fmt.Errorf("podman: %w", err)
	}
	if err := pm.EnsureMachine(); err != nil {
		return err
	}
	if err := pm.EnsureNetwork(); err != nil {
		return err
	}

	fmt.Println("-> Starting PgDog container...")
	if err := dm.EnsureContainer(&pd); err != nil {
		return err
	}

	cfg.Addons.PgDog[name] = pd
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	fmt.Printf("✓ pgdog installed: %q\n", name)
	fmt.Printf("  Container:    %s\n", pd.ContainerName)
	fmt.Printf("  Image:        %s\n", pd.ImageTag)
	fmt.Printf("  Pooler mode:  %s\n", pd.PoolerMode)
	fmt.Printf("  Listen:       %s\n", pd.ClientAddr())
	fmt.Printf("  Metrics:      http://%s:%d/metrics\n", pd.Host, pd.OpenmetricsPort)
	fmt.Printf("  Backends:     %d\n", len(pd.Backends))
	fmt.Printf("  Users:        %d\n", len(pd.Users))
	fmt.Printf("  Config:       %s\n", tomlPath)
	fmt.Printf("  Users file:   %s (mode 0600)\n", usersPath)
	fmt.Println()
	fmt.Printf("  Connect via PgDog:\n")
	fmt.Printf("    postgres://%s@%s/%s\n", pd.Users[0].Name, pd.ClientAddr(), pd.Users[0].Database)
	return nil
}

// ---------------------------------------------------------------------------
// install logic — haproxy
// ---------------------------------------------------------------------------

// parseHAProxyNode parses a --node value of the form NAME=HOST:PGPORT:RESTPORT:
// a Patroni member's server name, the host to reach it on, its PostgreSQL port
// (the routed backend) and its REST API port (the health-check target).
func parseHAProxyNode(spec string) (config.HAProxyTarget, error) {
	var t config.HAProxyTarget
	name, rest, ok := strings.Cut(spec, "=")
	if !ok || name == "" {
		return t, fmt.Errorf("node %q must be NAME=HOST:PGPORT:RESTPORT", spec)
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 3 {
		return t, fmt.Errorf("node %q: expected HOST:PGPORT:RESTPORT after the name", spec)
	}
	pgPort, err := strconv.Atoi(parts[1])
	if err != nil {
		return t, fmt.Errorf("node %q: invalid PG port %q", spec, parts[1])
	}
	restPort, err := strconv.Atoi(parts[2])
	if err != nil {
		return t, fmt.Errorf("node %q: invalid REST port %q", spec, parts[2])
	}
	if parts[0] == "" {
		return t, fmt.Errorf("node %q: host must not be empty", spec)
	}
	t.Name, t.Host, t.PGPort, t.RestPort = name, parts[0], pgPort, restPort
	return t, nil
}

// haScopeTargets derives HAProxy backends from every member of a Patroni scope
// in the local config: name, advertised host (loopback when not advertised),
// PG port and REST API port — exactly the coordinates `pg ha status` prints.
func haScopeTargets(cfg *config.Config, scope string) ([]config.HAProxyTarget, error) {
	cluster, ok := cfg.Addons.Patroni[scope]
	if !ok {
		return nil, fmt.Errorf("patroni scope %q not found (run 'pg ha create %s --member <m>' first)", scope, scope)
	}
	names := make([]string, 0, len(cluster.Members))
	for m := range cluster.Members {
		names = append(names, m)
	}
	sort.Strings(names)
	targets := make([]config.HAProxyTarget, 0, len(names))
	for _, m := range names {
		mb := cluster.Members[m]
		if mb.RemoteHost != "" && mb.RestapiPort == 0 {
			// Remote member without a REST port: HAProxy cannot health-check
			// it from here — re-register with `pg ha remote ... --member <m>
			// --restapi-port <p>` to include it as a backend.
			fmt.Printf("  [skip] %s: remote member without --restapi-port\n", m)
			continue
		}
		targets = append(targets, config.HAProxyTarget{
			Name:     m,
			Host:     pgConnectHost(mb),
			PGPort:   mb.HostPort,
			RestPort: mb.RestapiPort,
		})
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("patroni scope %q has no members on this host — create one with 'pg ha create %s --member <m>' or pass --node explicitly", scope, scope)
	}
	return targets, nil
}

// runAddonInstallHAProxy installs a standalone HAProxy container fronting a
// Patroni cluster. The backend nodes come either from --node specs (repeatable,
// explicit) or are auto-derived from a --ha scope. Re-running install re-derives
// / re-renders everything (clean re-render), which is also how members added
// later to the Patroni cluster join the load balancer.
func runAddonInstallHAProxy(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "haproxy"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	mode, _ := cmd.Flags().GetString("mode")
	haScope, _ := cmd.Flags().GetString("ha")
	nodeSpecs, _ := cmd.Flags().GetStringArray("node")
	rwPort, _ := cmd.Flags().GetInt("rw-port")
	roPort, _ := cmd.Flags().GetInt("ro-port")
	statsPort, _ := cmd.Flags().GetInt("stats-port")
	maxLag, _ := cmd.Flags().GetString("max-lag")

	if mode == "" {
		mode = "unified"
	}
	if mode != "unified" && mode != "split" {
		return fmt.Errorf("--mode must be \"unified\" (read-write through the leader) or \"split\" (separate read listener), got %q", mode)
	}
	if len(nodeSpecs) == 0 && haScope == "" {
		return fmt.Errorf("either --node NAME=HOST:PGPORT:RESTPORT (repeatable) or --ha <scope> is required to define the backend members")
	}
	if len(nodeSpecs) > 0 && haScope != "" {
		return fmt.Errorf("--node and --ha are mutually exclusive: --ha auto-derives every local member of the scope, --node lists them explicitly")
	}

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

	targets := make([]config.HAProxyTarget, 0, len(nodeSpecs))
	if haScope != "" {
		derived, err := haScopeTargets(cfg, haScope)
		if err != nil {
			return err
		}
		targets = derived
	} else {
		for _, s := range nodeSpecs {
			t, err := parseHAProxyNode(s)
			if err != nil {
				return err
			}
			targets = append(targets, t)
		}
	}

	if cfg.Addons.HAProxy == nil {
		cfg.Addons.HAProxy = make(map[string]config.HAProxyConfig)
	}
	existing, ok := cfg.Addons.HAProxy[name]
	if !ok {
		existing = config.HAProxyConfig{
			ContainerName: "pgcli-haproxy" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	existing.Mode = mode
	if haScope != "" {
		existing.HAScope = haScope
	}
	if rwPort != 0 {
		existing.WritePort = rwPort
	}
	if roPort != 0 {
		existing.ReadPort = roPort
	}
	if statsPort != 0 {
		existing.StatsPort = statsPort
	}
	if cmd.Flags().Changed("max-lag") {
		existing.ReplicaMaxLag = maxLag
	}
	if listenAddr, _ := cmd.Flags().GetString("listen"); listenAddr != "" {
		existing.Listen = listenAddr
	}
	// The target list is fully replaced by this command's inputs, so
	// re-running install is a clean re-render (and picks up members that a
	// later `pg ha create` added to the scope — or `pg ha remove` dropped).
	existing.Targets = targets
	if existing.Name == "" {
		existing.Name = name
	}
	cfg.Addons.HAProxy[name] = existing

	cfg.ApplyDefaults()
	hc := cfg.Addons.HAProxy[name]

	hm, err := podman.NewHAProxyManager(cfg)
	if err != nil {
		return fmt.Errorf("haproxy manager: %w", err)
	}

	cfgPathOut, err := hm.WriteConfigs(&hc)
	if err != nil {
		return err
	}

	fmt.Println("-> Starting HAProxy container...")
	if err := hm.EnsureContainer(&hc); err != nil {
		return err
	}

	cfg.Addons.HAProxy[name] = hc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	fmt.Printf("✓ haproxy installed: %q\n", name)
	fmt.Printf("  Container:    %s\n", hc.ContainerName)
	fmt.Printf("  Image:        %s\n", hc.ImageTag)
	fmt.Printf("  Mode:         %s\n", hc.EffectiveMode())
	if hc.HAScope != "" {
		fmt.Printf("  Patroni:      %s\n", hc.HAScope)
	}
	fmt.Printf("  Backends:     %d\n", len(hc.Targets))
	fmt.Printf("  Read-write:   %s:%d\n", hc.Listen, hc.WritePort)
	if hc.EffectiveMode() == "split" {
		lag := ""
		if hc.ReplicaMaxLag != "" {
			lag = fmt.Sprintf(" (lag ≤ %s)", hc.ReplicaMaxLag)
		}
		fmt.Printf("  Read-only:    %s:%d%s\n", hc.Listen, hc.ReadPort, lag)
	}
	fmt.Printf("  Stats:        http://%s:%d/\n", hc.Listen, hc.StatsPort)
	fmt.Printf("  Config:       %s\n", cfgPathOut)
	fmt.Println()
	fmt.Printf("  Connect via HAProxy:\n")
	fmt.Printf("    postgres://<user>@%s:%d/<database>\n", hc.Listen, hc.WritePort)
	return nil
}

// runAddonInstallMinio installs a standalone single-node MinIO container:
// S3-compatible object storage shared across the host (e.g. a pgBackRest
// repository for a Patroni cluster). The image is pull-only from the public
// ghcr repo. Root credentials are generated on first install and persisted to
// pg.yaml under addons.minio.<name>; a re-install keeps the stored set so the
// data directory stays readable with the same keys.
func runAddonInstallMinio(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "minio"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	apiPort, _ := cmd.Flags().GetInt("api-port")
	consolePort, _ := cmd.Flags().GetInt("console-port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	rootUser, _ := cmd.Flags().GetString("root-user")
	rootPassword, _ := cmd.Flags().GetString("root-password")
	endpoints, _ := cmd.Flags().GetStringSlice("endpoint")
	drives, _ := cmd.Flags().GetStringSlice("drive")
	force, _ := cmd.Flags().GetBool("force")
	tls, _ := cmd.Flags().GetBool("tls")
	tlsCert, _ := cmd.Flags().GetString("tls-cert")
	tlsKey, _ := cmd.Flags().GetString("tls-key")

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

	if cfg.Addons.Minio == nil {
		cfg.Addons.Minio = make(map[string]config.MinioConfig)
	}
	existing, ok := cfg.Addons.Minio[name]
	if !ok {
		existing = config.MinioConfig{
			ContainerName: "pgcli-minio" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	if dataDir != "" {
		existing.DataDir = dataDir
	}
	if apiPort != 0 {
		existing.APIPort = apiPort
	}
	if consolePort != 0 {
		existing.ConsolePort = consolePort
	}
	if rootUser != "" {
		existing.RootUser = rootUser
	}
	if rootPassword != "" {
		existing.RootPassword = rootPassword
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if len(endpoints) > 0 {
		existing.Endpoints = endpoints
	}
	if len(drives) > 0 {
		existing.Drives = drives
	}
	if tls {
		existing.TLS = true
	}
	if tlsCert != "" {
		existing.TLS = true
		existing.CertFile = podman.HostMountPath(tlsCert)
	}
	if tlsKey != "" {
		existing.TLS = true
		existing.KeyFile = podman.HostMountPath(tlsKey)
	}
	if (existing.CertFile == "") != (existing.KeyFile == "") {
		return fmt.Errorf("--tls-cert and --tls-key must be given together (a bring-your-own TLS pair needs both the cert and its key)")
	}
	// Three data-location axes — endpoints (cluster), drives (this node's
	// multi-drive set), data-dir (single export). drives+endpoints is MNMD, a
	// legal combination, but the matrix must name this node's /dataN slots and
	// divide evenly by drives-per-node; drives+data-dir has no merge semantics.
	// All checked post-merge so a stale key in pg.yaml conflicts like a flag.
	if err := podman.ValidateMNMDMatrix(existing.Endpoints, len(existing.Drives), existing.TLS); err != nil {
		return err
	}
	if len(existing.Drives) > 0 && existing.DataDir != "" {
		return fmt.Errorf("--drive and --data-dir cannot be combined — multi-drive mode takes its data locations from --drive only")
	}
	if existing.Name == "" {
		existing.Name = name
	}
	cfg.Addons.Minio[name] = existing

	// ApplyDefaults fills ContainerName/ImageTag/Listen/RootUser and assigns
	// the two ports from the minio_start_port pool. Credentials are NOT managed
	// there — they are a secret the install path owns.
	cfg.ApplyDefaults()
	mc := cfg.Addons.Minio[name]

	// BYO TLS: fail fast at install on a bad pair (mismatch, expired, CA-only,
	// unparseable) rather than letting the container crash-loop on handshake.
	// The SAN check is advisory: a domain cert is often meant to be dialed by a
	// name behind DNS/LB that is not this host's Listen, so only warn — and
	// never for a wildcard bind.
	if podman.BYOTLS(mc.TLS, mc.CertFile, mc.KeyFile) {
		ci, err := podman.ValidateBYOCert(mc.CertFile, mc.KeyFile)
		if err != nil {
			return fmt.Errorf("MinIO --tls-cert/--tls-key: %w", err)
		}
		fmt.Printf("-> MinIO BYO cert: CN=%q issuer=%q valid %s → %s\n",
			ci.Subject, ci.Issuer, ci.NotBefore.Format("2006-01-02"), ci.NotAfter.Format("2006-01-02"))
		if h := strings.TrimSuffix(mc.Listen, ":0"); h != "" && h != "0.0.0.0" && h != "::" && !podman.CertCoversHost(ci, h) {
			fmt.Printf("  [!] listen address %q is not a SAN of the cert (SANs: %s) — clients must reach MinIO by a name the cert does cover\n",
				mc.Listen, strings.Join(append(append([]string{}, ci.DNSNames...), ci.IPs...), ", "))
		}
	}

	// macOS: the store serves on the pgcli-net bridge with published ports, so
	// bring up the machine and the bridge first (no-ops on Linux, where MinIO
	// uses host networking).
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	mm, err := podman.NewMinioManager(cfg)
	if err != nil {
		return fmt.Errorf("minio manager: %w", err)
	}

	// If a same-named container is already present, don't recreate it — reuse
	// it (starting it if it's stopped). Reinstall is then a no-op against a
	// live instance; --force recreates it so changed ports/listen/credentials
	// take effect.
	exists, err := mm.ContainerExists(mc.ContainerName)
	if err != nil {
		return err
	}
	skipped := false
	if exists && !force {
		skipped = true
		if running, _ := mm.ContainerRunning(mc.ContainerName); running {
			fmt.Printf("-> MinIO container %q already running; skipping creation\n", mc.ContainerName)
			// Still refresh cert material: MinIO hot-reloads its cert files, so
			// this is how a running store adopts the leaf+CA chain (the
			// `pg backup fetch-ca` anchor) without a --force recreate.
			if mc.TLS {
				if caPath, err := mm.EnsureTLS(&mc); err != nil {
					fmt.Printf("  [!] TLS cert refresh failed: %v\n", err)
				} else if podman.BYOTLS(mc.TLS, mc.CertFile, mc.KeyFile) {
					fmt.Printf("  [OK] TLS cert pair validated (BYO: %s)\n", mc.CertFile)
				} else {
					fmt.Printf("  [OK] TLS certs current (CA: %s)\n", caPath)
				}
			}
		} else {
			fmt.Printf("-> MinIO container %q exists but is stopped; starting it\n", mc.ContainerName)
			if err := mm.StartContainer(&mc); err != nil {
				return err
			}
		}
	} else {
		// Fresh install (or the config survived a `remove` that kept it):
		// generate the root password once so the data dir stays readable.
		if mc.RootPassword == "" {
			pw, err := generatePassword(20)
			if err != nil {
				return fmt.Errorf("generating MinIO root password: %w", err)
			}
			mc.RootPassword = pw
		}
		fmt.Printf("-> Preparing MinIO image %s...\n", mc.ImageTag)
		if err := mm.EnsureImage(mc.ImageTag); err != nil {
			return err
		}
		// Distributed (MNSD) and single-node multi-drive (SNMD) both erase-code
		// across drives, and MinIO refuses any drive on the root filesystem
		// ("drive is part of root drive, will not be used"), so warn per-drive
		// before we try to start — the container would just fail to form
		// quorum. SNSD (single data_dir, no endpoints, no drives) has no such
		// requirement, so it stays silent.
		if len(mc.Endpoints) > 0 || len(mc.Drives) > 0 {
			for _, d := range mm.DrivesSharingRootDevice(&mc) {
				fmt.Printf("-> WARNING: drive %q is on the same device as the host root filesystem; MinIO will refuse it. Point --drive/--data-dir at separately-mounted disks.\n", d)
			}
		}
		fmt.Println("-> Starting MinIO container...")
		if err := mm.EnsureContainer(&mc); err != nil {
			return err
		}
	}

	cfg.Addons.Minio[name] = mc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	if skipped {
		fmt.Println()
		fmt.Printf("✓ minio already present: %q\n", name)
	} else {
		fmt.Println()
		fmt.Printf("✓ minio installed: %q\n", name)
	}
	fmt.Printf("  Container:    %s\n", mc.ContainerName)
	fmt.Printf("  Image:        %s\n", mc.ImageTag)
	if len(mc.Drives) > 0 {
		for i, d := range mm.Drives(&mc) {
			fmt.Printf("  Drive %d:       %s -> /data%d\n", i+1, d, i+1)
		}
	} else {
		fmt.Printf("  Data:         %s\n", mm.DataDir(&mc))
	}
	scheme := "http"
	if mc.TLS {
		scheme = "https"
	}
	fmt.Printf("  S3 API:       %s://%s:%d\n", scheme, mc.Listen, mc.APIPort)
	fmt.Printf("  Console:      %s://%s:%d\n", scheme, mc.Listen, mc.ConsolePort)
	fmt.Println()
	fmt.Printf("  Root user:     %s\n", mc.RootUser)
	fmt.Printf("  Root password: %s\n", mc.RootPassword)
	if mc.TLS {
		if podman.BYOTLS(mc.TLS, mc.CertFile, mc.KeyFile) {
			fmt.Printf("  TLS cert:      %s (BYO, key: %s)\n", mc.CertFile, mc.KeyFile)
		} else {
			fmt.Printf("  TLS CA cert:   %s\n", filepath.Join(mm.TLSDir(&mc), "ca.crt"))
		}
	}
	if len(mc.Endpoints) > 0 {
		fmt.Println()
		if len(mc.Drives) > 0 {
			fmt.Printf("  Distributed multi-drive mode (MNMD): %d endpoints = %d nodes × %d drives/node\n", len(mc.Endpoints), len(mc.Endpoints)/len(mc.Drives), len(mc.Drives))
		} else {
			fmt.Printf("  Distributed mode: %d endpoints\n", len(mc.Endpoints))
		}
		for _, ep := range mc.Endpoints {
			fmt.Printf("    - %s\n", ep)
		}
		fmt.Println("  NOTE: every node's pg.yaml must carry the identical endpoint list AND identical root credentials, or the cluster will not form.")
		if len(mc.Drives) > 0 {
			fmt.Println("        MNMD: each endpoint addresses one drive slot (/data1../dataN) on one node; every node contributes the same drive count, and this node mounts its own drives at those slots.")
		}
	}
	if len(mc.Drives) > 0 && len(mc.Endpoints) == 0 {
		fmt.Println()
		fmt.Printf("  Multi-drive mode (SNMD): %d drives\n", len(mc.Drives))
		for _, d := range mc.Drives {
			fmt.Printf("    - %s\n", d)
		}
		fmt.Printf("  NOTE: MinIO erasure-codes across these drives — by default roughly half are parity (a 4-drive set tolerates 2 failures), so usable capacity is about half the raw total. Each drive must sit on its own device; a drive that goes down is healed by MinIO itself when it returns.\n")
	}
	return nil
}

func runAddonInstallSilo(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "silo"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	apiPort, _ := cmd.Flags().GetInt("api-port")
	consolePort, _ := cmd.Flags().GetInt("console-port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	rootUser, _ := cmd.Flags().GetString("root-user")
	rootPassword, _ := cmd.Flags().GetString("root-password")
	endpoints, _ := cmd.Flags().GetStringSlice("endpoint")
	drives, _ := cmd.Flags().GetStringSlice("drive")
	force, _ := cmd.Flags().GetBool("force")
	tls, _ := cmd.Flags().GetBool("tls")
	tlsCert, _ := cmd.Flags().GetString("tls-cert")
	tlsKey, _ := cmd.Flags().GetString("tls-key")

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

	if cfg.Addons.Silo == nil {
		cfg.Addons.Silo = make(map[string]config.SiloConfig)
	}
	existing, ok := cfg.Addons.Silo[name]
	if !ok {
		existing = config.SiloConfig{
			ContainerName: "pgcli-silo" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	if dataDir != "" {
		existing.DataDir = dataDir
	}
	if apiPort != 0 {
		existing.APIPort = apiPort
	}
	if consolePort != 0 {
		existing.ConsolePort = consolePort
	}
	if rootUser != "" {
		existing.RootUser = rootUser
	}
	if rootPassword != "" {
		existing.RootPassword = rootPassword
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if len(endpoints) > 0 {
		existing.Endpoints = endpoints
	}
	if len(drives) > 0 {
		existing.Drives = drives
	}
	if tls {
		existing.TLS = true
	}
	if tlsCert != "" {
		existing.TLS = true
		existing.CertFile = podman.HostMountPath(tlsCert)
	}
	if tlsKey != "" {
		existing.TLS = true
		existing.KeyFile = podman.HostMountPath(tlsKey)
	}
	if (existing.CertFile == "") != (existing.KeyFile == "") {
		return fmt.Errorf("--tls-cert and --tls-key must be given together (a bring-your-own TLS pair needs both the cert and its key)")
	}
	// Three data-location axes — endpoints (cluster), drives (this node's
	// multi-drive set), data-dir (single export). drives+endpoints is MNMD, a
	// legal combination, but the matrix must name this node's /dataN slots and
	// divide evenly by drives-per-node; drives+data-dir has no merge semantics.
	// All checked post-merge so a stale key in pg.yaml conflicts like a flag.
	if err := podman.ValidateMNMDMatrix(existing.Endpoints, len(existing.Drives), existing.TLS); err != nil {
		return err
	}
	if len(existing.Drives) > 0 && existing.DataDir != "" {
		return fmt.Errorf("--drive and --data-dir cannot be combined — multi-drive mode takes its data locations from --drive only")
	}
	if existing.Name == "" {
		existing.Name = name
	}
	cfg.Addons.Silo[name] = existing

	// ApplyDefaults fills ContainerName/ImageTag/Listen/RootUser and assigns
	// the two ports from the shared minio_start_port pool (minio and silo draw
	// one cursor, so both can coexist). Credentials are NOT managed
	// there — they are a secret the install path owns.
	cfg.ApplyDefaults()
	sc := cfg.Addons.Silo[name]

	// BYO TLS: fail fast at install on a bad pair (mismatch, expired, CA-only,
	// unparseable) rather than letting the container crash-loop on handshake.
	// The SAN check is advisory: a domain cert is often meant to be dialed by a
	// name behind DNS/LB that is not this host's Listen, so only warn — and
	// never for a wildcard bind.
	if podman.BYOTLS(sc.TLS, sc.CertFile, sc.KeyFile) {
		ci, err := podman.ValidateBYOCert(sc.CertFile, sc.KeyFile)
		if err != nil {
			return fmt.Errorf("silo --tls-cert/--tls-key: %w", err)
		}
		fmt.Printf("-> silo BYO cert: CN=%q issuer=%q valid %s → %s\n",
			ci.Subject, ci.Issuer, ci.NotBefore.Format("2006-01-02"), ci.NotAfter.Format("2006-01-02"))
		if h := strings.TrimSuffix(sc.Listen, ":0"); h != "" && h != "0.0.0.0" && h != "::" && !podman.CertCoversHost(ci, h) {
			fmt.Printf("  [!] listen address %q is not a SAN of the cert (SANs: %s) — clients must reach silo by a name the cert does cover\n",
				sc.Listen, strings.Join(append(append([]string{}, ci.DNSNames...), ci.IPs...), ", "))
		}
	}

	// macOS: the store serves on the pgcli-net bridge with published ports, so
	// bring up the machine and the bridge first (no-ops on Linux, where silo
	// uses host networking).
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	sm, err := podman.NewSiloManager(cfg)
	if err != nil {
		return fmt.Errorf("silo manager: %w", err)
	}

	// If a same-named container is already present, don't recreate it — reuse
	// it (starting it if it's stopped). Reinstall is then a no-op against a
	// live instance; --force recreates it so changed ports/listen/credentials
	// take effect.
	exists, err := sm.ContainerExists(sc.ContainerName)
	if err != nil {
		return err
	}
	skipped := false
	if exists && !force {
		skipped = true
		if running, _ := sm.ContainerRunning(sc.ContainerName); running {
			fmt.Printf("-> silo container %q already running; skipping creation\n", sc.ContainerName)
			// Still refresh cert material: silo hot-reloads its cert files, so
			// this is how a running store adopts the leaf+CA chain (the
			// `pg backup fetch-ca` anchor) without a --force recreate.
			if sc.TLS {
				if caPath, err := sm.EnsureTLS(&sc); err != nil {
					fmt.Printf("  [!] TLS cert refresh failed: %v\n", err)
				} else if podman.BYOTLS(sc.TLS, sc.CertFile, sc.KeyFile) {
					fmt.Printf("  [OK] TLS cert pair validated (BYO: %s)\n", sc.CertFile)
				} else {
					fmt.Printf("  [OK] TLS certs current (CA: %s)\n", caPath)
				}
			}
		} else {
			fmt.Printf("-> silo container %q exists but is stopped; starting it\n", sc.ContainerName)
			if err := sm.StartContainer(&sc); err != nil {
				return err
			}
		}
	} else {
		// Fresh install (or the config survived a `remove` that kept it):
		// generate the root password once so the data dir stays readable.
		if sc.RootPassword == "" {
			pw, err := generatePassword(20)
			if err != nil {
				return fmt.Errorf("generating silo root password: %w", err)
			}
			sc.RootPassword = pw
		}
		fmt.Printf("-> Preparing silo image %s...\n", sc.ImageTag)
		if err := sm.EnsureImage(sc.ImageTag); err != nil {
			return err
		}
		// Distributed (MNSD) and single-node multi-drive (SNMD) both erase-code
		// across drives, and silo refuses any drive on the root filesystem
		// ("drive is part of root drive, will not be used"), so warn per-drive
		// before we try to start — the container would just fail to form
		// quorum. SNSD has no such requirement, so it stays silent.
		if len(sc.Endpoints) > 0 || len(sc.Drives) > 0 {
			for _, d := range sm.DrivesSharingRootDevice(&sc) {
				fmt.Printf("-> WARNING: drive %q is on the same device as the host root filesystem; silo will refuse it. Point --drive/--data-dir at separately-mounted disks.\n", d)
			}
		}
		fmt.Println("-> Starting silo container...")
		if err := sm.EnsureContainer(&sc); err != nil {
			return err
		}
	}

	cfg.Addons.Silo[name] = sc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	if skipped {
		fmt.Println()
		fmt.Printf("✓ silo already present: %q\n", name)
	} else {
		fmt.Println()
		fmt.Printf("✓ silo installed: %q\n", name)
	}
	fmt.Printf("  Container:    %s\n", sc.ContainerName)
	fmt.Printf("  Image:        %s\n", sc.ImageTag)
	if len(sc.Drives) > 0 {
		for i, d := range sm.Drives(&sc) {
			fmt.Printf("  Drive %d:       %s -> /data%d\n", i+1, d, i+1)
		}
	} else {
		fmt.Printf("  Data:         %s\n", sm.DataDir(&sc))
	}
	scheme := "http"
	if sc.TLS {
		scheme = "https"
	}
	fmt.Printf("  S3 API:       %s://%s:%d\n", scheme, sc.Listen, sc.APIPort)
	fmt.Printf("  Console:      %s://%s:%d\n", scheme, sc.Listen, sc.ConsolePort)
	fmt.Println()
	fmt.Printf("  Root user:     %s\n", sc.RootUser)
	fmt.Printf("  Root password: %s\n", sc.RootPassword)
	if sc.TLS {
		if podman.BYOTLS(sc.TLS, sc.CertFile, sc.KeyFile) {
			fmt.Printf("  TLS cert:      %s (BYO, key: %s)\n", sc.CertFile, sc.KeyFile)
		} else {
			fmt.Printf("  TLS CA cert:   %s\n", filepath.Join(sm.TLSDir(&sc), "ca.crt"))
		}
	}
	if len(sc.Endpoints) > 0 {
		fmt.Println()
		if len(sc.Drives) > 0 {
			fmt.Printf("  Distributed multi-drive mode (MNMD): %d endpoints = %d nodes × %d drives/node\n", len(sc.Endpoints), len(sc.Endpoints)/len(sc.Drives), len(sc.Drives))
		} else {
			fmt.Printf("  Distributed mode: %d endpoints\n", len(sc.Endpoints))
		}
		for _, ep := range sc.Endpoints {
			fmt.Printf("    - %s\n", ep)
		}
		fmt.Println("  NOTE: every node's pg.yaml must carry the identical endpoint list AND identical root credentials, or the cluster will not form.")
		if len(sc.Drives) > 0 {
			fmt.Println("        MNMD: each endpoint addresses one drive slot (/data1../dataN) on one node; every node contributes the same drive count, and this node mounts its own drives at those slots.")
		}
	}
	if len(sc.Drives) > 0 && len(sc.Endpoints) == 0 {
		fmt.Println()
		fmt.Printf("  Multi-drive mode (SNMD): %d drives\n", len(sc.Drives))
		for _, d := range sc.Drives {
			fmt.Printf("    - %s\n", d)
		}
		fmt.Printf("  NOTE: silo erasure-codes across these drives — by default roughly half are parity (a 4-drive set tolerates 2 failures), so usable capacity is about half the raw total. Each drive must sit on its own device; a drive that goes down is healed by silo itself when it returns.\n")
	}
	return nil
}

// runAddonInstallRustfs installs a rustfs container. Field-for-field twin of
// runAddonInstallSilo except where the runtime genuinely differs: rustfs has no
// multi-node single-drive topology (so ValidateRustfsVolumes replaces
// ValidateMNMDMatrix here), and its drive slots are 0-indexed /data/rustfsN
// rather than MinIO/silo's 1-indexed /dataN — the display and the notes below
// reflect rustfs's own layout and its hard distinct-physical-device rule.
func runAddonInstallRustfs(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "rustfs"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	apiPort, _ := cmd.Flags().GetInt("api-port")
	consolePort, _ := cmd.Flags().GetInt("console-port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	rootUser, _ := cmd.Flags().GetString("root-user")
	rootPassword, _ := cmd.Flags().GetString("root-password")
	endpoints, _ := cmd.Flags().GetStringSlice("endpoint")
	drives, _ := cmd.Flags().GetStringSlice("drive")
	force, _ := cmd.Flags().GetBool("force")
	tls, _ := cmd.Flags().GetBool("tls")
	tlsCert, _ := cmd.Flags().GetString("tls-cert")
	tlsKey, _ := cmd.Flags().GetString("tls-key")

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

	if cfg.Addons.Rustfs == nil {
		cfg.Addons.Rustfs = make(map[string]config.RustfsConfig)
	}
	existing, ok := cfg.Addons.Rustfs[name]
	if !ok {
		existing = config.RustfsConfig{
			ContainerName: "pgcli-rustfs" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	if dataDir != "" {
		existing.DataDir = dataDir
	}
	if apiPort != 0 {
		existing.APIPort = apiPort
	}
	if consolePort != 0 {
		existing.ConsolePort = consolePort
	}
	if rootUser != "" {
		existing.RootUser = rootUser
	}
	if rootPassword != "" {
		existing.RootPassword = rootPassword
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if len(endpoints) > 0 {
		existing.Endpoints = endpoints
	}
	if len(drives) > 0 {
		existing.Drives = drives
	}
	if tls {
		existing.TLS = true
	}
	if tlsCert != "" {
		existing.TLS = true
		existing.CertFile = podman.HostMountPath(tlsCert)
	}
	if tlsKey != "" {
		existing.TLS = true
		existing.KeyFile = podman.HostMountPath(tlsKey)
	}
	if (existing.CertFile == "") != (existing.KeyFile == "") {
		return fmt.Errorf("--tls-cert and --tls-key must be given together (a bring-your-own TLS pair needs both the cert and its key)")
	}
	// rustfs has SNSD/SNMD/MNMD but NO multi-node single-drive mode, so
	// endpoints require at least one drive. Checked post-merge so a stale key in
	// pg.yaml conflicts like a flag. The minimum drive/node count and the
	// distinct-physical-device rule are enforced by rustfs itself at startup
	// (it FATALs otherwise), not here.
	if err := podman.ValidateRustfsVolumes(existing.Endpoints, len(existing.Drives)); err != nil {
		return err
	}
	if len(existing.Drives) > 0 && existing.DataDir != "" {
		return fmt.Errorf("--drive and --data-dir cannot be combined — multi-drive mode takes its data locations from --drive only")
	}
	if existing.Name == "" {
		existing.Name = name
	}
	cfg.Addons.Rustfs[name] = existing

	// ApplyDefaults fills ContainerName/ImageTag/Listen/RootUser and assigns
	// the two ports from the shared minio_start_port pool (minio, silo and
	// rustfs draw one cursor, so all three can coexist). Credentials are NOT
	// managed there — they are a secret the install path owns.
	cfg.ApplyDefaults()
	rc := cfg.Addons.Rustfs[name]

	// BYO TLS: fail fast at install on a bad pair (mismatch, expired, CA-only,
	// unparseable) rather than letting the container crash-loop on handshake.
	// The SAN check is advisory: a domain cert is often meant to be dialed by a
	// name behind DNS/LB that is not this host's Listen, so only warn — and
	// never for a wildcard bind.
	if podman.BYOTLS(rc.TLS, rc.CertFile, rc.KeyFile) {
		ci, err := podman.ValidateBYOCert(rc.CertFile, rc.KeyFile)
		if err != nil {
			return fmt.Errorf("rustfs --tls-cert/--tls-key: %w", err)
		}
		fmt.Printf("-> rustfs BYO cert: CN=%q issuer=%q valid %s → %s\n",
			ci.Subject, ci.Issuer, ci.NotBefore.Format("2006-01-02"), ci.NotAfter.Format("2006-01-02"))
		if h := strings.TrimSuffix(rc.Listen, ":0"); h != "" && h != "0.0.0.0" && h != "::" && !podman.CertCoversHost(ci, h) {
			fmt.Printf("  [!] listen address %q is not a SAN of the cert (SANs: %s) — clients must reach rustfs by a name the cert does cover\n",
				rc.Listen, strings.Join(append(append([]string{}, ci.DNSNames...), ci.IPs...), ", "))
		}
	}

	// macOS: the store serves on the pgcli-net bridge with published ports, so
	// bring up the machine and the bridge first (no-ops on Linux, where rustfs
	// uses host networking).
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	rm, err := podman.NewRustfsManager(cfg)
	if err != nil {
		return fmt.Errorf("rustfs manager: %w", err)
	}

	// If a same-named container is already present, don't recreate it — reuse
	// it (starting it if it's stopped). Reinstall is then a no-op against a
	// live instance; --force recreates it so changed ports/listen/credentials
	// take effect.
	exists, err := rm.ContainerExists(rc.ContainerName)
	if err != nil {
		return err
	}
	skipped := false
	if exists && !force {
		skipped = true
		if running, _ := rm.ContainerRunning(rc.ContainerName); running {
			fmt.Printf("-> rustfs container %q already running; skipping creation\n", rc.ContainerName)
			// Still refresh cert material: rustfs hot-reloads its cert files, so
			// this is how a running store adopts the leaf+CA chain (the
			// `pg backup fetch-ca` anchor) without a --force recreate.
			if rc.TLS {
				if caPath, err := rm.EnsureTLS(&rc); err != nil {
					fmt.Printf("  [!] TLS cert refresh failed: %v\n", err)
				} else if podman.BYOTLS(rc.TLS, rc.CertFile, rc.KeyFile) {
					fmt.Printf("  [OK] TLS cert pair validated (BYO: %s)\n", rc.CertFile)
				} else {
					fmt.Printf("  [OK] TLS certs current (CA: %s)\n", caPath)
				}
			}
		} else {
			fmt.Printf("-> rustfs container %q exists but is stopped; starting it\n", rc.ContainerName)
			if err := rm.StartContainer(&rc); err != nil {
				return err
			}
		}
	} else {
		// Fresh install (or the config survived a `remove` that kept it):
		// generate the root password once so the data dir stays readable.
		if rc.RootPassword == "" {
			pw, err := generatePassword(20)
			if err != nil {
				return fmt.Errorf("generating rustfs root password: %w", err)
			}
			rc.RootPassword = pw
		}
		fmt.Printf("-> Preparing rustfs image %s...\n", rc.ImageTag)
		if err := rm.EnsureImage(rc.ImageTag); err != nil {
			return err
		}
		// rustfs erasure-codes across drives and, unlike MinIO/silo's advisory
		// check, FATALs at startup if two drives share a physical device. Warn
		// per-drive before we try to start so the operator sees the cause
		// rather than a crash-loop. SNSD has no such requirement, so silent.
		if len(rc.Endpoints) > 0 || len(rc.Drives) > 0 {
			for _, d := range rm.DrivesSharingRootDevice(&rc) {
				fmt.Printf("-> WARNING: drive %q is on the same device as the host root filesystem; rustfs requires every drive on its own physical device and will refuse this one. Point --drive/--data-dir at separately-mounted disks.\n", d)
			}
		}
		fmt.Println("-> Starting rustfs container...")
		if err := rm.EnsureContainer(&rc); err != nil {
			return err
		}
	}

	cfg.Addons.Rustfs[name] = rc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	if skipped {
		fmt.Println()
		fmt.Printf("✓ rustfs already present: %q\n", name)
	} else {
		fmt.Println()
		fmt.Printf("✓ rustfs installed: %q\n", name)
	}
	fmt.Printf("  Container:    %s\n", rc.ContainerName)
	fmt.Printf("  Image:        %s\n", rc.ImageTag)
	if len(rc.Drives) > 0 {
		for i, d := range rm.Drives(&rc) {
			fmt.Printf("  Drive %d:       %s -> /data/rustfs%d\n", i+1, d, i)
		}
	} else {
		fmt.Printf("  Data:         %s\n", rm.DataDir(&rc))
	}
	scheme := "http"
	if rc.TLS {
		scheme = "https"
	}
	fmt.Printf("  S3 API:       %s://%s:%d\n", scheme, rc.Listen, rc.APIPort)
	fmt.Printf("  Console:      %s://%s:%d\n", scheme, rc.Listen, rc.ConsolePort)
	fmt.Println()
	fmt.Printf("  Root user:     %s\n", rc.RootUser)
	fmt.Printf("  Root password: %s\n", rc.RootPassword)
	if rc.TLS {
		if podman.BYOTLS(rc.TLS, rc.CertFile, rc.KeyFile) {
			fmt.Printf("  TLS cert:      %s (BYO, key: %s)\n", rc.CertFile, rc.KeyFile)
		} else {
			fmt.Printf("  TLS CA cert:   %s\n", filepath.Join(rm.TLSDir(&rc), "ca.crt"))
		}
	}
	if len(rc.Endpoints) > 0 {
		fmt.Println()
		fmt.Printf("  Distributed multi-drive mode (MNMD): %d endpoints = %d nodes × %d drives/node\n", len(rc.Endpoints), len(rc.Endpoints)/len(rc.Drives), len(rc.Drives))
		for _, ep := range rc.Endpoints {
			fmt.Printf("    - %s\n", ep)
		}
		fmt.Println("  NOTE: every node's pg.yaml must carry the identical endpoint list AND identical root credentials, or the cluster will not form.")
		fmt.Println("        MNMD: each endpoint addresses one node's /data/rustfs0../data/rustfs(D-1) slots; every node contributes the same drive count, and this node mounts its own drives there.")
	}
	if len(rc.Drives) > 0 && len(rc.Endpoints) == 0 {
		fmt.Println()
		fmt.Printf("  Multi-drive mode (SNMD): %d drives\n", len(rc.Drives))
		for _, d := range rc.Drives {
			fmt.Printf("    - %s\n", d)
		}
		fmt.Printf("  NOTE: rustfs erasure-codes across these drives; usable capacity is a fraction of the raw total. rustfs hard-requires each drive on its own physical device (it aborts at startup otherwise, unlike MinIO's advisory check); a drive that goes down is healed when it returns.\n")
	}
	return nil
}

// redisEvictionPolicies are the --maxmemory-policy values pgcli accepts — the
// native redis-server set minus the lfu/random variants tied to maxmemory-
// samples tuning that pgcli does not expose.
var redisEvictionPolicies = []string{
	"noeviction", "allkeys-lru", "allkeys-lfu", "volatile-lru", "volatile-lfu", "volatile-ttl",
}

// redisAppendFsyncs are the --appendfsync strengths, in durability order.
var redisAppendFsyncs = []string{"always", "everysec", "no"}

// resolveRedisReplica turns the --replica-of / --replica-of-host /
// --replica-of-port flags into the stored ReplicaHost/ReplicaPort (and, for a
// local master, the borrowed Password and matched major) on rc. It runs after
// the flags are merged into the existing config entry but before the version is
// resolved and before any pull/create, so a bad topology is a fast, clear
// error rather than a half-installed container. rc is mutated in place.
//
//   - neither --replica-of nor --replica-of-host: this is a master; a stored
//     replica role survives untouched only if reinstall passes neither (a plain
//     no-flag reinstall must not silently promote a replica to master by
//     clearing the role — but that clearing would drop --replicaof from the
//     argv, so we deliberately do NOT re-derive the role from flags when both
//     are absent: rc keeps whatever its stored ReplicaHost/Port already say).
//   - both given: rejected (ambiguous).
//   - --replica-of <name> (local): the master addon must exist, be a different
//     instance, and share the major; the replica inherits the master's port
//     target (127.0.0.1 + master.Port) and, unless --password pinned an equal
//     value, the master's password — so one password spans requirepass and
//     masterauth.
//   - --replica-of-host <host> (remote): --replica-of-port is required, and
//     --password is required too (a remote master's password is not in this
//     config, so the operator must supply it; it must still equal the master's,
//     which only the operator can guarantee). No major check across hosts.
func resolveRedisReplica(cfg *config.Config, rc *config.RedisConfig, selfName, replicaOf, replicaOfHost, version string, replicaOfPort int, passwordGiven bool) error {
	if replicaOf != "" && replicaOfHost != "" {
		return fmt.Errorf("--replica-of (a local master addon) and --replica-of-host (a remote master) are mutually exclusive")
	}
	switch {
	case replicaOf == "" && replicaOfHost == "":
		// Neither replica flag: leave any stored role in place (this is a
		// plain reinstall of an existing instance — master or replica).
		return nil

	case replicaOf != "":
		if replicaOf == selfName {
			return fmt.Errorf("--replica-of %q cannot name the instance itself", replicaOf)
		}
		master, ok := cfg.Addons.Redis[replicaOf]
		if !ok {
			return fmt.Errorf("--replica-of %q is not an installed redis addon (available: %v)", replicaOf, sortedAddonNames(cfg.Addons.Redis))
		}
		// A replica must run the same major as its master — cross-version
		// replication is unsupported by Redis. `version` is the raw --version
		// flag (rc.Version is only the previously-stored value at this point);
		// reject an explicit mismatch, else adopt the master's major so the
		// version resolve below re-derives a matching image tag.
		wantMajor := version
		if wantMajor == "" {
			wantMajor = rc.Version
		}
		if wantMajor != "" && wantMajor != master.Version {
			return fmt.Errorf("replica major %q does not match master %q (%s) — Redis cannot replicate across majors", wantMajor, replicaOf, master.Version)
		}
		rc.Version = master.Version
		rc.ReplicaHost = "127.0.0.1"
		rc.ReplicaPort = master.Port
		if passwordGiven && rc.Password != "" && rc.Password != master.Password {
			return fmt.Errorf("replica --password must equal the master %q's password (requirepass and masterauth share one value)", replicaOf)
		}
		rc.Password = master.Password
		return nil

	default: // replicaOfHost != ""
		if replicaOfPort == 0 {
			return fmt.Errorf("--replica-of-host %q needs --replica-of-port (the remote master's port)", replicaOfHost)
		}
		if rc.Password == "" {
			return fmt.Errorf("a remote replica needs --password matching the master's (its value is not in this config to inherit)")
		}
		rc.ReplicaHost = replicaOfHost
		rc.ReplicaPort = replicaOfPort
		return nil
	}
}

// validateRedisKnobs rejects nonsense in the persistence/eviction knobs and the
// replica target before anything is pulled or created. It runs on the effective
// merged config, so constraints apply across reinstalls too (a stored
// --maxmemory-policy without a cap — from a hand-edit or a dropped flag — is
// still caught). Each knob is opt-in: an empty value means "Redis's own
// default" and is always valid. The save schedule is validated as pairs of
// integers (or the "no" disable token) because a malformed schedule would
// otherwise surface as a redis-server startup failure inside the container,
// where it is much harder to diagnose. Cross-instance replica checks (master
// exists, same major, password equality) live in resolveRedisReplica, which has
// the whole config; only the self-contained host/port invariant is checked here.
func validateRedisKnobs(rc config.RedisConfig) error {
	if rc.ReplicaHost != "" && rc.ReplicaPort == 0 {
		return fmt.Errorf("a replica needs a master port (ReplicaHost %q set with ReplicaPort 0 — reinstall with --replica-of/--replica-of-port)", rc.ReplicaHost)
	}
	if rc.MaxMemoryPolicy != "" {
		if rc.MaxMemory == "" {
			return fmt.Errorf("--maxmemory-policy requires --maxmemory (the policy only applies under a memory cap)")
		}
		if !slices.Contains(redisEvictionPolicies, rc.MaxMemoryPolicy) {
			return fmt.Errorf("unknown --maxmemory-policy %q (available: %s)", rc.MaxMemoryPolicy, strings.Join(redisEvictionPolicies, ", "))
		}
	}
	if rc.AppendFsync != "" {
		if !rc.AOF {
			return fmt.Errorf("--appendfsync requires --aof (it tunes the AOF flush the log enables)")
		}
		if !slices.Contains(redisAppendFsyncs, rc.AppendFsync) {
			return fmt.Errorf("unknown --appendfsync %q (available: %s)", rc.AppendFsync, strings.Join(redisAppendFsyncs, ", "))
		}
	}
	if rc.SaveSchedule != "" && rc.SaveSchedule != "no" {
		fields := strings.Fields(rc.SaveSchedule)
		if len(fields)%2 != 0 {
			return fmt.Errorf(`malformed --save %q: want "seconds changes" pairs, e.g. "900 1 300 10" (or "no" to disable snapshots)`, rc.SaveSchedule)
		}
		for _, f := range fields {
			n, err := strconv.Atoi(f)
			if err != nil || n < 0 {
				return fmt.Errorf(`malformed --save %q: %q is not a non-negative integer (want "seconds changes" pairs, e.g. "900 1 300 10")`, rc.SaveSchedule, f)
			}
		}
	}
	return nil
}

// redisEffectivePolicy is the eviction policy actually in force under a
// memory cap — the stored one, or allkeys-lru when the operator did not pick
// one (same fallback redisServerArgs emits, so displays never disagree with
// the container's argv).
func redisEffectivePolicy(rc config.RedisConfig) string {
	if rc.MaxMemoryPolicy != "" {
		return rc.MaxMemoryPolicy
	}
	return "allkeys-lru"
}

// redisRoleSummary renders the replication Role line shared by the install
// summary and addon list: a plain master, or a read replica naming the master
// endpoint it syncs from (the stored ReplicaHost is the resolved target, so
// what is displayed is what the argv carries).
func redisRoleSummary(rc config.RedisConfig) string {
	if rc.ReplicaHost != "" {
		return fmt.Sprintf("replica of %s:%d (read-only)", rc.ReplicaHost, rc.ReplicaPort)
	}
	return "master"
}

// redisPersistenceSummary renders the persistence line shared by the install
// summary and addon list: what actually protects the data right now.
func redisPersistenceSummary(rc config.RedisConfig) string {
	snapshotsOff := rc.SaveSchedule == "no"
	switch {
	case rc.AOF && snapshotsOff:
		return "aof only (appendonly yes" + redisFsyncSuffix(rc) + ", snapshots off)"
	case rc.AOF:
		return "rdb + aof (" + redisSaveLabel(rc) + ", appendonly yes" + redisFsyncSuffix(rc) + ")"
	case snapshotsOff:
		return "none (snapshots off, no aof)"
	default:
		return "rdb snapshots (" + redisSaveLabel(rc) + ")"
	}
}

func redisFsyncSuffix(rc config.RedisConfig) string {
	if rc.AppendFsync == "" {
		return ""
	}
	return ", appendfsync " + rc.AppendFsync
}

func redisSaveLabel(rc config.RedisConfig) string {
	if rc.SaveSchedule == "" {
		return "default save schedule"
	}
	return "save " + rc.SaveSchedule
}

// resolveRedisVersion applies the version-selection rules for the redis addon
// and writes the outcome into rc (Version / ImageTag). Precedence: an explicit
// --image wins outright and --version is demoted to a display label
// (reverse-parsed from the tag when it was not given); a bare --version looks
// the tag up in the config table; neither falls back to DefaultRedisMajor.
// An unknown --version is an error listing the selectable majors. Called after
// flags merge into the existing config entry, so a stored Version/ImageTag pair
// survives a no-flag reinstall and a --version change re-resolves the tag.
func resolveRedisVersion(rc *config.RedisConfig, version, imageTag string) error {
	if version != "" {
		if _, ok := config.RedisImageTagForMajor(version); !ok {
			return fmt.Errorf("unknown redis --version %q (available: %s)", version, strings.Join(config.RedisMajors(), ", "))
		}
		rc.Version = version
	}
	if imageTag != "" {
		rc.ImageTag = imageTag
		if version == "" {
			// --image bypasses the table: recover the major for display, or
			// leave it empty and show the tag verbatim.
			rc.Version = config.RedisMajorForImageTag(imageTag)
		}
		return nil
	}
	if rc.Version == "" {
		rc.Version = config.DefaultRedisMajor
	}
	tag, ok := config.RedisImageTagForMajor(rc.Version)
	if !ok {
		// Reachable only with a hand-edited version and no --image; ApplyDefaults
		// deliberately leaves such an ImageTag empty rather than rewriting the
		// operator's value, so surface it here instead of a confusing pull error.
		return fmt.Errorf("unknown redis version %q for addon %q (available: %s, or pass --image)", rc.Version, rc.Name, strings.Join(config.RedisMajors(), ", "))
	}
	// A --version given now (or a previously-resolved major) always re-derives
	// the tag; a stale stored tag from an older pgcli must not outrank it.
	rc.ImageTag = tag
	return nil
}

// runAddonInstallRedis installs a Redis container — a standalone KV store for
// cache, session, ranking and atomic-counter data. It is the simplest addon
// shape (one container, one port, no cluster modes) so the manager mirrors
// silo/rustfs, but it is the first version-selectable one: --version 7|8
// resolves through the config table (see resolveRedisVersion). Password follows
// rustfs's RootPassword: generated here, once, never in ApplyDefaults.
func runAddonInstallRedis(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "redis"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	version, _ := cmd.Flags().GetString("version")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	port, _ := cmd.Flags().GetInt("port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	password, _ := cmd.Flags().GetString("password")
	maxMemory, _ := cmd.Flags().GetString("maxmemory")
	maxMemoryPolicy, _ := cmd.Flags().GetString("maxmemory-policy")
	aof, _ := cmd.Flags().GetBool("aof")
	aofSet := cmd.Flags().Changed("aof")
	appendfsync, _ := cmd.Flags().GetString("appendfsync")
	saveSchedule, _ := cmd.Flags().GetString("save")
	replicaOf, _ := cmd.Flags().GetString("replica-of")
	replicaOfHost, _ := cmd.Flags().GetString("replica-of-host")
	replicaOfPort, _ := cmd.Flags().GetInt("replica-of-port")
	force, _ := cmd.Flags().GetBool("force")

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

	if cfg.Addons.Redis == nil {
		cfg.Addons.Redis = make(map[string]config.RedisConfig)
	}
	existing, ok := cfg.Addons.Redis[name]
	if !ok {
		existing = config.RedisConfig{
			ContainerName: "pgcli-redis" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if dataDir != "" {
		existing.DataDir = dataDir
	}
	if port != 0 {
		existing.Port = port
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if password != "" {
		existing.Password = password
	}
	if maxMemory != "" {
		existing.MaxMemory = maxMemory
	}
	if maxMemoryPolicy != "" {
		existing.MaxMemoryPolicy = maxMemoryPolicy
	}
	if aofSet {
		existing.AOF = aof
	}
	if appendfsync != "" {
		existing.AppendFsync = appendfsync
	}
	if saveSchedule != "" {
		existing.SaveSchedule = saveSchedule
	}
	// Resolve the replica role (if requested) before validating and before the
	// version step: a local --replica-of adopts the master's major and password,
	// and a bad topology (missing master, cross-major, remote without a
	// password) fails here rather than at pull time.
	passwordGiven := cmd.Flags().Changed("password")
	if err := resolveRedisReplica(cfg, &existing, name, replicaOf, replicaOfHost, version, replicaOfPort, passwordGiven); err != nil {
		return err
	}
	// Validate the effective (merged) values: stored flags count too, so
	// --maxmemory-policy alone is legal on an instance that already has a
	// --maxmemory cap.
	if err := validateRedisKnobs(existing); err != nil {
		return err
	}
	if existing.Name == "" {
		existing.Name = name
	}
	if err := resolveRedisVersion(&existing, version, imageTag); err != nil {
		return err
	}
	cfg.Addons.Redis[name] = existing

	// ApplyDefaults fills ContainerName/Listen and assigns the port from the
	// redis_start_port pool. Password is NOT managed there — it is a secret the
	// install path owns (ApplyDefaults runs on every load and must stay
	// deterministic).
	cfg.ApplyDefaults()
	rc := cfg.Addons.Redis[name]

	// macOS: redis serves on the pgcli-net bridge with the port published, so
	// bring up the machine and the bridge first (no-op on Linux, where host
	// networking is used).
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	rm, err := podman.NewRedisManager(cfg)
	if err != nil {
		return fmt.Errorf("redis manager: %w", err)
	}

	// Reuse semantics identical to the object stores: a live container is left
	// alone (reinstall is a no-op), a stopped one is started, and only --force
	// recreates it so changed port/listen/password take effect.
	exists, err := rm.ContainerExists(rc.ContainerName)
	if err != nil {
		return err
	}
	skipped := false
	if exists && !force {
		skipped = true
		if running, _ := rm.ContainerRunning(rc.ContainerName); running {
			fmt.Printf("-> redis container %q already running; skipping creation\n", rc.ContainerName)
		} else {
			fmt.Printf("-> redis container %q exists but is stopped; starting it\n", rc.ContainerName)
			if err := rm.StartContainer(&rc); err != nil {
				return err
			}
		}
	} else {
		// Fresh install (or the config survived a `remove` that kept it):
		// generate the password once so a reinstall of the same name can still
		// read the RDB it left behind.
		if rc.Password == "" {
			pw, err := generatePassword(20)
			if err != nil {
				return fmt.Errorf("generating redis password: %w", err)
			}
			rc.Password = pw
		}
		fmt.Printf("-> Preparing redis image %s...\n", rc.ImageTag)
		if err := rm.EnsureImage(rc.ImageTag); err != nil {
			return err
		}
		if rc.MaxMemory != "" {
			if redisEffectivePolicy(rc) == "noeviction" {
				fmt.Println("-> NOTE: --maxmemory with noeviction — writes fail past the cap instead of evicting (a hard ceiling).")
			} else {
				fmt.Printf("-> NOTE: --maxmemory with %s — Redis evicts keys to stay under the cap (a cache, not a hard store).\n", redisEffectivePolicy(rc))
			}
		}
		if rc.SaveSchedule == "no" && !rc.AOF {
			fmt.Println("-> NOTE: snapshots are disabled and AOF is off — this instance loses its whole dataset on restart. Pair --save no with --aof for a durable store.")
		}
		if rc.ReplicaHost != "" {
			fmt.Printf("-> NOTE: read replica — it starts an initial full sync from the master at %s:%d into its own data dir, and rejects writes (READONLY). Send writes to the master.\n", rc.ReplicaHost, rc.ReplicaPort)
		}
		fmt.Println("-> Starting redis container...")
		if err := rm.EnsureContainer(&rc); err != nil {
			return err
		}
	}

	cfg.Addons.Redis[name] = rc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	if skipped {
		fmt.Println()
		fmt.Printf("✓ redis already present: %q\n", name)
	} else {
		fmt.Println()
		fmt.Printf("✓ redis installed: %q\n", name)
	}
	fmt.Printf("  Container:  %s\n", rc.ContainerName)
	fmt.Printf("  Version:    %s\n", rc.Version)
	fmt.Printf("  Image:      %s\n", rc.ImageTag)
	fmt.Printf("  Data:       %s\n", rm.DataDir(&rc))
	fmt.Printf("  Address:    %s:%d\n", rc.Listen, rc.Port)
	fmt.Printf("  Role:       %s\n", redisRoleSummary(rc))
	if rc.MaxMemory != "" {
		fmt.Printf("  Maxmemory:   %s (%s)\n", rc.MaxMemory, redisEffectivePolicy(rc))
	}
	fmt.Printf("  Persistence: %s\n", redisPersistenceSummary(rc))
	fmt.Println()
	fmt.Printf("  Password:    %s\n", rc.Password)
	fmt.Println()
	fmt.Printf("  Client:      pg redis-cli ping\n")
	fmt.Printf("  Raw DSN:     redis://:%s@%s:%d/0\n", rc.Password, rc.Listen, rc.Port)
	if rc.Listen == "0.0.0.0" {
		fmt.Println("  NOTE: listening on every interface; the password above is the only gate.")
		fmt.Printf("        loopback-only: pg addon install redis --name %s --listen 127.0.0.1 --force\n", name)
	}
	return nil
}

// runAddonInstallPostgrest installs a PostgREST container: a single stateless
// process that exposes a PostgreSQL schema as a RESTful API. Like pgbouncer it
// works in two modes — local (-i, fronting an instance pgcli manages) or
// remote (--dsn + --pg-name, fronting any PG endpoint: a direct instance, a
// PgBouncer pool, or a Patroni cluster behind its HAProxy listener). Unlike
// pgbouncer it touches nothing inside the database: no auth user, no config
// files, no data dir. The DSN is passed to the container verbatim as
// PGRST_DB_URI; authenticator role / GRANTs / schema exposure stay the
// operator's (or the app's migrations) — install prints the hints.
func runAddonInstallPostgrest(cmd *cobra.Command) error {
	dsn, _ := cmd.Flags().GetString("dsn")
	pgName, _ := cmd.Flags().GetString("pg-name")
	port, _ := cmd.Flags().GetInt("port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	dbPool, _ := cmd.Flags().GetInt("db-pool")
	schemas, _ := cmd.Flags().GetString("schema")
	anonRole, _ := cmd.Flags().GetString("anon-role")
	jwtSecret, _ := cmd.Flags().GetString("jwt-secret")
	imageTag, _ := cmd.Flags().GetString("image")
	force, _ := cmd.Flags().GetBool("force")

	// Validate mutual exclusivity: --dsn/--pg-name vs -i
	if dsn != "" || pgName != "" {
		if dsn == "" {
			return fmt.Errorf("--pg-name requires --dsn")
		}
		if pgName == "" {
			return fmt.Errorf("--dsn requires --pg-name to identify this remote PostgREST API")
		}
		if err := checkDSNInstanceConflict(cmd); err != nil {
			return err
		}
	}

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

	// Determine mode: remote (--dsn + --pg-name, stored under
	// addons.postgrest.<pgName>) or local (-i, stored as an instance sidecar
	// keyed by the instance name). addonName is the map key (remote) or the
	// instance name (local) — same scheme as pgbouncer.
	remote := dsn != "" && pgName != ""
	var addonName string
	if remote {
		addonName = pgName
	} else {
		if _, ok := cfg.Instances[cfgInstance]; !ok {
			return fmt.Errorf("instance %q not found in config", cfgInstance)
		}
		cfg.SetInstance(cfgInstance)
		dsn = cfg.GetPostgresURL()
		addonName = cfgInstance
	}

	// Merge flags into the stored config (map value for remote, pointer for
	// local), then write back before ApplyDefaults so port assignment and
	// container naming see the final values.
	if remote {
		if cfg.Addons.Postgrest == nil {
			cfg.Addons.Postgrest = make(map[string]config.PostgrestConfig)
		}
		pc, _ := cfg.Addons.Postgrest[addonName]
		pc.Name = addonName
		pc.DSN = dsn
		if cmd.Flags().Changed("port") {
			pc.HostPort = port
		}
		if listenAddr != "" {
			pc.Listen = listenAddr
		}
		if cmd.Flags().Changed("db-pool") {
			pc.DbPool = dbPool
		}
		if schemas != "" {
			pc.Schemas = schemas
		}
		if anonRole != "" {
			pc.AnonRole = anonRole
		}
		if jwtSecret != "" {
			pc.JwtSecret = jwtSecret
		}
		if imageTag != "" {
			pc.ImageTag = imageTag
		}
		cfg.Addons.Postgrest[addonName] = pc
	} else {
		inst := cfg.Instances[addonName]
		if inst.Addons.Postgrest == nil {
			inst.Addons.Postgrest = &config.PostgrestConfig{}
		}
		pc := inst.Addons.Postgrest
		pc.DSN = dsn
		if cmd.Flags().Changed("port") {
			pc.HostPort = port
		}
		if listenAddr != "" {
			pc.Listen = listenAddr
		}
		if cmd.Flags().Changed("db-pool") {
			pc.DbPool = dbPool
		}
		if schemas != "" {
			pc.Schemas = schemas
		}
		if anonRole != "" {
			pc.AnonRole = anonRole
		}
		if jwtSecret != "" {
			pc.JwtSecret = jwtSecret
		}
		if imageTag != "" {
			pc.ImageTag = imageTag
		}
		cfg.Instances[addonName] = inst
	}

	// Set BackendHost best-effort from the DSN. ParseDSN can reject an unusual
	// URI (or one without a password) — that only costs the display field, the
	// raw DSN still reaches the container untouched.
	if host, bport, _, _, _, perr := podman.ParseDSN(dsn); perr == nil {
		if remote {
			pc := cfg.Addons.Postgrest[addonName]
			pc.BackendHost = fmt.Sprintf("%s:%d", host, bport)
			cfg.Addons.Postgrest[addonName] = pc
		} else {
			inst := cfg.Instances[addonName]
			inst.Addons.Postgrest.BackendHost = fmt.Sprintf("%s:%d", host, bport)
			cfg.Instances[addonName] = inst
		}
	}

	// Soft Patroni check: a member's direct PG port fronted by PostgREST breaks
	// writes on failover (the old leader stops accepting them and the DSN keeps
	// pointing there). If this host knows an HAProxy fronting a scope whose
	// members include this backend, steer toward the LB's rw listener.
	if w := postgrestPatroniMemberWarning(cfg, dsn); w != "" {
		fmt.Printf("-> WARNING: %s\n", w)
	}

	cfg.ApplyDefaults()

	// Re-fetch after ApplyDefaults (map values are copied; port may be assigned)
	if remote {
		if cfg.Addons.Postgrest == nil {
			return fmt.Errorf("postgrest addon %q not found in config after apply", addonName)
		}
	}

	pm, err := podman.New(cfg)
	if err != nil {
		return fmt.Errorf("podman: %w", err)
	}
	// macOS: bring up the podman machine and the pgcli-net bridge the container
	// joins (no-ops on Linux, where PostgREST uses host networking).
	if err := pm.EnsureMachine(); err != nil {
		return err
	}
	if err := pm.EnsureNetwork(); err != nil {
		return err
	}

	fmt.Println("-> Checking PG connectivity...")
	if err := pm.CheckDSNReachable(dsn); err != nil {
		return err
	}

	pgm, err := podman.NewPostgrestManager(cfg)
	if err != nil {
		return fmt.Errorf("postgrest manager: %w", err)
	}

	// Load the (now fully-defaulted) config for the manager call.
	var pc *config.PostgrestConfig
	if remote {
		v := cfg.Addons.Postgrest[addonName]
		pc = &v
	} else {
		pc = cfg.Instances[addonName].Addons.Postgrest
	}

	// Idempotency like MinIO/silo: an existing container is reused (a stopped
	// one is started); --force recreates it so changed dsn/port/db-pool/schema
	// take effect.
	exists, err := pgm.ContainerExists(pc.ContainerName)
	if err != nil {
		return err
	}
	skipped := false
	if exists && !force {
		skipped = true
		if running, _ := pgm.ContainerRunning(pc.ContainerName); running {
			fmt.Printf("-> PostgREST container %q already running; skipping creation\n", pc.ContainerName)
		} else {
			fmt.Printf("-> PostgREST container %q exists but is stopped; starting it\n", pc.ContainerName)
			if err := pgm.StartContainer(pc); err != nil {
				return err
			}
		}
	} else {
		fmt.Printf("-> Preparing PostgREST image %s...\n", pc.ImageTag)
		if err := pgm.EnsureImage(pc.ImageTag); err != nil {
			return err
		}
		fmt.Println("-> Starting PostgREST container...")
		if err := pgm.EnsureContainer(pc); err != nil {
			return err
		}
	}

	if remote {
		cfg.Addons.Postgrest[addonName] = *pc
	} else {
		inst := cfg.Instances[addonName]
		inst.Addons.Postgrest = pc
		cfg.Instances[addonName] = inst
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	if skipped {
		fmt.Printf("✓ postgrest already present: %q\n", addonName)
	} else {
		fmt.Printf("✓ postgrest installed: %q\n", addonName)
	}
	fmt.Printf("  Container:    %s\n", pc.ContainerName)
	fmt.Printf("  Image:        %s\n", pc.ImageTag)
	fmt.Printf("  Backend:      %s\n", pc.BackendHost)
	fmt.Printf("  REST API:     http://%s:%d\n", pc.Listen, pc.HostPort)
	fmt.Printf("  Schema:       %s\n", postgrestSchemasDisplay(pc.Schemas))
	fmt.Printf("  DB pool:      %s\n", postgrestPoolDisplay(pc.DbPool))
	fmt.Printf("  Anon role:    %s\n", postgrestAnonRoleDisplay(pc.AnonRole))
	fmt.Printf("  JWT auth:     %s\n", postgrestJwtDisplay(pc.JwtSecret))
	fmt.Println()
	fmt.Printf("  Database side is NOT touched by pgcli. PostgREST needs:\n")
	fmt.Printf("    - a login role it connects with (the DSN user);\n")
	fmt.Printf("    - for unauthenticated requests: a NOINHERIT role passed via\n")
	fmt.Printf("      --anon-role that has GRANT USAGE on the exposed schema and\n")
	fmt.Printf("      GRANTs on its tables (without it, anonymous access is off);\n")
	fmt.Printf("    - for JWT requests: a secret via --jwt-secret, and a NOINHERIT\n")
	fmt.Printf("      role named in each token's 'role' claim (without --jwt-secret,\n")
	fmt.Printf("      no token is accepted; without either flag, every request is 401);\n")
	fmt.Printf("    - after schema changes: NOTIFY pgrst, 'reload schema'\n")
	fmt.Printf("      (or restart this container) to refresh its schema cache.\n")
	return nil
}

// postgrestSchemasDisplay renders the exposed-schema setting for the summary,
// naming PostgREST's own default when the field is unset.
func postgrestSchemasDisplay(schemas string) string {
	if schemas == "" {
		return "public (PostgREST default)"
	}
	return schemas
}

// postgrestPoolDisplay renders the DB pool size for the summary, naming
// PostgREST's own default (10) when unset.
func postgrestPoolDisplay(dbPool int) string {
	if dbPool == 0 {
		return "10 (PostgREST default)"
	}
	return strconv.Itoa(dbPool)
}

// postgrestAnonRoleDisplay renders the anonymous role for the summary and
// `addon list` — unset means unauthenticated requests are refused, which is
// worth surfacing explicitly.
func postgrestAnonRoleDisplay(role string) string {
	if role == "" {
		return "none (anonymous access disabled — JWT only)"
	}
	return role
}

// postgrestJwtDisplay surfaces whether JWT auth is on for the summary and
// `addon list`. The secret is never printed — only its presence.
func postgrestJwtDisplay(secret string) string {
	if secret == "" {
		return "off (no --jwt-secret)"
	}
	return "enabled (secret set)"
}

// postgrestPatroniMemberWarning checks the DSN's backend against every Patroni
// member this host's config knows about. If the backend is a member's DIRECT
// PG address while an HAProxy fronts that same scope, warn — a direct member
// DSN loses writes after a failover, the LB listener follows the leader.
// Returns "" when there is nothing to warn about (not a Patroni backend at
// all, no HAProxy for that scope, or the DSN already points at the LB).
func postgrestPatroniMemberWarning(cfg *config.Config, dsn string) string {
	host, port, _, _, _, err := podman.ParseDSN(dsn)
	if err != nil {
		return "" // unparseable DSN: no opinion
	}
	for scope, cluster := range cfg.Addons.Patroni {
		for mbName, mb := range cluster.Members {
			mHost := pgConnectHost(mb)
			if (mHost == host || mHost == "127.0.0.1" && host == "localhost") && mb.HostPort == port {
				// backend is this member's direct PG port — is there an LB?
				for _, lb := range cfg.Addons.HAProxy {
					if lb.HAScope == scope && lb.WritePort != 0 {
						return fmt.Sprintf("DSN points at Patroni member %q's direct PG port (%s:%d) in scope %q; after a failover that node stops accepting writes. Point the DSN at the HAProxy listener instead: postgres://...@%s:%d/<db>",
							mbName, mHost, port, scope, lb.Listen, lb.WritePort)
					}
				}
				return fmt.Sprintf("DSN points at Patroni member %q's direct PG port (%s:%d) in scope %q; after a failover that node stops accepting writes. Install an HAProxy listener for the scope (pg addon install haproxy --ha %s) and point the DSN at it.",
					mbName, mHost, port, scope, scope)
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// list logic
// ---------------------------------------------------------------------------

func runAddonList() error {
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

	pbMgr, _ := podman.NewPgBouncerManager(cfg)
	pgm, _ := podman.NewPostgrestManager(cfg)
	em, _ := podman.NewEtcdManager(cfg)
	dm, _ := podman.NewPgDogManager(cfg)

	// Local add-ons (from instances)
	fmt.Println("Local add-ons:")
	hasLocal := false
	for name, inst := range cfg.Instances {
		if inst.Addons.PgBouncer == nil && inst.Addons.Postgrest == nil {
			continue
		}
		if pb := inst.Addons.PgBouncer; pb != nil {
			hasLocal = true
			status := "stopped"
			if pbMgr != nil {
				if running, err := pbMgr.ContainerRunning(pb.ContainerName); err == nil && running {
					status = "running"
				}
			}
			fmt.Printf("  %s (instance: %s)\n", "pgbouncer", name)
			fmt.Printf("    Status:      %s\n", status)
			fmt.Printf("    Host:        %s:%d\n", inst.Postgres.Host, pb.HostPort)
			fmt.Printf("    Backend:     %s\n", pb.BackendHost)
			fmt.Printf("    Port:        %d\n", pb.HostPort)
			fmt.Printf("    Pool mode:   %s\n", pb.PoolMode)
			fmt.Printf("    Container:   %s\n", pb.ContainerName)
		}
		if pr := inst.Addons.Postgrest; pr != nil {
			hasLocal = true
			status := "stopped"
			if pgm != nil {
				if running, err := pgm.ContainerRunning(pr.ContainerName); err == nil && running {
					status = "running"
				}
			}
			fmt.Printf("  %s (instance: %s)\n", "postgrest", name)
			fmt.Printf("    Status:      %s\n", status)
			fmt.Printf("    REST URL:    http://%s:%d\n", pr.Listen, pr.HostPort)
			fmt.Printf("    Backend:     %s\n", pr.BackendHost)
			fmt.Printf("    Schema:      %s\n", postgrestSchemasDisplay(pr.Schemas))
			fmt.Printf("    DB pool:     %s\n", postgrestPoolDisplay(pr.DbPool))
			fmt.Printf("    Anon role:   %s\n", postgrestAnonRoleDisplay(pr.AnonRole))
			fmt.Printf("    JWT auth:    %s\n", postgrestJwtDisplay(pr.JwtSecret))
			fmt.Printf("    Container:   %s\n", pr.ContainerName)
		}
	}
	if !hasLocal {
		fmt.Println("  (none)")
	}

	// Remote add-ons (from top-level addons)
	fmt.Println()
	fmt.Println("Remote add-ons:")
	hasRemote := false
	if cfg.Addons.PgBouncer != nil {
		for name, pb := range cfg.Addons.PgBouncer {
			hasRemote = true
			status := "stopped"
			if pbMgr != nil {
				if running, err := pbMgr.ContainerRunning(pb.ContainerName); err == nil && running {
					status = "running"
				}
			}
			fmt.Printf("  %s (pg-name: %s)\n", "pgbouncer", name)
			fmt.Printf("    Status:      %s\n", status)
			fmt.Printf("    Host:        127.0.0.1:%d\n", pb.HostPort)
			fmt.Printf("    Backend:     %s\n", pb.BackendHost)
			fmt.Printf("    Port:        %d\n", pb.HostPort)
			fmt.Printf("    Pool mode:   %s\n", pb.PoolMode)
			fmt.Printf("    Container:   %s\n", pb.ContainerName)
		}
	}
	for name, pr := range cfg.Addons.Postgrest {
		hasRemote = true
		status := "stopped"
		if pgm != nil {
			if running, err := pgm.ContainerRunning(pr.ContainerName); err == nil && running {
				status = "running"
			}
		}
		fmt.Printf("  %s (pg-name: %s)\n", "postgrest", name)
		fmt.Printf("    Status:      %s\n", status)
		fmt.Printf("    REST URL:    http://%s:%d\n", pr.Listen, pr.HostPort)
		fmt.Printf("    Backend:     %s\n", pr.BackendHost)
		fmt.Printf("    Schema:      %s\n", postgrestSchemasDisplay(pr.Schemas))
		fmt.Printf("    DB pool:     %s\n", postgrestPoolDisplay(pr.DbPool))
		fmt.Printf("    Anon role:   %s\n", postgrestAnonRoleDisplay(pr.AnonRole))
		fmt.Printf("    JWT auth:    %s\n", postgrestJwtDisplay(pr.JwtSecret))
		fmt.Printf("    Container:   %s\n", pr.ContainerName)
	}
	if !hasRemote {
		fmt.Println("  (none)")
	}

	// etcd (shared infrastructure, top-level addon)
	fmt.Println()
	fmt.Println("Infra add-ons (etcd):")
	hasEtcd := false
	for name, ec := range cfg.Addons.Etcd {
		hasEtcd = true
		status := "stopped"
		if em != nil {
			if running, err := em.ContainerRunning(ec.ContainerName); err == nil && running {
				status = "running"
			}
		}
		fmt.Printf("  %s (name: %s)\n", "etcd", name)
		fmt.Printf("    Status:      %s\n", status)
		fmt.Printf("    Cluster:     %s\n", ec.ClusterName)
		fmt.Printf("    Client URL:  http://127.0.0.1:%d\n", ec.ClientPort)
		fmt.Printf("    Client port: %d\n", ec.ClientPort)
		fmt.Printf("    Peer port:   %d\n", ec.PeerPort)
		fmt.Printf("    Image:       %s\n", ec.ImageTag)
		fmt.Printf("    Container:   %s\n", ec.ContainerName)
	}
	if !hasEtcd {
		fmt.Println("  (none)")
	}

	// pgdog (shared infrastructure proxy, top-level addon)
	fmt.Println()
	fmt.Println("Infra add-ons (pgdog):")
	hasPgDog := false
	for name, pd := range cfg.Addons.PgDog {
		hasPgDog = true
		status := "stopped"
		if dm != nil {
			if running, err := dm.ContainerRunning(pd.ContainerName); err == nil && running {
				status = "running"
			}
		}
		fmt.Printf("  %s (name: %s)\n", "pgdog", name)
		fmt.Printf("    Status:      %s\n", status)
		fmt.Printf("    Listen:      %s\n", pd.ClientAddr())
		fmt.Printf("    Client port: %d\n", pd.HostPort)
		fmt.Printf("    Metrics:     http://%s:%d/metrics\n", pd.Host, pd.OpenmetricsPort)
		fmt.Printf("    Pool mode:   %s\n", pd.PoolerMode)
		fmt.Printf("    Backends:    %d\n", len(pd.Backends))
		fmt.Printf("    Users:       %d\n", len(pd.Users))
		fmt.Printf("    Image:       %s\n", pd.ImageTag)
		fmt.Printf("    Container:   %s\n", pd.ContainerName)
	}
	if !hasPgDog {
		fmt.Println("  (none)")
	}

	// haproxy (Patroni load balancer, top-level addon; Linux-only manager)
	fmt.Println()
	fmt.Println("Infra add-ons (haproxy):")
	hasHAProxy := false
	if hm, err := podman.NewHAProxyManager(cfg); err == nil {
		for name, hc := range cfg.Addons.HAProxy {
			hasHAProxy = true
			status := "stopped"
			if running, err := hm.ContainerRunning(hc.ContainerName); err == nil && running {
				status = "running"
			}
			fmt.Printf("  %s (name: %s)\n", "haproxy", name)
			fmt.Printf("    Status:      %s\n", status)
			fmt.Printf("    Mode:        %s\n", hc.EffectiveMode())
			if hc.HAScope != "" {
				fmt.Printf("    Patroni:     %s\n", hc.HAScope)
			}
			fmt.Printf("    Read-write:  %s:%d\n", hc.Listen, hc.WritePort)
			if hc.EffectiveMode() == "split" {
				fmt.Printf("    Read-only:   %s:%d\n", hc.Listen, hc.ReadPort)
			}
			fmt.Printf("    Stats:       http://%s:%d/\n", hc.Listen, hc.StatsPort)
			fmt.Printf("    Backends:    %d\n", len(hc.Targets))
			for _, t := range hc.Targets {
				fmt.Printf("      - %-12s %s:%d (check :%d)\n", t.Name, t.Host, t.PGPort, t.RestPort)
			}
			fmt.Printf("    Image:       %s\n", hc.ImageTag)
			fmt.Printf("    Container:   %s\n", hc.ContainerName)
		}
	} else if len(cfg.Addons.HAProxy) > 0 {
		// Configured but the manager is unavailable (macOS): still show them.
		for name, hc := range cfg.Addons.HAProxy {
			hasHAProxy = true
			fmt.Printf("  %s (name: %s)\n", "haproxy", name)
			fmt.Printf("    Status:      n/a (%v)\n", err)
			fmt.Printf("    Mode:        %s\n", hc.EffectiveMode())
			fmt.Printf("    Read-write:  %s:%d\n", hc.Listen, hc.WritePort)
			if hc.EffectiveMode() == "split" {
				fmt.Printf("    Read-only:   %s:%d\n", hc.Listen, hc.ReadPort)
			}
			fmt.Printf("    Backends:    %d\n", len(hc.Targets))
			fmt.Printf("    Container:   %s\n", hc.ContainerName)
		}
	}
	if !hasHAProxy {
		fmt.Println("  (none)")
	}

	// minio (single-node object storage, top-level addon; Linux-only manager)
	fmt.Println()
	fmt.Println("Infra add-ons (minio):")
	hasMinio := false
	if mm, err := podman.NewMinioManager(cfg); err == nil {
		for name, mc := range cfg.Addons.Minio {
			hasMinio = true
			status := "stopped"
			if running, err := mm.ContainerRunning(mc.ContainerName); err == nil && running {
				status = "running"
			}
			fmt.Printf("  %s (name: %s)\n", "minio", name)
			fmt.Printf("    Status:      %s\n", status)
			fmt.Printf("    Listen:      %s\n", mc.Listen)
			fmt.Printf("    API port:    %d\n", mc.APIPort)
			fmt.Printf("    Console port: %d\n", mc.ConsolePort)
			scheme := "http"
			if mc.TLS {
				scheme = "https"
			}
			fmt.Printf("    Console URL: %s://%s:%d/\n", scheme, mc.Listen, mc.ConsolePort)
			if podman.BYOTLS(mc.TLS, mc.CertFile, mc.KeyFile) {
				fmt.Printf("    TLS:         on (BYO cert: %s, key: %s)\n", mc.CertFile, mc.KeyFile)
				fmt.Printf("                   replace files + pg addon install minio --name %s --tls-cert ... --tls-key ... --force to renew\n", name)
			} else if mc.TLS {
				caPath := filepath.Join(mm.TLSDir(&mc), tlsca.CACertFile)
				fmt.Printf("    TLS:         on (CA: %s)\n", caPath)
				// mc.Listen is the bind address (often 0.0.0.0) — the hints use
				// a dialable example host instead.
				fmt.Printf("                   as pgBackRest repo CA: pg backup setup --s3-endpoint <host>:%d --s3-ca-file %s\n", mc.APIPort, caPath)
				fmt.Printf("                   from another host:     pg backup fetch-ca <this-host>:%d\n", mc.APIPort)
			}
			if len(mc.Drives) > 0 {
				driveMode := "SNMD"
				if len(mc.Endpoints) > 0 {
					driveMode = "MNMD, this node"
				}
				fmt.Printf("    Drives:      %d (%s)\n", len(mc.Drives), driveMode)
				for i, d := range mc.Drives {
					fmt.Printf("      - /data%d <- %s\n", i+1, d)
				}
			} else {
				fmt.Printf("    Data:        %s\n", mm.DataDir(&mc))
			}
			if len(mc.Endpoints) > 0 {
				endpointMode := "distributed"
				if len(mc.Drives) > 0 {
					endpointMode = fmt.Sprintf("MNMD, %d nodes × %d drives/node", len(mc.Endpoints)/len(mc.Drives), len(mc.Drives))
				}
				fmt.Printf("    Endpoints:   %d (%s)\n", len(mc.Endpoints), endpointMode)
				for _, ep := range mc.Endpoints {
					fmt.Printf("      - %s\n", ep)
				}
			}
			fmt.Printf("    Root user:   %s\n", mc.RootUser)
			fmt.Printf("    Image:       %s\n", mc.ImageTag)
			fmt.Printf("    Container:   %s\n", mc.ContainerName)
		}
	} else if len(cfg.Addons.Minio) > 0 {
		// Configured but the manager is unavailable (macOS/arm): still show them.
		for name, mc := range cfg.Addons.Minio {
			hasMinio = true
			fmt.Printf("  %s (name: %s)\n", "minio", name)
			fmt.Printf("    Status:      n/a (%v)\n", err)
			fmt.Printf("    Listen:      %s\n", mc.Listen)
			fmt.Printf("    API port:    %d\n", mc.APIPort)
			fmt.Printf("    Console port: %d\n", mc.ConsolePort)
			fmt.Printf("    Container:   %s\n", mc.ContainerName)
		}
	}
	if !hasMinio {
		fmt.Println("  (none)")
	}

	// silo (Pigsty MinIO fork — S3 object storage, top-level addon)
	fmt.Println()
	fmt.Println("Infra add-ons (silo):")
	hasSilo := false
	if sm, err := podman.NewSiloManager(cfg); err == nil {
		for name, sc := range cfg.Addons.Silo {
			hasSilo = true
			status := "stopped"
			if running, err := sm.ContainerRunning(sc.ContainerName); err == nil && running {
				status = "running"
			}
			fmt.Printf("  %s (name: %s)\n", "silo", name)
			fmt.Printf("    Status:      %s\n", status)
			fmt.Printf("    Listen:      %s\n", sc.Listen)
			fmt.Printf("    API port:    %d\n", sc.APIPort)
			fmt.Printf("    Console port: %d\n", sc.ConsolePort)
			scheme := "http"
			if sc.TLS {
				scheme = "https"
			}
			fmt.Printf("    Console URL: %s://%s:%d/\n", scheme, sc.Listen, sc.ConsolePort)
			if podman.BYOTLS(sc.TLS, sc.CertFile, sc.KeyFile) {
				fmt.Printf("    TLS:         on (BYO cert: %s, key: %s)\n", sc.CertFile, sc.KeyFile)
				fmt.Printf("                   replace files + pg addon install silo --name %s --tls-cert ... --tls-key ... --force to renew\n", name)
			} else if sc.TLS {
				caPath := filepath.Join(sm.TLSDir(&sc), tlsca.CACertFile)
				fmt.Printf("    TLS:         on (CA: %s)\n", caPath)
				// sc.Listen is the bind address (often 0.0.0.0) — the hints use
				// a dialable example host instead.
				fmt.Printf("                   as pgBackRest repo CA: pg backup setup --s3-endpoint <host>:%d --s3-ca-file %s\n", sc.APIPort, caPath)
				fmt.Printf("                   from another host:     pg backup fetch-ca <this-host>:%d\n", sc.APIPort)
			}
			if len(sc.Drives) > 0 {
				driveMode := "SNMD"
				if len(sc.Endpoints) > 0 {
					driveMode = "MNMD, this node"
				}
				fmt.Printf("    Drives:      %d (%s)\n", len(sc.Drives), driveMode)
				for i, d := range sc.Drives {
					fmt.Printf("      - /data%d <- %s\n", i+1, d)
				}
			} else {
				fmt.Printf("    Data:        %s\n", sm.DataDir(&sc))
			}
			if len(sc.Endpoints) > 0 {
				endpointMode := "distributed"
				if len(sc.Drives) > 0 {
					endpointMode = fmt.Sprintf("MNMD, %d nodes × %d drives/node", len(sc.Endpoints)/len(sc.Drives), len(sc.Drives))
				}
				fmt.Printf("    Endpoints:   %d (%s)\n", len(sc.Endpoints), endpointMode)
				for _, ep := range sc.Endpoints {
					fmt.Printf("      - %s\n", ep)
				}
			}
			fmt.Printf("    Root user:   %s\n", sc.RootUser)
			fmt.Printf("    Image:       %s\n", sc.ImageTag)
			fmt.Printf("    Container:   %s\n", sc.ContainerName)
		}
	} else if len(cfg.Addons.Silo) > 0 {
		// Configured but the manager is unavailable (macOS/arm): still show them.
		for name, sc := range cfg.Addons.Silo {
			hasSilo = true
			fmt.Printf("  %s (name: %s)\n", "silo", name)
			fmt.Printf("    Status:      n/a (%v)\n", err)
			fmt.Printf("    Listen:      %s\n", sc.Listen)
			fmt.Printf("    API port:    %d\n", sc.APIPort)
			fmt.Printf("    Console port: %d\n", sc.ConsolePort)
			fmt.Printf("    Container:   %s\n", sc.ContainerName)
		}
	}
	if !hasSilo {
		fmt.Println("  (none)")
	}

	// rustfs (S3-compatible object storage — third store addon; Linux-only manager)
	fmt.Println()
	fmt.Println("Infra add-ons (rustfs):")
	hasRustfs := false
	if rm, err := podman.NewRustfsManager(cfg); err == nil {
		for name, rc := range cfg.Addons.Rustfs {
			hasRustfs = true
			status := "stopped"
			if running, err := rm.ContainerRunning(rc.ContainerName); err == nil && running {
				status = "running"
			}
			fmt.Printf("  %s (name: %s)\n", "rustfs", name)
			fmt.Printf("    Status:      %s\n", status)
			fmt.Printf("    Listen:      %s\n", rc.Listen)
			fmt.Printf("    API port:    %d\n", rc.APIPort)
			fmt.Printf("    Console port: %d\n", rc.ConsolePort)
			scheme := "http"
			if rc.TLS {
				scheme = "https"
			}
			fmt.Printf("    Console URL: %s://%s:%d/\n", scheme, rc.Listen, rc.ConsolePort)
			fmt.Printf("    Health:      %s://%s:%d/health\n", scheme, rc.Listen, rc.APIPort)
			if podman.BYOTLS(rc.TLS, rc.CertFile, rc.KeyFile) {
				fmt.Printf("    TLS:         on (BYO cert: %s, key: %s)\n", rc.CertFile, rc.KeyFile)
				fmt.Printf("                   replace files + pg addon install rustfs --name %s --tls-cert ... --tls-key ... --force to renew\n", name)
			} else if rc.TLS {
				caPath := filepath.Join(rm.TLSDir(&rc), tlsca.CACertFile)
				fmt.Printf("    TLS:         on (CA: %s)\n", caPath)
				fmt.Printf("                   as pgBackRest repo CA: pg backup setup --s3-endpoint <host>:%d --s3-ca-file %s\n", rc.APIPort, caPath)
				fmt.Printf("                   from another host:     pg backup fetch-ca <this-host>:%d\n", rc.APIPort)
			}
			if len(rc.Drives) > 0 {
				driveMode := "SNMD"
				if len(rc.Endpoints) > 0 {
					driveMode = "MNMD, this node"
				}
				fmt.Printf("    Drives:      %d (%s)\n", len(rc.Drives), driveMode)
				for i, d := range rc.Drives {
					fmt.Printf("      - /data/rustfs%d <- %s\n", i, d)
				}
			} else {
				fmt.Printf("    Data:        %s\n", rm.DataDir(&rc))
			}
			if len(rc.Endpoints) > 0 {
				endpointMode := fmt.Sprintf("MNMD, %d nodes × %d drives/node", len(rc.Endpoints)/len(rc.Drives), len(rc.Drives))
				fmt.Printf("    Endpoints:   %d (%s)\n", len(rc.Endpoints), endpointMode)
				for _, ep := range rc.Endpoints {
					fmt.Printf("      - %s\n", ep)
				}
			}
			fmt.Printf("    Root user:   %s\n", rc.RootUser)
			fmt.Printf("    Image:       %s\n", rc.ImageTag)
			fmt.Printf("    Container:   %s\n", rc.ContainerName)
		}
	} else if len(cfg.Addons.Rustfs) > 0 {
		// Configured but the manager is unavailable (macOS/arm): still show them.
		for name, rc := range cfg.Addons.Rustfs {
			hasRustfs = true
			fmt.Printf("  %s (name: %s)\n", "rustfs", name)
			fmt.Printf("    Status:      n/a (%v)\n", err)
			fmt.Printf("    Listen:      %s\n", rc.Listen)
			fmt.Printf("    API port:    %d\n", rc.APIPort)
			fmt.Printf("    Console port: %d\n", rc.ConsolePort)
			fmt.Printf("    Container:   %s\n", rc.ContainerName)
		}
	}
	if !hasRustfs {
		fmt.Println("  (none)")
	}

	// redis (standalone KV store — cache/session/ranking/counters)
	fmt.Println()
	fmt.Println("Infra add-ons (redis):")
	hasRedis := false
	if rm, err := podman.NewRedisManager(cfg); err == nil {
		for _, name := range sortedAddonNames(cfg.Addons.Redis) {
			rc := cfg.Addons.Redis[name]
			hasRedis = true
			status := "stopped"
			if running, err := rm.ContainerRunning(rc.ContainerName); err == nil && running {
				status = "running"
			}
			fmt.Printf("  %s (name: %s)\n", "redis", name)
			fmt.Printf("    Status:      %s\n", status)
			version := rc.Version
			if version == "" {
				version = config.RedisMajorForImageTag(rc.ImageTag)
			}
			if version != "" {
				fmt.Printf("    Version:     %s\n", version)
			}
			fmt.Printf("    Address:     %s:%d\n", rc.Listen, rc.Port)
			fmt.Printf("    Role:        %s\n", redisRoleSummary(rc))
			auth := "off"
			if rc.Password != "" {
				auth = "on (requirepass)"
			}
			fmt.Printf("    Auth:        %s\n", auth)
			if rc.MaxMemory != "" {
				fmt.Printf("    Maxmemory:   %s (%s)\n", rc.MaxMemory, redisEffectivePolicy(rc))
			}
			fmt.Printf("    Persistence: %s\n", redisPersistenceSummary(rc))
			fmt.Printf("    Data:        %s\n", rm.DataDir(&rc))
			fmt.Printf("    Image:       %s\n", rc.ImageTag)
			fmt.Printf("    Container:   %s\n", rc.ContainerName)
			fmt.Printf("    Client:      pg redis-cli --name %s ping\n", name)
		}
	} else if len(cfg.Addons.Redis) > 0 {
		// Configured but the manager is unavailable (podman missing): still show them.
		for _, name := range sortedAddonNames(cfg.Addons.Redis) {
			rc := cfg.Addons.Redis[name]
			hasRedis = true
			fmt.Printf("  %s (name: %s)\n", "redis", name)
			fmt.Printf("    Status:      n/a (%v)\n", err)
			fmt.Printf("    Address:     %s:%d\n", rc.Listen, rc.Port)
			fmt.Printf("    Container:   %s\n", rc.ContainerName)
		}
	}
	if !hasRedis {
		fmt.Println("  (none)")
	}

	return nil
}

// sortedAddonNames returns an addon map's keys in order, so `pg addon list` is
// stable across runs (Go map iteration is not).
func sortedAddonNames[T any](m map[string]T) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ---------------------------------------------------------------------------
// remove logic
// ---------------------------------------------------------------------------

func runAddonRemove(addonName string, cmd *cobra.Command) error {
	switch addonName {
	case "etcd":
		return runAddonRemoveEtcd(cmd)
	case "pgdog":
		return runAddonRemovePgDog(cmd)
	case "haproxy":
		return runAddonRemoveHAProxy(cmd)
	case "minio":
		return runAddonRemoveMinio(cmd)
	case "silo":
		return runAddonRemoveSilo(cmd)
	case "rustfs":
		return runAddonRemoveRustfs(cmd)
	case "redis":
		return runAddonRemoveRedis(cmd)
	case "postgrest":
		return runAddonRemovePostgrest(cmd)
	case "pgbouncer":
		// falls through to the PgBouncer flow below
	default:
		return fmt.Errorf("unknown addon: %s (available: pgbouncer, etcd, pgdog, haproxy, minio, silo, rustfs, redis, postgrest)", addonName)
	}

	pgName, _ := cmd.Flags().GetString("pg-name")

	// Determine mode: --pg-name → remote, otherwise → local (-i)
	isRemote := pgName != ""
	if !isRemote && cmd.Flags().Changed("instance") && cfgInstance != "default" {
		// -i explicitly set, use local mode
	} else if !isRemote {
		// Use the default -i value (could be "default" or user-set)
	}

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

	pbMgr, err := podman.NewPgBouncerManager(cfg)
	if err != nil {
		return fmt.Errorf("pgbouncer manager: %w", err)
	}

	if isRemote {
		// Remove remote PgBouncer
		if cfg.Addons.PgBouncer == nil {
			return fmt.Errorf("no remote PgBouncer add-ons configured")
		}
		pb, ok := cfg.Addons.PgBouncer[pgName]
		if !ok {
			return fmt.Errorf("remote PgBouncer %q not found", pgName)
		}

		fmt.Printf("-> Removing remote PgBouncer %q...\n", pgName)
		if err := pbMgr.Remove(&pb, pgName); err != nil {
			return err
		}

		delete(cfg.Addons.PgBouncer, pgName)
		if len(cfg.Addons.PgBouncer) == 0 {
			cfg.Addons.PgBouncer = nil
		}
		if err := cfg.Save(path); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		fmt.Printf("✓ Remote PgBouncer %q removed\n", pgName)
		return nil
	}

	// Remove local PgBouncer
	if _, ok := cfg.Instances[cfgInstance]; !ok {
		return fmt.Errorf("instance %q not found in config", cfgInstance)
	}

	inst := cfg.Instances[cfgInstance]
	if inst.Addons.PgBouncer == nil {
		fmt.Printf("PgBouncer is not installed for instance %q\n", cfgInstance)
		return nil
	}

	fmt.Printf("-> Removing PgBouncer from instance %q...\n", cfgInstance)
	if err := pbMgr.Remove(inst.Addons.PgBouncer, cfgInstance); err != nil {
		return err
	}

	inst.Addons.PgBouncer = nil
	cfg.Instances[cfgInstance] = inst
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("✓ PgBouncer removed from instance %q\n", cfgInstance)
	return nil
}

// runAddonRemovePostgrest removes a PostgREST container and its config entry,
// in either mode: --pg-name removes the top-level addons.postgrest.<name>
// entry, otherwise the local instance sidecar (instances.<name>.addons.postgrest).
// PostgREST is stateless, so there is nothing on the host to clean beyond the
// container itself.
func runAddonRemovePostgrest(cmd *cobra.Command) error {
	pgName, _ := cmd.Flags().GetString("pg-name")
	isRemote := pgName != ""

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

	pgm, err := podman.NewPostgrestManager(cfg)
	if err != nil {
		return fmt.Errorf("postgrest manager: %w", err)
	}

	if isRemote {
		if cfg.Addons.Postgrest == nil {
			return fmt.Errorf("no remote PostgREST add-ons configured")
		}
		pc, ok := cfg.Addons.Postgrest[pgName]
		if !ok {
			return fmt.Errorf("remote PostgREST %q not found", pgName)
		}
		fmt.Printf("-> Removing remote PostgREST %q...\n", pgName)
		if err := pgm.Remove(&pc); err != nil {
			return err
		}
		delete(cfg.Addons.Postgrest, pgName)
		if len(cfg.Addons.Postgrest) == 0 {
			cfg.Addons.Postgrest = nil
		}
		if err := cfg.Save(path); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		fmt.Printf("✓ Remote PostgREST %q removed\n", pgName)
		return nil
	}

	if _, ok := cfg.Instances[cfgInstance]; !ok {
		return fmt.Errorf("instance %q not found in config", cfgInstance)
	}
	inst := cfg.Instances[cfgInstance]
	if inst.Addons.Postgrest == nil {
		fmt.Printf("PostgREST is not installed for instance %q\n", cfgInstance)
		return nil
	}
	fmt.Printf("-> Removing PostgREST from instance %q...\n", cfgInstance)
	if err := pgm.Remove(inst.Addons.Postgrest); err != nil {
		return err
	}
	inst.Addons.Postgrest = nil
	cfg.Instances[cfgInstance] = inst
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ PostgREST removed from instance %q\n", cfgInstance)
	return nil
}

// ---------------------------------------------------------------------------
// remove logic — etcd
// ---------------------------------------------------------------------------

func runAddonRemoveEtcd(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "etcd"
	}

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

	if cfg.Addons.Etcd == nil {
		return fmt.Errorf("no etcd add-ons configured")
	}
	ec, ok := cfg.Addons.Etcd[name]
	if !ok {
		return fmt.Errorf("etcd %q not found", name)
	}

	em, err := podman.NewEtcdManager(cfg)
	if err != nil {
		return fmt.Errorf("etcd manager: %w", err)
	}

	// If other members of the same cluster are running, deregister this one
	// from the cluster first (via a running peer) so the quorum does not keep
	// a stale entry. Best-effort: skip when no peer is available (e.g. last
	// member, or cluster already down).
	for k, p := range cfg.Addons.Etcd {
		if k == name || p.ClusterName != ec.ClusterName {
			continue
		}
		if running, err := em.ContainerRunning(p.ContainerName); err == nil && running {
			fmt.Println("-> Deregistering member from the cluster...")
			if err := em.MemberRemove(p.ContainerName, p.ClientPort, ec.Name); err != nil {
				return err
			}
			break
		}
	}

	fmt.Printf("-> Removing etcd %q...\n", name)
	if err := em.Remove(&ec); err != nil {
		return err
	}

	delete(cfg.Addons.Etcd, name)
	if len(cfg.Addons.Etcd) == 0 {
		cfg.Addons.Etcd = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ etcd %q removed\n", name)
	return nil
}

// ---------------------------------------------------------------------------
// remove logic — pgdog
// ---------------------------------------------------------------------------

func runAddonRemovePgDog(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "pgdog"
	}

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

	if cfg.Addons.PgDog == nil {
		return fmt.Errorf("no pgdog add-ons configured")
	}
	pd, ok := cfg.Addons.PgDog[name]
	if !ok {
		return fmt.Errorf("pgdog %q not found", name)
	}

	dm, err := podman.NewPgDogManager(cfg)
	if err != nil {
		return fmt.Errorf("pgdog manager: %w", err)
	}

	fmt.Printf("-> Removing pgdog %q...\n", name)
	if err := dm.Remove(&pd); err != nil {
		return err
	}

	delete(cfg.Addons.PgDog, name)
	if len(cfg.Addons.PgDog) == 0 {
		cfg.Addons.PgDog = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ pgdog %q removed\n", name)
	return nil
}

// ---------------------------------------------------------------------------
// start / stop logic
// ---------------------------------------------------------------------------

func runAddonStart(addonName string, cmd *cobra.Command) error {
	switch addonName {
	case "etcd":
		return runAddonStartEtcd(cmd)
	case "pgdog":
		return runAddonStartPgDog(cmd)
	case "haproxy":
		return runAddonStartHAProxy(cmd)
	case "minio":
		return runAddonStartMinio(cmd)
	case "silo":
		return runAddonStartSilo(cmd)
	case "rustfs":
		return runAddonStartRustfs(cmd)
	case "redis":
		return runAddonStartRedis(cmd)
	case "pgbouncer":
		return runAddonStartPgBouncer(cmd)
	case "postgrest":
		return runAddonStartPostgrest(cmd)
	default:
		return fmt.Errorf("unknown addon: %s (available: pgbouncer, etcd, pgdog, haproxy, minio, silo, rustfs, redis, postgrest)", addonName)
	}
}

func runAddonStop(addonName string, cmd *cobra.Command) error {
	switch addonName {
	case "etcd":
		return runAddonStopEtcd(cmd)
	case "pgdog":
		return runAddonStopPgDog(cmd)
	case "haproxy":
		return runAddonStopHAProxy(cmd)
	case "minio":
		return runAddonStopMinio(cmd)
	case "silo":
		return runAddonStopSilo(cmd)
	case "rustfs":
		return runAddonStopRustfs(cmd)
	case "redis":
		return runAddonStopRedis(cmd)
	case "pgbouncer":
		return runAddonStopPgBouncer(cmd)
	case "postgrest":
		return runAddonStopPostgrest(cmd)
	default:
		return fmt.Errorf("unknown addon: %s (available: pgbouncer, etcd, pgdog, haproxy, minio, silo, rustfs, redis, postgrest)", addonName)
	}
}

// loadAddonCfg reads pg.yaml (or the -c override) into a fresh Config.
func loadAddonCfg() (*config.Config, string, error) {
	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, "", fmt.Errorf("config file not found: %s", path)
	}
	c, err := config.Load(path)
	if err != nil {
		return nil, "", fmt.Errorf("failed to load config: %w", err)
	}
	return c, path, nil
}

// ensureProxyBridge brings up the podman machine and pgcli-net bridge on macOS
// (both no-op on Linux) before a proxy container joins the bridge network.
func ensureProxyBridge(cfg *config.Config) error {
	pm, err := podman.New(cfg)
	if err != nil {
		return fmt.Errorf("podman: %w", err)
	}
	if err := pm.EnsureMachine(); err != nil {
		return err
	}
	return pm.EnsureNetwork()
}

func runAddonStartEtcd(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "etcd"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.Etcd == nil {
		return fmt.Errorf("no etcd add-ons configured (run 'pg addon install etcd')")
	}
	ec, ok := cfg.Addons.Etcd[name]
	if !ok {
		return fmt.Errorf("etcd %q not found (run 'pg addon install etcd --name %s')", name, name)
	}
	em, err := podman.NewEtcdManager(cfg)
	if err != nil {
		return fmt.Errorf("etcd manager: %w", err)
	}
	fmt.Printf("-> Starting etcd %q...\n", name)
	return em.StartContainer(&ec)
}

func runAddonStopEtcd(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "etcd"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	ec, ok := cfg.Addons.Etcd[name]
	if !ok {
		return fmt.Errorf("etcd %q not found", name)
	}
	em, err := podman.NewEtcdManager(cfg)
	if err != nil {
		return fmt.Errorf("etcd manager: %w", err)
	}
	running, _ := em.ContainerRunning(ec.ContainerName)
	if !running {
		fmt.Printf("etcd %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping etcd %q...\n", name)
	if _, err := em.Stop(ec.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ etcd %q stopped\n", name)
	return nil
}

func runAddonStartPgDog(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "pgdog"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.PgDog == nil {
		return fmt.Errorf("no pgdog add-ons configured (run 'pg addon install pgdog')")
	}
	pd, ok := cfg.Addons.PgDog[name]
	if !ok {
		return fmt.Errorf("pgdog %q not found (run 'pg addon install pgdog --name %s')", name, name)
	}
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}
	dm, err := podman.NewPgDogManager(cfg)
	if err != nil {
		return fmt.Errorf("pgdog manager: %w", err)
	}
	fmt.Printf("-> Starting pgdog %q...\n", name)
	return dm.StartContainer(&pd)
}

func runAddonStopPgDog(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "pgdog"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	pd, ok := cfg.Addons.PgDog[name]
	if !ok {
		return fmt.Errorf("pgdog %q not found", name)
	}
	dm, err := podman.NewPgDogManager(cfg)
	if err != nil {
		return fmt.Errorf("pgdog manager: %w", err)
	}
	running, _ := dm.ContainerRunning(pd.ContainerName)
	if !running {
		fmt.Printf("pgdog %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping pgdog %q...\n", name)
	if _, err := dm.Stop(pd.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ pgdog %q stopped\n", name)
	return nil
}

// ---------------------------------------------------------------------------
// remove / start / stop logic — haproxy
// ---------------------------------------------------------------------------

func runAddonRemoveHAProxy(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "haproxy"
	}

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

	if cfg.Addons.HAProxy == nil {
		return fmt.Errorf("no haproxy add-ons configured")
	}
	hc, ok := cfg.Addons.HAProxy[name]
	if !ok {
		return fmt.Errorf("haproxy %q not found", name)
	}

	hm, err := podman.NewHAProxyManager(cfg)
	if err != nil {
		return fmt.Errorf("haproxy manager: %w", err)
	}

	fmt.Printf("-> Removing haproxy %q...\n", name)
	if err := hm.Remove(&hc); err != nil {
		return err
	}

	delete(cfg.Addons.HAProxy, name)
	if len(cfg.Addons.HAProxy) == 0 {
		cfg.Addons.HAProxy = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ haproxy %q removed\n", name)
	return nil
}

func runAddonStartHAProxy(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "haproxy"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.HAProxy == nil {
		return fmt.Errorf("no haproxy add-ons configured (run 'pg addon install haproxy')")
	}
	hc, ok := cfg.Addons.HAProxy[name]
	if !ok {
		return fmt.Errorf("haproxy %q not found (run 'pg addon install haproxy --name %s')", name, name)
	}
	hm, err := podman.NewHAProxyManager(cfg)
	if err != nil {
		return fmt.Errorf("haproxy manager: %w", err)
	}
	fmt.Printf("-> Starting haproxy %q...\n", name)
	return hm.StartContainer(&hc)
}

func runAddonStopHAProxy(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "haproxy"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	hc, ok := cfg.Addons.HAProxy[name]
	if !ok {
		return fmt.Errorf("haproxy %q not found", name)
	}
	hm, err := podman.NewHAProxyManager(cfg)
	if err != nil {
		return fmt.Errorf("haproxy manager: %w", err)
	}
	running, _ := hm.ContainerRunning(hc.ContainerName)
	if !running {
		fmt.Printf("haproxy %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping haproxy %q...\n", name)
	if _, err := hm.Stop(hc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ haproxy %q stopped\n", name)
	return nil
}

// ---------------------------------------------------------------------------
// remove / start / stop logic — minio
// ---------------------------------------------------------------------------

func runAddonRemoveMinio(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "minio"
	}
	cleanData, _ := cmd.Flags().GetBool("clean-data")

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

	if cfg.Addons.Minio == nil {
		return fmt.Errorf("no minio add-ons configured")
	}
	mc, ok := cfg.Addons.Minio[name]
	if !ok {
		return fmt.Errorf("minio %q not found", name)
	}

	mm, err := podman.NewMinioManager(cfg)
	if err != nil {
		return fmt.Errorf("minio manager: %w", err)
	}

	fmt.Printf("-> Removing minio %q...\n", name)
	if err := mm.Remove(&mc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.Minio, name)
	if len(cfg.Addons.Minio) == 0 {
		cfg.Addons.Minio = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ minio %q removed\n", name)
	return nil
}

func runAddonRemoveSilo(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "silo"
	}
	cleanData, _ := cmd.Flags().GetBool("clean-data")

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

	if cfg.Addons.Silo == nil {
		return fmt.Errorf("no silo add-ons configured")
	}
	sc, ok := cfg.Addons.Silo[name]
	if !ok {
		return fmt.Errorf("silo %q not found", name)
	}

	sm, err := podman.NewSiloManager(cfg)
	if err != nil {
		return fmt.Errorf("silo manager: %w", err)
	}

	fmt.Printf("-> Removing silo %q...\n", name)
	if err := sm.Remove(&sc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.Silo, name)
	if len(cfg.Addons.Silo) == 0 {
		cfg.Addons.Silo = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ silo %q removed\n", name)
	return nil
}

func runAddonStartMinio(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "minio"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.Minio == nil {
		return fmt.Errorf("no minio add-ons configured (run 'pg addon install minio')")
	}
	mc, ok := cfg.Addons.Minio[name]
	if !ok {
		return fmt.Errorf("minio %q not found (run 'pg addon install minio --name %s')", name, name)
	}
	mm, err := podman.NewMinioManager(cfg)
	if err != nil {
		return fmt.Errorf("minio manager: %w", err)
	}
	fmt.Printf("-> Starting minio %q...\n", name)
	return mm.StartContainer(&mc)
}

func runAddonStartSilo(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "silo"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.Silo == nil {
		return fmt.Errorf("no silo add-ons configured (run 'pg addon install silo')")
	}
	sc, ok := cfg.Addons.Silo[name]
	if !ok {
		return fmt.Errorf("silo %q not found (run 'pg addon install silo --name %s')", name, name)
	}
	sm, err := podman.NewSiloManager(cfg)
	if err != nil {
		return fmt.Errorf("silo manager: %w", err)
	}
	fmt.Printf("-> Starting silo %q...\n", name)
	return sm.StartContainer(&sc)
}

func runAddonStopMinio(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "minio"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	mc, ok := cfg.Addons.Minio[name]
	if !ok {
		return fmt.Errorf("minio %q not found", name)
	}
	mm, err := podman.NewMinioManager(cfg)
	if err != nil {
		return fmt.Errorf("minio manager: %w", err)
	}
	running, _ := mm.ContainerRunning(mc.ContainerName)
	if !running {
		fmt.Printf("minio %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping minio %q...\n", name)
	if _, err := mm.Stop(mc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ minio %q stopped\n", name)
	return nil
}

func runAddonStopSilo(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "silo"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	sc, ok := cfg.Addons.Silo[name]
	if !ok {
		return fmt.Errorf("silo %q not found", name)
	}
	sm, err := podman.NewSiloManager(cfg)
	if err != nil {
		return fmt.Errorf("silo manager: %w", err)
	}
	running, _ := sm.ContainerRunning(sc.ContainerName)
	if !running {
		fmt.Printf("silo %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping silo %q...\n", name)
	if _, err := sm.Stop(sc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ silo %q stopped\n", name)
	return nil
}

func runAddonRemoveRustfs(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "rustfs"
	}
	cleanData, _ := cmd.Flags().GetBool("clean-data")

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

	if cfg.Addons.Rustfs == nil {
		return fmt.Errorf("no rustfs add-ons configured")
	}
	rc, ok := cfg.Addons.Rustfs[name]
	if !ok {
		return fmt.Errorf("rustfs %q not found", name)
	}

	rm, err := podman.NewRustfsManager(cfg)
	if err != nil {
		return fmt.Errorf("rustfs manager: %w", err)
	}

	fmt.Printf("-> Removing rustfs %q...\n", name)
	if err := rm.Remove(&rc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.Rustfs, name)
	if len(cfg.Addons.Rustfs) == 0 {
		cfg.Addons.Rustfs = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ rustfs %q removed\n", name)
	return nil
}

func runAddonStartRustfs(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "rustfs"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.Rustfs == nil {
		return fmt.Errorf("no rustfs add-ons configured (run 'pg addon install rustfs')")
	}
	rc, ok := cfg.Addons.Rustfs[name]
	if !ok {
		return fmt.Errorf("rustfs %q not found (run 'pg addon install rustfs --name %s')", name, name)
	}
	rm, err := podman.NewRustfsManager(cfg)
	if err != nil {
		return fmt.Errorf("rustfs manager: %w", err)
	}
	fmt.Printf("-> Starting rustfs %q...\n", name)
	return rm.StartContainer(&rc)
}

func runAddonStopRustfs(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "rustfs"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	rc, ok := cfg.Addons.Rustfs[name]
	if !ok {
		return fmt.Errorf("rustfs %q not found", name)
	}
	rm, err := podman.NewRustfsManager(cfg)
	if err != nil {
		return fmt.Errorf("rustfs manager: %w", err)
	}
	running, _ := rm.ContainerRunning(rc.ContainerName)
	if !running {
		fmt.Printf("rustfs %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping rustfs %q...\n", name)
	if _, err := rm.Stop(rc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ rustfs %q stopped\n", name)
	return nil
}

func runAddonRemoveRedis(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "redis"
	}
	cleanData, _ := cmd.Flags().GetBool("clean-data")

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

	if cfg.Addons.Redis == nil {
		return fmt.Errorf("no redis add-ons configured")
	}
	rc, ok := cfg.Addons.Redis[name]
	if !ok {
		return fmt.Errorf("redis %q not found", name)
	}

	rm, err := podman.NewRedisManager(cfg)
	if err != nil {
		return fmt.Errorf("redis manager: %w", err)
	}

	fmt.Printf("-> Removing redis %q...\n", name)
	if err := rm.Remove(&rc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.Redis, name)
	if len(cfg.Addons.Redis) == 0 {
		cfg.Addons.Redis = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ redis %q removed\n", name)
	if !cleanData {
		fmt.Printf("  (data dir kept — reinstall the same name revives the RDB; --clean-data to delete it)\n")
	}
	return nil
}

func runAddonStartRedis(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "redis"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.Redis == nil {
		return fmt.Errorf("no redis add-ons configured (run 'pg addon install redis')")
	}
	rc, ok := cfg.Addons.Redis[name]
	if !ok {
		return fmt.Errorf("redis %q not found (run 'pg addon install redis --name %s')", name, name)
	}
	rm, err := podman.NewRedisManager(cfg)
	if err != nil {
		return fmt.Errorf("redis manager: %w", err)
	}
	fmt.Printf("-> Starting redis %q...\n", name)
	return rm.StartContainer(&rc)
}

func runAddonStopRedis(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "redis"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	rc, ok := cfg.Addons.Redis[name]
	if !ok {
		return fmt.Errorf("redis %q not found", name)
	}
	rm, err := podman.NewRedisManager(cfg)
	if err != nil {
		return fmt.Errorf("redis manager: %w", err)
	}
	running, _ := rm.ContainerRunning(rc.ContainerName)
	if !running {
		fmt.Printf("redis %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping redis %q...\n", name)
	if _, err := rm.Stop(rc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ redis %q stopped\n", name)
	return nil
}

// resolvePgBouncerTarget maps (pg-name / cfgInstance) to (pbConf, instName).
// pg-name non-empty → remote (top-level addons.pgbouncer[pg-name]); otherwise
// local (instances.<-i>.addons.pgbouncer).
func resolvePgBouncerTarget(cfg *config.Config, cmd *cobra.Command) (*config.PgBouncerConfig, string, error) {
	pgName, _ := cmd.Flags().GetString("pg-name")
	if pgName != "" {
		pb, ok := cfg.Addons.PgBouncer[pgName]
		if !ok {
			return nil, "", fmt.Errorf("remote PgBouncer %q not found (run 'pg addon install pgbouncer --pg-name %s --dsn <dsn>')", pgName, pgName)
		}
		return &pb, pgName, nil
	}
	inst, ok := cfg.Instances[cfgInstance]
	if !ok {
		return nil, "", fmt.Errorf("instance %q not found in config", cfgInstance)
	}
	if inst.Addons.PgBouncer == nil {
		return nil, "", fmt.Errorf("PgBouncer is not installed for instance %q (run 'pg addon install pgbouncer -i %s')", cfgInstance, cfgInstance)
	}
	return inst.Addons.PgBouncer, cfgInstance, nil
}

func runAddonStartPgBouncer(cmd *cobra.Command) error {
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	pbConf, instName, err := resolvePgBouncerTarget(cfg, cmd)
	if err != nil {
		return err
	}
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}
	pbm, err := podman.NewPgBouncerManager(cfg)
	if err != nil {
		return fmt.Errorf("pgbouncer manager: %w", err)
	}
	fmt.Printf("-> Starting PgBouncer for %q...\n", instName)
	return pbm.StartContainer(pbConf, instName)
}

func runAddonStopPgBouncer(cmd *cobra.Command) error {
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	pbConf, instName, err := resolvePgBouncerTarget(cfg, cmd)
	if err != nil {
		return err
	}
	pbm, err := podman.NewPgBouncerManager(cfg)
	if err != nil {
		return fmt.Errorf("pgbouncer manager: %w", err)
	}
	running, _ := pbm.ContainerRunning(pbConf.ContainerName)
	if !running {
		fmt.Printf("PgBouncer for %q is not running\n", instName)
		return nil
	}
	fmt.Printf("-> Stopping PgBouncer for %q...\n", instName)
	if _, err := pbm.Stop(pbConf.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ PgBouncer for %q stopped\n", instName)
	return nil
}

// resolvePostgrestTarget maps (pg-name / cfgInstance) to
// (pcConf, name). pg-name non-empty → remote (top-level
// addons.postgrest[pg-name]); otherwise local (instances.<-i>.addons.postgrest).
func resolvePostgrestTarget(cfg *config.Config, cmd *cobra.Command) (*config.PostgrestConfig, string, error) {
	pgName, _ := cmd.Flags().GetString("pg-name")
	if pgName != "" {
		pc, ok := cfg.Addons.Postgrest[pgName]
		if !ok {
			return nil, "", fmt.Errorf("remote PostgREST %q not found (run 'pg addon install postgrest --pg-name %s --dsn <dsn>')", pgName, pgName)
		}
		return &pc, pgName, nil
	}
	inst, ok := cfg.Instances[cfgInstance]
	if !ok {
		return nil, "", fmt.Errorf("instance %q not found in config", cfgInstance)
	}
	if inst.Addons.Postgrest == nil {
		return nil, "", fmt.Errorf("PostgREST is not installed for instance %q (run 'pg addon install postgrest -i %s')", cfgInstance, cfgInstance)
	}
	return inst.Addons.Postgrest, cfgInstance, nil
}

func runAddonStartPostgrest(cmd *cobra.Command) error {
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	pc, name, err := resolvePostgrestTarget(cfg, cmd)
	if err != nil {
		return err
	}
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}
	pgm, err := podman.NewPostgrestManager(cfg)
	if err != nil {
		return fmt.Errorf("postgrest manager: %w", err)
	}
	fmt.Printf("-> Starting PostgREST for %q...\n", name)
	return pgm.StartContainer(pc)
}

func runAddonStopPostgrest(cmd *cobra.Command) error {
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	pc, name, err := resolvePostgrestTarget(cfg, cmd)
	if err != nil {
		return err
	}
	pgm, err := podman.NewPostgrestManager(cfg)
	if err != nil {
		return fmt.Errorf("postgrest manager: %w", err)
	}
	running, _ := pgm.ContainerRunning(pc.ContainerName)
	if !running {
		fmt.Printf("PostgREST for %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping PostgREST for %q...\n", name)
	if _, err := pgm.Stop(pc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ PostgREST for %q stopped\n", name)
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// nsSuffixCLI returns "-<namespace>" or "" — a CLI-level helper mirroring
// config.nsSuffix which is unexported.
func nsSuffixCLI(namespace string) string {
	if namespace == "" {
		return ""
	}
	return "-" + namespace
}
