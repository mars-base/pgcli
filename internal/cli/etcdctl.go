package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/podman"
)

func init() {
	etcdctlCmd.Flags().String("ca", "", "CA bundle on the host used to verify the etcd endpoints; mounted into the etcdctl container (overrides the member's CA)")
	etcdctlCmd.Flags().String("cert", "", "client certificate (PEM) on the host for endpoints that require client-certificate auth; mounted into the etcdctl container")
	etcdctlCmd.Flags().String("key", "", "client private key (PEM) on the host, paired with --cert")
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

Endpoints that require client-certificate authentication (K3s embedded etcd,
for example) take --ca together with --cert and --key; the files are mounted
into the container and handed to etcdctl through its native env vars.

Any etcdctl flag that pg's own flag parser would otherwise reject (for example
-w table or --hex) goes after -- .

Examples:
  pg etcdctl member list
  pg etcdctl endpoint health
  pg etcdctl put foo bar
  pg etcdctl get foo
  pg etcdctl endpoint status -- -w table
  pg etcdctl get foo -- --hex
  ETCDCTL_ENDPOINTS=http://127.0.0.1:2381 pg etcdctl member list
  ETCDCTL_ENDPOINTS=https://10.0.0.9:2379 pg etcdctl --ca /path/to/ca.crt member list
  ETCDCTL_ENDPOINTS=https://127.0.0.1:2379 pg etcdctl --ca ca.crt --cert client.crt --key client.key member list`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := loadConfigForDSN(); err != nil {
			return err
		}
		em, err := podman.NewEtcdManager(cfg)
		if err != nil {
			return fmt.Errorf("etcd manager: %w", err)
		}

		imageTag, defaultEndpoint, ec := etcdctlTargets(cfg)
		// Endpoint source: ETCDCTL_ENDPOINTS (etcdctl-native) or the config's
		// first member. If neither is present there is nowhere to talk to.
		if defaultEndpoint == "" && os.Getenv("ETCDCTL_ENDPOINTS") == "" {
			return fmt.Errorf("no etcd addon configured and ETCDCTL_ENDPOINTS is unset — install one with `pg addon install etcd --name <m>`, or export ETCDCTL_ENDPOINTS=http://127.0.0.1:<client-port>")
		}
		var opts podman.EtcdctlOpts
		if ec != nil {
			if ec.TLS {
				ca, err := em.ClientCAFile(ec)
				if err != nil {
					return err
				}
				opts.CAFile = ca
			}
			if ec.Auth {
				opts.Username = ec.AuthUser
				opts.Password = ec.AuthPassword
			}
		}
		if caFlag, _ := cmd.Flags().GetString("ca"); caFlag != "" {
			caPath, err := filepath.Abs(caFlag)
			if err != nil {
				return fmt.Errorf("--ca: %w", err)
			}
			if _, err := os.Stat(caPath); err != nil {
				return fmt.Errorf("--ca: %w", err)
			}
			opts.CAFile = caPath
		}

		certFlag, _ := cmd.Flags().GetString("cert")
		keyFlag, _ := cmd.Flags().GetString("key")
		if (certFlag == "") != (keyFlag == "") {
			return fmt.Errorf("--cert and --key must be given together")
		}
		if certFlag != "" {
			certPath, err := filepath.Abs(certFlag)
			if err != nil {
				return fmt.Errorf("--cert: %w", err)
			}
			if _, err := os.Stat(certPath); err != nil {
				return fmt.Errorf("--cert: %w", err)
			}
			keyPath, err := filepath.Abs(keyFlag)
			if err != nil {
				return fmt.Errorf("--key: %w", err)
			}
			if _, err := os.Stat(keyPath); err != nil {
				return fmt.Errorf("--key: %w", err)
			}
			opts.CertFile = certPath
			opts.KeyFile = keyPath
		}

		// Args after `--` (e.g. `-w table`, `--hex`) reach us stripped of the
		// `--` separator itself and in original order, so `args` is already
		// exactly what etcdctl should receive.
		return em.Etcdctl(imageTag, defaultEndpoint, opts, args)
	},
}

// etcdctlTargets picks the image tag, default client endpoint and member
// config for the etcdctl helper: the first etcd member (by name) in the
// config. The default etcd image is used when no member is configured; the
// member config is nil then.
func etcdctlTargets(cfg *config.Config) (imageTag, defaultEndpoint string, ec *config.EtcdConfig) {
	const defaultImage = "quay.io/coreos/etcd:v3.5.30"
	if len(cfg.Addons.Etcd) == 0 {
		return defaultImage, "", nil
	}
	names := make([]string, 0, len(cfg.Addons.Etcd))
	for name := range cfg.Addons.Etcd {
		names = append(names, name)
	}
	sort.Strings(names)
	member := cfg.Addons.Etcd[names[0]]
	imageTag = member.ImageTag
	if imageTag == "" {
		imageTag = defaultImage
	}
	if member.ClientPort != 0 {
		defaultEndpoint = fmt.Sprintf("%s://127.0.0.1:%d", member.ClientScheme(), member.ClientPort)
	}
	return imageTag, defaultEndpoint, &member
}
