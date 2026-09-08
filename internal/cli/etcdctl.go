package cli

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/podman"
)

func init() {
	rootCmd.AddCommand(etcdctlCmd)
}

var etcdctlCmd = &cobra.Command{
	Use:   "etcdctl <subcommand> [args...] [-- <etcdctl-flags>]",
	Short: "Run etcdctl against an etcd addon via a temporary container",
	Long: `etcdctl runs etcd's command-line client without requiring you to exec into a
member container. pgcli launches a short-lived container from the etcd image on
the host network and passes your arguments straight to etcdctl — so it works
even when no etcd member container exists locally (the image is pulled on
demand).

The target cluster is chosen from ETCDCTL_ENDPOINTS (etcdctl's native env var).
When it is unset, pgcli falls back to the first etcd member's client URL.

Any etcdctl flag that pg's own flag parser would otherwise reject (for example
-w table or --hex) goes after -- .

Examples:
  pg etcdctl member list
  pg etcdctl endpoint health
  pg etcdctl put foo bar
  pg etcdctl get foo
  pg etcdctl endpoint status -- -w table
  pg etcdctl get foo -- --hex
  ETCDCTL_ENDPOINTS=http://127.0.0.1:2381 pg etcdctl member list`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := loadConfigForDSN(); err != nil {
			return err
		}
		em, err := podman.NewEtcdManager(cfg)
		if err != nil {
			return fmt.Errorf("etcd manager: %w", err)
		}

		imageTag, defaultEndpoint := etcdctlTargets(cfg)
		// Endpoint source: ETCDCTL_ENDPOINTS (etcdctl-native) or the config's
		// first member. If neither is present there is nowhere to talk to.
		if defaultEndpoint == "" && os.Getenv("ETCDCTL_ENDPOINTS") == "" {
			return fmt.Errorf("no etcd addon configured and ETCDCTL_ENDPOINTS is unset — install one with `pg addon install etcd --name <m>`, or export ETCDCTL_ENDPOINTS=http://127.0.0.1:<client-port>")
		}

		// Args after `--` (e.g. `-w table`, `--hex`) reach us stripped of the
		// `--` separator itself and in original order, so `args` is already
		// exactly what etcdctl should receive.
		return em.Etcdctl(imageTag, defaultEndpoint, args)
	},
}

// etcdctlTargets picks the image tag and default client endpoint for the
// etcdctl helper: the first etcd member (by name) in the config. The default
// etcd image is used when no member is configured.
func etcdctlTargets(cfg *config.Config) (imageTag, defaultEndpoint string) {
	const defaultImage = "quay.io/coreos/etcd:v3.5.30"
	if len(cfg.Addons.Etcd) == 0 {
		return defaultImage, ""
	}
	names := make([]string, 0, len(cfg.Addons.Etcd))
	for name := range cfg.Addons.Etcd {
		names = append(names, name)
	}
	sort.Strings(names)
	ec := cfg.Addons.Etcd[names[0]]
	imageTag = ec.ImageTag
	if imageTag == "" {
		imageTag = defaultImage
	}
	if ec.ClientPort != 0 {
		defaultEndpoint = fmt.Sprintf("http://127.0.0.1:%d", ec.ClientPort)
	}
	return imageTag, defaultEndpoint
}
