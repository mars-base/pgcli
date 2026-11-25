package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/safeline"
)

// The console's 源 IP 获取方式 (SourceIP) setting decides which address
// tengine attributes a request to. The global record applies to every site
// that does not carry its own; per-site records need a Pro license.

var addonSafelineSourceIPCmd = &cobra.Command{
	Use:   "source-ip",
	Short: "View and change the client source IP acquisition (源 IP 获取方式)",
	Long: `View and change the console's 源 IP 获取方式 (SourceIP page): how tengine
derives the client address it logs and matches IP-based rules against.

  socket          the TCP connection's peer address (the default)
  xff-first       the rightmost X-Forwarded-For entry (the previous proxy)
  xff-second      the 2nd rightmost X-Forwarded-For entry
  xff-third       the 3rd rightmost X-Forwarded-For entry
  xff-dynamic     the X-Forwarded-For entry at --xff-position/--xff-index
  header          the address in the HTTP header named by --header
  proxy-protocol  the address from a PROXY Protocol preamble, accepted only
                  from the sources listed with --trust

Without --site-id the commands work on the global setting. With --site-id
they work on one site's own record; 'use-global' makes a site inherit the
global values again. The Community Edition rejects per-site proxy settings
with "license required" — only the global setting is editable there.

Warning: trusting a header or PROXY Protocol lets clients forge their source
address unless the listener receives traffic only from proxies you control;
IP-based ACL, rate limits and bans then match the forged address.

Subcommands:
  show        show the global setting or one site's record
  set         change the setting
  use-global  make a site follow the global setting again`,
}

var addonSafelineSourceIPShowCmd = &cobra.Command{
	Use:   "show [--site-id <id>]",
	Short: "Show the source IP acquisition setting",
	Long: `Show the global source IP acquisition setting, or one site's record when
--site-id is given. A site that follows the global setting shows the
effective global values.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineSourceIPShow(cmd)
	},
}

var addonSafelineSourceIPSetCmd = &cobra.Command{
	Use:   "set [--site-id <id>] --mode <mode> [flags]",
	Short: "Change the source IP acquisition setting",
	Long: `Change how tengine derives the client source address. Without --site-id
this writes the global setting; with --site-id it writes the site's own
record (Pro only).

Modes and their flags:
  --mode socket
  --mode xff-first | xff-second | xff-third
  --mode xff-dynamic  [--xff-position from-end|from-start] [--xff-index N]
  --mode header       --header <name>
  --mode proxy-protocol --trust <ip|cidr>[,<ip|cidr>...]

Only the ip_source/ip_value pair is written; every other proxy setting on
the site or global record stays as stored. The command re-reads the console
afterwards and reports a setting it did not apply.`,
	Example: `  pg addon safeline source-ip set --mode socket
  pg addon safeline source-ip set --mode xff-first
  pg addon safeline source-ip set --mode header --header X-Real-IP
  pg addon safeline source-ip set --site-id 3 --mode proxy-protocol \
      --trust 10.0.0.0/8,192.168.0.0/16`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineSourceIPSet(cmd)
	},
}

var addonSafelineSourceIPUseGlobalCmd = &cobra.Command{
	Use:   "use-global --site-id <id>",
	Short: "Make a site follow the global source IP acquisition",
	Long: `Make a site inherit the global source IP acquisition setting again,
dropping its own record (Pro only).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineSourceIPUseGlobal(cmd)
	},
}

func sourceIPModeLabel(mode string) string {
	return strings.ReplaceAll(mode, "_", "-")
}

func sourceIPDetail(mode, value string) string {
	switch mode {
	case safeline.SourceIPSocket:
		return "the TCP connection's peer address"
	case safeline.SourceIPXFFFirst:
		return "the rightmost X-Forwarded-For entry (the previous proxy)"
	case safeline.SourceIPXFFSecond:
		return "the 2nd rightmost X-Forwarded-For entry"
	case safeline.SourceIPXFFThird:
		return "the 3rd rightmost X-Forwarded-For entry"
	case safeline.SourceIPXFFDynamic:
		var sel struct {
			Position string `json:"position"`
			Index    int    `json:"index"`
		}
		if json.Unmarshal([]byte(value), &sel) == nil && sel.Index > 0 {
			where := "from the end"
			if sel.Position == "from_start" {
				where = "from the start"
			}
			return fmt.Sprintf("X-Forwarded-For entry %d %s", sel.Index, where)
		}
		return "X-Forwarded-For entry " + value
	case safeline.SourceIPHTTPHeader:
		return fmt.Sprintf("the client address in the %s header", value)
	case safeline.SourceIPProxyProtocol:
		return "the client address from PROXY Protocol, trusted sources " + value
	}
	return value
}

func printSourceIP(client *safeline.Client, instance string, siteID int) error {
	cur, err := client.GetSourceIP(siteID)
	if err != nil {
		return fmt.Errorf("reading source IP acquisition: %w", err)
	}
	if siteID == 0 {
		fmt.Printf("Source IP acquisition on %q (global):\n", instance)
		fmt.Printf("  Mode:   %s\n", sourceIPModeLabel(cur.Mode))
		fmt.Printf("  Detail: %s\n", sourceIPDetail(cur.Mode, cur.Value))
		return nil
	}
	site, err := client.GetSite(siteID)
	if err != nil {
		return fmt.Errorf("reading site %d: %w", siteID, err)
	}
	fmt.Printf("Source IP acquisition of site %d (%s) on %q:\n", siteID, strings.Join(site.ServerNames, ", "), instance)
	if cur.FollowGlobal {
		g, err := client.GetSourceIP(0)
		if err != nil {
			return fmt.Errorf("reading the global source IP acquisition: %w", err)
		}
		fmt.Printf("  Scope:  follows the global setting\n")
		fmt.Printf("  Mode:   %s\n", sourceIPModeLabel(g.Mode))
		fmt.Printf("  Detail: %s\n", sourceIPDetail(g.Mode, g.Value))
		return nil
	}
	fmt.Printf("  Scope:  site override\n")
	fmt.Printf("  Mode:   %s\n", sourceIPModeLabel(cur.Mode))
	fmt.Printf("  Detail: %s\n", sourceIPDetail(cur.Mode, cur.Value))
	return nil
}

func runAddonSafelineSourceIPShow(cmd *cobra.Command) error {
	client, sc, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	siteID, _ := cmd.Flags().GetInt("site-id")
	return printSourceIP(client, sc.Name, siteID)
}

func runAddonSafelineSourceIPSet(cmd *cobra.Command) error {
	client, sc, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	siteID, _ := cmd.Flags().GetInt("site-id")
	modeName, _ := cmd.Flags().GetString("mode")
	if modeName == "" {
		return fmt.Errorf("--mode is required (socket, xff-first, xff-second, xff-third, xff-dynamic, header or proxy-protocol)")
	}
	mode, err := safeline.NormalizeSourceIPMode(modeName)
	if err != nil {
		return err
	}
	header, _ := cmd.Flags().GetString("header")
	trust, _ := cmd.Flags().GetStringSlice("trust")
	position, _ := cmd.Flags().GetString("xff-position")
	index, _ := cmd.Flags().GetInt("xff-index")

	if mode != safeline.SourceIPHTTPHeader && cmd.Flags().Changed("header") {
		return fmt.Errorf("--header only applies to --mode header")
	}
	if mode != safeline.SourceIPProxyProtocol && cmd.Flags().Changed("trust") {
		return fmt.Errorf("--trust only applies to --mode proxy-protocol")
	}
	if mode != safeline.SourceIPXFFDynamic && (cmd.Flags().Changed("xff-position") || cmd.Flags().Changed("xff-index")) {
		return fmt.Errorf("--xff-position/--xff-index only apply to --mode xff-dynamic")
	}

	want := safeline.ProxyIPSource{Mode: mode}
	switch mode {
	case safeline.SourceIPHTTPHeader:
		if err := safeline.ValidateSourceIPHeader(header); err != nil {
			return err
		}
		want.Value = header
	case safeline.SourceIPProxyProtocol:
		value, err := safeline.NormalizeTrustedProxies(trust)
		if err != nil {
			return err
		}
		want.Value = value
	case safeline.SourceIPXFFDynamic:
		value, err := safeline.BuildXFFSelector(position, index)
		if err != nil {
			return err
		}
		want.Value = value
	}

	inTarget := "the global setting"
	if siteID > 0 {
		inTarget = fmt.Sprintf("site %d", siteID)
	}

	cur, err := client.GetSourceIP(siteID)
	if err != nil {
		return fmt.Errorf("reading source IP acquisition: %w", err)
	}
	effective := cur
	if siteID > 0 && cur.FollowGlobal {
		effective, err = client.GetSourceIP(0)
		if err != nil {
			return fmt.Errorf("reading the global source IP acquisition: %w", err)
		}
	}
	if effective.Mode == want.Mode && effective.Value == want.Value {
		fmt.Printf("Source IP acquisition of %s already %s on %q\n", inTarget, sourceIPModeLabel(want.Mode), sc.Name)
		return printSourceIP(client, sc.Name, siteID)
	}

	if err := client.SetSourceIP(siteID, want); err != nil {
		if siteID > 0 && strings.Contains(err.Error(), "license required") {
			return fmt.Errorf("setting the source IP acquisition of %s: %w — per-site proxy settings need a Pro license; only the global setting is editable in the Community Edition", inTarget, err)
		}
		return fmt.Errorf("setting the source IP acquisition of %s: %w", inTarget, err)
	}
	got, err := client.GetSourceIP(siteID)
	if err != nil {
		return fmt.Errorf("reading back source IP acquisition: %w", err)
	}
	if got.FollowGlobal || got.Mode != want.Mode || got.Value != want.Value {
		return fmt.Errorf("the console kept %s on %s instead of %s", sourceIPModeLabel(got.Mode), inTarget, sourceIPModeLabel(want.Mode))
	}
	fmt.Printf("✓ Set the source IP acquisition of %s to %s on %q\n", inTarget, sourceIPModeLabel(want.Mode), sc.Name)
	return printSourceIP(client, sc.Name, siteID)
}

func runAddonSafelineSourceIPUseGlobal(cmd *cobra.Command) error {
	client, sc, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	siteID, _ := cmd.Flags().GetInt("site-id")
	if siteID < 1 {
		return fmt.Errorf("--site-id is required (list the ids with 'pg addon safeline site list')")
	}
	cur, err := client.GetSourceIP(siteID)
	if err != nil {
		return fmt.Errorf("reading source IP acquisition: %w", err)
	}
	if cur.FollowGlobal {
		fmt.Printf("Site %d already follows the global source IP acquisition on %q\n", siteID, sc.Name)
		return printSourceIP(client, sc.Name, siteID)
	}
	want := safeline.ProxyIPSource{Mode: cur.Mode, Value: cur.Value, FollowGlobal: true}
	if err := client.SetSourceIP(siteID, want); err != nil {
		if strings.Contains(err.Error(), "license required") {
			return fmt.Errorf("making site %d follow the global source IP acquisition: %w — per-site proxy settings need a Pro license; only the global setting is editable in the Community Edition", siteID, err)
		}
		return fmt.Errorf("making site %d follow the global source IP acquisition: %w", siteID, err)
	}
	got, err := client.GetSourceIP(siteID)
	if err != nil {
		return fmt.Errorf("reading back source IP acquisition: %w", err)
	}
	if !got.FollowGlobal {
		return fmt.Errorf("the console kept site %d on its own source IP acquisition", siteID)
	}
	fmt.Printf("✓ Site %d now follows the global source IP acquisition on %q\n", siteID, sc.Name)
	return printSourceIP(client, sc.Name, siteID)
}
