package podman

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func swCfg(name string) *config.SeaweedfsConfig {
	return &config.SeaweedfsConfig{
		Name:          name,
		ContainerName: "pgcli-seaweedfs-" + name,
		Listen:        "127.0.0.1",
		Port:          8400,
		VolumePort:    8401,
		FilerPort:     8402,
		S3Port:        8403,
		RootUser:      "SWabcdef0123456789",
		RootPassword:  strings.Repeat("k", 40),
		Replication:   "000",
		VolumeSizeMB:  1024,
	}
}

// --- s3.json ---------------------------------------------------------------

func TestSeaweedS3JSONRendering(t *testing.T) {
	sc := swCfg("s1")
	sc.RootUser = "SWalicekey"
	sc.RootPassword = "sekret0123456789abcdef"

	out, err := renderSeaweedS3JSON(sc)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Must be valid JSON and match the 4.48 protojson schema pinned on vm01:
	// identities[].credentials is an ARRAY of lowerCamelCase accessKey/
	// secretKey, actions carries the bare "Admin" short-circuit, and there is
	// NO buckets field.
	var doc struct {
		Identities []struct {
			Name        string `json:"name"`
			Credentials []struct {
				AccessKey string `json:"accessKey"`
				SecretKey string `json:"secretKey"`
			} `json:"credentials"`
			Actions []string `json:"actions"`
		} `json:"identities"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("rendered s3.json is not valid JSON: %v\n%s", err, out)
	}
	if len(doc.Identities) != 1 {
		t.Fatalf("identities = %d, want 1", len(doc.Identities))
	}
	id := doc.Identities[0]
	if id.Name != "s1" {
		t.Errorf("identity name = %q, want s1", id.Name)
	}
	if len(id.Credentials) != 1 || id.Credentials[0].AccessKey != "SWalicekey" || id.Credentials[0].SecretKey != "sekret0123456789abcdef" {
		t.Errorf("credentials wrong: %+v", id.Credentials)
	}
	if len(id.Actions) != 1 || id.Actions[0] != "Admin" {
		t.Errorf("actions = %v, want [Admin]", id.Actions)
	}
	// The OLD doc shape must not resurface: it is exactly what 4.48 protojson
	// rejects.
	if strings.Contains(out, `"buckets"`) {
		t.Error("rendered s3.json contains a buckets field (pre-4.48 schema)")
	}
}

// --- filer.toml --------------------------------------------------------------

func TestSeaweedFilerTOMLRendering(t *testing.T) {
	out := renderSeaweedFilerTOML("10.0.0.11", 35432, "admin", "p'w", "seaweed_s1")
	if !strings.Contains(out, "[postgres2]") {
		t.Error("missing [postgres2] section")
	}
	for _, want := range []string{
		`enabled = true`,
		`hostname = "10.0.0.11"`,
		`port = 35432`,
		`username = "admin"`,
		`password = "p'w"`,
		`database = "seaweed_s1"`,
		`sslmode = "disable"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing line %q in:\n%s", want, out)
		}
	}
	// 4.48 leaves connection_max_open unbounded ON PURPOSE (bounded pool
	// deadlocks listings); pgcli must not render it.
	if strings.Contains(out, "connection_max_open") {
		t.Error("renders connection_max_open, overriding upstream's deliberate unbounded pool")
	}
	// There is no `dsn` key in postgres2_store.go — old wiki examples lie.
	if strings.Contains(out, "dsn") {
		t.Error("renders a dsn key the postgres2 store never reads")
	}
}

// --- peers -------------------------------------------------------------------

func TestSeaweedPeersNormalize(t *testing.T) {
	sc := swCfg("c1")
	sc.Cluster = "c1"
	sc.AdvertiseHost = "10.0.0.12"
	sc.Peers = []string{"10.0.0.11:8400", "10.0.0.12:8400", "10.0.0.11:8400", " 10.0.0.13:8400 "}

	got := NormalizeSeaweedfsPeers(sc)
	want := []string{"10.0.0.11:8400", "10.0.0.12:8400", "10.0.0.13:8400"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("peers = %v, want %v (deduped, trimmed, sorted)", got, want)
	}
	// The self entry is folded in even when absent from the config.
	sc2 := swCfg("c2")
	sc2.Cluster = "c1"
	sc2.AdvertiseHost = "10.0.0.13"
	sc2.Peers = []string{"10.0.0.11:8400"}
	if flag := SeaweedfsPeersFlag(sc2); flag != "10.0.0.11:8400,10.0.0.13:8400" {
		t.Errorf("peers flag = %q, want self appended+sorted", flag)
	}
	// Single-node has no peer list at all.
	if got := NormalizeSeaweedfsPeers(swCfg("s1")); got != nil {
		t.Errorf("single-node peers = %v, want nil", got)
	}
}

// --- weed argv ---------------------------------------------------------------

func findFlag(t *testing.T, args []string, prefix string) (string, bool) {
	t.Helper()
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return a, true
		}
	}
	return "", false
}

// assertWeedArgOrder checks the one thing a substring check cannot: POSITION.
// weed's stdlib flag parser treats the first non-flag word as the subcommand and
// only allows process-global flags before it, so every -dir/-ip/-master.*/-s3.*
// flag must sit AFTER "mini"/"server" — a passing substring test once shipped an
// argv that made weed exit 2 (vm01). args[0] is the lone global flag, args[1]
// the subcommand word.
func assertWeedArgOrder(t *testing.T, args []string, sub string) {
	t.Helper()
	if len(args) < 2 {
		t.Fatalf("argv too short to hold global + subcommand: %v", args)
	}
	if args[0] != "-logtostderr=true" {
		t.Errorf("args[0] = %q, want the only global flag -logtostderr=true", args[0])
	}
	if args[1] != sub {
		t.Errorf("args[1] = %q, want subcommand %q before all role flags", args[1], sub)
	}
	for _, a := range args[2:] {
		// Everything after the subcommand word is a flag (role toggles -filer
		// /-s3 included); a bare second word would be a stray argument.
		if !strings.HasPrefix(a, "-") {
			t.Errorf("unexpected non-flag arg %q after subcommand %q: %v", a, sub, args)
		}
	}
}

func TestSeaweedWeedArgsSingle(t *testing.T) {
	sc := swCfg("s1")
	args := buildWeedArgs(sc, "127.0.0.1", "", "")
	joined := strings.Join(args, " ")

	assertWeedArgOrder(t, args, "mini")

	// The trap: -ip must be present (not just -ip.bind), or uploads 500 on a
	// healthy-looking container (vm01).
	if v, ok := findFlag(t, args, "-ip="); !ok || v != "-ip=127.0.0.1" {
		t.Errorf("single-node must advertise -ip=Listen, got %q ok=%v", v, ok)
	}
	for _, want := range []string{
		"-logtostderr=true",
		"-ip.bind=127.0.0.1",
		"-master.port=8400",
		"-volume.port=8401",
		"-filer.port=8402",
		"-s3.port=8403",
		"-master.volumeSizeLimitMB=1024",
		"-s3.config=/etc/seaweedfs/s3.json",
		"-master.telemetry=false",
		"-s3.port.iceberg=0",
		"-s3.port.lance=0",
		"-webdav=false",
		"-admin.ui=false",
		"mini",
		"-bucket=s1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("single argv missing %q:\n%s", want, joined)
		}
	}
	// Exact: the single-dir case must use the bare constant, not a numbered slot.
	if v, ok := findFlag(t, args, "-dir="); !ok || v != "-dir=/data" {
		t.Errorf("single-node -dir = %q ok=%v, want -dir=/data", v, ok)
	}
	if strings.Contains(joined, "server") || strings.Contains(joined, "-master.peers") {
		t.Errorf("single argv must not carry cluster flags:\n%s", joined)
	}
}

// Multi-disk: weed's -dir is itself a comma list (verified on vm01 — mini
// stats each element and the volume server reports one DiskStatuses entry per
// dir), so N drives means one -dir=/data1,/data2,… with each host dir mounted
// at its own /dataN slot.
func TestSeaweedWeedArgsMultiDrive(t *testing.T) {
	sc := swCfg("s1")
	sc.Drives = []string{"/mnt/sw/d1", "/mnt/sw/d2", "/srv/sw/d3"}
	args := buildWeedArgs(sc, "127.0.0.1", "", "")

	if v, ok := findFlag(t, args, "-dir="); !ok || v != "-dir=/data1,/data2,/data3" {
		t.Errorf("multi-drive -dir = %q ok=%v, want -dir=/data1,/data2,/data3", v, ok)
	}
	// The single-dir constant must never leak in alongside the numbered slots.
	if strings.Contains(strings.Join(args, " "), "-dir=/data,") {
		t.Errorf("bare /data must not appear once drives are set:\n%s", strings.Join(args, " "))
	}

	// Same shape on a cluster member — drives are a per-node knob.
	cs := swCfg("c1")
	cs.Cluster = "lab"
	cs.AdvertiseHost = "10.0.0.12"
	cs.Replication = "001"
	cs.Peers = []string{"10.0.0.11:8400", "10.0.0.12:8400"}
	cs.Drives = []string{"/mnt/sw/d1", "/mnt/sw/d2"}
	cargs := buildWeedArgs(cs, "10.0.0.12", "", "")
	if v, ok := findFlag(t, cargs, "-dir="); !ok || v != "-dir=/data1,/data2" {
		t.Errorf("cluster member -dir = %q ok=%v, want -dir=/data1,/data2", v, ok)
	}
	assertWeedArgOrder(t, cargs, "server")
}

func TestSeaweedContainerDataDirs(t *testing.T) {
	if got := containerDataDirs(&config.SeaweedfsConfig{}); len(got) != 1 || got[0] != "/data" {
		t.Errorf("no drives = %v, want [/data]", got)
	}
	got := containerDataDirs(&config.SeaweedfsConfig{Drives: []string{"a", "b", "c", "d", "e"}})
	want := []string{"/data1", "/data2", "/data3", "/data4", "/data5"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("5 drives = %v, want %v", got, want)
	}
}

func TestSeaweedWeedArgsCluster(t *testing.T) {
	sc := swCfg("c1")
	sc.Cluster = "lab"
	sc.AdvertiseHost = "10.0.0.12"
	sc.Replication = "001"
	sc.Peers = []string{"10.0.0.11:8400", "10.0.0.13:8400"}
	args := buildWeedArgs(sc, "10.0.0.12", "", "")
	joined := strings.Join(args, " ")

	assertWeedArgOrder(t, args, "server")

	// Cluster advertises the peer-dialable host, NOT the bind.
	if v, ok := findFlag(t, args, "-ip="); !ok || v != "-ip=10.0.0.12" {
		t.Errorf("cluster must advertise -ip=AdvertiseHost, got %q ok=%v", v, ok)
	}
	for _, want := range []string{
		"server",
		"-master.peers=10.0.0.11:8400,10.0.0.12:8400,10.0.0.13:8400", // full MNMD list incl. self
		"-master.defaultReplication=001",
		"-filer",
		"-s3",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("cluster argv missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "mini") || strings.Contains(joined, "-bucket=") {
		t.Errorf("cluster argv must not carry mini flags:\n%s", joined)
	}
	// -admin.ui is mini-only: server's flag parser exits 2 on an undefined flag
	// (vm02 caught this), so it must never appear on the server side.
	if strings.Contains(joined, "-admin.ui") {
		t.Errorf("cluster argv must not carry the mini-only -admin.ui flag:\n%s", joined)
	}
}

func TestSeaweedWeedArgsTLS(t *testing.T) {
	sc := swCfg("s1")
	sc.TLS = true
	args := buildWeedArgs(sc, "127.0.0.1", seaweedContainerCert, seaweedContainerKey)
	joined := strings.Join(args, " ")

	assertWeedArgOrder(t, args, "mini")

	// TLS posture verified on vm01: cleartext listener OFF (port 0 = not
	// bound), the pooled port serves HTTPS, cert flags point at the mounts.
	for _, want := range []string{
		"-s3.port=0",
		"-s3.port.https=8403",
		"-s3.cert.file=/etc/seaweedfs/cert.pem",
		"-s3.key.file=/etc/seaweedfs/key.pem",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("TLS argv missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "-s3.port=8403") {
		t.Errorf("TLS argv must not bind the cleartext s3 port:\n%s", joined)
	}

	// No cert paths (TLS requested but resolution failed upstream) must not
	// produce a half-TLS argv.
	plain := strings.Join(buildWeedArgs(sc, "127.0.0.1", "", ""), " ")
	if !strings.Contains(plain, "-s3.port=8403") || strings.Contains(plain, "-s3.port.https") {
		t.Errorf("without cert material the argv must stay cleartext:\n%s", plain)
	}
}

// --- validation ---------------------------------------------------------------

func TestValidateSeaweedfsCluster(t *testing.T) {
	// Single-node happy path (and the ApplyDefaults state).
	ok := swCfg("s1")
	if err := ValidateSeaweedfsCluster(ok); err != nil {
		t.Fatalf("valid single-node rejected: %v", err)
	}
	// Empty replication is the same as 000 (defaults fill it, but the
	// validator must not invent a failure for a hand-edited config).
	empty := swCfg("s1")
	empty.Replication = ""
	if err := ValidateSeaweedfsCluster(empty); err != nil {
		t.Fatalf("empty replication rejected: %v", err)
	}
	// A non-000 placement on one node is guaranteed write failure.
	bad := swCfg("s1")
	bad.Replication = "001"
	if err := ValidateSeaweedfsCluster(bad); err == nil {
		t.Error("replication 001 accepted for single-node")
	}
	// Credential floors (garage's, same crash-loop rationale).
	shortAK := swCfg("s1")
	shortAK.RootUser = "short12"
	if err := ValidateSeaweedfsCluster(shortAK); err == nil {
		t.Error("access key of 7 accepted")
	}
	shortSK := swCfg("s1")
	shortSK.RootPassword = strings.Repeat("k", 15)
	if err := ValidateSeaweedfsCluster(shortSK); err == nil {
		t.Error("secret key of 15 accepted")
	}
	// Empty creds are the install handler's job, not a validation error.
	noCreds := swCfg("s1")
	noCreds.RootUser, noCreds.RootPassword = "", ""
	if err := ValidateSeaweedfsCluster(noCreds); err != nil {
		t.Errorf("empty creds rejected at validate stage: %v", err)
	}

	// Cluster happy path.
	cl := swCfg("c1")
	cl.Cluster = "lab"
	cl.Replication = "001"
	cl.AdvertiseHost = "10.0.0.11"
	cl.Peers = []string{"10.0.0.12:8400"}
	cl.FilerDSN = "postgres://admin:pw@127.0.0.1:35432/seaweed_c1"
	if err := ValidateSeaweedfsCluster(cl); err != nil {
		t.Fatalf("valid cluster member rejected: %v", err)
	}
	// AdvertiseHost required (-ip of peers).
	noAdv := *cl
	noAdv.AdvertiseHost = ""
	if err := ValidateSeaweedfsCluster(&noAdv); err == nil {
		t.Error("cluster without advertise host accepted")
	}
	// At least 2 members after folding in self.
	solo := *cl
	solo.Peers = nil
	if err := ValidateSeaweedfsCluster(&solo); err == nil {
		t.Error("cluster with only itself accepted")
	}
	// Malformed placement codes.
	for _, rep := range []string{"9", "00012", "0a0", ""} {
		badRep := *cl
		badRep.Replication = rep
		if err := ValidateSeaweedfsCluster(&badRep); err == nil {
			t.Errorf("cluster replication %q accepted", rep)
		}
	}
	// Shared metadata DB is a selection prerequisite, not optional.
	noDB := *cl
	noDB.FilerDSN = ""
	if err := ValidateSeaweedfsCluster(&noDB); err == nil {
		t.Error("cluster without filer DSN accepted")
	}
	// Shared credentials are the other prerequisite: each gateway reads only
	// its own s3.json, so a cluster cannot let nodes mint separate pairs.
	noCredsCl := *cl
	noCredsCl.RootUser, noCredsCl.RootPassword = "", ""
	if err := ValidateSeaweedfsCluster(&noCredsCl); err == nil {
		t.Error("cluster without explicit shared credentials accepted")
	}
	halfCredsCl := *cl
	halfCredsCl.RootPassword = ""
	if err := ValidateSeaweedfsCluster(&halfCredsCl); err == nil {
		t.Error("cluster with only an access key accepted")
	}
}

// --- cluster status -----------------------------------------------------------

// Fixture captured live on vm01 (master JSON API). The Leader format is
// ip:httpport.grpcport — parse contract for the health gate.
func TestParseSeaweedfsClusterStatus(t *testing.T) {
	st, err := ParseSeaweedfsClusterStatus(`{"IsLeader":true,"Leader":"10.0.0.11:8400.18400"}`)
	if err != nil {
		t.Fatalf("fixture parse: %v", err)
	}
	if !st.IsLeader || st.Leader != "10.0.0.11:8400.18400" {
		t.Errorf("fixture decoded wrong: %+v", st)
	}
	if st.Leader == "" {
		t.Error("green sample must have a leader")
	}
	// Junk (an HTML error page, say) must be an error, not a silent green.
	if _, err := ParseSeaweedfsClusterStatus("<html>502</html>"); err == nil {
		t.Error("non-JSON body accepted")
	}
	// Pre-election body shape: no leader yet → the gate keeps polling (cluster
	// members legitimately sit here until quorum forms).
	noLeader, err := ParseSeaweedfsClusterStatus(`{"IsLeader":false,"Leader":""}`)
	if err != nil || noLeader.Leader != "" {
		t.Errorf("pre-election parse = %+v err = %v", noLeader, err)
	}
}
