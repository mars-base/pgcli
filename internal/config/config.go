// Package config provides loading, validation, merging, and saving of pgcli configuration files.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mars-base/pgcli/internal/platform"
)

// Config is the complete pgcli configuration.
type Config struct {
	BaseDir                 string                    `yaml:"base_dir,omitempty"`
	Network                 string                    `yaml:"network,omitempty"`                    // shared podman network name, persisted at top level
	Namespace               string                    `yaml:"namespace,omitempty"`                  // container name namespace, empty = disabled (default)
	PGStartPort             int                       `yaml:"pg_start_port,omitempty"`              // starting PG host port, default 35432
	PGSSHPort               int                       `yaml:"pg_ssh_port,omitempty"`                // starting SSH host port, default 42201
	PgBouncerStartPort      int                       `yaml:"pgbouncer_start_port,omitempty"`       // starting PgBouncer host port, default 56432
	EtcdStartPort           int                       `yaml:"etcd_start_port,omitempty"`            // starting etcd client host port, default 2379 (peer gets the next free port)
	PgDogStartPort          int                       `yaml:"pgdog_start_port,omitempty"`           // starting PgDog host port, default 7432 (openmetrics gets the next free port)
	PatroniStartPort        int                       `yaml:"patroni_start_port,omitempty"`         // starting Patroni member PG host port, default 35532
	PatroniRestapiStartPort int                       `yaml:"patroni_restapi_start_port,omitempty"` // starting Patroni REST API host port, default 8008
	PatroniSSHStartPort     int                       `yaml:"patroni_ssh_start_port,omitempty"`     // starting Patroni SSH host port, default 42301 (separate from PG instance SSH pool at 42201)
	HaProxyStartPort        int                       `yaml:"haproxy_start_port,omitempty"`         // starting HAProxy listener host port, default 5000 (read port and stats take the next free ports)
	MinioStartPort          int                       `yaml:"minio_start_port,omitempty"`           // starting MinIO API host port, default 9000 (console takes the next free port)
	PostgrestStartPort      int                       `yaml:"postgrest_start_port,omitempty"`       // starting PostgREST HTTP host port, default 3500
	RedisStartPort          int                       `yaml:"redis_start_port,omitempty"`           // starting Redis host port, default 6379
	PredixyStartPort        int                       `yaml:"predixy_start_port,omitempty"`         // starting Predixy proxy listener host port, default 7617
	PgAdminStartPort        int                       `yaml:"pgadmin_start_port,omitempty"`         // starting pgAdmin 4 web host port, default 5050
	GrafanaStartPort        int                       `yaml:"grafana_start_port,omitempty"`         // starting Grafana web host port, default 3000
	PrometheusStartPort     int                       `yaml:"prometheus_start_port,omitempty"`      // starting Prometheus web host port, default 9090
	AlertmanagerStartPort   int                       `yaml:"alertmanager_start_port,omitempty"`    // starting Alertmanager web host port, default 9093
	GiteaStartPort          int                       `yaml:"gitea_start_port,omitempty"`           // starting Gitea web host port, default 3080 (Gitea's own default 3000 belongs to the Grafana pool here)
	GiteaSSHStartPort       int                       `yaml:"gitea_ssh_start_port,omitempty"`       // starting Gitea OpenSSH host port, default 2222 (allocated for every gitea instance — the image's sshd always runs, so even SSH-off needs a port to park it on, off 22)
	NginxStartPort          int                       `yaml:"nginx_start_port,omitempty"`           // starting nginx HTTP host port, default 8080 (HTTPS takes the next free port)
	GarageStartPort         int                       `yaml:"garage_start_port,omitempty"`          // starting Garage S3 API host port, default 3900 (RPC, web, admin take the next three free ports)
	SeaweedfsStartPort      int                       `yaml:"seaweedfs_start_port,omitempty"`       // starting SeaweedFS master host port, default 8400 (volume, filer, s3 take the next three free ports; gRPC twins at +10000 are reserved)
	VictoriaMetricsStartPort int                      `yaml:"victoriametrics_start_port,omitempty"` // starting VictoriaMetrics HTTP host port, default 8428
	VMClusterStartPort       int                      `yaml:"vmcluster_start_port,omitempty"`       // starting VMCluster port, default 8480 (5 consecutive ports per instance: insert HTTP, select HTTP, storage HTTP, internal insert TCP, internal select TCP)
	VictoriaLogsStartPort    int                      `yaml:"victorialogs_start_port,omitempty"`    // starting VictoriaLogs HTTP host port, default 9428
	Postgres                PostgresConfig            `yaml:"postgres"`
	Podman                  PodmanConfig              `yaml:"podman"`
	PITR                    PITRConfig                `yaml:"pitr"`
	Logging                 LoggingConfig             `yaml:"logging"`
	Backup                  BackupConfig              `yaml:"backup"`
	Pigsty                  PigstyConfig              `yaml:"pigsty"`
	Addons                  TopAddonsConfig           `yaml:"addons,omitempty"` // top-level addon configs (e.g. remote PgBouncer)
	Instances               map[string]InstanceConfig `yaml:"instances"`

	Instance string `yaml:"-"` // current instance name (set at runtime, not persisted)
}

// InstanceConfig is the configuration for a single database instance.
type InstanceConfig struct {
	Postgres PostgresConfig `yaml:"postgres"`
	Podman   PodmanConfig   `yaml:"podman"`
	PITR     PITRConfig     `yaml:"pitr"`
	// ReplicaOf names the primary instance this instance streams from via
	// physical replication. Empty means the instance is a primary.
	ReplicaOf string `yaml:"replica_of,omitempty"`
	// PrimaryDSN is the connection string of a remote primary on another
	// host. When set, the instance is a cross-network replica: the primary
	// is not managed locally, and replication setup must have been prepared
	// on the primary side (pg replica create ... --replica-host).
	PrimaryDSN string `yaml:"primary_dsn,omitempty"`
	// Extensions lists the PostgreSQL extension names installed in this
	// instance (managed by `pg extension install/remove`). Each start
	// ensures the matching packages are installed and shared_preload_libraries
	// is kept in sync; new extensions also have CREATE EXTENSION IF NOT EXISTS
	// run automatically.
	Extensions []string `yaml:"extensions,omitempty"`
	// Addons tracks optional sidecar components installed for this instance
	// (e.g. PgBouncer connection pooler). Managed by `pg addon install/remove`.
	Addons AddonsConfig `yaml:"addons,omitempty"`
	// Autostart starts this instance automatically on host boot via the
	// boot service (`pg autostart enable`). `pg stop` does not affect it.
	Autostart bool `yaml:"autostart,omitempty"`
	// Tune records the last `pg tune --apply`: the input specs (for display and
	// reproducibility) and the postgresql.conf parameters actually written into
	// the managed tuning block. Params are merged over pgcli's static tuning
	// defaults on every start. Cleared by `pg tune --revert`.
	Tune *TuneConfig `yaml:"tune,omitempty"`
}

// TuneConfig persists a `pg tune --apply` result for one target (an instance's
// managed conf block, or a Patroni cluster's DCS parameters).
type TuneConfig struct {
	// Inputs echoes the hardware/workload specs the recommendation was
	// computed from (type/hd/ram/cpus/db_size/conns/version).
	Inputs map[string]string `yaml:"inputs,omitempty"`
	// Params are the parameter name → value pairs actually applied, in
	// conf-file form (e.g. "4GB", "off"). For Patroni they are the names set
	// under postgresql.parameters in the DCS; --revert removes exactly these.
	Params map[string]string `yaml:"params,omitempty"`
}

// AddonsConfig tracks optional sidecar components attached to an instance.
type AddonsConfig struct {
	PgBouncer *PgBouncerConfig `yaml:"pgbouncer,omitempty"`
	Postgrest *PostgrestConfig `yaml:"postgrest,omitempty"`
}

// TopAddonsConfig holds top-level (cross-instance) addon configurations.
// These are for addons targeting PG instances NOT managed locally by pgcli
// (e.g. a PgBouncer fronting a remote database via --dsn), and for shared
// infrastructure addons like etcd.
type TopAddonsConfig struct {
	PgBouncer    map[string]PgBouncerConfig      `yaml:"pgbouncer,omitempty"`
	Postgrest    map[string]PostgrestConfig      `yaml:"postgrest,omitempty"`
	Etcd         map[string]EtcdConfig           `yaml:"etcd,omitempty"`
	PgDog        map[string]PgDogConfig          `yaml:"pgdog,omitempty"`
	Patroni      map[string]PatroniClusterConfig `yaml:"patroni,omitempty"`
	HAProxy      map[string]HAProxyConfig        `yaml:"haproxy,omitempty"`
	Minio        map[string]MinioConfig          `yaml:"minio,omitempty"`
	Silo         map[string]SiloConfig           `yaml:"silo,omitempty"`
	Rustfs       map[string]RustfsConfig         `yaml:"rustfs,omitempty"`
	Garage       map[string]GarageConfig         `yaml:"garage,omitempty"`
	Seaweedfs    map[string]SeaweedfsConfig      `yaml:"seaweedfs,omitempty"`
	Redis        map[string]RedisConfig          `yaml:"redis,omitempty"`
	Predixy      map[string]PredixyConfig        `yaml:"predixy,omitempty"`
	PgAdmin      map[string]PgAdminConfig        `yaml:"pgadmin,omitempty"`
	Grafana      map[string]GrafanaConfig        `yaml:"grafana,omitempty"`
	Prometheus   map[string]PrometheusConfig     `yaml:"prometheus,omitempty"`
	Alertmanager map[string]AlertmanagerConfig   `yaml:"alertmanager,omitempty"`
	Gitea        map[string]GiteaConfig          `yaml:"gitea,omitempty"`
	Nginx        map[string]NginxConfig          `yaml:"nginx,omitempty"`
	BeeGFS       map[string]BeeGFSConfig         `yaml:"beegfs,omitempty"`
	Juicefs      map[string]JuicefsConfig        `yaml:"juicefs,omitempty"`
	Zerofs       map[string]ZerofsConfig         `yaml:"zerofs,omitempty"`
	Duckdb       map[string]DuckdbConfig         `yaml:"duckdb,omitempty"`
	VictoriaMetrics map[string]VictoriaMetricsConfig `yaml:"victoriametrics,omitempty"`
	VMCluster      map[string]VMClusterConfig       `yaml:"vmcluster,omitempty"`
	VictoriaLogs   map[string]VictoriaLogsConfig    `yaml:"victorialogs,omitempty"`
}

// PgBouncerConfig holds the per-instance PgBouncer connection pooler settings.
type PgBouncerConfig struct {
	ContainerName   string `yaml:"container_name"`              // e.g. pgcli-pgbouncer-ns-<instance>
	ImageTag        string `yaml:"image_tag,omitempty"`         // docker.io/edoburu/pgbouncer:v1.25.2-p0 (default)
	HostPort        int    `yaml:"host_port,omitempty"`         // 56432+ auto-assigned
	PoolMode        string `yaml:"pool_mode,omitempty"`         // transaction (default)
	DSN             string `yaml:"dsn,omitempty"`               // remote PG DSN (remote mode only)
	BackendHost     string `yaml:"backend_host,omitempty"`      // backend PG host:port (e.g. "127.0.0.1:35435")
	MaxClientConn   int    `yaml:"max_client_conn,omitempty"`   // 100 (default)
	DefaultPoolSize int    `yaml:"default_pool_size,omitempty"` // 20 (default)

	// Pool sizing
	MinPoolSize        int `yaml:"min_pool_size,omitempty"`        // 0 (default, disabled)
	ReservePoolSize    int `yaml:"reserve_pool_size,omitempty"`    // 0 (default, disabled)
	MaxDBConnections   int `yaml:"max_db_connections,omitempty"`   // 0 (unlimited)
	MaxUserConnections int `yaml:"max_user_connections,omitempty"` // 0 (unlimited)

	// Timeouts (in seconds, 0 = disabled/use default)
	ServerIdleTimeout      int `yaml:"server_idle_timeout,omitempty"`      // 600 (default)
	ServerLifetime         int `yaml:"server_lifetime,omitempty"`          // 3600 (default)
	ServerConnectTimeout   int `yaml:"server_connect_timeout,omitempty"`   // 15 (default)
	QueryTimeout           int `yaml:"query_timeout,omitempty"`            // 0 (disabled, default)
	QueryWaitTimeout       int `yaml:"query_wait_timeout,omitempty"`       // 120 (default)
	IdleTransactionTimeout int `yaml:"idle_transaction_timeout,omitempty"` // 0 (disabled, default)
	TransactionTimeout     int `yaml:"transaction_timeout,omitempty"`      // 0 (disabled, default)

	// Admin access
	AdminUsers string `yaml:"admin_users,omitempty"` // comma-separated list
	StatsUsers string `yaml:"stats_users,omitempty"` // comma-separated list

	// Logging
	LogConnections    int `yaml:"log_connections,omitempty"`    // 1 (default, enabled)
	LogDisconnections int `yaml:"log_disconnections,omitempty"` // 1 (default, enabled)

	// Autostart starts this PgBouncer container automatically on host boot
	// via the boot service (`pg autostart enable --pgbouncer`).
	Autostart bool `yaml:"autostart,omitempty"`
}

// PostgrestConfig holds a PostgREST addon: a single stateless container that
// turns a PostgreSQL schema into a RESTful API. It connects to a PG endpoint
// given by DSN, so the backend may be a direct instance, a PgBouncer, or a
// Patroni+HAProxy writer address — the addon does not care which. Like
// PgBouncer it is dual-mode: a local sidecar attached to an instance
// (instances.<name>.addons.postgrest) or a top-level remote entry fronting a
// database via --dsn (addons.postgrest.<name>). PostgREST is env-configured
// (every setting is a PGRST_* variable) and holds no host data, so there is no
// data dir and no config file to render.
type PostgrestConfig struct {
	ContainerName string `yaml:"container_name"`         // pgcli-postgrest<ns>-<name>
	Name          string `yaml:"name,omitempty"`         // addon key, defaults to the map key
	ImageTag      string `yaml:"image_tag,omitempty"`    // docker.io/postgrest/postgrest:v16.3 (default)
	HostPort      int    `yaml:"host_port,omitempty"`    // HTTP host port, 3500+ auto-assigned
	Listen        string `yaml:"listen,omitempty"`       // bind address, default 127.0.0.1
	DSN           string `yaml:"dsn"`                    // backend PG URI, verbatim -> PGRST_DB_URI
	BackendHost   string `yaml:"backend_host,omitempty"` // backend host:port (display only, parsed from DSN best-effort)
	DbPool        int    `yaml:"db_pool,omitempty"`      // PGRST_DB_POOL; 0 = PostgREST's own default (10)
	Schemas       string `yaml:"schemas,omitempty"`      // PGRST_DB_SCHEMAS; exposed schema, e.g. "api" (empty = PostgREST default "public")
	AnonRole      string `yaml:"anon_role,omitempty"`    // PGRST_DB_ANON_ROLE; the NOINHERIT role unauthenticated requests SET ROLE to (empty = anonymous access disabled)
	JwtSecret     string `yaml:"jwt_secret,omitempty"`   // PGRST_JWT_SECRET; enables JWT-authenticated requests (empty = JWT off). A secret — set only via --jwt-secret, never defaulted.
	Autostart     bool   `yaml:"autostart,omitempty"`    // start on host boot via `pg autostart enable --postgrest`
}

// EtcdConfig holds a standalone etcd addon (a single-member key-value store,
// the DCS layer a Patroni/PostgreSQL HA cluster needs). etcd is shared
// infrastructure, so it is stored at the top level (addons.etcd.<name>)
// rather than under a specific instance.
type EtcdConfig struct {
	ContainerName string `yaml:"container_name"`           // e.g. pgcli-etcd-ns-<name>
	Name          string `yaml:"name,omitempty"`           // etcd --name, defaults to the addon key
	ClusterName   string `yaml:"cluster_name,omitempty"`   // etcd --initial-cluster-token (unique cluster id; all members must match)
	ImageTag      string `yaml:"image_tag,omitempty"`      // quay.io/coreos/etcd:v3.5.30 (default)
	DataDir       string `yaml:"data_dir,omitempty"`       // data dir root; member uses <root>/<name>/data, default <baseDir>/addon/etcd
	ClientPort    int    `yaml:"client_port,omitempty"`    // 2379+ auto-assigned
	PeerPort      int    `yaml:"peer_port,omitempty"`      // next free port after ClientPort
	AdvertiseHost string `yaml:"advertise_host,omitempty"` // host in this member's peer/client URLs; empty = 127.0.0.1 (single-host); set a LAN IP or FQDN for cross-host
	// Autostart brings this member's container up on host boot via the boot
	// service (pg start --autostart). It only starts an existing container —
	// etcd membership cannot be re-registered at boot — so install the member
	// first. Independent of the container's --restart policy.
	Autostart bool `yaml:"autostart,omitempty"`
}

// AdvertiseAddr is the host used in this member's peer/client URLs — the
// explicit AdvertiseHost, or loopback for a single-host cluster.
func (e EtcdConfig) AdvertiseAddr() string {
	if e.AdvertiseHost != "" {
		return e.AdvertiseHost
	}
	return "127.0.0.1"
}

// ClientURL is this member's advertised client endpoint.
func (e EtcdConfig) ClientURL() string {
	return fmt.Sprintf("http://%s:%d", e.AdvertiseAddr(), e.ClientPort)
}

// PeerURL is this member's advertised peer endpoint.
func (e EtcdConfig) PeerURL() string {
	return fmt.Sprintf("http://%s:%d", e.AdvertiseAddr(), e.PeerPort)
}

// PgDogBackend is one [[databases]] entry in pgdog.toml — a single PostgreSQL
// shard (and, within a shard, a role: primary or replica) behind the proxy.
type PgDogBackend struct {
	Name         string `yaml:"name"`            // logical database name clients connect to (the [[databases]] name)
	Host         string `yaml:"host"`            // backend PG host
	Port         int    `yaml:"port"`            // backend PG port
	DatabaseName string `yaml:"database_name"`   // real database name on the backend
	Shard        int    `yaml:"shard,omitempty"` // shard index (0 for a non-sharded setup)
	Role         string `yaml:"role,omitempty"`  // "primary" (default) or "replica"
}

// PgDogUser is one [[users]] entry in users.toml. PgDog stores no hashed
// passwords, so Password is always plaintext and is written to users.toml
// as-is (mode 0600 on the host, mounted read-only into the container).
type PgDogUser struct {
	Name            string `yaml:"name"`
	Password        string `yaml:"password"`
	Database        string `yaml:"database"`                   // the [[databases]] name this user may connect to
	ReplicationMode bool   `yaml:"replication_mode,omitempty"` // allow physical-replication connections
}

// PgDogShardedTable is one [[sharded_tables]] entry in pgdog.toml — a table
// whose routing is determined by a single column.
type PgDogShardedTable struct {
	Database string `yaml:"database"`
	Name     string `yaml:"name"`
	Column   string `yaml:"column"`
	DataType string `yaml:"data_type"` // e.g. "bigint", "text"
}

// PgDogConfig holds a standalone PgDog addon (PostgreSQL proxy: connection
// pooling, load balancing and sharding). Like etcd it is shared infrastructure
// rather than a per-instance sidecar, so it lives at the top level
// (addons.pgdog.<name>) and its config files are generated entirely from the
// flags passed to `pg addon install pgdog` — pgdog.toml + users.toml.
type PgDogConfig struct {
	ContainerName   string `yaml:"container_name"`              // e.g. pgcli-pgdog-ns-<name>
	Name            string `yaml:"name,omitempty"`              // addon key, defaults to the map key
	ImageTag        string `yaml:"image_tag,omitempty"`         // ghcr.io/pgdogdev/pgdog:v0.1.57 (default)
	Host            string `yaml:"host,omitempty"`              // listen address, default 127.0.0.1
	HostPort        int    `yaml:"host_port,omitempty"`         // client-facing port, 7432+ auto-assigned
	OpenmetricsPort int    `yaml:"openmetrics_port,omitempty"`  // Prometheus /metrics port, next free port after HostPort
	PoolerMode      string `yaml:"pooler_mode,omitempty"`       // transaction (default) | session
	Workers         int    `yaml:"workers,omitempty"`           // tokio worker threads, default 2
	DefaultPoolSize int    `yaml:"default_pool_size,omitempty"` // server connections per user/db pair, default 10

	Backends      []PgDogBackend      `yaml:"backends,omitempty"`       // [[databases]] entries
	Users         []PgDogUser         `yaml:"users,omitempty"`          // [[users]] entries (users.toml)
	ShardedTables []PgDogShardedTable `yaml:"sharded_tables,omitempty"` // [[sharded_tables]] entries

	// Autostart brings this container up on host boot via the boot service
	// (`pg autostart enable --pgdog`). It only starts an existing container,
	// reading the pgdog.toml/users.toml already on disk — so install first.
	Autostart bool `yaml:"autostart,omitempty"`
}

// ClientAddr is the address clients connect to through the proxy.
func (p PgDogConfig) ClientAddr() string {
	host := p.Host
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("%s:%d", host, p.HostPort)
}

// PatroniPasswords are the credentials a Patroni cluster uses internally. They
// live in the member's local patroni.yml (Patroni requires authentication in
// the per-node config, not the DCS), so for a cross-host cluster every host
// must carry the identical set — `pg ha create --passwords-file` copies them
// between hosts. Stored in pg.yaml at 0644 with the rest of the config; treat
// pg.yaml as a secret for HA clusters, same as pgdog's users.toml caveat.
type PatroniPasswords struct {
	Superuser     string `yaml:"superuser,omitempty"`        // postgres superuser password
	Replication   string `yaml:"replication,omitempty"`      // replication-role password
	Rewind        string `yaml:"rewind,omitempty"`           // pg_rewind role password
	RestapiUser   string `yaml:"restapi_user,omitempty"`     // REST API basic-auth username
	RestapiPasswd string `yaml:"restapi_password,omitempty"` // REST API basic-auth password
}

// PatroniMemberConfig is a single Patroni member (one postmaster, one
// patroni container) on this host. pgcli owns the container/image/config-file/
// ports; Patroni owns the postmaster and all failover decisions.
type PatroniMemberConfig struct {
	ContainerName string `yaml:"container_name,omitempty"` // pgcli-patroni<ns>-<scope>-<member>
	ImageTag      string `yaml:"image_tag,omitempty"`      // ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5 (default)
	AdvertiseHost string `yaml:"advertise_host,omitempty"` // connect_address host; empty = loopback only (single host); a LAN IP/FQDN for cross-host
	// RemoteHost declares this member as living on another physical host: pgcli
	// never creates or manages a local container for it, but its connection
	// params (HostPort/SSHPort/RestapiPort) feed the backup, SSH, and HAProxy
	// config generators so cross-host members are reachable. Empty = local.
	RemoteHost  string `yaml:"remote_host,omitempty"`
	HostPort    int    `yaml:"host_port,omitempty"`    // PG listen port, 35532+ auto-assigned per host
	RestapiPort int    `yaml:"restapi_port,omitempty"` // Patroni REST API port, 8008+ auto-assigned per host
	SSHPort     int    `yaml:"ssh_port,omitempty"`     // SSH port for pgbackrest backup, 42301+ auto-assigned per host
	DataDir     string `yaml:"data_dir,omitempty"`     // host dir bound to /var/lib/postgresql (PGDATA under it)
	// Autostart brings this member's container up on host boot via the boot
	// service (pg autostart enable --ha). Start-only: it starts the existing
	// container reading the patroni.yml already on disk; never re-renders.
	Autostart bool `yaml:"autostart,omitempty"`
}

// PatroniClusterConfig is one Patroni HA cluster (a `scope`), keyed in
// addons.patroni. Each host records only its own members; the cluster is
// reassembled across hosts through the shared DCS (etcd), so two hosts'
// pg.yaml files each hold a partial `Members` view of the same scope.
type PatroniClusterConfig struct {
	Name string `yaml:"name,omitempty"` // scope, defaults to the map key

	// DCS wiring: either reference local etcd addon members (rendered to their
	// client URLs) or give explicit host:port endpoints (external / cross-host
	// etcd). EtcdEndpoints wins if set.
	EtcdMembers   []string `yaml:"etcd_members,omitempty"`   // keys into cfg.Addons.Etcd
	EtcdEndpoints []string `yaml:"etcd_endpoints,omitempty"` // external host:port list (takes precedence)

	Passwords PatroniPasswords `yaml:"passwords,omitempty"`

	// Extensions lists the PostgreSQL extension names installed cluster-wide
	// (managed by `pg ha extension install/remove`). Cluster-scoped (like
	// Passwords), not per-member: all members must share the same packages and
	// shared_preload_libraries.
	Extensions []string `yaml:"extensions,omitempty"`

	// Tune records the last `pg tune --ha <scope> --apply`: input specs plus
	// the postgresql.parameters set in the DCS. --revert removes exactly those
	// parameter names from the cluster config.
	Tune *TuneConfig `yaml:"tune,omitempty"`

	Members map[string]PatroniMemberConfig `yaml:"members,omitempty"` // member name -> config (this host's only)
}

// HAProxyTarget is one backend server in a HAProxy listener: a Patroni member
// reached at its PostgreSQL port, health-checked at its REST API port. Patroni's
// GET health endpoints are unauthenticated by design, so HAProxy's `option
// httpchk` probes need no credentials — the leader answers 200 to `GET /` and a
// replica answers 200 to `GET /replica`, which is exactly how the rw / ro
// listeners split traffic.
type HAProxyTarget struct {
	Name     string `yaml:"name"`      // server name in haproxy.cfg (the Patroni member name)
	Host     string `yaml:"host"`      // backend host to reach the member on
	PGPort   int    `yaml:"pg_port"`   // PostgreSQL port (the routed backend port)
	RestPort int    `yaml:"rest_port"` // Patroni REST API port (the health-check port)
}

// HAProxyConfig holds a standalone HAProxy addon (TCP load balancer in front of
// a Patroni cluster). Like etcd/pgdog it is shared infrastructure, so it lives
// at the top level (addons.haproxy.<name>). It supports two routing modes:
//
//	unified — one listener routes everything to the current leader (GET /)
//	split   — a rw listener to the leader (GET /) plus a ro listener spread
//	          across replicas (GET /replica), giving read/write separation
//
// The backend servers are Patroni members; they can be injected once at install
// (--node NAME=HOST:PGPORT:RESTPORT, repeated) or auto-derived from a --ha
// scope, and re-run install to pick up members added later.
type HAProxyConfig struct {
	ContainerName string `yaml:"container_name"`            // pgcli-haproxy<ns>-<name>
	Name          string `yaml:"name,omitempty"`            // addon key, defaults to the map key
	ImageTag      string `yaml:"image_tag,omitempty"`       // docker.io/library/haproxy:3.2.23-alpine (default)
	HAScope       string `yaml:"ha_scope,omitempty"`        // Patroni scope these backends belong to (for --ha auto-derivation and display)
	Mode          string `yaml:"mode,omitempty"`            // "unified" (default) | "split" (read/write separation)
	Listen        string `yaml:"listen,omitempty"`          // bind address, default 127.0.0.1
	WritePort     int    `yaml:"write_port,omitempty"`      // rw (leader) listener port, 5000+ auto-assigned
	ReadPort      int    `yaml:"read_port,omitempty"`       // ro (replica) listener port; split mode only, next free port
	StatsPort     int    `yaml:"stats_port,omitempty"`      // HTTP stats page port, next free port after the others
	ReplicaMaxLag string `yaml:"replica_max_lag,omitempty"` // GET /replica?lag= threshold (e.g. "1MB"); empty = no lag filter

	// Autostart brings this container up on host boot via the boot service
	// (pg autostart enable --haproxy). Start-only: it starts the existing
	// container reading the haproxy.cfg already on disk — install first.
	Autostart bool            `yaml:"autostart,omitempty"`
	Targets   []HAProxyTarget `yaml:"targets,omitempty"` // backend Patroni members

	// ConfFile switches the addon from "render a Patroni read/write proxy" to
	// "run the operator's haproxy.cfg verbatim": pgcli binds the file into the
	// container read-only (haproxy validates it with -c at install), manages
	// the container lifecycle around it, and never rewrites or deletes the
	// file. All listeners are whatever the file declares — arbitrary native
	// proxies, not just Patroni. Mutually exclusive with --node/--ha; mode,
	// ports and max-lag are ignored because the file owns them.
	ConfFile string `yaml:"conf_file,omitempty"`
}

// EffectiveMode returns the routing mode, defaulting to "unified".
func (h HAProxyConfig) EffectiveMode() string {
	if h.Mode == "split" {
		return "split"
	}
	return "unified"
}

// NginxBackend is one upstream target in an nginx reverse proxy: a web
// service reached at one or more host:port addresses, exposed under a location
// path. The path is the nginx `location` prefix (e.g. "/admin" → pgAdmin,
// "/api" → PostgREST); the backends are the upstream server addresses
// (e.g. ["127.0.0.1:5050", "127.0.0.1:5051"]). When multiple backends are
// provided, nginx load-balances across them (round-robin by default).
type NginxBackend struct {
	Name     string   `yaml:"name"`     // upstream name in nginx.conf (e.g. "pgadmin")
	Path     string   `yaml:"path"`     // location path prefix (e.g. "/admin")
	Backends []string `yaml:"backends"` // host:port list (e.g. ["127.0.0.1:5050"])
}

// UnmarshalYAML supports both the old single-string "backend" field and the
// new "backends" list. This preserves backward compatibility with existing
// pg.yaml files written before multi-server upstream support was added.
func (nb *NginxBackend) UnmarshalYAML(unmarshal func(any) error) error {
	// Try new format first (backends list)
	type plain NginxBackend
	if err := unmarshal((*plain)(nb)); err == nil && len(nb.Backends) > 0 {
		return nil
	}
	// Fall back to old format (single backend string)
	var old struct {
		Name    string `yaml:"name"`
		Path    string `yaml:"path"`
		Backend string `yaml:"backend"`
	}
	if err := unmarshal(&old); err != nil {
		return err
	}
	nb.Name = old.Name
	nb.Path = old.Path
	if old.Backend != "" {
		nb.Backends = []string{old.Backend}
	}
	return nil
}

// NginxConfig holds a standalone nginx reverse proxy addon. Like HAProxy it is
// shared infrastructure (it typically frontends multiple web services —
// pgAdmin, PostgREST, even HAProxy stats — not a single instance's sidecar),
// so it lives at the top level (addons.nginx.<name>). Unlike HAProxy it is
// HTTP-only (path-based routing, optional TLS termination) rather than TCP
// (port-based load balancing).
//
// The config is file-driven (rendered nginx.conf + optional conf.d/*.conf),
// bind-mounted read-only into the official nginx:alpine image. TLS is optional:
// when enabled, pgcli generates a self-signed cert via tlsca (or accepts a BYO
// cert+key pair) and adds an HTTPS server block alongside the HTTP one.
type NginxConfig struct {
	ContainerName string `yaml:"container_name"`       // pgcli-nginx<ns>-<name>
	Name          string `yaml:"name,omitempty"`       // addon key, defaults to the map key
	ImageTag      string `yaml:"image_tag,omitempty"`  // docker.io/library/nginx:1.27-alpine (default)
	Listen        string `yaml:"listen,omitempty"`     // bind address, default 127.0.0.1
	HTTPPort      int    `yaml:"http_port,omitempty"`  // HTTP listener port, 8080+ auto-assigned
	HTTPSPort     int    `yaml:"https_port,omitempty"` // HTTPS listener port (TLS only), next free port after HTTP

	// WorkerConnections sets the maximum number of simultaneous connections per
	// worker process (nginx worker_connections). Default is 1024. Increase this
	// for high-traffic reverse proxies. When ConfFile is set, this value is
	// injected into the user-provided config (replacing any existing
	// worker_connections directive, or adding one to the events block).
	WorkerConnections int `yaml:"worker_connections,omitempty"`

	// TLS enables an HTTPS listener alongside the HTTP one. When true and
	// TLSCert is empty, pgcli generates a self-signed cert via tlsca.Generate
	// under <base>/addon/nginx/<name>/tls/. When TLSCert/TLSKey are set, the
	// given files are bind-mounted instead (BYO cert).
	TLS     bool   `yaml:"tls,omitempty"`
	TLSCert string `yaml:"tls_cert,omitempty"` // BYO cert path (PEM leaf + chain)
	TLSKey  string `yaml:"tls_key,omitempty"`  // BYO private key path (PEM)

	// Autostart brings this container up on host boot via the boot service
	// (pg autostart enable --nginx). Start-only: it starts the existing
	// container reading the nginx.conf already on disk — install first.
	Autostart bool           `yaml:"autostart,omitempty"`
	Backends  []NginxBackend `yaml:"backends,omitempty"` // upstream targets

	// ConfFile is the path to a user-provided nginx.conf. When set, the
	// template-driven Backends rendering is skipped — pgcli copies the file
	// into the addon directory and manages only the container lifecycle.
	// TLS/HTTPPort/HTTPSPort/Listen still take effect for container networking.
	ConfFile string `yaml:"conf_file,omitempty"`
}

// DefaultMinioImageTag is the public pre-built single-node MinIO image
// (upstream .deb binary on Alpine, web console included). It is a const here,
// the single source of truth, so ApplyDefaults and the podman manager agree.
const DefaultMinioImageTag = "ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226"

// DefaultSiloImageTag is the public Pigsty silo image — a MinIO fork that keeps
// the S3 API, the MINIO_* env contract, the `server /data --address :9000
// --console-address :9001` command line, and the --certs-dir file names, but
// ships the web console and its mcli client in one multi-arch image on
// docker.io (pull-only, like etcd/haproxy — pgcli never builds it). Both the
// silo addon and the `pg mcli` client use this image; `pg mcli` selects mcli
// via --entrypoint. It is a const here, the single source of truth, so
// ApplyDefaults and the podman managers agree.
const DefaultSiloImageTag = "docker.io/pgsty/silo:RELEASE.2026-09-16T00-00-00Z"

// DefaultRustfsImageTag pins the public rustfs image (https://rustfs.com) — a
// Rust object store that is S3-compatible on the wire but shares none of
// MinIO's runtime contract: it runs as a fixed non-root container user (uid/gid
// 10001), configures itself entirely through RUSTFS_* env (no `server <path>`
// argv from pgcli — the image's /entrypoint.sh derives that from RUSTFS_VOLUMES),
// names its TLS files rustfs_cert.pem/rustfs_key.pem, and offers only three
// topologies (SNSD/SNMD/MNMD — no multi-node single-drive). That fixed uid is why
// pgcli runs its OWN wrapper image rather than upstream's: the wrapper starts as
// container-root, chowns the bind-mounted dirs to 10001, then su-drops to rustfs
// (see internal/podman/rustfs.go §1), so pgcli never does host-side ownership.
// The wrapper is published to pgcli's ghcr by `make container-push-rustfs` and
// pulled at install (pull-only, like the other pgcli add-on images — pgcli never
// builds during install). Its tag tracks the upstream rustfs version it wraps
// (1.0.0, the first GA release; later 1.0.1-* are previews). The tag is the
// single source of truth that ApplyDefaults and the podman manager agree on.
const DefaultRustfsImageTag = "ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0"

// DefaultGarageImageTag pins the public Garage image
// (https://garagehq.deuxfleurs.fr) — a Rust, AGPLv3 S3-compatible object store
// designed for small self-hosted and geo-distributed clusters. Unlike rustfs
// (which needs a pgcli wrapper image to solve its fixed uid-10001 ownership
// problem), Garage runs as root in its own container, so pgcli pulls the
// upstream image directly — no wrapper, pull-only. The image is FROM scratch:
// no shell, but /garage is both the daemon (`garage server`) and the control
// CLI (`/garage key/bucket/layout ...`), so pgcli execs it for admin operations.
// v2.4.1 is the current stable line; its config is a TOML file, not env, and it
// serves plaintext HTTP with no native TLS (front it with nginx for HTTPS).
const DefaultGarageImageTag = "docker.io/dxflrs/garage:v2.4.1"

// DefaultSeaweedfsImageTag pins the public SeaweedFS image
// (https://github.com/seaweedfs/seaweedfs, Apache-2.0) — a Go object store
// whose volume-manager design sits between MinIO's all-erasure-coding and
// Garage's all-replication models: data lives in append-only 32GB-capped
// volumes replicated by placement code (replication for hot data, optional
// erasure coding for warm), which is what makes it fast for many small
// objects. The "_large_disk" variant is built with `-tags large_disk` (higher
// per-volume file-descriptor/inode limits) and is what pgcli pins. Alpine
// base, runs as root, ships /usr/bin/weed as both daemon (mini/server) and
// control CLI (shell) — pulled directly, no wrapper image. 4.48 is the current
// stable line (verified 2026-09-28); it has native S3 TLS.
const DefaultSeaweedfsImageTag = "docker.io/chrislusf/seaweedfs:4.48_large_disk"

// DefaultMCImageTag is the public pre-built MinIO client (mc) image: the
// static upstream binary on scratch with a CA bundle. `pg mc` runs it in a
// throwaway container. Not a yaml field — mc is a client, not an addon — so
// the const is the single source of truth; keep it in sync with the Makefile's
// MC_IMAGE ($(MC_TS) is derived from MC_VERSION).
const DefaultMCImageTag = "ghcr.io/mars-base/pgcli/pgcli-mc:20250813083541"

// DefaultPgBouncerImageTag pins the PgBouncer image to a specific upstream
// release rather than :latest, so installs are reproducible. edoburu/pgbouncer
// tags its images vX.Y.Z-pN; v1.25.2-p0 is the release currently tracked. Keep
// this and the Makefile (if one ever builds it) in sync.
const DefaultPgBouncerImageTag = "docker.io/edoburu/pgbouncer:v1.25.2-p0"

// DefaultPostgrestImageTag pins the PostgREST image to a specific upstream
// release rather than :latest, so installs are reproducible. postgrest/postgrest
// is the official multi-arch image on docker.io (pull-only — pgcli never builds
// it). v16.3 is the current stable line; keep this in sync when bumping.
const DefaultPostgrestImageTag = "docker.io/postgrest/postgrest:v16.3"

// DefaultPgAdminImageTag pins the pgAdmin 4 image to a specific upstream
// release rather than :latest, so installs are reproducible. dpage/pgadmin4 is
// the official multi-arch image on docker.io (pull-only — pgcli never builds
// it; unlike rustfs it needs no wrapper because its own entrypoint chowns
// /var/lib/pgadmin to uid 5050 and drops privileges). 9.18 is the current
// stable line (== latest at pinning time); the :snapshot tag is a nightly build
// and is deliberately never used. Keep this in sync when bumping.
const DefaultPgAdminImageTag = "docker.io/dpage/pgadmin4:9.18"

// DefaultGrafanaImageTag pins the Grafana image. Note: since release 12.4.0 the
// grafana/grafana-oss Docker Hub repo is FROZEN — the OSS build is published as
// grafana/grafana (identical content), so that is what pgcli pulls. The default
// tag has no suffix, i.e. the Alpine base variant (Grafana's recommended image);
// -ubuntu / -distroless / -slim variants exist but pgcli never selects them.
// 13.2.4 is a fixed stable release (verified on Docker Hub 2026-10-13). The
// image runs as the fixed uid 472 (group root, gid 0) and its /run.sh does NOT
// chown or drop privileges — see internal/podman.GrafanaManager for how pgcli
// makes a bind-mounted /var/lib/grafana writable by 472.
const DefaultGrafanaImageTag = "docker.io/grafana/grafana:13.2.4"

// DefaultPrometheusImageTag pins the Prometheus image. prom/prometheus is the
// official multi-arch image on docker.io (pull-only — pgcli never builds it).
// The default tag has no suffix, i.e. the busybox variant (the recommended
// non-distroless build, with a shell for debugging); a -distroless variant
// exists but pgcli never selects it. v3.15.0 is a fixed stable release on the
// current v3 line (verified on Docker Hub 2026-10-14; latest == v3.15.0 at
// pinning time). v3.13.x is the parallel LTS patch line, deliberately not
// pinned. The image runs as the fixed nobody uid 65534 (gid 65534) and its
// entrypoint does NOT chown or drop privileges — see
// internal/podman.PrometheusManager for how pgcli makes a bind-mounted
// /prometheus writable by 65534. Keep this in sync when bumping.
const DefaultPrometheusImageTag = "docker.io/prom/prometheus:v3.15.0"

// DefaultAlertmanagerImageTag pins the Alertmanager image. prom/alertmanager is
// the official multi-arch image on docker.io (pull-only — pgcli never builds it).
// The tag has no suffix, which for this image is simply the busybox-based
// build (unlike prom/prometheus there is no -alpine/-ubuntu family to choose;
// a -distroless variant exists and pgcli never selects it). It bundles both the
// alertmanager daemon and the amtool CLI. v0.34.1 is a fixed stable release on
// the current v0.34 line (verified 2026-10-15 via the GitHub releases API). The
// image runs as the fixed nobody uid 65534 (gid 65534) and does not fix up bind
// mounts — see internal/podman.AlertmanagerManager for how pgcli makes a
// bind-mounted /alertmanager writable by 65534. Keep this in sync when bumping.
const DefaultAlertmanagerImageTag = "docker.io/prom/alertmanager:v0.34.1"

// DefaultVictoriaMetricsImageTag pins the VictoriaMetrics single-node image.
// victoriametrics/victoria-metrics is the official multi-arch image on
// docker.io (pull-only — pgcli never builds it). v1.154.1 is a fixed stable
// release (2026-10-19). The image runs as root, so no pre-chown of the data
// dir is needed (unlike Prometheus's uid 65534). Keep this in sync when bumping.
const DefaultVictoriaMetricsImageTag = "docker.io/victoriametrics/victoria-metrics:v1.154.1"

// DefaultVMInsertImageTag pins the VictoriaMetrics cluster vminsert image.
// victoriametrics/vminsert is the official image on docker.io (pull-only — pgcli
// never builds it). The -cluster suffix is the cluster variant; without it the
// image is the single-node binary. v1.154.1-cluster matches the single-node pin.
const DefaultVMInsertImageTag = "docker.io/victoriametrics/vminsert:v1.154.1-cluster"

// DefaultVMSelectImageTag pins the VictoriaMetrics cluster vmselect image.
// Same release cadence as vminsert.
const DefaultVMSelectImageTag = "docker.io/victoriametrics/vmselect:v1.154.1-cluster"

// DefaultVMStorageImageTag pins the VictoriaMetrics cluster vmstorage image.
// Same release cadence as the single-node image.
const DefaultVMStorageImageTag = "docker.io/victoriametrics/vmstorage:v1.154.1-cluster"

// DefaultVictoriaLogsImageTag pins the VictoriaLogs single-node image.
// victoriametrics/victoria-logs is the official multi-arch image on docker.io
// (pull-only — pgcli never builds it). VictoriaLogs is the log database from the
// same family: one binary, one container, one HTTP port (9428) serving ingest,
// query, and the built-in UI.
const DefaultVictoriaLogsImageTag = "docker.io/victoriametrics/victoria-logs:v1.53.0"

// DefaultGiteaImageTag pins the Gitea image. gitea/gitea is the official
// multi-arch image on docker.io (pull-only — pgcli never builds it). Note the
// version line: Gitea DROPPED the historical "1." prefix in late 2026, so the
// release that would have been 1.28.0 is 28.0.0; 28.1.0 is the current latest
// (verified on Docker Hub 2026-10-15). pgcli pins the ROOTFUL build (no
// suffix): a -rootless variant exists but the two data layouts are mutually
// incompatible, so the flavor must never flip across reinstalls. The rootful
// image runs its gitea process as the `git` user, default uid/gid 1000 (the
// entrypoint takes USER_UID/USER_GID), and expects /data owned by that uid —
// see internal/podman.GiteaManager for how pgcli pre-chowns the bind-mounted
// data dir. Keep this in sync when bumping.
const DefaultGiteaImageTag = "docker.io/gitea/gitea:28.1.0"

// BeeGFSImageRepo is the common ghcr prefix for the three BeeGFS server images
// — thinkparq publishes one repo per service role (beegfs-mgmtd, beegfs-meta,
// beegfs-storage) all sharing a version, so pgcli stores the version in
// BeeGFSConfig.ImageTag and the manager builds each ref as
// BeeGFSImageRepo + "-<role>:" + version. There is no client image: the BeeGFS
// client is a kernel module and cannot be containerised. Keep the version
// constant in sync when bumping.
const BeeGFSImageRepo = "ghcr.io/thinkparq/beegfs"

// DefaultBeeGFSVersion pins the BeeGFS server triad to a specific release.
// 8.4.0 is the current latest stable (verified on ghcr 2026-10-19). The three
// images run as root and need no bind-mount chown — see
// internal/podman.BeeGFSManager.
const DefaultBeeGFSVersion = "8.4.0"

// The four fixed BeeGFS listener ports. These are stock protocol ports, not
// pool-allocated: on the host network the daemons bind them directly, and in
// 8.4 only mgmtd can be moved (--beemsg-port/--grpc-port) while meta and
// storage have no supported listen-port knob (verified on vm01). The fields on
// BeeGFSConfig exist so the plan/summary layer can print and validate them;
// ApplyDefaults pins them to these constants and the CLI rejects a second
// filesystem on the same host because it would collide on 8005/8003.
const (
	DefaultBeeGFSMgmtdPort     = 8008 // mgmtd classic BeeMsg port (meta/storage register here)
	DefaultBeeGFSMgmtdGRPCPort = 8010 // mgmtd gRPC port (the `beegfs` CLI dials this)
	DefaultBeeGFSMetaPort      = 8005 // metadata service
	DefaultBeeGFSStoragePort   = 8003 // storage service
)

// DefaultRedisMajor is the Redis major version installed when --version is not
// given: "8", the current stable line upstream (docker.io/library/redis).
const DefaultRedisMajor = "8"

// DefaultRedisPort is Redis's standard port — the base of the addon's pool and
// the fallback for `pg redis-cli --host` when no local addon port applies.
const DefaultRedisPort = 6379

// RedisClusterMinMasters is the minimum number of masters a native-Redis
// cluster needs before `--cluster create` can cover all 16384 hash slots. pgcli
// uses it only to shape the install-summary hint: below this many masters'
// worth of configured members of one --cluster token (which for a
// --cluster-replicas N group is RedisClusterMinMasters*(1+N) nodes in total)
// the next-step line says "install N more" rather than giving a --cluster create
// command that would itself fail.
const RedisClusterMinMasters = 3

// RedisClusterMaxReplicas caps --cluster-replicas as a sanity guard against a
// typo'd huge number — Redis itself has no fixed upper limit here (bounded only
// by having enough nodes), so this is a "did you mean that?" ceiling, not a
// protocol rule.
const RedisClusterMaxReplicas = 10

// RedisClusterMinNodes is the smallest number of cluster-enabled nodes that can
// satisfy RedisClusterMinMasters once each master carries `replicas` followers:
// every master needs itself plus its replicas, so the total is
// RedisClusterMinMasters*(1+replicas). Negative replicas are clamped to 0 so a
// bad value can never make this smaller than the masters-only floor.
func RedisClusterMinNodes(replicas int) int {
	if replicas < 0 {
		replicas = 0
	}
	return RedisClusterMinMasters * (1 + replicas)
}

// redisMajorImages maps a Redis major to the default upstream image tag. This
// is pgcli's first version-selection mechanism — every other add-on pins a
// single image — so the shape is deliberately local: a table plus the two
// resolvers below, not an abstraction other add-ons must conform to. Should a
// second versioned add-on appear (PG majors, etcd), lift this pattern rather
// than invent a new one.
//
// Tags track the latest patch release of each major; bump them with the
// matching docs/images.md row. Both are plain upstream images — pgcli builds no
// wrapper for Redis (it runs as container root and drops privileges itself).
var redisMajorImages = map[string]string{
	"7": "docker.io/library/redis:7.4.11",
	"8": "docker.io/library/redis:8.10.2",
}

// RedisMajors returns the selectable Redis majors, sorted ascending ("7", "8").
func RedisMajors() []string {
	majors := make([]string, 0, len(redisMajorImages))
	for major := range redisMajorImages {
		majors = append(majors, major)
	}
	sort.Strings(majors)
	return majors
}

// RedisImageTagForMajor resolves a major to its default image tag; ok is false
// for anything outside the table, which callers turn into an error listing
// RedisMajors.
func RedisImageTagForMajor(major string) (string, bool) {
	tag, ok := redisMajorImages[major]
	return tag, ok
}

// RedisMajorForImageTag reverse-parses an image tag to its major version —
// the leading digit run after the colon ("docker.io/library/redis:7.4.11" →
// "7"). Used only for display when --image bypassed the table; returns "" for
// unparseable or unknown majors, in which case the tag is shown verbatim.
func RedisMajorForImageTag(imageTag string) string {
	idx := strings.LastIndex(imageTag, ":")
	if idx < 0 || idx == len(imageTag)-1 {
		return ""
	}
	digits := make([]byte, 0, 2)
	for _, ch := range imageTag[idx+1:] {
		if ch < '0' || ch > '9' {
			break
		}
		digits = append(digits, byte(ch))
	}
	if len(digits) == 0 {
		return ""
	}
	major := string(digits)
	if _, ok := redisMajorImages[major]; !ok {
		return ""
	}
	return major
}

// MinioConfig holds a standalone MinIO addon (single-node S3-compatible object
// storage). Like etcd/pgdog/haproxy it is shared infrastructure, so it lives at
// the top level (addons.minio.<name>). The typical use is a pgBackRest repository
// reachable by every host of a Patroni cluster, which local per-host backup dirs
// cannot be.
//
// The image is pulled from the public ghcr.io/mars-base/pgcli registry; MinIO's
// official image no longer ships the web console, so this one is built from the
// upstream .deb binary on Alpine.
type MinioConfig struct {
	ContainerName string `yaml:"container_name"`      // pgcli-minio<ns>-<name>
	Name          string `yaml:"name,omitempty"`      // addon key, defaults to the map key
	ImageTag      string `yaml:"image_tag,omitempty"` // ghcr.io/mars-base/pgcli/pgcli-minio:... (default)
	DataDir       string `yaml:"data_dir,omitempty"`  // host dir bound to /data; default <base-dir>/addon/minio/<name>/data
	Listen        string `yaml:"listen,omitempty"`    // bind address, default 127.0.0.1
	APIPort       int    `yaml:"api_port,omitempty"`  // S3 API host port, 9000+ auto-assigned
	ConsolePort   int    `yaml:"console_port,omitempty"`
	// RootUser / RootPassword become MINIO_ROOT_USER / MINIO_ROOT_PASSWORD.
	// The password is generated at first install and persisted here (same route
	// as the Patroni password set); it is never printed by pg addon list.
	RootUser     string `yaml:"root_user,omitempty"`     // default admin
	RootPassword string `yaml:"root_password,omitempty"` // generated on first install

	// TLS turns on MinIO's native HTTPS: pgcli generates a self-signed CA plus a
	// leaf cert (SAN: loopback, localhost, this host's NIC IPv4 addresses) under
	// <base-dir>/tls/minio-<name>/ and mounts them via --certs-dir. Required for
	// pgBackRest S3 repositories — pgBackRest refuses plaintext HTTP for S3
	// (upstream issue #1590 was rejected), so a MinIO that should receive WAL
	// archive-push has to speak TLS. Clients that trust the CA (pgcli passes
	// repo*-s3-ca-file to pgBackRest) verify normally; `pg mc` uses --insecure
	// since mc has no clean per-alias CA hook.
	TLS bool `yaml:"tls,omitempty"`

	// CertFile / KeyFile bring your own certificate instead of the generated
	// self-signed pair: host paths to a PEM leaf (+ intermediates chain, mounted
	// read-only as /opt/minio/certs/public.crt) and its private key (as
	// private.key). Implies TLS. Setting either without the other falls back to
	// generated certs. pgcli never copies or re-signs them — renewing means
	// replacing the files and recreating the container (install --force), since
	// single-file bind mounts pin the source inode.
	CertFile string `yaml:"cert_file,omitempty"`
	KeyFile  string `yaml:"key_file,omitempty"`

	// Endpoints switches this addon to distributed (cluster) mode when non-empty:
	// the list is passed verbatim to `minio server <ep1> <ep2> ...` instead of the
	// single-node `/data`, e.g. http://10.0.0.1:9000/data. The URL path is the
	// in-container export dir: with no Drives this is /data (pgcli bind-mounts
	// data_dir there — MNSD, one endpoint per node); together with Drives each
	// node contributes one endpoint per drive (/data1../dataN — MNMD, the
	// host×drive matrix). Any other path hits the container's root fs and MinIO
	// refuses it ("drive is part of root drive"). Every node of the cluster must
	// carry an identical list AND identical root credentials (MinIO has no
	// runtime membership join — all peers handshake from this list at startup),
	// so this value is written into each node's own pg.yaml. Empty =>
	// single-node mode, unchanged behaviour.
	Endpoints []string `yaml:"endpoints,omitempty"`

	// Drives makes this node contribute several export drives: each entry is a
	// host directory on its own device (MinIO rejects a drive sharing the root
	// device), bind-mounted in order at /data1../dataN. With Endpoints empty
	// that is single-node multi-drive (SNMD: `minio server /data1 /data2 ...`);
	// with Endpoints non-empty it is one node of a multi-node multi-drive
	// cluster (MNMD — argv is the shared endpoint matrix, and the matrix must
	// carry exactly one endpoint per drive per node, pointing at /data1../dataN
	// of each host). Mutually exclusive with data_dir (the single-drive export).
	// Mode resolution is: endpoints+drives=MNMD, endpoints=MNSD, drives=SNMD,
	// else single data_dir. Empty => unchanged.
	Drives []string `yaml:"drives,omitempty"`

	// Autostart brings this container up on host boot via the boot service
	// (pg autostart enable --minio). Start-only: it starts the existing
	// container, so install first.
	Autostart bool `yaml:"autostart,omitempty"`
}

// SiloConfig holds a standalone silo addon — the Pigsty fork of MinIO. It is
// the field-for-field twin of MinioConfig: silo keeps the S3 API, the MINIO_*
// env contract, the `server /data --address --console-address` command line,
// the --certs-dir public.crt/private.key layout, and the distributed EC
// endpoints mode, so every knob means exactly what it means for minio. The one
// operational difference is the image: docker.io/pgsty/silo ships the console
// and its mcli client, so it is pulled from docker.io (public, multi-arch)
// rather than built like pgcli-minio. Ports come from the same shared pool as
// minio (minio_start_port), so a host can run both without colliding.
type SiloConfig struct {
	ContainerName string `yaml:"container_name"`      // pgcli-silo<ns>-<name>
	Name          string `yaml:"name,omitempty"`      // addon key, defaults to the map key
	ImageTag      string `yaml:"image_tag,omitempty"` // docker.io/pgsty/silo:... (default)
	DataDir       string `yaml:"data_dir,omitempty"`  // host dir bound to /data; default <base-dir>/addon/silo/<name>/data
	Listen        string `yaml:"listen,omitempty"`    // bind address, default 127.0.0.1
	APIPort       int    `yaml:"api_port,omitempty"`  // S3 API host port, 9000+ auto-assigned
	ConsolePort   int    `yaml:"console_port,omitempty"`
	// RootUser / RootPassword become MINIO_ROOT_USER / MINIO_ROOT_PASSWORD (the
	// env contract silo inherits from MinIO). The password is generated at first
	// install and persisted here; it is never printed by pg addon list.
	RootUser     string `yaml:"root_user,omitempty"`     // default admin
	RootPassword string `yaml:"root_password,omitempty"` // generated on first install

	// TLS turns on silo's native HTTPS (same --certs-dir contract as MinIO):
	// pgcli generates a self-signed CA plus a leaf under
	// <base-dir>/tls/silo/<name>/ and mounts them via --certs-dir. Required for
	// pgBackRest S3 repositories — pgBackRest refuses plaintext HTTP for S3.
	TLS bool `yaml:"tls,omitempty"`

	// CertFile / KeyFile bring your own certificate instead of the generated
	// pair: host paths to a PEM leaf (+ chain, mounted read-only as
	// /opt/silo/certs/public.crt) and its private key (private.key). Implies
	// TLS. Setting either without the other falls back to generated certs.
	CertFile string `yaml:"cert_file,omitempty"`
	KeyFile  string `yaml:"key_file,omitempty"`

	// Endpoints switches this addon to distributed (cluster) mode when non-empty:
	// the list is passed verbatim to `silo server <ep1> <ep2> ...` instead of the
	// single-node `/data`. With Drives set too, the list is the host×drive
	// matrix (MNMD) whose export paths name /data1../dataN on each node; with
	// Drives empty it is one /data endpoint per node (MNSD). Same
	// identical-list-and-credentials requirement as minio. Empty => single-node.
	Endpoints []string `yaml:"endpoints,omitempty"`

	// Drives makes this node contribute several export drives: host dirs, each
	// on its own device, bind-mounted at /data1../dataN. With Endpoints empty
	// that is single-node multi-drive (SNMD: `silo server /data1 ...`); with
	// Endpoints non-empty it is one node of a multi-node multi-drive cluster
	// (MNMD — argv is the shared endpoint matrix, one endpoint per drive per
	// node at /data1../dataN). Mutually exclusive with data_dir. Empty =>
	// unchanged.
	Drives []string `yaml:"drives,omitempty"`

	// Autostart brings this container up on host boot via the boot service
	// (pg autostart enable --silo). Start-only: it starts the existing
	// container, so install first.
	Autostart bool `yaml:"autostart,omitempty"`
}

// DefaultJuicefsVersion pins the host juicefs binary the installer downloads
// from juicedata/juicefs GitHub releases. The digests are the release
// checksums.txt entries for the linux tarballs (asset-level, matching what
// hostbin.Ensure verifies); --version latest re-resolves from the GitHub API.
const (
	DefaultJuicefsVersion = "1.4.1"
	juicefsSHAlinuxamd64  = "01ee09a21a9351a465e09906f113845e1c6a19bea70f530e5e2b2125b2dd3b82"
	juicefsSHAlinuxarm64  = "1015ade83a7a93180a29f6c93ee5780a3eda52522331934ef2ef0cc0921995fd"
)

// JuicefsSHA256 returns the pinned checksum of the juicefs release tarball
// for a linux architecture (amd64/arm64). An unknown arch returns "" — the
// installer refuses those rather than downloading unverified.
func JuicefsSHA256(arch string) string {
	switch arch {
	case "amd64":
		return juicefsSHAlinuxamd64
	case "arm64":
		return juicefsSHAlinuxarm64
	}
	return ""
}

// JuicefsConfig holds a host-installed JuiceFS client mount: the juicefs
// binary (not a container — FUSE has to live on the host to serve a host
// mount point) formatting a volume over an EXTERNAL S3 service. There is no
// same-host addon auto-detection: both the object backend (S3Endpoint +
// explicit AccessKey/SecretKey) and the metadata engine (MetaDSN, a URL
// juicefs itself accepts — sqlite3://, redis://, postgres://…) are supplied
// explicitly and stored resolved. Like BeeGFS the addon manages the client
// lifecycle (format once, mount/umount on demand); everything runs on this host.
//
// The S3 bucket itself is provisioned manually (pg mc) — pgcli only reads the
// endpoint/credentials you give it and never creates buckets or accounts.
type JuicefsConfig struct {
	Name    string `yaml:"name,omitempty"`    // addon key, defaults to the map key
	Version string `yaml:"version,omitempty"` // pinned release tag, e.g. "1.4.1" (v-prefix optional)

	// BinaryPath is where the host binary lives after install (or the PATH
	// entry Detect found). Empty until installed.
	BinaryPath string `yaml:"binary_path,omitempty"`

	// MetaDSN is the metadata-engine URL supplied explicitly at install
	// (juicefs accepts sqlite3://, redis://, postgres://…). There is no
	// same-host engine auto-detection — the operator names the store.
	MetaDSN string `yaml:"meta_dsn,omitempty"`

	// S3 backend: an EXTERNAL (cloud or remote-host) S3 service, given as
	// S3Endpoint (http(s)://host[:port], no bucket path) plus explicit
	// AccessKey/SecretKey — pgcli never inherits or auto-discovers a local
	// addon's credentials. Bucket is never created by pgcli. JuiceFS bakes the
	// endpoint into its volume metadata at format time, so it needs no region;
	// ZeroFS signs with one — S3Region (optional, zerofs defaults to us-east-1).
	S3Endpoint string `yaml:"s3_endpoint,omitempty"`
	S3Region   string `yaml:"s3_region,omitempty"`
	Bucket     string `yaml:"bucket,omitempty"`
	AccessKey  string `yaml:"access_key,omitempty"`
	SecretKey  string `yaml:"secret_key,omitempty"`

	// Host paths, all defaulted under <base>/addon/juicefs/<name>/: the mount
	// point (created empty; the operator decides what goes under it), the
	// local read cache dir, the mount daemon's pid and log files.
	MountPoint   string `yaml:"mount_point,omitempty"`
	CacheDir     string `yaml:"cache_dir,omitempty"`
	MountPidFile string `yaml:"mount_pid_file,omitempty"`
	MountLogFile string `yaml:"mount_log_file,omitempty"`

	// Autostart remounts on host boot via the boot service
	// (pg autostart enable --juicefs). Mount-only: the binary and volume must
	// already exist, so install first.
	Autostart bool `yaml:"autostart,omitempty"`
}

// DefaultZerofsVersion pins the ZeroFS (Barre/ZeroFS) release. One tarball
// carries every platform's binary; the digest is the shipped .sha256 sidecar
// of that tarball, and the installer extracts zerofs-linux-<arch>-pgo.
const (
	DefaultZerofsVersion = "2.3.5"
	zerofsTarballSHA256  = "367b2e79e05167af8409b28322f21b1ae5de033fc40820a3c27684a9974a56f2"

	// DefaultZerofsPort is the stock 9P listener port ZeroFS documents.
	DefaultZerofsPort = 5564
)

// ZerofsTarballSHA256 is the checksum of the release tarball for any arch (the
// asset is multi-platform).
func ZerofsTarballSHA256() string { return zerofsTarballSHA256 }

// ZerofsConditionalPut modes for the fencing primitive ZeroFS needs from the
// object store: auto probes the backing minio image tag, redis routes through
// a conditional-put helper on a pgcli redis addon, none trusts the store
// (only safe on a single-writer setup the operator controls).
const (
	ZerofsConditionalPutAuto  = "auto"
	ZerofsConditionalPutRedis = "redis"
	ZerofsConditionalPutNone  = "none"
)

// ValidZerofsConditionalPut reports whether mode is one of the three above.
func ValidZerofsConditionalPut(mode string) bool {
	switch mode {
	case ZerofsConditionalPutAuto, ZerofsConditionalPutRedis, ZerofsConditionalPutNone:
		return true
	}
	return false
}

// ZerofsConfig holds a host-installed ZeroFS deployment: one binary running
// as a 9P server over an encrypted LSM on the S3 addon (like JuiceFS's,
// minio or silo — but here the bucket is the whole filesystem, so ZeroFS
// additionally demands conditional-put support for fencing) plus a FUSE
// client mounting it. pgcli writes the server's config.toml; the 9P listener
// stays on loopback because the protocol has no authentication.
type ZerofsConfig struct {
	Name    string `yaml:"name,omitempty"`    // addon key, defaults to the map key
	Version string `yaml:"version,omitempty"` // pinned release tag, e.g. "2.3.5"

	BinaryPath string `yaml:"binary_path,omitempty"`

	// S3 backend, same contract as JuicefsConfig: an EXTERNAL endpoint given
	// explicitly (S3Endpoint + AccessKey/SecretKey), bucket provisioned by hand.
	// ZeroFS signs requests, so S3Region is honoured (optional; zerofs itself
	// defaults to us-east-1).
	S3Endpoint string `yaml:"s3_endpoint,omitempty"`
	S3Region   string `yaml:"s3_region,omitempty"`
	Bucket     string `yaml:"bucket,omitempty"`
	AccessKey  string `yaml:"access_key,omitempty"`
	SecretKey  string `yaml:"secret_key,omitempty"`

	// EncryptionPassword encrypts the LSM at rest (client-side; the S3 addon
	// sees only opaque objects). Always generated at install — losing it
	// loses the data.
	EncryptionPassword string `yaml:"encryption_password,omitempty"`

	// Server storage/cache: CacheDir doubles as the server's on-disk object
	// cache ([cache] dir) and the client's fuse cache; ServerCacheGB sizes the
	// server cache ([cache] size_in_gb). ConfigPath is the written config.toml.
	CacheDir      string `yaml:"cache_dir,omitempty"`
	ServerCacheGB int    `yaml:"server_cache_gb,omitempty"`
	ConfigPath    string `yaml:"config_path,omitempty"`

	// Listen is the 9P bind HOST (forced loopback by Validate — no auth on
	// 9P); ListenPort the TCP port (default 5564; pool-free, it is not an HTTP
	// API port).
	Listen         string `yaml:"listen,omitempty"`
	ListenPort     int    `yaml:"listen_port,omitempty"`
	ConditionalPut string `yaml:"conditional_put,omitempty"`

	// ConditionalPutURL is the fencing backend URL supplied explicitly (via
	// --redis-dsn) when ConditionalPut is "redis". It is what reaches
	// config.toml's `conditional_put` key; ConditionalPut itself stays the
	// enum so Validate and `pg config show` keep working on raw files.
	ConditionalPutURL string `yaml:"conditional_put_url,omitempty"`

	MountPoint string `yaml:"mount_point,omitempty"`

	// Process bookkeeping for the two long-lived pieces: the 9P server and the
	// FUSE mount client; both log to LogFile.
	ServerPidFile string `yaml:"server_pid_file,omitempty"`
	MountPidFile  string `yaml:"mount_pid_file,omitempty"`
	LogFile       string `yaml:"log_file,omitempty"`

	// Autostart restarts server+client on host boot (pg autostart enable
	// --zerofs). Start-only: install first.
	Autostart bool `yaml:"autostart,omitempty"`
}

// DefaultDuckdbVersion pins the DuckDB CLI version downloaded from
// duckdb/duckdb GitHub releases. v1.5.x uses .zip, v2.0.x uses .tar.gz.
const (
	DefaultDuckdbVersion = "1.5.6"
	duckdbSHA_1_5_6     = "6e89deac1ebbc36eed0291caf8b567b030c7b86ac35998f71854e22b3c5d5e2f" // duckdb_cli-linux-amd64.zip
	duckdbSHA_2_0_0     = "c6507206baf7793c6013dbd82368469a45f20dc0e17a6f92f204602f652ed2bc" // duckdb-cli-linux-amd64.tar.gz
)

// DuckdbSHA256 returns the pinned checksum for a specific DuckDB version
// and architecture (amd64 only). Unknown version/arch returns "".
func DuckdbSHA256(version, arch string) string {
	if arch != "amd64" {
		return ""
	}
	v := strings.TrimPrefix(version, "v")
	switch v {
	case "1.5.6":
		return duckdbSHA_1_5_6
	case "2.0.0":
		return duckdbSHA_2_0_0
	default:
		return ""
	}
}

// DuckdbConfig holds a host-installed DuckDB CLI deployment: the duckdb
// binary (not a container — it's a standalone CLI tool) providing an embedded
// OLAP database for analytical queries on Parquet/CSV/JSON files.
type DuckdbConfig struct {
	Name       string `yaml:"name,omitempty"`        // addon key, defaults to the map key
	Version    string `yaml:"version,omitempty"`     // pinned release tag, e.g. "1.5.6"
	BinaryPath string `yaml:"binary_path,omitempty"` // resolved path to duckdb binary
	DataDir    string `yaml:"data_dir,omitempty"`    // default database directory, <base>/addon/duckdb/<name>
}

// RustfsConfig holds a standalone rustfs addon. Its field set is a
// field-for-field twin of MinioConfig/SiloConfig so the CLI surface and config
// shape stay uniform across the three object stores, but rustfs shares NONE of
// MinIO's runtime contract (see DefaultRustfsImageTag and internal/podman
// RustfsManager): it runs as a fixed non-root user (uid/gid 10001, so every
// bind dir must be owned by that uid), configures itself via RUSTFS_* env
// rather than `server <path>` argv, names its TLS pair rustfs_cert.pem/
// rustfs_key.pem, and supports only three topologies. Consequently these two
// fields mean something narrower than they do for minio/silo:
//   - Endpoints is a multi-node (MNMD) node list, one scheme://host:port per
//     node; there is NO multi-node single-drive mode, so Endpoints with an
//     empty Drives is rejected at install rather than forming an MNSD cluster.
//   - Drives are the per-node export dirs; rustfs hard-refuses drives sharing a
//     physical device at startup (no pgcli-side cheap check — it surfaces as a
//     fatal in the container log).
//
// RootUser/RootPassword become RUSTFS_ACCESS_KEY/RUSTFS_SECRET_KEY (rustfs
// accepts no minimum length; the reserved default "rustfsadmin" only warns).
// Ports draw from the same shared pool as minio and silo (minio_start_port).
type RustfsConfig struct {
	ContainerName string `yaml:"container_name"`      // pgcli-rustfs<ns>-<name>
	Name          string `yaml:"name,omitempty"`      // addon key, defaults to the map key
	ImageTag      string `yaml:"image_tag,omitempty"` // ghcr.io/mars-base/pgcli/pgcli-rustfs:... (default)
	DataDir       string `yaml:"data_dir,omitempty"`  // host dir bound to /data; default <base-dir>/addon/rustfs/<name>/data
	Listen        string `yaml:"listen,omitempty"`    // bind address, default 127.0.0.1
	APIPort       int    `yaml:"api_port,omitempty"`  // S3 API host port, 9000+ auto-assigned
	ConsolePort   int    `yaml:"console_port,omitempty"`
	RootUser      string `yaml:"root_user,omitempty"`     // → RUSTFS_ACCESS_KEY, default admin
	RootPassword  string `yaml:"root_password,omitempty"` // → RUSTFS_SECRET_KEY, generated on first install

	// TLS turns on rustfs's native HTTPS: pgcli generates a self-signed CA plus
	// a leaf under <base-dir>/tls/rustfs/<name>/ and mounts the dir via
	// RUSTFS_TLS_PATH, naming the files rustfs_cert.pem/rustfs_key.pem (the only
	// names rustfs reads). The dir must be owned by the container uid (10001) or
	// rustfs cannot read the cert. Required for pgBackRest S3 repositories.
	TLS bool `yaml:"tls,omitempty"`

	// CertFile / KeyFile bring your own certificate instead of the generated
	// pair: host paths to a PEM leaf and its private key, mounted read-only at
	// the two names rustfs wants (rustfs_cert.pem / rustfs_key.pem). Implies
	// TLS. Setting either without the other falls back to generated certs. The
	// operator's files are never chowned by pgcli, so they must already be
	// readable by uid 10001 or rustfs fails to start on TLS.
	CertFile string `yaml:"cert_file,omitempty"`
	KeyFile  string `yaml:"key_file,omitempty"`

	// Endpoints switches this addon to multi-node (MNMD) mode: one
	// scheme://host:port per node. rustfs has no multi-node single-drive mode, so
	// Endpoints requires Drives — the pair forms the shared node×drive matrix.
	// Empty => single-node.
	Endpoints []string `yaml:"endpoints,omitempty"`

	// Drives makes this node contribute several export drives: host dirs, each on
	// its own device, bind-mounted at /data/rustfs0../rustfsN-1 (0-indexed, the
	// rustfs layout). With Endpoints empty that is single-node multi-drive (SNMD);
	// with Endpoints non-empty it is one node of an MNMD cluster. rustfs refuses
	// drives sharing a physical device at startup. Mutually exclusive with
	// data_dir. Empty => single-node single-drive (SNSD).
	Drives []string `yaml:"drives,omitempty"`

	// Autostart brings this container up on host boot via the boot service
	// (pg autostart enable --rustfs). Start-only: it starts the existing
	// container, so install first.
	Autostart bool `yaml:"autostart,omitempty"`
}

// GarageConfig holds a standalone Garage addon — the Deuxfleurs S3-compatible
// object store (https://garagehq.deuxfleurs.fr, AGPLv3), a Rust-native
// alternative to the archived MinIO. Like rustfs it is a sibling manager rather
// than a flag on MinioManager, and shares none of MinIO's runtime contract.
// Four things diverge from the rustfs/minio/silo surface (see
// internal/podman.GarageManager):
//
//  1. Config is a rendered TOML file (/etc/garage.toml), not env. pgcli writes
//     it under the instance dir and bind-mounts it read-only.
//  2. Two on-disk dirs, not one: MetadataDir (LMDB metadata — the small
//     frequently-touched store) and DataDir (the data blocks). rustfs/minio/
//     silo use a single DataDir.
//  3. Four ports, not the object stores' API+console pair: S3 API, node-to-node
//     RPC, the s3_web static-site endpoint, and the admin/metrics endpoint. They
//     come from Garage's OWN pool (garage_start_port, default 3900) as four
//     consecutive ports, not the shared minio_start_port cursor.
//  4. No native TLS — Garage serves plaintext HTTP only. pgBackRest's S3 repo
//     requires HTTPS, so front Garage with the nginx addon for TLS. Consequently
//     there are no TLS/CertFile/KeyFile fields here (install rejects --tls).
//
// Two topologies: single-node (v1 — ReplicationFactor 1, the container runs
// `garage server --single-node --default-bucket` and seeds credentials from
// env) and multi-node cluster (v2 — Cluster set; each host installs its own
// member, joins via known Peers, and the CLI drives `garage node connect` +
// `layout assign/apply` — see internal/cli.addon_garage.go). Peers are
// `id@addr` strings only; remote members never appear as pg.yaml entries, so
// ApplyDefaults and the port pool treat every Garage entry as local.
//
// Credentials differ in kind from the other stores: Garage uses access/secret
// keys. Single-node seeds them via GARAGE_DEFAULT_ACCESS_KEY /
// GARAGE_DEFAULT_SECRET_KEY env plus one default bucket (--default-bucket,
// named after the instance). A cluster cannot use that env path — the CLI
// creates the key (`garage key create`, Garage-manned GK+hex/64hex shape) and
// bucket once quorum forms, storing the results back into RootUser/RootPassword.
// RootUser/RootPassword keep the CLI-uniform names but map to those keys (the
// access key carries Garage's "GK-" prefix).
type GarageConfig struct {
	ContainerName string `yaml:"container_name"`      // pgcli-garage<ns>-<name>
	Name          string `yaml:"name,omitempty"`      // addon key, defaults to the map key
	ImageTag      string `yaml:"image_tag,omitempty"` // docker.io/dxflrs/garage:... (default)

	// MetadataDir and DataDir are the two host dirs Garage needs, bound to the
	// config's metadata_dir/data_dir. Defaults: <base>/addon/garage/<name>/{meta,data}.
	// Drives (multi-disk) replaces DataDir when set — the two are mutually
	// exclusive. Garage's data_dir accepts an array of { path, capacity }
	// tables since v0.9.0 (blocks spread across the dirs, balanced by per-dir
	// capacity). v2.4 REQUIRES a capacity on every entry, so pgcli fills each
	// with its filesystem's whole usable size — there is no per-drive capacity
	// knob. metadata_dir never joins the set: it is a single small LMDB dir by
	// upstream design.
	MetadataDir string   `yaml:"metadata_dir,omitempty"`
	DataDir     string   `yaml:"data_dir,omitempty"`
	Drives      []string `yaml:"drives,omitempty"`

	ConfigFile string `yaml:"config_file,omitempty"` // host path of the rendered garage.toml; default <base>/addon/garage/<name>/garage.toml

	Listen    string `yaml:"listen,omitempty"`     // bind address, default 127.0.0.1
	APIPort   int    `yaml:"api_port,omitempty"`   // S3 API host port, from garage_start_pool
	RPCPort   int    `yaml:"rpc_port,omitempty"`   // node-to-node RPC host port
	WebPort   int    `yaml:"web_port,omitempty"`   // s3_web static-site host port
	AdminPort int    `yaml:"admin_port,omitempty"` // admin API + metrics host port

	RootUser     string `yaml:"root_user,omitempty"`     // access key id (GK-prefixed): env-seeded single-node, `key create`-seeded clustered
	RootPassword string `yaml:"root_password,omitempty"` // secret key, paired with RootUser

	// RPCSecret is the 32-byte hex cluster secret (garage.toml rpc_secret).
	// MUST be identical on every member of a cluster — generated on the
	// bootstrap node and carried to joiners via --rpc-secret (operator-
	// transported; pgcli never executes on a remote host). Persisted so
	// restarts keep the same identity.
	RPCSecret string `yaml:"rpc_secret,omitempty"`

	Region            string `yaml:"region,omitempty"`             // s3_region, default "garage"
	ReplicationFactor int    `yaml:"replication_factor,omitempty"` // default 1; >1 requires Cluster

	// --- Multi-node (v2). Cluster non-empty switches the container off
	// --single-node and onto the cluster lifecycle (connect/assign/apply). ---
	Cluster string `yaml:"cluster,omitempty"` // cluster group name; non-empty = cluster mode
	// AdvertiseHost is this node's rpc_public_addr host (the LAN IP peers
	// dial). Required in cluster mode; empty in single-node (loopback only).
	AdvertiseHost string `yaml:"advertise_host,omitempty"`
	// Zone and Capacity(GiB) drive `garage layout assign -z/-c` for this node.
	// Capacity 0 means "start the node but don't auto-assign a role".
	Zone     string `yaml:"zone,omitempty"`
	Capacity int    `yaml:"capacity,omitempty"`
	// Peers are `id@addr` strings this node connects to on install. Remote
	// members exist ONLY here — never as map entries (no RemoteHost pattern).
	Peers []string `yaml:"peers,omitempty"`
	// NodeID is this node's `<64hex>@<addr>` identity, read back from
	// `garage node id` after first boot and cached for display/re-join. The
	// node keypair itself lives in MetadataDir.
	NodeID string `yaml:"node_id,omitempty"`

	// Autostart brings this container up on host boot via the boot service
	// (pg autostart enable --garage). Start-only: it starts the existing
	// container, so install first.
	Autostart bool `yaml:"autostart,omitempty"`
}

// Clustered reports whether this Garage entry runs in multi-node cluster mode.
func (g GarageConfig) Clustered() bool { return g.Cluster != "" }

// RPCPublicAddr renders the garage.toml rpc_public_addr value (this node's
// peer-dialable address). Bracket-safe for IPv6. Meaningful only when
// AdvertiseHost is set (cluster mode).
func (g GarageConfig) RPCPublicAddr() string {
	if g.AdvertiseHost == "" {
		return ""
	}
	host := g.AdvertiseHost
	if strings.Contains(host, ":") { // IPv6 literal / dual-stack wildcard
		host = "[" + host + "]"
	}
	return fmt.Sprintf("%s:%d", host, g.RPCPort)
}

// SeaweedfsConfig holds a standalone SeaweedFS addon — the Go object store at
// https://github.com/seaweedfs/seaweedfs (Apache-2.0). It is a sibling manager
// to garage/minio/rustfs/silo, not a flag on any of them, and shares Garage's
// "render a config file, bind-mount it read-only" shape plus MinIO's
// "static-membership, same command replicated to every host" cluster shape
// (see internal/podman.SeaweedfsManager). Where it diverges from Garage:
//
//  1. Four ROLES in one container (master Raft, volume server, filer, S3
//     gateway), not one daemon with four ports for one process. Single-node
//     runs `weed mini` (all roles, one process, upstream's blessed single-
//     node production form); cluster runs `weed server -filer -s3`.
//  2. Ports are four CONSECUTIVE (master, volume, filer, s3) from SeaweedFS's
//     own pool (seaweedfs_start_port, default 8400) — like Garage's own cursor,
//     not the shared minio cursor. Every role also opens a gRPC port at
//     http+10000 (verified on 4.48 for all four roles, not just filer), which
//     the pool reserves as used but never assigns as a knob.
//  3. Native S3 TLS (verified on vm01): --tls generates a self-signed pair and
//     --tls-cert/--tls-key bring your own (a pg cert leaf works directly), the
//     same contract as minio/rustfs — NOT Garage's nginx-front posture. With
//     TLS on, the S3 role serves HTTPS on the pooled port and stops listening
//     in cleartext entirely (-s3.port=0 -s3.port.https=P).
//  4. Cluster metadata: every node runs filer+gateway, so filer metadata MUST
//     live in a shared store — pgcli points it at a PostgreSQL via an explicit
//     --dsn (a host-published/LAN address, since the addon container cannot
//     reach an instance's container-local 127.0.0.1:5432; the operator
//     pre-creates the database). This is the plan's whole reason a cluster is
//     different from a bigger single node. Credentials follow the same shared
//     rule: one --root-user/--root-password pair for the whole cluster, because
//     each gateway authenticates against only its own s3.json.
//
// Cluster membership is MinIO-MNMD-style, not Garage's bootstrap/join: the
// Raft peer list (-master.peers) is a FULL static list every node passes, so
// the same install command runs on each host with only --advertise-host
// differing. There is no designated first node; a node whose peers are not all
// up yet just waits for quorum (the install health gate warns, never fails).
//
// Credentials are a single static identity in a rendered s3.json (4.48
// schema: identities[].credentials is an ARRAY of {accessKey,secretKey}, and
// the bare "Admin" action short-circuits every bucket check — per-bucket
// action maps are gone). RootUser/RootPassword keep the CLI-uniform names and
// map to that access/secret key pair. RootUser/RootPassword/FilerDSN are NOT
// defaulted by ApplyDefaults (determinism contract, same as Garage) — the
// install handler generates the pair and resolves the DSN.
type SeaweedfsConfig struct {
	ContainerName string `yaml:"container_name"`      // pgcli-seaweedfs<ns>-<name>
	Name          string `yaml:"name,omitempty"`      // addon key, defaults to the map key
	ImageTag      string `yaml:"image_tag,omitempty"` // docker.io/chrislusf/seaweedfs:... (default)

	// DataDir is the single host dir behind the container's -dir (volumes +
	// leveldb2 filer metadata when no FilerDSN). Default <base>/addon/
	// seaweedfs/<name>/data. Mutually exclusive with Drives.
	DataDir string `yaml:"data_dir,omitempty"`

	// Drives turns a single node into a multi-disk volume server: one host dir
	// per disk, each separately mounted (weed's -dir is a comma list dir[,dir]…,
	// and 4.48 reports each as its own DiskStatuses entry — verified). When set
	// they are bind-mounted at /data1../dataN and -dir lists those paths; empty
	// keeps the single DataDir at /data. Same semantics as MinIO/silo's SNMD;
	// pgcli only warns (never blocks) when a drive shares the host root device.
	Drives []string `yaml:"drives,omitempty"`

	// Listen binds every role's socket (-ip.bind) AND, in single-node mode,
	// the advertised -ip. Default 127.0.0.1. In cluster mode -ip is
	// AdvertiseHost instead (see the -ip trap documented on the manager).
	Listen string `yaml:"listen,omitempty"`

	// Port is the pool anchor = master HTTP port; Volume/Filer/S3 take the
	// next three. All four have a gRPC twin at +10000 that the pool records as
	// used.
	Port         int    `yaml:"port,omitempty"`          // master HTTP, from seaweedfs_start_port
	VolumePort   int    `yaml:"volume_port,omitempty"`   // volume server HTTP
	FilerPort    int    `yaml:"filer_port,omitempty"`    // filer HTTP
	S3Port       int    `yaml:"s3_port,omitempty"`       // S3 gateway HTTP, or HTTPS when TLS
	RootUser     string `yaml:"root_user,omitempty"`     // s3.json access key
	RootPassword string `yaml:"root_password,omitempty"` // s3.json secret key

	Bucket      string `yaml:"bucket,omitempty"`      // auto-created bucket, default = Name
	Replication string `yaml:"replication,omitempty"` // placement code, default "000" (single), e.g. "001"/"002" cluster

	// --- Multi-node. Cluster non-empty = cluster mode (weed server, shared
	// filer store). AdvertiseHost is this node's -ip that peers dial; required
	// in cluster mode. Peers is the FULL Raft list (ip:masterPort) written on
	// every node — the operator supplies the others' via --peer, pgcli always
	// folds in this node's own AdvertiseHost:Port. ---
	Cluster       string   `yaml:"cluster,omitempty"`
	AdvertiseHost string   `yaml:"advertise_host,omitempty"`
	Peers         []string `yaml:"peers,omitempty"`

	// FilerDSN is the shared PostgreSQL the cluster's filer metadata lives in
	// (rendered into filer.toml [postgres2]). Given via --dsn only, and it must
	// be dialable from every host (a host-published port / LAN address).
	// Required in cluster mode, optional single-node (default: filer metadata
	// stays in DataDir's leveldb2).
	FilerDSN string `yaml:"filer_dsn,omitempty"`

	// TLS is native S3 TLS: TLS generates a self-signed cert/key under the
	// instance dir; CertFile/KeyFile bring your own (pg cert output). When
	// either is set, S3Port serves HTTPS and the cleartext listener is off.
	TLS      bool   `yaml:"tls,omitempty"`
	CertFile string `yaml:"cert_file,omitempty"` // host path (BYO or generated), mounted as cert.pem
	KeyFile  string `yaml:"key_file,omitempty"`  // host path, mounted as key.pem

	// VolumeSizeMB is -master.volumeSizeLimitMB. mini defaults to 128, far too
	// small for a real deployment; pgcli defaults 1024.
	VolumeSizeMB uint `yaml:"volume_size_mb,omitempty"`

	// Autostart brings this container up on host boot via the boot service
	// (pg autostart enable --seaweedfs). Start-only, so install first.
	Autostart bool `yaml:"autostart,omitempty"`
}

// Clustered reports whether this SeaweedFS entry runs the multi-node topology.
func (s SeaweedfsConfig) Clustered() bool { return s.Cluster != "" }

// TLSOn reports whether the S3 gateway should serve HTTPS (either a
// pgcli-generated cert or a bring-your-own pair).
func (s SeaweedfsConfig) TLSOn() bool { return s.TLS || s.CertFile != "" || s.KeyFile != "" }

// GRPCPort returns a role's gRPC twin port (http + 10000). SeaweedFS opens
// these implicitly; the pool only needs to know they're taken, not assign
// them.
func SeaweedfsGRPCPort(httpPort int) int { return httpPort + 10000 }

// RedisConfig holds a standalone Redis addon: a KV store for cache, session,
// ranking and atomic-counter data, run as its own container off the upstream
// docker.io/library/redis image (no wrapper build — Redis runs fine as root
// inside the container, so there is no rustfs-style uid-drop contract).
//
// Version/ImageTag implement the first version-selection mechanism in pgcli:
// Version is the major ("7" | "8"), ImageTag the resolved full reference
// (redisMajorImages below). --image overrides ImageTag directly and Version is
// then reverse-parsed for display only. Redis defaults to Listen 0.0.0.0 with
// Password always generated (requirepass), since a port published on every
// interface is meaningless without auth. Persistence is the native RDB
// snapshot into /data (DataDir); AOF is opt-in via AOF. The persistence and
// eviction knobs (SaveSchedule, AppendFsync, MaxMemoryPolicy) stay opt-in too:
// an empty value means "Redis's own default", so the flags widen the surface
// without ever changing the shape of a default install.
type RedisConfig struct {
	ContainerName string `yaml:"container_name"`      // pgcli-redis<ns>-<name>
	Name          string `yaml:"name,omitempty"`      // addon key, defaults to the map key
	Version       string `yaml:"version,omitempty"`   // Redis major: "7" | "8"
	ImageTag      string `yaml:"image_tag,omitempty"` // docker.io/library/redis:<x.y.z>, resolved from Version
	DataDir       string `yaml:"data_dir,omitempty"`  // host dir bound to /data; default <base-dir>/addon/redis/<name>/data
	Listen        string `yaml:"listen,omitempty"`    // bind address, default 0.0.0.0
	Port          int    `yaml:"port,omitempty"`      // host port, 6379+ auto-assigned
	Password      string `yaml:"password,omitempty"`  // → requirepass, generated on first install
	MaxMemory     string `yaml:"maxmemory,omitempty"` // optional cap, e.g. "256mb"; empty = unlimited

	// MaxMemoryPolicy is the eviction policy paired with MaxMemory. Empty means
	// allkeys-lru — the cache default pgcli pins when --maxmemory is given
	// without a policy. Only meaningful with a MaxMemory cap.
	MaxMemoryPolicy string `yaml:"maxmemory_policy,omitempty"`

	// AOF turns on appendonly: a write-ahead log under DataDir in front of the
	// RDB snapshot, for crash-safe no-eviction stores. Off by default.
	AOF bool `yaml:"aof,omitempty"`

	// AppendFsync is the AOF flush strength: always | everysec | no. Empty
	// means Redis's own default (everysec). Only meaningful with AOF.
	AppendFsync string `yaml:"appendfsync,omitempty"`

	// SaveSchedule is the RDB snapshot plan as space-separated
	// "seconds changes" pairs ("900 1 300 10"). Empty means Redis's own
	// default schedule; "no" disables snapshots (the AOF-only shape).
	SaveSchedule string `yaml:"save,omitempty"`

	// ReplicaHost non-empty marks this instance a read replica of the master at
	// that host:port. It is resolved once at install (from a local --replica-of
	// name → 127.0.0.1 + the master's port + the master's password, or a remote
	// --replica-of-host/--replica-of-port), then stored so `pg addon start`
	// rebuilds the same --replicaof argv without the master still being in the
	// config. A replica's Password is required to equal the master's
	// (resolveRedisReplica), so that one field feeds both --requirepass and
	// --masterauth. Empty means this instance is a master.
	ReplicaHost string `yaml:"replica_host,omitempty"`

	// ReplicaPort is the master's port, paired with ReplicaHost. Kept
	// redundant with the host so a stored replica re-derives its argv without a
	// live master lookup.
	ReplicaPort int `yaml:"replica_port,omitempty"`

	// Cluster groups this node into a Redis native cluster: non-empty turns on
	// --cluster-enabled. Members sharing the same value form one cluster and
	// share the first member's password (so one REDISCLI_AUTH reaches them all
	// at `--cluster create`). pgcli does NOT assemble the cluster — it only
	// installs cluster-enabled nodes; you run `pg redis-cli -- --cluster create
	// ...` once. Empty means standalone/replica (unchanged shape). Mutually
	// exclusive with ReplicaHost (a node is either a cluster member or a
	// read-replica, not both — see resolveRedisCluster/validateRedisKnobs).
	Cluster string `yaml:"cluster,omitempty"`

	// AdvertiseHost is --cluster-announce-ip: the address this node announces to
	// peers and the operand host `--cluster create` should dial. Empty = it is
	// reached at 127.0.0.1 (single-host cluster); set a LAN IP/FQDN for a
	// cross-host cluster (Redis has no NAT/port remapping). Only meaningful with
	// Cluster set.
	AdvertiseHost string `yaml:"advertise_host,omitempty"`

	// ClusterReplicas is the per-master replica count this group is assembled
	// with: it is ONLY the operator's intent, echoed back in the install summary
	// so the suggested `pg redis-cli -- --cluster create ... --cluster-replicas N`
	// carries the right number, and so the summary counts nodes against
	// RedisClusterMinMasters*(1+N) instead of calling every member a master. It
	// changes NOTHING in the node's own argv — which members end up masters vs
	// followers is decided by Redis at --cluster create time, not at install.
	// Every member of one --cluster group shares the first member's value (like
	// the password). 0 (default) = masters-only.
	ClusterReplicas int `yaml:"cluster_replicas,omitempty"`

	// Autostart brings this container up on host boot via the boot service
	// (pg autostart enable --redis). Start-only: it starts the existing
	// container, so install first.
	Autostart bool `yaml:"autostart,omitempty"`
}

// ClusterEnabled reports whether this instance is a native-cluster member
// (Cluster set), i.e. whether redis-server should be launched --cluster-enabled.
func (r RedisConfig) ClusterEnabled() bool { return r.Cluster != "" }

// ClusterBusPort is the cluster-bus port Redis derives for this node: always
// client port + 10000. It is not configured or pooled — Redis opens it itself —
// so pgcli only needs it to document the cross-host firewall requirement and to
// fill --cluster-announce-bus-port.
func (r RedisConfig) ClusterBusPort() int { return r.Port + 10000 }

// PeerAddr is the host peers (and the `--cluster create` operands) should use to
// reach this node: the explicit AdvertiseHost for a cross-host member, else
// loopback for a single-host cluster. Mirrors EtcdConfig.AdvertiseAddr.
func (r RedisConfig) PeerAddr() string {
	if r.AdvertiseHost != "" {
		return r.AdvertiseHost
	}
	return "127.0.0.1"
}

// DefaultPredixyImageTag pins the Predixy image — a self-built wrapper of
// joyieldInc/predixy on Alpine (embed/predixy.Containerfile, built and pushed by
// `make container-build-predixy`). The wrapper exists because Predixy is
// config-file-only and ships a signed free-tier license.conf that expires; the
// build swaps in a fresh license, so pgcli never distributes or manages license
// life-cycle itself, it just bind-mounts its rendered predixy.conf OVER the
// baked path and lets the image's sibling license.conf show through. Pull-only,
// like the other pgcli add-on images. The tag is the single source of truth that
// ApplyDefaults and the podman manager agree on.
const DefaultPredixyImageTag = "ghcr.io/mars-base/pgcli/predixy:7.0.1-alpine"

// DefaultPredixyWorkers is Predixy's factory WorkerThreads value, used when
// --workers is not given.
const DefaultPredixyWorkers = 1

// PredixyConfig holds a Predixy proxy addon: a transparent Redis-Cluster proxy
// that re-exposes a native cluster (the redis addon's --cluster shape) as one
// ordinary redis:// endpoint, so clients need no cluster awareness — no -c, no
// MOVED handling. Like haproxy it is shared infrastructure driven purely by a
// generated config file, so it lives at the top level (addons.predixy.<name>).
//
// Backends are given explicitly as the full node list (--backend) rather than
// derived from local redis config: Predixy discovers the rest on its own, but
// pinning every node makes the intent visible in pg.yaml and removes any
// dependence on discovery timing.
type PredixyConfig struct {
	ContainerName string `yaml:"container_name"`      // pgcli-predixy<ns>-<name>
	Name          string `yaml:"name,omitempty"`      // addon key, defaults to the map key
	ImageTag      string `yaml:"image_tag,omitempty"` // ghcr.io/mars-base/pgcli/predixy:7.0.1-alpine (default)
	Listen        string `yaml:"listen,omitempty"`    // bind address, default 0.0.0.0
	Port          int    `yaml:"port,omitempty"`      // client-facing host port, 7617+ auto-assigned
	Workers       int    `yaml:"workers,omitempty"`   // → WorkerThreads, default 1

	// Backend is the proxied cluster's full node list as "host:port" entries,
	// written verbatim into ClusterServerPool.Servers. Required.
	Backend []string `yaml:"backend,omitempty"`

	// Password is ONE password serving two sides: the client-facing Authority
	// Auth (clients must AUTH it to the proxy) and ClusterServerPool.Password
	// (proxy to cluster). That is deliberate — it is the proxied cluster's own
	// requirepass, so `PW=$(pg addon password redis --name n1)` feeds both.
	Password string `yaml:"password,omitempty"`

	// Autostart brings this container up on host boot via the boot service
	// (pg autostart enable --predixy). Start-only: it starts the existing
	// container reading the predixy.conf already on disk, so install first.
	Autostart bool `yaml:"autostart,omitempty"`
}

// PgAdminConfig holds a pgAdmin 4 addon: the official web administration UI for
// PostgreSQL, run as the upstream dpage/pgadmin4 container. Like PostgREST it
// is a single stateless-config container driven entirely by PGADMIN_* env vars
// and it is dual-network (Linux host networking / macOS bridge), but unlike
// PostgREST it is NOT a sidecar of any one instance: it is stored top-level
// only (addons.pgadmin.<name>) because which servers it fronts is pgAdmin's own
// concern (its UI or an optional servers.json), not a per-instance attachment.
//
// It does hold state: /var/lib/pgadmin is its session/config database and must
// be persisted on the host, which is why it has a DataDir (the one field
// PostgREST lacks). The container's own entrypoint chowns that directory to its
// uid 5050 and drops privileges itself, so pgcli never chowns on the host — the
// same "image self-drops" class as MinIO/silo, not rustfs's fixed-uid wrapper.
//
// Email/Password are pgAdmin's WEB LOGIN credentials (PGADMIN_DEFAULT_*),
// required at launch, and have nothing to do with any PostgreSQL password.
// Password is generated when empty (see runAddonInstallPgAdmin) and is
// retrievable via `pg addon password pgadmin`.
//
// DSN/ServerName are optional: when DSN is non-empty, install renders a
// servers.json pre-registering that one server so the UI opens with it
// present. They are a one-time convenience, not a runtime coupling — pgAdmin
// runs fine with them unset (empty server list).
type PgAdminConfig struct {
	ContainerName string `yaml:"container_name"`        // pgcli-pgadmin<ns>-<name>
	Name          string `yaml:"name,omitempty"`        // addon key, defaults to the map key
	ImageTag      string `yaml:"image_tag,omitempty"`   // docker.io/dpage/pgadmin4:9.18 (default)
	HostPort      int    `yaml:"host_port,omitempty"`   // web host port, 5050+ auto-assigned
	Listen        string `yaml:"listen,omitempty"`      // bind address, default 127.0.0.1
	Email         string `yaml:"email,omitempty"`       // PGADMIN_DEFAULT_EMAIL, default admin@pgcli.lan (reserved TLDs like .local are rejected by the image's validator)
	Password      string `yaml:"password,omitempty"`    // PGADMIN_DEFAULT_PASSWORD; generated when empty. A secret — never defaulted to a literal.
	DataDir       string `yaml:"data_dir,omitempty"`    // host dir mounted at /var/lib/pgadmin; default <base>/addon/pgadmin/<name>/data
	DSN           string `yaml:"dsn,omitempty"`         // optional: when set, a servers.json entry is rendered from this URI
	ServerName    string `yaml:"server_name,omitempty"` // display name of the pre-registered server (defaults to the DSN host)
	// Autostart brings this container up on host boot via the boot service
	// (pg autostart enable --pgadmin). Start-only: it starts the existing
	// container, so install first.
	Autostart bool `yaml:"autostart,omitempty"`
}

// GrafanaConfig holds a standalone Grafana addon — the open-source
// observability dashboard (https://grafana.com/oss/grafana/). Shape mirrors
// PgAdminConfig: a web UI, one host port, env-only config (GF_*), an
// auto-generated login, and a persistent data dir outliving the container.
// Unlike pgAdmin there is no server-seeding concept (v1 is a pure web UI; no
// datasource/dashboard provisioning is mounted — see internal/podman.GrafanaManager).
//
// Ownership differs from pgAdmin in a load-bearing way: the image is USER 472
// (group root, gid 0) and its entrypoint does NOT chown or su-exec down — so
// pgAdmin's `--user 0` trick has no meaning here. pgcli instead pre-chowns the
// host data dir to 472:0 via `podman unshare chown` before starting the
// container, which only works on a rootless podman host (rootful hosts see the
// directory as the real fs and permission-fail; the manager surfaces a
// diagnostic there — see podman.GrafanaManager.createContainer).
type GrafanaConfig struct {
	ContainerName string `yaml:"container_name"`       // pgcli-grafana<ns>-<name>
	Name          string `yaml:"name,omitempty"`       // addon key, defaults to the map key
	ImageTag      string `yaml:"image_tag,omitempty"`  // docker.io/grafana/grafana:13.2.4 (default)
	HostPort      int    `yaml:"host_port,omitempty"`  // web host port, 3000+ auto-assigned
	Listen        string `yaml:"listen,omitempty"`     // bind address, default 0.0.0.0 (see ApplyDefaults walk below for rationale)
	AdminUser     string `yaml:"admin_user,omitempty"` // GF_SECURITY_ADMIN_USER, default admin
	Password      string `yaml:"password,omitempty"`   // GF_SECURITY_ADMIN_PASSWORD; generated when empty. A secret — never defaulted to a literal. Only takes effect on first startup against an empty grafana.db; a revived store keeps its own login (see podman.GrafanaStoreExists).
	DataDir       string `yaml:"data_dir,omitempty"`   // host dir mounted at /var/lib/grafana; default <base>/addon/grafana/<name>/data
	// Autostart brings this container up on host boot via the boot service
	// (pg autostart enable --grafana). Start-only: it starts the existing
	// container, so install first.
	Autostart bool `yaml:"autostart,omitempty"`
}

// PrometheusConfig holds a standalone Prometheus addon — the metrics
// collection backend (https://prometheus.io/). Shape mirrors GrafanaConfig: a
// web UI on one host port, a persistent data dir outliving the container, and
// Listen defaulting to 0.0.0.0. Unlike Grafana it is configured by a RENDERED
// prometheus.yml (bind-mounted read-only) plus CLI flags passed after the image
// (--config.file / --web.listen-address / --storage.tsdb.path /
// --storage.tsdb.retention.time), not environment variables — so see
// internal/podman.PrometheusManager for the render + argv shape.
//
// There is NO login/password: Prometheus has no authentication. Its HTTP API
// can run arbitrary PromQL, so a remote-exposed instance is guarded only by the
// network boundary (firewall / --listen 127.0.0.1), not a generated secret —
// the docs must not imply it is as safe to expose as Grafana.
//
// Ownership matches Grafana's divergence: the image is USER nobody (uid/gid
// 65534) and its entrypoint does NOT chown, so pgcli pre-chowns the host data
// dir to 65534:65534 before starting the container (podman unshare chown under
// rootless, direct chown as root on a rootful host).
//
// Retention/Scrape/ScrapeInterval are render knobs that only apply in
// rendered-config mode; when ConfFile is set they are mutually exclusive with
// it (pgcli copies the user file into the managed path and mounts that, so it
// does not render). Retention is the exception — it is a CLI flag, not a render
// knob, so it still applies alongside --conf-file.
type PrometheusConfig struct {
	ContainerName  string   `yaml:"container_name"`            // pgcli-prometheus<ns>-<name>
	Name           string   `yaml:"name,omitempty"`            // addon key, defaults to the map key
	ImageTag       string   `yaml:"image_tag,omitempty"`       // docker.io/prom/prometheus:v3.15.0 (default)
	HostPort       int      `yaml:"host_port,omitempty"`       // web host port, 9090+ auto-assigned
	Listen         string   `yaml:"listen,omitempty"`          // --web.listen-address bind, default 0.0.0.0 (see ApplyDefaults walk for rationale)
	DataDir        string   `yaml:"data_dir,omitempty"`        // host dir mounted at /prometheus (tsdb); default <base>/addon/prometheus/<name>/data
	Retention      string   `yaml:"retention,omitempty"`       // --storage.tsdb.retention.time, default 15d
	Scrape         []string `yaml:"scrape,omitempty"`          // JOB=host:port[,host:port...] [key=value...] scrape specs (bare key=value tokens = per-target labels), rendering one static_configs job per JOB in the rendered prometheus.yml (render-mode only)
	ScrapeInterval string   `yaml:"scrape_interval,omitempty"` // global scrape_interval in the rendered prometheus.yml, default 15s (render-mode only)
	// Alertmanager lists the Alertmanager instances this Prometheus forwards fired
	// alerts to — bare host[:port] or http(s)://host[:port][/path] URLs, rendered
	// into an `alerting:` section (render-mode only; mutually exclusive with
	// ConfFile, which owns the whole file). Setting it also folds one
	// `alertmanager` scrape job over the same targets into the rendered config so
	// Alertmanager's own /metrics is watched.
	Alertmanager []string `yaml:"alertmanager,omitempty"`
	// RemoteWrite lists Prometheus remote-write URLs (full http(s):// URLs,
	// repeatable) rendered into a `remote_write:` section of the prometheus.yml
	// (render-mode only; mutually exclusive with ConfFile). This is how a
	// Prometheus pushes its scraped samples to a long-term store such as
	// VictoriaMetrics — vminsert for the cluster edition
	// (http://host:8480/insert/0/prometheus/api/v1/write), victoria-metrics or
	// vmagent for the single-node one (http://host:8428/api/v1/write). The local
	// TSDB keeps serving its retention window regardless; the remote is a copy.
	RemoteWrite []string `yaml:"remote_write,omitempty"`
	// RemoteWriteQueue lists KEY=VALUE queue_config tunings (repeatable) applied
	// under EVERY rendered remote_write entry: capacity, max_samples_per_send,
	// max_shards, min_shards (positive integers) and batch_send_deadline,
	// min_backoff, max_backoff (durations). VictoriaMetrics' docs recommend
	// larger batches under load; the keys are whitelisted and validated at
	// install. Render-mode only, rides the same --conf-file exclusion as
	// RemoteWrite, and is meaningless without it.
	RemoteWriteQueue []string `yaml:"remote_write_queue,omitempty"`
	// RuleFiles lists host paths to Prometheus rule files (*.rules.yml with a
	// top-level `groups:` block). pgcli copies each into <base>/addon/prometheus/
	// <name>/rules and mounts that dir read-only at /etc/prometheus/rules; the
	// rendered prometheus.yml then references the copies via rule_files. Valid
	// alongside ConfFile — the dir is copied in both modes, but only the rendered
	// config wires it up automatically; a conf-file owner decides itself whether
	// to point its own rule_files at /etc/prometheus/rules.
	RuleFiles []string `yaml:"rule_files,omitempty"`
	ConfFile  string   `yaml:"conf_file,omitempty"` // optional host path to the user's own prometheus.yml; when set pgcli skips rendering and copies this file into the managed path to mount (mutually exclusive with Scrape/ScrapeInterval)
	// Autostart brings this container up on host boot via the boot service
	// (pg autostart enable --prometheus). Start-only: it starts the existing
	// container, so install first.
	Autostart bool `yaml:"autostart,omitempty"`
}

// VictoriaMetricsConfig holds a standalone VictoriaMetrics addon — a fast,
// Prometheus-compatible time-series database
// (https://victoriametrics.com/). Unlike Prometheus it needs no rendered config
// file; everything is passed as CLI flags after the image name. The single-node
// version is a single binary in a single container, no clustering.
//
// There is NO login/password: VictoriaMetrics has no authentication by default.
// Its HTTP API can ingest and query arbitrary metrics, so a remote-exposed
// instance is guarded only by the network boundary (firewall / --listen
// 127.0.0.1), same as Prometheus.
//
// The image runs as root, so no pre-chown of the data dir is needed (unlike
// Prometheus's uid 65534).
type VictoriaMetricsConfig struct {
	ContainerName string   `yaml:"container_name"`       // pgcli-victoriametrics<ns>-<name>
	Name          string   `yaml:"name,omitempty"`       // addon key
	ImageTag      string   `yaml:"image_tag,omitempty"`  // docker.io/victoriametrics/victoria-metrics:v1.154.1
	HostPort      int      `yaml:"host_port,omitempty"`  // HTTP port, 8428+ auto-assigned
	Listen        string   `yaml:"listen,omitempty"`     // -httpListenAddr bind, default 0.0.0.0
	DataDir       string   `yaml:"data_dir,omitempty"`   // host dir mounted at /victoria-metrics-data
	Retention     string   `yaml:"retention,omitempty"`  // -retentionPeriod value (months), default "1"
	ExtraArgs     []string `yaml:"extra_args,omitempty"` // additional CLI flags, e.g. ["-search.maxUniqueTimeseries=1000000"]
	// Autostart brings this container up on host boot via the boot service
	// (pg autostart enable --victoriametrics). Start-only: it starts the existing
	// container, so install first.
	Autostart bool `yaml:"autostart,omitempty"`
}

// VMClusterStorageNode is one vmstorage address that vminsert/vmselect dial.
// Address is host:port for the insert protocol (e.g. "10.0.0.1:8483");
// SelectPort is the select protocol port on the same host (e.g. 8484).
type VMClusterStorageNode struct {
	Address    string `yaml:"address"`     // host:insertInternalPort
	SelectPort int    `yaml:"select_port"` // selectInternalPort on the same host
}

// VMClusterConfig holds a VictoriaMetrics cluster addon — three components
// (vminsert, vmselect, vmstorage) each running as a separate container.
// Unlike the single-node victoriametrics addon (one container, one port, one
// image), vmcluster manages up to three containers with three separate images
// and five ports (3 HTTP + 2 internal TCP for inter-component communication).
//
// Topology:
//   - Converged single-host (default): all three components on one host using
//     host networking. Analogous to BeeGFS v1.
//   - Cross-host (v1): --roles selects which components THIS host runs;
//     --storage-node lists ALL vmstorage addresses in the cluster for sharding
//     and replication. Each host runs its own `pg addon install vmcluster`.
//
// Like the single-node VictoriaMetrics, all three images run as root, so no
// pre-chown of the data dir is needed. Only vmstorage has persistent data;
// vminsert and vmselect are stateless.
type VMClusterConfig struct {
	Name            string `yaml:"name,omitempty"`
	ContainerPrefix string `yaml:"container_prefix,omitempty"` // pgcli-vmcluster<ns>-<name>

	InsertImageTag  string `yaml:"insert_image_tag,omitempty"`
	SelectImageTag  string `yaml:"select_image_tag,omitempty"`
	StorageImageTag string `yaml:"storage_image_tag,omitempty"`

	Listen string `yaml:"listen,omitempty"` // default 0.0.0.0

	// HTTP ports (pool-allocated, 5 consecutive from vmcluster_start_port)
	InsertPort         int `yaml:"insert_port,omitempty"`          // vminsert HTTP
	SelectPort         int `yaml:"select_port,omitempty"`          // vmselect HTTP
	StoragePort        int `yaml:"storage_port,omitempty"`         // vmstorage HTTP
	InternalInsertPort int `yaml:"internal_insert_port,omitempty"` // vmstorage TCP (vminsert protocol)
	InternalSelectPort int `yaml:"internal_select_port,omitempty"` // vmstorage TCP (vmselect protocol)

	// Roles is the subset of {insert,select,storage} THIS host runs.
	// Empty = all three (converged single-host cluster).
	Roles []string `yaml:"roles,omitempty"`

	// StorageNodes lists ALL vmstorage addresses in the cluster.
	// vminsert and vmselect use this to build -storageNode flags.
	// Empty on a converged host → auto-derived as 127.0.0.1:<internalPorts>.
	StorageNodes []VMClusterStorageNode `yaml:"storage_nodes,omitempty"`

	// StorageDataDir: only used when this host runs the storage role.
	StorageDataDir string `yaml:"storage_data_dir,omitempty"`
	Retention      string `yaml:"retention,omitempty"`
	ReplicationFactor int `yaml:"replication_factor,omitempty"` // vminsert flag

	InsertExtraArgs  []string `yaml:"insert_extra_args,omitempty"`
	SelectExtraArgs  []string `yaml:"select_extra_args,omitempty"`
	StorageExtraArgs []string `yaml:"storage_extra_args,omitempty"`

	Autostart bool `yaml:"autostart,omitempty"`
}

// ContainerName returns one component's container name: the shared prefix plus
// the role suffix (pgcli-vmcluster<ns>-<name>-{insert,select,storage}).
func (v *VMClusterConfig) ContainerName(role string) string {
	return v.ContainerPrefix + "-" + role
}

// RolesInOrder returns the components this host runs, in startup dependency
// order (storage → insert → select). An empty Roles (converged) yields all
// three; otherwise the stored Roles filtered to the known set and reordered.
func (v *VMClusterConfig) RolesInOrder() []string {
	return orderVMClusterRoles(v.Roles)
}

// RunsRole reports whether this host runs the given component role.
func (v *VMClusterConfig) RunsRole(role string) bool {
	return slices.Contains(v.RolesInOrder(), role)
}

// EffectiveStorageNodes returns the explicit StorageNodes or auto-derives a
// single-node entry for converged deployments (127.0.0.1:<internalPorts>).
func (v *VMClusterConfig) EffectiveStorageNodes() []VMClusterStorageNode {
	if len(v.StorageNodes) > 0 {
		return v.StorageNodes
	}
	host := v.Listen
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return []VMClusterStorageNode{{
		Address:    fmt.Sprintf("%s:%d", host, v.InternalInsertPort),
		SelectPort: v.InternalSelectPort,
	}}
}

// VMCluster component roles, mirrored in the CLI flag help. These live in
// config so both config normalisation and the podman manager share one source.
const (
	VMClusterRoleInsert  = "insert"
	VMClusterRoleSelect  = "select"
	VMClusterRoleStorage = "storage"
)

// allVMClusterRoles is the canonical startup order a full cluster uses:
// storage first (insert/select need it), then insert, then select.
var allVMClusterRoles = []string{VMClusterRoleStorage, VMClusterRoleInsert, VMClusterRoleSelect}

// orderVMClusterRoles dedupes roles and returns them in canonical order,
// dropping nothing unknown (validation rejects those separately).
func orderVMClusterRoles(roles []string) []string {
	if len(roles) == 0 {
		return allVMClusterRoles
	}
	set := make(map[string]bool, len(roles))
	for _, r := range roles {
		set[r] = true
	}
	out := make([]string, 0, len(set))
	for _, s := range allVMClusterRoles {
		if set[s] {
			out = append(out, s)
		}
	}
	return out
}

// VictoriaLogsConfig holds a standalone VictoriaLogs addon — the log database
// from the VictoriaMetrics family (https://docs.victoriametrics.com/victorialogs/).
// It is the exact mirror of VictoriaMetricsConfig: one binary in one container,
// one HTTP port (default 9428) serving ingest, LogsQL queries, and the built-in
// UI. No rendered config file; everything is CLI flags after the image name.
//
// There is NO login/password: VictoriaLogs has no authentication by default.
// Its HTTP API can ingest and query arbitrary logs, so a remote-exposed
// instance is guarded only by the network boundary (firewall / --listen
// 127.0.0.1), same as VictoriaMetrics.
//
// The image runs as root, so no pre-chown of the data dir is needed (unlike
// Prometheus's uid 65534).
type VictoriaLogsConfig struct {
	ContainerName string   `yaml:"container_name"`       // pgcli-victorialogs<ns>-<name>
	Name          string   `yaml:"name,omitempty"`       // addon key
	ImageTag      string   `yaml:"image_tag,omitempty"`  // docker.io/victoriametrics/victoria-logs:v1.53.0
	HostPort      int      `yaml:"host_port,omitempty"`  // HTTP port, 9428+ auto-assigned
	Listen        string   `yaml:"listen,omitempty"`     // -httpListenAddr bind, default 0.0.0.0
	DataDir       string   `yaml:"data_dir,omitempty"`   // host dir mounted at /victoria-logs-data
	Retention     string   `yaml:"retention,omitempty"`  // -retentionPeriod value (e.g. "30d", "3months"; the binary's own default is "1" month)
	ExtraArgs     []string `yaml:"extra_args,omitempty"` // additional CLI flags, e.g. ["-search.maxQueryDuration=60s"]
	// Autostart brings this container up on host boot via the boot service.
	// Start-only: it starts the existing container, so install first.
	Autostart bool `yaml:"autostart,omitempty"`
}
// dedup/grouping/routing frontend of the Prometheus stack
// (https://prometheus.io/docs/alerting/latest/alertmanager/). Shape mirrors
// PrometheusConfig: a web UI on one host port, a persistent data dir outliving
// the container, Listen defaulting to 0.0.0.0, and configuration by a RENDERED
// alertmanager.yml (bind-mounted read-only) plus CLI flags after the image
// (--config.file / --web.listen-address / --storage.path), not env vars — see
// internal/podman.AlertmanagerManager for the render + argv shape.
//
// There is NO login/password: Alertmanager has no authentication, and the
// exposure is worse than Prometheus's — anyone who can reach the port can
// create SILENCES, i.e. suppress the on-call notifications. The firewall /
// --listen 127.0.0.1 boundary is the only guard; the docs must say so loudly.
//
// Ownership matches Prometheus: the image is USER nobody (uid/gid 65534) and
// does not fix up bind mounts, so pgcli pre-chowns the host data dir to
// 65534:65534 (podman unshare chown rootless, direct chown rootful-as-root).
//
// v1 deliberately does NOT wire this to a Prometheus instance: no alerting
// section is injected into a rendered prometheus.yml. Real notification
// channels (email/Slack/routing trees) arrive via ConfFile.
type AlertmanagerConfig struct {
	ContainerName string   `yaml:"container_name"`      // pgcli-alertmanager<ns>-<name>
	Name          string   `yaml:"name,omitempty"`      // addon key, defaults to the map key
	ImageTag      string   `yaml:"image_tag,omitempty"` // docker.io/prom/alertmanager:v0.34.1 (default)
	HostPort      int      `yaml:"host_port,omitempty"` // web host port, 9093+ auto-assigned
	Listen        string   `yaml:"listen,omitempty"`    // --web.listen-address bind, default 0.0.0.0
	DataDir       string   `yaml:"data_dir,omitempty"`  // host dir mounted at /alertmanager (silence + notify state); default <base>/addon/alertmanager/<name>/data
	Webhook       []string `yaml:"webhook,omitempty"`   // webhook URLs rendered into the web.hook receiver of the alertmanager.yml (render-mode only)
	ConfFile      string   `yaml:"conf_file,omitempty"` // optional host path to the user's own alertmanager.yml; when set pgcli skips rendering and copies this file into the managed path to mount (mutually exclusive with Webhook)
	// Autostart brings this container up on host boot via the boot service
	// (pg autostart enable --alertmanager). Start-only: it starts the existing
	// container, so install first.
	Autostart bool `yaml:"autostart,omitempty"`
}

// GiteaConfig holds a standalone Gitea addon — the self-hosted Git service
// (https://about.gitea.com/). Shape mirrors GrafanaConfig: a web UI on one
// host port, env-only config (GITEA__<section>__<KEY> variables the image's
// environment-to-ini folds into app.ini at every boot), an auto-generated
// admin login, and a persistent /data dir outliving the container.
//
// Two deliberate divergences from the Grafana template:
//
//   - The crypto secrets (SECRET_KEY / INTERNAL_TOKEN) are NEVER pgcli-
//     injected. The image re-applies GITEA__* env into the PERSISTED
//     <data>/gitea/conf/app.ini on every boot, so a per-install regenerated
//     secret would silently re-key a surviving database whose 2FA seeds and
//     OAuth client secrets were encrypted under the old one — unrecoverable,
//     with no error. Gitea generates them itself on first boot; the volume
//     owns crypto state, pgcli owns only the admin web login.
//   - Non-interactive boot: INSTALL_LOCK=true via env skips the web setup
//     wizard, and the admin account is created afterward with
//     `gitea admin user create` executed inside the container (see
//     podman.GiteaManager.CreateAdmin), generated-password semantics otherwise
//     identical to Grafana's (only honoured against a fresh store).
//
// Ownership: the rootful image runs as the `git` user, default uid/gid 1000,
// and its entrypoint expects /data owned by it. pgcli pre-chowns the host data
// dir to 1000:1000 (podman unshare chown under rootless, direct chown as root
// on a rootful host — same contract as Grafana/Prometheus, different uid).
type GiteaConfig struct {
	ContainerName string `yaml:"container_name"`       // pgcli-gitea<ns>-<name>
	Name          string `yaml:"name,omitempty"`       // addon key, defaults to the map key
	ImageTag      string `yaml:"image_tag,omitempty"`  // docker.io/gitea/gitea:28.1.0 (default)
	HostPort      int    `yaml:"host_port,omitempty"`  // web host port, 3080+ auto-assigned
	Listen        string `yaml:"listen,omitempty"`     // bind address, default 0.0.0.0 (see ApplyDefaults walk for rationale)
	AdminUser     string `yaml:"admin_user,omitempty"` // web login username, default gitea (distinct from the container's OS user git)
	Password      string `yaml:"password,omitempty"`   // admin password; generated when empty. A secret — never defaulted to a literal. Only takes effect on first startup against a fresh store; a revived instance keeps its own login (see podman.GiteaStoreExists).
	DataDir       string `yaml:"data_dir,omitempty"`   // host dir mounted at /data (repos, app.ini, SQLite, ssh keys); default <base>/addon/gitea/<name>/data
	SSH           bool   `yaml:"ssh,omitempty"`        // live git-over-ssh: DISABLE_SSH=false so Gitea writes the git user's authorized_keys for the image's sshd (clone over ssh:// in addition to HTTP). Off by default — but that only stops *live* cloning, not the sshd process itself: the official image runs s6 sshd unconditionally, so even an SSH-off instance parks it on SSHPort below.
	SSHPort       int    `yaml:"ssh_port,omitempty"`   // host port the image's s6 sshd binds (via the bare SSH_LISTEN_PORT/SSH_PORT env it actually reads). Allocated from the gitea_ssh_start_port pool for EVERY instance, SSH on or off — parking the always-on sshd off 22 is the whole point. When SSH is on it is a live listener mirrored into the clone URLs; when off it is an idle listener nothing connects to. 0 = auto.
	Dsn           string `yaml:"dsn,omitempty"`        // optional postgres://user:pass@host:port/db for Gitea's metadata database (rendered into GITEA__database__*); empty = embedded SQLite inside /data. The database must already exist — Gitea creates its own schema on first boot.
	// AllowRegistration opens Gitea's self-service signup (the web UI shows a
	// Register entry; DISABLE_REGISTRATION=false). Off by default — closed
	// self-registration is one of the two guards on the default 0.0.0.0 listen.
	// Signups always create NORMAL users: Gitea has no way to make the web
	// register form produce an admin, so this opens account spam, not takeover.
	AllowRegistration bool `yaml:"allow_registration,omitempty"`
	// Autostart brings this container up on host boot via the boot service
	// (pg autostart enable --gitea). Start-only: it starts the existing
	// container, so install first.
	Autostart bool `yaml:"autostart,omitempty"`
}

// BeeGFSConfig holds a standalone BeeGFS filesystem addon — the parallel
// (scale-out) file system whose SERVER side is three cooperating services, not
// one container. This is pgcli's first multi-container addon: a single map
// entry describes a whole filesystem, and one manager brings up and tears down
// three ordered containers sharing one connection-auth secret:
//
//   - mgmtd: the management daemon — node/target registry every other service
//     and every client dials first. Owns an SQLite database (its data dir).
//     Listens on TWO ports: the classic BeeMsg port (default 8008) that
//     meta/storage speak, and a gRPC port (default 8010) that the modern
//     `beegfs` CLI connects to. Both are movable via --beemsg-port/--grpc-port.
//   - meta: the metadata service. Owns a metadata target directory.
//   - storage: a data service. Owns one or more storage target directories
//     (StorageTargetDirs) — several disks each become a target in the ONE
//     storage container, which is how a host exposes per-disk bandwidth.
//
// Roles selects which of these THIS host runs; empty means all three (the v1
// converged single-host cluster). v2 cross-host clusters set Roles to a subset
// on each host and give the non-mgmtd hosts a MgmtdHost to register against —
// every host runs its own local `pg` command, since BeeGFS daemons
// self-register over the network (pgcli never SSHes; this mirrors how Patroni
// cross-host members work).
//
// Unlike every web addon, there is no "host port" to publish: the services run
// on the host network (BeeGFS speaks its own protocol, not HTTP), so the four
// ports below are the ACTUAL listener ports on the host. In 8.4 only the mgmtd
// pair can be moved; meta (8005) and storage (8003) have no supported
// listen-port override at all, which is why pgcli still runs ONE filesystem per
// host (a second would collide on the fixed ports) — spreading a single
// filesystem across hosts via Roles is the cross-host answer. See the note at
// the end of autoAssignPorts and the field comments below.
//
// The client is a kernel module (DKMS) and is deliberately out of scope —
// pgcli neither installs nor mounts it; docs/beegfs explains client setup.
// A BeeGFS "license" is a system-specific registration the user obtains from
// ThinkParQ; pgcli never generates one but can CARRY a license file the user
// downloaded (BeeGFSConfig.License → the mgmtd container's
// BEEGFS_LICENSE_FILE_DATA). With no license the 8.4 management daemon warns
// on every command and caps client mounts at five.
//
// Ownership: the three images run as root, so the bind-mounted dirs need no
// chown (unlike Gitea's uid-1000 or Grafana's uid-472 dance).
type BeeGFSConfig struct {
	Name string `yaml:"name,omitempty"` // addon key; the map key, defaults to it
	// ContainerPrefix is shared by the three containers, which are named
	// <prefix>-mgmtd / -meta / -storage. Default pgcli-beegfs<ns>-<name>.
	ContainerPrefix string `yaml:"container_prefix,omitempty"`
	// ImageTag stores only the VERSION (e.g. "8.4.0"); the manager combines it
	// with BeeGFSImageRepo and each role. All three services run the same tag.
	ImageTag string `yaml:"image_tag,omitempty"`
	// Listen is the address clients and this host's own tooling dial mgmtd on.
	// Host networking means the services actually bind all interfaces; this
	// value is the self-reference used in sysMgmtdHost and mount hints.
	// Default 0.0.0.0.
	Listen string `yaml:"listen,omitempty"`
	// The four listener ports. These are the daemons' ACTUAL host-network bind
	// ports (BeeGFS speaks its own protocol, not HTTP, so there is no container
	// port-mapping to allocate against). Only mgmtd's two can be moved in 8.4
	// (via --beemsg-port/--grpc-port, emitted by the manager); meta and storage
	// have no supported listen-port override, so v1 runs exactly one filesystem
	// per host on the stock defaults (8008/8010/8005/8003) — the CLI rejects a
	// second filesystem and any non-default meta/storage port. See the note at
	// the end of autoAssignPorts.
	MgmtdPort     int `yaml:"mgmtd_port,omitempty"`      // mgmtd BeeMsg port (default 8008, movable)
	MgmtdGRPCPort int `yaml:"mgmtd_grpc_port,omitempty"` // mgmtd gRPC port for `beegfs` CLI (default 8010, movable)
	MetaPort      int `yaml:"meta_port,omitempty"`       // meta service port (fixed 8005)
	StoragePort   int `yaml:"storage_port,omitempty"`    // storage service port (fixed 8003)
	// ConnAuth is the shared connection-authentication secret the three server
	// services (and clients) must all agree on. Generated on install when
	// empty and stored here — a secret, never defaulted to a literal.
	ConnAuth string `yaml:"conn_auth,omitempty"`
	// License is the CONTENT of a ThinkParQ license certificate (PEM), carried
	// to the mgmtd container as BEEGFS_LICENSE_FILE_DATA (the official image's
	// init.py writes it to /etc/beegfs/license.pem; only mgmtd ships
	// libbeegfs_license.so, so only the host running mgmtd stores this).
	// pgcli never generates a license — the user obtains one out of band
	// (`beegfs license --get` prints the system-bound URL); Validate only
	// sanity-checks the PEM shape to catch a wrong file.
	License string `yaml:"license,omitempty"`
	// The three service data directories, each bind-mounted into its own
	// container and surviving across container recreation. Empty means
	// <base>/addon/beegfs/<name>/{mgmtd,meta,storage}; the manager resolves
	// them (same contract as Gitea.DataDir).
	MgmtdDataDir   string `yaml:"mgmtd_data_dir,omitempty"`
	MetaDataDir    string `yaml:"meta_data_dir,omitempty"`
	StorageDataDir string `yaml:"storage_data_dir,omitempty"`
	// Roles is the subset of {mgmtd,meta,storage} THIS host runs (v2 cross-host
	// model: every host runs its own local `pg` command with its role subset and
	// the daemons self-register into mgmtd over the network — pgcli never SSHes
	// anywhere). Empty means all three, the v1 converged single-host cluster.
	// ApplyDefaults normalises it to deduped canonical order.
	Roles []string `yaml:"roles,omitempty"`
	// MgmtdHost is the address meta/storage dial to reach the management daemon.
	// Empty falls back to this host's own Listen (correct when mgmtd runs here);
	// a JOIN host (Roles without mgmtd) must set it, enforced by Validate.
	MgmtdHost string `yaml:"mgmtd_host,omitempty"`
	// NodeID is the BeeGFS per-service-type node number this host's meta/storage
	// services register under (-s <id>). NodeIDs are namespaced by service type,
	// so a meta on host A and a storage on host B can both be 1. 0 → 1 (v1).
	// Target IDs derive as NodeID*100 + ordinal, so v1's storage target 101 is
	// exactly node 1 target 1.
	NodeID int `yaml:"node_id,omitempty"`
	// StorageTargetDirs lists one host directory PER storage target on this host
	// (v2: several disks, each a target, all in the one storage container).
	// Empty means the single StorageDataDir — the v1 shape. The manager emits one
	// beegfs_setup_N + one bind mount per entry and joins them for
	// storeStorageDirectory (init.py's invariant: setups == targets).
	StorageTargetDirs []string `yaml:"storage_target_dirs,omitempty"`
	// Autostart brings the whole filesystem up on host boot via the boot
	// service (pg autostart enable --beegfs), starting the three containers in
	// dependency order. Start-only: it starts existing containers, install first.
	Autostart bool `yaml:"autostart,omitempty"`
}

// ContainerName returns one service role's container name: the shared prefix
// plus the role suffix (pgcli-beegfs<ns>-<name>-mgmtd / -meta / -storage).
func (b *BeeGFSConfig) ContainerName(role string) string {
	return b.ContainerPrefix + "-" + role
}

// RolesInOrder returns the services this host runs, in startup dependency order
// (mgmtd → meta → storage). An empty Roles (v1 converged) yields all three;
// otherwise the stored Roles filtered to the known set and reordered canonically
// so callers never depend on the user's flag order.
func (b *BeeGFSConfig) RolesInOrder() []string {
	return orderBeeGFSServices(b.Roles)
}

// RunsService reports whether this host runs the given service role.
func (b *BeeGFSConfig) RunsService(role string) bool {
	return slices.Contains(b.RolesInOrder(), role)
}

// RunsMgmtd reports whether mgmtd lives on this host — the gate that decides
// whether a fresh install generates a connection-auth secret (only the mgmtd
// host owns it) or must be handed one via --password.
func (b *BeeGFSConfig) RunsMgmtd() bool { return b.RunsService(BeeGFSServiceMgmtd) }

// BeeGFS storage service roles, mirrored in the CLI flag help. These live in
// config so both config normalisation and the podman manager share one source.
const (
	BeeGFSServiceMgmtd   = "mgmtd"
	BeeGFSServiceMeta    = "meta"
	BeeGFSServiceStorage = "storage"
)

// allBeeGFSServices is the canonical startup order a full filesystem uses.
var allBeeGFSServices = []string{BeeGFSServiceMgmtd, BeeGFSServiceMeta, BeeGFSServiceStorage}

// orderBeeGFSServices dedupes roles and returns them in canonical order,
// dropping nothing unknown (validation rejects those separately).
func orderBeeGFSServices(roles []string) []string {
	if len(roles) == 0 {
		return allBeeGFSServices
	}
	set := make(map[string]bool, len(roles))
	for _, r := range roles {
		set[r] = true
	}
	out := make([]string, 0, len(set))
	for _, s := range allBeeGFSServices {
		if set[s] {
			out = append(out, s)
		}
	}
	return out
}

// ValidBeeGFSService reports whether s is one of the three known roles.
func ValidBeeGFSService(s string) bool {
	return s == BeeGFSServiceMgmtd || s == BeeGFSServiceMeta || s == BeeGFSServiceStorage
}

// PostgresConfig holds PostgreSQL connection settings.
type PostgresConfig struct {
	URL      string `yaml:"url"`      // connection string (postgres://user:pass@host:port/db)
	Host     string `yaml:"host"`     // host, default localhost
	Port     int    `yaml:"port"`     // port, default 5432
	User     string `yaml:"user"`     // user, default admin
	Password string `yaml:"password"` // password, default admin
	Database string `yaml:"database"` // database name, default admin
}

// PodmanConfig holds Podman container settings.
type PodmanConfig struct {
	ContainerName string `yaml:"container_name"` // PG container name, default pgcli-pg
	DataDir       string `yaml:"data_dir"`       // PG data directory (host path), default ~/.pgcli/dbdata/<name>/data
	ImageTag      string `yaml:"image_tag"`      // image tag, default ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0
	HostPort      int    `yaml:"host_port"`      // host port for PG mapping, 0=auto-assign from 35432
	SSHPort       int    `yaml:"ssh_port"`       // SSH port for pgbackrest, 0=auto-assign from 42201
	Network       string `yaml:"network"`        // podman network name, default pgcli-net
}

// PITRConfig holds PITR backup/restore settings.
type PITRConfig struct {
	Enabled          bool   `yaml:"enabled"`           // whether PITR is enabled
	PgBackRestStanza string `yaml:"pgbackrest_stanza"` // pgBackRest stanza name
}

// LoggingConfig holds logging settings.
type LoggingConfig struct {
	Level string `yaml:"level"` // debug / info / warn / error, default info
}

// BackupConfig holds shared pgbackrest backup container settings.
type BackupConfig struct {
	ContainerName string `yaml:"container_name"` // backup container name, default pgcli-backup
	ImageTag      string `yaml:"image_tag"`      // backup image tag, default pgcli-backup:2.58.0
	DataDir       string `yaml:"data_dir"`       // pgbackrest repo dir, default ~/.pgcli/backup/data
	LogDir        string `yaml:"log_dir"`        // pgbackrest log dir, default ~/.pgcli/backup/log
	RetentionFull int    `yaml:"retention_full"` // number of full backups to retain, default 7
	// Autostart starts the backup container automatically on host boot
	// (`pg autostart enable --backup`).
	Autostart bool `yaml:"autostart,omitempty"`

	// Repo selects where pgBackRest writes backups. Zero value = local POSIX
	// repo1 under DataDir (the historical behaviour). Setting Repo.S3 turns
	// repo1 into an S3 repository that every stanza (regular instances and
	// Patroni members alike) pushes to.
	Repo BackupRepo `yaml:"repo,omitempty"`
}

// BackupRepo groups repository backends for the shared backup container. Only
// one backend may be active; S3 is the only non-default one today.
type BackupRepo struct {
	// S3 configures repo1 as an S3-compatible repository (MinIO, AWS S3, ...).
	// nil => local POSIX repo under BackupConfig.DataDir.
	S3 *BackupRepoS3 `yaml:"s3,omitempty"`
}

// BackupRepoS3 holds explicit S3 repository settings — deliberately no
// "auto-wire the local minio addon" magic: endpoint and credentials are always
// written by hand in pg.yaml (or via `pg backup setup --s3-*`). pgBackRest
// requires HTTPS for S3, so an endpoint backed by a private CA needs CAFile.
type BackupRepoS3 struct {
	Endpoint  string `yaml:"endpoint"`             // host:port, no scheme — e.g. 10.0.0.9:9000
	Bucket    string `yaml:"bucket"`               // repo1-s3-bucket
	Region    string `yaml:"region,omitempty"`     // default us-east-1 (MinIO accepts anything)
	Path      string `yaml:"path,omitempty"`       // repo1-path inside the bucket, default /pgbackrest
	AccessKey string `yaml:"access_key"`           // repo1-s3-key
	SecretKey string `yaml:"secret_key"`           // repo1-s3-key-secret (stored in pg.yaml like every other pgcli secret)
	URIStyle  string `yaml:"uri_style,omitempty"`  // repo1-s3-uri-style, default path (MinIO needs path)
	CAFile    string `yaml:"ca_file,omitempty"`    // host path to a PEM CA bundle; empty = image's system CAs
	VerifyTLS *bool  `yaml:"verify_tls,omitempty"` // default true; false = repo*-s3-verify-tls=n escape hatch
}

// PigstyConfig holds Pigsty extension repository settings.
type PigstyConfig struct {
	Repo string `yaml:"repo"` // Pigsty DEB repo base URL, default https://repo.pigsty.io
}

// Default returns a Config populated with default values.
func Default() *Config {
	return &Config{
		BaseDir:                 "", // empty means use platform default
		PGStartPort:             35432,
		PGSSHPort:               42201,
		PgBouncerStartPort:      56432,
		EtcdStartPort:           2379,
		PgDogStartPort:          7432,
		PatroniStartPort:        35532,
		PatroniRestapiStartPort: 8008,
		PatroniSSHStartPort:     42301,
		HaProxyStartPort:        5000,
		MinioStartPort:          9000,
		PostgrestStartPort:      3500,
		RedisStartPort:          6379,
		PredixyStartPort:        7617,
		PgAdminStartPort:        5050,
		GrafanaStartPort:        3000,
		PrometheusStartPort:     9090,
		AlertmanagerStartPort:   9093,
		GiteaStartPort:          3080,
		GiteaSSHStartPort:       2222,
		NginxStartPort:          8080,
		GarageStartPort:         3900,
		SeaweedfsStartPort:      8400,
		VictoriaMetricsStartPort: 8428,
		VMClusterStartPort:       8480,
		VictoriaLogsStartPort:    9428,
		Postgres: PostgresConfig{
			Host:     "127.0.0.1",
			Port:     5432,
			User:     "admin",
			Password: "admin",
			Database: "admin",
		},
		Podman: PodmanConfig{
			ContainerName: "pgcli-pg",
			DataDir:       filepath.Join(platform.DefaultConfigDir(), "dbdata", "data"),
			ImageTag:      "ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0",
			Network:       "pgcli-net",
		},
		PITR: PITRConfig{
			Enabled:          true,
			PgBackRestStanza: "pgcli",
		},
		Logging: LoggingConfig{
			Level: "info",
		},
		Backup: BackupConfig{
			ContainerName: "pgcli-backup",
			ImageTag:      "ghcr.io/mars-base/pgcli/pgcli-backup:2.58.0",
			DataDir:       filepath.Join(platform.DefaultConfigDir(), "backup", "data"),
			LogDir:        filepath.Join(platform.DefaultConfigDir(), "backup", "log"),
			RetentionFull: 7,
			Autostart:     true, // default: start backup container on boot
		},
		Pigsty: PigstyConfig{
			Repo: "https://repo.pigsty.io",
		},
		Instances: make(map[string]InstanceConfig),
	}
}

// nsSuffix returns "-<namespace>" or "" for a namespace-prefixed name.
func nsSuffix(namespace string) string {
	if namespace == "" {
		return ""
	}
	return "-" + namespace
}

// PatroniScope is the scope a Patroni cluster uses in the shared DCS: the
// config-level scope namespaced so two pgcli namespaces sharing one etcd do
// not collide (Patroni's etcd prefix is the raw scope and has no namespace of
// its own). Exported for the podman layer, which renders it into patroni.yml.
func (c *Config) PatroniScope(scope string) string {
	return scope + nsSuffix(c.Namespace)
}

// InstanceDefaults returns default configuration for the named instance.
// Container names, stanza names, etc. are derived from the instance name.
// If BaseDir is set, data paths are relative to it; otherwise uses platform default.
func (c *Config) InstanceDefaults(name string) *InstanceConfig {
	baseDir := platform.DefaultConfigDir()
	if c.BaseDir != "" {
		baseDir = c.BaseDir
	}
	return &InstanceConfig{
		Postgres: PostgresConfig{
			Host:     c.Postgres.Host,
			Port:     5432,
			User:     c.Postgres.User,
			Password: c.Postgres.Password,
			Database: name + "_db",
		},
		Podman: PodmanConfig{
			ContainerName: "pgcli-pg" + nsSuffix(c.Namespace) + "-" + name,
			DataDir:       filepath.Join(baseDir, "dbdata", name, "data"),
			ImageTag:      c.Podman.ImageTag,
			HostPort:      0, // auto-assigned
			SSHPort:       0, // auto-assigned
		},
		PITR: PITRConfig{
			Enabled:          true,
			PgBackRestStanza: "pgcli_" + name,
		},
	}
}

// SetInstance merges the named instance's configuration into top-level fields.
// Instance-level values take precedence over global defaults.
func (c *Config) SetInstance(name string) error {
	c.Instance = name

	inst, ok := c.Instances[name]
	if !ok {
		// Check if it's a Patroni member and give a helpful hint
		for scope, cluster := range c.Addons.Patroni {
			if _, isMember := cluster.Members[name]; isMember {
				return fmt.Errorf("instance %q is a Patroni member in scope %q — use `pg ha start %s --member %s` instead",
					name, scope, scope, name)
			}
		}
		return fmt.Errorf("instance %q not found in config", name)
	}
	// Merge Postgres config
	if inst.Postgres.Host != "" {
		c.Postgres.Host = inst.Postgres.Host
	}
	if inst.Postgres.Port != 0 {
		c.Postgres.Port = inst.Postgres.Port
	}
	if inst.Postgres.User != "" {
		c.Postgres.User = inst.Postgres.User
	}
	if inst.Postgres.Password != "" {
		c.Postgres.Password = inst.Postgres.Password
	}
	if inst.Postgres.Database != "" {
		c.Postgres.Database = inst.Postgres.Database
	}
	if inst.Postgres.URL != "" {
		c.Postgres.URL = inst.Postgres.URL
	}

	// Merge Podman config
	if inst.Podman.ContainerName != "" {
		c.Podman.ContainerName = inst.Podman.ContainerName
	}
	if inst.Podman.DataDir != "" {
		c.Podman.DataDir = inst.Podman.DataDir
	}
	if inst.Podman.ImageTag != "" {
		c.Podman.ImageTag = inst.Podman.ImageTag
	}
	// HostPort maps to Postgres.Port for external connections (GetPostgresURL).
	if inst.Podman.HostPort != 0 {
		c.Podman.HostPort = inst.Podman.HostPort
		c.Postgres.Port = inst.Podman.HostPort
	}
	if inst.Podman.SSHPort != 0 {
		c.Podman.SSHPort = inst.Podman.SSHPort
	}

	// Merge PITR config
	c.PITR.Enabled = inst.PITR.Enabled
	if inst.PITR.PgBackRestStanza != "" {
		c.PITR.PgBackRestStanza = inst.PITR.PgBackRestStanza
	}

	return nil
}

// IsReplica reports whether the currently-selected instance is a physical
// replica (ReplicaOf or PrimaryDSN set) of another instance.
func (c *Config) IsReplica() bool {
	inst, ok := c.Instances[c.Instance]
	return ok && (inst.ReplicaOf != "" || inst.PrimaryDSN != "")
}

// ReplicaOf returns the primary instance name for a replica, or "" if the
// named instance is not a replica.
func (c *Config) ReplicaOf(name string) string {
	inst, ok := c.Instances[name]
	if !ok {
		return ""
	}
	return inst.ReplicaOf
}

// Load reads configuration from a file and merges it with defaults.
func Load(path string) (*Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // config file doesn't exist, return defaults
		}
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}

	cfg.ApplyDefaults()
	return cfg, nil
}

// displayConfig is the serializable subset of Config for save/display.
// Global postgres/podman/pitr are excluded -- they are in-memory defaults only.
type displayConfig struct {
	BaseDir                 string                    `yaml:"base_dir,omitempty"`
	Network                 string                    `yaml:"network,omitempty"`
	Namespace               string                    `yaml:"namespace,omitempty"`
	PGStartPort             int                       `yaml:"pg_start_port,omitempty"`
	PGSSHPort               int                       `yaml:"pg_ssh_port,omitempty"`
	PgBouncerStartPort      int                       `yaml:"pgbouncer_start_port,omitempty"`
	EtcdStartPort           int                       `yaml:"etcd_start_port,omitempty"`
	PgDogStartPort          int                       `yaml:"pgdog_start_port,omitempty"`
	PatroniStartPort        int                       `yaml:"patroni_start_port,omitempty"`
	PatroniRestapiStartPort int                       `yaml:"patroni_restapi_start_port,omitempty"`
	HaProxyStartPort        int                       `yaml:"haproxy_start_port,omitempty"`
	MinioStartPort          int                       `yaml:"minio_start_port,omitempty"`
	PostgrestStartPort      int                       `yaml:"postgrest_start_port,omitempty"`
	RedisStartPort          int                       `yaml:"redis_start_port,omitempty"`
	PredixyStartPort        int                       `yaml:"predixy_start_port,omitempty"`
	PgAdminStartPort        int                       `yaml:"pgadmin_start_port,omitempty"`
	GrafanaStartPort        int                       `yaml:"grafana_start_port,omitempty"`
	PrometheusStartPort     int                       `yaml:"prometheus_start_port,omitempty"`
	AlertmanagerStartPort   int                       `yaml:"alertmanager_start_port,omitempty"`
	GiteaStartPort          int                       `yaml:"gitea_start_port,omitempty"`
	GiteaSSHStartPort       int                       `yaml:"gitea_ssh_start_port,omitempty"`
	NginxStartPort          int                       `yaml:"nginx_start_port,omitempty"`
	GarageStartPort         int                       `yaml:"garage_start_port,omitempty"`
	SeaweedfsStartPort      int                       `yaml:"seaweedfs_start_port,omitempty"`
	VictoriaMetricsStartPort int                      `yaml:"victoriametrics_start_port,omitempty"`
	VMClusterStartPort       int                      `yaml:"vmcluster_start_port,omitempty"`
	VictoriaLogsStartPort    int                      `yaml:"victorialogs_start_port,omitempty"`
	Logging                 LoggingConfig             `yaml:"logging"`
	Backup                  BackupConfig              `yaml:"backup"`
	Pigsty                  PigstyConfig              `yaml:"pigsty"`
	Addons                  TopAddonsConfig           `yaml:"addons,omitempty"`
	Instances               map[string]InstanceConfig `yaml:"instances"`
}

// Display returns a view of the config suitable for display or saving.
func (c *Config) Display() displayConfig {
	return displayConfig{
		BaseDir:                 c.BaseDir,
		Network:                 c.Podman.Network,
		Namespace:               c.Namespace,
		PGStartPort:             c.PGStartPort,
		PGSSHPort:               c.PGSSHPort,
		PgBouncerStartPort:      c.PgBouncerStartPort,
		EtcdStartPort:           c.EtcdStartPort,
		PgDogStartPort:          c.PgDogStartPort,
		PatroniStartPort:        c.PatroniStartPort,
		PatroniRestapiStartPort: c.PatroniRestapiStartPort,
		HaProxyStartPort:        c.HaProxyStartPort,
		MinioStartPort:          c.MinioStartPort,
		PostgrestStartPort:      c.PostgrestStartPort,
		RedisStartPort:          c.RedisStartPort,
		PredixyStartPort:        c.PredixyStartPort,
		PgAdminStartPort:        c.PgAdminStartPort,
		GrafanaStartPort:        c.GrafanaStartPort,
		PrometheusStartPort:     c.PrometheusStartPort,
		AlertmanagerStartPort:   c.AlertmanagerStartPort,
		GiteaStartPort:          c.GiteaStartPort,
		GiteaSSHStartPort:       c.GiteaSSHStartPort,
		NginxStartPort:          c.NginxStartPort,
		GarageStartPort:         c.GarageStartPort,
		SeaweedfsStartPort:      c.SeaweedfsStartPort,
		VictoriaMetricsStartPort: c.VictoriaMetricsStartPort,
		VMClusterStartPort:      c.VMClusterStartPort,
		VictoriaLogsStartPort:   c.VictoriaLogsStartPort,
		Logging:                 c.Logging,
		Backup:                  c.Backup,
		Pigsty:                  c.Pigsty,
		Addons:                  c.Addons,
		Instances:               c.Instances,
	}
}

// Save writes the configuration to a file.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	data, err := yaml.Marshal(c.Display())
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing config file %s: %w", path, err)
	}
	return nil
}

// Validate checks that the configuration is complete.
func (c *Config) Validate() error {
	if c.Namespace != "" {
		nsOK, err := regexp.MatchString(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`, c.Namespace)
		if err != nil || !nsOK {
			return fmt.Errorf("namespace %q must match [A-Za-z0-9][A-Za-z0-9_-]{0,31} (no spaces or slashes)", c.Namespace)
		}
	}
	for name, val := range map[string]int{"pg_start_port": c.PGStartPort, "pg_ssh_port": c.PGSSHPort} {
		if val < 1 || val > 65535 {
			return fmt.Errorf("%s must be between 1 and 65535, got %d", name, val)
		}
	}
	if c.Podman.ContainerName == "" {
		return fmt.Errorf("podman.container_name must not be empty")
	}
	if c.PITR.Enabled && c.PITR.PgBackRestStanza == "" {
		return fmt.Errorf("pitr.pgbackrest_stanza must not be empty (PITR is enabled)")
	}
	// BeeGFS v2 cross-host entries: a hand-edited pg.yaml can pin a role list
	// the manager cannot build containers for, a node id that would collide
	// with derived target ids, or a join host with no mgmtd to join.
	for name, bc := range c.Addons.BeeGFS {
		for _, role := range bc.Roles {
			if !ValidBeeGFSService(role) {
				return fmt.Errorf("addons.beegfs.%s.roles: unknown service %q (valid: mgmtd, meta, storage)", name, role)
			}
		}
		if bc.NodeID < 0 || bc.NodeID > 655 {
			return fmt.Errorf("addons.beegfs.%s.node_id must be 1-655 (target ids derive as node_id*100+N), got %d", name, bc.NodeID)
		}
		if len(bc.Roles) > 0 && !bc.RunsService(BeeGFSServiceMgmtd) && bc.MgmtdHost == "" {
			return fmt.Errorf("addons.beegfs.%s.mgmtd_host must be set when roles %v do not include mgmtd — this host joins an existing filesystem and must know where its management daemon lives", name, bc.Roles)
		}
		// A license is verified by mgmtd alone (only its image ships
		// libbeegfs_license.so) — storing one on a join host would silently
		// never load, so refuse the drift. Shape check is deliberately shallow:
		// cryptographic validity is mgmtd's job, this only catches feeding pg
		// the wrong file.
		if bc.License != "" {
			if !bc.RunsService(BeeGFSServiceMgmtd) {
				return fmt.Errorf("addons.beegfs.%s.license is only valid on the host whose roles include mgmtd — the management daemon verifies the certificate", name)
			}
			if !strings.Contains(bc.License, "BEGIN") || !strings.Contains(bc.License, "END") {
				return fmt.Errorf("addons.beegfs.%s.license does not look like a PEM certificate (no BEGIN/END block) — expected the ThinkParQ license .pem file", name)
			}
		}
	}
	for name, jc := range c.Addons.Juicefs {
		if jc.MountPoint != "" && !filepath.IsAbs(jc.MountPoint) {
			return fmt.Errorf("addons.juicefs.%s.mount_point must be an absolute path, got %q", name, jc.MountPoint)
		}
		// MetaDSN is the explicitly-supplied metadata-engine URL (mount needs
		// only it, after format); it must carry a scheme juicefs accepts.
		if err := ValidateJuicefsDSNScheme(jc.MetaDSN); err != nil {
			return fmt.Errorf("addons.juicefs.%s: %w", name, err)
		}
		if err := ValidateS3Endpoint("addons.juicefs."+name, jc.S3Endpoint); err != nil {
			return err
		}
	}
	zports := map[int]string{}
	for name, zc := range c.Addons.Zerofs {
		if zc.MountPoint != "" && !filepath.IsAbs(zc.MountPoint) {
			return fmt.Errorf("addons.zerofs.%s.mount_point must be an absolute path, got %q", name, zc.MountPoint)
		}
		// 9P has no authentication layer — a non-loopback listener exposes the
		// filesystem to the network.
		if zc.Listen != "" && !isLoopbackHost(zc.Listen) {
			return fmt.Errorf("addons.zerofs.%s.listen %q must be loopback (127.0.0.1/::1/localhost) — the 9P protocol carries no authentication", name, zc.Listen)
		}
		if zc.ConditionalPut != "" && !ValidZerofsConditionalPut(zc.ConditionalPut) {
			return fmt.Errorf("addons.zerofs.%s.conditional_put %q is not one of auto, redis, none", name, zc.ConditionalPut)
		}
		if err := ValidateS3Endpoint("addons.zerofs."+name, zc.S3Endpoint); err != nil {
			return err
		}
		if zc.ListenPort != 0 {
			// A zerofs server that loses the 9P bind starts silently and
			// listens on nothing, so one port per instance is enforced here —
			// conservatively keyed on the port alone (127.0.0.1 and ::1 are
			// distinct sockets, but a refused load beats that silent failure).
			if prev, dup := zports[zc.ListenPort]; dup {
				return fmt.Errorf("addons.zerofs.%s.listen_port %d already used by %s (one 9P port per addon instance)", name, zc.ListenPort, prev)
			}
			zports[zc.ListenPort] = name
		}
	}
	return nil
}

// ValidateS3Endpoint checks a bare http(s)://host[:port] external S3 endpoint
// (no bucket path, no embedded credentials or query). Shape-only: the presence
// requirement is enforced at install, so a half-written config still loads.
func ValidateS3Endpoint(path, endpoint string) error {
	if endpoint == "" {
		return nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%s.s3_endpoint %q is not a bare http(s)://host[:port] URL", path, endpoint)
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("%s.s3_endpoint %q must not carry a path — the bucket is named separately", path, endpoint)
	}
	if u.RawQuery != "" || u.User != nil {
		return fmt.Errorf("%s.s3_endpoint %q must not embed credentials or a query string", path, endpoint)
	}
	return nil
}

// ValidateJuicefsDSNScheme checks MetaDSN carries one of the URL schemes
// juicefs itself accepts (postgres://, redis://, sqlite3://). It is required —
// there is no same-host engine auto-detection, so the operator always names it.
func ValidateJuicefsDSNScheme(dsn string) error {
	if dsn == "" {
		return fmt.Errorf("meta_dsn is required — the metadata engine is always given explicitly (--dsn)")
	}
	for _, scheme := range []string{"postgres://", "postgresql://", "redis://", "rediss://", "sqlite3://"} {
		if strings.HasPrefix(dsn, scheme) {
			return nil
		}
	}
	return fmt.Errorf("meta_dsn %q must start with postgres://, redis:// or sqlite3://", dsn)
}

// isLoopbackHost reports whether a listen host is loopback-only.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// GetPostgresURL returns the PostgreSQL connection string.
func (c *Config) GetPostgresURL() string {
	if c.Postgres.URL != "" {
		return c.Postgres.URL
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s",
		c.Postgres.User, c.Postgres.Password,
		c.Postgres.Host, c.Postgres.Port,
		c.Postgres.Database)
}

// ApplyDefaults fills zero-value fields with their defaults.
func (c *Config) ApplyDefaults() {
	d := Default()

	// Namespace / port bases
	if c.PGStartPort == 0 {
		c.PGStartPort = d.PGStartPort
	}
	if c.PGSSHPort == 0 {
		c.PGSSHPort = d.PGSSHPort
	}
	if c.PgBouncerStartPort == 0 {
		c.PgBouncerStartPort = d.PgBouncerStartPort
	}
	if c.EtcdStartPort == 0 {
		c.EtcdStartPort = d.EtcdStartPort
	}
	if c.PatroniStartPort == 0 {
		c.PatroniStartPort = d.PatroniStartPort
	}
	if c.PatroniRestapiStartPort == 0 {
		c.PatroniRestapiStartPort = d.PatroniRestapiStartPort
	}
	if c.HaProxyStartPort == 0 {
		c.HaProxyStartPort = d.HaProxyStartPort
	}
	if c.MinioStartPort == 0 {
		c.MinioStartPort = d.MinioStartPort
	}
	if c.PostgrestStartPort == 0 {
		c.PostgrestStartPort = d.PostgrestStartPort
	}
	if c.RedisStartPort == 0 {
		c.RedisStartPort = d.RedisStartPort
	}
	if c.PredixyStartPort == 0 {
		c.PredixyStartPort = d.PredixyStartPort
	}
	if c.PgAdminStartPort == 0 {
		c.PgAdminStartPort = d.PgAdminStartPort
	}
	if c.GrafanaStartPort == 0 {
		c.GrafanaStartPort = d.GrafanaStartPort
	}
	if c.PrometheusStartPort == 0 {
		c.PrometheusStartPort = d.PrometheusStartPort
	}
	if c.AlertmanagerStartPort == 0 {
		c.AlertmanagerStartPort = d.AlertmanagerStartPort
	}
	if c.GiteaStartPort == 0 {
		c.GiteaStartPort = d.GiteaStartPort
	}
	if c.GiteaSSHStartPort == 0 {
		c.GiteaSSHStartPort = d.GiteaSSHStartPort
	}
	if c.NginxStartPort == 0 {
		c.NginxStartPort = d.NginxStartPort
	}
	if c.GarageStartPort == 0 {
		c.GarageStartPort = d.GarageStartPort
	}
	if c.SeaweedfsStartPort == 0 {
		c.SeaweedfsStartPort = d.SeaweedfsStartPort
	}
	if c.VictoriaMetricsStartPort == 0 {
		c.VictoriaMetricsStartPort = d.VictoriaMetricsStartPort
	}
	if c.VMClusterStartPort == 0 {
		c.VMClusterStartPort = d.VMClusterStartPort
	}
	if c.VictoriaLogsStartPort == 0 {
		c.VictoriaLogsStartPort = d.VictoriaLogsStartPort
	}

	// Postgres
	if c.Postgres.Host == "" {
		c.Postgres.Host = d.Postgres.Host
	}
	if c.Postgres.Port == 0 {
		c.Postgres.Port = d.Postgres.Port
	}
	if c.Postgres.User == "" {
		c.Postgres.User = d.Postgres.User
	}
	if c.Postgres.Password == "" {
		c.Postgres.Password = d.Postgres.Password
	}
	if c.Postgres.Database == "" {
		c.Postgres.Database = d.Postgres.Database
	}

	// Podman
	if c.Podman.ContainerName == "" {
		c.Podman.ContainerName = d.Podman.ContainerName
	}
	if c.Podman.DataDir == "" {
		c.Podman.DataDir = d.Podman.DataDir
	}
	if c.Podman.ImageTag == "" {
		c.Podman.ImageTag = d.Podman.ImageTag
	}
	if c.Podman.Network == "" {
		if c.Network != "" {
			c.Podman.Network = c.Network
		} else {
			c.Podman.Network = d.Podman.Network
		}
	}

	// PITR
	if c.PITR.PgBackRestStanza == "" {
		c.PITR.PgBackRestStanza = d.PITR.PgBackRestStanza
	}

	// Logging
	if c.Logging.Level == "" {
		c.Logging.Level = d.Logging.Level
	}

	// Backup
	if c.Backup.ContainerName == "" {
		c.Backup.ContainerName = d.Backup.ContainerName
	}
	// With a namespace, the shared backup container must be isolated too,
	// otherwise configs sharing one host would collide on "pgcli-backup".
	// Only rename while it still holds the bare default; an explicit name is kept.
	if c.Namespace != "" && c.Backup.ContainerName == "pgcli-backup" {
		c.Backup.ContainerName = "pgcli-backup" + nsSuffix(c.Namespace)
	}
	if c.Backup.ImageTag == "" {
		c.Backup.ImageTag = d.Backup.ImageTag
	}
	if c.Backup.DataDir == "" {
		c.Backup.DataDir = d.Backup.DataDir
	}
	if c.Backup.LogDir == "" {
		c.Backup.LogDir = d.Backup.LogDir
	}
	if c.Backup.RetentionFull == 0 {
		c.Backup.RetentionFull = d.Backup.RetentionFull
	}
	if c.Backup.Repo.S3 != nil {
		s3 := c.Backup.Repo.S3
		if s3.Region == "" {
			s3.Region = "us-east-1"
		}
		if s3.URIStyle == "" {
			s3.URIStyle = "path"
		}
		if s3.Path == "" {
			s3.Path = "/pgbackrest"
		}
	}

	// Pigsty
	if c.Pigsty.Repo == "" {
		c.Pigsty.Repo = d.Pigsty.Repo
	}

	// Instances: apply per-instance defaults
	if c.Instances == nil {
		c.Instances = make(map[string]InstanceConfig)
	}
	for name, inst := range c.Instances {
		def := c.InstanceDefaults(name)
		if inst.Postgres.Host == "" {
			inst.Postgres.Host = def.Postgres.Host
		}
		if inst.Postgres.Port == 0 {
			inst.Postgres.Port = def.Postgres.Port
		}
		if inst.Postgres.User == "" {
			inst.Postgres.User = def.Postgres.User
		}
		if inst.Postgres.Password == "" {
			inst.Postgres.Password = def.Postgres.Password
		}
		if inst.Postgres.Database == "" {
			inst.Postgres.Database = def.Postgres.Database
		}
		if inst.Podman.ContainerName == "" {
			inst.Podman.ContainerName = def.Podman.ContainerName
		}
		if inst.Podman.DataDir == "" {
			inst.Podman.DataDir = def.Podman.DataDir
		}
		if inst.Podman.ImageTag == "" {
			inst.Podman.ImageTag = def.Podman.ImageTag
		}
		if inst.Podman.HostPort == 0 {
			inst.Podman.HostPort = def.Podman.HostPort
		}
		if inst.Podman.SSHPort == 0 {
			inst.Podman.SSHPort = def.Podman.SSHPort
		}
		if inst.PITR.PgBackRestStanza == "" {
			inst.PITR.PgBackRestStanza = def.PITR.PgBackRestStanza
		}
		// PgBouncer addon defaults (only when PgBouncer is present)
		if inst.Addons.PgBouncer != nil {
			pb := inst.Addons.PgBouncer
			if pb.ContainerName == "" {
				pb.ContainerName = "pgcli-pgbouncer" + nsSuffix(c.Namespace) + "-" + name
			}
			if pb.ImageTag == "" {
				pb.ImageTag = DefaultPgBouncerImageTag
			}
			if pb.PoolMode == "" {
				pb.PoolMode = "transaction"
			}
			if pb.MaxClientConn == 0 {
				pb.MaxClientConn = 100
			}
			if pb.DefaultPoolSize == 0 {
				pb.DefaultPoolSize = 20
			}
			// New optional fields: 0 means "use PgBouncer's own default"
			// so we don't need to fill them — pgbouncer.ini will omit them.
			inst.Addons.PgBouncer = pb
		}
		// PostgREST addon defaults (only when a postgrest sidecar is present).
		// DbPool/Schemas are left empty on purpose: 0/"" means "use PostgREST's
		// own default" (10 / public), so the env is simply not set.
		if inst.Addons.Postgrest != nil {
			pr := inst.Addons.Postgrest
			if pr.ContainerName == "" {
				pr.ContainerName = "pgcli-postgrest" + nsSuffix(c.Namespace) + "-" + name
			}
			if pr.ImageTag == "" {
				pr.ImageTag = DefaultPostgrestImageTag
			}
			if pr.Listen == "" {
				pr.Listen = "127.0.0.1"
			}
			inst.Addons.Postgrest = pr
		}
		c.Instances[name] = inst
	}

	// Top-level addons defaults (remote PgBouncer pools)
	for name, addon := range c.Addons.PgBouncer {
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-pgbouncer" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = DefaultPgBouncerImageTag
		}
		if addon.PoolMode == "" {
			addon.PoolMode = "transaction"
		}
		if addon.MaxClientConn == 0 {
			addon.MaxClientConn = 100
		}
		if addon.DefaultPoolSize == 0 {
			addon.DefaultPoolSize = 20
		}
		c.Addons.PgBouncer[name] = addon
	}

	// Top-level addons defaults (remote PostgREST APIs). Same dual shape as the
	// PgBouncer map; DbPool/Schemas stay empty to defer to PostgREST's defaults.
	for name, addon := range c.Addons.Postgrest {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-postgrest" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = DefaultPostgrestImageTag
		}
		if addon.Listen == "" {
			addon.Listen = "127.0.0.1"
		}
		c.Addons.Postgrest[name] = addon
	}

	// Top-level addons defaults (etcd)
	for name, addon := range c.Addons.Etcd {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ClusterName == "" {
			addon.ClusterName = "pgcli-etcd"
		}
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-etcd" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = "quay.io/coreos/etcd:v3.5.30"
		}
		c.Addons.Etcd[name] = addon
	}

	// Top-level addons defaults (pgdog proxy)
	for name, addon := range c.Addons.PgDog {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-pgdog" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = "ghcr.io/pgdogdev/pgdog:v0.1.57"
		}
		if addon.Host == "" {
			addon.Host = "127.0.0.1"
		}
		if addon.PoolerMode == "" {
			addon.PoolerMode = "transaction"
		}
		if addon.Workers == 0 {
			addon.Workers = 2
		}
		if addon.DefaultPoolSize == 0 {
			addon.DefaultPoolSize = 10
		}
		c.Addons.PgDog[name] = addon
	}

	// Top-level addons defaults (Patroni HA clusters). Each host records only
	// its own members of a scope; the cluster reassembles across hosts via the
	// shared DCS. Container name, image, and per-member data dir are filled
	// here so `pg ha create` need only set the flags.
	{
		patroniBaseDir := platform.DefaultConfigDir()
		if c.BaseDir != "" {
			patroniBaseDir = c.BaseDir
		}
		for scope, cluster := range c.Addons.Patroni {
			if cluster.Name == "" {
				cluster.Name = scope
			}
			nsScope := scope + nsSuffix(c.Namespace)
			// Detect cluster-wide image tag: if any member has a non-default
			// image (e.g. -ext), use it as the default for new members too.
			clusterImageTag := ""
			for _, mb := range cluster.Members {
				if mb.ImageTag != "" && mb.ImageTag != "ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5" {
					clusterImageTag = mb.ImageTag
					break
				}
			}
			for member, mb := range cluster.Members {
				if mb.RemoteHost != "" {
					continue // remote member: connection params only, no local container
				}
				if mb.ContainerName == "" {
					mb.ContainerName = "pgcli-patroni" + nsSuffix(c.Namespace) + "-" + scope + "-" + member
				}
				if mb.ImageTag == "" {
					if clusterImageTag != "" {
						mb.ImageTag = clusterImageTag
					} else {
						mb.ImageTag = "ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5"
					}
				}
				if mb.DataDir == "" {
					mb.DataDir = filepath.Join(patroniBaseDir, "addon", "patroni", nsScope, member)
				}
				cluster.Members[member] = mb
			}
			c.Addons.Patroni[scope] = cluster
		}
	}

	// Top-level addons defaults (HAProxy load balancers in front of Patroni
	// clusters). Container name and image are filled here so `pg addon install
	// haproxy` need only set the flags.
	for name, addon := range c.Addons.HAProxy {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-haproxy" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = "docker.io/library/haproxy:3.2.23-alpine"
		}
		if addon.Listen == "" {
			addon.Listen = "127.0.0.1"
		}
		c.Addons.HAProxy[name] = addon
	}

	// Top-level addons defaults (nginx reverse proxy in front of web services).
	// Container name and image are filled here so `pg addon install nginx` need
	// only set the flags.
	for name, addon := range c.Addons.Nginx {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-nginx" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = "docker.io/library/nginx:1.27-alpine"
		}
		if addon.Listen == "" {
			addon.Listen = "127.0.0.1"
		}
		c.Addons.Nginx[name] = addon
	}

	// Top-level addons defaults (MinIO single-node object storage). DataDir is
	// intentionally left empty here — the manager resolves it relative to the
	// base dir at container-creation time.
	for name, addon := range c.Addons.Minio {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-minio" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = DefaultMinioImageTag
		}
		if addon.Listen == "" {
			addon.Listen = "127.0.0.1"
		}
		if addon.RootUser == "" {
			addon.RootUser = "admin"
		}
		c.Addons.Minio[name] = addon
	}

	// Top-level addons defaults (silo — the Pigsty MinIO fork). Same shape as
	// the minio block; DataDir is left empty for the manager to resolve under
	// the base dir at container-creation time.
	for name, addon := range c.Addons.Silo {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-silo" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = DefaultSiloImageTag
		}
		if addon.Listen == "" {
			addon.Listen = "127.0.0.1"
		}
		if addon.RootUser == "" {
			addon.RootUser = "admin"
		}
		c.Addons.Silo[name] = addon
	}

	// Top-level addons defaults (rustfs). Same shape as the minio/silo blocks;
	// DataDir is left empty for the manager to resolve under the base dir at
	// container-creation time. RootUser defaults to "admin" (rustfs imposes no
	// minimum access-key length — verified 3/4/5/18-char keys all start — and
	// this avoids the reserved "rustfsadmin" that only warns).
	for name, addon := range c.Addons.Rustfs {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-rustfs" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = DefaultRustfsImageTag
		}
		if addon.Listen == "" {
			addon.Listen = "127.0.0.1"
		}
		if addon.RootUser == "" {
			addon.RootUser = "admin"
		}
		c.Addons.Rustfs[name] = addon
	}

	// Top-level addons defaults (Garage). Same shape as the rustfs block; the
	// two data dirs and the config file are left empty for the manager to
	// resolve under the base dir at container-creation time. RootUser/
	// RootPassword are NOT defaulted here: Garage access keys must carry the
	// "GK-" prefix and the secret is generated once, so the install handler
	// generates both (a hand-written config without them just means "generate
	// on next install", same as rustfs's password). Region and
	// ReplicationFactor carry Garage-specific defaults.
	for name, addon := range c.Addons.Garage {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-garage" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = DefaultGarageImageTag
		}
		if addon.Listen == "" {
			addon.Listen = "127.0.0.1"
		}
		if addon.Region == "" {
			addon.Region = "garage"
		}
		if addon.ReplicationFactor == 0 {
			addon.ReplicationFactor = 1
		}
		c.Addons.Garage[name] = addon
	}

	// Top-level addons defaults (SeaweedFS). Same shape as the Garage block:
	// DataDir stays empty for the manager to resolve under the base dir.
	// RootUser/RootPassword/FilerDSN are NOT defaulted here (determinism
	// contract) — the install handler generates the credential pair and
	// resolves the filer DSN. Replication (placement code) and VolumeSizeMB
	// carry SeaweedFS-specific defaults; the bucket defaults to the instance
	// name (mirrors Garage's --default-bucket-named-after-instance choice).
	for name, addon := range c.Addons.Seaweedfs {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-seaweedfs" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = DefaultSeaweedfsImageTag
		}
		if addon.Listen == "" {
			addon.Listen = "127.0.0.1"
		}
		if addon.Replication == "" {
			addon.Replication = "000"
		}
		if addon.VolumeSizeMB == 0 {
			addon.VolumeSizeMB = 1024
		}
		if addon.Bucket == "" {
			addon.Bucket = addon.Name
		}
		c.Addons.Seaweedfs[name] = addon
	}

	// Top-level addons defaults (Redis). Version is the source of truth for the
	// image when ImageTag is empty: an empty Version means DefaultRedisMajor, an
	// unknown one is left alone here (the CLI rejects it at install; hand-edited
	// configs surface as an image-not-found pull error, not a silent rewrite).
	// Password is deliberately NOT generated here — ApplyDefaults must stay
	// deterministic (it runs on every load), so the CLI install handler fills it
	// once, same as rustfs's RootPassword. Listen defaults to 0.0.0.0:
	// requirepass is always set, so an unauthenticated Redis is not a
	// reachable state.
	for name, addon := range c.Addons.Redis {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-redis" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.Version == "" && addon.ImageTag == "" {
			addon.Version = DefaultRedisMajor
		}
		if addon.ImageTag == "" {
			if tag, ok := RedisImageTagForMajor(addon.Version); ok {
				addon.ImageTag = tag
			}
		}
		if addon.Listen == "" {
			addon.Listen = "0.0.0.0"
		}
		c.Addons.Redis[name] = addon
	}

	// Top-level addons defaults (Predixy). Password and Backend are deliberately
	// NOT defaulted here — the CLI install handler requires them (ApplyDefaults
	// must stay deterministic). Listen defaults to 0.0.0.0 like Redis: a proxy
	// whose only access contract is AUTH has no reason to bind loopback-only.
	// Workers defaults to Predixy's own factory value (1).
	for name, addon := range c.Addons.Predixy {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-predixy" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = DefaultPredixyImageTag
		}
		if addon.Listen == "" {
			addon.Listen = "0.0.0.0"
		}
		if addon.Workers == 0 {
			addon.Workers = DefaultPredixyWorkers
		}
		c.Addons.Predixy[name] = addon
	}

	// Top-level addons defaults (pgAdmin 4). Password is deliberately NOT
	// defaulted here — ApplyDefaults must stay deterministic and a login
	// password must never be a hardcoded literal; the CLI install handler
	// generates one when empty. DSN/ServerName stay empty by default (no
	// pre-registered server). Listen defaults to loopback like PostgREST — a
	// web admin UI should not bind 0.0.0.0 unless asked.
	for name, addon := range c.Addons.PgAdmin {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-pgadmin" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = DefaultPgAdminImageTag
		}
		if addon.Listen == "" {
			addon.Listen = "127.0.0.1"
		}
		if addon.Email == "" {
			// The container's entrypoint validates this with email-validator and
			// refuses reserved TLDs — `.local` included — so pgAdmin would
			// crash-loop. `.lan` is accepted (and still non-routable).
			addon.Email = "admin@pgcli.lan"
		}
		c.Addons.PgAdmin[name] = addon
	}

	// Top-level addons defaults (Grafana). Same shape as pgAdmin above and the
	// same determinism constraint: Password is deliberately NOT defaulted here
	// (ApplyDefaults must stay deterministic and a login password must never be
	// a hardcoded literal; the CLI install handler generates one when empty).
	// Listen defaults to 0.0.0.0 — unlike pgAdmin — on purpose: a monitoring
	// dashboard is meant to be viewed from other machines, containers run on
	// the host network (netFlags → --network host on Linux), so a loopback bind
	// would make the panel unreachable remotely. The exposure is guarded by the
	// auto-generated admin password plus GF_USERS_ALLOW_SIGN_UP=false at
	// launch (see podman.grafanaArgsAndEnv); `--listen 127.0.0.1` opts back
	// into local-only.
	for name, addon := range c.Addons.Grafana {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-grafana" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = DefaultGrafanaImageTag
		}
		if addon.Listen == "" {
			addon.Listen = "0.0.0.0"
		}
		if addon.AdminUser == "" {
			addon.AdminUser = "admin"
		}
		c.Addons.Grafana[name] = addon
	}

	// Top-level addons defaults (Prometheus). Same shape as Grafana above.
	// Listen defaults to 0.0.0.0 for the same reason — a metrics backend is
	// scraped/queried from other machines (and Grafana reads its API) — but
	// unlike Grafana there is NO password guarding that exposure: Prometheus has
	// no authentication and its API runs arbitrary PromQL, so the firewall /
	// `--listen 127.0.0.1` is the only boundary (documented in the manager and
	// the addon page). Retention and ScrapeInterval are deterministic literals
	// (not secrets), so filling them here is safe; they only take effect in
	// rendered-config mode for ScrapeInterval, while Retention is a CLI flag and
	// applies even alongside --conf-file. Scrape (extra targets), Alertmanager
	// (forwarding URLs) and RuleFiles are never defaulted — empty means "render
	// without an alerting section / without rule_files", the same contract.
	for name, addon := range c.Addons.Prometheus {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-prometheus" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = DefaultPrometheusImageTag
		}
		if addon.Listen == "" {
			addon.Listen = "0.0.0.0"
		}
		if addon.Retention == "" {
			addon.Retention = "15d"
		}
		if addon.ScrapeInterval == "" {
			addon.ScrapeInterval = "15s"
		}
		c.Addons.Prometheus[name] = addon
	}

	// Top-level addons defaults (VictoriaMetrics). Same shape as Prometheus:
	// Listen defaults to 0.0.0.0 because a metrics service is reached from other
	// machines. No auth — guarded only by the network boundary. Retention
	// (months) is a deterministic literal, not a secret.
	for name, addon := range c.Addons.VictoriaMetrics {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-victoriametrics" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = DefaultVictoriaMetricsImageTag
		}
		if addon.Listen == "" {
			addon.Listen = "0.0.0.0"
		}
		if addon.Retention == "" {
			addon.Retention = "1"
		}
		c.Addons.VictoriaMetrics[name] = addon
	}

	// Top-level addons defaults (VictoriaLogs). Exact mirror of the
	// VictoriaMetrics block above: one container, one port, pure CLI flags,
	// no auth — guarded only by the network boundary. Retention is NOT
	// defaulted here: an empty value simply omits -retentionPeriod and lets
	// the victoria-logs binary apply its own default, so pgcli never shadows
	// an upstream default that may change between releases.
	for name, addon := range c.Addons.VictoriaLogs {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-victorialogs" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = DefaultVictoriaLogsImageTag
		}
		if addon.Listen == "" {
			addon.Listen = "0.0.0.0"
		}
		c.Addons.VictoriaLogs[name] = addon
	}

	// Top-level addons defaults (Alertmanager). Same shape as Prometheus above:
	// Listen defaults to 0.0.0.0 because a metrics-adjacent service is reached
	// from other machines — but with a sharper edge than Prometheus: Alertmanager
	// has no auth AND an exposed instance lets anyone create silences, i.e. mute
	// your alerts. Network boundary is the only guard (see the addon docs).
	// Webhook (rendered notification URLs) is never defaulted — an empty list
	// still renders a valid no-op receiver.
	for name, addon := range c.Addons.Alertmanager {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-alertmanager" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = DefaultAlertmanagerImageTag
		}
		if addon.Listen == "" {
			addon.Listen = "0.0.0.0"
		}
		c.Addons.Alertmanager[name] = addon
	}

	// Top-level addons defaults (Gitea). Same shape as Grafana above: Listen
	// defaults to 0.0.0.0 (a git host is cloned from other machines), and
	// Password is deliberately NOT defaulted here — ApplyDefaults must stay
	// deterministic and a login password must never be a hardcoded literal;
	// the CLI install handler generates one when empty, and it only ever takes
	// effect against a fresh store (see podman.GiteaStoreExists). AdminUser
	// defaults to "gitea" on purpose: "git" is the container's OS user that
	// ssh clone authenticates AS, keeping the web admin distinct avoids two
	// different "git"s in every clone URL and error message. SSH/SSHPort/Dsn
	// are never defaulted — zero/empty means "built-in SSH listener off /
	// embedded SQLite", which are the rendered configurations, not gaps.
	for name, addon := range c.Addons.Gitea {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-gitea" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = DefaultGiteaImageTag
		}
		if addon.Listen == "" {
			addon.Listen = "0.0.0.0"
		}
		if addon.AdminUser == "" {
			addon.AdminUser = "gitea"
		}
		c.Addons.Gitea[name] = addon
	}

	// Top-level addons defaults (BeeGFS). Listen defaults to 0.0.0.0 (a
	// parallel filesystem is mounted from other hosts). ImageTag stores only a
	// version and defaults to DefaultBeeGFSVersion. ConnAuth is deliberately
	// NOT defaulted — it is a shared secret the CLI generates on install. The
	// four ports default to the STOCK BeeGFS conventions (8008/8010/8005/8003):
	// meta and storage have no supported listen-port override in 8.4, so v1
	// runs exactly one filesystem per host on these fixed ports (see the
	// note at the end of autoAssignPorts). A non-zero user-pinned port is kept
	// as-is — the daemon flag is emitted for it — but only mgmtd's two can
	// actually move; the install handler rejects the rest. The data dirs are
	// never defaulted — the manager resolves them under base_dir, same
	// contract as Gitea.DataDir. Autostart is a bool, no default.
	for name, addon := range c.Addons.BeeGFS {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerPrefix == "" {
			addon.ContainerPrefix = "pgcli-beegfs" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = DefaultBeeGFSVersion
		}
		if addon.Listen == "" {
			addon.Listen = "0.0.0.0"
		}
		if addon.MgmtdPort == 0 {
			addon.MgmtdPort = DefaultBeeGFSMgmtdPort
		}
		if addon.MgmtdGRPCPort == 0 {
			addon.MgmtdGRPCPort = DefaultBeeGFSMgmtdGRPCPort
		}
		if addon.MetaPort == 0 {
			addon.MetaPort = DefaultBeeGFSMetaPort
		}
		if addon.StoragePort == 0 {
			addon.StoragePort = DefaultBeeGFSStoragePort
		}
		// v2 normalization: canonical role order (empty = the full v1 trio),
		// node id 0 → 1 (v1 compat), and the storage target list defaulting to
		// the single StorageDataDir — but only resolved lazily in the manager,
		// since StorageDataDir itself has no default here (same contract as the
		// other dirs: empty means "derive under base_dir").
		addon.Roles = orderBeeGFSServices(addon.Roles)
		if addon.NodeID == 0 {
			addon.NodeID = 1
		}
		c.Addons.BeeGFS[name] = addon
	}

	// Top-level addons defaults (VMCluster — VictoriaMetrics cluster edition).
	// Three images (vminsert / vmselect / vmstorage), five ports per instance
	// (3 HTTP + 2 internal TCP), host-network only on Linux. Same shape as
	// BeeGFS above: Listen defaults to 0.0.0.0, Roles ordered storage → insert
	// → select (startup dependency order), ReplicationFactor defaults to 1.
	// StorageNodes is deliberately NOT defaulted here — the CLI handler or
	// EffectiveStorageNodes() derives it at use time.
	for name, addon := range c.Addons.VMCluster {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.ContainerPrefix == "" {
			addon.ContainerPrefix = "pgcli-vmcluster" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.InsertImageTag == "" {
			addon.InsertImageTag = DefaultVMInsertImageTag
		}
		if addon.SelectImageTag == "" {
			addon.SelectImageTag = DefaultVMSelectImageTag
		}
		if addon.StorageImageTag == "" {
			addon.StorageImageTag = DefaultVMStorageImageTag
		}
		if addon.Listen == "" {
			addon.Listen = "0.0.0.0"
		}
		if addon.ReplicationFactor == 0 {
			addon.ReplicationFactor = 1
		}
		if addon.Retention == "" {
			addon.Retention = "1"
		}
		addon.Roles = orderVMClusterRoles(addon.Roles)
		c.Addons.VMCluster[name] = addon
	}

	// Top-level addons defaults (JuiceFS). These are host binaries, so the
	// paths are real directories on this machine, all rooted at
	// <base>/addon/juicefs/<name>/ — unlike BeeGFS, where dirs stay lazy and
	// the manager resolves them, the CLI needs the mount point present before
	// `juicefs mount`, so ApplyDefaults fills every path deterministically and
	// the installer MkdirAlls them. The external-backend values — S3Endpoint,
	// MetaDSN, AccessKey/SecretKey — are deliberately NOT defaulted: there is no
	// same-host auto-detection to fall back on, so the install must supply them.
	juicefsBaseDir := platform.DefaultConfigDir()
	if c.BaseDir != "" {
		juicefsBaseDir = c.BaseDir
	}
	for name, addon := range c.Addons.Juicefs {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.Version == "" {
			addon.Version = DefaultJuicefsVersion
		}
		addonDir := filepath.Join(juicefsBaseDir, "addon", "juicefs", name)
		if addon.Bucket == "" {
			addon.Bucket = "juicefs-" + name
		}
		if addon.MountPoint == "" {
			addon.MountPoint = filepath.Join(addonDir, "mnt")
		}
		if addon.CacheDir == "" {
			addon.CacheDir = filepath.Join(addonDir, "cache")
		}
		if addon.MountPidFile == "" {
			addon.MountPidFile = filepath.Join(addonDir, "mount.pid")
		}
		if addon.MountLogFile == "" {
			addon.MountLogFile = filepath.Join(addonDir, "mount.log")
		}
		c.Addons.Juicefs[name] = addon
	}

	// Top-level addons defaults (ZeroFS). Same host-path story as JuiceFS:
	// everything under <base>/addon/zerofs/<name>/. EncryptionPassword and the
	// external S3 endpoint/credentials are install-supplied, not defaulted here.
	// The 9P port is NOT drawn from the S3 pool — it is a filesystem protocol
	// port with its own default, and ZeroFS runs one server per addon name, so
	// collisions are caught by Validate across entries instead of auto-shifted.
	zerofsBaseDir := platform.DefaultConfigDir()
	if c.BaseDir != "" {
		zerofsBaseDir = c.BaseDir
	}
	for name, addon := range c.Addons.Zerofs {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.Version == "" {
			addon.Version = DefaultZerofsVersion
		}
		addonDir := filepath.Join(zerofsBaseDir, "addon", "zerofs", name)
		if addon.Bucket == "" {
			addon.Bucket = "zerofs-" + name
		}
		if addon.Listen == "" {
			addon.Listen = "127.0.0.1"
		}
		if addon.ListenPort == 0 {
			addon.ListenPort = DefaultZerofsPort
		}
		if addon.ConditionalPut == "" {
			addon.ConditionalPut = ZerofsConditionalPutAuto
		}
		if addon.CacheDir == "" {
			addon.CacheDir = filepath.Join(addonDir, "cache")
		}
		if addon.ServerCacheGB == 0 {
			addon.ServerCacheGB = 100
		}
		if addon.ConfigPath == "" {
			addon.ConfigPath = filepath.Join(addonDir, "config.toml")
		}
		if addon.MountPoint == "" {
			addon.MountPoint = filepath.Join(addonDir, "mnt")
		}
		if addon.ServerPidFile == "" {
			addon.ServerPidFile = filepath.Join(addonDir, "server.pid")
		}
		if addon.MountPidFile == "" {
			addon.MountPidFile = filepath.Join(addonDir, "mount.pid")
		}
		if addon.LogFile == "" {
			addon.LogFile = filepath.Join(addonDir, "zerofs.log")
		}
		c.Addons.Zerofs[name] = addon
	}

	// Apply defaults for DuckDB addon
	for name, addon := range c.Addons.Duckdb {
		if addon.Name == "" {
			addon.Name = name
		}
		if addon.DataDir == "" {
			duckdbBaseDir := platform.DefaultConfigDir()
			if c.BaseDir != "" {
				duckdbBaseDir = c.BaseDir
			}
			addon.DataDir = filepath.Join(duckdbBaseDir, "addon", "duckdb", name)
		}
		if addon.BinaryPath == "" {
			addon.BinaryPath = "/usr/local/bin/duckdb"
		}
		c.Addons.Duckdb[name] = addon
	}

	// Auto-assign host, SSH and PgBouncer ports for instances that don't have one set.
	c.autoAssignPorts()
}

// autoAssignPorts assigns sequential host ports (PG + SSH + PgBouncer) to
// instances that have HostPort=0 / SSHPort=0 / PgBouncer.HostPort=0.
// PG ports start at pg_start_port (default 35432), SSH ports start at
// pg_ssh_port (default 42201), PgBouncer ports start at pgbouncer_start_port
// (default 56432).
//
// Instances are processed in alphabetical order by name. Explicitly-set ports
// are respected and skipped. The "default" instance always gets the base port.
func (c *Config) autoAssignPorts() {
	names := make([]string, 0, len(c.Instances))
	for name := range c.Instances {
		names = append(names, name)
	}
	sort.Strings(names)

	// Put "default" first so it always gets the lowest port
	sorted := make([]string, 0, len(names))
	for _, n := range names {
		if n == "default" {
			sorted = append([]string{n}, sorted...)
		} else {
			sorted = append(sorted, n)
		}
	}

	// All platforms use host networking: PG from c.PGStartPort, SSH from c.PGSSHPort.
	pgBase := c.PGStartPort
	sshBase := c.PGSSHPort
	pbBase := c.PgBouncerStartPort
	etcdBase := c.EtcdStartPort
	pgdogBase := c.PgDogStartPort
	prBase := c.PostgrestStartPort

	// Probe already-used ports so multiple config files (or other services)
	// on the same host don't collide.
	usedPorts := platform.GetUsedPorts()

	// Collect explicitly-assigned ports from this config: a stopped instance's
	// port is not listening, so without this a new instance (e.g. a clone)
	// could steal it and break the original when it starts later.
	assignedPG := map[int]bool{}
	assignedSSH := map[int]bool{}
	assignedPB := map[int]bool{}
	for _, inst := range c.Instances {
		if inst.Podman.HostPort != 0 {
			assignedPG[inst.Podman.HostPort] = true
		}
		if inst.Podman.SSHPort != 0 {
			assignedSSH[inst.Podman.SSHPort] = true
		}
		if inst.Addons.PgBouncer != nil && inst.Addons.PgBouncer.HostPort != 0 {
			assignedPB[inst.Addons.PgBouncer.HostPort] = true
		}
	}
	// Collect top-level addon ports too
	for _, addon := range c.Addons.PgBouncer {
		if addon.HostPort != 0 {
			assignedPB[addon.HostPort] = true
		}
	}
	assignedEtcd := map[int]bool{}
	for _, addon := range c.Addons.Etcd {
		if addon.ClientPort != 0 {
			assignedEtcd[addon.ClientPort] = true
		}
		if addon.PeerPort != 0 {
			assignedEtcd[addon.PeerPort] = true
		}
	}
	assignedPgDog := map[int]bool{}
	for _, addon := range c.Addons.PgDog {
		if addon.HostPort != 0 {
			assignedPgDog[addon.HostPort] = true
		}
		if addon.OpenmetricsPort != 0 {
			assignedPgDog[addon.OpenmetricsPort] = true
		}
	}
	assignedPR := map[int]bool{}
	for _, inst := range c.Instances {
		if inst.Addons.Postgrest != nil && inst.Addons.Postgrest.HostPort != 0 {
			assignedPR[inst.Addons.Postgrest.HostPort] = true
		}
	}
	for _, addon := range c.Addons.Postgrest {
		if addon.HostPort != 0 {
			assignedPR[addon.HostPort] = true
		}
	}

	nextPG := pgBase
	nextSSH := sshBase
	nextPB := pbBase
	nextEtcd := etcdBase
	nextPgDog := pgdogBase
	nextPR := prBase
	for _, name := range sorted {
		inst := c.Instances[name]
		changed := false

		if inst.Podman.HostPort == 0 {
			for (usedPorts != nil && usedPorts[nextPG]) || assignedPG[nextPG] {
				nextPG++
			}
			inst.Podman.HostPort = nextPG
			nextPG++
			changed = true
		} else if inst.Podman.HostPort >= nextPG {
			nextPG = inst.Podman.HostPort + 1
		}

		if inst.Podman.SSHPort == 0 && sshBase > 0 {
			for (usedPorts != nil && usedPorts[nextSSH]) || assignedSSH[nextSSH] {
				nextSSH++
			}
			inst.Podman.SSHPort = nextSSH
			nextSSH++
			changed = true
		} else if inst.Podman.SSHPort >= nextSSH && sshBase > 0 {
			nextSSH = inst.Podman.SSHPort + 1
		}

		if inst.Addons.PgBouncer != nil && inst.Addons.PgBouncer.HostPort == 0 && pbBase > 0 {
			for (usedPorts != nil && usedPorts[nextPB]) || assignedPB[nextPB] {
				nextPB++
			}
			inst.Addons.PgBouncer.HostPort = nextPB
			nextPB++
			changed = true
		} else if inst.Addons.PgBouncer != nil && inst.Addons.PgBouncer.HostPort >= nextPB && pbBase > 0 {
			nextPB = inst.Addons.PgBouncer.HostPort + 1
		}

		if inst.Addons.Postgrest != nil && inst.Addons.Postgrest.HostPort == 0 && prBase > 0 {
			for (usedPorts != nil && usedPorts[nextPR]) || assignedPR[nextPR] {
				nextPR++
			}
			inst.Addons.Postgrest.HostPort = nextPR
			nextPR++
			changed = true
		} else if inst.Addons.Postgrest != nil && inst.Addons.Postgrest.HostPort >= nextPR && prBase > 0 {
			nextPR = inst.Addons.Postgrest.HostPort + 1
		}

		if changed {
			c.Instances[name] = inst
		}
	}

	// Allocate ports for top-level addons (remote PgBouncer pools)
	for name, addon := range c.Addons.PgBouncer {
		if addon.HostPort == 0 && pbBase > 0 {
			for (usedPorts != nil && usedPorts[nextPB]) || assignedPB[nextPB] {
				nextPB++
			}
			addon.HostPort = nextPB
			nextPB++
			c.Addons.PgBouncer[name] = addon
		} else if addon.HostPort >= nextPB && pbBase > 0 {
			nextPB = addon.HostPort + 1
		}
	}

	// Allocate ports for top-level addons (remote PostgREST APIs). Single port
	// per entry, same shape as the PgBouncer map above.
	for name, addon := range c.Addons.Postgrest {
		if addon.HostPort == 0 && prBase > 0 {
			for (usedPorts != nil && usedPorts[nextPR]) || assignedPR[nextPR] {
				nextPR++
			}
			addon.HostPort = nextPR
			nextPR++
			c.Addons.Postgrest[name] = addon
		} else if addon.HostPort >= nextPR && prBase > 0 {
			nextPR = addon.HostPort + 1
		}
	}

	// Allocate ports for top-level addons (etcd members, client + peer).
	// Both ports are taken together so a member's two ports never overlap
	// another member's or any other service's.
	for name, addon := range c.Addons.Etcd {
		if addon.ClientPort == 0 && etcdBase > 0 {
			for (usedPorts != nil && usedPorts[nextEtcd]) || assignedEtcd[nextEtcd] {
				nextEtcd++
			}
			clientPort := nextEtcd
			nextEtcd++
			for (usedPorts != nil && usedPorts[nextEtcd]) || assignedEtcd[nextEtcd] {
				nextEtcd++
			}
			peerPort := nextEtcd
			nextEtcd++

			addon.ClientPort = clientPort
			addon.PeerPort = peerPort
			c.Addons.Etcd[name] = addon
		} else if addon.ClientPort != 0 {
			if addon.ClientPort >= nextEtcd {
				nextEtcd = addon.ClientPort + 1
			}
			if addon.PeerPort >= nextEtcd {
				nextEtcd = addon.PeerPort + 1
			}
		}
	}

	// Allocate ports for top-level addons (pgdog proxies, client + openmetrics).
	// Same paired allocation as etcd: a proxy's two ports never overlap another
	// proxy's or any other service's.
	for name, addon := range c.Addons.PgDog {
		if addon.HostPort == 0 && pgdogBase > 0 {
			for (usedPorts != nil && usedPorts[nextPgDog]) || assignedPgDog[nextPgDog] {
				nextPgDog++
			}
			hostPort := nextPgDog
			nextPgDog++
			for (usedPorts != nil && usedPorts[nextPgDog]) || assignedPgDog[nextPgDog] {
				nextPgDog++
			}
			metricsPort := nextPgDog
			nextPgDog++

			addon.HostPort = hostPort
			addon.OpenmetricsPort = metricsPort
			c.Addons.PgDog[name] = addon
		} else if addon.HostPort != 0 {
			if addon.HostPort >= nextPgDog {
				nextPgDog = addon.HostPort + 1
			}
			if addon.OpenmetricsPort >= nextPgDog {
				nextPgDog = addon.OpenmetricsPort + 1
			}
		}
	}

	// Allocate ports for top-level addons (Patroni HA members). Each member has
	// two ports from two independent pools: the PG listen port (patroni_start_
	// port base, default 35532) and the REST API port (patroni_restapi_start_
	// port base, default 8008). The pools are separate because they live on
	// different well-known ranges; members are iterated by name so allocation is
	// deterministic across map-ordering. Only this host's members are in the
	// config; a cross-host peer's port is not allocated here (it is set by that
	// host's own `pg ha create`, and must match — see docs).
	patroniBase := c.PatroniStartPort
	patroniRestBase := c.PatroniRestapiStartPort
	patroniSSHBase := c.PatroniSSHStartPort
	assignedPatroniPG := map[int]bool{}
	assignedPatroniRest := map[int]bool{}
	assignedPatroniSSH := map[int]bool{}
	for _, cluster := range c.Addons.Patroni {
		for _, mb := range cluster.Members {
			if mb.HostPort != 0 {
				assignedPatroniPG[mb.HostPort] = true
			}
			if mb.RestapiPort != 0 {
				assignedPatroniRest[mb.RestapiPort] = true
			}
			if mb.SSHPort != 0 {
				assignedPatroniSSH[mb.SSHPort] = true
			}
		}
	}
	{
		scopes := make([]string, 0, len(c.Addons.Patroni))
		for scope := range c.Addons.Patroni {
			scopes = append(scopes, scope)
		}
		sort.Strings(scopes)
		nextPG := patroniBase
		nextRest := patroniRestBase
		nextSSH := patroniSSHBase
		for _, scope := range scopes {
			cluster := c.Addons.Patroni[scope]
			members := make([]string, 0, len(cluster.Members))
			for m := range cluster.Members {
				members = append(members, m)
			}
			sort.Strings(members)
			for _, m := range members {
				mb := cluster.Members[m]
				if mb.RemoteHost != "" {
					continue // remote member: ports are declared on its own host, never auto-assigned here
				}
				if mb.HostPort == 0 && patroniBase > 0 {
					for (usedPorts != nil && usedPorts[nextPG]) || assignedPatroniPG[nextPG] {
						nextPG++
					}
					mb.HostPort = nextPG
					nextPG++
				} else if mb.HostPort >= nextPG {
					nextPG = mb.HostPort + 1
				}
				if mb.RestapiPort == 0 && patroniRestBase > 0 {
					for (usedPorts != nil && usedPorts[nextRest]) || assignedPatroniRest[nextRest] {
						nextRest++
					}
					mb.RestapiPort = nextRest
					nextRest++
				} else if mb.RestapiPort >= nextRest {
					nextRest = mb.RestapiPort + 1
				}
				if mb.SSHPort == 0 && patroniSSHBase > 0 {
					for (usedPorts != nil && usedPorts[nextSSH]) || assignedPatroniSSH[nextSSH] {
						nextSSH++
					}
					mb.SSHPort = nextSSH
					nextSSH++
				} else if mb.SSHPort >= nextSSH {
					nextSSH = mb.SSHPort + 1
				}
				cluster.Members[m] = mb
			}
			c.Addons.Patroni[scope] = cluster
		}
	}

	// Allocate ports for top-level addons (HAProxy load balancers). Each
	// instance takes up to three ports from one pool (haproxy_start_port base,
	// default 5000): the rw listener, the ro listener (split mode only), and
	// the stats page. They are allocated together so one instance's ports never
	// overlap another's or any other service's. Names are sorted for a
	// deterministic, map-order-independent assignment. Conf-file instances are
	// skipped: their listeners live in the user's haproxy.cfg, so pgcli has no
	// port to name.
	haBase := c.HaProxyStartPort
	assignedHA := map[int]bool{}
	for _, addon := range c.Addons.HAProxy {
		if addon.ConfFile != "" {
			continue
		}
		for _, p := range []int{addon.WritePort, addon.ReadPort, addon.StatsPort} {
			if p != 0 {
				assignedHA[p] = true
			}
		}
	}
	{
		names := make([]string, 0, len(c.Addons.HAProxy))
		for name := range c.Addons.HAProxy {
			names = append(names, name)
		}
		sort.Strings(names)
		next := haBase
		// nextFree returns the next unused port at or above `next`, advancing
		// `next` past it. Only real (non-zero) ports are consumed.
		nextFree := func() int {
			for (usedPorts != nil && usedPorts[next]) || assignedHA[next] {
				next++
			}
			p := next
			next++
			return p
		}
		for _, name := range names {
			addon := c.Addons.HAProxy[name]
			if addon.ConfFile != "" {
				continue
			}
			if addon.WritePort == 0 && haBase > 0 {
				addon.WritePort = nextFree()
			} else if addon.WritePort >= next {
				next = addon.WritePort + 1
			}
			if addon.EffectiveMode() == "split" && addon.ReadPort == 0 && haBase > 0 {
				addon.ReadPort = nextFree()
			} else if addon.ReadPort >= next {
				next = addon.ReadPort + 1
			}
			if addon.StatsPort == 0 && haBase > 0 {
				addon.StatsPort = nextFree()
			} else if addon.StatsPort >= next {
				next = addon.StatsPort + 1
			}
			c.Addons.HAProxy[name] = addon
		}
	}

	// Allocate ports for top-level object-storage addons (MinIO, silo, rustfs).
	// Each instance takes two consecutive ports from ONE shared pool
	// (minio_start_port base, default 9000): the S3 API and the web console. The
	// three stores draw from the same cursor so they can coexist on a host
	// without colliding — reserved explicit ports from all three tables feed one
	// `assigned` set, and assignment runs the minio table first, then silo, then
	// rustfs, each in sorted order.
	minioBase := c.MinioStartPort
	assignedMinio := map[int]bool{}
	for _, addon := range c.Addons.Minio {
		for _, p := range []int{addon.APIPort, addon.ConsolePort} {
			if p != 0 {
				assignedMinio[p] = true
			}
		}
	}
	for _, addon := range c.Addons.Silo {
		for _, p := range []int{addon.APIPort, addon.ConsolePort} {
			if p != 0 {
				assignedMinio[p] = true
			}
		}
	}
	for _, addon := range c.Addons.Rustfs {
		for _, p := range []int{addon.APIPort, addon.ConsolePort} {
			if p != 0 {
				assignedMinio[p] = true
			}
		}
	}
	next := minioBase
	nextFree := func() int {
		for (usedPorts != nil && usedPorts[next]) || assignedMinio[next] {
			next++
		}
		p := next
		next++
		return p
	}
	// assign draws one table's ports from the shared cursor. The else-branches
	// (explicit port >= cursor → bump cursor past it) only fire within the
	// table being walked, which is why an explicit port far above the pool base
	// does not shift pairs assigned from the same table afterwards — the
	// behaviour the minio tests have always pinned, kept identical here.
	assign := func(apiPort, consolePort *int) {
		if *apiPort == 0 && minioBase > 0 {
			*apiPort = nextFree()
		} else if *apiPort >= next {
			next = *apiPort + 1
		}
		if *consolePort == 0 && minioBase > 0 {
			*consolePort = nextFree()
		} else if *consolePort >= next {
			next = *consolePort + 1
		}
	}
	{
		names := make([]string, 0, len(c.Addons.Minio))
		for name := range c.Addons.Minio {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			addon := c.Addons.Minio[name]
			assign(&addon.APIPort, &addon.ConsolePort)
			c.Addons.Minio[name] = addon
		}
	}
	{
		names := make([]string, 0, len(c.Addons.Silo))
		for name := range c.Addons.Silo {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			addon := c.Addons.Silo[name]
			assign(&addon.APIPort, &addon.ConsolePort)
			c.Addons.Silo[name] = addon
		}
	}
	{
		names := make([]string, 0, len(c.Addons.Rustfs))
		for name := range c.Addons.Rustfs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			addon := c.Addons.Rustfs[name]
			assign(&addon.APIPort, &addon.ConsolePort)
			c.Addons.Rustfs[name] = addon
		}
	}

	// Allocate ports for Redis addons from their own pool (redis_start_port,
	// default 6379) — one port per instance, unlike the object stores which
	// take an API+console pair from the shared minio pool. Redis is not
	// co-located with them by design (different role, different defaults), and
	// it has no second port unless TLS is added later. A native-cluster member
	// does implicitly open a cluster-bus port, but that is client port + 10000
	// derived by Redis itself at runtime — not something to allocate here — so
	// the pool stays one client port per instance even for cluster nodes.
	{
		redisBase := c.RedisStartPort
		assignedRedis := map[int]bool{}
		for _, addon := range c.Addons.Redis {
			if addon.Port != 0 {
				assignedRedis[addon.Port] = true
			}
		}
		next := redisBase
		nextFreeRedis := func() int {
			for (usedPorts != nil && usedPorts[next]) || assignedRedis[next] {
				next++
			}
			p := next
			next++
			return p
		}
		names := make([]string, 0, len(c.Addons.Redis))
		for name := range c.Addons.Redis {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			addon := c.Addons.Redis[name]
			if addon.Port == 0 && redisBase > 0 {
				addon.Port = nextFreeRedis()
			} else if addon.Port >= next {
				next = addon.Port + 1
			}
			c.Addons.Redis[name] = addon
		}
	}

	// Allocate ports for Predixy addons from their own pool (predixy_start_port,
	// default 7617 — the image's only EXPOSEd port). One port per instance: the
	// proxy has a single client-facing listener, no stats or console port.
	{
		predixyBase := c.PredixyStartPort
		assignedPredixy := map[int]bool{}
		for _, addon := range c.Addons.Predixy {
			if addon.Port != 0 {
				assignedPredixy[addon.Port] = true
			}
		}
		next := predixyBase
		nextFreePredixy := func() int {
			for (usedPorts != nil && usedPorts[next]) || assignedPredixy[next] {
				next++
			}
			p := next
			next++
			return p
		}
		names := make([]string, 0, len(c.Addons.Predixy))
		for name := range c.Addons.Predixy {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			addon := c.Addons.Predixy[name]
			if addon.Port == 0 && predixyBase > 0 {
				addon.Port = nextFreePredixy()
			} else if addon.Port >= next {
				next = addon.Port + 1
			}
			c.Addons.Predixy[name] = addon
		}
	}

	// Allocate ports for pgAdmin addons from their own pool (pgadmin_start_port,
	// default 5050 — pgAdmin's docs' example port). One port per instance: the
	// web UI has a single listener, no console or stats port. Shape is identical
	// to the Predixy block above (the newest, single-port precedent).
	{
		pgAdminBase := c.PgAdminStartPort
		assignedPgAdmin := map[int]bool{}
		for _, addon := range c.Addons.PgAdmin {
			if addon.HostPort != 0 {
				assignedPgAdmin[addon.HostPort] = true
			}
		}
		next := pgAdminBase
		nextFreePgAdmin := func() int {
			for (usedPorts != nil && usedPorts[next]) || assignedPgAdmin[next] {
				next++
			}
			p := next
			next++
			return p
		}
		names := make([]string, 0, len(c.Addons.PgAdmin))
		for name := range c.Addons.PgAdmin {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			addon := c.Addons.PgAdmin[name]
			if addon.HostPort == 0 && pgAdminBase > 0 {
				addon.HostPort = nextFreePgAdmin()
			} else if addon.HostPort >= next {
				next = addon.HostPort + 1
			}
			c.Addons.PgAdmin[name] = addon
		}
	}

	// Allocate ports for Grafana addons from their own pool
	// (grafana_start_port, default 3000 — Grafana's default web port). One
	// port per instance, same shape as the pgAdmin block above: a web UI
	// listener and nothing else. 3000 does not collide with any other pool's
	// base port on this host (see the *StartPort fields block near the top of
	// this file for the current bases) — so a Grafana instance and a pgAdmin
	// instance always end up on distinct ports even before the nextFree cursor
	// logic gets a chance to skip.
	{
		grafanaBase := c.GrafanaStartPort
		assignedGrafana := map[int]bool{}
		for _, addon := range c.Addons.Grafana {
			if addon.HostPort != 0 {
				assignedGrafana[addon.HostPort] = true
			}
		}
		next := grafanaBase
		nextFreeGrafana := func() int {
			for (usedPorts != nil && usedPorts[next]) || assignedGrafana[next] {
				next++
			}
			p := next
			next++
			return p
		}
		names := make([]string, 0, len(c.Addons.Grafana))
		for name := range c.Addons.Grafana {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			addon := c.Addons.Grafana[name]
			if addon.HostPort == 0 && grafanaBase > 0 {
				addon.HostPort = nextFreeGrafana()
			} else if addon.HostPort >= next {
				next = addon.HostPort + 1
			}
			c.Addons.Grafana[name] = addon
		}
	}

	// Allocate ports for Prometheus addons from their own pool
	// (prometheus_start_port, default 9090 — Prometheus's default web port). One
	// port per instance, same shape as the Grafana block above: a web UI
	// listener and nothing else. 9090 does not collide with any other pool's
	// base port on this host (see the *StartPort fields block near the top of
	// this file for the current bases) — so a Prometheus instance and a Grafana
	// instance always end up on distinct ports even before the nextFree cursor
	// logic gets a chance to skip.
	{
		prometheusBase := c.PrometheusStartPort
		assignedPrometheus := map[int]bool{}
		for _, addon := range c.Addons.Prometheus {
			if addon.HostPort != 0 {
				assignedPrometheus[addon.HostPort] = true
			}
		}
		next := prometheusBase
		nextFreePrometheus := func() int {
			for (usedPorts != nil && usedPorts[next]) || assignedPrometheus[next] {
				next++
			}
			p := next
			next++
			return p
		}
		names := make([]string, 0, len(c.Addons.Prometheus))
		for name := range c.Addons.Prometheus {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			addon := c.Addons.Prometheus[name]
			if addon.HostPort == 0 && prometheusBase > 0 {
				addon.HostPort = nextFreePrometheus()
			} else if addon.HostPort >= next {
				next = addon.HostPort + 1
			}
			c.Addons.Prometheus[name] = addon
		}
	}

	// Allocate ports for VictoriaMetrics addons from their own pool
	// (victoriametrics_start_port, default 8428 — VictoriaMetrics's default HTTP
	// port). One port per instance, same shape as the Prometheus block above.
	{
		vmBase := c.VictoriaMetricsStartPort
		assignedVM := map[int]bool{}
		for _, addon := range c.Addons.VictoriaMetrics {
			if addon.HostPort != 0 {
				assignedVM[addon.HostPort] = true
			}
		}
		next := vmBase
		nextFreeVM := func() int {
			for (usedPorts != nil && usedPorts[next]) || assignedVM[next] {
				next++
			}
			p := next
			next++
			return p
		}
		names := make([]string, 0, len(c.Addons.VictoriaMetrics))
		for name := range c.Addons.VictoriaMetrics {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			addon := c.Addons.VictoriaMetrics[name]
			if addon.HostPort == 0 && vmBase > 0 {
				addon.HostPort = nextFreeVM()
			} else if addon.HostPort >= next {
				next = addon.HostPort + 1
			}
			c.Addons.VictoriaMetrics[name] = addon
		}
	}

	// Allocate ports for VictoriaLogs addons from their own pool
	// (victorialogs_start_port, default 9428 — VictoriaLogs's default HTTP
	// port). One port per instance, same shape as the VictoriaMetrics block
	// above. 9428 shares no base with VictoriaMetrics (8428), vmcluster
	// (8480-8484) or Prometheus (9090).
	{
		vlBase := c.VictoriaLogsStartPort
		assignedVL := map[int]bool{}
		for _, addon := range c.Addons.VictoriaLogs {
			if addon.HostPort != 0 {
				assignedVL[addon.HostPort] = true
			}
		}
		next := vlBase
		nextFreeVL := func() int {
			for (usedPorts != nil && usedPorts[next]) || assignedVL[next] {
				next++
			}
			p := next
			next++
			return p
		}
		names := make([]string, 0, len(c.Addons.VictoriaLogs))
		for name := range c.Addons.VictoriaLogs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			addon := c.Addons.VictoriaLogs[name]
			if addon.HostPort == 0 && vlBase > 0 {
				addon.HostPort = nextFreeVL()
			} else if addon.HostPort >= next {
				next = addon.HostPort + 1
			}
			c.Addons.VictoriaLogs[name] = addon
		}
	}

	// Allocate ports for Alertmanager addons from their own pool
	// (alertmanager_start_port, default 9093 — Alertmanager's default web port).
	// One port per instance, same shape as the Prometheus block above. Unlike
	// Prometheus (which binds one web port), a stock Alertmanager would also open
	// its 9094 HA gossip port — pgcli disables that with --cluster.listen-address=
	// for single instances, so this pool really is one port per instance. 9093
	// shares no base with Prometheus (9090), Grafana (3000) or the rest.
	{
		alertmanagerBase := c.AlertmanagerStartPort
		assignedAlertmanager := map[int]bool{}
		for _, addon := range c.Addons.Alertmanager {
			if addon.HostPort != 0 {
				assignedAlertmanager[addon.HostPort] = true
			}
		}
		next := alertmanagerBase
		nextFreeAlertmanager := func() int {
			for (usedPorts != nil && usedPorts[next]) || assignedAlertmanager[next] {
				next++
			}
			p := next
			next++
			return p
		}
		names := make([]string, 0, len(c.Addons.Alertmanager))
		for name := range c.Addons.Alertmanager {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			addon := c.Addons.Alertmanager[name]
			if addon.HostPort == 0 && alertmanagerBase > 0 {
				addon.HostPort = nextFreeAlertmanager()
			} else if addon.HostPort >= next {
				next = addon.HostPort + 1
			}
			c.Addons.Alertmanager[name] = addon
		}
	}

	// Allocate ports for VMCluster addons from their own pool
	// (vmcluster_start_port, default 8480 — VictoriaMetrics cluster's
	// conventional insert HTTP port). Each instance gets 5 consecutive ports:
	//   +0  InsertPort         (vminsert HTTP — ingestion endpoint)
	//   +1  SelectPort         (vmselect HTTP — query endpoint)
	//   +2  StoragePort        (vmstorage HTTP — admin/metrics endpoint)
	//   +3  InternalInsertPort (vmstorage TCP — vminsert dials this)
	//   +4  InternalSelectPort (vmstorage TCP — vmselect dials this)
	// 5 consecutive ports per instance, same shape as BeeGFS's 4-port block.
	// The consecutive allocation matters because --storage-node references
	// the internal TCP ports explicitly, so the block must stay together.
	// 8480 shares no number with VictoriaMetrics (8428), Prometheus (9090),
	// Grafana (3000), pgAdmin (5050), or nginx (8080).
	{
		vmcBase := c.VMClusterStartPort
		assignedVMC := map[int]bool{}
		for _, addon := range c.Addons.VMCluster {
			for _, p := range []int{addon.InsertPort, addon.SelectPort, addon.StoragePort,
				addon.InternalInsertPort, addon.InternalSelectPort} {
				if p != 0 {
					assignedVMC[p] = true
				}
			}
		}
		next := vmcBase
		nextFreeVMC := func() int {
			// Find 5 consecutive free ports.
			candidate := next
			for {
				ok := true
				for i := range 5 {
					if (usedPorts != nil && usedPorts[candidate+i]) || assignedVMC[candidate+i] {
						ok = false
						candidate = candidate + i + 1
						break
					}
				}
				if ok {
					break
				}
			}
			p := candidate
			for i := range 5 {
				assignedVMC[p+i] = true
			}
			next = p + 5
			return p
		}
		names := make([]string, 0, len(c.Addons.VMCluster))
		for name := range c.Addons.VMCluster {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			addon := c.Addons.VMCluster[name]
			if addon.InsertPort == 0 && vmcBase > 0 {
				base := nextFreeVMC()
				addon.InsertPort = base
				addon.SelectPort = base + 1
				addon.StoragePort = base + 2
				addon.InternalInsertPort = base + 3
				addon.InternalSelectPort = base + 4
			} else if addon.InsertPort >= next {
				next = addon.InsertPort + 5
			}
			c.Addons.VMCluster[name] = addon
		}
	}

	// Allocate ports for Gitea addons — TWO pools, because a Gitea instance
	// listens twice at most: the web UI/API (gitea_start_port, default 3080)
	// and the built-in OpenSSH listener (gitea_ssh_start_port, default 2222).
	// 3080 rather than Gitea's stock 3000 because 3000 is the Grafana pool's
	// base here; a separate SSH cursor matters because host networking means
	// the listener binds HOST ports directly — the classic 222 mapping would
	// collide with a host sshd on 22's sibling conventions, and the pool skips
	// anything already taken. The SSH cursor only advances for instances that
	// ENABLED ssh (SSH=false allocates nothing; a stock-disabled Gitea is one
	// port, like every other web addon). Both blocks follow the alertmanager
	// shape one-for-one otherwise.
	{
		giteaBase := c.GiteaStartPort
		assignedGitea := map[int]bool{}
		for _, addon := range c.Addons.Gitea {
			if addon.HostPort != 0 {
				assignedGitea[addon.HostPort] = true
			}
		}
		next := giteaBase
		nextFreeGitea := func() int {
			for (usedPorts != nil && usedPorts[next]) || assignedGitea[next] {
				next++
			}
			p := next
			next++
			return p
		}
		names := make([]string, 0, len(c.Addons.Gitea))
		for name := range c.Addons.Gitea {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			addon := c.Addons.Gitea[name]
			if addon.HostPort == 0 && giteaBase > 0 {
				addon.HostPort = nextFreeGitea()
			} else if addon.HostPort >= next {
				next = addon.HostPort + 1
			}
			c.Addons.Gitea[name] = addon
		}
	}
	{
		// The image's s6 sshd always runs, so EVERY instance — SSH on or off —
		// needs its own port to park that sshd on (off 22, off the host sshd,
		// and off the other instances' parked sshds, since host networking puts
		// them all in one netns). The SSH pool therefore advances for every
		// gitea instance, not only the SSH-enabled ones; gc.SSH still decides
		// whether that port is a live clone listener or an idle one.
		sshBase := c.GiteaSSHStartPort
		assignedGiteaSSH := map[int]bool{}
		for _, addon := range c.Addons.Gitea {
			if addon.SSHPort != 0 {
				assignedGiteaSSH[addon.SSHPort] = true
			}
		}
		next := sshBase
		nextFreeGiteaSSH := func() int {
			for (usedPorts != nil && usedPorts[next]) || assignedGiteaSSH[next] {
				next++
			}
			p := next
			next++
			return p
		}
		names := make([]string, 0, len(c.Addons.Gitea))
		for name := range c.Addons.Gitea {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			addon := c.Addons.Gitea[name]
			if addon.SSHPort == 0 && sshBase > 0 {
				addon.SSHPort = nextFreeGiteaSSH()
			} else if addon.SSHPort >= next {
				next = addon.SSHPort + 1
			}
			c.Addons.Gitea[name] = addon
		}
	}

	// Allocate ports for nginx addons from their own pool (nginx_start_port,
	// default 8080). Each instance takes up to two ports: the HTTP listener and
	// the HTTPS listener (TLS only). The two ports are allocated together so
	// one instance's ports never overlap another's or any other service's.
	{
		nginxBASE := c.NginxStartPort
		assignedNginx := map[int]bool{}
		for _, addon := range c.Addons.Nginx {
			for _, p := range []int{addon.HTTPPort, addon.HTTPSPort} {
				if p != 0 {
					assignedNginx[p] = true
				}
			}
		}
		next := nginxBASE
		nextFreeNginx := func() int {
			for (usedPorts != nil && usedPorts[next]) || assignedNginx[next] {
				next++
			}
			p := next
			next++
			return p
		}
		names := make([]string, 0, len(c.Addons.Nginx))
		for name := range c.Addons.Nginx {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			addon := c.Addons.Nginx[name]
			if addon.HTTPPort == 0 && nginxBASE > 0 {
				addon.HTTPPort = nextFreeNginx()
			} else if addon.HTTPPort >= next {
				next = addon.HTTPPort + 1
			}
			if addon.TLS && addon.HTTPSPort == 0 && nginxBASE > 0 {
				addon.HTTPSPort = nextFreeNginx()
			} else if addon.HTTPSPort >= next {
				next = addon.HTTPSPort + 1
			}
			c.Addons.Nginx[name] = addon
		}
	}

	// Allocate ports for Garage addons from their own pool (garage_start_port,
	// default 3900 — Garage's upstream defaults). Unlike minio/silo/rustfs,
	// which take an API+console pair from the shared minio_start_port cursor,
	// each Garage instance takes FOUR consecutive ports: the S3 API, the node-
	// to-node RPC, the s3_web static-site endpoint, and the admin/metrics
	// endpoint. They are allocated together per instance so one instance's four
	// never overlap another's or the cursor's. Garage is a distinct role (its own
	// store on its own default base port), so it does not share the minio cursor.
	{
		garageBase := c.GarageStartPort
		assignedGarage := map[int]bool{}
		for _, addon := range c.Addons.Garage {
			for _, p := range []int{addon.APIPort, addon.RPCPort, addon.WebPort, addon.AdminPort} {
				if p != 0 {
					assignedGarage[p] = true
				}
			}
		}
		next := garageBase
		nextFreeGarage := func() int {
			for (usedPorts != nil && usedPorts[next]) || assignedGarage[next] {
				next++
			}
			p := next
			next++
			return p
		}
		assignGarage := func(port *int) {
			if *port == 0 && garageBase > 0 {
				*port = nextFreeGarage()
			} else if *port >= next {
				next = *port + 1
			}
		}
		names := make([]string, 0, len(c.Addons.Garage))
		for name := range c.Addons.Garage {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			addon := c.Addons.Garage[name]
			// API first, then RPC/Web/Admin, so an instance's four land
			// consecutively from the cursor.
			assignGarage(&addon.APIPort)
			assignGarage(&addon.RPCPort)
			assignGarage(&addon.WebPort)
			assignGarage(&addon.AdminPort)
			c.Addons.Garage[name] = addon
		}
	}

	// Allocate ports for SeaweedFS addons from their own pool
	// (seaweedfs_start_port, default 8400). Like Garage, each instance takes
	// FOUR consecutive ports — master, volume, filer, s3 — allocated together
	// per instance so one instance's four never overlap another's or the
	// cursor's. SeaweedFS differs from Garage in one structural way: every
	// role ALSO implicitly opens a gRPC port at http+10000 (verified on 4.48
	// for all four roles), which is not a knob to assign but must be recorded
	// as taken so the cursor never walks an instance into one of its own
	// siblings' gRPC ports. The +10000 of the NEXT not-yet-allocated port is
	// likewise marked used as the cursor advances, to stop a later instance's
	// http ports landing in an earlier instance's gRPC range.
	{
		seaweedfsBase := c.SeaweedfsStartPort
		assignedSeaweedfs := map[int]bool{}
		markSeaweedfsUsed := func(p int) { assignedSeaweedfs[p] = true }
		for _, addon := range c.Addons.Seaweedfs {
			for _, p := range []int{addon.Port, addon.VolumePort, addon.FilerPort, addon.S3Port} {
				if p != 0 {
					markSeaweedfsUsed(p)
					markSeaweedfsUsed(SeaweedfsGRPCPort(p))
				}
			}
		}
		next := seaweedfsBase
		nextFreeSeaweedfs := func() int {
			for (usedPorts != nil && usedPorts[next]) || assignedSeaweedfs[next] || assignedSeaweedfs[SeaweedfsGRPCPort(next)] {
				next++
			}
			p := next
			next++
			markSeaweedfsUsed(p)
			markSeaweedfsUsed(SeaweedfsGRPCPort(p))
			return p
		}
		assignSeaweedfs := func(port *int) {
			if *port == 0 && seaweedfsBase > 0 {
				*port = nextFreeSeaweedfs()
			} else if *port >= next {
				next = *port + 1
			}
		}
		names := make([]string, 0, len(c.Addons.Seaweedfs))
		for name := range c.Addons.Seaweedfs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			addon := c.Addons.Seaweedfs[name]
			// master first, then volume/filer/s3, so an instance's four land
			// consecutively from the cursor.
			assignSeaweedfs(&addon.Port)
			assignSeaweedfs(&addon.VolumePort)
			assignSeaweedfs(&addon.FilerPort)
			assignSeaweedfs(&addon.S3Port)
			c.Addons.Seaweedfs[name] = addon
		}
	}

	// No BeeGFS port pool, deliberately. BeeGFS daemons bind their ports on the
	// host network, and only the mgmtd pair (8008/8010) accepts overrides
	// (--beemsg-port/--grpc-port); the meta and storage services (8005/8003)
	// have NO supported listen-port knob in 8.4 (verified on vm01: connPortsList
	// and connTcpPort are rejected outright, and the connMetaPort key is
	// accepted but breaks node registration). Two filesystems on one host would
	// therefore always collide on 8005/8003 regardless of allocation, so v1 is
	// one filesystem per host on the stock ports — the ApplyDefaults walk above
	// pins the four port fields, and a second map entry is rejected at install.
}
