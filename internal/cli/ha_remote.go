package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

// --- remote -----------------------------------------------------------
//
// Cross-host Patroni members join the cluster with `pg ha create` ON THEIR OWN
// host, so this host's pg.yaml never learns about them. Backup (pgBackRest over
// SSH) still needs to reach those members. `pg ha remote` records a remote
// member here without creating anything: patronictl (via the shared DCS)
// discovers the member's host and PG port, pgcli stores it as a RemoteHost
// declaration, and the backup config generators fold it into pgbackrest.conf
// and the SSH config.

var haRemoteCmd = &cobra.Command{
	Use:   "remote <scope>",
	Short: "Register a Patroni member that lives on another host",
	Long: `Record a remote cluster member (typically the leader, which lives on
another host and is unreachable via 127.0.0.1) so the backup container can
reach it over SSH. Nothing is created on this host: the member keeps running
where it was joined from.

By default the leader is discovered live through patronictl (the shared DCS).
Use --host/--pg-port to register a specific member or to work while the DCS is
unreachable. The member's SSH port on the remote host defaults to 22; pass
--ssh-port when pgcli allocated another one there.

After registering, refresh the backup config (pg backup setup) so the new
stanza and SSH host entry take effect.

Examples:
  pg ha remote app
  pg ha remote app --member node3 --ssh-port 42301
  pg ha remote app --member node3 --host 10.0.0.12 --pg-port 5432
  pg ha remote remove app --member node3`,
	Args:      cobra.ExactArgs(1),
	ValidArgs: nil,
	RunE:      runHARemoteAdd,
}

var haRemoteRemoveCmd = &cobra.Command{
	Use:   "remove <scope>",
	Short: "Drop a remote member registration from this host's config",
	Long: `Remove a member registered with 'pg ha remote'. This only edits this
host's config — the remote container itself is untouched.`,
	Args:      cobra.ExactArgs(1),
	ValidArgs: nil,
	RunE:      runHARemoteRemove,
}

func remoteConfigPath() (string, error) {
	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", fmt.Errorf("config file not found: %s -- run \"pg config init\" first", path)
	}
	return path, nil
}

func runHARemoteAdd(cmd *cobra.Command, args []string) error {
	scope := args[0]
	member, _ := cmd.Flags().GetString("member")
	host, _ := cmd.Flags().GetString("host")
	pgPort, _ := cmd.Flags().GetInt("pg-port")
	sshPort, _ := cmd.Flags().GetInt("ssh-port")
	restPort, _ := cmd.Flags().GetInt("restapi-port")

	path, err := remoteConfigPath()
	if err != nil {
		return err
	}
	c, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	cfgPath = path
	cfg = c

	cluster, ok := c.Addons.Patroni[scope]
	if !ok {
		return fmt.Errorf("scope %q not found in this host's config (a DCS wiring is needed to discover the cluster)", scope)
	}
	if cluster.Members == nil {
		cluster.Members = map[string]config.PatroniMemberConfig{}
	}

	// Discovery fills in whatever the flags did not pin. Explicit flags
	// always win; discovery is skipped for fully-specified registrations.
	if host == "" || pgPort == 0 || member == "" {
		pm, err := podman.NewPatroniManager(c)
		if err != nil {
			return err
		}
		dName, dHost, dPort, err := pm.DiscoverLeader(&cluster)
		if err != nil {
			if host == "" || pgPort == 0 {
				return fmt.Errorf("leader discovery failed and --host/--pg-port were not given: %w", err)
			}
		} else if host == "" {
			host = dHost
			pgPort = dPort
			if member == "" {
				member = dName
			}
			fmt.Printf("-> discovered leader %q at %s:%d\n", dName, dHost, dPort)
		}
	}
	if member == "" {
		return fmt.Errorf("--member is required when the leader cannot be discovered")
	}
	if host == "" {
		return fmt.Errorf("--host is required when the leader cannot be discovered")
	}
	if pgPort == 0 {
		return fmt.Errorf("--pg-port is required when the leader cannot be discovered")
	}
	if sshPort == 0 {
		sshPort = 22
	}
	if mb := cluster.Members[member]; mb.RemoteHost == "" && mb.ContainerName != "" {
		return fmt.Errorf("member %q is local to this host (container %s) — not a remote peer", member, mb.ContainerName)
	}
	if err := checkHAName("member", member); err != nil {
		return err
	}

	mb := cluster.Members[member]
	mb.RemoteHost = host
	mb.HostPort = pgPort
	mb.SSHPort = sshPort
	mb.RestapiPort = restPort
	mb.Autostart = false
	cluster.Members[member] = mb
	c.Addons.Patroni[scope] = cluster

	if err := c.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("✓ remote member %q registered in scope %q\n", member, scope)
	fmt.Printf("  pg:      %s:%d\n", host, pgPort)
	fmt.Printf("  ssh:     %s:%d (postgres)\n", host, sshPort)
	if restPort != 0 {
		fmt.Printf("  restapi: %s:%d\n", host, restPort)
	}
	fmt.Println()
	fmt.Println("Next: authorize this host's backup key on the member, then refresh the backup config:")
	fmt.Printf("  scp %s postgres@%s:/tmp/backup_id_rsa.pub\n", remotePubKeyPath(c), host)
	fmt.Printf("  ssh postgres@%s -p %d 'mkdir -p /tmp/pgcli-sshd/authorized_keys && cp /tmp/backup_id_rsa.pub /tmp/pgcli-sshd/authorized_keys/postgres && chmod 600 /tmp/pgcli-sshd/authorized_keys/postgres'\n", host, sshPort)
	fmt.Printf("  pg backup setup\n")
	return nil
}

func runHARemoteRemove(cmd *cobra.Command, args []string) error {
	scope := args[0]
	member, _ := cmd.Flags().GetString("member")
	if member == "" {
		return fmt.Errorf("--member is required")
	}

	path, err := remoteConfigPath()
	if err != nil {
		return err
	}
	c, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	cfgPath = path
	cfg = c

	cluster, ok := c.Addons.Patroni[scope]
	if !ok {
		return fmt.Errorf("scope %q not found", scope)
	}
	mb, ok := cluster.Members[member]
	if !ok {
		return fmt.Errorf("member %q is not part of scope %q", member, scope)
	}
	if mb.RemoteHost == "" {
		return fmt.Errorf("member %q is local to this host — use 'pg ha remove %s --member %s'", member, scope, member)
	}
	delete(cluster.Members, member)
	if len(cluster.Members) == 0 {
		delete(c.Addons.Patroni, scope)
	} else {
		c.Addons.Patroni[scope] = cluster
	}
	if err := c.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ removed remote member %q from scope %q (remote container on %s untouched)\n", member, scope, mb.RemoteHost)
	return nil
}

// remotePubKeyPath returns the host-side path of the backup SSH public key.
func remotePubKeyPath(c *config.Config) string {
	bm, err := podman.NewBackupManager(c)
	if err != nil {
		return "<backup ssh public key>"
	}
	return bm.SSHKeyPaths().Public
}

func init() {
	haRemoteCmd.Flags().String("member", "", "member name (default: the current leader, discovered via patronictl)")
	haRemoteCmd.Flags().String("host", "", "remote host IP/FQDN (default: discovered from the DCS)")
	haRemoteCmd.Flags().Int("pg-port", 0, "remote PostgreSQL port (default: discovered from the DCS)")
	haRemoteCmd.Flags().Int("ssh-port", 0, "remote SSH port (default 22)")
	haRemoteCmd.Flags().Int("restapi-port", 0, "remote Patroni REST API port (optional)")
	haRemoteRemoveCmd.Flags().String("member", "", "member name (required)")
	haRemoteCmd.AddCommand(haRemoteRemoveCmd)
	haCmd.AddCommand(haRemoteCmd)
}
