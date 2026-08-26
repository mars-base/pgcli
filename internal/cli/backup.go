package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

var backupBaseDir string

func init() {
	rootCmd.AddCommand(backupCmd)
	backupCmd.AddCommand(backupSetupCmd)
	backupCmd.AddCommand(backupStartCmd)
	backupCmd.AddCommand(backupStopCmd)
	backupCmd.AddCommand(backupStatusCmd)

	backupSetupCmd.Flags().StringVar(&backupBaseDir, "base-dir", "", "base directory for backup data and logs (overrides config base_dir)")
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
  4. Create and start the backup container

The backup container is shared across all database instances.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadRawConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

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
		fmt.Println("\n-> Step 1/4: Building backup image...")
		if err := bm.EnsureBackupImage(); err != nil {
			return err
		}

		// 2. Create directories
		fmt.Println("\n-> Step 2/4: Creating backup directories...")
		if err := bm.EnsureBackupDirs(); err != nil {
			return err
		}

		// 3. Generate pgbackrest.conf
		fmt.Println("\n-> Step 3/4: Generating pgbackrest.conf...")
		confPath, err := bm.WritePgbackrestConf()
		if err != nil {
			return err
		}

		// 4. Create and start backup container
		fmt.Println("\n-> Step 4/4: Starting backup container...")

		if err := bm.EnsureBackupContainer(confPath); err != nil {
			return err
		}

		fmt.Println("\nOK backup setup complete!")
		fmt.Printf("  Container: %s\n", cfg.Backup.ContainerName)
		fmt.Printf("  Image:     %s\n", cfg.Backup.ImageTag)
		fmt.Printf("  Config:    %s\n", confPath)
		fmt.Printf("  Stanzas:   %d\n", pitrCount)
		return nil
	},
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
