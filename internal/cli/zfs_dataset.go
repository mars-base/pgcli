package cli

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/zfs"
)

var zfsDatasetCmd = &cobra.Command{
	Use:   "dataset",
	Short: "Create and manage ZFS datasets",
	Long:  `Create and manage ZFS datasets inside a pool.`,
}

var zfsDatasetCreateCmd = &cobra.Command{
	Use:   "create NAME",
	Short: "Create a dataset",
	Long: `Create a dataset. Parent datasets are not created unless --parents is given.
Properties are passed to "zfs create -o".

Examples:
  pg zfs dataset create tank/data
  pg zfs dataset create tank/data/postgres --parents
  pg zfs dataset create tank/data --prop quota=100G --prop recordsize=128k`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := zfs.ValidateDatasetName(name); err != nil {
			return err
		}
		if strings.Contains(name, "@") {
			return fmt.Errorf("%q is a snapshot; use \"pg zfs snapshot create\"", name)
		}
		props, err := zfs.ParseProps(zfsFlagStringArray(cmd, "prop"))
		if err != nil {
			return err
		}
		parents, _ := cmd.Flags().GetBool("parents")
		ctx, r, zfsBin, err := zfsUserlandTools(cmd)
		if err != nil {
			return err
		}
		if err := r.RunPrivileged(ctx, zfsBin, zfs.DatasetCreateArgs(name, parents, props)...); err != nil {
			return err
		}
		fmt.Printf("Created dataset %q.\n", name)
		if len(props) > 0 {
			fmt.Printf("Properties:  %s\n", zfsPropSummary(props))
		}
		return nil
	},
}

var zfsDatasetListCmd = &cobra.Command{
	Use:   "list [DATASET]",
	Short: "List datasets",
	Long: `List datasets. With a dataset name, list it and its children. Use --props with
a comma-separated property list to replace the default columns.

Examples:
  pg zfs dataset list
  pg zfs dataset list tank
  pg zfs dataset list tank/data --props used,avail,quota,compression`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		parent := ""
		if len(args) == 1 {
			if err := zfs.ValidateDatasetName(args[0]); err != nil {
				return err
			}
			if strings.Contains(args[0], "@") {
				return fmt.Errorf("%q is a snapshot; use \"pg zfs snapshot list\"", args[0])
			}
			parent = args[0]
		}
		ctx, r, zfsBin, err := zfsUserlandTools(cmd)
		if err != nil {
			return err
		}
		props, _ := cmd.Flags().GetString("props")
		if props != "" {
			return listDatasetProps(ctx, r, zfsBin, parent, props)
		}
		rows, err := zfs.ListDatasets(ctx, r, parent)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			fmt.Println("No datasets.")
			return nil
		}
		writer := tabwriter.NewWriter(os.Stdout, 2, 8, 2, ' ', 0)
		fmt.Fprintln(writer, "DATASET\tUSED\tAVAIL\tREFER\tMOUNTPOINT")
		for _, row := range rows {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
				row.Name,
				zfs.FormatBytes(row.Used),
				zfs.FormatBytes(row.Avail),
				zfs.FormatBytes(row.Refer),
				zfsValueOr(row.Mountpoint, "-"),
			)
		}
		return writer.Flush()
	},
}

var zfsDatasetSetCmd = &cobra.Command{
	Use:   "set NAME KEY=VALUE...",
	Short: "Set dataset properties",
	Long: `Set one or more properties with "zfs set".

Examples:
  pg zfs dataset set tank/data compression=lz4
  pg zfs dataset set tank/data quota=100G recordsize=128k`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := zfs.ValidateDatasetName(name); err != nil {
			return err
		}
		props, err := zfs.ParseProps(args[1:])
		if err != nil {
			return err
		}
		setArgs, err := zfs.DatasetSetArgs(name, props)
		if err != nil {
			return err
		}
		ctx, r, zfsBin, err := zfsUserlandTools(cmd)
		if err != nil {
			return err
		}
		if err := r.RunPrivileged(ctx, zfsBin, setArgs...); err != nil {
			return err
		}
		fmt.Printf("Updated %q: %s\n", name, zfsPropSummary(props))
		return nil
	},
}

var zfsDatasetCloneCmd = &cobra.Command{
	Use:   "clone SNAPSHOT NAME",
	Short: "Clone a snapshot into a new dataset",
	Long: `Create a writable dataset from a snapshot.

Examples:
  pg zfs snapshot create tank/data@before-upgrade
  pg zfs dataset clone tank/data@before-upgrade tank/data-old
  pg zfs dataset clone tank/data@v1 tank/data-copy --prop mountpoint=/tank/copy`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, _, err := zfs.SplitSnapshot(args[0]); err != nil {
			return err
		}
		name := args[1]
		if err := zfs.ValidateDatasetName(name); err != nil {
			return err
		}
		if strings.Contains(name, "@") {
			return fmt.Errorf("clone target %q must be a dataset, not a snapshot", name)
		}
		props, err := zfs.ParseProps(zfsFlagStringArray(cmd, "prop"))
		if err != nil {
			return err
		}
		ctx, r, zfsBin, err := zfsUserlandTools(cmd)
		if err != nil {
			return err
		}
		if err := r.RunPrivileged(ctx, zfsBin, zfs.DatasetCloneArgs(args[0], name, props)...); err != nil {
			return err
		}
		fmt.Printf("Cloned %q to %q.\n", args[0], name)
		return nil
	},
}

var zfsDatasetDestroyCmd = &cobra.Command{
	Use:   "destroy NAME",
	Short: "Destroy a dataset",
	Long: `Destroy a dataset and its data. A dataset with children requires --recursive.
This is permanent and requires confirmation; --force only skips the prompt.

Examples:
  pg zfs dataset destroy tank/data-old
  pg zfs dataset destroy tank/data --recursive --force`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := zfs.ValidateDatasetName(name); err != nil {
			return err
		}
		if strings.Contains(name, "@") {
			return fmt.Errorf("%q is a snapshot; use \"pg zfs snapshot destroy\"", name)
		}
		recursive, _ := cmd.Flags().GetBool("recursive")
		ctx, r, zfsBin, err := zfsUserlandTools(cmd)
		if err != nil {
			return err
		}
		fmt.Printf("Dataset:     %s\n", name)
		if recursive {
			fmt.Println("Mode:        recursive (children and snapshots are destroyed)")
		}
		fmt.Println("  [!] This permanently destroys the dataset and its data.")
		if !confirmZFS(zfsForce(cmd), fmt.Sprintf("Destroy dataset %q? [y/N] ", name)) {
			printZFSCancelled()
			return nil
		}
		if err := r.RunPrivileged(ctx, zfsBin, zfs.DatasetDestroyArgs(name, recursive)...); err != nil {
			if !recursive && strings.Contains(strings.ToLower(err.Error()), "has children") {
				return fmt.Errorf("%w (hint: add --recursive)", err)
			}
			return err
		}
		fmt.Printf("Destroyed dataset %q.\n", name)
		return nil
	},
}

func init() {
	zfsDatasetCreateCmd.Flags().BoolP("parents", "p", false, "create missing parent datasets")
	zfsDatasetCreateCmd.Flags().StringArray("prop", nil, "dataset property KEY=VALUE; repeatable")

	zfsDatasetListCmd.Flags().String("props", "", "comma-separated properties to show instead of the default columns")

	zfsDatasetCloneCmd.Flags().StringArray("prop", nil, "property for the cloned dataset KEY=VALUE; repeatable")

	zfsDatasetDestroyCmd.Flags().BoolP("recursive", "r", false, "destroy child datasets and snapshots too")
	addZFSForceFlag(zfsDatasetDestroyCmd)

	zfsDatasetCmd.AddCommand(
		zfsDatasetCreateCmd,
		zfsDatasetListCmd,
		zfsDatasetSetCmd,
		zfsDatasetCloneCmd,
		zfsDatasetDestroyCmd,
	)
	zfsCmd.AddCommand(zfsDatasetCmd)
}

func zfsUserlandTools(cmd *cobra.Command) (context.Context, *zfs.Runner, string, error) {
	if err := requireZFSInstalled(); err != nil {
		return nil, nil, "", err
	}
	zfsBin, err := zfs.LookPath("zfs")
	if err != nil {
		return nil, nil, "", err
	}
	r, err := newZFSRunner(cmd)
	if err != nil {
		return nil, nil, "", err
	}
	return zfsContext(cmd), r, zfsBin, nil
}

var datasetPropRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9:_-]*$`)

func listDatasetProps(ctx context.Context, r *zfs.Runner, zfsBin, parent, props string) error {
	fields := strings.Split(props, ",")
	for i := range fields {
		fields[i] = strings.TrimSpace(fields[i])
		if !datasetPropRe.MatchString(fields[i]) {
			return fmt.Errorf("invalid property name %q", fields[i])
		}
	}
	args := []string{"list", "-H", "-p", "-o", "name," + strings.Join(fields, ",")}
	if parent != "" {
		args = append(args, "-r", parent)
	}
	out, err := r.OutputPrivileged(ctx, zfsBin, args...)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no datasets available") {
			fmt.Println("No datasets.")
			return nil
		}
		return err
	}
	writer := tabwriter.NewWriter(os.Stdout, 2, 8, 2, ' ', 0)
	fmt.Fprint(writer, "NAME")
	for _, field := range fields {
		fmt.Fprintf(writer, "\t%s", strings.ToUpper(field))
	}
	fmt.Fprintln(writer)
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fmt.Fprintln(writer, line)
	}
	return writer.Flush()
}
