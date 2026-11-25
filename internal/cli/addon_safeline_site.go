package cli

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/podman"
	"github.com/mars-base/pgcli/internal/safeline"
)

// ---------------------------------------------------------------------------
//
// Protected sites live in the SafeLine console, but everything the console UI
// does over its own web API (encrypted admin login, then /api/open/site) can
// be done from the host too. These subcommands drive that API with the stored
// admin password, so sites can be scripted without a browser.

var addonSafelineCmd = &cobra.Command{
	Use:   "safeline",
	Short: "Manage protected sites on a SafeLine addon",
	Long: `Manage protected sites (the applications tengine fronts) on a SafeLine
add-on instance.

Site changes go through the console's own web API with the admin password
pgcli stores in pg.yaml — the same calls the console UI makes — so the
tengine rules are published immediately, exactly as if the site had been
created in the browser.

Subcommands:
  site       List, create, update, delete and health-check protected sites
  site cc    View and change a site's CC protection (waiting room, rate limits)
  cert       List, upload, renew and delete TLS certificates
  telemetry  View and switch off the console's threat-intelligence sharing

Examples:
  pg addon safeline site list
  pg addon safeline site create --port 443_ssl --domain www.example.com \
      --upstream http://10.0.0.5:8080 --cert fullchain.pem --key privkey.pem --health-check
  pg addon safeline site health
  pg addon safeline site mode --site-id 3 dryrun
  pg addon safeline site cc show --site-id 3
  pg addon safeline cert update --cert-id 2 --cert fullchain.pem --key privkey.pem
  pg addon safeline cert list
  pg addon safeline telemetry disable`,
}

var addonSafelineSiteCmd = &cobra.Command{
	Use:   "site",
	Short: "List, create, update, delete and health-check protected sites",
	Long: `Manage the protected sites of a SafeLine instance.

A site maps host entry port(s) served by tengine to one or more upstream
servers. Entry ports bind directly on the host (tengine uses host
networking), so a port already in use is refused at creation.

Upstream health checks probe each upstream's HTTP path every 10-3600
seconds. TLS certificates for _ssl entry ports are managed with
'pg addon safeline cert' (upload, renew in place, delete) or bound directly
via --cert/--key or --cert-id on create and rebound with
'site update --site-id <id> --cert-id <new id>'; custom error pages stay in
the console UI.

Each site runs in one of three modes — defense (block attacks, the console
default), dryrun (record detections only) or offline (maintenance page) —
switched with 'pg addon safeline site mode'. Its CC protection — the waiting
room and the rate-limit rules — is managed with 'pg addon safeline site cc'.

The instance must be running ('pg addon start safeline --name <name>').`,
}

var addonSafelineSiteListCmd = &cobra.Command{
	Use:   "list",
	Short: "List protected sites",
	Long: `List every protected site with its id, domains, entry ports, upstreams,
status, run mode, health check settings and comment.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineSiteList(cmd)
	},
}

var addonSafelineSiteCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a protected site",
	Long: `Create a protected site: tengine starts accepting traffic on the given
entry port(s) and proxies it to the upstream(s).

--port takes the entry port on the host; suffix it _ssl to terminate TLS on
that port (the console's default certificate is used). --domain defaults to
"*" (any Host header); pass the real domain(s) so tengine routes by virtual
host. --upstream takes absolute http:// or https:// URLs.

--balance picks the load-balancing algorithm across multiple upstreams:
roundrobin (default), leastconn, iphash or backup. The console pgcli installs
runs as the Community Edition, which allows one upstream per site; a second
upstream is refused with "license required" (load balancing needs Pro).

--health-check enables upstream health checks (interval 10-3600 seconds,
default 10; probe path must start with /).

TLS entry ports (a --port suffixed _ssl) terminate TLS at tengine. Without a
certificate the console's default certificate is used; bind a real one with
--cert/--key (uploaded first, see 'pg addon safeline cert') or with --cert-id
(a certificate already stored in the console). Weights and backup upstream
roles stay console-only.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineSiteCreate(cmd)
	},
}

var addonSafelineSiteUpdateCmd = &cobra.Command{
	Use:   "update --site-id <id>",
	Short: "Change a site's upstreams, health check settings or bound certificate",
	Long: `Change the upstreams, the upstream health check settings or the bound TLS
certificate of an existing site (--site-id; list the ids with
'pg addon safeline site list').

--upstream replaces the site's whole upstream list (repeatable or
comma-separated), so pass the complete set you want tengine to balance
across. --add-upstream appends to the current list and --remove-upstream
drops entries from it; every change is published to tengine immediately.
The Community Edition allows one upstream per site, so growing the list
beyond one is refused by the console with "license required" (load
balancing needs Pro).

--health-check enables the health check (--health-check=false disables it),
while --health-check-interval (10-3600 seconds) and --health-check-path
adjust the probe. Enabling without an interval uses the current one, or 10
seconds when none was set yet.

--cert-id binds a different certificate already stored in the console to the
site's TLS entry port. To renew a certificate that several sites share,
replace its PEM in place with
'pg addon safeline cert update --cert-id <id> --cert ... --key ...'
instead — every bound site picks up the new certificate without being
touched. Only the flags you pass are changed; other site settings keep their
console values.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineSiteUpdate(cmd)
	},
}

var addonSafelineSiteDeleteCmd = &cobra.Command{
	Use:   "delete --site-id <id> [--site-id <id>...]",
	Short: "Delete protected sites",
	Long: `Delete one or more protected sites by id (--site-id, repeatable or
comma-separated). Their entry ports are released and tengine stops serving
them; this cannot be undone from pgcli (re-create the site to restore it).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineSiteDelete(cmd)
	},
}

var addonSafelineSiteHealthCmd = &cobra.Command{
	Use:   "health [--site-id <id> ...]",
	Short: "Probe the upstreams of protected sites",
	Long: `Probe each selected site's upstreams through the console and print the
result per upstream: "healthy" when the upstream answers, otherwise the
console's error such as "unreachable".

With no --site-id every site is probed. The command exits non-zero when any
upstream is unhealthy, so it can gate scripts and monitoring.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineSiteHealth(cmd)
	},
}

var addonSafelineSiteModeCmd = &cobra.Command{
	Use:   "mode --site-id <id> [--site-id <id>...] <defense|dryrun|offline>",
	Short: "Switch protected sites between defense, dryrun and offline modes",
	Long: `Switch one or more protected sites to a run mode:

  defense   block attacks (the console default); enabled rules are enforced
  dryrun    observation mode: detections are only recorded, nothing is blocked
  offline   maintenance mode: a maintenance page is served, the site is
            inaccessible

Pass the site ids with --site-id (repeatable or comma-separated; list them
with 'pg addon safeline site list') and the mode as the only positional
argument. The change is published to tengine immediately, exactly like the
console's own mode switch.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineSiteMode(cmd, args[0])
	},
}

// resolveSafelineConsole loads the instance, checks the console container is
// running, and logs in with the stored admin password.
func resolveSafelineConsole(cmd *cobra.Command) (*safeline.Client, config.SafelineConfig, error) {
	name := safelineFlagName(cmd)
	c, _, sc, err := resolveSafelineInstance(name)
	if err != nil {
		return nil, config.SafelineConfig{}, err
	}
	m, err := podman.NewSafelineManager(c)
	if err != nil {
		return nil, config.SafelineConfig{}, err
	}
	running, exists, err := m.ContainerStatus(&sc, podman.SafelineRoleMgt)
	if err != nil {
		return nil, config.SafelineConfig{}, err
	}
	if !exists || !running {
		return nil, config.SafelineConfig{}, fmt.Errorf("safeline %q console container is not running (start it with 'pg addon start safeline --name %s')", name, name)
	}
	client := safeline.NewClient(podman.ConsoleURL(&sc), sc.AdminUser, sc.AdminPassword)
	if err := client.Login(); err != nil {
		return nil, config.SafelineConfig{}, fmt.Errorf("logging in to the SafeLine console at %s: %w", podman.ConsoleURL(&sc), err)
	}
	return client, sc, nil
}

func runAddonSafelineSiteList(cmd *cobra.Command) error {
	client, sc, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	sites, err := client.ListSites()
	if err != nil {
		return err
	}
	if len(sites) == 0 {
		fmt.Printf("No protected sites on %q (console %s).\n", sc.Name, podman.ConsoleURL(&sc))
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tDOMAINS\tPORTS\tUPSTREAMS\tSTATUS\tMODE\tHEALTH\tCOMMENT")
	for _, s := range sites {
		status := "disabled"
		if s.IsEnabled {
			status = "enabled"
		}
		health := "off"
		if s.HealthCheck {
			health = fmt.Sprintf("every %ds %s", s.HealthCheckInterval, s.HealthCheckPath)
		}
		comment := s.Comment
		if comment == "" {
			comment = "-"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			s.ID,
			strings.Join(s.ServerNames, ","),
			strings.Join(s.Ports, ","),
			strings.Join(s.EffectiveUpstreams(), ","),
			status,
			safeline.ModeName(s.Mode),
			health,
			comment,
		)
	}
	return w.Flush()
}

func runAddonSafelineSiteCreate(cmd *cobra.Command) error {
	fs := cmd.Flags()
	ports, _ := fs.GetStringSlice("port")
	domains, _ := fs.GetStringSlice("domain")
	upstreams, _ := fs.GetStringSlice("upstream")
	comment, _ := fs.GetString("comment")
	balanceName, _ := fs.GetString("balance")
	healthCheck, _ := fs.GetBool("health-check")
	interval, _ := fs.GetInt("health-check-interval")
	path, _ := fs.GetString("health-check-path")
	certID, _ := fs.GetInt("cert-id")
	certPath, _ := fs.GetString("cert")
	keyPath, _ := fs.GetString("key")

	if err := safeline.ValidateEntryPorts(ports); err != nil {
		return err
	}
	if err := safeline.ValidateUpstreams(upstreams); err != nil {
		return err
	}
	balance, err := safeline.ParseBalance(balanceName)
	if err != nil {
		return err
	}
	if !healthCheck && (fs.Changed("health-check-interval") || fs.Changed("health-check-path")) {
		return fmt.Errorf("--health-check-interval and --health-check-path only apply together with --health-check")
	}
	if healthCheck {
		if err := safeline.ValidateHealthCheck(interval, path); err != nil {
			return err
		}
	}
	if fs.Changed("cert-id") && (certPath != "" || keyPath != "") {
		return fmt.Errorf("--cert-id binds an existing certificate; do not combine it with --cert/--key")
	}
	if certPath != "" || keyPath != "" {
		if certPath == "" || keyPath == "" {
			return fmt.Errorf("--cert and --key must be given together")
		}
	} else if fs.Changed("cert-id") && certID < 1 {
		return fmt.Errorf("invalid certificate id %d", certID)
	}
	names := safeline.NormalizeServerNames(domains)

	client, _, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	if certPath != "" {
		crt, key, err := safeline.LoadCertificate(certPath, keyPath)
		if err != nil {
			return err
		}
		if certID, err = client.UploadCert(crt, key); err != nil {
			return fmt.Errorf("uploading certificate: %w", err)
		}
	}
	id, err := client.CreateSite(safeline.SiteSpec{
		ServerNames:         names,
		Ports:               ports,
		Upstreams:           upstreams,
		Comment:             comment,
		BalanceType:         balance,
		CertID:              certID,
		HealthCheck:         healthCheck,
		HealthCheckInterval: interval,
		HealthCheckPath:     path,
	})
	if err != nil {
		if certPath != "" {
			_ = client.DeleteCert(certID)
		}
		return fmt.Errorf("creating site: %w", licenseHint(err))
	}

	fmt.Printf("✓ Created SafeLine site %d\n", id)
	fmt.Printf("  Domains:    %s\n", strings.Join(names, ", "))
	fmt.Printf("  Ports:      %s\n", strings.Join(ports, ", "))
	fmt.Printf("  Upstreams:  %s\n", strings.Join(upstreams, ", "))
	fmt.Printf("  Balance:    %s\n", safeline.BalanceName(balance))
	if certID > 0 {
		fmt.Printf("  Certificate: %d\n", certID)
	}
	if healthCheck {
		fmt.Printf("  Health:     every %ds on %s\n", interval, path)
	} else {
		fmt.Printf("  Health:     off\n")
	}
	return nil
}

func runAddonSafelineSiteUpdate(cmd *cobra.Command) error {
	fs := cmd.Flags()
	id, _ := fs.GetInt("site-id")
	if id < 1 {
		return fmt.Errorf("--site-id is required (list the ids with 'pg addon safeline site list')")
	}
	var patch safeline.HealthCheckPatch
	if fs.Changed("health-check") {
		v, _ := fs.GetBool("health-check")
		patch.Enabled = &v
	}
	if fs.Changed("health-check-interval") {
		v, _ := fs.GetInt("health-check-interval")
		if err := safeline.ValidateHealthCheck(v, "/"); err != nil {
			return err
		}
		patch.Interval = &v
	}
	if fs.Changed("health-check-path") {
		v, _ := fs.GetString("health-check-path")
		if err := safeline.ValidateHealthCheckPath(v); err != nil {
			return err
		}
		patch.Path = &v
	}
	healthChanged := patch.Enabled != nil || patch.Interval != nil || patch.Path != nil
	certChanged := fs.Changed("cert-id")
	certID, _ := fs.GetInt("cert-id")
	if certChanged && certID < 1 {
		return fmt.Errorf("invalid certificate id %d (list the ids with 'pg addon safeline cert list')", certID)
	}
	upstreamsChanged := fs.Changed("upstream")
	upstreams, _ := fs.GetStringSlice("upstream")
	addChanged := fs.Changed("add-upstream")
	addUpstreams, _ := fs.GetStringSlice("add-upstream")
	removeChanged := fs.Changed("remove-upstream")
	removeUpstreams, _ := fs.GetStringSlice("remove-upstream")
	if upstreamsChanged {
		if err := safeline.ValidateUpstreams(upstreams); err != nil {
			return err
		}
	}
	if upstreamsChanged && (addChanged || removeChanged) {
		return fmt.Errorf("--upstream replaces the whole list; do not combine it with --add-upstream/--remove-upstream")
	}
	if addChanged {
		if err := safeline.ValidateUpstreams(addUpstreams); err != nil {
			return err
		}
	}
	if removeChanged && len(removeUpstreams) == 0 {
		return fmt.Errorf("--remove-upstream needs at least one URL")
	}
	if !healthChanged && !certChanged && !upstreamsChanged && !addChanged && !removeChanged {
		return fmt.Errorf("nothing to update: pass --upstream, --add-upstream, --remove-upstream, --health-check[=false], --health-check-interval, --health-check-path or --cert-id")
	}

	client, _, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	var site *safeline.Site
	var added, removed []string
	if upstreamsChanged || addChanged || removeChanged {
		want := upstreams
		if !upstreamsChanged {
			cur, err := client.GetSite(id)
			if err != nil {
				return fmt.Errorf("updating site %d: %w", id, err)
			}
			current := cur.EffectiveUpstreams()
			for _, u := range addUpstreams {
				if !slices.Contains(current, u) && !slices.Contains(added, u) {
					added = append(added, u)
				}
			}
			if want, err = safeline.MergeUpstreams(current, addUpstreams, removeUpstreams); err != nil {
				return fmt.Errorf("updating site %d: %w", id, err)
			}
			if len(want) == 0 {
				return fmt.Errorf("cannot remove every upstream of site %d", id)
			}
			removed = removeUpstreams
		}
		if site, err = client.SetSiteUpstreams(id, want); err != nil {
			return fmt.Errorf("updating site %d: %w", id, licenseHint(err))
		}
	}
	if healthChanged {
		if site, err = client.PatchSiteHealthCheck(id, patch); err != nil {
			return fmt.Errorf("updating site %d: %w", id, err)
		}
	}
	if certChanged {
		if site, err = client.SetSiteCertID(id, certID); err != nil {
			return fmt.Errorf("updating site %d: %w", id, err)
		}
	}
	fmt.Printf("✓ Updated SafeLine site %d\n", id)
	if ups := site.EffectiveUpstreams(); len(ups) > 0 {
		fmt.Printf("  Upstreams:  %s\n", strings.Join(ups, ", "))
	}
	if len(added) > 0 {
		fmt.Printf("  Added:      %s\n", strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		fmt.Printf("  Removed:    %s\n", strings.Join(removed, ", "))
	}
	if site.HealthCheck {
		fmt.Printf("  Health:     every %ds on %s\n", site.HealthCheckInterval, site.HealthCheckPath)
	} else {
		fmt.Printf("  Health:     off\n")
	}
	if site.CertID > 0 {
		fmt.Printf("  Certificate: %d\n", site.CertID)
	}
	return nil
}

func runAddonSafelineSiteDelete(cmd *cobra.Command) error {
	raw, _ := cmd.Flags().GetIntSlice("site-id")
	if len(raw) == 0 {
		return fmt.Errorf("at least one --site-id is required (list them with 'pg addon safeline site list')")
	}
	ids := make([]int, 0, len(raw))
	for _, id := range raw {
		if id < 1 {
			return fmt.Errorf("invalid site id %d", id)
		}
		ids = append(ids, id)
	}
	client, _, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	if err := client.DeleteSites(ids); err != nil {
		return fmt.Errorf("deleting site(s): %w", err)
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.Itoa(id))
	}
	fmt.Printf("✓ Deleted SafeLine site(s): %s\n", strings.Join(parts, ", "))
	return nil
}

func runAddonSafelineSiteHealth(cmd *cobra.Command) error {
	client, _, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	sites, err := client.ListSites()
	if err != nil {
		return err
	}
	raw, _ := cmd.Flags().GetIntSlice("site-id")
	wanted := make(map[int]bool, len(raw))
	for _, id := range raw {
		if id < 1 {
			return fmt.Errorf("invalid site id %d", id)
		}
		wanted[id] = true
	}
	selected := make([]safeline.Site, 0, len(sites))
	for _, s := range sites {
		if len(wanted) > 0 && !wanted[s.ID] {
			continue
		}
		delete(wanted, s.ID)
		selected = append(selected, s)
	}
	if len(wanted) > 0 {
		missing := make([]string, 0, len(wanted))
		for id := range wanted {
			missing = append(missing, strconv.Itoa(id))
		}
		return fmt.Errorf("site id(s) not found: %s (list them with 'pg addon safeline site list')", strings.Join(missing, ", "))
	}
	if len(selected) == 0 {
		fmt.Println("No protected sites configured.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "SITE\tDOMAINS\tUPSTREAM\tSTATUS")
	unhealthy, total := 0, 0
	for _, s := range selected {
		upstreams := s.EffectiveUpstreams()
		if len(upstreams) == 0 {
			fmt.Fprintf(w, "%d\t%s\t-\tno upstreams\n", s.ID, strings.Join(s.ServerNames, ","))
			continue
		}
		res, err := client.HealthCheck(upstreams, s.ServerNames)
		if err != nil {
			return fmt.Errorf("site %d health check: %w", s.ID, err)
		}
		for _, u := range upstreams {
			total++
			status := "healthy"
			if res[u] != "" {
				status = res[u]
				unhealthy++
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", s.ID, strings.Join(s.ServerNames, ","), u, status)
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if unhealthy > 0 {
		return fmt.Errorf("%d of %d upstreams unhealthy", unhealthy, total)
	}
	return nil
}

func runAddonSafelineSiteMode(cmd *cobra.Command, name string) error {
	mode, err := safeline.ParseMode(name)
	if err != nil {
		return err
	}
	raw, _ := cmd.Flags().GetIntSlice("site-id")
	if len(raw) == 0 {
		return fmt.Errorf("at least one --site-id is required (list them with 'pg addon safeline site list')")
	}
	client, _, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	sites, err := client.ListSites()
	if err != nil {
		return err
	}
	known := make(map[int]bool, len(sites))
	for _, s := range sites {
		known[s.ID] = true
	}
	ids := make([]int, 0, len(raw))
	var missing []string
	for _, id := range raw {
		if id < 1 {
			return fmt.Errorf("invalid site id %d", id)
		}
		if !known[id] {
			missing = append(missing, strconv.Itoa(id))
			continue
		}
		ids = append(ids, id)
	}
	if len(missing) > 0 {
		return fmt.Errorf("site id(s) not found: %s (list them with 'pg addon safeline site list')", strings.Join(missing, ", "))
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.Itoa(id))
	}
	current := make(map[int]int, len(sites))
	for _, s := range sites {
		current[s.ID] = s.Mode
	}
	changed := make([]int, 0, len(ids))
	for _, id := range ids {
		if current[id] != mode {
			changed = append(changed, id)
		}
	}
	if len(changed) == 0 {
		fmt.Printf("Site(s) %s already in %s mode\n", strings.Join(parts, ", "), safeline.ModeName(mode))
		return nil
	}
	if err := client.SetSiteMode(changed, mode); err != nil {
		return fmt.Errorf("switching site mode: %w", err)
	}
	fmt.Printf("✓ Set SafeLine site(s) %s to %s mode\n", strings.Join(parts, ", "), safeline.ModeName(mode))
	return nil
}

// licenseHint annotates the console's bare "license required" refusal with
// the feature it is gating: the console image ships as the Community Edition,
// where a site is limited to a single upstream.
func licenseHint(err error) error {
	if err == nil || !strings.Contains(err.Error(), "license required") {
		return err
	}
	return fmt.Errorf("%w (the Community Edition allows one upstream per site; multiple upstreams and load balancing require a Pro license)", err)
}
