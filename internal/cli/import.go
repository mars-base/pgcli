package cli

import (
	"github.com/spf13/cobra"
)

var importCmd = &cobra.Command{
	Use:   "import [flags] <input-file>",
	Short: "Import database from dump file",
	Long: `Import database from a dump file using pg_restore or psql.

Automatically detects format and compression from filename.
- .sql or .sql.gz → plain SQL format (psql)
- other extensions → custom format (pg_restore)

Examples:
  pg import -i proj02 dump.dump                 # custom format
  pg import -i proj02 dump.sql                     # SQL format
  pg import -i proj02 dump.dump.gz              # custom + gzip
  pg import -i proj02 -d other_db dump.sql         # specific database
  pg import -i proj02 --clean dump.dump         # drop objects before restore`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := loadConfig(); err != nil {
			return err
		}

		pm, err := newPodman()
		if err != nil {
			return err
		}

		inputFile := args[0]

		database, _ := cmd.Flags().GetString("database")
		if database == "" {
			database = cfg.Postgres.Database
		}

		clean, _ := cmd.Flags().GetBool("clean")
		verbose, _ := cmd.Flags().GetBool("verbose")

		return pm.ImportDatabase(inputFile, database, clean, verbose)
	},
}

func init() {
	importCmd.Flags().StringP("database", "d", "", "database name (default: instance database)")
	importCmd.Flags().Bool("clean", false, "drop database objects before restoring")
	importCmd.Flags().BoolP("verbose", "v", false, "show progress during import")

	rootCmd.AddCommand(importCmd)
}
