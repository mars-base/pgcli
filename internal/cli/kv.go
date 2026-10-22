package cli

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/kv"
)

// pg kv deploys BYTEA and JSONB KV-cache functions to a PostgreSQL instance.
// It is a lightweight Redis alternative for pure KV caching (low-frequency
// reads/writes, no counter/ranking/ZSET support).

func init() {
	kvCmd.Flags().String("type", "all", "cache type: all, bytea, json")
	kvCmd.Flags().String("database", "", "target database name (required)")
	kvCmd.Flags().String("dsn", "", "target a remote database (postgres://user:pass@host:port/db)")
	kvCmd.Flags().Bool("dry-run", false, "print SQL without executing")
	_ = kvCmd.MarkFlagRequired("database")
	rootCmd.AddCommand(kvCmd)
}

var kvCmd = &cobra.Command{
	Use:   "kv",
	Short: "Deploy BYTEA and JSONB KV-cache functions to a PostgreSQL instance",
	Long: `kv deploys a set of KV-cache functions to a PostgreSQL instance. Two
variants are available: BYTEA (generic binary values) and JSONB (structured JSON
with GIN indexing).

Based on UNLOGGED TABLE (faster writes, data lost on crash — acceptable for
cache). Automatic expiration via TTL + batch cleanup. Safe concurrency via
UPSERT atomicity.

This is a lightweight Redis alternative for pure KV caching (low-frequency
reads/writes, < 500 QPS). It does not support counter/ranking/ZSET operations.

Examples:
  pg kv -i myinst --database mydb
  pg kv -i myinst --database mydb --type json
  pg kv --dsn postgres://user:pass@host:5432/otherdb --database mydb
  pg kv -i myinst --database mydb --dry-run`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cacheType, _ := cmd.Flags().GetString("type")
		database, _ := cmd.Flags().GetString("database")
		dsn, _ := cmd.Flags().GetString("dsn")
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		switch cacheType {
		case "all", "bytea", "json":
		default:
			return fmt.Errorf("--type must be all, bytea, or json (got %q)", cacheType)
		}

		var sqlFiles []string
		switch cacheType {
		case "all":
			sqlFiles = []string{"sql/pg-kv-cache.sql", "sql/pg-kv-cache-json.sql"}
		case "bytea":
			sqlFiles = []string{"sql/pg-kv-cache.sql"}
		case "json":
			sqlFiles = []string{"sql/pg-kv-cache-json.sql"}
		}

		// For DSN mode, rewrite the database portion of the URL
		targetDSN := dsn
		if dsn != "" {
			rewritten, err := rewriteDSNDatabase(dsn, database)
			if err != nil {
				return err
			}
			targetDSN = rewritten
		}

		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "=== pg kv ===\n")

		var target string
		if dsn != "" {
			if err := checkDSNInstanceConflict(cmd); err != nil {
				return err
			}
			if err := loadConfigForDSN(); err != nil {
				return err
			}
			target = redactDSN(targetDSN)
		} else {
			if err := loadConfig(); err != nil {
				return err
			}
			if err := ensureContainerRunning(); err != nil {
				return err
			}
			target = fmt.Sprintf("instance %q", cfg.Instance)
		}

		fmt.Fprintf(out, "-> Deploying KV cache to %s (database: %s) ...\n", target, database)

		if dryRun {
			for _, sqlFile := range sqlFiles {
				data, err := kv.SQLs.ReadFile(sqlFile)
				if err != nil {
					return fmt.Errorf("reading %s: %w", sqlFile, err)
				}
				fmt.Fprintf(out, "\n--- %s ---\n%s\n", sqlFile, string(data))
			}
			fmt.Fprintf(out, "\n(dry-run mode — no changes applied)\n")
			return nil
		}

		for _, sqlFile := range sqlFiles {
			data, err := kv.SQLs.ReadFile(sqlFile)
			if err != nil {
				return fmt.Errorf("reading %s: %w", sqlFile, err)
			}

			if err := applySQL(sqlFile, string(data), database, targetDSN); err != nil {
				return err
			}

			funcCount := strings.Count(string(data), "CREATE OR REPLACE FUNCTION")
			fmt.Fprintf(out, "  [OK] %s  applied (%d functions)\n", filepath.Base(sqlFile), funcCount)
		}

		fmt.Fprintf(out, "-> Verifying ...\n")
		if err := verifyFunctions(database, targetDSN, cacheType, out); err != nil {
			return err
		}
		fmt.Fprintf(out, "✓ KV cache ready\n")
		instFlag := ""
		if dsn == "" && cfgInstance != "default" {
			instFlag = fmt.Sprintf("-i %s ", cfgInstance)
		}
		if cacheType == "all" || cacheType == "bytea" {
			fmt.Fprintf(out, "  Try: pg exec %s-c \"SELECT kv_set('hello', 'world', 60)\"\n", instFlag)
			fmt.Fprintf(out, "  Try: pg exec %s-c \"SELECT kv_get_text('hello')\"\n", instFlag)
		}
		if cacheType == "all" || cacheType == "json" {
			fmt.Fprintf(out, "  Try: pg exec %s-c \"SELECT json_set('j', '{\\\"a\\\":1}')\"\n", instFlag)
			fmt.Fprintf(out, "  Try: pg exec %s-c \"SELECT json_get_field('j', 'a')\"\n", instFlag)
		}

		return nil
	},
}

// rewriteDSNDatabase replaces the database portion of a DSN URL with the given database name.
func rewriteDSNDatabase(dsn, database string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("invalid --dsn: %w", err)
	}
	u.Path = "/" + database
	return u.String(), nil
}

// applySQL deploys a SQL file to the target database.
func applySQL(sqlFile, sql, database, dsn string) error {
	if dsn != "" {
		return applySQLDSN(sqlFile, sql, dsn)
	}
	return applySQLLocal(sqlFile, sql, database)
}

// applySQLDSN deploys SQL via an ephemeral psql container (DSN mode).
func applySQLDSN(sqlFile, sql, dsn string) error {
	pm, err := newPodman()
	if err != nil {
		return err
	}

	// Write SQL to temp file on host, bind-mount into ephemeral psql container
	tmpFile, err := os.CreateTemp("", "pg-kv-*.sql")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.WriteString(sql); err != nil {
		tmpFile.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	tmpFile.Close()

	// Run ephemeral psql container with bind-mounted SQL file
	containerName := fmt.Sprintf("pgcli-kv-%d", os.Getpid())
	cmd := exec.Command("podman", "run", "--rm", "--name", containerName,
		"--network", "host",
		"-v", tmpPath+":/tmp/kv.sql:ro",
		cfg.Podman.ImageTag,
		"psql", "--dbname="+dsn, "-v", "ON_ERROR_STOP=1", "-f", "/tmp/kv.sql")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("applying %s: %w", filepath.Base(sqlFile), err)
	}

	_ = pm // suppress unused warning; pm is available for future use
	return nil
}

// applySQLLocal deploys SQL via podman exec into the running instance container.
func applySQLLocal(sqlFile, sql, database string) error {
	pm, err := newPodman()
	if err != nil {
		return err
	}

	// Write SQL to temp file on host
	tmpFile, err := os.CreateTemp("", "pg-kv-*.sql")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.WriteString(sql); err != nil {
		tmpFile.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	tmpFile.Close()

	containerName := cfg.Podman.ContainerName
	containerPath := "/tmp/" + filepath.Base(sqlFile)

	// podman cp <host-path> <container>:<container-path>
	if _, err := exec.Command("podman", "cp", tmpPath, containerName+":"+containerPath).Output(); err != nil {
		return fmt.Errorf("copying SQL to container: %w", err)
	}

	// Run psql -f with ON_ERROR_STOP
	if _, err := pm.Exec("psql", "-v", "ON_ERROR_STOP=1", "-U", cfg.Postgres.User, "-d", database, "-f", containerPath); err != nil {
		return fmt.Errorf("applying %s: %w", filepath.Base(sqlFile), err)
	}

	// Clean up temp file in container (best effort)
	_, _ = pm.Exec("rm", "-f", containerPath)

	return nil
}

// verifyFunctions checks that the expected number of functions are present.
func verifyFunctions(database, dsn, cacheType string, out io.Writer) error {
	var expected int
	var funcNames []string
	switch cacheType {
	case "bytea":
		expected = 6
		funcNames = []string{"kv_set", "kv_set_with_result", "kv_get", "kv_get_text", "kv_del", "kv_cleanup_batch"}
	case "json":
		expected = 9
		funcNames = []string{"json_set", "json_set_with_result", "json_get", "json_get_field", "json_get_field_json", "json_contains", "json_update_field", "json_del", "json_cleanup_batch"}
	case "all":
		expected = 15
		funcNames = []string{
			"kv_set", "kv_set_with_result", "kv_get", "kv_get_text", "kv_del", "kv_cleanup_batch",
			"json_set", "json_set_with_result", "json_get", "json_get_field", "json_get_field_json", "json_contains", "json_update_field", "json_del", "json_cleanup_batch",
		}
	}

	funcList := "'" + strings.Join(funcNames, "','") + "'"
	query := fmt.Sprintf("SELECT COUNT(*) FROM pg_proc WHERE proname IN (%s);", funcList)

	pm, err := newPodman()
	if err != nil {
		return err
	}

	var countStr string
	if dsn != "" {
		countStr, err = pm.ExecDSNQuery(dsn, query)
	} else {
		countStr, err = pm.Exec("psql", "-U", cfg.Postgres.User, "-d", database, "-t", "-A", "-c", query)
	}
	if err != nil {
		return fmt.Errorf("verifying functions: %w", err)
	}

	countStr = strings.TrimSpace(countStr)
	if countStr != fmt.Sprintf("%d", expected) {
		return fmt.Errorf("expected %d functions, found %s", expected, countStr)
	}

	fmt.Fprintf(out, "  [OK] %d/%d cache functions present\n", expected, expected)
	return nil
}
