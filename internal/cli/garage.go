package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/podman"
)

func init() {
	rootCmd.AddCommand(garageCmd)
	garageCmd.Flags().String("name", "", "garage instance to run against (default: the first by name)")
	// pgcli's flags are only recognised BEFORE the garage subcommand word, so
	// garage's own flags (--read, --json, -c) pass through untouched instead of
	// being claimed by pg's parser.
	garageCmd.Flags().SetInterspersed(false)
}

var garageCmd = &cobra.Command{
	Use:   "garage <subcommand> [args...]",
	Short: "Run the Garage control CLI inside an instance's container",
	Long: `garage drives Garage's own control CLI (status, health, layout, node, key,
bucket, ...) without you exec'ing into the container: pgcli runs the arguments
verbatim as ` + "`/garage ...`" + ` inside the target instance's running container and
prints what it returns. This is the day-2 operations entry point — mint an
app key, grant it on a bucket, inspect the layout or the replication queue.

The target is --name when given, otherwise the first garage instance by name.
Unlike ` + "`pg etcdctl`" + `/` + "`pg mc`" + ` (temporary client containers), ` + "`pg garage`" + `
needs a RUNNING member: the CLI reads the node's local garage.toml and RPC
endpoint, so the container is the only place it can run.

pgcli's own flags are only recognised before the subcommand word; everything
after it is forwarded to garage verbatim, so garage's flags (--read --write,
-z, -c, ...) need no ` + "`--`" + ` separator.

Output on a cluster member is the raw garage table. garage's CLI is known to
print its result and then linger (peers dial it back); pgcli cuts such a
command off once its output has been quiet for 3s, so a hang here surfaces as
a loud timeout instead — re-run the same command.

Examples:
  pg garage status
  pg garage --name store key create app-key
  pg garage --name store bucket allow store --key GK... --read --write --owner
  pg garage --name store layout show
  pg garage --name store health
  pg garage bucket list`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadAddonCfg()
		if err != nil {
			return err
		}
		name, _ := cmd.Flags().GetString("name")
		gc, err := resolveGarageTarget(c, name)
		if err != nil {
			return err
		}
		gm, err := podman.NewGarageManager(c)
		if err != nil {
			return fmt.Errorf("garage manager: %w", err)
		}
		// Generous bound: this is a passthrough, and ops like a rebalancing
		// `layout apply` legitimately run for minutes. The 3s idle-kill inside
		// still resolves garage's print-then-linger fast.
		out, execErr := gm.ExecCaptureTimeout(gc, garageCLIPassthroughTimeout, args...)
		// garage tables go to stdout, its log lines to stderr; ExecCapture
		// merges and sanitises both, so print whatever arrived before deciding
		// whether it also failed.
		fmt.Print(out)
		if out != "" && out[len(out)-1] != '\n' {
			fmt.Println()
		}
		return execErr
	},
}

// garageCLIPassthroughTimeout bounds a user-invoked `pg garage` control
// command; the install flow's own 45s ExecCapture stays as is.
const garageCLIPassthroughTimeout = 10 * time.Minute

// resolveGarageTarget picks the GarageConfig for `pg garage`: the named
// instance, or the first by name when --name is absent. With several instances
// configured and no --name, the choice is announced on stderr so piped output
// stays clean (redis_cli.go pattern).
func resolveGarageTarget(c *config.Config, name string) (*config.GarageConfig, error) {
	if len(c.Addons.Garage) == 0 {
		return nil, fmt.Errorf("no garage add-ons configured — install one with `pg addon install garage --name <n>`")
	}
	if name == "" {
		names := sortedAddonNames(c.Addons.Garage)
		name = names[0]
		if len(names) > 1 {
			gc := c.Addons.Garage[name]
			fmt.Fprintf(os.Stderr, "-> garage → %s (%s) — pick another with --name\n", name, gc.ContainerName)
		}
	}
	gc, ok := c.Addons.Garage[name]
	if !ok {
		return nil, fmt.Errorf("garage %q not found (available: %v)", name, sortedAddonNames(c.Addons.Garage))
	}
	return &gc, nil
}
