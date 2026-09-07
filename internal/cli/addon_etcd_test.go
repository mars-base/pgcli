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

func TestResolveUnderBase(t *testing.T) {
	if got := resolveUnderBase("/home/pg", "etcd-data"); got != "/home/pg/etcd-data" {
		t.Errorf("relative path = %q, want /home/pg/etcd-data", got)
	}
	if got := resolveUnderBase("/home/pg", "/abs/data"); got != "/abs/data" {
		t.Errorf("absolute path = %q, want /abs/data (kept as-is)", got)
	}
}
