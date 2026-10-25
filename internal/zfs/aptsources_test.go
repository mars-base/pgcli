package zfs

import (
	"strings"
	"testing"
)

func TestAPTArchiveURI(t *testing.T) {
	if !APTArchiveURI(FamilyDebian, "http://deb.debian.org/debian") {
		t.Error("deb.debian.org should match Debian")
	}
	if !APTArchiveURI(FamilyDebian, "http://security.debian.org/debian-security") {
		t.Error("security.debian.org should match Debian")
	}
	if APTArchiveURI(FamilyDebian, "https://apt.postgresql.org/pub/repos/apt") {
		t.Error("third-party URI must not match")
	}
	if !APTArchiveURI(FamilyUbuntu, "http://archive.ubuntu.com/ubuntu") {
		t.Error("archive.ubuntu.com should match Ubuntu")
	}
	if !APTArchiveURI(FamilyUbuntu, "http://ports.ubuntu.com/ubuntu-ports") {
		t.Error("ports.ubuntu.com should match Ubuntu")
	}
	if !APTArchiveURI(FamilyUbuntu, "mirror+file:/etc/apt/mirrors/ubuntu.list") {
		t.Error("mirror+file must match")
	}
	if APTArchiveURI(FamilyUbuntu, "http://deb.debian.org/debian") {
		t.Error("Debian URI must not match Ubuntu")
	}
}

func TestEnableComponentInDeb822(t *testing.T) {
	content := `Types: deb
URIs: http://deb.debian.org/debian
Suites: bookworm bookworm-updates
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg

Types: deb
URIs: https://apt.postgresql.org/pub/repos/apt
Suites: bookworm-pgdg
Components: main
`
	match := func(uri string) bool { return APTArchiveURI(FamilyDebian, uri) }
	updated, changed := EnableComponentInDeb822(content, "contrib", match)
	if !changed {
		t.Fatal("expected change")
	}
	if !strings.Contains(updated, "Components: main contrib") {
		t.Errorf("contrib not added:\n%s", updated)
	}
	if !strings.Contains(updated, "Components: main\n") {
		t.Errorf("third-party stanza must be untouched:\n%s", updated)
	}
	again, changedAgain := EnableComponentInDeb822(updated, "contrib", match)
	if changedAgain || again != updated {
		t.Errorf("second pass is not idempotent:\n%s", again)
	}
}

func TestEnableComponentInDeb822WithoutComponents(t *testing.T) {
	content := "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: bookworm\n"
	updated, changed := EnableComponentInDeb822(content, "contrib", func(uri string) bool {
		return APTArchiveURI(FamilyDebian, uri)
	})
	if !changed {
		t.Fatal("expected change")
	}
	if !strings.Contains(updated, "Components: main contrib") {
		t.Errorf("expected Components line:\n%s", updated)
	}
}

func TestEnableComponentInList(t *testing.T) {
	content := `# Debian sources
deb http://deb.debian.org/debian bookworm main
deb [arch=amd64 signed-by=/usr/share/keyrings/x.gpg] http://security.debian.org/debian-security bookworm-security main
deb https://apt.postgresql.org/pub/repos/apt bookworm-pgdg main
deb-src http://deb.debian.org/debian bookworm main
`
	match := func(uri string) bool { return APTArchiveURI(FamilyDebian, uri) }
	updated, changed := EnableComponentInList(content, "contrib", match)
	if !changed {
		t.Fatal("expected change")
	}
	for _, want := range []string{
		"deb http://deb.debian.org/debian bookworm main contrib",
		"bookworm-security main contrib",
		"deb-src http://deb.debian.org/debian bookworm main contrib",
		"deb https://apt.postgresql.org/pub/repos/apt bookworm-pgdg main",
		"# Debian sources",
		"[arch=amd64 signed-by=/usr/share/keyrings/x.gpg]",
	} {
		if !strings.Contains(updated, want) {
			t.Errorf("missing %q:\n%s", want, updated)
		}
	}
	again, changedAgain := EnableComponentInList(updated, "contrib", match)
	if changedAgain || again != updated {
		t.Errorf("second pass is not idempotent:\n%s", again)
	}
}

func TestEnableComponentInListAddsMain(t *testing.T) {
	content := "deb http://deb.debian.org/debian bookworm\n"
	updated, changed := EnableComponentInList(content, "contrib", func(uri string) bool {
		return APTArchiveURI(FamilyDebian, uri)
	})
	if !changed {
		t.Fatal("expected change")
	}
	if !strings.Contains(updated, "bookworm main contrib") {
		t.Errorf("expected main contrib, got %q", updated)
	}
}

func TestEnableComponentUbuntuUniverse(t *testing.T) {
	content := "deb http://archive.ubuntu.com/ubuntu noble main restricted\n"
	updated, changed := EnableComponentInList(content, "universe", func(uri string) bool {
		return APTArchiveURI(FamilyUbuntu, uri)
	})
	if !changed || !strings.Contains(updated, "main restricted universe") {
		t.Errorf("universe not added: %q", updated)
	}
}

func TestTokenizeListLineKeepsBracket(t *testing.T) {
	tokens := tokenizeListLine("deb [arch=amd64 signed-by=/a/b] http://x/y bookworm main")
	want := []string{"deb", "[arch=amd64 signed-by=/a/b]", "http://x/y", "bookworm", "main"}
	if strings.Join(tokens, "|") != strings.Join(want, "|") {
		t.Errorf("tokens = %v", tokens)
	}
}

func TestAPTComponent(t *testing.T) {
	if got := APTComponent(FamilyDebian); got != "contrib" {
		t.Errorf("Debian component = %q", got)
	}
	if got := APTComponent(FamilyUbuntu); got != "universe" {
		t.Errorf("Ubuntu component = %q", got)
	}
	if got := APTComponent(FamilyFedora); got != "" {
		t.Errorf("Fedora component = %q", got)
	}
}
