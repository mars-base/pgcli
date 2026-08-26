//go:build !windows

package podman

// EnsurePodmanService is a no-op on Unix platforms where podman talks to the
// local podman socket directly.
func EnsurePodmanService() error {
	return nil
}

// EnsurePGPortProxy is a no-op on Unix — no portproxy needed.
func (m *Manager) EnsurePGPortProxy() {}
