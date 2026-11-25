package cli

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/mars-base/pgcli/internal/safeline"
)

// The console's CC protection (CC 防护) holds two per-site settings: the
// waiting room and the three rate-limit rules (req, attack, error). A site
// either carries its own rate-limit rules or follows the global set, which the
// console stores as site 0.

var addonSafelineSiteCcCmd = &cobra.Command{
	Use:   "cc",
	Short: "View and change a site's CC protection (waiting room, rate limits)",
	Long: `View and change the CC protection (CC 防护) the console shows for a
protected site: the waiting room and the rate-limit rules.

Subcommands:
  show          show both settings of one site
  waiting-room  show or change the waiting room
  rate-limit    show or change the three rate-limit rules (req, attack,
                error), or the global rules sites can share

The site id comes from 'pg addon safeline site list' (--site-id). The rules
apply while the site runs in defense mode.`,
}

var addonSafelineSiteCcShowCmd = &cobra.Command{
	Use:   "show --site-id <id>",
	Short: "Show a site's waiting room and rate-limit rules",
	Long: `Show one site's waiting-room configuration and its effective rate-limit
rules, saying whether they are the site's own or the global set it follows.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineSiteCcShow(cmd)
	},
}

var addonSafelineSiteCcWaitingRoomCmd = &cobra.Command{
	Use:   "waiting-room --site-id <id> [--enabled[=false]] [--max-concurrent N] [--session-timeout N]",
	Short: "Show or change a site's waiting room",
	Long: `Show the site's waiting room, or change it when flags are passed:

  --enabled           enable the waiting room (--enabled=false disables it)
  --max-concurrent    maximum concurrent visitors (1-5000)
  --session-timeout   minutes a queued session stays alive (1-30)

The two thresholds are a Pro feature: the Community Edition console applies
only --enabled and keeps its defaults (5 concurrent, 3 minutes). The command
re-reads the room afterwards and reports the thresholds the console did not
apply instead of pretending the change stuck.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineSiteCcWaitingRoom(cmd)
	},
}

var addonSafelineSiteCcRateLimitCmd = &cobra.Command{
	Use:   "rate-limit (--site-id <id> | --global) [--type <req|attack|error>] [flags]",
	Short: "Show or change a site's rate-limit rules (or the global rules)",
	Long: `Show a site's effective rate-limit rules, or edit them. Every site has
three built-in rules — req (requests), attack (attack hits) and error
(matching responses) — and by default follows the global rule set.

Without flags the effective rules are printed; --type narrows the output to
one rule. Rule flags:

  --type             rule to change: req, attack or error (required with the
                     rule flags below)
  --enabled          enable the rule (--enabled=false disables it)
  --period           statistics window in seconds (10-600)
  --count            requests allowed per window (at least 1)
  --action           what happens when the limit fires: block, challenge_v1
                     or challenge_v2
  --block-min        block duration in minutes (1-1440)

A site following the global rules cannot change them per site: pass --global
to edit the shared rules for every follower, or --use-global=false to give
this site its own copy first. The copy seeds all three rules from the globals
and disables them; add --type plus rule flags to enable and configure one in
the same call. A site that already has its own rules keeps them: the switch
is then a no-op and rule flags edit the rule in place. --use-global switches
a site back to the shared rules. Writing a rule with the values it already
has is a no-op.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineSiteCcRateLimit(cmd)
	},
}

func ccSiteID(cmd *cobra.Command) (int, error) {
	id, _ := cmd.Flags().GetInt("site-id")
	if id < 1 {
		return 0, fmt.Errorf("--site-id is required (list the ids with 'pg addon safeline site list')")
	}
	return id, nil
}

func waitingRoomSummary(wr *safeline.WaitingRoom) string {
	state := "disabled"
	if wr.IsEnabled {
		state = "enabled"
	}
	return fmt.Sprintf("%s (max concurrent %d, session timeout %dm)", state, wr.MaxConcurrent, wr.SessionTimeout)
}

func runAddonSafelineSiteCcShow(cmd *cobra.Command) error {
	id, err := ccSiteID(cmd)
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
	wr, err := client.GetWaitingRoom(id)
	if err != nil {
		return fmt.Errorf("site %d waiting room: %w", id, err)
	}
	rules, err := client.ListRateLimits(id)
	if err != nil {
		return fmt.Errorf("site %d rate limits: %w", id, err)
	}
	scope := "global"
	if !safeline.RateLimitsFollowGlobal(rules) {
		scope = "custom"
	}
	fmt.Printf("Site %d (%s) CC protection\n", site.ID, strings.Join(site.ServerNames, ","))
	fmt.Printf("  Waiting room:  %s\n", waitingRoomSummary(wr))
	fmt.Printf("  Rate limits:   %s (%d rules)\n", scope, len(rules))
	return printRateLimitRules(rules)
}

func runAddonSafelineSiteCcWaitingRoom(cmd *cobra.Command) error {
	id, err := ccSiteID(cmd)
	if err != nil {
		return err
	}
	fs := cmd.Flags()
	client, _, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	cur, err := client.GetWaitingRoom(id)
	if err != nil {
		return fmt.Errorf("site %d waiting room: %w", id, err)
	}
	orig := *cur
	changed := false
	if fs.Changed("enabled") {
		cur.IsEnabled, _ = fs.GetBool("enabled")
		changed = true
	}
	if fs.Changed("max-concurrent") {
		cur.MaxConcurrent, _ = fs.GetInt("max-concurrent")
		changed = true
	}
	if fs.Changed("session-timeout") {
		cur.SessionTimeout, _ = fs.GetInt("session-timeout")
		changed = true
	}
	if !changed {
		fmt.Printf("Site %d waiting room: %s\n", id, waitingRoomSummary(cur))
		return nil
	}
	if *cur == orig {
		fmt.Printf("Site %d waiting room unchanged: %s\n", id, waitingRoomSummary(cur))
		return nil
	}
	if err := safeline.ValidateWaitingRoom(cur.MaxConcurrent, cur.SessionTimeout); err != nil {
		return err
	}
	if err := client.SetWaitingRoom(id, *cur); err != nil {
		return fmt.Errorf("updating site %d waiting room: %w", id, err)
	}
	got, err := client.GetWaitingRoom(id)
	if err != nil {
		return fmt.Errorf("reading back site %d waiting room: %w", id, err)
	}
	fmt.Printf("✓ Updated SafeLine site %d waiting room: %s\n", id, waitingRoomSummary(got))
	var ignored []string
	if fs.Changed("max-concurrent") {
		want, _ := fs.GetInt("max-concurrent")
		if got.MaxConcurrent != want {
			ignored = append(ignored, fmt.Sprintf("max concurrent %d (console kept %d)", want, got.MaxConcurrent))
		}
	}
	if fs.Changed("session-timeout") {
		want, _ := fs.GetInt("session-timeout")
		if got.SessionTimeout != want {
			ignored = append(ignored, fmt.Sprintf("session timeout %dm (console kept %dm)", want, got.SessionTimeout))
		}
	}
	if len(ignored) > 0 {
		return fmt.Errorf("the console did not apply %s: waiting-room thresholds need a Pro license, the Community Edition applies only --enabled", strings.Join(ignored, " and "))
	}
	return nil
}

func runAddonSafelineSiteCcRateLimit(cmd *cobra.Command) error {
	fs := cmd.Flags()
	global, _ := fs.GetBool("global")
	id, _ := fs.GetInt("site-id")
	if global && id != 0 {
		return fmt.Errorf("--global works on the shared rules; do not combine it with --site-id")
	}
	if !global && id < 1 {
		return fmt.Errorf("--site-id is required (or pass --global to work on the shared rules)")
	}
	useGlobalChanged := fs.Changed("use-global")
	useGlobal, _ := fs.GetBool("use-global")
	ruleFlags := fs.Changed("enabled") || fs.Changed("period") || fs.Changed("count") ||
		fs.Changed("action") || fs.Changed("block-min")
	typeChanged := fs.Changed("type")
	if global && useGlobalChanged {
		return fmt.Errorf("--use-global is a per-site switch; --global already works on the shared rules")
	}
	if ruleFlags && !typeChanged {
		return fmt.Errorf("--type req|attack|error is required with --enabled/--period/--count/--action/--block-min")
	}
	if typeChanged {
		t, _ := fs.GetString("type")
		if err := safeline.ValidateRateLimitType(t); err != nil {
			return err
		}
	}
	if useGlobalChanged && useGlobal {
		if typeChanged || ruleFlags {
			return fmt.Errorf("--use-global switches the site back to the shared rules; edit them with --global instead")
		}
	}
	if useGlobalChanged && !useGlobal && typeChanged && !ruleFlags {
		return fmt.Errorf("--use-global=false only switches to custom rules (all disabled); add --enabled or another rule flag to configure one, or drop --type")
	}

	client, _, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	scopeID := id
	owner := fmt.Sprintf("site %d", id)
	where := owner
	if global {
		scopeID = 0
		owner = "the global rules"
		where = "global"
	}
	rules, err := client.ListRateLimits(scopeID)
	if err != nil {
		return fmt.Errorf("reading rate limits of %s: %w", owner, err)
	}

	viewOnly := !useGlobalChanged && !ruleFlags
	if viewOnly {
		if global {
			fmt.Printf("Global rate limits (followed by every site without custom rules):\n")
		} else {
			scope := "global"
			if !safeline.RateLimitsFollowGlobal(rules) {
				scope = "custom"
			}
			fmt.Printf("Site %d rate limits: %s\n", id, scope)
		}
		if typeChanged {
			t, _ := fs.GetString("type")
			rules = filterRateLimitRules(rules, t)
		}
		return printRateLimitRules(rules)
	}

	if useGlobalChanged && useGlobal {
		if safeline.RateLimitsFollowGlobal(rules) {
			fmt.Printf("Site %d already follows the global rate-limit rules\n", id)
			return printRateLimitRules(rules)
		}
		if err := client.UseGlobalRateLimits(id); err != nil {
			return fmt.Errorf("switching site %d to the global rules: %w", id, err)
		}
		fmt.Printf("✓ Site %d now follows the global rate-limit rules\n", id)
		updated, err := client.ListRateLimits(id)
		if err != nil {
			return fmt.Errorf("reading back site %d rate limits: %w", id, err)
		}
		return printRateLimitRules(updated)
	}
	if useGlobalChanged && !useGlobal {
		if !safeline.RateLimitsFollowGlobal(rules) {
			// The site already carries its own rules; there is nothing to seed,
			// so the rule flags (if any) edit them in place below.
			if !ruleFlags {
				fmt.Printf("Site %d already uses custom rate-limit rules\n", id)
				return printRateLimitRules(rules)
			}
		} else {
			seeded := make([]safeline.RateLimitRule, len(rules))
			for i, r := range rules {
				r.UseGlobal = false
				r.Enabled = false
				seeded[i] = r
			}
			if ruleFlags {
				t, _ := fs.GetString("type")
				idx := indexRateLimitRule(seeded, t)
				if idx < 0 {
					return fmt.Errorf("no %s rule to configure", t)
				}
				if err := applyRateLimitFlags(fs, &seeded[idx]); err != nil {
					return err
				}
			}
			if err := client.SetCustomRateLimits(id, seeded); err != nil {
				return fmt.Errorf("switching site %d to custom rate-limit rules: %w", id, err)
			}
			if ruleFlags {
				t, _ := fs.GetString("type")
				fmt.Printf("✓ Site %d now uses custom rate-limit rules; %s rule configured\n", id, t)
			} else {
				fmt.Printf("✓ Site %d now uses custom rate-limit rules (all rules disabled; enable one with --type <type> --enabled)\n", id)
			}
			updated, err := client.ListRateLimits(id)
			if err != nil {
				return fmt.Errorf("reading back site %d rate limits: %w", id, err)
			}
			return printRateLimitRules(updated)
		}
	}

	if !global && safeline.RateLimitsFollowGlobal(rules) {
		return fmt.Errorf("site %d follows the global rate-limit rules; pass --global to edit the shared rules or --use-global=false to give this site its own rules first", id)
	}
	t, _ := fs.GetString("type")
	idx := indexRateLimitRule(rules, t)
	if idx < 0 {
		return fmt.Errorf("no %s rule found in %s", t, owner)
	}
	rule := rules[idx]
	before := rule
	if err := applyRateLimitFlags(fs, &rule); err != nil {
		return err
	}
	if rule.Enabled == before.Enabled && rule.Period == before.Period && rule.Count == before.Count &&
		rule.Action == before.Action && rule.BlockMin == before.BlockMin {
		fmt.Printf("Rate-limit rule %s of %s unchanged\n", t, owner)
		return printRateLimitRules(rules)
	}
	if err := client.UpdateRateLimit(scopeID, rule); err != nil {
		return fmt.Errorf("updating the %s rule in %s: %w", t, owner, err)
	}
	fmt.Printf("✓ Updated %s rate-limit rule %s\n", where, t)
	updated, err := client.ListRateLimits(scopeID)
	if err != nil {
		return fmt.Errorf("reading back rate limits of %s: %w", owner, err)
	}
	return printRateLimitRules(updated)
}

func applyRateLimitFlags(fs *pflag.FlagSet, rule *safeline.RateLimitRule) error {
	if fs.Changed("enabled") {
		rule.Enabled, _ = fs.GetBool("enabled")
	}
	if fs.Changed("period") {
		rule.Period, _ = fs.GetInt("period")
	}
	if fs.Changed("count") {
		rule.Count, _ = fs.GetInt("count")
	}
	if fs.Changed("action") {
		rule.Action, _ = fs.GetString("action")
	}
	if fs.Changed("block-min") {
		rule.BlockMin, _ = fs.GetInt("block-min")
	}
	return safeline.ValidateRateLimitRule(*rule)
}

func indexRateLimitRule(rules []safeline.RateLimitRule, typ string) int {
	for i, r := range rules {
		if r.Type == typ {
			return i
		}
	}
	return -1
}

func filterRateLimitRules(rules []safeline.RateLimitRule, typ string) []safeline.RateLimitRule {
	out := make([]safeline.RateLimitRule, 0, 1)
	for _, r := range rules {
		if r.Type == typ {
			out = append(out, r)
		}
	}
	return out
}

func printRateLimitRules(rules []safeline.RateLimitRule) error {
	order := map[string]int{
		safeline.RateLimitReq:    0,
		safeline.RateLimitAttack: 1,
		safeline.RateLimitError:  2,
	}
	sorted := slices.Clone(rules)
	sort.SliceStable(sorted, func(i, j int) bool {
		oi, iok := order[sorted[i].Type]
		oj, jok := order[sorted[j].Type]
		if iok && jok {
			return oi < oj
		}
		return iok && !jok
	})
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "TYPE\tENABLED\tPERIOD\tCOUNT\tACTION\tBLOCK\tCONDITIONS")
	for _, r := range sorted {
		enabled := "no"
		if r.Enabled {
			enabled = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%ds\t%d\t%s\t%dm\t%s\n",
			r.Type, enabled, r.Period, r.Count, r.Action, r.BlockMin, rateLimitConditions(r))
	}
	return w.Flush()
}

func rateLimitConditions(r safeline.RateLimitRule) string {
	if len(r.Conditions) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(r.Conditions))
	for _, c := range r.Conditions {
		field := c.Condition.Field
		if field == "" {
			field = c.Strategy
		}
		parts = append(parts, fmt.Sprintf("%s %s %s", field, c.Condition.Operator, strings.Join(c.Condition.Value, ",")))
	}
	return strings.Join(parts, "; ")
}
