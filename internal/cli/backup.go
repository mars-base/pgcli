package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

var backupBaseDir string

// S3 repo flags for `pg backup setup`. All are optional; setting any of them
// switches (or keeps) the S3 repository. Once configured, re-running setup
// without flags just refreshes containers and stanzas.
var (
	s3Endpoint, s3Bucket, s3Region, s3Path, s3CAFile string
	s3AccessKey, s3SecretKey                         string
	s3NoVerify                                       bool
)

func init() {
	rootCmd.AddCommand(backupCmd)
	backupCmd.AddCommand(backupSetupCmd)
	backupCmd.AddCommand(backupStartCmd)
	backupCmd.AddCommand(backupStopCmd)
	backupCmd.AddCommand(backupStatusCmd)
	backupCmd.AddCommand(backupFetchCACmd)

	backupSetupCmd.Flags().StringVar(&backupBaseDir, "base-dir", "", "base directory for backup data and logs (overrides config base_dir)")
	backupSetupCmd.Flags().StringVar(&s3Endpoint, "s3-endpoint", "", "S3 repository host:port (pgBackRest forces HTTPS - use a TLS-terminated endpoint, e.g. a MinIO installed with 'pg addon install minio --tls')")
	backupSetupCmd.Flags().StringVar(&s3Bucket, "s3-bucket", "", "S3 bucket (must already exist)")
	backupSetupCmd.Flags().StringVar(&s3AccessKey, "s3-access-key", "", "S3 access key")
	backupSetupCmd.Flags().StringVar(&s3SecretKey, "s3-secret-key", "", "S3 secret key (prefer editing backup.repo.s3.secret_key in pg.yaml so it stays out of shell history)")
	backupSetupCmd.Flags().StringVar(&s3Region, "s3-region", "", "S3 region (default us-east-1)")
	backupSetupCmd.Flags().StringVar(&s3Path, "s3-path", "", "path prefix inside the bucket (default /pgbackrest)")
	backupSetupCmd.Flags().StringVar(&s3CAFile, "s3-ca-file", "", "host path to a PEM CA bundle trusted for the endpoint (mounted into the backup and member containers as /etc/pgbackrest/ca.crt; on a remote storage host, pull it with 'pg backup fetch-ca')")
	backupSetupCmd.Flags().BoolVar(&s3NoVerify, "s3-no-verify-tls", false, "skip TLS certificate verification (repo1-s3-verify-tls=n) — escape hatch for self-signed endpoints you cannot hand a CA for")

	backupFetchCACmd.Flags().StringVar(&fetchCAOut, "out", "", "where to write the fetched CA (default <base-dir>/backup/repo-ca/ca-<endpoint>.crt)")
}

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Manage the shared pgbackrest backup container",
	Long: `backup manages a shared pgbackrest container that handles backups for all instances.

The backup container is shared across all database instances -- each instance
gets its own pgbackrest stanza, but they all share a single pgbackrest repository.

Subcommands:
  setup     Build image, create directories, generate config, start container
  start     Start the backup container
  stop      Stop the backup container
  status    Show backup container status
  fetch-ca  Fetch an S3 endpoint's TLS CA certificate over the network`,
}

// loadRawConfig loads config without calling SetInstance (for backup commands
// that operate on all instances rather than a single one).
func loadRawConfig() (*config.Config, error) {
	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	return config.Load(path)
}

// --- backup setup -----------------------------------------------

var backupSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Initialize the shared pgbackrest backup container",
	Long: `setup initializes the shared pgbackrest backup environment:

  1. Build the backup image (Debian + pgbackrest)
  2. Create data and log directories
  3. Generate pgbackrest.conf with stanzas for all PITR-enabled instances
     (plus the member-local archive view for Patroni WAL push)
  4. Create and start the backup container
  5. Patroni members: recreate stale ones (replicas first, cluster paused) so
     they mount the archive config and run with archiving enabled
  6. stanza-create + check for every Patroni stanza

The backup container is shared across all database instances. With
--s3-endpoint/--s3-bucket/... the Patroni stanzas back up to an S3 repository
(pgBackRest requires HTTPS — pair with ` + "`pg addon install minio --tls`" + ` or
any TLS S3 endpoint; regular-instance backups stay on the local repo).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := cfgPath
		if path == "" {
			path = platform.DefaultConfigPath()
		}
		cfg, err := loadRawConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		// Persist S3 repo settings from flags BEFORE any base-dir override, so
		// the saved pg.yaml keeps its original base_dir. Re-running without
		// flags preserves the stored repo (there is no CLI way to unset it;
		// edit pg.yaml to revert to the local repo).
		if s3Endpoint != "" || s3Bucket != "" || s3AccessKey != "" || s3SecretKey != "" || s3CAFile != "" || s3NoVerify {
			if s3Endpoint == "" && cfg.Backup.Repo.S3 == nil {
				return fmt.Errorf("--s3-endpoint is required to configure an S3 repo")
			}
			if cfg.Backup.Repo.S3 == nil {
				cfg.Backup.Repo.S3 = &config.BackupRepoS3{}
			}
			r := cfg.Backup.Repo.S3
			if s3Endpoint != "" {
				r.Endpoint = s3Endpoint
			}
			if s3Bucket != "" {
				r.Bucket = s3Bucket
			}
			if s3AccessKey != "" {
				r.AccessKey = s3AccessKey
			}
			if s3SecretKey != "" {
				r.SecretKey = s3SecretKey
			}
			if s3Region != "" {
				r.Region = s3Region
			}
			if s3Path != "" {
				r.Path = s3Path
			}
			if cmd.Flags().Changed("s3-ca-file") {
				r.CAFile = s3CAFile
			}
			if s3NoVerify {
				f := false
				r.VerifyTLS = &f
			}
			if err := cfg.Save(path); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}
			fmt.Printf("-> S3 repo stored in %s: endpoint=%s bucket=%s\n", path, r.Endpoint, r.Bucket)
		}
		cfg.ApplyDefaults() // fills region/uri_style/path defaults

		// Override BaseDir temporarily for backup path computation (per-backup only, not persisted)
		origBaseDir := cfg.BaseDir
		origBackupDataDir := cfg.Backup.DataDir
		origBackupLogDir := cfg.Backup.LogDir
		applyBaseDir := func() {
			cfg.BaseDir = origBaseDir
			cfg.Backup.DataDir = origBackupDataDir
			cfg.Backup.LogDir = origBackupLogDir
			if backupBaseDir != "" {
				cfg.BaseDir = backupBaseDir
				cfg.Backup.DataDir = filepath.Join(backupBaseDir, "backup", "data")
				cfg.Backup.LogDir = filepath.Join(backupBaseDir, "backup", "log")
			}
		}
		restoreBaseDir := func() {
			cfg.BaseDir = origBaseDir
			cfg.Backup.DataDir = origBackupDataDir
			cfg.Backup.LogDir = origBackupLogDir
		}
		applyBaseDir()
		defer restoreBaseDir()
		// saveCfg persists pg.yaml without the --base-dir override leaking in
		// (a pulled repo CA path saved under a temp base_dir would be wrong
		// for every later run).
		saveCfg := func() error {
			restoreBaseDir()
			defer applyBaseDir()
			return cfg.Save(path)
		}

		bm, err := podman.NewBackupManager(cfg)
		if err != nil {
			return err
		}

		fmt.Println("=== pg backup setup ===")

		// -- Pre-flight checks ----------------------------------

		// 1. Ensure shared network (PG <-> backup containers communicate via bridge)
		fmt.Println("\n-> Ensuring shared network...")
		if err := bm.EnsureNetwork(); err != nil {
			return err
		}

		// 2. Check PITR-enabled instances and their container status
		pitrCount := 0
		for name, inst := range cfg.Instances {
			if !inst.PITR.Enabled {
				continue
			}
			pitrCount++
			stanza := inst.PITR.PgBackRestStanza
			if stanza == "" {
				stanza = "pgcli_" + name
			}
			container := inst.Podman.ContainerName
			if container == "" {
				container = "pgcli-pg-" + name
			}
			fmt.Printf("    %-12s stanza=%-20s container=%-20s",
				name, stanza, container)
			running, err := bm.CheckContainerRunning(container)
			if err != nil {
				fmt.Printf(" [error: %v]\n", err)
			} else if running {
				fmt.Printf(" [running]\n")
			} else {
				fmt.Printf(" [stopped]\n")
			}
		}
		if pitrCount == 0 {
			fmt.Println("\n!  No instances with PITR enabled -- backup container will have no stanzas")
		}

		// 1. Build backup image
		fmt.Println("\n-> Step 1/6: Building backup image...")
		if err := bm.EnsureBackupImage(); err != nil {
			return err
		}

		// 2. Create directories
		fmt.Println("\n-> Step 2/6: Creating backup directories...")
		if err := bm.EnsureBackupDirs(); err != nil {
			return err
		}

		// 2b. Cross-host backup trust: resolve the S3 repository CA against the
		// DCS member registry BEFORE generating configs, so this run's backup
		// and member containers mount the right ca_file. A host that set
		// --s3-ca-file publishes it for peers; a joiner that left it empty
		// pulls the cluster's CA and points ca_file at the pulled copy.
		if err := syncRepoCA(cfg, bm, saveCfg); err != nil {
			fmt.Printf("  [!] repo CA sync failed (falling back to local ca_file): %v\n", err)
		}

		// 3. Generate pgbackrest.conf (backup-container view, pg1-host set)
		// and the member-local archive view (no pg1-host, required for
		// archive-push — [072]).
		fmt.Println("\n-> Step 3/6: Generating pgbackrest.conf...")
		confPath, err := bm.WritePgbackrestConf()
		if err != nil {
			return err
		}
		if _, err := bm.WritePgbackrestArchiveConf(); err != nil {
			return err
		}

		// 4. Create and start backup container
		fmt.Println("\n-> Step 4/6: Starting backup container...")

		if err := bm.EnsureBackupContainer(confPath); err != nil {
			return err
		}

		// 5. Patroni members: distribute cross-host backup trust (pubkey merge +
		// repo CA are already handled above), then pick up the archive conf +
		// archive parameters. Stale members are recreated replicas-first
		// inside a pause window.
		fmt.Println("\n-> Step 5/6: Patroni WAL archiving...")
		setupPatroniBackupTrust(cfg, bm)
		if err := setupPatroniArchiving(cfg, bm); err != nil {
			return err
		}

		// 6. Create missing stanzas and verify the repository.
		fmt.Println("\n-> Step 6/6: Stanza check...")
		if err := bm.StanzaCreatePatroni(); err != nil {
			return err
		}

		fmt.Println("\nOK backup setup complete!")
		fmt.Printf("  Container: %s\n", cfg.Backup.ContainerName)
		fmt.Printf("  Image:     %s\n", cfg.Backup.ImageTag)
		fmt.Printf("  Config:    %s\n", confPath)
		fmt.Printf("  Stanzas:   %d PITR instance(s)\n", pitrCount)
		if len(cfg.Addons.Patroni) > 0 {
			fmt.Printf("  Patroni:   %d cluster(s), repo: ", len(cfg.Addons.Patroni))
			if s3 := cfg.Backup.Repo.S3; s3 != nil {
				fmt.Printf("s3://%s/%s (%s)\n", s3.Bucket, strings.TrimPrefix(s3.Path, "/"), s3.Endpoint)
			} else {
				fmt.Println("local")
			}
		}
		return nil
	},
}

// syncRepoCA reconciles the S3 repository CA with the DCS member registry
// before backup configs are generated. pgBackRest's repo1-s3-ca-file names a
// *host* path, so the CA has to exist locally (and be recorded in pg.yaml)
// before Step 3 renders the configs and Step 4/5 mount the file.
//
// Two roles, decided by this host's local config:
//
//   - ca_file set (operator ran setup --s3-ca-file): publish the PEM into every
//     Patroni scope's registry key, so hosts that join later pick it up.
//   - ca_file empty but a peer published one: pull it to a pgcli-managed path
//     and point cfg.Backup.Repo.S3.CAFile at it, then persist pg.yaml — the
//     joiner needs no manual scp of ca.crt.
//
// The registry link is plaintext etcd with no auth, which is fine here: a CA
// certificate is public material (publishing it grants nothing; only the S3
// secret_key is sensitive and that never enters the registry).
//
// No-op without an S3 repo or without Patroni clusters (no registry to talk
// to). A failure to reach the DCS is returned for the caller to warn about —
// the run continues on whatever ca_file the host already has.
func syncRepoCA(cfg *config.Config, bm *podman.BackupManager, saveCfg func() error) error {
	s3 := cfg.Backup.Repo.S3
	if s3 == nil || len(cfg.Addons.Patroni) == 0 {
		return nil
	}
	pm, err := podman.NewPatroniManager(cfg)
	if err != nil {
		return err
	}

	if s3.CAFile != "" {
		pem, err := os.ReadFile(s3.CAFile)
		if err != nil {
			return fmt.Errorf("reading ca_file %s: %w", s3.CAFile, err)
		}
		var firstErr error
		for scope, cluster := range cfg.Addons.Patroni {
			if err := pm.PublishRepoCA(&cluster, string(pem)); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("publishing repo CA for cluster %s: %w", scope, err)
			}
		}
		if firstErr == nil {
			fmt.Printf("  [OK] repo CA published to %d cluster registry/registries (from %s)\n",
				len(cfg.Addons.Patroni), s3.CAFile)
		}
		return firstErr
	}

	// ca_file empty: pull from whichever scope has published one. The S3 repo
	// is shared, so any published CA applies to the endpoint everywhere; the
	// local file is named after the scope it came from so it is re-pullable.
	var pulled, pulledScope string
	for scope, cluster := range cfg.Addons.Patroni {
		if ca := pm.RepoCAFromDCS(&cluster); ca != "" {
			pulled, pulledScope = ca, scope
			break
		}
	}
	if pulled == "" {
		// Nothing published yet (this is likely the first host). Continue with
		// system CAs; a later setup re-run or the operator's --s3-ca-file
		// seeds the registry.
		fmt.Println("  (no repo CA in the cluster registry yet; using system CAs)")
		return nil
	}
	caPath := bm.RepoCAPath(cfg.PatroniScope(pulledScope))
	if err := os.MkdirAll(filepath.Dir(caPath), 0700); err != nil {
		return fmt.Errorf("creating repo CA dir: %w", err)
	}
	if err := os.WriteFile(caPath, []byte(pulled), 0644); err != nil {
		return fmt.Errorf("writing repo CA: %w", err)
	}
	s3.CAFile = caPath
	if err := saveCfg(); err != nil {
		return fmt.Errorf("saving config with pulled repo CA: %w", err)
	}
	fmt.Printf("  [OK] repo CA pulled from the cluster registry -> %s (ca_file set in pg.yaml)\n", caPath)
	return nil
}

// setupPatroniBackupTrust distributes cross-host backup trust through the DCS
// member registry, right before the stale-member check:
//
//  1. Re-register each local member's ports+backup pubkey (idempotent, and it
//     carries the pubkey for hosts that created members before this field
//     existed).
//  2. Merge every cluster member's backup pubkey into this host's
//     authorized_keys_cluster file. Patroni members bind-mount that file, so
//     the same-inode rewrite is live inside running containers — a member only
//     needs the *mount* (recreate, handled by the readiness check below), not
//     the *content*, to arrive.
//
// Called once per setup run. Best effort per scope: a DCS that is temporarily
// unreachable must not abort setup, since WAL archiving works without
// cross-host trust (only the full-backup/check SSH probe needs it).
func setupPatroniBackupTrust(cfg *config.Config, bm *podman.BackupManager) {
	if len(cfg.Addons.Patroni) == 0 {
		return
	}
	pm, err := podman.NewPatroniManager(cfg)
	if err != nil {
		fmt.Printf("  [!] backup trust: patroni manager unavailable: %v\n", err)
		return
	}
	for scope, cluster := range cfg.Addons.Patroni {
		for member := range cluster.Members {
			if cluster.Members[member].RemoteHost != "" {
				continue // only local members register into the shared DCS
			}
			if err := pm.RegisterMemberPorts(&cluster, member); err != nil {
				fmt.Printf("  [!] backup trust: registering %s/%s pubkey failed: %v\n", scope, member, err)
			}
		}

		members, err := pm.DiscoverAllMembers(&cluster)
		if err != nil {
			fmt.Printf("  [!] backup trust: cluster %s topology unavailable, skipping pubkey merge: %v\n", scope, err)
			continue
		}
		var keys []string
		for _, cm := range members {
			if cm.BackupPubKey != "" {
				keys = append(keys, cm.BackupPubKey)
			}
		}
		nsScope := cfg.PatroniScope(scope)
		if _, err := bm.WriteClusterAuthKeys(nsScope, keys); err != nil {
			fmt.Printf("  [!] backup trust: writing merged authorized_keys for %s failed: %v\n", scope, err)
			continue
		}
		fmt.Printf("  [OK] %s: cross-host backup trust merged %d member key(s)\n", scope, len(keys))
	}
}

// setupPatroniArchiving brings every local Patroni member up to the current
// backup configuration: archive conf mounted + archive parameters rendered in
// its patroni.yml (both land via a container recreate, which is also what
// restarts PostgreSQL for postmaster-level archive_mode). The cluster is
// paused across the operation so recreating the leader cannot strand the
// cluster, and members are recreated replicas-first. No-op for hosts without
// Patroni clusters or fully current members.
func setupPatroniArchiving(cfg *config.Config, bm *podman.BackupManager) error {
	if len(cfg.Addons.Patroni) == 0 {
		fmt.Println("  (no Patroni clusters)")
		return nil
	}
	pm, err := podman.NewPatroniManager(cfg)
	if err != nil {
		return err
	}
	for scope, cluster := range cfg.Addons.Patroni {
		cluster := cluster
		nsScope := cfg.PatroniScope(scope)

		var stale []string
		for member := range cluster.Members {
			ready, reason, err := pm.MemberArchiveReady(&cluster, member)
			if err != nil {
				return err
			}
			if !ready {
				fmt.Printf("  [stale] %s/%s: %s\n", scope, member, reason)
				stale = append(stale, member)
			}
		}
		if len(stale) == 0 {
			fmt.Printf("  [OK] %s: all local members archive-ready\n", scope)
			continue
		}

		fmt.Printf("-> Pausing cluster %s (no auto-failover during recreate)\n", scope)
		if err := pauseCluster(pm, &cluster, nsScope); err != nil {
			return err
		}
		resumeErr := func(opErr error) error {
			if _, err := pm.PatronictlCapture(&cluster, "resume", nsScope, "--wait"); err != nil {
				fmt.Printf("  [!] resuming cluster %s failed: %v\n", scope, err)
				if opErr == nil {
					opErr = fmt.Errorf("resume cluster %s: %w", scope, err)
				}
			}
			return opErr
		}

		// Replicas before the leader: recreating the leader last (under
		// pause) means one planned demotion instead of a cascading one.
		for _, member := range replicasFirstOrder(pm, &cluster, nsScope) {
			if !containsStr(stale, member) {
				continue
			}
			fmt.Printf("  -> Recreating member %s...\n", member)
			if err := pm.RecreateMemberWithImage(&cluster, member, cluster.Members[member].ImageTag); err != nil {
				return resumeErr(fmt.Errorf("recreating member %s: %w", member, err))
			}
			joined := false
			for i := 0; i < 24; i++ { // 2min timeout, same budget as ha extension
				time.Sleep(5 * time.Second)
				if out, err := pm.PatronictlCapture(&cluster, "list", nsScope, "-f", "json"); err == nil &&
					strings.Contains(out, fmt.Sprintf(`"Member": "%s"`, member)) {
					joined = true
					break
				}
			}
			if !joined {
				return resumeErr(fmt.Errorf("member %s did not rejoin within 2m", member))
			}
			fmt.Printf("  [OK] Member %s rejoined\n", member)
		}

		if err := resumeErr(nil); err != nil {
			return err
		}
		fmt.Printf("  [OK] %s: archive-ready after recreating %d member(s)\n", scope, len(stale))
	}
	return nil
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// --- backup start ------------------------------------------------

var backupStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the backup container",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadRawConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		bm, err := podman.NewBackupManager(cfg)
		if err != nil {
			return err
		}

		fmt.Printf("-> Starting backup container %s...\n", cfg.Backup.ContainerName)
		if err := bm.StartBackupContainer(); err != nil {
			return err
		}
		fmt.Println("[OK] Backup container started")
		return nil
	},
}

// --- backup stop -------------------------------------------------

var backupStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the backup container",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadRawConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		bm, err := podman.NewBackupManager(cfg)
		if err != nil {
			return err
		}

		fmt.Printf("-> Stopping backup container %s...\n", cfg.Backup.ContainerName)
		if err := bm.StopBackupContainer(); err != nil {
			return err
		}
		fmt.Println("[OK] Backup container stopped")
		return nil
	},
}

// --- backup status -----------------------------------------------

var backupStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show backup container status",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadRawConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		bm, err := podman.NewBackupManager(cfg)
		if err != nil {
			return err
		}

		fmt.Println("=== backup status ===")

		cs, err := bm.BackupContainerStatus()
		if err != nil {
			return err
		}

		fmt.Printf("Container: %s\n", cs.Name)
		fmt.Printf("  Status:  %s\n", cs.Status)
		if cs.Ports != "" {
			fmt.Printf("  Ports:   %s\n", cs.Ports)
		}

		// Show configured stanzas
		pitrCount := 0
		for _, inst := range cfg.Instances {
			if inst.PITR.Enabled {
				pitrCount++
			}
		}
		fmt.Printf("\nStanzas: %d PITR-enabled instance(s)\n", pitrCount)
		for name, inst := range cfg.Instances {
			if !inst.PITR.Enabled {
				continue
			}
			stanza := inst.PITR.PgBackRestStanza
			if stanza == "" {
				stanza = "pgcli_" + name
			}
			fmt.Printf("  - %s (container: %s)\n", stanza, inst.Podman.ContainerName)
		}

		fmt.Println()
		return nil
	},
}

// --- backup fetch-ca ----------------------------------------------

var fetchCAOut string

var backupFetchCACmd = &cobra.Command{
	Use:   "fetch-ca [endpoint]",
	Short: "Fetch an S3 endpoint's TLS CA certificate over the network",
	Long: `fetch-ca dials the S3 repository endpoint over TLS and saves the CA
certificate that signed its leaf, so a Patroni host on a different machine than
the storage host can trust it without anyone scp'ing ca.crt across.

The endpoint argument defaults to backup.repo.s3.endpoint from pg.yaml. The
command prints the saved path and a sha256 fingerprint — cross-check the
fingerprint against the storage host (sha256sum of its tls/minio/<name>/ca.crt)
the way you would an SSH host key, since fetching a trust anchor before you
trust anything is inherently trust-on-first-use.

Then hand the file to setup, which publishes it to the cluster's etcd registry
so the remaining hosts need nothing at all:

  pg backup setup --s3-ca-file <path>

Works against a pgcli-served MinIO (pgcli's own CA sits in its TLS chain). A
publicly-caught endpoint (real AWS S3) needs no ca_file and this command is
pointless there. This is a deliberate one-shot rather than something setup runs
on its own: a silent TOFU dial on every setup would bury the fingerprint nobody
was asked to look at.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadRawConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		endpoint := ""
		if len(args) == 1 {
			endpoint = args[0]
		} else if cfg.Backup.Repo.S3 != nil {
			endpoint = cfg.Backup.Repo.S3.Endpoint
		}
		if endpoint == "" {
			return fmt.Errorf("no endpoint given and backup.repo.s3.endpoint is unset — usage: pg backup fetch-ca <host:port>")
		}

		pemStr, err := podman.FetchRepoCA(endpoint)
		if err != nil {
			return err
		}

		out := fetchCAOut
		if out == "" {
			base := cfg.BaseDir
			if base == "" {
				base = platform.DefaultConfigDir()
			}
			out = filepath.Join(base, "backup", "repo-ca", "ca-"+caFileStem(podman.NormalizeS3Endpoint(endpoint))+".crt")
		}
		if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
			return fmt.Errorf("creating CA dir: %w", err)
		}
		// 0644: this file is bind-mounted into backup/member containers, whose
		// postgres-uid processes must read it (the same trap WriteClusterAuthKeys
		// documents).
		if err := os.WriteFile(out, []byte(pemStr), 0644); err != nil {
			return fmt.Errorf("writing CA: %w", err)
		}
		if err := os.Chmod(out, 0644); err != nil {
			return fmt.Errorf("chmod %s: %w", out, err)
		}

		sum := sha256.Sum256([]byte(pemStr))
		fmt.Printf("  [OK] CA fetched from %s\n", podman.NormalizeS3Endpoint(endpoint))
		fmt.Printf("       saved:    %s\n", out)
		fmt.Printf("       SHA-256:  %x\n", sum)
		fmt.Printf("Next:         pg backup setup --s3-ca-file %s\n", out)
		return nil
	},
}

// caFileStem turns a normalized host:port into a safe file stem
// (10.0.0.9:9000 -> 10.0.0.9-9000; [fd00::9]:9000 -> fd00--9-9000).
func caFileStem(hostport string) string {
	s := strings.ReplaceAll(hostport, "[", "")
	s = strings.ReplaceAll(s, "]", "")
	return strings.ReplaceAll(s, ":", "-")
}
