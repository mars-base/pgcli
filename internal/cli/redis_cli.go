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

The target addon is --name when given, otherwise the first redis addon by name.
Its port and requirepass password are read from the config, so you never type a
password. REDISCLI_AUTH (redis-cli's native env var), if set, overrides the
config password.

pgcli's own flags are only recognised before the command word; everything after
it is forwarded to redis-cli verbatim — negative indexes (lrange k 0 -1) and
redis-cli's own -h/-p work without -- (though ` + "`pg redis-cli info -- -h 10.0.0.7`" + `
still reads fine).

Examples:
  pg redis-cli ping
  pg redis-cli set session:42 '{"user":1}'
  pg redis-cli get session:42
  pg redis-cli lrange queue 0 -1
  pg redis-cli --name cache keys 'user:*'
  pg redis-cli info -- -h 10.0.0.7 -p 6380
  REDISCLI_AUTH=otherpass pg redis-cli dbsize`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := loadConfigForDSN(); err != nil {
			return err
		}
		name, _ := cmd.Flags().GetString("name")
		rc, err := resolveRedisTarget(cfg, name)
		if err != nil {
			return err
		}
		rm, err := podman.NewRedisManager(cfg)
		if err != nil {
			return fmt.Errorf("redis manager: %w", err)
		}
		// With several addons installed and no --name, say which one answered —
		// on stderr, so piping redis-cli output stays clean. A single addon or an
		// explicit --name is unambiguous and stays silent, like `pg etcdctl`.
		if name == "" && len(cfg.Addons.Redis) > 1 {
			fmt.Fprintf(os.Stderr, "-> redis-cli → %s (%s:%d) — pick another with --name\n", rc.Name, rc.Listen, rc.Port)
		}
		return rm.RedisCLI(rc.ImageTag, rc.Password, rc.Port, args)
	},
}

// resolveRedisTarget picks the addon redis-cli talks to: the named one, or the
// first by sorted name when --name is omitted. The returned config has
// ImageTag/Port/Password resolved (ApplyDefaults has already run during load).
func resolveRedisTarget(cfg *config.Config, name string) (*config.RedisConfig, error) {
	if len(cfg.Addons.Redis) == 0 {
		return nil, fmt.Errorf("no redis add-ons configured — install one with `pg addon install redis`")
	}
	if name != "" {
		rc, ok := cfg.Addons.Redis[name]
		if !ok {
			return nil, fmt.Errorf("redis %q not found (available: %v)", name, sortedAddonNames(cfg.Addons.Redis))
		}
		return &rc, nil
	}
	names := sortedAddonNames(cfg.Addons.Redis)
	rc := cfg.Addons.Redis[names[0]]
	return &rc, nil
}
