package clickhousekeeper

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
)

// MemberStatus is the connectivity result for one Keeper voter. State is
// populated only for the local member, via mntr.
type MemberStatus struct {
	ServerID   int
	Host       string
	ClientPort int
	Reachable  bool
	State      string
}

// ClusterStatus summarizes one Keeper's view of its members. ReachableQuorum
// only means the client ports answered a 4lw ruok probe; it is not a Raft
// quorum proof.
type ClusterStatus struct {
	Members         []MemberStatus
	Reachable       int
	Quorum          int
	ReachableQuorum bool
	LocalState      string
}

// Ruok sends the 4lw ruok command and checks for "imok". Keeper answers ruok
// even while mntr blocks without a Raft quorum, so this is the readiness probe.
func Ruok(host string, port int, timeout time.Duration) error {
	body, err := fourLetterWord(host, port, "ruok", timeout)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(body)) != "imok" {
		return fmt.Errorf("%s returned an unexpected ruok response", net.JoinHostPort(host, strconv.Itoa(port)))
	}
	return nil
}

// Mntr sends the 4lw mntr command and parses key<TAB>value lines.
func Mntr(host string, port int, timeout time.Duration) (map[string]string, error) {
	body, err := fourLetterWord(host, port, "mntr", timeout)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("%s returned an empty mntr response", net.JoinHostPort(host, strconv.Itoa(port)))
	}
	return values, nil
}

// KeeperState returns zk_server_state from mntr (standalone, leader, follower
// or observer).
func KeeperState(host string, port int, timeout time.Duration) (string, error) {
	values, err := Mntr(host, port, timeout)
	if err != nil {
		return "", err
	}
	state := values["zk_server_state"]
	if state == "" {
		return "", fmt.Errorf("mntr response has no zk_server_state")
	}
	return state, nil
}

// KeeperClusterStatus probes every voter's client port (ruok) and reads the
// local member's Raft state over mntr. Client reachability is reported
// separately from a quorum guarantee; callers must not present it as proof.
func KeeperClusterStatus(kc config.ClickHouseKeeperConfig, timeout time.Duration) (ClusterStatus, error) {
	if kc.ClientPort < 1 || kc.ClientPort > 65535 {
		return ClusterStatus{}, fmt.Errorf("client_port must be between 1 and 65535, got %d", kc.ClientPort)
	}
	voters, err := config.ClickHouseKeeperVoters(kc)
	if err != nil {
		return ClusterStatus{}, err
	}

	status := ClusterStatus{
		Members: make([]MemberStatus, 0, len(voters)),
		Quorum:  len(voters)/2 + 1,
	}
	localHost := LocalProbeHost(kc.Listen)
	for _, voter := range voters {
		host := voter.Host
		if voter.ID == kc.ServerID {
			host = localHost
		}
		member := MemberStatus{ServerID: voter.ID, Host: host, ClientPort: kc.ClientPort}
		if err := Ruok(host, kc.ClientPort, timeout); err == nil {
			member.Reachable = true
			status.Reachable++
		}
		if voter.ID == kc.ServerID {
			if state, err := KeeperState(host, kc.ClientPort, timeout); err == nil {
				member.State = state
				status.LocalState = state
			}
		}
		status.Members = append(status.Members, member)
	}
	status.ReachableQuorum = status.Reachable >= status.Quorum
	return status, nil
}

func fourLetterWord(host string, port int, word string, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	address := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", address, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	if _, err := conn.Write([]byte(word)); err != nil {
		return nil, fmt.Errorf("sending %s to %s: %w", word, address, err)
	}
	body, err := io.ReadAll(conn)
	if err != nil {
		return nil, fmt.Errorf("reading %s response from %s: %w", word, address, err)
	}
	return body, nil
}
