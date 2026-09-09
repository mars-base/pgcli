package podman

import (
	"strconv"
)

// macOS runs podman inside a machine VM where `--network host` binds to the
// VM's loopback — invisible to the Mac. The instance path already solves this
// by switching to the pgcli-net bridge and publishing each port N:N so gvproxy
// forwards it to the Mac (podman.go:1926-1950). The proxy addons mirror that
// pattern. These helpers keep the decision in pure functions driven by a
// `useBridge` bool, because platform.Detect() reads runtime.GOOS and cannot be
// injected in unit tests that run on Linux.

// netFlags returns the network args for `podman run`.
//
//	Linux  (useBridge=false): exactly ["--network", "host"] — no publish args.
//	macOS  (useBridge=true):  ["--network", network] plus one "-p P:P" per port.
func netFlags(useBridge bool, network string, ports ...int) []string {
	if !useBridge {
		return []string{"--network", "host"}
	}
	args := []string{"--network", network}
	for _, p := range ports {
		s := strconv.Itoa(p)
		args = append(args, "-p", s+":"+s)
	}
	return args
}

// backendBindHost rewrites a backend host that points at loopback when the
// consumer is a bridge-attached container and cannot reach the host's
// loopback: on macOS the PG instance lives on pgcli-net and is reachable by
// its container name, so the loopback host is replaced with it. Every other
// case — Linux, an explicit non-loopback host (remote backend), or no
// container name to substitute (pgbouncer in remote mode) — is returned
// unchanged.
func backendBindHost(useBridge bool, host, containerName string) string {
	if !useBridge || containerName == "" {
		return host
	}
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		return containerName
	}
	return host
}

// proxyBindHost widens a proxy's own listen bind for bridge networking: with
// the port published via `-p N:N`, the VM's forwarding cannot reach a process
// bound only to 127.0.0.1 inside the container, so the empty default and every
// loopback value are rendered as 0.0.0.0. Non-loopback hosts (an explicit bind
// the user chose) pass through untouched, and the Linux path never rewrites.
func proxyBindHost(useBridge bool, host string) string {
	if !useBridge {
		return host
	}
	if host == "" || host == "127.0.0.1" || host == "localhost" || host == "::1" {
		return "0.0.0.0"
	}
	return host
}
