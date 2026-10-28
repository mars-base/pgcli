package cli

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

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
  queue     List, purge or delete queues (via rabbitmqctl)

Examples:
  pg addon install rabbitmq --name rmq
  pg addon install rabbitmq --listen 0.0.0.0 --tls
  pg addon install rabbitmq --tls-cert cert.pem --tls-key key.pem
  pg addon rabbitmq status --name rmq
  pg addon rabbitmq queue list --name rmq
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
	Short: "List, purge and delete queues",
}

var addonRabbitMQQueueListCmd = &cobra.Command{
	Use:   "list",
	Short: "List queues with message and consumer counts",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonRabbitMQQueueList(cmd) },
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
	addonRabbitMQQueueCmd.AddCommand(addonRabbitMQQueueListCmd, addonRabbitMQQueuePurgeCmd, addonRabbitMQQueueDeleteCmd)

	for _, sub := range []*cobra.Command{
		addonRabbitMQStatusCmd, addonRabbitMQShellCmd,
		addonRabbitMQQueueListCmd, addonRabbitMQQueuePurgeCmd, addonRabbitMQQueueDeleteCmd,
	} {
		sub.Flags().String("name", "", "rabbitmq instance name (default \"rabbitmq\")")
	}
	for _, sub := range []*cobra.Command{
		addonRabbitMQQueueListCmd, addonRabbitMQQueuePurgeCmd, addonRabbitMQQueueDeleteCmd,
	} {
		sub.Flags().String("vhost", "/", "RabbitMQ virtual host")
	}
	for _, sub := range []*cobra.Command{addonRabbitMQQueuePurgeCmd, addonRabbitMQQueueDeleteCmd} {
		sub.Flags().Bool("yes", false, "skip the confirmation prompt")
	}
	addonRabbitMQQueueDeleteCmd.Flags().Bool("if-empty", false, "only delete the queue when it has no messages")
	addonRabbitMQQueueDeleteCmd.Flags().Bool("if-unused", false, "only delete the queue when it has no consumers")

	addonInstallCmd.Flags().Int("amqp-port", 0, "RabbitMQ AMQP host port (0=auto-assign from rabbitmq_start_port, default 5672)")
	addonInstallCmd.Flags().Int("management-port", 0, "RabbitMQ management web UI/API host port (0=auto-assign from rabbitmq_management_start_port, default 15672)")
}
