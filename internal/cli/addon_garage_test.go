package cli

import (
	"testing"
)

func TestGarageInstallRole(t *testing.T) {
	tests := []struct {
		name    string
		cluster string
		peers   []string
		want    garageRole
	}{
		{"no cluster is single-node", "", nil, garageSingle},
		{"cluster without peers bootstraps", "c1", nil, garageBootstrap},
		{"cluster with peers joins", "c1", []string{"abcd@10.0.0.1:3901"}, garageJoin},
	}
	for _, tc := range tests {
		if got := garageInstallRole(tc.cluster, tc.peers); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestAppendPeers(t *testing.T) {
	got := appendPeers([]string{"a@1"}, []string{"a@1", "b@2"})
	if len(got) != 2 || got[0] != "a@1" || got[1] != "b@2" {
		t.Errorf("merge/dedupe = %v", got)
	}
	// Flag-free re-run must not disturb the persisted list.
	if got := appendPeers(got, nil); len(got) != 2 {
		t.Errorf("nil added changed list: %v", got)
	}
}
