package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateHost(t *testing.T) {
	valid := []string{
		"docker.m.daocloud.io",
		"mirror.example.com:5000",
		"localhost",
		"127.0.0.1",
		"127.0.0.1:5000",
		"10.0.0.10",
		"my-registry.example.com",
		"a",
		"[fd00::1]",
		"[fd00::1]:5000",
	}
	for _, host := range valid {
		if err := ValidateHost(host); err != nil {
			t.Errorf("ValidateHost(%q) = %v, want nil", host, err)
		}
	}

	invalid := []string{
		"",
		"https://mirror.example.com",
		"http://mirror.example.com:5000",
		"bad host",
		"bad\thost",
		"mirror.example.com:0",
		":5000",
		"mirror.example.com:65536",
		"mirror.example.com:abc",
		"mirror.example.com:",
		"fd00::1",
		"[fd00::1",
		"[fd00::1]x",
		"[not-ipv6]",
		"[fe80::1%eth0]",
		"mirror.example.com/path",
		"user@mirror.example.com",
		"mirror.example.com?x",
		"mirror.example.com#x",
		"mirror.example.com%x",
		"mirror\"example.com",
		"mirror\\example.com",
		"-bad.example.com",
		"bad-.example.com",
		"bad..example.com",
		"mirror.example.com.",
		strings.Repeat("a", 64) + ".com",
		strings.Repeat("a", 254),
		"例子.example.com",
	}
	for _, host := range invalid {
		if err := ValidateHost(host); err == nil {
			t.Errorf("ValidateHost(%q) = nil, want error", host)
		}
	}
}

func TestRenderSingleMatchesLegacyInstaller(t *testing.T) {
	got, err := Render(map[string]string{DefaultRegistry: "docker.m.daocloud.io"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := `[[registry]]
prefix = "docker.io"
location = "docker.io"

[[registry.mirror]]
location = "docker.m.daocloud.io"
`
	if string(got) != want {
		t.Fatalf("Render content mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderMultipleRegistriesInStableOrder(t *testing.T) {
	got, err := Render(map[string]string{
		"quay.io":   "quay.example.com:5000",
		"docker.io": "docker.m.daocloud.io",
		"ghcr.io":   "ghcr.example.com",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := `[[registry]]
prefix = "docker.io"
location = "docker.io"

[[registry.mirror]]
location = "docker.m.daocloud.io"

[[registry]]
prefix = "ghcr.io"
location = "ghcr.io"

[[registry.mirror]]
location = "ghcr.example.com"

[[registry]]
prefix = "quay.io"
location = "quay.io"

[[registry.mirror]]
location = "quay.example.com:5000"
`
	if string(got) != want {
		t.Fatalf("Render content mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderRejectsInvalidInput(t *testing.T) {
	if _, err := Render(map[string]string{"example.com": "mirror.example.com"}); err == nil {
		t.Fatal("Render accepted an unsupported registry")
	}
	if _, err := Render(map[string]string{DefaultRegistry: "https://mirror.example.com"}); err == nil {
		t.Fatal("Render accepted a URL")
	}
	if _, err := Render(nil); err == nil {
		t.Fatal("Render accepted an empty map")
	}
}

func TestParseEntries(t *testing.T) {
	data, err := Render(map[string]string{
		DefaultRegistry: "docker.m.daocloud.io",
		"ghcr.io":       "ghcr.example.com:5000",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	entries, err := ParseEntries(data)
	if err != nil {
		t.Fatalf("ParseEntries: %v", err)
	}
	if len(entries) != 2 || entries[DefaultRegistry] != "docker.m.daocloud.io" || entries["ghcr.io"] != "ghcr.example.com:5000" {
		t.Errorf("entries = %v", entries)
	}

	withComments := []byte(`# managed by pg
[[registry]]
prefix = "docker.io"
location = "docker.io"

# the mirror
[[registry.mirror]]
location = "docker.m.daocloud.io" # fallback
`)
	entries, err = ParseEntries(withComments)
	if err != nil {
		t.Fatalf("ParseEntries with comments: %v", err)
	}
	if entries[DefaultRegistry] != "docker.m.daocloud.io" {
		t.Errorf("entries = %v", entries)
	}

	empty, err := ParseEntries(nil)
	if err != nil || len(empty) != 0 {
		t.Errorf("ParseEntries(empty) = %v, %v", empty, err)
	}
}

func TestParseEntriesRejectsMalformedFiles(t *testing.T) {
	cases := map[string]string{
		"mirror before registry": `[[registry.mirror]]
location = "mirror.example.com"
`,
		"mirror without location": `[[registry]]
prefix = "docker.io"
location = "docker.io"

[[registry.mirror]]
`,
		"multiple mirrors": `[[registry]]
prefix = "docker.io"
location = "docker.io"

[[registry.mirror]]
location = "a.example.com"

[[registry.mirror]]
location = "b.example.com"
`,
		"unsupported registry": `[[registry]]
prefix = "example.com"
location = "example.com"

[[registry.mirror]]
location = "mirror.example.com"
`,
		"unterminated value": `[[registry]]
prefix = "docker.io"
location = "docker.io"

[[registry.mirror]]
location = "mirror.example.com
`,
		"escaped value": `[[registry]]
prefix = "docker.io"
location = "docker.io"

[[registry.mirror]]
location = "mirror\\example.com"
`,
	}
	for name, data := range cases {
		if _, err := ParseEntries([]byte(data)); err == nil {
			t.Errorf("%s: ParseEntries accepted malformed input", name)
		}
	}
}

func TestResolveTarget(t *testing.T) {
	home := "/home/alice"
	system := "/tmp/system.conf"
	legacy := "/tmp/legacy.conf"

	target, err := ResolveTarget(home, system, legacy, Privilege{Root: true})
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	if !target.System || target.Path != system || target.LegacyPath != legacy {
		t.Errorf("root target = %+v", target)
	}

	target, err = ResolveTarget(home, system, legacy, Privilege{Sudo: true})
	if err != nil {
		t.Fatalf("sudo: %v", err)
	}
	if !target.System || target.Path != system || target.LegacyPath != legacy {
		t.Errorf("sudo target = %+v", target)
	}

	target, err = ResolveTarget(home, system, legacy, Privilege{})
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	if target.System || target.Path != UserPath(home) || target.LegacyPath != UserLegacyPath(home) {
		t.Errorf("user target = %+v", target)
	}

	if _, err := ResolveTarget("", system, legacy, Privilege{}); err == nil {
		t.Fatal("ResolveTarget accepted an empty home")
	}
}

func TestSelectInvokingHome(t *testing.T) {
	if got := selectInvokingHome(0, "alice", "/home/alice", "/root"); got != "/home/alice" {
		t.Errorf("root with SUDO_USER = %q", got)
	}
	if got := selectInvokingHome(0, "", "", "/root"); got != "/root" {
		t.Errorf("root without SUDO_USER = %q", got)
	}
	if got := selectInvokingHome(0, "root", "/root", "/root"); got != "/root" {
		t.Errorf("root with SUDO_USER=root = %q", got)
	}
	if got := selectInvokingHome(1000, "alice", "/home/alice", "/home/bob"); got != "/home/bob" {
		t.Errorf("non-root = %q", got)
	}
}

func newTestManager(t *testing.T, priv Privilege) (*Manager, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	system := filepath.Join(dir, "etc", DropInName)
	legacy := filepath.Join(dir, "etc", LegacyDropInName)
	return &Manager{Priv: priv, Home: home, SystemPath: system, SystemLegacyPath: legacy}, home, system, legacy
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func mustRender(t *testing.T, mirrors map[string]string) []byte {
	t.Helper()
	data, err := Render(mirrors)
	if err != nil {
		t.Fatalf("Render(%v): %v", mirrors, err)
	}
	return data
}

func mustRenderString(t *testing.T, mirrors map[string]string) string {
	t.Helper()
	return string(mustRender(t, mirrors))
}

func readEntries(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	entries, err := ParseEntries(data)
	if err != nil {
		t.Fatalf("ParseEntries(%s): %v", path, err)
	}
	return entries
}

func TestManagerSetRootMigratesLegacyAndCleansUser(t *testing.T) {
	m, home, system, legacy := newTestManager(t, Privilege{Root: true})
	writeTestFile(t, legacy, mustRenderString(t, map[string]string{DefaultRegistry: "old.example.com"}))
	writeTestFile(t, UserLegacyPath(home), mustRenderString(t, map[string]string{DefaultRegistry: "user-old.example.com"}))

	res, err := m.Set(map[string]string{"ghcr.io": "ghcr.example.com"})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !res.System || res.Path != system {
		t.Fatalf("result = %+v", res)
	}
	if res.RemovedLegacyPath != legacy {
		t.Errorf("RemovedLegacyPath = %q", res.RemovedLegacyPath)
	}
	if len(res.RemovedUserMirrors) != 1 || res.RemovedUserMirrors[0] != DefaultRegistry {
		t.Errorf("RemovedUserMirrors = %v", res.RemovedUserMirrors)
	}

	entries := readEntries(t, system)
	if entries[DefaultRegistry] != "old.example.com" || entries["ghcr.io"] != "ghcr.example.com" {
		t.Errorf("system entries = %v", entries)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy file still exists: %v", err)
	}
	if _, err := os.Stat(UserLegacyPath(home)); !os.IsNotExist(err) {
		t.Errorf("user legacy file still exists: %v", err)
	}
}

func TestManagerSetUserRewritesAndPreservesOtherRegistries(t *testing.T) {
	m, home, _, _ := newTestManager(t, Privilege{})
	userPath := UserPath(home)

	res, err := m.Set(map[string]string{DefaultRegistry: "docker.example.com"})
	if err != nil {
		t.Fatalf("Set docker: %v", err)
	}
	if res.System || res.Path != userPath {
		t.Fatalf("result = %+v", res)
	}

	writeTestFile(t, userPath, "<!-- marker -->\n"+mustRenderString(t, map[string]string{DefaultRegistry: "docker.example.com"}))
	res, err = m.Set(map[string]string{DefaultRegistry: "docker.example.com"})
	if err != nil {
		t.Fatalf("Set docker again: %v", err)
	}
	if res.Previous[DefaultRegistry] != "docker.example.com" {
		t.Errorf("Previous = %v", res.Previous)
	}
	data, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(data), "marker") {
		t.Error("set did not rewrite the file")
	}

	if _, err := m.Set(map[string]string{"ghcr.io": "ghcr.example.com:5000"}); err != nil {
		t.Fatalf("Set ghcr: %v", err)
	}
	entries := readEntries(t, userPath)
	if len(entries) != 2 || entries[DefaultRegistry] != "docker.example.com" || entries["ghcr.io"] != "ghcr.example.com:5000" {
		t.Errorf("entries = %v", entries)
	}
}

func TestManagerSetUserReportsShadowedSystem(t *testing.T) {
	m, home, system, _ := newTestManager(t, Privilege{})
	writeTestFile(t, system, mustRenderString(t, map[string]string{
		DefaultRegistry: "system-docker.example.com",
		"ghcr.io":       "system-ghcr.example.com",
	}))

	res, err := m.Set(map[string]string{"ghcr.io": "user-ghcr.example.com"})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	source, ok := res.Shadowed["ghcr.io"]
	if !ok || source.Host != "system-ghcr.example.com" || source.Path != system {
		t.Errorf("Shadowed = %+v", res.Shadowed)
	}
	if _, ok := res.Shadowed[DefaultRegistry]; ok {
		t.Errorf("unexpected shadow for docker.io: %+v", res.Shadowed)
	}
	entries := readEntries(t, UserPath(home))
	if len(entries) != 1 || entries["ghcr.io"] != "user-ghcr.example.com" {
		t.Errorf("user entries = %v", entries)
	}
}

func TestManagerSetSystemCleansOnlyConflictingUserEntries(t *testing.T) {
	m, home, system, _ := newTestManager(t, Privilege{Root: true})
	writeTestFile(t, system, mustRenderString(t, map[string]string{"ghcr.io": "system-ghcr.example.com"}))
	writeTestFile(t, UserPath(home), mustRenderString(t, map[string]string{
		DefaultRegistry: "user-docker.example.com",
		"ghcr.io":       "user-ghcr.example.com",
		"quay.io":       "user-quay.example.com",
	}))

	res, err := m.Set(map[string]string{DefaultRegistry: "system-docker.example.com"})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if res.RewrittenUserPath != UserPath(home) {
		t.Errorf("RewrittenUserPath = %q", res.RewrittenUserPath)
	}
	joined := strings.Join(res.RemovedUserMirrors, ",")
	if joined != "docker.io,ghcr.io" {
		t.Errorf("RemovedUserMirrors = %v", res.RemovedUserMirrors)
	}
	entries := readEntries(t, UserPath(home))
	if len(entries) != 1 || entries["quay.io"] != "user-quay.example.com" {
		t.Errorf("user entries = %v", entries)
	}
	systemEntries := readEntries(t, system)
	if systemEntries[DefaultRegistry] != "system-docker.example.com" || systemEntries["ghcr.io"] != "system-ghcr.example.com" {
		t.Errorf("system entries = %v", systemEntries)
	}
}

func TestManagerSetReplacesUnparseableUserFile(t *testing.T) {
	m, home, _, _ := newTestManager(t, Privilege{})
	writeTestFile(t, UserPath(home), "not a mirror config\n")

	res, err := m.Set(map[string]string{"quay.io": "quay.example.com"})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !res.ReplacedUnparseable {
		t.Error("ReplacedUnparseable = false")
	}
	entries := readEntries(t, UserPath(home))
	if entries["quay.io"] != "quay.example.com" {
		t.Errorf("entries = %v", entries)
	}
}

func TestManagerShowMultiRegistryPrecedence(t *testing.T) {
	m, home, system, _ := newTestManager(t, Privilege{Root: true})
	writeTestFile(t, system, mustRenderString(t, map[string]string{
		DefaultRegistry: "system-docker.example.com",
		"ghcr.io":       "system-ghcr.example.com",
	}))
	writeTestFile(t, UserPath(home), mustRenderString(t, map[string]string{"ghcr.io": "user-ghcr.example.com"}))

	res, err := m.Show()
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	statuses := statusMap(res)
	if statuses[DefaultRegistry].Host != "system-docker.example.com" || statuses[DefaultRegistry].Scope != "system" {
		t.Errorf("docker status = %+v", statuses[DefaultRegistry])
	}
	if statuses["ghcr.io"].Host != "user-ghcr.example.com" || statuses["ghcr.io"].Scope != "user" {
		t.Errorf("ghcr status = %+v", statuses["ghcr.io"])
	}
	if statuses["ghcr.io"].Shadowed == nil || statuses["ghcr.io"].Shadowed.Host != "system-ghcr.example.com" {
		t.Errorf("ghcr shadow = %+v", statuses["ghcr.io"].Shadowed)
	}
	if statuses["quay.io"].Host != "" {
		t.Errorf("quay status = %+v", statuses["quay.io"])
	}
	if res.Uncertain {
		t.Error("Uncertain = true")
	}
}

func TestManagerShowLegacyAndNewFiles(t *testing.T) {
	m, _, system, legacy := newTestManager(t, Privilege{Root: true})
	writeTestFile(t, legacy, mustRenderString(t, map[string]string{DefaultRegistry: "legacy-docker.example.com"}))
	writeTestFile(t, system, mustRenderString(t, map[string]string{"ghcr.io": "ghcr.example.com"}))

	res, err := m.Show()
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	statuses := statusMap(res)
	if statuses[DefaultRegistry].Host != "legacy-docker.example.com" || !statuses[DefaultRegistry].Legacy {
		t.Errorf("docker status = %+v", statuses[DefaultRegistry])
	}
	if statuses["ghcr.io"].Host != "ghcr.example.com" || statuses["ghcr.io"].Legacy {
		t.Errorf("ghcr status = %+v", statuses["ghcr.io"])
	}
	if !res.System.LegacyExists {
		t.Error("LegacyExists = false")
	}
}

func TestManagerShowUnparseableUser(t *testing.T) {
	m, home, system, _ := newTestManager(t, Privilege{Root: true})
	writeTestFile(t, system, mustRenderString(t, map[string]string{DefaultRegistry: "system.example.com"}))
	writeTestFile(t, UserPath(home), "not a mirror config\n")

	res, err := m.Show()
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !res.Uncertain {
		t.Error("Uncertain = false")
	}
	if res.User.Err == nil {
		t.Error("user parse error not recorded")
	}
}

func TestManagerCleanRegistryWithoutPrivilegeLeavesStateUntouched(t *testing.T) {
	m, home, system, _ := newTestManager(t, Privilege{})
	systemContent := mustRenderString(t, map[string]string{
		DefaultRegistry: "system-docker.example.com",
		"ghcr.io":       "system-ghcr.example.com",
	})
	userContent := mustRenderString(t, map[string]string{
		DefaultRegistry: "user-docker.example.com",
		"ghcr.io":       "user-ghcr.example.com",
	})
	writeTestFile(t, system, systemContent)
	writeTestFile(t, UserPath(home), userContent)

	_, err := m.Clean([]string{"ghcr.io"})
	if err == nil || !strings.Contains(err.Error(), "requires root") {
		t.Fatalf("Clean error = %v", err)
	}
	if data, readErr := os.ReadFile(system); readErr != nil || string(data) != systemContent {
		t.Errorf("system file changed: %v", readErr)
	}
	if data, readErr := os.ReadFile(UserPath(home)); readErr != nil || string(data) != userContent {
		t.Errorf("user file changed: %v", readErr)
	}
}

func TestManagerCleanRegistryRemovesFromBothScopes(t *testing.T) {
	m, home, system, legacy := newTestManager(t, Privilege{Root: true})
	writeTestFile(t, legacy, mustRenderString(t, map[string]string{DefaultRegistry: "legacy-docker.example.com"}))
	writeTestFile(t, system, mustRenderString(t, map[string]string{
		DefaultRegistry: "system-docker.example.com",
		"ghcr.io":       "system-ghcr.example.com",
	}))
	writeTestFile(t, UserPath(home), mustRenderString(t, map[string]string{
		DefaultRegistry: "user-docker.example.com",
		"ghcr.io":       "user-ghcr.example.com",
		"quay.io":       "user-quay.example.com",
	}))

	res, err := m.Clean([]string{"ghcr.io"})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if len(res.RemovedRegistries) != 1 || res.RemovedRegistries[0] != "ghcr.io" {
		t.Errorf("RemovedRegistries = %v", res.RemovedRegistries)
	}
	systemEntries := readEntries(t, system)
	if len(systemEntries) != 1 || systemEntries[DefaultRegistry] != "system-docker.example.com" {
		t.Errorf("system entries = %v", systemEntries)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy file still exists: %v", err)
	}
	userEntries := readEntries(t, UserPath(home))
	if len(userEntries) != 2 || userEntries[DefaultRegistry] != "user-docker.example.com" || userEntries["quay.io"] != "user-quay.example.com" {
		t.Errorf("user entries = %v", userEntries)
	}
}

func TestManagerCleanRegistryRemovesLegacyOnly(t *testing.T) {
	m, _, system, legacy := newTestManager(t, Privilege{Root: true})
	writeTestFile(t, legacy, mustRenderString(t, map[string]string{DefaultRegistry: "legacy-docker.example.com"}))

	res, err := m.Clean([]string{DefaultRegistry})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if len(res.RemovedRegistries) != 1 || res.RemovedRegistries[0] != DefaultRegistry {
		t.Errorf("RemovedRegistries = %v", res.RemovedRegistries)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy file still exists: %v", err)
	}
	if _, err := os.Stat(system); !os.IsNotExist(err) {
		t.Errorf("system file exists: %v", err)
	}
}

func TestManagerCleanAllRemovesBothScopes(t *testing.T) {
	m, home, system, legacy := newTestManager(t, Privilege{Root: true})
	writeTestFile(t, system, mustRenderString(t, map[string]string{DefaultRegistry: "system.example.com"}))
	writeTestFile(t, legacy, mustRenderString(t, map[string]string{DefaultRegistry: "legacy.example.com"}))
	writeTestFile(t, UserPath(home), mustRenderString(t, map[string]string{"ghcr.io": "user.example.com"}))
	writeTestFile(t, UserLegacyPath(home), mustRenderString(t, map[string]string{DefaultRegistry: "user-legacy.example.com"}))

	res, err := m.Clean(nil)
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if len(res.Removed) != 4 {
		t.Errorf("Removed = %v", res.Removed)
	}
	for _, path := range []string{system, legacy, UserPath(home), UserLegacyPath(home)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s still exists: %v", path, err)
		}
	}
}

func TestManagerCleanUserOnlyWithoutPrivilege(t *testing.T) {
	m, home, _, _ := newTestManager(t, Privilege{})
	writeTestFile(t, UserPath(home), mustRenderString(t, map[string]string{DefaultRegistry: "user.example.com"}))

	res, err := m.Clean(nil)
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if len(res.Removed) != 1 || res.Removed[0] != UserPath(home) {
		t.Errorf("Removed = %v", res.Removed)
	}
}

func TestManagerCleanAllWithoutPrivilegeLeavesStateUntouched(t *testing.T) {
	m, home, system, _ := newTestManager(t, Privilege{})
	writeTestFile(t, system, mustRenderString(t, map[string]string{DefaultRegistry: "system.example.com"}))
	writeTestFile(t, UserPath(home), mustRenderString(t, map[string]string{DefaultRegistry: "user.example.com"}))

	_, err := m.Clean(nil)
	if err == nil || !strings.Contains(err.Error(), "requires root") {
		t.Fatalf("Clean error = %v", err)
	}
	if _, err := os.Stat(system); err != nil {
		t.Errorf("system file removed: %v", err)
	}
	if _, err := os.Stat(UserPath(home)); err != nil {
		t.Errorf("user file removed: %v", err)
	}
}

func statusMap(res ShowResult) map[string]MirrorStatus {
	statuses := make(map[string]MirrorStatus)
	for _, status := range res.Mirrors {
		statuses[status.Registry] = status
	}
	return statuses
}
