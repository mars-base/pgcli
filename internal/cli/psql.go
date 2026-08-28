package cli

import (
	"github.com/spf13/cobra"
)

func init() {
	psqlCmd.Flags().String("dsn", "", "database connection string for remote database (postgres://user:pass@host:port/db)")
	rootCmd.AddCommand(psqlCmd)
}

var psqlCmd = &cobra.Command{
	Use:   "psql [flags] [-- <psql-args>...]",
	Short: "Open interactive psql session",
	Long: `psql opens an interactive PostgreSQL shell (psql) inside the container.

Additional psql arguments are passed through after --.

With --dsn, connects to a remote database via a temporary container.

Examples:
  pg psql                              # interactive psql
  pg psql -i proj01                    # specific instance
  pg psql -- -c "SELECT version()"     # one-shot SQL
  pg psql -- -d other_db               # connect to different database
  pg psql -- -U other_user             # connect as different user
  pg psql --dsn postgres://user:pass@host:5432/db`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dsn, _ := cmd.Flags().GetString("dsn")

		if dsn != "" {
			if err := loadConfigForDSN(); err != nil {
				return err
			}
			pm, err := newPodman()
			if err != nil {
				return err
			}
			return pm.PsqlDSN(dsn, args)
		}

		if err := loadConfig(); err != nil {
			return err
		}

		pm, err := newPodman()
		if err != nil {
			return err
		}

		// Default args: psql with instance user and database
		psqlArgs := []string{"psql", "-U", cfg.Postgres.User, "-d", cfg.Postgres.Database}
		psqlArgs = append(psqlArgs, args...)

		return pm.ExecInteractive(psqlArgs...)
	},
}
