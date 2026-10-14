package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/podman"
)

func init() {
	rootCmd.AddCommand(weedCmd)
	weedCmd.Flags().String("name", "", "seaweedfs instance to run against (default: the first by name)")
	weedCmd.Flags().Bool("shell", false, "pipe one or more lines into `weed shell` instead of running argv verbatim (day-2 menu: cluster.status, volume.list, s3.bucket.list, fs.ls, ec.encode, ...)")
	// pgcli's flags are only recognised BEFORE the weed subcommand word, so
	// weed's own flags pass through untouched (same contract as pg garage).
	weedCmd.Flags().SetInterspersed(false)
}

var weedCmd = &cobra.Command{
	Use:   "weed <subcommand> [args...] | --shell <line>...",
	Short: "Run SeaweedFS's own weed CLI inside an instance's container",
	Long: `weed drives SeaweedFS's control tool without you exec'ing into the
container: pgcli runs the arguments verbatim as ` + "`/usr/bin/weed ...`" + ` inside the
target instance's running container and prints what it returns. This is the
day-2 operations entry point — inspect volumes, run shell commands.

The target is --name when given, otherwise the first seaweedfs instance by name.
Unlike ` + "`pg mc`" + ` (a temporary client container), ` + "`pg weed`" + ` needs a RUNNING
member: most weed subcommands handshake with the master, so the container is
the natural place to run them.

Two shapes:
  pg weed <args>...           → ` + "`weed <args>`" + ` verbatim in the container
  pg weed --shell <line>...   → each line piped into ` + "`weed shell`" + `, pre-wired to
                                this instance's master and filer (no flags to
                                remember; loopback so it works on macOS too)

` + "`--shell`" + ` is the convenience form: it runs the lines non-interactively and
prints the result. ` + "`weed shell`" + ` without --shell is interactive and needs a TTY,
so pipe it yourself if you want that.

weed CLIs can print their result and then linger (they keep gRPC channels
open); pgcli cuts such a command off once its output has been quiet for 3s, so
a hang surfaces as a loud timeout instead — re-run the same command.

Examples:
  pg weed --shell "cluster.status"
  pg weed --name store --shell "volume.list" "s3.bucket.list"
  pg weed --shell "fs.ls /"
  pg weed flag.lookup 0,1,2          # verbatim weed subcommand
  pg weed shell                      # interactive (needs a TTY)`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadAddonCfg()
		if err != nil {
			return err
		}
		name, _ := cmd.Flags().GetString("name")
		useShell, _ := cmd.Flags().GetBool("shell")
		sc, err := resolveSeaweedfsTarget(c, name)
		if err != nil {
			return err
		}
		sm, err := podman.NewSeaweedfsManager(c)
		if err != nil {
			return fmt.Errorf("seaweedfs manager: %w", err)
		}
		if useShell {
			return runWeedShell(sm, sc, args)
		}
		out, execErr := sm.ExecCaptureTimeout(sc, weedCLIPassthroughTimeout, args...)
		// weed tables go to stdout, its log lines to stderr; ExecCapture
		// merges both, so print whatever arrived before deciding on failure.
		fmt.Print(out)
		if out != "" && !strings.HasSuffix(out, "\n") {
			fmt.Println()
		}
		return execErr
	},
}

// weedCLIPassthroughTimeout bounds a user-invoked `pg weed` control command.
// Generous: ops like `ec.encode` legitimately run for minutes; the 3s
// idle-kill still resolves the print-then-linger fast.
const weedCLIPassthroughTimeout = 10 * time.Minute

// runWeedShell feeds each argument as one shell line, joined with newlines, to
// a non-interactive `weed shell` wired to THIS instance's master and filer.
func runWeedShell(sm *podman.SeaweedfsManager, sc *config.SeaweedfsConfig, lines []string) error {
	script, err := weedShellScript(sc, lines)
	if err != nil {
		return err
	}
	out, execErr := sm.ExecShellTimeout(sc, weedCLIPassthroughTimeout, script)
	fmt.Print(out)
	if out != "" && !strings.HasSuffix(out, "\n") {
		fmt.Println()
	}
	return execErr
}

// weedShellScript builds the in-container `sh -c` that pipes the given lines
// into `weed shell -master -filer`. Loopback (not the Listen/Advertise address)
// is deliberate: on macOS the container is bridge-networked, and weed dials
// these from inside the container, where 127.0.0.1 is always the master it
// lives next to. Each line becomes its own single-quoted printf argument
// (printf '%s\n' with several args emits one line per arg), so multi-command
// input stays readable and each line is quoted independently. A line carrying a
// single quote is rejected — there is no in-band escape that survives the
// surrounding sh -c — rather than silently mis-quoted.
func weedShellScript(sc *config.SeaweedfsConfig, lines []string) (string, error) {
	quoted := make([]string, 0, len(lines))
	for _, ln := range lines {
		if strings.ContainsRune(ln, '\'') {
			return "", fmt.Errorf("weed shell line contains a single quote and cannot be passed through: %q", ln)
		}
		quoted = append(quoted, "'"+ln+"'")
	}
	master := fmt.Sprintf("127.0.0.1:%d", sc.Port)
	filer := fmt.Sprintf("127.0.0.1:%d", sc.FilerPort)
	script := fmt.Sprintf("printf '%%s\\n' %s | /usr/bin/weed shell -master=%s -filer=%s",
		strings.Join(quoted, " "), master, filer)
	return script, nil
}

// resolveSeaweedfsTarget picks the SeaweedfsConfig for `pg weed`: the named
// instance, or the first by name when --name is absent. With several instances
// configured and no --name, the choice is announced on stderr so piped output
// stays clean (garage/redis_cli pattern).
func resolveSeaweedfsTarget(c *config.Config, name string) (*config.SeaweedfsConfig, error) {
	if len(c.Addons.Seaweedfs) == 0 {
		return nil, fmt.Errorf("no seaweedfs add-ons configured — install one with `pg addon install seaweedfs --name <n>`")
	}
	if name == "" {
		names := sortedAddonNames(c.Addons.Seaweedfs)
		name = names[0]
		if len(names) > 1 {
			sc := c.Addons.Seaweedfs[name]
			fmt.Fprintf(os.Stderr, "-> seaweedfs → %s (%s) — pick another with --name\n", name, sc.ContainerName)
		}
	}
	sc, ok := c.Addons.Seaweedfs[name]
	if !ok {
		return nil, fmt.Errorf("seaweedfs %q not found (available: %v)", name, sortedAddonNames(c.Addons.Seaweedfs))
	}
	return &sc, nil
}
