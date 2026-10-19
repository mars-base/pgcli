package podman

import (
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// beegfsTestConfig is the minimum viable filesystem config the argv builders
// consume: ApplyDefaults-shaped fields, no podman required.
func beegfsTestConfig() *config.BeeGFSConfig {
	return &config.BeeGFSConfig{
		Name:            "fs1",
		ContainerPrefix: "pgcli-beegfs-fs1",
		ImageTag:        "8.4.0",
		Listen:          "0.0.0.0",
		MgmtdPort:       config.DefaultBeeGFSMgmtdPort,
		MgmtdGRPCPort:   config.DefaultBeeGFSMgmtdGRPCPort,
		MetaPort:        config.DefaultBeeGFSMetaPort,
		StoragePort:     config.DefaultBeeGFSStoragePort,
		ConnAuth:        "shared-auth",
	}
}

// The three builders must share the vm01-proven skeleton: host network (never a
// -p publish), :z bind mount of the role's subdir under /data, restart policy,
// no --user/privileged flags (images run as root), and the image ref + daemon
// args last.
func assertBeeGFSCommon(t *testing.T, args []string, role, dataDir string) {
	t.Helper()
	joined := strings.Join(args, " ")
	if want := "--name pgcli-beegfs-fs1-" + role; !strings.Contains(joined, want) {
		t.Errorf("args missing %q", want)
	}
	if !slices.Contains(args, "--network") || !slices.Contains(args, "host") {
		t.Errorf("BeeGFS must run on host networking: %v", args)
	}
	if slices.Contains(args, "-p") || slices.Contains(args, "--publish") {
		t.Errorf("host networking must never publish ports: %v", args)
	}
	for _, forbidden := range []string{"--privileged", "--user", "-u"} {
		if slices.Contains(args, forbidden) {
			t.Errorf("forbidden flag %s present (root images, no privilege dance): %v", forbidden, args)
		}
	}
	mount := dataDir + ":/data/" + role + ":z"
	if !slices.Contains(args, mount) {
		t.Errorf("args missing bind mount %q", mount)
	}
	if !slices.Contains(args, "CONN_AUTH_FILE_DATA=shared-auth") {
		t.Error("shared conn-auth env missing")
	}
	if idx := slices.Index(args, "--restart"); idx == -1 || args[idx+1] != "unless-stopped" {
		t.Errorf("missing --restart unless-stopped: %v", args)
	}
	// The image ref must be present and every daemon arg must follow it.
	img := beegfsImageRef(role, "8.4.0")
	i := slices.Index(args, img)
	if i == -1 {
		t.Fatalf("image %s missing", img)
	}
	if !strings.HasPrefix(img, config.BeeGFSImageRepo+"-"+role+":") {
		t.Errorf("image ref %q must derive from BeeGFSImageRepo + role", img)
	}
}

func TestBeeGFSMgmtdArgs(t *testing.T) {
	bc := beegfsTestConfig()
	args := beegfsMgmtdArgsAndEnv(bc, "/base/addon/beegfs/fs1/mgmtd")
	assertBeeGFSCommon(t, args, BeeGFSServiceMgmtd, "/base/addon/beegfs/fs1/mgmtd")

	setupIdx := slices.IndexFunc(args, func(a string) bool { return strings.HasPrefix(a, "beegfs_setup_1=") })
	if setupIdx == -1 {
		t.Fatal("beegfs_setup_1 env missing")
	}
	setup := args[setupIdx]
	// init.py invariant: one setup command per target; v8 mgmtd's target is the
	// db-file parent, and -C/-p style args belong to the other services.
	for _, want := range []string{"beegfs-mgmtd --init", "--db-file /data/mgmtd/mgmtd.sqlite", "--log-target stderr"} {
		if !strings.Contains(setup, want) {
			t.Errorf("setup env missing %q: %s", want, setup)
		}
	}
	// Daemon args after the image: the proven vm01 set, TLS disabled per the
	// v1 decision, and STOCK ports mean NO --beemsg-port/--grpc-port flags.
	after := strings.Join(args[slices.Index(args, beegfsImageRef(BeeGFSServiceMgmtd, "8.4.0"))+1:], " ")
	for _, want := range []string{"--db-file /data/mgmtd/mgmtd.sqlite", "--tls-disable true"} {
		if !strings.Contains(after, want) {
			t.Errorf("mgmtd daemon args missing %q: %s", want, after)
		}
	}
	for _, unwanted := range []string{"--beemsg-port", "--grpc-port"} {
		if strings.Contains(after, unwanted) {
			t.Errorf("default ports must not emit %s: %s", unwanted, after)
		}
	}
}

func TestBeeGFSMetaArgs(t *testing.T) {
	bc := beegfsTestConfig()
	args := beegfsMetaArgsAndEnv(bc, "/base/addon/beegfs/fs1/meta")
	assertBeeGFSCommon(t, args, BeeGFSServiceMeta, "/base/addon/beegfs/fs1/meta")

	var setup string
	for _, a := range args {
		if strings.HasPrefix(a, "beegfs_setup_1=") {
			setup = a
		}
	}
	for _, want := range []string{"beegfs-setup-meta", "-p /data/meta", "-s 1"} {
		if !strings.Contains(setup, want) {
			t.Errorf("meta setup missing %q: %s", want, setup)
		}
	}
	after := strings.Join(args[slices.Index(args, beegfsImageRef(BeeGFSServiceMeta, "8.4.0"))+1:], " ")
	// The cluster self-reference must be loopback under host networking even
	// though Listen is the wildcard — 0.0.0.0 is not a connectable address.
	for _, want := range []string{"sysMgmtdHost=127.0.0.1", "storeMetaDirectory=/data/meta", "storeAllowFirstRunInit=false"} {
		if !strings.Contains(after, want) {
			t.Errorf("meta daemon args missing %q: %s", want, after)
		}
	}
	if strings.Contains(after, "connMgmtdPort") {
		t.Errorf("default mgmtd port must not emit connMgmtdPort: %s", after)
	}
}

func TestBeeGFSStorageArgs(t *testing.T) {
	bc := beegfsTestConfig()
	args := beegfsStorageArgsAndEnv(bc, []string{"/base/addon/beegfs/fs1/storage"})
	assertBeeGFSCommon(t, args, BeeGFSServiceStorage, "/base/addon/beegfs/fs1/storage")

	var setup string
	for _, a := range args {
		if strings.HasPrefix(a, "beegfs_setup_1=") {
			setup = a
		}
	}
	for _, want := range []string{"beegfs-setup-storage", "-p /data/storage", "-s 1", "-i 101"} {
		if !strings.Contains(setup, want) {
			t.Errorf("storage setup missing %q: %s", want, setup)
		}
	}
	after := strings.Join(args[slices.Index(args, beegfsImageRef(BeeGFSServiceStorage, "8.4.0"))+1:], " ")
	if !strings.Contains(after, "storeStorageDirectory=/data/storage") {
		t.Errorf("storage daemon args missing storeStorageDirectory: %s", after)
	}
}

// A moved mgmtd port is the only supported override in the system, so it must
// thread through: mgmtd gets both --beemsg-port/--grpc-port flags, meta and
// storage re-target registration via connMgmtdPort (the TCP-only key — vm01
// showed the TCP/UDP twins are deprecated and error out alongside connMgmtdPort).
func TestBeeGFSMgmtdPortOverrideThreads(t *testing.T) {
	bc := beegfsTestConfig()
	bc.MgmtdPort = 19008
	bc.MgmtdGRPCPort = 19010

	mg := strings.Join(beegfsMgmtdArgsAndEnv(bc, "/d/mgmtd"), " ")
	if !strings.Contains(mg, "--beemsg-port 19008") || !strings.Contains(mg, "--grpc-port 19010") {
		t.Errorf("mgmtd missing moved-port flags: %s", mg)
	}
	for _, args := range [][]string{
		beegfsMetaArgsAndEnv(bc, "/d/meta"),
		beegfsStorageArgsAndEnv(bc, []string{"/d/storage"}),
	} {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "connMgmtdPort=19008") {
			t.Errorf("service missing connMgmtdPort re-target: %s", joined)
		}
		if strings.Contains(joined, "connMgmtdPortTCP") {
			t.Errorf("deprecated connMgmtdPortTCP would abort the daemon: %s", joined)
		}
	}
}

// sysMgmtdHost with an explicit Listen must carry the operator's address, not
// loopback — that is the value clients will replicate from the mount hint.
func TestBeeGFSSysMgmtdHostExplicitListen(t *testing.T) {
	bc := beegfsTestConfig()
	bc.Listen = "10.0.0.5"
	joined := strings.Join(beegfsMetaArgsAndEnv(bc, "/d/meta"), " ")
	if !strings.Contains(joined, "sysMgmtdHost=10.0.0.5") {
		t.Errorf("explicit Listen must become sysMgmtdHost: %s", joined)
	}
}

// v2 join host: MgmtdHost overrides everything (it must NEVER fall back to
// loopback — the mgmtd is on another machine), the node ID reaches both
// daemons' -s, and a moved BeeMsg port still threads connMgmtdPort so the
// joiner dials the remote's moved port.
func TestBeeGFSJoinHostArgs(t *testing.T) {
	bc := beegfsTestConfig()
	bc.Roles = []string{config.BeeGFSServiceStorage}
	bc.MgmtdHost = "10.241.20.147"
	bc.NodeID = 2
	args := beegfsStorageArgsAndEnv(bc, []string{"/d/t1"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "sysMgmtdHost=10.241.20.147") {
		t.Errorf("join host must dial the stored mgmtd address: %s", joined)
	}
	if strings.Contains(joined, "sysMgmtdHost=127.0.0.1") {
		t.Errorf("MgmtdHost must never fall back to loopback on a join host: %s", joined)
	}
	setup := args[slices.IndexFunc(args, func(a string) bool { return strings.HasPrefix(a, "beegfs_setup_1=") })]
	if !strings.Contains(setup, "-s 2") || !strings.Contains(setup, "-i 201") {
		t.Errorf("join storage setup must carry node id 2 / target id 201: %s", setup)
	}

	bc2 := beegfsTestConfig()
	bc2.MgmtdHost = "10.0.0.9"
	bc2.MgmtdPort = 18008
	meta := strings.Join(beegfsMetaArgsAndEnv(bc2, "/d/meta"), " ")
	for _, want := range []string{"sysMgmtdHost=10.0.0.9", "connMgmtdPort=18008"} {
		if !strings.Contains(meta, want) {
			t.Errorf("join meta missing %q: %s", want, meta)
		}
	}
}

// v2 multi-target storage: one container, N setups numbered like the official
// beegfs_setup_N scheme, one -v per target at /data/storage/<targetID>, and a
// comma-joined storeStorageDirectory.
func TestBeeGFSStorageMultiTarget(t *testing.T) {
	bc := beegfsTestConfig()
	bc.NodeID = 2
	args := beegfsStorageArgsAndEnv(bc, []string{"/mnt/d1", "/mnt/d2"})
	joined := strings.Join(args, " ")

	var setups []string
	for _, a := range args {
		if strings.HasPrefix(a, "beegfs_setup_") {
			setups = append(setups, a)
		}
	}
	if len(setups) != 2 {
		t.Fatalf("want 2 setup envs, got %v", setups)
	}
	if !strings.HasPrefix(setups[0], "beegfs_setup_1=beegfs-setup-storage -C -p /data/storage/201 -s 2 -i 201") {
		t.Errorf("setup_1 wrong: %s", setups[0])
	}
	if !strings.HasPrefix(setups[1], "beegfs_setup_2=beegfs-setup-storage -C -p /data/storage/202 -s 2 -i 202") {
		t.Errorf("setup_2 wrong: %s", setups[1])
	}
	for _, want := range []string{"/mnt/d1:/data/storage/201:z", "/mnt/d2:/data/storage/202:z"} {
		if !slices.Contains(args, want) {
			t.Errorf("missing per-target bind %q: %v", want, args)
		}
	}
	if !strings.Contains(joined, "storeStorageDirectory=/data/storage/201,/data/storage/202") {
		t.Errorf("storeStorageDirectory must list every target dir: %s", joined)
	}
}

// The single-target container path must stay EXACTLY /data/storage — the v1
// target dir records its own path internally, so a revived v1 store breaks if
// this ever moves.
func TestBeeGFSStorageSingleTargetPathIsV1(t *testing.T) {
	bc := beegfsTestConfig()
	args := beegfsStorageArgsAndEnv(bc, []string{"/d/storage"})
	if !slices.Contains(args, "/d/storage:/data/storage:z") {
		t.Errorf("single target must mount at the v1 path /data/storage: %v", args)
	}
}

// The license env rides ONLY to mgmtd (its image alone ships
// libbeegfs_license.so, and its start.sh wires the env to
// --license-cert-file). The exact argv entry must be the raw PEM — init.py
// writes the env value verbatim to /etc/beegfs/license.pem — and meta/storage
// must never see it even when the config carries one.
func TestBeeGFSLicenseEnvIsMgmtdOnly(t *testing.T) {
	const pem = "-----BEGIN CERTIFICATE-----\nMIIBfake\n-----END CERTIFICATE-----"
	bc := beegfsTestConfig()
	bc.License = pem

	mg := beegfsMgmtdArgsAndEnv(bc, "/d/mgmtd")
	if !hasFlagValue(mg, "-e", "BEEGFS_LICENSE_FILE_DATA="+pem) {
		t.Errorf("mgmtd args missing the exact license env: %v", mg)
	}
	for name, args := range map[string][]string{
		"meta":    beegfsMetaArgsAndEnv(bc, "/d/meta"),
		"storage": beegfsStorageArgsAndEnv(bc, []string{"/d/storage"}),
	} {
		if strings.Contains(strings.Join(args, " "), "BEEGFS_LICENSE_FILE_DATA") {
			t.Errorf("%s must never receive the license env: %v", name, args)
		}
	}

	// No license → no empty env either.
	bc.License = ""
	if strings.Contains(strings.Join(beegfsMgmtdArgsAndEnv(bc, "/d/mgmtd"), " "), "BEEGFS_LICENSE_FILE_DATA") {
		t.Error("unlicensed mgmtd must not carry the env at all")
	}
}

// hasFlagValue reports whether flag is immediately followed by value.
func hasFlagValue(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestBeeGFSImageRefsDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, role := range []string{BeeGFSServiceMgmtd, BeeGFSServiceMeta, BeeGFSServiceStorage} {
		ref := beegfsImageRef(role, "8.4.0")
		want := "ghcr.io/thinkparq/beegfs-" + role + ":8.4.0"
		if ref != want {
			t.Errorf("image ref = %q, want %q", ref, want)
		}
		if seen[ref] {
			t.Errorf("duplicate image ref %s", ref)
		}
		seen[ref] = true
	}
}

func TestBeeGFSClusterState(t *testing.T) {
	cases := []struct {
		st   ClusterState
		want string
	}{
		{ClusterState{MgmtdUp: true, MetaUp: true, StorageUp: true}, "running"},
		{ClusterState{}, "stopped"},
		{ClusterState{MgmtdUp: true}, "partial (mgmtd)"},
		{ClusterState{MgmtdUp: true, MetaUp: true}, "partial (mgmtd,meta)"},
		{ClusterState{MetaUp: true, StorageUp: true}, "partial (meta,storage)"},
	}
	for _, c := range cases {
		if got := c.st.String(); got != c.want {
			t.Errorf("ClusterState(%+v) = %q, want %q", c.st, got, c.want)
		}
	}
	full := ClusterState{MgmtdUp: true, MetaUp: true, StorageUp: true}
	if !full.AllUp() || (ClusterState{MgmtdUp: true}).AllUp() {
		t.Error("AllUp wrong")
	}
	if !(ClusterState{}).AllDown() || (ClusterState{StorageUp: true}).AllDown() {
		t.Error("AllDown wrong")
	}
}

// v2: a state built from a config counts only the roles THAT host runs — a
// storage-only member is "running" with just its own container up, and a
// hand-built zero state keeps the legacy full-trio semantics.
func TestBeeGFSClusterStateRolesAware(t *testing.T) {
	storageOnly := &config.BeeGFSConfig{Roles: []string{config.BeeGFSServiceStorage}}
	st := ClusterState{bc: storageOnly, StorageUp: true}
	if got := st.String(); got != "running" {
		t.Errorf("storage-only host with storage up = %q, want running", got)
	}
	if !st.AllUp() {
		t.Error("storage-only host with storage up must be AllUp")
	}
	if (ClusterState{bc: storageOnly}).AllUp() || !(ClusterState{bc: storageOnly}).AllDown() {
		t.Error("storage-only host with nothing up must be AllDown, not AllUp")
	}
	// A stopped meta on a host running meta+storage is partial, counted over
	// exactly its own roles.
	join := &config.BeeGFSConfig{Roles: []string{config.BeeGFSServiceMeta, config.BeeGFSServiceStorage}}
	if got := (ClusterState{bc: join, StorageUp: true}).String(); got != "partial (storage)" {
		t.Errorf("meta+storage host, storage up only = %q, want partial (storage)", got)
	}
}
