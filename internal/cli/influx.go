package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/influxdb"
	"github.com/mars-base/pgcli/internal/podman"
)

func init() {
	rootCmd.AddCommand(influxCmd)
	f := influxCmd.Flags()
	f.String("name", "influxdb", "managed influxdb instance name (ignored with --host/--dsn)")
	f.String("database", "", "select a database (-database)")
	f.String("host", "", "remote InfluxDB host (enables remote mode)")
	f.Int("port", 0, "remote InfluxDB HTTP port (default 8086)")
	f.String("dsn", "", "remote InfluxDB URL: influxdb://user:pass@host:8086/db or https://...")
	f.String("username", "", "remote username (default $INFLUX_USERNAME)")
	f.String("password", "", "remote password (default $INFLUX_PASSWORD; prefer the env var: argv is visible in ps)")
	f.Bool("ssl", false, "connect to a remote server over HTTPS (-ssl)")
	f.Bool("unsafe-ssl", false, "skip TLS verification for a remote HTTPS server (-unsafeSsl)")
}

var influxCmd = &cobra.Command{
	Use:   "influx [flags] [-- <influx args...>]",
	Short: "Run the influx CLI against a local addon or a remote InfluxDB instance",
	Long: `influx runs the InfluxDB 1.x command-line client without installing it:
pgcli launches a short-lived container from the pgcli-influxdb image (the same
1.13.1 image the addon runs) and passes every argument straight through to
influx. The container is removed after each command.

Two connection modes:

  local (default)   --name selects a managed addon instance; pgcli injects
                    -host/-port and authenticates with the instance's admin
                    credentials. Start it with "pg addon start influxdb".
  remote            --host/--port (or --dsn) targets any reachable InfluxDB
                    1.x server. Credentials come from --username/--password,
                    the DSN userinfo, or INFLUX_USERNAME/INFLUX_PASSWORD.

In both modes the password travels in the process environment and is passed
to the container by NAME only, so it never appears in the podman argv (it can
still be visible in pg's own argv if you use --password; prefer the env var).
An import file named by "-import -path FILE" is mounted into the container at
its absolute path, so local exports import into remote servers too.

influx v1 has no subcommands — operations are flags (-execute, -import,
-format, -precision, ...). Any influx flag that pg's own parser would reject
goes after -- . Arguments are forwarded verbatim and in order.

Examples:
  pg influx -- -execute "SHOW DATABASES"
  pg influx --database demo -- -execute "SELECT * FROM cpu LIMIT 5"
  pg influx --host db.example.com --username admin --ssl -- -execute "SHOW DATABASES"
  pg influx --dsn 'influxdb://admin:secret@db.example.com:8086/demo' -- -execute "SHOW MEASUREMENTS"
  pg influx --dsn 'https://admin:secret@db.example.com:8086/demo?unsafe_ssl=true' -- -execute "SHOW DATABASES"
  pg influx --host db.example.com --database demo -- -import -path /tmp/demo.txt
  pg influx --name metrics`,
	RunE: func(cmd *cobra.Command, args []string) error {
		flags := cmd.Flags()
		name, _ := flags.GetString("name")
		database, _ := flags.GetString("database")
		host, _ := flags.GetString("host")
		port, _ := flags.GetInt("port")
		dsn, _ := flags.GetString("dsn")
		username, _ := flags.GetString("username")
		password, _ := flags.GetString("password")
		ssl, _ := flags.GetBool("ssl")
		unsafeSSL, _ := flags.GetBool("unsafe-ssl")

		remoteFlags := []string{"host", "port", "dsn", "username", "password", "ssl", "unsafe-ssl"}
		remote := false
		for _, f := range remoteFlags {
			if flags.Changed(f) {
				remote = true
			}
		}

		if remote && flags.Changed("name") {
			return fmt.Errorf("--name selects a managed instance; it cannot be combined with remote connection flags (--host/--dsn)")
		}

		r, err := podman.NewInfluxRunner()
		if err != nil {
			return err
		}

		if remote {
			if dsn != "" {
				for _, f := range remoteFlags {
					if f == "dsn" {
						continue
					}
					if flags.Changed(f) {
						return fmt.Errorf("--dsn already carries the connection settings; do not combine it with --%s", f)
					}
				}
				opts, err := influxdb.ParseRemoteDSN(dsn)
				if err != nil {
					return err
				}
				if database != "" {
					opts.Database = database
				}
				// DSN userinfo wins; fall back to the env vars the CLI itself
				// honors, matching the --host mode.
				if opts.Username == "" {
					opts.Username = os.Getenv("INFLUX_USERNAME")
				}
				if opts.Password == "" {
					opts.Password = os.Getenv("INFLUX_PASSWORD")
				}
				return r.Run(config.DefaultInfluxDBImageTag, opts.Env(), append(opts.Args(), args...))
			}
			if host == "" {
				return fmt.Errorf("remote mode needs --host or --dsn")
			}
			opts := influxdb.RemoteOptions{
				Host:      host,
				Port:      port,
				Database:  database,
				SSL:       ssl,
				UnsafeSSL: unsafeSSL,
			}
			if opts.Port == 0 {
				opts.Port = influxdb.DefaultRemotePort
			}
			opts.Username, opts.Password = username, password
			if opts.Username == "" {
				opts.Username = os.Getenv("INFLUX_USERNAME")
			}
			if opts.Password == "" {
				opts.Password = os.Getenv("INFLUX_PASSWORD")
			}
			if opts.Password != "" && opts.Username == "" {
				return fmt.Errorf("a password needs a username (--username or INFLUX_USERNAME)")
			}
			if opts.UnsafeSSL && !opts.SSL {
				return fmt.Errorf("--unsafe-ssl only applies with --ssl")
			}
			return r.Run(config.DefaultInfluxDBImageTag, opts.Env(), append(opts.Args(), args...))
		}

		c, _, err := loadAddonCfg()
		if err != nil {
			return err
		}
		c.ApplyDefaults()
		ic, ok := c.Addons.InfluxDB[name]
		if !ok {
			avail := sortedAddonNames(c.Addons.InfluxDB)
			if len(avail) == 0 {
				return fmt.Errorf("no influxdb instances installed — create one with: pg addon install influxdb --name %s (or use --host/--dsn for a remote server)", name)
			}
			return fmt.Errorf("influxdb %q not found (available: %s)", name, strings.Join(avail, ", "))
		}
		if m, err := podman.NewInfluxDBManager(c); err == nil {
			if running, _ := m.ContainerRunning(ic.ContainerName); !running {
				fmt.Fprintf(os.Stderr, "[!] influxdb %q is not running — start it with: pg addon start influxdb --name %s\n", name, name)
			}
		}
		conn := influxdb.CLIArgs(ic)
		if database != "" {
			conn = append(conn, "-database", database)
		}
		return r.Run(ic.ImageTag, influxdb.CLIEnv(ic, true), append(conn, args...))
	},
}
