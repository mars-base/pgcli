package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

var (
	configJSON        bool
	configAdd         string
	configBaseDir     string
	configNamespace   string
	configPGStartPort int
	configPGSSHPort   int
)

func init() {
	rootCmd.AddCommand(configCmd)
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configInitCmd)
	configCmd.AddCommand(configValidateCmd)

	configShowCmd.Flags().BoolVar(&configJSON, "json", false, "output in JSON format")
	configInitCmd.Flags().StringVar(&configAdd, "add", "", "add an instance with this name during init (default: no instances)")
	configInitCmd.Flags().StringVar(&configBaseDir, "base-dir", "", "base directory for all data paths (default ~/.pgcli)")
	configInitCmd.Flags().StringVar(&configNamespace, "namespace", "default", "namespace prefix for container names (isolates configs sharing one host; pass \"\" to disable)")
	configInitCmd.Flags().IntVar(&configPGStartPort, "pg-start-port", 35432, "starting port for PG host port allocation")
	configInitCmd.Flags().IntVar(&configPGSSHPort, "pg-ssh-port", 42201, "starting port for SSH host port allocation")
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Configuration management",
	Long: `config manages pg configuration files.

  Use -o / --output to specify a custom output path (init command).`,
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show current configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Load raw config without instance overlay to avoid duplicate fields
		path := cfgPath
		if path == "" {
			path = platform.DefaultConfigPath()
		}
		rawCfg, err := config.Load(path)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if configJSON {
			data, err := json.MarshalIndent(rawCfg.Display(), "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
		} else {
			data, err := yaml.Marshal(rawCfg.Display())
			if err != nil {
				return err
			}
			fmt.Println(string(data))
		}
		return nil
	},
}

var configInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate default configuration file",
	Long: `Generate a default configuration file.

By default, instances start empty. Use --add to include a named instance template.
Use --output / -o to specify a custom output path.
Use --base-dir to set a custom base directory for all data paths (backup and db data).

--namespace prefixes every container name with <namespace>- so multiple config
files on the same host can manage isolated instances without name clashes
(default: "default"; pass --namespace "" to keep legacy names without a prefix).
--pg-start-port / --pg-ssh-port set the starting ports for automatic PG and
SSH port allocation (defaults 35432 / 42201); give different configs disjoint
port ranges so they never collide.

Examples:
  pg config init                              # empty instances (default path ~/.pgcli)
  pg config init -o ./my-pg.yaml          # custom output path
  pg config init --add default                # add a "default" instance
  pg config init --base-dir /data/pg        # all data under /data/pg
  pg config init -o ./pg.yaml --add myproj --base-dir /mnt/storage/pg
  pg config init --namespace t1 --pg-start-port 38000 --pg-ssh-port 43000 --add proj1` ,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := cfgOutput
		if path == "" {
			path = platform.DefaultConfigPath()
		}

		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("config file already exists: %s -- remove it first or use -o / --output to specify a different path", path)
		}

		cfg := config.Default()

		// Namespace and port bases (applied before ApplyDefaults so zero
		// values fall back to defaults, and so the namespace can rename the
		// backup container and instance container names).
		cfg.Namespace = configNamespace
		cfg.PGStartPort = configPGStartPort
		cfg.PGSSHPort = configPGSSHPort

		// Set base directory if --base-dir is provided
		if configBaseDir != "" {
			// Check that the path is usable: reject if it's an existing file;
			// warn if it's a non-empty directory that will gain pg subdirs.
			if info, err := os.Stat(configBaseDir); err == nil {
				if !info.IsDir() {
					return fmt.Errorf("base-dir %s exists but is not a directory", configBaseDir)
				}
				entries, err := os.ReadDir(configBaseDir)
				if err == nil && len(entries) > 0 {
					fmt.Printf("Warning: base-dir %s already exists and is not empty.\n", configBaseDir)
					fmt.Printf("pg will create subdirectories (dbdata/, backup/) alongside existing content.\n")
					if !confirmPrompt("Continue? [y/N]: ") {
						return fmt.Errorf("aborted by user")
					}
				}
			}
			cfg.BaseDir = configBaseDir
			cfg.Backup.DataDir = filepath.Join(configBaseDir, "backup", "data")
			cfg.Backup.LogDir = filepath.Join(configBaseDir, "backup", "log")
		}

		if configAdd != "" {
			inst := cfg.InstanceDefaults(configAdd)
			password, err := generatePassword(16)
			if err != nil {
				return fmt.Errorf("failed to generate password: %w", err)
			}
			inst.Postgres.Password = password
			cfg.Instances[configAdd] = *inst
		}

		cfg.ApplyDefaults()

		if err := cfg.Save(path); err != nil {
			return err
		}

		fmt.Printf("[OK] config file generated: %s\n", path)
		if configBaseDir != "" {
			fmt.Printf("  base-dir:    %s\n", configBaseDir)
		}
		fmt.Printf("  namespace:   %s\n", cfg.Namespace)
		fmt.Printf("  pg-start-port: %d\n", cfg.PGStartPort)
		fmt.Printf("  pg-ssh-port:   %d\n", cfg.PGSSHPort)
		if len(cfg.Instances) == 0 {
			fmt.Println("  instances: (empty -- add instances manually or use --add)")
		} else {
			for name := range cfg.Instances {
				fmt.Printf("  instances.%s: ready\n", name)
			}
		}
		return nil
	},
}

var configValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate configuration file",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Load raw config without SetInstance -- validate checks the file structure,
		// not whether a particular instance is ready to run.
		path := cfgPath
		if path == "" {
			path = platform.DefaultConfigPath()
		}
		rawCfg, err := config.Load(path)
		if err != nil {
			return err
		}

		if err := rawCfg.Validate(); err != nil {
			return err
		}

		if len(rawCfg.Instances) == 0 {
			fmt.Println("Info: No instances configured (add instances under the `instances:` key)")
		} else {
			for name, inst := range rawCfg.Instances {
				if inst.Podman.ContainerName == "" {
					return fmt.Errorf("instances.%s.podman.container_name must not be empty", name)
				}
				if inst.PITR.Enabled && inst.PITR.PgBackRestStanza == "" {
					return fmt.Errorf("instances.%s.pitr.pgbackrest_stanza must not be empty (PITR is enabled)", name)
				}
			}
		}

		fmt.Println("[OK] config validation passed")
		return nil
	},
}
