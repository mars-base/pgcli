package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/safeline"
)

// The console's BOT protection (人机验证) challenges visitors with an
// invisible verification. Each site carries its own configuration — switch
// and optional match conditions — which the console stores as a challenge
// policy behind the site's challenge_id. The slide verification, validity
// editing and replay protection need a Pro license and stay out of this
// command; the CLI always writes the invisible level and keeps whatever
// validity is stored.

var addonSafelineSiteBotCmd = &cobra.Command{
	Use:   "bot",
	Short: "View and change a site's BOT protection (人机验证)",
	Long: `View and change the BOT protection (人机验证) of one protected site: an
invisible verification that challenged visitors pass without interaction.

Subcommands:
  show       show whether BOT protection is on, the verification validity and
             the match conditions
  enable     turn it on, keeping the stored validity and match conditions
  disable    turn it off
  condition  change which visitors are challenged (满足条件时认证)

Each site has its own configuration. The slide verification (滑动验证), the
validity setting and replay protection require a Pro license: the Community
Edition keeps the invisible level and a one-hour validity. Match conditions
(which visitors get challenged) are kept as the console has them. BOT
protection applies while the site runs in defense mode.`,
}

var addonSafelineSiteBotShowCmd = &cobra.Command{
	Use:   "show --site-id <id>",
	Short: "Show a site's BOT protection",
	Long: `Show one site's BOT protection: whether it is enabled, the
verification level, how long a passed verification stays valid and which
visitors are challenged.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineSiteBotShow(cmd)
	},
}

var addonSafelineSiteBotEnableCmd = &cobra.Command{
	Use:   "enable --site-id <id> [--where <rule> ...]",
	Short: "Enable a site's BOT protection",
	Long: `Enable a site's BOT protection with the invisible verification. The
stored validity is kept; without --where the stored match conditions are kept
too, and a site without any conditions challenges every visitor. Each --where
rule sets the conditions in the same call (same syntax as 'bot condition') —
the console only accepts conditions while the protection is on, so this is
the way to pre-configure a disabled site.

Like the console page, the command re-reads the site afterwards and reports a
configuration the console did not apply instead of pretending the change
stuck.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineSiteBotEnable(cmd)
	},
}

var addonSafelineSiteBotDisableCmd = &cobra.Command{
	Use:   "disable --site-id <id>",
	Short: "Disable a site's BOT protection",
	Long: `Disable a site's BOT protection. The stored configuration — validity
and match conditions — is kept, so enabling it again restores it.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineSiteBotDisable(cmd)
	},
}

var addonSafelineSiteBotConditionCmd = &cobra.Command{
	Use:   "condition --site-id <id> (--where <rule> ... | --all-visitors)",
	Short: "Change which visitors a site's BOT protection challenges",
	Long: `Change the match conditions of a site's BOT protection: the console's
"仅针对特定条件启用人机验证" option in its "满足条件时认证" mode — only the
visitors matching the conditions are challenged. The switch, validity and the
other mode (满足条件时跳过认证) are left alone. Conditions can only be changed
while the protection is on; a disabled site is pointed at 'bot enable --where'.

Each --where rule is one condition group, and groups are ORed. Inside a rule,
&& joins conditions that must all match (ANDed), and | separates the values of
one condition:

  --where 'uri:prefix=/admin && method:eq=POST'
  --where 'src_ip:cidr=10.0.0.0/8|192.168.0.0/16'

A condition is field:operator=value. Header and parameter fields address the
header or parameter by name: req_header/User-Agent:has=curl. Fields: src_ip,
uri, uri_no_query, host, req_header, req_raw_header, res_raw_header, req_body,
res_body, get_param, post_param, method, website, ja4 — each takes the
operators the console offers. Some console choices are out of reach here:
geo_* and asn_* on src_ip need a Pro license, and in/not_in on src_ip plus
ja4_in select items that only exist in the console (IP groups, recorded JA4
fingerprints) — configure those in the console.

--all-visitors drops the conditions and challenges every visitor again. The
command re-reads the site afterwards and reports conditions the console did
not apply.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineSiteBotCondition(cmd)
	},
}

func botSiteID(cmd *cobra.Command) (int, error) {
	id, _ := cmd.Flags().GetInt("site-id")
	if id < 1 {
		return 0, fmt.Errorf("--site-id is required (list the ids with 'pg addon safeline site list')")
	}
	return id, nil
}

func botLevelName(level int) string {
	if level == safeline.BotLevelInvisible {
		return "invisible verification"
	}
	return fmt.Sprintf("level %d", level)
}

func botSummary(cfg *safeline.BotConfig) string {
	state := "disabled"
	if cfg.Enabled {
		state = "enabled"
	}
	return fmt.Sprintf("%s (%s, valid %dh)", state, botLevelName(cfg.Level), cfg.Expire/3600)
}

func botConditionsSummary(cfg *safeline.BotConfig) string {
	if len(cfg.Pattern) == 0 {
		return "all visitors"
	}
	if cfg.Negate {
		return "all but visitors matching the conditions below"
	}
	return "only visitors matching the conditions below"
}

func printBotConfig(site *safeline.Site, cfg *safeline.BotConfig) error {
	fmt.Printf("Site %d (%s) BOT protection (人机验证)\n", site.ID, strings.Join(site.ServerNames, ","))
	fmt.Printf("  Status:    %s\n", func() string {
		if cfg.Enabled {
			return "enabled"
		}
		return "disabled"
	}())
	fmt.Printf("  Level:     %s\n", botLevelName(cfg.Level))
	fmt.Printf("  Validity:  %dh\n", cfg.Expire/3600)
	fmt.Printf("  Match:     %s\n", botConditionsSummary(cfg))
	if len(cfg.Pattern) == 0 {
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "GROUP\tFIELD\tOP\tVALUE")
	for i, group := range cfg.Pattern {
		for _, cond := range group {
			field := cond.Key
			if cond.SubKey != "" {
				field += ":" + cond.SubKey
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", i+1, field, cond.Operator, strings.Join(cond.Value, ","))
		}
	}
	return w.Flush()
}

func runAddonSafelineSiteBotShow(cmd *cobra.Command) error {
	id, err := botSiteID(cmd)
	if err != nil {
		return err
	}
	client, _, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	site, err := client.GetSite(id)
	if err != nil {
		return fmt.Errorf("site %d: %w", id, err)
	}
	cfg, err := client.GetSiteBot(id)
	if err != nil {
		return fmt.Errorf("site %d BOT protection: %w", id, err)
	}
	return printBotConfig(site, cfg)
}

func runAddonSafelineSiteBotEnable(cmd *cobra.Command) error {
	id, err := botSiteID(cmd)
	if err != nil {
		return err
	}
	client, _, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	site, err := client.GetSite(id)
	if err != nil {
		return fmt.Errorf("site %d: %w", id, err)
	}
	cur, err := client.GetSiteBot(id)
	if err != nil {
		return fmt.Errorf("site %d BOT protection: %w", id, err)
	}
	pattern := cur.Pattern
	rules, _ := cmd.Flags().GetStringArray("where")
	if len(rules) > 0 {
		if pattern, err = parseBotWhere(rules); err != nil {
			return err
		}
	}
	if cur.Enabled && !cur.Negate && cur.Level == safeline.BotLevelInvisible && sameBotPattern(cur.Pattern, pattern) {
		fmt.Printf("Site %d (%s) BOT protection already enabled with the requested configuration\n", site.ID, strings.Join(site.ServerNames, ","))
		return printBotConfig(site, cur)
	}
	if err := client.SetSiteBot(id, true, cur.Expire, cur.Negate, pattern); err != nil {
		return fmt.Errorf("enabling site %d BOT protection: %w", id, err)
	}
	got, err := client.GetSiteBot(id)
	if err != nil {
		return fmt.Errorf("reading back site %d BOT protection: %w", id, err)
	}
	if !got.Enabled {
		return fmt.Errorf("the console did not enable BOT protection on site %d", id)
	}
	if len(rules) > 0 && !sameBotPattern(got.Pattern, pattern) {
		return fmt.Errorf("the console did not apply the requested match conditions on site %d", id)
	}
	if got.Level != safeline.BotLevelInvisible {
		return fmt.Errorf("the console kept verification level %d (the CLI writes the invisible level)", got.Level)
	}
	fmt.Printf("✓ Enabled SafeLine site %d (%s) BOT protection: %s\n", site.ID, strings.Join(site.ServerNames, ","), botSummary(got))
	return printBotConfig(site, got)
}

func runAddonSafelineSiteBotDisable(cmd *cobra.Command) error {
	id, err := botSiteID(cmd)
	if err != nil {
		return err
	}
	client, _, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	site, err := client.GetSite(id)
	if err != nil {
		return fmt.Errorf("site %d: %w", id, err)
	}
	cur, err := client.GetSiteBot(id)
	if err != nil {
		return fmt.Errorf("site %d BOT protection: %w", id, err)
	}
	if !cur.Enabled {
		fmt.Printf("Site %d (%s) BOT protection is already disabled\n", site.ID, strings.Join(site.ServerNames, ","))
		return printBotConfig(site, cur)
	}
	if err := client.SetSiteBot(id, false, cur.Expire, cur.Negate, cur.Pattern); err != nil {
		return fmt.Errorf("disabling site %d BOT protection: %w", id, err)
	}
	got, err := client.GetSiteBot(id)
	if err != nil {
		return fmt.Errorf("reading back site %d BOT protection: %w", id, err)
	}
	if got.Enabled {
		return fmt.Errorf("the console did not disable BOT protection on site %d", id)
	}
	fmt.Printf("✓ Disabled SafeLine site %d (%s) BOT protection\n", site.ID, strings.Join(site.ServerNames, ","))
	return printBotConfig(site, got)
}

// parseBotWhere turns the repeatable --where rules into the console's pattern
// form: every rule is one OR group, && joins ANDed conditions inside a rule,
// and | separates the values of one condition.
func parseBotWhere(rules []string) ([][]safeline.BotCondition, error) {
	var groups [][]safeline.BotCondition
	for _, rule := range rules {
		var group []safeline.BotCondition
		for _, part := range strings.Split(rule, "&&") {
			part = strings.TrimSpace(part)
			if part == "" {
				return nil, fmt.Errorf("empty condition in %q", rule)
			}
			cond, err := parseBotCondition(part)
			if err != nil {
				return nil, err
			}
			group = append(group, cond)
		}
		groups = append(groups, group)
	}
	return groups, nil
}

func parseBotCondition(s string) (safeline.BotCondition, error) {
	left, right, ok := strings.Cut(s, "=")
	if !ok {
		// Operator exist/not_exist takes no value and may omit the =.
		left, right = s, ""
	}
	fieldPart, op, ok := strings.Cut(left, ":")
	if !ok {
		return safeline.BotCondition{}, fmt.Errorf("condition %q has no operator (field:operator=value)", s)
	}
	field, sub, _ := strings.Cut(fieldPart, "/")
	cond := safeline.BotCondition{Key: field, Operator: op, SubKey: sub}
	for _, v := range strings.Split(right, "|") {
		if v = strings.TrimSpace(v); v != "" {
			cond.Value = append(cond.Value, v)
		}
	}
	if err := safeline.ValidateBotCondition(cond); err != nil {
		return cond, err
	}
	return cond, nil
}

func sameBotPattern(a, b [][]safeline.BotCondition) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			x, y := a[i][j], b[i][j]
			if x.Key != y.Key || x.Operator != y.Operator || x.SubKey != y.SubKey || len(x.Value) != len(y.Value) {
				return false
			}
			for k := range x.Value {
				if x.Value[k] != y.Value[k] {
					return false
				}
			}
		}
	}
	return true
}

func runAddonSafelineSiteBotCondition(cmd *cobra.Command) error {
	id, err := botSiteID(cmd)
	if err != nil {
		return err
	}
	rules, _ := cmd.Flags().GetStringArray("where")
	all, _ := cmd.Flags().GetBool("all-visitors")
	if all && len(rules) > 0 {
		return fmt.Errorf("--all-visitors cannot be combined with --where")
	}
	if !all && len(rules) == 0 {
		return fmt.Errorf("pass --where '<field>:<operator>=<value>' (repeat for OR groups) or --all-visitors")
	}
	var pattern [][]safeline.BotCondition
	if !all {
		if pattern, err = parseBotWhere(rules); err != nil {
			return err
		}
	}
	client, _, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	site, err := client.GetSite(id)
	if err != nil {
		return fmt.Errorf("site %d: %w", id, err)
	}
	cur, err := client.GetSiteBot(id)
	if err != nil {
		return fmt.Errorf("site %d BOT protection: %w", id, err)
	}
	// The console ignores condition writes while the protection is off
	// (its rule editor is only reachable in the enabled state).
	if !cur.Enabled {
		return fmt.Errorf("BOT protection is disabled on site %d; enable it first with 'pg addon safeline site bot enable --site-id %d --where ...'", id, id)
	}
	// The CLI writes the console's 满足条件时认证 mode; 满足条件时跳过认证
	// (negate) is not implemented and always goes out as false here.
	if !cur.Negate && sameBotPattern(cur.Pattern, pattern) {
		fmt.Printf("Site %d (%s) BOT match conditions unchanged\n", site.ID, strings.Join(site.ServerNames, ","))
		return printBotConfig(site, cur)
	}
	if err := client.SetSiteBot(id, cur.Enabled, cur.Expire, false, pattern); err != nil {
		return fmt.Errorf("updating site %d BOT match conditions: %w", id, err)
	}
	got, err := client.GetSiteBot(id)
	if err != nil {
		return fmt.Errorf("reading back site %d BOT protection: %w", id, err)
	}
	if got.Negate || !sameBotPattern(got.Pattern, pattern) {
		return fmt.Errorf("the console did not apply the requested match conditions on site %d", id)
	}
	fmt.Printf("✓ Updated SafeLine site %d (%s) BOT match conditions\n", site.ID, strings.Join(site.ServerNames, ","))
	return printBotConfig(site, got)
}
