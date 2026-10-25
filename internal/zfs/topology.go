package zfs

import (
	"fmt"
	"strings"
)

// PoolType is a supported data-vdev layout.
type PoolType string

const (
	TypeStripe PoolType = "stripe"
	TypeMirror PoolType = "mirror"
	TypeRaidz1 PoolType = "raidz1"
	TypeRaidz2 PoolType = "raidz2"
	TypeRaidz3 PoolType = "raidz3"
)

// ParsePoolType accepts ZFS names plus common RAID aliases. The user-facing
// RAID2/RAID3 map to raidz2/raidz3 because ZFS has no standard RAID2/RAID3.
func ParsePoolType(value string) (PoolType, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "", nil
	case "stripe", "striped", "raid0":
		return TypeStripe, nil
	case "mirror", "raid1":
		return TypeMirror, nil
	case "raidz", "raidz1", "raid5":
		return TypeRaidz1, nil
	case "raidz2", "raid2", "raid6":
		return TypeRaidz2, nil
	case "raidz3", "raid3":
		return TypeRaidz3, nil
	default:
		return "", fmt.Errorf("invalid --type %q: valid values are stripe, mirror, raidz1, raidz2, raidz3 (aliases: raid0, raid1, raid2, raid3)", value)
	}
}

// Keyword returns the zpool vdev keyword ("" for stripe).
func (t PoolType) Keyword() string {
	switch t {
	case TypeMirror:
		return "mirror"
	case TypeRaidz1:
		return "raidz"
	case TypeRaidz2:
		return "raidz2"
	case TypeRaidz3:
		return "raidz3"
	default:
		return ""
	}
}

// MinDevices is the hard lower bound accepted by ZFS.
func (t PoolType) MinDevices() int {
	switch t {
	case TypeMirror:
		return 2
	case TypeRaidz1:
		return 2
	case TypeRaidz2:
		return 3
	case TypeRaidz3:
		return 4
	default:
		return 1
	}
}

// RecommendedDevices is the count below which a warning is emitted.
func (t PoolType) RecommendedDevices() int {
	switch t {
	case TypeRaidz1:
		return 3
	case TypeRaidz2:
		return 4
	case TypeRaidz3:
		return 5
	default:
		return t.MinDevices()
	}
}

// Label describes the layout for humans.
func (t PoolType) Label() string {
	switch t {
	case TypeStripe:
		return "stripe (no redundancy, RAID0-like)"
	case TypeMirror:
		return "mirror (RAID1-like)"
	case TypeRaidz1:
		return "raidz1 (single parity, RAID5-like)"
	case TypeRaidz2:
		return "raidz2 (double parity, RAID6-like)"
	case TypeRaidz3:
		return "raidz3 (triple parity)"
	default:
		return string(t)
	}
}

// AutoType chooses a layout for the simple positional-device path.
func AutoType(devices int) PoolType {
	switch {
	case devices <= 1:
		return TypeStripe
	case devices == 2:
		return TypeMirror
	case devices == 3:
		return TypeRaidz1
	default:
		return TypeRaidz2
	}
}

// VDev is one vdev group and its layout.
type VDev struct {
	Type    PoolType
	Devices []string
}

// Args renders a vdev for zpool create/add.
func (v VDev) Args() []string {
	if v.Type == "" || v.Type == TypeStripe {
		return append([]string{}, v.Devices...)
	}
	args := []string{v.Type.Keyword()}
	return append(args, v.Devices...)
}

// Topology is the complete set of vdevs for a pool.
type Topology struct {
	Data    []VDev
	Special []VDev
	Log     []VDev
	Cache   []string
	Spare   []string
}

// AllDevices lists every device in every class.
func (t Topology) AllDevices() []string {
	var out []string
	for _, vdevs := range [][]VDev{t.Data, t.Special, t.Log} {
		for _, vdev := range vdevs {
			out = append(out, vdev.Devices...)
		}
	}
	out = append(out, t.Cache...)
	out = append(out, t.Spare...)
	return out
}

// DataDevices lists only data-vdev devices (used for autotrim detection).
func (t Topology) DataDevices() []string {
	var out []string
	for _, vdev := range t.Data {
		out = append(out, vdev.Devices...)
	}
	return out
}

// MapDevices rewrites every device through replacements.
func (t Topology) MapDevices(replacements map[string]string) Topology {
	mapList := func(values []string) []string {
		out := make([]string, len(values))
		for i, value := range values {
			if replacement, ok := replacements[value]; ok {
				out[i] = replacement
			} else {
				out[i] = value
			}
		}
		return out
	}
	mapVDevs := func(vdevs []VDev) []VDev {
		out := make([]VDev, len(vdevs))
		for i, vdev := range vdevs {
			out[i] = VDev{Type: vdev.Type, Devices: mapList(vdev.Devices)}
		}
		return out
	}
	return Topology{
		Data:    mapVDevs(t.Data),
		Special: mapVDevs(t.Special),
		Log:     mapVDevs(t.Log),
		Cache:   mapList(t.Cache),
		Spare:   mapList(t.Spare),
	}
}

// BuildOptions carries the topology flags.
type BuildOptions struct {
	Type         PoolType
	TypeExplicit bool
	Data         []string
	Special      []string
	Log          []string
	Cache        []string
	Spare        []string
}

// BuildTopology validates device counts and assembles a topology.
func BuildTopology(positional []string, opts BuildOptions) (Topology, []string, error) {
	var warnings []string
	var dataGroups [][]string
	if len(opts.Data) > 0 {
		if len(positional) > 0 {
			return Topology{}, nil, fmt.Errorf("use either positional devices or --data, not both")
		}
		for _, raw := range opts.Data {
			group := NormalizeDeviceList([]string{raw})
			if len(group) == 0 {
				return Topology{}, nil, fmt.Errorf("--data must not be empty")
			}
			dataGroups = append(dataGroups, group)
		}
	} else {
		group := NormalizeDeviceList(positional)
		if len(group) == 0 {
			return Topology{}, nil, fmt.Errorf("at least one device is required")
		}
		dataGroups = [][]string{group}
	}
	if (len(opts.Data) > 0 || len(opts.Special) > 0) && !opts.TypeExplicit {
		return Topology{}, nil, fmt.Errorf("--type is required when using --data or --special")
	}
	poolType := opts.Type
	if poolType == "" {
		total := 0
		for _, group := range dataGroups {
			total += len(group)
		}
		poolType = AutoType(total)
	}

	topo := Topology{}
	for _, group := range dataGroups {
		if err := validateVDev("data", poolType, group, &warnings); err != nil {
			return Topology{}, nil, err
		}
		topo.Data = append(topo.Data, VDev{Type: poolType, Devices: group})
	}
	for _, raw := range opts.Special {
		group := NormalizeDeviceList([]string{raw})
		if len(group) == 0 {
			return Topology{}, nil, fmt.Errorf("--special must not be empty")
		}
		if err := validateVDev("special", poolType, group, &warnings); err != nil {
			return Topology{}, nil, err
		}
		topo.Special = append(topo.Special, VDev{Type: poolType, Devices: group})
	}
	logDevices := NormalizeDeviceList(opts.Log)
	switch len(logDevices) {
	case 0:
	case 1:
		topo.Log = []VDev{{Type: TypeStripe, Devices: logDevices}}
		warnings = append(warnings, "a single log (SLOG) device has no redundancy; add pairs or use --type for data only")
	default:
		topo.Log = []VDev{{Type: TypeMirror, Devices: logDevices}}
	}
	topo.Cache = NormalizeDeviceList(opts.Cache)
	topo.Spare = NormalizeDeviceList(opts.Spare)

	seen := map[string]string{}
	for _, class := range []struct {
		name   string
		values []string
	}{
		{"data", topo.DataDevices()},
		{"special", devicesOf(topo.Special)},
		{"log", devicesOf(topo.Log)},
		{"cache", topo.Cache},
		{"spare", topo.Spare},
	} {
		for _, device := range class.values {
			if previous, ok := seen[device]; ok {
				return Topology{}, nil, fmt.Errorf("device %q appears in both %s and %s", device, previous, class.name)
			}
			seen[device] = class.name
		}
	}
	if poolType == TypeStripe {
		for _, vdev := range topo.Data {
			if len(vdev.Devices) > 1 {
				warnings = append(warnings, "stripe has no redundancy; any single disk failure loses the whole pool")
				break
			}
		}
		if len(topo.Data) == 1 && len(topo.Data[0].Devices) == 1 {
			warnings = append(warnings, "a single-device stripe has no redundancy; any disk failure loses the whole pool")
		}
	}
	return topo, warnings, nil
}

func devicesOf(vdevs []VDev) []string {
	var out []string
	for _, vdev := range vdevs {
		out = append(out, vdev.Devices...)
	}
	return out
}

func validateVDev(class string, poolType PoolType, devices []string, warnings *[]string) error {
	if len(devices) < poolType.MinDevices() {
		return fmt.Errorf("%s vdev with --type %s requires at least %d devices, got %d", class, poolType, poolType.MinDevices(), len(devices))
	}
	if len(devices) < poolType.RecommendedDevices() {
		*warnings = append(*warnings, fmt.Sprintf("%s vdev has %d devices; %s is recommended with %d or more", class, len(devices), poolType, poolType.RecommendedDevices()))
	}
	return nil
}

// CreateArgs builds `zpool create` argv. Property keys are sorted so output
// and tests are deterministic.
func CreateArgs(pool string, topo Topology, poolProps, fsProps map[string]string) []string {
	args := []string{"create"}
	for _, key := range SortedProps(poolProps) {
		args = append(args, "-o", key+"="+poolProps[key])
	}
	for _, key := range SortedProps(fsProps) {
		args = append(args, "-O", key+"="+fsProps[key])
	}
	args = append(args, pool)
	for _, vdev := range topo.Data {
		args = append(args, vdev.Args()...)
	}
	if len(topo.Special) > 0 {
		args = append(args, "special")
		for _, vdev := range topo.Special {
			args = append(args, vdev.Args()...)
		}
	}
	if len(topo.Log) > 0 {
		args = append(args, "log")
		for _, vdev := range topo.Log {
			args = append(args, vdev.Args()...)
		}
	}
	if len(topo.Cache) > 0 {
		args = append(args, "cache")
		args = append(args, topo.Cache...)
	}
	if len(topo.Spare) > 0 {
		args = append(args, "spare")
		args = append(args, topo.Spare...)
	}
	return args
}

// WithDryRun inserts zpool's -n flag after the subcommand.
func WithDryRun(args []string) []string {
	if len(args) == 0 {
		return args
	}
	out := make([]string, 0, len(args)+1)
	out = append(out, args[0], "-n")
	out = append(out, args[1:]...)
	return out
}

// AddClass is a valid class for `pg zfs pool add`.
type AddClass string

const (
	ClassData    AddClass = "data"
	ClassSpecial AddClass = "special"
	ClassLog     AddClass = "log"
	ClassCache   AddClass = "cache"
	ClassSpare   AddClass = "spare"
)

// ParseAddClass validates --class.
func ParseAddClass(value string) (AddClass, error) {
	switch AddClass(strings.ToLower(strings.TrimSpace(value))) {
	case "", ClassData:
		return ClassData, nil
	case ClassSpecial:
		return ClassSpecial, nil
	case ClassLog:
		return ClassLog, nil
	case ClassCache:
		return ClassCache, nil
	case ClassSpare:
		return ClassSpare, nil
	default:
		return "", fmt.Errorf("invalid --class %q: valid values are data, special, log, cache, spare", value)
	}
}

// AddArgs builds `zpool add` argv for one class.
func AddArgs(pool string, topo Topology, class AddClass) ([]string, error) {
	args := []string{"add", pool}
	switch class {
	case ClassData:
		for _, vdev := range topo.Data {
			args = append(args, vdev.Args()...)
		}
	case ClassSpecial:
		args = append(args, "special")
		for _, vdev := range topo.Special {
			args = append(args, vdev.Args()...)
		}
	case ClassLog:
		args = append(args, "log")
		for _, vdev := range topo.Log {
			args = append(args, vdev.Args()...)
		}
	case ClassCache:
		args = append(args, "cache")
		args = append(args, topo.Cache...)
	case ClassSpare:
		args = append(args, "spare")
		args = append(args, topo.Spare...)
	default:
		return nil, fmt.Errorf("invalid vdev class %q", class)
	}
	if len(args) == 2 {
		return nil, fmt.Errorf("no devices given for class %s", class)
	}
	return args, nil
}

// BuildAddTopology builds a single-class topology for `pool add`. Unlike
// create, only the class being extended is populated.
func BuildAddTopology(positional []string, class AddClass, opts BuildOptions) (Topology, []string, error) {
	if class == ClassData {
		return BuildTopology(positional, opts)
	}
	if len(opts.Data) > 0 {
		return Topology{}, nil, fmt.Errorf("--data is only valid with --class data")
	}
	devices := NormalizeDeviceList(positional)
	if len(devices) == 0 {
		return Topology{}, nil, fmt.Errorf("at least one device is required")
	}
	if err := ensureUniqueDevices(devices); err != nil {
		return Topology{}, nil, err
	}
	var warnings []string
	topo := Topology{}
	switch class {
	case ClassSpecial:
		if opts.Type == "" {
			return Topology{}, nil, fmt.Errorf("--type is required for --class special")
		}
		if err := validateVDev("special", opts.Type, devices, &warnings); err != nil {
			return Topology{}, nil, err
		}
		topo.Special = []VDev{{Type: opts.Type, Devices: devices}}
	case ClassLog:
		if opts.Type != "" {
			return Topology{}, nil, fmt.Errorf("--type is not used for log devices (they are mirrored automatically)")
		}
		if len(devices) == 1 {
			topo.Log = []VDev{{Type: TypeStripe, Devices: devices}}
			warnings = append(warnings, "a single log (SLOG) device has no redundancy")
		} else {
			topo.Log = []VDev{{Type: TypeMirror, Devices: devices}}
		}
	case ClassCache:
		if opts.Type != "" {
			return Topology{}, nil, fmt.Errorf("--type is not used for cache devices")
		}
		topo.Cache = devices
	case ClassSpare:
		if opts.Type != "" {
			return Topology{}, nil, fmt.Errorf("--type is not used for spare devices")
		}
		topo.Spare = devices
	default:
		return Topology{}, nil, fmt.Errorf("invalid vdev class %q", class)
	}
	return topo, warnings, nil
}

func ensureUniqueDevices(devices []string) error {
	seen := map[string]bool{}
	for _, device := range devices {
		if seen[device] {
			return fmt.Errorf("device %q appears more than once", device)
		}
		seen[device] = true
	}
	return nil
}
