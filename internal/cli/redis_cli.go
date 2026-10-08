package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/podman"
)

func init() {
	rootCmd.AddCommand(redisCLICmd)
	redisCLICmd.Flags().String("name", "", "redis addon to target (default: the first by name)")
	redisCLICmd.Flags().String("host", "", "connect to this host instead of the local addon (remote endpoint)")
	redisCLICmd.Flags().Int("port", 0, "port for --host (default: the addon's port, else 6379)")
	// pgcli's flags are only recognised BEFORE the redis command word, so
	// redis arguments like lrange's negative index (-1) pass through untouched
	// instead of being parsed as pgcli shorthand flags.
	redisCLICmd.Flags().SetInterspersed(false)
}

var redisCLICmd = &cobra.Command{
	Use:   "redis-cli <command> [args...] [-- <redis-cli-flags>]",
	Short: "Run redis-cli against a Redis addon via a temporary container",
	Long: `redis-cli runs Redis's command-line client without requiring you to exec into
a container. pgcli launches a short-lived container from the addon's Redis image
and passes your arguments straight to redis-cli — so it works even when the
container itself is stopped (the image is pulled on demand).

By default the target addon is --name when given, otherwise the first redis
addon by name, reached on the local host. Its port and requirepass password are
read from the config, so you never type a password. REDISCLI_AUTH (redis-cli's
native env var), if set, overrides the config password.

--host/--port point at any remote endpoint instead of the local one. With a
local addon configured, its image and password are reused (so a replica of the
same shape just works); with none configured, the default major's image is used
and you must supply REDISCLI_AUTH (or pass -a after the command word).

pgcli's own flags are only recognised before the command word; everything after
it is forwarded to redis-cli verbatim — negative indexes (lrange k 0 -1) and
redis-cli's own option flags (via --) work that way. To retarget the connection,
use --host/--port, not redis-cli's -h/-p (those land after the command word and
become command arguments).

For a native-cluster node (installed with --cluster) the client runs with -c, so
key MOVED redirects between masters are followed automatically. To assemble a
cluster or run redis-cli's cluster admin subcommands, forward them after --:
pgcli injects the (shared) cluster password via REDISCLI_AUTH and redis-cli's
--cluster does its own dialling, e.g.
  pg redis-cli --name n1 -- --cluster create n1host:p1 n2host:p2 n3host:p3 --cluster-replicas 0

Examples:
  pg redis-cli ping
  pg redis-cli set session:42 '{"user":1}'
  pg redis-cli lrange queue 0 -1
  pg redis-cli --name cache keys 'user:*'
  pg redis-cli --host 10.10.0.158 ping
  pg redis-cli --host 10.10.0.158 --port 6380 info
  REDISCLI_AUTH=otherpass pg redis-cli --host 10.10.0.158 dbsize
  pg redis-cli info -- --no-auth-warning
  pg redis-cli --name n1 -- --cluster create 127.0.0.1:6379 127.0.0.1:6380 127.0.0.1:6381 --cluster-replicas 0`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := loadConfigForDSN(); err != nil {
			return err
		}
		name, _ := cmd.Flags().GetString("name")
		host, _ := cmd.Flags().GetString("host")
		port, _ := cmd.Flags().GetInt("port")

		target, err := resolveRedisCLITarget(cfg, name, host, port)
		if err != nil {
			return err
		}
		rm, err := podman.NewRedisManager(cfg)
		if err != nil {
			return fmt.Errorf("redis manager: %w", err)
		}
		// With several addons installed, no --name, and no explicit --host, say
		// which one answered — on stderr, so piping redis-cli output stays
		// clean. An explicit --name or --host is unambiguous and stays silent.
		if name == "" && host == "" && len(cfg.Addons.Redis) > 1 {
			fmt.Fprintf(os.Stderr, "-> redis-cli → %s (%s:%d) — pick another with --name, or a remote with --host\n",
				target.addonName, target.host, target.port)
		}
		return rm.RedisCLI(target.imageTag, target.password, target.host, target.port, target.cluster, args)
	},
}

// redisCLITarget is the resolved connection for one `pg redis-cli` invocation.
type redisCLITarget struct {
	addonName string // empty for a pure-remote target (no local addon used)
	imageTag  string
	password  string // may be empty; REDISCLI_AUTH in the environment wins
	host      string // empty = manager default (local loopback / bridge)
	port      int
	cluster   bool // a native-cluster node: RedisCLI injects -c so MOVED follows itself
}

// resolveRedisCLITarget turns the (name, --host, --port) flag trio plus the
// config into a concrete target. Rules:
//
//   - no --host: the addon is required (the first by name, or --name), and the
//     local host is used (manager decides loopback vs bridge), with its stored
//     port; --port overrides the port.
//   - --host set: it overrides the connection host. If an addon is in play
//     (--name, or the first one when several exist), its image and password are
//     reused; the port defaults to the addon's when --port is omitted, else
//     6379. With no local addon at all, the default major's image is used and
//     auth must come from REDISCLI_AUTH (or a forwarded -a).
//   - an explicit --name that does not exist is always an error, with or
//     without --host.
func resolveRedisCLITarget(cfg *config.Config, name, host string, port int) (redisCLITarget, error) {
	haveAddons := len(cfg.Addons.Redis) > 0

	// An explicit --name must resolve, whether or not --host is given.
	var rc config.RedisConfig
	if name != "" {
		if !haveAddons {
			return redisCLITarget{}, fmt.Errorf("no redis add-ons configured — install one with `pg addon install redis`, or reach a remote with --host")
		}
		found, ok := cfg.Addons.Redis[name]
		if !ok {
			return redisCLITarget{}, fmt.Errorf("redis %q not found (available: %v)", name, sortedAddonNames(cfg.Addons.Redis))
		}
		rc = found
	}

	switch {
	case host == "":
		// Local-only path: an addon is required.
		if !haveAddons {
			return redisCLITarget{}, fmt.Errorf("no redis add-ons configured — install one with `pg addon install redis`, or reach a remote with --host")
		}
		if name == "" {
			first := sortedAddonNames(cfg.Addons.Redis)[0]
			rc = cfg.Addons.Redis[first]
		}
		p := port
		if p == 0 {
			p = rc.Port
		}
		return redisCLITarget{
			addonName: rc.Name,
			imageTag:  rc.ImageTag,
			password:  rc.Password,
			port:      p,
			cluster:   rc.ClusterEnabled(),
		}, nil

	case name != "" || haveAddons:
		// --host with an addon in play: reuse image/password, override the host.
		if name == "" {
			first := sortedAddonNames(cfg.Addons.Redis)[0]
			rc = cfg.Addons.Redis[first]
		}
		p := port
		if p == 0 {
			p = rc.Port
			if p == 0 {
				p = config.DefaultRedisPort
			}
		}
		return redisCLITarget{
			addonName: rc.Name,
			imageTag:  rc.ImageTag,
			password:  rc.Password,
			host:      host,
			port:      p,
			cluster:   rc.ClusterEnabled(),
		}, nil

	default:
		// --host with no local addon: pure remote. Default major's image; the
		// password comes only from REDISCLI_AUTH / a forwarded -a.
		p := port
		if p == 0 {
			p = config.DefaultRedisPort
		}
		tag, _ := config.RedisImageTagForMajor(config.DefaultRedisMajor)
		return redisCLITarget{
			imageTag: tag,
			host:     host,
			port:     p,
		}, nil
	}
}
