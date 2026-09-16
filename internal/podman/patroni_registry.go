// Cross-host Patroni member registry. Patroni already records each member's
// connect_address (host + PG port) in the DCS, so `patronictl list` from any
// host sees the whole cluster's topology. What Patroni does NOT know is
// pgcli's per-member SSH/REST ports -- private to pgcli, needed by the backup
// container to SSH into a member that lives on another host. This registry
// stores exactly those ports in etcd under a pgcli-owned prefix, written by
// `pg ha create` and read back by the backup config generators.
//
// Layout:  /pgcli/ha/<nsScope>/<member>  ->  {"ssh_port":42301,"restapi_port":8008}
//
// It deliberately lives outside Patroni's /service/<scope> keyspace so the two
// never collide, and stores no secret -- only ports.
package podman

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
)

// ClusterMember is one member of a Patroni cluster as seen cluster-wide: its
// DCS-advertised reach address plus pgcli's private SSH port, resolved by
// merging `patronictl list` topology with the member registry. Unlike a
// config.PatroniMemberConfig (local-only), a ClusterMember may live on another
// host -- this is what the backup config generators iterate to reach the leader
// wherever it runs.
type ClusterMember struct {
	Name        string
	Host        string // reachable IP/FQDN from connect_address
	HostPort    int    // PostgreSQL port
	SSHPort     int    // from the registry; 0 if the member's host never registered
	RestapiPort int    // from the registry (or local config); 0 if unknown
	Role        string // leader / replica / …
	Local       bool   // this member has a container on the current host
}

// DiscoverAllMembers returns every member of the cluster across all hosts.
// Topology (name, host, pg port, role) comes from `patronictl list -f json`
// against the shared DCS; SSH/REST ports come from the pgcli registry. Local
// members fall back to their own config entry when the registry lacks them
// (e.g. a member created before the registry existed).
//
// A DCS that is unreachable yields an error; callers that merely want a best
// effort (backup config) can fall back to the local-only view.
func (m *PatroniManager) DiscoverAllMembers(cluster *config.PatroniClusterConfig) ([]ClusterMember, error) {
	nsScope := m.cfg.PatroniScope(cluster.Name)
	out, err := m.PatronictlCapture(cluster, "list", nsScope, "-f", "json")
	if err != nil {
		return nil, fmt.Errorf("querying cluster topology: %w", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, fmt.Errorf("parsing patronictl list: %w", err)
	}
	ports := m.memberPortsFromDCS(cluster)

	var members []ClusterMember
	seen := map[string]bool{}
	for _, row := range rows {
		name, _ := row["Member"].(string)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		role, _ := row["Role"].(string)
		hostField, _ := row["Host"].(string)
		host, portStr, splitErr := net.SplitHostPort(hostField)
		pgPort := 0
		if splitErr == nil {
			pgPort, _ = strconv.Atoi(portStr)
		}

		cm := ClusterMember{Name: name, Host: host, HostPort: pgPort, Role: role}
		// Registry ports first (authoritative, cross-host-capable)…
		if p, ok := ports[name]; ok {
			cm.SSHPort = p.SSHPort
			cm.RestapiPort = p.RestapiPort
			if cm.HostPort == 0 {
				cm.HostPort = p.HostPort
			}
		}
		// …then local config as a fallback for members on this host.
		if mb, ok := cluster.Members[name]; ok {
			cm.Local = mb.RemoteHost == ""
			if cm.SSHPort == 0 {
				cm.SSHPort = mb.SSHPort
			}
			if cm.RestapiPort == 0 {
				cm.RestapiPort = mb.RestapiPort
			}
			if cm.HostPort == 0 {
				cm.HostPort = mb.HostPort
			}
			if cm.Host == "" {
				cm.Host = mb.AdvertiseHost
			}
		}
		members = append(members, cm)
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("cluster %q reports no members in the DCS", cluster.Name)
	}
	return members, nil
}

// memberPorts is the pgcli-private per-member info mirrored into the DCS.
type memberPorts struct {
	SSHPort     int `json:"ssh_port"`
	RestapiPort int `json:"restapi_port,omitempty"`
	HostPort    int `json:"host_port,omitempty"` // redundancy: also in patronictl list, kept for a self-contained read
}

// registryKeyPath is the etcd prefix for one scope's member registry.
func registryKeyPath(nsScope string) string {
	return "/pgcli/ha/" + nsScope + "/"
}

func (m *PatroniManager) registryClient(cluster *config.PatroniClusterConfig) (*etcdClient, error) {
	csv, err := m.dcsEndpoints(cluster)
	if err != nil {
		return nil, err
	}
	return newEtcdClient(strings.Split(csv, ",")), nil
}

// RegisterMemberPorts writes a member's pgcli-private ports to the DCS. Called
// after a successful `pg ha create`/recreate so other hosts' backup config
// generators can reach this member over SSH without manual registration. Ports
// are read from the member's own config entry. Errors are returned for the
// caller to (non-fatally) report -- the member is already up, so a registry
// failure must not fail the install.
func (m *PatroniManager) RegisterMemberPorts(cluster *config.PatroniClusterConfig, member string) error {
	mb, ok := cluster.Members[member]
	if !ok {
		return fmt.Errorf("member %q is not part of cluster %q", member, cluster.Name)
	}
	nsScope := m.cfg.PatroniScope(cluster.Name)
	cli, err := m.registryClient(cluster)
	if err != nil {
		return err
	}
	blob, err := json.Marshal(memberPorts{
		SSHPort:     mb.SSHPort,
		RestapiPort: mb.RestapiPort,
		HostPort:    mb.HostPort,
	})
	if err != nil {
		return fmt.Errorf("encoding member ports: %w", err)
	}
	return cli.put(registryKeyPath(nsScope)+member, string(blob))
}

// UnregisterMemberPorts drops one member's registry entry (on `pg ha remove`).
func (m *PatroniManager) UnregisterMemberPorts(cluster *config.PatroniClusterConfig, member string) error {
	nsScope := m.cfg.PatroniScope(cluster.Name)
	cli, err := m.registryClient(cluster)
	if err != nil {
		return err
	}
	return cli.delete(registryKeyPath(nsScope) + member)
}

// UnregisterScopePorts drops every member entry for a scope (on `--scope-all`).
func (m *PatroniManager) UnregisterScopePorts(cluster *config.PatroniClusterConfig) error {
	nsScope := m.cfg.PatroniScope(cluster.Name)
	cli, err := m.registryClient(cluster)
	if err != nil {
		return err
	}
	return cli.deletePrefix(registryKeyPath(nsScope))
}

// memberPortsFromDCS reads back every registered member's ports for a scope,
// keyed by member name. Stale entries (a member removed on its host without a
// matching unregister) are tolerated: the caller only keeps entries whose
// member also appears in patronictl list.
func (m *PatroniManager) memberPortsFromDCS(cluster *config.PatroniClusterConfig) map[string]memberPorts {
	nsScope := m.cfg.PatroniScope(cluster.Name)
	cli, err := m.registryClient(cluster)
	if err != nil {
		return nil
	}
	kvs, err := cli.getPrefix(registryKeyPath(nsScope))
	if err != nil {
		return nil
	}
	out := make(map[string]memberPorts, len(kvs))
	prefix := registryKeyPath(nsScope)
	for k, v := range kvs {
		member := strings.TrimPrefix(k, prefix)
		if member == "" {
			continue
		}
		var p memberPorts
		if json.Unmarshal([]byte(v), &p) == nil {
			out[member] = p
		}
	}
	return out
}
