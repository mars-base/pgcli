package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/podman"
	"github.com/mars-base/pgcli/internal/rabbitmq"
)

// resolveRabbitMQInstance loads the config, applies defaults and returns the
// named RabbitMQ instance.
func resolveRabbitMQInstance(name string) (*config.Config, string, config.RabbitMQConfig, error) {
	c, path, err := loadAddonCfg()
	if err != nil {
		return nil, "", config.RabbitMQConfig{}, err
	}
	c.ApplyDefaults()
	rc, ok := c.Addons.RabbitMQ[name]
	if !ok {
		return nil, "", config.RabbitMQConfig{}, fmt.Errorf("rabbitmq %q not found (see 'pg addon list')", name)
	}
	return c, path, rc, nil
}

func rabbitMQFlagName(cmd *cobra.Command) string {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "rabbitmq"
	}
	return name
}

// rabbitMQAdminPassword reuses a kept secret (same-name reinstall after
// `remove` without --clean-data), otherwise generates a fresh password.
func rabbitMQAdminPassword(secretPath string) (string, error) {
	data, err := os.ReadFile(secretPath)
	switch {
	case err == nil:
		if pw := strings.TrimSpace(string(data)); pw != "" {
			return pw, nil
		}
	case !os.IsNotExist(err):
		return "", err
	}
	return generatePassword(20)
}

// rabbitMQDirEmpty reports whether path is absent or has no entries. A new
// clustered member must not inherit Mnesia state from an older standalone
// instance: peer discovery only runs on an empty data dir.
func rabbitMQDirEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

// rabbitMQInstallOptions captures the Changed bits so a --force re-install
// keeps stored values for flags that were not passed.
type rabbitMQInstallOptions struct {
	listenSet            bool
	amqpPortSet          bool
	managementPortSet    bool
	tlsPortSet           bool
	tlsSet               bool
	tlsCertSet           bool
	tlsKeySet            bool
	adminUserSet         bool
	adminPasswordSet     bool
	adminPasswordFileSet bool
	versionSet           bool
	dataDirSet           bool
	settingsSet          bool
	clusterSet           bool
	nodeNameSet          bool
	etcdSet              bool
	etcdEndpointsSet     bool
	erlangCookieFileSet  bool
	secretsFromEtcdSet   bool
}

func readRabbitMQInstallOptions(fs *pflag.FlagSet) rabbitMQInstallOptions {
	return rabbitMQInstallOptions{
		listenSet:            fs.Changed("listen"),
		amqpPortSet:          fs.Changed("amqp-port"),
		managementPortSet:    fs.Changed("management-port"),
		tlsPortSet:           fs.Changed("tls-port"),
		tlsSet:               fs.Changed("tls"),
		tlsCertSet:           fs.Changed("tls-cert"),
		tlsKeySet:            fs.Changed("tls-key"),
		adminUserSet:         fs.Changed("admin-user"),
		adminPasswordSet:     fs.Changed("admin-password"),
		adminPasswordFileSet: fs.Changed("admin-password-file"),
		versionSet:           fs.Changed("version"),
		dataDirSet:           fs.Changed("data-dir"),
		settingsSet:          fs.Changed("config-option"),
		clusterSet:           fs.Changed("cluster"),
		nodeNameSet:          fs.Changed("node-name"),
		etcdSet:              fs.Changed("etcd"),
		etcdEndpointsSet:     fs.Changed("etcd-endpoints"),
		erlangCookieFileSet:  fs.Changed("erlang-cookie-file"),
		secretsFromEtcdSet:   fs.Changed("secrets-from-etcd"),
	}
}

// readRabbitMQSecretFile reads a shared secret from disk. Only trailing
// newlines are stripped so a password may contain interior spaces; the value
// is validated by the caller's kind.
func readRabbitMQSecretFile(path, kind string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s file %s: %w", kind, path, err)
	}
	value := strings.TrimRight(string(data), "\r\n")
	if value == "" {
		return "", fmt.Errorf("%s file %s is empty", kind, path)
	}
	if strings.ContainsRune(value, '\x00') {
		return "", fmt.Errorf("%s file %s must not contain NUL bytes", kind, path)
	}
	return value, nil
}

func rabbitMQSecretPath(flag, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("--%s must not be empty", flag)
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolving --%s %s: %w", flag, value, err)
	}
	return abs, nil
}

func rabbitMQGenerateErlangCookie() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating Erlang cookie: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func writeRabbitMQSecretFile(path, value string, force bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating directory for %s: %w", path, err)
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s already exists (use --force to overwrite)", path)
		}
		return fmt.Errorf("creating %s: %w", path, err)
	}
	if _, err := f.WriteString(value + "\n"); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	// The open mode only applies on creation; --force may have reused a file
	// with broader permissions.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("setting permissions on %s: %w", path, err)
	}
	return nil
}

func resolveRabbitMQSecretEtcdEndpoints(members, explicit []string) ([]string, error) {
	if len(explicit) > 0 {
		return config.NormalizeRabbitMQEtcdEndpoints(explicit)
	}
	if len(members) == 0 {
		return nil, nil
	}
	c, _, err := loadAddonCfg()
	if err != nil {
		return nil, err
	}
	return resolveRabbitMQEtcdEndpoints(c, members, explicit)
}

// readRabbitMQSecretsFromEtcd loads the shared cookie and admin password that
// "secret generate" published. Both are cluster-level values: clients never
// need them to connect.
func readRabbitMQSecretsFromEtcd(endpoints []string, cluster string) (string, string, error) {
	cookieKey := rabbitmq.ErlangCookieKey(cluster)
	cookie, found, err := podman.EtcdGetValue(endpoints, cookieKey)
	if err != nil {
		return "", "", fmt.Errorf("reading Erlang cookie from etcd: %w", err)
	}
	if !found {
		return "", "", fmt.Errorf("etcd key %s not found; run 'pg addon rabbitmq secret generate --cluster %s' on the management host first", cookieKey, cluster)
	}
	if err := config.ValidateRabbitMQErlangCookie(cookie); err != nil {
		return "", "", fmt.Errorf("etcd key %s: %w", cookieKey, err)
	}
	passwordKey := rabbitmq.AdminPasswordKey(cluster)
	password, found, err := podman.EtcdGetValue(endpoints, passwordKey)
	if err != nil {
		return "", "", fmt.Errorf("reading admin password from etcd: %w", err)
	}
	if !found {
		return "", "", fmt.Errorf("etcd key %s not found; run 'pg addon rabbitmq secret generate --cluster %s' on the management host first", passwordKey, cluster)
	}
	if strings.ContainsAny(password, "\r\n") {
		return "", "", fmt.Errorf("etcd key %s must not contain line breaks", passwordKey)
	}
	return cookie, password, nil
}

func runAddonRabbitMQSecretGenerate(cmd *cobra.Command) error {
	cluster, err := cmd.Flags().GetString("cluster")
	if err != nil {
		return err
	}
	if !config.ValidRabbitMQClusterName(cluster) {
		return fmt.Errorf("--cluster %q is not a valid RabbitMQ cluster name (letters, digits, '_' and '-', starting with a letter or digit, max 48 chars)", cluster)
	}
	members, err := cmd.Flags().GetStringSlice("etcd")
	if err != nil {
		return err
	}
	explicit, err := cmd.Flags().GetStringSlice("etcd-endpoints")
	if err != nil {
		return err
	}
	endpoints, err := resolveRabbitMQSecretEtcdEndpoints(members, explicit)
	if err != nil {
		return err
	}
	if len(endpoints) == 0 {
		return fmt.Errorf("--etcd <member,...> or --etcd-endpoints <host:port,...> is required so the secrets can be published to etcd")
	}
	cookieFlag, err := cmd.Flags().GetString("cookie-file")
	if err != nil {
		return err
	}
	passwordFlag, err := cmd.Flags().GetString("admin-password-file")
	if err != nil {
		return err
	}
	force, err := cmd.Flags().GetBool("force")
	if err != nil {
		return err
	}
	keepCookie, err := cmd.Flags().GetBool("keep-cookie")
	if err != nil {
		return err
	}
	if keepCookie && !force {
		return fmt.Errorf("--keep-cookie requires --force")
	}
	cookiePath, err := rabbitMQSecretPath("cookie-file", cookieFlag)
	if err != nil {
		return err
	}
	passwordPath, err := rabbitMQSecretPath("admin-password-file", passwordFlag)
	if err != nil {
		return err
	}
	if cookiePath == passwordPath {
		return fmt.Errorf("--cookie-file and --admin-password-file must be different paths")
	}
	cookieKey := rabbitmq.ErlangCookieKey(cluster)
	passwordKey := rabbitmq.AdminPasswordKey(cluster)
	if !force {
		for _, path := range []string{cookiePath, passwordPath} {
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists (use --force to overwrite)", path)
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("checking %s: %w", path, err)
			}
		}
		for _, key := range []string{cookieKey, passwordKey} {
			_, found, err := podman.EtcdGetValue(endpoints, key)
			if err != nil {
				return fmt.Errorf("checking etcd key %s: %w", key, err)
			}
			if found {
				return fmt.Errorf("etcd key %s already exists (use --force to overwrite)", key)
			}
		}
	}
	cookie := ""
	if keepCookie {
		foundCookie, found, err := podman.EtcdGetValue(endpoints, cookieKey)
		if err != nil {
			return fmt.Errorf("reading etcd key %s: %w", cookieKey, err)
		}
		if !found {
			return fmt.Errorf("etcd key %s not found; omit --keep-cookie to generate a new cookie", cookieKey)
		}
		if err := config.ValidateRabbitMQErlangCookie(foundCookie); err != nil {
			return fmt.Errorf("etcd key %s: %w", cookieKey, err)
		}
		cookie = foundCookie
	} else {
		cookie, err = rabbitMQGenerateErlangCookie()
		if err != nil {
			return err
		}
	}
	password, err := generatePassword(24)
	if err != nil {
		return err
	}
	if err := writeRabbitMQSecretFile(cookiePath, cookie, force); err != nil {
		return err
	}
	if err := writeRabbitMQSecretFile(passwordPath, password, force); err != nil {
		return err
	}
	values := map[string]string{passwordKey: password}
	if !keepCookie {
		values[cookieKey] = cookie
	}
	if err := podman.EtcdPutValues(endpoints, values); err != nil {
		return fmt.Errorf("writing secrets to etcd: %w", err)
	}
	fmt.Println("Generated shared cluster secrets (values are not printed):")
	if keepCookie {
		fmt.Printf("  Erlang cookie:  %s (kept; unchanged)\n", cookiePath)
	} else {
		fmt.Printf("  Erlang cookie:  %s\n", cookiePath)
	}
	fmt.Printf("  Admin password: %s\n", passwordPath)
	fmt.Printf("  Etcd:           %s\n", strings.Join(endpoints, ", "))
	fmt.Printf("  Keys:           %s\n", cookieKey)
	fmt.Printf("                  %s\n\n", passwordKey)
	fmt.Println("Install each member with --secrets-from-etcd and the same --cluster and etcd endpoints.")
	return nil
}

// resolveRabbitMQEtcdEndpoints turns --etcd-endpoints (external, preferred)
// or --etcd (local addon member names) into a normalized endpoint list.
func resolveRabbitMQEtcdEndpoints(c *config.Config, members, explicit []string) ([]string, error) {
	if len(explicit) > 0 {
		return config.NormalizeRabbitMQEtcdEndpoints(explicit)
	}
	if len(members) == 0 {
		return nil, nil
	}
	endpoints := make([]string, 0, len(members))
	for _, member := range members {
		member = strings.TrimSpace(member)
		ec, ok := c.Addons.Etcd[member]
		if !ok {
			return nil, fmt.Errorf("etcd member %q not found (see 'pg addon list')", member)
		}
		endpoints = append(endpoints, net.JoinHostPort(ec.AdvertiseAddr(), strconv.Itoa(ec.ClientPort)))
	}
	return config.NormalizeRabbitMQEtcdEndpoints(endpoints)
}

// preflightRabbitMQEtcdEndpoints fails before any container work when none of
// the configured etcd endpoints answers. Peer discovery needs etcd for the
// first boot; an already-formed node does not consult it on restart.
func preflightRabbitMQEtcdEndpoints(endpoints []string) error {
	if len(endpoints) == 0 {
		return fmt.Errorf("cluster mode requires at least one etcd endpoint")
	}
	var failures []string
	for _, endpoint := range endpoints {
		conn, err := net.DialTimeout("tcp", endpoint, 3*time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		failures = append(failures, fmt.Sprintf("%s (%v)", endpoint, err))
	}
	return fmt.Errorf("no etcd endpoint is reachable: %s — start etcd or fix --etcd/--etcd-endpoints", strings.Join(failures, ", "))
}

// checkRabbitMQTopologyImmutable rejects changes to the identity fields that
// decide cluster membership. Etcd endpoints may change; the admin password may
// be rotated (but must be rotated on every member).
func checkRabbitMQTopologyImmutable(stored, next config.RabbitMQConfig) error {
	if stored.Cluster != next.Cluster {
		return fmt.Errorf("rabbitmq %q is installed as %s; changing --cluster is not supported — remove it with --clean-data and install a new instance",
			next.Name, rabbitMQDescribeMode(stored))
	}
	if stored.NodeName != "" && stored.NodeName != next.NodeName {
		return fmt.Errorf("rabbitmq %q is installed as node %q; changing --node-name is not supported", next.Name, stored.NodeName)
	}
	if stored.ErlangCookie != "" && stored.ErlangCookie != next.ErlangCookie && next.ErlangCookie != "" {
		return fmt.Errorf("rabbitmq %q has a stored Erlang cookie; changing it is not supported — every member must keep the same cookie", next.Name)
	}
	return nil
}

func rabbitMQDescribeMode(rc config.RabbitMQConfig) string {
	if rc.Cluster == "" {
		return "a standalone node"
	}
	return fmt.Sprintf("cluster %q", rc.Cluster)
}

// parseRabbitMQConfigOptions parses repeatable KEY=VALUE overrides for
// rabbitmq.conf.
func parseRabbitMQConfigOptions(options []string) (map[string]string, error) {
	settings := make(map[string]string, len(options))
	for _, opt := range options {
		key, value, ok := strings.Cut(opt, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --config-option %q: use KEY=VALUE, e.g. 'log.file.level=debug'", opt)
		}
		settings[key] = value
	}
	if err := config.ValidateRabbitMQSettings(settings); err != nil {
		return nil, err
	}
	return settings, nil
}

// applyRabbitMQInstallFlags overlays the explicitly set install flags on k.
func applyRabbitMQInstallFlags(k *config.RabbitMQConfig, fs *pflag.FlagSet, opts rabbitMQInstallOptions) error {
	if opts.adminPasswordSet && opts.adminPasswordFileSet {
		return fmt.Errorf("pass either --admin-password or --admin-password-file, not both")
	}
	if opts.secretsFromEtcdSet {
		fromEtcd, _ := fs.GetBool("secrets-from-etcd")
		if fromEtcd && (opts.erlangCookieFileSet || opts.adminPasswordFileSet) {
			return fmt.Errorf("--secrets-from-etcd cannot be combined with --erlang-cookie-file or --admin-password-file")
		}
		if fromEtcd && opts.adminPasswordSet {
			return fmt.Errorf("--secrets-from-etcd cannot be combined with --admin-password")
		}
	}
	if opts.clusterSet {
		k.Cluster, _ = fs.GetString("cluster")
	}
	if opts.nodeNameSet {
		k.NodeName, _ = fs.GetString("node-name")
	}
	if k.Cluster != "" && opts.adminPasswordSet {
		return fmt.Errorf("cluster mode rejects --admin-password; use --secrets-from-etcd or --admin-password-file so every member shares the same credential")
	}
	if opts.erlangCookieFileSet {
		path, _ := fs.GetString("erlang-cookie-file")
		cookie, err := readRabbitMQSecretFile(path, "Erlang cookie")
		if err != nil {
			return fmt.Errorf("--erlang-cookie-file: %w", err)
		}
		if err := config.ValidateRabbitMQErlangCookie(cookie); err != nil {
			return fmt.Errorf("--erlang-cookie-file: %w", err)
		}
		k.ErlangCookie = cookie
	}
	if opts.adminPasswordFileSet {
		path, _ := fs.GetString("admin-password-file")
		password, err := readRabbitMQSecretFile(path, "admin password")
		if err != nil {
			return fmt.Errorf("--admin-password-file: %w", err)
		}
		if strings.ContainsAny(password, "\r\n") {
			return fmt.Errorf("--admin-password-file %s must not contain line breaks", path)
		}
		k.AdminPassword = password
	}
	if opts.versionSet {
		k.Version, _ = fs.GetString("version")
	}
	if k.Version == "" {
		k.Version = config.DefaultRabbitMQVersion
	}
	tag, ok := config.RabbitMQImageTagForVersion(k.Version)
	if !ok {
		return fmt.Errorf("RabbitMQ %q is not available (supported: %s; pass the exact upstream tag, e.g. 4.4.0)", k.Version, config.DefaultRabbitMQVersion)
	}
	k.ImageTag = tag
	if opts.listenSet {
		k.Listen, _ = fs.GetString("listen")
	}
	if opts.amqpPortSet {
		k.AMQPPort, _ = fs.GetInt("amqp-port")
	}
	if opts.managementPortSet {
		k.ManagementPort, _ = fs.GetInt("management-port")
	}
	if opts.adminUserSet {
		k.AdminUser, _ = fs.GetString("admin-user")
	}
	if opts.adminPasswordSet {
		k.AdminPassword, _ = fs.GetString("admin-password")
	}
	if opts.dataDirSet {
		k.DataDir, _ = fs.GetString("data-dir")
	}
	if opts.settingsSet {
		options, _ := fs.GetStringArray("config-option")
		settings, err := parseRabbitMQConfigOptions(options)
		if err != nil {
			return err
		}
		k.Settings = settings
	}

	if opts.tlsCertSet || opts.tlsKeySet {
		cert, _ := fs.GetString("tls-cert")
		key, _ := fs.GetString("tls-key")
		if cert == "" || key == "" {
			return fmt.Errorf("--tls-cert and --tls-key must be supplied together")
		}
		k.CertFile = cert
		k.KeyFile = key
		k.TLS = true
	}
	if opts.tlsSet {
		tls, _ := fs.GetBool("tls")
		k.TLS = tls
		if !tls {
			k.CertFile = ""
			k.KeyFile = ""
		}
	}
	if opts.tlsPortSet {
		k.AMQPTLSPort, _ = fs.GetInt("tls-port")
	}
	if !k.TLS {
		k.AMQPTLSPort = 0
		k.ManagementTLSPort = 0
	}
	return nil
}

// validateRabbitMQBYOTLS fails before any image pull or container work when the
// operator-supplied certificate is unusable or does not cover the bind address.
func validateRabbitMQBYOTLS(k config.RabbitMQConfig) error {
	if !rabbitmq.BYOTLS(k) {
		return nil
	}
	ci, err := podman.ValidateBYOCert(k.CertFile, k.KeyFile)
	if err != nil {
		return fmt.Errorf("--tls-cert/--tls-key: %w", err)
	}
	if ip := net.ParseIP(k.Listen); ip != nil && !ip.IsUnspecified() && !podman.CertCoversHost(ci, k.Listen) {
		return fmt.Errorf("TLS certificate %s does not cover the listen address %s; add it as a SAN or use --listen 0.0.0.0", k.CertFile, k.Listen)
	}
	return nil
}

// --- install / lifecycle ----------------------------------------------------

func runAddonInstallRabbitMQ(cmd *cobra.Command) error {
	name := rabbitMQFlagName(cmd)
	if !config.ValidRabbitMQName(name) {
		return fmt.Errorf("invalid RabbitMQ instance name %q: use letters, digits, '_' and '-', starting with a letter or digit (max 48 chars)", name)
	}
	for _, flag := range []string{"advertise-host", "node-id", "peer", "join"} {
		if cmd.Flags().Changed(flag) {
			return fmt.Errorf("RabbitMQ does not use --%s; its peer-visible identity is --node-name NAME@HOST", flag)
		}
	}
	fmt.Printf("=== pg addon install rabbitmq (%s) ===\n", name)

	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if c.Addons.RabbitMQ == nil {
		c.Addons.RabbitMQ = make(map[string]config.RabbitMQConfig)
	}
	opts := readRabbitMQInstallOptions(cmd.Flags())
	force, _ := cmd.Flags().GetBool("force")
	secretsFromEtcd, _ := cmd.Flags().GetBool("secrets-from-etcd")
	c.ApplyDefaults()
	stored, present := c.Addons.RabbitMQ[name]

	if present && !force {
		state := "stopped"
		if m, err := podman.NewRabbitMQManager(c); err == nil {
			if running, _ := m.ContainerRunning(stored.ContainerName); running {
				state = "running"
			}
		}
		tlsInfo := ""
		if stored.TLS {
			tlsInfo = fmt.Sprintf(", tls %s", rabbitmq.AMQPSAddr(stored))
		}
		fmt.Printf("✓ rabbitmq %q already installed (%s, amqp %s%s, management %s, admin %s) — re-install with --force to rewrite the config and recreate the container\n",
			name, state, rabbitmq.AMQPAddr(stored), tlsInfo, rabbitmq.ManagementURL(stored), stored.AdminUser)
		fmt.Printf("  Password: pg addon password rabbitmq --name %s\n", name)
		return nil
	}

	rc := config.RabbitMQConfig{Name: name}
	if present {
		rc = stored
	}
	rc.Name = name
	if err := applyRabbitMQInstallFlags(&rc, cmd.Flags(), opts); err != nil {
		return err
	}
	c.Addons.RabbitMQ[name] = rc
	c.ApplyDefaults()
	rc = c.Addons.RabbitMQ[name]

	if present {
		if err := checkRabbitMQTopologyImmutable(stored, rc); err != nil {
			return err
		}
		if rc.Cluster != "" && opts.adminPasswordFileSet && rc.AdminPassword != stored.AdminPassword {
			fmt.Fprintln(os.Stderr, "[!] admin password changed: apply the same password to EVERY cluster member; each node reconciles the shared admin account against its own stored value on start")
		}
	}
	if rc.Cluster == "" && (opts.nodeNameSet || opts.etcdSet || opts.etcdEndpointsSet || opts.erlangCookieFileSet || secretsFromEtcd) {
		return fmt.Errorf("--node-name, --etcd, --etcd-endpoints, --erlang-cookie-file and --secrets-from-etcd require --cluster")
	}
	if rc.Cluster != "" {
		if opts.etcdSet || opts.etcdEndpointsSet {
			members, _ := cmd.Flags().GetStringSlice("etcd")
			explicit, _ := cmd.Flags().GetStringSlice("etcd-endpoints")
			endpoints, err := resolveRabbitMQEtcdEndpoints(c, members, explicit)
			if err != nil {
				return err
			}
			rc.EtcdEndpoints = endpoints
			c.Addons.RabbitMQ[name] = rc
		}
		if len(rc.EtcdEndpoints) > 0 {
			endpoints, err := config.NormalizeRabbitMQEtcdEndpoints(rc.EtcdEndpoints)
			if err != nil {
				return err
			}
			rc.EtcdEndpoints = endpoints
			c.Addons.RabbitMQ[name] = rc
		}
		if !present {
			empty, err := rabbitMQDirEmpty(rc.DataDir)
			if err != nil {
				return err
			}
			if !empty {
				return fmt.Errorf("data dir %s already contains RabbitMQ state; a clustered member needs an empty data dir — remove the previous instance with 'pg addon remove rabbitmq --name %s --clean-data' and reinstall", rc.DataDir, name)
			}
		}
		if rc.NodeName == "" {
			return fmt.Errorf("cluster mode requires --node-name NAME@HOST (unique per member and resolvable from every other member)")
		}
		if len(rc.EtcdEndpoints) == 0 {
			return fmt.Errorf("cluster mode requires --etcd <member,...> (local etcd addon members) or --etcd-endpoints <host:port,...> (external etcd)")
		}
		if secretsFromEtcd {
			cookie, password, err := readRabbitMQSecretsFromEtcd(rc.EtcdEndpoints, rc.Cluster)
			if err != nil {
				return err
			}
			rc.ErlangCookie = cookie
			rc.AdminPassword = password
			c.Addons.RabbitMQ[name] = rc
			if present {
				if err := checkRabbitMQTopologyImmutable(stored, rc); err != nil {
					return err
				}
				if rc.AdminPassword != stored.AdminPassword {
					fmt.Fprintln(os.Stderr, "[!] admin password changed: apply the same password to EVERY cluster member; each node reconciles the shared admin account against its own stored value on start")
				}
			}
		}
		if rc.ErlangCookie == "" {
			return fmt.Errorf("cluster mode requires --erlang-cookie-file FILE or --secrets-from-etcd with the Erlang cookie shared by every member")
		}
		if rc.AdminPassword == "" {
			return fmt.Errorf("cluster mode requires --admin-password-file FILE or --secrets-from-etcd with the admin password shared by every member")
		}
	}

	if rc.AdminPassword == "" {
		pw, err := rabbitMQAdminPassword(rc.SecretPath)
		if err != nil {
			return fmt.Errorf("resolving admin password: %w", err)
		}
		rc.AdminPassword = pw
		c.Addons.RabbitMQ[name] = rc
	}
	if err := validateRabbitMQBYOTLS(rc); err != nil {
		return err
	}
	if rc.Cluster != "" {
		if err := preflightRabbitMQEtcdEndpoints(rc.EtcdEndpoints); err != nil {
			return err
		}
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if err := rabbitmq.WriteSecret(rc); err != nil {
		return fmt.Errorf("writing password file: %w", err)
	}
	// Persist before starting so a failed boot is still inspectable and the
	// generated admin password is not lost.
	if err := c.Save(path); err != nil {
		return fmt.Errorf("  [X] saving config: %w", err)
	}

	m, err := podman.NewRabbitMQManager(c)
	if err != nil {
		return err
	}
	if err := m.EnsureImage(rc.ImageTag); err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] image:     %s\n", rc.ImageTag)
	fmt.Printf("  [OK] config:    %s\n", rc.ConfigPath)
	fmt.Printf("  [OK] data:      %s\n", rc.DataDir)
	if err := m.EnsureContainer(&rc); err != nil {
		m.LogsTail(rc.ContainerName, 30)
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] container: %s\n", rc.ContainerName)
	if rc.Cluster != "" {
		fmt.Printf("  [i] cluster member registered; if no peer is running yet this node stays a single-member cluster until the others install\n")
	}

	fmt.Println()
	fmt.Printf("✓ RabbitMQ %q installed\n", name)
	fmt.Printf("  Version:    %s\n", rc.Version)
	fmt.Printf("  Image:      %s\n", rc.ImageTag)
	fmt.Printf("  Container:  %s\n", rc.ContainerName)
	fmt.Printf("  AMQP:       amqp://%s\n", rabbitmq.AMQPAddr(rc))
	fmt.Printf("  Management: %s\n", rabbitmq.ManagementURL(rc))
	if rc.Cluster != "" {
		fmt.Printf("  Cluster:    %s\n", rc.Cluster)
		fmt.Printf("  Node:       %s\n", rc.NodeName)
		fmt.Printf("  Etcd:       %s\n", strings.Join(rc.EtcdEndpoints, ", "))
	}
	if rc.TLS {
		fmt.Printf("  AMQPS:      amqps://%s\n", rabbitmq.AMQPSAddr(rc))
		fmt.Printf("  Mgmt TLS:   %s\n", rabbitmq.ManagementTLSURL(rc))
		if rabbitmq.BYOTLS(rc) {
			fmt.Printf("  TLS cert:   %s (bring your own)\n", rc.CertFile)
		} else {
			fmt.Printf("  CA cert:    %s\n", rabbitmq.TLSCAPath(rc))
		}
	}
	fmt.Printf("  Admin:      %s\n", rc.AdminUser)
	fmt.Printf("  Password:   %s\n", rc.AdminPassword)
	fmt.Printf("  Data dir:   %s\n", rc.DataDir)
	fmt.Printf("  Config:     %s\n", rc.ConfigPath)
	fmt.Println()
	fmt.Printf("  Password: pg addon password rabbitmq --name %s\n", name)
	fmt.Printf("  Status:   pg addon rabbitmq status --name %s\n", name)
	fmt.Printf("  Queues:   pg addon rabbitmq queue list --name %s\n", name)
	fmt.Printf("  Declare:  pg addon rabbitmq queue declare orders --name %s\n", name)
	fmt.Printf("  Shell:    pg addon rabbitmq shell --name %s\n", name)
	return nil
}

func runAddonRemoveRabbitMQ(cmd *cobra.Command) error {
	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	name := rabbitMQFlagName(cmd)
	cleanData, _ := cmd.Flags().GetBool("clean-data")
	c.ApplyDefaults()
	rc, ok := c.Addons.RabbitMQ[name]
	if !ok {
		return fmt.Errorf("rabbitmq %q not found (see 'pg addon list')", name)
	}
	fmt.Printf("=== pg addon remove rabbitmq (%s) ===\n", name)

	m, err := podman.NewRabbitMQManager(c)
	if err != nil {
		return err
	}
	if err := m.RemoveContainer(&rc); err != nil {
		return err
	}
	delete(c.Addons.RabbitMQ, name)
	if len(c.Addons.RabbitMQ) == 0 {
		c.Addons.RabbitMQ = nil
	}
	if err := c.Save(path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Printf("  [OK] config entry removed (%s)\n", path)

	if cleanData {
		if err := m.CleanData(&rc); err != nil {
			return err
		}
	} else {
		fmt.Printf("  [i] kept %s (data, config, secrets) — pass --clean-data to drop\n", filepath.Dir(filepath.Dir(rc.ConfigPath)))
	}
	fmt.Printf("✓ RabbitMQ %q removed\n", name)
	return nil
}

func runAddonStartRabbitMQ(cmd *cobra.Command) error {
	name := rabbitMQFlagName(cmd)
	c, _, rc, err := resolveRabbitMQInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewRabbitMQManager(c)
	if err != nil {
		return err
	}
	already, err := m.StartContainer(&rc)
	if err != nil {
		m.LogsTail(rc.ContainerName, 20)
		return err
	}
	if already {
		fmt.Printf("✓ rabbitmq %q already running (AMQP %s, Management %s)\n", name, rabbitmq.AMQPAddr(rc), rabbitmq.ManagementURL(rc))
		return nil
	}
	fmt.Printf("✓ rabbitmq %q started (AMQP %s, Management %s)\n", name, rabbitmq.AMQPAddr(rc), rabbitmq.ManagementURL(rc))
	return nil
}

func runAddonStopRabbitMQ(cmd *cobra.Command) error {
	name := rabbitMQFlagName(cmd)
	c, _, rc, err := resolveRabbitMQInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewRabbitMQManager(c)
	if err != nil {
		return err
	}
	wasRunning, err := m.Stop(rc.ContainerName)
	if err != nil {
		return err
	}
	if !wasRunning {
		fmt.Printf("✓ rabbitmq %q already stopped\n", name)
		return nil
	}
	fmt.Printf("✓ rabbitmq %q stopped\n", name)
	return nil
}

// requireRabbitMQRunning returns a manager for a running instance.
func requireRabbitMQRunning(cmd *cobra.Command) (*podman.RabbitMQManager, config.RabbitMQConfig, error) {
	name := rabbitMQFlagName(cmd)
	c, _, rc, err := resolveRabbitMQInstance(name)
	if err != nil {
		return nil, rc, err
	}
	m, err := podman.NewRabbitMQManager(c)
	if err != nil {
		return nil, rc, err
	}
	if running, _ := m.ContainerRunning(rc.ContainerName); !running {
		return nil, rc, fmt.Errorf("rabbitmq %q is not running — 'pg addon start rabbitmq --name %s' first", name, name)
	}
	return m, rc, nil
}

// --- status / shell ---------------------------------------------------------

func runAddonRabbitMQStatus(cmd *cobra.Command) error {
	name := rabbitMQFlagName(cmd)
	c, _, rc, err := resolveRabbitMQInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewRabbitMQManager(c)
	if err != nil {
		return err
	}
	state := "stopped"
	if running, _ := m.ContainerRunning(rc.ContainerName); running {
		state = "running"
	}
	fmt.Printf("rabbitmq %q — %s\n", name, state)
	fmt.Printf("  Version:    %s\n", rc.Version)
	fmt.Printf("  Image:      %s\n", rc.ImageTag)
	fmt.Printf("  Container:  %s\n", rc.ContainerName)
	if rc.Cluster != "" {
		fmt.Printf("  Cluster:    %s\n", rc.Cluster)
		fmt.Printf("  Node:       %s\n", rc.NodeName)
		fmt.Printf("  Etcd:       %s\n", strings.Join(rc.EtcdEndpoints, ", "))
	}
	fmt.Printf("  Listen:     %s\n", rc.Listen)
	fmt.Printf("  AMQP:       amqp://%s\n", rabbitmq.AMQPAddr(rc))
	fmt.Printf("  Management: %s\n", rabbitmq.ManagementURL(rc))
	if rc.TLS {
		fmt.Printf("  AMQPS:      amqps://%s\n", rabbitmq.AMQPSAddr(rc))
		fmt.Printf("  Mgmt TLS:   %s\n", rabbitmq.ManagementTLSURL(rc))
		if rabbitmq.BYOTLS(rc) {
			fmt.Printf("  TLS cert:   %s (bring your own)\n", rc.CertFile)
		} else {
			fmt.Printf("  CA cert:    %s\n", rabbitmq.TLSCAPath(rc))
		}
	}
	fmt.Printf("  Admin user: %s\n", rc.AdminUser)
	fmt.Printf("  EPMD port:  %d\n", rc.EPMDPort)
	fmt.Printf("  Dist port:  %d\n", rc.DistPort)
	fmt.Printf("  Data dir:   %s\n", rc.DataDir)
	fmt.Printf("  Config:     %s\n", rc.ConfigPath)
	fmt.Printf("  Settings:   %d override(s)\n", len(rc.Settings))
	fmt.Printf("  Password:   pg addon password rabbitmq --name %s\n", name)
	fmt.Printf("  Logs:       pg logs addon rabbitmq --name %s\n", name)
	return nil
}

func runAddonRabbitMQShell(cmd *cobra.Command) error {
	name := rabbitMQFlagName(cmd)
	c, _, rc, err := resolveRabbitMQInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewRabbitMQManager(c)
	if err != nil {
		return err
	}
	if running, _ := m.ContainerRunning(rc.ContainerName); !running {
		fmt.Fprintf(os.Stderr, "[!] rabbitmq %q is not running — start it with: pg addon start rabbitmq --name %s\n", name, name)
	}
	return m.Exec(&rc, true)
}

// --- cluster commands -------------------------------------------------------

func requireRabbitMQCluster(cmd *cobra.Command) (*podman.RabbitMQManager, config.RabbitMQConfig, error) {
	m, rc, err := requireRabbitMQRunning(cmd)
	if err != nil {
		return nil, rc, err
	}
	if rc.Cluster == "" {
		return nil, rc, fmt.Errorf("rabbitmq %q is a standalone instance; cluster commands require an instance installed with --cluster", rc.Name)
	}
	return m, rc, nil
}

func runAddonRabbitMQClusterStatus(cmd *cobra.Command) error {
	m, rc, err := requireRabbitMQCluster(cmd)
	if err != nil {
		return err
	}
	out, err := m.ClusterStatus(&rc)
	if err != nil {
		return err
	}
	fmt.Print(out)
	if !strings.HasSuffix(out, "\n") {
		fmt.Println()
	}
	return nil
}

func runAddonRabbitMQClusterForget(cmd *cobra.Command, args []string) error {
	m, rc, err := requireRabbitMQCluster(cmd)
	if err != nil {
		return err
	}
	node := args[0]
	if _, _, err := config.ParseRabbitMQNodeName(node); err != nil {
		return fmt.Errorf("invalid node name %q: %w", node, err)
	}
	if node == rc.NodeName {
		return fmt.Errorf("refusing to forget the local node %q; run this command on a surviving member", node)
	}
	yes, _ := cmd.Flags().GetBool("yes")
	if !yes && !confirmPrompt(fmt.Sprintf("Remove node %q from cluster %q? It must be stopped first. [y/N] ", node, rc.Cluster)) {
		fmt.Println("aborted")
		return nil
	}
	out, err := m.ForgetClusterNode(&rc, node)
	if err != nil {
		return err
	}
	if msg := strings.TrimSpace(out); msg != "" {
		fmt.Println(msg)
	}
	fmt.Printf("✓ node %q removed from cluster %q\n", node, rc.Cluster)
	fmt.Println("  Rebalance quorum queues on a remaining member: rabbitmq-queues rebalance quorum")
	return nil
}

// --- queue commands ---------------------------------------------------------

func rabbitMQVhost(cmd *cobra.Command) string {
	vhost, _ := cmd.Flags().GetString("vhost")
	if vhost == "" {
		return "/"
	}
	return vhost
}

// rabbitMQAPI resolves a running instance and returns a management API client.
func rabbitMQAPI(cmd *cobra.Command) (*rabbitmq.APIClient, config.RabbitMQConfig, error) {
	_, rc, err := requireRabbitMQRunning(cmd)
	if err != nil {
		return nil, rc, err
	}
	return rabbitmq.NewAPIClient(rc), rc, nil
}

// parseRabbitMQKeyValues parses repeatable KEY=VALUE flags. Values that parse
// as JSON keep their JSON type (numbers, booleans, objects); anything else is
// a string.
func parseRabbitMQKeyValues(flag string, pairs []string) (map[string]any, error) {
	out := make(map[string]any, len(pairs))
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --%s %q: use KEY=VALUE", flag, pair)
		}
		var parsed any
		if err := json.Unmarshal([]byte(value), &parsed); err != nil {
			parsed = value
		}
		out[key] = parsed
	}
	return out, nil
}

func rabbitMQFormatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		data, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		return string(data)
	}
}

func rabbitMQFormatArguments(m map[string]any) string {
	if len(m) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+rabbitMQFormatValue(m[k]))
	}
	return strings.Join(parts, ", ")
}

func rabbitMQPrintJSON(raw []byte) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		fmt.Println(string(raw))
		return
	}
	fmt.Println(buf.String())
}

// rabbitMQDurationMS converts a Go duration or a bare number of milliseconds
// to the integer milliseconds RabbitMQ queue arguments expect.
func rabbitMQDurationMS(flag, value string) (int64, error) {
	if ms, err := strconv.ParseInt(value, 10, 64); err == nil {
		if ms < 0 {
			return 0, fmt.Errorf("--%s must not be negative", flag)
		}
		return ms, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid --%s %q: use milliseconds or a duration such as 30m, 24h", flag, value)
	}
	return int64(d / time.Millisecond), nil
}

func runAddonRabbitMQQueueList(cmd *cobra.Command) error {
	m, rc, err := requireRabbitMQRunning(cmd)
	if err != nil {
		return err
	}
	out, err := m.Queue(&rc, rabbitMQVhost(cmd), "list_queues",
		"name", "type", "messages", "messages_ready", "messages_unacknowledged", "consumers")
	if err != nil {
		return err
	}
	out = strings.TrimRight(out, "\n")
	if out == "" {
		fmt.Printf("no queues on rabbitmq %q (vhost %s)\n", rc.Name, rabbitMQVhost(cmd))
		return nil
	}
	// rabbitmqctl already prints a tab-separated header row.
	fmt.Println(out)
	return nil
}

func runAddonRabbitMQQueuePurge(cmd *cobra.Command, args []string) error {
	m, rc, err := requireRabbitMQRunning(cmd)
	if err != nil {
		return err
	}
	queue := args[0]
	vhost := rabbitMQVhost(cmd)
	yes, _ := cmd.Flags().GetBool("yes")
	if !yes && !confirmPrompt(fmt.Sprintf("Purge every message from queue %q on rabbitmq %q (vhost %s)? [y/N] ", queue, rc.Name, vhost)) {
		fmt.Println("aborted")
		return nil
	}
	out, err := m.Queue(&rc, vhost, "purge_queue", queue)
	if err != nil {
		return err
	}
	if msg := strings.TrimSpace(out); msg != "" {
		fmt.Println(msg)
	}
	fmt.Printf("✓ queue %q purged on rabbitmq %q\n", queue, rc.Name)
	return nil
}

func runAddonRabbitMQQueueDelete(cmd *cobra.Command, args []string) error {
	m, rc, err := requireRabbitMQRunning(cmd)
	if err != nil {
		return err
	}
	queue := args[0]
	vhost := rabbitMQVhost(cmd)
	yes, _ := cmd.Flags().GetBool("yes")
	ifEmpty, _ := cmd.Flags().GetBool("if-empty")
	ifUnused, _ := cmd.Flags().GetBool("if-unused")
	if !yes {
		extra := ""
		if ifEmpty {
			extra += " only if empty"
		}
		if ifUnused {
			extra += " only if unused"
		}
		if !confirmPrompt(fmt.Sprintf("Delete queue %q on rabbitmq %q (vhost %s)%s? [y/N] ", queue, rc.Name, vhost, extra)) {
			fmt.Println("aborted")
			return nil
		}
	}
	ctlArgs := []string{"delete_queue", queue}
	if ifEmpty {
		ctlArgs = append(ctlArgs, "--if-empty")
	}
	if ifUnused {
		ctlArgs = append(ctlArgs, "--if-unused")
	}
	out, err := m.Queue(&rc, vhost, ctlArgs...)
	if err != nil {
		return err
	}
	if msg := strings.TrimSpace(out); msg != "" {
		fmt.Println(msg)
	}
	fmt.Printf("✓ queue %q deleted on rabbitmq %q\n", queue, rc.Name)
	return nil
}

type rabbitMQQueueDeclareOptions struct {
	durable       bool
	autoDelete    bool
	queueType     string
	arguments     map[string]any
	messageTTL    int64
	queueExpires  int64
	messageTTLSet bool
	expiresSet    bool
}

// parseRabbitMQQueueDeclareFlags validates declare flags before any container
// work. defaultType applies when --type is not given: quorum on a cluster
// member (the only replicated, HA-capable queue type), classic standalone.
func parseRabbitMQQueueDeclareFlags(fs *pflag.FlagSet, defaultType string) (rabbitMQQueueDeclareOptions, error) {
	var opts rabbitMQQueueDeclareOptions
	opts.durable, _ = fs.GetBool("durable")
	opts.autoDelete, _ = fs.GetBool("auto-delete")
	opts.queueType, _ = fs.GetString("type")
	if opts.queueType == "" {
		opts.queueType = defaultType
	}
	if opts.queueType != "classic" && opts.queueType != "quorum" {
		return opts, fmt.Errorf("invalid --type %q: use classic or quorum", opts.queueType)
	}
	if !opts.durable {
		return opts, fmt.Errorf("RabbitMQ 4.x rejects transient (non-durable) non-exclusive queues; declare a durable queue")
	}
	if opts.queueType == "quorum" && opts.autoDelete {
		return opts, fmt.Errorf("quorum queues must be durable and cannot be auto-delete")
	}
	argFlags, _ := fs.GetStringArray("argument")
	arguments, err := parseRabbitMQKeyValues("argument", argFlags)
	if err != nil {
		return opts, err
	}
	arguments["x-queue-type"] = opts.queueType
	if fs.Changed("message-ttl") {
		ttlText, _ := fs.GetString("message-ttl")
		opts.messageTTL, err = rabbitMQDurationMS("message-ttl", ttlText)
		if err != nil {
			return opts, err
		}
		opts.messageTTLSet = true
		arguments["x-message-ttl"] = opts.messageTTL
	}
	if fs.Changed("expires") {
		expiresText, _ := fs.GetString("expires")
		opts.queueExpires, err = rabbitMQDurationMS("expires", expiresText)
		if err != nil {
			return opts, err
		}
		opts.expiresSet = true
		if opts.queueExpires <= 0 {
			return opts, fmt.Errorf("--expires must be a positive duration (x-expires cannot be 0)")
		}
		arguments["x-expires"] = opts.queueExpires
	}
	opts.arguments = arguments
	return opts, nil
}

// rabbitMQDefaultQueueType picks the queue type when --type is omitted:
// quorum on a cluster member (the only replicated HA-capable type in RabbitMQ
// 4.x), classic standalone.
func rabbitMQDefaultQueueType(rc config.RabbitMQConfig) string {
	if rc.Cluster != "" {
		return "quorum"
	}
	return "classic"
}

func runAddonRabbitMQQueueDeclare(cmd *cobra.Command, args []string) error {
	api, rc, err := rabbitMQAPI(cmd)
	if err != nil {
		return err
	}
	opts, err := parseRabbitMQQueueDeclareFlags(cmd.Flags(), rabbitMQDefaultQueueType(rc))
	if err != nil {
		return err
	}
	queue := args[0]
	vhost := rabbitMQVhost(cmd)
	err = api.DeclareQueue(vhost, queue, rabbitmq.QueueDeclareOptions{
		Durable:    opts.durable,
		AutoDelete: opts.autoDelete,
		Arguments:  opts.arguments,
	})
	if err != nil {
		return err
	}
	fmt.Printf("✓ queue %q declared on rabbitmq %q (vhost %s, type %s, durable %t, auto-delete %t)\n",
		queue, rc.Name, vhost, opts.queueType, opts.durable, opts.autoDelete)
	if opts.messageTTLSet {
		fmt.Printf("  message TTL:  %d ms (messages expire while waiting in the queue)\n", opts.messageTTL)
	}
	if opts.expiresSet {
		fmt.Printf("  queue expiry: %d ms (deleted after being unused for this long)\n", opts.queueExpires)
	}
	return nil
}

func runAddonRabbitMQQueueInfo(cmd *cobra.Command, args []string) error {
	api, rc, err := rabbitMQAPI(cmd)
	if err != nil {
		return err
	}
	queue := args[0]
	vhost := rabbitMQVhost(cmd)
	info, raw, err := api.QueueInfo(vhost, queue)
	if err != nil {
		return err
	}
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		rabbitMQPrintJSON(raw)
		return nil
	}
	fmt.Printf("queue %q on rabbitmq %q (vhost %s)\n", queue, rc.Name, vhost)
	for _, field := range [][2]string{
		{"type", "type"},
		{"state", "state"},
		{"durable", "durable"},
		{"auto-delete", "auto_delete"},
		{"messages", "messages"},
		{"ready", "messages_ready"},
		{"unacked", "messages_unacknowledged"},
		{"consumers", "consumers"},
		{"memory", "memory"},
		{"policy", "policy"},
		{"arguments", "arguments"},
		{"node", "node"},
	} {
		v, ok := info[field[1]]
		if !ok {
			continue
		}
		fmt.Printf("  %-14s %s\n", field[0]+":", rabbitMQFormatValue(v))
	}
	return nil
}

func runAddonRabbitMQQueuePublish(cmd *cobra.Command, args []string) error {
	api, rc, err := rabbitMQAPI(cmd)
	if err != nil {
		return err
	}
	queue := args[0]
	vhost := rabbitMQVhost(cmd)
	payload, _ := cmd.Flags().GetString("payload")
	payloadFile, _ := cmd.Flags().GetString("payload-file")
	payloadSet := cmd.Flags().Changed("payload")
	fileSet := cmd.Flags().Changed("payload-file")
	if payloadSet == fileSet {
		return fmt.Errorf("pass exactly one of --payload or --payload-file (- for stdin)")
	}
	if fileSet {
		if payloadFile == "-" {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("reading stdin: %w", err)
			}
			payload = string(data)
		} else {
			data, err := os.ReadFile(payloadFile)
			if err != nil {
				return fmt.Errorf("reading --payload-file: %w", err)
			}
			payload = string(data)
		}
	}
	exchange, _ := cmd.Flags().GetString("exchange")
	if exchange == "" {
		exchange = "amq.default"
	}
	routingKey, _ := cmd.Flags().GetString("routing-key")
	if !cmd.Flags().Changed("routing-key") {
		routingKey = queue
	}
	propertyFlags, _ := cmd.Flags().GetStringArray("property")
	properties, err := parseRabbitMQKeyValues("property", propertyFlags)
	if err != nil {
		return err
	}
	routed, err := api.Publish(vhost, exchange, routingKey, payload, properties)
	if err != nil {
		return err
	}
	if !routed {
		fmt.Fprintf(os.Stderr, "[!] message was not routed (exchange %q, routing key %q) — no matching binding\n", exchange, routingKey)
		return nil
	}
	fmt.Printf("✓ published %d byte(s) to exchange %q (routing key %q) on rabbitmq %q (vhost %s)\n",
		len(payload), exchange, routingKey, rc.Name, vhost)
	return nil
}

func runAddonRabbitMQQueueGet(cmd *cobra.Command, args []string) error {
	api, rc, err := rabbitMQAPI(cmd)
	if err != nil {
		return err
	}
	queue := args[0]
	vhost := rabbitMQVhost(cmd)
	count, _ := cmd.Flags().GetInt("count")
	if count <= 0 {
		return fmt.Errorf("--count must be greater than 0")
	}
	consume, _ := cmd.Flags().GetBool("consume")
	ackmode := "ack_requeue_true"
	if consume {
		ackmode = "ack_requeue_false"
	}
	messages, raw, err := api.Get(vhost, queue, count, ackmode)
	if err != nil {
		return err
	}
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		rabbitMQPrintJSON(raw)
		return nil
	}
	if len(messages) == 0 {
		fmt.Printf("no messages in queue %q on rabbitmq %q (vhost %s)\n", queue, rc.Name, vhost)
		return nil
	}
	for i, msg := range messages {
		exchange := msg.Exchange
		if exchange == "" {
			exchange = "(default)"
		}
		fmt.Printf("--- message %d/%d (exchange %q, routing key %q, redelivered %t) ---\n",
			i+1, len(messages), exchange, msg.RoutingKey, msg.Redelivered)
		if len(msg.Properties) > 0 {
			fmt.Printf("properties: %s\n", rabbitMQFormatArguments(msg.Properties))
		}
		if msg.PayloadEncoding == "base64" {
			fmt.Printf("payload (base64): %s\n", msg.Payload)
		} else {
			fmt.Println(msg.Payload)
		}
	}
	if consume {
		fmt.Printf("✓ consumed %d message(s) from queue %q\n", len(messages), queue)
	} else {
		fmt.Printf("✓ peeked %d message(s) from queue %q (not removed; use --consume to ack)\n", len(messages), queue)
	}
	return nil
}

func rabbitMQExchangeFlag(cmd *cobra.Command) (string, error) {
	exchange, _ := cmd.Flags().GetString("exchange")
	if exchange == "" || exchange == "amq.default" {
		return "", fmt.Errorf("--exchange is required and must name a real exchange (the default exchange cannot be bound explicitly)")
	}
	return exchange, nil
}

func rabbitMQRoutingKey(cmd *cobra.Command, queue string) string {
	routingKey, _ := cmd.Flags().GetString("routing-key")
	if !cmd.Flags().Changed("routing-key") {
		return queue
	}
	return routingKey
}

func runAddonRabbitMQQueueBind(cmd *cobra.Command, args []string) error {
	api, rc, err := rabbitMQAPI(cmd)
	if err != nil {
		return err
	}
	queue := args[0]
	exchange, err := rabbitMQExchangeFlag(cmd)
	if err != nil {
		return err
	}
	vhost := rabbitMQVhost(cmd)
	routingKey := rabbitMQRoutingKey(cmd, queue)
	argFlags, _ := cmd.Flags().GetStringArray("argument")
	arguments, err := parseRabbitMQKeyValues("argument", argFlags)
	if err != nil {
		return err
	}
	if err := api.Bind(vhost, queue, exchange, routingKey, arguments); err != nil {
		return err
	}
	fmt.Printf("✓ bound queue %q to exchange %q (routing key %q) on rabbitmq %q (vhost %s)\n",
		queue, exchange, routingKey, rc.Name, vhost)
	return nil
}

func runAddonRabbitMQQueueUnbind(cmd *cobra.Command, args []string) error {
	api, rc, err := rabbitMQAPI(cmd)
	if err != nil {
		return err
	}
	queue := args[0]
	exchange, err := rabbitMQExchangeFlag(cmd)
	if err != nil {
		return err
	}
	vhost := rabbitMQVhost(cmd)
	routingKey := rabbitMQRoutingKey(cmd, queue)
	var arguments map[string]any
	if cmd.Flags().Changed("argument") {
		argFlags, _ := cmd.Flags().GetStringArray("argument")
		arguments, err = parseRabbitMQKeyValues("argument", argFlags)
		if err != nil {
			return err
		}
	}
	removed, err := api.Unbind(vhost, queue, exchange, routingKey, arguments)
	if err != nil {
		return err
	}
	if removed == 0 {
		fmt.Printf("no matching binding from exchange %q to queue %q (routing key %q) on rabbitmq %q (vhost %s)\n",
			exchange, queue, routingKey, rc.Name, vhost)
		return nil
	}
	fmt.Printf("✓ removed %d binding(s) from exchange %q to queue %q (routing key %q) on rabbitmq %q (vhost %s)\n",
		removed, exchange, queue, routingKey, rc.Name, vhost)
	return nil
}

func runAddonRabbitMQQueueBindings(cmd *cobra.Command, args []string) error {
	api, rc, err := rabbitMQAPI(cmd)
	if err != nil {
		return err
	}
	queue := args[0]
	vhost := rabbitMQVhost(cmd)
	bindings, raw, err := api.QueueBindings(vhost, queue)
	if err != nil {
		return err
	}
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		rabbitMQPrintJSON(raw)
		return nil
	}
	if len(bindings) == 0 {
		fmt.Printf("no bindings for queue %q on rabbitmq %q (vhost %s)\n", queue, rc.Name, vhost)
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "EXCHANGE\tROUTING KEY\tARGUMENTS")
	for _, b := range bindings {
		rk := b.RoutingKey
		if rk == "" {
			rk = "(empty)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", b.Source, rk, rabbitMQFormatArguments(b.Arguments))
	}
	w.Flush()
	return nil
}

// --- command tree -----------------------------------------------------------

var addonRabbitMQCmd = &cobra.Command{
	Use:   "rabbitmq",
	Short: "Manage RabbitMQ container instances (standalone or cluster)",
	Long: `Manage RabbitMQ add-on instances (official rabbitmq management image).

pgcli runs RabbitMQ as a single node with host networking, or as one member of
a multi-host cluster built through etcd peer discovery. AMQP, the management
web UI/API and Erlang distribution are bound to --listen (default 127.0.0.1
standalone, 0.0.0.0 in a cluster) and allocated from separate port pools, so
several standalone instances coexist on one host. TLS is optional but
additive: --tls keeps the plaintext AMQP/management listeners and adds AMQPS
plus management HTTPS.

Cluster mode requires --cluster NAME, --node-name NODE@HOST, at least one etcd
endpoint (--etcd or --etcd-endpoints), and shared secrets across every member.
Generate the shared Erlang cookie and admin password once with
"pg addon rabbitmq secret generate" (it publishes them to etcd), then install
each member with --secrets-from-etcd. The older --erlang-cookie-file /
--admin-password-file pair is still accepted for manually distributed files.
Members discover each other through etcd on first boot; nodes with an
already-initialised data directory rejoin through their last-seen peers.

The administrator account is created on first boot and reconciled by pgcli on
every start, so the password stored in pg.yaml is always the live one.
"pg addon password rabbitmq" reads it back.

Subcommands:
  status    Show container state, endpoints, version and paths
  shell     Open a bash shell in the container
  queue     List/declare/inspect queues, publish/get messages and manage bindings
  cluster   Show cluster status and forget decommissioned members
  secret    Generate the shared Erlang cookie and admin password files

Examples:
  pg addon install rabbitmq --name rmq
  pg addon install rabbitmq --listen 0.0.0.0 --tls
  pg addon install rabbitmq --tls-cert cert.pem --tls-key key.pem
  pg addon rabbitmq secret generate --cluster lab --etcd-endpoints 10.0.0.1:2379
  pg addon install rabbitmq --name mq --cluster lab \
    --node-name rabbit@node1 --etcd-endpoints 10.0.0.1:2379 --secrets-from-etcd
  pg addon rabbitmq status --name rmq
  pg addon rabbitmq cluster status --name mq
  pg addon rabbitmq cluster forget rabbit@node3 --name mq --yes
  pg addon rabbitmq queue list --name rmq
  pg addon rabbitmq queue declare orders --name rmq
  pg addon rabbitmq queue publish orders --payload '{"id":1}' --name rmq
  pg addon rabbitmq queue get orders --count 5 --name rmq
  pg addon rabbitmq queue bind orders --exchange events --routing-key 'order.*' --name rmq
  pg addon rabbitmq queue purge orders --name rmq --yes
  pg addon rabbitmq queue delete orders --name rmq --vhost / --if-empty
  pg logs addon rabbitmq --name rmq -f
  pg addon remove rabbitmq --name rmq --clean-data`,
}

var addonRabbitMQStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show RabbitMQ container state, endpoints and paths",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonRabbitMQStatus(cmd) },
}

var addonRabbitMQShellCmd = &cobra.Command{
	Use:   "shell",
	Short: "Open a bash shell in the RabbitMQ container",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonRabbitMQShell(cmd) },
}

var addonRabbitMQClusterCmd = &cobra.Command{
	Use:   "cluster",
	Short: "Inspect and repair RabbitMQ cluster membership",
	Long: `Inspect and repair a RabbitMQ cluster from one member.

Members join automatically through etcd on first boot and rejoin through their
last-seen peers after a restart, so no manual join step is needed. Use
"forget" only for a member that will not come back.`,
}

var addonRabbitMQClusterStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show cluster members, partitions and listeners",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonRabbitMQClusterStatus(cmd) },
}

var addonRabbitMQClusterForgetCmd = &cobra.Command{
	Use:   "forget NODE",
	Short: "Remove an offline node from the cluster",
	Long: `Remove an offline node from the cluster metadata.

Stop the target node first and run this on a surviving member. Afterwards
rebalance quorum queues (inside the container: rabbitmq-queues rebalance quorum)
so replicas are placed on the remaining nodes.`,
	Args: cobra.ExactArgs(1),
	RunE: runAddonRabbitMQClusterForget,
}

var addonRabbitMQSecretCmd = &cobra.Command{
	Use:   "secret",
	Short: "Generate and publish the shared cluster secrets",
	Long: `Generate the files shared by every member of a RabbitMQ cluster.

Cluster members must use the same Erlang cookie and administrator password.
"generate" creates both files and stores their contents in etcd, so every
member can read them with --secrets-from-etcd during install instead of
copying files between hosts.`,
}

var addonRabbitMQSecretGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate the shared secrets and write them to etcd",
	Long: `Generate the shared Erlang cookie and administrator password, write both local
files with mode 0600, and publish their contents to etcd for the cluster.

The values are not printed. Existing files and etcd keys are left untouched
unless --force is given. Install each member with --secrets-from-etcd and the
same --cluster / etcd endpoints; no file copying between hosts is needed. The
secrets are stored in etcd as plain text, so restrict access to the etcd
client port to the cluster and management hosts.

After members are installed the Erlang cookie is immutable. To rotate only the
administrator password, use --force --keep-cookie: the existing cookie is
re-read from etcd and republished unchanged. Regenerating the cookie with
--force alone is only safe before any member has been installed.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error { return runAddonRabbitMQSecretGenerate(cmd) },
}

var addonRabbitMQQueueCmd = &cobra.Command{
	Use:   "queue",
	Short: "Manage queues, messages and bindings",
	Long: `Manage queues in a RabbitMQ instance.

list and purge/delete use rabbitmqctl inside the container. declare, info,
publish, get, bind, unbind and bindings go through the management HTTP API.

Subcommands:
  list       List queues with message and consumer counts
  declare    Declare a queue (create, or verify an identical one)
  info       Show one queue's state, counters and arguments
  publish    Publish a single message through an exchange
  get        Peek at or consume messages from a queue
  bind       Create a binding from an exchange to a queue
  unbind     Remove matching bindings
  bindings   List a queue's bindings
  purge      Remove all messages from a queue
  delete     Delete a queue`,
}

var addonRabbitMQQueueListCmd = &cobra.Command{
	Use:   "list",
	Short: "List queues with message and consumer counts",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonRabbitMQQueueList(cmd) },
}

var addonRabbitMQQueueDeclareCmd = &cobra.Command{
	Use:   "declare QUEUE",
	Short: "Declare a queue (create, or verify an identical one)",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonRabbitMQQueueDeclare,
}

var addonRabbitMQQueueInfoCmd = &cobra.Command{
	Use:   "info QUEUE",
	Short: "Show one queue's state, counters and arguments",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonRabbitMQQueueInfo,
}

var addonRabbitMQQueuePublishCmd = &cobra.Command{
	Use:   "publish QUEUE",
	Short: "Publish a single message through an exchange",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonRabbitMQQueuePublish,
}

var addonRabbitMQQueueGetCmd = &cobra.Command{
	Use:   "get QUEUE",
	Short: "Peek at or consume messages from a queue",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonRabbitMQQueueGet,
}

var addonRabbitMQQueueBindCmd = &cobra.Command{
	Use:   "bind QUEUE",
	Short: "Create a binding from an exchange to a queue",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonRabbitMQQueueBind,
}

var addonRabbitMQQueueUnbindCmd = &cobra.Command{
	Use:   "unbind QUEUE",
	Short: "Remove bindings from an exchange to a queue",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonRabbitMQQueueUnbind,
}

var addonRabbitMQQueueBindingsCmd = &cobra.Command{
	Use:   "bindings QUEUE",
	Short: "List a queue's bindings",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonRabbitMQQueueBindings,
}

var addonRabbitMQQueuePurgeCmd = &cobra.Command{
	Use:   "purge QUEUE",
	Short: "Remove all messages from a queue",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonRabbitMQQueuePurge,
}

var addonRabbitMQQueueDeleteCmd = &cobra.Command{
	Use:   "delete QUEUE",
	Short: "Delete a queue",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonRabbitMQQueueDelete,
}

func init() {
	addonCmd.AddCommand(addonRabbitMQCmd)
	addonRabbitMQCmd.AddCommand(addonRabbitMQStatusCmd, addonRabbitMQShellCmd, addonRabbitMQQueueCmd, addonRabbitMQClusterCmd, addonRabbitMQSecretCmd)
	addonRabbitMQClusterCmd.AddCommand(addonRabbitMQClusterStatusCmd, addonRabbitMQClusterForgetCmd)
	addonRabbitMQSecretCmd.AddCommand(addonRabbitMQSecretGenerateCmd)
	addonRabbitMQSecretGenerateCmd.Flags().String("cluster", "", "RabbitMQ cluster name shared by every member (required)")
	addonRabbitMQSecretGenerateCmd.Flags().StringSlice("etcd", nil, "local etcd addon member name(s) holding the secrets, e.g. --etcd dcs (repeatable or comma-separated)")
	addonRabbitMQSecretGenerateCmd.Flags().StringSlice("etcd-endpoints", nil, "etcd endpoints as host:port holding the secrets, e.g. 10.0.0.1:2379 (repeatable or comma-separated; takes precedence over --etcd)")
	addonRabbitMQSecretGenerateCmd.Flags().String("cookie-file", "/tmp/rmq.cookie", "local output path for the shared Erlang cookie file")
	addonRabbitMQSecretGenerateCmd.Flags().String("admin-password-file", "/tmp/rmq.admin", "local output path for the shared administrator password file")
	addonRabbitMQSecretGenerateCmd.Flags().Bool("force", false, "overwrite existing files and etcd keys")
	addonRabbitMQSecretGenerateCmd.Flags().Bool("keep-cookie", false, "with --force: keep the existing Erlang cookie and rotate only the admin password (the cookie is immutable once members are installed)")
	addonRabbitMQQueueCmd.AddCommand(
		addonRabbitMQQueueListCmd,
		addonRabbitMQQueueDeclareCmd,
		addonRabbitMQQueueInfoCmd,
		addonRabbitMQQueuePublishCmd,
		addonRabbitMQQueueGetCmd,
		addonRabbitMQQueueBindCmd,
		addonRabbitMQQueueUnbindCmd,
		addonRabbitMQQueueBindingsCmd,
		addonRabbitMQQueuePurgeCmd,
		addonRabbitMQQueueDeleteCmd,
	)

	for _, sub := range []*cobra.Command{
		addonRabbitMQStatusCmd, addonRabbitMQShellCmd,
		addonRabbitMQClusterStatusCmd, addonRabbitMQClusterForgetCmd,
		addonRabbitMQQueueListCmd, addonRabbitMQQueueDeclareCmd, addonRabbitMQQueueInfoCmd,
		addonRabbitMQQueuePublishCmd, addonRabbitMQQueueGetCmd,
		addonRabbitMQQueueBindCmd, addonRabbitMQQueueUnbindCmd, addonRabbitMQQueueBindingsCmd,
		addonRabbitMQQueuePurgeCmd, addonRabbitMQQueueDeleteCmd,
	} {
		sub.Flags().String("name", "", "rabbitmq instance name (default \"rabbitmq\")")
	}
	for _, sub := range []*cobra.Command{
		addonRabbitMQQueueListCmd, addonRabbitMQQueueDeclareCmd, addonRabbitMQQueueInfoCmd,
		addonRabbitMQQueuePublishCmd, addonRabbitMQQueueGetCmd,
		addonRabbitMQQueueBindCmd, addonRabbitMQQueueUnbindCmd, addonRabbitMQQueueBindingsCmd,
		addonRabbitMQQueuePurgeCmd, addonRabbitMQQueueDeleteCmd,
	} {
		sub.Flags().String("vhost", "/", "RabbitMQ virtual host")
	}
	for _, sub := range []*cobra.Command{addonRabbitMQQueuePurgeCmd, addonRabbitMQQueueDeleteCmd, addonRabbitMQClusterForgetCmd} {
		sub.Flags().Bool("yes", false, "skip the confirmation prompt")
	}
	addonRabbitMQQueueDeleteCmd.Flags().Bool("if-empty", false, "only delete the queue when it has no messages")
	addonRabbitMQQueueDeleteCmd.Flags().Bool("if-unused", false, "only delete the queue when it has no consumers")

	addonRabbitMQQueueDeclareCmd.Flags().Bool("durable", true, "declare a durable queue")
	addonRabbitMQQueueDeclareCmd.Flags().Bool("auto-delete", false, "delete the queue when its last consumer disconnects")
	addonRabbitMQQueueDeclareCmd.Flags().String("type", "", "queue type: classic or quorum (default: quorum on a cluster, classic standalone)")
	addonRabbitMQQueueDeclareCmd.Flags().String("message-ttl", "", "drop messages that wait in the queue longer than this (e.g. 30m, 24h, or milliseconds)")
	addonRabbitMQQueueDeclareCmd.Flags().String("expires", "", "delete the queue after it is unused for this long (e.g. 24h, or milliseconds)")
	addonRabbitMQQueueDeclareCmd.Flags().StringArray("argument", nil, "queue argument KEY=VALUE (repeatable; JSON values keep their type)")

	addonRabbitMQQueueInfoCmd.Flags().Bool("json", false, "print the raw management API JSON")

	addonRabbitMQQueuePublishCmd.Flags().String("payload", "", "message body")
	addonRabbitMQQueuePublishCmd.Flags().String("payload-file", "", "read the message body from a file (- for stdin)")
	addonRabbitMQQueuePublishCmd.Flags().String("exchange", "amq.default", "target exchange (amq.default routes by queue name)")
	addonRabbitMQQueuePublishCmd.Flags().String("routing-key", "", "routing key (default: the queue name)")
	addonRabbitMQQueuePublishCmd.Flags().StringArray("property", nil, "message property KEY=VALUE (repeatable; JSON values keep their type)")

	addonRabbitMQQueueGetCmd.Flags().Int("count", 1, "maximum number of messages to fetch")
	addonRabbitMQQueueGetCmd.Flags().Bool("consume", false, "acknowledge and remove the fetched messages (default: peek and requeue)")
	addonRabbitMQQueueGetCmd.Flags().Bool("json", false, "print the raw management API JSON")

	for _, sub := range []*cobra.Command{addonRabbitMQQueueBindCmd, addonRabbitMQQueueUnbindCmd} {
		sub.Flags().String("exchange", "", "source exchange (required)")
		sub.Flags().String("routing-key", "", "routing key (default: the queue name)")
		sub.Flags().StringArray("argument", nil, "binding argument KEY=VALUE (repeatable; JSON values keep their type)")
	}
	addonRabbitMQQueueBindingsCmd.Flags().Bool("json", false, "print the raw management API JSON")

	addonInstallCmd.Flags().Int("amqp-port", 0, "RabbitMQ AMQP host port (0=auto-assign from rabbitmq_start_port, default 5672)")
	addonInstallCmd.Flags().Int("management-port", 0, "RabbitMQ management web UI/API host port (0=auto-assign from rabbitmq_management_start_port, default 15672)")
	addonInstallCmd.Flags().String("node-name", "", "RabbitMQ cluster: this member's Erlang node name NODE@HOST, e.g. rabbit@node1 (required on first install; HOST must resolve from every member and must not be loopback; immutable after install)")
	addonInstallCmd.Flags().StringSlice("etcd", nil, "RabbitMQ cluster: local etcd addon member name(s) used for peer discovery, e.g. --etcd dcs (repeatable or comma-separated); alternative to --etcd-endpoints")
	addonInstallCmd.Flags().StringSlice("etcd-endpoints", nil, "RabbitMQ cluster: etcd endpoints as host:port for peer discovery, e.g. 10.0.0.1:2379,10.0.0.2:2379 (repeatable or comma-separated; an http:// prefix is stripped; takes precedence over --etcd)")
	addonInstallCmd.Flags().String("erlang-cookie-file", "", "RabbitMQ cluster: file holding the shared Erlang cookie (required on first cluster install unless --secrets-from-etcd; must be identical on every member and is immutable after install)")
	addonInstallCmd.Flags().String("admin-password-file", "", "read the admin password from FILE instead of --admin-password (required on first RabbitMQ cluster install unless --secrets-from-etcd; also accepted standalone)")
	addonInstallCmd.Flags().Bool("secrets-from-etcd", false, "RabbitMQ cluster: read the shared Erlang cookie and admin password from etcd keys written by 'pg addon rabbitmq secret generate'")
}
