package zfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectDistroFile(t *testing.T) {
	cases := []struct {
		name    string
		content string
		family  DistroFamily
	}{
		{
			name: "debian 12",
			content: `PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
NAME="Debian GNU/Linux"
VERSION_ID="12"
ID=debian
`,
			family: FamilyDebian,
		},
		{
			name: "debian 13",
			content: `PRETTY_NAME="Debian GNU/Linux 13 (trixie)"
ID=debian
VERSION_ID="13"
`,
			family: FamilyDebian,
		},
		{
			name: "ubuntu 24.04",
			content: `PRETTY_NAME="Ubuntu 24.04.1 LTS"
ID=ubuntu
ID_LIKE=debian
VERSION_ID="24.04"
`,
			family: FamilyUbuntu,
		},
		{
			name: "ubuntu 26.04",
			content: `ID=ubuntu
ID_LIKE=debian
VERSION_ID="26.04"
`,
			family: FamilyUbuntu,
		},
		{
			name: "fedora 44",
			content: `NAME="Fedora Linux"
ID=fedora
VERSION_ID=44
`,
			family: FamilyFedora,
		},
		{
			name: "rocky 10",
			content: `NAME="Rocky Linux"
ID="rocky"
ID_LIKE="rhel centos fedora"
VERSION_ID="10.0"
`,
			family: FamilyRHEL,
		},
		{
			name: "linux mint",
			content: `NAME="Linux Mint"
ID=linuxmint
ID_LIKE="ubuntu debian"
VERSION_ID="22"
`,
			family: FamilyUbuntu,
		},
		{
			name: "arch",
			content: `NAME="Arch Linux"
ID=arch
`,
			family: FamilyUnknown,
		},
		{
			name: "comments and export",
			content: `# leading comment
export ID=debian
VERSION_ID='12'
`,
			family: FamilyDebian,
		},
	}
	dir := t.TempDir()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-"))
			if err := os.WriteFile(path, []byte(tc.content), 0644); err != nil {
				t.Fatal(err)
			}
			distro, err := DetectDistroFile(path)
			if err != nil {
				t.Fatalf("DetectDistroFile: %v", err)
			}
			if distro.Family != tc.family {
				t.Errorf("family = %s, want %s", distro.Family, tc.family)
			}
			if distro.Display() == "" {
				t.Errorf("expected a display name, got %+v", distro)
			}
		})
	}
}

func TestClassifyDistroExactIDWins(t *testing.T) {
	if got := ClassifyDistro("ubuntu", "debian"); got != FamilyUbuntu {
		t.Errorf("ubuntu with debian ID_LIKE = %s, want ubuntu", got)
	}
	if got := ClassifyDistro("rocky", "rhel centos"); got != FamilyRHEL {
		t.Errorf("rocky = %s, want rhel", got)
	}
}

func TestUnsupportedMessage(t *testing.T) {
	d := Distro{ID: "alpine", PrettyName: "Alpine Linux", Family: FamilyUnknown}
	msg := d.UnsupportedMessage()
	for _, want := range []string{"unsupported", "Debian 12/13", "Alpine:", "Arch:", "openSUSE:"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
}
