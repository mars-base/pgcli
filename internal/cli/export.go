package cli

import (
	"github.com/spf13/cobra"
)

var exportCmd = &cobra.Command{
	Use:   "export [flags] [-o <output-file>]",
	Short: "Export database to dump file",
	Long: `Export database to a dump file using pg_dump.

Supports custom format (pg_dump -Fc, recommended) and plain SQL format.
Automatically compresses if output filename ends with .gz.
When no output file is specified, writes to stdout (for piping).

Examples:
  pg export -i proj01 -o dump.dump              # custom format
  pg export -i proj01 -o dump.sql                  # SQL format
  pg export -i proj01 -o dump.dump.gz           # custom + gzip
  pg export -i proj01 -o dump.sql.gz               # SQL + gzip
  pg export -i proj01 -d other_db -o dump.sql      # specific database
  pg export -i proj01 -o dump.sql --compress=9     # max compression
  pg export -i proj01 | pg import -i proj02       # pipe between instances`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := loadConfig(); err != nil {
			return err
		}

		pm, err := newPodman()
		if err != nil {
			return err
		}

		outputFile, _ := cmd.Flags().GetString("output")

		database, _ := cmd.Flags().GetString("database")
		if database == "" {
			database = cfg.Postgres.Database
		}

		compressLevel, _ := cmd.Flags().GetInt("compress")
		verbose, _ := cmd.Flags().GetBool("verbose")

		return pm.ExportDatabase(outputFile, database, compressLevel, verbose)
	},
}

func init() {
	exportCmd.Flags().StringP("output", "o", "", "output file (writes to stdout if not specified)")
	exportCmd.Flags().StringP("database", "d", "", "database name (default: instance database)")
	exportCmd.Flags().Int("compress", 6, "compression level (0-9, for plain format)")
	exportCmd.Flags().BoolP("verbose", "v", false, "show progress during export")

	rootCmd.AddCommand(exportCmd)
}
