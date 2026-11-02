package cli

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/podman"
)

var addonDocumentDBCmd = &cobra.Command{
	Use:   "documentdb",
	Short: "Run mongosh against a managed or remote DocumentDB gateway",
	Long: `Connect to a DocumentDB gateway with mongosh, or run read-only inspection
queries. pgcli pulls a small mongosh-only image on first use and runs it as a
throwaway container: the pgcli binary itself carries no MongoDB driver and
does not grow.

Local mode (the default) reads the add-on's pg.yaml entry (client user,
password, port, TLS mode). Remote mode connects to any reachable gateway:

  --dsn / --uri       one full mongodb:// connection string (recommended
                      for another host). Its userinfo, database and query
                      are kept; DOCDB_PASSWORD fills a missing password.
                      DOCDB_URI provides a default when no --dsn is given.
  --host/--port       alternative to --dsn, typed field by field; credentials
                      from --username/--password or DOCDB_USERNAME/DOCDB_PASSWORD

An explicit --name always selects local mode.

In both modes the connection string reaches the container through an
environment variable passed by name only, so the password never appears in
podman's argv.`,
}

var addonDocumentDBShellCmd = &cobra.Command{
	Use:   "shell [flags] [-- <mongosh args...>]",
	Short: "Open an interactive mongosh session",
	Long: `Open an interactive mongosh session against the gateway. The initial
database is the managed add-on's target database (local mode), the URI's
database, or --database. Extra arguments after -- are forwarded to mongosh
verbatim.

Examples:
  pg addon documentdb shell
  pg addon documentdb shell --database app
  pg addon documentdb shell --dsn 'mongodb://docdb_admin@10.0.0.5:10260/app'
  pg addon documentdb shell -- --eval 'db.version()'`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadDocumentDBMongoClient(cmd)
		if err != nil {
			return err
		}
		databaseFlag, _ := cmd.Flags().GetString("database")
		database, err := c.resolveDatabase(databaseFlag, false)
		if err != nil {
			return err
		}
		tlsMode := "off"
		if c.tls {
			tlsMode = "on"
		}
		fmt.Fprintf(os.Stderr, "-> mongosh %s (tls %s)\n", c.display(database), tlsMode)
		return c.run(database, true, args)
	},
}

var addonDocumentDBDatabasesCmd = &cobra.Command{
	Use:   "databases",
	Short: "Inspect databases served by the gateway",
}

var addonDocumentDBDatabasesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List databases",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadDocumentDBMongoClient(cmd)
		if err != nil {
			return err
		}
		return c.evalJS("", documentDBListDatabasesJS(), "no databases with collections (DocumentDB lists a database only after it contains at least one collection)")
	},
}

var addonDocumentDBCollectionsCmd = &cobra.Command{
	Use:   "collections",
	Short: "Inspect collections in a database",
}

var addonDocumentDBCollectionsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List collections in a database",
	Long: `List collections in a database. The managed add-on's target database,
the URI's database, or --database selects the database.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadDocumentDBMongoClient(cmd)
		if err != nil {
			return err
		}
		databaseFlag, _ := cmd.Flags().GetString("database")
		database, err := c.resolveDatabase(databaseFlag, true)
		if err != nil {
			return err
		}
		return c.evalJS(database, documentDBListCollectionsJS(database), fmt.Sprintf("no collections in database %q", database))
	},
}

var addonDocumentDBIndexesCmd = &cobra.Command{
	Use:   "indexes",
	Short: "Inspect indexes on a collection",
}

var addonDocumentDBIndexesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List indexes on a collection",
	Long: `List indexes on a collection as one JSON object per line. The managed
add-on's target database, the URI's database, or --database selects the
database.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadDocumentDBMongoClient(cmd)
		if err != nil {
			return err
		}
		databaseFlag, _ := cmd.Flags().GetString("database")
		database, err := c.resolveDatabase(databaseFlag, true)
		if err != nil {
			return err
		}
		collection, _ := cmd.Flags().GetString("collection")
		if collection == "" {
			return fmt.Errorf("--collection is required")
		}
		return c.evalJS(database, documentDBListIndexesJS(database, collection), "")
	},
}

type documentDBRemote struct {
	host     string
	port     int
	username string
	password string
	database string
	uri      *url.URL
}

type documentDBMongoClient struct {
	dc     config.DocumentDBConfig
	remote *documentDBRemote
	runner *podman.MongoshRunner
	image  string
	tls    bool
}

func loadDocumentDBMongoClient(cmd *cobra.Command) (*documentDBMongoClient, error) {
	flags := cmd.Flags()
	name, _ := flags.GetString("name")
	if name == "" {
		name = documentDBDefaultName
	}
	image, _ := flags.GetString("image")
	if image == "" {
		image = config.DefaultMongoshImageTag
	}
	tlsFlag, _ := flags.GetBool("tls")

	host, _ := flags.GetString("host")
	port, _ := flags.GetInt("port")
	uri, _ := flags.GetString("uri")
	dsn, _ := flags.GetString("dsn")
	if dsn != "" {
		if uri != "" && uri != dsn {
			return nil, fmt.Errorf("--uri and --dsn are aliases; set only one")
		}
		uri = dsn
	}
	if uri == "" && !flags.Changed("name") {
		explicitRemote := false
		for _, f := range []string{"host", "port", "username", "password"} {
			if flags.Changed(f) {
				explicitRemote = true
			}
		}
		if !explicitRemote {
			uri = strings.TrimSpace(os.Getenv("DOCDB_URI"))
		}
	}
	remoteUser, _ := flags.GetString("username")
	remotePass, _ := flags.GetString("password")

	remote := uri != ""
	for _, f := range []string{"host", "port", "uri", "dsn", "username", "password"} {
		if flags.Changed(f) {
			remote = true
		}
	}
	if remote && flags.Changed("name") {
		return nil, fmt.Errorf("--name selects a managed add-on; it cannot be combined with remote connection flags (--host/--uri/--dsn)")
	}

	if remote {
		if uri != "" {
			if flags.Changed("host") || flags.Changed("port") || flags.Changed("username") || flags.Changed("password") {
				return nil, fmt.Errorf("--uri/--dsn already carries the connection settings; do not combine it with --host/--port/--username/--password")
			}
			rc, err := parseDocumentDBRemoteURI(uri, os.Getenv("DOCDB_USERNAME"), os.Getenv("DOCDB_PASSWORD"))
			if err != nil {
				return nil, err
			}
			runner, err := podman.NewMongoshRunner()
			if err != nil {
				return nil, err
			}
			return &documentDBMongoClient{remote: rc, runner: runner, image: image, tls: tlsFlag}, nil
		}

		host = strings.TrimSpace(host)
		if host == "" {
			return nil, fmt.Errorf("remote mode needs --host, --uri or --dsn")
		}
		if strings.Contains(host, "://") {
			return nil, fmt.Errorf("--host takes a host name or IP; use --uri/--dsn for a full MongoDB connection string")
		}
		if _, _, err := net.SplitHostPort(host); err == nil {
			return nil, fmt.Errorf("--host must not include a port; use --port")
		}
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("--port must be between 1 and 65535")
		}
		if remoteUser == "" {
			remoteUser = os.Getenv("DOCDB_USERNAME")
		}
		if remotePass == "" {
			remotePass = os.Getenv("DOCDB_PASSWORD")
		}
		if remoteUser == "" {
			return nil, fmt.Errorf("remote mode needs --username or DOCDB_USERNAME")
		}
		runner, err := podman.NewMongoshRunner()
		if err != nil {
			return nil, err
		}
		return &documentDBMongoClient{
			remote: &documentDBRemote{host: host, port: port, username: remoteUser, password: remotePass},
			runner: runner,
			image:  image,
			tls:    tlsFlag,
		}, nil
	}

	cfg, _, err := loadAddonCfg()
	if err != nil {
		return nil, err
	}
	cfg.ApplyDefaults()
	dc, ok := cfg.Addons.DocumentDB[name]
	if !ok {
		avail := sortedAddonNames(cfg.Addons.DocumentDB)
		if len(avail) == 0 {
			return nil, fmt.Errorf("no documentdb add-ons installed — create one with: pg addon install documentdb --pg-name <instance> (or use --host/--uri/--dsn for a remote gateway)")
		}
		return nil, fmt.Errorf("documentdb %q not found (available: %s)", name, strings.Join(avail, ", "))
	}
	if m, err := podman.NewDocumentDBManager(cfg); err == nil {
		if running, _ := m.ContainerRunning(dc.ContainerName); !running {
			fmt.Fprintf(os.Stderr, "[!] documentdb gateway %q is not running — start it with: pg addon start documentdb --name %s\n", name, name)
		}
	}
	runner, err := podman.NewMongoshRunner()
	if err != nil {
		return nil, err
	}
	return &documentDBMongoClient{
		dc:     dc,
		runner: runner,
		image:  image,
		tls:    tlsFlag || dc.TLSMode == "require",
	}, nil
}

// parseDocumentDBRemoteURI validates a mongodb:// URI and fills the defaults
// pgcli's own connection strings always carry. Explicit values in the URI win;
// envUser/envPass are used only when the userinfo is absent or has no password.
func parseDocumentDBRemoteURI(raw, envUser, envPass string) (*documentDBRemote, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid MongoDB URI: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "mongodb") {
		return nil, fmt.Errorf("unsupported MongoDB URI scheme %q (use mongodb://)", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("MongoDB URI has no host")
	}
	if strings.Contains(u.Host, ",") {
		return nil, fmt.Errorf("MongoDB URI must contain a single host")
	}

	port := 27017
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("MongoDB URI has an invalid port %q", p)
		}
		port = n
	}
	database := strings.TrimPrefix(u.Path, "/")
	if strings.Contains(database, "/") {
		return nil, fmt.Errorf("MongoDB URI path must be a single database name")
	}

	if u.User == nil {
		if envUser != "" {
			u.User = url.UserPassword(envUser, envPass)
		}
	} else if _, ok := u.User.Password(); !ok && envPass != "" {
		u.User = url.UserPassword(u.User.Username(), envPass)
	}

	q := u.Query()
	if q.Get("authSource") == "" {
		q.Set("authSource", "admin")
	}
	if q.Get("serverSelectionTimeoutMS") == "" {
		q.Set("serverSelectionTimeoutMS", "10000")
	}
	if q.Get("directConnection") == "" {
		q.Set("directConnection", "true")
	}
	u.RawQuery = q.Encode()

	username := ""
	if u.User != nil {
		username = u.User.Username()
	}
	return &documentDBRemote{
		host:     u.Hostname(),
		port:     port,
		username: username,
		database: database,
		uri:      u,
	}, nil
}

// uriString returns the connection string for one invocation. For --uri mode
// the parsed URI is copied, so per-command database and --tls overrides do not
// mutate the base URL.
func (r *documentDBRemote) uriString(database string, tls bool) string {
	if r.uri == nil {
		dc := config.DocumentDBConfig{
			ClientUser:     r.username,
			ClientPassword: r.password,
			HostPort:       r.port,
		}
		return podman.DocumentDBMongoshURI(&dc, r.host, tls, database)
	}
	u := *r.uri
	if database != "" {
		u.Path = "/" + database
		u.RawPath = ""
	}
	if tls {
		q := u.Query()
		q.Set("tls", "true")
		q.Set("tlsAllowInvalidCertificates", "true")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func (c *documentDBMongoClient) host() string {
	if c.remote != nil {
		return c.remote.host
	}
	return c.runner.Host(c.dc.Listen)
}

func (c *documentDBMongoClient) port() int {
	if c.remote != nil {
		return c.remote.port
	}
	return c.dc.HostPort
}

func (c *documentDBMongoClient) username() string {
	if c.remote != nil {
		return c.remote.username
	}
	return c.dc.ClientUser
}

func (c *documentDBMongoClient) defaultDatabase() string {
	if c.remote != nil {
		return c.remote.database
	}
	return c.dc.Database
}

func (c *documentDBMongoClient) resolveDatabase(value string, required bool) (string, error) {
	if value != "" {
		return value, nil
	}
	if database := c.defaultDatabase(); database != "" {
		return database, nil
	}
	if required {
		return "", fmt.Errorf("--database is required in remote mode when the URI has no database")
	}
	return "", nil
}

func (c *documentDBMongoClient) display(database string) string {
	address := net.JoinHostPort(c.host(), strconv.Itoa(c.port()))
	if user := c.username(); user != "" {
		address = user + "@" + address
	}
	if database != "" {
		address += "/" + database
	}
	return address
}

func (c *documentDBMongoClient) connectionURI(database string) string {
	if c.remote != nil {
		return c.remote.uriString(database, c.tls)
	}
	return podman.DocumentDBMongoshURI(&c.dc, c.runner.Host(c.dc.Listen), c.tls, database)
}

func (c *documentDBMongoClient) run(database string, interactive bool, args []string) error {
	envs := []string{"PGCLI_DOCDB_URI=" + c.connectionURI(database)}
	if interactive {
		if term := os.Getenv("TERM"); term != "" {
			envs = append(envs, "TERM="+term)
		}
	}
	return c.runner.Run(c.image, envs, interactive, args)
}

// evalJS prints the query result and, when it is empty, an explanatory note on
// stderr. stdout stays parseable: one name or JSON document per line.
func (c *documentDBMongoClient) evalJS(database, js, emptyHint string) error {
	envs := []string{"PGCLI_DOCDB_URI=" + c.connectionURI(database)}
	out, err := c.runner.Capture(c.image, envs, []string{"--quiet", "--eval", js})
	if err != nil {
		return err
	}
	fmt.Print(out)
	if strings.TrimSpace(out) == "" && emptyHint != "" {
		fmt.Fprintln(os.Stderr, emptyHint)
	}
	return nil
}

func documentDBListDatabasesJS() string {
	return `db.adminCommand({listDatabases:1}).databases.map(d => d.name).sort().forEach(n => print(n))`
}

func documentDBListCollectionsJS(database string) string {
	return fmt.Sprintf(`db.getSiblingDB(%s).getCollectionNames().sort().forEach(n => print(n))`, documentDBJSString(database))
}

func documentDBListIndexesJS(database, collection string) string {
	return fmt.Sprintf(`db.getSiblingDB(%s).getCollection(%s).getIndexes().forEach(i => print(JSON.stringify(i)))`,
		documentDBJSString(database), documentDBJSString(collection))
}

// documentDBJSString encodes a JavaScript string literal; JSON string syntax
// is valid JavaScript for the names a Mongo database/collection can carry.
func documentDBJSString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
