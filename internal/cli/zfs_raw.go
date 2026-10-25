package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/zfs"
)

var zfsRawCmd = &cobra.Command{
	Use:   "raw",
	Short: "Pass arguments directly to zpool or zfs",
	Long: `Pass arguments directly to zpool or zfs.

WARNING: raw commands bypass device safety checks and confirmation prompts.
They are executed with root privileges (directly or via sudo -n). Native
options such as -f, -o and -O are available only here.

Examples:
  pg zfs raw zpool status -P
  pg zfs raw zpool labelclear -f /dev/vdb
  pg zfs raw zpool attach -o ashift=12 tank /dev/vdb /dev/vdd
  pg zfs raw zfs list -o name,quota,used tank`,
}

func newZFSRawCmd(tool string) *cobra.Command {
	return &cobra.Command{
		Use:                tool + " ARGS...",
		Short:              "Run " + tool + " with the given arguments",
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireZFSInstalled(); err != nil {
				return err
			}
			binary, err := zfs.LookPath(tool)
			if err != nil {
				return err
			}
			r, err := newZFSRunner(cmd)
			if err != nil {
				return err
			}
			if err := r.RunPrivileged(zfsContext(cmd), binary, args...); err != nil {
				return fmt.Errorf("%s %s failed: %w", tool, zfs.ShellJoin(args), err)
			}
			return nil
		},
	}
}

func init() {
	zfsRawCmd.AddCommand(newZFSRawCmd("zpool"), newZFSRawCmd("zfs"))
	zfsCmd.AddCommand(zfsRawCmd)
}
