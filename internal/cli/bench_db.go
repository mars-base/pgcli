package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/mars-base/pgcli/internal/podman"
)

type benchSQLRunner interface {
	QueryPsql(podman.BenchContainerOptions) (string, error)
}

type benchDBProbe struct {
	CanCreate bool
	Exists    bool
}

type benchDBResult struct {
	Target       benchTarget
	CanCreate    bool
	AutoSelected bool
	Created      bool
}

var benchDBNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]{0,62}$`)

func validateBenchDBName(name string) error {
	if !benchDBNamePattern.MatchString(name) {
		return fmt.Errorf("--bench-db %q is not a valid database name: use 1-63 characters matching [A-Za-z_][A-Za-z0-9_$]", name)
	}
	switch name {
	case "template0", "template1":
		return fmt.Errorf("--bench-db %q is a template database and cannot be used", name)
	}
	return nil
}

func benchDBProbeQuery(name string) string {
	return fmt.Sprintf(
		"SELECT (SELECT rolsuper OR rolcreatedb FROM pg_roles WHERE rolname = current_user), EXISTS (SELECT 1 FROM pg_database WHERE datname = '%s')",
		strings.ReplaceAll(name, "'", "''"),
	)
}

func createBenchDBQuery(name string) string {
	return "CREATE DATABASE " + quoteBenchIdent(name)
}

func quoteBenchIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func parseBenchDBProbe(out string) (benchDBProbe, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	parts := strings.Split(line, "|")
	if len(parts) != 2 {
		return benchDBProbe{}, fmt.Errorf("unexpected permission probe output %q", line)
	}
	canCreate, err := parseBenchBool(parts[0])
	if err != nil {
		return benchDBProbe{}, fmt.Errorf("unexpected permission probe output %q: %w", line, err)
	}
	exists, err := parseBenchBool(parts[1])
	if err != nil {
		return benchDBProbe{}, fmt.Errorf("unexpected permission probe output %q: %w", line, err)
	}
	return benchDBProbe{CanCreate: canCreate, Exists: exists}, nil
}

func parseBenchBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "t", "true":
		return true, nil
	case "f", "false":
		return false, nil
	}
	return false, fmt.Errorf("expected t or f, got %q", s)
}

// resolveBenchDatabase picks a dedicated benchmark database when the account
// is allowed to create one. It never drops an existing database: --init only
// recreates pgbench's own tables inside it.
func resolveBenchDatabase(pm benchSQLRunner, target benchTarget, controlDB, image, benchDB string) (benchDBResult, error) {
	control := target.withDatabase(controlDB)
	probe, err := runBenchDBProbe(pm, control, image, benchDB)
	if err != nil {
		return benchDBResult{}, err
	}
	result := benchDBResult{Target: target, CanCreate: probe.CanCreate}
	if !probe.CanCreate {
		return result, nil
	}

	result.AutoSelected = true
	result.Target = target.withDatabase(benchDB)
	if probe.Exists {
		return result, nil
	}

	if _, err := runBenchSQL(pm, control, image, createBenchDBQuery(benchDB)); err != nil {
		// CREATE DATABASE may have committed while the one-shot container
		// still reported a cleanup failure; trust a fresh existence check.
		exists, checkErr := benchDatabaseExists(pm, control, image, benchDB)
		if checkErr == nil && exists {
			result.Created = true
			return result, nil
		}
		return benchDBResult{}, fmt.Errorf("creating benchmark database %q: %w", benchDB, err)
	}
	result.Created = true
	return result, nil
}

func runBenchDBProbe(pm benchSQLRunner, target benchTarget, image, benchDB string) (benchDBProbe, error) {
	out, err := runBenchSQL(pm, target, image, benchDBProbeQuery(benchDB))
	if err != nil {
		return benchDBProbe{}, fmt.Errorf("checking benchmark database %q: %w", benchDB, err)
	}
	probe, err := parseBenchDBProbe(out)
	if err != nil {
		return benchDBProbe{}, fmt.Errorf("checking benchmark database %q: %w", benchDB, err)
	}
	return probe, nil
}

func benchDatabaseExists(pm benchSQLRunner, target benchTarget, image, benchDB string) (bool, error) {
	probe, err := runBenchDBProbe(pm, target, image, benchDB)
	if err != nil {
		return false, err
	}
	return probe.Exists, nil
}

func runBenchSQL(pm benchSQLRunner, target benchTarget, image, sql string) (string, error) {
	return pm.QueryPsql(podman.BenchContainerOptions{
		Image:      image,
		Entrypoint: "psql",
		Args:       []string{"-X", "-q", "-t", "-A", "-v", "ON_ERROR_STOP=1", "-c", sql},
		Env:        target.env(),
		Mounts:     target.Mounts,
	})
}
