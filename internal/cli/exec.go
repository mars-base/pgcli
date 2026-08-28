package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

func init() {
	execCmd.Flags().String("dsn", "", "database connection string for remote database (postgres://user:pass@host:port/db)")
	rootCmd.AddCommand(execCmd)
}

var execCmd = &cobra.Command{
	Use:   "exec [flags] [-- <command> [args...]]",
	Short: "Execute SQL or a command inside the PostgreSQL container",
	Long: `exec runs SQL or a command inside the running PostgreSQL container.

Without --, the arguments are treated as SQL and executed via psql
with the instance's configured user and database.

With --, the arguments are passed directly as a container command.

Examples:
  pg exec "SELECT version()"
  pg exec "CREATE TABLE test (id serial PRIMARY KEY, msg text)"
  pg exec -i myinst "SELECT count(*) FROM users"
  pg exec -- psql -U pgcli -d mydb
  pg exec -- bash -c "cat /var/lib/postgresql/data/postgresql.conf"
  pg exec -- pg_isready
  pg exec --dsn postgres://user:pass@host:5432/db "SELECT count(*) FROM users"`,
	DisableFlagParsing: false,
	RunE: func(cmd *cobra.Command, args []string) error {
		dsn, _ := cmd.Flags().GetString("dsn")

		if len(args) == 0 {
			return fmt.Errorf("no command specified. Usage: pg exec \"SELECT 1\" or pg exec -- <command>")
		}

		// Container commands (after --) only make sense for a local instance.
		if dsn != "" && cmd.ArgsLenAtDash() != -1 {
			return fmt.Errorf("--dsn only supports SQL mode; container commands require a local instance (drop --dsn)")
		}

		if dsn != "" {
			if err := checkDSNInstanceConflict(cmd); err != nil {
				return err
			}
			if err := loadConfigForDSN(); err != nil {
				return err
			}
			pm, err := newPodman()
			if err != nil {
				return err
			}
			return pm.ExecDSN(dsn, strings.Join(args, " "))
		}

		if err := loadConfig(); err != nil {
			return err
		}
		containerName := cfg.Podman.ContainerName

		// Check if container is running
		checkCmd := exec.Command("podman", "inspect", "-f", "{{.State.Running}}", containerName)
		output, err := checkCmd.Output()
		if err != nil {
			return fmt.Errorf("container '%s' not found. Run 'pg start -i %s' to create and start it", containerName, cfg.Instance)
		}
		if strings.TrimSpace(string(output)) != "true" {
			return fmt.Errorf("container '%s' is stopped. Run 'pg start -i %s' to start it", containerName, cfg.Instance)
		}

		// Check if -- was used (cobra passes args after -- as positional args).
		// If the user provided --, args are raw container commands.
		// If not, treat the joined args as SQL for psql.
		dashDash := cmd.ArgsLenAtDash()
		var podmanArgs []string

		if dashDash == -1 {
			// No -- : treat as SQL
			sql := strings.Join(args, " ")
			podmanArgs = []string{"exec", "-i", containerName,
				"psql", "-U", cfg.Postgres.User, "-d", cfg.Postgres.Database,
				"-c", sql,
			}
		} else {
			// -- present: pass through as container command
			podmanArgs = []string{"exec", "-i", containerName}
			podmanArgs = append(podmanArgs, args...)
		}

		execCmd := exec.Command("podman", podmanArgs...)
		execCmd.Stdin = os.Stdin
		execCmd.Stdout = os.Stdout
		execCmd.Stderr = os.Stderr
		return execCmd.Run()
	},
}
