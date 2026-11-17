package cli

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

// isCgroupPermError detects rootless podman cgroup.procs permission denied errors.
func isCgroupPermError(msg string) bool {
	return strings.Contains(msg, "cgroup.procs") && strings.Contains(msg, "Permission denied")
}

func init() {
	execCmd.Flags().String("dsn", "", "database connection string for remote database (postgres://user:pass@host:port/db)")
	execCmd.Flags().StringP("database", "d", "", "database for SQL mode (defaults to the instance or DSN database)")
	rootCmd.AddCommand(execCmd)
}

// execDSNWithDatabase rewrites the database (URL path) of a DSN while keeping
// the credentials, host, and query parameters intact.
func execDSNWithDatabase(dsn, database string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("invalid DSN: %w", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", fmt.Errorf("invalid DSN: scheme must be postgres:// or postgresql://")
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid DSN: missing host")
	}
	u.Path = "/" + database
	u.RawPath = ""
	return u.String(), nil
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
  pg exec -i myinst --database bench_db "SELECT count(*) FROM pgbench_accounts"
  pg exec -- psql -U pgcli -d mydb
  pg exec -- bash -c "cat /var/lib/postgresql/data/postgresql.conf"
  pg exec -- pg_isready
  pg exec --dsn postgres://user:pass@host:5432/db "SELECT count(*) FROM users"
  pg exec --dsn postgres://user:pass@host:5432/db --database bench_db "SELECT 1"`,
	DisableFlagParsing: false,
	RunE: func(cmd *cobra.Command, args []string) error {
		dsn, _ := cmd.Flags().GetString("dsn")
		database, _ := cmd.Flags().GetString("database")

		if len(args) == 0 {
			return fmt.Errorf("no command specified. Usage: pg exec \"SELECT 1\" or pg exec -- <command>")
		}

		dashDash := cmd.ArgsLenAtDash()
		if dashDash != -1 && database != "" {
			return fmt.Errorf("--database only applies to SQL mode; pass the database to the container command instead")
		}

		// Container commands (after --) only make sense for a local instance.
		if dsn != "" && dashDash != -1 {
			return fmt.Errorf("--dsn only supports SQL mode; container commands require a local instance (drop --dsn)")
		}

		if dsn != "" {
			if err := checkDSNInstanceConflict(cmd); err != nil {
				return err
			}
			if err := loadConfigForDSN(); err != nil {
				return err
			}
			if database != "" {
				var err error
				dsn, err = execDSNWithDatabase(dsn, database)
				if err != nil {
					return err
				}
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
		var podmanArgs []string

		if dashDash == -1 {
			// No -- : treat as SQL
			sql := strings.Join(args, " ")
			db := cfg.Postgres.Database
			if database != "" {
				db = database
			}
			podmanArgs = []string{"exec", "-i", containerName,
				"psql", "-U", cfg.Postgres.User, "-d", db,
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
		var stderrBuf strings.Builder
		execCmd.Stderr = &stderrBuf
		if err := execCmd.Run(); err != nil {
			// Cgroup fallback: container started by systemd user unit, caller in different scope
			if isCgroupPermError(stderrBuf.String()) {
				runArgs := append([]string{"--user", "--scope", "--quiet", "podman"}, podmanArgs...)
				cmd2 := exec.Command("systemd-run", runArgs...)
				cmd2.Stdin = os.Stdin
				cmd2.Stdout = os.Stdout
				cmd2.Stderr = os.Stderr
				return cmd2.Run()
			}
			// Not a cgroup error — replay captured stderr and return original error
			fmt.Fprint(os.Stderr, stderrBuf.String())
			return err
		}
		return nil
	},
}
