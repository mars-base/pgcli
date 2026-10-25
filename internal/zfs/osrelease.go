package zfs

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"strings"
)

// DistroFamily identifies the package-management family of a Linux distro.
type DistroFamily int

const (
	FamilyUnknown DistroFamily = iota
	FamilyDebian
	FamilyUbuntu
	FamilyFedora
	FamilyRHEL
)

func (f DistroFamily) String() string {
	switch f {
	case FamilyDebian:
		return "debian"
	case FamilyUbuntu:
		return "ubuntu"
	case FamilyFedora:
		return "fedora"
	case FamilyRHEL:
		return "rhel"
	default:
		return "unknown"
	}
}

// Distro is the subset of /etc/os-release that drives installation.
type Distro struct {
	ID         string
	IDLike     string
	VersionID  string
	PrettyName string
	Family     DistroFamily
	OSRelease  string
}

// Supported reports whether `pg zfs install` knows how to install on this
// distro family.
func (d Distro) Supported() bool {
	return d.Family != FamilyUnknown
}

// Display returns the pretty name, falling back to ID + version.
func (d Distro) Display() string {
	if d.PrettyName != "" {
		return d.PrettyName
	}
	if d.VersionID != "" {
		return d.ID + " " + d.VersionID
	}
	return d.ID
}

// DetectDistro reads /etc/os-release.
func DetectDistro() (Distro, error) {
	return DetectDistroFile("/etc/os-release")
}

// DetectDistroFile reads an os-release file at an arbitrary path (tests).
func DetectDistroFile(path string) (Distro, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Distro{}, fmt.Errorf("reading %s: %w", path, err)
	}
	d := ParseOSRelease(string(data))
	d.OSRelease = path
	d.Family = ClassifyDistro(d.ID, d.IDLike)
	return d, nil
}

// ParseOSRelease parses the shell-like key=value format, ignoring comments and
// handling single/double quotes.
func ParseOSRelease(data string) Distro {
	values := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		values[key] = value
	}
	return Distro{
		ID:         strings.ToLower(values["ID"]),
		IDLike:     strings.ToLower(values["ID_LIKE"]),
		VersionID:  values["VERSION_ID"],
		PrettyName: values["PRETTY_NAME"],
	}
}

// ClassifyDistro maps ID / ID_LIKE to a package family. The exact ID wins;
// derivatives inherit from ID_LIKE. Ubuntu is checked before Debian because
// Ubuntu's ID_LIKE is "debian".
func ClassifyDistro(id, idLike string) DistroFamily {
	switch strings.ToLower(id) {
	case "debian":
		return FamilyDebian
	case "ubuntu":
		return FamilyUbuntu
	case "fedora":
		return FamilyFedora
	case "rhel", "centos", "rocky", "almalinux", "ol", "cloudlinux":
		return FamilyRHEL
	}
	fields := strings.Fields(strings.ToLower(idLike))
	for _, family := range []struct {
		names []string
		fam   DistroFamily
	}{
		{[]string{"ubuntu"}, FamilyUbuntu},
		{[]string{"debian"}, FamilyDebian},
		{[]string{"rhel", "centos"}, FamilyRHEL},
		{[]string{"fedora"}, FamilyFedora},
	} {
		for _, name := range family.names {
			if slices.Contains(fields, name) {
				return family.fam
			}
		}
	}
	return FamilyUnknown
}

// PackageManager returns the command used to install packages.
func (d Distro) PackageManager() string {
	switch d.Family {
	case FamilyDebian, FamilyUbuntu:
		return "apt-get"
	case FamilyFedora, FamilyRHEL:
		return "dnf"
	default:
		return ""
	}
}

// UnsupportedMessage returns actionable manual instructions for distros the
// installer does not handle.
func (d Distro) UnsupportedMessage() string {
	var b strings.Builder
	fmt.Fprintf(&b, "unsupported Linux distribution %q (ID=%s, ID_LIKE=%s)\n", d.Display(), d.ID, d.IDLike)
	b.WriteString("supported systems: Debian 12/13, Ubuntu 24.04/26.04, Fedora 44, Rocky Linux 10\n")
	b.WriteString("manual install hints:\n")
	b.WriteString("  Alpine:   apk add zfs zfs-lts\n")
	b.WriteString("  Arch:     use the archzfs repository (zfs-linux)\n")
	b.WriteString("  openSUSE: use the filesystems repository (zfs, zfs-kmp-default)\n")
	b.WriteString("after installing, run \"pg zfs status\" to verify")
	return b.String()
}
