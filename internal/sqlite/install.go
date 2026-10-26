package sqlite

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mars-base/pgcli/internal/hostbin"
)

// LitestreamVersion is the only supported Litestream line: v0.5's config and
// CLI are incompatible with 0.3.x, so binaries of other versions are ignored.
const LitestreamVersion = "0.5.17"

const litestreamRepo = "benbjohnson/litestream"

// SHA256 digests come from the v0.5.17 checksums.txt release asset.
var litestreamSHA256 = map[string]string{
	"amd64": "cfb371176d164437ae869f8351cfde49bd1804ae71c61923f75c9cba9c9c006d",
	"arm64": "f8ca4a050095c1efbda2c4365172e61bf9d955ea0d9ac42f448b52e51819baa5",
}

var litestreamArch = map[string]string{
	"amd64": "x86_64",
	"arm64": "arm64",
}

// LitestreamDownload describes the pinned Linux release asset.
func LitestreamDownload(goarch string) (hostbin.Download, error) {
	arch, ok := litestreamArch[goarch]
	if !ok {
		return hostbin.Download{}, fmt.Errorf("unsupported architecture %q for Litestream (supported: amd64, arm64)", goarch)
	}
	asset := fmt.Sprintf("litestream-%s-linux-%s.tar.gz", LitestreamVersion, arch)
	return hostbin.Download{
		Binary: "litestream",
		URL:    fmt.Sprintf("https://github.com/%s/releases/download/v%s/%s", litestreamRepo, LitestreamVersion, asset),
		Member: "litestream",
		SHA256: litestreamSHA256[goarch],
	}, nil
}

var semverRE = regexp.MustCompile(`\bv?(\d+\.\d+\.\d+)\b`)

// ParseVersionOutput extracts a semver from `litestream version` output.
func ParseVersionOutput(out string) string {
	m := semverRE.FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	return m[1]
}

// SupportedLitestreamVersion reports whether the output describes a 0.5.x
// release.
func SupportedLitestreamVersion(out string) bool {
	return strings.HasPrefix(ParseVersionOutput(out), "0.5.")
}

// PackageCommand is one distro package-manager invocation.
type PackageCommand struct {
	Name string
	Args []string
}

// SQLitePackageCommand returns the install command for sqlite3 on pm.
func SQLitePackageCommand(pm string) (PackageCommand, bool) {
	switch pm {
	case "apt-get":
		return PackageCommand{pm, []string{"install", "-y", "--no-install-recommends", "sqlite3"}}, true
	case "dnf":
		return PackageCommand{pm, []string{"install", "-y", "sqlite"}}, true
	case "yum":
		return PackageCommand{pm, []string{"install", "-y", "sqlite"}}, true
	case "pacman":
		return PackageCommand{pm, []string{"-S", "--noconfirm", "--needed", "sqlite"}}, true
	case "zypper":
		return PackageCommand{pm, []string{"install", "-y", "sqlite3"}}, true
	case "apk":
		return PackageCommand{pm, []string{"add", "sqlite"}}, true
	default:
		return PackageCommand{}, false
	}
}

// PackageManagers lists supported managers in detection order.
var PackageManagers = []string{"apt-get", "dnf", "yum", "pacman", "zypper", "apk"}

// DetectPackageManager finds the first supported package manager on PATH.
func DetectPackageManager() (string, bool) {
	for _, pm := range PackageManagers {
		if _, err := exec.LookPath(pm); err == nil {
			return pm, true
		}
	}
	return "", false
}

// PrivilegePrefixFor decides how to run a privileged package install without
// an interactive prompt.
func PrivilegePrefixFor(root, hasSudo, sudoWorks bool) ([]string, error) {
	if root {
		return nil, nil
	}
	if !hasSudo {
		return nil, fmt.Errorf("installing sqlite3 requires root, and sudo is not installed — run this command as root")
	}
	if !sudoWorks {
		return nil, fmt.Errorf("installing sqlite3 requires root, and passwordless sudo is unavailable — run \"sudo pg sqlite install\"")
	}
	return []string{"sudo", "-n"}, nil
}

// DetectPrivilege probes root and passwordless sudo.
func DetectPrivilege() ([]string, error) {
	root := os.Geteuid() == 0
	_, sudoErr := exec.LookPath("sudo")
	hasSudo := sudoErr == nil
	sudoWorks := false
	if hasSudo {
		sudoWorks = exec.Command("sudo", "-n", "true").Run() == nil
	}
	return PrivilegePrefixFor(root, hasSudo, sudoWorks)
}

// LitestreamCandidates lists binary paths in resolution order, de-duplicated.
func LitestreamCandidates(preferred, recorded, pathBin, home string) []string {
	candidates := []string{preferred, recorded, pathBin, "/usr/local/bin/litestream"}
	if home != "" {
		candidates = append(candidates, filepath.Join(home, ".local", "bin", "litestream"))
	}
	seen := make(map[string]bool, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// ResolveLitestreamBinary finds a working 0.5.x binary, preferring an explicit
// --litestream-binary then the path recorded in pg.yaml.
func ResolveLitestreamBinary(preferred, recorded string) (string, string, error) {
	pathBin, _ := hostbin.Detect("litestream")
	home, _ := os.UserHomeDir()

	var unsupported []string
	for _, candidate := range LitestreamCandidates(preferred, recorded, pathBin, home) {
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		out, err := exec.Command(candidate, "version").Output()
		if err != nil {
			continue
		}
		version := ParseVersionOutput(string(out))
		if strings.HasPrefix(version, "0.5.") {
			return candidate, version, nil
		}
		unsupported = append(unsupported, fmt.Sprintf("%s (%s)", candidate, version))
	}
	if preferred != "" {
		return "", "", fmt.Errorf("litestream binary %s is not a supported 0.5.x release — run \"pg sqlite install --force-litestream\"", preferred)
	}
	msg := "no Litestream 0.5.x binary found — run \"pg sqlite install\""
	if len(unsupported) > 0 {
		msg += "; unsupported versions found: " + strings.Join(unsupported, ", ")
	}
	return "", "", fmt.Errorf("%s", msg)
}
