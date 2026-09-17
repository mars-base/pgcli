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
var createPasswordLength int

func init() {
	rootCmd.AddCommand(createCmd)
	createCmd.Flags().StringVar(&createBaseDir, "base-dir", "", "custom base directory for data and wal (overrides config base_dir)")
	createCmd.Flags().IntVar(&createPasswordLength, "password-length", defaultPasswordLength, "length of the generated password (8-64)")
}

// defaultPasswordLength is the length used when --password-length is not given,
// with passwordLengthFloor / passwordLengthCeiling as the accepted range.
// Passwords are drawn from a 62-char set (a-z A-Z 0-9) via crypto/rand, so 16
// chars is ~95 bits of entropy; the bounds only guard against a mistyped,
// effectively-brute-forceable or absurdly long value.
const (
	defaultPasswordLength = 16
	passwordLengthFloor   = 8
	passwordLengthCeiling = 64
)

// validatePasswordLength rejects a length outside [floor, ceiling].
func validatePasswordLength(n int) error {
	if n < passwordLengthFloor || n > passwordLengthCeiling {
		return fmt.Errorf("--password-length must be between %d and %d (got %d)",
			passwordLengthFloor, passwordLengthCeiling, n)
	}
	return nil
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

The password is a random string (a-z A-Z 0-9, crypto/rand); its length defaults to
16 and is adjustable with --password-length (8-64).

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
		if err := validatePasswordLength(createPasswordLength); err != nil {
			return err
		}
		password, err := generatePassword(createPasswordLength)
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
