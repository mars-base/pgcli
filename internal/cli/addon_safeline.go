package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

// resolveSafelineInstance loads the config, applies defaults and returns the
// named SafeLine stack.
func resolveSafelineInstance(name string) (*config.Config, string, config.SafelineConfig, error) {
	c, path, err := loadAddonCfg()
	if err != nil {
		return nil, "", config.SafelineConfig{}, err
	}
	c.ApplyDefaults()
	sc, ok := c.Addons.Safeline[name]
	if !ok {
		return nil, "", config.SafelineConfig{}, fmt.Errorf("safeline %q not found (see 'pg addon list')", name)
	}
	return c, path, sc, nil
}

func safelineFlagName(cmd *cobra.Command) string {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "safeline"
	}
	return name
}

// rejectSafelineFlags refuses install flags that belong to other addons.
// SafeLine pins all seven images by --version/--postgres-version, bundles its
// own PostgreSQL, and the console serves HTTPS with its own certificate.
func rejectSafelineFlags(fs *pflag.FlagSet) error {
	for _, name := range []string{
		"image", "dsn", "pg-name", "password", "config-option", "config-file",
		"tls", "tls-pem", "tls-cert", "tls-key", "advertise-host", "cluster",
		"peer", "join",
	} {
		if fs.Changed(name) {
			return fmt.Errorf("--%s is not supported for SafeLine: the stack bundles its own PostgreSQL %s and every image is pinned by --version/--postgres-version", name, config.DefaultSafelinePostgresVersion)
		}
	}
	return nil
}

// applySafelineInstallFlags overlays the supported install flags onto sc.
func applySafelineInstallFlags(sc *config.SafelineConfig, fs *pflag.FlagSet, baseDir string) error {
	if fs.Changed("version") {
		v, _ := fs.GetString("version")
		if !config.ValidSafelineVersion(v) {
			return fmt.Errorf("invalid --version %q: use a release tag such as %s", v, config.DefaultSafelineVersion)
		}
		sc.Version = v
	}
	if fs.Changed("postgres-version") {
		v, _ := fs.GetString("postgres-version")
		if !config.ValidSafelineVersion(v) {
			return fmt.Errorf("invalid --postgres-version %q: use a tag such as %s", v, config.DefaultSafelinePostgresVersion)
		}
		sc.PostgresVersion = v
	}
	if fs.Changed("subnet-prefix") {
		p, _ := fs.GetString("subnet-prefix")
		if !config.ValidSafelineSubnetPrefix(p) {
			return fmt.Errorf("invalid --subnet-prefix %q: use three octets such as %s", p, config.DefaultSafelineSubnetPrefix)
		}
		sc.SubnetPrefix = p
	}
	if fs.Changed("port") {
		port, _ := fs.GetInt("port")
		if port < 1 || port > 65535 {
			return fmt.Errorf("--port must be between 1 and 65535, got %d", port)
		}
		sc.ConsolePort = port
	}
	if fs.Changed("listen") {
		sc.Listen, _ = fs.GetString("listen")
	}
	if fs.Changed("data-dir") {
		dir, _ := fs.GetString("data-dir")
		sc.DataDir = resolveUnderBase(baseDir, dir)
	}
	return nil
}

// checkSafelineNoOpFlags refuses to silently ignore flags on an
// already-installed stack: every supported flag changes the stack, so a
// mismatch requires --force.
func checkSafelineNoOpFlags(name string, stored config.SafelineConfig, fs *pflag.FlagSet, baseDir string) error {
	desired := stored
	if err := applySafelineInstallFlags(&desired, fs, baseDir); err != nil {
		return err
	}
	changed := func(flag, was, now string) string {
		if was == now {
			return ""
		}
		return fmt.Sprintf("%s (%s -> %s)", flag, was, now)
	}
	for _, c := range []string{
		changed("--version", stored.Version, desired.Version),
		changed("--postgres-version", stored.PostgresVersion, desired.PostgresVersion),
		changed("--subnet-prefix", stored.SubnetPrefix, desired.SubnetPrefix),
		changed("--port", fmt.Sprint(stored.ConsolePort), fmt.Sprint(desired.ConsolePort)),
		changed("--listen", stored.Listen, desired.Listen),
		changed("--data-dir", stored.DataDir, desired.DataDir),
	} {
		if c != "" {
			return fmt.Errorf("safeline %q is already installed; %s differs from the stored value — re-run with --force to recreate the stack", name, c)
		}
	}
	return nil
}

func runAddonInstallSafeline(cmd *cobra.Command) error {
	name := safelineFlagName(cmd)
	if !config.ValidSafelineName(name) {
		return fmt.Errorf("invalid SafeLine instance name %q: use letters, digits, '_' and '-', starting with a letter or digit (max 48 chars)", name)
	}
	fmt.Printf("=== pg addon install safeline (%s) ===\n", name)

	fs := cmd.Flags()
	if err := rejectSafelineFlags(fs); err != nil {
		return err
	}
	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	force, _ := fs.GetBool("force")
	c.ApplyDefaults()
	if c.Addons.Safeline == nil {
		c.Addons.Safeline = make(map[string]config.SafelineConfig)
	}
	baseDir := c.BaseDir
	if baseDir == "" {
		baseDir = platform.DefaultConfigDir()
	}
	stored, present := c.Addons.Safeline[name]

	if present && !force {
		if err := checkSafelineNoOpFlags(name, stored, fs, baseDir); err != nil {
			return err
		}
		state := "unknown"
		if m, err := podman.NewSafelineManager(c); err == nil {
			if n, err := m.RunningCount(&stored); err == nil {
				state = fmt.Sprintf("%d/7 containers running", n)
			}
		}
		fmt.Printf("✓ safeline %q already installed (%s, console %s) — re-install with --force to rewrite the config and recreate the stack\n",
			name, state, podman.ConsoleURL(&stored))
		fmt.Printf("  Admin:       %s (retrieve the password with `pg addon password safeline --name %s`)\n", stored.AdminUser, name)
		fmt.Printf("  Subnet:      %s\n", config.SafelineSubnet(stored))
		fmt.Printf("  Data dir:    %s\n", stored.DataDir)
		return nil
	}

	sc := config.SafelineConfig{Name: name}
	if present {
		sc = stored
	}
	sc.Name = name
	if err := applySafelineInstallFlags(&sc, fs, baseDir); err != nil {
		return err
	}
	c.Addons.Safeline[name] = sc
	c.ApplyDefaults()
	sc = c.Addons.Safeline[name]
	if err := c.Validate(); err != nil {
		return err
	}

	m, err := podman.NewSafelineManager(c)
	if err != nil {
		return err
	}
	if err := m.EnsureImages(&sc); err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] images: SafeLine %s (6 components) + PostgreSQL %s\n", sc.Version, sc.PostgresVersion)
	if sc.PostgresPassword == "" {
		// The bundled cluster keeps the password it was initialized with, so
		// data kept by `remove` (without --clean-data) is only usable again
		// with the recorded password.
		pw, err := m.ReadBundledPassword(&sc)
		if err != nil {
			return fmt.Errorf("  [X] %v", err)
		}
		if pw != "" {
			fmt.Printf("  [i] reusing the bundled PostgreSQL password kept under %s\n", sc.DataDir)
		} else {
			pw, err = generatePassword(24)
			if err != nil {
				return err
			}
			if err := m.WriteBundledPassword(&sc, pw); err != nil {
				return fmt.Errorf("  [X] %v", err)
			}
		}
		sc.PostgresPassword = pw
		c.Addons.Safeline[name] = sc
	} else if err := m.WriteBundledPassword(&sc, sc.PostgresPassword); err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	if sc.DetectorMgmtToken == "" {
		// The detector image gates its management API (fvm pushes rules there,
		// luigi polls it) on a shared token; without one the console cannot
		// publish site rules.
		tok, err := generatePassword(32)
		if err != nil {
			return err
		}
		sc.DetectorMgmtToken = tok
		c.Addons.Safeline[name] = sc
	}
	// Persist before starting so a failed first boot stays inspectable.
	if err := c.Save(path); err != nil {
		return fmt.Errorf("  [X] saving config: %w", err)
	}
	if err := m.EnsureNetwork(&sc); err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	if err := m.EnsureContainers(&sc); err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	// resetadmin rotates the console password, so run it only when none is
	// recorded and on --force (the stack was just recreated).
	if sc.AdminPassword == "" || (present && force) {
		user, pw, err := m.ResetAdmin(&sc)
		if err != nil {
			return fmt.Errorf("  [X] %v", err)
		}
		sc.AdminUser = user
		sc.AdminPassword = pw
		c.Addons.Safeline[name] = sc
		if err := c.Save(path); err != nil {
			return fmt.Errorf("  [X] saving config: %w", err)
		}
	}

	fmt.Println()
	fmt.Printf("✓ SafeLine WAF %q installed\n", name)
	fmt.Printf("  Console:     %s (self-signed certificate)\n", podman.ConsoleURL(&sc))
	fmt.Printf("  Admin:       %s\n", sc.AdminUser)
	fmt.Printf("  Password:    %s (also stored: `pg addon password safeline --name %s`)\n", sc.AdminPassword, name)
	fmt.Printf("  Version:     SafeLine %s, PostgreSQL %s\n", sc.Version, sc.PostgresVersion)
	fmt.Printf("  Subnet:      %s (gateway %s)\n", config.SafelineSubnet(sc), config.SafelineGateway(sc))
	fmt.Printf("  Containers:  postgres, mgt, detector, luigi, fvm, chaos (bridge %s); tengine (host network)\n", sc.NetworkName)
	fmt.Printf("  Data dir:    %s\n", sc.DataDir)
	fmt.Printf("  Traffic:     tengine listens on host networking — add protected sites in the console;\n")
	fmt.Printf("               their entry ports default to 80/443 and must be free on this host\n")
	return nil
}

func runAddonRemoveSafeline(cmd *cobra.Command) error {
	name := safelineFlagName(cmd)
	c, path, sc, err := resolveSafelineInstance(name)
	if err != nil {
		return err
	}
	cleanData, _ := cmd.Flags().GetBool("clean-data")
	fmt.Printf("=== pg addon remove safeline (%s) ===\n", name)

	m, err := podman.NewSafelineManager(c)
	if err != nil {
		return err
	}
	if err := m.RemoveContainers(&sc); err != nil {
		return err
	}
	if err := m.RemoveNetworkIfUnused(&sc); err != nil {
		return err
	}
	delete(c.Addons.Safeline, name)
	if len(c.Addons.Safeline) == 0 {
		c.Addons.Safeline = nil
	}
	if err := c.Save(path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Printf("  [OK] config entry removed (%s)\n", path)

	if cleanData {
		if err := m.CleanData(&sc); err != nil {
			return err
		}
	} else {
		fmt.Printf("  [i] kept %s (bundled PostgreSQL data + console state) — pass --clean-data to drop\n", sc.DataDir)
	}
	fmt.Printf("✓ SafeLine WAF %q removed\n", name)
	return nil
}

func runAddonStartSafeline(cmd *cobra.Command) error {
	name := safelineFlagName(cmd)
	c, path, sc, err := resolveSafelineInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewSafelineManager(c)
	if err != nil {
		return err
	}
	var already bool
	if sc.DetectorMgmtToken == "" {
		// Installs made before pgcli managed the token: generate one and
		// recreate the containers so detector, fvm and luigi share it.
		tok, err := generatePassword(32)
		if err != nil {
			return err
		}
		sc.DetectorMgmtToken = tok
		c.Addons.Safeline[name] = sc
		if err := c.Save(path); err != nil {
			return fmt.Errorf("saving config: %w", err)
		}
		if err := m.EnsureContainers(&sc); err != nil {
			return err
		}
	} else {
		already, err = m.StartContainers(&sc)
		if err != nil {
			return err
		}
	}
	if sc.AdminPassword == "" {
		user, pw, err := m.ResetAdmin(&sc)
		if err != nil {
			return err
		}
		sc.AdminUser = user
		sc.AdminPassword = pw
		c.Addons.Safeline[name] = sc
		if err := c.Save(path); err != nil {
			return fmt.Errorf("saving config: %w", err)
		}
		fmt.Printf("  [OK] console admin: %s / %s\n", sc.AdminUser, sc.AdminPassword)
	}
	if already {
		fmt.Printf("✓ safeline %q already running (console %s)\n", name, podman.ConsoleURL(&sc))
		return nil
	}
	fmt.Printf("✓ safeline %q started (console %s)\n", name, podman.ConsoleURL(&sc))
	return nil
}

func runAddonStopSafeline(cmd *cobra.Command) error {
	name := safelineFlagName(cmd)
	c, _, sc, err := resolveSafelineInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewSafelineManager(c)
	if err != nil {
		return err
	}
	if err := m.StopContainers(&sc); err != nil {
		return err
	}
	fmt.Printf("✓ safeline %q stopped\n", name)
	return nil
}
