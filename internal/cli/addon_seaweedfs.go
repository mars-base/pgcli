package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

// generateSeaweedAccessKey builds an SeaweedFS access key id: "SW" + 28 hex
// chars (garage's GK+32hex shape, shortened to sit comfortably under S3's
// 128-char id ceiling). Only used when --root-user is omitted.
func generateSeaweedAccessKey() (string, error) {
	b := make([]byte, 14)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "SW" + hex.EncodeToString(b), nil
}

// generateSeaweedSecretKey builds a 64-hex-char secret (`openssl rand -hex
// 32`, the shape every other store addon uses).
func generateSeaweedSecretKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// The metadata database is NOT auto-created: the earlier --filer-pg path that
// did is gone (its GetPostgresURL() resolved to the instance container's
// 127.0.0.1:5432 — unreachable from a host-network addon container), so
// cluster installs take an explicit --dsn with the host-published address and
// the operator pre-creates the database.

// runAddonInstallSeaweedfs installs a SeaweedFS node — single-node (`weed
// mini`) or one member of a cross-host cluster (`weed server -filer -s3`).
// The cluster shape is minio-MNMD, NOT garage: there is no bootstrap/join
// distinction — the operator runs the SAME install command on every host, full
// Raft membership arrives via the merged --peer list, and a node that comes up
// before quorum simply waits: the health gate warns instead of failing, and
// whichever install completes quorum finishes the seeding. Unlike MinIO,
// though, the command is NOT freely per-host parameterised: SeaweedFS reads
// S3 credentials from each node's own static s3.json, so a cluster must pass
// the SAME --root-user/--root-password everywhere (auto-generation is refused
// under --cluster), and the filer metadata --dsn is shared by definition.
func runAddonInstallSeaweedfs(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "seaweedfs"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	port, _ := cmd.Flags().GetInt("port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	rootUser, _ := cmd.Flags().GetString("root-user")
	rootPassword, _ := cmd.Flags().GetString("root-password")
	force, _ := cmd.Flags().GetBool("force")
	bucket, _ := cmd.Flags().GetString("bucket")
	replication, _ := cmd.Flags().GetString("replication")
	volumeSizeMB, _ := cmd.Flags().GetInt("volume-size")

	// Multi-node knobs.
	cluster, _ := cmd.Flags().GetString("cluster")
	advertiseHost, _ := cmd.Flags().GetString("advertise-host")
	peersFlag, _ := cmd.Flags().GetStringArray("peer")

	// Shared filer metadata PostgreSQL (explicit DSN; the metadata database is
	// pre-created by the operator — pgcli only probes reachability).
	dsn, _ := cmd.Flags().GetString("dsn")

	// Native S3 TLS — same flag pair semantics as minio/rustfs.
	tls, _ := cmd.Flags().GetBool("tls")
	tlsCert, _ := cmd.Flags().GetString("tls-cert")
	tlsKey, _ := cmd.Flags().GetString("tls-key")

	// Cluster-only flags on a single-node install are always a mistake —
	// refuse rather than silently drop them (garage's rule, same rationale).
	if cluster == "" {
		if len(peersFlag) > 0 {
			return fmt.Errorf("--peer requires --cluster (multi-node seaweedfs)")
		}
		if replication != "" && replication != "000" {
			return fmt.Errorf("seaweedfs single-node cannot replicate (no peers): drop --replication or pass --cluster")
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

	filerDSN := dsn

	if cfg.Addons.Seaweedfs == nil {
		cfg.Addons.Seaweedfs = make(map[string]config.SeaweedfsConfig)
	}
	existing, ok := cfg.Addons.Seaweedfs[name]
	if !ok {
		existing = config.SeaweedfsConfig{
			ContainerName: "pgcli-seaweedfs" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	if dataDir != "" {
		existing.DataDir = dataDir
	}
	if port != 0 {
		// The single port knob: ApplyDefaults' pool walk takes it as the
		// master anchor and assigns volume/filer/s3 as the next three.
		existing.Port = port
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if rootUser != "" {
		existing.RootUser = rootUser
	}
	if rootPassword != "" {
		existing.RootPassword = rootPassword
	}
	// A half pair is always a mistake — one of the two on its own means the
	// operator intended to give both (same rule as --tls-cert/--tls-key).
	// Checked on the MERGED config, not the raw flags: re-running an install on
	// a node that already has a pair may legitimately pass neither.
	if (existing.RootUser == "") != (existing.RootPassword == "") {
		return fmt.Errorf("--root-user and --root-password must be given together (a credential pair needs both the access key and its secret)")
	}
	if bucket != "" {
		existing.Bucket = bucket
	}
	if replication != "" {
		// In a cluster the placement code is baked into every member's Raft
		// consensus semantics for new writes; changing it post-hoc is a
		// re-create concern (volume rebalancing is out of v1 scope), so the
		// merged value must not drift within one cluster.
		if existing.Clustered() && existing.Replication != "" && existing.Replication != replication {
			return fmt.Errorf("seaweedfs %q already belongs to cluster %q with replication %q — changing it means recreating the cluster (v1 does not reconfigure placement in place)", name, existing.Cluster, existing.Replication)
		}
		existing.Replication = replication
	}
	if volumeSizeMB != 0 {
		existing.VolumeSizeMB = uint(volumeSizeMB)
	}
	if cluster != "" {
		if existing.Cluster != "" && existing.Cluster != cluster {
			return fmt.Errorf("seaweedfs %q already belongs to cluster %q, not %q (each host runs its own pg.yaml; use --name to keep members distinct)", name, existing.Cluster, cluster)
		}
		existing.Cluster = cluster
	}
	if advertiseHost != "" {
		existing.AdvertiseHost = advertiseHost
	}
	existing.Peers = appendPeers(existing.Peers, peersFlag)
	if filerDSN != "" {
		existing.FilerDSN = filerDSN
	}
	if tlsCert != "" && tlsKey != "" {
		existing.TLS = true
		existing.CertFile = podman.HostMountPath(tlsCert)
		existing.KeyFile = podman.HostMountPath(tlsKey)
	} else if tlsCert != "" || tlsKey != "" {
		return fmt.Errorf("--tls-cert and --tls-key must be given together (a bring-your-own TLS pair needs both the cert and its key)")
	}
	if tls {
		existing.TLS = true
	}
	if err := podman.ValidateSeaweedfsCluster(&existing); err != nil {
		return err
	}
	cfg.Addons.Seaweedfs[name] = existing

	// ApplyDefaults fills ContainerName/ImageTag/Listen/Replication("000")/
	// Bucket(=Name)/VolumeSizeMB and assigns the four ports from SeaweedFS's
	// own pool (seaweedfs_start_port, default 8400, gRPC twins reserved).
	// Credentials and the filer DSN are NOT managed there — secrets and
	// instance resolution belong to this handler.
	cfg.ApplyDefaults()
	sc := cfg.Addons.Seaweedfs[name]

	// BYO TLS: fail fast at install on a bad pair (mismatch, expired, CA-only)
	// rather than letting the container crash-loop — same gate as minio.
	if sc.TLSOn() && sc.CertFile != "" && sc.KeyFile != "" {
		ci, err := podman.ValidateBYOCert(sc.CertFile, sc.KeyFile)
		if err != nil {
			return fmt.Errorf("seaweedfs --tls-cert/--tls-key: %w", err)
		}
		fmt.Printf("-> SeaweedFS BYO cert: CN=%q issuer=%q valid %s → %s\n",
			ci.Subject, ci.Issuer, ci.NotBefore.Format("2006-01-02"), ci.NotAfter.Format("2006-01-02"))
	}

	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	sm, err := podman.NewSeaweedfsManager(cfg)
	if err != nil {
		return fmt.Errorf("seaweedfs manager: %w", err)
	}

	// Reuse-without-force semantics as garage/rustfs: a present container is
	// reused (started if stopped); --force recreates so changed ports/flags/
	// credentials take effect.
	exists, err := sm.ContainerExists(sc.ContainerName)
	if err != nil {
		return err
	}
	skipped := false
	if exists && !force {
		skipped = true
		if running, _ := sm.ContainerRunning(sc.ContainerName); running {
			fmt.Printf("-> seaweedfs container %q already running; skipping creation\n", sc.ContainerName)
		} else {
			fmt.Printf("-> seaweedfs container %q exists but is stopped; starting it\n", sc.ContainerName)
			if err := sm.StartContainer(&sc); err != nil {
				return err
			}
		}
	} else {
		// s3.json carries the credentials and weed reads it at startup, so the
		// pair must exist before the container does. Auto-generation is a
		// SINGLE-NODE convenience: each node renders its OWN s3.json, and
		// SeaweedFS has no shared-credential backend without a standalone IAM
		// server (out of v1 scope) — a cluster where nodes mint their own pairs
		// would have every gateway authenticate a different identity, so
		// ValidateSeaweedfsCluster (run at merge time, above) already required
		// explicit creds under --cluster; by here only single-node can be
		// empty. One cluster, one pair: the first node's install generates it
		// (or the operator supplies it), prints it, and the SAME --root-user/
		// --root-password must be carried to every remaining node.
		if sc.RootUser == "" {
			ak, err := generateSeaweedAccessKey()
			if err != nil {
				return fmt.Errorf("generating seaweedfs access key: %w", err)
			}
			sc.RootUser = ak
		}
		if sc.RootPassword == "" {
			sk, err := generateSeaweedSecretKey()
			if err != nil {
				return fmt.Errorf("generating seaweedfs secret key: %w", err)
			}
			sc.RootPassword = sk
		}
		cfg.Addons.Seaweedfs[name] = sc

		// External metadata PostgreSQL: probe only. pgcli never creates a
		// database it does not own — the operator pre-creates the filer
		// metadata database (weed's postgres2 store then creates its own
		// tables inside it on first boot; verified on vm01).
		if sc.FilerDSN != "" {
			pm, err := podman.New(cfg)
			if err != nil {
				return fmt.Errorf("podman: %w", err)
			}
			if err := pm.CheckDSNReachable(sc.FilerDSN); err != nil {
				return err
			}
		}

		fmt.Printf("-> Preparing seaweedfs image %s...\n", sc.ImageTag)
		if err := sm.EnsureImage(sc.ImageTag); err != nil {
			return err
		}
		role := "single-node (weed mini)"
		if sc.Clustered() {
			role = fmt.Sprintf("cluster %q member (weed server)", sc.Cluster)
		}
		fmt.Printf("-> Starting seaweedfs container (%s)...\n", role)
		if err := sm.EnsureContainer(&sc); err != nil {
			return err
		}
	}

	cfg.Addons.Seaweedfs[name] = sc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Health gate: detached + --restart unless-stopped swallows a bad argv into
	// a crash loop behind a green-looking install (the garage lesson), so poll
	// the master's JSON /cluster/status before declaring success. 30s covers a
	// cold image-start on the VMs.
	fmt.Println("-> Waiting for the seaweedfs master to answer /cluster/status...")
	var status podman.SeaweedfsClusterStatus
	green := false
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if running, _ := sm.ContainerRunning(sc.ContainerName); !running {
			break // crash-loop: the container died; no point polling a corpse
		}
		if st, err := sm.ClusterStatus(&sc); err == nil && st.Leader != "" {
			status = st
			green = true
			break
		}
		time.Sleep(time.Second)
	}
	if green {
		fmt.Printf("  [OK] cluster leader: %s\n", status.Leader)
	} else if sc.Clustered() {
		// Raft needs a majority before any leader exists — a lone first node
		// is normal waiting state, not a failure (MNMD semantics: the quorum
		// completes when the LAST member's install runs).
		fmt.Println("  [!] no leader yet — normal while the cluster is still assembling;")
		fmt.Println("      finish the remaining members' installs, then re-run this command here to re-check.")
	} else {
		fmt.Println("  [!] master not answering /cluster/status yet — check: pg logs addon seaweedfs --name " + sc.Name)
	}

	// Bucket seeding: `weed mini -bucket` creates it on first boot
	// single-node; for the cluster (weed server has no -bucket flag) the mc
	// mb below is the real seeding step. Idempotent either way, and only worth
	// attempting once this node has a leader to serve writes.
	if green {
		if err := seedSeaweedfsBucket(&sc); err != nil {
			if sc.Clustered() {
				fmt.Printf("  [!] bucket seeding incomplete: %v\n      re-run this install command once the cluster has quorum (mb is idempotent)\n", err)
			} else {
				return err
			}
		}
	}

	fmt.Println()
	if skipped {
		fmt.Printf("✓ seaweedfs already present: %q\n", name)
	} else {
		fmt.Printf("✓ seaweedfs installed: %q\n", name)
	}
	fmt.Printf("  Container:     %s\n", sc.ContainerName)
	fmt.Printf("  Image:         %s\n", sc.ImageTag)
	fmt.Printf("  Data:          %s\n", sm.DataDir(&sc))
	fmt.Printf("  S3 endpoint:   %s\n", sm.S3URL(&sc))
	fmt.Printf("  Ports:         master=%d volume=%d filer=%d s3=%d (gRPC twins +10000)\n", sc.Port, sc.VolumePort, sc.FilerPort, sc.S3Port)
	fmt.Printf("  Bucket:        %s\n", sc.Bucket)
	if sc.Clustered() {
		fmt.Printf("  Cluster:       %s (replication %s, advertise %s)\n", sc.Cluster, sc.Replication, sc.AdvertiseHost)
		fmt.Printf("  Peers:         %s\n", podman.SeaweedfsPeersFlag(&sc))
		fmt.Printf("  Filer store:   postgres %s\n", filerDBDisplay(sc.FilerDSN))
	}
	fmt.Println()
	fmt.Printf("  Access key:    %s\n", sc.RootUser)
	if sc.RootPassword != "" {
		fmt.Printf("  Secret key:    %s\n", sc.RootPassword)
	}
	if sc.TLSOn() {
		fmt.Println()
		fmt.Println("  NOTE: TLS is on — the S3 port serves HTTPS only (the cleartext listener is")
		fmt.Println("        off). Clients must use the https:// endpoint above; for the pgcli-")
		fmt.Println("        generated CA pass its ca.crt to mc/aws-cli, or use --insecure on")
		fmt.Println("        loopback (`pg mc` does this automatically).")
	}
	if sc.Clustered() {
		fmt.Println()
		fmt.Printf("  To complete the cluster, run the SAME install on each remaining host —\n")
		fmt.Printf("  shared credentials and the filer DSN are identical everywhere; only\n")
		fmt.Printf("  --advertise-host changes (that host's own peer-reachable IP):\n")
		fmt.Printf("    pg addon install seaweedfs --name %s --cluster %s \\\n", sc.Name, sc.Cluster)
		fmt.Printf("      --advertise-host <next-host-ip> --replication %s \\\n", sc.Replication)
		fmt.Printf("      --root-user %s --root-password %s --dsn %q \\\n", sc.RootUser, sc.RootPassword, sc.FilerDSN)
		fmt.Printf("      --peer %s\n", strings.Join(podman.NormalizeSeaweedfsPeers(&sc), " --peer "))
	}
	fmt.Printf("  Try it:        pg mc alias set %s %s %s %s && pg mc ls %s\n",
		sc.Name, sm.S3URL(&sc), sc.RootUser, sc.RootPassword, sc.Bucket)
	return nil
}

// seedSeaweedfsBucket idempotently creates the bucket through the S3 API with
// the pgcli-mc container. No persistent alias: MC_HOST_<x> env injects a
// one-shot alias (forwarded verbatim into the mc container by MCRunner), so
// ~/.mc stays untouched. The endpoint must be dialable from the mc container:
// Linux host networking reaches the host loopback; macOS bridge does not, so
// the seed is skipped there (the printed "Try it" alias works for the user
// because gvproxy publishes the port).
func seedSeaweedfsBucket(sc *config.SeaweedfsConfig) error {
	if platform.Detect() == platform.MacOS {
		return nil
	}
	mcm, err := podman.NewMCRunner()
	if err != nil {
		return err
	}
	scheme := "http"
	if sc.TLSOn() {
		scheme = "https"
	}
	alias := "pgcli-seaweedfs-seed"
	os.Setenv("MC_HOST_"+alias, fmt.Sprintf("%s://%s:%s@127.0.0.1:%d", scheme, sc.RootUser, sc.RootPassword, sc.S3Port))
	defer os.Unsetenv("MC_HOST_" + alias)
	// --ignore-existing is the bucket-exists guard: SeaweedFS answers a second
	// mb with BucketAlreadyOwnedByYou ("you already own it"), which plain mc
	// exits 1 on (vm01). With the flag the same call exits 0 either way.
	return mcm.Run([]string{"mb", "--ignore-existing", alias + "/" + sc.Bucket})
}

// filerDBDisplay renders a DSN as host:port/db with the password hidden, for
// the install summary and `pg addon list`.
func filerDBDisplay(dsn string) string {
	host, port, _, _, db, err := podman.ParseDSN(dsn)
	if err != nil {
		return "(unparseable dsn)"
	}
	return fmt.Sprintf("%s:%d/%s", host, port, db)
}

// runAddonRemoveSeaweedfs removes the seaweedfs container and its config
// entry. The filer metadata DATABASE is never touched — the notice is printed
// so an operator who wants it gone knows exactly what to drop.
func runAddonRemoveSeaweedfs(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "seaweedfs"
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

	if cfg.Addons.Seaweedfs == nil {
		return fmt.Errorf("no seaweedfs add-ons configured")
	}
	sc, ok := cfg.Addons.Seaweedfs[name]
	if !ok {
		return fmt.Errorf("seaweedfs %q not found", name)
	}

	sm, err := podman.NewSeaweedfsManager(cfg)
	if err != nil {
		return fmt.Errorf("seaweedfs manager: %w", err)
	}

	fmt.Printf("-> Removing seaweedfs %q...\n", name)
	if err := sm.Remove(&sc, cleanData); err != nil {
		return err
	}
	if sc.FilerDSN != "" {
		fmt.Printf("  [i] The filer metadata database (%s) was KEPT — pgcli never drops databases; drop it yourself if the data is disposable.\n", filerDBDisplay(sc.FilerDSN))
	}

	delete(cfg.Addons.Seaweedfs, name)
	if len(cfg.Addons.Seaweedfs) == 0 {
		cfg.Addons.Seaweedfs = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ seaweedfs %q removed\n", name)
	return nil
}

// runAddonStartSeaweedfs starts an existing seaweedfs container.
func runAddonStartSeaweedfs(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "seaweedfs"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.Seaweedfs == nil {
		return fmt.Errorf("no seaweedfs add-ons configured (run 'pg addon install seaweedfs')")
	}
	sc, ok := cfg.Addons.Seaweedfs[name]
	if !ok {
		return fmt.Errorf("seaweedfs %q not found (run 'pg addon install seaweedfs --name %s')", name, name)
	}
	sm, err := podman.NewSeaweedfsManager(cfg)
	if err != nil {
		return fmt.Errorf("seaweedfs manager: %w", err)
	}
	fmt.Printf("-> Starting seaweedfs %q...\n", name)
	return sm.StartContainer(&sc)
}

// runAddonStopSeaweedfs stops a running seaweedfs container.
func runAddonStopSeaweedfs(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "seaweedfs"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	sc, ok := cfg.Addons.Seaweedfs[name]
	if !ok {
		return fmt.Errorf("seaweedfs %q not found", name)
	}
	sm, err := podman.NewSeaweedfsManager(cfg)
	if err != nil {
		return fmt.Errorf("seaweedfs manager: %w", err)
	}
	running, _ := sm.ContainerRunning(sc.ContainerName)
	if !running {
		fmt.Printf("seaweedfs %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping seaweedfs %q...\n", name)
	if _, err := sm.Stop(sc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ seaweedfs %q stopped\n", name)
	return nil
}
