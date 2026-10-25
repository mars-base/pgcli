package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/zfs"
)

var zfsCmd = &cobra.Command{
	Use:   "zfs",
	Short: "Install and manage ZFS pools, datasets and snapshots",
	Long: `Install ZFS and manage storage pools, datasets and snapshots.

ZFS is Linux-only. The command family is intentionally stateless: it talks
directly to the kernel and does not read or write pg.yaml.

Common workflow:
  pg zfs install
  pg zfs disk list
  pg zfs pool create tank /dev/vdb /dev/vdc
  pg zfs dataset create tank/backups
  pg zfs snapshot create tank/backups@before-upgrade

Use "pg zfs raw zpool ..." or "pg zfs raw zfs ..." for options that are not
wrapped. Raw commands bypass safety checks and confirmation prompts.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if platform.Detect() != platform.Linux {
			return fmt.Errorf("pg zfs requires Linux")
		}
		if cmd.Root().PersistentFlags().Changed("output") {
			return fmt.Errorf("pg zfs does not use --output: use --prop/-p for pool properties and --fs-prop/-P for root dataset properties")
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(zfsCmd)
}

func zfsContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

func newZFSRunner(cmd *cobra.Command) (*zfs.Runner, error) {
	return zfs.NewRunner(zfsContext(cmd))
}

func requireZFSInstalled() error {
	return zfs.EnsureZFSBinaries()
}

// loadDeviceInventory loads lsblk and, best-effort, existing pool usage.
func loadDeviceInventory(ctx context.Context, r *zfs.Runner) (*zfs.DeviceSet, map[string][]string, error) {
	devices, err := zfs.LoadBlockDevices(ctx, r)
	if err != nil {
		return nil, nil, err
	}
	usage, _ := zfs.LoadZFSUsage(ctx, r)
	return zfs.NewDeviceSet(devices), usage, nil
}

func confirmZFS(force bool, prompt string) bool {
	if force {
		return true
	}
	return confirmPrompt(prompt)
}

func printZFSCancelled() {
	fmt.Println("Cancelled.")
}

func printZFSWarnings(warnings []string) {
	for _, warning := range warnings {
		fmt.Printf("  [!] %s\n", warning)
	}
}

func zfsFlagStringArray(cmd *cobra.Command, name string) []string {
	values, _ := cmd.Flags().GetStringArray(name)
	return values
}

func zfsPropSummary(props map[string]string) string {
	keys := zfs.SortedProps(props)
	if len(keys) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+props[key])
	}
	return strings.Join(parts, " ")
}
