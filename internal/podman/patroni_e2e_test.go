package podman

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
	yaml "gopkg.in/yaml.v3"
)

// initdb mixes value flags (encoding=UTF8) with boolean flags. A boolean
// rendered as {data-checksums: ""} becomes `--data-checksums=`, which PG 18's
// initdb rejects ("option '--data-checksums' doesn't allow an argument"); only
// the bare list form renders flag-only. This was found in E2E, so it must not
// silently regress.
func TestPatroniInitdbRendersBareBooleanFlag(t *testing.T) {
	cfg := config.Default()
	m := &PatroniManager{cfg: cfg, dataDir: t.TempDir()}
	cluster := testPatroniCluster()
	cluster.EtcdEndpoints = []string{"127.0.0.1:2379"}

	doc, err := m.renderPatroniYML(cluster, "node1", cluster.Members["node1"], cfg.PatroniScope("app"), "127.0.0.1:2379")
	if err != nil {
		t.Fatalf("renderPatroniYML: %v", err)
	}
	// Marshal then re-parse: the assertion must hold for the YAML Patroni
	// actually reads, not just for Go's in-memory representation.
	out, _ := yaml.Marshal(doc)
	var back map[string]any
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	initdb := back["bootstrap"].(map[string]any)["initdb"].([]any)

	var sawEncoding, sawBareChecksums bool
	for _, item := range initdb {
		switch v := item.(type) {
		case map[string]any:
			// A key mapped to the empty string is what becomes `--flag=`.
			for k, val := range v {
				if fmt.Sprint(val) == "" {
					t.Errorf("initdb option %q rendered as an empty-valued map — becomes `--%s=`, which PG 18 initdb rejects; booleans must be bare list items", k, k)
				}
			}
			if v["encoding"] == "UTF8" {
				sawEncoding = true
			}
		case string:
			if v == "data-checksums" {
				sawBareChecksums = true
			}
		}
	}
	if !sawEncoding {
		t.Error("expected initdb to set encoding=UTF8")
	}
	if !sawBareChecksums {
		t.Error("expected a bare `data-checksums` initdb item")
	}
}

// Rootless podman maps the image's baked-in postgres uid (999) onto a host
// subuid that does NOT own the 0600 patroni.yml pgcli writes, so a container
// running as postgres crash-loops on PermissionError. Both the member daemon
// and the ephemeral patronictl container must instead run as the host file
// owner: --userns=keep-id plus --user <uid>:<gid>.
// patronictl's third confirmation prompt on a HEALTHY cluster asks for the
// leader's member name, which RemoveScopeFromDCS must discover up front from
// `list -f json`. The rows mix value types ("TL" is a number), so a strict
// map[string]string decode fails the entire array and silently yields an empty
// leader — the scripted remove then aborts at the prompt and orphans DCS keys.
func TestPatroniLeaderFromListJSON(t *testing.T) {
	healthy := `[{"Cluster": "e2e", "Member": "n1", "Host": "127.0.0.1:35599", "Role": "Leader", "State": "running", "TL": 1},` +
		`{"Cluster": "e2e", "Member": "n2", "Host": "127.0.0.1:35598", "Role": "Replica", "State": "streaming", "TL": 1}]`
	if got := patroniLeaderFromListJSON(healthy); got != "n1" {
		t.Errorf("leader from healthy list = %q, want n1 (numeric TL must not break decoding)", got)
	}

	// A stopped replica reports Role "stopped", Member present, no leader.
	dead := `[{"Cluster": "e2e", "Member": "n1", "Role": "Replica", "State": "stopped"}]`
	if got := patroniLeaderFromListJSON(dead); got != "" {
		t.Errorf("no leader present = %q, want empty", got)
	}

	// Unparseable or empty output must not panic or fabricate a name.
	for _, bad := range []string{"", "not json", "[]", "{}"} {
		if got := patroniLeaderFromListJSON(bad); got != "" {
			t.Errorf("patroniLeaderFromListJSON(%q) = %q, want empty", bad, got)
		}
	}
}

func TestPatroniUserFlagsAreHostUid(t *testing.T) {
	flags := patroniUserFlags()
	joined := strings.Join(flags, " ")

	if !strings.Contains(joined, "--userns keep-id") {
		t.Errorf("must keep the host uid in the namespace, got %q", joined)
	}
	want := fmt.Sprintf("--user %d:%d", os.Getuid(), os.Getgid())
	if !strings.Contains(joined, want) {
		t.Errorf("must run as the host file-owner uid, want %q in %q", want, joined)
	}
	if strings.Contains(joined, "postgres") {
		t.Errorf("must not pin the image's postgres uid (it cannot read our 0600 config): %q", joined)
	}
}
