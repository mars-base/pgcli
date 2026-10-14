package podman

import (
	"strings"
	"testing"
)

// Real output captured from garage v2.4.1 on vm01 (after ExecCapture's
// sanitizeGarage has dropped the ANSI netapp INFO lines). The parsers are
// text-anchored on exactly these shapes — v2.4 exposes no --json.

const sampleStatus = `==== HEALTHY NODES ====
ID                Hostname     Address             Tags       Zone  Capacity  DataAvail         Version
39b983bc9147da7f  debian12-01  10.0.0.11:3901  [default]  dc1   19.5 GiB  11.9 GiB (61.3%)  v2.4.1
`

const sampleStatusThree = `==== HEALTHY NODES ====
ID                Hostname     Address             Tags       Zone  Capacity  DataAvail         Version
39b983bc9147da7f  debian12-01  10.0.0.11:3901  [default]  z1    15 GiB    11.9 GiB (61.3%)  v2.4.1
41c2f7a01b3e9d55  debian12-02  10.0.0.12:3901  [default]  z2    15 GiB    11.8 GiB (60.9%)  v2.4.1
7a55e0b2c9d13f68  debian12-03  10.0.0.14:3901  [default]  z3    15 GiB    12.1 GiB (62.4%)  v2.4.1
`

const sampleStatusUnassigned = `==== HEALTHY NODES ====
ID                Hostname     Address             Tags       Zone  Capacity  DataAvail   Version
39b983bc9147da7f  debian12-01  10.0.0.11:3901  [default]  --    NO ROLE ASSIGNED      v2.4.1
41c2f7a01b3e9d55  debian12-02  10.0.0.12:3901  [default]  z2    15 GiB    11.8 GiB   v2.4.1

==== UNHEALTHY NODES ====
7a55e0b2c9d13f68  debian12-03  10.0.0.14:3901  [default]  z3    15 GiB                v2.4.1
`

const sampleLayoutShow = `==== CURRENT CLUSTER LAYOUT ====
ID                Tags       Zone  Capacity  Usable capacity
39b983bc9147da7f  [default]  dc1   19.5 GiB  19.5 GiB (100.0%)

Zone redundancy: maximum

Current cluster layout version: 1
`

const sampleLayoutStaged = `==== CURRENT CLUSTER LAYOUT ====
ID                Tags       Zone  Capacity  Usable capacity
39b983bc9147da7f  [default]  dc1   19.5 GiB  19.5 GiB (100.0%)

==== STAGED ROLE CHANGES ====
ID                Tags       Zone  Capacity
41c2f7a01b3e9d55  [default]  z2    15 GiB

Current cluster layout version: 1
`

const sampleKeyCreate = `==== ACCESS KEY INFORMATION ====
Key ID:              GKfaad7bf416c4d7ebfa3be0aa
Key name:            fmt-probe2
Secret key:          4e3c5a57ca3b703bccd13104e13da4540a218562a473ae3dd759b8b98d097925
Created:             2026-10-12 09:02:41.767 +00:00
Validity:            valid
Expiration:          never

Can create buckets:  false

==== BUCKETS FOR THIS KEY ====
Permissions  ID  Global aliases  Local aliases
`

const sampleKeyList = `ID                                  Created     Name                Expiration
GKd201a14bd66f7b68ad7f8cc5ed0a76fb  2026-10-12  default access key  never
GKa82dd80eaff78cd32e4cbddc          2026-10-12  pgcli-fmt-probe     never
GKfaad7bf416c4d7ebfa3be0aa          2026-10-12  fmt-probe2          never
`

func TestSanitizeGarage(t *testing.T) {
	raw := "\x1b[2m2026-10-12T09:02:41Z\x1b[0m \x1b[32m INFO\x1b[0m \x1b[2mgarage_net::netapp\x1b[0m: Connected to 127.0.0.1:3901, negotiating handshake...\n" +
		"39b983bc9147da7f34c4c445e894806d3263547a3d600c5a121756bb54c17a0b\n"
	got := parseNodeIDCheck(sanitizeGarage(raw))
	if got != "39b983bc9147da7f34c4c445e894806d3263547a3d600c5a121756bb54c17a0b" {
		t.Errorf("node id after sanitize = %q", got)
	}
	// `pg garage key create` prints its banner verbatim — "INFORMATION"
	// contains "INFO", and a plain substring test would eat the heading
	// (observed live on vm01 before the ANSI requirement).
	out := sanitizeGarage(sampleKeyCreate)
	if !strings.Contains(out, "==== ACCESS KEY INFORMATION ====") {
		t.Errorf("banner dropped by sanitize:\n%s", out)
	}
	// Uncoloured netapp log lines (no TTY) must still drop via the module path.
	out = sanitizeGarage("2026-10-12T09:02:41Z INFO garage_net::netapp: Connected to 127.0.0.1:3901\nid-line\n")
	if strings.Contains(out, "netapp") {
		t.Errorf("uncoloured netapp line survived:\n%s", out)
	}
}

// parseNodeIDCheck is a test helper wrapping the error half of parseNodeID.
func parseNodeIDCheck(out string) string {
	id, err := parseNodeID(out)
	if err != nil {
		return "ERR: " + err.Error()
	}
	return id
}

func TestParseNodeID(t *testing.T) {
	// Single-node with no rpc_public_addr: bare 64-hex, plus trailing blank.
	if id, err := parseNodeID(sampleNodeID + "\n"); err != nil || len(id) != 64 {
		t.Errorf("bare id: %q %v", id, err)
	}
	// Cluster member with rpc_public_addr: `<64hex>@<addr>`.
	const withAddr = sampleNodeID + "@10.0.0.11:3901\n"
	id, err := parseNodeID(withAddr)
	if err != nil || id != sampleNodeID+"@10.0.0.11:3901" {
		t.Errorf("id@addr: %q %v", id, err)
	}
	// Cluster mode appends a connect-hint block after the id (vm02, v2.4.1) —
	// anchoring on the last line would return its prose.
	const hintID = "5b370435a60739ed7ac1994a39e97329d509b7a6d7bb8d7206d598561c1c9187@10.0.0.12:3901"
	id, err = parseNodeID(sampleNodeIDWithHint)
	if err != nil || id != hintID {
		t.Errorf("id with hint block: %q %v", id, err)
	}
	if _, err := parseNodeID("error: could not load system config\n"); err == nil {
		t.Error("error text must not parse as a node id")
	}
}

const sampleNodeID = "39b983bc9147da7f34c4c445e894806d3263547a3d600c5a121756bb54c17a0b"

// sampleNodeIDWithHint is vm02's real `garage node id` output once the node is
// part of a cluster (INFO handshake lines already stripped, as ExecCapture does).
const sampleNodeIDWithHint = `5b370435a60739ed7ac1994a39e97329d509b7a6d7bb8d7206d598561c1c9187@10.0.0.12:3901

To instruct a node to connect to this node, run the following command on that node:
    garage [-c <config file path>] node connect 5b370435a60739ed7ac1994a39e97329d509b7a6d7bb8d7206d598561c1c9187@10.0.0.12:3901

This node identifier can also be added as a bootstrap node in other node's garage.toml files:
    bootstrap_peers = [
        "5b370435a60739ed7ac1994a39e97329d509b7a6d7bb8d7206d598561c1c9187@10.0.0.12:3901",
        ...
    ]

Security notice: Garage's intra-cluster communications are secured primarily by the shared
secret value rpc_secret.
`

func TestCountAssignedNodes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"single assigned", sampleStatus, 1},
		{"three assigned", sampleStatusThree, 3},
		{"unhealthy and NO ROLE rows excluded", sampleStatusUnassigned, 1},
		{"empty output", "", 0},
	}
	for _, tc := range tests {
		if got := countAssignedNodes(tc.in); got != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
}

func TestLayoutShowVersion(t *testing.T) {
	if v, err := layoutShowVersion(sampleLayoutShow); err != nil || v != 1 {
		t.Errorf("show: v=%d err=%v", v, err)
	}
	// The staged variant still reports the CURRENT version (apply takes +1).
	if v, err := layoutShowVersion(sampleLayoutStaged); err != nil || v != 1 {
		t.Errorf("staged: v=%d err=%v", v, err)
	}
	if _, err := layoutShowVersion("no version here"); err == nil {
		t.Error("missing marker must error")
	}
	if !strings.Contains(sampleLayoutStaged, "==== STAGED ROLE CHANGES ====") {
		t.Fatal("sample lost the staged marker")
	}
}

func TestParseKeyCreate(t *testing.T) {
	ak, sk, err := parseKeyCreate(sampleKeyCreate)
	if err != nil {
		t.Fatal(err)
	}
	if ak != "GKfaad7bf416c4d7ebfa3be0aa" {
		t.Errorf("access key = %q", ak)
	}
	if len(sk) != 64 || strings.TrimSpace(sk) != sk {
		t.Errorf("secret key = %q", sk)
	}
	if _, _, err := parseKeyCreate("garage: error: bad thing"); err == nil {
		t.Error("error output must not parse")
	}
}

func TestKeyListIDForName(t *testing.T) {
	// Multi-word names ("default access key") must not block matching a
	// single-token name, and a substring must not count as a match.
	if got := keyListIDForName(sampleKeyList, "fmt-probe2"); got != "GKfaad7bf416c4d7ebfa3be0aa" {
		t.Errorf("fmt-probe2 id = %q", got)
	}
	if got := keyListIDForName(sampleKeyList, "probe"); got != "" {
		t.Errorf("substring %q must not match a whole field", got)
	}
	if got := keyListIDForName(sampleKeyList, "pgcli-fmt-probe"); got != "GKa82dd80eaff78cd32e4cbddc" {
		t.Errorf("pgcli-fmt-probe id = %q", got)
	}
	if got := keyListIDForName(sampleKeyList, "default access key"); got != "GKd201a14bd66f7b68ad7f8cc5ed0a76fb" {
		t.Errorf("multi-word name id = %q", got)
	}
}

func TestGarageAlready(t *testing.T) {
	ok := []string{
		"Error: Bucket already exists",
		"error: Node is already connected to this peer",
		"nothing to apply, the staged layout is empty",
	}
	for _, s := range ok {
		if !garageAlready(s, nil) {
			t.Errorf("%q should count as already-done", s)
		}
	}
	bad := []string{
		"Error: PermissionDenied",
		"connection refused",
	}
	for _, s := range bad {
		if garageAlready(s, nil) {
			t.Errorf("%q must not be swallowed as already-done", s)
		}
	}
}
