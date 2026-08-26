package cli

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"os"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

var createBaseDir string

func init() {
	rootCmd.AddCommand(createCmd)
	createCmd.Flags().StringVar(&createBaseDir, "base-dir", "", "custom base directory for data and wal (overrides config base_dir)")
}

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new database instance",
	Long: `Create a new PostgreSQL database instance with a random password.

The instance name is specified with -i / --instance.
Database name is derived as <instance>_db.

Data directory uses the base_dir from config if set,
otherwise defaults to ~/.pgcli/dbdata/<instance>/.
Use --base-dir to override the config base_dir for this instance.

Examples:
  pg create -i proj01
  pg create -i myapp --config ./custom.yaml
  pg create -i proj01 --base-dir /data/pg`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Determine config path
		path := cfgPath
		if path == "" {
			path = platform.DefaultConfigPath()
		}

		// Check if config file exists
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return fmt.Errorf("config file not found: %s -- run \"pg config init\" first", path)
		}

		// Load existing config (without SetInstance)
		cfg, err := config.Load(path)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		// Check if instance already exists
		if _, ok := cfg.Instances[cfgInstance]; ok {
			return fmt.Errorf("instance %q already exists in config", cfgInstance)
		}

		// Generate random password
		password, err := generatePassword(16)
		if err != nil {
			return fmt.Errorf("failed to generate password: %w", err)
		}

		// Override BaseDir temporarily for path computation (per-instance only, not persisted)
		origBaseDir := cfg.BaseDir
		if createBaseDir != "" {
			cfg.BaseDir = createBaseDir
		}

		// Build instance config -- InstanceDefaults respects cfg.BaseDir
		inst := cfg.InstanceDefaults(cfgInstance)
		inst.Postgres.Database = cfgInstance + "_db"
		inst.Postgres.Password = password

		// Restore original BaseDir so Save() doesn't persist the flag override
		cfg.BaseDir = origBaseDir

		// Add instance to config
		cfg.Instances[cfgInstance] = *inst
		cfg.ApplyDefaults()
		if err := cfg.Save(path); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		fmt.Printf("[OK] instance %q created\n", cfgInstance)
		fmt.Printf("  database:   %s\n", inst.Postgres.Database)
		fmt.Printf("  password:   %s\n", inst.Postgres.Password)
		fmt.Printf("  container:  %s\n", inst.Podman.ContainerName)
		fmt.Printf("  data_dir:   %s\n", inst.Podman.DataDir)
		fmt.Printf("  stanza:     %s\n", inst.PITR.PgBackRestStanza)
		fmt.Printf("  config:     %s\n", path)
		return nil
	},
}

// generatePassword generates a random password with the given length.
// Characters: a-z, A-Z, 0-9.
func generatePassword(length int) (string, error) {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return "", err
		}
		b[i] = chars[n.Int64()]
	}
	return string(b), nil
}
