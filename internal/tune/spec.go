// Package tune ports the pgtune configuration-tuning model (le0pard/pgtune,
// MIT) into a pure, dependency-free recommender: given a machine's specs and a
// workload type it computes a set of postgresql.conf parameters. Nothing here
// touches a database or the filesystem — the caller renders and applies.
package tune

import (
	"fmt"
	"math"
	"slices"
)

// Workload is the shape of traffic the database serves; it selects the memory
// partition and WAL/parallelism presets.
type Workload string

const (
	TypeWeb     Workload = "web"
	TypeOLTP    Workload = "oltp"
	TypeDW      Workload = "dw"
	TypeDesktop Workload = "desktop"
	TypeMixed   Workload = "mixed"
)

// Disk is the storage class; it drives random_page_cost and
// effective_io_concurrency.
type Disk string

const (
	HDSSD  Disk = "ssd"
	HDHDD  Disk = "hdd"
	HDSAN  Disk = "san"
	HDNVME Disk = "nvme"
)

// DBSize is the whole-database size relative to RAM — le0pard's three buckets.
type DBSize string

const (
	SizeLess    DBSize = "less_ram"    // fits in RAM
	SizeMid     DBSize = "mid_ram"     // a few times RAM
	SizeGreater DBSize = "greater_ram" // far larger than RAM
)

// OS is the target platform; it gates effective_io_concurrency (Linux only)
// and the Windows sub-2GB allocation caps.
type OS string

const (
	OSLinux   OS = "linux"
	OSWindows OS = "windows"
	OSMac     OS = "mac"
)

// Inputs are the resolved specs a recommendation is computed from. Zero
// enum fields are normalised by Recommend to safe defaults; RAM and CPUs must
// be positive. DBVersion is a server_version_num (e.g. 180006).
type Inputs struct {
	DBVersion int
	OS        OS
	Type      Workload
	RAMBytes  int64
	CPUs      int
	Disk      Disk
	Size      DBSize
	MaxConns  int // 0 → workload preset
}

// Param is one recommended setting. Value is already unit-formatted for
// postgresql.conf. Reason is a one-line justification; Warning is set for the
// caveats le0pard prints (compile flags, DW cost defaults). RequiresRestart
// marks postmaster-level settings that reload will not pick up.
type Param struct {
	Name            string
	Value           string
	Reason          string
	Warning         string
	RequiresRestart bool
}

// Normalize fills in safe defaults for blank enum fields (mixed/ssd/mid/linux),
// clamps CPUs/version, and validates the enums. Callers that display or persist
// Inputs should normalize before doing so — Recommend normalizes its own copy
// but the caller's value is otherwise left raw (blank Type/Disk/Size).
func (in *Inputs) Normalize() error {
	return in.normalize()
}

func (in *Inputs) normalize() error {
	if in.RAMBytes <= 0 {
		return fmt.Errorf("tune: RAM must be positive, got %d bytes", in.RAMBytes)
	}
	if in.CPUs < 1 {
		in.CPUs = 1
	}
	if in.DBVersion <= 0 {
		in.DBVersion = 180000
	}
	if in.Type == "" {
		in.Type = TypeMixed
	}
	if in.Disk == "" {
		in.Disk = HDSSD
	}
	if in.Size == "" {
		in.Size = SizeMid
	}
	if in.OS == "" {
		in.OS = OSLinux
	}
	if !valid(in.Type, TypeWeb, TypeOLTP, TypeDW, TypeDesktop, TypeMixed) {
		return fmt.Errorf("tune: unknown workload type %q", in.Type)
	}
	if !valid(in.Disk, HDSSD, HDHDD, HDSAN, HDNVME) {
		return fmt.Errorf("tune: unknown disk type %q", in.Disk)
	}
	if !valid(in.Size, SizeLess, SizeMid, SizeGreater) {
		return fmt.Errorf("tune: unknown db size class %q", in.Size)
	}
	if in.MaxConns < 0 {
		in.MaxConns = 0
	}
	return nil
}

func valid[T comparable](v T, set ...T) bool {
	return slices.Contains(set, v)
}

// MaxConnectionsPreset is le0pard's per-workload connection default.
func MaxConnectionsPreset(t Workload) int {
	switch t {
	case TypeWeb:
		return 200
	case TypeOLTP:
		return 300
	case TypeDW:
		return 40
	case TypeDesktop:
		return 20
	default: // mixed
		return 100
	}
}

// internal size helpers — le0pard computes in kB; we mirror that exactly.
const (
	kb = 1024.0
	mb = 1024 * kb
	gb = 1024 * mb
)

func (in Inputs) ramKB() float64 { return float64(in.RAMBytes) / kb }

// majorVersion is the PostgreSQL major number (server_version_num / 10000),
// which is what all of le0pard's version comparisons use.
func (in Inputs) major() int { return in.DBVersion / 10000 }

// formatValue renders a kB count the way le0pard's view.jsx does: exact GB →
// "NgB", else exact MB → "nMB", else the raw kB count.
func formatValue(valueKb int) string {
	switch {
	case valueKb%1048576 == 0:
		return fmt.Sprintf("%dGB", valueKb/1048576)
	case valueKb%1024 == 0:
		return fmt.Sprintf("%dMB", valueKb/1024)
	default:
		return fmt.Sprintf("%dkB", valueKb)
	}
}

// floorKb truncates a float kB value to an integer kB (le0pard floors).
func floorKb(v float64) int { return int(math.Floor(v)) }
