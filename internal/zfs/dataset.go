package zfs

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// DatasetRow is one line of `zfs list`.
type DatasetRow struct {
	Name       string
	Used       int64
	Avail      int64
	Refer      int64
	Mountpoint string
}

// ParseDatasetList parses `zfs list -H -p`.
func ParseDatasetList(out []byte) ([]DatasetRow, error) {
	var rows []DatasetRow
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 5 {
			return nil, fmt.Errorf("unexpected zfs list output: %q", line)
		}
		row := DatasetRow{Name: fields[0], Mountpoint: fields[4]}
		if row.Mountpoint == "-" {
			row.Mountpoint = ""
		}
		var err error
		if row.Used, err = parseSizeField(fields[1]); err != nil {
			return nil, fmt.Errorf("zfs list used %q: %w", fields[1], err)
		}
		if row.Avail, err = parseSizeField(fields[2]); err != nil {
			return nil, fmt.Errorf("zfs list avail %q: %w", fields[2], err)
		}
		if row.Refer, err = parseSizeField(fields[3]); err != nil {
			return nil, fmt.Errorf("zfs list refer %q: %w", fields[3], err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// ListDatasets lists datasets, optionally recursively below parent.
func ListDatasets(ctx context.Context, r *Runner, parent string) ([]DatasetRow, error) {
	zfs, err := LookPath("zfs")
	if err != nil {
		return nil, err
	}
	args := []string{"list", "-H", "-p", "-o", "name,used,avail,refer,mountpoint"}
	if parent != "" {
		args = append(args, "-r", parent)
	}
	out, err := r.OutputPrivileged(ctx, zfs, args...)
	if err != nil {
		if isNoPoolsError(err) {
			return nil, nil
		}
		return nil, err
	}
	return ParseDatasetList(out)
}

// DatasetCreateArgs builds `zfs create`.
func DatasetCreateArgs(name string, parents bool, props map[string]string) []string {
	args := []string{"create"}
	if parents {
		args = append(args, "-p")
	}
	for _, key := range SortedProps(props) {
		args = append(args, "-o", key+"="+props[key])
	}
	return append(args, name)
}

// DatasetSetArgs builds `zfs set` for one or more k=v pairs.
func DatasetSetArgs(name string, props map[string]string) ([]string, error) {
	if len(props) == 0 {
		return nil, fmt.Errorf("at least one property k=v is required")
	}
	keys := make([]string, 0, len(props))
	for key := range props {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	args := []string{"set"}
	for _, key := range keys {
		args = append(args, key+"="+props[key])
	}
	return append(args, name), nil
}

// DatasetCloneArgs builds `zfs clone`.
func DatasetCloneArgs(snapshot, name string, props map[string]string) []string {
	args := []string{"clone"}
	for _, key := range SortedProps(props) {
		args = append(args, "-o", key+"="+props[key])
	}
	return append(args, snapshot, name)
}

// DatasetDestroyArgs builds `zfs destroy`.
func DatasetDestroyArgs(name string, recursive bool) []string {
	args := []string{"destroy"}
	if recursive {
		args = append(args, "-r")
	}
	return append(args, name)
}
