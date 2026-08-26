package podman

import "strings"

// isPodmanCleanupNoise reports whether stderr only contains podman cleanup
// messages after a container has already exited. This happens when podman
// receives a terminal signal (e.g. SIGWINCH) while removing a `run --rm`
// container and tries to forward it to the now-gone container.
func isPodmanCleanupNoise(stderr string) bool {
	if stderr == "" {
		return false
	}
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, "forwarding signal") &&
			(strings.Contains(line, "no such container") || strings.Contains(line, "container has already been removed")) {
			continue
		}
		if strings.Contains(line, "container has already been removed") {
			continue
		}
		return false
	}
	return true
}
