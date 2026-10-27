// Package influx2tsdb holds the settings and probes for the influx2tsdb-proxy
// container: an InfluxDB 1.x HTTP API (Line Protocol + InfluxQL) in front of a
// TimescaleDB database.
package influx2tsdb

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
)

// StartTimeout bounds how long a freshly created proxy container may take to
// answer /ping.
const StartTimeout = 20 * time.Second

// Args builds the proxy command line for a configured target DSN.
func Args(c config.Influx2TSDBConfig, dsn string) []string {
	args := []string{
		"-pg", dsn,
		"-port", strconv.Itoa(c.Port),
		"-pool", strconv.Itoa(c.PoolSize),
	}
	if c.Database != "" {
		args = append(args, "-db", c.Database)
	}
	if c.Verbose {
		args = append(args, "-verbose")
	}
	return args
}

// LocalDSN derives the connection string for a locally managed instance. The
// proxy runs on the host, so it dials the host-mapped port rather than the
// container's internal 5432.
func LocalDSN(inst config.InstanceConfig) (string, error) {
	if inst.Podman.HostPort == 0 {
		return "", fmt.Errorf("instance has no host port assigned")
	}
	if inst.Postgres.User == "" {
		return "", fmt.Errorf("instance has no PostgreSQL user configured")
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(inst.Postgres.User, inst.Postgres.Password),
		Host:     net.JoinHostPort("127.0.0.1", strconv.Itoa(inst.Podman.HostPort)),
		Path:     "/" + inst.Postgres.Database,
		RawQuery: "sslmode=disable",
	}
	return u.String(), nil
}

// RedactDSN hides the password for display.
func RedactDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		return dsn
	}
	if _, set := u.User.Password(); set {
		u.User = url.UserPassword(u.User.Username(), "***")
	}
	// url.String escapes the placeholder; show the conventional *** instead.
	return strings.ReplaceAll(u.String(), "%2A%2A%2A", "***")
}

// Ping checks the proxy's /ping endpoint. The InfluxDB 1.x API answers 204.
func Ping(port int, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/ping")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET /ping returned %s", resp.Status)
	}
	return nil
}

// WaitForPing polls /ping until the proxy answers or the timeout expires.
func WaitForPing(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := Ping(port, 2*time.Second)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TargetSummary renders the configured target without leaking the password.
func TargetSummary(c config.Influx2TSDBConfig) string {
	if c.TargetInstance != "" {
		return "instance " + c.TargetInstance
	}
	return RedactDSN(c.DSN)
}

// DatabaseName returns the effective InfluxDB database name. When unset, the
// proxy derives it from the PostgreSQL database in the resolved DSN.
func DatabaseName(c config.Influx2TSDBConfig, dsn string) string {
	if c.Database != "" {
		return c.Database
	}
	u, err := url.Parse(dsn)
	if err == nil {
		if db := strings.TrimPrefix(u.Path, "/"); db != "" {
			return db
		}
	}
	return ""
}
