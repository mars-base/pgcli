package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/registry"
)

func init() {
	rootCmd.AddCommand(mirrorCmd)
	mirrorCmd.AddCommand(mirrorSetCmd, mirrorShowCmd, mirrorCleanCmd)
}

var mirrorCmd = &cobra.Command{
	Use:   "mirror",
	Short: "Configure container registry mirrors",
	Long: `Configure podman registry mirrors for docker.io, ghcr.io and quay.io.

The command writes a single drop-in configuration at
/etc/containers/registries.conf.d/99-pgcli-registry-mirror.conf when run as
root or when passwordless sudo is available. Otherwise it writes to the
invoking user's ~/.config/containers/registries.conf.d/ directory.

The old pgcli-managed 99-pgcli-docker-mirror.conf file is migrated
automatically by the next set operation. This command is Linux-only and does
not read or write pg.yaml.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if platform.Detect() != platform.Linux {
			return fmt.Errorf("pg mirror requires Linux")
		}
		if cmd.Root().PersistentFlags().Changed("output") {
			return fmt.Errorf("pg mirror does not use --output")
		}
		return nil
	},
}

var mirrorSetCmd = &cobra.Command{
	Use:   "set HOST | set REGISTRY=HOST [REGISTRY=HOST...]",
	Short: "Configure or update registry mirrors",
	Long: `Configure or update registry mirrors.

A single HOST targets docker.io. To configure one or more registries
explicitly, use REGISTRY=HOST; supported registries are docker.io, ghcr.io
and quay.io.

HOST accepts a hostname, hostname:port, bracketed IPv6, or [IPv6]:port.
Every set rewrites the pgcli-managed file, preserving mirrors for registries
that are not mentioned in the current invocation.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runMirrorSet,
}

var mirrorShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the effective registry mirrors",
	Args:  cobra.NoArgs,
	RunE:  runMirrorShow,
}

var mirrorCleanCmd = &cobra.Command{
	Use:   "clean [REGISTRY...]",
	Short: "Remove registry mirror configuration",
	Long: `Remove pgcli-managed registry mirror configuration.

Without arguments, remove all managed config files. With one or more
registries (docker.io, ghcr.io, quay.io), remove only those mirrors from both
the system and user scopes.`,
	Args: cobra.ArbitraryArgs,
	RunE: runMirrorClean,
}

func runMirrorSet(cmd *cobra.Command, args []string) error {
	mirrors, err := parseMirrorArgs(args)
	if err != nil {
		return err
	}
	res, err := registry.NewManager().Set(mirrors)
	if err != nil {
		return err
	}

	for _, registryName := range registry.Registries() {
		host, ok := res.Mirrors[registryName]
		if !ok {
			continue
		}
		previous, existed := res.Previous[registryName]
		switch {
		case existed && previous != host:
			fmt.Printf("  [OK] %s mirror updated: %s -> %s\n", registryName, previous, host)
		case existed:
			fmt.Printf("  [OK] %s mirror configured: %s (file rewritten)\n", registryName, host)
		default:
			fmt.Printf("  [OK] %s mirror configured: %s\n", registryName, host)
		}
	}
	fmt.Printf("  Config: %s\n", res.Path)

	if res.ReplacedUnparseable {
		fmt.Printf("  [!] the previous managed config could not be fully parsed and was replaced\n")
	}
	if !res.System {
		fmt.Printf("  [i] passwordless sudo unavailable; using user-level config\n")
	}
	if res.RemovedLegacyPath != "" {
		fmt.Printf("  [i] migrated and removed legacy config: %s\n", res.RemovedLegacyPath)
	}
	for _, path := range res.RemovedUserPaths {
		fmt.Printf("  [i] removed user-level config that would have overridden the system config: %s\n", path)
	}
	if len(res.RemovedUserMirrors) > 0 && res.RewrittenUserPath != "" {
		fmt.Printf("  [i] removed user-level mirrors for %s from %s\n", strings.Join(res.RemovedUserMirrors, ", "), res.RewrittenUserPath)
	}
	if !res.System {
		for _, registryName := range registry.Registries() {
			source, ok := res.Shadowed[registryName]
			if !ok {
				continue
			}
			legacy := ""
			if source.Legacy {
				legacy = ", legacy"
			}
			fmt.Printf("  [i] system %s mirror %s (%s%s) is shadowed by this user-level config\n", registryName, source.Host, source.Path, legacy)
		}
		if res.SystemUncertain {
			fmt.Printf("  [i] the system-level managed config could not be parsed; shadowing state is unknown\n")
		}
	}
	fmt.Printf("  [i] configured registries use the mirror; other registries stay direct\n")
	return nil
}

func parseMirrorArgs(args []string) (map[string]string, error) {
	if len(args) == 1 && !strings.Contains(args[0], "=") {
		return map[string]string{registry.DefaultRegistry: args[0]}, nil
	}

	mirrors := make(map[string]string)
	for _, arg := range args {
		registryName, host, ok := strings.Cut(arg, "=")
		if !ok {
			return nil, fmt.Errorf("expected REGISTRY=HOST (for example ghcr.io=mirror.example.com); a single HOST means docker.io")
		}
		if !registry.IsSupportedRegistry(registryName) {
			return nil, fmt.Errorf("unsupported registry %q: supported registries are %s", registryName, strings.Join(registry.Registries(), ", "))
		}
		if host == "" {
			return nil, fmt.Errorf("%s: mirror host is required", registryName)
		}
		if _, duplicate := mirrors[registryName]; duplicate {
			return nil, fmt.Errorf("registry %q specified more than once", registryName)
		}
		mirrors[registryName] = host
	}
	return mirrors, nil
}

func runMirrorShow(cmd *cobra.Command, args []string) error {
	res, err := registry.NewManager().Show()
	if err != nil {
		return err
	}

	if res.System.Err != nil {
		fmt.Fprintf(os.Stderr, "  [!] %v\n", res.System.Err)
	}
	if res.User.Err != nil {
		fmt.Fprintf(os.Stderr, "  [!] %v\n", res.User.Err)
	}

	printed := false
	for _, status := range res.Mirrors {
		if status.Host == "" {
			continue
		}
		printed = true
		fmt.Printf("%s mirror: %s\n", status.Registry, status.Host)
		legacy := ""
		if status.Legacy {
			legacy = ", legacy"
		}
		fmt.Printf("  Source: %s (%s%s)\n", status.Path, status.Scope, legacy)
		if status.Shadowed != nil {
			fmt.Printf("  [i] system config %s at %s is shadowed by the user-level file\n", status.Shadowed.Host, status.Shadowed.Path)
		}
	}

	if !printed {
		if res.Uncertain {
			fmt.Println("No registry mirrors could be determined.")
		} else {
			fmt.Println("No registry mirrors configured.")
		}
		fmt.Println("Configure one with: pg mirror set HOST (docker.io) or pg mirror set ghcr.io=HOST")
	} else {
		if res.System.LegacyExists {
			fmt.Printf("  [i] legacy config detected: %s (migrated by the next pg mirror set)\n", res.System.LegacyPath)
		}
		if res.User.LegacyExists {
			fmt.Printf("  [i] legacy user config detected: %s (migrated by the next pg mirror set)\n", res.User.LegacyPath)
		}
	}
	if res.Uncertain {
		fmt.Fprintln(os.Stderr, "  [!] some managed config could not be parsed; values above may not be effective")
	}
	return nil
}

func runMirrorClean(cmd *cobra.Command, args []string) error {
	res, err := registry.NewManager().Clean(args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		if len(res.Removed) == 0 {
			fmt.Println("  [i] no registry mirror config to clean")
			return nil
		}
		for _, path := range res.Removed {
			fmt.Printf("  [OK] removed %s\n", path)
		}
		fmt.Println("  [i] new pulls use the upstream registries directly; running containers are unaffected")
		return nil
	}

	if len(res.RemovedRegistries) == 0 {
		fmt.Printf("  [i] no mirror configured for %s\n", strings.Join(args, ", "))
		return nil
	}
	for _, registryName := range res.RemovedRegistries {
		fmt.Printf("  [OK] removed %s mirror\n", registryName)
	}
	for _, path := range res.Paths {
		fmt.Printf("  [i] updated %s\n", path)
	}
	for _, registryName := range res.RemovedRegistries {
		fmt.Printf("  [i] new %s pulls use %s directly; running containers are unaffected\n", registryName, registryName)
	}
	return nil
}
