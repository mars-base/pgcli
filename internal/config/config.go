// Package config provides loading, validation, merging, and saving of pgcli configuration files.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

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
}

// AddonsConfig tracks optional sidecar components attached to an instance.
type AddonsConfig struct {
	PgBouncer *PgBouncerConfig `yaml:"pgbouncer,omitempty"`
}

// TopAddonsConfig holds top-level (cross-instance) addon configurations.
// These are for addons targeting PG instances NOT managed locally by pgcli
// (e.g. a PgBouncer fronting a remote database via --dsn), and for shared
// infrastructure addons like etcd.
type TopAddonsConfig struct {
	PgBouncer map[string]PgBouncerConfig      `yaml:"pgbouncer,omitempty"`
	Etcd      map[string]EtcdConfig           `yaml:"etcd,omitempty"`
	PgDog     map[string]PgDogConfig          `yaml:"pgdog,omitempty"`
	Patroni   map[string]PatroniClusterConfig `yaml:"patroni,omitempty"`
}

// PgBouncerConfig holds the per-instance PgBouncer connection pooler settings.
type PgBouncerConfig struct {
	ContainerName   string `yaml:"container_name"`              // e.g. pgcli-pgbouncer-ns-<instance>
	ImageTag        string `yaml:"image_tag,omitempty"`         // edoburu/pgbouncer:latest (default)
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
	HostPort      int    `yaml:"host_port,omitempty"`      // PG listen port, 35532+ auto-assigned per host
	RestapiPort   int    `yaml:"restapi_port,omitempty"`   // Patroni REST API port, 8008+ auto-assigned per host
	DataDir       string `yaml:"data_dir,omitempty"`       // host dir bound to /var/lib/postgresql (PGDATA under it)
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

	Members map[string]PatroniMemberConfig `yaml:"members,omitempty"` // member name -> config (this host's only)
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
	return nil
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
				pb.ImageTag = "edoburu/pgbouncer:latest"
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
		c.Instances[name] = inst
	}

	// Top-level addons defaults (remote PgBouncer pools)
	for name, addon := range c.Addons.PgBouncer {
		if addon.ContainerName == "" {
			addon.ContainerName = "pgcli-pgbouncer" + nsSuffix(c.Namespace) + "-" + name
		}
		if addon.ImageTag == "" {
			addon.ImageTag = "edoburu/pgbouncer:latest"
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
			for member, mb := range cluster.Members {
				if mb.ContainerName == "" {
					mb.ContainerName = "pgcli-patroni" + nsSuffix(c.Namespace) + "-" + scope + "-" + member
				}
				if mb.ImageTag == "" {
					mb.ImageTag = "ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5"
				}
				if mb.DataDir == "" {
					mb.DataDir = filepath.Join(patroniBaseDir, "addon", "patroni", nsScope, member)
				}
				cluster.Members[member] = mb
			}
			c.Addons.Patroni[scope] = cluster
		}
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

	nextPG := pgBase
	nextSSH := sshBase
	nextPB := pbBase
	nextEtcd := etcdBase
	nextPgDog := pgdogBase
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
	assignedPatroniPG := map[int]bool{}
	assignedPatroniRest := map[int]bool{}
	for _, cluster := range c.Addons.Patroni {
		for _, mb := range cluster.Members {
			if mb.HostPort != 0 {
				assignedPatroniPG[mb.HostPort] = true
			}
			if mb.RestapiPort != 0 {
				assignedPatroniRest[mb.RestapiPort] = true
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
		for _, scope := range scopes {
			cluster := c.Addons.Patroni[scope]
			members := make([]string, 0, len(cluster.Members))
			for m := range cluster.Members {
				members = append(members, m)
			}
			sort.Strings(members)
			for _, m := range members {
				mb := cluster.Members[m]
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
				cluster.Members[m] = mb
			}
			c.Addons.Patroni[scope] = cluster
		}
	}
}
