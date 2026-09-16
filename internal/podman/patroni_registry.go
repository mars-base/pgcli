// Cross-host Patroni member registry. Patroni already records each member's
// connect_address (host + PG port) in the DCS, so `patronictl list` from any
// host sees the whole cluster's topology. What Patroni does NOT know is
// pgcli's per-member SSH/REST ports -- private to pgcli, needed by the backup
// container to SSH into a member that lives on another host. This registry
// stores exactly those ports in etcd under a pgcli-owned prefix, written by
// `pg ha create` and read back by the backup config generators.
//
// Layout:  /pgcli/ha/<nsScope>/<member>  ->  {"ssh_port":42301,"restapi_port":8008,"backup_pubkey":"ssh-rsa AAAA..."}
//
//	/pgcli/ha/<nsScope>/.repo/ca   ->  {"ca_pem":"-----BEGIN CERTIFICATE-----..."}
//
// It deliberately lives outside Patroni's /service/<scope> keyspace so the two
// never collide, and stores no secret.
//
// SECURITY: the DCS link is plaintext HTTP with no authentication (see
// etcdkv.go). Anything here is readable and writable by every host that can
// reach the etcd client port. Only PUBLIC material is ever stored — each host's
// backup SSH *public* key and the repository's self-signed *CA certificate*.
// Never put a private key, password, or the S3 secret_key in this registry.
package podman

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
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
	Name         string
	Host         string // reachable IP/FQDN from connect_address
	HostPort     int    // PostgreSQL port
	SSHPort      int    // from the registry; 0 if the member's host never registered
	RestapiPort  int    // from the registry (or local config); 0 if unknown
	BackupPubKey string // the member's host backup-container SSH public key (authorized_keys line)
	Role         string // leader / replica / …
	Local        bool   // this member has a container on the current host
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
			cm.BackupPubKey = p.BackupPubKey
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

// memberPorts is the pgcli-private per-member info mirrored into the DCS. Only
// public material lives here (see the package SECURITY note): the ports and the
// host's backup-container SSH *public* key — never any private key or secret.
type memberPorts struct {
	SSHPort      int    `json:"ssh_port"`
	RestapiPort  int    `json:"restapi_port,omitempty"`
	HostPort     int    `json:"host_port,omitempty"` // redundancy: also in patronictl list, kept for a self-contained read
	BackupPubKey string `json:"backup_pubkey,omitempty"`
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
	// The member's host backup-container public key (a single authorized_keys
	// line). Best effort: an older host that has not generated its key yet
	// registers without it, and the cluster just lacks an auto-trust entry until
	// its next backup setup publishes one.
	var pubkey string
	if bm, err := NewBackupManager(m.cfg); err == nil {
		if data, err := os.ReadFile(bm.SSHKeyPaths().Public); err == nil {
			pubkey = strings.TrimSpace(string(data))
		}
	}
	blob, err := json.Marshal(memberPorts{
		SSHPort:      mb.SSHPort,
		RestapiPort:  mb.RestapiPort,
		HostPort:     mb.HostPort,
		BackupPubKey: pubkey,
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

// repoCAKeyPath is the scope-level key holding the backup repository's
// self-signed CA certificate. It lives under the same per-scope prefix as the
// member keys (so UnregisterScopePorts clears it too), but a leading-dot,
// slash-containing name Patroni never uses for a member, so it cannot collide.
func repoCAKeyPath(nsScope string) string {
	return registryKeyPath(nsScope) + ".repo/ca"
}

type repoCAValue struct {
	CAPEM string `json:"ca_pem"`
}

// PublishRepoCA stores the S3 repository's CA certificate (public material) in
// the DCS under the scope, so hosts that later join pick it up instead of a
// operator hand-copying ca.crt. Called by `pg backup setup` on the host that
// configured ca_file.
func (m *PatroniManager) PublishRepoCA(cluster *config.PatroniClusterConfig, caPEM string) error {
	nsScope := m.cfg.PatroniScope(cluster.Name)
	cli, err := m.registryClient(cluster)
	if err != nil {
		return err
	}
	blob, err := json.Marshal(repoCAValue{CAPEM: caPEM})
	if err != nil {
		return fmt.Errorf("encoding repo CA: %w", err)
	}
	return cli.put(repoCAKeyPath(nsScope), string(blob))
}

// RepoCAFromDCS reads the scope's published repository CA, or "" when none has
// been published yet. Best effort: a DCS error is not surfaced (the caller
// falls back to its own local ca_file configuration).
func (m *PatroniManager) RepoCAFromDCS(cluster *config.PatroniClusterConfig) string {
	nsScope := m.cfg.PatroniScope(cluster.Name)
	cli, err := m.registryClient(cluster)
	if err != nil {
		return ""
	}
	kvs, err := cli.getPrefix(repoCAKeyPath(nsScope))
	if err != nil {
		return ""
	}
	v, ok := kvs[repoCAKeyPath(nsScope)]
	if !ok {
		return ""
	}
	var ca repoCAValue
	if json.Unmarshal([]byte(v), &ca) != nil {
		return ""
	}
	return ca.CAPEM
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
		if member == "" || strings.ContainsAny(member, "./") {
			continue // scope-level keys like ".repo/ca" are not members
		}
		var p memberPorts
		if json.Unmarshal([]byte(v), &p) == nil {
			out[member] = p
		}
	}
	return out
}
