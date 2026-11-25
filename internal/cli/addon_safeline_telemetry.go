package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/safeline"
)

// The console's 通用设置 → 威胁情报共享计划 reports what this instance detects
// back to Chaitin's threat-intelligence servers. None of the switches gate a
// protection feature, so a private deployment can leave them all off.

var addonSafelineTelemetryCmd = &cobra.Command{
	Use:   "telemetry",
	Short: "View and switch off the console's threat-intelligence sharing",
	Long: `View and switch the console's threat-intelligence sharing (通用设置 →
威胁情报共享计划): the switches that report what this instance detects back to
Chaitin's threat-intelligence servers.

  malicious-ips  IP 情报共享计划: malicious IPs detected by this instance
  threat-intel   共享威胁情报
  fingerprint    浏览器指纹信息共享计划

Subcommands:
  show     show the three switches
  disable  switch every sharing switch off
  enable   switch every sharing switch on

Switches already in the requested state are not written.`,
}

var addonSafelineTelemetryShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the threat-intelligence sharing switches",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineTelemetryShow(cmd)
	},
}

var addonSafelineTelemetryDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Switch threat-intelligence sharing off",
	Long: `Switch all three sharing switches off: the instance stops reporting
malicious IPs, threat intelligence and browser fingerprints to Chaitin. The
detection modules keep working; the malicious-IP switch additionally gates the
malicious-IP intelligence groups used in rules.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineTelemetrySet(cmd, false)
	},
}

var addonSafelineTelemetryEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Switch threat-intelligence sharing on",
	Long: `Switch all three sharing switches on again, undoing 'telemetry
disable'.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonSafelineTelemetrySet(cmd, true)
	},
}

func printTelemetry(t *safeline.TelemetryState) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "SWITCH\tSTATE\tCONSOLE SETTING")
	fmt.Fprintf(w, "malicious-ips\t%s\tIP 情报共享计划（共享恶意 IP 信息）\n", onOff(t.ShareAttackIPs))
	fmt.Fprintf(w, "threat-intel\t%s\t共享威胁情报\n", onOff(t.ShareThreatIntel))
	fmt.Fprintf(w, "fingerprint\t%s\t浏览器指纹信息共享计划\n", onOff(t.ShareFingerprint))
	return w.Flush()
}

func runAddonSafelineTelemetryShow(cmd *cobra.Command) error {
	client, sc, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	t, err := client.GetTelemetry()
	if err != nil {
		return fmt.Errorf("reading threat-intelligence sharing: %w", err)
	}
	fmt.Printf("Threat-intelligence sharing on %q:\n", sc.Name)
	return printTelemetry(t)
}

func runAddonSafelineTelemetrySet(cmd *cobra.Command, on bool) error {
	client, sc, err := resolveSafelineConsole(cmd)
	if err != nil {
		return err
	}
	cur, err := client.GetTelemetry()
	if err != nil {
		return fmt.Errorf("reading threat-intelligence sharing: %w", err)
	}
	want := safeline.TelemetryState{ShareAttackIPs: on, ShareThreatIntel: on, ShareFingerprint: on}
	changed, err := client.SetTelemetry(*cur, want)
	if err != nil {
		return fmt.Errorf("switching threat-intelligence sharing: %w", err)
	}
	verb := "off"
	if on {
		verb = "on"
	}
	if len(changed) == 0 {
		fmt.Printf("Threat-intelligence sharing already %s on %q\n", verb, sc.Name)
		return printTelemetry(cur)
	}
	got, err := client.GetTelemetry()
	if err != nil {
		return fmt.Errorf("reading back threat-intelligence sharing: %w", err)
	}
	if miss := safeline.TelemetryMismatch(*got, on); len(miss) > 0 {
		kept := "on"
		if on {
			kept = "off"
		}
		return fmt.Errorf("the console kept %s %s", strings.Join(miss, ", "), kept)
	}
	fmt.Printf("✓ Turned %s threat-intelligence sharing on %q\n", verb, sc.Name)
	return printTelemetry(got)
}
