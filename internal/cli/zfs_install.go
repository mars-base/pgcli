package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/zfs"
)

var zfsInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install ZFS packages for this Linux distribution",
	Long: `Detect the distribution, enable the archive component that carries ZFS,
install the matching packages, load the kernel module and enable boot services.

There are no installation flags by design. Supported systems:
Debian 12/13, Ubuntu 24.04/26.04, Fedora 44, Rocky Linux 10.

The command is idempotent: a healthy install is left untouched. It requires
root or passwordless sudo and never prompts for a sudo password.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := newZFSRunner(cmd)
		if err != nil {
			return err
		}
		return zfs.Install(zfsContext(cmd), r)
	},
}

var zfsStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show ZFS packages, kernel module and pools",
	Long: `Show the detected distribution, ZFS userland and kernel module versions,
privilege state and a pool summary. This is read-only and also works when ZFS
is not installed yet.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := zfsContext(cmd)
		r := zfs.NewReadOnlyRunner(ctx)
		status := zfs.LoadStatus(ctx, r)
		printZFSStatus(status)
		return nil
	},
}

func init() {
	zfsCmd.AddCommand(zfsInstallCmd, zfsStatusCmd)
}

func printZFSStatus(status zfs.SystemStatus) {
	if status.DistroErr != nil {
		fmt.Printf("OS:            unknown (%v)\n", status.DistroErr)
	} else {
		fmt.Printf("OS:            %s\n", status.Distro.Display())
	}
	fmt.Printf("Family:        %s\n", status.Distro.Family)

	health := status.Health
	switch {
	case !health.Installed():
		fmt.Println("ZFS:           not installed (run \"pg zfs install\")")
	default:
		fmt.Println("ZFS:           installed")
		fmt.Printf("Userland:      %s\n", zfsValueOr(health.UserlandVersion, "unknown"))
		if health.ModuleLoaded {
			fmt.Printf("Kernel module: %s (loaded)\n", zfsValueOr(health.ModuleVersion, "unknown"))
			if health.VersionMismatch() {
				fmt.Println("  [!] Userland and kernel module versions differ")
			}
		} else {
			fmt.Println("Kernel module: not loaded (run \"pg zfs install\")")
		}
	}
	fmt.Printf("Kernel:        %s\n", zfsValueOr(health.KernelRelease, "unknown"))

	switch {
	case status.Root:
		fmt.Println("Privilege:     root")
	case status.CanPrivilege:
		fmt.Println("Privilege:     passwordless sudo")
	default:
		fmt.Println("Privilege:     non-root (pool operations need root or passwordless sudo)")
	}

	if health.Installed() {
		switch {
		case status.PoolErr != nil:
			fmt.Printf("Pools:         unavailable (%v)\n", status.PoolErr)
		case len(status.Pools) == 0:
			fmt.Println("Pools:         none")
		default:
			parts := make([]string, 0, len(status.Pools))
			for _, pool := range status.Pools {
				parts = append(parts, fmt.Sprintf("%s (%s)", pool.Name, pool.Health))
			}
			fmt.Printf("Pools:         %s\n", strings.Join(parts, ", "))
		}
	}

	if status.SecureBoot {
		fmt.Println("Secure Boot:   enabled")
		fmt.Println("  [!] DKMS-built modules must be signed; use your distro's MOK enrollment flow if loading fails")
	} else {
		fmt.Println("Secure Boot:   disabled")
	}
}

func zfsValueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
