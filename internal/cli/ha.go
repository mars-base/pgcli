package cli

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

var haCmd = &cobra.Command{
	Use:   "ha",
	Short: "Manage Patroni-based PostgreSQL high-availability clusters",
	Long: `Manage PostgreSQL high availability via Patroni — automatic failover,
switchover, and a DCS-backed cluster (etcd) of PostgreSQL members.

This is a distinct mode from plain ` + "`pg`" + ` instances. Patroni (not pgcli)
owns each member's postmaster: it runs initdb, starts/stops PostgreSQL, manages
replication, and performs failover. pgcli owns the container, image, per-member
patroni.yml, ports, DCS wiring, and the credentials, and drives the cluster
through patronictl in short-lived containers.

Key semantics to internalize before operating:
  - Patroni is PID 1 in its container, so "recreate the container" == "take the
    node fully offline"; if it was the leader, that triggers a failover. That is
    why ` + "`create`" + ` is a re-install (destructive) and ` + "`edit-config`" + ` never
    recreates a container (it only edits the DCS).
  - Starting/stopping the container is NOT a safe way to restart PostgreSQL. For
    planned maintenance, ` + "`pg ha pause`" + ` first (it disables auto-failover).
  - A paused cluster has NO automatic failover until resumed.

Single-host cluster (uses the etcd addon as DCS):
  pg addon install etcd --name m1
  pg ha create app --member node1 --etcd m1
  pg ha create app --member node2 --etcd m1
  pg ha status app

Cross-host members (run one create per host, sharing one DCS and one password
set; see ` + "`pg ha create --help`" + ` and the docs page):
  pg ha create app --member node1 --advertise-host 10.0.0.11 --etcd-endpoints 10.0.0.9:2379 --passwords-file app.yml
  pg ha create app --member node2 --advertise-host 10.0.0.12 --etcd-endpoints 10.0.0.9:2379 --passwords-file app.yml

Commands:
  pg ha create <scope> --member <m> ...   register + (re)install a member
  pg ha status   [scope]                  cluster(s) overview / patronictl list
  pg ha switchover <scope>                planned leader change
  pg ha failover   <scope>                promote a replica now
  pg ha pause|resume <scope>              disable/enable auto-failover
  pg ha edit-config <scope> -- [flags]    view/patch the DCS dynamic config
  pg ha start|stop <scope> --member|--all container start/stop (see semantics)
  pg ha remove <scope> --member|--scope-all  remove member(s) and (optionally) DCS
  pg ha passwords <scope> [--file F]         export the stored password set
                                             (for --passwords-file on other hosts)
  pg ha ctl <scope> -- <patronictl args>  passthrough to any patronictl command

Linux only (rootless podman host networking), like the etcd addon.`,
}

// --- create -----------------------------------------------------------

var haCreateCmd = &cobra.Command{
	Use:   "create <scope>",
	Short: "Register and (re)install a Patroni member",
	Long: `Register a Patroni member in the config, render its patroni.yml, and run
its container. The first member of a scope bootstraps the cluster (initdb, wins
the leader race); every later member automatically pg_basebackups from the
current leader — no flag is needed to say "add" vs "join".

WARNING — re-install semantics: this stops and recreates the member container.
Because Patroni is PID 1, recreating a running member takes that node offline
and, if it is the leader, triggers a failover. Re-running create to change one
member's config is fine, but prefer ` + "`pg ha edit-config`" + ` for dynamic
settings (it never touches the container).

Password set: the first ` + "`create`" + ` for a scope generates superuser,
replication, rewind and REST-API credentials and stores them in pg.yaml. For
cross-host members the ENTIRE set must be identical on every host (Patroni's
REST/rewind/replication auth is cluster-wide), so export the first host's
passwords to a file and pass it with --passwords-file on the others.

Examples:
  pg ha create app --member node1 --etcd m1
  pg ha create app --member node2 --etcd m1
  pg ha create app --member node1 --etcd-endpoints 10.0.0.9:2379,10.0.0.10:2379
  pg ha create app --member node2 --advertise-host 10.0.0.12 --host-port 5432 --etcd-endpoints 10.0.0.9:2379 --passwords-file app.yml
  pg ha passwords app --file app-passwd.yml   # export the set for other hosts`,
	Args:      cobra.ExactArgs(1),
	ValidArgs: nil,
	RunE:      runHACreate,
}

func runHACreate(cmd *cobra.Command, args []string) error {
	scope := args[0]
	member, _ := cmd.Flags().GetString("member")
	etcdList, _ := cmd.Flags().GetStringSlice("etcd")
	etcdEndpoints, _ := cmd.Flags().GetStringSlice("etcd-endpoints")
	advertiseHost, _ := cmd.Flags().GetString("advertise-host")
	hostPort, _ := cmd.Flags().GetInt("host-port")
	restapiPort, _ := cmd.Flags().GetInt("restapi-port")
	passFile, _ := cmd.Flags().GetString("passwords-file")

	if member == "" {
		return fmt.Errorf("--member is required")
	}
	if len(etcdList) == 0 && len(etcdEndpoints) == 0 {
		return fmt.Errorf("a DCS is required: pass --etcd <m1,m2,...> (local etcd addon members) or --etcd-endpoints <host:port,...> (external / cross-host)")
	}

	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s -- run \"pg config init\" first", path)
	}
	c, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	cfgPath = path
	cfg = c

	if c.Addons.Patroni == nil {
		c.Addons.Patroni = map[string]config.PatroniClusterConfig{}
	}
	// Validate names: they become DCS keys, container names and path segments,
	// so they must be [A-Za-z0-9][A-Za-z0-9_-]* (matching the namespace rule).
	if err := checkHAName("scope", scope); err != nil {
		return err
	}
	if err := checkHAName("member", member); err != nil {
		return err
	}
	cluster, existed := c.Addons.Patroni[scope]
	if cluster.Name == "" {
		cluster.Name = scope
	}
	if cluster.Members == nil {
		cluster.Members = map[string]config.PatroniMemberConfig{}
	}

	// DCS wiring.
	if len(etcdList) > 0 {
		cluster.EtcdMembers = etcdList
	}
	if len(etcdEndpoints) > 0 {
		cluster.EtcdEndpoints = etcdEndpoints
	}

	// Passwords: load (cross-host must match) or, for a brand-new scope,
	// generate once and persist. An existing scope keeps its stored set.
	if passFile != "" {
		if err := loadPasswordsFile(&cluster.Passwords, passFile); err != nil {
			return err
		}
	} else if !existed || cluster.Passwords.Superuser == "" {
		if err := fillGeneratedPasswords(&cluster.Passwords); err != nil {
			return fmt.Errorf("generating passwords: %w", err)
		}
	}

	// Register the member (empty fields are filled by ApplyDefaults).
	mb := cluster.Members[member]
	mb.AdvertiseHost = advertiseHost
	if hostPort != 0 {
		mb.HostPort = hostPort
	}
	if restapiPort != 0 {
		mb.RestapiPort = restapiPort
	}
	cluster.Members[member] = mb
	c.Addons.Patroni[scope] = cluster

	c.ApplyDefaults()
	if err := c.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Reflect the fully-defaulted member back for reporting + container boot.
	mb = c.Addons.Patroni[scope].Members[member]
	nsScope := c.PatroniScope(scope)

	pm, err := podman.NewPatroniManager(c)
	if err != nil {
		return err
	}
	if err := pm.EnsurePatroniImage(mb.ImageTag); err != nil {
		return fmt.Errorf("preparing Patroni image: %w", err)
	}
	clusterRef := c.Addons.Patroni[scope]
	if _, err := pm.WriteMemberConfig(&clusterRef, member); err != nil {
		return fmt.Errorf("writing patroni.yml: %w", err)
	}
	if err := pm.EnsureMemberContainer(&clusterRef, member); err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("✓ Patroni member %q installed in scope %q\n", member, scope)
	fmt.Printf("  scope (DCS):  %s\n", nsScope)
	fmt.Printf("  container:    %s\n", mb.ContainerName)
	fmt.Printf("  data dir:     %s\n", mb.DataDir)
	fmt.Printf("  image:        %s\n", mb.ImageTag)
	fmt.Printf("  pg:           %s:%d\n", pgConnectHost(mb), mb.HostPort)
	fmt.Printf("  restapi:      %s:%d\n", pgConnectHost(mb), mb.RestapiPort)
	fmt.Printf("  patroni.yml:  %s\n", patroniYMLPath(c, nsScope, member))
	if passFile != "" {
		fmt.Printf("  passwords:    from %s\n", passFile)
	} else {
		fmt.Printf("  passwords:    stored in %s (patroni.passwords)\n", path)
	}
	if mb.AdvertiseHost == "" {
		fmt.Println("  [note] no --advertise-host: this member is loopback-only (single-host). Set it for cross-host members.")
	}
	fmt.Println()
	fmt.Println("Join the cluster (next member, or another host):")
	fmt.Printf("  pg ha create %s --member <m>%s\n", scope, joinHint(c, scope, mb))
	return nil
}

// joinHint renders the DCS flags a follow-up member should reuse.
func joinHint(c *config.Config, scope string, mb config.PatroniMemberConfig) string {
	cluster := c.Addons.Patroni[scope]
	hint := " --etcd " + strings.Join(cluster.EtcdMembers, ",")
	if len(cluster.EtcdMembers) == 0 {
		hint = " --etcd-endpoints " + strings.Join(cluster.EtcdEndpoints, ",")
	}
	if cluster.Passwords.Superuser != "" {
		hint += " --passwords-file <exported.yml>"
	}
	return hint
}

func patroniYMLPath(c *config.Config, nsScope, member string) string {
	base := c.BaseDir
	if base == "" {
		base = platform.DefaultConfigDir()
	}
	return fmt.Sprintf("%s/addon/patroni/%s/%s/patroni.yml", strings.TrimSuffix(base, "/"), nsScope, member)
}

func fillGeneratedPasswords(p *config.PatroniPasswords) error {
	su, err := generatePassword(16)
	if err != nil {
		return err
	}
	repl, err := generatePassword(16)
	if err != nil {
		return err
	}
	rw, err := generatePassword(16)
	if err != nil {
		return err
	}
	rest, err := generatePassword(16)
	if err != nil {
		return err
	}
	p.Superuser, p.Replication, p.Rewind = su, repl, rw
	if p.RestapiUser == "" {
		p.RestapiUser = "postgres"
	}
	p.RestapiPasswd = rest
	return nil
}

// loadPasswordsFile reads a small YAML file holding just the passwords block
// (superuser/replication/rewind/restapi_user/restapi_password). Cross-host
// members must share one password set, so the first host's file is the source
// of truth for the rest.
func loadPasswordsFile(p *config.PatroniPasswords, file string) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("reading --passwords-file: %w", err)
	}
	if err := yaml.Unmarshal(b, p); err != nil {
		return fmt.Errorf("parsing --passwords-file %s: %w", file, err)
	}
	if p.Superuser == "" || p.RestapiPasswd == "" {
		return fmt.Errorf("--passwords-file %s is missing superuser/restapi_password", file)
	}
	if p.RestapiUser == "" {
		p.RestapiUser = "postgres"
	}
	return nil
}

// --- passwords (export) -------------------------------------------------

var haPasswordsCmd = &cobra.Command{
	Use:   "passwords <scope>",
	Short: "Export a cluster's stored password set for --passwords-file",
	Long: `Print the password set that pg ha create generated and stored in pg.yaml
for the scope, in exactly the YAML format --passwords-file consumes. Use it to
seed cross-host members with the identical set:

  pg ha passwords app --file app-passwd.yml     # written mode 0600
  pg ha create app --member node2 --advertise-host 10.0.0.12 \
    --etcd-endpoints 10.0.0.9:2379 --passwords-file app-passwd.yml

Without --file the YAML goes to stdout; prefer --file so the secrets do not end
up in shell history or terminal scrollback.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := loadConfigForDSN(); err != nil {
			return err
		}
		cluster, err := lookupHACluster(args[0])
		if err != nil {
			return err
		}
		if cluster.Passwords.Superuser == "" {
			return fmt.Errorf("scope %q has no stored password set (no member created yet?)", args[0])
		}
		b, err := yaml.Marshal(cluster.Passwords)
		if err != nil {
			return fmt.Errorf("marshaling passwords: %w", err)
		}
		file, _ := cmd.Flags().GetString("file")
		if file == "" {
			os.Stdout.Write(b)
			return nil
		}
		if err := os.WriteFile(file, b, 0600); err != nil {
			return fmt.Errorf("writing %s: %w", file, err)
		}
		fmt.Printf("[OK] passwords for scope %q written to %s (mode 0600)\n", args[0], file)
		return nil
	},
}

// --- ctl (generic patronictl passthrough) -----------------------------

var haCtlCmd = &cobra.Command{
	Use:   "ctl <scope> -- [patronictl args...]",
	Short: "Run any patronictl command against a cluster (passthrough)",
	Long: `ctl launches patronictl in a short-lived container pointed at the scope's
DCS and forwards your arguments verbatim — the escape hatch for anything pgcli
does not wrap (top, history, members, show-config, restart, reload, ...).

Flags patronictl would parse itself (e.g. -w, --force) go after -- .

Examples:
  pg ha ctl app -- list
  pg ha ctl app -- topology
  pg ha ctl app -- show-config
  pg ha ctl app -- history --limit 5
  pg ha ctl app -- restart`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := loadConfigForDSN(); err != nil {
			return err
		}
		scope := args[0]
		cluster, err := lookupHACluster(scope)
		if err != nil {
			return err
		}
		pm, err := podman.NewPatroniManager(cfg)
		if err != nil {
			return err
		}
		return pm.Patronictl(cluster, args[1:]...)
	},
}

// --- status -----------------------------------------------------------

var haStatusCmd = &cobra.Command{
	Use:   "status [scope]",
	Short: "Show Patroni clusters, or one cluster's member table",
	Long: `With no scope, lists every Patroni cluster registered locally with its
member/container state. With a scope, runs ` + "`patronictl list`" + ` and
explicitly flags a paused cluster (paused means no automatic failover).

Examples:
  pg ha status
  pg ha status app`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := loadConfigForDSN(); err != nil {
			return err
		}
		pm, err := podman.NewPatroniManager(cfg)
		if err != nil {
			return err
		}
		if len(args) == 0 {
			return haStatusAll(pm)
		}
		cluster, err := lookupHACluster(args[0])
		if err != nil {
			return err
		}
		return pm.Patronictl(cluster, "list")
	},
}

// haStatusAll prints a compact roll-up of every cluster and its local members'
// container state — no patronictl call, so it works even with the DCS down.
func haStatusAll(pm *podman.PatroniManager) error {
	if len(cfg.Addons.Patroni) == 0 {
		fmt.Println("No Patroni clusters configured — create one with `pg ha create <scope> --member <m> --etcd <m1>`")
		return nil
	}
	scopes := make([]string, 0, len(cfg.Addons.Patroni))
	for s := range cfg.Addons.Patroni {
		scopes = append(scopes, s)
	}
	sort.Strings(scopes)
	for _, scope := range scopes {
		cluster := cfg.Addons.Patroni[scope]
		fmt.Printf("%s (scope=%s, members=%d)\n", scope, cfg.PatroniScope(scope), len(cluster.Members))
		members := make([]string, 0, len(cluster.Members))
		for m := range cluster.Members {
			members = append(members, m)
		}
		sort.Strings(members)
		for _, m := range members {
			mb := cluster.Members[m]
			state := "?"
			if running, err := pm.ContainerRunning(mb.ContainerName); err == nil {
				state = "stopped"
				if running {
					state = "running"
				}
			}
			fmt.Printf("  %-14s %-8s pg=%s:%d  rest=%d\n", m, state, pgConnectHost(mb), mb.HostPort, mb.RestapiPort)
		}
	}
	fmt.Println("\nMember detail: pg ha status <scope>")
	return nil
}

// --- remove -----------------------------------------------------------

var haRemoveCmd = &cobra.Command{
	Use:   "remove <scope>",
	Short: "Remove Patroni member(s) and optionally the cluster",
	Long: `Remove a member's container and config, or the whole scope.

  --member <m>     remove one member (its container + patroni.yml). Status may
                   still show peers registered in the DCS; those live on their
                   own hosts and are removed there.
  --scope-all      remove every LOCAL member, then clear the cluster from the
                   DCS (patronictl remove -f). Cross-host members' containers
                   are untouched — remove them on their own hosts.
  --clean-data     also delete the member data dir(s) (default: keep + print the
                   path). Mirrors ` + "`pg destroy --clean-data`" + `.
  --force          skip the interactive confirmation.

Order for --scope-all: stop → rm container → patronictl remove (DCS) → prune
config entry.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		scope := args[0]
		member, _ := cmd.Flags().GetString("member")
		scopeAll, _ := cmd.Flags().GetBool("scope-all")
		cleanData, _ := cmd.Flags().GetBool("clean-data")
		force, _ := cmd.Flags().GetBool("force")
		if member == "" && !scopeAll {
			return fmt.Errorf("choose --member <m> or --scope-all")
		}
		if member != "" && scopeAll {
			return fmt.Errorf("--member and --scope-all are mutually exclusive")
		}

		path := cfgPath
		if path == "" {
			path = platform.DefaultConfigPath()
		}
		c, err := config.Load(path)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		cfgPath, cfg = path, c
		cluster, ok := c.Addons.Patroni[scope]
		if !ok {
			return fmt.Errorf("scope %q is not configured", scope)
		}
		pm, err := podman.NewPatroniManager(c)
		if err != nil {
			return err
		}

		targets := []string{member}
		if scopeAll {
			targets = nil
			for m := range cluster.Members {
				targets = append(targets, m)
			}
			sort.Strings(targets)
		}
		for _, m := range targets {
			if _, ok := cluster.Members[m]; !ok {
				return fmt.Errorf("member %q is not part of scope %q", m, scope)
			}
		}

		if !force {
			label := scope + "/" + member
			if scopeAll {
				label = fmt.Sprintf("all %d local member(s) of scope %q and its DCS entry", len(targets), scope)
			}
			if !confirmPrompt(fmt.Sprintf("Remove %s%s? [y/N] ", label, dataSuffix(cleanData))) {
				fmt.Println("Aborted.")
				return nil
			}
		}

		// Clear the DCS FIRST when the whole scope goes away: patronictl
		// remove discovers the cluster from the DCS but still needs a member
		// config (image tag, endpoints) to launch from, which the container/
		// config-dir cleanup below destroys. Removing a non-existent scope is a
		// clean no-op.
		if scopeAll {
			if err := pm.RemoveScopeFromDCS(&cluster); err != nil {
				fmt.Printf("  [!] patronictl remove (DCS): %v — the scope may still exist in etcd\n", err)
			} else {
				fmt.Println("  [OK] DCS entry removed")
			}
		}

		// Stop + remove local member containers/config.
		for _, m := range targets {
			mb := cluster.Members[m]
			if err := pm.RemoveMemberContainer(&cluster, m); err != nil {
				fmt.Printf("  [!] %s: %v\n", m, err)
			}
			if cleanData {
				if err := os.RemoveAll(mb.DataDir); err != nil && !os.IsNotExist(err) {
					fmt.Printf("  [!] removing data dir %s: %v\n", mb.DataDir, err)
				} else {
					fmt.Printf("  [OK] removed data dir: %s\n", mb.DataDir)
				}
			} else {
				fmt.Printf("  [kept] data dir: %s\n", mb.DataDir)
			}
			delete(cluster.Members, m)
		}

		if scopeAll {
			delete(c.Addons.Patroni, scope)
		} else {
			c.Addons.Patroni[scope] = cluster
		}

		if err := c.Save(path); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		fmt.Printf("✓ removed %d member(s) from scope %q\n", len(targets), scope)
		return nil
	},
}

func dataSuffix(cleanData bool) string {
	if cleanData {
		return " AND WIPE DATA"
	}
	return ""
}

// --- operations (thin patronictl wrappers) ----------------------------

// haPassthrough builds a wrapper command that loads the named scope and runs
// patronictl with argv produced by build(). Args uses ArbitraryArgs so tokens
// after `--` (patronictl's own flags, which cobra strips) reach build()
// untouched; the command enforces its own positional-arg shape in build().
func haPassthrough(use, short, long string, configure func(*cobra.Command)) *cobra.Command {
	c := &cobra.Command{Use: use, Short: short, Long: long, Args: cobra.ArbitraryArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("usage: pg ha %s", use)
		}
		if err := loadConfigForDSN(); err != nil {
			return err
		}
		cluster, err := lookupHACluster(args[0])
		if err != nil {
			return err
		}
		pm, err := podman.NewPatroniManager(cfg)
		if err != nil {
			return err
		}
		ctlArgs, err := patronictlArgsFor(cmd, args)
		if err != nil {
			return err
		}
		return pm.Patronictl(cluster, ctlArgs...)
	}}
	if configure != nil {
		configure(c)
	}
	return c
}

var haSwitchoverCmd = haPassthrough(
	"switchover <scope>",
	"Planned leader change to another member",
	`Switchover is a controlled, zero-downtime leader change. patronictl prompts
for confirmation itself; use --yes to skip it (for scripted / cross-host use).

Examples:
  pg ha switchover app                     # patronictl asks which member to promote
  pg ha switchover app --candidate node2 --primary node1
  pg ha switchover app --yes`,
	func(c *cobra.Command) {
		c.Flags().String("candidate", "", "member to promote to primary")
		c.Flags().String("primary", "", "current primary to demote")
		c.Flags().Bool("yes", false, "skip patronictl's confirmation prompt")
	},
)

var haFailoverCmd = haPassthrough(
	"failover <scope>",
	"Promote a replica to primary now",
	`Force a failover: promote a replica to primary immediately. Use when the
leader is gone or for testing. patronictl confirms on its own (--yes to skip).

Examples:
  pg ha failover app
  pg ha failover app --candidate node2 --yes`,
	func(c *cobra.Command) {
		c.Flags().String("candidate", "", "member to promote")
		c.Flags().Bool("yes", false, "skip patronictl's confirmation prompt")
	},
)

var haPauseCmd = haPassthrough(
	"pause <scope>",
	"Disable automatic failover",
	`Pause puts the cluster in a maintenance mode where Patroni takes NO
automatic action (no failover, no config auto-apply). Do this before planned
work on the leader. Resume to re-enable. --wait blocks until applied on all
members.

Example:
  pg ha pause app
  pg ha pause app --wait`,
	func(c *cobra.Command) {
		c.Flags().Bool("wait", false, "wait until pause is applied on all members")
	},
)

var haResumeCmd = haPassthrough(
	"resume <scope>",
	"Re-enable automatic failover",
	`Resume a paused cluster so Patroni again manages failover and dynamic config.
--wait blocks until the pause is cleared on all members.

Example:
  pg ha resume app
  pg ha resume app --wait`,
	func(c *cobra.Command) {
		c.Flags().Bool("wait", false, "wait until pause is cleared on all members")
	},
)

var haEditConfigCmd = haPassthrough(
	"edit-config <scope> -- [patronictl flags]",
	"View or patch the cluster's dynamic config in the DCS",
	`edit-config changes the DYNAMIC configuration stored in the DCS — it never
recreates a member container. An interactive $EDITOR opens by default; patch
non-interactively with -s/--set (after --), or view with --show.

Flags after -- are patronictl's own and are forwarded verbatim (this command
does NOT alias -s itself, so native -s/--set are unambiguous).

Examples:
  pg ha edit-config app --show
  pg ha edit-config app -- -s synchronous_mode=true
  pg ha edit-config app            # opens $EDITOR`,
	func(c *cobra.Command) {
		c.Flags().Bool("show", false, "show the current dynamic config without editing")
	},
)

// patronictlArgsFor turns a wrapper command's argv (args[0] = scope) and flags
// into patronictl argv. cmd.Flags().Args() carries tokens after `--` verbatim.
func patronictlArgsFor(cmd *cobra.Command, args []string) ([]string, error) {
	scope := args[0]
	rest := args[1:] // native patronictl flags after `--`
	base := []string{cmd.Name()}

	switch cmd.Name() {
	case "switchover":
		if v, _ := cmd.Flags().GetString("primary"); v != "" {
			base = append(base, "--primary", v)
		}
		if v, _ := cmd.Flags().GetString("candidate"); v != "" {
			base = append(base, "--candidate", v)
		}
		if v, _ := cmd.Flags().GetBool("yes"); v {
			base = append(base, "--force")
		}
		base = append(base, scope)
	case "failover":
		if v, _ := cmd.Flags().GetString("candidate"); v != "" {
			base = append(base, "--candidate", v)
		}
		if v, _ := cmd.Flags().GetBool("yes"); v {
			base = append(base, "--force")
		}
		base = append(base, scope)
	case "pause", "resume":
		if v, _ := cmd.Flags().GetBool("wait"); v {
			base = append(base, "--wait")
		}
		base = append(base, scope)
	case "edit-config":
		if v, _ := cmd.Flags().GetBool("show"); v {
			// --show is a patronictl flag, not a subcommand.
			return []string{"show-config", scope}, nil
		}
		base = append(base, scope)
	default:
		base = append(base, scope)
	}
	return append(base, rest...), nil
}

// --- container start / stop -------------------------------------------

var haStartCmd = &cobra.Command{
	Use:   "start <scope>",
	Short: "Start Patroni member containers",
	Long: `Start member containers (plain podman start) — for a container that was
stopped. This is NOT a safe way to restart PostgreSQL: starting a member's
container starts Patroni, which may join, promote or demote according to the
DCS state. For planned work, prefer ` + "`pg ha pause`" + ` first.

Examples:
  pg ha start app --member node1
  pg ha start app --all`,
	Args: cobra.ExactArgs(1),
	RunE: runHAStartStop,
}

var haStopCmd = &cobra.Command{
	Use:   "stop <scope>",
	Short: "Stop Patroni member containers",
	Long: `Stop member containers (plain podman stop). Stopping a RUNNING member —
especially the leader — takes it offline and can trigger an automatic failover.
Pause the cluster first for planned maintenance.

Examples:
  pg ha stop app --member node1
  pg ha stop app --all`,
	Args: cobra.ExactArgs(1),
	RunE: runHAStartStop,
}

func runHAStartStop(cmd *cobra.Command, args []string) error {
	scope := args[0]
	member, _ := cmd.Flags().GetString("member")
	all, _ := cmd.Flags().GetBool("all")
	if member == "" && !all {
		return fmt.Errorf("choose --member <m> or --all")
	}
	if member != "" && all {
		return fmt.Errorf("--member and --all are mutually exclusive")
	}
	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	c, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	cfgPath, cfg = path, c
	cluster, ok := c.Addons.Patroni[scope]
	if !ok {
		return fmt.Errorf("scope %q is not configured", scope)
	}
	pm, err := podman.NewPatroniManager(c)
	if err != nil {
		return err
	}

	targets := []string{member}
	if all {
		targets = nil
		for m := range cluster.Members {
			targets = append(targets, m)
		}
		sort.Strings(targets)
	}

	start := cmd.Name() == "start"
	for _, m := range targets {
		mb, ok := cluster.Members[m]
		if !ok {
			return fmt.Errorf("member %q is not part of scope %q", m, scope)
		}
		if start {
			if err := pm.StartMemberContainer(&cluster, m); err != nil {
				return err
			}
			continue
		}
		if out, err := pm.StopMemberContainer(mb.ContainerName); err != nil {
			return fmt.Errorf("stopping member %s: %w", m, err)
		} else {
			_ = out
			fmt.Printf("  [OK] stopped %s\n", mb.ContainerName)
		}
	}
	return nil
}

// --- helpers ----------------------------------------------------------

// lookupHACluster returns a pointer to the named scope's cluster config so
// managers can read it without copying the member map. It requires the scope to
// exist in cfg.
func lookupHACluster(scope string) (*config.PatroniClusterConfig, error) {
	cluster, ok := cfg.Addons.Patroni[scope]
	if !ok {
		return nil, fmt.Errorf("scope %q is not configured — `pg ha create %s --member <m>` first", scope, scope)
	}
	return &cluster, nil
}

// pgConnectHost is the host a client/patronictl should reach a member on for
// local reporting: the advertised address, or loopback for a local member.
func pgConnectHost(mb config.PatroniMemberConfig) string {
	if mb.AdvertiseHost != "" {
		return mb.AdvertiseHost
	}
	return "127.0.0.1"
}

// haNamePattern mirrors the namespace rule in config.Validate: scope and
// member names become DCS keys, container names and path segments.
var haNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)

func checkHAName(kind, name string) error {
	if !haNamePattern.MatchString(name) {
		return fmt.Errorf("%s %q must match [A-Za-z0-9][A-Za-z0-9_-]{0,31} (no spaces, slashes, or dots)", kind, name)
	}
	return nil
}

func init() {
	haCreateCmd.Flags().String("member", "", "member name (required)")
	haCreateCmd.Flags().StringSlice("etcd", nil, "local etcd addon member names to use as the DCS (e.g. m1,m2,m3)")
	haCreateCmd.Flags().StringSlice("etcd-endpoints", nil, "external DCS endpoints host:port,... (cross-host; takes precedence over --etcd)")
	haCreateCmd.Flags().String("advertise-host", "", "reachable host/IP for this member's connect_address (set for cross-host members; empty = loopback only)")
	haCreateCmd.Flags().Int("host-port", 0, "explicit PG port (auto-assigned from the patroni pool if 0; set explicitly and identically on cross-host peers)")
	haCreateCmd.Flags().Int("restapi-port", 0, "explicit Patroni REST API port (auto-assigned if 0)")
	haCreateCmd.Flags().String("passwords-file", "", "YAML file with the cluster password set (required for cross-host members; must match on every host)")

	haRemoveCmd.Flags().String("member", "", "remove a single member")
	haRemoveCmd.Flags().Bool("scope-all", false, "remove every local member and clear the cluster from the DCS")
	haRemoveCmd.Flags().Bool("clean-data", false, "also delete the member data dir(s)")
	haRemoveCmd.Flags().Bool("force", false, "skip confirmation")

	for _, c := range []*cobra.Command{haStartCmd, haStopCmd} {
		c.Flags().String("member", "", "member name")
		c.Flags().Bool("all", false, "all local members of the scope")
	}

	rootCmd.AddCommand(haCmd)
	haCmd.AddCommand(
		haCreateCmd, haStatusCmd, haRemoveCmd, haCtlCmd,
		haSwitchoverCmd, haFailoverCmd, haPauseCmd, haResumeCmd, haEditConfigCmd,
		haStartCmd, haStopCmd, haPasswordsCmd,
	)
	haPasswordsCmd.Flags().String("file", "", "write the YAML to this file (mode 0600) instead of stdout")
}
