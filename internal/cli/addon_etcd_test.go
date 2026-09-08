package cli

import (
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func TestBootstrapClusterFirstMember(t *testing.T) {
	ec := config.EtcdConfig{Name: "m1", PeerPort: 2380, ClusterName: "pgcli-etcd"}
	cluster, state := bootstrapCluster(ec, nil)
	if state != "new" {
		t.Errorf("first member state = %q, want new", state)
	}
	if cluster != "m1=http://127.0.0.1:2380" {
		t.Errorf("first member initial-cluster = %q", cluster)
	}
}

func TestBootstrapClusterJoinMember(t *testing.T) {
	ec := config.EtcdConfig{Name: "m3", PeerPort: 2384, ClusterName: "pgcli-etcd"}
	peers := []config.EtcdConfig{
		{Name: "m1", PeerPort: 2380},
		{Name: "m2", PeerPort: 2382},
	}
	cluster, state := bootstrapCluster(ec, peers)
	if state != "existing" {
		t.Errorf("join member state = %q, want existing", state)
	}
	want := "m1=http://127.0.0.1:2380,m2=http://127.0.0.1:2382,m3=http://127.0.0.1:2384"
	if cluster != want {
		t.Errorf("join member initial-cluster =\n  %q\nwant\n  %q", cluster, want)
	}
}

func TestBootstrapClusterAdvertiseHost(t *testing.T) {
	ec := config.EtcdConfig{Name: "m2", PeerPort: 2380, AdvertiseHost: "10.0.0.2"}
	peers := []config.EtcdConfig{
		{Name: "m1", PeerPort: 2380, AdvertiseHost: "10.0.0.1"},
	}
	cluster, state := bootstrapCluster(ec, peers)
	if state != "existing" {
		t.Errorf("state = %q, want existing", state)
	}
	want := "m1=http://10.0.0.1:2380,m2=http://10.0.0.2:2380"
	if cluster != want {
		t.Errorf("initial-cluster =\n  %q\nwant\n  %q", cluster, want)
	}
}

func TestParseInitialCluster(t *testing.T) {
	out := `Member m2 added to cluster 1234abcd5678ef90

ETCD_NAME="m2"
ETCD_INITIAL_CLUSTER="m1=http://10.0.0.1:2380,m2=http://10.0.0.2:2380"
ETCD_INITIAL_CLUSTER_STATE="existing"
`
	got, ok := parseInitialCluster(out)
	if !ok {
		t.Fatal("parseInitialCluster did not find ETCD_INITIAL_CLUSTER")
	}
	want := "m1=http://10.0.0.1:2380,m2=http://10.0.0.2:2380"
	if got != want {
		t.Errorf("parsed = %q, want %q", got, want)
	}
}

func TestParseInitialClusterMissing(t *testing.T) {
	if _, ok := parseInitialCluster("Error: context deadline exceeded"); ok {
		t.Error("parseInitialCluster succeeded on output without ETCD_INITIAL_CLUSTER")
	}
}

func TestResolveUnderBase(t *testing.T) {
	if got := resolveUnderBase("/home/pg", "etcd-data"); got != "/home/pg/etcd-data" {
		t.Errorf("relative path = %q, want /home/pg/etcd-data", got)
	}
	if got := resolveUnderBase("/home/pg", "/abs/data"); got != "/abs/data" {
		t.Errorf("absolute path = %q, want /abs/data (kept as-is)", got)
	}
}
