package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/podman"
)

func init() {
	rootCmd.AddCommand(mcCmd)
}

var mcCmd = &cobra.Command{
	Use:   "mc <command> [args...] [-- <mc-flags>]",
	Short: "Run the MinIO client (mc) via a temporary container",
	Long: `mc runs MinIO's command-line client without installing it: pgcli launches a
short-lived container from the pgcli-mc image and passes your arguments
straight to mc (the image entrypoint). The container is removed after each
command.

Aliases persist on the host at ~/.mc/config.json — mc's native default path
— so ` + "`pg mc alias set`" + ` once works everywhere, including a native mc
install sharing the same file. ` + "`MC_HOST_<name>`" + ` environment variables are
forwarded too, so stateless (no-config) invocations keep working.

On macOS the MinIO addon itself is not supported (Linux only), but pg mc
still works against remote or LAN endpoints — point aliases at a routable
address (or host.containers.internal), not 127.0.0.1, which inside the
container's bridge network is the container itself.

Any mc flag that pg's own flag parser would otherwise reject (for example
--all or --json) goes after -- .

Examples:
  pg mc alias set store http://127.0.0.1:9000 admin <password>
  pg mc ls store
  pg mc mb store/backups
  pg mc cp ./dump.pglz store/backups/
  pg mc ls store -- --all
  MC_HOST_store=http://admin:pass@127.0.0.1:9000 pg mc ls store`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Config load is only for parity with sibling commands and to surface
		// a bad --config early; mc itself needs none of it (image tag is a
		// constant, endpoint comes from the alias or MC_HOST_*).
		if err := loadConfigForDSN(); err != nil {
			return err
		}
		m, err := podman.NewMCRunner()
		if err != nil {
			return fmt.Errorf("mc runner: %w", err)
		}
		// Args after `--` reach us stripped of the separator and in original
		// order, so `args` is already exactly what mc should receive.
		return m.Run(args)
	},
}
