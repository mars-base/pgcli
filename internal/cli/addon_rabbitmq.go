package cli

import (
	"bytes"
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

// rabbitMQInstallOptions captures the Changed bits so a --force re-install
// keeps stored values for flags that were not passed.
type rabbitMQInstallOptions struct {
	listenSet         bool
	amqpPortSet       bool
	managementPortSet bool
	tlsPortSet        bool
	tlsSet            bool
	tlsCertSet        bool
	tlsKeySet         bool
	adminUserSet      bool
	adminPasswordSet  bool
	versionSet        bool
	dataDirSet        bool
	settingsSet       bool
}

func readRabbitMQInstallOptions(fs *pflag.FlagSet) rabbitMQInstallOptions {
	return rabbitMQInstallOptions{
		listenSet:         fs.Changed("listen"),
		amqpPortSet:       fs.Changed("amqp-port"),
		managementPortSet: fs.Changed("management-port"),
		tlsPortSet:        fs.Changed("tls-port"),
		tlsSet:            fs.Changed("tls"),
		tlsCertSet:        fs.Changed("tls-cert"),
		tlsKeySet:         fs.Changed("tls-key"),
		adminUserSet:      fs.Changed("admin-user"),
		adminPasswordSet:  fs.Changed("admin-password"),
		versionSet:        fs.Changed("version"),
		dataDirSet:        fs.Changed("data-dir"),
		settingsSet:       fs.Changed("config-option"),
	}
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

	fmt.Println()
	fmt.Printf("✓ RabbitMQ %q installed\n", name)
	fmt.Printf("  Version:    %s\n", rc.Version)
	fmt.Printf("  Image:      %s\n", rc.ImageTag)
	fmt.Printf("  Container:  %s\n", rc.ContainerName)
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
// or API work.
func parseRabbitMQQueueDeclareFlags(fs *pflag.FlagSet) (rabbitMQQueueDeclareOptions, error) {
	var opts rabbitMQQueueDeclareOptions
	opts.durable, _ = fs.GetBool("durable")
	opts.autoDelete, _ = fs.GetBool("auto-delete")
	opts.queueType, _ = fs.GetString("type")
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

func runAddonRabbitMQQueueDeclare(cmd *cobra.Command, args []string) error {
	opts, err := parseRabbitMQQueueDeclareFlags(cmd.Flags())
	if err != nil {
		return err
	}
	api, rc, err := rabbitMQAPI(cmd)
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
	Short: "Manage single-node RabbitMQ container instances",
	Long: `Manage RabbitMQ add-on instances (official rabbitmq management image).

pgcli runs RabbitMQ as a single node with host networking. AMQP, the
management web UI/API and Erlang distribution are bound to --listen
(default 127.0.0.1) and allocated from separate port pools, so several
instances coexist on one host. TLS is optional but additive: --tls keeps the
plaintext AMQP/management listeners and adds AMQPS plus management HTTPS.

The administrator account is created on first boot and reconciled by pgcli on
every start, so the password stored in pg.yaml is always the live one.
"pg addon password rabbitmq" reads it back.

Subcommands:
  status    Show container state, endpoints, version and paths
  shell     Open a bash shell in the container
  queue     List/declare/inspect queues, publish/get messages and manage bindings

Examples:
  pg addon install rabbitmq --name rmq
  pg addon install rabbitmq --listen 0.0.0.0 --tls
  pg addon install rabbitmq --tls-cert cert.pem --tls-key key.pem
  pg addon rabbitmq status --name rmq
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
	addonRabbitMQCmd.AddCommand(addonRabbitMQStatusCmd, addonRabbitMQShellCmd, addonRabbitMQQueueCmd)
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
	for _, sub := range []*cobra.Command{addonRabbitMQQueuePurgeCmd, addonRabbitMQQueueDeleteCmd} {
		sub.Flags().Bool("yes", false, "skip the confirmation prompt")
	}
	addonRabbitMQQueueDeleteCmd.Flags().Bool("if-empty", false, "only delete the queue when it has no messages")
	addonRabbitMQQueueDeleteCmd.Flags().Bool("if-unused", false, "only delete the queue when it has no consumers")

	addonRabbitMQQueueDeclareCmd.Flags().Bool("durable", true, "declare a durable queue")
	addonRabbitMQQueueDeclareCmd.Flags().Bool("auto-delete", false, "delete the queue when its last consumer disconnects")
	addonRabbitMQQueueDeclareCmd.Flags().String("type", "classic", "queue type: classic or quorum")
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
}
