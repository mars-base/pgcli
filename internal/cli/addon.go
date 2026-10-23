package cli

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/hostbin"
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
  pg addon install garage
          Stored under top-level addons.garage in config (Linux and macOS).
          Garage (garagehq.deuxfleurs.fr) — a Rust, AGPLv3 S3-compatible object
          store, run from the upstream docker.io/dxflrs/garage image (no wrapper;
          it runs as root in the container). pgcli manages it single-node only
          (replication_factor=1). It configures itself from a rendered
          garage.toml (not env), needs a metadata dir plus its data dir(s) — one
          per disk via --drive, or a single data dir — opens four ports from
          their own pool (API/RPC/Web/Admin, default base 3900), and serves
          plaintext HTTP — no native TLS, so front it with the nginx addon for an
          HTTPS S3 endpoint that pgBackRest will accept.
  pg addon install seaweedfs
          Stored under top-level addons.seaweedfs in config (Linux and macOS).
          SeaweedFS (seaweedfs.io) — a Go, Apache-2.0 S3-compatible object store
          that manages disks at volume granularity (O(1) small-file reads), run
          from the upstream docker.io/chrislusf/seaweedfs image (no wrapper; it
          runs as root). Single-node runs "weed mini" (master+volume+filer+S3 in
          one process); --cluster switches to "weed server -filer -s3" with one
          member per host — MNMD like MinIO: run the SAME install on every host
          (only --advertise-host differs) and pass the FULL --peer Raft list on
          each. Cluster members share filer metadata in a PostgreSQL database
          (--dsn postgres://..., reachable from every host; the operator
          pre-creates the database — pgcli only probes it) and MUST present one
          shared S3 credential (--root-user/--root-password identical on every
          node; auto-generation is refused under --cluster). Four ports from their own pool (master/volume/filer/
          s3, default base 8400; the gRPC twins at +10000 are reserved too).
          Serves HTTPS natively with --tls / --tls-cert+--tls-key (the S3 port
          becomes TLS-only).
  pg addon install redis
          Stored under top-level addons.redis in config (Linux and macOS).
          A standalone Redis (KV store for cache, session, ranking and
          atomic-counter data) run from the plain upstream docker.io/library/redis
          image — no wrapper, no pgcli build. --version 7|8 picks the major
          (each maps to a pinned patch tag); a requirepass password is generated
          on first install and persisted, so the default 0.0.0.0 bind is always
          authenticated. Data is an RDB snapshot under the addon's data dir, kept
          across remove/reinstall. Read replicas via --replica-of (no
          automatic failover); native cluster members via --cluster — install
          every member, then assemble once with "pg redis-cli -- --cluster
          create ..." (pgcli installs cluster-enabled nodes; it does not run
          the assembly). --cluster-replicas N sets followers per master for that
          create (0 = masters-only). Sentinel is out of scope.
  pg addon install postgrest
          Exposes a PostgreSQL schema as a REST API (single stateless
          container, dual mode like pgbouncer): local -i fronting an
          instance, or remote --dsn fronting any PG endpoint (direct
          instance, PgBouncer, or a Patroni cluster behind its HAProxy
          listener). Stored under instances.<name>.addons.postgrest (local)
          or addons.postgrest.<name> (remote).
  pg addon install pgadmin
          Stored under top-level addons.pgadmin in config (Linux and macOS).
          pgAdmin 4 — the official web administration UI for PostgreSQL —
          run from the upstream docker.io/dpage/pgadmin4 image. It is NOT a
          sidecar of any one instance (which servers it fronts is the UI's
          own concern), so unlike postgrest/pgbouncer it is top-level only;
          --dsn/--pg-name just pre-seed its server list once. A web login
          email/password (PGADMIN_DEFAULT_*) is required at first launch;
          the password is generated and printed once — retrieve it with
          "pg addon password pgadmin". Its data dir holds pgAdmin's own
          config/session DB and survives remove unless --clean-data. The
          container's entrypoint chowns the mounted dir to its uid 5050 and
          drops privileges itself, so pgcli never chowns on the host.
  pg addon install grafana
          Stored under top-level addons.grafana in config (Linux and macOS).
          Grafana — the open-source observability dashboard — run from the
          official docker.io/grafana image (NOT grafana-oss: that repo is
          frozen since 12.4.0; grafana/grafana carries the OSS build). A web
          login user/password (GF_SECURITY_ADMIN_*) is required at first
          launch; the password is generated and printed once — retrieve it
          with "pg addon password grafana". Listen defaults to 0.0.0.0 (a
          dashboard is meant to be viewed from other machines; containers run
          on the host network), guarded by the generated password and
          self-signup being off. Its data dir holds Grafana's own sqlite meta
          DB (grafana.db) and survives remove unless --clean-data. The image
          runs as a fixed uid 472 and its entrypoint does NO ownership work,
          so pgcli pre-chowns the data dir on the host via "podman unshare
          chown" (rootless only; a rootful host gets a manual-chown hint).
  pg addon install prometheus
          Stored under top-level addons.prometheus in config (Linux and macOS).
          Prometheus — the metrics collection backend Grafana reads from — run
          from the official docker.io/prom/prometheus image. Like Grafana it is
          top-level only (it scrapes many endpoints, not one instance's). It is
          configured by a RENDERED prometheus.yml — a self-monitoring job plus
          one static_configs job per --scrape JOB, folding that JOB's targets into
          it — bind-mounted
          read-only and passed as --config.file, alongside --web.listen-address
          and --storage.tsdb.retention.time; or, with --conf-file, your own file
          copied into pgcli's managed path. There is NO web login/password:
          Prometheus ships no authentication, so the default 0.0.0.0 listen is
          guarded ONLY by the network boundary — pass --listen 127.0.0.1 to keep
          it loopback-only. Reload config without a recreate via "pg addon
          prometheus reload" (SIGHUP). Its TSDB data dir survives remove unless
          --clean-data. The image runs as a fixed uid 65534 (nobody) and creates
          no writable dir, so pgcli pre-chowns the data dir on the host (same
          path as Grafana).
  pg addon install alertmanager
          Stored under top-level addons.alertmanager in config (Linux and macOS).
          Alertmanager — the alert dedup/grouping/routing frontend of the
          Prometheus stack — run from the official docker.io/prom/alertmanager
          image. Top-level only (it fronts a whole alerting pipeline, not one
          instance's). It is configured by a RENDERED alertmanager.yml — one
          minimal route pointing at a single receiver: "web.hook" carrying every
          --webhook-url as one webhook_configs entry (fan-out), or a name-only
          "no-op" receiver when none are given — bind-mounted read-only and
          passed as --config.file, alongside --web.listen-address, --storage.path
          and --cluster.listen-address= (empty, disabling the 9094 HA gossip);
          or, with --conf-file, your own file copied into pgcli's managed path
          (mutually exclusive with --webhook-url). The Alertmanager side has no
          Prometheus concept of its own — wiring happens on the Prometheus side:
          pg addon install prometheus --alertmanager-url <this instance> injects
          the alerting section (and watches this instance's /metrics).
          There is NO web
          login/password: Alertmanager ships no authentication, and its API lets
          anyone who reaches it CREATE SILENCES (mute on-call) — the default
          0.0.0.0 listen is guarded ONLY by the network boundary; pass
          --listen 127.0.0.1 to keep it loopback-only. Reload without a recreate
          via "pg addon alertmanager reload" (SIGHUP; the HTTP /-/reload endpoint
          exists and cannot be disabled, but pgcli never uses it). Its
          silence/notification state dir survives remove unless --clean-data. The
          image runs as a fixed uid 65534 (nobody) and creates no writable dir,
          so pgcli pre-chowns the data dir on the host (same path as Grafana).
  pg addon install gitea
          Stored under top-level addons.gitea in config (Linux and macOS).
          Gitea — the self-hosted Git service — run from the official
          docker.io/gitea/gitea image (the version line moved from 1.2x to 28.x
          in late 2026). A web login user/password is required at first launch;
          the password is generated and printed once — retrieve it with
          "pg addon password gitea". Unlike Grafana, the account is not seeded
          from env: pgcli skips the setup wizard (INSTALL_LOCK=true), waits for
          first-boot migrations, then runs "gitea admin user create" inside the
          container. Listen defaults to 0.0.0.0 (a Git host is pushed to from
          other machines), guarded by the generated password and closed
          self-registration. Optional --ssh-port exposes git-over-ssh (the
          image's OpenSSH) clone/push on its own port pool. Metadata is the embedded SQLite in
          the data dir by default, or an EXTERNAL PostgreSQL via --dsn (must
          already exist; same contract as PostgREST). Crypto secrets
          (SECRET_KEY/INTERNAL_TOKEN) are never injected — the volume owns them
          (re-injecting a per-install secret would silently re-key a surviving
          DB). Its data dir holds repos, app.ini and the SQLite DB and survives
          remove unless --clean-data. The rootful image su-execs the git user
          (uid/gid 1000) but does not chown an existing bind mount, so pgcli
          pre-chowns the data dir on the host (rootless via "podman unshare
          chown 1000:1000"; a rootful host running pgcli as root chowns directly).
  pg addon install beegfs
          Stored under top-level addons.beegfs in config (Linux only).
          BeeGFS — the scale-out parallel filesystem — pgcli's first
          MULTI-container addon: one filesystem = the mgmtd → meta → storage
          services from ghcr.io/thinkparq/beegfs, started in dependency order
          by one install. By default THIS host runs all three (the converged
          single-host filesystem); --roles mgmtd,meta / --roles storage scopes
          it to a subset, and a subset without mgmtd points at the cluster with
          --mgmtd-host <ip> + --node-id <n> + --password <conn-auth> — every
          host runs its own pgcli install and the daemons self-register into
          mgmtd (pgcli never SSHes). --storage-target repeats for multiple
          storage targets/disks on one host (target IDs derive from --node-id).
          Stock protocol ports (mgmtd 8008/8010, meta 8005, storage 8003);
          meta and storage have no listen-port override in 8.4, so exactly one
          filesystem can exist per host (enforced). Only mgmtd's two ports move,
          via --beegfs-mgmtd-port/--beegfs-mgmtd-grpc-port. Host networking, no
          privileged mode; the images run as root and own their data dirs. The
          daemons share a connection-auth secret ("pg addon password beegfs";
          generated on the host that runs mgmtd); mgmtd runs with TLS disabled
          (clients need --tls-disable too). The CLIENT is a kernel module —
          pgcli never installs or mounts it; "pg addon beegfs mount-hint"
          prints the exact client config, "pg addon beegfs join-hint" the
          per-host join commands, and "pg addon beegfs check" reports cluster
          health from the daemons' logs. An unlicensed build logs a quota
          warning and caps client mounts at five; a license is a registration
          you obtain from ThinkParQ (never generated here), and pgcli CARRIES a
          downloaded PEM ("--license-file <pem>" at install, or "pg addon beegfs
          license --set <pem>" / "--clear" to hot-swap; the PEM is stored in the
          config and reaches the mgmtd container only — mgmtd alone verifies it).
  pg addon install juicefs
          Stored under top-level addons.juicefs in config (Linux only).
          JuiceFS — an S3-backed POSIX filesystem, pgcli's first HOST-BINARY
          addon: the juicefs binary is downloaded from GitHub releases (sha256
          pinned, --install-dir picks where; a binary already on PATH is
          reused) and run directly on the host — no container. The mount is a
          plain host directory (default <base_dir>/addon/juicefs/<name>/mnt),
          visible to host processes; it does NOT propagate into PG instance
          containers. Both sides of a volume are given EXPLICITLY — there is no
          same-host auto-detection, because the S3 provider and the client
          normally live on different machines: --s3-endpoint http(s)://host[:port]
          plus --access-key/--secret-key name the external object store (the
          bucket itself is never created — pgcli prints the exact "pg mc mb ..."
          line to run yourself), and --dsn (alias --meta-dsn) is the metadata
          engine URL itself (postgres://, redis:// or sqlite3://; create the
          database beforehand — pgcli never creates or drops it). format runs
          once — an existing volume in the metadata engine is detected and left
          alone (even under --force, which only rebuilds the config entry from
          the flags); mount -d self-daemonizes with its log under the
          addon dir. "pg addon juicefs mount|umount|status [--name]" manage
          the mount; "pg addon remove juicefs" unmounts and forgets but KEEPS
          the bucket, metadata and binary. The S3 secret key is retrievable
          with "pg addon password juicefs".
  pg addon install zerofs
          Stored under top-level addons.zerofs in config (Linux only).
          ZeroFS — an encrypted, self-maintaining S3 filesystem (LSM on S3,
          no external metadata engine), also host-binary (GitHub releases,
          sha256-pinned). pgcli writes the zerofs config.toml itself and runs
          TWO host processes: the 9P server (loopback-only by construction —
          9P has no authentication) and the FUSE client that mounts it at
          --mount-point. Encryption password is generated ("pg addon password
          zerofs"); it WRAPS the bucket's data key, so losing it means losing
          the data — and after "pg addon remove" (which keeps the bucket),
          re-attaching needs the ORIGINAL password via --encryption-password.
          Fencing needs conditional put (If-None-Match): --conditional-put
          auto (default) assumes it (MinIO 2022-08+ and real S3 have it);
          for stores lacking it (e.g. silo) use --conditional-put redis with an
          explicit --redis-dsn redis://…, or none for a single writer only.
          Same EXPLICIT external-S3 contract (--s3-endpoint + credentials,
          optional --s3-region) and manual "pg mc" bucket provisioning as
          juicefs. "pg addon zerofs start|stop|mount|umount|status [--name]"
          manage it; "pg logs addon zerofs --service server|mount" tail either
          log.
  pg addon install duckdb
          Stored under top-level addons.duckdb in config (Linux only, amd64).
          DuckDB — an embedded OLAP database ("SQLite for analytics"), C++
          columnar engine that queries Parquet/CSV/JSON directly. Host-binary
          addon: the CLI zip is downloaded from GitHub releases (sha256 pinned,
          --install-dir picks where; a binary already on PATH is reused). No
          container, no daemon — DuckDB is a standalone CLI tool. Common
          extensions (httpfs, parquet, json) are installed automatically unless
          --no-extensions. --version picks the release (default 1.5.6 LTS;
          2.0.0 also supported). --data-dir sets the default database directory
          (default <base_dir>/addon/duckdb/<name>). "pg addon duckdb status"
          shows the binary version and path; "pg addon duckdb shell" launches
          an interactive DuckDB session.

Commands:
  pg addon install <addon>    install an add-on
  pg addon list               list all installed add-ons (--show-password to reveal credentials)
  pg addon password <addon>   print one add-on's stored password
  pg addon start <addon>      start an installed but stopped add-on
  pg addon stop <addon>       stop a running add-on (keeps config)
  pg addon remove <addon>     remove an add-on`,
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
  haproxy     TCP load balancer in front of a Patroni cluster (unified or read/write split), or a custom haproxy.cfg via --conf-file for any native proxying
  minio       single-node S3-compatible object storage (web console included; Linux host network, macOS bridge)
  silo        MinIO's Pigsty fork — same S3 object storage, from the public docker.io/pgsty/silo image (console + mcli client bundled; Linux host network, macOS bridge)
  rustfs      Rust S3-compatible object storage from pgcli's wrapper image, ghcr.io/mars-base/pgcli/pgcli-rustfs (console included; Linux only; the fixed upstream uid 10001 is handled inside the container, three topologies SNSD/SNMD/MNMD, each drive on its own physical device)
  garage      Garage (garagehq.deuxfleurs.fr) — Rust S3-compatible object store from the upstream docker.io/dxflrs/garage image, single-node or multi-node cross-host cluster (--cluster --peer); plaintext HTTP with no native TLS (front with the nginx addon for HTTPS/pgBackRest), four ports from garage_start_port (default 3900), one seeded access key + one default bucket
  seaweedfs   SeaweedFS (seaweedfs.io) — Go Apache-2.0 S3-compatible object store from the upstream docker.io/chrislusf/seaweedfs image, volume-granularity disk management; single-node "weed mini" or cross-host "weed server" cluster (--cluster, MNMD: same command on every host, full --peer Raft list, shared filer metadata in a --dsn PostgreSQL, and one shared --root-user/--root-password across all nodes); native S3 TLS via --tls or --tls-cert/--tls-key, four ports from seaweedfs_start_port (default 8400)
  redis       standalone KV store (cache/session/ranking/counters) from the plain upstream docker.io/library/redis image; --version 7|8 selects the major, requirepass auto-generated, RDB persistence, read replicas via --replica-of (no automatic failover), native cluster masters via --cluster (assemble once with "pg redis-cli -- --cluster create")
  predixy     Redis protocol proxy fronting a native redis cluster as one plain redis:// endpoint (clients need no -c / MOVED handling; --backend lists the full node set, --password reuses the cluster's requirepass; Linux only)
  postgrest   stateless REST API in front of a PostgreSQL schema (single container; Linux host network, macOS bridge)
  pgadmin     pgAdmin 4 — the official web administration UI (single container from docker.io/dpage/pgadmin4; web login auto-generated; Linux host network, macOS bridge)
  grafana     Grafana — the open-source observability dashboard (single container from docker.io/grafana; web login auto-generated; defaults to 0.0.0.0 since a dashboard is meant to be viewed remotely; Linux host network, macOS bridge)
  prometheus  Prometheus — the metrics collection backend Grafana reads from (single container from docker.io/prom/prometheus; rendered prometheus.yml or --conf-file, --scrape targets, --alertmanager-url to forward alerts + watch Alertmanager, --rule-file to load alerting rules, SIGHUP reload; NO web login — defaults to 0.0.0.0 guarded only by the network, use --listen 127.0.0.1 for loopback; Linux host network, macOS bridge)
  victoriametrics VictoriaMetrics — fast, Prometheus-compatible time-series database (single container from docker.io/victoriametrics/victoria-metrics; pure CLI flags, no config file; --retention-months for data retention, --extra-arg for custom flags like -search.maxUniqueTimeseries; NO web login — defaults to 0.0.0.0 guarded only by the network, use --listen 127.0.0.1 for loopback; Linux host network, macOS bridge)
  vmcluster   VMCluster — VictoriaMetrics cluster edition: 3 components (vminsert + vmselect + vmstorage) from docker.io/victoriametrics/{vminsert,vmselect,vmstorage}:v1.154.1-cluster; --roles selects which components THIS host runs (default: all three = converged single-host cluster); --storage-node lists ALL vmstorage addresses (repeatable, enables multi-node sharding + replication); pure CLI flags, no config file; 5 ports from vmcluster_start_port (default 8480-8484); NO auth — defaults to 0.0.0.0, use --listen 127.0.0.1 for loopback; Linux host network only
  alertmanager Alertmanager — the alert dedup/grouping/routing frontend of the Prometheus stack (single container from docker.io/prom/alertmanager; rendered alertmanager.yml with a no-op or --webhook-url web.hook receiver, or --conf-file; SIGHUP reload; cluster gossip disabled; NO web login — defaults to 0.0.0.0 guarded only by the network, and silences are dangerous, use --listen 127.0.0.1 for loopback; wired to Prometheus from the Prometheus side via --alertmanager-url; Linux host network, macOS bridge)
  gitea       Gitea — the self-hosted Git service (single container from docker.io/gitea/gitea; web login auto-generated and created via "gitea admin user create"; optional --ssh-port for clone/push over SSH; SQLite by default or --dsn to put its metadata in a PostgreSQL you already run; defaults to 0.0.0.0 since a Git host is reached from other machines; Linux host network, macOS bridge)
  beegfs      BeeGFS — the scale-out parallel filesystem (mgmtd/meta/storage services from ghcr.io/thinkparq/beegfs — brought up in dependency order by one install; by default all three on this host = a converged single-host filesystem, --roles scopes it to a subset and a --roles-without-mgmtd host joins an existing cluster via --mgmtd-host/--node-id/--password — every host runs its own pgcli, pgcli never SSHes; --storage-target repeats for multiple disks/targets; fixed protocol ports 8008/8010/8005/8003, meta/storage cannot move so one filesystem per host; shared conn-auth secret (generated on the mgmtd host), mgmtd TLS disabled; an unlicensed build caps client mounts at five — pgcli never generates a license but CARRIES a ThinkParQ PEM via --license-file / "pg addon beegfs license" (mgmtd-only); the kernel-module client is never installed — "pg addon beegfs mount-hint" prints its config, "join-hint" the per-host join commands; Linux host network only)
  juicefs     JuiceFS — S3-backed POSIX filesystem installed as a HOST BINARY (GitHub releases + sha256; no container): an EXTERNAL S3 backend given explicitly (--s3-endpoint http(s)://host:port + --access-key/--secret-key — no same-host addon auto-detection; buckets via "pg mc" by hand, pgcli prints the exact command) and an explicit metadata URL (--dsn: postgres://, redis:// or sqlite3://); mounts a plain host directory; "pg addon juicefs mount|umount|status"; Linux only
  zerofs      ZeroFS — encrypted self-maintaining S3 filesystem (LSM on S3, no external metadata) as a HOST BINARY (GitHub releases + sha256): an EXTERNAL S3 backend given explicitly (--s3-endpoint + --access-key/--secret-key, optional --s3-region; same no-auto-detection contract as juicefs); pgcli writes config.toml and runs a loopback 9P server + FUSE client; generated encryption password wraps the bucket's data key ("pg addon password zerofs" to recover, --encryption-password to re-attach a kept bucket); needs conditional-put support (MinIO 2022-08+ / real S3) else --conditional-put redis --redis-dsn; Linux only
  duckdb      DuckDB — embedded OLAP database as a HOST BINARY (GitHub releases + sha256 zip; no container, no daemon): C++ columnar engine querying Parquet/CSV/JSON directly; --version pins the release (default 1.5.6 LTS, 2.0.0 also supported); common extensions (httpfs, parquet, json) auto-installed; "pg addon duckdb status|shell"; Linux only, amd64
  nginx       HTTP reverse proxy in front of web services (path-based routing, optional TLS termination; single container from docker.io/library/nginx; Linux host network, macOS bridge)

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
  pg addon install haproxy [--name lb] --conf-file /path/haproxy.cfg
  pg addon install haproxy [--name lb] --conf-file /path/haproxy.cfg --reload
  Members are injected once via --node, or auto-derived from a Patroni scope via
  --ha. After adding or removing a member ("pg ha create" / "pg ha remove"),
  re-run install to re-sync the backend list.
  With --conf-file, pgcli instead runs your haproxy.cfg verbatim (mutually
  exclusive with --node/--ha) for any native HAProxy proxying — the file is
  validated with "haproxy -c" before the container starts, mounted read-only,
  and never rewritten; you own all listeners. Add --reload to an install of an
  already-running conf-file instance to apply an edited file by hot-reloading
  the master (SIGUSR2) instead of recreating the container, so no in-flight
  connections are dropped (same as "pg addon haproxy reload").

Infra addon (minio — single-node S3-compatible object storage, Linux and macOS;
            silo — its Pigsty fork, identical flags, same shared port pool;
            rustfs — a Rust S3 store, Linux only, same shared port pool;
            garage — a Rust S3 store (garagehq.deuxfleurs.fr), single-node, its
            OWN port pool (garage_start_port, four ports) and NO native TLS;
            seaweedfs — a Go S3 store (seaweedfs.io), single-node or MNMD
            cluster, its OWN port pool (seaweedfs_start_port, four ports) and
            NATIVE TLS):
  pg addon install minio [--name store] [--api-port N] [--console-port N]
                         [--listen 127.0.0.1] [--root-user admin] [--data-dir ...] [--force]
  pg addon install silo  [--name store] [--api-port N] [--console-port N]
                         [--listen 127.0.0.1] [--root-user admin] [--data-dir ...] [--force]
  pg addon install rustfs [--name store] [--api-port N] [--console-port N]
                         [--listen 127.0.0.1] [--root-user admin] [--data-dir ...] [--force]
  pg addon install garage [--name store] [--api-port N]
                         [--listen 127.0.0.1] [--data-dir ...] [--force]
  pg addon install seaweedfs [--name store] [--port N]
                         [--listen 127.0.0.1] [--root-user ...] [--data-dir ...] [--force]
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

  garage diverges from the other stores: a rendered garage.toml (not env), a
  metadata dir plus its data dir(s) (one per disk via --drive, or a single data
  dir), FOUR ports from its own garage_start_port pool (API/RPC/Web/Admin,
  default base 3900), and NO native TLS — it serves plaintext HTTP and
  pgBackRest refuses that, so front it with the nginx addon for HTTPS. Credentials are a seeded access key (auto GK-
  prefixed) + secret key, plus one default bucket named after the instance. Pass
  --tls to it and you get an error pointing at the nginx recipe.

  seaweedfs diverges too: it runs "weed mini" (all roles, one process) by default
  and "weed server -filer -s3" per host under --cluster — MNMD like MinIO, so run
  the SAME install command on every host (only --advertise-host differs; the
  shared credentials and --dsn are identical everywhere) and pass
  the FULL --peer <ip>:<master_port> Raft list on each; a node started before
  quorum waits, which the health gate reports as a warning, not a failure. Cluster
  members share filer metadata in one PostgreSQL database (--dsn postgres://...,
  reachable from every host — so a host-published/LAN address, and the operator
  pre-creates the database; pgcli only probes it). Credentials are ONE shared
  pair for the whole cluster: pass the same --root-user/--root-password on every
  node (auto-generation is refused under --cluster, since each node reads only
  its own s3.json).
  --replication sets the placement code (000 single-node; 001 = one remote copy).
  Unlike garage it HAS native TLS: --tls (self-signed) or
  --tls-cert/--tls-key (BYO, e.g. "pg cert" output) makes the S3 port HTTPS-only.
  Four ports (master/volume/filer/s3) come from seaweedfs_start_port (default
  8400); each role's gRPC twin at +10000 is reserved too.

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
  --cluster <name> marks the instance a member of a native Redis cluster: it is
  installed cluster-enabled (bus port = client+10000) but NOT assembled — pgcli
  never runs --cluster create for you. Install every member (they share the
  first member's password; use --advertise-host for cross-host), then assemble
  once yourself: "pg redis-cli --name <m1> -- --cluster create m1:p1 m2:p2
  m3:p3 --cluster-replicas N". N (per-master replicas, --cluster-replicas) is
  0 by default = masters-only, minimum 3 masters; with N>0 you need 3*(1+N)
  nodes total and Redis decides which become followers at create time. Clients
  follow redirects automatically: "pg redis-cli --name <m> get k" runs with -c.
  A cluster member cannot also be a read replica.
  Talk to it with "pg redis-cli" (a short-lived redis-cli container wired to the
  first redis addon; see that command's help).
  pg addon install predixy
          Stored under top-level addons.predixy in config (Linux only).
          Predixy — a Redis protocol proxy that fronts a native redis cluster
          (the --cluster shape above) as one ordinary redis:// endpoint, so
          clients need no cluster awareness: no -c, no MOVED handling.
          --backend host:port,... lists the FULL cluster node set (explicit,
          required — pgcli never derives it from the local redis addons);
          --password is the proxied cluster's own requirepass, reused on both
          sides (clients AUTH it to the proxy; the proxy AUTHs it to the
          cluster): PW=$(pg addon password redis --name n1) feeds both.
          --workers N sets Predixy's WorkerThreads (default 1). The port comes
          from its own pool (predixy_start_port, default 7617). Stateless — no
          data dir; remove only deletes the rendered predixy.conf.

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

pgAdmin 4 (the official web administration UI; top-level addon, Linux and macOS):
  pg addon install pgadmin [--name ui] [--email admin@pgcli.lan] [--password ...]
                           [--port N] [--listen 127.0.0.1] [--data-dir ...]
                           [--dsn <dsn> | --pg-name <instance>] [--image ...] [--force]
  --email/--password are pgAdmin's WEB LOGIN credentials (PGADMIN_DEFAULT_*),
  required at first launch; the password is generated when omitted, printed once,
  and stored under addons.pgadmin.<name>.password — read it with
  "pg addon password pgadmin". They are not a PostgreSQL password. The image
  validates --email and rejects reserved TLDs (.local included), so the default
  uses .lan.
  Optionally seed one server into the UI's list: pass --dsn (any PG endpoint) or
  --pg-name (resolve the DSN from a locally-managed instance — these two are
  mutually exclusive here). pgAdmin cannot import a server password, so the
  seeded entry prompts for it on first connect. --name is the addon key (defaults
  to "pgadmin"); it has nothing to do with --pg-name.
  Data is pgAdmin's own config/session DB (sqlite) under --data-dir (default
  <base_dir>/addon/pgadmin/<name>/data), so remove/reinstall revives the saved
  servers and settings; --clean-data deletes it (under rootless that goes through
  "podman unshare rm" since the files land owned by the container's mapped
  5050 uid). The image's entrypoint chowns the mounted dir to uid 5050 and drops
  privileges itself — pgcli never chowns on the host, and no wrapper image is
  needed. TLS is not wired up yet (plaintext http; put it behind a reverse proxy
  if you need HTTPS).
  Re-running install reuses a live container; --force recreates it to apply a
  changed --port/--listen/--email/--password/--dsn/--pg-name.

Grafana (the open-source observability dashboard; top-level addon, Linux and macOS):
  pg addon install grafana [--name ui] [--admin-user admin] [--password ...]
                           [--port N] [--listen 0.0.0.0] [--data-dir ...] [--image ...] [--force]
  --admin-user/--password are Grafana's WEB LOGIN credentials (GF_SECURITY_ADMIN_*),
  required at first launch; the password is generated when omitted, printed once,
  and stored under addons.grafana.<name>.password — read it with
  "pg addon password grafana". They are not a PostgreSQL password.
  --listen defaults to 0.0.0.0: unlike pgAdmin (which defaults to loopback), a
  monitoring dashboard is meant to be viewed from other machines, and on Linux the
  container runs on the host network — so 0.0.0.0 is what makes it reachable. The
  exposure is guarded by the generated strong password and self-signup being off
  (GF_USERS_ALLOW_SIGN_UP=false); pass --listen 127.0.0.1 to keep it loopback-only.
  Config is env-only (GF_*); v1 provisions no datasource or dashboard — add them in
  the browser. Data is Grafana's own sqlite meta DB (grafana.db) under --data-dir
  (default <base_dir>/addon/grafana/<name>/data), so remove/reinstall revives saved
  dashboards and settings; --clean-data deletes it (under rootless that goes through
  "podman unshare rm" since the files land owned by the container's mapped 472 uid).
  Unlike pgAdmin, the image's entrypoint does NOT chown or drop privileges — it runs
  as a baked uid 472 (group root, gid 0) and only warns if the dir is unwritable. So
  pgcli pre-chowns the data dir on the host via "podman unshare chown 472:0", which
  works under rootless podman; a rootful host gets an explicit manual-chown hint
  (see Known limitations). GF_SECURITY_ADMIN_* applies only while grafana.db is
  absent, so on a revived store the printed password is inert — reset it inside the
  container with "grafana-cli admin reset-admin-password" if needed. TLS is not
  wired up yet (plaintext http; front it with the nginx addon for HTTPS).
  Re-running install reuses a live container; --force recreates it to apply a
  changed --port/--listen/--admin-user/--password.

Prometheus (the metrics collection backend; top-level addon, Linux and macOS):
  pg addon install prometheus [--name p1] [--scrape JOB=HOST:PORT[,HOST:PORT...]] (repeatable)
                              [--scrape-append JOB=HOST:PORT[,HOST:PORT...]] (repeatable)
                              [--scrape-interval 15s] [--retention 15d]
                              [--alertmanager-url URL] (repeatable)
                              [--rule-file FILE] (repeatable)
                              [--port N] [--listen 0.0.0.0] [--conf-file FILE]
                              [--data-dir ...] [--image ...] [--force]
  Stored under top-level addons.prometheus in config. Prometheus scrapes metrics
  endpoints and serves its own query UI/API + PromQL; Grafana reads from it as a
  datasource. It is configured by a rendered prometheus.yml (a self-monitoring job
  scraping its own /metrics, plus one static_configs job per --scrape JOB, which
  folds all that JOB's targets into one job) that pgcli bind-mounts read-only and
  passes as --config.file, alongside --web.listen-address and
  --storage.tsdb.retention.time. A --scrape spec MUST carry a job name
  (JOB=host:port...; there is no bare-target form) and may list several
  comma-separated targets. --scrape declares the FULL job set (replaces the stored
  list); --scrape-append MERGES into it (folding each appended job's targets into
  the same-named job) — every appended target is probed with GET /metrics and
  install aborts, touching nothing, if any returns no data. The two are mutually
  exclusive. --conf-file instead copies YOUR prometheus.yml into pgcli's managed
  path (mutually exclusive with --scrape/--scrape-append/--scrape-interval and
  --alertmanager-url, which all only shape the rendered file).
  --alertmanager-url (bare host[:port] or http(s)://host[:port], repeatable) wires
  this Prometheus to one or more Alertmanager instances: the rendered file gains an
  alerting: section pushing fired alerts there PLUS a matching "alertmanager"
  scrape job so their own /metrics is watched. All URLs are validated (and must
  share one scheme — http or https) BEFORE anything is written; connectivity is
  NOT probed (Prometheus shows a dead alertmanager as DOWN on its own).
  --rule-file (repeatable) copies YOUR Prometheus rule files (*.rules.yml with a
  groups: block) into the managed rules dir, bind-mounts that dir read-only at
  /etc/prometheus/rules, and the rendered file loads them via rule_files. Unlike
  --alertmanager-url it stays valid alongside --conf-file (the copy and mount
  happen either way — your own file decides whether to reference them). The two
  new flags are wired into the SIGHUP reload too, with one exception: a running
  instance that goes from NO rule files to some needs the new mount, so install
  recreates it rather than only reloading.
  There is NO web login/password: Prometheus ships no authentication. --listen
  defaults to 0.0.0.0 (so Grafana on another host can reach it), but that endpoint
  runs arbitrary PromQL and is guarded ONLY by your firewall — pass
  --listen 127.0.0.1 to keep it loopback-only.
  Reload config changes WITHOUT recreating the container via "pg addon prometheus
  reload" (rewrites the managed file, then SIGHUP); a broken file is logged and
  the last good config keeps serving. Data is the TSDB under --data-dir (default
  <base_dir>/addon/prometheus/<name>/data), so remove/reinstall revives collected
  metrics; --clean-data deletes it. The image runs as uid/gid 65534 (nobody) and
  creates no writable dir, so pgcli pre-chowns the data dir on the host (rootless
  via "podman unshare chown 65534:65534"; a rootful host gets a manual-chown hint).
  Re-running install hot-reloads a live container when the config changed; --force
  recreates it to apply a changed --port/--listen/--retention.

Alertmanager (the alert dedup/grouping/routing frontend of the Prometheus stack; top-level addon, Linux and macOS):
  pg addon install alertmanager [--name am1] [--webhook-url URL] (repeatable)
                                [--port N] [--listen 0.0.0.0] [--conf-file FILE]
                                [--data-dir ...] [--image ...] [--force]
  Stored under top-level addons.alertmanager in config. Alertmanager receives
  alerts from Prometheus, deduplicates and groups them, and routes them to
  notification integrations (webhook/email/Slack/...); it also owns silences and
  inhibition. It is configured by a rendered alertmanager.yml — one minimal route
  (group by alertname) pointing at a single receiver — that pgcli bind-mounts
  read-only and passes as --config.file, alongside --web.listen-address,
  --storage.path, and --cluster.listen-address= (empty, which DISABLES the 9094
  HA gossip since a single instance has no peers). Each --webhook-url becomes one
  webhook_configs entry of the SAME "web.hook" receiver (so several URLs fan out
  from one notification); with no --webhook-url the rendered config uses a
  name-only "no-op" receiver — the UI/API is fully live but nothing is ever sent.
  URLs are validated (must parse as http(s) with a host) BEFORE anything is
  written; connectivity is NOT probed (a webhook fires at alert time). --conf-file
  instead copies YOUR alertmanager.yml into pgcli's managed path (mutually
  exclusive with --webhook-url) — the way to get real email/Slack/routing trees.
  The wiring into Prometheus happens on the PROMETHEUS side: pg addon install
  prometheus --alertmanager-url <host:port of this instance> injects the alerting
  section there (and adds the scrape job watching this instance's /metrics);
  in --conf-file mode write the alerting: block yourself.
  There is NO web login/password: Alertmanager ships no authentication, and this is
  sharper than Prometheus — anyone who reaches the port can CREATE SILENCES, i.e.
  mute your on-call. --listen defaults to 0.0.0.0; pass --listen 127.0.0.1 to keep
  it loopback-only, or firewall it. (Unlike Prometheus, Alertmanager's HTTP POST
  /-/reload endpoint CANNOT be disabled — pgcli simply never uses it.)
  Reload config changes WITHOUT recreating the container via "pg addon
  alertmanager reload" (rewrites the managed file, then SIGHUP). Data is the
  silence/notification state under --data-dir (default
  <base_dir>/addon/alertmanager/<name>/data), so remove/reinstall revives
  silences; --clean-data deletes it. The image runs as uid/gid 65534 (nobody) and
  creates no writable dir, so pgcli pre-chowns the data dir on the host (rootless
  via "podman unshare chown 65534:65534"; a rootful host gets a manual-chown hint).
  Re-running install hot-reloads a live container when the config changed; --force
  recreates it to apply a changed --port/--listen.

Gitea (the self-hosted Git service; top-level addon, Linux and macOS):
  pg addon install gitea [--name git1] [--admin-user gitea] [--password ...]
                         [--port N] [--ssh-port N] [--listen 0.0.0.0]
                         [--allow-registration] [--force]
                         [--dsn postgres://...] [--data-dir ...] [--image ...]
  --admin-user/--password are Gitea's WEB LOGIN credentials for the site-admin
  account; the password is generated when omitted, printed once, and stored under
  addons.gitea.<name>.password — read it with "pg addon password gitea". Unlike
  Grafana (GF_SECURITY_ADMIN_*), Gitea has NO env for the admin login: pgcli
  skips the web setup wizard (INSTALL_LOCK=true), waits for the container to
  finish first-boot migrations, then runs "gitea admin user create" inside it.
  Because the account is created (not seeded from env), the stored password is the
  one pgcli CREATED it with — on a revived data dir whose admin already exists,
  creation is skipped and the printed password is inert (rotate it inside the
  container with "gitea admin user change-password"). SECRET_KEY/INTERNAL_TOKEN
  are NEVER injected: the image's environment-to-ini re-applies every GITEA__*
  env into the PERSISTED app.ini on each boot, so a per-install secret would
  silently re-key a surviving database's encrypted 2FA/OAuth state — the volume
  owns crypto secrets, pgcli owns only the login above.
  --ssh-port N turns on git-over-ssh on host port N (N>0). The official image
  runs an OpenSSH sshd unconditionally, so SSH-off does not stop that daemon —
  it just parks it on an idle pool port (off 22, so it never fights the host
  sshd). Unset, the instance auto-assigns from gitea_ssh_start_port (default
  2222); pass --ssh-port 0 to take N off and let it re-park. On Linux the port
  is bound directly (host networking), so the clone URL is
  ssh://git@<host>:N/<owner>/<repo>.git. Without it, clone/push is HTTP-only.
  --listen defaults to 0.0.0.0 (a Git host is usually pushed to from other
  machines; containers run on the host network), guarded by the generated
  password and closed self-registration; pass --listen 127.0.0.1 for loopback.
  --allow-registration opens that last guard: Gitea's web UI shows a Register
  entry and anyone reaching the port can sign up (always as a NORMAL user —
  Gitea cannot make the register form produce an admin — so this is spam risk,
  not takeover). It is a stored toggle applied on --force recreate; close it
  again with --allow-registration=false --force.
  ROOT_URL is derived from --listen (a wildcard 0.0.0.0 collapses to localhost)
  so displayed clone URLs are connectable rather than echoing the bind address.
  --dsn puts Gitea's metadata in an EXTERNAL PostgreSQL instead of the embedded
  SQLite (same contract as PostgREST --dsn: the database must already exist and
  be reachable from the container — pgcli never creates or drops it, Gitea builds
  its schema on first boot); the repos and app.ini still live in --data-dir.
  Data is Gitea's single /data volume (repos, app.ini, its self-generated
  secrets, the default SQLite DB, ssh host keys) under --data-dir (default
  <base_dir>/addon/gitea/<name>/data), so remove/reinstall revives repos and
  settings; --clean-data deletes it (rootless: via "podman unshare rm" since
  files land owned by the container's mapped 1000 uid). The rootful image starts
  as root and su-execs the git user (uid/gid 1000, USER_UID/USER_GID) but does
  NOT chown an existing bind mount, so pgcli pre-chowns the data dir on the host
  (rootless via "podman unshare chown 1000:1000"; a rootful host running pgcli
  as root gets a direct recursive chown). TLS is not wired up yet (plaintext
  http; front it with the nginx addon for HTTPS). Re-running install reuses a
  live container; --force recreates it to apply a changed
  --port/--ssh-port/--listen/--admin-user/--password/--dsn.

nginx (HTTP reverse proxy in front of web services; top-level addon, Linux and macOS):
  pg addon install nginx [--name proxy]
                         [--upstream name=<n>,path=<p>,backend=<h:p>] (repeatable)
                         [--http-port N] [--https-port N] [--listen 127.0.0.1]
                         [--tls] [--tls-cert FILE] [--tls-key FILE]
                         [--image ...] [--force]
  Stored under top-level addons.nginx in config. nginx is an HTTP reverse proxy
  that fronts multiple web services (pgAdmin, PostgREST, etc.) with path-based
  routing and optional TLS termination. Each --upstream adds one backend:
  name is the upstream name (defaults to a sanitized path if omitted), path is
  the location (defaults to /), backend is the host:port to proxy to (repeat
  backend=... for multiple servers in the same upstream, or use backends=h1:p1,h2:p2).
  --tls enables an HTTPS listener alongside HTTP; without --tls-cert/--tls-key
  pgcli generates a self-signed cert via tlsca. Re-running install with --force
  recreates the container to apply changed backends or TLS settings.

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
  pg addon install haproxy --name lb --conf-file /etc/haproxy/lb.cfg   # run a custom haproxy.cfg verbatim (any native proxying)
  pg addon install minio --name store --data-dir /srv/minio
  pg addon install silo --name store --tls
  pg addon install rustfs --name store --tls
  pg addon install rustfs --name store --drive /mnt/rustfs/d0 --drive /mnt/rustfs/d1 --drive /mnt/rustfs/d2 --drive /mnt/rustfs/d3 --tls
  pg addon install garage --name store            # single-node, plaintext HTTP; front with nginx for TLS
  pg addon install seaweedfs --name store --tls    # single-node "weed mini", native HTTPS S3
  pg addon install seaweedfs --name store --drive /mnt/sw/d1 --drive /mnt/sw/d2   # multi-disk volume server (one dir per disk)
  pg addon install seaweedfs --name store --cluster lab --advertise-host 10.0.0.11 --replication 001 --root-user <SW-access-key> --root-password <secret-key> --dsn "postgres://admin:<pw>@10.0.0.11:35432/seaweed_store" --peer 10.0.0.11:8400 --peer 10.0.0.12:8400 --peer 10.0.0.13:8400
                                     # run the SAME command on .12 and .13, changing only --advertise-host (creds and --dsn stay identical)
  pg addon install redis
  pg addon install redis --name cache --version 7 --maxmemory 256mb
  pg addon install redis --name session --listen 127.0.0.1
  pg addon install redis --name cache-r --replica-of cache
  pg addon install redis --name remote-r --replica-of-host 10.0.0.9 --replica-of-port 6379 --password <master-pw>
  pg addon install predixy --name proxy --backend 127.0.0.1:6379,127.0.0.1:6380,127.0.0.1:6381 --password "$(pg addon password redis --name n1)"
  pg addon install predixy --name proxy --backend 10.0.0.11:6379,10.0.0.12:6379,10.0.0.13:6379 --password <cluster-pw> --workers 4 --port 7617
  pg addon install postgrest -i proj01 --schema api --anon-role web_anon
  pg addon install postgrest --dsn "postgres://api:pass@127.0.0.1:5000/appdb" --pg-name app-api --schema api
  pg addon install pgadmin
  pg addon install pgadmin --name console --email me@example.com
  pg addon install pgadmin --pg-name proj01
  pg addon install pgadmin --dsn "postgres://readonly:pass@127.0.0.1:5000/appdb"
  pg addon install grafana
  pg addon install grafana --name dash --admin-user ops
  pg addon install grafana --name local --listen 127.0.0.1   # loopback-only instead of the 0.0.0.0 default
  pg addon install prometheus
  pg addon install prometheus --name p1 --scrape postgres-exporter=127.0.0.1:9187 --retention 30d
  pg addon install prometheus --name p1 --scrape node=10.0.0.5:9100,10.0.0.6:9100   # one job, two targets
  pg addon install prometheus --name p1 --scrape-append node=10.0.0.7:9100           # add a target to the node job (probed; aborts if down)
  pg addon install prometheus --name local --listen 127.0.0.1   # loopback-only (Prometheus has no auth)
  pg addon install prometheus --name p1 --conf-file /etc/prometheus/my.yml   # run your own prometheus.yml verbatim
  pg addon install prometheus --name p1 --alertmanager-url 127.0.0.1:9093   # forward fired alerts to an alertmanager (bare host:port, repeatable; also scrapes it)
  pg addon install prometheus --name p1 --rule-file /srv/rules/alert.rules.yml   # copy a rule file in and load it via rule_files (repeatable; works with --conf-file)
  pg addon install alertmanager
  pg addon install alertmanager --name am1 --webhook-url http://127.0.0.1:5001/   # fan-out to one webhook
  pg addon install alertmanager --name am1 --webhook-url http://a:5001/ --webhook-url http://b:5001/   # both URLs in one web.hook receiver
  pg addon install alertmanager --name local --listen 127.0.0.1   # loopback-only (Alertmanager has no auth, and silences are dangerous)
  pg addon install alertmanager --name am1 --conf-file /etc/alertmanager/my.yml   # run your own alertmanager.yml verbatim
  pg addon install gitea
  pg addon install gitea --name git1 --admin-user root                         # web login; admin created after first boot
  pg addon install gitea --name git1 --ssh-port 2222                           # also expose git@...:2222 clone/push over SSH
  pg addon install gitea --name git1 --dsn "postgres://gitea:pass@127.0.0.1:5432/giteadb"  # metadata in an existing PostgreSQL instead of SQLite
  pg addon install gitea --name local --listen 127.0.0.1                       # loopback-only instead of the 0.0.0.0 default
  pg addon install gitea --name git1 --allow-registration --force             # open self-service signup (web Register entry; signups are normal users)
  pg addon install beegfs                                                   # converged single-host filesystem: mgmtd+meta+storage, fixed ports
  pg addon install beegfs --name fs1 --listen 10.0.0.11                     # advertise that address as sysMgmtdHost (clients mount it)
  pg addon install beegfs --name fs1 --beegfs-mgmtd-port 18008 --beegfs-mgmtd-grpc-port 18010   # move mgmtd's two ports (meta/storage re-target automatically)
  pg addon install beegfs --name fs1 --roles storage --mgmtd-host 10.0.0.11 --node-id 2 --password <conn-auth> --storage-target /mnt/d1 --storage-target /mnt/d2  # cross-host: add a 2-target storage node (run ON that host; conn-auth from "pg addon beegfs join-hint")
  pg addon install beegfs --license-file license.pem                        # carry a ThinkParQ license PEM into mgmtd (mgmtd host only; hot-swap later with "pg addon beegfs license --set")
  pg addon install juicefs --name data --s3-endpoint https://s3.example.com --access-key AK --secret-key SK --bucket my-bucket --dsn "postgres://u:p@meta-host:5432/jfs?sslmode=disable"   # external S3 + external metadata db (create the db first — pgcli never creates it)
  pg addon install juicefs --name j1 --s3-endpoint http://minio.internal:9000 --access-key AK --secret-key SK --dsn "sqlite3:///var/lib/jfs/j1.db"   # http endpoint + local sqlite metadata; mount at <base_dir>/addon/juicefs/j1/mnt
  pg addon install zerofs --name z1 --s3-endpoint https://s3.example.com --access-key AK --secret-key SK --bucket zerofs-z1   # auto fencing (needs conditional-put support), generated encryption password (pg addon password zerofs)
  pg addon install zerofs --name z1 --s3-endpoint http://store.internal:9000 --access-key AK --secret-key SK --conditional-put redis --redis-dsn redis://:pw@fencer:6379   # store lacks If-None-Match → fence through an explicit redis URL
  pg addon install zerofs --name z1 --encryption-password '<from pg addon password zerofs>'   # re-attach a bucket whose entry was removed (a fresh password could not unwrap its data)
  pg addon install duckdb                                                                  # default 1.5.6 LTS, install extensions, data dir at <base>/addon/duckdb/duckdb
  pg addon install duckdb --version 2.0.0 --name analytics                                 # pin v2.0.0 under a custom name
  pg addon install nginx --name proxy --upstream name=pgadmin,path=/admin,backend=127.0.0.1:5050
  pg addon install nginx --name proxy --upstream name=pgadmin,path=/admin,backend=127.0.0.1:5050 --upstream name=postgrest,path=/api,backend=127.0.0.1:3000 --tls`,
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
	Long: `List all installed add-ons. The default output is safe to paste into
tickets and screenshots: stored credentials (Redis requirepass, the Predixy
proxy password, the object stores' root passwords) are shown only as presence
markers. Add --show-password to print them too — or read them straight from
pg.yaml.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		showPassword, _ := cmd.Flags().GetBool("show-password")
		return runAddonList(showPassword)
	},
}

// addonPasswordCmd prints one addon's stored password on stdout — the
// on-demand reveal counterpart to the deliberately redacting `pg addon list`.
// The name mirrors `pg ha passwords`: a dedicated read-only credential command,
// shaped for $(...) capture in scripts.
var addonPasswordCmd = &cobra.Command{
	Use:   "password <addon>",
	Short: "Print an add-on's stored password (Redis requirepass, Predixy proxy password, MinIO/silo/rustfs/garage/seaweedfs key secret, pgAdmin/Grafana/Gitea web login password, BeeGFS conn-auth secret, juicefs S3 secret, zerofs encryption password)",
	Long: `Print the password stored for one add-on instance: the Redis
requirepass (generated by pgcli), the Predixy proxy password (the
operator-supplied copy of the proxied cluster's requirepass), the
MinIO/silo/rustfs root password or garage secret key (the access-key secret — pair it with the
instance's Root user from "pg addon list"), or the pgAdmin, Grafana or Gitea web
login password (the PGADMIN_DEFAULT_PASSWORD / GF_SECURITY_ADMIN_PASSWORD / Gitea
admin-account password generated for the UI's sign-in form — not a PostgreSQL
password), or the BeeGFS connection-auth secret (the shared CONN_AUTH_FILE_DATA
value every daemon and client must carry — not a login, but the addon's only
credential), or the JuiceFS S3 secret-key it formatted the volume with, or the
ZeroFS encryption password (the DEK wrap key — losing it means losing the
volume's data; pass it back to "pg addon install zerofs --encryption-password"
to re-attach a bucket after a remove).

The single value goes to stdout with no decoration, so it composes into
scripts and app config:

  export REDISCLI_AUTH="$(pg addon password redis --name cache)"
  mc alias set local http://127.0.0.1:9000 admin "$(pg addon password minio --name store)"

The default "pg addon list" never prints passwords; this command is the
explicit, opt-in way to reveal one without opening pg.yaml.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		file, _ := cmd.Flags().GetString("file")
		return runAddonPassword(args[0], name, file)
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

Infra add-ons (etcd/pgdog/haproxy/minio/silo/rustfs/garage/seaweedfs/redis/predixy/pgadmin/grafana/prometheus/alertmanager/gitea/beegfs/juicefs/zerofs/duckdb) use --name.
Remove keeps the data directory; pass --clean-data for object-storage/KV/pgAdmin/Grafana/Prometheus/Alertmanager/Gitea/BeeGFS/DuckDB
data to go too. For beegfs the unit is the filesystem on THIS host — its role containers (all three on a converged host, the configured subset on a cross-host member) go, and --clean-data deletes their data dirs.
For juicefs/zerofs (host-binary addons) remove unmounts and stops the processes but NEVER touches object storage or the
metadata engine — only the config entry goes; --clean-data additionally deletes the addon's host dir (cache, logs, sqlite
meta, config.toml). For zerofs the bucket survives by design: re-attach it later by passing the original password
("pg addon password zerofs" before removing) to a fresh install via --encryption-password.
For duckdb (host-binary CLI tool) remove deletes the config entry; --clean-data additionally deletes the data directory (database files).
The binary is kept (shared across instances).

Examples:
  pg addon remove pgbouncer -i proj01
  pg addon remove pgbouncer --pg-name remote-proj01
  pg addon remove postgrest -i proj01
  pg addon remove postgrest --pg-name app-api
  pg addon remove redis --name cache
  pg addon remove redis --name cache --clean-data
  pg addon remove predixy --name proxy
  pg addon remove pgadmin
  pg addon remove pgadmin --clean-data
  pg addon remove grafana
  pg addon remove grafana --clean-data
  pg addon remove prometheus
  pg addon remove prometheus --clean-data
  pg addon remove alertmanager
  pg addon remove alertmanager --clean-data
  pg addon remove gitea
  pg addon remove gitea --clean-data
  pg addon remove beegfs
  pg addon remove beegfs --clean-data
  pg addon remove juicefs --name data
  pg addon remove juicefs --name data --clean-data
  pg addon remove zerofs --name z1 --clean-data
  pg addon remove duckdb --name analytics
  pg addon remove duckdb --name analytics --clean-data`,
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
  garage     pg addon start garage [--name store]
  seaweedfs  pg addon start seaweedfs [--name store]
  redis      pg addon start redis [--name cache]
  predixy    pg addon start predixy [--name proxy]
  pgadmin    pg addon start pgadmin [--name pgadmin]
  grafana    pg addon start grafana [--name grafana]
  prometheus pg addon start prometheus [--name prometheus]
  alertmanager pg addon start alertmanager [--name alertmanager]
  gitea      pg addon start gitea [--name gitea]
  beegfs     pg addon start beegfs [--name fs1]   (starts this host's roles in dependency order — mgmtd → meta → storage, or the subset a cross-host member runs)
  juicefs    pg addon start juicefs [--name data]   (host binary: mounts the volume — alias of "pg addon juicefs mount")
  zerofs     pg addon start zerofs [--name z1]   (host binary: brings up the 9P server — alias of "pg addon zerofs start"; the FUSE client is "pg addon zerofs mount")
  duckdb     pg addon start duckdb [--name duckdb]   (no-op: DuckDB is a CLI tool, not a daemon — run "pg addon duckdb shell" instead)
  nginx      pg addon start nginx [--name proxy]
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
  pg addon start garage --name store
  pg addon start redis --name cache
  pg addon start predixy --name proxy
  pg addon start pgadmin
  pg addon start grafana
  pg addon start prometheus
  pg addon start alertmanager
  pg addon start gitea
  pg addon start beegfs
  pg addon start juicefs --name data
  pg addon start zerofs --name z1
  pg addon start nginx --name proxy
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
  garage     pg addon stop garage [--name store]
  seaweedfs  pg addon stop seaweedfs [--name store]
  redis      pg addon stop redis [--name cache]
  predixy    pg addon stop predixy [--name proxy]
  pgadmin    pg addon stop pgadmin [--name pgadmin]
  grafana    pg addon stop grafana [--name grafana]
  prometheus pg addon stop prometheus [--name prometheus]
  alertmanager pg addon stop alertmanager [--name alertmanager]
  gitea      pg addon stop gitea [--name gitea]
  beegfs     pg addon stop beegfs [--name fs1]   (stops this host's roles in reverse order — storage → meta → mgmtd)
  juicefs    pg addon stop juicefs [--name data]   (host binary: unmounts — alias of "pg addon juicefs umount")
  zerofs     pg addon stop zerofs [--name z1]   (host binary: unmounts the client and stops the 9P server — alias of "pg addon zerofs stop")
  duckdb     pg addon stop duckdb [--name duckdb]   (no-op: DuckDB is a CLI tool, not a daemon)
  nginx      pg addon stop nginx [--name proxy]
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
  pg addon stop garage --name store
  pg addon stop redis --name cache
  pg addon stop predixy --name proxy
  pg addon stop pgadmin
  pg addon stop grafana
  pg addon stop prometheus
  pg addon stop alertmanager
  pg addon stop gitea
  pg addon stop beegfs
  pg addon stop juicefs --name data
  pg addon stop zerofs --name z1
  pg addon stop nginx --name proxy
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
	addonCmd.AddCommand(addonInstallCmd, addonListCmd, addonPasswordCmd, addonRemoveCmd, addonStartCmd, addonStopCmd)
	addonCmd.AddCommand(addonNginxCmd, addonHAProxyCmd, addonPrometheusCmd, addonAlertmanagerCmd, addonGiteaCmd)
	addonNginxCmd.AddCommand(addonNginxReloadCmd, addonNginxTestCmd, addonNginxExecCmd)
	addonHAProxyCmd.AddCommand(addonHAProxyReloadCmd)
	addonPrometheusCmd.AddCommand(addonPrometheusReloadCmd)
	addonAlertmanagerCmd.AddCommand(addonAlertmanagerReloadCmd)
	addonGiteaCmd.AddCommand(addonGiteaUserCmd)
	addonGiteaUserCmd.AddCommand(addonGiteaUserCreateCmd, addonGiteaUserDeleteCmd, addonGiteaUserListCmd)
	addonHAProxyReloadCmd.Flags().String("name", "", "haproxy instance name")
	addonPrometheusReloadCmd.Flags().String("name", "", "prometheus instance name (default \"prometheus\")")
	addonAlertmanagerReloadCmd.Flags().String("name", "", "alertmanager instance name (default \"alertmanager\")")
	addonNginxReloadCmd.Flags().String("name", "", "nginx instance name (default \"nginx\")")
	addonNginxTestCmd.Flags().String("name", "", "nginx instance name (default \"nginx\")")
	addonNginxTestCmd.Flags().String("conf-file", "", "path to an nginx.conf file to test directly (no installed instance required; overrides --name)")
	addonNginxTestCmd.Flags().String("image", "", "image tag to use for the temporary test container (default: docker.io/library/nginx:1.27-alpine)")
	addonNginxExecCmd.Flags().String("name", "", "nginx instance name (default \"nginx\")")
	addonGiteaUserCreateCmd.Flags().String("name", "", "gitea instance name (default \"gitea\")")
	addonGiteaUserCreateCmd.Flags().String("username", "", "account to create (required)")
	addonGiteaUserCreateCmd.Flags().String("email", "", "account email (default \"<username>@localhost\")")
	addonGiteaUserCreateCmd.Flags().String("password", "", "login password; generate a strong one if omitted (printed once, not stored)")
	addonGiteaUserCreateCmd.Flags().Bool("admin", false, "create a site admin instead of a normal member")
	addonGiteaUserCreateCmd.Flags().Bool("must-change-password", false, "force a password change on first login (off by default — on it also blocks HTTP clone/push and API basic-auth until changed)")
	addonGiteaUserDeleteCmd.Flags().String("name", "", "gitea instance name (default \"gitea\")")
	addonGiteaUserDeleteCmd.Flags().String("username", "", "account to delete (required)")
	addonGiteaUserDeleteCmd.Flags().Bool("purge", false, "also remove the account's repositories, organizations and comments (destructive)")
	addonGiteaUserListCmd.Flags().String("name", "", "gitea instance name (default \"gitea\")")

	// Basic flags
	addonInstallCmd.Flags().String("dsn", "", "pgbouncer/postgrest: PG instance connection string for remote mode (postgres://user:pass@host:port/db) / pgadmin: a one-time server to pre-register in its UI (mutually exclusive with --pg-name there) / seaweedfs: the filer's shared metadata PostgreSQL for --cluster (REQUIRED; must be reachable from EVERY host, so use the host-published port / LAN address, not an instance's container-local 127.0.0.1:5432; the metadata database itself must already exist — pgcli only probes connectivity and never creates or drops databases here, weed's postgres2 store creates its own tables inside it) / gitea: the metadata database instead of the embedded SQLite (optional; same contract — the database must already exist, Gitea creates its own schema on first boot, and it must be reachable from the Gitea container)")
	addonInstallCmd.Flags().String("pg-name", "", "name to identify a remote PgBouncer or PostgREST (required with --dsn) / pgadmin: resolve the DSN from this locally-managed instance instead of passing --dsn (its own addon --name is unrelated to this)")
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
	addonInstallCmd.Flags().String("name", "", "addon key/name for the etcd member, pgdog proxy, minio, silo, rustfs, garage, seaweedfs, redis, predixy, pgadmin, grafana, prometheus, alertmanager, gitea or beegfs instance (default \"etcd\"/\"pgdog\"/\"minio\"/\"silo\"/\"rustfs\"/\"garage\"/\"seaweedfs\"/\"redis\"/\"predixy\"/\"pgadmin\"/\"grafana\"/\"prometheus\"/\"alertmanager\"/\"gitea\"/\"beegfs\")")
	addonInstallCmd.Flags().Int("client-port", 0, "etcd client host port (0=auto-assign from etcd_start_port)")
	addonInstallCmd.Flags().Int("peer-port", 0, "etcd peer host port (0=auto-assign, next free port after client)")
	addonInstallCmd.Flags().String("image", "", "override the addon image tag verbatim (etcd default quay.io/coreos/etcd:v3.5.30; redis default resolved from --version, e.g. docker.io/library/redis:8.10.2; predixy default ghcr.io/mars-base/pgcli/predixy:7.0.1-alpine; pgadmin default docker.io/dpage/pgadmin4:9.18; grafana default docker.io/grafana/grafana:13.2.4; prometheus default docker.io/prom/prometheus:v3.15.0; alertmanager default docker.io/prom/alertmanager:v0.34.1; gitea default docker.io/gitea/gitea:28.1.0)")
	addonInstallCmd.Flags().String("cluster", "", "cluster grouping token: etcd --initial-cluster-token (default \"pgcli-etcd\"); redis native-cluster name (--cluster-enabled yes, members sharing it form one cluster you assemble once with `pg redis-cli -- --cluster create ...`); garage — switch on multi-node mode (members sharing the name join one Garage cluster; requires --advertise-host); seaweedfs — switch on multi-node mode (members sharing the name join one Raft cluster via the full --peer list; requires --advertise-host, a shared --dsn metadata database, and the SAME --root-user/--root-password on every node)")
	addonInstallCmd.Flags().StringArray("peer", nil, "cluster peer list (repeat one per peer): garage — an existing member's <id>@<addr> to connect to (a joiner usually needs just one — peering is transitive; printed by the bootstrap node's install summary). seaweedfs — a member's <ip>:<master_port> Raft address; pass the FULL list on EVERY node (weed requires the complete membership, MNMD-style, no bootstrap/join distinction), your own entry optional")
	addonInstallCmd.Flags().String("replication", "", "seaweedfs cluster only: placement code XYZ (X=remote copies, Y=distinct racks, Z=distinct data centers; each digit 0-3), e.g. 001 = one remote copy. Default \"000\" (single-node — no peers to copy to). Immutable once set in a cluster")
	addonInstallCmd.Flags().String("bucket", "", "seaweedfs: default bucket to create and seed on first boot (default = instance name)")
	addonInstallCmd.Flags().Int("volume-size", 0, "seaweedfs: per-volume size cap in MB (-master.volumeSizeLimitMB; default 1024 — weed mini's upstream default of 128 is too small for real use). 0 = keep default")
	addonInstallCmd.Flags().String("zone", "", "garage cluster only: this node's layout zone (`garage layout assign -z`) — nodes in different zones survive a whole-zone outage; requires --capacity")
	addonInstallCmd.Flags().Int("capacity", 0, "garage cluster only: this node's layout capacity in GiB (`garage layout assign -c`; requires --zone; 0 = start the node without assigning a role)")
	addonInstallCmd.Flags().Int("replication-factor", 0, "garage cluster only: object replication factor (garage.toml replication_factor; must be <= node count — a 3-node cluster with rf 3 tolerates losing 2; set it on EVERY member, the value is per-node config). Empty for garage = default 1 (single-node); etcd/redis ignore it")
	addonInstallCmd.Flags().String("rpc-secret", "", "garage cluster only: the shared 64-hex cluster RPC secret (garage.toml rpc_secret) — generated by the bootstrap node and printed once; every joiner MUST pass the same value (pgcli never executes remotely, so you carry it). Invalid on a single-node install")
	addonInstallCmd.Flags().Int("cluster-replicas", 0, "redis native-cluster only: per-master replica count for the --cluster create you will run (default 0 = masters-only). Only shapes the suggested assemble command and node-count guidance — which nodes become followers is decided by Redis at create time, not here; the whole group inherits the first member's value")
	addonInstallCmd.Flags().String("data-dir", "", "data directory for an etcd cluster root, a MinIO, silo, rustfs, garage, redis, pgAdmin, Grafana, Gitea or DuckDB instance, absolute or relative to base_dir (etcd default <base_dir>/addon/etcd, each member uses <root>/<name>/data; minio default <base_dir>/addon/minio/<name>/data; silo default <base_dir>/addon/silo/<name>/data; rustfs default <base_dir>/addon/rustfs/<name>/data; garage default <base_dir>/addon/garage/<name>/data (its metadata dir is a sibling .../meta); seaweedfs default <base_dir>/addon/seaweedfs/<name>/data; redis default <base_dir>/addon/redis/<name>/data; pgadmin default <base_dir>/addon/pgadmin/<name>/data; grafana default <base_dir>/addon/grafana/<name>/data; prometheus default <base_dir>/addon/prometheus/<name>/data; alertmanager default <base_dir>/addon/alertmanager/<name>/data; gitea default <base_dir>/addon/gitea/<name>/data; beegfs — ONE dir for the whole filesystem, its three services use <dir>/mgmtd, <dir>/meta and <dir>/storage, default root <base_dir>/addon/beegfs/<name>; duckdb default <base_dir>/addon/duckdb/<name>)")
	addonInstallCmd.Flags().String("advertise-host", "", "peer-visible host: etcd member peer/client URLs, redis native-cluster --cluster-announce-ip, , garage cluster rpc_public_addr, and seaweedfs cluster weed -ip that peers dial (empty=127.0.0.1 single-host; set a LAN IP or FQDN for cross-host clusters — required on every garage or seaweedfs --cluster member)")
	addonInstallCmd.Flags().String("join", "", "client endpoint of an existing cluster member to join cross-host, e.g. http://10.0.0.12:2379 (implies --initial-cluster-state existing; requires --advertise-host)")
	addonRemoveCmd.Flags().String("name", "", "name of the etcd member, pgdog proxy, haproxy, minio, silo, rustfs, garage, seaweedfs, redis, predixy, pgadmin, grafana, prometheus, alertmanager, gitea or beegfs instance to remove (default \"etcd\"/\"pgdog\"/\"haproxy\"/\"minio\"/\"silo\"/\"rustfs\"/\"garage\"/\"seaweedfs\"/\"redis\"/\"predixy\"/\"pgadmin\"/\"grafana\"/\"prometheus\"/\"alertmanager\"/\"gitea\"/\"beegfs\")")
	addonRemoveCmd.Flags().Bool("clean-data", false, "also delete the MinIO/silo/rustfs/garage/seaweedfs/redis/pgAdmin/Grafana/Prometheus/Alertmanager/Gitea data directory (object storage / backup repository / key-value data / pgAdmin config+sessions, Grafana dashboards sqlite DB, Prometheus TSDB, Alertmanager silences+notification state, or Gitea repos + app.ini + its default SQLite DB) and pgcli-rendered files beside it, or the juicefs/zerofs host per-instance dir (cache, logs, sqlite meta); never the S3 bucket or a PG metadata database. No-op for predixy, which is stateless. Never drops a seaweedfs filer metadata database or a Gitea --dsn PostgreSQL database")

	// haproxy flags (top-level load balancer in front of a Patroni cluster)
	addonInstallCmd.Flags().String("mode", "", "HAProxy routing mode: unified (default, all traffic to the leader) or split (separate read listener for replicas)")
	addonInstallCmd.Flags().String("ha", "", "Patroni scope to derive backend members from (auto: every local member's pg + restapi port); mutually exclusive with --node")
	addonInstallCmd.Flags().StringArray("node", nil, "HAProxy backend node NAME=HOST:PGPORT:RESTPORT (repeatable; PGPORT is the routed backend, RESTPORT the health-check port)")
	addonInstallCmd.Flags().Int("rw-port", 0, "HAProxy read-write listener host port (0=auto-assign from haproxy_start_port)")
	addonInstallCmd.Flags().Int("ro-port", 0, "HAProxy read-only listener host port, split mode only (0=auto-assign, next free port)")
	addonInstallCmd.Flags().Int("stats-port", 0, "HAProxy stats page host port (0=auto-assign, next free port)")
	addonInstallCmd.Flags().String("max-lag", "", "replica lag threshold for the read listener, e.g. 1MB (split mode; empty=no filter)")

	// nginx flags (top-level HTTP reverse proxy in front of web services)
	addonInstallCmd.Flags().Int("http-port", 0, "nginx HTTP listener host port (0=auto-assign from nginx_start_port)")
	addonInstallCmd.Flags().Int("https-port", 0, "nginx HTTPS listener host port, TLS only (0=auto-assign, next free port)")
	addonInstallCmd.Flags().Int("worker-connections", 0, "nginx worker_connections (max simultaneous connections per worker; default 1024)")
	addonInstallCmd.Flags().StringArray("upstream", nil, "nginx upstream target name=<upstream>,path=<location>,backend=<host:port> (repeatable; e.g. --upstream name=pgadmin,path=/admin,backend=127.0.0.1:5050, or backends=h1:p1,h2:p2 for multiple servers)")
	addonInstallCmd.Flags().String("conf-file", "", "path to a full custom config file (nginx: nginx.conf, mutually exclusive with --upstream; haproxy: haproxy.cfg, mutually exclusive with --node/--ha; prometheus: prometheus.yml, mutually exclusive with --scrape/--scrape-append/--scrape-interval/--alertmanager-url (but NOT --rule-file, which is copied and mounted either way); alertmanager: alertmanager.yml, mutually exclusive with --webhook-url) — pgcli copies it into its managed path and mounts that, manages the container lifecycle, and you control the whole config; validated with nginx -t / haproxy -c at install (prometheus/alertmanager: not pre-validated — a bad file is logged and the last good config keeps serving; reload with SIGHUP)")
	addonInstallCmd.Flags().Bool("reload", false, "haproxy --conf-file only: hot-reload the running instance (validate with haproxy -c, then SIGUSR2) instead of recreating the container — no dropped connections; ignored otherwise")

	// prometheus flags (top-level metrics collection backend)
	addonInstallCmd.Flags().String("retention", "", "Prometheus TSDB retention (--storage.tsdb.retention.time), e.g. 15d, 6mo, 2y (default \"15d\")")
	addonInstallCmd.Flags().String("scrape-interval", "", "Prometheus global scrape_interval in the rendered prometheus.yml (default \"15s\"); mutually exclusive with --conf-file")
	addonInstallCmd.Flags().StringArray("scrape", nil, "Prometheus scrape spec JOB=HOST:PORT[,HOST:PORT...] (repeatable; the JOB name is required, and several comma-separated targets share one job) — declares the FULL job set (replaces the stored list), each rendering one static_configs job alongside the self-monitoring job; e.g. --scrape postgres-exporter=127.0.0.1:9187. Mutually exclusive with --scrape-append and --conf-file")
	addonInstallCmd.Flags().StringArray("scrape-append", nil, "Prometheus scrape spec(s) JOB=HOST:PORT[,HOST:PORT...] to ADD to the instance's stored --scrape list (same form as --scrape, repeatable) instead of replacing it; folds each appended job's targets into the same-named job. Every appended target is probed with GET /metrics and install aborts, touching nothing, if any returns no data. Mutually exclusive with --scrape and --conf-file")
	addonInstallCmd.Flags().StringArray("alertmanager-url", nil, "Prometheus: Alertmanager instance(s) to forward fired alerts to (repeatable; bare HOST[:PORT] — a portless entry gets Alertmanager's 9093 — or http(s)://HOST[:PORT][/path]). The rendered prometheus.yml gains an alerting: section pushing there PLUS a matching alertmanager scrape job (so Alertmanager's own /metrics is watched; not a separate opt-in). All entries are validated (and must share ONE scheme — mixing http and https aborts) BEFORE anything is written; connectivity is NOT probed (Prometheus shows a dead alertmanager itself). Mutually exclusive with --conf-file, which owns the whole file — write your own alerting: section there")
	addonInstallCmd.Flags().StringArray("rule-file", nil, "Prometheus: path to a rule file (*.rules.yml with a top-level groups: block; repeatable) — pgcli copies each into its managed rules dir (basename only; two files sharing a basename abort), bind-mounts that dir read-only at /etc/prometheus/rules, and the rendered prometheus.yml loads the copies via rule_files. Valid alongside --conf-file: the copy and mount happen either way, and your own file decides whether to reference /etc/prometheus/rules. A missing or empty file aborts, touching nothing")
	addonInstallCmd.Flags().StringArray("webhook-url", nil, "Alertmanager webhook URL to notify (repeatable; e.g. --webhook-url http://hook:5001/) — every URL is rendered as one webhook_configs entry of the SAME web.hook receiver (so multiple URLs fan out); the root route points at it. Each URL is validated (must parse as an http(s) URL with a host) BEFORE anything is written; a bad URL aborts the install touching nothing. Connectivity is NOT probed (a webhook fires at alert time, not install time). Mutually exclusive with --conf-file; without it the rendered config uses a name-only no-op receiver (nothing is ever sent)")

	// victoriametrics flags (top-level time-series database)
	addonInstallCmd.Flags().String("retention-months", "", "VictoriaMetrics data retention in months (default \"1\"); e.g. 3 = 3 months")
	addonInstallCmd.Flags().StringArray("extra-arg", nil, "VictoriaMetrics: extra CLI flag passed to the binary (repeatable), e.g. --extra-arg -search.maxUniqueTimeseries=1000000 — appended last so last-wins overrides earlier flags")

	// vmcluster flags (VictoriaMetrics cluster — cross-host, horizontal scaling)
	addonInstallCmd.Flags().StringArray("storage-node", nil, "VMCluster: vmstorage address (repeatable; format host:insertPort:selectPort, e.g. 10.0.0.1:8483:8484). Lists ALL vmstorage nodes in the cluster for sharding/replication. Omit for converged single-host (auto-derived)")
	addonInstallCmd.Flags().Int("insert-port", 0, "VMCluster: vminsert HTTP port")
	addonInstallCmd.Flags().Int("select-port", 0, "VMCluster: vmselect HTTP port")
	addonInstallCmd.Flags().Int("storage-port", 0, "VMCluster: vmstorage HTTP port")
	addonInstallCmd.Flags().Int("internal-insert-port", 0, "VMCluster: vmstorage TCP port for vminsert protocol")
	addonInstallCmd.Flags().Int("internal-select-port", 0, "VMCluster: vmstorage TCP port for vmselect protocol")
	addonInstallCmd.Flags().String("insert-image", "", "VMCluster: vminsert image override")
	addonInstallCmd.Flags().String("select-image", "", "VMCluster: vmselect image override")
	addonInstallCmd.Flags().String("storage-image", "", "VMCluster: vmstorage image override")
	addonInstallCmd.Flags().StringArray("insert-extra-arg", nil, "VMCluster: extra CLI flag for vminsert (repeatable)")
	addonInstallCmd.Flags().StringArray("select-extra-arg", nil, "VMCluster: extra CLI flag for vmselect (repeatable)")
	addonInstallCmd.Flags().StringArray("storage-extra-arg", nil, "VMCluster: extra CLI flag for vmstorage (repeatable)")

	// minio flags (top-level single-node S3-compatible object storage)
	addonInstallCmd.Flags().Int("api-port", 0, "MinIO/silo/rustfs S3 API host port (0=auto-assign from the shared minio_start_port pool); for garage, the S3 API port (0=auto from garage_start_port, default 3900 — garage takes four consecutive ports API/RPC/Web/Admin)")
	addonInstallCmd.Flags().Int("console-port", 0, "MinIO/silo/rustfs web console host port (0=auto-assign, next free port)")
	addonInstallCmd.Flags().String("listen", "", "bind address for the haproxy listeners / MinIO, silo, rustfs, garage or seaweedfs server / PostgREST HTTP server / pgAdmin web server (default \"127.0.0.1\"; 0.0.0.0 exposes them on the network) — Redis, Predixy and Grafana default to \"0.0.0.0\" instead, since their password is always required (and a dashboard is meant to be viewed remotely); pass 127.0.0.1 to keep them loopback-only. Gitea also defaults to \"0.0.0.0\" — a Git host is pushed to from other machines, and its generated admin login plus closed self-registration are the gate. Prometheus and Alertmanager ALSO default to 0.0.0.0 but have NO auth at all — Alertmanager worse: anyone reaching its port can create silences (mute on-call). Both are guarded only by the network boundary, so pass 127.0.0.1 to keep them loopback-only. BeeGFS is a different shape again: its daemons always bind the fixed protocol ports on the host network, so --listen does NOT gate anything there — it only picks the address the cluster self-references and clients dial for mgmtd (sysMgmtdHost), defaulting to \"0.0.0.0\" because a parallel filesystem is mounted from other machines")
	addonInstallCmd.Flags().String("root-user", "", "MinIO/silo/rustfs root user (default \"admin\"; the root password is generated on first install, printed once, and stored in the config). For garage this is the seeded access key: omit it to auto-generate a GK-prefixed key (garage has no \"admin\" user). For seaweedfs it is the s3.json identity access key: omit it to auto-generate an SW-prefixed key on a SINGLE-NODE install only — under --cluster both it and --root-password are REQUIRED, since each node reads only its own s3.json and must present the identical identity")
	addonInstallCmd.Flags().String("root-password", "", "MinIO/silo/rustfs root password (generated on first install if omitted; pass the SAME value on every node of a distributed cluster so all pg.yaml files share one credential without copying it by hand). For garage this is the seeded secret key; for seaweedfs the s3.json identity secret key — required (with --root-user) on every node of a --cluster install")
	addonInstallCmd.Flags().StringSlice("endpoint", nil, "MinIO/silo/rustfs distributed-mode endpoint(s), e.g. --endpoint http://10.0.0.1:9000/data (MinIO/silo: path is the in-container export dir — /data with a plain --data-dir node, or /data1../dataN on a --drive node (MNMD); repeat for every node's every drive. rustfs: one endpoint per node — scheme://host:port, no path; it has no multi-node single-drive mode so --drive is required, and it derives the /data/rustfs0../rustfsN suffix itself. The list AND root credentials must match every node's pg.yaml — enables cluster mode; omit for single-node)")
	addonInstallCmd.Flags().StringSlice("drive", nil, "MinIO/silo/rustfs/seaweedfs/garage multi-drive host dir(s), each on its own disk — e.g. --drive /mnt/minio/disk1 --drive /mnt/minio/disk2 ... (repeat for each drive; for MinIO/silo alone this is single-node multi-drive (SNMD), combined with --endpoint it is one node of a multi-node multi-drive cluster (MNMD), and each endpoint addresses this node's drive slots (/data1../dataN for MinIO/silo, /data/rustfs0../rustfsN for rustfs). For seaweedfs it turns one node's volume server into a multi-disk one: each drive is bind-mounted at /data1../dataN and weed's -dir lists them all comma-joined (volumes spread across the disks, no erasure-coding between them); works on a single node and per-node in a --cluster. For garage it feeds the rendered garage.toml's data_dir array (Garage spreads blocks across the dirs, balancing by per-dir capacity; metadata_dir stays a single dir); works single-node and per-node in a --cluster. Mutually exclusive with --data-dir. MinIO/silo reject a drive sharing the root device; rustfs hard-requires every drive on its own physical device; seaweedfs and garage only warn)")
	addonInstallCmd.Flags().Bool("tls", false, "MinIO/silo/rustfs/seaweedfs: serve HTTPS via pgcli's self-signed CA (certs generated under <base_dir>/tls/<minio|silo|rustfs>/<name>/, or the addon dir for seaweedfs; hand ca.crt to pgBackRest as backup.repo.s3.ca_file). Required for a store used as a pgBackRest S3 repo — pgBackRest refuses plaintext HTTP. Seaweedfs serves TLS natively: the S3 port becomes HTTPS-only (cleartext listener off). Changing this needs --force to recreate. NOT for garage: it has no native TLS (rejects --tls); front it with the nginx addon")
	addonInstallCmd.Flags().String("tls-cert", "", "MinIO/silo/rustfs: serve HTTPS with THIS certificate file instead of the generated self-signed one (PEM leaf + any intermediate chain; mounted read-only as public.crt for MinIO/silo, rustfs_cert.pem for rustfs, cert.pem for seaweedfs). Implies --tls. Renew by replacing the file then --force to recreate (a single-file mount pins the source inode). A public-CA cert needs no --s3-ca-file on clients; a private-CA one passes its chain/CA there")
	addonInstallCmd.Flags().String("tls-key", "", "MinIO/silo/rustfs: private key for --tls-cert (PEM; mounted read-only as private.key for MinIO/silo, rustfs_key.pem for rustfs, key.pem for seaweedfs). Must pair with the cert; both are required to enable BYO TLS")
	addonInstallCmd.Flags().Bool("force", false, "recreate the MinIO/silo/rustfs/garage/seaweedfs/redis/predixy/PostgREST/pgAdmin/Grafana/Prometheus/Alertmanager/Gitea container (or, for BeeGFS, the whole 3-container filesystem cluster) even if one already exists (to apply changed ports/listen/credentials, a changed PostgREST --dsn/--db-pool/--schema or Predixy --backend, a changed pgAdmin --email/--dsn/--pg-name, Grafana --admin-user or Gitea --admin-user/--ssh-port/--dsn seed/--allow-registration, or to force-recreate a Prometheus instance instead of hot-reloading its config). For juicefs it rebuilds the config entry from the flags (format is still skipped when the volume already exists in the metadata engine); for zerofs it re-renders config.toml and restarts the server/client, KEEPING the stored encryption password (a fresh one could not unwrap the bucket's existing data); for duckdb it rebuilds the config entry from the flags")

	// redis flags (top-level standalone KV store — the first version-selectable addon)
	addonInstallCmd.Flags().String("version", "", "Redis major version to install: 7 or 8 (default \"8\"); each maps to a pinned upstream docker.io/library/redis tag. For juicefs/zerofs/duckdb it instead pins the host binary release to download (default: the version's pinned sha256). Ignored for every other addon")
	addonInstallCmd.Flags().String("password", "", "Redis requirepass password (generated on first install if omitted; pass it explicitly to pin the value across reinstalls) / Predixy proxy password (REQUIRED, never generated: it must be the proxied cluster's requirepass — read it with `pg addon password redis --name <member>`) / pgAdmin, Grafana or Gitea WEB login password (generated on first install if omitted — not a PostgreSQL password; retrieve it with `pg addon password pgadmin`/`grafana`/`gitea`. For Grafana it only takes effect while grafana.db is absent; for Gitea the stored password is the one pgcli CREATES the admin account with, so it is inert on a revived store whose admin already exists) / BeeGFS connection-auth secret (generated on first install ONLY on a host running mgmtd; REQUIRED on a cross-host join (--roles without mgmtd) and must equal the cluster's value — retrieve it with `pg addon password beegfs` or `pg addon beegfs join-hint`)")
	addonInstallCmd.Flags().String("admin-user", "", "Grafana WEB login user (GF_SECURITY_ADMIN_USER; default \"admin\") / Gitea WEB login user (created via `gitea admin user create`; default \"gitea\" — deliberately NOT the container's OS user \"git\") — the account you sign into the web UI with, unrelated to any PostgreSQL role")
	addonInstallCmd.Flags().Bool("allow-registration", false, "Gitea: open self-service signup (GITEA__service__DISABLE_REGISTRATION=false; the web UI shows a Register entry). Closed by default — it is one of the two guards on the default 0.0.0.0 listen. Signups always create NORMAL users: Gitea offers no way for the register form to produce an admin, so this opens account spam, not takeover. The toggle needs --force to reach the live container; close it again with --allow-registration=false --force")
	addonInstallCmd.Flags().String("email", "", "pgAdmin WEB login email (PGADMIN_DEFAULT_EMAIL; default \"admin@pgcli.lan\") — the account you sign into the web UI with, unrelated to any PostgreSQL role; the image rejects reserved TLDs such as .local")
	addonInstallCmd.Flags().String("maxmemory", "", "Redis memory cap, e.g. 256mb or 2gb — turns the instance into a real cache (allkeys-lru evicts keys at the cap); empty means no cap")
	addonInstallCmd.Flags().String("maxmemory-policy", "", "Redis eviction policy to pair with --maxmemory (noeviction | allkeys-lru | allkeys-lfu | volatile-lru | volatile-lfu | volatile-ttl; default allkeys-lru; requires --maxmemory)")
	addonInstallCmd.Flags().Bool("aof", false, "Redis: enable AOF (appendonly yes) — a write-ahead log on top of the RDB snapshot, for crash-safe no-eviction stores; off by default")
	addonInstallCmd.Flags().String("appendfsync", "", "Redis AOF flush strength: always (durability) | everysec (default) | no (throughput); requires --aof")
	addonInstallCmd.Flags().String("save", "", `Redis RDB snapshot schedule, e.g. "900 1 300 10" (empty keeps Redis's own default; "no" disables snapshots, e.g. for an AOF-only instance)`)
	addonInstallCmd.Flags().String("replica-of", "", "Redis: make this instance a read-only replica of the named local master addon (its host/port/password are resolved from the config; the master must already be installed and on the same major)")
	addonInstallCmd.Flags().String("replica-of-host", "", "Redis: address of a remote master to replicate (cross-host; pair with --replica-of-port and --password matching the master's; mutually exclusive with --replica-of)")
	addonInstallCmd.Flags().Int("replica-of-port", 0, "Redis: remote master's port, paired with --replica-of-host")

	// start / stop flags
	addonStartCmd.Flags().String("name", "", "name of the etcd member, pgdog proxy, haproxy, minio, silo, rustfs, garage, seaweedfs, redis, predixy, pgadmin, grafana, prometheus, alertmanager, gitea or beegfs instance to start (default \"etcd\"/\"pgdog\"/\"haproxy\"/\"minio\"/\"silo\"/\"rustfs\"/\"garage\"/\"seaweedfs\"/\"redis\"/\"predixy\"/\"pgadmin\"/\"grafana\"/\"prometheus\"/\"alertmanager\"/\"gitea\"/\"beegfs\")")
	addonStartCmd.Flags().String("pg-name", "", "name of a remote PgBouncer or PostgREST to start")
	addonStopCmd.Flags().String("name", "", "name of the etcd member, pgdog proxy, haproxy, minio, silo, rustfs, garage, seaweedfs, redis, predixy, pgadmin, grafana, prometheus, alertmanager, gitea or beegfs instance to stop (default \"etcd\"/\"pgdog\"/\"haproxy\"/\"minio\"/\"silo\"/\"rustfs\"/\"garage\"/\"seaweedfs\"/\"redis\"/\"predixy\"/\"pgadmin\"/\"grafana\"/\"prometheus\"/\"alertmanager\"/\"gitea\"/\"beegfs\")")
	addonStopCmd.Flags().String("pg-name", "", "name of a remote PgBouncer or PostgREST to stop")

	// list / password flags
	addonListCmd.Flags().Bool("show-password", false, "also print stored credentials (Redis requirepass, Predixy proxy password, MinIO/silo/rustfs/garage/seaweedfs key secret, pgAdmin/Grafana/Gitea web login password, BeeGFS conn-auth secret) — off by default so the listing stays paste-safe")
	addonPasswordCmd.Flags().String("name", "", "instance to reveal (redis/predixy/minio/silo/rustfs/garage/seaweedfs/pgadmin/grafana/gitea/beegfs; default \"redis\"/\"predixy\"/\"minio\"/\"silo\"/\"rustfs\"/\"garage\"/\"seaweedfs\"/\"pgadmin\"/\"grafana\"/\"gitea\"/\"beegfs\")")
	addonPasswordCmd.Flags().String("file", "", "write the password to this file (mode 0600) instead of stdout, keeping it out of shell history and scrollback")

	// pgdog flags (top-level shared Postgres proxy addon)
	addonInstallCmd.Flags().Int("port", 0, "PgDog client host port (0=auto-assign from pgdog_start_port; openmetrics takes the next free port) / PostgREST HTTP host port (0=auto-assign from postgrest_start_port) / Redis host port (0=auto-assign from redis_start_port, default 6379) / Predixy proxy host port (0=auto-assign from predixy_start_port, default 7617) / pgAdmin web host port (0=auto-assign from pgadmin_start_port, default 5050) / Grafana web host port (0=auto-assign from grafana_start_port, default 3000) / Prometheus web host port (0=auto-assign from prometheus_start_port, default 9090) / Alertmanager web host port (0=auto-assign from alertmanager_start_port, default 9093) / Gitea web host port (0=auto-assign from gitea_start_port, default 3080)")
	addonInstallCmd.Flags().Int("ssh-port", 0, "Gitea: host port for git-over-ssh (the image's OpenSSH) clone/push — any N>0 enables it. The image always runs sshd, so N=0 does not stop it, only parks it on an idle pool port (off 22). With no explicit value the port auto-assigns from gitea_ssh_start_port (default 2222); on Linux that port is bound directly (host networking), so the clone URL shows ssh://git@<listen>:<port>/...")
	addonInstallCmd.Flags().String("host", "", "PgDog listen address (default 127.0.0.1)")
	addonInstallCmd.Flags().String("pool-mode", "", "PgDog pooler mode: transaction (default) or session")
	addonInstallCmd.Flags().Int("workers", 0, "worker threads: PgDog (default 2) / Predixy proxy WorkerThreads (default 1)")
	addonInstallCmd.Flags().StringArray("backend", nil, "PgDog backend database NAME=HOST:PORT:DBNAME[:SHARD[:ROLE]] (repeatable) / Predixy cluster node HOST:PORT (repeatable, or one comma-separated list; the FULL node set of the proxied cluster is required)")
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
	case "garage":
		return runAddonInstallGarage(cmd)
	case "seaweedfs":
		return runAddonInstallSeaweedfs(cmd)
	case "redis":
		return runAddonInstallRedis(cmd)
	case "predixy":
		return runAddonInstallPredixy(cmd)
	case "postgrest":
		return runAddonInstallPostgrest(cmd)
	case "pgadmin":
		return runAddonInstallPgAdmin(cmd)
	case "grafana":
		return runAddonInstallGrafana(cmd)
	case "prometheus":
		return runAddonInstallPrometheus(cmd)
	case "victoriametrics":
		return runAddonInstallVictoriaMetrics(cmd)
	case "vmcluster":
		return runAddonInstallVMCluster(cmd)
	case "alertmanager":
		return runAddonInstallAlertmanager(cmd)
	case "gitea":
		return runAddonInstallGitea(cmd)
	case "beegfs":
		return runAddonInstallBeeGFS(cmd)
	case "juicefs":
		return runAddonInstallJuicefs(cmd)
	case "zerofs":
		return runAddonInstallZerofs(cmd)
	case "nginx":
		return runAddonInstallNginx(cmd)
	case "duckdb":
		return runAddonInstallDuckdb(cmd)
	case "pgbouncer":
		// falls through to the PgBouncer flow below
	default:
		return fmt.Errorf("unknown addon: %s (available: pgbouncer, etcd, pgdog, haproxy, minio, silo, rustfs, garage, seaweedfs, redis, predixy, postgrest, pgadmin, grafana, prometheus, victoriametrics, vmcluster, alertmanager, gitea, beegfs, juicefs, zerofs, nginx, duckdb)", addonName)
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
//
// With --conf-file the addon instead runs the operator's haproxy.cfg verbatim,
// for arbitrary native proxying (see the nginx addon's --conf-file mode). The
// file is validated with `haproxy -c` before the container is (re)created.
func runAddonInstallHAProxy(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "haproxy"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	mode, _ := cmd.Flags().GetString("mode")
	haScope, _ := cmd.Flags().GetString("ha")
	nodeSpecs, _ := cmd.Flags().GetStringArray("node")
	confFile, _ := cmd.Flags().GetString("conf-file")
	rwPort, _ := cmd.Flags().GetInt("rw-port")
	roPort, _ := cmd.Flags().GetInt("ro-port")
	statsPort, _ := cmd.Flags().GetInt("stats-port")
	maxLag, _ := cmd.Flags().GetString("max-lag")
	doReload, _ := cmd.Flags().GetBool("reload")

	if confFile != "" {
		if len(nodeSpecs) > 0 || haScope != "" {
			return fmt.Errorf("--conf-file is mutually exclusive with --node/--ha: the file defines the whole config, pgcli derives no backends")
		}
		info, err := os.Stat(confFile)
		if os.IsNotExist(err) {
			return fmt.Errorf("--conf-file: %s not found", confFile)
		}
		if err != nil {
			return fmt.Errorf("--conf-file: %w", err)
		}
		if info.Size() == 0 {
			return fmt.Errorf("--conf-file: %s is empty", confFile)
		}
	}

	if mode == "" {
		mode = "unified"
	}
	if mode != "unified" && mode != "split" {
		return fmt.Errorf("--mode must be \"unified\" (read-write through the leader) or \"split\" (separate read listener), got %q", mode)
	}
	if confFile == "" {
		if len(nodeSpecs) == 0 && haScope == "" {
			return fmt.Errorf("either --node NAME=HOST:PGPORT:RESTPORT (repeatable) or --ha <scope> is required to define the backend members (or --conf-file for a custom haproxy.cfg)")
		}
		if len(nodeSpecs) > 0 && haScope != "" {
			return fmt.Errorf("--node and --ha are mutually exclusive: --ha auto-derives every local member of the scope, --node lists them explicitly")
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

	targets := make([]config.HAProxyTarget, 0, len(nodeSpecs))
	if confFile == "" {
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
	// Conf-file mode is likewise set or cleared wholesale: passing --conf-file
	// switches an existing rendered instance to it (targets dropped), omitting
	// it switches back.
	existing.Targets = targets
	existing.ConfFile = confFile
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

	cfgPathOut := hm.ConfigPath(&hc)

	// Snapshot the on-disk copy before overwriting it: if the new config fails
	// `haproxy -c` below, restore it, so a stopped/rebooted container never
	// recreates from a config that was rejected at install.
	oldCfg, hadOld := func() ([]byte, bool) {
		if b, err := os.ReadFile(cfgPathOut); err == nil {
			return b, true
		}
		return nil, false
	}()

	if _, err := hm.WriteConfigs(&hc); err != nil {
		return err
	}

	// Check-only gate (`haproxy -c` in a throwaway container): the config that
	// is now on disk is what the container will start with, so an invalid one —
	// almost always the operator's, in conf-file mode — fails here instead of
	// crash-looping a detached container behind --restart unless-stopped. On
	// failure the previous good copy is put back.
	fmt.Println("-> Validating HAProxy config...")
	if err := hm.ValidateConfig(&hc, cfgPathOut); err != nil {
		if hadOld {
			if rbErr := os.WriteFile(cfgPathOut, oldCfg, 0644); rbErr != nil {
				fmt.Printf("  [!] could not restore previous haproxy.cfg (%v) — the container may be left with a broken config on disk\n", rbErr)
			} else {
				fmt.Println("  [!] restored the previous haproxy.cfg; the running container (if any) is untouched")
			}
		}
		return err
	}

	// --reload is conf-file-only and meaningful only for an already-running
	// instance: rather than recreating the container (which cuts in-flight
	// connections), signal the master to hot-swap the worker from the config
	// that WriteConfigs + ValidateConfig just put on disk.
	if doReload && hc.ConfFile != "" {
		running, _ := hm.ContainerRunning(hc.ContainerName)
		if running {
			cfg.Addons.HAProxy[name] = hc
			if err := cfg.Save(path); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}
			if err := hm.SignalReload(&hc); err != nil {
				return err
			}
			fmt.Println()
			fmt.Printf("✓ haproxy reloaded: %q\n", name)
			fmt.Printf("  Container:    %s (unchanged)\n", hc.ContainerName)
			fmt.Printf("  Mode:         custom (user config)\n")
			fmt.Printf("  Config:       %s (from %s)\n", cfgPathOut, hc.ConfFile)
			fmt.Println()
			fmt.Printf("  Applied without recreating the container — no dropped connections.\n")
			fmt.Printf("  'pg logs %s' shows HAProxy's own output.\n", name)
			return nil
		}
		fmt.Println("  [!] --reload requested but the instance is not running; creating it normally")
	}
	if doReload && hc.ConfFile == "" {
		fmt.Println("  [!] --reload is only meaningful for --conf-file instances; a rendered instance is recreated")
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
	if hc.ConfFile != "" {
		fmt.Printf("  Mode:         custom (user config — all listeners come from the file)\n")
		fmt.Printf("  Config:       %s (from %s)\n", cfgPathOut, hc.ConfFile)
		fmt.Println()
		fmt.Printf("  pgcli manages the container only; to change the proxies edit %s\n", hc.ConfFile)
		fmt.Printf("  and hot-reload it with 'pg addon haproxy reload --name %s' (validates\n", name)
		fmt.Printf("  with `haproxy -c` first, then reloads without dropping connections).\n")
		fmt.Printf("  'pg logs %s' shows HAProxy's own output, including the bound frontends.\n", name)
		return nil
	}
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

// parsePredixyBackends normalises the --backend values into a deduplicated
// host:port list. Each value is either one host:port or a comma-separated run of
// them, so `--backend a:1,b:2 --backend c:3` and `--backend a:1,b:2,c:3` agree.
// net.SplitHostPort does the shape check (it also accepts [v6]:port), which
// leaves only "port is numeric and in range" to verify here; the host part is
// not resolved, so a name that does not exist is Predixy's problem at connect
// time, not a reason to refuse the install.
func parsePredixyBackends(specs []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, spec := range specs {
		for _, item := range strings.Split(spec, ",") {
			entry := strings.TrimSpace(item)
			if entry == "" {
				return nil, fmt.Errorf("--backend %q has an empty entry", spec)
			}
			host, portStr, err := net.SplitHostPort(entry)
			if err != nil {
				return nil, fmt.Errorf("--backend %q is not host:port (e.g. 127.0.0.1:6379): %w", entry, err)
			}
			if host == "" {
				return nil, fmt.Errorf("--backend %q has an empty host", entry)
			}
			port, err := strconv.Atoi(portStr)
			if err != nil || port < 1 || port > 65535 {
				return nil, fmt.Errorf("--backend %q has an invalid port %q", entry, portStr)
			}
			if seen[entry] {
				return nil, fmt.Errorf("--backend %q is listed twice — the proxy dials each node once", entry)
			}
			seen[entry] = true
			out = append(out, entry)
		}
	}
	return out, nil
}

// validatePredixyConfig checks the merged config (stored values count too, like
// validateRedisKnobs) so a hand-edited pg.yaml fails at the next install rather
// than at Predixy parse time.
func validatePredixyConfig(pc config.PredixyConfig) error {
	if len(pc.Backend) == 0 {
		return fmt.Errorf("predixy %q has no backend nodes — pass --backend host:port[,host:port...]", pc.Name)
	}
	if pc.Password == "" {
		return fmt.Errorf("predixy %q has no password — pass --password (the proxied cluster's requirepass)", pc.Name)
	}
	// Predixy reads these two values as quoted strings, and its parser ends the
	// quote on either of these characters — no escaping exists. The generated
	// redis password alphabet can produce neither; only an explicit --password
	// can, so refuse it here rather than write a config the proxy mis-parses.
	if strings.ContainsAny(pc.Password, "\"\n") {
		return fmt.Errorf("predixy %q password must not contain a double quote or a newline (Predixy's quoted-string syntax has no escape for them)", pc.Name)
	}
	if pc.Workers < 0 {
		return fmt.Errorf("predixy %q workers must be >= 1, got %d", pc.Name, pc.Workers)
	}
	return nil
}

// runAddonInstallPredixy installs a Predixy proxy container in front of a native
// redis cluster. Unlike haproxy — whose backends it can derive from a local
// Patroni scope — predixy takes its backend list verbatim from --backend: the
// full node set of a cluster that may well live on other hosts, so there is
// nothing reliable to derive. The password is likewise required rather than
// generated: it must be the proxied cluster's own requirepass, which
// `pg addon password redis --name <m>` hands back.
func runAddonInstallPredixy(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "predixy"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	listenAddr, _ := cmd.Flags().GetString("listen")
	port, _ := cmd.Flags().GetInt("port")
	workers, _ := cmd.Flags().GetInt("workers")
	password, _ := cmd.Flags().GetString("password")
	backendSpecs, _ := cmd.Flags().GetStringArray("backend")
	force, _ := cmd.Flags().GetBool("force")

	backends, err := parsePredixyBackends(backendSpecs)
	if err != nil {
		return err
	}
	if len(backends) == 0 {
		return fmt.Errorf("--backend host:port[,host:port...] is required: list every node of the cluster being proxied")
	}
	if password == "" {
		return fmt.Errorf("--password is required: Predixy authenticates both its own clients and the proxied cluster with it, so it must be the cluster's requirepass (read it back with `pg addon password redis --name <member>`)")
	}
	if strings.ContainsAny(password, "\"\n") {
		return fmt.Errorf("--password must not contain a double quote or a newline (Predixy's quoted-string syntax has no escape for them)")
	}
	if workers < 0 {
		return fmt.Errorf("--workers must be >= 1, got %d", workers)
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

	if cfg.Addons.Predixy == nil {
		cfg.Addons.Predixy = make(map[string]config.PredixyConfig)
	}
	existing, ok := cfg.Addons.Predixy[name]
	if !ok {
		existing = config.PredixyConfig{
			ContainerName: "pgcli-predixy" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if port != 0 {
		existing.Port = port
	}
	if workers != 0 {
		existing.Workers = workers
	}
	if password != "" {
		existing.Password = password
	}
	// The backend list is fully replaced by this command's input, so re-running
	// install re-syncs it — the way a cluster grows (add nodes, `--cluster
	// add-node`) is by installing more members, then re-listing them here.
	existing.Backend = backends
	if existing.Name == "" {
		existing.Name = name
	}
	if err := validatePredixyConfig(existing); err != nil {
		return err
	}
	cfg.Addons.Predixy[name] = existing

	// ApplyDefaults fills ContainerName/ImageTag/Listen/Workers and assigns the
	// port from the predixy_start_pool. Like every other secret in pgcli,
	// Password is never touched here — the install path owns it.
	cfg.ApplyDefaults()
	pc := cfg.Addons.Predixy[name]

	pm, err := podman.NewPredixyManager(cfg)
	if err != nil {
		return fmt.Errorf("predixy manager: %w", err)
	}

	// Reuse semantics as the other addons: a live container is left alone, a
	// stopped one is started, only --force recreates it so a changed
	// port/listen/password/backend set takes effect.
	exists, err := pm.ContainerExists(pc.ContainerName)
	if err != nil {
		return err
	}
	recreated := true
	cfgPathOut, err := pm.WriteConfigs(&pc)
	if err != nil {
		return err
	}
	if exists && !force {
		recreated = false
		if running, _ := pm.ContainerRunning(pc.ContainerName); running {
			fmt.Printf("-> predixy container %q already running; config rewritten, pass --force to recreate it\n", pc.ContainerName)
		} else {
			fmt.Printf("-> predixy container %q exists but is stopped; starting it\n", pc.ContainerName)
			if err := pm.StartContainer(&pc); err != nil {
				return err
			}
		}
	} else {
		fmt.Printf("-> Preparing predixy image %s...\n", pc.ImageTag)
		if err := pm.EnsureImage(pc.ImageTag); err != nil {
			return err
		}
		fmt.Println("-> Starting Predixy container...")
		if err := pm.EnsureContainer(&pc); err != nil {
			return err
		}
	}

	cfg.Addons.Predixy[name] = pc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	if recreated {
		fmt.Printf("✓ predixy installed: %q\n", name)
	} else {
		fmt.Printf("✓ predixy already present: %q\n", name)
	}
	fmt.Printf("  Container:    %s\n", pc.ContainerName)
	fmt.Printf("  Image:        %s\n", pc.ImageTag)
	fmt.Printf("  Listen:       %s:%d\n", pc.Listen, pc.Port)
	fmt.Printf("  Workers:      %d\n", pc.Workers)
	fmt.Printf("  Backends:     %d (%s)\n", len(pc.Backend), strings.Join(pc.Backend, ", "))
	fmt.Printf("  Config:       %s\n", cfgPathOut)
	fmt.Println()
	fmt.Printf("  Password:     %s\n", pc.Password)
	fmt.Printf("  Raw DSN:      redis://:%s@%s:%d/0\n", pc.Password, pc.Listen, pc.Port)
	if pc.Listen == "0.0.0.0" {
		fmt.Println("  NOTE: listening on every interface; the password above is the only gate.")
		fmt.Printf("        loopback-only: pg addon install predixy --name %s --listen 127.0.0.1 --force\n", name)
	}
	fmt.Println()
	fmt.Printf("  Client:       redis-cli -h %s -p %d -a '<password>' ping\n", pc.Listen, pc.Port)
	fmt.Println("  The proxy absorbs MOVED: an ordinary redis client — no -c, no")
	fmt.Println("  redirect handling — reads and writes the whole cluster key space")
	fmt.Println("  through this one endpoint.")
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
	// Cluster and replica are mutually exclusive; resolveRedisCluster enforces
	// it for the flag path, this catches a hand-edited pg.yaml that sets both.
	if rc.Cluster != "" && rc.ReplicaHost != "" {
		return fmt.Errorf("an instance cannot be both a native-cluster member (cluster %q) and a read replica of %s:%d — pick one role", rc.Cluster, rc.ReplicaHost, rc.ReplicaPort)
	}
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
// summary and addon list: a plain master, a read replica naming the master
// endpoint it syncs from (the stored ReplicaHost is the resolved target, so
// what is displayed is what the argv carries), or a native-cluster member
// naming its group and the peer-visible endpoint peers reach it at.
func redisRoleSummary(rc config.RedisConfig) string {
	if rc.ClusterEnabled() {
		return fmt.Sprintf("cluster member of %q (%s:%d)", rc.Cluster, rc.PeerAddr(), rc.Port)
	}
	if rc.ReplicaHost != "" {
		return fmt.Sprintf("replica of %s:%d (read-only)", rc.ReplicaHost, rc.ReplicaPort)
	}
	return "master"
}

// resolveRedisCluster is the cluster analogue of resolveRedisReplica, run at
// install before validation. A native cluster is assembled ONCE by the operator
// (`pg redis-cli -- --cluster create ...`), so pgcli's only jobs here are: mark
// this instance a member of the --cluster group, pin its peer-visible address
// (--advertise-host → --cluster-announce-ip), and make every member of one group
// share ONE password.
//
// The shared password is load-bearing, not cosmetic: redis-cli --cluster
// authenticates to every node in its operand list with a single -a/--password
// (the dry-check on VM01 confirmed --cluster ignores -h/-p and honours
// REDISCLI_AUTH alone). So after the first member of a group is installed,
// subsequent --cluster installs inherit that member's stored password; an
// explicit --password that disagrees is rejected up front rather than surfacing
// as a per-node auth failure inside --cluster create.
//
// cluster="" is a no-op: it preserves any stored Cluster/AdvertiseHost across a
// plain reinstall (same as the replica path leaves a stored role alone).
//
// --cluster-replicas N is only the operator's intent echoed back by the summary
// (it never reaches redis-server argv — Redis assigns master vs follower at
// --cluster create). Like the password it is a GROUP property: the first member
// sets it, later members inherit it, and an explicit disagreement is rejected so
// the copy-pasteable create command and the node-count guidance stay consistent.
func resolveRedisCluster(cfg *config.Config, rc *config.RedisConfig, selfName, cluster, advertiseHost, replicaOf, replicaOfHost string, replicas int, replicasGiven, passwordGiven bool) error {
	if cluster == "" {
		return nil
	}
	if rc.ReplicaHost != "" || replicaOf != "" || replicaOfHost != "" {
		return fmt.Errorf("--cluster (native-cluster member) and --replica-of/--replica-of-host (read replica) are mutually exclusive — an instance is either a cluster member or a replica of a master, never both")
	}
	if replicas < 0 {
		return fmt.Errorf("--cluster-replicas must be >= 0 (0 = masters-only), got %d", replicas)
	}
	if replicas > config.RedisClusterMaxReplicas {
		return fmt.Errorf("--cluster-replicas %d is above the sanity cap of %d — this wants very many nodes and is almost certainly a typo", replicas, config.RedisClusterMaxReplicas)
	}
	rc.Cluster = cluster
	if advertiseHost != "" {
		rc.AdvertiseHost = advertiseHost
	}
	// Base password = the first OTHER configured member of this group, by name
	// (deterministic; matches how the group's members were installed in order).
	var base *config.RedisConfig
	for _, name := range sortedAddonNames(cfg.Addons.Redis) {
		if name == selfName {
			continue
		}
		other := cfg.Addons.Redis[name]
		if other.Cluster == cluster {
			base = &other
			break
		}
	}
	if base == nil {
		// First member of the group: leave Password alone (empty → the install
		// path generates it; an explicit --password is honoured as the group's
		// shared value that later members inherit). The replicas hint likewise
		// starts here — this member's --cluster-replicas becomes the group's.
		if replicasGiven {
			rc.ClusterReplicas = replicas
		}
		return nil
	}
	if passwordGiven && rc.Password != "" && rc.Password != base.Password {
		return fmt.Errorf("cluster member %q must share the group's password (set by cluster %q's first member) — omit --password to inherit it", selfName, cluster)
	}
	rc.Password = base.Password
	if replicasGiven && replicas != base.ClusterReplicas {
		return fmt.Errorf("cluster member %q must share the group's --cluster-replicas (set to %d by cluster %q) — omit it to inherit, or assemble a different cluster", selfName, base.ClusterReplicas, cluster)
	}
	rc.ClusterReplicas = base.ClusterReplicas
	return nil
}

// redisClusterPeerAddrs returns the "addr:port" operands for every configured
// member of self's --cluster group (self included), ordered by name and
// dialled at each member's PeerAddr (--advertise-host, else 127.0.0.1). This is
// exactly the node list `--cluster create` takes, so the summary hands the
// operator a copy-pasteable command instead of a description.
func redisClusterPeerAddrs(cfg *config.Config, self config.RedisConfig) []string {
	var out []string
	for _, name := range sortedAddonNames(cfg.Addons.Redis) {
		m := cfg.Addons.Redis[name]
		if m.Cluster == "" || m.Cluster != self.Cluster {
			continue
		}
		out = append(out, fmt.Sprintf("%s:%d", m.PeerAddr(), m.Port))
	}
	return out
}

// redisClusterNextSteps builds the install-summary guidance for a
// native-cluster member. pgcli installs cluster-enabled nodes but never
// assembles them, so this returns the ONE command the operator runs to form the
// cluster — once enough nodes are configured — or, below that, how many more
// members to install first. The count is in NODES, not masters: a --cluster-replicas
// N group needs RedisClusterMinMasters*(1+N) nodes (each master plus its
// followers), and which member ends up a follower is Redis's decision at
// --cluster create, so pgcli only echoes the operator's replica intent back into
// the suggested command. Keeping the assembled command out of pgcli (no
// automatic --cluster create) is the deliberate two-stage design.
func redisClusterNextSteps(cfg *config.Config, selfName string, self config.RedisConfig) string {
	peers := redisClusterPeerAddrs(cfg, self)
	operands := strings.Join(peers, " ")
	minNodes := config.RedisClusterMinNodes(self.ClusterReplicas)
	out := "\n"
	// masters-only reads naturally as "N masters configured"; with followers it
	// would mislabel every node as a master, so switch to a node count there.
	noun := "masters"
	if self.ClusterReplicas > 0 {
		noun = "nodes"
	}
	if len(peers) < minNodes {
		need := minNodes - len(peers)
		out += fmt.Sprintf("  Cluster:     %q — %d/%d %s configured. Install %d more with --cluster %s, then:\n",
			self.Cluster, len(peers), minNodes, noun, need, self.Cluster)
	} else {
		out += fmt.Sprintf("  Cluster:     %q — %d %s configured. Assemble once (pgcli does not run this):\n",
			self.Cluster, len(peers), noun)
	}
	out += fmt.Sprintf("               pg redis-cli --name %s -- --cluster create %s --cluster-replicas %d\n",
		selfName, operands, self.ClusterReplicas)
	out += "               (bus port = client+10000; for cross-host peers open it on the firewall, and give each member --advertise-host)\n"
	return out
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
	clusterToken, _ := cmd.Flags().GetString("cluster")
	clusterReplicas, _ := cmd.Flags().GetInt("cluster-replicas")
	advertiseHost, _ := cmd.Flags().GetString("advertise-host")
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
	replicasGiven := cmd.Flags().Changed("cluster-replicas")
	// Cluster membership first: it decides the shared password (inherited from
	// the group's first member) and owns the cluster↔replica exclusion, so a
	// `--cluster ... --replica-of ...` collision fails before the replica path.
	if err := resolveRedisCluster(cfg, &existing, name, clusterToken, advertiseHost, replicaOf, replicaOfHost, clusterReplicas, replicasGiven, passwordGiven); err != nil {
		return err
	}
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
		if rc.ClusterEnabled() {
			fmt.Printf("-> NOTE: native-cluster member of %q — it comes up cluster-enabled but INCOMPLETE until you assemble the group once (see the \"Cluster:\" line below). pgcli does not run --cluster create for you.\n", rc.Cluster)
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
	if rc.ClusterEnabled() {
		fmt.Print(redisClusterNextSteps(cfg, name, rc))
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

// runAddonInstallPgAdmin installs a pgAdmin 4 container — the official web
// administration UI for PostgreSQL, run from the upstream dpage/pgadmin4 image.
// It is a top-level-only addon (addons.pgadmin.<name>, key defaults to "pgadmin")
// unlike PostgREST's dual local/remote mode: which servers pgAdmin fronts is
// the UI's own concern, not a per-instance attachment. --dsn/--pg-name are just
// a one-time convenience to pre-seed servers.json so the browser opens with
// that server already listed; they never gate anything at runtime.
//
// Email/Password are pgAdmin's WEB LOGIN credentials — not a PostgreSQL
// password — and are required at first launch (the container refuses to
// initialize without them). Password is generated here when empty, the same way
// redis/minio generate theirs: never in ApplyDefaults, which runs on every
// config load and must stay deterministic.
func runAddonInstallPgAdmin(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "pgadmin"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	port, _ := cmd.Flags().GetInt("port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	email, _ := cmd.Flags().GetString("email")
	password, _ := cmd.Flags().GetString("password")
	dsn, _ := cmd.Flags().GetString("dsn")
	pgName, _ := cmd.Flags().GetString("pg-name")
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

	// --dsn and --pg-name both mean "seed this server" and are mutually
	// exclusive here (unlike pgbouncer/postgrest, where --pg-name is the map key
	// and pairs with --dsn): --dsn supplies the URI directly, --pg-name resolves
	// it from an instance pgcli already manages. Neither is required.
	if dsn != "" && pgName != "" {
		return fmt.Errorf("--dsn and --pg-name are mutually exclusive for pgadmin (--pg-name resolves the DSN itself from a local instance)")
	}
	if pgName != "" {
		if _, ok := cfg.Instances[pgName]; !ok {
			return fmt.Errorf("instance %q not found in config", pgName)
		}
		if err := cfg.SetInstance(pgName); err != nil {
			return fmt.Errorf("resolving instance %q: %w", pgName, err)
		}
		dsn = cfg.GetPostgresURL()
	}

	if cfg.Addons.PgAdmin == nil {
		cfg.Addons.PgAdmin = make(map[string]config.PgAdminConfig)
	}
	existing, ok := cfg.Addons.PgAdmin[name]
	if !ok {
		existing = config.PgAdminConfig{
			ContainerName: "pgcli-pgadmin" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if dataDir != "" {
		existing.DataDir = dataDir
	}
	if cmd.Flags().Changed("port") {
		existing.HostPort = port
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if email != "" {
		existing.Email = email
	}
	if password != "" {
		existing.Password = password
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	if dsn != "" {
		existing.DSN = dsn
		if pgName != "" {
			existing.ServerName = pgName
		}
	}
	cfg.Addons.PgAdmin[name] = existing

	// ApplyDefaults fills ContainerName/Listen/Email and assigns HostPort from
	// the pgadmin_start_port pool. Password is NOT managed there — it is a
	// secret the install path owns, and the entrypoint requires it at first
	// launch (an already-initialised data dir ignores it, so the generated value
	// stays a real login only for a fresh install).
	cfg.ApplyDefaults()
	ac := cfg.Addons.PgAdmin[name]

	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	pgm, err := podman.NewPgAdminManager(cfg)
	if err != nil {
		return fmt.Errorf("pgAdmin manager: %w", err)
	}

	// Reuse semantics identical to redis/the object stores: a live container is
	// left alone (reinstall is a no-op), a stopped one is started, and only
	// --force recreates it so changed port/listen/email take effect.
	exists, err := pgm.ContainerExists(ac.ContainerName)
	if err != nil {
		return err
	}
	skipped := false
	if exists && !force {
		skipped = true
		if running, _ := pgm.ContainerRunning(ac.ContainerName); running {
			fmt.Printf("-> pgAdmin container %q already running; skipping creation\n", ac.ContainerName)
		} else {
			fmt.Printf("-> pgAdmin container %q exists but is stopped; starting it\n", ac.ContainerName)
			if err := pgm.StartContainer(&ac); err != nil {
				return err
			}
		}
	} else {
		// Fresh install, or a reinstall after a `remove` that kept the data dir.
		// Generate the login password when none is stored. NOTE: pgAdmin only
		// honours PGADMIN_DEFAULT_PASSWORD while initialising an EMPTY data dir —
		// a revived pgadmin4.db keeps whatever account it was first created with,
		// so a freshly generated password here would NOT be the working login. We
		// detect that case below and say so rather than print a password that
		// silently does nothing.
		if ac.Password == "" {
			pw, err := generatePassword(20)
			if err != nil {
				return fmt.Errorf("generating pgAdmin password: %w", err)
			}
			ac.Password = pw
		}
		revived := pgAdminStoreExists(pgm.DataDir(&ac))
		fmt.Printf("-> Preparing pgAdmin image %s...\n", ac.ImageTag)
		if err := pgm.EnsureImage(ac.ImageTag); err != nil {
			return err
		}
		if dsn != "" {
			src := "from --dsn"
			if pgName != "" {
				src = fmt.Sprintf("from instance %q", pgName)
			}
			if podman.DSNHasPassword(dsn) {
				fmt.Printf("-> NOTE: seeding servers.json %s — the connection password is pre-configured via a pgpass file, so the seeded server connects without prompting.\n", src)
			} else {
				fmt.Printf("-> NOTE: seeding servers.json %s — it carries no password (pgAdmin cannot store one in servers.json), so you will be prompted on first connect.\n", src)
			}
		}
		if revived {
			// Pin the login block to point out the printed password is inert.
			fmt.Println("-> NOTE: reviving an existing pgAdmin data dir — its stored login account still applies; the password below only takes effect on a --clean-data reinstall.")
		}
		fmt.Println("-> Starting pgAdmin container...")
		if err := pgm.EnsureContainer(&ac); err != nil {
			return err
		}
	}

	cfg.Addons.PgAdmin[name] = ac
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	if skipped {
		fmt.Printf("✓ pgAdmin already present: %q\n", name)
	} else {
		fmt.Printf("✓ pgAdmin installed: %q\n", name)
	}
	fmt.Printf("  Container:  %s\n", ac.ContainerName)
	fmt.Printf("  Image:      %s\n", ac.ImageTag)
	fmt.Printf("  Data:       %s\n", pgm.DataDir(&ac))
	fmt.Printf("  URL:        http://%s:%d/\n", ac.Listen, ac.HostPort)
	if ac.DSN != "" {
		fmt.Printf("  Seeded:     %s\n", pgadminSeededDisplay(ac))
		if podman.DSNHasPassword(ac.DSN) {
			fmt.Println("  Seed auth:  password pre-configured (pgpass) — no prompt on first connect")
		} else {
			fmt.Println("  Seed auth:  no password in the DSN — first connect will prompt")
		}
	}
	fmt.Println()
	fmt.Printf("  Login email:    %s\n", ac.Email)
	fmt.Printf("  Login password: %s\n", ac.Password)
	fmt.Println("  (Web login only — not a PostgreSQL password. Retrieve it later with `pg addon password pgadmin`.)")
	fmt.Println()
	fmt.Println("  Add servers to browse in the web UI itself, or reinstall with --dsn/--pg-name to pre-seed one.")
	if ac.Listen == "0.0.0.0" {
		fmt.Println("  NOTE: listening on every interface; the login above is the only gate.")
		fmt.Printf("        loopback-only: pg addon install pgadmin --name %s --listen 127.0.0.1 --force\n", name)
	}
	return nil
}

// pgadminSeededDisplay renders the optional servers.json seed for the summary —
// it shows the display name and host, never a DSN (which would carry a
// password).
func pgadminSeededDisplay(ac config.PgAdminConfig) string {
	name := ac.ServerName
	if name == "" {
		name = "from --dsn"
	}
	return name
}

// pgAdminStoreExists reports whether a data dir already holds pgAdmin's
// config/session DB (pgadmin4.db) — the signal that install is reviving a kept
// store rather than initialising a fresh one. pgAdmin only reads
// PGADMIN_DEFAULT_PASSWORD when that file is absent, so the login password the
// install prints is inert on a revived store.
func pgAdminStoreExists(dataDir string) bool {
	if dataDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dataDir, "pgadmin4.db"))
	return err == nil
}

// runAddonInstallGrafana installs a Grafana container — the open-source
// observability dashboard — from the official docker.io/grafana image. Like
// pgAdmin it is a top-level-only addon (addons.grafana.<name>, key defaults to
// "grafana"): it fronts no single instance, so there is no --dsn/--pg-name seed
// (v1 provisions no datasource or dashboard — configure those in the browser).
//
// AdminUser/Password are Grafana's WEB LOGIN credentials (GF_SECURITY_ADMIN_*),
// required at first launch. Password is generated here when empty, the same way
// pgAdmin/redis generate theirs: never in ApplyDefaults, which runs on every
// config load and must stay deterministic.
func runAddonInstallGrafana(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "grafana"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	port, _ := cmd.Flags().GetInt("port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	adminUser, _ := cmd.Flags().GetString("admin-user")
	password, _ := cmd.Flags().GetString("password")
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

	if cfg.Addons.Grafana == nil {
		cfg.Addons.Grafana = make(map[string]config.GrafanaConfig)
	}
	existing, ok := cfg.Addons.Grafana[name]
	if !ok {
		existing = config.GrafanaConfig{
			ContainerName: "pgcli-grafana" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if dataDir != "" {
		existing.DataDir = dataDir
	}
	if cmd.Flags().Changed("port") {
		existing.HostPort = port
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if adminUser != "" {
		existing.AdminUser = adminUser
	}
	if password != "" {
		existing.Password = password
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	cfg.Addons.Grafana[name] = existing

	// ApplyDefaults fills ContainerName/Listen/AdminUser and assigns HostPort
	// from the grafana_start_port pool. Password is NOT managed there — it is a
	// secret the install path owns, and Grafana requires it at first launch (an
	// already-initialised grafana.db ignores it, so the generated value stays a
	// real login only for a fresh install).
	cfg.ApplyDefaults()
	gc := cfg.Addons.Grafana[name]

	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	gm, err := podman.NewGrafanaManager(cfg)
	if err != nil {
		return fmt.Errorf("Grafana manager: %w", err)
	}

	// Reuse semantics identical to pgAdmin/redis: a live container is left alone
	// (reinstall is a no-op), a stopped one is started, and only --force
	// recreates it so changed port/listen/admin-user take effect.
	exists, err := gm.ContainerExists(gc.ContainerName)
	if err != nil {
		return err
	}
	skipped := false
	if exists && !force {
		skipped = true
		if running, _ := gm.ContainerRunning(gc.ContainerName); running {
			fmt.Printf("-> Grafana container %q already running; skipping creation\n", gc.ContainerName)
		} else {
			fmt.Printf("-> Grafana container %q exists but is stopped; starting it\n", gc.ContainerName)
			if err := gm.StartContainer(&gc); err != nil {
				return err
			}
		}
	} else {
		// Fresh install, or a reinstall after a `remove` that kept the data dir.
		// Generate the login password when none is stored. NOTE: Grafana only
		// honours GF_SECURITY_ADMIN_PASSWORD while grafana.db is ABSENT — a
		// revived store keeps whatever login was set on its first startup, so a
		// freshly generated password here would NOT be the working login. We
		// detect that case below and say so rather than print a password that
		// silently does nothing.
		if gc.Password == "" {
			pw, err := generatePassword(20)
			if err != nil {
				return fmt.Errorf("generating Grafana password: %w", err)
			}
			gc.Password = pw
		}
		revived := podman.GrafanaStoreExists(gm.DataDir(&gc))
		fmt.Printf("-> Preparing Grafana image %s...\n", gc.ImageTag)
		if err := gm.EnsureImage(gc.ImageTag); err != nil {
			return err
		}
		if revived {
			// Pin the login block to point out the printed password is inert.
			fmt.Println("-> NOTE: reviving an existing Grafana data dir — its stored login still applies; the password below only takes effect on a --clean-data reinstall (or reset it inside the container with `grafana-cli admin reset-admin-password`).")
		}
		fmt.Println("-> Starting Grafana container...")
		if err := gm.EnsureContainer(&gc); err != nil {
			return err
		}
	}

	cfg.Addons.Grafana[name] = gc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	if skipped {
		fmt.Printf("✓ Grafana already present: %q\n", name)
	} else {
		fmt.Printf("✓ Grafana installed: %q\n", name)
	}
	fmt.Printf("  Container:  %s\n", gc.ContainerName)
	fmt.Printf("  Image:      %s\n", gc.ImageTag)
	fmt.Printf("  Data:       %s\n", gm.DataDir(&gc))
	fmt.Printf("  URL:        http://%s:%d/\n", gc.Listen, gc.HostPort)
	fmt.Println()
	fmt.Printf("  Login user:     %s\n", gc.AdminUser)
	fmt.Printf("  Login password: %s\n", gc.Password)
	fmt.Println("  (Web login only — not a PostgreSQL password. Retrieve it later with `pg addon password grafana`.)")
	fmt.Println()
	fmt.Println("  Add datasources and dashboards in the web UI itself; v1 provisions none.")
	if gc.Listen == "0.0.0.0" {
		fmt.Println("  NOTE: listening on every interface (the dashboard is meant to be viewed remotely); the login above and closed self-signup are the only gate.")
		fmt.Printf("        loopback-only: pg addon install grafana --name %s --listen 127.0.0.1 --force\n", name)
	}
	return nil
}

// ---------------------------------------------------------------------------
// list logic
// ---------------------------------------------------------------------------

// runAddonList prints every installed add-on. showPassword opts into printing
// the stored credentials (Redis requirepass, the object stores' root
// passwords); the default keeps the listing paste-safe.
func runAddonList(showPassword bool) error {
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
			if hc.ConfFile != "" {
				fmt.Printf("    Mode:        custom (user config)\n")
				fmt.Printf("    Config:      %s\n", hc.ConfFile)
				fmt.Printf("    Image:       %s\n", hc.ImageTag)
				fmt.Printf("    Container:   %s\n", hc.ContainerName)
				continue
			}
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
			if showPassword && mc.RootPassword != "" {
				fmt.Printf("    Root password: %s\n", mc.RootPassword)
			}
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
			if showPassword && sc.RootPassword != "" {
				fmt.Printf("    Root password: %s\n", sc.RootPassword)
			}
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
			if showPassword && rc.RootPassword != "" {
				fmt.Printf("    Root password: %s\n", rc.RootPassword)
			}
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

	// garage (S3-compatible object storage — fourth store addon; single-node
	// or one member of a multi-node cluster)
	fmt.Println()
	fmt.Println("Infra add-ons (garage):")
	hasGarage := false
	if gm, err := podman.NewGarageManager(cfg); err == nil {
		for name, gc := range cfg.Addons.Garage {
			hasGarage = true
			status := "stopped"
			running, err := gm.ContainerRunning(gc.ContainerName)
			if err == nil && running {
				status = "running"
			}
			fmt.Printf("  %s (name: %s)\n", "garage", name)
			fmt.Printf("    Status:      %s\n", status)
			if gc.Clustered() {
				fmt.Printf("    Cluster:     %s (rf %d)\n", gc.Cluster, gc.ReplicationFactor)
				fmt.Printf("    Advertise:   %s\n", gc.RPCPublicAddr())
				fmt.Printf("    Zone/Cap:    %s / %d GiB\n", gc.Zone, gc.Capacity)
				if gc.NodeID != "" {
					fmt.Printf("    Node ID:     %s\n", gc.NodeID)
				}
				if len(gc.Peers) > 0 {
					fmt.Printf("    Peers:       %d known\n", len(gc.Peers))
				}
				// Best-effort cluster view via this node's `garage status`;
				// silent when the call fails or the text moved.
				if running {
					if out, err := gm.Status(&gc); err == nil {
						fmt.Printf("    Nodes:       %d assigned (healthy)\n", podman.CountAssignedHealthyNodes(out))
					}
				}
			}
			fmt.Printf("    Listen:      %s\n", gc.Listen)
			fmt.Printf("    API port:    %d\n", gc.APIPort)
			fmt.Printf("    RPC/Web/Admin: %d/%d/%d\n", gc.RPCPort, gc.WebPort, gc.AdminPort)
			fmt.Printf("    S3 URL:      http://%s:%d\n", gc.Listen, gc.APIPort)
			if len(gc.Drives) > 0 {
				fmt.Printf("    Drives:      %d (multi-disk data_dir)\n", len(gc.Drives))
				slots := podman.GarageContainerDataDirs(len(gc.Drives))
				for i, d := range gc.Drives {
					fmt.Printf("      - %s <- %s\n", slots[i], d)
				}
			} else {
				fmt.Printf("    Data:        %s\n", gm.DataDir(&gc))
			}
			fmt.Printf("    Metadata:    %s\n", gm.MetadataDir(&gc))
			fmt.Printf("    Access key:  %s\n", gc.RootUser)
			if showPassword && gc.RootPassword != "" {
				fmt.Printf("    Secret key:  %s\n", gc.RootPassword)
			}
			fmt.Printf("    Bucket:      %s\n", gc.Name)
			fmt.Printf("    Image:       %s\n", gc.ImageTag)
			fmt.Printf("    Container:   %s\n", gc.ContainerName)
			fmt.Printf("    NOTE:        plaintext HTTP only — front with the nginx addon for TLS\n")
		}
	} else if len(cfg.Addons.Garage) > 0 {
		// Configured but the manager is unavailable (macOS/arm): still show them.
		for name, gc := range cfg.Addons.Garage {
			hasGarage = true
			fmt.Printf("  %s (name: %s)\n", "garage", name)
			fmt.Printf("    Status:      n/a (%v)\n", err)
			fmt.Printf("    Listen:      %s\n", gc.Listen)
			fmt.Printf("    API port:    %d\n", gc.APIPort)
			fmt.Printf("    Container:   %s\n", gc.ContainerName)
		}
	}
	if !hasGarage {
		fmt.Println("  (none)")
	}

	// seaweedfs (S3-compatible object storage — volume-granularity store;
	// single-node "weed mini" or one member of an MNMD Raft cluster)
	fmt.Println()
	fmt.Println("Infra add-ons (seaweedfs):")
	hasSeaweedfs := false
	if sm, err := podman.NewSeaweedfsManager(cfg); err == nil {
		for name, sc := range cfg.Addons.Seaweedfs {
			hasSeaweedfs = true
			status := "stopped"
			running, err := sm.ContainerRunning(sc.ContainerName)
			if err == nil && running {
				status = "running"
			}
			bucket := sc.Bucket
			if bucket == "" {
				bucket = sc.Name
			}
			s3url := sm.S3URL(&sc)
			fmt.Printf("  %s (name: %s)\n", "seaweedfs", name)
			fmt.Printf("    Status:      %s\n", status)
			if sc.Clustered() {
				peers := podman.NormalizeSeaweedfsPeers(&sc)
				fmt.Printf("    Cluster:     %s (replication %s, %d peers configured)\n", sc.Cluster, sc.Replication, len(peers))
				fmt.Printf("    Peers:       %s\n", strings.Join(peers, ","))
				// Best-effort leader read from this node's master; a member
				// without quorum answers with an empty Leader (or nothing at
				// all), which is waiting state, not failure.
				if running {
					if st, err := sm.ClusterStatus(&sc); err == nil {
						if st.Leader != "" {
							fmt.Printf("    Leader:      %s\n", st.Leader)
						} else {
							fmt.Printf("    Leader:      (none yet — still electing / no quorum)\n")
						}
					}
				}
				if sc.FilerDSN != "" {
					fmt.Printf("    Filer store: postgres %s\n", dsnDisplay(sc.FilerDSN))
				}
			}
			fmt.Printf("    Listen:      %s\n", sc.Listen)
			fmt.Printf("    Ports:       master=%d volume=%d filer=%d s3=%d\n", sc.Port, sc.VolumePort, sc.FilerPort, sc.S3Port)
			fmt.Printf("    S3 URL:      %s\n", s3url)
			if len(sc.Drives) > 0 {
				fmt.Printf("    Drives:      %d (multi-disk volume server)\n", len(sc.Drives))
				for i, d := range sc.Drives {
					fmt.Printf("      - /data%d <- %s\n", i+1, d)
				}
			} else {
				fmt.Printf("    Data:        %s\n", sm.DataDir(&sc))
			}
			fmt.Printf("    Access key:  %s\n", sc.RootUser)
			if showPassword && sc.RootPassword != "" {
				fmt.Printf("    Secret key:  %s\n", sc.RootPassword)
			}
			fmt.Printf("    Bucket:      %s\n", bucket)
			fmt.Printf("    Image:       %s\n", sc.ImageTag)
			fmt.Printf("    Container:   %s\n", sc.ContainerName)
			if sc.TLSOn() {
				fmt.Printf("    TLS:         native S3 HTTPS on %d (cleartext listener off)\n", sc.S3Port)
			}
		}
	} else if len(cfg.Addons.Seaweedfs) > 0 {
		// Configured but the manager is unavailable (no podman): still show them.
		for name, sc := range cfg.Addons.Seaweedfs {
			hasSeaweedfs = true
			scheme := "http"
			if sc.TLSOn() {
				scheme = "https"
			}
			bucket := sc.Bucket
			if bucket == "" {
				bucket = sc.Name
			}
			fmt.Printf("  %s (name: %s)\n", "seaweedfs", name)
			fmt.Printf("    Status:      n/a (%v)\n", err)
			fmt.Printf("    Listen:      %s\n", sc.Listen)
			fmt.Printf("    S3 URL:      %s://%s:%d\n", scheme, sc.Listen, sc.S3Port)
			fmt.Printf("    Bucket:      %s\n", bucket)
			fmt.Printf("    Container:   %s\n", sc.ContainerName)
		}
	}
	if !hasSeaweedfs {
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
			if rc.ClusterEnabled() {
				cluster := rc.Cluster
				if rc.AdvertiseHost != "" {
					cluster += " @" + rc.AdvertiseHost
				}
				fmt.Printf("    Cluster:     %s (bus %d)\n", cluster, rc.ClusterBusPort())
			}
			auth := "off"
			if rc.Password != "" {
				auth = "on (requirepass)"
			}
			fmt.Printf("    Auth:        %s\n", auth)
			if showPassword && rc.Password != "" {
				fmt.Printf("    Password:    %s\n", rc.Password)
				fmt.Printf("    Raw DSN:     redis://:%s@%s:%d/0\n", rc.Password, rc.Listen, rc.Port)
			}
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

	// predixy (Redis cluster proxy — one plain endpoint in front of a cluster)
	fmt.Println()
	fmt.Println("Infra add-ons (predixy):")
	hasPredixy := false
	if pm, err := podman.NewPredixyManager(cfg); err == nil {
		for _, name := range sortedAddonNames(cfg.Addons.Predixy) {
			pc := cfg.Addons.Predixy[name]
			hasPredixy = true
			status := "stopped"
			if running, err := pm.ContainerRunning(pc.ContainerName); err == nil && running {
				status = "running"
			}
			fmt.Printf("  %s (name: %s)\n", "predixy", name)
			fmt.Printf("    Status:      %s\n", status)
			fmt.Printf("    Address:     %s:%d\n", pc.Listen, pc.Port)
			fmt.Printf("    Workers:     %d\n", pc.Workers)
			fmt.Printf("    Backends:    %d (%s)\n", len(pc.Backend), strings.Join(pc.Backend, ", "))
			fmt.Printf("    Client DSN:  redis://:<password>@%s:%d/0\n", pc.Listen, pc.Port)
			if showPassword && pc.Password != "" {
				fmt.Printf("    Password:    %s\n", pc.Password)
			}
			fmt.Printf("    Image:       %s\n", pc.ImageTag)
			fmt.Printf("    Container:   %s\n", pc.ContainerName)
			fmt.Printf("    Config:      %s\n", filepath.Join(pm.ConfigDir(&pc), "predixy.conf"))
		}
	} else if len(cfg.Addons.Predixy) > 0 {
		// Configured but the manager is unavailable (podman missing, or macOS):
		// still show them.
		for _, name := range sortedAddonNames(cfg.Addons.Predixy) {
			pc := cfg.Addons.Predixy[name]
			hasPredixy = true
			fmt.Printf("  %s (name: %s)\n", "predixy", name)
			fmt.Printf("    Status:      n/a (%v)\n", err)
			fmt.Printf("    Address:     %s:%d\n", pc.Listen, pc.Port)
			fmt.Printf("    Container:   %s\n", pc.ContainerName)
		}
	}
	if !hasPredixy {
		fmt.Println("  (none)")
	}

	// pgAdmin (web administration UI — top-level, one container, persistent data dir)
	fmt.Println()
	fmt.Println("Web add-ons (pgadmin):")
	hasPgAdmin := false
	if pgm, err := podman.NewPgAdminManager(cfg); err == nil {
		for _, name := range sortedAddonNames(cfg.Addons.PgAdmin) {
			ac := cfg.Addons.PgAdmin[name]
			hasPgAdmin = true
			status := "stopped"
			if running, err := pgm.ContainerRunning(ac.ContainerName); err == nil && running {
				status = "running"
			}
			fmt.Printf("  %s (name: %s)\n", "pgadmin", name)
			fmt.Printf("    Status:      %s\n", status)
			fmt.Printf("    URL:         http://%s:%d/\n", ac.Listen, ac.HostPort)
			fmt.Printf("    Login email: %s\n", ac.Email)
			if showPassword && ac.Password != "" {
				fmt.Printf("    Login password: %s\n", ac.Password)
			}
			if ac.DSN != "" {
				fmt.Printf("    Seeded:      %s\n", pgadminSeededDisplay(ac))
			}
			fmt.Printf("    Data:        %s\n", pgm.DataDir(&ac))
			fmt.Printf("    Image:       %s\n", ac.ImageTag)
			fmt.Printf("    Container:   %s\n", ac.ContainerName)
		}
	} else if len(cfg.Addons.PgAdmin) > 0 {
		// Configured but the manager is unavailable (podman missing): still show them.
		for _, name := range sortedAddonNames(cfg.Addons.PgAdmin) {
			ac := cfg.Addons.PgAdmin[name]
			hasPgAdmin = true
			fmt.Printf("  %s (name: %s)\n", "pgadmin", name)
			fmt.Printf("    Status:      n/a (%v)\n", err)
			fmt.Printf("    URL:         http://%s:%d/\n", ac.Listen, ac.HostPort)
			fmt.Printf("    Container:   %s\n", ac.ContainerName)
		}
	}
	if !hasPgAdmin {
		fmt.Println("  (none)")
	}

	// Grafana (observability dashboard — top-level, one container, persistent data dir)
	fmt.Println()
	fmt.Println("Web add-ons (grafana):")
	hasGrafana := false
	if gm, err := podman.NewGrafanaManager(cfg); err == nil {
		for _, name := range sortedAddonNames(cfg.Addons.Grafana) {
			gc := cfg.Addons.Grafana[name]
			hasGrafana = true
			status := "stopped"
			if running, err := gm.ContainerRunning(gc.ContainerName); err == nil && running {
				status = "running"
			}
			fmt.Printf("  %s (name: %s)\n", "grafana", name)
			fmt.Printf("    Status:      %s\n", status)
			fmt.Printf("    URL:         http://%s:%d/\n", gc.Listen, gc.HostPort)
			fmt.Printf("    Login user:  %s\n", gc.AdminUser)
			if showPassword && gc.Password != "" {
				fmt.Printf("    Login password: %s\n", gc.Password)
			}
			fmt.Printf("    Data:        %s\n", gm.DataDir(&gc))
			fmt.Printf("    Image:       %s\n", gc.ImageTag)
			fmt.Printf("    Container:   %s\n", gc.ContainerName)
		}
	} else if len(cfg.Addons.Grafana) > 0 {
		// Configured but the manager is unavailable (podman missing): still show them.
		for _, name := range sortedAddonNames(cfg.Addons.Grafana) {
			gc := cfg.Addons.Grafana[name]
			hasGrafana = true
			fmt.Printf("  %s (name: %s)\n", "grafana", name)
			fmt.Printf("    Status:      n/a (%v)\n", err)
			fmt.Printf("    URL:         http://%s:%d/\n", gc.Listen, gc.HostPort)
			fmt.Printf("    Container:   %s\n", gc.ContainerName)
		}
	}
	if !hasGrafana {
		fmt.Println("  (none)")
	}

	// prometheus (metrics collection backend, top-level addon; Linux host network, macOS bridge)
	fmt.Println()
	fmt.Println("Web add-ons (prometheus):")
	hasPrometheus := false
	if pm, err := podman.NewPrometheusManager(cfg); err == nil {
		for _, name := range sortedAddonNames(cfg.Addons.Prometheus) {
			pc := cfg.Addons.Prometheus[name]
			hasPrometheus = true
			status := "stopped"
			if running, err := pm.ContainerRunning(pc.ContainerName); err == nil && running {
				status = "running"
			}
			fmt.Printf("  %s (name: %s)\n", "prometheus", name)
			fmt.Printf("    Status:      %s\n", status)
			fmt.Printf("    URL:         http://%s:%d/\n", pc.Listen, pc.HostPort)
			mode := fmt.Sprintf("rendered (%d scrape job(s))", len(pc.Scrape))
			if len(pc.Alertmanager) > 0 {
				mode += fmt.Sprintf(" + alertmanager x%d", len(pc.Alertmanager))
			}
			if len(pc.RuleFiles) > 0 {
				mode += fmt.Sprintf(", %d rule file(s)", len(pc.RuleFiles))
			}
			if pc.ConfFile != "" {
				mode = "conf-file"
				if len(pc.RuleFiles) > 0 {
					mode += fmt.Sprintf(" (+%d rule file(s) mounted)", len(pc.RuleFiles))
				}
			}
			fmt.Printf("    Config:      %s [%s]\n", pm.ConfigPath(&pc), mode)
			fmt.Printf("    Retention:   %s\n", pc.Retention)
			fmt.Printf("    Data:        %s\n", pm.DataDir(&pc))
			fmt.Printf("    Image:       %s\n", pc.ImageTag)
			fmt.Printf("    Container:   %s\n", pc.ContainerName)
		}
	} else if len(cfg.Addons.Prometheus) > 0 {
		// Configured but the manager is unavailable (podman missing): still show them.
		for _, name := range sortedAddonNames(cfg.Addons.Prometheus) {
			pc := cfg.Addons.Prometheus[name]
			hasPrometheus = true
			fmt.Printf("  %s (name: %s)\n", "prometheus", name)
			fmt.Printf("    Status:      n/a (%v)\n", err)
			fmt.Printf("    URL:         http://%s:%d/\n", pc.Listen, pc.HostPort)
			fmt.Printf("    Container:   %s\n", pc.ContainerName)
		}
	}
	if !hasPrometheus {
		fmt.Println("  (none)")
	}

	// victoriametrics (time-series database, top-level addon; Linux host network, macOS bridge)
	fmt.Println()
	fmt.Println("Web add-ons (victoriametrics):")
	hasVM := false
	if vm, err := podman.NewVictoriaMetricsManager(cfg); err == nil {
		for _, name := range sortedAddonNames(cfg.Addons.VictoriaMetrics) {
			vc := cfg.Addons.VictoriaMetrics[name]
			hasVM = true
			status := "stopped"
			if running, err := vm.ContainerRunning(vc.ContainerName); err == nil && running {
				status = "running"
			}
			fmt.Printf("  %s (name: %s)\n", "victoriametrics", name)
			fmt.Printf("    Status:      %s\n", status)
			fmt.Printf("    URL:         http://%s:%d/\n", vc.Listen, vc.HostPort)
			fmt.Printf("    Retention:   %s month(s)\n", vc.Retention)
			if len(vc.ExtraArgs) > 0 {
				fmt.Printf("    Extra args:  %s\n", strings.Join(vc.ExtraArgs, " "))
			}
			fmt.Printf("    Data:        %s\n", vm.DataDir(&vc))
			fmt.Printf("    Image:       %s\n", vc.ImageTag)
			fmt.Printf("    Container:   %s\n", vc.ContainerName)
		}
	} else if len(cfg.Addons.VictoriaMetrics) > 0 {
		for _, name := range sortedAddonNames(cfg.Addons.VictoriaMetrics) {
			vc := cfg.Addons.VictoriaMetrics[name]
			hasVM = true
			fmt.Printf("  %s (name: %s)\n", "victoriametrics", name)
			fmt.Printf("    Status:      n/a (%v)\n", err)
			fmt.Printf("    URL:         http://%s:%d/\n", vc.Listen, vc.HostPort)
			fmt.Printf("    Container:   %s\n", vc.ContainerName)
		}
	}
	if !hasVM {
		fmt.Println("  (none)")
	}

	// vmcluster (VictoriaMetrics cluster — cross-host, horizontal scaling)
	fmt.Println()
	fmt.Println("Cluster add-ons (vmcluster):")
	hasVMC := false
	if vmc, err := podman.NewVMClusterManager(cfg); err == nil {
		for _, name := range sortedAddonNames(cfg.Addons.VMCluster) {
			vc := cfg.Addons.VMCluster[name]
			hasVMC = true
			st, _ := vmc.ClusterStatus(&vc)
			fmt.Printf("  %s (name: %s)\n", "vmcluster", name)
			fmt.Printf("    Status:      %s\n", st.String())
			fmt.Printf("    Roles:       %s\n", strings.Join(vc.RolesInOrder(), ", "))
			if vc.RunsRole(config.VMClusterRoleInsert) {
				fmt.Printf("    Insert URL:  http://%s:%d/\n", vc.Listen, vc.InsertPort)
			}
			if vc.RunsRole(config.VMClusterRoleSelect) {
				fmt.Printf("    Select URL:  http://%s:%d/\n", vc.Listen, vc.SelectPort)
			}
			if vc.RunsRole(config.VMClusterRoleStorage) {
				fmt.Printf("    Storage URL: http://%s:%d/\n", vc.Listen, vc.StoragePort)
				fmt.Printf("    Internal:    TCP %d (insert), %d (select)\n", vc.InternalInsertPort, vc.InternalSelectPort)
				fmt.Printf("    Retention:   %s month(s)\n", vc.Retention)
				fmt.Printf("    Data:        %s\n", vmc.DataDir(&vc))
				fmt.Printf("    Image:       %s\n", vc.StorageImageTag)
			}
			if vc.RunsRole(config.VMClusterRoleInsert) && !vc.RunsRole(config.VMClusterRoleStorage) {
				fmt.Printf("    Image:       %s\n", vc.InsertImageTag)
			}
			if vc.RunsRole(config.VMClusterRoleSelect) && !vc.RunsRole(config.VMClusterRoleStorage) && !vc.RunsRole(config.VMClusterRoleInsert) {
				fmt.Printf("    Image:       %s\n", vc.SelectImageTag)
			}
			// Containers.
			for _, role := range vc.RolesInOrder() {
				fmt.Printf("    Container:   %s (%s)\n", vc.ContainerName(role), role)
			}
			// Storage nodes.
			nodes := vc.EffectiveStorageNodes()
			if len(nodes) > 0 {
				fmt.Print("    Storage nodes: ")
				for i, sn := range nodes {
					if i > 0 {
						fmt.Print(", ")
					}
					host := strings.Split(sn.Address, ":")[0]
					insertPort := strings.Split(sn.Address, ":")[1]
					fmt.Printf("%s:%s:%d", host, insertPort, sn.SelectPort)
				}
				fmt.Println()
			}
		}
	} else if len(cfg.Addons.VMCluster) > 0 {
		for _, name := range sortedAddonNames(cfg.Addons.VMCluster) {
			vc := cfg.Addons.VMCluster[name]
			hasVMC = true
			fmt.Printf("  %s (name: %s)\n", "vmcluster", name)
			fmt.Printf("    Status:      n/a (%v)\n", err)
			fmt.Printf("    Roles:       %s\n", strings.Join(vc.RolesInOrder(), ", "))
		}
	}
	if !hasVMC {
		fmt.Println("  (none)")
	}

	// alertmanager (alert routing frontend, top-level addon; Linux host network, macOS bridge)
	fmt.Println()
	fmt.Println("Web add-ons (alertmanager):")
	hasAlertmanager := false
	if am, err := podman.NewAlertmanagerManager(cfg); err == nil {
		for _, name := range sortedAddonNames(cfg.Addons.Alertmanager) {
			ac := cfg.Addons.Alertmanager[name]
			hasAlertmanager = true
			status := "stopped"
			if running, err := am.ContainerRunning(ac.ContainerName); err == nil && running {
				status = "running"
			}
			fmt.Printf("  %s (name: %s)\n", "alertmanager", name)
			fmt.Printf("    Status:      %s\n", status)
			fmt.Printf("    URL:         http://%s:%d/\n", ac.Listen, ac.HostPort)
			mode := fmt.Sprintf("rendered (%d webhook)", len(ac.Webhook))
			if ac.ConfFile != "" {
				mode = "conf-file"
			}
			fmt.Printf("    Config:      %s [%s]\n", am.ConfigPath(&ac), mode)
			fmt.Printf("    Data:        %s\n", am.DataDir(&ac))
			fmt.Printf("    Image:       %s\n", ac.ImageTag)
			fmt.Printf("    Container:   %s\n", ac.ContainerName)
		}
	} else if len(cfg.Addons.Alertmanager) > 0 {
		// Configured but the manager is unavailable (podman missing): still show them.
		for _, name := range sortedAddonNames(cfg.Addons.Alertmanager) {
			ac := cfg.Addons.Alertmanager[name]
			hasAlertmanager = true
			fmt.Printf("  %s (name: %s)\n", "alertmanager", name)
			fmt.Printf("    Status:      n/a (%v)\n", err)
			fmt.Printf("    URL:         http://%s:%d/\n", ac.Listen, ac.HostPort)
			fmt.Printf("    Container:   %s\n", ac.ContainerName)
		}
	}
	if !hasAlertmanager {
		fmt.Println("  (none)")
	}

	// gitea (self-hosted Git service — top-level, one container, persistent data dir)
	fmt.Println()
	fmt.Println("Web add-ons (gitea):")
	hasGitea := false
	if gm, err := podman.NewGiteaManager(cfg); err == nil {
		for _, name := range sortedAddonNames(cfg.Addons.Gitea) {
			gc := cfg.Addons.Gitea[name]
			hasGitea = true
			status := "stopped"
			if running, err := gm.ContainerRunning(gc.ContainerName); err == nil && running {
				status = "running"
			}
			fmt.Printf("  %s (name: %s)\n", "gitea", name)
			fmt.Printf("    Status:      %s\n", status)
			fmt.Printf("    Web URL:     http://%s:%d/\n", podman.GiteaDisplayHost(gc.Listen), gc.HostPort)
			if gc.SSH {
				fmt.Printf("    Git SSH:     ssh://git@%s:%d/\n", podman.GiteaDisplayHost(gc.Listen), gc.SSHPort)
			}
			if gc.Dsn != "" {
				fmt.Printf("    Database:    external PostgreSQL (%s)\n", dsnDisplay(gc.Dsn))
			}
			fmt.Printf("    Login user:  %s\n", gc.AdminUser)
			if showPassword && gc.Password != "" {
				fmt.Printf("    Login password: %s\n", gc.Password)
			}
			fmt.Printf("    Data:        %s\n", gm.DataDir(&gc))
			fmt.Printf("    Image:       %s\n", gc.ImageTag)
			fmt.Printf("    Container:   %s\n", gc.ContainerName)
		}
	} else if len(cfg.Addons.Gitea) > 0 {
		// Configured but the manager is unavailable (podman missing): still show them.
		for _, name := range sortedAddonNames(cfg.Addons.Gitea) {
			gc := cfg.Addons.Gitea[name]
			hasGitea = true
			fmt.Printf("  %s (name: %s)\n", "gitea", name)
			fmt.Printf("    Status:      n/a (%v)\n", err)
			fmt.Printf("    Web URL:     http://%s:%d/\n", podman.GiteaDisplayHost(gc.Listen), gc.HostPort)
			fmt.Printf("    Container:   %s\n", gc.ContainerName)
		}
	}
	if !hasGitea {
		fmt.Println("  (none)")
	}

	// beegfs (parallel filesystem — top-level, THREE containers per filesystem;
	// the list unit is the cluster, so its status is the aggregate of mgmtd +
	// meta + storage, not one container's running flag)
	fmt.Println()
	fmt.Println("Infra add-ons (beegfs):")
	hasBeeGFS := false
	if bm, err := podman.NewBeeGFSManager(cfg); err == nil {
		for _, name := range sortedAddonNames(cfg.Addons.BeeGFS) {
			bc := cfg.Addons.BeeGFS[name]
			hasBeeGFS = true
			status := "n/a"
			if st, err := bm.ClusterStatus(&bc); err == nil {
				status = st.String()
			}
			fmt.Printf("  %s (name: %s)\n", "beegfs", name)
			fmt.Printf("    Status:      %s\n", status)
			fmt.Printf("    Roles:       %s (on this host)\n", strings.Join(bc.RolesInOrder(), ", "))
			if bc.MgmtdHost != "" {
				fmt.Printf("    Mgmtd:       %s:%d (BeeMsg) / %s:%d (gRPC) (remote — this host joins it)\n", bc.MgmtdHost, bc.MgmtdPort, bc.MgmtdHost, bc.MgmtdGRPCPort)
			} else {
				fmt.Printf("    Mgmtd:       %s:%d (BeeMsg) / %s:%d (gRPC)\n", beegfsDisplayHost(bc.Listen), bc.MgmtdPort, beegfsDisplayHost(bc.Listen), bc.MgmtdGRPCPort)
			}
			fmt.Printf("    Ports:       meta %d, storage %d (fixed)\n", bc.MetaPort, bc.StoragePort)
			auth := "none"
			if bc.ConnAuth != "" {
				auth = "set (connection-auth)"
			}
			fmt.Printf("    Auth:        %s\n", auth)
			if showPassword && bc.ConnAuth != "" {
				fmt.Printf("    Conn-auth:   %s\n", bc.ConnAuth)
			}
			fmt.Printf("    Image:       %s-{%s}:%s\n", config.BeeGFSImageRepo, strings.Join(bc.RolesInOrder(), ","), bc.ImageTag)
			dataDirs := make([]string, 0, 4)
			containers := make([]string, 0, 3)
			for _, role := range bc.RolesInOrder() {
				containers = append(containers, bc.ContainerName(role))
				dataDirs = append(dataDirs, bm.DataDirs(&bc, role)...)
			}
			fmt.Printf("    Data:        %s\n", strings.Join(dataDirs, ", "))
			fmt.Printf("    Containers:  %s\n", strings.Join(containers, ", "))
		}
	} else if len(cfg.Addons.BeeGFS) > 0 {
		// Configured but the manager is unavailable (podman missing): still show them.
		for _, name := range sortedAddonNames(cfg.Addons.BeeGFS) {
			bc := cfg.Addons.BeeGFS[name]
			hasBeeGFS = true
			containers := make([]string, 0, 3)
			for _, role := range bc.RolesInOrder() {
				containers = append(containers, bc.ContainerName(role))
			}
			fmt.Printf("  %s (name: %s)\n", "beegfs", name)
			fmt.Printf("    Status:      n/a (manager unavailable)\n")
			fmt.Printf("    Roles:       %s (on this host)\n", strings.Join(bc.RolesInOrder(), ", "))
			fmt.Printf("    Mgmtd:       %s:%d (BeeMsg)\n", beegfsDisplayHost(bc.Listen), bc.MgmtdPort)
			fmt.Printf("    Containers:  %s\n", strings.Join(containers, ", "))
		}
	}
	if !hasBeeGFS {
		fmt.Println("  (none)")
	}

	// juicefs (host-binary FUSE volume over an external S3 service)
	fmt.Println()
	fmt.Println("Filesystem add-ons (juicefs):")
	hasJuicefs := false
	for _, name := range sortedAddonNames(cfg.Addons.Juicefs) {
		jc := cfg.Addons.Juicefs[name]
		hasJuicefs = true
		state := "not mounted"
		if isMountPoint(jc.MountPoint) {
			state = "mounted"
		}
		fmt.Printf("  %s (name: %s)\n", "juicefs", name)
		fmt.Printf("    Status:      %s\n", state)
		fmt.Printf("    Metadata:    %s\n", dsnDisplay(jc.MetaDSN))
		fmt.Printf("    S3:          %s, bucket %s\n", jc.S3Endpoint, jc.Bucket)
		fmt.Printf("    Binary:      %s\n", jc.BinaryPath)
		fmt.Printf("    Mount:       %s\n", jc.MountPoint)
		fmt.Printf("    Cache:       %s\n", jc.CacheDir)
		if showPassword && jc.SecretKey != "" {
			fmt.Printf("    S3 secret:   %s\n", jc.SecretKey)
		}
	}
	if !hasJuicefs {
		fmt.Println("  (none)")
	}

	// zerofs (host-binary 9P volume over an external S3 service, dual process)
	fmt.Println()
	fmt.Println("Filesystem add-ons (zerofs):")
	hasZerofs := false
	for _, name := range sortedAddonNames(cfg.Addons.Zerofs) {
		zc := cfg.Addons.Zerofs[name]
		hasZerofs = true
		srv := "stopped"
		if hostbin.Alive(zc.ServerPidFile) {
			srv = "running"
		}
		state := "not mounted"
		if isMountPoint(zc.MountPoint) {
			state = "mounted"
		}
		fmt.Printf("  %s (name: %s)\n", "zerofs", name)
		fmt.Printf("    Server:      %s (%s:%d, 9P)\n", srv, zc.Listen, zc.ListenPort)
		fmt.Printf("    Mount:       %s at %s\n", state, zc.MountPoint)
		fmt.Printf("    S3:          %s, bucket %s\n", zc.S3Endpoint, zc.Bucket)
		fmt.Printf("    Fence:       %s\n", zc.ConditionalPut)
		fmt.Printf("    Binary:      %s\n", zc.BinaryPath)
		fmt.Printf("    Config:      %s\n", zc.ConfigPath)
		if showPassword && zc.EncryptionPassword != "" {
			fmt.Printf("    Encryption:  %s\n", zc.EncryptionPassword)
		}
	}
	if !hasZerofs {
		fmt.Println("  (none)")
	}

	// duckdb (host-binary embedded OLAP, no daemon)
	fmt.Println()
	fmt.Println("Analytics add-ons (duckdb):")
	hasDuckdb := false
	for _, name := range sortedAddonNames(cfg.Addons.Duckdb) {
		dc := cfg.Addons.Duckdb[name]
		hasDuckdb = true
		state := "not found"
		if _, err := os.Stat(dc.BinaryPath); err == nil {
			// Try to extract version
			cmd := exec.Command(dc.BinaryPath, "--version")
			if out, err := cmd.Output(); err == nil {
				ver := extractDuckdbVersion(string(out))
				state = fmt.Sprintf("v%s", ver)
			} else {
				state = "error (cannot execute binary)"
			}
		}
		fmt.Printf("  %s (name: %s)\n", "duckdb", name)
		fmt.Printf("    Status:      %s\n", state)
		fmt.Printf("    Binary:      %s\n", dc.BinaryPath)
		fmt.Printf("    Version:     %s (configured)\n", dc.Version)
		fmt.Printf("    Data dir:    %s\n", dc.DataDir)
	}
	if !hasDuckdb {
		fmt.Println("  (none)")
	}

	// nginx (HTTP reverse proxy, top-level addon; Linux + macOS)
	fmt.Println()
	fmt.Println("Web add-ons (nginx):")
	hasNginx := false
	if nm, err := podman.NewNginxManager(cfg); err == nil {
		for _, name := range sortedAddonNames(cfg.Addons.Nginx) {
			nc := cfg.Addons.Nginx[name]
			hasNginx = true
			status := "stopped"
			if running, err := nm.ContainerRunning(nc.ContainerName); err == nil && running {
				status = "running"
			}
			fmt.Printf("  %s (name: %s)\n", "nginx", name)
			fmt.Printf("    Status:      %s\n", status)
			fmt.Printf("    HTTP:        http://%s:%d/\n", nc.Listen, nc.HTTPPort)
			if nc.TLS && nc.HTTPSPort > 0 {
				fmt.Printf("    HTTPS:       https://%s:%d/\n", nc.Listen, nc.HTTPSPort)
			}
			fmt.Printf("    Backends:    %d\n", len(nc.Backends))
			for _, b := range nc.Backends {
				fmt.Printf("      %-12s %s -> %s\n", b.Name, b.Path, strings.Join(b.Backends, ", "))
			}
			fmt.Printf("    Image:       %s\n", nc.ImageTag)
			fmt.Printf("    Container:   %s\n", nc.ContainerName)
		}
	} else if len(cfg.Addons.Nginx) > 0 {
		for _, name := range sortedAddonNames(cfg.Addons.Nginx) {
			nc := cfg.Addons.Nginx[name]
			hasNginx = true
			fmt.Printf("  %s (name: %s)\n", "nginx", name)
			fmt.Printf("    Status:      n/a (%v)\n", err)
			fmt.Printf("    HTTP:        http://%s:%d/\n", nc.Listen, nc.HTTPPort)
			fmt.Printf("    Container:   %s\n", nc.ContainerName)
		}
	}
	if !hasNginx {
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
// password logic
// ---------------------------------------------------------------------------

// addonPasswords indexes every instance that has a stored password, keyed
// "<addon>:<name>". Only these addons qualify: etcd and haproxy have no
// stored credential at all, and PG instance passwords are not auto-generated by
// pgcli — neither belongs in a "print the stored password" surface that promises
// to work. (The root *user* is not part of this: it is never secret, so
// `pg addon list` always prints it.)
//
// Predixy's entry is never pgcli-generated — it is the operator-supplied copy of
// the proxied cluster's requirepass — but it is still the answer to "what
// password does this addon listen for", so `pg addon password predixy` works the
// same way and the two sides can be cross-checked in one command.
//
// pgAdmin's entry is its WEB login password (PGADMIN_DEFAULT_PASSWORD), not a
// PostgreSQL password — but it is still pgcli-generated and the only credential
// this addon has, so it belongs here for the same "retrieve what I printed at
// install" reason as redis's requirepass.
//
// Grafana's entry is exactly the same story with GF_SECURITY_ADMIN_PASSWORD
// (which, like pgAdmin's, only takes effect while grafana.db is absent).
func addonPasswords(cfg *config.Config) map[string]string {
	out := map[string]string{}
	for name, rc := range cfg.Addons.Redis {
		out["redis:"+name] = rc.Password
	}
	for name, pc := range cfg.Addons.Predixy {
		out["predixy:"+name] = pc.Password
	}
	for name, mc := range cfg.Addons.Minio {
		out["minio:"+name] = mc.RootPassword
	}
	for name, sc := range cfg.Addons.Silo {
		out["silo:"+name] = sc.RootPassword
	}
	for name, rc := range cfg.Addons.Rustfs {
		out["rustfs:"+name] = rc.RootPassword
	}
	for name, gc := range cfg.Addons.Garage {
		out["garage:"+name] = gc.RootPassword
	}
	for name, sc := range cfg.Addons.Seaweedfs {
		out["seaweedfs:"+name] = sc.RootPassword
	}
	for name, ac := range cfg.Addons.PgAdmin {
		out["pgadmin:"+name] = ac.Password
	}
	for name, gc := range cfg.Addons.Grafana {
		out["grafana:"+name] = gc.Password
	}
	// Gitea's stored password is the one pgcli created the admin account with
	// (see runAddonInstallGitea) — a real login for a fresh install, but only
	// informational on a revived store whose admin pre-dates this pgcli
	// config entry (rotate it inside the container with `gitea admin user
	// change-password` in that case).
	for name, gc := range cfg.Addons.Gitea {
		out["gitea:"+name] = gc.Password
	}
	// BeeGFS's entry is the connection-auth secret (CONN_AUTH_FILE_DATA) shared
	// by the three daemons and every client — not a login, but the addon's only
	// credential and the one thing re-installs must be able to reproduce.
	for name, bc := range cfg.Addons.BeeGFS {
		out["beegfs:"+name] = bc.ConnAuth
	}
	// juicefs's credential is the S3 secret-key it formatted the volume with
	// (stored in the volume's metadata thereafter, but this is the one the
	// reinstall must reproduce).
	for name, jc := range cfg.Addons.Juicefs {
		out["juicefs:"+name] = jc.SecretKey
	}
	// zerofs's is the encryption password wrapping the bucket's data-encryption
	// key — losing it means losing the volume's data, so re-installs must be
	// able to reproduce it (pg addon install zerofs --encryption-password).
	for name, zc := range cfg.Addons.Zerofs {
		out["zerofs:"+name] = zc.EncryptionPassword
	}
	return out
}

// runAddonPassword resolves "<addon>" (plus --name, defaulting to the addon's
// own name) to the stored password and prints it bare on stdout, or to --file
// mode 0600 following the `pg ha passwords` precedent.
func runAddonPassword(addon, name, file string) error {
	if name == "" {
		name = addon
	}
	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	all := addonPasswords(cfg)
	key := addon + ":" + name
	entry, ok := all[key]
	if !ok {
		var known []string
		for k := range all {
			if strings.HasPrefix(k, addon+":") {
				known = append(known, strings.TrimPrefix(k, addon+":"))
			}
		}
		sort.Strings(known)
		if len(known) == 0 {
			return fmt.Errorf("no %q add-on is installed (and it stores no generated password)", addon)
		}
		return fmt.Errorf("no %s instance %q installed; installed: %s", addon, name, strings.Join(known, ", "))
	}
	if entry == "" {
		// A garage cluster member that has not seeded yet legitimately holds no
		// secret: `garage key create` runs once on the install that completes
		// quorum, and only that host's pg.yaml stores the printed secret.
		if addon == "garage" && cfg.Addons.Garage[name].Clustered() {
			return fmt.Errorf("garage %q (cluster %q) has no seeded key yet — the access key and its secret are created by `garage key create` on the install that completes quorum, and are stored ONLY on that host's pg.yaml; re-run the remaining members' install commands, then read it there", name, cfg.Addons.Garage[name].Cluster)
		}
		return fmt.Errorf("%s has no stored password (empty in %s)", key, path)
	}

	if file != "" {
		if err := os.WriteFile(file, []byte(entry+"\n"), 0600); err != nil {
			return fmt.Errorf("writing %s: %w", file, err)
		}
		fmt.Printf("[OK] password for %s written to %s (mode 0600)\n", key, file)
		return nil
	}
	fmt.Println(entry)
	return nil
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
	case "garage":
		return runAddonRemoveGarage(cmd)
	case "seaweedfs":
		return runAddonRemoveSeaweedfs(cmd)
	case "redis":
		return runAddonRemoveRedis(cmd)
	case "predixy":
		return runAddonRemovePredixy(cmd)
	case "postgrest":
		return runAddonRemovePostgrest(cmd)
	case "pgadmin":
		return runAddonRemovePgAdmin(cmd)
	case "grafana":
		return runAddonRemoveGrafana(cmd)
	case "prometheus":
		return runAddonRemovePrometheus(cmd)
	case "victoriametrics":
		return runAddonRemoveVictoriaMetrics(cmd)
	case "vmcluster":
		return runAddonRemoveVMCluster(cmd)
	case "alertmanager":
		return runAddonRemoveAlertmanager(cmd)
	case "gitea":
		return runAddonRemoveGitea(cmd)
	case "beegfs":
		return runAddonRemoveBeeGFS(cmd)
	case "juicefs":
		return runAddonRemoveJuicefs(cmd)
	case "zerofs":
		return runAddonRemoveZerofs(cmd)
	case "nginx":
		return runAddonRemoveNginx(cmd)
	case "duckdb":
		return runAddonRemoveDuckdb(cmd)
	case "pgbouncer":
		// falls through to the PgBouncer flow below
	default:
		return fmt.Errorf("unknown addon: %s (available: pgbouncer, etcd, pgdog, haproxy, minio, silo, rustfs, garage, seaweedfs, redis, predixy, postgrest, pgadmin, grafana, prometheus, victoriametrics, vmcluster, alertmanager, gitea, beegfs, juicefs, zerofs, nginx, duckdb)", addonName)
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
	case "garage":
		return runAddonStartGarage(cmd)
	case "seaweedfs":
		return runAddonStartSeaweedfs(cmd)
	case "redis":
		return runAddonStartRedis(cmd)
	case "predixy":
		return runAddonStartPredixy(cmd)
	case "pgbouncer":
		return runAddonStartPgBouncer(cmd)
	case "postgrest":
		return runAddonStartPostgrest(cmd)
	case "pgadmin":
		return runAddonStartPgAdmin(cmd)
	case "grafana":
		return runAddonStartGrafana(cmd)
	case "prometheus":
		return runAddonStartPrometheus(cmd)
	case "victoriametrics":
		return runAddonStartVictoriaMetrics(cmd)
	case "vmcluster":
		return runAddonStartVMCluster(cmd)
	case "alertmanager":
		return runAddonStartAlertmanager(cmd)
	case "gitea":
		return runAddonStartGitea(cmd)
	case "beegfs":
		return runAddonStartBeeGFS(cmd)
	case "juicefs":
		return runAddonStartJuicefs(cmd)
	case "zerofs":
		return runAddonStartZerofs(cmd)
	case "nginx":
		return runAddonStartNginx(cmd)
	case "duckdb":
		fmt.Println("DuckDB is a CLI tool, not a daemon — start/stop are not applicable.")
		fmt.Println("Run 'pg addon duckdb shell' to launch an interactive session.")
		return nil
	default:
		return fmt.Errorf("unknown addon: %s (available: pgbouncer, etcd, pgdog, haproxy, minio, silo, rustfs, garage, seaweedfs, redis, predixy, postgrest, pgadmin, grafana, prometheus, victoriametrics, vmcluster, alertmanager, gitea, beegfs, juicefs, zerofs, nginx, duckdb)", addonName)
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
	case "garage":
		return runAddonStopGarage(cmd)
	case "seaweedfs":
		return runAddonStopSeaweedfs(cmd)
	case "redis":
		return runAddonStopRedis(cmd)
	case "predixy":
		return runAddonStopPredixy(cmd)
	case "pgbouncer":
		return runAddonStopPgBouncer(cmd)
	case "postgrest":
		return runAddonStopPostgrest(cmd)
	case "pgadmin":
		return runAddonStopPgAdmin(cmd)
	case "grafana":
		return runAddonStopGrafana(cmd)
	case "prometheus":
		return runAddonStopPrometheus(cmd)
	case "victoriametrics":
		return runAddonStopVictoriaMetrics(cmd)
	case "vmcluster":
		return runAddonStopVMCluster(cmd)
	case "alertmanager":
		return runAddonStopAlertmanager(cmd)
	case "gitea":
		return runAddonStopGitea(cmd)
	case "beegfs":
		return runAddonStopBeeGFS(cmd)
	case "juicefs":
		return runAddonStopJuicefs(cmd)
	case "zerofs":
		return runAddonStopZerofs(cmd)
	case "nginx":
		return runAddonStopNginx(cmd)
	case "duckdb":
		fmt.Println("DuckDB is a CLI tool, not a daemon — start/stop are not applicable.")
		return nil
	default:
		return fmt.Errorf("unknown addon: %s (available: pgbouncer, etcd, pgdog, haproxy, minio, silo, rustfs, garage, seaweedfs, redis, predixy, postgrest, pgadmin, grafana, prometheus, victoriametrics, vmcluster, alertmanager, gitea, beegfs, juicefs, zerofs, nginx, duckdb)", addonName)
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

// parseNginxBackend parses a --backend spec into a NginxBackend. The format is
// name=<upstream>,path=<location>,backend=<host:port> (all required). Used
// repeatable for multiple upstreams.
func parseNginxBackend(spec string) (config.NginxBackend, error) {
	var b config.NginxBackend
	var hasBackend, hasBackends bool
	parts := strings.Split(spec, ",")
	for i := 0; i < len(parts); i++ {
		k, v, ok := strings.Cut(parts[i], "=")
		if !ok {
			// No "=" — continuation of the previous key's value (for
			// backends=h1:p1,h2:p2 where commas separate servers, not fields)
			return b, fmt.Errorf("backend spec %q: expected key=value pairs separated by commas", spec)
		}
		switch k {
		case "name":
			b.Name = v
		case "path":
			b.Path = v
		case "backend":
			hasBackend = true
			b.Backends = append(b.Backends, v)
		case "backends":
			hasBackends = true
			b.Backends = append(b.Backends, v)
			// Consume subsequent comma-separated parts that lack "=" —
			// they are additional server addresses, not new keys.
			for i+1 < len(parts) && !strings.Contains(parts[i+1], "=") {
				i++
				b.Backends = append(b.Backends, parts[i])
			}
		default:
			return b, fmt.Errorf("backend spec %q: unknown key %q (expected name, path, backend, backends)", spec, k)
		}
	}
	if b.Name == "" {
		return b, fmt.Errorf("backend spec %q: name is required", spec)
	}
	if b.Path == "" {
		return b, fmt.Errorf("backend spec %q: path is required", spec)
	}
	if len(b.Backends) == 0 {
		return b, fmt.Errorf("backend spec %q: backend or backends is required", spec)
	}
	if hasBackend && hasBackends {
		return b, fmt.Errorf("backend spec %q: backend and backends are mutually exclusive", spec)
	}
	return b, nil
}

// runAddonInstallNginx installs a standalone nginx reverse proxy container:
// path-based HTTP routing in front of web services (pgAdmin, PostgREST, etc.).
// The image is pull-only from the public docker.io/library repo. Config is
// rendered to nginx.conf and bind-mounted read-only.
//
// Backends can be specified explicitly (--upstream name=...,path=...,backend=...
// repeatable).
func runAddonInstallNginx(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "nginx"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	httpPort, _ := cmd.Flags().GetInt("http-port")
	httpsPort, _ := cmd.Flags().GetInt("https-port")
	workerConns, _ := cmd.Flags().GetInt("worker-connections")
	backendSpecs, _ := cmd.Flags().GetStringArray("upstream")
	confFile, _ := cmd.Flags().GetString("conf-file")
	tls, _ := cmd.Flags().GetBool("tls")
	tlsCert, _ := cmd.Flags().GetString("tls-cert")
	tlsKey, _ := cmd.Flags().GetString("tls-key")

	// TLS cert/key must come together
	if tlsCert != "" && tlsKey == "" {
		return fmt.Errorf("--tls-cert requires --tls-key")
	}
	if tlsKey != "" && tlsCert == "" {
		return fmt.Errorf("--tls-key requires --tls-cert")
	}
	if tlsCert != "" {
		tls = true // BYO cert implies TLS
	}

	// --conf-file is mutually exclusive with --upstream
	if confFile != "" && len(backendSpecs) > 0 {
		return fmt.Errorf("--conf-file is mutually exclusive with --upstream")
	}

	// Validate --conf-file if provided
	if confFile != "" {
		info, err := os.Stat(confFile)
		if os.IsNotExist(err) {
			return fmt.Errorf("--conf-file: %s not found", confFile)
		}
		if err != nil {
			return fmt.Errorf("--conf-file: %w", err)
		}
		if info.Size() == 0 {
			return fmt.Errorf("--conf-file: %s is empty", confFile)
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

	// Build backend list: explicit --upstream specs + convenience flags
	// (skipped when --conf-file is used)
	var backends []config.NginxBackend
	if confFile == "" {
		backends = make([]config.NginxBackend, 0, len(backendSpecs))
		for _, s := range backendSpecs {
			b, err := parseNginxBackend(s)
			if err != nil {
				return err
			}
			backends = append(backends, b)
		}

		if len(backends) == 0 {
			return fmt.Errorf("no backends: pass --upstream name=...,path=...,backend=... (repeatable), or --conf-file for a custom nginx.conf")
		}
	}

	if cfg.Addons.Nginx == nil {
		cfg.Addons.Nginx = make(map[string]config.NginxConfig)
	}
	existing, ok := cfg.Addons.Nginx[name]
	if !ok {
		existing = config.NginxConfig{
			ContainerName: "pgcli-nginx" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	if httpPort != 0 {
		existing.HTTPPort = httpPort
	}
	if httpsPort != 0 {
		existing.HTTPSPort = httpsPort
	}
	if listenAddr, _ := cmd.Flags().GetString("listen"); listenAddr != "" {
		existing.Listen = listenAddr
	}
	existing.TLS = tls
	existing.TLSCert = tlsCert
	existing.TLSKey = tlsKey
	existing.WorkerConnections = workerConns
	existing.Backends = backends
	existing.ConfFile = confFile
	if existing.Name == "" {
		existing.Name = name
	}
	cfg.Addons.Nginx[name] = existing

	cfg.ApplyDefaults()
	nc := cfg.Addons.Nginx[name]

	nm, err := podman.NewNginxManager(cfg)
	if err != nil {
		return fmt.Errorf("nginx manager: %w", err)
	}

	fmt.Println("-> Pulling nginx image...")
	if err := nm.EnsureImage(nc.ImageTag); err != nil {
		return err
	}

	// Snapshot the existing config on disk before overwriting (for diff + rollback).
	baseDir := cfg.BaseDir
	if baseDir == "" {
		baseDir = platform.DefaultConfigDir()
	}
	oldConfPath := filepath.Join(baseDir, "addon", "nginx", name, "nginx.conf")
	oldConf, _ := os.ReadFile(oldConfPath)

	cfgPathOut, err := nm.WriteConfigs(&nc)
	if err != nil {
		return err
	}

	force, _ := cmd.Flags().GetBool("force")
	if !force {
		running, err := nm.ContainerRunning(nc.ContainerName)
		if err == nil && running {
			// Config on disk has been updated by WriteConfigs above.
			// If it actually changed, auto-test and reload (rollback on failure).
			newConf, _ := os.ReadFile(cfgPathOut)
			if len(oldConf) > 0 && string(oldConf) != string(newConf) {
				fmt.Println("-> Config changed, testing new configuration...")
				if testErr := nm.Test(&nc); testErr != nil {
					// Rollback: restore old config
					_ = os.WriteFile(cfgPathOut, oldConf, 0644)
					return fmt.Errorf("config validation failed, rolled back: %w", testErr)
				}
				fmt.Println("-> Reloading nginx...")
				if reloadErr := nm.Reload(&nc); reloadErr != nil {
					_ = os.WriteFile(cfgPathOut, oldConf, 0644)
					return fmt.Errorf("reload failed, rolled back config: %w", reloadErr)
				}
				fmt.Println("  ✓ Config reloaded successfully")
			} else {
				fmt.Println("  [OK] nginx running, config unchanged")
			}
			fmt.Println()
			fmt.Printf("✓ nginx installed: %q\n", name)
			fmt.Printf("  Container:    %s\n", nc.ContainerName)
			fmt.Printf("  Image:        %s\n", nc.ImageTag)
			fmt.Printf("  HTTP:         http://%s:%d/\n", nc.Listen, nc.HTTPPort)
			if nc.TLS && nc.HTTPSPort > 0 {
				fmt.Printf("  HTTPS:        https://%s:%d/\n", nc.Listen, nc.HTTPSPort)
			}
			if nc.ConfFile != "" {
				fmt.Printf("  Config:       %s (from %s)\n", cfgPathOut, nc.ConfFile)
			} else {
				fmt.Printf("  Backends:     %d\n", len(nc.Backends))
				for _, b := range nc.Backends {
					fmt.Printf("    %s -> %s%s\n", b.Path, strings.Join(b.Backends, ", "), " ("+b.Name+")")
				}
				fmt.Printf("  Config:       %s\n", cfgPathOut)
			}
			fmt.Printf("  Logs:         %s\n", filepath.Dir(cfgPathOut)+"/log/")
			return nil
		}
	}

	fmt.Println("-> Starting nginx container...")
	if err := nm.EnsureContainer(&nc); err != nil {
		return err
	}

	cfg.Addons.Nginx[name] = nc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	fmt.Printf("✓ nginx installed: %q\n", name)
	fmt.Printf("  Container:    %s\n", nc.ContainerName)
	fmt.Printf("  Image:        %s\n", nc.ImageTag)
	fmt.Printf("  HTTP:         http://%s:%d/\n", nc.Listen, nc.HTTPPort)
	if nc.TLS && nc.HTTPSPort > 0 {
		fmt.Printf("  HTTPS:        https://%s:%d/\n", nc.Listen, nc.HTTPSPort)
		if nc.TLSCert == "" {
			fmt.Printf("  TLS cert:     self-signed (ca.crt under %s)\n", filepath.Dir(cfgPathOut)+"/tls")
		} else {
			fmt.Printf("  TLS cert:     BYO (%s)\n", nc.TLSCert)
		}
	}
	if nc.ConfFile != "" {
		fmt.Printf("  Config:       %s (from %s)\n", cfgPathOut, nc.ConfFile)
	} else {
		fmt.Printf("  Backends:     %d\n", len(nc.Backends))
		for _, b := range nc.Backends {
			fmt.Printf("    %s -> %s%s\n", b.Path, strings.Join(b.Backends, ", "), " ("+b.Name+")")
		}
		fmt.Printf("  Config:       %s\n", cfgPathOut)
	}
	fmt.Printf("  Logs:         %s\n", filepath.Dir(cfgPathOut)+"/log/")
	return nil
}

// runAddonRemoveNginx removes an nginx reverse proxy container and its config
// directory (rendered nginx.conf + optional TLS certs).
func runAddonRemoveNginx(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "nginx"
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

	if cfg.Addons.Nginx == nil {
		return fmt.Errorf("no nginx add-ons configured")
	}
	nc, ok := cfg.Addons.Nginx[name]
	if !ok {
		return fmt.Errorf("nginx %q not found", name)
	}

	nm, err := podman.NewNginxManager(cfg)
	if err != nil {
		return fmt.Errorf("nginx manager: %w", err)
	}

	fmt.Printf("-> Removing nginx %q...\n", name)
	if err := nm.Remove(&nc); err != nil {
		return err
	}

	delete(cfg.Addons.Nginx, name)
	if len(cfg.Addons.Nginx) == 0 {
		cfg.Addons.Nginx = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ nginx %q removed\n", name)
	return nil
}

// runAddonStartNginx starts a stopped nginx container without regenerating
// config (autostart-on-boot path).
func runAddonStartNginx(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "nginx"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.Nginx == nil {
		return fmt.Errorf("no nginx add-ons configured (run 'pg addon install nginx')")
	}
	nc, ok := cfg.Addons.Nginx[name]
	if !ok {
		return fmt.Errorf("nginx %q not found (run 'pg addon install nginx --name %s')", name, name)
	}
	nm, err := podman.NewNginxManager(cfg)
	if err != nil {
		return fmt.Errorf("nginx manager: %w", err)
	}
	fmt.Printf("-> Starting nginx %q...\n", name)
	return nm.StartContainer(&nc)
}

// runAddonStopNginx stops a running nginx container.
func runAddonStopNginx(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "nginx"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	nc, ok := cfg.Addons.Nginx[name]
	if !ok {
		return fmt.Errorf("nginx %q not found", name)
	}
	nm, err := podman.NewNginxManager(cfg)
	if err != nil {
		return fmt.Errorf("nginx manager: %w", err)
	}
	running, _ := nm.ContainerRunning(nc.ContainerName)
	if !running {
		fmt.Printf("nginx %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping nginx %q...\n", name)
	if _, err := nm.Stop(nc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ nginx %q stopped\n", name)
	return nil
}

// runAddonRemovePredixy removes a Predixy proxy container and its rendered
// config dir. There is no data dir to keep or clean — the proxy is stateless —
// so --clean-data does not apply.
func runAddonRemovePredixy(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "predixy"
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

	if cfg.Addons.Predixy == nil {
		return fmt.Errorf("no predixy add-ons configured")
	}
	pc, ok := cfg.Addons.Predixy[name]
	if !ok {
		return fmt.Errorf("predixy %q not found", name)
	}

	pm, err := podman.NewPredixyManager(cfg)
	if err != nil {
		return fmt.Errorf("predixy manager: %w", err)
	}

	fmt.Printf("-> Removing predixy %q...\n", name)
	if err := pm.Remove(&pc); err != nil {
		return err
	}

	delete(cfg.Addons.Predixy, name)
	if len(cfg.Addons.Predixy) == 0 {
		cfg.Addons.Predixy = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ predixy %q removed\n", name)
	return nil
}

func runAddonStartPredixy(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "predixy"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.Predixy == nil {
		return fmt.Errorf("no predixy add-ons configured (run 'pg addon install predixy')")
	}
	pc, ok := cfg.Addons.Predixy[name]
	if !ok {
		return fmt.Errorf("predixy %q not found (run 'pg addon install predixy --name %s')", name, name)
	}
	pm, err := podman.NewPredixyManager(cfg)
	if err != nil {
		return fmt.Errorf("predixy manager: %w", err)
	}
	fmt.Printf("-> Starting predixy %q...\n", name)
	return pm.StartContainer(&pc)
}

func runAddonStopPredixy(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "predixy"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	pc, ok := cfg.Addons.Predixy[name]
	if !ok {
		return fmt.Errorf("predixy %q not found", name)
	}
	pm, err := podman.NewPredixyManager(cfg)
	if err != nil {
		return fmt.Errorf("predixy manager: %w", err)
	}
	running, _ := pm.ContainerRunning(pc.ContainerName)
	if !running {
		fmt.Printf("predixy %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping predixy %q...\n", name)
	if _, err := pm.Stop(pc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ predixy %q stopped\n", name)
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

// runAddonRemovePgAdmin removes a pgAdmin container. The data dir (the
// config/session DB) is kept by default so reinstalling under the same name
// revives the saved servers; --clean-data deletes it through the manager's
// removeHostDir path (the files are owned by the container's mapped 5050 uid).
func runAddonRemovePgAdmin(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "pgadmin"
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

	if cfg.Addons.PgAdmin == nil {
		return fmt.Errorf("no pgAdmin add-ons configured")
	}
	ac, ok := cfg.Addons.PgAdmin[name]
	if !ok {
		return fmt.Errorf("pgAdmin %q not found", name)
	}

	pgm, err := podman.NewPgAdminManager(cfg)
	if err != nil {
		return fmt.Errorf("pgAdmin manager: %w", err)
	}

	fmt.Printf("-> Removing pgAdmin %q...\n", name)
	if err := pgm.Remove(&ac, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.PgAdmin, name)
	if len(cfg.Addons.PgAdmin) == 0 {
		cfg.Addons.PgAdmin = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ pgAdmin %q removed\n", name)
	if !cleanData {
		fmt.Printf("  (data dir kept — reinstall the same name revives the saved servers; --clean-data to delete it)\n")
	}
	return nil
}

func runAddonStartPgAdmin(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "pgadmin"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.PgAdmin == nil {
		return fmt.Errorf("no pgAdmin add-ons configured (run 'pg addon install pgadmin')")
	}
	ac, ok := cfg.Addons.PgAdmin[name]
	if !ok {
		return fmt.Errorf("pgAdmin %q not found (run 'pg addon install pgadmin --name %s')", name, name)
	}
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}
	pgm, err := podman.NewPgAdminManager(cfg)
	if err != nil {
		return fmt.Errorf("pgAdmin manager: %w", err)
	}
	fmt.Printf("-> Starting pgAdmin %q...\n", name)
	return pgm.StartContainer(&ac)
}

func runAddonStopPgAdmin(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "pgadmin"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	ac, ok := cfg.Addons.PgAdmin[name]
	if !ok {
		return fmt.Errorf("pgAdmin %q not found", name)
	}
	pgm, err := podman.NewPgAdminManager(cfg)
	if err != nil {
		return fmt.Errorf("pgAdmin manager: %w", err)
	}
	running, _ := pgm.ContainerRunning(ac.ContainerName)
	if !running {
		fmt.Printf("pgAdmin %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping pgAdmin %q...\n", name)
	if _, err := pgm.Stop(ac.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ pgAdmin %q stopped\n", name)
	return nil
}

// runAddonRemoveGrafana removes a Grafana container. The data dir (grafana.db)
// is kept unless --clean-data, so remove+reinstall revives saved dashboards and
// the original web login.
func runAddonRemoveGrafana(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "grafana"
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

	if cfg.Addons.Grafana == nil {
		return fmt.Errorf("no Grafana add-ons configured")
	}
	gc, ok := cfg.Addons.Grafana[name]
	if !ok {
		return fmt.Errorf("Grafana %q not found", name)
	}

	gm, err := podman.NewGrafanaManager(cfg)
	if err != nil {
		return fmt.Errorf("Grafana manager: %w", err)
	}

	fmt.Printf("-> Removing Grafana %q...\n", name)
	if err := gm.Remove(&gc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.Grafana, name)
	if len(cfg.Addons.Grafana) == 0 {
		cfg.Addons.Grafana = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ Grafana %q removed\n", name)
	if !cleanData {
		fmt.Printf("  (data dir kept — reinstall the same name revives the saved dashboards and login; --clean-data to delete it)\n")
	}
	return nil
}

func runAddonStartGrafana(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "grafana"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.Grafana == nil {
		return fmt.Errorf("no Grafana add-ons configured (run 'pg addon install grafana')")
	}
	gc, ok := cfg.Addons.Grafana[name]
	if !ok {
		return fmt.Errorf("Grafana %q not found (run 'pg addon install grafana --name %s')", name, name)
	}
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}
	gm, err := podman.NewGrafanaManager(cfg)
	if err != nil {
		return fmt.Errorf("Grafana manager: %w", err)
	}
	fmt.Printf("-> Starting Grafana %q...\n", name)
	return gm.StartContainer(&gc)
}

func runAddonStopGrafana(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "grafana"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	gc, ok := cfg.Addons.Grafana[name]
	if !ok {
		return fmt.Errorf("Grafana %q not found", name)
	}
	gm, err := podman.NewGrafanaManager(cfg)
	if err != nil {
		return fmt.Errorf("Grafana manager: %w", err)
	}
	running, _ := gm.ContainerRunning(gc.ContainerName)
	if !running {
		fmt.Printf("Grafana %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping Grafana %q...\n", name)
	if _, err := gm.Stop(gc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ Grafana %q stopped\n", name)
	return nil
}

// runAddonRemoveGitea removes a Gitea container. The data dir (repos, app.ini
// with Gitea's self-generated crypto secrets, and the default SQLite DB) is
// kept unless --clean-data, so remove+reinstall revives everything — including
// the admin login, which is why a reinstall prints an inert-password NOTE
// rather than silently "changing" it.
func runAddonRemoveGitea(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "gitea"
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

	if cfg.Addons.Gitea == nil {
		return fmt.Errorf("no Gitea add-ons configured")
	}
	gc, ok := cfg.Addons.Gitea[name]
	if !ok {
		return fmt.Errorf("Gitea %q not found", name)
	}

	gm, err := podman.NewGiteaManager(cfg)
	if err != nil {
		return fmt.Errorf("Gitea manager: %w", err)
	}

	fmt.Printf("-> Removing Gitea %q...\n", name)
	if err := gm.Remove(&gc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.Gitea, name)
	if len(cfg.Addons.Gitea) == 0 {
		cfg.Addons.Gitea = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ Gitea %q removed\n", name)
	if !cleanData {
		fmt.Printf("  (data dir kept — reinstall the same name revives repos and settings; --clean-data to delete it. With --dsn the external database is never touched either way.)\n")
	}
	return nil
}

func runAddonStartGitea(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "gitea"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.Gitea == nil {
		return fmt.Errorf("no Gitea add-ons configured (run 'pg addon install gitea')")
	}
	gc, ok := cfg.Addons.Gitea[name]
	if !ok {
		return fmt.Errorf("Gitea %q not found (run 'pg addon install gitea --name %s')", name, name)
	}
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}
	gm, err := podman.NewGiteaManager(cfg)
	if err != nil {
		return fmt.Errorf("Gitea manager: %w", err)
	}
	fmt.Printf("-> Starting Gitea %q...\n", name)
	return gm.StartContainer(&gc)
}

func runAddonStopGitea(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "gitea"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	gc, ok := cfg.Addons.Gitea[name]
	if !ok {
		return fmt.Errorf("Gitea %q not found", name)
	}
	gm, err := podman.NewGiteaManager(cfg)
	if err != nil {
		return fmt.Errorf("Gitea manager: %w", err)
	}
	running, _ := gm.ContainerRunning(gc.ContainerName)
	if !running {
		fmt.Printf("Gitea %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping Gitea %q...\n", name)
	if _, err := gm.Stop(gc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ Gitea %q stopped\n", name)
	return nil
}

// runAddonInstallGitea installs a Gitea container — the self-hosted Git
// service — from the official docker.io/gitea image. Like Grafana it is a
// top-level-only addon (addons.gitea.<name>, key defaults to "gitea") with a
// web login and a persistent data dir; configuration is env-only through the
// image's GITEA__<section>__<KEY> mechanism (the same shape as Grafana's GF_*,
// folded into app.ini at every boot by the image's environment-to-ini).
//
// Two divergences from the Grafana template shape the install flow:
//   - The admin LOGIN is not an env var. INSTALL_LOCK=true skips the web setup
//     wizard (an empty instance boots straight to the sign-in page), and the
//     account is created afterwards via `gitea admin user create` inside the
//     running container (GiteaManager.CreateAdmin) — so install waits for the
//     first-boot migrations, then creates the admin. Password is generated
//     here when empty, never in ApplyDefaults (which must stay deterministic).
//     On a revived store (or when --dsn points at a database that already has
//     the account) creation is skipped and the printed login stays what the
//     store carries — same inert-password caveat as Grafana, detected with
//     AdminExists instead of the SQLite file stat because a --dsn store is not
//     visible from the host.
//   - SECRET_KEY/INTERNAL_TOKEN are deliberately never injected: env
//     re-application into the PERSISTED app.ini would re-key a surviving
//     database's encrypted 2FA seeds/OAuth secrets on every reinstall —
//     silent, unrecoverable. The volume owns crypto state; pgcli owns only
//     the admin login printed below.
func runAddonInstallGitea(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "gitea"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	port, _ := cmd.Flags().GetInt("port")
	sshPort, _ := cmd.Flags().GetInt("ssh-port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	adminUser, _ := cmd.Flags().GetString("admin-user")
	password, _ := cmd.Flags().GetString("password")
	dsn, _ := cmd.Flags().GetString("dsn")
	allowReg, _ := cmd.Flags().GetBool("allow-registration")
	force, _ := cmd.Flags().GetBool("force")

	// --dsn is validated BEFORE anything is written: an unparseable DSN would
	// otherwise land in pg.yaml and boot a Gitea that cannot reach its
	// database (the podmanArgs builder logs-and-falls-back-to-SQLite as a last
	// resort, which is exactly the silent surprise to prevent here). The
	// database itself must already exist — pgcli does not create it, Gitea
	// creates its schema on first boot (same contract as PostgREST --dsn).
	if dsn != "" {
		if _, _, _, _, _, err := podman.ParseDSN(dsn); err != nil {
			return fmt.Errorf("invalid --dsn for Gitea: %w", err)
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

	if cfg.Addons.Gitea == nil {
		cfg.Addons.Gitea = make(map[string]config.GiteaConfig)
	}
	existing, ok := cfg.Addons.Gitea[name]
	if !ok {
		existing = config.GiteaConfig{
			ContainerName: "pgcli-gitea" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if dataDir != "" {
		existing.DataDir = dataDir
	}
	if cmd.Flags().Changed("port") {
		existing.HostPort = port
	}
	if cmd.Flags().Changed("ssh-port") {
		// --ssh-port N (N>0) makes the listener live: git-over-ssh clones on
		// host port N. 0 disables live cloning — but the image's s6 sshd is
		// never turn-off-able, so the port is not cleared here either; it just
		// becomes where that always-running sshd parks instead. An unchanged
		// config entry keeps its stored value (so a plain re-install does not
		// silently drop SSH), and a stored SSHPort==0 gets a pool assignment
		// from ApplyDefaults below.
		existing.SSHPort = sshPort
		existing.SSH = sshPort > 0
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if adminUser != "" {
		existing.AdminUser = adminUser
	}
	if password != "" {
		existing.Password = password
	}
	if dsn != "" {
		existing.Dsn = dsn
	}
	if cmd.Flags().Changed("allow-registration") {
		// Gated on Changed like --ssh-port: a plain re-install without the flag
		// must not silently close registration on an instance opened with it.
		// Flipping the stored value only affects the live container through the
		// --force recreate below (the toggle rides env into app.ini).
		existing.AllowRegistration = allowReg
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	// NB: SSHPort is deliberately NOT zeroed when SSH is off. The image's s6
	// sshd always runs, so an SSH-off instance still needs a port to park it on
	// (off 22); ApplyDefaults below allocates one for every gitea instance.
	cfg.Addons.Gitea[name] = existing

	// ApplyDefaults fills ContainerName/Listen/AdminUser and assigns HostPort
	// (plus SSHPort from the second pool, for every instance — SSH on gets a
	// live listener, SSH off gets an idle parked sshd) from the
	// gitea_start_port / gitea_ssh_start_port pools. Password is NOT managed
	// there — it is a secret the install path owns.
	cfg.ApplyDefaults()
	gc := cfg.Addons.Gitea[name]

	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	gm, err := podman.NewGiteaManager(cfg)
	if err != nil {
		return fmt.Errorf("Gitea manager: %w", err)
	}

	// Reuse semantics identical to Grafana: a live container is left alone, a
	// stopped one is started, and only --force recreates it so changed
	// port/listen/admin-user/ssh/dsn take effect.
	exists, err := gm.ContainerExists(gc.ContainerName)
	if err != nil {
		return err
	}
	skipped := false
	if exists && !force {
		skipped = true
		if running, _ := gm.ContainerRunning(gc.ContainerName); running {
			fmt.Printf("-> Gitea container %q already running; skipping creation\n", gc.ContainerName)
		} else {
			fmt.Printf("-> Gitea container %q exists but is stopped; starting it\n", gc.ContainerName)
			if err := gm.StartContainer(&gc); err != nil {
				return err
			}
		}
	} else {
		// Fresh install, or a reinstall after a `remove` that kept the data
		// dir. Generate the login password when none is stored. The revive
		// check is the DB the INSTANCE reports, not a host-side file stat:
		// with --dsn the metadata lives in the external Postgres where
		// GiteaStoreExists cannot see. Ask the container after it is up
		// (AdminExists below) — but the password must already be decided to
		// create the admin with, so on a revive the printed login simply
		// stops being this one, same note as Grafana's.
		if gc.Password == "" {
			pw, err := generatePassword(20)
			if err != nil {
				return fmt.Errorf("generating Gitea password: %w", err)
			}
			gc.Password = pw
		}
		// SQLite-only cheap pre-check, kept for the NOTE wording parity on a
		// revived local store; the authoritative gate is AdminExists below.
		revived := gc.Dsn == "" && podman.GiteaStoreExists(gm.DataDir(&gc))
		fmt.Printf("-> Preparing Gitea image %s...\n", gc.ImageTag)
		if err := gm.EnsureImage(gc.ImageTag); err != nil {
			return err
		}
		if revived {
			fmt.Println("-> NOTE: reviving an existing Gitea data dir — its stored admin login still applies; the password below only takes effect if that account does not exist (change it inside the container with `gitea admin user change-password`).")
		}
		fmt.Println("-> Starting Gitea container...")
		if err := gm.EnsureContainer(&gc); err != nil {
			return err
		}
		// First boot runs migrations (app.ini keys, SQLite DB or Postgres
		// schema) before the CLI answers; CreateAdmin must wait for that.
		fmt.Println("-> Waiting for Gitea to initialize...")
		if err := gm.WaitForReady(&gc, 2*time.Minute); err != nil {
			return err
		}
		adminExists, err := gm.AdminExists(&gc)
		if err != nil {
			return fmt.Errorf("checking Gitea admin user: %w", err)
		}
		if !adminExists {
			fmt.Printf("-> Creating admin user %q...\n", gc.AdminUser)
			if err := gm.CreateAdmin(&gc); err != nil {
				return err
			}
		}
	}

	cfg.Addons.Gitea[name] = gc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	if skipped {
		fmt.Printf("✓ Gitea already present: %q\n", name)
	} else {
		fmt.Printf("✓ Gitea installed: %q\n", name)
	}
	fmt.Printf("  Container:  %s\n", gc.ContainerName)
	fmt.Printf("  Image:      %s\n", gc.ImageTag)
	fmt.Printf("  Data:       %s\n", gm.DataDir(&gc))
	if gc.Dsn != "" {
		fmt.Printf("  Database:   %s (external PostgreSQL via dsn)\n", dsnDisplay(gc.Dsn))
	}
	fmt.Printf("  Web URL:    http://%s:%d/\n", podman.GiteaDisplayHost(gc.Listen), gc.HostPort)
	if gc.SSH {
		fmt.Printf("  Git SSH:    ssh://git@%s:%d/<owner>/<repo>.git\n", podman.GiteaDisplayHost(gc.Listen), gc.SSHPort)
	} else {
		fmt.Println("  Git SSH:    disabled (HTTP clone/push only; enable with --ssh-port N --force)")
	}
	fmt.Println()
	fmt.Printf("  Login user:     %s\n", gc.AdminUser)
	fmt.Printf("  Login password: %s\n", gc.Password)
	fmt.Println("  (Web login only — not a PostgreSQL password. Retrieve it later with `pg addon password gitea`.)")
	fmt.Println()
	if gc.AllowRegistration {
		fmt.Println("  Self-registration: OPEN (--allow-registration; the web UI shows a Register entry — signups are always NORMAL users, never admins)")
	} else {
		fmt.Println("  Self-registration: closed (open it with --allow-registration --force; accounts via `pg addon gitea user create`)")
	}
	fmt.Println()
	fmt.Println("  Create your first org/repo in the web UI; the admin above has site-admin rights.")
	if gc.Listen == "0.0.0.0" {
		if gc.AllowRegistration {
			fmt.Println("  NOTE: listening on every interface AND self-registration is open — anyone who can reach the port can create accounts (normal users only, but expect spam). Close it again (--allow-registration=false --force) or bind loopback.")
		} else {
			fmt.Println("  NOTE: listening on every interface (a Git host is usually reached from other machines); the login above and closed self-registration are the only gate.")
		}
		fmt.Printf("        loopback-only: pg addon install gitea --name %s --listen 127.0.0.1 --force\n", name)
	}
	return nil
}

// runAddonInstallPrometheus installs a Prometheus container — the metrics
// collection backend — from the official docker.io/prom/prometheus image. Like
// Grafana it is a top-level-only addon (addons.prometheus.<name>, key defaults to
// "prometheus") on one host port with a persistent TSDB dir. Unlike Grafana it
// is configured by a RENDERED prometheus.yml (self-scrape + one job per
// --scrape target) or, with --conf-file, the operator's own file — plus CLI
// flags passed after the image. There is no login/password: Prometheus ships no
// authentication, so the only gate on the default 0.0.0.0 listen is the network
// boundary (see the exposure NOTE below).
//
// Install follows the nginx "render → write → reload-if-changed" flow: a running
// instance whose config actually changed is hot-reloaded (SIGHUP) instead of
// recreated; --force or a stopped/absent container takes the recreate path.
func runAddonInstallPrometheus(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "prometheus"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	port, _ := cmd.Flags().GetInt("port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	retention, _ := cmd.Flags().GetString("retention")
	scrapeInterval, _ := cmd.Flags().GetString("scrape-interval")
	scrapeSpecs, _ := cmd.Flags().GetStringArray("scrape")
	scrapeAppendSpecs, _ := cmd.Flags().GetStringArray("scrape-append")
	amURLs, _ := cmd.Flags().GetStringArray("alertmanager-url")
	ruleFiles, _ := cmd.Flags().GetStringArray("rule-file")
	confFile, _ := cmd.Flags().GetString("conf-file")
	force, _ := cmd.Flags().GetBool("force")

	// --scrape (declare the full set) and --scrape-append (merge into the stored
	// set) are two views of the same list — passing both is contradictory.
	if cmd.Flags().Changed("scrape") && cmd.Flags().Changed("scrape-append") {
		return fmt.Errorf("--scrape and --scrape-append are mutually exclusive (one declares the full target set, the other merges into it)")
	}

	// --conf-file replaces the rendered config, so it is mutually exclusive with
	// the render knobs --scrape/--scrape-append/--scrape-interval/--alertmanager-url
	// (an operator file owns its alerting: section too). (--retention and
	// --listen are CLI flags, and --rule-file only feeds a managed mount whose
	// use the operator's file decides, so all three stay valid alongside it.)
	if confFile != "" && (len(scrapeSpecs) > 0 || len(scrapeAppendSpecs) > 0 || cmd.Flags().Changed("scrape-interval") || len(amURLs) > 0) {
		return fmt.Errorf("--conf-file is mutually exclusive with --scrape/--scrape-append/--scrape-interval/--alertmanager-url (they configure the rendered prometheus.yml) — write your own alerting: section in the file instead")
	}
	if confFile != "" {
		info, err := os.Stat(confFile)
		if os.IsNotExist(err) {
			return fmt.Errorf("--conf-file: %s not found", confFile)
		}
		if err != nil {
			return fmt.Errorf("--conf-file: %w", err)
		}
		if info.Size() == 0 {
			return fmt.Errorf("--conf-file: %s is empty", confFile)
		}
	}

	// Validate the NEW flags up front, like --webhook-url does: a malformed URL
	// or a missing/empty rule file aborts before any managed file is rewritten.
	// No connectivity probe — Prometheus reports a dead alertmanager as DOWN in
	// its own UI; the format check is all install-time signal there is.
	if len(amURLs) > 0 {
		if err := podman.ValidateAlertmanagerURLs(amURLs); err != nil {
			return fmt.Errorf("--alertmanager-url: %w", err)
		}
	}
	for _, rf := range ruleFiles {
		info, err := os.Stat(rf)
		if os.IsNotExist(err) {
			return fmt.Errorf("--rule-file: %s not found", rf)
		}
		if err != nil {
			return fmt.Errorf("--rule-file: %w", err)
		}
		if info.IsDir() {
			return fmt.Errorf("--rule-file: %s is a directory — pass individual rule files (a glob expands to them)", rf)
		}
		if info.Size() == 0 {
			return fmt.Errorf("--rule-file: %s is empty", rf)
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

	if cfg.Addons.Prometheus == nil {
		cfg.Addons.Prometheus = make(map[string]config.PrometheusConfig)
	}
	existing, ok := cfg.Addons.Prometheus[name]
	if !ok {
		existing = config.PrometheusConfig{
			ContainerName: "pgcli-prometheus" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if dataDir != "" {
		existing.DataDir = dataDir
	}
	if cmd.Flags().Changed("port") {
		existing.HostPort = port
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if retention != "" {
		existing.Retention = retention
	}
	if cmd.Flags().Changed("scrape-interval") {
		existing.ScrapeInterval = scrapeInterval
	}
	if cmd.Flags().Changed("scrape") {
		existing.Scrape = scrapeSpecs
	}
	if cmd.Flags().Changed("scrape-append") {
		merged, err := podman.MergeScrapeSpecs(existing.Scrape, scrapeAppendSpecs)
		if err != nil {
			return err
		}
		existing.Scrape = merged
	}
	// alertmanager and rule_files are set or cleared wholesale like conf_file —
	// they have no append form (an alertmanager list doubles as the derived
	// scrape job's target set, and rule files arrive via shell globs, so merging
	// stored and passed lists would silently resurrect dropped entries). The
	// pre-update emptiness is remembered: flipping it changes the container's
	// MOUNT set, which no SIGHUP can apply (see the recreate branch below).
	hadRules := len(existing.RuleFiles) > 0
	if cmd.Flags().Changed("alertmanager-url") {
		existing.Alertmanager = amURLs
	}
	if cmd.Flags().Changed("rule-file") {
		existing.RuleFiles = ruleFiles
	}
	// conf_file is set or cleared wholesale: passing --conf-file switches to
	// operator-file mode; omitting it reverts to render mode (nil-out below).
	existing.ConfFile = confFile
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	cfg.Addons.Prometheus[name] = existing

	// ApplyDefaults fills ContainerName/Listen/Retention/ScrapeInterval and
	// assigns HostPort from the prometheus_start_port pool. No secret is involved.
	cfg.ApplyDefaults()
	pc := cfg.Addons.Prometheus[name]

	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	pm, err := podman.NewPrometheusManager(cfg)
	if err != nil {
		return fmt.Errorf("Prometheus manager: %w", err)
	}

	// Scrape-target probe. The two flags differ on purpose: a freshly appended
	// target (--scrape-append) that yields no data is a HARD error — you just
	// declared you want it scraped, so a dead endpoint is a mistake worth aborting
	// on (and the container is left untouched, config un-written). A full-set
	// declaration (--scrape) only WARNS, because that list is idempotent
	// re-declared state and one transiently-down exporter must not block a
	// reinstall; Prometheus surfaces DOWN targets on its own anyway.
	//
	// Skipped on macOS: the probe dials from the HOST, but under the bridge the
	// container's 127.0.0.1 is its own loopback, so a host-side verdict would be
	// wrong (it would reject or warn about targets the container can reach).
	if platform.Detect() != platform.MacOS {
		if cmd.Flags().Changed("scrape-append") {
			unreachable, perr := podman.ProbeScrapeTargets(scrapeAppendSpecs)
			if perr != nil {
				return perr
			}
			if len(unreachable) > 0 {
				return fmt.Errorf("--scrape-append target(s) returned no data (nothing written; existing instance untouched):\n  - %s",
					strings.Join(unreachable, "\n  - "))
			}
		} else if cmd.Flags().Changed("scrape") {
			unreachable, perr := podman.ProbeScrapeTargets(scrapeSpecs)
			if perr != nil {
				return perr
			}
			for _, line := range unreachable {
				fmt.Printf("  [!] scrape target unreachable (installing anyway — Prometheus will show it DOWN): %s\n", line)
			}
		}
	}

	fmt.Printf("-> Preparing Prometheus image %s...\n", pc.ImageTag)
	if err := pm.EnsureImage(pc.ImageTag); err != nil {
		return err
	}

	// Snapshot the mounted prometheus.yml before overwriting, so a running
	// instance whose config actually changed can be reloaded instead of recreated.
	cfgPathOut := pm.ConfigPath(&pc)
	oldConf, _ := os.ReadFile(cfgPathOut)

	if _, err := pm.WriteConfig(&pc); err != nil {
		return err
	}

	running, err := pm.ContainerRunning(pc.ContainerName)
	if err != nil {
		return err
	}
	// A --rule-file set going from empty to non-empty (or back) changes the
	// container's MOUNT set — the read-only /etc/prometheus/rules bind exists
	// only while there are rule files. SIGHUP rereads the config but cannot add
	// a mount, so that transition (and its reverse) recreates the container even
	// without --force. Losing scrapes for a few seconds beats a Prometheus whose
	// rule_files: points at an unmounted directory.
	rulesMountChanged := hadRules != (len(pc.RuleFiles) > 0)
	reloaded := false
	if running && !force && !rulesMountChanged {
		// Rendered/copied config is already refreshed on disk. If it differs, hot
		// reload (SIGHUP); a broken conf-file would surface in the logs but never
		// kills the running server (Prometheus keeps the last good config).
		newConf, _ := os.ReadFile(cfgPathOut)
		if string(oldConf) != string(newConf) {
			fmt.Println("-> Config changed, reloading Prometheus (SIGHUP)...")
			if err := pm.SignalReload(&pc); err != nil {
				return err
			}
			reloaded = true
		} else {
			fmt.Println("  [OK] Prometheus running, config unchanged")
		}
	} else {
		fmt.Println("-> Starting Prometheus container...")
		if err := pm.EnsureContainer(&pc); err != nil {
			return err
		}
	}

	cfg.Addons.Prometheus[name] = pc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	switch {
	case !running:
		fmt.Printf("✓ Prometheus installed: %q\n", name)
	case force || rulesMountChanged:
		fmt.Printf("✓ Prometheus %q recreated (%s)\n", name, map[bool]string{true: "--force", false: "rules mount set changed"}[force])
	case reloaded:
		fmt.Printf("✓ Prometheus %q (config reloaded)\n", name)
	default:
		fmt.Printf("✓ Prometheus %q (unchanged)\n", name)
	}
	fmt.Printf("  Container:  %s\n", pc.ContainerName)
	fmt.Printf("  Image:      %s\n", pc.ImageTag)
	fmt.Printf("  Data:       %s\n", pm.DataDir(&pc))
	fmt.Printf("  Config:     %s", cfgPathOut)
	if pc.ConfFile != "" {
		fmt.Printf(" (from --conf-file %s)\n", pc.ConfFile)
	} else {
		jobs := len(pc.Scrape)
		if len(pc.Alertmanager) > 0 {
			jobs++ // the derived alertmanager scrape job
		}
		fmt.Printf(" (rendered: 1 self job + %d scrape job(s))\n", jobs)
	}
	if len(pc.Alertmanager) > 0 {
		fmt.Printf("  Alerts:     %s\n", strings.Join(pc.Alertmanager, ", "))
	}
	if len(pc.RuleFiles) > 0 {
		ref := ", referenced by the rendered rule_files"
		if pc.ConfFile != "" {
			ref = " — your conf-file decides whether to reference them"
		}
		fmt.Printf("  Rules:      %d file(s) copied to %s (mounted at /etc/prometheus/rules%s)\n",
			len(pc.RuleFiles), pm.RulesDir(&pc), ref)
	}
	fmt.Printf("  Retention:  %s\n", pc.Retention)
	fmt.Printf("  URL:        http://%s:%d/\n", pc.Listen, pc.HostPort)
	fmt.Println()
	if pc.Listen == "0.0.0.0" {
		fmt.Println("  NOTE: listening on every interface. Prometheus has NO authentication — its HTTP API runs arbitrary")
		fmt.Println("        PromQL, so this endpoint is guarded only by your firewall, not a login. Point Grafana at it,")
		fmt.Println("        but restrict exposure with `pg addon install prometheus --name <name> --listen 127.0.0.1 --force`.")
	}
	return nil
}

// resolvePrometheusConfig loads the config and returns the PrometheusConfig for
// the given --name (default "prometheus"), or an error if not found. Shared by
// the start/stop/reload subcommands.
func resolvePrometheusConfig(cmd *cobra.Command) (*config.PrometheusConfig, error) {
	if err := loadConfigForDSN(); err != nil {
		return nil, err
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "prometheus"
	}
	if cfg.Addons.Prometheus == nil {
		return nil, fmt.Errorf("no prometheus addons configured")
	}
	pc, ok := cfg.Addons.Prometheus[name]
	if !ok {
		return nil, fmt.Errorf("prometheus instance %q not found (use 'pg addon list' to see available)", name)
	}
	return &pc, nil
}

// runAddonRemovePrometheus removes a Prometheus container and pgcli's rendered
// prometheus.yml copy. The TSDB data dir is kept unless --clean-data, so
// remove+reinstall revives collected metrics. In --conf-file mode the operator's
// original file lives wherever they put it and is never touched.
func runAddonRemovePrometheus(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "prometheus"
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

	if cfg.Addons.Prometheus == nil {
		return fmt.Errorf("no Prometheus add-ons configured")
	}
	pc, ok := cfg.Addons.Prometheus[name]
	if !ok {
		return fmt.Errorf("Prometheus %q not found", name)
	}

	pm, err := podman.NewPrometheusManager(cfg)
	if err != nil {
		return fmt.Errorf("Prometheus manager: %w", err)
	}

	fmt.Printf("-> Removing Prometheus %q...\n", name)
	if err := pm.Remove(&pc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.Prometheus, name)
	if len(cfg.Addons.Prometheus) == 0 {
		cfg.Addons.Prometheus = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ Prometheus %q removed\n", name)
	if !cleanData {
		fmt.Printf("  (data dir kept — reinstall the same name revives collected metrics; --clean-data to delete it)\n")
	}
	return nil
}

func runAddonStartPrometheus(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "prometheus"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.Prometheus == nil {
		return fmt.Errorf("no Prometheus add-ons configured (run 'pg addon install prometheus')")
	}
	pc, ok := cfg.Addons.Prometheus[name]
	if !ok {
		return fmt.Errorf("Prometheus %q not found (run 'pg addon install prometheus --name %s')", name, name)
	}
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}
	pm, err := podman.NewPrometheusManager(cfg)
	if err != nil {
		return fmt.Errorf("Prometheus manager: %w", err)
	}
	fmt.Printf("-> Starting Prometheus %q...\n", name)
	return pm.StartContainer(&pc)
}

func runAddonStopPrometheus(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "prometheus"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	pc, ok := cfg.Addons.Prometheus[name]
	if !ok {
		return fmt.Errorf("Prometheus %q not found", name)
	}
	pm, err := podman.NewPrometheusManager(cfg)
	if err != nil {
		return fmt.Errorf("Prometheus manager: %w", err)
	}
	running, _ := pm.ContainerRunning(pc.ContainerName)
	if !running {
		fmt.Printf("Prometheus %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping Prometheus %q...\n", name)
	if _, err := pm.Stop(pc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ Prometheus %q stopped\n", name)
	return nil
}

// runAddonPrometheusReload rewrites the mounted prometheus.yml (re-render in
// render mode, re-copy in --conf-file mode) and signals the running container to
// reread it via SIGHUP — no container recreate, no HTTP reload endpoint. The
// common case is --conf-file: edit your own file, then reload.
func runAddonPrometheusReload(cmd *cobra.Command) error {
	pc, err := resolvePrometheusConfig(cmd)
	if err != nil {
		return err
	}
	pm, err := podman.NewPrometheusManager(cfg)
	if err != nil {
		return fmt.Errorf("Prometheus manager: %w", err)
	}
	fmt.Printf("-> Reloading prometheus %q...\n", pc.Name)
	return pm.Reload(pc)
}

// runAddonInstallAlertmanager installs an Alertmanager container — the alert
// dedup/grouping/routing frontend of the Prometheus stack — from the official
// docker.io/prom/alertmanager image. It is the Prometheus addon's twin in shape:
// top-level-only (addons.alertmanager.<name>, key defaults to "alertmanager"),
// one host port, persistent state dir, configured by a RENDERED alertmanager.yml
// (one minimal route pointing at a single receiver — "web.hook" carrying every
// --webhook-url, or a "no-op" receiver when none are given) or, with --conf-file,
// the operator's own file; reload via SIGHUP.
//
// The wiring into Prometheus lives on the PROMETHEUS side (--alertmanager-url
// renders the alerting section there); this addon itself has no Prometheus
// concept — it just receives whatever is pushed to its API.
//
// Security is stricter-than-Prometheus framing: there is no auth, and the API
// lets anyone who reaches the port create silences (mute on-call) — so the
// exposure NOTE below is louder than Prometheus's.
func runAddonInstallAlertmanager(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "alertmanager"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	port, _ := cmd.Flags().GetInt("port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	webhooks, _ := cmd.Flags().GetStringArray("webhook-url")
	confFile, _ := cmd.Flags().GetString("conf-file")
	force, _ := cmd.Flags().GetBool("force")

	// --conf-file replaces the rendered config entirely, so it cannot coexist
	// with --webhook-url (which configures the rendered receiver).
	if confFile != "" && len(webhooks) > 0 {
		return fmt.Errorf("--conf-file is mutually exclusive with --webhook-url (it configures the rendered alertmanager.yml)")
	}
	if confFile != "" {
		info, err := os.Stat(confFile)
		if os.IsNotExist(err) {
			return fmt.Errorf("--conf-file: %s not found", confFile)
		}
		if err != nil {
			return fmt.Errorf("--conf-file: %w", err)
		}
		if info.Size() == 0 {
			return fmt.Errorf("--conf-file: %s is empty", confFile)
		}
	}
	// Validate webhook URLs BEFORE writing anything — a malformed URL aborts
	// with nothing touched (no pg.yaml change, no config rewrite, no reload).
	// This is format-only validation, not a connectivity probe: unlike a
	// Prometheus scrape target (GET /metrics at scrape time), a webhook is a POST
	// that only fires at alert time, so probing it at install would say nothing
	// useful about a not-yet-running listener.
	if err := podman.ValidateWebhooks(webhooks); err != nil {
		return fmt.Errorf("--webhook-url: %w", err)
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

	if cfg.Addons.Alertmanager == nil {
		cfg.Addons.Alertmanager = make(map[string]config.AlertmanagerConfig)
	}
	existing, ok := cfg.Addons.Alertmanager[name]
	if !ok {
		existing = config.AlertmanagerConfig{
			ContainerName: "pgcli-alertmanager" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if dataDir != "" {
		existing.DataDir = dataDir
	}
	if cmd.Flags().Changed("port") {
		existing.HostPort = port
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if cmd.Flags().Changed("webhook-url") {
		// Set or cleared wholesale: passing --webhook-url replaces the stored
		// list (it declares the full fan-out set), omitting it reverts to no-op.
		existing.Webhook = webhooks
	}
	existing.ConfFile = confFile
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	cfg.Addons.Alertmanager[name] = existing

	cfg.ApplyDefaults()
	ac := cfg.Addons.Alertmanager[name]

	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	am, err := podman.NewAlertmanagerManager(cfg)
	if err != nil {
		return fmt.Errorf("Alertmanager manager: %w", err)
	}

	fmt.Printf("-> Preparing Alertmanager image %s...\n", ac.ImageTag)
	if err := am.EnsureImage(ac.ImageTag); err != nil {
		return err
	}

	cfgPathOut := am.ConfigPath(&ac)
	oldConf, _ := os.ReadFile(cfgPathOut)

	if _, err := am.WriteConfig(&ac); err != nil {
		return err
	}

	running, err := am.ContainerRunning(ac.ContainerName)
	if err != nil {
		return err
	}
	reloaded := false
	if running && !force {
		newConf, _ := os.ReadFile(cfgPathOut)
		if string(oldConf) != string(newConf) {
			fmt.Println("-> Config changed, reloading Alertmanager (SIGHUP)...")
			if err := am.SignalReload(&ac); err != nil {
				return err
			}
			reloaded = true
		} else {
			fmt.Println("  [OK] Alertmanager running, config unchanged")
		}
	} else {
		fmt.Println("-> Starting Alertmanager container...")
		if err := am.EnsureContainer(&ac); err != nil {
			return err
		}
	}

	cfg.Addons.Alertmanager[name] = ac
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	if running && !force {
		fmt.Printf("✓ Alertmanager %q (%s)\n", name, map[bool]string{true: "config reloaded", false: "unchanged"}[reloaded])
	} else {
		fmt.Printf("✓ Alertmanager installed: %q\n", name)
	}
	fmt.Printf("  Container:  %s\n", ac.ContainerName)
	fmt.Printf("  Image:      %s\n", ac.ImageTag)
	fmt.Printf("  Data:       %s\n", am.DataDir(&ac))
	fmt.Printf("  Config:     %s", cfgPathOut)
	switch {
	case ac.ConfFile != "":
		fmt.Printf(" (from --conf-file %s)\n", ac.ConfFile)
	case len(ac.Webhook) > 0:
		fmt.Printf(" (rendered: webhook receiver, %d url(s))\n", len(ac.Webhook))
	default:
		fmt.Printf(" (rendered: no-op receiver)\n")
	}
	fmt.Printf("  URL:        http://%s:%d/\n", ac.Listen, ac.HostPort)
	fmt.Println()
	if ac.Listen == "0.0.0.0" {
		fmt.Println("  NOTE: listening on every interface. Alertmanager has NO authentication, and this is more")
		fmt.Println("        dangerous than an open Prometheus: anyone who reaches this port can CREATE SILENCES,")
		fmt.Println("        i.e. silently mute every notification your on-call expects. Restrict exposure with")
		fmt.Println("        `pg addon install alertmanager --name <name> --listen 127.0.0.1 --force`, or firewall it.")
	}
	return nil
}

// resolveAlertmanagerConfig loads the config and returns the AlertmanagerConfig
// for the given --name (default "alertmanager"), or an error if not found.
// Shared by the start/stop/reload subcommands.
func resolveAlertmanagerConfig(cmd *cobra.Command) (*config.AlertmanagerConfig, error) {
	if err := loadConfigForDSN(); err != nil {
		return nil, err
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "alertmanager"
	}
	if cfg.Addons.Alertmanager == nil {
		return nil, fmt.Errorf("no alertmanager addons configured")
	}
	ac, ok := cfg.Addons.Alertmanager[name]
	if !ok {
		return nil, fmt.Errorf("alertmanager instance %q not found (use 'pg addon list' to see available)", name)
	}
	return &ac, nil
}

// runAddonRemoveAlertmanager removes an Alertmanager container and pgcli's
// rendered alertmanager.yml copy. The state dir (silences, notification state)
// is kept unless --clean-data. In --conf-file mode the operator's original file
// lives wherever they put it and is never touched.
func runAddonRemoveAlertmanager(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "alertmanager"
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

	if cfg.Addons.Alertmanager == nil {
		return fmt.Errorf("no Alertmanager add-ons configured")
	}
	ac, ok := cfg.Addons.Alertmanager[name]
	if !ok {
		return fmt.Errorf("Alertmanager %q not found", name)
	}

	am, err := podman.NewAlertmanagerManager(cfg)
	if err != nil {
		return fmt.Errorf("Alertmanager manager: %w", err)
	}

	fmt.Printf("-> Removing Alertmanager %q...\n", name)
	if err := am.Remove(&ac, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.Alertmanager, name)
	if len(cfg.Addons.Alertmanager) == 0 {
		cfg.Addons.Alertmanager = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ Alertmanager %q removed\n", name)
	if !cleanData {
		fmt.Printf("  (data dir kept — reinstall the same name revives silences and notification state; --clean-data to delete it)\n")
	}
	return nil
}

func runAddonStartAlertmanager(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "alertmanager"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.Alertmanager == nil {
		return fmt.Errorf("no Alertmanager add-ons configured (run 'pg addon install alertmanager')")
	}
	ac, ok := cfg.Addons.Alertmanager[name]
	if !ok {
		return fmt.Errorf("Alertmanager %q not found (run 'pg addon install alertmanager --name %s')", name, name)
	}
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}
	am, err := podman.NewAlertmanagerManager(cfg)
	if err != nil {
		return fmt.Errorf("Alertmanager manager: %w", err)
	}
	fmt.Printf("-> Starting Alertmanager %q...\n", name)
	return am.StartContainer(&ac)
}

func runAddonStopAlertmanager(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "alertmanager"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	ac, ok := cfg.Addons.Alertmanager[name]
	if !ok {
		return fmt.Errorf("Alertmanager %q not found", name)
	}
	am, err := podman.NewAlertmanagerManager(cfg)
	if err != nil {
		return fmt.Errorf("Alertmanager manager: %w", err)
	}
	running, _ := am.ContainerRunning(ac.ContainerName)
	if !running {
		fmt.Printf("Alertmanager %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping Alertmanager %q...\n", name)
	if _, err := am.Stop(ac.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ Alertmanager %q stopped\n", name)
	return nil
}

// runAddonAlertmanagerReload rewrites the mounted alertmanager.yml (re-render in
// render mode, re-copy in --conf-file mode) and signals the running container to
// reread it via SIGHUP — no container recreate. Note: unlike Prometheus's
// opt-in HTTP reload endpoint, Alertmanager's POST /-/reload cannot be disabled;
// pgcli simply never uses it, so the mechanism is uniform across addons.
func runAddonAlertmanagerReload(cmd *cobra.Command) error {
	ac, err := resolveAlertmanagerConfig(cmd)
	if err != nil {
		return err
	}
	am, err := podman.NewAlertmanagerManager(cfg)
	if err != nil {
		return fmt.Errorf("Alertmanager manager: %w", err)
	}
	fmt.Printf("-> Reloading alertmanager %q...\n", ac.Name)
	return am.Reload(ac)
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

// ---------------------------------------------------------------------------
// nginx-specific subcommands: reload / test / exec
// ---------------------------------------------------------------------------

var addonNginxCmd = &cobra.Command{
	Use:   "nginx",
	Short: "Manage nginx addon instances",
	Long: `Manage nginx addon instances with dedicated subcommands.

Subcommands:
  test     Test nginx configuration syntax (nginx -t) using a temporary container
  reload   Reload nginx configuration (nginx -s reload) without restarting the container
  exec     Execute a command inside the running nginx container

Examples:
  pg addon nginx test --name proxy           # validate config syntax (works even when stopped)
  pg addon nginx reload --name proxy         # graceful restart after config change
  pg addon nginx exec --name proxy -- nginx -V
  pg addon nginx exec --name proxy -- cat /etc/nginx/nginx.conf`,
}

var addonNginxTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Test nginx configuration syntax (nginx -t)",
	Long: `Validate the nginx configuration syntax by running 'nginx -t' in a temporary
container. Does NOT require the nginx container to be running — a throwaway
container is created with the same image and config mounts, runs the test,
and is automatically removed. This is the safe way to check config changes
before applying them with reload.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonNginxTest(cmd)
	},
}

var addonNginxReloadCmd = &cobra.Command{
	Use:   "reload",
	Short: "Reload nginx configuration (nginx -s reload)",
	Long: `Send a reload signal to the running nginx process, causing it to re-read
its configuration files without restarting the container. Use 'test' first
to validate the config syntax before reloading.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonNginxReload(cmd)
	},
}

var addonNginxExecCmd = &cobra.Command{
	Use:   "exec -- <command> [args...]",
	Short: "Execute a command inside the nginx container",
	Long: `Run an arbitrary command inside the running nginx container with full
stdin/stdout/stderr passthrough. Requires -- to separate pgcli flags from
the container command.

Examples:
  pg addon nginx exec --name proxy -- cat /etc/nginx/nginx.conf
  pg addon nginx exec --name proxy -- nginx -V
  pg addon nginx exec --name proxy -- sh -c 'ls -la /etc/nginx/conf.d/'`,
	DisableFlagParsing: false,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonNginxExec(cmd, args)
	},
}

// resolveNginxConfig loads the config and returns the NginxConfig for the
// given --name (default "nginx"), or an error if not found.
func resolveNginxConfig(cmd *cobra.Command) (*config.NginxConfig, error) {
	if err := loadConfigForDSN(); err != nil {
		return nil, err
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "nginx"
	}
	if cfg.Addons.Nginx == nil {
		return nil, fmt.Errorf("no nginx addons configured")
	}
	nc, ok := cfg.Addons.Nginx[name]
	if !ok {
		return nil, fmt.Errorf("nginx instance %q not found (use 'pg addon list' to see available)", name)
	}
	return &nc, nil
}

func runAddonNginxTest(cmd *cobra.Command) error {
	confFile, _ := cmd.Flags().GetString("conf-file")
	image, _ := cmd.Flags().GetString("image")

	// Direct file test: no installed instance required
	if confFile != "" {
		if _, err := os.Stat(confFile); err != nil {
			return fmt.Errorf("--conf-file: %w", err)
		}
		if image == "" {
			image = podman.DefaultNginxImageTag
		}
		if err := loadConfigForDSN(); err != nil {
			return err
		}
		mgr, err := podman.NewNginxManager(cfg)
		if err != nil {
			return fmt.Errorf("nginx manager: %w", err)
		}
		return mgr.TestFile(confFile, image)
	}

	// Instance test: use the installed config
	nc, err := resolveNginxConfig(cmd)
	if err != nil {
		return err
	}
	mgr, err := podman.NewNginxManager(cfg)
	if err != nil {
		return fmt.Errorf("nginx manager: %w", err)
	}
	return mgr.Test(nc)
}

func runAddonNginxReload(cmd *cobra.Command) error {
	nc, err := resolveNginxConfig(cmd)
	if err != nil {
		return err
	}
	mgr, err := podman.NewNginxManager(cfg)
	if err != nil {
		return fmt.Errorf("nginx manager: %w", err)
	}
	return mgr.Reload(nc)
}

func runAddonNginxExec(cmd *cobra.Command, args []string) error {
	nc, err := resolveNginxConfig(cmd)
	if err != nil {
		return err
	}
	dashIdx := cmd.ArgsLenAtDash()
	if dashIdx == -1 || dashIdx >= len(args) {
		return fmt.Errorf("exec requires -- <command>, e.g. pg addon nginx exec --name proxy -- cat /etc/nginx/nginx.conf")
	}
	mgr, err := podman.NewNginxManager(cfg)
	if err != nil {
		return fmt.Errorf("nginx manager: %w", err)
	}
	return mgr.Exec(nc, args[dashIdx:])
}

// ---------------------------------------------------------------------------
// haproxy-specific subcommands: reload
// ---------------------------------------------------------------------------

var addonHAProxyCmd = &cobra.Command{
	Use:   "haproxy",
	Short: "Manage HAProxy addon instances",
	Long: `Manage HAProxy addon instances with dedicated subcommands.

Subcommands:
  reload   Hot-reload a --conf-file instance without recreating the container

Examples:
  pg addon haproxy reload --name custom   # apply an edited haproxy.cfg seamlessly`,
}

var addonHAProxyReloadCmd = &cobra.Command{
	Use:   "reload",
	Short: "Hot-reload a conf-file haproxy instance (no container restart)",
	Long: `Apply config changes to a running --conf-file HAProxy instance without
recreating the container — the HAProxy equivalent of 'pg addon nginx reload'.
The official image runs HAProxy in master-worker mode, so this sends SIGUSR2 to
the master: a fresh worker is started, listeners are handed over, and in-flight
connections drain gracefully on the old worker (no dropped connections), unlike
the lossy container recreate that 'pg addon install' performs.

It first re-copies the operator's file to the mounted haproxy.cfg and validates
it with 'haproxy -c' in a throwaway container. A config that fails the check is
rejected: the previous good copy is restored on disk and nothing is reloaded —
so a broken file can never be picked up by a later restart.

Only --conf-file instances are supported. A rendered instance rebuilds its
config from --node/--ha on every install, so its update path stays
'pg addon install haproxy'.

The instance must already be running ('pg addon start haproxy --name <name>').

Examples:
  pg addon haproxy reload --name custom`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonHAProxyReload(cmd)
	},
}

func runAddonHAProxyReload(cmd *cobra.Command) error {
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
	fmt.Printf("-> Reloading haproxy %q...\n", name)
	return hm.Reload(&hc)
}

// ---------------------------------------------------------------------------
// prometheus-specific subcommands: reload
// ---------------------------------------------------------------------------

var addonPrometheusCmd = &cobra.Command{
	Use:   "prometheus",
	Short: "Manage Prometheus addon instances",
	Long: `Manage Prometheus addon instances with dedicated subcommands.

Subcommands:
  reload   Hot-reload the mounted prometheus.yml via SIGHUP (no container recreate)

Examples:
  pg addon prometheus reload --name p1   # apply an edited config seamlessly`,
}

var addonPrometheusReloadCmd = &cobra.Command{
	Use:   "reload",
	Short: "Hot-reload a running Prometheus instance (SIGHUP, no container restart)",
	Long: `Apply config changes to a running Prometheus instance without recreating the
container — rewrite the managed prometheus.yml (re-render it in render mode, or
re-copy the operator's --conf-file into the managed path), then send SIGHUP so
Prometheus rereads it in place. This is the Prometheus equivalent of
'pg addon nginx reload' / 'pg addon haproxy reload'.

It uses SIGHUP rather than the HTTP POST /-/reload endpoint on purpose, so the
unauthenticated reload route never needs to be enabled (--web.enable-lifecycle
is not set). Prometheus keeps serving on its last good config and logs an error
if the new one is malformed; check 'pg logs' after reloading a hand-written file.

The instance must already be running ('pg addon start prometheus --name <name>').

Examples:
  pg addon prometheus reload --name p1`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonPrometheusReload(cmd)
	},
}

// ---------------------------------------------------------------------------
// alertmanager-specific subcommands: reload
// ---------------------------------------------------------------------------

var addonAlertmanagerCmd = &cobra.Command{
	Use:   "alertmanager",
	Short: "Manage Alertmanager addon instances",
	Long: `Manage Alertmanager addon instances with dedicated subcommands.

Subcommands:
  reload   Hot-reload the mounted alertmanager.yml via SIGHUP (no container recreate)

Examples:
  pg addon alertmanager reload --name am1   # apply an edited config seamlessly`,
}

var addonAlertmanagerReloadCmd = &cobra.Command{
	Use:   "reload",
	Short: "Hot-reload a running Alertmanager instance (SIGHUP, no container restart)",
	Long: `Apply config changes to a running Alertmanager instance without recreating
the container — rewrite the managed alertmanager.yml (re-render it in render
mode, or re-copy the operator's --conf-file into the managed path), then send
SIGHUP so Alertmanager rereads it in place. This is the Alertmanager equivalent
of 'pg addon nginx reload' / 'pg addon prometheus reload'.

Note: unlike Prometheus (where pgcli never enables the HTTP reload route at
all), Alertmanager's POST /-/reload endpoint exists and CANNOT be disabled.
pgcli simply uses SIGHUP instead, so the mechanism is uniform across addons and
never depends on that unauthenticated route. Alertmanager keeps serving on its
last good config and logs an error if the new one is malformed; check 'pg logs'
after reloading a hand-written file.

The instance must already be running ('pg addon start alertmanager --name <name>').

Examples:
  pg addon alertmanager reload --name am1`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonAlertmanagerReload(cmd)
	},
}

// ---------------------------------------------------------------------------
// gitea-specific subcommands: user create / delete / list
// ---------------------------------------------------------------------------
//
// Gitea's self-registration is closed by pgcli (DISABLE_REGISTRATION=true), so
// every account beyond the install-time admin is created by an admin. These
// wrap the in-container `gitea admin user ...` CLI — which must run as the
// container's git uid (1000), handled by GiteaManager.execGitea — so operators
// do not have to hand-write `podman exec -u 1000 ...`.

var addonGiteaCmd = &cobra.Command{
	Use:   "gitea",
	Short: "Manage Gitea addon accounts",
	Long: `Manage accounts on a Gitea addon instance with dedicated subcommands.

Gitea runs with self-registration disabled, so accounts are created by an
admin — here, from the host, without a manual 'podman exec'.

Subcommands:
  user   Create, delete, and list Gitea web accounts (members or admins)

Examples:
  pg addon gitea user create --username alice --email alice@corp.example
  pg addon gitea user list`,
}

var addonGiteaUserCmd = &cobra.Command{
	Use:   "user",
	Short: "Create, delete, and list Gitea accounts",
	Long: `Manage the Gitea web accounts (the same credentials used to sign into the UI
and for HTTP clone/push). Self-registration is off, so this is how colleagues
get accounts.

Subcommands:
  create   Create an account (member by default; --admin for a site admin)
  delete   Delete an account (--purge to drop its repos/orgs/comments too)
  list     List all accounts (ID, username, email, active, admin, 2FA)

The instance must already be running ('pg addon start gitea --name <name>').`,
}

var addonGiteaUserCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a Gitea web account",
	Long: `Create an account on a running Gitea instance by running 'gitea admin user
create' inside its container (as the git uid). Pass --username; the account is a
normal member unless you add --admin.

Password: pass --password to pin the login. With no --password pgcli generates a
strong one and prints it once (retrieve a lost generated password only by
creating a new account or resetting inside the container — it is not stored in
pg.yaml, since these are Gitea's own accounts, not pgcli-managed logins).

--must-change-password defaults to false: Gitea would otherwise force a password
change on first login, which also blocks HTTP clone/push and API basic-auth
until changed. Set --must-change-password=true to restore that behavior (e.g.
to hand a one-time password to someone who must set their own).

--email defaults to <username>@localhost; pass a real address when users should
receive notifications or password resets.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonGiteaUserCreate(cmd)
	},
}

var addonGiteaUserDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete a Gitea web account",
	Long: `Delete an account by running 'gitea admin user delete' inside the container.
A plain delete only succeeds for an account that owns nothing — if it still
owns repositories Gitea refuses it with 'user still has ownership of
repositories'. Pass --purge to remove the account AND everything it owns
(repositories, organizations, comments), which is destructive and not
reversible from pgcli.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonGiteaUserDelete(cmd)
	},
}

var addonGiteaUserListCmd = &cobra.Command{
	Use:   "list",
	Short: "List Gitea web accounts",
	Long: `List every account in the instance's database — ID, username, email, whether
it is active, an admin, and has 2FA. Mirrors 'gitea admin user list'.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonGiteaUserList(cmd)
	},
}

// resolveGiteaConfig loads the config and returns the GiteaConfig for the given
// --name (default "gitea"), erroring if it is missing or not running — the
// user subcommands exec inside the container, so a stopped instance is a clear
// failure rather than a cryptic podman error.
func resolveGiteaConfig(cmd *cobra.Command) (*config.GiteaConfig, *podman.GiteaManager, error) {
	if err := loadConfigForDSN(); err != nil {
		return nil, nil, err
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "gitea"
	}
	if cfg.Addons.Gitea == nil {
		return nil, nil, fmt.Errorf("no gitea add-ons configured (run 'pg addon install gitea')")
	}
	gc, ok := cfg.Addons.Gitea[name]
	if !ok {
		return nil, nil, fmt.Errorf("gitea instance %q not found (use 'pg addon list' to see available)", name)
	}
	gm, err := podman.NewGiteaManager(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("gitea manager: %w", err)
	}
	running, err := gm.ContainerRunning(gc.ContainerName)
	if err != nil {
		return nil, nil, err
	}
	if !running {
		return nil, nil, fmt.Errorf("gitea instance %q is not running (start it with 'pg addon start gitea --name %s')", name, name)
	}
	return &gc, gm, nil
}

func runAddonGiteaUserCreate(cmd *cobra.Command) error {
	gc, gm, err := resolveGiteaConfig(cmd)
	if err != nil {
		return err
	}
	username, _ := cmd.Flags().GetString("username")
	if username == "" {
		return fmt.Errorf("--username is required")
	}
	email, _ := cmd.Flags().GetString("email")
	if email == "" {
		email = username + "@localhost"
	}
	password, _ := cmd.Flags().GetString("password")
	admin, _ := cmd.Flags().GetBool("admin")
	mustChange, _ := cmd.Flags().GetBool("must-change-password")

	generated := false
	if password == "" {
		pw, err := generatePassword(20)
		if err != nil {
			return fmt.Errorf("generating Gitea password: %w", err)
		}
		password = pw
		generated = true
	}

	exists, err := gm.UserExists(gc, username)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("gitea user %q already exists (delete it first with 'pg addon gitea user delete --username %s')", username, username)
	}

	if err := gm.CreateUser(gc, podman.GiteaUserCreateSpec{
		Username:           username,
		Email:              email,
		Password:           password,
		Admin:              admin,
		MustChangePassword: mustChange,
	}); err != nil {
		return err
	}

	role := "member"
	if admin {
		role = "site admin"
	}
	fmt.Printf("✓ Created Gitea %s: %q\n", role, username)
	fmt.Printf("  Email:      %s\n", email)
	if generated {
		fmt.Printf("  Password:   %s\n", password)
		fmt.Println("  (Generated — not stored anywhere. Record it now; it cannot be recovered.)")
	} else {
		fmt.Println("  Password:   <the value you passed with --password>")
	}
	if mustChange {
		fmt.Println("  NOTE: must-change-password is ON — they must change it on first login, and until then HTTP clone/push and API basic-auth return 403.")
	}
	return nil
}

func runAddonGiteaUserDelete(cmd *cobra.Command) error {
	gc, gm, err := resolveGiteaConfig(cmd)
	if err != nil {
		return err
	}
	username, _ := cmd.Flags().GetString("username")
	if username == "" {
		return fmt.Errorf("--username is required")
	}
	purge, _ := cmd.Flags().GetBool("purge")

	exists, err := gm.UserExists(gc, username)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("gitea user %q not found (list accounts with 'pg addon gitea user list')", username)
	}
	if username == gc.AdminUser {
		return fmt.Errorf("refusing to delete the install admin %q — it is pgcli's stored Gitea login (see 'pg addon password gitea'); create another admin first, or rotate this one in-container", username)
	}

	if err := gm.DeleteUser(gc, username, purge); err != nil {
		return err
	}
	fmt.Printf("✓ Deleted Gitea user %q\n", username)
	if purge {
		fmt.Println("  (purged: their repositories, organizations and comments were removed too)")
	} else {
		fmt.Println("  (owned nothing, so it removed cleanly — an account with repositories requires --purge)")
	}
	return nil
}

func runAddonGiteaUserList(cmd *cobra.Command) error {
	gc, gm, err := resolveGiteaConfig(cmd)
	if err != nil {
		return err
	}
	out, err := gm.ListUsers(gc)
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}
