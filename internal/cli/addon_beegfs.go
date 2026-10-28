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
// One config entry (addons.beegfs.<name>) is one filesystem ROLE SET: on a v1
// host all three containers — mgmtd → meta → storage — on a cross-host member
// (--roles storage) just its own subset. BeeGFSManager brings them up in that
// dependency order and tears them down in reverse, so every operation here
// speaks in terms of the cluster-as-this-host-runs-it, not a single container.
// Three rules from the daemon reality shape the flags:
//
//   - The listen ports are FIXED stock protocol ports (8008/8010/8005/8003).
//     mgmtd's two ports can move (--beegfs-mgmtd-port/--beegfs-mgmtd-grpc-port);
//     meta and storage have NO supported listen-port override in 8.4, so there
//     is no --port knob for them and exactly one filesystem can exist per host
//     (BeeGFSManager.EnsureCluster guards the second one out). Cross-host, each
//     host binds its own 8005/8003, so scaling means adding roles on OTHER
//     hosts — each runs its own local `pg addon install beegfs --roles ...`;
//     pgcli never SSHes anywhere (the daemons self-register via sysMgmtdHost).
//   - The client is a kernel module and is never installed or mounted by
//     pgcli. `pg addon beegfs mount-hint` renders the exact config a host with
//     a beegfs-client package needs; `pg addon beegfs join-hint` renders the
//     verbatim per-host join commands.
//   - The conn-auth secret is GENERATED only on a host that runs mgmtd —
//     a join host must be given the existing one (--password, see join-hint);
//     pgcli never invents a secret for a member of an existing cluster.
//     The license follows the same mgmtd-only shape from the other side: it is
//     never generated (a ThinkParQ registration obtained out of band), only
//     CARRIED — --license-file / `pg addon beegfs license --set` store the PEM
//     and inject it into the mgmtd container alone, the only image whose daemon
//     verifies it.
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

// runAddonInstallBeeGFS installs (or revives) this host's slice of a BeeGFS
// filesystem: the mgmtd/meta/storage containers named by --roles (all three by
// default — the v1 converged filesystem), from ghcr.io/thinkparq/beegfs-<role>,
// wired to one shared connection-auth secret. Unlike every single-container
// addon there is no "already running → leave it alone" reuse of ONE container —
// the unit is the cluster AS THIS HOST RUNS IT, so reuse means "all my roles up
// → skip, all my roles down → start, anything else → EnsureCluster recreates
// the set in order". --force always takes the recreate path.
func runAddonInstallBeeGFS(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "beegfs"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	listenAddr, _ := cmd.Flags().GetString("listen")
	password, _ := cmd.Flags().GetString("password")
	rolesRaw, _ := cmd.Flags().GetString("roles")
	mgmtdHost, _ := cmd.Flags().GetString("mgmtd-host")
	nodeID, _ := cmd.Flags().GetInt("node-id")
	storageTargets, _ := cmd.Flags().GetStringArray("storage-target")
	licenseFile, _ := cmd.Flags().GetString("license-file")
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
	if cmd.Flags().Changed("roles") {
		var roles []string
		for r := range strings.SplitSeq(rolesRaw, ",") {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			if !config.ValidBeeGFSService(r) {
				return fmt.Errorf("--roles: unknown service %q (valid: mgmtd, meta, storage)", r)
			}
			roles = append(roles, r)
		}
		if len(roles) == 0 {
			return fmt.Errorf("--roles is empty — omit the flag to run the full converged (mgmtd+meta+storage) filesystem")
		}
		existing.Roles = roles
	}
	if cmd.Flags().Changed("mgmtd-host") {
		existing.MgmtdHost = strings.TrimSpace(mgmtdHost)
	}
	if cmd.Flags().Changed("node-id") {
		existing.NodeID = nodeID
	}
	if cmd.Flags().Changed("storage-target") {
		existing.StorageTargetDirs = storageTargets
	}
	if cmd.Flags().Changed("license-file") {
		pem, err := readBeeGFSLicense(licenseFile)
		if err != nil {
			return err
		}
		existing.License = pem
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
	// Same belt for the cross-host fields — config.Validate() only runs on the
	// `pg config` path, so an install must not be able to start a meta/storage
	// that would fall back to dial-able-but-wrong 127.0.0.1.
	if !bc.RunsMgmtd() && bc.MgmtdHost == "" {
		return fmt.Errorf("--mgmtd-host is required when --roles excludes mgmtd: this host's meta/storage daemons need the address to register against")
	}
	// A join host must carry the cluster's EXISTING secret — a generated one
	// could only ever fail registration, and pgcli never invents secrets for
	// members of an existing cluster. (ConnAuth can only be empty here on a
	// join path: every reuse path needs the stored secret anyway.)
	if !bc.RunsMgmtd() && bc.ConnAuth == "" {
		return fmt.Errorf("joining filesystem %q needs the cluster's connection-auth secret: pass --password <secret> (retrieve it on the mgmtd host with `pg addon password beegfs`, or render the full join command with `pg addon beegfs join-hint --name %s`)", name, name)
	}
	// The license is verified by mgmtd alone (only its image ships the license
	// library) — a join host carrying one would silently never load it.
	if !bc.RunsMgmtd() && bc.License != "" {
		return fmt.Errorf("--license-file is only valid on the host running mgmtd: the management daemon is what verifies the certificate (load it there with `pg addon beegfs license --set`, then join hosts need nothing)")
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
	// resolved would bake an empty one into the daemons. Existence of the
	// first role in this host's startup order is the discriminator (v1: the
	// mgmtd container; a join host: e.g. its storage container).
	ownRoles := bc.RolesInOrder()
	firstExists, err := bm.ContainerExists(bc.ContainerName(ownRoles[0]))
	if err != nil {
		return err
	}
	skipped := false
	switch {
	case st.AllUp() && !force:
		skipped = true
		fmt.Printf("-> BeeGFS filesystem %q already running on this host (%s); skipping creation. Use --force to recreate these containers, or `pg addon beegfs check` for health.\n", name, strings.Join(ownRoles, " + "))
	case st.AllDown() && !force && firstExists:
		// Containers exist and are stopped (or partially missing): start in
		// dependency order — the lighter path, no image pull, no recreate.
		fmt.Printf("-> BeeGFS filesystem is stopped; starting this host's roles (%s)...\n", strings.Join(ownRoles, " → "))
		if err := bm.StartCluster(&bc); err != nil {
			return err
		}
	default:
		if bc.ConnAuth == "" {
			// Only a host running mgmtd may generate the secret (the join case
			// was refused by the gate above).
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
	// mgmtdTarget is what this host's meta/storage dial as sysMgmtdHost: the
	// stored address on a join host, this host's display address otherwise
	// (mirrors the manager's own target resolution).
	mgmtdTarget := bc.MgmtdHost
	if mgmtdTarget == "" {
		mgmtdTarget = beegfsDisplayHost(bc.Listen)
	}
	containers := make([]string, 0, len(ownRoles))
	for _, role := range ownRoles {
		containers = append(containers, bc.ContainerName(role))
	}
	fmt.Printf("  Roles:      %s (on this host)\n", strings.Join(ownRoles, ", "))
	fmt.Printf("  Containers: %s\n", strings.Join(containers, ", "))
	fmt.Printf("  Images:     %s-{%s}:%s\n", config.BeeGFSImageRepo, strings.Join(ownRoles, ","), bc.ImageTag)
	localNote := " (remote — this host joins it)"
	if bc.RunsMgmtd() {
		localNote = " (this host)"
	}
	fmt.Printf("  Mgmtd:      %s:%d (BeeMsg) / %s:%d (gRPC)%s\n", mgmtdTarget, bc.MgmtdPort, mgmtdTarget, bc.MgmtdGRPCPort, localNote)
	if bc.RunsService(podman.BeeGFSServiceMeta) || bc.RunsService(podman.BeeGFSServiceStorage) {
		if bc.RunsService(podman.BeeGFSServiceStorage) {
			fmt.Printf("  Node ID:    %d (storage targets %s)\n", bc.NodeID, strings.Join(bm.DataDirs(&bc, podman.BeeGFSServiceStorage), ", "))
		} else {
			fmt.Printf("  Node ID:    %d\n", bc.NodeID)
		}
	}
	fmt.Printf("  Ports:      meta %d, storage %d (fixed protocol ports; one filesystem per host, roles may spread across hosts)\n", bc.MetaPort, bc.StoragePort)
	for _, role := range ownRoles {
		if role == podman.BeeGFSServiceStorage {
			continue // its dirs printed under Node ID
		}
		fmt.Printf("  Data (%s): %s\n", role, bm.DataDir(&bc, role))
	}
	fmt.Println()
	if bc.ConnAuth != "" {
		fmt.Printf("  Conn-auth secret: %s\n", bc.ConnAuth)
		fmt.Println("  (Shared by every daemon in the filesystem and every client's /etc/beegfs/conn.auth. Retrieve it later with `pg addon password beegfs`.)")
	} else {
		fmt.Println("  Conn-auth: none in the config — the running containers carry whatever they were created with.")
	}
	fmt.Println()
	fmt.Println("  TLS: mgmtd runs with --tls-disable true (v1 default) — clients must pass --tls-disable too.")
	if bc.License != "" {
		fmt.Printf("  License:    PEM carried in pg.yaml, loaded by mgmtd (mgmtd-only) — `pg addon beegfs license --name %s` shows what it verified.\n", name)
	} else {
		fmt.Println("  License: unlicensed 8.4 warns on every command and caps client mounts at five; a community/enterprise")
		fmt.Println("           license is a ThinkParQ registration you obtain out of band (URL from `beegfs license`) — pgcli")
		fmt.Println("           never generates one, but carries a downloaded PEM via --license-file / `pg addon beegfs license --set`.")
	}
	fmt.Println()
	fmt.Printf("  Mount a client (kernel module, NOT installed by pgcli — the Linux host needs the beegfs-client package):\n")
	fmt.Printf("      pg addon beegfs mount-hint --name %s\n", name)
	fmt.Println("  prints the exact beegfs-client.conf / beegfs-mounts.conf / conn.auth contents.")
	if bc.RunsMgmtd() {
		fmt.Println()
		fmt.Printf("  Add storage/meta on ANOTHER host (each runs pgcli locally; pgcli never SSHes — the daemons self-register):\n")
		fmt.Printf("      pg addon beegfs join-hint --name %s\n", name)
		fmt.Println("  prints the verbatim per-host install commands with this filesystem's mgmtd address and conn-auth.")
	}
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

	roles := bc.RolesInOrder()
	rev := make([]string, len(roles))
	for i, r := range roles {
		rev[len(roles)-1-i] = r
	}
	fmt.Printf("-> Removing BeeGFS filesystem %q on this host (%s)...\n", name, strings.Join(rev, " → "))
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
	fmt.Printf("-> Starting BeeGFS filesystem %q (this host's roles: %s)...\n", name, strings.Join(bc.RolesInOrder(), " → "))
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
	roles := bc.RolesInOrder()
	rev := make([]string, len(roles))
	for i, r := range roles {
		rev[len(roles)-1-i] = r
	}
	fmt.Printf("-> Stopping BeeGFS filesystem %q (%s)...\n", name, strings.Join(rev, " → "))
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

// readBeeGFSLicense carries a ThinkParQ license certificate into the config:
// pgcli never generates a license (it is a system-bound registration the user
// obtains out of band from `beegfs license --get`), it only moves the PEM the
// user downloaded. The BEGIN/END sniff is a shape check to catch feeding a
// private key or a random file — the real verification is mgmtd's, at load.
func readBeeGFSLicense(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading license file: %w", err)
	}
	pem := strings.TrimSpace(string(data))
	if pem == "" {
		return "", fmt.Errorf("license file %s is empty", path)
	}
	if !strings.Contains(pem, "BEGIN") || !strings.Contains(pem, "END") {
		return "", fmt.Errorf("%s does not look like a PEM license certificate (no BEGIN/END block) — pass the .pem ThinkParQ's portal gave you", path)
	}
	return pem, nil
}

// runAddonBeeGFSLicense reports or updates the filesystem's license. The
// certificate lives on the mgmtd host only (its image is the only one shipping
// libbeegfs_license.so), and the container env is baked at create time, so
// --set/--clear reload via RestartRole(mgmtd) — the join daemons simply
// re-register, clients keep doing IO throughout.
func runAddonBeeGFSLicense(cmd *cobra.Command) error {
	setFile, _ := cmd.Flags().GetString("set")
	clear, _ := cmd.Flags().GetBool("clear")
	if setFile != "" && clear {
		return fmt.Errorf("--set and --clear are mutually exclusive")
	}
	if cmd.Flags().Changed("set") && setFile == "" {
		return fmt.Errorf("--set needs the path to a ThinkParQ license .pem")
	}

	bc, bm, err := resolveBeeGFSConfig(cmd)
	if err != nil {
		return err
	}
	if !bc.RunsMgmtd() {
		return fmt.Errorf("the license lives on the host running mgmtd — its daemon is what verifies the certificate; this host's roles are %s", strings.Join(bc.RolesInOrder(), ", "))
	}

	path := cfgPath
	if setFile != "" || clear {
		if setFile != "" {
			pem, err := readBeeGFSLicense(setFile)
			if err != nil {
				return err
			}
			bc.License = pem
		} else {
			bc.License = ""
		}
		cfg.Addons.BeeGFS[bc.Name] = *bc
		if err := cfg.Save(path); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		if clear {
			fmt.Printf("-> License removed from %q; reloading the mgmtd container unlicensed...\n", bc.Name)
		} else {
			fmt.Printf("-> License stored in %s; reloading the mgmtd container to load it...\n", path)
		}
		if err := bm.RestartRole(bc, podman.BeeGFSServiceMgmtd); err != nil {
			return err
		}
	}

	// Status, in both modes: what the config carries, and what mgmtd itself
	// said about the certificate when it started.
	if bc.License == "" {
		fmt.Printf("BeeGFS filesystem %q: no license in pg.yaml (unlicensed 8.4 caps client mounts at five and refuses enterprise features)\n", bc.Name)
	} else {
		fmt.Printf("BeeGFS filesystem %q: license PEM carried in pg.yaml (%d bytes), injected into the mgmtd container as BEEGFS_LICENSE_FILE_DATA\n", bc.Name, len(bc.License))
	}
	st, err := bm.ClusterStatus(bc)
	if err != nil {
		return err
	}
	if !st.Up(podman.BeeGFSServiceMgmtd) {
		fmt.Println("mgmtd is not running — nothing to report from the daemon's side; start it to load/verify the license.")
		return nil
	}
	logs, err := bm.RoleLogs(bc, podman.BeeGFSServiceMgmtd)
	if err != nil {
		return err
	}
	// mgmtd re-logs its verification failure on every registration request —
	// the same message over and over, differing only by timestamp. Collapse by
	// the message text (the leading "[...] LEVEL" stripped) and count, so the
	// status stays readable however long the container has been up.
	var order []string          // message texts, in first-seen order
	count := map[string]int{}   // message text -> occurrences
	full := map[string]string{} // message text -> first full line
	for line := range strings.SplitSeq(logs, "\n") {
		if !beegfsLicenseLogLine(line) {
			continue
		}
		trimmed := strings.TrimSpace(line)
		key := trimmed
		if _, after, found := strings.Cut(trimmed, "] "); found {
			key = after
		}
		if count[key] == 0 {
			order = append(order, key)
			full[key] = trimmed
		}
		count[key]++
	}
	if len(order) == 0 {
		if bc.License == "" {
			fmt.Println("  mgmtd logs no license lines — that is the unlicensed state (quota/mount-cap warnings aside).")
		} else {
			fmt.Println("  mgmtd logged no license line — the container may predate the license; `pg addon beegfs license --set <pem>` re-loads it.")
		}
		return nil
	}
	for _, key := range order {
		if count[key] > 1 {
			fmt.Printf("  %s   (x%d)\n", full[key], count[key])
		} else {
			fmt.Println("  " + full[key])
		}
	}
	return nil
}

// beegfsLicenseLogLine picks the mgmtd log lines that speak about the license:
// the success marker, and any ERROR/WARNING whose text mentions it. Exported
// nowhere on purpose — `pg logs addon beegfs --service mgmtd` shows everything.
func beegfsLicenseLogLine(line string) bool {
	l := strings.ToLower(line)
	if !strings.Contains(l, "license") {
		return false
	}
	return strings.Contains(l, "error") || strings.Contains(l, "warning") ||
		strings.Contains(line, "Successfully loaded license certificate")
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
	fmt.Printf("\n(roles on this host: %s", strings.Join(bc.RolesInOrder(), ", "))
	if bc.MgmtdHost != "" {
		fmt.Printf("; mgmtd is remote at %s:%d)", bc.MgmtdHost, bc.MgmtdPort)
	} else {
		fmt.Print(")")
	}
	fmt.Println(" — up/registered are read from the daemons' own log lines: mgmtd serves once it logs its shutdown-wait, meta/storage log registration completion — no in-container beegfs CLI exists in these images.)")
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

	// MgmtdHost (set on a join host's entry) wins; otherwise this host's own
	// Listen address is the mgmtd.
	mgmtd := bc.MgmtdHost
	wildcard := false
	if mgmtd == "" {
		// 0.0.0.0 is not dial-able: for the hint, the operator's reachable
		// address is what clients need — tell them instead of printing the
		// wildcard.
		wildcard = bc.Listen == "" || bc.Listen == "0.0.0.0" || bc.Listen == "::"
		mgmtd = beegfsDisplayHost(bc.Listen)
	}
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
	fmt.Printf("3. /etc/beegfs/beegfs-mounts.conf (one mount per line, fields: <mountpoint> <client-conf> <fstype>):\n")
	fmt.Printf("   /mnt/beegfs /etc/beegfs/beegfs-client.conf beegfs\n\n")
	fmt.Println("Then: systemctl restart beegfs-client   (or /etc/init.d/beegfs-client rebuild + restart on non-systemd hosts)")
	fmt.Println("Verify from the client:  df -h /mnt/beegfs  &&  echo hi > /mnt/beegfs/t && cat /mnt/beegfs/t")
	mgmtdAddr := fmt.Sprintf("%s:%d", mgmtd, bc.MgmtdGRPCPort)
	if wildcard {
		mgmtdAddr = "<mgmtd-host-ip>:" + fmt.Sprint(bc.MgmtdGRPCPort)
	}
	fmt.Println("Server-side view (beegfs-tools, TLS-disabled form; lists every node incl. clients):  beegfs --tls-disable --mgmtd-addr=" + mgmtdAddr + " node list")
	return nil
}

// runAddonBeeGFSJoinHint renders the verbatim per-host install commands the rest
// of the cluster needs — pure local output, like mount-hint. It is meant to run
// on the host that holds the mgmtd entry (that is where ConnAuth lives); the
// model is declarative per-host execution: every other host runs its OWN pgcli
// install with --roles, and the daemons self-register via sysMgmtdHost. pgcli
// never SSHes anywhere.
func runAddonBeeGFSJoinHint(cmd *cobra.Command) error {
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
	if bc.ConnAuth == "" {
		return fmt.Errorf("filesystem %q has no conn-auth secret in this host's config — run join-hint where it was installed (`pg addon password beegfs` retrieves it)", name)
	}

	// The address joiners dial: a stored MgmtdHost wins, else this host's own
	// Listen — and the wildcard becomes a placeholder, since 0.0.0.0 cannot be
	// dialed from another machine.
	addr := bc.MgmtdHost
	if addr == "" {
		if bc.Listen == "" || bc.Listen == "0.0.0.0" || bc.Listen == "::" {
			addr = "<mgmtd-host-ip>"
		} else {
			addr = bc.Listen
		}
	}

	// The movable mgmtd ports ride along only when off stock — joiners inherit
	// the BeeMsg port via connMgmtdPort, and it is part of the command only so
	// the rendered line is copy-paste exact.
	portFlags := ""
	if bc.MgmtdPort != config.DefaultBeeGFSMgmtdPort {
		portFlags += fmt.Sprintf(" --beegfs-mgmtd-port %d", bc.MgmtdPort)
	}
	if bc.MgmtdGRPCPort != config.DefaultBeeGFSMgmtdGRPCPort {
		portFlags += fmt.Sprintf(" --beegfs-mgmtd-grpc-port %d", bc.MgmtdGRPCPort)
	}

	fmt.Printf("Join filesystem %q from ANOTHER host — each host runs pgcli locally (pgcli never SSHes; the daemons self-register into mgmtd at %s):\n\n", name, addr)
	fmt.Printf("Storage node (one or more target dirs, one target per disk — repeat --storage-target):\n")
	fmt.Printf("  pg addon install beegfs --name %s --roles storage --mgmtd-host %s%s \\\n", name, addr, portFlags)
	fmt.Printf("      --node-id <n> --password %s --storage-target <mnt-disk-1> [--storage-target <mnt-disk-2> ...]\n\n", bc.ConnAuth)
	fmt.Printf("Metadata node (only if this filesystem's meta is not yet on some host):\n")
	fmt.Printf("  pg addon install beegfs --name %s --roles meta --mgmtd-host %s%s --node-id <n> --password %s\n\n", name, addr, portFlags, bc.ConnAuth)
	fmt.Printf("<n> is this host's BeeGFS node ID (1..655); its storage targets derive from it (node-id 2 → targets 201, 202, ...). The conn-auth secret above is the SAME every daemon and client carries — `pg addon password beegfs` retrieves it. Verify after joining, from any host with beegfs-tools: `beegfs --tls-disable --mgmtd-addr=%s:%d node list`.\n", addr, bc.MgmtdGRPCPort)
	if bc.License != "" {
		fmt.Println("This filesystem carries a license — join hosts need nothing: only mgmtd ships the license library and verifies the certificate for the whole cluster.")
	}
	return nil
}

// ---------------------------------------------------------------------------
// commands
// ---------------------------------------------------------------------------

var addonBeeGFSCmd = &cobra.Command{
	Use:   "beegfs",
	Short: "BeeGFS parallel-filesystem addon: check cluster health, render client mount / join configs",
	Long: `Inspect a BeeGFS filesystem addon — pgcli's parallel filesystem, run as
managed containers (mgmtd + meta + storage; a cross-host cluster stores the
same filesystem name on each host with only the roles that host runs).

Subcommands:
  check       Aggregate container state + each daemon's own registration log line
  license     Show the mgmtd's loaded ThinkParQ license, or hot-swap/clear it
  mount-hint  Print the exact beegfs-client config for mounting from a Linux host
  join-hint   Print the verbatim per-host install commands for adding roles on
              other hosts (run on the mgmtd host)

Examples:
  pg addon beegfs check
  pg addon beegfs license --name fs1
  pg addon beegfs license --name fs1 --set /path/to/license.pem
  pg addon beegfs mount-hint --name fs1
  pg addon beegfs join-hint --name fs1`,
}

var addonBeeGFSCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Report a BeeGFS filesystem's cluster health",
	Long: `Report which of THIS host's BeeGFS containers (mgmtd/meta/storage, per the
configured roles) are running and whether each logged its readiness marker —
mgmtd "Waiting for shutdown signal" (serving), meta/storage "Registration and
management info download complete" (joined the cluster). The images ship no
beegfs-ctl/check-servers, so the daemons' own logs are the authoritative
in-container signal; for a mounted client's view use beegfs-utils' beegfs CLI
against the mgmtd gRPC port.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonBeeGFSCheck(cmd)
	},
}

var addonBeeGFSLicenseCmd = &cobra.Command{
	Use:   "license",
	Short: "Show or hot-swap a BeeGFS filesystem's ThinkParQ license (mgmtd host)",
	Long: `Report the license state of THIS host's filesystem: whether pg.yaml
carries a license PEM, and what the mgmtd daemon logged when it tried to
load it ("Successfully loaded license certificate", or the errors from a
bad/expired one). --set <pem> stores a downloaded certificate and reloads
the mgmtd container to pick it up (the env only lands at creation, so the
reload recreates mgmtd — meta/storage and clients re-register on their own);
--clear removes it again.

pgcli never GENERATES a license: it is a system-bound registration you
obtain out of band from ThinkParQ (` + "`beegfs license --get`" + ` prints the
URL, the portal hands back a .pem — community licenses are free, enterprise
ones paid). Only the mgmtd host may carry one: its image is the only one
shipping the license library, and its daemon verifies the certificate for
the whole filesystem.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonBeeGFSLicense(cmd)
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

var addonBeeGFSJoinHintCmd = &cobra.Command{
	Use:   "join-hint",
	Short: "Print the per-host commands for adding roles on other hosts",
	Long: `Render the verbatim 'pg addon install beegfs' commands the other hosts
of a cross-host filesystem need — storage nodes (optionally multiple
--storage-target disks each) and a metadata node, all pointed at this
filesystem's mgmtd address with its conn-auth secret. Run it on the host that
installed the filesystem (that is where ConnAuth lives). Pure local output —
no container is touched; each target host runs its own pgcli install and the
daemons self-register into mgmtd (pgcli never SSHes).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonBeeGFSJoinHint(cmd)
	},
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

func init() {
	addonCmd.AddCommand(addonBeeGFSCmd)
	addonBeeGFSCmd.AddCommand(addonBeeGFSCheckCmd, addonBeeGFSLicenseCmd, addonBeeGFSMountHintCmd, addonBeeGFSJoinHintCmd)

	addonBeeGFSCheckCmd.Flags().String("name", "", "beegfs filesystem name (default \"beegfs\")")
	addonBeeGFSLicenseCmd.Flags().String("name", "", "beegfs filesystem name (default \"beegfs\")")
	addonBeeGFSLicenseCmd.Flags().String("set", "", "beegfs filesystem name's license: store this .pem and reload the mgmtd container")
	addonBeeGFSLicenseCmd.Flags().Bool("clear", false, "beegfs filesystem name's license: remove the stored PEM and reload mgmtd unlicensed")
	addonBeeGFSMountHintCmd.Flags().String("name", "", "beegfs filesystem name (default \"beegfs\")")
	addonBeeGFSJoinHintCmd.Flags().String("name", "", "beegfs filesystem name (default \"beegfs\")")

	// Install-time knobs. The meta/storage ports have NO flags on purpose —
	// 8.4 gives those daemons no listen-port override, so there is nothing to
	// set; only mgmtd's two movable ports get flags.
	addonInstallCmd.Flags().Int("beegfs-mgmtd-port", 0, "BeeGFS only: move the mgmtd BeeMsg port off 8008 (meta/storage re-target registration automatically; clients need connMgmtdPort=<port>). 0 = stock 8008")
	addonInstallCmd.Flags().Int("beegfs-mgmtd-grpc-port", 0, "BeeGFS only: move the mgmtd gRPC port off 8010 (what a TLS-disabled client's --mgmtd-addr dials). 0 = stock 8010")
	// v2 cross-host roles. Each host runs its own install with its subset;
	// the daemons self-register into mgmtd — pgcli never SSHes.
	addonInstallCmd.Flags().String("roles", "", "which services THIS host runs — BeeGFS: comma list of mgmtd,meta,storage (default: all three = the converged single-host filesystem; a subset without mgmtd must pass --mgmtd-host and --password). VMCluster: comma list of insert,select,storage (default: all three = converged; an insert/select-only host needs --storage-node to reach the cluster). VLCluster: comma list of insert,select,storage (default: all three = converged; an insert/select-only host MUST pass --storage-node host:storagePort)")
	addonInstallCmd.Flags().String("mgmtd-host", "", "BeeGFS only: address of the mgmtd host this host's meta/storage register against (required when --roles excludes mgmtd)")
	addonInstallCmd.Flags().Int("node-id", 0, "BeeGFS: this host's BeeGFS node ID for its meta/storage services (1..655; storage target IDs derive from it: node 2 → targets 201, 202, ...). 0 = stock 1 — pass it explicitly on every join host, IDs must be unique per service type across hosts. Kafka: this node's 1-based KRaft node ID in --cluster mode (unique per cluster, required on a fresh cluster install; single-node Kafka is always node 1)")
	addonInstallCmd.Flags().StringArray("storage-target", nil, "BeeGFS only: a host directory holding one storage target — repeat for multiple disks/targets (empty = the single default .../storage dir)")
	addonInstallCmd.Flags().String("license-file", "", "BeeGFS only: path to a ThinkParQ license .pem to carry into the mgmtd container (BEEGFS_LICENSE_FILE_DATA). mgmtd host only — pgcli never generates licenses; `pg addon beegfs license` shows/updates it later")
}
