package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/inspect"
	"github.com/mars-base/pgcli/internal/podman"
)

// pg inspect runs a single-snapshot health inspection — ported in spirit from
// pgbot (Apache-2.0). It never holds a connection: SQL goes through psql
// (podman exec locally, an ephemeral psql container for --dsn), each domain a
// read-only json_agg query the inspect package decodes and analyses.

func init() {
	inspectCmd.Flags().String("dsn", "", "inspect a remote database (postgres://user:pass@host:port/db) instead of a local instance")
	inspectCmd.Flags().String("html", "", "write a self-contained HTML report to this path")
	inspectCmd.Flags().Bool("open", false, "after --html, try to open the report in a browser (needs a GUI)")
	inspectCmd.Flags().Bool("full", false, "print the raw section tables, not just the summary")
	inspectCmd.Flags().Bool("json", false, "emit the whole report as JSON (no terminal summary)")
	inspectCmd.Flags().Duration("interval", time.Second, "gap between the two counter samples used for rates")
	inspectCmd.Flags().Int("top", 20, "number of pg_stat_statements rows to consider")
	rootCmd.AddCommand(inspectCmd)
}

var inspectCmd = &cobra.Command{
	Use:   "inspect",
	Short: "Diagnose PostgreSQL health and print a report (findings + tables)",
	Long: `inspect samples the standard statistics views once and produces a
deterministic health report: a 0-100 score plus findings graded
CRITICAL/WARNING/NOTE/GOOD, with optional raw tables for queries, indexes,
vacuum/tables, settings and sessions.

Everything is read-only (each statement runs inside a READ ONLY transaction);
a database the user can query with the pg_monitor role is enough.

Examples:
  pg inspect -i myinst
  pg inspect -i myinst --full
  pg inspect --dsn postgres://user:pass@host:5432/db --html /tmp/report.html
  pg inspect -i myinst --json | jq '.findings | length'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dsn, _ := cmd.Flags().GetString("dsn")
		htmlPath, _ := cmd.Flags().GetString("html")
		doOpen, _ := cmd.Flags().GetBool("open")
		full, _ := cmd.Flags().GetBool("full")
		asJSON, _ := cmd.Flags().GetBool("json")
		interval, _ := cmd.Flags().GetDuration("interval")
		top, _ := cmd.Flags().GetInt("top")

		if doOpen && htmlPath == "" {
			return fmt.Errorf("--open needs --html <path> (there is nothing to open without a report file)")
		}

		var (
			q      inspect.Querier
			target string
		)
		if dsn != "" {
			if err := checkDSNInstanceConflict(cmd); err != nil {
				return err
			}
			if err := loadConfigForDSN(); err != nil {
				return err
			}
			pm, err := newPodman()
			if err != nil {
				return err
			}
			target = redactDSN(dsn)
			q = &dsnQuerier{pm: pm, dsn: dsn}
		} else {
			if err := loadConfig(); err != nil {
				return err
			}
			if err := ensureContainerRunning(); err != nil {
				return err
			}
			pm, err := newPodman()
			if err != nil {
				return err
			}
			target = fmt.Sprintf("instance %q (%s)", cfg.Instance, cfg.Podman.ContainerName)
			q = &localQuerier{pm: pm}
		}

		ctx, err := inspect.Run(q, target, inspect.Options{Interval: interval, Limit: top})
		if err != nil {
			return err
		}

		out := cmd.OutOrStdout()
		switch {
		case asJSON:
			if err := inspect.JSON(out, ctx); err != nil {
				return fmt.Errorf("encoding report: %w", err)
			}
		case full:
			inspect.Full(out, ctx)
		default:
			inspect.Summary(out, ctx)
		}

		if htmlPath != "" {
			abs, err := filepath.Abs(htmlPath)
			if err != nil {
				abs = htmlPath
			}
			if err := os.WriteFile(abs, []byte(inspect.HTML(ctx)), 0o644); err != nil {
				return fmt.Errorf("writing HTML report: %w", err)
			}
			fmt.Fprintf(out, "\nHTML report: %s\n", abs)
			if doOpen {
				if err := openBrowser(abs); err != nil {
					fmt.Fprintf(out, "(could not open a browser: %v — open the file above manually)\n", err)
				}
			}
		}
		return nil
	},
}

// localQuerier runs diagnostics SQL via psql inside the instance container,
// capturing -tA output (one json_agg array per statement).
type localQuerier struct{ pm *podman.Manager }

func (l *localQuerier) Query(sql string) (string, error) {
	return l.pm.Exec("psql", "-U", cfg.Postgres.User, "-d", cfg.Postgres.Database,
		"-q", "-t", "-A", "-c", readOnly(sql))
}

// dsnQuerier runs diagnostics SQL against a remote DSN by launching an
// ephemeral psql container (the same path pgcli uses to introspect sources).
type dsnQuerier struct {
	pm  *podman.Manager
	dsn string
}

func (d *dsnQuerier) Query(sql string) (string, error) {
	return d.pm.ExecDSNQuery(d.dsn, readOnly(sql))
}

// readOnly runs a diagnostic SELECT inside an explicit read-only transaction so
// a typo or an unexpected write cannot succeed. The .sql files return a single
// json_agg value, which is the only row psql -tA prints from the transaction.
// The terminator is normalized: without it the appended COMMIT glues onto the
// statement ("… WHERE x = 'y' COMMIT") — a syntax error, or worse, parsed as a
// column alias.
func readOnly(sql string) string {
	sql = strings.TrimSpace(sql)
	if !strings.HasSuffix(sql, ";") {
		sql += ";"
	}
	return "BEGIN TRANSACTION READ ONLY; " + sql + " COMMIT;"
}

// ensureContainerRunning is the friendly pre-flight: psql-over-podman needs the
// instance up, so say so before the first query fails cryptically.
func ensureContainerRunning() error {
	name := cfg.Podman.ContainerName
	out, err := exec.Command("podman", "inspect", "-f", "{{.State.Running}}", name).Output()
	if err != nil {
		return fmt.Errorf("container %q not found. Run 'pg start -i %s' first", name, cfg.Instance)
	}
	if strings.TrimSpace(string(out)) != "true" {
		return fmt.Errorf("container %q is stopped. Run 'pg start -i %s' first", name, cfg.Instance)
	}
	return nil
}

func openBrowser(path string) error {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", path)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		c = exec.Command("xdg-open", path)
	}
	return c.Start()
}

// redactDSN keeps the connection summary free of passwords: host:port/db only.
func redactDSN(dsn string) string {
	host, port, _, _, database, err := podman.ParseDSN(dsn)
	if err != nil {
		return "remote database"
	}
	if database == "" {
		return fmt.Sprintf("%s:%d", host, port)
	}
	return fmt.Sprintf("%s:%d/%s", host, port, database)
}
