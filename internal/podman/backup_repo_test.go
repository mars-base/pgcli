package podman

import (
	"os"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
	yaml "gopkg.in/yaml.v3"
)

func TestPatroniStanzaName(t *testing.T) {
	// Cluster-wide, not per-member: stanza-create must reach the primary,
	// which a per-member stanza cannot do ([056]). The name carries the
	// DCS-qualified scope so two pgcli namespaces sharing one S3 bucket stay
	// apart (a bare scope would collide on pgcli_app).
	if got, want := patroniStanzaName("app"), "pgcli_app"; got != want {
		t.Fatalf("stanza name = %q, want %q", got, want)
	}
	if got, want := patroniStanzaName("app-prod"), "pgcli_app-prod"; got != want {
		t.Fatalf("namespaced stanza name = %q, want %q", got, want)
	}
}

func testS3Repo() *config.BackupRepoS3 {
	return &config.BackupRepoS3{
		Endpoint: "10.0.0.9:9000", Bucket: "pgbackrest", Region: "us-east-1",
		Path: "/pgbackrest", AccessKey: "admin", SecretKey: "s3cr3t", URIStyle: "path",
	}
}

func testTargets() []patroniBackupTarget {
	return []patroniBackupTarget{
		{scope: "app", member: "node1", alias: "pgcli-patroni-app-node1", hostPort: 35532},
		{scope: "app", member: "node2", alias: "pgcli-patroni-app-node2", hostPort: 35533},
		{scope: "app", member: "node3", alias: "10.0.0.12", hostPort: 35532}, // cross-host
		{scope: "other", member: "node1", alias: "pgcli-patroni-other-node1", hostPort: 35600},
	}
}

// The backup-container view: ONE stanza per cluster carrying every member as
// an additional pg*-host, so pgBackRest locates the primary itself and survives
// failovers. A per-member stanza (the previous design) fails stanza-create on
// a replica with [056] "unable to find primary cluster".
func TestPatroniBackupStanzasClusterWide(t *testing.T) {
	m := &BackupManager{cfg: &config.Config{Backup: config.BackupConfig{Repo: config.BackupRepo{S3: testS3Repo()}}}}
	got, count := m.patroniBackupStanzas(testTargets())
	if count != 2 {
		t.Fatalf("stanza count = %d, want 2 (one per scope)", count)
	}
	if strings.Contains(got, "pgcli_app_node") {
		t.Fatalf("per-member stanza name leaked:\n%s", got)
	}
	for _, want := range []string{
		"[pgcli_app]", "[pgcli_other]",
		"pg1-host=pgcli-patroni-app-node1",
		"pg2-host=pgcli-patroni-app-node2",
		"pg3-host=10.0.0.12",
		"pg3-port=35532",
		"pg3-socket-path=/var/lib/postgresql",
		"repo1-type=s3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("backup stanzas missing %q:\n%s", want, got)
		}
	}
}

// The member-local view: hostless single stanza per cluster — archive-push only
// reads pg1-path, and any pg*-host aborts it with [072].
func TestPatroniArchiveStanzasHostless(t *testing.T) {
	m := &BackupManager{cfg: &config.Config{Backup: config.BackupConfig{Repo: config.BackupRepo{S3: testS3Repo()}}}}
	got, count := m.patroniArchiveStanzas(testTargets())
	if count != 2 {
		t.Fatalf("stanza count = %d, want 2", count)
	}
	if strings.Contains(got, "-host=") || strings.Contains(got, "pg1-port=") {
		t.Fatalf("archive view must carry no pg*-host/port ([072]):\n%s", got)
	}
	for _, want := range []string{"[pgcli_app]", "pg1-path=/var/lib/postgresql/data", "repo1-type=s3"} {
		if !strings.Contains(got, want) {
			t.Errorf("archive stanzas missing %q:\n%s", want, got)
		}
	}
	// Both views agree on the stanza set and the S3 repo scoping.
	backing, _ := m.patroniBackupStanzas(testTargets())
	if strings.Count(backing, "repo1-s3-key-secret=") != 2 || strings.Count(got, "repo1-s3-key-secret=") != 2 {
		t.Error("S3 repo must appear exactly once per cluster stanza")
	}

	// The generator must qualify the scope with the pgcli namespace: two
	// namespaces sharing one S3 bucket must not collide on one stanza.
	mNS := &BackupManager{cfg: &config.Config{Namespace: "prod", Backup: config.BackupConfig{Repo: config.BackupRepo{S3: testS3Repo()}}}}
	nsGot, _ := mNS.patroniBackupStanzas(testTargets())
	if !strings.Contains(nsGot, "[pgcli_app-prod]") || strings.Contains(nsGot, "[pgcli_app]\n") {
		t.Errorf("namespaced stanza header wrong:\n%s", nsGot)
	}
	nsArc, _ := mNS.patroniArchiveStanzas(testTargets())
	if !strings.Contains(nsArc, "[pgcli_app-prod]") {
		t.Errorf("namespaced archive stanza wrong:\n%s", nsArc)
	}
}

func TestS3StanzaLines(t *testing.T) {
	// No S3 configured -> empty, so [global] stays local and regular instances
	// are unaffected.
	m := &BackupManager{cfg: &config.Config{Backup: config.BackupConfig{RetentionFull: 7}}}
	if got := m.s3StanzaLines(); got != "" {
		t.Fatalf("expected empty s3 block, got %q", got)
	}

	verifyOff := false
	m2 := &BackupManager{cfg: &config.Config{Backup: config.BackupConfig{
		RetentionFull: 7,
		Repo: config.BackupRepo{S3: &config.BackupRepoS3{
			Endpoint: "10.0.0.9:9000", Bucket: "pgbackrest", Region: "us-east-1",
			Path: "/pgbackrest", AccessKey: "admin", SecretKey: "s3cr3t",
			URIStyle: "path", CAFile: "/host/ca.crt", VerifyTLS: &verifyOff,
		}},
	}}}
	got := m2.s3StanzaLines()
	for _, want := range []string{
		"repo1-type=s3",
		"repo1-s3-endpoint=10.0.0.9:9000",
		"repo1-s3-bucket=pgbackrest",
		"repo1-s3-key=admin",
		"repo1-s3-key-secret=s3cr3t",
		"repo1-s3-verify-tls=n",
		"repo1-s3-ca-file=/etc/pgbackrest/ca.crt",
		"repo1-path=/pgbackrest",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("s3 block missing %q:\n%s", want, got)
		}
	}
	// The block must NOT touch the local [global] repo path — it only carries
	// per-stanza S3 overrides.
	if strings.Contains(got, "repo1-retention") || strings.Contains(got, "compress") {
		t.Errorf("s3 block leaked global settings:\n%s", got)
	}
}

// Verify the default verify-tls is "y" when unset (nil) and the CA line is
// omitted when no CAFile is configured.
func TestS3StanzaLinesDefaults(t *testing.T) {
	m := &BackupManager{cfg: &config.Config{Backup: config.BackupConfig{
		RetentionFull: 7,
		Repo: config.BackupRepo{S3: &config.BackupRepoS3{
			Endpoint: "s3.amazonaws.com:443", Bucket: "b", Region: "us-east-1",
			Path: "/p", AccessKey: "k", SecretKey: "v", URIStyle: "host",
		}},
	}}}
	got := m.s3StanzaLines()
	if !strings.Contains(got, "repo1-s3-verify-tls=y\n") {
		t.Errorf("expected verify-tls=y by default:\n%s", got)
	}
	if strings.Contains(got, "ca-file") {
		t.Errorf("expected no ca-file line when CAFile empty:\n%s", got)
	}
}

// Archive parameters must land in the member's LOCAL patroni.yml only when the
// S3 repo is configured (the opt-in): HA clusters without S3 keep their
// current no-archiving behaviour. The stanza is cluster-wide, so the same
// command is valid for every member; it still rides in local config because
// DCS is edit-config's territory (Patroni applies a GUC from patroni.yml when
// DCS does not manage it).
func TestPatroniArchiveParamsGatedOnS3Repo(t *testing.T) {
	cluster := testPatroniCluster()
	cluster.EtcdEndpoints = []string{"127.0.0.1:2379"}

	// No S3 repo: untouched (existing HA clusters keep current behaviour).
	cfgOff := config.Default()
	mOff := &PatroniManager{cfg: cfgOff, dataDir: t.TempDir()}
	path, err := mOff.WriteMemberConfig(cluster, "node1")
	if err != nil {
		t.Fatalf("WriteMemberConfig: %v", err)
	}
	out, _ := os.ReadFile(path)
	if strings.Contains(string(out), "archive_mode") {
		t.Fatalf("archive params must be absent without an S3 repo:\n%s", out)
	}

	// S3 repo: archive_mode/timeout/command rendered with the member stanza.
	cfgOn := config.Default()
	cfgOn.Backup.Repo.S3 = &config.BackupRepoS3{Endpoint: "10.0.0.9:9000", Bucket: "b", AccessKey: "k", SecretKey: "v"}
	mOn := &PatroniManager{cfg: cfgOn, dataDir: t.TempDir()}
	path, err = mOn.WriteMemberConfig(cluster, "node1")
	if err != nil {
		t.Fatalf("WriteMemberConfig: %v", err)
	}
	out, _ = os.ReadFile(path)
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	params := doc["postgresql"].(map[string]any)["parameters"].(map[string]any)
	if params["archive_mode"] != "on" {
		t.Errorf("archive_mode = %v, want on", params["archive_mode"])
	}
	if params["archive_timeout"] != 10 {
		t.Errorf("archive_timeout = %v, want 10", params["archive_timeout"])
	}
	wantCmd := `pgbackrest --stanza=pgcli_app archive-push "%p"`
	if params["archive_command"] != wantCmd {
		t.Errorf("archive_command = %v, want %v", params["archive_command"], wantCmd)
	}
	// DCS bootstrap block must NOT carry it — pgcli never owns DCS params.
	dcs := doc["bootstrap"].(map[string]any)["dcs"].(map[string]any)
	dcsParams := dcs["postgresql"].(map[string]any)["parameters"].(map[string]any)
	if _, ok := dcsParams["archive_command"]; ok {
		t.Error("archive_command must not be in bootstrap.dcs (DCS is edit-config's territory)")
	}
}
