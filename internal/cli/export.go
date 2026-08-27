package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var exportCmd = &cobra.Command{
	Use:   "export [flags] -o <output-file>",
	Short: "Export database to dump file",
	Long: `Export database to a dump file using pg_dump.

Supports custom format (pg_dump -Fc, recommended) and plain SQL format.
Automatically compresses if output filename ends with .gz.

Examples:
  pg export -i proj01 -o dump.dump              # custom format
  pg export -i proj01 -o dump.sql                  # SQL format
  pg export -i proj01 -o dump.dump.gz           # custom + gzip
  pg export -i proj01 -o dump.sql.gz               # SQL + gzip
  pg export -i proj01 -d other_db -o dump.sql      # specific database
  pg export -i proj01 -o dump.sql --compress=9     # max compression`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := loadConfig(); err != nil {
			return err
		}

		pm, err := newPodman()
		if err != nil {
			return err
		}

		outputFile, _ := cmd.Flags().GetString("output")
		if outputFile == "" {
			return fmt.Errorf("output file is required (-o or --output)")
		}

		database, _ := cmd.Flags().GetString("database")
		if database == "" {
			database = cfg.Postgres.Database
		}

		compressLevel, _ := cmd.Flags().GetInt("compress")

		return pm.ExportDatabase(outputFile, database, compressLevel)
	},
}

func init() {
	exportCmd.Flags().StringP("output", "o", "", "output file (required)")
	exportCmd.Flags().StringP("database", "d", "", "database name (default: instance database)")
	exportCmd.Flags().Int("compress", 6, "compression level (0-9, for plain format)")
	exportCmd.MarkFlagRequired("output")

	rootCmd.AddCommand(exportCmd)
}
