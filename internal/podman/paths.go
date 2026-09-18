package podman

import (
	"path/filepath"
)

// hostMountPath returns the cleaned absolute host path for podman -v mounts.
func hostMountPath(hostPath string) string {
	abs, _ := filepath.Abs(hostPath)
	if abs == "" {
		return hostPath
	}
	return abs
}

// HostMountPath is the exported form, for callers that persist a user-supplied
// mount source already absolutized (the CLI records addon cert paths this way).
func HostMountPath(hostPath string) string {
	return hostMountPath(hostPath)
}
