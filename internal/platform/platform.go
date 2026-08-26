// Package platform provides cross-platform adaptation: OS detection, dependency checks, default paths.
package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
)

// OS represents the operating system type.
type OS int

const (
	Linux OS = iota // Linux (native Podman)
	MacOS           // macOS (requires podman machine)
)

// String returns the human-readable name of the OS.
func (o OS) String() string {
	switch o {
	case Linux:
		return "linux"
	case MacOS:
		return "macOS"
	default:
		return "unknown"
	}
}

// Detect returns the current operating system.
func Detect() OS {
	switch runtime.GOOS {
	case "linux":
		return Linux
	case "darwin":
		return MacOS
	default:
		return Linux // fallback
	}
}

// NeedsPodmanMachine returns whether podman machine is needed (macOS only).
func NeedsPodmanMachine() bool {
	return Detect() == MacOS
}

// --- Dependency checks ---

// DepStatus describes the status of a dependency.
type DepStatus struct {
	Name    string // Dependency name (e.g. "podman")
	Found   bool   // Whether it is installed
	Path    string // Binary path
	Version string // Version string
	Hint    string // Installation hint
}

// CheckPodman checks if podman is available, returns its path and version.
func CheckPodman() DepStatus {
	path, err := exec.LookPath("podman")
	if err != nil {
		return DepStatus{
			Name:  "podman",
			Found: false,
			Hint:  podmanInstallHint(),
		}
	}
	ver, _ := runCmd(path, "--version")
	return DepStatus{
		Name:    "podman",
		Found:   true,
		Path:    path,
		Version: ver,
	}
}

// CheckPodmanMachine checks podman machine status (macOS only).
func CheckPodmanMachine() DepStatus {
	path, err := exec.LookPath("podman")
	if err != nil {
		return DepStatus{
			Name:  "podman-machine",
			Found: false,
			Hint:  "podman is not installed",
		}
	}
	out, err := runCmd(path, "machine", "list")
	if err != nil {
		return DepStatus{
			Name:  "podman-machine",
			Found: false,
			Hint:  fmt.Sprintf("podman machine unavailable: %v", err),
		}
	}
	return DepStatus{
		Name:    "podman-machine",
		Found:   true,
		Path:    path,
		Version: out,
	}
}

// MissingPrereqs returns the list of missing dependencies.
func MissingPrereqs() []DepStatus {
	var missing []DepStatus
	for _, d := range []DepStatus{
		CheckPodman(),
	} {
		if !d.Found {
			missing = append(missing, d)
		}
	}
	if NeedsPodmanMachine() {
		if d := CheckPodmanMachine(); !d.Found {
			missing = append(missing, d)
		}
	}
	return missing
}

// --- Default paths ---

// DefaultConfigDir returns the pgcli configuration directory.
func DefaultConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pgcli")
}

// DefaultConfigPath returns the pgcli configuration file path.
func DefaultConfigPath() string {
	return filepath.Join(DefaultConfigDir(), "pg.yaml")
}

// GetUsedPorts returns the set of TCP ports currently listening on the container
// host. We probe to avoid port collisions.
//
//	Linux:   ss -tlnH directly on the host
//	macOS:   podman machine ssh <name> ss -tlnH (probes inside the VM)
func GetUsedPorts() map[int]bool {
	var cmd *exec.Cmd
	switch Detect() {
	case Linux:
		cmd = exec.Command("ss", "-tlnH")
	case MacOS:
		name := os.Getenv("PODMAN_MACHINE_NAME")
		if name == "" {
			name = "podman-machine-default"
		}
		cmd = exec.Command("podman", "machine", "ssh", name, "ss", "-tlnH")
	default:
		return nil
	}
	out, err := cmd.Output()
	if err != nil {
		return nil // can't probe, fall back to sequential assignment
	}
	// ss -tlnH output lines:  LISTEN  0  4096  127.0.0.1:5432  0.0.0.0:*
	re := regexp.MustCompile(`:(\d+)\s`)
	used := make(map[int]bool)
	for _, match := range re.FindAllStringSubmatch(string(out), -1) {
		if port, err := strconv.Atoi(match[1]); err == nil {
			used[port] = true
		}
	}
	return used
}

// --- Internal helpers ---

func runCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func podmanInstallHint() string {
	switch Detect() {
	case Linux:
		return "Install podman: curl -fsSL -o ~/.local/bin/podman https://github.com/89luca89/podman-launcher/releases/latest/download/podman-launcher-amd64 && chmod +x ~/.local/bin/podman"
	case MacOS:
		return "Install podman: brew install podman"
	default:
		return "Please install podman"
	}
}
