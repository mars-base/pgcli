package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/zfs"
)

var zfsPoolCmd = &cobra.Command{
	Use:   "pool",
	Short: "Create and manage ZFS storage pools",
	Long: `Create and manage ZFS storage pools.

Positional devices select a layout automatically: 1 disk is a stripe,
2 disks a mirror, 3 disks raidz1 and 4 or more disks raidz2. Use --type
to override the choice, or --data to build multiple vdevs explicitly.`,
}

var zfsPoolCreateCmd = &cobra.Command{
	Use:   "create NAME DEVICE...",
	Short: "Create a pool",
	Long: `Create a pool. Devices must be whole disks under /dev without partitions,
filesystems or other pool membership.

Layout is chosen from the positional device count:
  1 disk    stripe (no redundancy; extra warning)
  2 disks   mirror
  3 disks   raidz1
  4+ disks  raidz2

Defaults: ashift=12, autoexpand=on, autotrim=on for non-rotational disks with
discard support, compression=zstd, atime=off, xattr=sa, acltype=posixacl and
dnodesize=auto on the root dataset. Override with --prop / --fs-prop, or use
--no-defaults.

Examples:
  pg zfs pool create tank /dev/vdb /dev/vdc
  pg zfs pool create tank /dev/vdb /dev/vdc /dev/vdd /dev/vde
  pg zfs pool create tank --type raidz3 /dev/vdb /dev/vdc /dev/vdd /dev/vde
  pg zfs pool create fast --type mirror --data /dev/vdb,/dev/vdc --data /dev/vdd,/dev/vde
  pg zfs pool create tank /dev/vdb /dev/vdc --prop ashift=13 --fs-prop compression=lz4`,
	Args: cobra.MinimumNArgs(1),
	RunE: runZFSPoolCreate,
}

var zfsPoolListCmd = &cobra.Command{
	Use:   "list",
	Short: "List pools",
	Long: `List pools with size, allocation, health and alternate root.

Examples:
  pg zfs pool list`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, r, _, err := zfsPoolTools(cmd)
		if err != nil {
			return err
		}
		pools, err := zfs.ListPools(ctx, r)
		if err != nil {
			return err
		}
		if len(pools) == 0 {
			fmt.Println("No pools.")
			return nil
		}
		writer := tabwriter.NewWriter(os.Stdout, 2, 8, 2, ' ', 0)
		fmt.Fprintln(writer, "POOL\tSIZE\tALLOC\tFREE\tHEALTH\tALTROOT")
		for _, pool := range pools {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
				pool.Name,
				zfs.FormatBytes(pool.Size),
				zfs.FormatBytes(pool.Alloc),
				zfs.FormatBytes(pool.Free),
				pool.Health,
				zfsValueOr(pool.AltRoot, "-"),
			)
		}
		return writer.Flush()
	},
}

var zfsPoolStatusCmd = &cobra.Command{
	Use:   "status [NAME]",
	Short: "Show detailed pool status",
	Long: `Show zpool status output. Without a name all pools are shown; with --brief
only pools with problems are printed (zpool status -x).

Examples:
  pg zfs pool status
  pg zfs pool status tank
  pg zfs pool status --brief`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, r, zpool, err := zfsPoolTools(cmd)
		if err != nil {
			return err
		}
		brief, _ := cmd.Flags().GetBool("brief")
		zpoolArgs := []string{"status"}
		if brief {
			zpoolArgs = append(zpoolArgs, "-x")
		}
		if len(args) == 1 {
			name, err := zfsName(args[0])
			if err != nil {
				return err
			}
			zpoolArgs = append(zpoolArgs, name)
		}
		out, err := r.OutputPrivileged(ctx, zpool, zpoolArgs...)
		if err != nil {
			return err
		}
		fmt.Print(string(out))
		return nil
	},
}

var zfsPoolAddCmd = &cobra.Command{
	Use:   "add NAME DEVICE...",
	Short: "Add vdevs to a pool",
	Long: `Add data, special, log, cache or spare vdevs to an existing pool.

With --class data (the default), the layout is chosen from the device count
like "pool create", or set explicitly with --type. --class special also
requires --type. Log devices are mirrored automatically; cache and spare
devices take no --type.

Examples:
  pg zfs pool add tank /dev/vdd /dev/vde
  pg zfs pool add tank --type mirror /dev/vdd,/dev/vde /dev/vdf,/dev/vdg
  pg zfs pool add tank --class special --type mirror /dev/vdd /dev/vde
  pg zfs pool add tank --class cache /dev/vdd`,
	Args: cobra.MinimumNArgs(2),
	RunE: runZFSPoolAdd,
}

var zfsPoolAttachCmd = &cobra.Command{
	Use:   "attach NAME DEVICE NEWDEVICE",
	Short: "Attach a disk to a mirror vdev",
	Long: `Attach NEWDEVICE to DEVICE, turning a two-way mirror into a three-way
mirror (or extending an existing mirror). The new disk must pass the same
safety checks as "pool create". raidz expansion needs ZFS 2.3+ and is left to
"pg zfs raw zpool attach".

Examples:
  pg zfs pool attach tank /dev/vdb /dev/vdd`,
	Args: cobra.ExactArgs(3),
	RunE: runZFSPoolAttach,
}

var zfsPoolDetachCmd = &cobra.Command{
	Use:   "detach NAME DEVICE",
	Short: "Detach a disk from a mirror vdev",
	Long: `Detach DEVICE from its mirror. ZFS refuses to detach the last healthy device
of a mirror.

Examples:
  pg zfs pool detach tank /dev/vdd`,
	Args: cobra.ExactArgs(2),
	RunE: runZFSPoolDetach,
}

var zfsPoolReplaceCmd = &cobra.Command{
	Use:   "replace NAME OLDDEVICE NEWDEVICE",
	Short: "Replace a disk in a pool",
	Long: `Replace OLDDEVICE with NEWDEVICE. The new disk must pass the same safety
checks as "pool create". A resilver starts immediately.

Examples:
  pg zfs pool replace tank /dev/vdb /dev/vdd`,
	Args: cobra.ExactArgs(3),
	RunE: runZFSPoolReplace,
}

var zfsPoolOfflineCmd = &cobra.Command{
	Use:   "offline NAME DEVICE",
	Short: "Take a disk offline",
	Long: `Take DEVICE offline so the pool stops using it. Use this before physically
removing a disk from a redundant vdev.

Examples:
  pg zfs pool offline tank /dev/vdb`,
	Args: cobra.ExactArgs(2),
	RunE: runZFSPoolOffline,
}

var zfsPoolOnlineCmd = &cobra.Command{
	Use:   "online NAME DEVICE",
	Short: "Bring a disk online",
	Long: `Bring an offline device back online and start a resilver if needed.

Examples:
  pg zfs pool online tank /dev/vdb`,
	Args: cobra.ExactArgs(2),
	RunE: runZFSPoolOnline,
}

var zfsPoolExportCmd = &cobra.Command{
	Use:   "export NAME",
	Short: "Export a pool",
	Long: `Export a pool so it can be imported on another system. The pool is cleanly
unmounted first.

Examples:
  pg zfs pool export tank`,
	Args: cobra.ExactArgs(1),
	RunE: runZFSPoolExport,
}

var zfsPoolImportCmd = &cobra.Command{
	Use:   "import [NAME]",
	Short: "List importable pools or import one",
	Long: `Without a name, list importable pools (zpool import). With a name, import
that pool. Use --dir when the pool lives on disks outside the default scan
paths, --no-mount to skip mounting datasets, and --altroot to mount datasets
under an alternate root.

Examples:
  pg zfs pool import
  pg zfs pool import tank
  pg zfs pool import tank --dir /dev/disk/by-id
  pg zfs pool import tank --no-mount --altroot /mnt`,
	Args: cobra.MaximumNArgs(1),
	RunE: runZFSPoolImport,
}

var zfsPoolScrubCmd = &cobra.Command{
	Use:   "scrub NAME",
	Short: "Start or stop a scrub",
	Long: `Start a scrub to verify data and repair it from redundancy. Use --stop to
cancel a running scrub.

Examples:
  pg zfs pool scrub tank
  pg zfs pool scrub tank --stop`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, r, zpool, err := zfsPoolTools(cmd)
		if err != nil {
			return err
		}
		name, err := zfsName(args[0])
		if err != nil {
			return err
		}
		stop, _ := cmd.Flags().GetBool("stop")
		zpoolArgs := []string{"scrub"}
		if stop {
			zpoolArgs = append(zpoolArgs, "-s")
		}
		zpoolArgs = append(zpoolArgs, name)
		if err := r.RunPrivileged(ctx, zpool, zpoolArgs...); err != nil {
			return err
		}
		if stop {
			fmt.Printf("Stopped scrub on pool %q.\n", name)
		} else {
			fmt.Printf("Scrub started on pool %q. Follow it with \"pg zfs pool status %s\".\n", name, name)
		}
		return nil
	},
}

var zfsPoolDestroyCmd = &cobra.Command{
	Use:   "destroy NAME",
	Short: "Destroy a pool and all its data",
	Long: `Destroy a pool, its datasets and all data on its disks. This is permanent
and requires confirmation. --force only skips the prompt; it is never passed
to ZFS as -f.

Examples:
  pg zfs pool destroy tank
  pg zfs pool destroy tank --force`,
	Args: cobra.ExactArgs(1),
	RunE: runZFSPoolDestroy,
}

func init() {
	zfsPoolCreateCmd.Flags().StringP("type", "t", "", "layout: stripe, mirror, raidz1, raidz2, raidz3 (aliases raid0/raid1/raid2/raid3)")
	zfsPoolCreateCmd.Flags().StringArray("data", nil, "comma-separated data vdev group; repeat for multiple vdevs (requires --type)")
	zfsPoolCreateCmd.Flags().StringArray("special", nil, "comma-separated special vdev group (requires --type)")
	zfsPoolCreateCmd.Flags().StringArray("log", nil, "comma-separated log (SLOG) devices")
	zfsPoolCreateCmd.Flags().StringArray("cache", nil, "comma-separated cache (L2ARC) devices")
	zfsPoolCreateCmd.Flags().StringArray("spare", nil, "comma-separated hot spare devices")
	zfsPoolCreateCmd.Flags().StringArrayP("prop", "p", nil, "pool property KEY=VALUE; repeatable")
	zfsPoolCreateCmd.Flags().StringArrayP("fs-prop", "P", nil, "root dataset property KEY=VALUE; repeatable")
	zfsPoolCreateCmd.Flags().Bool("no-defaults", false, "do not apply pgcli default properties")
	addZFSForceFlag(zfsPoolCreateCmd)

	zfsPoolStatusCmd.Flags().Bool("brief", false, "only show pools with problems")

	zfsPoolAddCmd.Flags().String("class", "data", "vdev class: data, special, log, cache, spare")
	zfsPoolAddCmd.Flags().StringP("type", "t", "", "layout for data or special vdevs")
	addZFSForceFlag(zfsPoolAddCmd)

	addZFSForceFlag(zfsPoolAttachCmd)
	addZFSForceFlag(zfsPoolDetachCmd)
	addZFSForceFlag(zfsPoolReplaceCmd)
	addZFSForceFlag(zfsPoolOfflineCmd)
	addZFSForceFlag(zfsPoolExportCmd)
	addZFSForceFlag(zfsPoolDestroyCmd)

	zfsPoolImportCmd.Flags().String("dir", "", "search for pools in this directory")
	zfsPoolImportCmd.Flags().Bool("no-mount", false, "do not mount datasets after importing")
	zfsPoolImportCmd.Flags().String("altroot", "", "use an alternate root for imported datasets")
	addZFSForceFlag(zfsPoolImportCmd)

	zfsPoolScrubCmd.Flags().Bool("stop", false, "stop a running scrub")

	zfsPoolCmd.AddCommand(
		zfsPoolCreateCmd,
		zfsPoolListCmd,
		zfsPoolStatusCmd,
		zfsPoolAddCmd,
		zfsPoolAttachCmd,
		zfsPoolDetachCmd,
		zfsPoolReplaceCmd,
		zfsPoolOfflineCmd,
		zfsPoolOnlineCmd,
		zfsPoolExportCmd,
		zfsPoolImportCmd,
		zfsPoolScrubCmd,
		zfsPoolDestroyCmd,
	)
	zfsCmd.AddCommand(zfsPoolCmd)
}

func addZFSForceFlag(cmd *cobra.Command) {
	cmd.Flags().Bool("force", false, "skip the confirmation prompt (never passed to ZFS as -f)")
}

func zfsForce(cmd *cobra.Command) bool {
	force, _ := cmd.Flags().GetBool("force")
	return force
}

func zfsName(value string) (string, error) {
	if err := zfs.ValidatePoolName(value); err != nil {
		return "", err
	}
	return value, nil
}

func zfsPoolTools(cmd *cobra.Command) (context.Context, *zfs.Runner, string, error) {
	if err := requireZFSInstalled(); err != nil {
		return nil, nil, "", err
	}
	zpool, err := zfs.LookPath("zpool")
	if err != nil {
		return nil, nil, "", err
	}
	r, err := newZFSRunner(cmd)
	if err != nil {
		return nil, nil, "", err
	}
	return zfsContext(cmd), r, zpool, nil
}

func runZFSPoolCreate(cmd *cobra.Command, args []string) error {
	name, err := zfsName(args[0])
	if err != nil {
		return err
	}
	poolType, typeExplicit, err := zfsPoolTypeFlag(cmd)
	if err != nil {
		return err
	}
	topo, warnings, err := zfs.BuildTopology(args[1:], zfs.BuildOptions{
		Type:         poolType,
		TypeExplicit: typeExplicit,
		Data:         zfsFlagStringArray(cmd, "data"),
		Special:      zfsFlagStringArray(cmd, "special"),
		Log:          zfsFlagStringArray(cmd, "log"),
		Cache:        zfsFlagStringArray(cmd, "cache"),
		Spare:        zfsFlagStringArray(cmd, "spare"),
	})
	if err != nil {
		return err
	}
	ctx, r, zpool, err := zfsPoolTools(cmd)
	if err != nil {
		return err
	}
	set, usage, err := loadDeviceInventory(ctx, r)
	if err != nil {
		return err
	}
	topo, err = set.ResolveTopology(topo, usage)
	if err != nil {
		return err
	}

	noDefaults, _ := cmd.Flags().GetBool("no-defaults")
	poolProps, err := zfs.ParseProps(zfsFlagStringArray(cmd, "prop"))
	if err != nil {
		return err
	}
	fsProps, err := zfs.ParseProps(zfsFlagStringArray(cmd, "fs-prop"))
	if err != nil {
		return err
	}
	poolProps = zfs.MergeProps(zfs.DefaultPoolProps(), poolProps, noDefaults)
	fsProps = zfs.MergeProps(zfs.DefaultFSProps(), fsProps, noDefaults)
	if !noDefaults && poolProps["autotrim"] == "" && zfs.AutotrimEligible(set.Devices, topo.DataDevices()) {
		poolProps["autotrim"] = "on"
	}

	if _, err := r.OutputPrivileged(ctx, zpool, zfs.PoolCreateDryRunArgs(name, topo, poolProps, fsProps)...); err != nil {
		return fmt.Errorf("dry-run failed: %w", err)
	}

	fmt.Printf("Pool:        %s\n", name)
	fmt.Printf("Layout:      %s\n", poolTypeOrAuto(topo))
	printVDevs("Data", topo.Data)
	printVDevs("Special", topo.Special)
	printVDevs("Log", topo.Log)
	if len(topo.Cache) > 0 {
		fmt.Printf("Cache:       %s\n", strings.Join(topo.Cache, " "))
	}
	if len(topo.Spare) > 0 {
		fmt.Printf("Spare:       %s\n", strings.Join(topo.Spare, " "))
	}
	fmt.Printf("Pool props:  %s\n", zfsPropSummary(poolProps))
	fmt.Printf("FS props:    %s\n", zfsPropSummary(fsProps))
	fmt.Printf("Mountpoint:  /%s\n", name)
	fmt.Printf("Disks:       %d\n", len(topo.AllDevices()))
	printZFSWarnings(warnings)

	if !confirmZFS(zfsForce(cmd), fmt.Sprintf("Create pool %q? [y/N] ", name)) {
		printZFSCancelled()
		return nil
	}
	if err := r.RunPrivileged(ctx, zpool, zfs.CreateArgs(name, topo, poolProps, fsProps)...); err != nil {
		return err
	}
	fmt.Printf("Created pool %q at /%s with %d disk(s).\n", name, name, len(topo.AllDevices()))
	fmt.Printf("Next: pg zfs dataset create %s/data\n", name)
	return nil
}

func runZFSPoolAdd(cmd *cobra.Command, args []string) error {
	name, err := zfsName(args[0])
	if err != nil {
		return err
	}
	classValue, _ := cmd.Flags().GetString("class")
	class, err := zfs.ParseAddClass(classValue)
	if err != nil {
		return err
	}
	poolType, typeExplicit, err := zfsPoolTypeFlag(cmd)
	if err != nil {
		return err
	}
	topo, warnings, err := zfs.BuildAddTopology(args[1:], class, zfs.BuildOptions{
		Type:         poolType,
		TypeExplicit: typeExplicit,
	})
	if err != nil {
		return err
	}
	ctx, r, zpool, err := zfsPoolTools(cmd)
	if err != nil {
		return err
	}
	set, usage, err := loadDeviceInventory(ctx, r)
	if err != nil {
		return err
	}
	topo, err = set.ResolveTopology(topo, usage)
	if err != nil {
		return err
	}
	addArgs, err := zfs.AddArgs(name, topo, class)
	if err != nil {
		return err
	}
	if _, err := r.OutputPrivileged(ctx, zpool, zfs.WithDryRun(addArgs)...); err != nil {
		return fmt.Errorf("dry-run failed: %w", err)
	}

	fmt.Printf("Pool:        %s\n", name)
	fmt.Printf("Class:       %s\n", class)
	printClassVDevs(class, topo)
	printZFSWarnings(warnings)

	if !confirmZFS(zfsForce(cmd), fmt.Sprintf("Add these devices to pool %q? [y/N] ", name)) {
		printZFSCancelled()
		return nil
	}
	if err := r.RunPrivileged(ctx, zpool, addArgs...); err != nil {
		return err
	}
	fmt.Printf("Added %s vdev(s) to pool %q.\n", class, name)
	return nil
}

func runZFSPoolAttach(cmd *cobra.Command, args []string) error {
	name, err := zfsName(args[0])
	if err != nil {
		return err
	}
	ctx, r, zpool, err := zfsPoolTools(cmd)
	if err != nil {
		return err
	}
	set, usage, err := loadDeviceInventory(ctx, r)
	if err != nil {
		return err
	}
	oldDevice, err := resolvePoolDevice(set, args[1])
	if err != nil {
		return err
	}
	newDevice, err := set.ResolveDevice(args[2], usage)
	if err != nil {
		return err
	}
	fmt.Printf("Pool:        %s\n", name)
	fmt.Printf("Attach:      %s -> %s (mirror)\n", newDevice, oldDevice)
	fmt.Println("A resilver starts immediately; follow it with \"pg zfs pool status\".")
	if !confirmZFS(zfsForce(cmd), fmt.Sprintf("Attach %s to pool %q? [y/N] ", newDevice, name)) {
		printZFSCancelled()
		return nil
	}
	if err := r.RunPrivileged(ctx, zpool, "attach", name, oldDevice, newDevice); err != nil {
		return err
	}
	fmt.Printf("Attached %s to %s in pool %q.\n", newDevice, oldDevice, name)
	return nil
}

func runZFSPoolDetach(cmd *cobra.Command, args []string) error {
	name, err := zfsName(args[0])
	if err != nil {
		return err
	}
	ctx, r, zpool, err := zfsPoolTools(cmd)
	if err != nil {
		return err
	}
	set, _, err := loadDeviceInventory(ctx, r)
	if err != nil {
		return err
	}
	device, err := resolvePoolDevice(set, args[1])
	if err != nil {
		return err
	}
	fmt.Printf("Pool:        %s\n", name)
	fmt.Printf("Detach:      %s\n", device)
	if !confirmZFS(zfsForce(cmd), fmt.Sprintf("Detach %s from pool %q? [y/N] ", device, name)) {
		printZFSCancelled()
		return nil
	}
	if err := r.RunPrivileged(ctx, zpool, "detach", name, device); err != nil {
		return err
	}
	fmt.Printf("Detached %s from pool %q.\n", device, name)
	return nil
}

func runZFSPoolReplace(cmd *cobra.Command, args []string) error {
	name, err := zfsName(args[0])
	if err != nil {
		return err
	}
	ctx, r, zpool, err := zfsPoolTools(cmd)
	if err != nil {
		return err
	}
	set, usage, err := loadDeviceInventory(ctx, r)
	if err != nil {
		return err
	}
	oldDevice, err := resolvePoolDevice(set, args[1])
	if err != nil {
		return err
	}
	newDevice, err := set.ResolveDevice(args[2], usage)
	if err != nil {
		return err
	}
	fmt.Printf("Pool:        %s\n", name)
	fmt.Printf("Replace:     %s -> %s\n", oldDevice, newDevice)
	fmt.Println("A resilver starts immediately; follow it with \"pg zfs pool status\".")
	if !confirmZFS(zfsForce(cmd), fmt.Sprintf("Replace %s with %s in pool %q? [y/N] ", oldDevice, newDevice, name)) {
		printZFSCancelled()
		return nil
	}
	if err := r.RunPrivileged(ctx, zpool, "replace", name, oldDevice, newDevice); err != nil {
		return err
	}
	fmt.Printf("Replacing %s with %s in pool %q.\n", oldDevice, newDevice, name)
	return nil
}

func runZFSPoolOffline(cmd *cobra.Command, args []string) error {
	name, err := zfsName(args[0])
	if err != nil {
		return err
	}
	ctx, r, zpool, err := zfsPoolTools(cmd)
	if err != nil {
		return err
	}
	set, _, err := loadDeviceInventory(ctx, r)
	if err != nil {
		return err
	}
	device, err := resolvePoolDevice(set, args[1])
	if err != nil {
		return err
	}
	if !confirmZFS(zfsForce(cmd), fmt.Sprintf("Take %s offline in pool %q? [y/N] ", device, name)) {
		printZFSCancelled()
		return nil
	}
	if err := r.RunPrivileged(ctx, zpool, "offline", name, device); err != nil {
		return err
	}
	fmt.Printf("Took %s offline in pool %q.\n", device, name)
	return nil
}

func runZFSPoolOnline(cmd *cobra.Command, args []string) error {
	name, err := zfsName(args[0])
	if err != nil {
		return err
	}
	ctx, r, zpool, err := zfsPoolTools(cmd)
	if err != nil {
		return err
	}
	set, _, err := loadDeviceInventory(ctx, r)
	if err != nil {
		return err
	}
	device, err := resolvePoolDevice(set, args[1])
	if err != nil {
		return err
	}
	if err := r.RunPrivileged(ctx, zpool, "online", name, device); err != nil {
		return err
	}
	fmt.Printf("Brought %s online in pool %q; a resilver may be running.\n", device, name)
	return nil
}

func runZFSPoolExport(cmd *cobra.Command, args []string) error {
	name, err := zfsName(args[0])
	if err != nil {
		return err
	}
	ctx, r, zpool, err := zfsPoolTools(cmd)
	if err != nil {
		return err
	}
	if !confirmZFS(zfsForce(cmd), fmt.Sprintf("Export pool %q? [y/N] ", name)) {
		printZFSCancelled()
		return nil
	}
	if err := r.RunPrivileged(ctx, zpool, "export", name); err != nil {
		return err
	}
	fmt.Printf("Exported pool %q. Re-import with \"pg zfs pool import %s\".\n", name, name)
	return nil
}

func runZFSPoolImport(cmd *cobra.Command, args []string) error {
	ctx, r, zpool, err := zfsPoolTools(cmd)
	if err != nil {
		return err
	}
	dir, _ := cmd.Flags().GetString("dir")
	noMount, _ := cmd.Flags().GetBool("no-mount")
	altroot, _ := cmd.Flags().GetString("altroot")

	importArgs := []string{"import"}
	if dir != "" {
		importArgs = append(importArgs, "-d", dir)
	}
	if noMount {
		importArgs = append(importArgs, "-N")
	}
	if altroot != "" {
		importArgs = append(importArgs, "-o", "altroot="+altroot)
	}

	if len(args) == 0 {
		out, err := r.OutputPrivileged(ctx, zpool, importArgs...)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "no pools available to import") {
				fmt.Println("No pools available to import.")
				return nil
			}
			return err
		}
		fmt.Print(string(out))
		return nil
	}

	name, err := zfsName(args[0])
	if err != nil {
		return err
	}
	importArgs = append(importArgs, name)
	fmt.Printf("Pool:        %s\n", name)
	if dir != "" {
		fmt.Printf("Search dir:  %s\n", dir)
	}
	if noMount {
		fmt.Println("Mount:       skipped (--no-mount)")
	}
	if altroot != "" {
		fmt.Printf("Altroot:     %s\n", altroot)
	}
	if !confirmZFS(zfsForce(cmd), fmt.Sprintf("Import pool %q? [y/N] ", name)) {
		printZFSCancelled()
		return nil
	}
	if err := r.RunPrivileged(ctx, zpool, importArgs...); err != nil {
		return err
	}
	fmt.Printf("Imported pool %q.\n", name)
	return nil
}

func runZFSPoolDestroy(cmd *cobra.Command, args []string) error {
	name, err := zfsName(args[0])
	if err != nil {
		return err
	}
	ctx, r, zpool, err := zfsPoolTools(cmd)
	if err != nil {
		return err
	}
	fmt.Printf("Pool:        %s\n", name)
	fmt.Println("  [!] This permanently destroys the pool, all datasets and all data.")
	if !confirmZFS(zfsForce(cmd), fmt.Sprintf("Destroy pool %q? [y/N] ", name)) {
		printZFSCancelled()
		return nil
	}
	if err := r.RunPrivileged(ctx, zpool, "destroy", name); err != nil {
		return err
	}
	fmt.Printf("Destroyed pool %q.\n", name)
	return nil
}

func zfsPoolTypeFlag(cmd *cobra.Command) (zfs.PoolType, bool, error) {
	value, _ := cmd.Flags().GetString("type")
	poolType, err := zfs.ParsePoolType(value)
	if err != nil {
		return "", false, err
	}
	return poolType, cmd.Flags().Changed("type"), nil
}

func poolTypeOrAuto(topo zfs.Topology) string {
	if len(topo.Data) == 0 {
		return "unknown"
	}
	labels := make([]string, 0, len(topo.Data))
	for _, vdev := range topo.Data {
		labels = append(labels, vdev.Type.Label())
	}
	return strings.Join(labels, ", ")
}

func printVDevs(label string, vdevs []zfs.VDev) {
	if len(vdevs) == 0 {
		return
	}
	parts := make([]string, 0, len(vdevs))
	for _, vdev := range vdevs {
		parts = append(parts, strings.Join(vdev.Args(), " "))
	}
	fmt.Printf("%-12s %s\n", label+":", strings.Join(parts, " | "))
}

func printClassVDevs(class zfs.AddClass, topo zfs.Topology) {
	switch class {
	case zfs.ClassData:
		printVDevs("Data", topo.Data)
	case zfs.ClassSpecial:
		printVDevs("Special", topo.Special)
	case zfs.ClassLog:
		printVDevs("Log", topo.Log)
	case zfs.ClassCache:
		fmt.Printf("Cache:       %s\n", strings.Join(topo.Cache, " "))
	case zfs.ClassSpare:
		fmt.Printf("Spare:       %s\n", strings.Join(topo.Spare, " "))
	}
}

// resolvePoolDevice canonicalizes a device path when possible but also
// accepts paths that are no longer present, which is required for detaching or
// offlining a failed disk.
func resolvePoolDevice(set *zfs.DeviceSet, path string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(clean) || !strings.HasPrefix(clean, "/dev/") {
		return "", fmt.Errorf("device must be an absolute /dev path: %q", path)
	}
	if canonical, _, err := set.Resolve(path); err == nil {
		return canonical, nil
	}
	return clean, nil
}
