package podman

import (
	"testing"
)

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
