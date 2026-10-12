package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

// generateGarageAccessKey builds a Garage access key id: "GK" + 32 hex chars,
// the shape `openssl rand -hex 16` produces in Garage's own quick-start.
func generateGarageAccessKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "GK" + hex.EncodeToString(b), nil
}

// generateGarageSecretKey builds a 64-hex-char secret — the shape Garage
// documents for GARAGE_DEFAULT_SECRET_KEY (`openssl rand -hex 32`), reused for
// the rpc_secret (also 32 random bytes, hex-encoded).
func generateGarageSecretKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// runAddonInstallGarage installs a single-node Garage container. Shape mirrors
// runAddonInstallRustfs, minus everything Garage has no runtime for: no TLS
// (rejected with a pointer to the nginx front), no endpoints/drives (v1 is
// single-node; ValidateGarageSingle catches a hand-edited replication_factor),
// and credentials are an access/secret key pair generated here (GK-prefixed)
// rather than an "admin" user.
func runAddonInstallGarage(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "garage"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	apiPort, _ := cmd.Flags().GetInt("api-port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	rootUser, _ := cmd.Flags().GetString("root-user")
	rootPassword, _ := cmd.Flags().GetString("root-password")
	force, _ := cmd.Flags().GetBool("force")

	// Garage serves plaintext HTTP only — refuse --tls/--tls-cert/--tls-key
	// with the recipe instead of silently ignoring the operator's intent.
	tls, _ := cmd.Flags().GetBool("tls")
	tlsCert, _ := cmd.Flags().GetString("tls-cert")
	tlsKey, _ := cmd.Flags().GetString("tls-key")
	if tls || tlsCert != "" || tlsKey != "" {
		return fmt.Errorf("garage has no native TLS (it serves plaintext HTTP); front it with the nginx addon via --conf-file instead (template --upstream mode breaks S3 request signing) — see docs/addon/garage")
	}

	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s -- run \"pg config init\" first", path)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if cfg.Addons.Garage == nil {
		cfg.Addons.Garage = make(map[string]config.GarageConfig)
	}
	existing, ok := cfg.Addons.Garage[name]
	if !ok {
		existing = config.GarageConfig{
			ContainerName: "pgcli-garage" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	if dataDir != "" {
		existing.DataDir = dataDir
	}
	if apiPort != 0 {
		// Only the API port is a knob; ApplyDefaults' garage pool walk takes
		// the next free ports for RPC/Web/Admin after it.
		existing.APIPort = apiPort
	}
	if rootUser != "" {
		existing.RootUser = rootUser
	}
	if rootPassword != "" {
		existing.RootPassword = rootPassword
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if err := podman.ValidateGarageSingle(existing.ReplicationFactor); err != nil {
		return err
	}
	cfg.Addons.Garage[name] = existing

	// ApplyDefaults fills ContainerName/ImageTag/Listen/Region/ReplicationFactor
	// and assigns the four ports from Garage's own pool (garage_start_port,
	// default 3900). Credentials and rpc_secret are NOT managed there — they
	// are secrets the install path owns.
	cfg.ApplyDefaults()
	gc := cfg.Addons.Garage[name]

	// macOS: the store serves on the pgcli-net bridge with published ports, so
	// bring up the machine and the bridge first (no-op on Linux, where Garage
	// uses host networking).
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	gm, err := podman.NewGarageManager(cfg)
	if err != nil {
		return fmt.Errorf("garage manager: %w", err)
	}

	// Same reuse-without-force semantics as rustfs: a present container is
	// reused (started if stopped); --force recreates so changed ports/listen/
	// credentials take effect.
	exists, err := gm.ContainerExists(gc.ContainerName)
	if err != nil {
		return err
	}
	skipped := false
	if exists && !force {
		skipped = true
		if running, _ := gm.ContainerRunning(gc.ContainerName); running {
			fmt.Printf("-> garage container %q already running; skipping creation\n", gc.ContainerName)
		} else {
			fmt.Printf("-> garage container %q exists but is stopped; starting it\n", gc.ContainerName)
			if err := gm.StartContainer(&gc); err != nil {
				return err
			}
		}
	} else {
		// First-boot seeding (GARAGE_DEFAULT_* env) needs the key pair and the
		// cluster secret before the container is created; generate each once
		// and persist them in pg.yaml.
		if gc.RootUser == "" {
			ak, err := generateGarageAccessKey()
			if err != nil {
				return fmt.Errorf("generating garage access key: %w", err)
			}
			gc.RootUser = ak
		}
		if gc.RootPassword == "" {
			sk, err := generateGarageSecretKey()
			if err != nil {
				return fmt.Errorf("generating garage secret key: %w", err)
			}
			gc.RootPassword = sk
		}
		if gc.RPCSecret == "" {
			rs, err := generateGarageSecretKey()
			if err != nil {
				return fmt.Errorf("generating garage rpc secret: %w", err)
			}
			gc.RPCSecret = rs
		}
		fmt.Printf("-> Preparing garage image %s...\n", gc.ImageTag)
		if err := gm.EnsureImage(gc.ImageTag); err != nil {
			return err
		}
		fmt.Println("-> Starting garage container (single-node)...")
		if err := gm.EnsureContainer(&gc); err != nil {
			return err
		}
	}

	cfg.Addons.Garage[name] = gc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	if skipped {
		fmt.Printf("✓ garage already present: %q\n", name)
	} else {
		fmt.Printf("✓ garage installed: %q\n", name)
	}
	fmt.Printf("  Container:     %s\n", gc.ContainerName)
	fmt.Printf("  Image:         %s\n", gc.ImageTag)
	fmt.Printf("  Data:          %s\n", gm.DataDir(&gc))
	fmt.Printf("  Metadata:      %s\n", gm.MetadataDir(&gc))
	fmt.Printf("  Config:        %s\n", filepath.Join(filepath.Dir(gm.DataDir(&gc)), "garage.toml"))
	fmt.Printf("  S3 API:        http://%s:%d\n", gc.Listen, gc.APIPort)
	fmt.Printf("  RPC/Web/Admin: %d/%d/%d\n", gc.RPCPort, gc.WebPort, gc.AdminPort)
	fmt.Println()
	fmt.Printf("  Access key:    %s\n", gc.RootUser)
	fmt.Printf("  Secret key:    %s\n", gc.RootPassword)
	fmt.Printf("  Bucket:        %s (created on first boot, owned by the access key)\n", gc.Name)
	fmt.Println()
	fmt.Println("  NOTE: Garage serves plaintext HTTP only. pgBackRest requires HTTPS — front it")
	fmt.Println("        with the nginx addon via --conf-file (template --upstream mode strips")
	fmt.Println("        the port from Host and breaks S3 signing; see docs/addon/garage):")
	fmt.Printf("          backend 127.0.0.1:%d, listen ssl, proxy_set_header Host $http_host\n", gc.APIPort)
	fmt.Printf("  Try it:        pg mc alias set %s http://127.0.0.1:%d %s %s && pg mc ls %s\n",
		gc.Name, gc.APIPort, gc.RootUser, gc.RootPassword, gc.Name)
	return nil
}

// runAddonRemoveGarage removes the garage container and its config entry.
func runAddonRemoveGarage(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "garage"
	}
	cleanData, _ := cmd.Flags().GetBool("clean-data")

	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s -- run \"pg config init\" first", path)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if cfg.Addons.Garage == nil {
		return fmt.Errorf("no garage add-ons configured")
	}
	gc, ok := cfg.Addons.Garage[name]
	if !ok {
		return fmt.Errorf("garage %q not found", name)
	}

	gm, err := podman.NewGarageManager(cfg)
	if err != nil {
		return fmt.Errorf("garage manager: %w", err)
	}

	fmt.Printf("-> Removing garage %q...\n", name)
	if err := gm.Remove(&gc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.Garage, name)
	if len(cfg.Addons.Garage) == 0 {
		cfg.Addons.Garage = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ garage %q removed\n", name)
	return nil
}

// runAddonStartGarage starts an existing garage container.
func runAddonStartGarage(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "garage"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.Garage == nil {
		return fmt.Errorf("no garage add-ons configured (run 'pg addon install garage')")
	}
	gc, ok := cfg.Addons.Garage[name]
	if !ok {
		return fmt.Errorf("garage %q not found (run 'pg addon install garage --name %s')", name, name)
	}
	gm, err := podman.NewGarageManager(cfg)
	if err != nil {
		return fmt.Errorf("garage manager: %w", err)
	}
	fmt.Printf("-> Starting garage %q...\n", name)
	return gm.StartContainer(&gc)
}

// runAddonStopGarage stops a running garage container.
func runAddonStopGarage(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "garage"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	gc, ok := cfg.Addons.Garage[name]
	if !ok {
		return fmt.Errorf("garage %q not found", name)
	}
	gm, err := podman.NewGarageManager(cfg)
	if err != nil {
		return fmt.Errorf("garage manager: %w", err)
	}
	running, _ := gm.ContainerRunning(gc.ContainerName)
	if !running {
		fmt.Printf("garage %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping garage %q...\n", name)
	if _, err := gm.Stop(gc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ garage %q stopped\n", name)
	return nil
}
