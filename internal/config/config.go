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
	BaseDir     string                    `yaml:"base_dir,omitempty"`
	Network     string                    `yaml:"network,omitempty"` // shared podman network name, persisted at top level
	Namespace   string                    `yaml:"namespace,omitempty"` // container name namespace, empty = disabled (default)
	PGStartPort int                       `yaml:"pg_start_port,omitempty"` // starting PG host port, default 35432
	PGSSHPort   int                       `yaml:"pg_ssh_port,omitempty"` // starting SSH host port, default 42201
	Postgres    PostgresConfig            `yaml:"postgres"`
	Podman      PodmanConfig              `yaml:"podman"`
	PITR        PITRConfig                `yaml:"pitr"`
	Logging     LoggingConfig             `yaml:"logging"`
	Backup      BackupConfig              `yaml:"backup"`
	Instances   map[string]InstanceConfig `yaml:"instances"`

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
}

// Default returns a Config populated with default values.
func Default() *Config {
	return &Config{
		BaseDir:     "", // empty means use platform default
		PGStartPort: 35432,
		PGSSHPort:   42201,
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
	BaseDir     string                    `yaml:"base_dir,omitempty"`
	Network     string                    `yaml:"network,omitempty"`
	Namespace   string                    `yaml:"namespace,omitempty"`
	PGStartPort int                       `yaml:"pg_start_port,omitempty"`
	PGSSHPort   int                       `yaml:"pg_ssh_port,omitempty"`
	Logging     LoggingConfig             `yaml:"logging"`
	Backup      BackupConfig              `yaml:"backup"`
	Instances   map[string]InstanceConfig `yaml:"instances"`
}

// Display returns a view of the config suitable for display or saving.
func (c *Config) Display() displayConfig {
	return displayConfig{
		BaseDir:     c.BaseDir,
		Network:     c.Podman.Network,
		Namespace:   c.Namespace,
		PGStartPort: c.PGStartPort,
		PGSSHPort:   c.PGSSHPort,
		Logging:     c.Logging,
		Backup:      c.Backup,
		Instances:   c.Instances,
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

// applyDefaults fills zero-value fields with their defaults.
func (c *Config) ApplyDefaults() {
	d := Default()

	// Namespace / port bases
	if c.PGStartPort == 0 {
		c.PGStartPort = d.PGStartPort
	}
	if c.PGSSHPort == 0 {
		c.PGSSHPort = d.PGSSHPort
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
		c.Instances[name] = inst
	}

	// Auto-assign host and SSH ports for instances that don't have one set.
	c.autoAssignPorts()
}

// autoAssignPorts assigns sequential host ports (PG + SSH) to instances that
// have HostPort=0 / SSHPort=0. PG ports start at pg_start_port (default
// 35432), SSH ports start at pg_ssh_port (default 42201).
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

	// Probe already-used ports so multiple config files (or other services)
	// on the same host don't collide.
	usedPorts := platform.GetUsedPorts()

	// Collect explicitly-assigned ports from this config: a stopped instance's
	// port is not listening, so without this a new instance (e.g. a clone)
	// could steal it and break the original when it starts later.
	assignedPG := map[int]bool{}
	assignedSSH := map[int]bool{}
	for _, inst := range c.Instances {
		if inst.Podman.HostPort != 0 {
			assignedPG[inst.Podman.HostPort] = true
		}
		if inst.Podman.SSHPort != 0 {
			assignedSSH[inst.Podman.SSHPort] = true
		}
	}

	nextPG := pgBase
	nextSSH := sshBase
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

		if changed {
			c.Instances[name] = inst
		}
	}
}
