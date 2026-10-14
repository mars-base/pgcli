package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

// generateGarageAccessKey builds a Garage access key id: "GK" + 32 hex chars,
// the shape `openssl rand -hex 16` produces in Garage's own quick-start. Only
// the single-node path uses it (env seeding); a cluster mints its own key via
// `garage key create` once quorum forms.
func generateGarageAccessKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "GK" + hex.EncodeToString(b), nil
}

// generateGarageSecretKey builds a 64-hex-char secret — the shape Garage
// documents for GARAGE_DEFAULT_SECRET_KEY (`openssl rand -hex 32`), reused for
// the rpc_secret (also 32 random bytes, hex-encoded).
func generateGarageSecretKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// garageRole is which part an install plays in a cluster's life. It is a pure
// decision over the merged config so it can be unit-tested (garageInstallRole).
type garageRole int

const (
	garageSingle    garageRole = iota // no --cluster: v1 single-node box
	garageBootstrap                   // --cluster, no peers: first member, generates rpc_secret
	garageJoin                        // --cluster with peers: joins the existing cluster
)

func (r garageRole) String() string {
	switch r {
	case garageJoin:
		return "joining cluster"
	case garageBootstrap:
		return "bootstrapping cluster"
	default:
		return "single-node"
	}
}

// garageInstallRole decides the install role: single without --cluster, join
// when peers are already known (first --peer flag or persisted from an earlier
// run), bootstrap otherwise. A bootstrap re-run stays "bootstrap" (everything
// downstream is idempotent) — its practical difference from join is only that
// it has no peer to introduce itself to.
func garageInstallRole(cluster string, peers []string) garageRole {
	if cluster == "" {
		return garageSingle
	}
	if len(peers) > 0 {
		return garageJoin
	}
	return garageBootstrap
}

// appendPeers merges flag-provided peers into the persisted list, deduped.
func appendPeers(existing, added []string) []string {
	out := existing
	for _, p := range added {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// seedState is the outcome of the quorum-gated seeding attempt on a cluster
// member's install tail.
type garageSeedState int

const (
	garageSeedSkipped garageSeedState = iota // single-node: env seeding owns it
	garageSeedSeeded                         // this node just created key+bucket
	garageSeedByPeer                         // key already in the ring (another member seeded)
	garageSeedPending                        // quorum not reached yet — retry on a later install
)

// runGarageClusterTail drives everything that assembles a cluster member after
// its container is up: identity, peering, layout, and the quorum-gated seed.
// Every step is idempotent (garageAlready / KeyIDByName guards), so this
// tail also makes "re-run the same install command" the recovery for a lost
// layout-apply version race, and lets the LAST member to join do the seeding.
// It mutates gc (NodeID, RootUser/RootPassword) for the caller to persist.
func runGarageClusterTail(gm *podman.GarageManager, gc *config.GarageConfig) (garageSeedState, error) {
	fmt.Println("-> Waiting for the garage node identity (first boot generates the keypair)...")
	nodeID, err := gm.WaitForNodeID(gc, 30*time.Second)
	if err != nil {
		return garageSeedPending, err
	}
	// Garage prints `<64hex>@<addr>` when rpc_public_addr is set, but only the
	// bare hex on some paths — normalize so the persisted/cached identity is
	// always dialable by a fresh joiner (peers must introduce themselves by
	// id@addr before layout-driven discovery is available).
	if !strings.Contains(nodeID, "@") && gc.RPCPublicAddr() != "" {
		nodeID += "@" + gc.RPCPublicAddr()
	}
	gc.NodeID = nodeID
	fmt.Printf("  [OK] node id: %s\n", nodeID)

	for _, p := range gc.Peers {
		if p == nodeID {
			continue // a persisted peer can be ourselves after a re-install
		}
		fmt.Printf("-> Connecting to peer %s...\n", p)
		if err := gm.ConnectPeer(gc, p); err != nil {
			return garageSeedPending, err
		}
	}

	if gc.Zone != "" && gc.Capacity > 0 {
		fmt.Printf("-> Assigning layout role (zone %s, capacity %d GiB) and applying...\n", gc.Zone, gc.Capacity)
		if err := gm.LayoutAssign(gc, nodeID); err != nil {
			return garageSeedPending, err
		}
		if err := gm.LayoutApply(gc); err != nil {
			if !errors.Is(err, podman.ErrLayoutAwaitQuorum) {
				return garageSeedPending, err
			}
			// Normal mid-assembly state: the role stays staged cluster-wide
			// and the last member's apply commits it. Fall through to the
			// quorum check, which reports exactly who is still missing.
			fmt.Println("  [i] layout staged but not applied yet — Garage needs replication-factor assigned nodes to commit (later joins will).")
		}
	}

	// Seeding gate: writes need replication_factor distinct assigned nodes, and
	// `key create` itself is a write — short of that it fails, so warn instead.
	assigned, err := gm.CountAssignedNodes(gc)
	if err != nil {
		return garageSeedPending, err
	}
	if assigned < gc.ReplicationFactor {
		fmt.Printf("  [!] %d/%d nodes assigned a layout role — not enough to seed the key and bucket yet.\n", assigned, gc.ReplicationFactor)
		fmt.Println("      Finish joining the remaining members; the LAST install seeds them automatically.")
		return garageSeedPending, nil
	}

	keyID, exists, err := gm.KeyIDByName(gc, gc.Name)
	if err != nil {
		return garageSeedPending, err
	}
	if !exists {
		fmt.Printf("-> Seeding cluster credentials (key %q + bucket %q)...\n", gc.Name, gc.Name)
		access, secret, err := gm.KeyCreate(gc, gc.Name)
		if err != nil {
			return garageSeedPending, err
		}
		gc.RootUser, gc.RootPassword = access, secret
		keyID = access
	} else if gc.RootUser == "" {
		// Another member seeded (or seeded in an earlier run of this node).
		// Recover the public key id so `pg addon list` shows it; the secret
		// only ever lives on the host that minted it.
		gc.RootUser = keyID
	}
	// Bucket + grant run unconditionally: idempotent ("already exists"/
	// "already allows"), and this closes the gap if a seeding run died after
	// `key create` but before `bucket allow`.
	if err := gm.BucketCreate(gc, gc.Name); err != nil {
		return garageSeedPending, err
	}
	if err := gm.BucketAllow(gc, gc.Name, keyID); err != nil {
		return garageSeedPending, err
	}
	if exists {
		return garageSeedByPeer, nil
	}
	return garageSeedSeeded, nil
}

// runAddonInstallGarage installs a Garage node — single-node (v1 path, byte
// identical) or one member of a multi-node cluster (v2). Shape mirrors
// runAddonInstallRustfs, minus everything Garage has no runtime for: no TLS
// (rejected with a pointer to the nginx front), no endpoints/drives (a cluster
// is assembled with --peer id@addr strings, not a central endpoint list), and
// credentials are access/secret keys — env-seeded single-node, `key create`
// seeded once a cluster reaches quorum. pgcli NEVER executes on a peer's host:
// each node's install drives only its own container, and the operator carries
// the printed join command (rpc_secret included) to the next host.
func runAddonInstallGarage(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "garage"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	drives, _ := cmd.Flags().GetStringSlice("drive")
	apiPort, _ := cmd.Flags().GetInt("api-port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	rootUser, _ := cmd.Flags().GetString("root-user")
	rootPassword, _ := cmd.Flags().GetString("root-password")
	force, _ := cmd.Flags().GetBool("force")

	// Multi-node (v2) knobs.
	cluster, _ := cmd.Flags().GetString("cluster")
	advertiseHost, _ := cmd.Flags().GetString("advertise-host")
	peersFlag, _ := cmd.Flags().GetStringArray("peer")
	zone, _ := cmd.Flags().GetString("zone")
	capacity, _ := cmd.Flags().GetInt("capacity")
	replicationFactor, _ := cmd.Flags().GetInt("replication-factor")
	rpcSecret, _ := cmd.Flags().GetString("rpc-secret")

	// Garage serves plaintext HTTP only — refuse --tls/--tls-cert/--tls-key
	// with the recipe instead of silently ignoring the operator's intent.
	tls, _ := cmd.Flags().GetBool("tls")
	tlsCert, _ := cmd.Flags().GetString("tls-cert")
	tlsKey, _ := cmd.Flags().GetString("tls-key")
	if tls || tlsCert != "" || tlsKey != "" {
		return fmt.Errorf("garage has no native TLS (it serves plaintext HTTP); front it with the nginx addon via --conf-file instead (template --upstream mode breaks S3 request signing) — see docs/addon/garage")
	}

	// Cluster-only flags on a single-node install are always a mistake — refuse
	// rather than silently drop them (a lone --rpc-secret especially means the
	// operator meant --cluster).
	if cluster == "" {
		if rpcSecret != "" {
			return fmt.Errorf("--rpc-secret is a cluster (garage.toml rpc_secret sharing) knob — pass it together with --cluster")
		}
		if len(peersFlag) > 0 || zone != "" || capacity != 0 {
			return fmt.Errorf("--peer/--zone/--capacity require --cluster (multi-node garage)")
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

	if cfg.Addons.Garage == nil {
		cfg.Addons.Garage = make(map[string]config.GarageConfig)
	}
	existing, ok := cfg.Addons.Garage[name]
	if !ok {
		existing = config.GarageConfig{
			ContainerName: "pgcli-garage" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	if dataDir != "" {
		existing.DataDir = dataDir
	}
	if len(drives) > 0 {
		existing.Drives = drives
	}
	// Multi-disk mode takes its data locations from --drive only; there is no
	// merge semantics with a lone --data-dir. Checked post-merge so a stale
	// `drives:` key in pg.yaml conflicts like a flag.
	if len(existing.Drives) > 0 && existing.DataDir != "" {
		return fmt.Errorf("--drive and --data-dir cannot be combined — multi-drive mode takes its data locations from --drive only")
	}
	if apiPort != 0 {
		// Only the API port is a knob; ApplyDefaults' garage pool walk takes
		// the next free ports for RPC/Web/Admin after it.
		existing.APIPort = apiPort
	}
	if rootUser != "" {
		existing.RootUser = rootUser
	}
	if rootPassword != "" {
		existing.RootPassword = rootPassword
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if cluster != "" {
		if existing.Cluster != "" && existing.Cluster != cluster {
			return fmt.Errorf("garage %q already belongs to cluster %q, not %q (each host runs its own pg.yaml; use --name to keep members distinct)", name, existing.Cluster, cluster)
		}
		existing.Cluster = cluster
	}
	if advertiseHost != "" {
		existing.AdvertiseHost = advertiseHost
	}
	if zone != "" {
		existing.Zone = zone
	}
	if capacity != 0 {
		existing.Capacity = capacity
	}
	if replicationFactor != 0 {
		existing.ReplicationFactor = replicationFactor
	}
	existing.Peers = appendPeers(existing.Peers, peersFlag)
	if rpcSecret != "" {
		// The joiner must carry the bootstrap node's exact secret; a mismatch
		// silently partitions the cluster, so reject it here instead.
		if existing.RPCSecret != "" && existing.RPCSecret != rpcSecret {
			return fmt.Errorf("--rpc-secret does not match the value stored for garage %q (every cluster member must share one rpc_secret)", name)
		}
		existing.RPCSecret = rpcSecret
	}
	if err := podman.ValidateGarageCluster(&existing); err != nil {
		return err
	}
	cfg.Addons.Garage[name] = existing

	// ApplyDefaults fills ContainerName/ImageTag/Listen/Region/ReplicationFactor
	// and assigns the four ports from Garage's own pool (garage_start_port,
	// default 3900). Credentials and rpc_secret are NOT managed there — they
	// are secrets the install path owns.
	cfg.ApplyDefaults()
	gc := cfg.Addons.Garage[name]
	role := garageInstallRole(gc.Cluster, gc.Peers)

	// macOS: the store serves on the pgcli-net bridge with published ports, so
	// bring up the machine and the bridge first (no-op on Linux, where Garage
	// uses host networking).
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	gm, err := podman.NewGarageManager(cfg)
	if err != nil {
		return fmt.Errorf("garage manager: %w", err)
	}

	// Same reuse-without-force semantics as rustfs: a present container is
	// reused (started if stopped); --force recreates so changed ports/listen/
	// credentials take effect. The cluster tail runs either way — re-running an
	// install is the documented recovery for a layout version race.
	exists, err := gm.ContainerExists(gc.ContainerName)
	if err != nil {
		return err
	}
	skipped := false
	if exists && !force {
		skipped = true
		if running, _ := gm.ContainerRunning(gc.ContainerName); running {
			fmt.Printf("-> garage container %q already running; skipping creation\n", gc.ContainerName)
		} else {
			fmt.Printf("-> garage container %q exists but is stopped; starting it\n", gc.ContainerName)
			if err := gm.StartContainer(&gc); err != nil {
				return err
			}
		}
	} else {
		// Single-node first-boot seeding (GARAGE_DEFAULT_* env) needs the key
		// pair before the container is created — generate each once and persist
		// in pg.yaml. A cluster CANNOT take that path (the env seed is a
		// --single-node feature): its key/bucket arrive via the tail below, and
		// the only secret generated upfront is the shared rpc_secret.
		if !gc.Clustered() {
			if gc.RootUser == "" {
				ak, err := generateGarageAccessKey()
				if err != nil {
					return fmt.Errorf("generating garage access key: %w", err)
				}
				gc.RootUser = ak
			}
			if gc.RootPassword == "" {
				sk, err := generateGarageSecretKey()
				if err != nil {
					return fmt.Errorf("generating garage secret key: %w", err)
				}
				gc.RootPassword = sk
			}
		}
		if gc.RPCSecret == "" {
			rs, err := generateGarageSecretKey()
			if err != nil {
				return fmt.Errorf("generating garage rpc secret: %w", err)
			}
			gc.RPCSecret = rs
		}
		fmt.Printf("-> Preparing garage image %s...\n", gc.ImageTag)
		if err := gm.EnsureImage(gc.ImageTag); err != nil {
			return err
		}
		if len(gc.Drives) > 0 {
			for _, d := range gm.DrivesSharingRootDevice(&gc) {
				fmt.Printf("-> WARNING: drive %q is on the same device as the host root filesystem; Garage will still use it, but multi-drive only pays off on separately-mounted disks.\n", d)
			}
		}
		fmt.Printf("-> Starting garage container (%s)... \n", role)
		if err := gm.EnsureContainer(&gc); err != nil {
			return err
		}
	}

	cfg.Addons.Garage[name] = gc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	seedState := garageSeedSkipped
	if gc.Clustered() {
		var tailErr error
		seedState, tailErr = runGarageClusterTail(gm, &gc)
		// Persist identity/credentials even when the tail failed — the node id
		// and a done seeding are real state on disk; a re-run resumes.
		cfg.Addons.Garage[name] = gc
		if err := cfg.Save(path); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		if tailErr != nil {
			return tailErr
		}
	}

	fmt.Println()
	if skipped {
		fmt.Printf("✓ garage already present: %q\n", name)
	} else {
		fmt.Printf("✓ garage installed: %q\n", name)
	}
	fmt.Printf("  Container:     %s\n", gc.ContainerName)
	fmt.Printf("  Image:         %s\n", gc.ImageTag)
	if len(gc.Drives) > 0 {
		slots := podman.GarageContainerDataDirs(len(gc.Drives))
		for i, d := range gc.Drives {
			fmt.Printf("  Drive %d:       %s -> %s\n", i+1, d, slots[i])
		}
	} else {
		fmt.Printf("  Data:          %s\n", gm.DataDir(&gc))
	}
	fmt.Printf("  Metadata:      %s\n", gm.MetadataDir(&gc))
	fmt.Printf("  Config:        %s\n", filepath.Join(filepath.Dir(gm.DataDir(&gc)), "garage.toml"))
	fmt.Printf("  S3 API:        http://%s:%d\n", gc.Listen, gc.APIPort)
	fmt.Printf("  RPC/Web/Admin: %d/%d/%d\n", gc.RPCPort, gc.WebPort, gc.AdminPort)
	if gc.Clustered() {
		fmt.Printf("  Cluster:       %s (role: %s, replication factor %d)\n", gc.Cluster, role, gc.ReplicationFactor)
		fmt.Printf("  Advertise:     %s\n", gc.RPCPublicAddr())
		fmt.Printf("  Zone/Capacity: %s / %d GiB\n", gc.Zone, gc.Capacity)
		fmt.Printf("  Node ID:       %s\n", gc.NodeID)
		if len(gc.Peers) > 0 {
			fmt.Printf("  Peers:         %s\n", strings.Join(gc.Peers, ", "))
		}
	}
	fmt.Println()
	switch seedState {
	case garageSeedPending:
		fmt.Println("  Access key:    (not seeded yet — the cluster needs replication-factor assigned nodes)")
		fmt.Printf("  Bucket:        %s (key and bucket are seeded by the install that completes the quorum)\n", gc.Name)
	case garageSeedByPeer:
		fmt.Printf("  Access key:    %s (seeded by another member — this host's pg.yaml keeps no secret; read it with `pg addon password garage --name %s` on that host)\n", gc.RootUser, gc.Name)
		fmt.Printf("  Bucket:        %s\n", gc.Name)
	default:
		fmt.Printf("  Access key:    %s\n", gc.RootUser)
		fmt.Printf("  Secret key:    %s\n", gc.RootPassword)
		if seedState == garageSeedSeeded {
			fmt.Printf("  Bucket:        %s (cluster key + bucket seeded on this install)\n", gc.Name)
		} else {
			fmt.Printf("  Bucket:        %s (created on first boot, owned by the access key)\n", gc.Name)
		}
	}
	fmt.Println()
	fmt.Println("  NOTE: Garage serves plaintext HTTP only. pgBackRest requires HTTPS — front it")
	fmt.Println("        with the nginx addon via --conf-file (template --upstream mode strips")
	fmt.Println("        the port from Host and breaks S3 signing; see docs/addon/garage):")
	fmt.Printf("          backend 127.0.0.1:%d, listen ssl, proxy_set_header Host $http_host\n", gc.APIPort)
	if gc.Clustered() {
		fmt.Println()
		fmt.Println("  To add ANOTHER member, run on the next host (carrying --rpc-secret yourself;")
		fmt.Println("  pgcli never executes remotely). --advertise-host is that host's own peer-")
		fmt.Println("  reachable IP; pick a --zone per failure domain and keep --replication-factor:")
		fmt.Printf("    pg addon install garage --name %s --cluster %s \\\n", gc.Name, gc.Cluster)
		fmt.Printf("      --advertise-host <next-host-ip> --rpc-secret %s \\\n", gc.RPCSecret)
		fmt.Printf("      --peer %s --zone <zone> --capacity <GiB> --replication-factor %d\n", gc.NodeID, gc.ReplicationFactor)
	}
	// Only usable here when this host actually holds the secret: a joiner
	// (ByPeer) or an unseeded node (Pending) must take the pair from the host
	// that minted the key.
	if seedState == garageSeedSkipped || seedState == garageSeedSeeded {
		fmt.Printf("  Try it:        pg mc alias set %s http://127.0.0.1:%d %s %s && pg mc ls %s\n",
			gc.Name, gc.APIPort, gc.RootUser, gc.RootPassword, gc.Name)
	}
	return nil
}

// runAddonRemoveGarage removes the garage container and its config entry.
func runAddonRemoveGarage(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "garage"
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

	if cfg.Addons.Garage == nil {
		return fmt.Errorf("no garage add-ons configured")
	}
	gc, ok := cfg.Addons.Garage[name]
	if !ok {
		return fmt.Errorf("garage %q not found", name)
	}

	gm, err := podman.NewGarageManager(cfg)
	if err != nil {
		return fmt.Errorf("garage manager: %w", err)
	}

	fmt.Printf("-> Removing garage %q...\n", name)
	if err := gm.Remove(&gc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.Garage, name)
	if len(cfg.Addons.Garage) == 0 {
		cfg.Addons.Garage = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ garage %q removed\n", name)
	return nil
}

// runAddonStartGarage starts an existing garage container.
func runAddonStartGarage(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "garage"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.Garage == nil {
		return fmt.Errorf("no garage add-ons configured (run 'pg addon install garage')")
	}
	gc, ok := cfg.Addons.Garage[name]
	if !ok {
		return fmt.Errorf("garage %q not found (run 'pg addon install garage --name %s')", name, name)
	}
	gm, err := podman.NewGarageManager(cfg)
	if err != nil {
		return fmt.Errorf("garage manager: %w", err)
	}
	fmt.Printf("-> Starting garage %q...\n", name)
	return gm.StartContainer(&gc)
}

// runAddonStopGarage stops a running garage container.
func runAddonStopGarage(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "garage"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	gc, ok := cfg.Addons.Garage[name]
	if !ok {
		return fmt.Errorf("garage %q not found", name)
	}
	gm, err := podman.NewGarageManager(cfg)
	if err != nil {
		return fmt.Errorf("garage manager: %w", err)
	}
	running, _ := gm.ContainerRunning(gc.ContainerName)
	if !running {
		fmt.Printf("garage %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping garage %q...\n", name)
	if _, err := gm.Stop(gc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ garage %q stopped\n", name)
	return nil
}
