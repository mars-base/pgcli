package influxdb

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
)

const (
	// StartTimeout covers a cold first boot (new meta store) on a slow host.
	StartTimeout = 20 * time.Second
	// StopGrace is how long a stopped container's influxd gets to shut down.
	StopGrace = 30 * time.Second
)

// Ping checks the unauthenticated health endpoint.
func Ping(ic config.InfluxDBConfig, timeout time.Duration) error {
	client := &http.Client{
		// Loopback only; never route health checks through an HTTP proxy.
		Transport: &http.Transport{Proxy: nil},
		Timeout:   timeout,
	}
	resp, err := client.Get(PingURL(ic))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("%s returned HTTP %s", PingURL(ic), resp.Status)
	}
	return nil
}

// WaitForPing polls /ping until it answers 204 or the timeout expires.
func WaitForPing(ic config.InfluxDBConfig, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := Ping(ic, 2*time.Second); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// CLIEnv returns the environment that authenticates the influx CLI without
// putting the password on the command line (visible in ps).
func CLIEnv(ic config.InfluxDBConfig, authenticate bool) []string {
	if !authenticate {
		return nil
	}
	return []string{
		"INFLUX_USERNAME=" + ic.AdminUser,
		"INFLUX_PASSWORD=" + ic.AdminPassword,
	}
}

// CLIArgs returns the connection arguments every influx CLI invocation needs.
func CLIArgs(ic config.InfluxDBConfig) []string {
	return []string{
		"-host", ConnectHost(ic.Listen),
		"-port", strconv.Itoa(ic.HostPort),
	}
}

// BootstrapAuth makes sure the admin credentials stored in pg.yaml work.
// run executes an influx CLI command against this instance and returns its
// combined output. On a fresh data directory with auth-enabled=true, InfluxDB
// 1.x allows CREATE USER WITH ALL PRIVILEGES without credentials; that is the
// only unauthenticated statement this function relies on.
func BootstrapAuth(ic config.InfluxDBConfig, run func(env, args []string) ([]byte, error)) error {
	if err := checkAuth(ic, run); err == nil {
		return nil
	}
	stmt := fmt.Sprintf("CREATE USER %s WITH PASSWORD %s WITH ALL PRIVILEGES",
		InfluxQLIdent(ic.AdminUser), InfluxQLString(ic.AdminPassword))
	if out, err := run(nil, []string{"-execute", stmt}); err != nil {
		return fmt.Errorf("creating admin user %q failed (the data dir may already contain other users — pass the existing --admin-password or clean the data dir): %w\n%s", ic.AdminUser, err, strings.TrimSpace(string(out)))
	}
	if err := checkAuth(ic, run); err != nil {
		return fmt.Errorf("admin user %q was created but authentication still fails: %w", ic.AdminUser, err)
	}
	return nil
}

func checkAuth(ic config.InfluxDBConfig, run func(env, args []string) ([]byte, error)) error {
	out, err := run(CLIEnv(ic, true), []string{"-execute", "SHOW USERS"})
	if err != nil {
		return fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// BackupOptions maps the CLI flags onto influxd backup's selection flags.
type BackupOptions struct {
	Database   string
	Since      string
	Start, End string
}

// BackupArgs builds `<influxd> backup -portable ... <out>`. The RPC endpoint
// is always loopback: the protocol has no authentication upstream.
func BackupArgs(ic config.InfluxDBConfig, out string, opts BackupOptions) ([]string, error) {
	if out == "" {
		return nil, fmt.Errorf("backup output path must not be empty")
	}
	args := []string{
		"backup", "-portable",
		"-host", net.JoinHostPort("127.0.0.1", strconv.Itoa(ic.RPCPort)),
	}
	if opts.Database != "" {
		args = append(args, "-db", opts.Database)
	}
	switch {
	case opts.Since != "":
		if opts.Start != "" || opts.End != "" {
			return nil, fmt.Errorf("--since cannot be combined with --start/--end")
		}
		args = append(args, "-since", opts.Since)
	case opts.Start != "" || opts.End != "":
		if opts.Start == "" || opts.End == "" {
			return nil, fmt.Errorf("--start and --end must be given together")
		}
		args = append(args, "-start", opts.Start, "-end", opts.End)
	}
	return append(args, out), nil
}

// RestoreArgs builds `<influxd> restore -portable ... <backup>`. The restore
// subcommand is ONLINE: it talks to the running server over the RPC endpoint
// (unauthenticated, so this is always loopback).
func RestoreArgs(ic config.InfluxDBConfig, backupDir, database, newdb string) ([]string, error) {
	if backupDir == "" {
		return nil, fmt.Errorf("backup directory must not be empty")
	}
	if newdb != "" && database == "" {
		return nil, fmt.Errorf("--newdb requires --database")
	}
	args := []string{"restore", "-portable", "-host", net.JoinHostPort("127.0.0.1", strconv.Itoa(ic.RPCPort))}
	if database != "" {
		args = append(args, "-db", database)
	}
	if newdb != "" {
		args = append(args, "-newdb", newdb)
	}
	return append(args, backupDir), nil
}
