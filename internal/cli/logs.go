package cli

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
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
  pg logs addon pgbouncer --pg-name my-pool -f
  pg logs addon etcd --name m1         # etcd member logs
  pg logs addon pgdog --name proxy     # PgDog proxy logs
  pg logs addon haproxy --name lb      # HAProxy instance logs`,
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

Requires the addon type (pgbouncer, etcd, pgdog, haproxy, patroni).
Use -i for local instance addons, --pg-name for remote addons,
--name for an etcd member, pgdog proxy, haproxy instance, or Patroni member
(default "etcd"/"pgdog"/"haproxy"; Patroni members have no default and also
require --scope).

Examples:
  pg logs addon pgbouncer -i myinst
  pg logs addon pgbouncer -i myinst -f
  pg logs addon pgbouncer --pg-name my-pool
  pg logs addon pgbouncer --pg-name my-pool -f
  pg logs addon pgbouncer --pg-name my-pool -n 200
  pg logs addon etcd --name m1
  pg logs addon etcd --name m1 -f
  pg logs addon etcd --name m2 -n 200
  pg logs addon pgdog --name proxy
  pg logs addon pgdog --name proxy -f
  pg logs addon haproxy --name lb
  pg logs addon haproxy --name lb -f
  pg logs addon patroni --scope app --name node1
  pg logs addon patroni --scope app --name node1 -f`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		follow, _ := cmd.Flags().GetBool("follow")
		tail, _ := cmd.Flags().GetInt("tail")
		pgName, _ := cmd.Flags().GetString("pg-name")
		etcdName, _ := cmd.Flags().GetString("name")
		scope, _ := cmd.Flags().GetString("scope")

		addonType := args[0]

		// loadConfigForDSN, not loadConfig: `pg logs addon ...` only needs the
		// addon section of the config, and requiring a valid *instance* here
		// would make etcd/pgdog/patroni logs unusable on a config that has
		// addons but no PG instance yet. pgbouncer's local branch below reads
		// cfgInstance (the raw -i flag) rather than cfg.Instance, which only
		// SetInstance populates.
		if err := loadConfigForDSN(); err != nil {
			return err
		}
		if scope != "" && addonType != "patroni" {
			return fmt.Errorf("--scope only applies to the patroni addon type")
		}

		var containerName string

		switch addonType {
		case "patroni":
			if pgName != "" {
				return fmt.Errorf("--pg-name selects a remote PgBouncer; use --scope/--name for a Patroni member")
			}
			if scope == "" {
				return fmt.Errorf("--scope is required for Patroni members, e.g. pg logs addon patroni --scope app --name node1")
			}
			if cfg.Addons.Patroni == nil {
				return fmt.Errorf("no Patroni HA clusters configured")
			}
			cluster, ok := cfg.Addons.Patroni[scope]
			if !ok {
				return fmt.Errorf("Patroni cluster %q not found (use 'pg ha status' to list scopes)", scope)
			}
			if etcdName == "" {
				return fmt.Errorf("--name is required: the Patroni member, e.g. pg logs addon patroni --scope %s --name node1", scope)
			}
			mb, ok := cluster.Members[etcdName]
			if !ok {
				return fmt.Errorf("Patroni member %q not found in cluster %q (this host's view: %s)", etcdName, scope, patroniMemberNames(cluster))
			}
			containerName = mb.ContainerName
		case "etcd", "pgdog", "haproxy":
			// (patroni is handled above; it shares --name but requires --scope.)
			if pgName != "" {
				return fmt.Errorf("--pg-name selects a remote PgBouncer; use --name for an %s", addonType)
			}
			deflt := addonType
			name := etcdName
			if name == "" {
				name = deflt
			}
			switch addonType {
			case "etcd":
				if cfg.Addons.Etcd == nil {
					return fmt.Errorf("no etcd addons configured")
				}
				ec, ok := cfg.Addons.Etcd[name]
				if !ok {
					return fmt.Errorf("etcd member %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = ec.ContainerName
			case "pgdog":
				if cfg.Addons.PgDog == nil {
					return fmt.Errorf("no pgdog addons configured")
				}
				pd, ok := cfg.Addons.PgDog[name]
				if !ok {
					return fmt.Errorf("pgdog proxy %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = pd.ContainerName
			default: // haproxy
				if cfg.Addons.HAProxy == nil {
					return fmt.Errorf("no haproxy addons configured")
				}
				hc, ok := cfg.Addons.HAProxy[name]
				if !ok {
					return fmt.Errorf("haproxy instance %q not found (use 'pg addon list' to see available)", name)
				}
				containerName = hc.ContainerName
			}
		case "pgbouncer":
			if etcdName != "" {
				return fmt.Errorf("--name selects an etcd member, pgdog proxy, haproxy instance, or Patroni member; use -i or --pg-name for PgBouncer")
			}
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
				// Local mode: -i. Uses cfgInstance (the raw flag) since this
				// path loads config without SetInstance (see comment above).
				inst, ok := cfg.Instances[cfgInstance]
				if !ok {
					return fmt.Errorf("instance %q not found", cfgInstance)
				}
				if inst.Addons.PgBouncer == nil {
					return fmt.Errorf("no PgBouncer addon configured for instance %q", cfgInstance)
				}
				containerName = inst.Addons.PgBouncer.ContainerName
			}
		default:
			return fmt.Errorf("unknown addon: %s (available: pgbouncer, etcd, pgdog, haproxy, patroni)", addonType)
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
	logsAddonCmd.Flags().String("pg-name", "", "Remote addon pooler name (for remote PgBouncer)")
	logsAddonCmd.Flags().String("name", "", "etcd member name, pgdog proxy name, haproxy instance name, or Patroni member name")
	logsAddonCmd.Flags().String("scope", "", "Patroni cluster scope (required only with addon type patroni)")

	rootCmd.AddCommand(logsCmd)
	logsCmd.AddCommand(logsAddonCmd)
}

// ---------------------------------------------------------------------------
// log output helpers
// ---------------------------------------------------------------------------

// patroniMemberNames lists a cluster's members (this host's config view), for
// a "member not found" error message.
func patroniMemberNames(cluster config.PatroniClusterConfig) string {
	names := make([]string, 0, len(cluster.Members))
	for name := range cluster.Members {
		names = append(names, name)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

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
