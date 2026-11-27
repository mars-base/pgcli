package cli

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

// ---------------------------------------------------------------------------
// install logic — k3s
// ---------------------------------------------------------------------------

// runAddonInstallK3s installs one K3s node (server or agent) as a rootful
// Podman container on the host network. Every node needs a pre-generated
// token file: K3s would otherwise mint one per-cluster on first boot and the
// other members could not join.
func runAddonInstallK3s(cmd *cobra.Command) error {
	if cmd.Flags().Changed("dsn") || cmd.Flags().Changed("pg-name") {
		return errors.New("k3s is a local infra add-on: it runs on the host's network namespace and does not accept --dsn/--pg-name")
	}

	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "k3s"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	version, _ := cmd.Flags().GetString("version")
	role, _ := cmd.Flags().GetString("role")
	clusterInit, _ := cmd.Flags().GetBool("cluster-init")
	serverURL, _ := cmd.Flags().GetString("server")
	nodeName, _ := cmd.Flags().GetString("node-name")
	advertiseAddress, _ := cmd.Flags().GetString("advertise-address")
	tlsSAN, _ := cmd.Flags().GetStringArray("tls-san")
	disable, _ := cmd.Flags().GetStringArray("disable")
	clusterCIDR, _ := cmd.Flags().GetString("cluster-cidr")
	serviceCIDR, _ := cmd.Flags().GetString("service-cidr")
	kubeProxyArgs, _ := cmd.Flags().GetStringArray("kube-proxy-arg")
	tokenFile, _ := cmd.Flags().GetString("token-file")
	caDir, _ := cmd.Flags().GetString("ca-dir")
	registriesFile, _ := cmd.Flags().GetString("registries-file")
	mirrors, _ := cmd.Flags().GetStringArray("registry-mirror")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	kubeletDir, _ := cmd.Flags().GetString("kubelet-dir")
	cniDir, _ := cmd.Flags().GetString("cni-dir")
	logDir, _ := cmd.Flags().GetString("log-dir")
	logFile, _ := cmd.Flags().GetString("log-file")
	containerLogFile, _ := cmd.Flags().GetString("container-log-file")
	force, _ := cmd.Flags().GetBool("force")

	if cmd.Flags().Changed("image") && cmd.Flags().Changed("version") {
		return errors.New("--image and --version are mutually exclusive: --version builds docker.io/rancher/k3s:<tag>, --image overrides the full reference")
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

	if cfg.Addons.K3s == nil {
		cfg.Addons.K3s = make(map[string]config.K3sConfig)
	}
	existing, ok := cfg.Addons.K3s[name]
	if !ok {
		existing = config.K3sConfig{
			ContainerName: "pgcli-k3s" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}

	base := cfg.BaseDir
	if base == "" {
		base = platform.DefaultConfigDir()
	}

	if imageTag != "" {
		existing.ImageTag = imageTag
		existing.Version = ""
	} else if version != "" {
		existing.Version = version
		existing.ImageTag = ""
	}
	if cmd.Flags().Changed("role") {
		existing.Role = strings.ToLower(strings.TrimSpace(role))
	}
	if cmd.Flags().Changed("cluster-init") {
		existing.ClusterInit = clusterInit
	}
	if cmd.Flags().Changed("server") {
		existing.ServerURL = serverURL
	}
	if cmd.Flags().Changed("node-name") && nodeName != "" {
		existing.NodeName = nodeName
	}
	if cmd.Flags().Changed("advertise-address") {
		existing.AdvertiseAddress = advertiseAddress
	}
	if cmd.Flags().Changed("tls-san") {
		existing.TLSSAN = tlsSAN
	}
	if cmd.Flags().Changed("disable") {
		existing.Disable = disable
	}
	if cmd.Flags().Changed("cluster-cidr") {
		existing.ClusterCIDR = strings.TrimSpace(clusterCIDR)
	}
	if cmd.Flags().Changed("service-cidr") {
		existing.ServiceCIDR = strings.TrimSpace(serviceCIDR)
	}
	if cmd.Flags().Changed("kube-proxy-arg") {
		existing.KubeProxyArgs = kubeProxyArgs
	}
	if cmd.Flags().Changed("token-file") && tokenFile != "" {
		existing.TokenFile = podman.HostMountPath(tokenFile)
	}
	if cmd.Flags().Changed("ca-dir") {
		existing.CADir = ""
		if caDir != "" {
			existing.CADir = podman.HostMountPath(caDir)
		}
	}
	if cmd.Flags().Changed("registries-file") {
		existing.RegistriesFile = ""
		if registriesFile != "" {
			existing.RegistriesFile = podman.HostMountPath(registriesFile)
		}
	}
	if cmd.Flags().Changed("registry-mirror") {
		parsed, err := parseK3sRegistryMirrors(mirrors)
		if err != nil {
			return err
		}
		existing.RegistryMirrors = parsed
	}
	if dataDir != "" {
		existing.DataDir = resolveUnderBase(base, dataDir)
	}
	if kubeletDir != "" {
		existing.KubeletDir = resolveUnderBase(base, kubeletDir)
	}
	if cniDir != "" {
		existing.CNIDir = resolveUnderBase(base, cniDir)
	}
	if logDir != "" {
		existing.LogDir = resolveUnderBase(base, logDir)
	}
	if cmd.Flags().Changed("log-file") {
		existing.LogFile = ""
		if logFile != "" {
			existing.LogFile = podman.HostMountPath(logFile)
		}
	}
	if cmd.Flags().Changed("container-log-file") {
		existing.ContainerLogFile = ""
		if containerLogFile != "" {
			existing.ContainerLogFile = podman.HostMountPath(containerLogFile)
		}
	}
	cfg.Addons.K3s[name] = existing

	cfg.ApplyDefaults()
	kc := cfg.Addons.K3s[name]
	if err := cfg.Validate(); err != nil {
		return err
	}

	km, err := podman.NewK3sManager(cfg)
	if err != nil {
		return fmt.Errorf("k3s manager: %w", err)
	}

	if force {
		fmt.Printf("-> Recreating k3s %s container %q...\n", kc.Role, kc.ContainerName)
		if err := km.RecreateContainer(&kc); err != nil {
			return err
		}
	} else {
		fmt.Printf("-> Installing k3s %s %q...\n", kc.Role, name)
		if err := km.EnsureContainer(&kc); err != nil {
			return err
		}
	}

	cfg.Addons.K3s[name] = kc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	fmt.Printf("✓ k3s %s installed: %q\n", kc.Role, name)
	fmt.Printf("  Container:   %s\n", kc.ContainerName)
	fmt.Printf("  Node:        %s\n", kc.NodeName)
	fmt.Printf("  Image:       %s\n", kc.ImageTag)
	if kc.Role == "server" {
		switch {
		case kc.ClusterInit:
			fmt.Println("  Datastore:   embedded etcd (--cluster-init, HA first server)")
		case kc.Joiner():
			fmt.Printf("  Joins:       %s\n", kc.ServerURL)
		default:
			fmt.Println("  Datastore:   SQLite (standalone server)")
		}
		if kc.AdvertiseAddress != "" {
			fmt.Printf("  API:         https://%s:6443\n", kc.AdvertiseAddress)
		}
		if kc.ClusterCIDR != "" {
			fmt.Printf("  Pod CIDR:    %s\n", kc.ClusterCIDR)
		}
		if kc.ServiceCIDR != "" {
			fmt.Printf("  Service CIDR: %s\n", kc.ServiceCIDR)
		}
	} else {
		fmt.Printf("  Joins:       %s\n", kc.ServerURL)
	}
	fmt.Printf("  Data dir:    %s\n", kc.DataDir)
	fmt.Printf("  Kubelet dir: %s\n", kc.KubeletDir)
	fmt.Printf("  CNI dir:     %s\n", kc.CNIDir)
	fmt.Printf("  Log dir:     %s\n", kc.LogDir)
	if kc.LogFile != "" {
		fmt.Printf("  Log file:    %s\n", kc.LogFile)
	}
	fmt.Printf("  Token file:  %s\n", kc.TokenFile)
	if len(kc.KubeProxyArgs) > 0 {
		fmt.Printf("  kube-proxy:  %s\n", strings.Join(kc.KubeProxyArgs, ", "))
	}
	if kc.CADir != "" {
		fmt.Printf("  Custom CA:   %s\n", kc.CADir)
	}
	if kc.RegistriesFile != "" {
		fmt.Printf("  Registries:  %s\n", kc.RegistriesFile)
	}
	if len(kc.RegistryMirrors) > 0 {
		registries := make([]string, 0, len(kc.RegistryMirrors))
		for registry := range kc.RegistryMirrors {
			registries = append(registries, registry)
		}
		sort.Strings(registries)
		fmt.Println("  Mirrors:")
		for _, registry := range registries {
			fmt.Printf("    %s -> %s\n", registry, strings.Join(kc.RegistryMirrors[registry], ", "))
		}
	}
	fmt.Println()
	if kc.Role == "server" {
		fmt.Printf("  Kubernetes API: https://%s:6443\n", k3sAPIHost(kc))
		fmt.Printf("  kubectl:        pg k3s kubectl --name %s -- get nodes -o wide\n", name)
		fmt.Printf("  kubeconfig:     pg k3s kubeconfig --name %s --output <file>\n", name)
		fmt.Printf("  Check readiness: pg k3s kubectl --name %s -- get --raw=/readyz\n", name)
	}
	return nil
}

// parseK3sRegistryMirrors turns repeated REGISTRY=ENDPOINT flags into the
// config map, merging endpoints for the same registry.
func parseK3sRegistryMirrors(specs []string) (map[string][]string, error) {
	out := make(map[string][]string, len(specs))
	for _, spec := range specs {
		registry, endpoint, ok := strings.Cut(spec, "=")
		registry = strings.TrimSpace(registry)
		endpoint = strings.TrimSpace(endpoint)
		if !ok || registry == "" || endpoint == "" {
			return nil, fmt.Errorf("--registry-mirror %q: expected REGISTRY=ENDPOINT, e.g. docker.io=https://<id>.mirror.aliyuncs.com", spec)
		}
		out[registry] = append(out[registry], endpoint)
	}
	return out, nil
}

// k3sAPIHost is the address clients use to reach a server's API.
func k3sAPIHost(kc config.K3sConfig) string {
	if kc.AdvertiseAddress != "" {
		return kc.AdvertiseAddress
	}
	if kc.NodeName != "" {
		return kc.NodeName
	}
	return "127.0.0.1"
}

// ---------------------------------------------------------------------------
// remove logic — k3s
// ---------------------------------------------------------------------------

func runAddonRemoveK3s(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "k3s"
	}
	cleanData, _ := cmd.Flags().GetBool("clean-data")

	cfg, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.K3s == nil {
		return errors.New("no k3s nodes configured")
	}
	kc, ok := cfg.Addons.K3s[name]
	if !ok {
		return fmt.Errorf("k3s %q not found", name)
	}

	km, err := podman.NewK3sManager(cfg)
	if err != nil {
		return fmt.Errorf("k3s manager: %w", err)
	}

	fmt.Printf("-> Removing k3s %s %q...\n", kc.Role, name)
	if err := km.Remove(&kc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.K3s, name)
	if len(cfg.Addons.K3s) == 0 {
		cfg.Addons.K3s = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ k3s %q removed\n", name)
	return nil
}

// ---------------------------------------------------------------------------
// start / stop logic — k3s
// ---------------------------------------------------------------------------

func runAddonStartK3s(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "k3s"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.K3s == nil {
		return errors.New("no k3s nodes configured (run 'pg addon install k3s --token-file <file>')")
	}
	kc, ok := cfg.Addons.K3s[name]
	if !ok {
		return fmt.Errorf("k3s %q not found (run 'pg addon install k3s --name %s --token-file <file>')", name, name)
	}
	km, err := podman.NewK3sManager(cfg)
	if err != nil {
		return fmt.Errorf("k3s manager: %w", err)
	}
	fmt.Printf("-> Starting k3s %s %q...\n", kc.Role, name)
	return km.StartContainer(&kc)
}

func runAddonStopK3s(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "k3s"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	kc, ok := cfg.Addons.K3s[name]
	if !ok {
		return fmt.Errorf("k3s %q not found", name)
	}
	km, err := podman.NewK3sManager(cfg)
	if err != nil {
		return fmt.Errorf("k3s manager: %w", err)
	}
	running, _ := km.ContainerRunning(kc.ContainerName)
	if !running {
		fmt.Printf("k3s %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping k3s %q...\n", name)
	if _, err := km.Stop(kc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ k3s %q stopped\n", name)
	return nil
}

// ---------------------------------------------------------------------------
// pg k3s — kubectl and kubeconfig against the local node
// ---------------------------------------------------------------------------

var k3sCmd = &cobra.Command{
	Use:   "k3s",
	Short: "Run kubectl against a local K3s add-on node and export its kubeconfig",
	Long: `Drive the K3s add-on installed by "pg addon install k3s".

K3s nodes run as rootful Podman containers on the host network, so pgcli
reaches their kubectl through "podman exec" — nothing has to be installed on
the host and no remote cluster is involved. Put kubectl flags after -- :
pg's own parser would otherwise swallow or reject them (-o is pg's config
output flag, -n is unknown to it).

Examples:
  pg k3s kubectl get nodes
  pg k3s kubectl -- get nodes -o wide
  pg k3s kubectl -- get pods -n kube-system
  pg k3s kubectl -- apply -f manifest.yaml
  pg k3s kubeconfig --output ~/.kube/config
  pg k3s kubeconfig --name k3s > kubeconfig.yaml`,
}

var k3sKubectlCmd = &cobra.Command{
	Use:   "kubectl [--name <node>] [-- <kubectl args...>]",
	Short: "Run kubectl inside a K3s server container",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		cfg, kc, err := k3sCLITarget(name)
		if err != nil {
			return err
		}
		km, err := podman.NewK3sManager(cfg)
		if err != nil {
			return fmt.Errorf("k3s manager: %w", err)
		}
		return km.Kubectl(&kc, args, stdinIsTerminal())
	},
}

var k3sKubeconfigCmd = &cobra.Command{
	Use:   "kubeconfig [--name <node>] [--output <file>]",
	Short: "Print a K3s server's admin kubeconfig (or write it to a file)",
	Long: `Read the admin kubeconfig from a K3s server node.

When the node was installed with --advertise-address, the loopback API URL in
the file is rewritten to that address so the kubeconfig works from other
hosts. --output writes it with mode 0600; without it the file goes to stdout.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		output, _ := cmd.Flags().GetString("output")
		cfg, kc, err := k3sCLITarget(name)
		if err != nil {
			return err
		}
		km, err := podman.NewK3sManager(cfg)
		if err != nil {
			return fmt.Errorf("k3s manager: %w", err)
		}
		kubeconfig, err := km.Kubeconfig(&kc)
		if err != nil {
			return err
		}
		if output == "" {
			fmt.Print(kubeconfig)
			return nil
		}
		if err := os.WriteFile(output, []byte(kubeconfig), 0o600); err != nil {
			return fmt.Errorf("writing kubeconfig to %s: %w", output, err)
		}
		fmt.Printf("✓ kubeconfig written to %s (mode 0600)\n", output)
		return nil
	},
}

func init() {
	k3sKubectlCmd.Flags().String("name", "", "k3s server node to run kubectl in (default: the only configured node)")
	k3sKubeconfigCmd.Flags().String("name", "", "k3s server node to read the kubeconfig from (default: the only configured node)")
	k3sKubeconfigCmd.Flags().StringP("output", "o", "", "write the kubeconfig to this file (mode 0600) instead of stdout")

	k3sCmd.AddCommand(k3sKubectlCmd, k3sKubeconfigCmd)
	rootCmd.AddCommand(k3sCmd)
}

// k3sCLITarget resolves the server node kubectl/kubeconfig operate on. An
// agent node has no admin kubeconfig, and with several configured nodes the
// choice is ambiguous.
func k3sCLITarget(name string) (*config.Config, config.K3sConfig, error) {
	c, _, err := loadAddonCfg()
	if err != nil {
		return nil, config.K3sConfig{}, err
	}
	if len(c.Addons.K3s) == 0 {
		return nil, config.K3sConfig{}, errors.New("no k3s nodes configured (run 'pg addon install k3s --token-file <file>')")
	}
	if name == "" {
		if len(c.Addons.K3s) > 1 {
			names := make([]string, 0, len(c.Addons.K3s))
			for k := range c.Addons.K3s {
				names = append(names, k)
			}
			sort.Strings(names)
			return nil, config.K3sConfig{}, fmt.Errorf("multiple k3s nodes configured (%s); pass --name", strings.Join(names, ", "))
		}
		for _, kc := range c.Addons.K3s {
			if kc.Role != "server" {
				return nil, config.K3sConfig{}, fmt.Errorf("k3s node %q is an agent; kubectl and kubeconfig are available on a server node", kc.Name)
			}
			return c, kc, nil
		}
	}
	kc, ok := c.Addons.K3s[name]
	if !ok {
		return nil, config.K3sConfig{}, fmt.Errorf("k3s node %q not found (use 'pg addon list' to see available)", name)
	}
	if kc.Role != "server" {
		return nil, config.K3sConfig{}, fmt.Errorf("k3s node %q is an agent; kubectl and kubeconfig are available on a server node", name)
	}
	return c, kc, nil
}
