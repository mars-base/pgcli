package cli

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// Parent command: pg logs (default behavior = show PG instance logs)
// ---------------------------------------------------------------------------

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "View PostgreSQL instance logs",
	Long: `View PostgreSQL instance console output logs.

By default, shows the PostgreSQL server logs for the current instance
(without -i, defaults to "default").
Use "pg logs addon" to view addon console output logs.

Examples:
  pg logs                              # PG logs (default instance), last 50 lines
  pg logs -f                           # Follow PG logs
  pg logs -n 200                       # Last 200 lines
  pg logs -i myinst                    # Specific instance
  pg logs addon pgbouncer -i myinst    # Local PgBouncer logs for myinst
  pg logs addon pgbouncer --pg-name my-pool   # Remote PgBouncer logs
  pg logs addon pgbouncer --pg-name my-pool -f`,
	RunE: func(cmd *cobra.Command, args []string) error {
		follow, _ := cmd.Flags().GetBool("follow")
		tail, _ := cmd.Flags().GetInt("tail")

		if err := loadConfig(); err != nil {
			return err
		}

		return runPodmanLogs(cfg.Podman.ContainerName, tail, follow)
	},
}

// ---------------------------------------------------------------------------
// Subcommand: pg logs addon <type> --pg-name <name>
// ---------------------------------------------------------------------------

var logsAddonCmd = &cobra.Command{
	Use:   "addon <addon-type>",
	Short: "Show addon console output logs",
	Long: `Show addon console output logs.

Requires the addon type (e.g. pgbouncer).
Use -i for local instance addons, --pg-name for remote addons.

Examples:
  pg logs addon pgbouncer -i myinst
  pg logs addon pgbouncer -i myinst -f
  pg logs addon pgbouncer -i myinst
  pg logs addon pgbouncer -i myinst -f
  pg logs addon pgbouncer --pg-name my-pool
  pg logs addon pgbouncer --pg-name my-pool -f
  pg logs addon pgbouncer --pg-name my-pool -n 200`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		follow, _ := cmd.Flags().GetBool("follow")
		tail, _ := cmd.Flags().GetInt("tail")
		pgName, _ := cmd.Flags().GetString("pg-name")

		addonType := args[0]
		if addonType != "pgbouncer" {
			return fmt.Errorf("unknown addon: %s (available: pgbouncer)", addonType)
		}

		if err := loadConfig(); err != nil {
			return err
		}

		var containerName string

		if pgName != "" {
			// Remote mode
			if cfg.Addons.PgBouncer == nil {
				return fmt.Errorf("no remote PgBouncer addons configured")
			}
			pb, ok := cfg.Addons.PgBouncer[pgName]
			if !ok {
				return fmt.Errorf("remote PgBouncer %q not found (use 'pg addon list' to see available)", pgName)
			}
			containerName = pb.ContainerName
		} else {
			// Local mode: -i
			inst, ok := cfg.Instances[cfg.Instance]
			if !ok {
				return fmt.Errorf("instance %q not found", cfg.Instance)
			}
			if inst.Addons.PgBouncer == nil {
				return fmt.Errorf("no PgBouncer addon configured for instance %q", cfg.Instance)
			}
			containerName = inst.Addons.PgBouncer.ContainerName
		}

		return runPodmanLogs(containerName, tail, follow)
	},
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

func init() {
	// Flags on parent (pg logs)
	logsCmd.Flags().BoolP("follow", "f", false, "Stream logs continuously")
	logsCmd.Flags().IntP("tail", "n", 50, "Number of lines to show (0 = all)")

	// Flags on addon subcommand
	logsAddonCmd.Flags().BoolP("follow", "f", false, "Stream logs continuously")
	logsAddonCmd.Flags().IntP("tail", "n", 50, "Number of lines to show (0 = all)")
	logsAddonCmd.Flags().String("pg-name", "", "Remote addon pooler name (required)")

	rootCmd.AddCommand(logsCmd)
	logsCmd.AddCommand(logsAddonCmd)
}

// ---------------------------------------------------------------------------
// log output helpers
// ---------------------------------------------------------------------------

// runPodmanLogs runs podman logs directly, output goes to os.Stdout.
func runPodmanLogs(containerName string, tail int, follow bool) error {
	args := []string{"logs"}

	if follow {
		args = append(args, "-f")
	}

	if tail > 0 {
		args = append(args, "--tail", fmt.Sprintf("%d", tail))
	} else if !follow {
		args = append(args, "--tail", "all")
	}

	args = append(args, containerName)

	cmd := exec.Command("podman", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
