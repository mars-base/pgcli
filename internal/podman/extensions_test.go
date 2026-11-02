package podman

import (
	"slices"
	"strings"
	"testing"
)

func TestExtContainerfileRuntimeUser(t *testing.T) {
	tests := []struct {
		name      string
		buildFrom string
		baseUser  string
		wantLast  string
	}{
		{
			name:      "root base image keeps root runtime user",
			buildFrom: "ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0",
			baseUser:  "",
			wantLast:  "",
		},
		{
			name:      "patroni base image keeps postgres runtime user",
			buildFrom: "ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5",
			baseUser:  "postgres",
			wantLast:  "USER postgres\n",
		},
		{
			name:      "ext base image keeps postgres runtime user",
			buildFrom: "ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5-ext",
			baseUser:  "postgres",
			wantLast:  "USER postgres\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extContainerfile(tt.buildFrom, []string{"postgresql-18-pgmq"}, nil, "https://repo.pigsty.io", tt.baseUser)
			if !strings.Contains(got, "USER root") {
				t.Errorf("containerfile missing USER root for apt install:\n%s", got)
			}
			if tt.wantLast == "" {
				if strings.Contains(got, "USER postgres") {
					t.Errorf("containerfile must not force postgres for a root base image:\n%s", got)
				}
				return
			}
			if !strings.HasSuffix(got, tt.wantLast) {
				t.Errorf("containerfile should end with %q, got:\n%s", tt.wantLast, got)
			}
		})
	}
}

func TestSameImageUser(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"", "", true},
		{"", "root", true},
		{"root", "0", true},
		{"0:0", "", true},
		{"postgres", "postgres", true},
		{"postgres", "", false},
		{"postgres", "root", false},
	}
	for _, tt := range tests {
		if got := sameImageUser(tt.a, tt.b); got != tt.want {
			t.Errorf("sameImageUser(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestBuildPreloadCSV(t *testing.T) {
	tests := []struct {
		name       string
		extensions []string
		wantCSV    string
		wantCron   bool
	}{
		{
			name:       "empty list",
			extensions: []string{},
			wantCSV:    "",
			wantCron:   false,
		},
		{
			name:       "single extension needing preload",
			extensions: []string{"pg_stat_statements"},
			wantCSV:    "pg_stat_statements",
			wantCron:   false,
		},
		{
			name:       "multiple extensions needing preload",
			extensions: []string{"pg_stat_statements", "pg_prewarm"},
			wantCSV:    "pg_stat_statements,pg_prewarm",
			wantCron:   false,
		},
		{
			name:       "pg_cron sets hasCron flag",
			extensions: []string{"pg_stat_statements", "pg_cron"},
			wantCSV:    "pg_stat_statements,pg_cron",
			wantCron:   true,
		},
		{
			name:       "citus forced first",
			extensions: []string{"pg_stat_statements", "citus"},
			wantCSV:    "citus,pg_stat_statements",
			wantCron:   false,
		},
		{
			name:       "citus and pg_cron together",
			extensions: []string{"pg_stat_statements", "citus", "pg_cron"},
			wantCSV:    "citus,pg_stat_statements,pg_cron",
			wantCron:   true,
		},
		{
			name:       "timescaledb forced first",
			extensions: []string{"pg_stat_statements", "timescaledb"},
			wantCSV:    "timescaledb,pg_stat_statements",
			wantCron:   false,
		},
		{
			name:       "multiple preload-first extensions",
			extensions: []string{"pg_stat_statements", "citus", "timescaledb"},
			wantCSV:    "citus,timescaledb,pg_stat_statements",
			wantCron:   false,
		},
		{
			name:       "extensions not needing preload are skipped",
			extensions: []string{"hstore", "pg_stat_statements", "uuid-ossp"},
			wantCSV:    "pg_stat_statements",
			wantCron:   false,
		},
		{
			name:       "all extensions builtin (no preload needed)",
			extensions: []string{"hstore", "uuid-ossp"},
			wantCSV:    "",
			wantCron:   false,
		},
		{
			name:       "documentdb expands to four libraries",
			extensions: []string{"documentdb"},
			wantCSV:    "pg_cron,pg_documentdb_core,pg_documentdb,pg_documentdb_extended_rum",
			wantCron:   true,
		},
		{
			name:       "documentdb deduplicates an explicit pg_cron",
			extensions: []string{"pg_cron", "documentdb"},
			wantCSV:    "pg_cron,pg_documentdb_core,pg_documentdb,pg_documentdb_extended_rum",
			wantCron:   true,
		},
		{
			name:       "documentdb keeps preload-first ahead of its libraries",
			extensions: []string{"documentdb", "citus"},
			wantCSV:    "citus,pg_cron,pg_documentdb_core,pg_documentdb,pg_documentdb_extended_rum",
			wantCron:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCSV, gotCron := BuildPreloadCSV(tt.extensions)
			if gotCSV != tt.wantCSV {
				t.Errorf("BuildPreloadCSV() csv = %q, want %q", gotCSV, tt.wantCSV)
			}
			if gotCron != tt.wantCron {
				t.Errorf("BuildPreloadCSV() hasCron = %v, want %v", gotCron, tt.wantCron)
			}
		})
	}
}

func TestExtensionImageTag(t *testing.T) {
	tests := []struct {
		name    string
		baseTag string
		exts    []string
		want    string
	}{
		{
			name:    "pgcli-pg base image",
			baseTag: "ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0",
			exts:    []string{"pg_stat_statements"},
			want:    "ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0-ext",
		},
		{
			name:    "pgcli-patroni base image",
			baseTag: "ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5",
			exts:    []string{"pg_cron"},
			want:    "ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5-ext",
		},
		{
			name:    "already -ext image strips suffix before re-adding",
			baseTag: "ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0-ext",
			exts:    []string{"pg_stat_statements"},
			want:    "ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0-ext",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtensionImageTag(tt.baseTag, tt.exts)
			if got != tt.want {
				t.Errorf("ExtensionImageTag() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBaseImageTag(t *testing.T) {
	tests := []struct {
		name string
		tag  string
		want string
	}{
		{
			name: "no -ext suffix",
			tag:  "ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0",
			want: "ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0",
		},
		{
			name: "with -ext suffix",
			tag:  "ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0-ext",
			want: "ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0",
		},
		{
			name: "patroni with -ext",
			tag:  "ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5-ext",
			want: "ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BaseImageTag(tt.tag)
			if got != tt.want {
				t.Errorf("BaseImageTag() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtMustBeFirst(t *testing.T) {
	tests := []struct {
		name string
		ext  string
		want bool
	}{
		{"citus must be first", "citus", true},
		{"timescaledb must be first", "timescaledb", true},
		{"pg_stat_statements no order requirement", "pg_stat_statements", false},
		{"pg_cron no order requirement", "pg_cron", false},
		{"hstore (builtin, no preload)", "hstore", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtMustBeFirst(tt.ext)
			if got != tt.want {
				t.Errorf("ExtMustBeFirst(%q) = %v, want %v", tt.ext, got, tt.want)
			}
		})
	}
}

func TestHasNonBuiltinExtensions(t *testing.T) {
	tests := []struct {
		name       string
		extensions []string
		want       bool
	}{
		{
			name:       "empty list",
			extensions: []string{},
			want:       false,
		},
		{
			name:       "all builtin",
			extensions: []string{"hstore", "uuid-ossp"},
			want:       false,
		},
		{
			name:       "one non-builtin",
			extensions: []string{"hstore", "pg_cron"},
			want:       true,
		},
		{
			name:       "all non-builtin",
			extensions: []string{"pg_stat_statements", "pg_cron"},
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasNonBuiltinExtensions(tt.extensions)
			if got != tt.want {
				t.Errorf("HasNonBuiltinExtensions() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDocumentDBExtDebURL(t *testing.T) {
	const base = "https://github.com/documentdb/documentdb/releases/download/v1.0-0/"
	tests := []struct {
		arch    string
		wantURL string
		wantSHA string
	}{
		{
			arch:    "amd64",
			wantURL: base + "ubuntu24.04-postgresql-18-documentdb_1.0-0_amd64.deb",
			wantSHA: "cc26f8225af13233f901f86292d5a8373276654454c671316cd603d20378fdbf",
		},
		{
			arch:    "arm64",
			wantURL: base + "ubuntu24.04-postgresql-18-documentdb_1.0-0_arm64.deb",
			wantSHA: "6afc91e21b72990d1d43421b79e4484d7cd55366c1d0436fa71f4cc2f119d6eb",
		},
	}
	for _, tt := range tests {
		url, sha, err := documentdbExtDebURL(tt.arch)
		if err != nil {
			t.Fatalf("%s: %v", tt.arch, err)
		}
		if url != tt.wantURL {
			t.Errorf("%s URL = %q, want %q", tt.arch, url, tt.wantURL)
		}
		if sha != tt.wantSHA {
			t.Errorf("%s sha = %q, want %q", tt.arch, sha, tt.wantSHA)
		}
	}
	if _, _, err := documentdbExtDebURL("riscv64"); err == nil || !strings.Contains(err.Error(), "unsupported architecture") {
		t.Errorf("unknown arch: want unsupported-architecture error, got %v", err)
	}
}

func TestDocumentDBIcuDebURL(t *testing.T) {
	tests := []struct {
		arch    string
		wantURL string
		wantSHA string
	}{
		{
			arch:    "amd64",
			wantURL: "https://archive.ubuntu.com/ubuntu/pool/main/i/icu/libicu74_74.2-1ubuntu3.1_amd64.deb",
			wantSHA: "c9a70989678660eed9a1e904c74fa043da8bec8e2036856fc16e31ced79b04f8",
		},
		{
			arch:    "arm64",
			wantURL: "https://ports.ubuntu.com/ubuntu-ports/pool/main/i/icu/libicu74_74.2-1ubuntu3.1_arm64.deb",
			wantSHA: "48f93acf50dcf237a8d58ce366730a28438ce52d3f06d7a2a88b51261dd791f7",
		},
	}
	for _, tt := range tests {
		url, sha, err := documentdbIcuDebURL(tt.arch)
		if err != nil {
			t.Fatalf("%s: %v", tt.arch, err)
		}
		if url != tt.wantURL {
			t.Errorf("%s URL = %q, want %q", tt.arch, url, tt.wantURL)
		}
		if sha != tt.wantSHA {
			t.Errorf("%s sha = %q, want %q", tt.arch, sha, tt.wantSHA)
		}
	}
	if _, _, err := documentdbIcuDebURL("riscv64"); err == nil || !strings.Contains(err.Error(), "unsupported architecture") {
		t.Errorf("unknown arch: want unsupported-architecture error, got %v", err)
	}
}

func TestExtContainerfileDocumentDB(t *testing.T) {
	debs := []string{documentdbDebFileName, documentdbIcuDebFileName}
	got := extContainerfile("postgres:18", []string{"postgresql-18-pgvector"}, debs, "https://repo.pigsty.io", "")
	for _, want := range []string{
		"COPY pgcli-documentdb.deb /tmp/pgcli-documentdb.deb",
		"COPY pgcli-libicu74.deb /tmp/pgcli-libicu74.deb",
		"apt-get install -y postgresql-18-pgvector /tmp/pgcli-documentdb.deb /tmp/pgcli-libicu74.deb patchelf",
		"patchelf --add-needed libicui18n.so.74 /usr/lib/postgresql/18/lib/pg_documentdb_core.so",
		"apt-get purge -y --auto-remove curl gnupg2 lsb-release patchelf",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("containerfile missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "postgresql-18-documentdb") {
		t.Errorf("documentdb must not be requested from the repo as postgresql-18-documentdb:\n%s", got)
	}

	// Rebuild-on-existing-ext path must also copy the DEBs and patch the lib.
	ext := extContainerfile("postgres:18-ext", nil, debs, "https://repo.pigsty.io", "postgres")
	for _, want := range []string{
		"COPY pgcli-documentdb.deb /tmp/pgcli-documentdb.deb",
		"COPY pgcli-libicu74.deb /tmp/pgcli-libicu74.deb",
		"apt-get install -y /tmp/pgcli-documentdb.deb /tmp/pgcli-libicu74.deb patchelf \\",
		"patchelf --add-needed libicui18n.so.74 /usr/lib/postgresql/18/lib/pg_documentdb_core.so",
		"apt-get purge -y patchelf",
	} {
		if !strings.Contains(ext, want) {
			t.Errorf("ext-base containerfile missing %q:\n%s", want, ext)
		}
	}
	if !strings.HasSuffix(ext, "USER postgres\n") {
		t.Errorf("containerfile must restore the base runtime user:\n%s", ext)
	}

	// A non-DocumentDB vendor DEB must not pull in patchelf.
	plain := extContainerfile("postgres:18-ext", nil, []string{"other.deb"}, "https://repo.pigsty.io", "")
	if strings.Contains(plain, "patchelf") {
		t.Errorf("non-DocumentDB build must not use patchelf:\n%s", plain)
	}
}

func TestExtPreloadLibsDocumentDB(t *testing.T) {
	libs, first := ExtPreloadLibs("documentdb")
	want := []string{"pg_cron", "pg_documentdb_core", "pg_documentdb", "pg_documentdb_extended_rum"}
	if !slices.Equal(libs, want) {
		t.Errorf("ExtPreloadLibs(documentdb) = %v, want %v", libs, want)
	}
	if first {
		t.Error("documentdb must not be forced to position 0")
	}

	libs, first = ExtPreloadLibs("citus")
	if !slices.Equal(libs, []string{"citus"}) || !first {
		t.Errorf("ExtPreloadLibs(citus) = %v, first=%v; want [citus] true", libs, first)
	}

	if libs, _ := ExtPreloadLibs("hstore"); libs != nil {
		t.Errorf("ExtPreloadLibs(hstore) = %v, want nil", libs)
	}
	if libs, _ := ExtPreloadLibs("pgvector"); libs != nil {
		t.Errorf("ExtPreloadLibs(pgvector) = %v, want nil (vector has no preload)", libs)
	}

	if !ExtNeedsPreload("documentdb") {
		t.Error("ExtNeedsPreload(documentdb) must be true")
	}
	if ExtMustBeFirst("documentdb") {
		t.Error("ExtMustBeFirst(documentdb) must be false")
	}
}

func TestRenderExtConfBlock(t *testing.T) {
	got := renderExtConfBlock(
		[]string{"pg_cron", "pg_documentdb_core", "pg_documentdb", "pg_documentdb_extended_rum"},
		true, true, "demo_db", 56432,
	)
	wants := []string{
		pgExtBegin,
		"shared_preload_libraries = 'pg_cron,pg_documentdb_core,pg_documentdb,pg_documentdb_extended_rum'",
		"cron.database_name = 'demo_db'",
		"cron.use_background_workers = on",
		"documentdb.enableBackgroundWorker = true",
		"documentdb.enableBackgroundWorkerJobs = true",
		"documentdb.indexBuildsScheduledOnBgWorker = false",
		"documentdb.localhost_connection_string = 'host=/var/run/postgresql port=56432'",
		"default_toast_compression = 'lz4'",
		"documentdb.rum_library_load_option = 'require_documentdb_extended_rum'",
		"documentdb.alternate_index_handler_name = 'extended_rum'",
		pgExtEnd,
	}
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if !slices.Equal(lines, wants) {
		t.Errorf("renderExtConfBlock() =\n%s\nwant lines:\n%s", got, strings.Join(wants, "\n"))
	}

	empty := renderExtConfBlock(nil, false, false, "demo_db", 5000)
	if extBlockHasSettings(empty) {
		t.Errorf("empty block must have no settings:\n%s", empty)
	}
	if !extBlockHasSettings(got) {
		t.Errorf("documentdb block must report settings:\n%s", got)
	}
}

func TestExtCreateStatements(t *testing.T) {
	got := ExtCreateStatements("documentdb")
	want := []string{
		"CREATE EXTENSION IF NOT EXISTS pg_cron",
		`CREATE EXTENSION IF NOT EXISTS "documentdb" CASCADE`,
		`CREATE EXTENSION IF NOT EXISTS "documentdb_extended_rum"`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("ExtCreateStatements(documentdb) = %v, want %v", got, want)
	}

	got = ExtCreateStatements("pgvector")
	if !slices.Equal(got, []string{`CREATE EXTENSION IF NOT EXISTS "vector"`}) {
		t.Errorf("ExtCreateStatements(pgvector) = %v", got)
	}
}

func TestExtDropStatements(t *testing.T) {
	withCron := ExtDropStatements("documentdb", true)
	want := []string{
		`DROP EXTENSION IF EXISTS "documentdb_extended_rum"`,
		`DROP EXTENSION IF EXISTS "documentdb"`,
		`DROP EXTENSION IF EXISTS "documentdb_core"`,
		`DROP EXTENSION IF EXISTS pg_cron`,
	}
	if !slices.Equal(withCron, want) {
		t.Errorf("ExtDropStatements(documentdb, true) = %v, want %v", withCron, want)
	}
	if got := ExtDropStatements("documentdb", false); len(got) != 3 {
		t.Errorf("ExtDropStatements(documentdb, false) must keep pg_cron: %v", got)
	}
	if got := ExtDropStatements("pg_cron", false); !slices.Equal(got, []string{`DROP EXTENSION IF EXISTS "pg_cron"`}) {
		t.Errorf("ExtDropStatements(pg_cron) = %v", got)
	}

	if !ExtensionsNeedCron([]string{"hstore", "documentdb"}) {
		t.Error("documentdb must count as needing pg_cron")
	}
	if !ExtensionsNeedCron([]string{"pg_cron"}) {
		t.Error("pg_cron must report itself")
	}
	if ExtensionsNeedCron([]string{"hstore", "pgvector"}) {
		t.Error("no cron consumer expected")
	}
}
