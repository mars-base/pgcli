package zfs

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	poolNameRe    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]*$`)
	datasetNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]*(/[A-Za-z0-9_.:-]+)*$`)
)

// ValidatePoolName accepts the conservative ZFS pool-name character set.
func ValidatePoolName(name string) error {
	if name == "" {
		return fmt.Errorf("pool name must not be empty")
	}
	if !poolNameRe.MatchString(name) {
		return fmt.Errorf("invalid pool name %q: use letters, digits, '_', '-', '.' and start with a letter", name)
	}
	return nil
}

// ValidateDatasetName accepts pool/child names and snapshots (dataset@snap).
func ValidateDatasetName(name string) error {
	if name == "" {
		return fmt.Errorf("dataset name must not be empty")
	}
	base, snap, hasSnap := strings.Cut(name, "@")
	if hasSnap {
		if snap == "" {
			return fmt.Errorf("invalid snapshot name %q: missing snapshot after '@'", name)
		}
		if strings.Contains(snap, "/") {
			return fmt.Errorf("invalid snapshot name %q: snapshot component must not contain '/'", name)
		}
	}
	first, _, _ := strings.Cut(base, "/")
	if err := ValidatePoolName(first); err != nil {
		return fmt.Errorf("invalid dataset name %q: %w", name, err)
	}
	if !datasetNameRe.MatchString(base) {
		return fmt.Errorf("invalid dataset name %q", name)
	}
	return nil
}

// SplitSnapshot splits dataset@snapshot and validates both halves.
func SplitSnapshot(name string) (dataset, snapshot string, err error) {
	dataset, snapshot, ok := strings.Cut(name, "@")
	if !ok || dataset == "" || snapshot == "" {
		return "", "", fmt.Errorf("invalid snapshot %q: expected DATASET@SNAPSHOT", name)
	}
	if err := ValidateDatasetName(dataset); err != nil {
		return "", "", err
	}
	if strings.ContainsAny(snapshot, "/ ") {
		return "", "", fmt.Errorf("invalid snapshot name %q", snapshot)
	}
	return dataset, snapshot, nil
}

// NormalizeDeviceList splits comma-separated values and repeated flags.
func NormalizeDeviceList(values []string) []string {
	var out []string
	for _, value := range values {
		for part := range strings.SplitSeq(value, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}
