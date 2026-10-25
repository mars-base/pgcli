package zfs

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// SnapshotRow is one line of `zfs list -t snapshot`.
type SnapshotRow struct {
	Name     string
	Used     int64
	Refer    int64
	Creation int64
}

// ParseSnapshotList parses `zfs list -t snapshot -H -p`.
func ParseSnapshotList(out []byte) ([]SnapshotRow, error) {
	var rows []SnapshotRow
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 4 {
			return nil, fmt.Errorf("unexpected zfs snapshot list output: %q", line)
		}
		row := SnapshotRow{Name: fields[0]}
		var err error
		if row.Used, err = parseSizeField(fields[1]); err != nil {
			return nil, fmt.Errorf("snapshot used %q: %w", fields[1], err)
		}
		if row.Refer, err = parseSizeField(fields[2]); err != nil {
			return nil, fmt.Errorf("snapshot refer %q: %w", fields[2], err)
		}
		row.Creation, err = strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("snapshot creation %q: %w", fields[3], err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// ListSnapshots lists snapshots, optionally recursively below parent.
func ListSnapshots(ctx context.Context, r *Runner, parent string) ([]SnapshotRow, error) {
	zfs, err := LookPath("zfs")
	if err != nil {
		return nil, err
	}
	args := []string{"list", "-H", "-p", "-t", "snapshot", "-o", "name,used,refer,creation"}
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
	return ParseSnapshotList(out)
}

// SnapshotCreateArgs builds `zfs snapshot`.
func SnapshotCreateArgs(name string, recursive bool) []string {
	args := []string{"snapshot"}
	if recursive {
		args = append(args, "-r")
	}
	return append(args, name)
}

// SnapshotRollbackArgs builds `zfs rollback`.
func SnapshotRollbackArgs(name string, recursive bool) []string {
	args := []string{"rollback"}
	if recursive {
		args = append(args, "-r")
	}
	return append(args, name)
}

// SnapshotDestroyArgs builds `zfs destroy` for a snapshot.
func SnapshotDestroyArgs(name string, recursive bool) []string {
	args := []string{"destroy"}
	if recursive {
		args = append(args, "-r")
	}
	return append(args, name)
}
