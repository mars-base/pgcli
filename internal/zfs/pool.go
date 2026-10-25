package zfs

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// PoolRow is one line of `zpool list`.
type PoolRow struct {
	Name    string
	Size    int64
	Alloc   int64
	Free    int64
	Health  string
	AltRoot string
}

// ParsePoolList parses `zpool list -H -p`.
func ParsePoolList(out []byte) ([]PoolRow, error) {
	var rows []PoolRow
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 6 {
			return nil, fmt.Errorf("unexpected zpool list output: %q", line)
		}
		row := PoolRow{
			Name:    fields[0],
			Health:  fields[4],
			AltRoot: fields[5],
		}
		if row.AltRoot == "-" {
			row.AltRoot = ""
		}
		var err error
		if row.Size, err = parseSizeField(fields[1]); err != nil {
			return nil, fmt.Errorf("zpool list size %q: %w", fields[1], err)
		}
		if row.Alloc, err = parseSizeField(fields[2]); err != nil {
			return nil, fmt.Errorf("zpool list alloc %q: %w", fields[2], err)
		}
		if row.Free, err = parseSizeField(fields[3]); err != nil {
			return nil, fmt.Errorf("zpool list free %q: %w", fields[3], err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func parseSizeField(value string) (int64, error) {
	if value == "-" || value == "" {
		return -1, nil
	}
	return strconv.ParseInt(value, 10, 64)
}

// ListPools returns the configured pools.
func ListPools(ctx context.Context, r *Runner) ([]PoolRow, error) {
	zpool, err := LookPath("zpool")
	if err != nil {
		return nil, err
	}
	out, err := r.OutputPrivileged(ctx, zpool, "list", "-H", "-p", "-o", "name,size,alloc,free,health,altroot")
	if err != nil {
		if isNoPoolsError(err) {
			return nil, nil
		}
		return nil, err
	}
	return ParsePoolList(out)
}

func isNoPoolsError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no pools available") || strings.Contains(message, "no datasets available")
}

// PoolCreateDryRunArgs returns the zpool create argv with -n inserted.
func PoolCreateDryRunArgs(pool string, topo Topology, poolProps, fsProps map[string]string) []string {
	return WithDryRun(CreateArgs(pool, topo, poolProps, fsProps))
}
