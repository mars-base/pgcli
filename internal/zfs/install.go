package zfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const (
	debianFedoraReleaseURL = "https://zfsonlinux.org/fedora/zfs-release-3-1"
	rockyEPELReleaseURL    = "https://zfsonlinux.org/epel/zfs-release-3-0"
)

var (
	distTagRe     = regexp.MustCompile(`^\.[a-z]+\d+$`)
	versionCoreRe = regexp.MustCompile(`\d+\.\d+\.\d+`)
)

// Health is the result of probing the ZFS userland and kernel module.
type Health struct {
	ZpoolPath       string
	ZFSPath         string
	UserlandVersion string
	ModuleVersion   string
	ModuleLoaded    bool
	KernelRelease   string
}

func (h Health) Installed() bool { return h.ZpoolPath != "" && h.ZFSPath != "" }

func (h Health) VersionMismatch() bool {
	userland := versionCoreRe.FindString(h.UserlandVersion)
	module := versionCoreRe.FindString(h.ModuleVersion)
	return userland != "" && module != "" && userland != module
}

func (h Health) Ready() bool {
	return h.Installed() && h.ModuleLoaded && !h.VersionMismatch()
}

// ProbeHealth inspects binaries, the running kernel and the loaded module.
func ProbeHealth(ctx context.Context, r *Runner) Health {
	health := Health{}
	if path, err := LookPath("zpool"); err == nil {
		health.ZpoolPath = path
	}
	if path, err := LookPath("zfs"); err == nil {
		health.ZFSPath = path
	}
	health.ModuleLoaded = r.LoadedModule()
	if health.ModuleLoaded {
		health.ModuleVersion, _ = r.ModuleVersion(ctx)
	}
	health.KernelRelease, _ = r.KernelRelease(ctx)
	if health.ZpoolPath != "" {
		health.UserlandVersion, _ = r.UserlandVersion(ctx)
	}
	return health
}

// Install performs the zero-argument installation flow.
func Install(ctx context.Context, r *Runner) error {
	distro, err := DetectDistro()
	if err != nil {
		return err
	}
	fmt.Printf("Detected: %s [%s]\n", distro.Display(), distro.Family)
	if !distro.Supported() {
		return errors.New(distro.UnsupportedMessage())
	}

	health := ProbeHealth(ctx, r)
	if health.KernelRelease == "" {
		return fmt.Errorf("cannot determine the running kernel release")
	}
	if health.Ready() {
		printHealth(health)
		fmt.Println("ZFS is already installed and healthy.")
		return nil
	}

	if health.Installed() && !health.ModuleLoaded {
		fmt.Println("-> ZFS userland is present but the kernel module is not loaded; repairing...")
		if err := repairModule(ctx, r, distro, health); err == nil {
			if err := finishSetup(ctx, r); err != nil {
				return err
			}
			return verifyInstalled(ctx, r)
		} else {
			fmt.Printf("  [!] Module repair failed: %v\n", err)
			fmt.Println("-> Falling back to a full package install...")
		}
	}

	switch distro.Family {
	case FamilyDebian, FamilyUbuntu:
		err = installAPT(ctx, r, distro, health)
	case FamilyFedora, FamilyRHEL:
		err = installDNF(ctx, r, distro, health)
	default:
		return errors.New(distro.UnsupportedMessage())
	}
	if err != nil {
		return err
	}
	if err := finishSetup(ctx, r); err != nil {
		return err
	}
	return verifyInstalled(ctx, r)
}

func aptPackages(distro Distro, kernelRelease string) []string {
	switch distro.Family {
	case FamilyDebian:
		// DKMS needs a compiler; never assume it is already installed.
		return []string{"build-essential", "linux-headers-" + kernelRelease, "zfs-dkms", "zfsutils-linux", "zfs-zed"}
	case FamilyUbuntu:
		// Ubuntu ships an in-tree module; universe's zfs-dkms is outdated.
		return []string{"zfsutils-linux", "zfs-zed"}
	default:
		return nil
	}
}

func installAPT(ctx context.Context, r *Runner, distro Distro, health Health) error {
	if _, err := LookPath("apt-get"); err != nil {
		return err
	}
	fmt.Printf("-> Ensuring %s is enabled in %s archive sources...\n", APTComponent(distro.Family), distro.Family)
	if _, err := EnableAPTComponent(ctx, r, distro); err != nil {
		return err
	}
	packages := aptPackages(distro, health.KernelRelease)
	if err := aptGetInstall(ctx, r, packages, true); err != nil {
		return err
	}
	return repairModule(ctx, r, distro, health)
}

func aptGetInstall(ctx context.Context, r *Runner, packages []string, update bool) error {
	apt, err := LookPath("apt-get")
	if err != nil {
		return err
	}
	env := []string{"DEBIAN_FRONTEND=noninteractive"}
	if update {
		fmt.Println("-> Updating package lists...")
		if err := r.RunPrivilegedEnv(ctx, env, apt, "update"); err != nil {
			return fmt.Errorf("apt-get update: %w", err)
		}
	}
	fmt.Printf("-> Installing %s...\n", strings.Join(packages, " "))
	args := append([]string{"install", "-y", "--no-install-recommends"}, packages...)
	if err := r.RunPrivilegedEnv(ctx, env, apt, args...); err != nil {
		return fmt.Errorf("apt-get install: %w", err)
	}
	return nil
}

// ZFSReleaseURL constructs the OpenZFS release RPM URL for the running RPM
// distro. distTag comes from `rpm --eval %{dist}`.
func ZFSReleaseURL(family DistroFamily, distTag string) (string, error) {
	distTag = strings.TrimSpace(distTag)
	if !distTagRe.MatchString(distTag) {
		return "", fmt.Errorf("unexpected RPM dist tag %q (from rpm --eval %%{dist})", distTag)
	}
	switch family {
	case FamilyFedora:
		if !strings.HasPrefix(distTag, ".fc") {
			return "", fmt.Errorf("Fedora dist tag must start with .fc, got %q", distTag)
		}
		return debianFedoraReleaseURL + distTag + ".noarch.rpm", nil
	case FamilyRHEL:
		if !strings.HasPrefix(distTag, ".el") {
			return "", fmt.Errorf("RHEL dist tag must start with .el, got %q", distTag)
		}
		return rockyEPELReleaseURL + distTag + ".noarch.rpm", nil
	default:
		return "", fmt.Errorf("no OpenZFS release URL for %s", family)
	}
}

func installDNF(ctx context.Context, r *Runner, distro Distro, health Health) error {
	dnf, err := LookPath("dnf")
	if err != nil {
		return err
	}
	rpm, err := LookPath("rpm")
	if err != nil {
		return err
	}
	out, err := r.Output(ctx, rpm, "--eval", "%{dist}")
	if err != nil {
		return err
	}
	distTag := strings.TrimSpace(string(out))
	url, err := ZFSReleaseURL(distro.Family, distTag)
	if err != nil {
		return err
	}
	fmt.Printf("-> Installing OpenZFS repository for %s...\n", distTag)
	if err := r.RunPrivileged(ctx, dnf, "install", "-y", url); err != nil {
		return fmt.Errorf("installing OpenZFS release package %s: %w", url, err)
	}
	if distro.Family == FamilyRHEL {
		fmt.Println("-> Ensuring EPEL is available...")
		if err := r.RunPrivileged(ctx, dnf, "install", "-y", "epel-release"); err != nil {
			fmt.Printf("  [!] epel-release install failed (continuing): %v\n", err)
		}
	}
	if pkg, err := r.Output(ctx, rpm, "-q", "zfs-fuse"); err == nil && strings.TrimSpace(string(pkg)) != "" {
		fmt.Println("  [!] zfs-fuse is installed and conflicts with OpenZFS; remove it if the module fails to load")
	}
	packages := []string{"kernel-devel-" + health.KernelRelease, "dkms", "zfs"}
	fmt.Printf("-> Installing %s...\n", strings.Join(packages, " "))
	args := append([]string{"install", "-y"}, packages...)
	if err := r.RunPrivileged(ctx, dnf, args...); err != nil {
		return fmt.Errorf("dnf install: %w", err)
	}
	return repairModule(ctx, r, distro, health)
}

func repairModule(ctx context.Context, r *Runner, distro Distro, health Health) error {
	modprobe, err := LookPath("modprobe")
	if err != nil {
		return err
	}
	if err := r.RunPrivileged(ctx, modprobe, "zfs"); err == nil {
		return nil
	}
	if distro.Family == FamilyUbuntu {
		extra := "linux-modules-extra-" + health.KernelRelease
		fmt.Printf("  -> Installing %s for the ZFS kernel module...\n", extra)
		if extraErr := aptGetInstall(ctx, r, []string{extra}, false); extraErr != nil {
			fmt.Printf("  [!] %v\n", extraErr)
		}
		if err := r.RunPrivileged(ctx, modprobe, "zfs"); err == nil {
			return nil
		}
	}
	if dkms, dkmsErr := LookPath("dkms"); dkmsErr == nil {
		fmt.Printf("  -> Rebuilding the ZFS module via DKMS for kernel %s...\n", health.KernelRelease)
		if err := r.RunPrivileged(ctx, dkms, "autoinstall", "-k", health.KernelRelease); err != nil {
			fmt.Printf("  [!] dkms autoinstall: %v\n", err)
		}
		if err := r.RunPrivileged(ctx, modprobe, "zfs"); err == nil {
			return nil
		}
	}
	return fmt.Errorf("the zfs kernel module could not be loaded for kernel %s", health.KernelRelease)
}

func finishSetup(ctx context.Context, r *Runner) error {
	if err := ensureModuleLoadConfig(ctx, r); err != nil {
		return err
	}
	ensureServices(ctx, r)
	return nil
}

func ensureModuleLoadConfig(ctx context.Context, r *Runner) error {
	const path = "/etc/modules-load.d/zfs.conf"
	data, err := r.ReadFile(ctx, path)
	content := string(data)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if moduleLoadConfigured(content) {
		return nil
	}
	updated := content
	if updated != "" && !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	updated += "zfs\n"
	fmt.Println("-> Ensuring zfs loads at boot...")
	if err := r.WriteFileAtomic(ctx, path, []byte(updated), 0644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

func moduleLoadConfigured(content string) bool {
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "zfs" {
			return true
		}
	}
	return false
}

func ensureServices(ctx context.Context, r *Runner) {
	systemctl, err := LookPath("systemctl")
	if err != nil {
		return
	}
	units := []string{
		"zfs-load-module.service",
		"zfs-import-cache.service",
		"zfs-mount.service",
		"zfs-zed.service",
		"zfs-scrub-monthly.timer",
		"zfs-trim-weekly.timer",
	}
	enabled := 0
	for _, unit := range units {
		if _, err := r.OutputPrivileged(ctx, systemctl, "cat", unit); err != nil {
			continue
		}
		if err := r.RunPrivileged(ctx, systemctl, "enable", "--now", unit); err != nil {
			fmt.Printf("  [!] Could not enable %s: %v\n", unit, err)
			continue
		}
		enabled++
	}
	if enabled > 0 {
		fmt.Printf("  [OK] Enabled %d ZFS systemd unit(s)\n", enabled)
	}
}

func verifyInstalled(ctx context.Context, r *Runner) error {
	health := ProbeHealth(ctx, r)
	printHealth(health)
	if !health.Ready() {
		if dkms, err := LookPath("dkms"); err == nil {
			if out, dkmsErr := r.Output(ctx, dkms, "status"); dkmsErr == nil && strings.TrimSpace(string(out)) != "" {
				fmt.Println("dkms status:")
				fmt.Print(string(out))
			}
		}
		return fmt.Errorf("ZFS is installed but not ready; see the health details above")
	}
	fmt.Println("ZFS installation complete.")
	if SecureBootEnabled() {
		fmt.Println()
		fmt.Println("  [!] Secure Boot is enabled. DKMS-built ZFS modules must be signed;")
		fmt.Println("      use your distro's MOK enrollment flow if module loading fails after a kernel update.")
	}
	return nil
}

func printHealth(health Health) {
	fmt.Println()
	fmt.Printf("  Userland:      %s\n", valueOr(health.UserlandVersion, "unknown"))
	if health.ModuleLoaded {
		fmt.Printf("  Kernel module: %s (loaded)\n", valueOr(health.ModuleVersion, "unknown"))
	} else {
		fmt.Println("  Kernel module: not loaded")
	}
	fmt.Printf("  Kernel:        %s\n", valueOr(health.KernelRelease, "unknown"))
	if health.VersionMismatch() {
		fmt.Println("  [!] Userland and kernel module versions differ")
	}
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
