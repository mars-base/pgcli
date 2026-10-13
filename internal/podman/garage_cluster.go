package podman

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
)

// Multi-node (v2) control surface. A cluster member is started with a bare
// `garage server` (see createContainer); everything that assembles the cluster
// — peering, the CRDT layout, and credential seeding — is driven through the
// in-container /garage CLI via ExecCapture. pgcli NEVER runs these on a remote
// host: each member bootstraps itself and the operator copies the printed
// join command to the next host (the ha_remote.go pattern). Output parsing is
// text-based because garage v2.4 exposes no --json on these subcommands.

// ExecCapture runs `/garage <args...>` inside a running member and returns its
// combined stdout+stderr so the caller can parse it. Unlike Exec (which pipes
// to the terminal), this is for the control commands whose output pgcli reads:
// node id / status / layout / key create. Output is sanitizeGarage()'d first:
// every garage subcommand handshakes with the local RPC endpoint and prints
// ANSI-colored INFO lines to stderr — captured together with stdout, those
// lines would otherwise become the "last non-empty line" of `node id`.
func (m *GarageManager) ExecCapture(gc *config.GarageConfig, args ...string) (string, error) {
	return m.ExecCaptureTimeout(gc, 45*time.Second, args...)
}

// ExecCaptureTimeout is ExecCapture with an explicit bound. The install flow
// uses 45s (every step there is a fast control command); the user-facing
// `pg garage` passthrough needs minutes for ops like a rebalancing
// `layout apply`.
func (m *GarageManager) ExecCaptureTimeout(gc *config.GarageConfig, d time.Duration, args ...string) (string, error) {
	running, err := m.containerRunning(gc.ContainerName)
	if err != nil {
		return "", err
	}
	if !running {
		return "", fmt.Errorf("garage container %s is not running — run 'pg addon start garage --name %s' first", gc.ContainerName, gc.Name)
	}
	execArgs := []string{"exec", gc.ContainerName, "/garage"}
	execArgs = append(execArgs, args...)
	// Bounded: garage's CLI can wedge against a half-converged cluster during
	// first boot (observed: `layout show` hanging minutes while the server had
	// already answered). A loud timeout the operator re-runs beats a silent
	// deadlock. garageExecOutput also resolves the "prints then never exits"
	// case that a plain Output()+timeout pair mishandles.
	out, err := m.garageExecOutput(d, execArgs...)
	if err != nil {
		// m.run only surfaces stderr on failure; for these CLI commands the
		// useful message is often on stdout, so take what we got.
		return sanitizeGarage(out), err
	}
	return sanitizeGarage(out), nil
}

// sanitizeGarage drops garage's netapp handshake log lines from captured
// output. Colored log lines carry ANSI escapes plus the INFO level; the module
// path catches them uncoloured too. A bare "INFO" substring must NOT be the
// test — Garage's own banners are table headers like
// "==== ACCESS KEY INFORMATION ====" which pg garage shows verbatim.
func sanitizeGarage(out string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "garage_net::netapp") ||
			(strings.Contains(line, "INFO") && strings.Contains(line, "\x1b[")) {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// parseNodeID pulls the node identity out of `garage node id` output: the line
// whose first field is the 64-hex key — `<64hex>` (single-node, no
// rpc_public_addr) or `<64hex>@<addr>` (cluster). In cluster mode the CLI
// appends a "To instruct a node to connect..." hint block (with bootstrap_peers
// examples and a security notice) after the id, so anchoring on the LAST
// non-empty line would parse that block's prose instead.
func parseNodeID(out string) (string, error) {
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		id := fields[0]
		hexPart, addr, hasAddr := strings.Cut(id, "@")
		// Exactly 64 leading hex chars (a quoted "…", prose, or the header
		// line all fail this), and an @ must be followed by an address.
		if len(hexPart) != 64 || firstHexLen(hexPart) != 64 || (hasAddr && addr == "") {
			continue
		}
		return id, nil
	}
	return "", fmt.Errorf("could not parse garage node id from: %q", strings.TrimSpace(out))
}

// firstHexLen returns the length of the leading run of hex chars in s (used to
// sanity-check a node id line without over-parsing the @addr suffix).
func firstHexLen(s string) int {
	n := 0
	for n < len(s) && strings.IndexByte("0123456789abcdefABCDEF", s[n]) >= 0 {
		n++
	}
	return n
}

// NodeID returns this member's `<id>` or `<id>@<addr>` identity, cached into
// gc.NodeID so a recreate reconnects as the same node without a fresh read.
func (m *GarageManager) NodeID(gc *config.GarageConfig) (string, error) {
	out, err := m.ExecCapture(gc, "node", "id")
	if err != nil {
		return "", err
	}
	id, err := parseNodeID(out)
	if err != nil {
		return "", err
	}
	return id, nil
}

// WaitForNodeID polls `garage node id` until it yields a value or the deadline
// passes. The node keypair is generated asynchronously during first boot, so a
// freshly started member may briefly refuse — this bounded wait is what keeps
// install from assuming the identity exists the instant the container is up.
func (m *GarageManager) WaitForNodeID(gc *config.GarageConfig, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		id, err := m.NodeID(gc)
		if err == nil && id != "" {
			return id, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("garage node id not available within %s (node keypair may still be initializing): %w", timeout, err)
		}
		time.Sleep(2 * time.Second)
	}
}

// ConnectPeer instructs this member to peer with a known `id@addr`. Peering is
// transitive, so a joiner only connects to one existing node, not the full
// mesh. Errors already carrying an "already connected" text are treated as
// success (idempotent re-install).
func (m *GarageManager) ConnectPeer(gc *config.GarageConfig, peer string) error {
	out, err := m.ExecCapture(gc, "node", "connect", peer)
	if err != nil && !garageAlready(out, err) {
		return fmt.Errorf("garage node connect %s: %w", peer, err)
	}
	return nil
}

// Status returns the raw `garage status` output (for display and for
// CountAssignedNodes).
func (m *GarageManager) Status(gc *config.GarageConfig) (string, error) {
	return m.ExecCapture(gc, "status")
}

// countAssignedNodes counts healthy nodes that already hold a layout role.
// `garage status` groups nodes under `==== HEALTHY NODES ====` (and other
// banners); a row under a non-healthy banner must not count toward quorum, and
// a healthy row may still carry the "NO ROLE ASSIGNED" sentinel — connected but
// not yet in the layout. Node rows are anchored on the hex id column so
// banners, the header line and blanks are skipped.
func countAssignedNodes(statusOut string) int {
	n := 0
	inHealthy := false
	for line := range strings.SplitSeq(statusOut, "\n") {
		if s := strings.TrimSpace(line); strings.HasPrefix(s, "====") {
			// "UNHEALTHY NODES" contains "HEALTHY" — anchor on "HEALTHY NODES"
			// only when the banner is not the UNHEALTHY one.
			inHealthy = strings.Contains(s, "HEALTHY NODES") && !strings.Contains(s, "UNHEALTHY")
			continue
		}
		if !inHealthy {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 || firstHexLen(fields[0]) < 8 {
			continue // not a node row (column header, blank)
		}
		if !strings.Contains(line, "NO ROLE ASSIGNED") {
			n++
		}
	}
	return n
}

// CountAssignedHealthyNodes exposes the status-text quorum count to the CLI
// (pg addon list shows "Nodes: N assigned (healthy)" from a live status).
func CountAssignedHealthyNodes(statusOut string) int {
	return countAssignedNodes(statusOut)
}

// CountAssignedNodes reports how many healthy nodes currently have a role
// assigned — the quorum check a cluster uses to decide it is safe to seed.
func (m *GarageManager) CountAssignedNodes(gc *config.GarageConfig) (int, error) {
	out, err := m.Status(gc)
	if err != nil {
		return 0, err
	}
	return countAssignedNodes(out), nil
}

// layoutShowVersion parses "Current cluster layout version: N" out of
// `garage layout show`.
func layoutShowVersion(out string) (int, error) {
	const marker = "Current cluster layout version:"
	for line := range strings.SplitSeq(out, "\n") {
		if _, rest, found := strings.Cut(line, marker); found {
			var v int
			if _, err := fmt.Sscanf(strings.TrimSpace(rest), "%d", &v); err != nil {
				return 0, fmt.Errorf("parsing layout version: %w", err)
			}
			return v, nil
		}
	}
	return 0, fmt.Errorf("no layout version line in: %q", strings.TrimSpace(out))
}

// LayoutAssign stages this node's role (zone + capacity). Capacity is GiB → the
// suffix Garage's `-c` accepts. Uses the node's hex id (peer addr suffix
// stripped) so it matches the node column.
func (m *GarageManager) LayoutAssign(gc *config.GarageConfig, nodeID string) error {
	id, _, _ := strings.Cut(nodeID, "@")
	args := []string{"layout", "assign", id, "-z", gc.Zone, "-c", fmt.Sprintf("%dGiB", gc.Capacity)}
	out, err := m.ExecCapture(gc, args...)
	if err != nil && !garageAlready(out, err) {
		return fmt.Errorf("garage layout assign: %w", err)
	}
	return nil
}

// ErrLayoutAwaitQuorum reports that Garage refused to apply a layout whose
// positive-capacity node count is below replication_factor — the normal state
// while members are still joining (verified on v2.4.1: "The number of nodes
// with positive capacity (1) is smaller than the replication factor (3)").
// The staged changes are NOT lost: staging is cluster-replicated, so the LAST
// member's install sees the full staged set and its apply commits v1. pgcli
// surfaces this as a warning, not a failure.
var ErrLayoutAwaitQuorum = errors.New("garage layout staged, awaiting replication-factor nodes")

func layoutAwaitQuorum(out string, err error) bool {
	return strings.Contains(strings.ToLower(out+" "+fmt.Sprint(err)),
		"is smaller than the replication factor")
}

// LayoutApply commits staged layout changes at currentVersion+1. Garage rejects
// a version that is not exactly prev+1, which is a natural optimistic lock: a
// concurrent install makes our apply fail, and the caller surfaces "re-run the
// same command" (assign is idempotent, so the retry just re-stages and re-reads).
// An empty staging (no `==== STAGED ROLE CHANGES ====` section — e.g. a no-op
// re-install) skips the apply entirely, because applying zero changes would
// still bump the version.
func (m *GarageManager) LayoutApply(gc *config.GarageConfig) error {
	show, err := m.ExecCapture(gc, "layout", "show")
	if err != nil {
		return fmt.Errorf("garage layout show: %w", err)
	}
	if !strings.Contains(show, "==== STAGED ROLE CHANGES ====") {
		return nil
	}
	cur, err := layoutShowVersion(show)
	if err != nil {
		return err
	}
	out, err := m.ExecCapture(gc, "layout", "apply", "--version", fmt.Sprintf("%d", cur+1))
	if err != nil {
		if garageAlready(out, err) {
			return nil
		}
		if layoutAwaitQuorum(out, err) {
			return ErrLayoutAwaitQuorum
		}
		return fmt.Errorf("garage layout apply --version %d: %w (layout changed concurrently — re-run the same install command)", cur+1, err)
	}
	return nil
}

// BucketCreate creates a bucket (idempotent on "already exists").
func (m *GarageManager) BucketCreate(gc *config.GarageConfig, name string) error {
	out, err := m.ExecCapture(gc, "bucket", "create", name)
	if err != nil && !garageAlready(out, err) {
		return fmt.Errorf("garage bucket create %s: %w", name, err)
	}
	return nil
}

// parseKeyCreate pulls the access key id and secret out of `garage key create`
// output ("Key ID:" / "Secret key:" lines). Garage mints these; the shape is
// GK+32hex / 64hex, matching what v1 generated locally.
func parseKeyCreate(out string) (accessKey, secretKey string, err error) {
	for line := range strings.SplitSeq(out, "\n") {
		lower := strings.ToLower(line)
		if i := strings.Index(lower, "key id:"); i >= 0 {
			accessKey = strings.TrimSpace(line[i+len("key id:"):])
		} else if i := strings.Index(lower, "secret key:"); i >= 0 {
			secretKey = strings.TrimSpace(line[i+len("secret key:"):])
		}
	}
	if accessKey == "" || secretKey == "" {
		return "", "", fmt.Errorf("could not parse key id/secret from garage key create output: %q", strings.TrimSpace(out))
	}
	return accessKey, secretKey, nil
}

// KeyCreate mints a cluster access key named after the instance and returns its
// (Garage-generated) access/secret pair. The caller persists them into
// gc.RootUser/gc.RootPassword so `pg addon password garage` keeps working.
func (m *GarageManager) KeyCreate(gc *config.GarageConfig, name string) (string, string, error) {
	out, err := m.ExecCapture(gc, "key", "create", name)
	if err != nil {
		return "", "", fmt.Errorf("garage key create %s: %w", name, err)
	}
	return parseKeyCreate(out)
}

// BucketAllow grants a key read/write/owner on a bucket (idempotent).
func (m *GarageManager) BucketAllow(gc *config.GarageConfig, bucket, keyID string) error {
	out, err := m.ExecCapture(gc, "bucket", "allow", bucket, "--key", keyID, "--read", "--write", "--owner")
	if err != nil && !garageAlready(out, err) {
		return fmt.Errorf("garage bucket allow %s --key %s: %w", bucket, keyID, err)
	}
	return nil
}

// KeyIDByName returns the GK access-key id of the key named `name`, if present
// (see keyListIDForName for the row parsing). `key create` is NOT idempotent
// (a second run mints a second key with the same name), so the seeding path
// checks for existence here first — and because a key's secret is printed
// exactly once at creation, only the node that mints it can store it; every
// other member recovers the public id and points the operator at that node.
func (m *GarageManager) KeyIDByName(gc *config.GarageConfig, name string) (string, bool, error) {
	out, err := m.ExecCapture(gc, "key", "list")
	if err != nil {
		return "", false, err
	}
	id := keyListIDForName(out, name)
	return id, id != "", nil
}

// keyListIDForName scans `key list` output for the key named `name`, returning
// its GK id ("" when absent). Row shape is ID / Created / Name / Expiration;
// the Name column can be multi-word ("default access key"), so it is the join
// of everything between the id+Created prefix and the trailing Expiration
// field — a plain field-set match would miss it.
func keyListIDForName(out, name string) string {
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || !strings.HasPrefix(fields[0], "GK") {
			continue
		}
		// fields[0]=id, fields[1]=Created, fields[len-1]=Expiration,
		// fields[2:len-1]=Name (possibly several words).
		got := strings.Join(fields[2:len(fields)-1], " ")
		if got == name {
			return fields[0]
		}
	}
	return ""
}

// garageAlready reports whether an error is really "operation already done"
// (peering to an already-connected node, re-creating an existing key/bucket,
// re-assigning an unchanged role). All cluster steps run on every joiner's
// install tail, so idempotency here is what lets the LAST node finish seeding
// while earlier nodes safely no-op.
// garageAlreadyTexts are the "operation already done" messages the garage CLI
// prints when a step was previously applied (peering to an already-connected
// node, re-creating an existing key/bucket, re-assigning an unchanged role).
// All cluster steps run on every joiner's install tail, so idempotency here is
// what lets the LAST node finish seeding while earlier nodes safely no-op.
var garageAlreadyTexts = []string{
	"already exists", "already connected", "already assigned",
	"already allows", "nothing to apply", "no changes",
}

func garageAlready(out string, err error) bool {
	joined := strings.ToLower(out + " " + fmt.Sprint(err))
	return slices.ContainsFunc(garageAlreadyTexts, func(s string) bool {
		return strings.Contains(joined, s)
	})
}
