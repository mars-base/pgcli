package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// pgExporterPasswordCmd returns a command carrying just the two monitor-password
// flags; Set() marks them as changed, matching how the real install command
// distinguishes "unset" from "explicitly empty".
func pgExporterPasswordCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().String("monitor-password", "", "")
	cmd.Flags().String("monitor-password-file", "", "")
	return cmd
}

func TestResolvePGExporterMonitorPassword(t *testing.T) {
	t.Run("unset generates", func(t *testing.T) {
		pw, set, err := resolvePGExporterMonitorPassword(pgExporterPasswordCmd())
		if err != nil || set || pw != "" {
			t.Fatalf("got pw=%q set=%v err=%v, want empty/unset", pw, set, err)
		}
	})

	t.Run("inline value", func(t *testing.T) {
		cmd := pgExporterPasswordCmd()
		if err := cmd.Flags().Set("monitor-password", "s3cret"); err != nil {
			t.Fatal(err)
		}
		pw, set, err := resolvePGExporterMonitorPassword(cmd)
		if err != nil || !set || pw != "s3cret" {
			t.Fatalf("got pw=%q set=%v err=%v", pw, set, err)
		}
	})

	t.Run("file strips trailing newlines", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "monitor.pw")
		if err := os.WriteFile(path, []byte("file-secret\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := pgExporterPasswordCmd()
		if err := cmd.Flags().Set("monitor-password-file", path); err != nil {
			t.Fatal(err)
		}
		pw, set, err := resolvePGExporterMonitorPassword(cmd)
		if err != nil || !set || pw != "file-secret" {
			t.Fatalf("got pw=%q set=%v err=%v", pw, set, err)
		}
	})

	t.Run("inline and file are mutually exclusive", func(t *testing.T) {
		cmd := pgExporterPasswordCmd()
		if err := cmd.Flags().Set("monitor-password", "inline"); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Flags().Set("monitor-password-file", "/tmp/whatever"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := resolvePGExporterMonitorPassword(cmd); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("explicit empty inline value is rejected", func(t *testing.T) {
		cmd := pgExporterPasswordCmd()
		if err := cmd.Flags().Set("monitor-password", ""); err != nil {
			t.Fatal(err)
		}
		if _, _, err := resolvePGExporterMonitorPassword(cmd); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("empty file is rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty.pw")
		if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := pgExporterPasswordCmd()
		if err := cmd.Flags().Set("monitor-password-file", path); err != nil {
			t.Fatal(err)
		}
		if _, _, err := resolvePGExporterMonitorPassword(cmd); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("NUL bytes in file are rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nul.pw")
		if err := os.WriteFile(path, []byte("a\x00b"), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := pgExporterPasswordCmd()
		if err := cmd.Flags().Set("monitor-password-file", path); err != nil {
			t.Fatal(err)
		}
		if _, _, err := resolvePGExporterMonitorPassword(cmd); err == nil {
			t.Fatal("expected an error")
		}
	})
}
