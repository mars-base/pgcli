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
