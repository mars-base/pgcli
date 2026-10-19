package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

// ---------------------------------------------------------------------------
// beegfs — pgcli's first MULTI-CONTAINER addon
// ---------------------------------------------------------------------------
//
// One config entry (addons.beegfs.<name>) is a whole parallel filesystem: three
// containers — mgmtd → meta → storage — that BeeGFSManager brings up in that
// dependency order and tears down in reverse. Every operation here therefore
// speaks in terms of the cluster, not a single container, and two rules from
// the daemon reality shape the flags:
//
//   - The listen ports are FIXED stock protocol ports (8008/8010/8005/8003).
//     mgmtd's two ports can move (--beegfs-mgmtd-port/--beegfs-mgmtd-grpc-port);
//     meta and storage have NO supported listen-port override in 8.4, so there
//     is no --port knob for them and exactly one filesystem can exist per host
//     (BeeGFSManager.EnsureCluster guards the second one out).
//   - The client is a kernel module and is never installed or mounted by
//     pgcli. `pg addon beegfs mount-hint` renders the exact config a host with
//     a beegfs-client package needs.
//
// TLS is disabled on mgmtd (--tls-disable true, the v1 decision), so only
// clients passing the same --tls-disable can join; ConnAuth is the shared
// connection-auth secret every daemon and client must carry.

// beegfsDisplayHost renders a Listen address for humans: the wildcard becomes
// localhost, matching what every other addon's summary prints.
func beegfsDisplayHost(listen string) string {
	if listen == "" || listen == "0.0.0.0" || listen == "::" {
		return "localhost"
	}
	return listen
}

// runAddonInstallBeeGFS installs (or revives) a converged single-host BeeGFS
// filesystem: three containers from ghcr.io/thinkparq/beegfs-{mgmtd,meta,
// storage} wired to one shared connection-auth secret. Unlike every single-
// container addon there is no "already running → leave it alone" reuse of ONE
// container — the unit is the cluster, so reuse means "all three up → skip,
// all three down → start, anything else → EnsureCluster recreates the whole
// set in order". --force always takes the recreate path.
func runAddonInstallBeeGFS(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "beegfs"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	listenAddr, _ := cmd.Flags().GetString("listen")
	password, _ := cmd.Flags().GetString("password")
	force, _ := cmd.Flags().GetBool("force")

	// The port knobs are Changed()-gated like every addon int flag: a plain
	// re-install must not zero stored values. Only mgmtd's two can move.
	mgmtdPort, _ := cmd.Flags().GetInt("beegfs-mgmtd-port")
	mgmtdGRPCPort, _ := cmd.Flags().GetInt("beegfs-mgmtd-grpc-port")
	for _, p := range []struct {
		flag string
		val  int
	}{{"beegfs-mgmtd-port", mgmtdPort}, {"beegfs-mgmtd-grpc-port", mgmtdGRPCPort}} {
		if cmd.Flags().Changed(p.flag) && (p.val < 1 || p.val > 65535) {
			return fmt.Errorf("--%s must be 1-65535, got %d", p.flag, p.val)
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

	if cfg.Addons.BeeGFS == nil {
		cfg.Addons.BeeGFS = make(map[string]config.BeeGFSConfig)
	}
	existing, ok := cfg.Addons.BeeGFS[name]
	if !ok {
		existing = config.BeeGFSConfig{
			ContainerPrefix: "pgcli-beegfs" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:            name,
		}
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if password != "" {
		existing.ConnAuth = password
	}
	if cmd.Flags().Changed("beegfs-mgmtd-port") {
		existing.MgmtdPort = mgmtdPort
	}
	if cmd.Flags().Changed("beegfs-mgmtd-grpc-port") {
		existing.MgmtdGRPCPort = mgmtdGRPCPort
	}
	if dataDir != "" {
		// One flag lays out the whole filesystem: the three role dirs become
		// <dir>/{mgmtd,meta,storage}, matching the manager's default shape
		// under base_dir — operators never address the roles separately.
		existing.MgmtdDataDir = dataDir + "/mgmtd"
		existing.MetaDataDir = dataDir + "/meta"
		existing.StorageDataDir = dataDir + "/storage"
	}
	cfg.Addons.BeeGFS[name] = existing

	cfg.ApplyDefaults()
	bc := cfg.Addons.BeeGFS[name]

	// Belt and braces for a MetaPort/StoragePort hand-edited into pg.yaml: the
	// daemons cannot bind anything but the stock ports, so refuse early with
	// the real reason instead of letting the meta container crash-loop on 8005.
	if bc.MetaPort != config.DefaultBeeGFSMetaPort || bc.StoragePort != config.DefaultBeeGFSStoragePort {
		return fmt.Errorf("meta/storage ports are fixed by BeeGFS 8.4 (8005/8003, no daemon override exists) — got %d/%d in the config; remove the pinned values", bc.MetaPort, bc.StoragePort)
	}

	bm, err := podman.NewBeeGFSManager(cfg)
	if err != nil {
		return fmt.Errorf("BeeGFS manager: %w", err)
	}

	// Cluster-granular reuse. The guard inside EnsureCluster (one filesystem
	// per host) also runs on the start path's eventual recreate, but checking
	// it here would need the manager's config view — StartCluster of a
	// stored, complete cluster cannot create a second filesystem anyway.
	st, err := bm.ClusterStatus(&bc)
	if err != nil {
		return err
	}
	// "AllDown" cannot tell a stopped cluster from a fresh install (nothing
	// exists is also all-down), and starting before the conn-auth secret is
	// generated would bake an empty one into the daemons. Existence of the
	// mgmtd container is the discriminator.
	mgmtdExists, err := bm.ContainerExists(bc.ContainerName(podman.BeeGFSServiceMgmtd))
	if err != nil {
		return err
	}
	skipped := false
	switch {
	case st.AllUp() && !force:
		skipped = true
		fmt.Printf("-> BeeGFS filesystem %q already running (mgmtd + meta + storage); skipping creation. Use --force to recreate the three containers, or `pg addon beegfs check` for health.\n", name)
	case st.AllDown() && !force && mgmtdExists:
		// Containers exist and are stopped (or partially missing): start in
		// dependency order — the lighter path, no image pull, no recreate.
		fmt.Println("-> BeeGFS filesystem is stopped; starting the cluster (mgmtd → meta → storage)...")
		if err := bm.StartCluster(&bc); err != nil {
			return err
		}
	default:
		if bc.ConnAuth == "" {
			pw, err := generatePassword(20)
			if err != nil {
				return fmt.Errorf("generating BeeGFS connection-auth secret: %w", err)
			}
			bc.ConnAuth = pw
		}
		revived := podman.MgmtdStoreExists(bm.DataDir(&bc, podman.BeeGFSServiceMgmtd))
		fmt.Printf("-> Preparing BeeGFS %s images (%s)...\n", bc.ImageTag, config.BeeGFSImageRepo)
		if err := bm.EnsureImage(&bc); err != nil {
			return err
		}
		if revived {
			fmt.Println("-> NOTE: reviving an existing filesystem store (mgmtd.sqlite present) — its registered nodes/targets and stored connection auth still apply; the --password you pass only reaches fresh daemons via conn.auth, the SQLite registry keeps whatever it had.")
		}
		if err := bm.EnsureCluster(&bc); err != nil {
			return err
		}
	}

	cfg.Addons.BeeGFS[name] = bc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	if skipped {
		fmt.Printf("✓ BeeGFS already present: %q\n", name)
	} else {
		fmt.Printf("✓ BeeGFS installed: %q\n", name)
	}
	fmt.Printf("  Containers: %s\n", strings.Join([]string{
		bc.ContainerName(podman.BeeGFSServiceMgmtd),
		bc.ContainerName(podman.BeeGFSServiceMeta),
		bc.ContainerName(podman.BeeGFSServiceStorage),
	}, ", "))
	fmt.Printf("  Images:     %s-{mgmtd,meta,storage}:%s\n", config.BeeGFSImageRepo, bc.ImageTag)
	fmt.Printf("  Mgmtd:      %s:%d (BeeMsg) / %s:%d (gRPC)\n", beegfsDisplayHost(bc.Listen), bc.MgmtdPort, beegfsDisplayHost(bc.Listen), bc.MgmtdGRPCPort)
	fmt.Printf("  Ports:      meta %d, storage %d (fixed protocol ports; one filesystem per host)\n", bc.MetaPort, bc.StoragePort)
	for _, role := range []string{podman.BeeGFSServiceMgmtd, podman.BeeGFSServiceMeta, podman.BeeGFSServiceStorage} {
		fmt.Printf("  Data (%s): %s\n", role, bm.DataDir(&bc, role))
	}
	fmt.Println()
	if bc.ConnAuth != "" {
		fmt.Printf("  Conn-auth secret: %s\n", bc.ConnAuth)
		fmt.Println("  (Shared by all three daemons and every client's /etc/beegfs/conn.auth. Retrieve it later with `pg addon password beegfs`.)")
	} else {
		fmt.Println("  Conn-auth: none in the config — the running containers carry whatever they were created with.")
	}
	fmt.Println()
	fmt.Println("  TLS: mgmtd runs with --tls-disable true (v1 default) — clients must pass --tls-disable too.")
	fmt.Println("  License: unlicensed 8.4 logs a quota warning but the filesystem works; a community/enterprise")
	fmt.Println("           license is a registration you obtain from ThinkParQ — pgcli never generates or injects one.")
	fmt.Println()
	fmt.Printf("  Mount a client (kernel module, NOT installed by pgcli — the Linux host needs the beegfs-client package):\n")
	fmt.Printf("      pg addon beegfs mount-hint --name %s\n", name)
	fmt.Println("  prints the exact beegfs-client.conf / beegfs-mounts.conf / conn.auth contents.")
	if bc.Listen == "0.0.0.0" {
		fmt.Println()
		fmt.Println("  NOTE: listening on every interface (a parallel filesystem is mounted from other machines) with TLS disabled — anyone reaching ports 8008/8010/8005/8003 who holds the conn-auth secret is a cluster member. Bind loopback or firewall the ports if this host is not private.")
		fmt.Printf("        loopback-only: pg addon install beegfs --name %s --listen 127.0.0.1 --force\n", name)
	}
	return nil
}

// runAddonRemoveBeeGFS removes the whole filesystem (three containers). Data
// dirs survive by default — a reinstall under the same name revives the
// registry and targets; --clean-data deletes all three.
func runAddonRemoveBeeGFS(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "beegfs"
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

	if cfg.Addons.BeeGFS == nil {
		return fmt.Errorf("no BeeGFS add-ons configured")
	}
	bc, ok := cfg.Addons.BeeGFS[name]
	if !ok {
		return fmt.Errorf("BeeGFS filesystem %q not found", name)
	}

	bm, err := podman.NewBeeGFSManager(cfg)
	if err != nil {
		return fmt.Errorf("BeeGFS manager: %w", err)
	}

	fmt.Printf("-> Removing BeeGFS filesystem %q (storage → meta → mgmtd)...\n", name)
	if err := bm.RemoveCluster(&bc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.BeeGFS, name)
	if len(cfg.Addons.BeeGFS) == 0 {
		cfg.Addons.BeeGFS = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ BeeGFS %q removed\n", name)
	if !cleanData {
		fmt.Println("  (data dirs kept — reinstall the same name revives the filesystem; --clean-data to delete them. A client mountpoint pointing at this host must be unmounted either way.)")
	}
	return nil
}

// runAddonStartBeeGFS starts an installed filesystem's three containers in
// dependency order.
func runAddonStartBeeGFS(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "beegfs"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.BeeGFS == nil {
		return fmt.Errorf("no BeeGFS add-ons configured (run 'pg addon install beegfs')")
	}
	bc, ok := cfg.Addons.BeeGFS[name]
	if !ok {
		return fmt.Errorf("BeeGFS filesystem %q not found (run 'pg addon install beegfs --name %s')", name, name)
	}
	bm, err := podman.NewBeeGFSManager(cfg)
	if err != nil {
		return fmt.Errorf("BeeGFS manager: %w", err)
	}
	fmt.Printf("-> Starting BeeGFS filesystem %q (mgmtd → meta → storage)...\n", name)
	return bm.StartCluster(&bc)
}

// runAddonStopBeeGFS stops the three containers in reverse dependency order.
func runAddonStopBeeGFS(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "beegfs"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.BeeGFS == nil {
		return fmt.Errorf("no BeeGFS add-ons configured")
	}
	bc, ok := cfg.Addons.BeeGFS[name]
	if !ok {
		return fmt.Errorf("BeeGFS filesystem %q not found", name)
	}
	bm, err := podman.NewBeeGFSManager(cfg)
	if err != nil {
		return fmt.Errorf("BeeGFS manager: %w", err)
	}
	st, err := bm.ClusterStatus(&bc)
	if err != nil {
		return err
	}
	if st.AllDown() {
		fmt.Printf("BeeGFS filesystem %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping BeeGFS filesystem %q (storage → meta → mgmtd)...\n", name)
	if err := bm.StopCluster(&bc); err != nil {
		return err
	}
	fmt.Printf("✓ BeeGFS filesystem %q stopped\n", name)
	return nil
}

// resolveBeeGFSConfig loads the config and returns the filesystem entry for
// --name (default "beegfs") plus a manager. requireRunning guards the `check`
// subcommand (a stopped cluster still answers check meaningfully — "stopped"
// IS the answer — so check does NOT require running; mount-hint is pure-local
// and never touches podman either).
func resolveBeeGFSConfig(cmd *cobra.Command) (*config.BeeGFSConfig, *podman.BeeGFSManager, error) {
	if err := loadConfigForDSN(); err != nil {
		return nil, nil, err
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "beegfs"
	}
	if cfg.Addons.BeeGFS == nil {
		return nil, nil, fmt.Errorf("no beegfs add-ons configured (run 'pg addon install beegfs')")
	}
	bc, ok := cfg.Addons.BeeGFS[name]
	if !ok {
		return nil, nil, fmt.Errorf("beegfs filesystem %q not found (use 'pg addon list' to see available)", name)
	}
	bm, err := podman.NewBeeGFSManager(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("beegfs manager: %w", err)
	}
	return &bc, bm, nil
}

func runAddonBeeGFSCheck(cmd *cobra.Command) error {
	bc, bm, err := resolveBeeGFSConfig(cmd)
	if err != nil {
		return err
	}
	out, err := bm.CheckServers(bc)
	if err != nil {
		return err
	}
	fmt.Print(out)
	fmt.Println("\n(up/registered are read from the daemons' own log lines: mgmtd serves once it logs its shutdown-wait, meta/storage log registration completion — no in-container beegfs CLI exists in these images.)")
	return nil
}

// runAddonBeeGFSMountHint renders the client-side config for one filesystem —
// pure local output, never touches podman, since the client is a host kernel
// module pgcli does not manage. The values mirror what the daemons were given:
// connMgmtdPort only when the BeeMsg port was moved off 8008.
func runAddonBeeGFSMountHint(cmd *cobra.Command) error {
	if err := loadConfigForDSN(); err != nil {
		return err
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "beegfs"
	}
	if cfg.Addons.BeeGFS == nil {
		return fmt.Errorf("no beegfs add-ons configured (run 'pg addon install beegfs')")
	}
	bc, ok := cfg.Addons.BeeGFS[name]
	if !ok {
		return fmt.Errorf("beegfs filesystem %q not found (use 'pg addon list' to see available)", name)
	}

	mgmtd := beegfsDisplayHost(bc.Listen)
	// 0.0.0.0 is not dial-able: for the hint, the operator's reachable address
	// is what clients need — tell them instead of printing the wildcard.
	wildcard := bc.Listen == "" || bc.Listen == "0.0.0.0" || bc.Listen == "::"
	if wildcard {
		mgmtd = "<mgmtd-host-ip>"
	}

	fmt.Printf("BeeGFS client config for filesystem %q — apply on a Linux host with the beegfs-client + beegfs-utils packages installed (pgcli never installs them; they need a matching kernel module / DKMS):\n\n", name)
	if wildcard {
		fmt.Printf("Replace <mgmtd-host-ip> with this host's address as clients see it. The cluster runs with TLS disabled: every beegfs CLI command below needs --tls-disable --mgmtd-addr=<mgmtd-host-ip>:%d.\n", bc.MgmtdGRPCPort)
		fmt.Println()
	}

	fmt.Printf("1. /etc/beegfs/conn.auth (mode 0600) — the SAME secret as the servers:\n")
	fmt.Printf("   %s\n\n", bc.ConnAuth)
	fmt.Println("2. /etc/beegfs/beegfs-client.conf — set in the main section:")
	if bc.MgmtdPort != config.DefaultBeeGFSMgmtdPort {
		fmt.Printf("   connMgmtdPort=%d\n", bc.MgmtdPort)
	}
	fmt.Printf("   sysMgmtdHost=%s\n\n", mgmtd)
	fmt.Printf("3. /etc/beegfs/beegfs-mounts.conf (one mount per line):\n")
	fmt.Printf("   /mnt/beegfs %s@%s:/ _sysName=<client-name>\n\n", name, mgmtd)
	fmt.Println("Then: systemctl restart beegfs-client   (or /etc/init.d/beegfs-client rebuild + restart on non-systemd hosts)")
	fmt.Println("Verify from the client:  df -h /mnt/beegfs  &&  echo hi > /mnt/beegfs/t && cat /mnt/beegfs/t")
	mgmtdAddr := fmt.Sprintf("%s:%d", mgmtd, bc.MgmtdGRPCPort)
	if wildcard {
		mgmtdAddr = "<mgmtd-host-ip>:" + fmt.Sprint(bc.MgmtdGRPCPort)
	}
	fmt.Println("Server-side view (beegfs-tools, TLS-disabled form; lists every node incl. clients):  beegfs --tls-disable --mgmtd-addr=" + mgmtdAddr + " node list")
	return nil
}

// ---------------------------------------------------------------------------
// commands
// ---------------------------------------------------------------------------

var addonBeeGFSCmd = &cobra.Command{
	Use:   "beegfs",
	Short: "BeeGFS parallel-filesystem addon: check cluster health, render client mount config",
	Long: `Inspect a BeeGFS filesystem addon — pgcli's converged single-host parallel
filesystem (three containers: mgmtd + meta + storage).

Subcommands:
  check       Aggregate container state + each daemon's own registration log line
  mount-hint  Print the exact beegfs-client config for mounting from a Linux host

Examples:
  pg addon beegfs check
  pg addon beegfs mount-hint --name fs1`,
}

var addonBeeGFSCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Report a BeeGFS filesystem's cluster health",
	Long: `Report which of the three containers (mgmtd/meta/storage) are running and
whether each service logged its readiness marker — mgmtd "Waiting for shutdown
signal" (serving), meta/storage "Registration and management info download
complete" (joined the cluster). The images ship no beegfs-ctl/check-servers,
so the daemons' own logs are the authoritative in-container signal; for a
mounted client's view use beegfs-utils' beegfs CLI against the mgmtd gRPC port.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonBeeGFSCheck(cmd)
	},
}

var addonBeeGFSMountHintCmd = &cobra.Command{
	Use:   "mount-hint",
	Short: "Print the beegfs-client mount config for a filesystem",
	Long: `Render the three files a Linux host needs to mount this filesystem with the
beegfs-client package: /etc/beegfs/conn.auth (the shared secret — same value
the daemons run with), the beegfs-client.conf lines (sysMgmtdHost, and
connMgmtdPort if mgmtd's port was moved), and a beegfs-mounts.conf entry.
Pure local output — no container is touched, since the client is a kernel
module pgcli never installs. The cluster runs with mgmtd TLS disabled, so
every beegfs CLI call needs --tls-disable --mgmtd-addr=<host>:<grpc-port>.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonBeeGFSMountHint(cmd)
	},
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

func init() {
	addonCmd.AddCommand(addonBeeGFSCmd)
	addonBeeGFSCmd.AddCommand(addonBeeGFSCheckCmd, addonBeeGFSMountHintCmd)

	addonBeeGFSCheckCmd.Flags().String("name", "", "beegfs filesystem name (default \"beegfs\")")
	addonBeeGFSMountHintCmd.Flags().String("name", "", "beegfs filesystem name (default \"beegfs\")")

	// Install-time knobs. The meta/storage ports have NO flags on purpose —
	// 8.4 gives those daemons no listen-port override, so there is nothing to
	// set; only mgmtd's two movable ports get flags.
	addonInstallCmd.Flags().Int("beegfs-mgmtd-port", 0, "BeeGFS only: move the mgmtd BeeMsg port off 8008 (meta/storage re-target registration automatically; clients need connMgmtdPort=<port>). 0 = stock 8008")
	addonInstallCmd.Flags().Int("beegfs-mgmtd-grpc-port", 0, "BeeGFS only: move the mgmtd gRPC port off 8010 (what a TLS-disabled client's --mgmtd-addr dials). 0 = stock 8010")
}
