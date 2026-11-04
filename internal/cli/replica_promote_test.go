package cli

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func s3Repo() *config.BackupRepoS3 {
	return &config.BackupRepoS3{Endpoint: "10.0.0.9:9000", Bucket: "b", AccessKey: "k", SecretKey: "v"}
}

// promotionBackupTarget: a complete global S3 always wins; every other shape —
// same-host or cross-host replica alike — falls back to the historical local
// repo. Only an S3 block that is present but incomplete is refused, because
// silently starting local would ignore an obvious misconfiguration.
func TestPromotionBackupTarget(t *testing.T) {
	cases := []struct {
		name    string
		s3      *config.BackupRepoS3
		want    string
		wantErr string
	}{
		{
			name: "with s3",
			s3:   s3Repo(),
			want: config.BackupTargetS3,
		},
		{
			name: "without s3",
			s3:   nil,
			want: "",
		},
		{
			name:    "s3 configured but incomplete",
			s3:      &config.BackupRepoS3{Endpoint: "10.0.0.9:9000"},
			wantErr: "incomplete",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Backup.Repo.S3 = c.s3
			got, err := promotionBackupTarget(cfg)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("target = %q, want %q", got, c.want)
			}
		})
	}
}
