package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/podman"
)

// pg ha exec / pg ha psql run SQL against a Patroni cluster without entering a
// member container and without the user assembling a --dsn: pgcli resolves the
// target from the DCS roster (leader by default, --member to aim at a specific
// node), builds the connection string from the stored superuser password, and
// streams psql from a throwaway host-network container. The target may be on a
// remote host — the connect path is plain TCP + scram, same as replication.

func init() {
	haCmd.AddCommand(haExecCmd, haPsqlCmd)
	for _, c := range []*cobra.Command{haExecCmd, haPsqlCmd} {
		c.Flags().String("member", "", "target this member instead of the leader (e.g. a replica for read-only queries)")
		c.Flags().String("database", "postgres", "target database")
	}
}

var haExecCmd = &cobra.Command{
	Use:   "exec <scope> <sql...>",
	Short: "Run SQL against a Patroni cluster (no dsn, no container exec)",
	Long: `exec runs one-shot SQL against a Patroni cluster (scope) and streams the
psql output — headers, column widths, errors — straight to the terminal.

No --dsn to assemble and no member container to enter: the target is resolved
from the cluster's own state and the stored superuser password is used for
authentication. By default the query goes to the current leader; --member aims
it at a specific member (a replica gives you a read-only view). Remote members
work too — this is a plain TCP connection, not a podman exec.

Examples:
  pg ha exec app "SELECT version()"
  pg ha exec app "SELECT count(*) FROM pg_stat_activity"
  pg ha exec app --member node2 "SELECT pg_is_in_recovery()"
  pg ha exec app --database mydb "SELECT * FROM t LIMIT 5"`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := loadConfigForDSN(); err != nil {
			return err
		}
		cluster, err := lookupHACluster(args[0])
		if err != nil {
			return err
		}
		sql := strings.Join(args[1:], " ")
		if strings.TrimSpace(sql) == "" {
			return fmt.Errorf("no SQL given — pg ha exec %s \"SELECT 1\"", args[0])
		}
		member, _ := cmd.Flags().GetString("member")
		database, _ := cmd.Flags().GetString("database")
		pm, err := podman.NewPatroniManager(cfg)
		if err != nil {
			return err
		}
		return pm.ExecHA(cluster, member, database, sql)
	},
}

var haPsqlCmd = &cobra.Command{
	Use:   "psql <scope> [-- <psql-args>...]",
	Short: "Open interactive psql against a Patroni cluster (no dsn, no container exec)",
	Long: `psql opens an interactive psql shell against a Patroni cluster (scope),
the cluster-side twin of ` + "`pg psql`" + `. It connects to the current
leader — or to one member with --member — through the same resolved-target
connection as ` + "`pg ha exec`" + `: no --dsn, no member container.

Additional psql arguments are passed through after --.

Examples:
  pg ha psql app
  pg ha psql app --member node3
  pg ha psql app --database mydb
  pg ha psql app -- -c "SELECT 1"`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := loadConfigForDSN(); err != nil {
			return err
		}
		cluster, err := lookupHACluster(args[0])
		if err != nil {
			return err
		}
		member, _ := cmd.Flags().GetString("member")
		database, _ := cmd.Flags().GetString("database")
		pm, err := podman.NewPatroniManager(cfg)
		if err != nil {
			return err
		}
		return pm.PsqlHA(cluster, member, database, args[1:])
	},
}
