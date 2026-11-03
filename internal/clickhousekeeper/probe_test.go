package clickhousekeeper

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mars-base/pgcli/internal/config"
)

type fakeKeeper struct {
	host string
	port int
}

func startFakeKeeper(t *testing.T, handler func(command string) string) fakeKeeper {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4)
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				_, _ = c.Write([]byte(handler(string(buf))))
			}(conn)
		}
	}()
	addr := listener.Addr().(*net.TCPAddr)
	return fakeKeeper{host: "127.0.0.1", port: addr.Port}
}

func keeperResponder(command string) string {
	switch command {
	case "ruok":
		return "imok"
	case "mntr":
		return "zk_version\tClickHouse Keeper\n" +
			"zk_server_state\tleader\n" +
			"zk_followers\t2\n" +
			"zk_synced_followers\t2\n"
	default:
		return ""
	}
}

func TestRuok(t *testing.T) {
	fake := startFakeKeeper(t, keeperResponder)
	if err := Ruok(fake.host, fake.port, time.Second); err != nil {
		t.Fatalf("Ruok: %v", err)
	}

	bad := startFakeKeeper(t, func(string) string { return "bad" })
	if err := Ruok(bad.host, bad.port, time.Second); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("error = %v, want unexpected response error", err)
	}

	closed := startFakeKeeper(t, keeperResponder)
	if err := Ruok(closed.host, closed.port+1, 50*time.Millisecond); err == nil {
		t.Fatal("Ruok on a closed port succeeded")
	}
}

func TestMntr(t *testing.T) {
	fake := startFakeKeeper(t, keeperResponder)
	values, err := Mntr(fake.host, fake.port, time.Second)
	if err != nil {
		t.Fatalf("Mntr: %v", err)
	}
	if values["zk_server_state"] != "leader" {
		t.Errorf("zk_server_state = %q", values["zk_server_state"])
	}
	if values["zk_followers"] != "2" {
		t.Errorf("zk_followers = %q", values["zk_followers"])
	}
	state, err := KeeperState(fake.host, fake.port, time.Second)
	if err != nil {
		t.Fatalf("KeeperState: %v", err)
	}
	if state != "leader" {
		t.Errorf("KeeperState = %q, want leader", state)
	}
}

func TestKeeperClusterStatusStandalone(t *testing.T) {
	fake := startFakeKeeper(t, keeperResponder)
	kc := config.ClickHouseKeeperConfig{
		Listen:        "127.0.0.1",
		ServerID:      1,
		AdvertiseHost: "127.0.0.1",
		ClientPort:    fake.port,
		RaftPort:      9234,
	}
	status, err := KeeperClusterStatus(kc, time.Second)
	if err != nil {
		t.Fatalf("KeeperClusterStatus: %v", err)
	}
	if status.Reachable != 1 || status.Quorum != 1 || !status.ReachableQuorum {
		t.Errorf("status = %+v, want 1/1 reachable quorum", status)
	}
	if status.LocalState != "leader" {
		t.Errorf("LocalState = %q, want leader", status.LocalState)
	}
	if len(status.Members) != 1 || !status.Members[0].Reachable {
		t.Errorf("members = %+v", status.Members)
	}
}

func TestKeeperClusterStatusMultiMemberCountsReachability(t *testing.T) {
	fake := startFakeKeeper(t, keeperResponder)
	kc := config.ClickHouseKeeperConfig{
		Listen:        "127.0.0.1",
		ServerID:      1,
		AdvertiseHost: "10.0.0.1",
		Peers:         []string{"2@192.0.2.1", "3@192.0.2.2"},
		ClientPort:    fake.port,
		RaftPort:      9234,
	}
	status, err := KeeperClusterStatus(kc, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("KeeperClusterStatus: %v", err)
	}
	if status.Reachable != 1 {
		t.Errorf("Reachable = %d, want 1", status.Reachable)
	}
	if status.Quorum != 2 {
		t.Errorf("Quorum = %d, want 2", status.Quorum)
	}
	if status.ReachableQuorum {
		t.Error("ReachableQuorum = true with 1/3 reachable")
	}
	if len(status.Members) != 3 {
		t.Fatalf("members = %d, want 3", len(status.Members))
	}
	if !status.Members[0].Reachable || status.Members[1].Reachable || status.Members[2].Reachable {
		t.Errorf("member reachability = %+v", status.Members)
	}
}

func TestKeeperClusterStatusUnreachable(t *testing.T) {
	fake := startFakeKeeper(t, keeperResponder)
	kc := config.ClickHouseKeeperConfig{
		Listen:        "127.0.0.1",
		ServerID:      1,
		AdvertiseHost: "127.0.0.1",
		ClientPort:    fake.port + 1,
		RaftPort:      9234,
	}
	status, err := KeeperClusterStatus(kc, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("KeeperClusterStatus: %v", err)
	}
	if status.Reachable != 0 || status.ReachableQuorum {
		t.Errorf("status = %+v, want zero reachable", status)
	}
	if status.LocalState != "" {
		t.Errorf("LocalState = %q, want empty", status.LocalState)
	}
}

func TestKeeperClusterStatusRejectsInvalidPort(t *testing.T) {
	kc := config.ClickHouseKeeperConfig{Listen: "127.0.0.1", ServerID: 1, AdvertiseHost: "127.0.0.1", ClientPort: 0, RaftPort: 9234}
	if _, err := KeeperClusterStatus(kc, time.Second); err == nil || !strings.Contains(err.Error(), "client_port") {
		t.Fatalf("error = %v, want client_port error", err)
	}
}
