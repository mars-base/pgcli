package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/safeline"
)

// The console's attack protection (攻击防护) runs 14 semantic-analysis
// detection modules. Each module has a level; a site follows the global
// levels or carries its own copy, like the CC rate-limit rules.

var addonSafelineSiteProtectCmd = &cobra.Command{
	Use:   "protect",
	Short: "View and change attack protection (semantic analysis modules)",
	Long: `View and change the semantic-analysis detection modules behind the
console's attack protection (攻击防护): 14 modules (SQL injection, XSS, command
injection, …), each running at one of four levels:

  disable   do not detect (禁用)
  observe   log risky requests only (仅观察)
  balance   block obvious attacks, low false positives (平衡防护, the default)
  strict    block more requests, higher false positives (高强度防护)

Subcommands:
  show        show the global levels, or one site's effective levels
  mode        set every module to one level in one call, globally or for one
              site
  use-global  switch a site back to the global levels

A site follows the global levels until 'mode --site-id' gives it its own copy.
The changes publish to the detection engine immediately.`,
}

var addonSafelineSiteProtectShowCmd = &cobra.Command{
	Use:   "show [--site-id <id>]",
	Short: "Show global or per-site semantic analysis levels",
	Long: `Show the effective levels of the 14 semantic-analysis detection
modules: the global configuration without --site-id, or one site's effective
levels (its own copy or the global ones it follows) with it.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineSiteProtectShow(cmd)
	},
}

var addonSafelineSiteProtectModeCmd = &cobra.Command{
	Use:   "mode <disable|observe|balance|strict> [--site-id <id>]",
	Short: "Set every semantic analysis module to one level",
	Long: `Set all 14 semantic-analysis detection modules to one level in a
single call — the CLI equivalent of the console page's batch button.

Without --site-id the global configuration changes, which every site without
its own copy follows. With --site-id the site gets its own copy, seeded from
the levels it currently runs, with every module set to the new level; switch
it back with 'pg addon safeline site protect use-global --site-id <id>'.`,
	Args: cobra.ExactArgs(1),
	RunE: runAddonSafelineSiteProtectMode,
}

var addonSafelineSiteProtectUseGlobalCmd = &cobra.Command{
	Use:   "use-global --site-id <id>",
	Short: "Make a site follow the global semantic analysis levels",
	Long: `Make one site follow the global semantic-analysis levels again,
dropping the site's own copy if it has one.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineSiteProtectUseGlobal(cmd)
	},
}

func protectSiteID(cmd *cobra.Command) int {
	id, _ := cmd.Flags().GetInt("site-id")
	return id
}

func semanticsSummary(levels map[string]string) string {
	first := ""
	for _, m := range safeline.SemanticsModules {
		lv := levels[m]
		if first == "" {
			first = lv
		} else if lv != first {
			return "custom per module"
		}
	}
	if first == "" {
		return "not configured"
	}
	return fmt.Sprintf("all %d modules at %s", len(safeline.SemanticsModules), safeline.SemanticsLevelName(first))
}

func printSemanticsLevels(levels map[string]string) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "MODULE\tLEVEL")
	for _, m := range safeline.SemanticsModules {
		fmt.Fprintf(w, "%s\t%s\n", m, safeline.SemanticsLevelName(levels[m]))
	}
	return w.Flush()
}

func runAddonSafelineSiteProtectShow(cmd *cobra.Command) error {
	client, _, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	global, err := client.GetGlobalSemantics()
	if err != nil {
		return fmt.Errorf("reading global attack protection: %w", err)
	}
	id := protectSiteID(cmd)
	if id == 0 {
		fmt.Printf("Global attack protection (semantic analysis): %s\n", semanticsSummary(global))
		return printSemanticsLevels(global)
	}
	site, err := client.GetSite(id)
	if err != nil {
		return fmt.Errorf("site %d: %w", id, err)
	}
	own, err := client.GetSiteSemantics(id)
	if err != nil {
		return fmt.Errorf("site %d semantics: %w", id, err)
	}
	scope := "global"
	levels := global
	if own != nil && len(own.Semantics) > 0 {
		scope = "custom"
		levels = own.Semantics
	}
	fmt.Printf("Site %d (%s) attack protection: %s (%s)\n", site.ID, strings.Join(site.ServerNames, ","), scope, semanticsSummary(levels))
	return printSemanticsLevels(levels)
}

func runAddonSafelineSiteProtectMode(cmd *cobra.Command, args []string) error {
	level, err := safeline.ParseSemanticsMode(args[0])
	if err != nil {
		return err
	}
	client, _, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	id := protectSiteID(cmd)
	if id == 0 {
		cur, err := client.GetGlobalSemantics()
		if err != nil {
			return fmt.Errorf("reading global attack protection: %w", err)
		}
		if len(safeline.SemanticsMismatch(cur, level)) == 0 {
			fmt.Printf("Global attack protection unchanged: %s\n", semanticsSummary(cur))
			return printSemanticsLevels(cur)
		}
		if err := client.SetGlobalSemantics(safeline.BatchSemantics(cur, level)); err != nil {
			return fmt.Errorf("updating global attack protection: %w", err)
		}
		got, err := client.GetGlobalSemantics()
		if err != nil {
			return fmt.Errorf("reading back global attack protection: %w", err)
		}
		if miss := safeline.SemanticsMismatch(got, level); len(miss) > 0 {
			return fmt.Errorf("the console kept the old levels of %s", strings.Join(miss, ", "))
		}
		fmt.Printf("✓ Updated global attack protection: %s\n", semanticsSummary(got))
		return printSemanticsLevels(got)
	}
	site, err := client.GetSite(id)
	if err != nil {
		return fmt.Errorf("site %d: %w", id, err)
	}
	seed, err := client.GetGlobalSemantics()
	if err != nil {
		return fmt.Errorf("reading global attack protection: %w", err)
	}
	own, err := client.GetSiteSemantics(id)
	if err != nil {
		return fmt.Errorf("site %d semantics: %w", id, err)
	}
	if own != nil && len(own.Semantics) > 0 {
		seed = own.Semantics
		if len(safeline.SemanticsMismatch(own.Semantics, level)) == 0 {
			fmt.Printf("Site %d (%s) attack protection unchanged: %s\n", site.ID, strings.Join(site.ServerNames, ","), semanticsSummary(own.Semantics))
			return printSemanticsLevels(own.Semantics)
		}
	}
	if err := client.SetSiteSemantics(id, false, safeline.BatchSemantics(seed, level)); err != nil {
		return fmt.Errorf("updating site %d attack protection: %w", id, err)
	}
	got, err := client.GetSiteSemantics(id)
	if err != nil {
		return fmt.Errorf("reading back site %d semantics: %w", id, err)
	}
	if got == nil || len(got.Semantics) == 0 {
		return fmt.Errorf("the console did not give site %d its own semantics", id)
	}
	if miss := safeline.SemanticsMismatch(got.Semantics, level); len(miss) > 0 {
		return fmt.Errorf("the console kept the old levels of %s", strings.Join(miss, ", "))
	}
	fmt.Printf("✓ Site %d (%s) now has its own attack protection levels: %s\n", site.ID, strings.Join(site.ServerNames, ","), semanticsSummary(got.Semantics))
	return printSemanticsLevels(got.Semantics)
}

func runAddonSafelineSiteProtectUseGlobal(cmd *cobra.Command) error {
	id := protectSiteID(cmd)
	if id < 1 {
		return fmt.Errorf("--site-id is required (list the ids with 'pg addon safeline site list')")
	}
	client, _, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	if _, err := client.GetSite(id); err != nil {
		return fmt.Errorf("site %d: %w", id, err)
	}
	global, err := client.GetGlobalSemantics()
	if err != nil {
		return fmt.Errorf("reading global attack protection: %w", err)
	}
	own, err := client.GetSiteSemantics(id)
	if err != nil {
		return fmt.Errorf("site %d semantics: %w", id, err)
	}
	if own == nil || len(own.Semantics) == 0 {
		fmt.Printf("Site %d already follows the global attack protection levels\n", id)
		return nil
	}
	if err := client.SetSiteSemantics(id, true, global); err != nil {
		return fmt.Errorf("switching site %d to the global levels: %w", id, err)
	}
	own, err = client.GetSiteSemantics(id)
	if err != nil {
		return fmt.Errorf("reading back site %d semantics: %w", id, err)
	}
	if own != nil && len(own.Semantics) > 0 {
		return fmt.Errorf("the console kept site %d's own semantics", id)
	}
	fmt.Printf("✓ Site %d now follows the global attack protection levels\n", id)
	return nil
}
