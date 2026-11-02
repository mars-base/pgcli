package cli

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/podman"
)

const documentDBDefaultName = "documentdb"

// documentDBExtensions are the extensions the gateway requires in the target
// database. documentdb's CASCADE install also creates documentdb_core.
var documentDBExtensions = []string{"documentdb", "documentdb_core", "documentdb_extended_rum", "pg_cron"}

// loadDocumentDBTargetConfig loads a SECOND copy of pg.yaml and selects the
// target instance, so its merged Podman/Postgres fields address the target
// container without disturbing the add-on config the caller is editing.
func loadDocumentDBTargetConfig(path, instance string) (*config.Config, error) {
	c, err := config.Load(path)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	if err := c.SetInstance(instance); err != nil {
		return nil, err
	}
	return c, nil
}

// verifyDocumentDBTarget checks the target instance is running, exposes its
// Unix socket to add-ons, and has the DocumentDB extensions installed.
func verifyDocumentDBTarget(cfg *config.Config, database string) (*podman.Manager, error) {
	pm, err := podman.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("podman: %w", err)
	}
	running, err := pm.ContainerRunning(cfg.Podman.ContainerName)
	if err != nil {
		return nil, err
	}
	installHint := fmt.Sprintf("pg extension install documentdb -i %s --auto-restart", cfg.Instance)
	if !running {
		return nil, fmt.Errorf("target instance %q is not running — start it first with `pg start -i %s`", cfg.Instance, cfg.Instance)
	}
	if !pm.HasMount("/var/run/postgresql") {
		return nil, fmt.Errorf("target instance %q does not export /var/run/postgresql (the gateway connects over its Unix socket)\n  rebuild it first with: %s", cfg.Instance, installHint)
	}

	out, err := pm.Exec("psql", "-U", cfg.Postgres.User, "-d", database, "-t", "-A", "-c",
		"SELECT extname FROM pg_extension WHERE extname IN ('documentdb','documentdb_core','documentdb_extended_rum','pg_cron')")
	if err != nil {
		return nil, fmt.Errorf("querying extensions in instance %q (database %q): %w\n  install them first with: %s", cfg.Instance, database, err, installHint)
	}
	present := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			present[name] = true
		}
	}
	var missing []string
	for _, ext := range documentDBExtensions {
		if !present[ext] {
			missing = append(missing, ext)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("target instance %q (database %q) is missing DocumentDB extension(s): %s\n  install them first with:\n    %s", cfg.Instance, database, strings.Join(missing, ", "), installHint)
	}
	return pm, nil
}

// ensureDocumentDBBackendRole guarantees the passwordless role the gateway
// connects as exists with LOGIN SUPERUSER.
func ensureDocumentDBBackendRole(pm *podman.Manager, cfg *config.Config, database string) error {
	role := podman.DocumentDBBackendUser
	sql := "DO $$ BEGIN " +
		"IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '" + role + "') THEN " +
		"ALTER ROLE " + role + " WITH LOGIN SUPERUSER; " +
		"ELSE CREATE ROLE " + role + " WITH LOGIN SUPERUSER; END IF; END $$;"
	if _, err := pm.Exec("psql", "-v", "ON_ERROR_STOP=1", "-U", cfg.Postgres.User, "-d", database, "-c", sql); err != nil {
		return fmt.Errorf("preparing backend role %s: %w", role, err)
	}
	return nil
}

// documentDBUserSpec renders the createUser/updateUser BSON document. Native
// users are PostgreSQL login roles; the accepted built-in role combination is
// readWriteAnyDatabase + clusterAdmin (mapped to DocumentDB's admin role).
func documentDBUserSpec(user, password string, existing bool) (action string, raw []byte, err error) {
	if existing {
		raw, err = json.Marshal(map[string]any{
			"updateUser": user,
			"pwd":        password,
			"$db":        "admin",
		})
		return "update_user", raw, err
	}
	raw, err = json.Marshal(map[string]any{
		"createUser": user,
		"pwd":        password,
		"roles": []map[string]string{
			{"role": "readWriteAnyDatabase", "db": "admin"},
			{"role": "clusterAdmin", "db": "admin"},
		},
		"$db": "admin",
	})
	return "create_user", raw, err
}

// ensureDocumentDBClientUser creates or rotates the gateway's client user via
// the official DocumentDB API. The spec travels on psql's stdin (never argv),
// and the session disables statement logging before the password is sent.
func ensureDocumentDBClientUser(pm *podman.Manager, cfg *config.Config, database, user, password string) (created bool, err error) {
	escaped := strings.ReplaceAll(user, "'", "''")
	out, err := pm.Exec("psql", "-U", cfg.Postgres.User, "-d", database, "-t", "-A", "-c",
		"SELECT 1 FROM pg_roles WHERE rolname = '"+escaped+"'")
	if err != nil {
		return false, fmt.Errorf("checking DocumentDB user %q: %w", user, err)
	}
	existing := strings.TrimSpace(out) == "1"

	action, spec, err := documentDBUserSpec(user, password, existing)
	if err != nil {
		return false, fmt.Errorf("encoding DocumentDB user spec: %w", err)
	}
	script := "SET log_statement = 'none';\n" +
		"SET log_min_duration_statement = -1;\n" +
		"SET log_min_error_statement = 'panic';\n" +
		fmt.Sprintf("SELECT documentdb_api.%s('%s'::documentdb_core.bson);\n",
			action, strings.ReplaceAll(string(spec), "'", "''"))
	if _, err := pm.ExecStdin(script, "psql", "-v", "ON_ERROR_STOP=1", "-U", cfg.Postgres.User, "-d", database); err != nil {
		return false, fmt.Errorf("running documentdb_api.%s for %q: %w", action, user, err)
	}
	return !existing, nil
}

func generateDocumentDBPassword() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating DocumentDB client password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// runAddonInstallDocumentDB installs the DocumentDB gateway: the official Rust
// service that speaks the MongoDB wire protocol in front of the DocumentDB
// PostgreSQL extension. The gateway connects to a LOCAL pgcli instance over a
// shared Unix socket — the official .deb refuses password authentication, so
// --dsn/--admin-dsn are rejected.
func runAddonInstallDocumentDB(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = documentDBDefaultName
	}
	imageTag, _ := cmd.Flags().GetString("image")
	port, _ := cmd.Flags().GetInt("port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	force, _ := cmd.Flags().GetBool("force")
	pgName, _ := cmd.Flags().GetString("pg-name")
	database, _ := cmd.Flags().GetString("database")
	username, _ := cmd.Flags().GetString("username")
	password, _ := cmd.Flags().GetString("password")
	tlsMode, _ := cmd.Flags().GetString("tls-mode")

	if cmd.Flags().Changed("dsn") || cmd.Flags().Changed("admin-dsn") {
		return fmt.Errorf("documentdb is local-instance only: the gateway connects over a shared Unix socket and the official .deb refuses password auth — use --pg-name <instance>, not --dsn/--admin-dsn")
	}

	cfg, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.DocumentDB == nil {
		cfg.Addons.DocumentDB = make(map[string]config.DocumentDBConfig)
	}
	dc, ok := cfg.Addons.DocumentDB[name]
	if !ok {
		dc = config.DocumentDBConfig{
			ContainerName: "pgcli-documentdb" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}

	if pgName != "" {
		dc.TargetInstance = pgName
	}
	if dc.TargetInstance == "" {
		return fmt.Errorf("a target is required: pass --pg-name <local-instance> (the gateway shares the instance's Unix socket; remote connections are not supported)")
	}
	targetInst, ok := cfg.Instances[dc.TargetInstance]
	if !ok {
		return fmt.Errorf("target instance %q not found in config", dc.TargetInstance)
	}
	if cmd.Flags().Changed("database") {
		dc.Database = database
	}
	if dc.Database == "" {
		dc.Database = targetInst.Postgres.Database
	}
	if cmd.Flags().Changed("port") {
		dc.HostPort = port
	}
	if listenAddr != "" {
		dc.Listen = listenAddr
	}
	if imageTag != "" {
		dc.ImageTag = imageTag
	}
	if username != "" {
		dc.ClientUser = username
	}
	if tlsMode != "" {
		dc.TLSMode = tlsMode
	}

	passwordPrinted := ""
	switch {
	case password != "":
		dc.ClientPassword = password
		passwordPrinted = password
	case dc.ClientPassword == "":
		generated, err := generateDocumentDBPassword()
		if err != nil {
			return err
		}
		dc.ClientPassword = generated
		passwordPrinted = generated
	}

	cfg.Addons.DocumentDB[name] = dc
	cfg.ApplyDefaults()
	dc = cfg.Addons.DocumentDB[name]
	if err := cfg.Validate(); err != nil {
		return err
	}

	dm, err := podman.NewDocumentDBManager(cfg)
	if err != nil {
		return fmt.Errorf("documentdb manager: %w", err)
	}
	running, err := dm.ContainerRunning(dc.ContainerName)
	if err != nil {
		return err
	}
	if running && !force {
		fmt.Println("  [OK] DocumentDB gateway running, config unchanged")
		cfg.Addons.DocumentDB[name] = dc
		if err := cfg.Save(path); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		printDocumentDBSummary(&dc, name, false, false, "")
		return nil
	}

	targetCfg, err := loadDocumentDBTargetConfig(path, dc.TargetInstance)
	if err != nil {
		return err
	}
	tm, err := verifyDocumentDBTarget(targetCfg, dc.Database)
	if err != nil {
		return err
	}

	fmt.Printf("-> Preparing target instance %q (database %q)...\n", dc.TargetInstance, dc.Database)
	if err := tm.EnsureDocumentDBLocalTrust(podman.DocumentDBBackendUser); err != nil {
		return err
	}
	if err := ensureDocumentDBBackendRole(tm, targetCfg, dc.Database); err != nil {
		return err
	}
	created, err := ensureDocumentDBClientUser(tm, targetCfg, dc.Database, dc.ClientUser, dc.ClientPassword)
	if err != nil {
		return err
	}
	if created {
		fmt.Printf("  [OK] DocumentDB client user %q created\n", dc.ClientUser)
	} else {
		fmt.Printf("  [OK] DocumentDB client user %q password updated\n", dc.ClientUser)
	}

	fmt.Printf("-> Preparing DocumentDB gateway image %s...\n", dc.ImageTag)
	if err := dm.EnsureImage(dc.ImageTag); err != nil {
		return err
	}
	urlFile, configFile, err := dm.WriteFiles(&dc, targetCfg.Podman.HostPort)
	if err != nil {
		return err
	}
	fmt.Println("-> Starting DocumentDB gateway container...")
	if err := dm.EnsureContainer(&dc, urlFile, configFile); err != nil {
		return err
	}

	cfg.Addons.DocumentDB[name] = dc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	printDocumentDBSummary(&dc, name, true, force, passwordPrinted)
	return nil
}

// documentDBEndpoint renders a MongoDB connection string. With password it is
// the one-time credential-bearing form; without, a placeholder for display.
func documentDBEndpoint(dc *config.DocumentDBConfig, password string) string {
	host := dc.Listen
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::1":
		host = "[::1]"
	}
	userinfo := url.User(dc.ClientUser).String()
	if password != "" {
		userinfo = url.UserPassword(dc.ClientUser, password).String()
	}
	q := url.Values{}
	q.Set("directConnection", "true")
	if dc.TLSMode == "require" {
		q.Set("tls", "true")
		q.Set("tlsAllowInvalidCertificates", "true")
	}
	path := "/"
	if dc.Database != "" {
		path = "/" + url.PathEscape(dc.Database)
	}
	return "mongodb://" + userinfo + "@" + host + ":" + strconv.Itoa(dc.HostPort) + path + "?" + q.Encode()
}

func printDocumentDBSummary(dc *config.DocumentDBConfig, name string, installed, force bool, password string) {
	fmt.Println()
	switch {
	case installed && force:
		fmt.Printf("✓ DocumentDB gateway %q recreated (--force)\n", name)
	case installed:
		fmt.Printf("✓ DocumentDB gateway installed: %q\n", name)
	default:
		fmt.Printf("✓ DocumentDB gateway %q (unchanged)\n", name)
	}
	fmt.Printf("  Container:  %s\n", dc.ContainerName)
	fmt.Printf("  Image:      %s\n", dc.ImageTag)
	fmt.Printf("  Endpoint:   %s\n", documentDBEndpoint(dc, ""))
	fmt.Printf("  Target:     instance %s (database %s)\n", dc.TargetInstance, dc.Database)
	fmt.Printf("  Client:     %s (DocumentDB native user)\n", dc.ClientUser)
	fmt.Printf("  TLS mode:   %s\n", dc.TLSMode)
	if password != "" {
		fmt.Println()
		fmt.Printf("  Client password: %s\n", password)
		fmt.Printf("  (printed once — retrieve it later with `pg addon password documentdb --name %s`)\n", name)
		fmt.Println()
		fmt.Println("  Python client example (see site/content/docs/addon/documentdb.md):")
		fmt.Printf("    mongodb://%s:<password>@%s:%d/%s?directConnection=true\n", dc.ClientUser, endpointHost(dc), dc.HostPort, url.PathEscape(dc.Database))
	}
	fmt.Println()
	fmt.Printf("  retrieve the password: pg addon password documentdb --name %s\n", name)
	if dc.TLSMode == "allow" {
		fmt.Println("  TLS: plaintext and the auto-generated self-signed TLS cert are both accepted;")
		fmt.Println("       pass --tls-mode require --force to accept TLS only.")
	}
	fmt.Println("  remove keeps DocumentDB data, the client user, and the docdb_gateway role.")
}

func endpointHost(dc *config.DocumentDBConfig) string {
	switch dc.Listen {
	case "", "0.0.0.0":
		return "127.0.0.1"
	case "::1":
		return "[::1]"
	default:
		return dc.Listen
	}
}

func runAddonStartDocumentDB(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = documentDBDefaultName
	}
	cfg, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.DocumentDB == nil {
		return fmt.Errorf("no documentdb add-ons configured (run 'pg addon install documentdb --pg-name <instance>')")
	}
	dc, ok := cfg.Addons.DocumentDB[name]
	if !ok {
		return fmt.Errorf("documentdb %q not found (run 'pg addon install documentdb --name %s --pg-name <instance>')", name, name)
	}

	targetCfg, err := loadDocumentDBTargetConfig(path, dc.TargetInstance)
	if err != nil {
		return err
	}
	if _, err := verifyDocumentDBTarget(targetCfg, dc.Database); err != nil {
		return err
	}
	dm, err := podman.NewDocumentDBManager(cfg)
	if err != nil {
		return fmt.Errorf("documentdb manager: %w", err)
	}
	urlFile, configFile, err := dm.WriteFiles(&dc, targetCfg.Podman.HostPort)
	if err != nil {
		return err
	}
	fmt.Printf("-> Starting DocumentDB gateway %q...\n", name)
	return dm.StartContainer(&dc, urlFile, configFile)
}

func runAddonStopDocumentDB(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = documentDBDefaultName
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	dc, ok := cfg.Addons.DocumentDB[name]
	if !ok {
		return fmt.Errorf("documentdb %q not found", name)
	}
	dm, err := podman.NewDocumentDBManager(cfg)
	if err != nil {
		return fmt.Errorf("documentdb manager: %w", err)
	}
	running, _ := dm.ContainerRunning(dc.ContainerName)
	if !running {
		fmt.Printf("DocumentDB gateway %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping DocumentDB gateway %q...\n", name)
	if _, err := dm.Stop(dc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ DocumentDB gateway %q stopped\n", name)
	return nil
}

// runAddonRemoveDocumentDB removes the gateway container. --clean-data also
// deletes the TLS state volume and rendered config. PostgreSQL-side data,
// DocumentDB client users, and the docdb_gateway role are kept.
func runAddonRemoveDocumentDB(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = documentDBDefaultName
	}
	cleanData, _ := cmd.Flags().GetBool("clean-data")

	cfg, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.DocumentDB == nil {
		return fmt.Errorf("no documentdb add-ons configured")
	}
	dc, ok := cfg.Addons.DocumentDB[name]
	if !ok {
		return fmt.Errorf("documentdb %q not found", name)
	}
	dm, err := podman.NewDocumentDBManager(cfg)
	if err != nil {
		return fmt.Errorf("documentdb manager: %w", err)
	}

	fmt.Printf("-> Removing DocumentDB gateway %q...\n", name)
	if err := dm.Remove(&dc, cleanData); err != nil {
		return err
	}
	delete(cfg.Addons.DocumentDB, name)
	if len(cfg.Addons.DocumentDB) == 0 {
		cfg.Addons.DocumentDB = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ DocumentDB gateway %q removed\n", name)
	fmt.Printf("  Kept on instance %q: DocumentDB data, client user %q, and backend role %q.\n", dc.TargetInstance, dc.ClientUser, podman.DocumentDBBackendUser)
	fmt.Println("  To drop the users by hand:")
	fmt.Printf("    DROP ROLE %q;  -- the DocumentDB client user\n", dc.ClientUser)
	fmt.Printf("    DROP ROLE %q;\n", podman.DocumentDBBackendUser)
	return nil
}
