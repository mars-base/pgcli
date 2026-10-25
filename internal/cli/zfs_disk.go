package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/zfs"
)

var zfsDiskCmd = &cobra.Command{
	Use:   "disk",
	Short: "Inspect host disks",
	Long:  `Inspect whole disks, their mount state, ZFS ownership and whether they are safe for a new pool.`,
}

var zfsDiskListCmd = &cobra.Command{
	Use:   "list",
	Short: "List disks and whether they are safe for a new pool",
	Long: `List whole disks by default. Partitions, loop devices and disk members are
hidden unless --all is given.

SAFE=no means "pg zfs pool create" will refuse the device. The NOTE column
explains why. This command is read-only and works without root; ZFS ownership
requires root or passwordless sudo.

Examples:
  pg zfs disk list
  pg zfs disk list --all`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := zfsContext(cmd)
		r := zfs.NewReadOnlyRunner(ctx)
		devices, err := zfs.LoadBlockDevices(ctx, r)
		if err != nil {
			return err
		}
		usage, usageErr := zfs.LoadZFSUsage(ctx, r)
		set := zfs.NewDeviceSet(devices)
		all, _ := cmd.Flags().GetBool("all")

		writer := tabwriter.NewWriter(os.Stdout, 2, 8, 2, ' ', 0)
		fmt.Fprintln(writer, "DEVICE\tSIZE\tMODEL\tSERIAL\tMOUNT\tZFS\tSAFE\tNOTE")
		rows := 0
		for i := range set.Devices {
			dev := &set.Devices[i]
			if !all && dev.Type != "disk" {
				continue
			}
			check := set.Inspect(dev, usage)
			safe := "yes"
			note := "-"
			if !check.Safe {
				safe = "no"
				note = check.Reason
			}
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				dev.Path,
				zfs.FormatBytes(dev.Size),
				zfsValueOr(strings.TrimSpace(dev.Model), "-"),
				zfsValueOr(strings.TrimSpace(dev.Serial), "-"),
				zfsValueOr(strings.Join(check.Mountpoints, ","), "-"),
				zfsValueOr(strings.Join(check.UsedPools, ","), "-"),
				safe,
				note,
			)
			rows++
		}
		if err := writer.Flush(); err != nil {
			return err
		}
		if rows == 0 {
			fmt.Println("No disks found.")
		}
		if usageErr != nil {
			fmt.Printf("\n  [!] ZFS ownership unavailable: %v\n", usageErr)
		}
		return nil
	},
}

func init() {
	zfsDiskListCmd.Flags().BoolP("all", "a", false, "include partitions and other block devices")
	zfsDiskCmd.AddCommand(zfsDiskListCmd)
	zfsCmd.AddCommand(zfsDiskCmd)
}
