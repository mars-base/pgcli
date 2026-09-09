package podman

import (
	"reflect"
	"testing"
)

func TestNetFlags(t *testing.T) {
	tests := []struct {
		name      string
		useBridge bool
		network   string
		ports     []int
		want      []string
	}{
		{
			name:      "linux ignores ports, returns bare host network",
			useBridge: false,
			network:   "pgcli-net",
			ports:     []int{6432, 7432},
			want:      []string{"--network", "host"},
		},
		{
			name:      "macOS bridge with no ports publishes nothing",
			useBridge: true,
			network:   "pgcli-net",
			ports:     nil,
			want:      []string{"--network", "pgcli-net"},
		},
		{
			name:      "macOS bridge publishes one port",
			useBridge: true,
			network:   "pgcli-net",
			ports:     []int{6432},
			want:      []string{"--network", "pgcli-net", "-p", "6432:6432"},
		},
		{
			name:      "macOS bridge publishes multiple ports in order",
			useBridge: true,
			network:   "pgcli-net",
			ports:     []int{7432, 7433},
			want:      []string{"--network", "pgcli-net", "-p", "7432:7432", "-p", "7433:7433"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := netFlags(tt.useBridge, tt.network, tt.ports...)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("netFlags(%v, %q, %v) = %v, want %v", tt.useBridge, tt.network, tt.ports, got, tt.want)
			}
		})
	}
}

func TestBackendBindHost(t *testing.T) {
	tests := []struct {
		name          string
		useBridge     bool
		host          string
		containerName string
		want          string
	}{
		{"linux keeps host as-is", false, "127.0.0.1", "pgcli-pg-default", "127.0.0.1"},
		{"macOS loopback rewrites to container name", true, "127.0.0.1", "pgcli-pg-default", "pgcli-pg-default"},
		{"macOS localhost rewrites to container name", true, "localhost", "pgcli-pg-default", "pgcli-pg-default"},
		{"macOS explicit remote host is untouched", true, "10.0.0.5", "pgcli-pg-default", "10.0.0.5"},
		{"macOS with no container name (remote mode) keeps host", true, "127.0.0.1", "", "127.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := backendBindHost(tt.useBridge, tt.host, tt.containerName)
			if got != tt.want {
				t.Errorf("backendBindHost(%v, %q, %q) = %q, want %q", tt.useBridge, tt.host, tt.containerName, got, tt.want)
			}
		})
	}
}

func TestProxyBindHost(t *testing.T) {
	tests := []struct {
		name      string
		useBridge bool
		host      string
		want      string
	}{
		{"linux default loopback untouched", false, "127.0.0.1", "127.0.0.1"},
		{"linux explicit bind untouched", false, "10.0.0.5", "10.0.0.5"},
		{"macOS empty default widened", true, "", "0.0.0.0"},
		{"macOS loopback widened", true, "127.0.0.1", "0.0.0.0"},
		{"macOS localhost widened", true, "localhost", "0.0.0.0"},
		{"macOS explicit non-loopback bind respected", true, "10.0.0.5", "10.0.0.5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := proxyBindHost(tt.useBridge, tt.host)
			if got != tt.want {
				t.Errorf("proxyBindHost(%v, %q) = %q, want %q", tt.useBridge, tt.host, got, tt.want)
			}
		})
	}
}
