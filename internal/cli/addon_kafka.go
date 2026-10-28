package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/kafka"
	"github.com/mars-base/pgcli/internal/podman"
)

// resolveKafkaInstance loads the config, applies defaults and returns the
// named Kafka node.
func resolveKafkaInstance(name string) (*config.Config, string, config.KafkaConfig, error) {
	c, path, err := loadAddonCfg()
	if err != nil {
		return nil, "", config.KafkaConfig{}, err
	}
	c.ApplyDefaults()
	kc, ok := c.Addons.Kafka[name]
	if !ok {
		return nil, "", config.KafkaConfig{}, fmt.Errorf("kafka %q not found (see 'pg addon list')", name)
	}
	return c, path, kc, nil
}

func kafkaFlagName(cmd *cobra.Command) string {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "kafka"
	}
	return name
}

// parseKafkaConfigOptions parses repeatable KEY=VALUE overrides for
// server.properties.
func parseKafkaConfigOptions(options []string) (map[string]string, error) {
	settings := make(map[string]string, len(options))
	for _, opt := range options {
		key, value, ok := strings.Cut(opt, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --config-option %q: use KEY=VALUE, e.g. num.partitions=3", opt)
		}
		settings[key] = value
	}
	if err := config.ValidateKafkaSettings(settings); err != nil {
		return nil, err
	}
	return settings, nil
}

// --- install / lifecycle ----------------------------------------------------

func runAddonInstallKafka(cmd *cobra.Command) error {
	name := kafkaFlagName(cmd)
	if !config.ValidKafkaName(name) {
		return fmt.Errorf("invalid Kafka instance name %q: use letters, digits, '_' and '-', starting with a letter or digit (max 48 chars)", name)
	}
	fmt.Printf("=== pg addon install kafka (%s) ===\n", name)

	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if c.Addons.Kafka == nil {
		c.Addons.Kafka = make(map[string]config.KafkaConfig)
	}
	kc, present := c.Addons.Kafka[name]
	force, _ := cmd.Flags().GetBool("force")
	if present && !force {
		c.ApplyDefaults()
		kc = c.Addons.Kafka[name]
		state := "stopped"
		if m, err := podman.NewKafkaManager(c); err == nil {
			if running, _ := m.ContainerRunning(kc.ContainerName); running {
				state = "running"
			}
		}
		fmt.Printf("✓ kafka %q already installed (%s, broker %s, controller %s, node %d) — re-install with --force to rewrite the config and recreate the container\n",
			name, state, kafka.AdvertisedBrokerEndpoint(kc), kafka.LocalControllerEndpoint(kc), kc.NodeID)
		return nil
	}

	fs := cmd.Flags()
	cluster, _ := fs.GetString("cluster")
	peers, _ := fs.GetStringArray("peer")
	if cluster != "" || len(peers) > 0 || fs.Changed("node-id") {
		return fmt.Errorf("multi-node Kafka (--cluster/--peer/--node-id) is not implemented yet; this release installs a single combined KRaft node")
	}

	if !present {
		kc = config.KafkaConfig{Name: name}
	}
	kc.Name = name

	if fs.Changed("version") {
		kc.Version, _ = fs.GetString("version")
	}
	if kc.Version == "" {
		kc.Version = config.DefaultKafkaVersion
	}
	imageTag, ok := config.KafkaImageTagForVersion(kc.Version)
	if !ok {
		return fmt.Errorf("Kafka version %q is not a valid image tag (default %s; pass the exact upstream tag, e.g. 4.4.0)", kc.Version, config.DefaultKafkaVersion)
	}
	kc.ImageTag = imageTag
	if v, _ := fs.GetString("listen"); v != "" {
		kc.Listen = v
	}
	if v, _ := fs.GetString("advertise-host"); v != "" {
		kc.AdvertiseHost = v
	}
	if fs.Changed("broker-port") {
		kc.BrokerPort, _ = fs.GetInt("broker-port")
	}
	if fs.Changed("controller-port") {
		kc.ControllerPort, _ = fs.GetInt("controller-port")
	}
	if v, _ := fs.GetString("data-dir"); v != "" {
		kc.DataDir = v
	}
	if fs.Changed("cluster-id") {
		kc.ClusterID, _ = fs.GetString("cluster-id")
	}
	if fs.Changed("config-option") {
		options, _ := fs.GetStringArray("config-option")
		settings, err := parseKafkaConfigOptions(options)
		if err != nil {
			return err
		}
		kc.Settings = settings
	}

	c.Addons.Kafka[name] = kc
	c.ApplyDefaults()
	kc = c.Addons.Kafka[name]

	// A fresh install adopts the cluster/node ids already formatted into the
	// data dir (remove without --clean-data, then reinstall). An existing entry
	// (--force) keeps its stored ids and is checked against the data dir.
	if !present && !fs.Changed("cluster-id") {
		kc.ClusterID = ""
		if !fs.Changed("node-id") {
			kc.NodeID = 0
		}
	}
	if err := kafka.AdoptMetaData(&kc); err != nil {
		return err
	}
	if kc.NodeID == 0 {
		kc.NodeID = 1
	}
	if kc.ClusterID == "" {
		kc.ClusterID, err = kafka.GenerateClusterID()
		if err != nil {
			return err
		}
	}
	c.Addons.Kafka[name] = kc
	c.ApplyDefaults()
	kc = c.Addons.Kafka[name]
	if err := c.Validate(); err != nil {
		c.Addons.Kafka[name] = kc
		return err
	}

	m, err := podman.NewKafkaManager(c)
	if err != nil {
		return err
	}
	if err := m.EnsureImage(kc.ImageTag); err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] image: %s\n", kc.ImageTag)

	// Persist before starting so a failed first boot remains inspectable and
	// the generated cluster id is not lost.
	if err := c.Save(path); err != nil {
		return fmt.Errorf("  [X] saving config: %w", err)
	}
	if err := m.EnsureContainer(&kc); err != nil {
		m.LogsTail(kc.ContainerName, 20)
		return fmt.Errorf("  [X] %v", err)
	}

	fmt.Println()
	fmt.Printf("✓ Kafka %q installed\n", name)
	fmt.Printf("  Version:     %s\n", kc.Version)
	fmt.Printf("  Image:       %s\n", kc.ImageTag)
	fmt.Printf("  Container:   %s\n", kc.ContainerName)
	fmt.Printf("  Broker:      %s\n", kafka.AdvertisedBrokerEndpoint(kc))
	fmt.Printf("  Controller:  %s\n", kafka.LocalControllerEndpoint(kc))
	fmt.Printf("  Node ID:     %d\n", kc.NodeID)
	fmt.Printf("  Cluster ID:  %s\n", kc.ClusterID)
	fmt.Printf("  Data dir:    %s\n", kc.DataDir)
	fmt.Printf("  Config:      %s\n", kc.ConfigPath)
	fmt.Println()
	fmt.Printf("  Status:      pg addon kafka status --name %s\n", name)
	fmt.Printf("  Create topic: pg addon kafka topic create demo --partitions 3 --name %s\n", name)
	fmt.Printf("  Shell:       pg addon kafka shell --name %s\n", name)
	fmt.Printf("  Logs:        pg logs addon kafka --name %s\n", name)
	return nil
}

func runAddonRemoveKafka(cmd *cobra.Command) error {
	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	name := kafkaFlagName(cmd)
	cleanData, _ := cmd.Flags().GetBool("clean-data")
	c.ApplyDefaults()
	kc, ok := c.Addons.Kafka[name]
	if !ok {
		return fmt.Errorf("kafka %q not found (see 'pg addon list')", name)
	}
	fmt.Printf("=== pg addon remove kafka (%s) ===\n", name)

	m, err := podman.NewKafkaManager(c)
	if err != nil {
		return err
	}
	if err := m.RemoveContainer(&kc); err != nil {
		return err
	}
	delete(c.Addons.Kafka, name)
	if len(c.Addons.Kafka) == 0 {
		c.Addons.Kafka = nil
	}
	if err := c.Save(path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Printf("  [OK] config entry removed (%s)\n", path)

	if cleanData {
		if err := m.CleanData(&kc); err != nil {
			return err
		}
	} else {
		fmt.Printf("  [i] kept %s (data + config) — pass --clean-data to drop\n", filepath.Dir(filepath.Dir(kc.ConfigPath)))
	}
	fmt.Printf("✓ Kafka %q removed\n", name)
	return nil
}

func runAddonStartKafka(cmd *cobra.Command) error {
	name := kafkaFlagName(cmd)
	c, _, kc, err := resolveKafkaInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewKafkaManager(c)
	if err != nil {
		return err
	}
	already, err := m.StartContainer(&kc)
	if err != nil {
		m.LogsTail(kc.ContainerName, 20)
		return err
	}
	if already {
		fmt.Printf("✓ kafka %q already running (broker %s)\n", name, kafka.AdvertisedBrokerEndpoint(kc))
		return nil
	}
	fmt.Printf("✓ kafka %q started (broker %s)\n", name, kafka.AdvertisedBrokerEndpoint(kc))
	return nil
}

func runAddonStopKafka(cmd *cobra.Command) error {
	name := kafkaFlagName(cmd)
	c, _, kc, err := resolveKafkaInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewKafkaManager(c)
	if err != nil {
		return err
	}
	wasRunning, err := m.Stop(kc.ContainerName)
	if err != nil {
		return err
	}
	if !wasRunning {
		fmt.Printf("✓ kafka %q already stopped\n", name)
		return nil
	}
	fmt.Printf("✓ kafka %q stopped\n", name)
	return nil
}

// --- status / shell / topic / cluster -------------------------------------

func runAddonKafkaStatus(cmd *cobra.Command) error {
	name := kafkaFlagName(cmd)
	c, _, kc, err := resolveKafkaInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewKafkaManager(c)
	if err != nil {
		return err
	}
	state := "stopped"
	if running, _ := m.ContainerRunning(kc.ContainerName); running {
		state = "running"
	}
	topology := "single-node combined KRaft"
	if kc.Clustered() {
		topology = fmt.Sprintf("cluster %q, node %d", kc.Cluster, kc.NodeID)
	}
	fmt.Printf("kafka %q — %s (%s)\n", name, state, topology)
	fmt.Printf("  Version:     %s\n", kc.Version)
	fmt.Printf("  Image:       %s\n", kc.ImageTag)
	fmt.Printf("  Container:   %s\n", kc.ContainerName)
	fmt.Printf("  Broker:      %s\n", kafka.AdvertisedBrokerEndpoint(kc))
	fmt.Printf("  Controller:  %s\n", kafka.LocalControllerEndpoint(kc))
	fmt.Printf("  Listen:      %s\n", kc.Listen)
	fmt.Printf("  Node ID:     %d\n", kc.NodeID)
	fmt.Printf("  Cluster ID:  %s\n", kc.ClusterID)
	fmt.Printf("  Data dir:    %s\n", kc.DataDir)
	fmt.Printf("  Config:      %s\n", kc.ConfigPath)
	fmt.Printf("  Settings:    %d override(s)\n", len(kc.Settings))
	fmt.Printf("  Logs:        pg logs addon kafka --name %s\n", name)
	return nil
}

func runAddonKafkaShell(cmd *cobra.Command, args []string) error {
	name := kafkaFlagName(cmd)
	c, _, kc, err := resolveKafkaInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewKafkaManager(c)
	if err != nil {
		return err
	}
	if running, _ := m.ContainerRunning(kc.ContainerName); !running {
		fmt.Fprintf(os.Stderr, "[!] kafka %q is not running — start it with: pg addon start kafka --name %s\n", name, name)
	}
	return m.Exec(&kc, true, args...)
}

func requireKafkaRunning(cmd *cobra.Command) (*podman.KafkaManager, config.KafkaConfig, error) {
	name := kafkaFlagName(cmd)
	c, _, kc, err := resolveKafkaInstance(name)
	if err != nil {
		return nil, kc, err
	}
	m, err := podman.NewKafkaManager(c)
	if err != nil {
		return nil, kc, err
	}
	if running, _ := m.ContainerRunning(kc.ContainerName); !running {
		return nil, kc, fmt.Errorf("kafka %q is not running — 'pg addon start kafka --name %s' first", name, name)
	}
	return m, kc, nil
}

var kafkaTopicNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,249}$`)

func validKafkaTopicName(topic string) bool {
	return kafkaTopicNameRe.MatchString(topic) && topic != "." && topic != ".."
}

func runAddonKafkaTopicCreate(cmd *cobra.Command, args []string) error {
	topic := args[0]
	if !validKafkaTopicName(topic) {
		return fmt.Errorf("invalid topic name %q: use letters, digits, '.', '_' and '-' (max 249 chars)", topic)
	}
	m, kc, err := requireKafkaRunning(cmd)
	if err != nil {
		return err
	}
	createArgs := []string{"--create", "--topic", topic}
	if cmd.Flags().Changed("partitions") {
		partitions, _ := cmd.Flags().GetInt("partitions")
		if partitions < 1 {
			return fmt.Errorf("--partitions must be >= 1")
		}
		createArgs = append(createArgs, "--partitions", strconv.Itoa(partitions))
	}
	replicationFactor, _ := cmd.Flags().GetInt("replication-factor")
	if replicationFactor < 0 {
		return fmt.Errorf("--replication-factor must be >= 0 (0 = this instance's default)")
	}
	if replicationFactor == 0 {
		voters, err := config.KafkaVoters(kc)
		if err != nil {
			return err
		}
		replicationFactor = config.KafkaReplicationFactor(len(voters))
	}
	createArgs = append(createArgs, "--replication-factor", strconv.Itoa(replicationFactor))
	configs, _ := cmd.Flags().GetStringArray("config")
	for _, entry := range configs {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(key) == "" || strings.ContainsAny(entry, "\x00\r\n") {
			return fmt.Errorf("invalid --config %q: use KEY=VALUE", entry)
		}
		createArgs = append(createArgs, "--config", entry)
	}
	if out, err := m.Topic(&kc, createArgs...); err != nil {
		return fmt.Errorf("creating topic %q: %w (%s)", topic, err, strings.TrimSpace(out))
	}
	fmt.Printf("✓ topic %q created on kafka %q (replication factor %d)\n", topic, kc.Name, replicationFactor)
	return nil
}

func runAddonKafkaTopicList(cmd *cobra.Command, _ []string) error {
	m, kc, err := requireKafkaRunning(cmd)
	if err != nil {
		return err
	}
	listArgs := []string{"--list"}
	if exclude, _ := cmd.Flags().GetBool("exclude-internal"); exclude {
		listArgs = append(listArgs, "--exclude-internal")
	}
	out, err := m.Topic(&kc, listArgs...)
	if err != nil {
		return fmt.Errorf("listing topics: %w (%s)", err, strings.TrimSpace(out))
	}
	fmt.Print(out)
	return nil
}

func runAddonKafkaTopicDescribe(cmd *cobra.Command, args []string) error {
	m, kc, err := requireKafkaRunning(cmd)
	if err != nil {
		return err
	}
	out, err := m.Topic(&kc, "--describe", "--topic", args[0])
	if err != nil {
		return fmt.Errorf("describing topic %q: %w (%s)", args[0], err, strings.TrimSpace(out))
	}
	fmt.Print(out)
	return nil
}

func runAddonKafkaTopicDelete(cmd *cobra.Command, args []string) error {
	topic := args[0]
	m, kc, err := requireKafkaRunning(cmd)
	if err != nil {
		return err
	}
	yes, _ := cmd.Flags().GetBool("yes")
	if !yes && !confirmPrompt(fmt.Sprintf("Delete topic %q on kafka %q? All of its messages are deleted. [y/N] ", topic, kc.Name)) {
		fmt.Println("aborted")
		return nil
	}
	out, err := m.Topic(&kc, "--delete", "--topic", topic)
	if err != nil {
		return fmt.Errorf("deleting topic %q: %w (%s)", topic, err, strings.TrimSpace(out))
	}
	fmt.Printf("✓ topic %q deleted\n", topic)
	return nil
}

func runAddonKafkaClusterStatus(cmd *cobra.Command) error {
	m, kc, err := requireKafkaRunning(cmd)
	if err != nil {
		return err
	}
	out, err := m.ClusterStatus(&kc)
	if err != nil {
		return fmt.Errorf("reading metadata quorum: %w (%s)", err, strings.TrimSpace(out))
	}
	fmt.Print(out)
	return nil
}

// --- command tree ----------------------------------------------------------

var addonKafkaCmd = &cobra.Command{
	Use:   "kafka",
	Short: "Manage Kafka (KRaft) container instances",
	Long: `Manage Kafka add-on instances (official apache/kafka image).

Kafka 4.x is KRaft-only: the broker and metadata controller run in one process
(combined mode) and ZooKeeper is not used. pgcli pins a cluster id, renders
server.properties, bind-mounts the data dir and starts the container on the
host network. Single-node installs listen on 127.0.0.1 by default and have no
authentication — keep them on a trusted network or behind a firewall.

Subcommands:
  status    Show container state, endpoints, node/cluster ids and paths
  shell     Open bash in the container
  topic     Create, list, describe or delete topics
  cluster   Show the KRaft metadata quorum status

Examples:
  pg addon install kafka
  pg addon install kafka --name k1 --broker-port 9092 --controller-port 19093
  pg addon kafka topic create demo --partitions 3 --name k1
  pg addon kafka topic list --exclude-internal --name k1
  pg addon kafka topic describe demo --name k1
  pg addon kafka topic delete demo --name k1 --yes
  pg addon kafka cluster status --name k1
  pg addon kafka shell --name k1
  pg logs addon kafka --name k1 -f
  pg addon remove kafka --name k1 --clean-data`,
}

var addonKafkaStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Kafka container state, endpoints and paths",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonKafkaStatus(cmd) },
}

var addonKafkaShellCmd = &cobra.Command{
	Use:   "shell [-- COMMAND...]",
	Short: "Open bash (or run a command) in the Kafka container",
	Args:  cobra.ArbitraryArgs,
	RunE:  runAddonKafkaShell,
}

var addonKafkaTopicCmd = &cobra.Command{
	Use:   "topic",
	Short: "Create, list, describe and delete topics",
}

var addonKafkaTopicCreateCmd = &cobra.Command{
	Use:   "create NAME",
	Short: "Create a topic",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonKafkaTopicCreate,
}

var addonKafkaTopicListCmd = &cobra.Command{
	Use:   "list",
	Short: "List topics",
	RunE:  runAddonKafkaTopicList,
}

var addonKafkaTopicDescribeCmd = &cobra.Command{
	Use:   "describe NAME",
	Short: "Describe a topic's partitions and replicas",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonKafkaTopicDescribe,
}

var addonKafkaTopicDeleteCmd = &cobra.Command{
	Use:   "delete NAME",
	Short: "Delete a topic and all of its messages",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonKafkaTopicDelete,
}

var addonKafkaClusterCmd = &cobra.Command{
	Use:   "cluster",
	Short: "Inspect the KRaft metadata quorum",
}

var addonKafkaClusterStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the metadata quorum leader and voters",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonKafkaClusterStatus(cmd) },
}

func init() {
	addonCmd.AddCommand(addonKafkaCmd)
	addonKafkaCmd.AddCommand(addonKafkaStatusCmd, addonKafkaShellCmd, addonKafkaTopicCmd, addonKafkaClusterCmd)
	addonKafkaTopicCmd.AddCommand(addonKafkaTopicCreateCmd, addonKafkaTopicListCmd, addonKafkaTopicDescribeCmd, addonKafkaTopicDeleteCmd)
	addonKafkaClusterCmd.AddCommand(addonKafkaClusterStatusCmd)

	for _, sub := range []*cobra.Command{
		addonKafkaStatusCmd, addonKafkaShellCmd,
		addonKafkaTopicCreateCmd, addonKafkaTopicListCmd, addonKafkaTopicDescribeCmd, addonKafkaTopicDeleteCmd,
		addonKafkaClusterStatusCmd,
	} {
		sub.Flags().String("name", "", "kafka instance name (default \"kafka\")")
	}

	addonKafkaTopicCreateCmd.Flags().Int("partitions", 0, "number of partitions (default: broker num.partitions, 1)")
	addonKafkaTopicCreateCmd.Flags().Int("replication-factor", 0, "replication factor (0 = this instance's default: 1 single-node)")
	addonKafkaTopicCreateCmd.Flags().StringArray("config", nil, "topic config KEY=VALUE (repeatable)")
	addonKafkaTopicListCmd.Flags().Bool("exclude-internal", false, "hide Kafka's internal topics")
	addonKafkaTopicDeleteCmd.Flags().Bool("yes", false, "skip the confirmation prompt")

	addonInstallCmd.Flags().Int("broker-port", 0, "Kafka broker host port (0=auto-assign from kafka_broker_start_port, default 9092)")
	addonInstallCmd.Flags().Int("controller-port", 0, "Kafka KRaft controller host port (0=auto-assign from kafka_controller_start_port, default 19093)")
	addonInstallCmd.Flags().String("cluster-id", "", "Kafka KRaft cluster id to adopt (default: adopt meta.properties, else generate a random 22-char id)")
}
