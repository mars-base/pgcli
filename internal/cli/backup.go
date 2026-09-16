package cli

import (
	"fmt"
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

	backupSetupCmd.Flags().StringVar(&backupBaseDir, "base-dir", "", "base directory for backup data and logs (overrides config base_dir)")
	backupSetupCmd.Flags().StringVar(&s3Endpoint, "s3-endpoint", "", "S3 repository host:port (pgBackRest forces HTTPS - use a TLS-terminated endpoint, e.g. a MinIO installed with 'pg addon install minio --tls')")
	backupSetupCmd.Flags().StringVar(&s3Bucket, "s3-bucket", "", "S3 bucket (must already exist)")
	backupSetupCmd.Flags().StringVar(&s3AccessKey, "s3-access-key", "", "S3 access key")
	backupSetupCmd.Flags().StringVar(&s3SecretKey, "s3-secret-key", "", "S3 secret key (prefer editing backup.repo.s3.secret_key in pg.yaml so it stays out of shell history)")
	backupSetupCmd.Flags().StringVar(&s3Region, "s3-region", "", "S3 region (default us-east-1)")
	backupSetupCmd.Flags().StringVar(&s3Path, "s3-path", "", "path prefix inside the bucket (default /pgbackrest)")
	backupSetupCmd.Flags().StringVar(&s3CAFile, "s3-ca-file", "", "host path to a PEM CA bundle trusted for the endpoint (mounted into the backup and member containers as /etc/pgbackrest/ca.crt)")
	backupSetupCmd.Flags().BoolVar(&s3NoVerify, "s3-no-verify-tls", false, "skip TLS certificate verification (repo1-s3-verify-tls=n) — escape hatch for self-signed endpoints you cannot hand a CA for")
}

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Manage the shared pgbackrest backup container",
	Long: `backup manages a shared pgbackrest container that handles backups for all instances.

The backup container is shared across all database instances -- each instance
gets its own pgbackrest stanza, but they all share a single pgbackrest repository.

Subcommands:
  setup   Build image, create directories, generate config, start container
  start   Start the backup container
  stop    Stop the backup container
  status  Show backup container status`,
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
		if backupBaseDir != "" {
			cfg.BaseDir = backupBaseDir
			cfg.Backup.DataDir = filepath.Join(backupBaseDir, "backup", "data")
			cfg.Backup.LogDir = filepath.Join(backupBaseDir, "backup", "log")
		}
		defer func() { cfg.BaseDir = origBaseDir }()

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

		// 5. Patroni members: pick up the archive conf + archive parameters.
		// Stale members are recreated replicas-first inside a pause window.
		fmt.Println("\n-> Step 5/6: Patroni WAL archiving...")
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
