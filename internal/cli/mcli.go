package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/podman"
)

func init() {
	rootCmd.AddCommand(mcliCmd)
}

var mcliCmd = &cobra.Command{
	Use:   "mcli <command> [args...] [-- <mcli-flags>]",
	Short: "Run the silo client (mcli) via a temporary container",
	Long: `mcli runs silo's (Pigsty's MinIO fork) command-line client without installing
it: pgcli launches a short-lived container from the docker.io/pgsty/silo image
and passes your arguments straight to mcli (selected via --entrypoint, since
the image's default entrypoint runs the server). The container is removed after
each command.

mcli speaks the same alias/config contract as mc, so it interoperates with a
minio or silo store alike. Aliases persist on the host at ~/.mcli/config.json —
mcli's native default path — so ` + "`pg mcli alias set`" + ` once works everywhere,
including a native mcli install sharing the same file. ` + "`MC_HOST_<name>`" + `
environment variables are forwarded too, so stateless (no-config) invocations
keep working.

On macOS the container sits on the bridge network, where a 127.0.0.1 alias is
the container's own loopback — point an alias at a local pg addon silo instance
via host.containers.internal:<port>, or at a remote store via its routable
address. Local file operands of cp/mirror/diff are mounted into the container
at their real (absolute) paths, so uploads and downloads work as expected; on
macOS the path must be under your home directory.

Any mcli flag that pg's own flag parser would otherwise reject (for example
--all or --json) goes after -- .

Examples:
  pg mcli alias set store http://127.0.0.1:9000 admin <password>
  pg mcli ls store
  pg mcli mb store/backups
  pg mcli cp ./dump.pglz store/backups/
  pg mcli ls store -- --all
  MC_HOST_store=http://admin:pass@127.0.0.1:9000 pg mcli ls store`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Config load is only for parity with sibling commands and to surface
		// a bad --config early; mcli itself needs none of it (image tag is a
		// constant, endpoint comes from the alias or MC_HOST_*).
		if err := loadConfigForDSN(); err != nil {
			return err
		}
		m, err := podman.NewMCLIRunner()
		if err != nil {
			return fmt.Errorf("mcli runner: %w", err)
		}
		// Args after `--` reach us stripped of the separator and in original
		// order, so `args` is already exactly what mcli should receive.
		return m.Run(args)
	},
}
