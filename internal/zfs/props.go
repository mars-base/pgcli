package zfs

import (
	"fmt"
	"maps"
	"sort"
	"strings"
)

// DefaultPoolProps are applied to every pool unless the user overrides them or
// passes --no-defaults.
func DefaultPoolProps() map[string]string {
	return map[string]string{
		"ashift":     "12",
		"autoexpand": "on",
	}
}

// DefaultFSProps are applied to the root dataset of a new pool.
func DefaultFSProps() map[string]string {
	return map[string]string{
		"compression": "zstd",
		"atime":       "off",
		"xattr":       "sa",
		"acltype":     "posixacl",
		"dnodesize":   "auto",
	}
}

// ParseProps parses repeatable k=v flags.
func ParseProps(values []string) (map[string]string, error) {
	props := map[string]string{}
	for _, value := range values {
		key, val, ok := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid property %q: expected KEY=VALUE", value)
		}
		if strings.ContainsAny(key, " \t") {
			return nil, fmt.Errorf("invalid property name %q", key)
		}
		props[key] = val
	}
	return props, nil
}

// MergeProps overlays user properties on defaults. With noDefaults, only the
// user's properties remain.
func MergeProps(defaults, user map[string]string, noDefaults bool) map[string]string {
	out := map[string]string{}
	if !noDefaults {
		maps.Copy(out, defaults)
	}
	maps.Copy(out, user)
	return out
}

// SortedProps returns property names in deterministic order.
func SortedProps(props map[string]string) []string {
	keys := make([]string, 0, len(props))
	for key := range props {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// AutotrimEligible reports whether every data device is non-rotational and
// reports discard support. This is a conservative check: any uncertainty
// disables it.
func AutotrimEligible(devices []BlockDevice, dataDevices []string) bool {
	wanted := map[string]bool{}
	for _, dev := range dataDevices {
		wanted[dev] = true
	}
	found := 0
	for _, dev := range devices {
		if !wanted[dev.Path] && !wanted[dev.Name] {
			continue
		}
		found++
		if dev.Rotational || (dev.DiscardGran <= 0 && dev.DiscardMax <= 0) {
			return false
		}
	}
	return found == len(wanted) && found > 0
}
