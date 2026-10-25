package zfs

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// BlockDevice is the subset of lsblk -J output used for safety checks.
type BlockDevice struct {
	Name        string        `json:"name"`
	Path        string        `json:"-"`
	Type        string        `json:"type"`
	Size        int64         `json:"size"`
	Model       string        `json:"model"`
	Serial      string        `json:"serial"`
	Rotational  bool          `json:"rota"`
	FSType      string        `json:"fstype"`
	Mountpoints []string      `json:"mountpoints"`
	Mountpoint  string        `json:"mountpoint"`
	PKName      string        `json:"pkname"`
	ReadOnly    bool          `json:"ro"`
	DiscardGran int64         `json:"disc-gran"`
	DiscardMax  int64         `json:"disc-max"`
	Children    []BlockDevice `json:"children"`
	ParentName  string        `json:"-"`
}

// LoadBlockDevices runs lsblk and flattens the device tree.
func LoadBlockDevices(ctx context.Context, r *Runner) ([]BlockDevice, error) {
	lsblk, err := LookPath("lsblk")
	if err != nil {
		return nil, err
	}
	full := "NAME,TYPE,SIZE,MODEL,SERIAL,ROTA,FSTYPE,MOUNTPOINTS,PKNAME,RO,DISC-GRAN,DISC-MAX"
	reduced := "NAME,TYPE,SIZE,MODEL,SERIAL,ROTA,FSTYPE,PKNAME,RO"
	out, err := r.Output(ctx, lsblk, "-J", "-b", "-o", full)
	if err != nil {
		// util-linux before 2.37 has no MOUNTPOINTS column.
		out, err = r.Output(ctx, lsblk, "-J", "-b", "-o", reduced)
		if err != nil {
			return nil, err
		}
	}
	var doc struct {
		BlockDevices []BlockDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("parsing lsblk output: %w", err)
	}
	var flat []BlockDevice
	var walk func(dev BlockDevice, parent string)
	walk = func(dev BlockDevice, parent string) {
		children := dev.Children
		dev.Children = nil
		if dev.Path == "" && dev.Name != "" {
			dev.Path = "/dev/" + dev.Name
		}
		if dev.PKName == "" {
			dev.PKName = parent
		}
		dev.ParentName = parent
		if dev.Mountpoint != "" {
			dev.Mountpoints = append(dev.Mountpoints, dev.Mountpoint)
			dev.Mountpoint = ""
		}
		flat = append(flat, dev)
		for _, child := range children {
			walk(child, dev.Name)
		}
	}
	for _, dev := range doc.BlockDevices {
		walk(dev, "")
	}
	return flat, nil
}

// DeviceSet indexes lsblk devices and provides safety checks.
type DeviceSet struct {
	Devices    []BlockDevice
	byName     map[string]*BlockDevice
	children   map[string][]*BlockDevice
	rootDisk   string
	mountTable map[string][]string
}

// NewDeviceSet builds the indexes. Root-disk and /proc/mounts detection is
// best-effort; a failure simply leaves those checks empty.
func NewDeviceSet(devices []BlockDevice) *DeviceSet {
	set := &DeviceSet{
		Devices:    devices,
		byName:     make(map[string]*BlockDevice, len(devices)),
		children:   map[string][]*BlockDevice{},
		mountTable: readMountTable(),
	}
	for i := range set.Devices {
		dev := &set.Devices[i]
		set.byName[dev.Name] = dev
		if dev.PKName != "" {
			set.children[dev.PKName] = append(set.children[dev.PKName], dev)
		}
	}
	set.rootDisk = set.findRootDisk()
	return set
}

func (s *DeviceSet) findRootDisk() string {
	source := ""
	for path, mounts := range s.mountTable {
		if slices.Contains(mounts, "/") {
			source = path
			break
		}
	}
	if source == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		resolved = source
	}
	name := filepath.Base(resolved)
	dev, ok := s.byName[name]
	if !ok {
		return ""
	}
	for dev.ParentName != "" {
		parent, ok := s.byName[dev.ParentName]
		if !ok {
			break
		}
		dev = parent
	}
	return dev.Name
}

// Resolve maps a user-supplied path to its canonical device node and lsblk
// entry. Only /dev paths that resolve to block devices known to lsblk are
// accepted.
func (s *DeviceSet) Resolve(path string) (string, *BlockDevice, error) {
	if !filepath.IsAbs(path) {
		return "", nil, fmt.Errorf("device must be an absolute /dev path: %q", path)
	}
	clean := filepath.Clean(path)
	if !strings.HasPrefix(clean, "/dev/") {
		return "", nil, fmt.Errorf("device must be under /dev: %q", path)
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", nil, fmt.Errorf("resolving %q: %w", path, err)
	}
	if !strings.HasPrefix(resolved, "/dev/") {
		return "", nil, fmt.Errorf("%q resolves outside /dev: %q", path, resolved)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", nil, fmt.Errorf("stat %q: %w", resolved, err)
	}
	if fi.Mode()&os.ModeDevice == 0 || fi.Mode()&os.ModeCharDevice != 0 {
		return "", nil, fmt.Errorf("%q is not a block device", path)
	}
	name := filepath.Base(resolved)
	dev, ok := s.byName[name]
	if !ok {
		return "", nil, fmt.Errorf("%q is not a whole disk known to lsblk", path)
	}
	return resolved, dev, nil
}

func (s *DeviceSet) descendants(dev *BlockDevice) []*BlockDevice {
	var out []*BlockDevice
	var walk func(d *BlockDevice)
	walk = func(d *BlockDevice) {
		for _, child := range s.children[d.Name] {
			out = append(out, child)
			walk(child)
		}
	}
	walk(dev)
	return out
}

func (s *DeviceSet) mountpointsFor(dev *BlockDevice) []string {
	seen := map[string]bool{}
	var out []string
	add := func(mps ...string) {
		for _, mp := range mps {
			if mp == "" || seen[mp] {
				continue
			}
			seen[mp] = true
			out = append(out, mp)
		}
	}
	nodes := append([]*BlockDevice{dev}, s.descendants(dev)...)
	for _, node := range nodes {
		add(node.Mountpoints...)
		add(s.mountTable[node.Path]...)
		add(s.mountTable["/dev/"+node.Name]...)
	}
	return out
}

func (s *DeviceSet) usedPoolsFor(dev *BlockDevice, usage map[string][]string) []string {
	if len(usage) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	nodes := append([]*BlockDevice{dev}, s.descendants(dev)...)
	for _, node := range nodes {
		for path, pools := range usage {
			resolved := path
			if canonical, err := filepath.EvalSymlinks(path); err == nil {
				resolved = canonical
			}
			if resolved != node.Path && filepath.Base(resolved) != node.Name {
				continue
			}
			for _, pool := range pools {
				if !seen[pool] {
					seen[pool] = true
					out = append(out, pool)
				}
			}
		}
	}
	return out
}

// DeviceCheck is the safety verdict for one disk.
type DeviceCheck struct {
	Safe          bool
	Reason        string
	Canonical     string
	Name          string
	Mountpoints   []string
	UsedPools     []string
	HasPartitions bool
	IsRoot        bool
}

// Inspect validates a whole disk.
func (s *DeviceSet) Inspect(dev *BlockDevice, usage map[string][]string) DeviceCheck {
	check := DeviceCheck{
		Canonical:     dev.Path,
		Name:          dev.Name,
		Mountpoints:   s.mountpointsFor(dev),
		UsedPools:     s.usedPoolsFor(dev, usage),
		HasPartitions: len(s.children[dev.Name]) > 0,
		IsRoot:        s.rootDisk != "" && s.rootDisk == dev.Name,
	}
	reject := func(reason string) DeviceCheck {
		check.Safe = false
		check.Reason = reason
		return check
	}
	if dev.Type != "disk" {
		return reject(fmt.Sprintf("is a %s, not a whole disk", dev.Type))
	}
	if check.IsRoot {
		return reject("contains the root filesystem")
	}
	// Pool membership and mounts take precedence over the partition check:
	// in-use members always carry ZFS partitions, and suggesting a wipe for
	// them would be actively dangerous.
	if len(check.UsedPools) > 0 {
		return reject("is in use by pool " + strings.Join(check.UsedPools, ", "))
	}
	if len(check.Mountpoints) > 0 {
		return reject("is mounted at " + strings.Join(check.Mountpoints, ", "))
	}
	if check.HasPartitions {
		return reject("contains partitions; wipe the partition table first")
	}
	if dev.FSType != "" {
		if dev.FSType == "zfs_member" {
			return reject("has a ZFS label; run \"pg zfs raw zpool labelclear -f " + dev.Path + "\" and wipefs")
		}
		return reject("contains a filesystem (" + dev.FSType + "); use wipefs first")
	}
	if dev.ReadOnly {
		return reject("is read-only")
	}
	check.Safe = true
	return check
}

// ResolveDevice validates one user device and returns its canonical path.
func (s *DeviceSet) ResolveDevice(path string, usage map[string][]string) (string, error) {
	canonical, dev, err := s.Resolve(path)
	if err != nil {
		return "", err
	}
	check := s.Inspect(dev, usage)
	if !check.Safe {
		return "", fmt.Errorf("refusing device %q (%s): %s", path, canonical, check.Reason)
	}
	return canonical, nil
}

// ResolveTopology validates every device and rewrites the topology with
// canonical paths. Duplicate devices are rejected after symlink resolution.
func (s *DeviceSet) ResolveTopology(topo Topology, usage map[string][]string) (Topology, error) {
	replacements := map[string]string{}
	seen := map[string]string{}
	for _, path := range topo.AllDevices() {
		if previous, ok := seen[path]; ok {
			return Topology{}, fmt.Errorf("device %q appears more than once", previous)
		}
		canonical, err := s.ResolveDevice(path, usage)
		if err != nil {
			return Topology{}, err
		}
		if previous, ok := seen[canonical]; ok {
			return Topology{}, fmt.Errorf("devices %q and %q resolve to the same disk %s", previous, path, canonical)
		}
		seen[canonical] = path
		replacements[path] = canonical
	}
	return topo.MapDevices(replacements), nil
}

func readMountTable() map[string][]string {
	out := map[string][]string{}
	file, err := os.Open("/proc/mounts")
	if err != nil {
		return out
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		source := unescapeMount(fields[0])
		target := unescapeMount(fields[1])
		out[source] = append(out[source], target)
	}
	_ = scanner.Err()
	return out
}

func unescapeMount(value string) string {
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return replacer.Replace(value)
}

// ParseZFSUsage maps device paths to the pools using them.
func ParseZFSUsage(out []byte) map[string][]string {
	usage := map[string][]string{}
	current := ""
	seen := map[string]map[string]bool{}
	for line := range strings.SplitSeq(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if pool, ok := strings.CutPrefix(trimmed, "pool:"); ok {
			current = strings.TrimSpace(pool)
			continue
		}
		if current == "" || !strings.HasPrefix(trimmed, "/dev/") {
			continue
		}
		device := strings.Fields(trimmed)[0]
		if seen[device] == nil {
			seen[device] = map[string]bool{}
		}
		if !seen[device][current] {
			seen[device][current] = true
			usage[device] = append(usage[device], current)
		}
	}
	return usage
}

// LoadZFSUsage reads `zpool status -P`. It returns nil when ZFS is not
// installed, and an error when the command cannot be run.
func LoadZFSUsage(ctx context.Context, r *Runner) (map[string][]string, error) {
	if EnsureZFSBinaries() != nil {
		return nil, nil
	}
	zpool, err := LookPath("zpool")
	if err != nil {
		return nil, nil
	}
	out, err := r.OutputPrivileged(ctx, zpool, "status", "-P")
	if err != nil {
		if fallback, fallbackErr := r.Output(ctx, zpool, "status", "-P"); fallbackErr == nil {
			out = fallback
		} else {
			return nil, err
		}
	}
	return ParseZFSUsage(out), nil
}
