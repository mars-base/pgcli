// Package registry manages the podman drop-in configuration that routes
// container registry pulls through mirrors.
package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/netip"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	DropInName       = "99-pgcli-registry-mirror.conf"
	LegacyDropInName = "99-pgcli-docker-mirror.conf"
	SystemDir        = "/etc/containers/registries.conf.d"
	SystemPath       = SystemDir + "/" + DropInName
	SystemLegacyPath = SystemDir + "/" + LegacyDropInName
	DefaultRegistry  = "docker.io"
)

var supportedRegistries = []string{DefaultRegistry, "ghcr.io", "quay.io"}

// ErrNoHome is returned when the invoking user's home directory is unknown.
var ErrNoHome = errors.New("cannot determine the invoking user's home directory")

// Registries returns the registries that pg mirror can configure.
func Registries() []string {
	return append([]string(nil), supportedRegistries...)
}

// IsSupportedRegistry reports whether registry can be configured by pg mirror.
func IsSupportedRegistry(registry string) bool {
	for _, r := range supportedRegistries {
		if r == registry {
			return true
		}
	}
	return false
}

// Privilege describes how the current process can write system-level files.
type Privilege struct {
	Root bool
	Sudo bool
}

// CanPrivilege reports whether system-level files can be written.
func (p Privilege) CanPrivilege() bool { return p.Root || p.Sudo }

// DetectPrivilege probes root and passwordless sudo. It never prompts.
func DetectPrivilege() Privilege {
	p := Privilege{Root: os.Geteuid() == 0}
	if p.Root {
		return p
	}
	if _, err := exec.LookPath("sudo"); err != nil {
		return p
	}
	if err := exec.Command("sudo", "-n", "true").Run(); err == nil {
		p.Sudo = true
	}
	return p
}

// InvokingHome returns the home directory of the account that invoked pg.
// Under sudo this is the original user's home, not /root.
func InvokingHome() string {
	processHome, _ := os.UserHomeDir()
	if os.Geteuid() != 0 {
		return processHome
	}
	name := os.Getenv("SUDO_USER")
	if name == "" || name == "root" {
		return processHome
	}
	u, err := user.Lookup(name)
	if err != nil {
		return processHome
	}
	return selectInvokingHome(0, name, u.HomeDir, processHome)
}

func selectInvokingHome(euid int, sudoUser, sudoHome, processHome string) string {
	if euid == 0 && sudoUser != "" && sudoUser != "root" && sudoHome != "" {
		return sudoHome
	}
	return processHome
}

// UserPath returns the per-user drop-in path for home.
func UserPath(home string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "containers", "registries.conf.d", DropInName)
}

// UserLegacyPath returns the old per-user Docker Hub drop-in path for home.
func UserLegacyPath(home string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "containers", "registries.conf.d", LegacyDropInName)
}

// Target is the file selected for a mirror configuration change.
type Target struct {
	Path       string
	LegacyPath string
	System     bool
}

// ResolveTarget selects the system path when privileged writes are available,
// otherwise the invoking user's home path.
func ResolveTarget(home, systemPath, systemLegacyPath string, p Privilege) (Target, error) {
	if p.CanPrivilege() {
		if systemPath == "" {
			systemPath = SystemPath
		}
		if systemLegacyPath == "" {
			systemLegacyPath = SystemLegacyPath
		}
		return Target{Path: systemPath, LegacyPath: systemLegacyPath, System: true}, nil
	}
	path := UserPath(home)
	legacyPath := UserLegacyPath(home)
	if path == "" {
		return Target{}, ErrNoHome
	}
	return Target{Path: path, LegacyPath: legacyPath}, nil
}

// ValidateHost validates a mirror address in HOST[:PORT] or [IPv6][:PORT] form.
// It does not resolve DNS or probe reachability.
func ValidateHost(host string) error {
	if host == "" {
		return errors.New("mirror host is required: expected HOST or HOST:PORT (for example docker.m.daocloud.io)")
	}
	if strings.Contains(host, "://") {
		return fmt.Errorf("mirror host must not be a URL (got %q): use HOST or HOST:PORT", host)
	}
	for _, r := range host {
		if r <= ' ' || r == 0x7f {
			return fmt.Errorf("invalid mirror host %q: whitespace and control characters are not allowed", host)
		}
		if r == '"' || r == '\\' {
			return fmt.Errorf("invalid mirror host %q: quotes and backslashes are not allowed", host)
		}
		switch r {
		case '/', '@', '?', '#', '%':
			return fmt.Errorf("invalid mirror host %q: character %q is not allowed", host, r)
		}
	}

	if strings.HasPrefix(host, "[") {
		end := strings.IndexByte(host, ']')
		if end < 0 {
			return fmt.Errorf("invalid mirror host %q: missing closing bracket", host)
		}
		addr, err := netip.ParseAddr(host[1:end])
		if err != nil || !addr.Is6() || addr.Zone() != "" {
			return fmt.Errorf("invalid mirror host %q: expected a bracketed IPv6 address", host)
		}
		rest := host[end+1:]
		if rest == "" {
			return nil
		}
		if !strings.HasPrefix(rest, ":") {
			return fmt.Errorf("invalid mirror host %q: unexpected text after the IPv6 address", host)
		}
		return validatePort(host, rest[1:])
	}

	if strings.Count(host, ":") > 1 {
		return fmt.Errorf("invalid mirror host %q: IPv6 addresses must be bracketed, for example [fd00::1]:5000", host)
	}

	hostPart := host
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		hostPart = host[:i]
		if err := validatePort(host, host[i+1:]); err != nil {
			return err
		}
	}
	return validateHostPart(host, hostPart)
}

func validatePort(host, port string) error {
	if port == "" {
		return fmt.Errorf("invalid mirror host %q: port must be an integer between 1 and 65535", host)
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return fmt.Errorf("invalid mirror host %q: port must be an integer between 1 and 65535", host)
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid mirror host %q: port must be an integer between 1 and 65535", host)
	}
	return nil
}

func validateHostPart(full, host string) error {
	if host == "" {
		return fmt.Errorf("invalid mirror host %q: expected a hostname, IPv4 address, or bracketed IPv6 address", full)
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Is6() {
			return fmt.Errorf("invalid mirror host %q: IPv6 addresses must be bracketed, for example [fd00::1]:5000", full)
		}
		return nil
	}
	if len(host) > 253 {
		return fmt.Errorf("invalid mirror host %q: hostname is too long", full)
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 {
			return fmt.Errorf("invalid mirror host %q: hostname labels must be 1-63 characters", full)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("invalid mirror host %q: hostname labels must not start or end with a hyphen", full)
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return fmt.Errorf("invalid mirror host %q: expected a hostname, IPv4 address, or bracketed IPv6 address", full)
		}
	}
	return nil
}

// Render returns the podman drop-in content for mirrors. Registries are always
// written in a stable order so repeated set operations produce the same file.
func Render(mirrors map[string]string) ([]byte, error) {
	if len(mirrors) == 0 {
		return nil, errors.New("no mirrors configured")
	}
	for registry := range mirrors {
		if !IsSupportedRegistry(registry) {
			return nil, fmt.Errorf("unsupported registry %q: supported registries are %s", registry, strings.Join(supportedRegistries, ", "))
		}
	}

	var b strings.Builder
	first := true
	for _, registry := range supportedRegistries {
		host, ok := mirrors[registry]
		if !ok {
			continue
		}
		if err := ValidateHost(host); err != nil {
			return nil, fmt.Errorf("%s: %w", registry, err)
		}
		if !first {
			b.WriteByte('\n')
		}
		first = false
		fmt.Fprintf(&b, "[[registry]]\nprefix = %q\nlocation = %q\n\n[[registry.mirror]]\nlocation = %q\n", registry, registry, host)
	}
	return []byte(b.String()), nil
}

// ParseEntries extracts the registry mirrors from a pgcli-managed drop-in.
// It intentionally supports only the simple format written by Render.
func ParseEntries(data []byte) (map[string]string, error) {
	entries := make(map[string]string)
	table := ""
	currentRegistry := ""
	mirrorLocation := false
	sawContent := false
	finishTable := func() error {
		if table == "mirror" && !mirrorLocation {
			return errors.New("[[registry.mirror]] has no location")
		}
		return nil
	}

	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(stripComment(raw))
		if line == "" {
			continue
		}
		sawContent = true
		if strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]") {
			if err := finishTable(); err != nil {
				return nil, fmt.Errorf("line %d: %w", i+1, err)
			}
			name := strings.TrimSpace(line[2 : len(line)-2])
			switch name {
			case "registry":
				table = "registry"
				currentRegistry = ""
			case "registry.mirror":
				if table != "registry" || currentRegistry == "" {
					return nil, fmt.Errorf("line %d: [[registry.mirror]] requires a preceding [[registry]] with a prefix", i+1)
				}
				if _, exists := entries[currentRegistry]; exists {
					return nil, fmt.Errorf("line %d: multiple mirrors for %s are not supported", i+1, currentRegistry)
				}
				table = "mirror"
				mirrorLocation = false
			default:
				table = "other"
				currentRegistry = ""
			}
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if err := finishTable(); err != nil {
				return nil, fmt.Errorf("line %d: %w", i+1, err)
			}
			table = "other"
			currentRegistry = ""
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch {
		case table == "registry" && key == "prefix":
			registry, err := parseQuotedValue(value)
			if err != nil {
				return nil, fmt.Errorf("line %d: prefix: %w", i+1, err)
			}
			if !IsSupportedRegistry(registry) {
				return nil, fmt.Errorf("line %d: unsupported registry %q", i+1, registry)
			}
			currentRegistry = registry
		case table == "mirror" && key == "location":
			if mirrorLocation {
				return nil, fmt.Errorf("line %d: duplicate location in [[registry.mirror]]", i+1)
			}
			host, err := parseQuotedValue(value)
			if err != nil {
				return nil, fmt.Errorf("line %d: location: %w", i+1, err)
			}
			if err := ValidateHost(host); err != nil {
				return nil, fmt.Errorf("line %d: %w", i+1, err)
			}
			entries[currentRegistry] = host
			mirrorLocation = true
		}
	}
	if err := finishTable(); err != nil {
		return nil, err
	}
	if len(entries) == 0 && sawContent {
		return nil, errors.New("no registry mirror entries found")
	}
	return entries, nil
}

func parseQuotedValue(value string) (string, error) {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", errors.New("expected a quoted string")
	}
	inner := value[1 : len(value)-1]
	if inner == "" {
		return "", errors.New("empty value")
	}
	if strings.ContainsAny(inner, "\"\\") {
		return "", errors.New("quotes and backslashes are not allowed")
	}
	return inner, nil
}

func stripComment(line string) string {
	inQuote := false
	escaped := false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if inQuote && r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			inQuote = !inQuote
			continue
		}
		if r == '#' && !inQuote {
			return line[:i]
		}
	}
	return line
}

// MirrorSource identifies the file that supplied a mirror value.
type MirrorSource struct {
	Host   string
	Path   string
	Legacy bool
}

// ScopeState describes the managed drop-ins for one scope (system or user).
type ScopeState struct {
	Scope        string
	Path         string
	LegacyPath   string
	Entries      map[string]string
	Sources      map[string]MirrorSource
	NewExists    bool
	LegacyExists bool
	Err          error
}

// Exists reports whether either managed file exists in this scope.
func (s ScopeState) Exists() bool { return s.NewExists || s.LegacyExists }

// MirrorStatus reports the effective mirror for one registry.
type MirrorStatus struct {
	Registry string
	Host     string
	Scope    string
	Path     string
	Legacy   bool
	Shadowed *MirrorSource
}

// ShowResult reports the system and user files and the effective mirrors.
type ShowResult struct {
	System    ScopeState
	User      ScopeState
	Mirrors   []MirrorStatus
	Uncertain bool
}

// SetResult describes a completed set operation.
type SetResult struct {
	Path                string
	System              bool
	Mirrors             map[string]string
	Previous            map[string]string
	ReplacedUnparseable bool
	RemovedLegacyPath   string
	RemovedUserPaths    []string
	RewrittenUserPath   string
	RemovedUserMirrors  []string
	Shadowed            map[string]MirrorSource
	SystemUncertain     bool
}

// CleanResult reports what a clean operation removed or rewrote.
type CleanResult struct {
	Removed           []string
	RemovedRegistries []string
	Paths             []string
}

// Manager performs mirror changes for one invocation.
type Manager struct {
	Priv             Privilege
	Home             string
	SystemPath       string
	SystemLegacyPath string
}

// NewManager detects privileges and the invoking user's home.
func NewManager() *Manager {
	return &Manager{
		Priv:             DetectPrivilege(),
		Home:             InvokingHome(),
		SystemPath:       SystemPath,
		SystemLegacyPath: SystemLegacyPath,
	}
}

func (m *Manager) systemPath() string {
	if m.SystemPath != "" {
		return m.SystemPath
	}
	return SystemPath
}

func (m *Manager) systemLegacyPath() string {
	if m.SystemLegacyPath != "" {
		return m.SystemLegacyPath
	}
	return SystemLegacyPath
}

func (m *Manager) userPaths() (string, string, error) {
	if m.Home == "" {
		return "", "", ErrNoHome
	}
	return UserPath(m.Home), UserLegacyPath(m.Home), nil
}

// Set writes mirrors to the selected drop-in. Every call rewrites the file.
// Writing the system file removes user-level entries that would otherwise
// override it.
func (m *Manager) Set(mirrors map[string]string) (SetResult, error) {
	if len(mirrors) == 0 {
		return SetResult{}, errors.New("no mirror specified")
	}
	for registry, host := range mirrors {
		if !IsSupportedRegistry(registry) {
			return SetResult{}, fmt.Errorf("unsupported registry %q: supported registries are %s", registry, strings.Join(supportedRegistries, ", "))
		}
		if err := ValidateHost(host); err != nil {
			return SetResult{}, fmt.Errorf("%s: %w", registry, err)
		}
	}

	target, err := ResolveTarget(m.Home, m.systemPath(), m.systemLegacyPath(), m.Priv)
	if err != nil {
		return SetResult{}, err
	}
	scope := "user"
	if target.System {
		scope = "system"
	}
	state := m.readScope(scope, target.Path, target.LegacyPath, target.System)

	res := SetResult{
		Path:                target.Path,
		System:              target.System,
		Mirrors:             maps.Clone(mirrors),
		Previous:            make(map[string]string),
		ReplacedUnparseable: state.Err != nil,
	}
	entries := maps.Clone(state.Entries)
	if entries == nil {
		entries = make(map[string]string)
	}
	for registry, host := range mirrors {
		if old, ok := entries[registry]; ok {
			res.Previous[registry] = old
		}
		entries[registry] = host
	}

	data, err := Render(entries)
	if err != nil {
		return res, err
	}
	if err := m.writeFile(target.Path, data, target.System); err != nil {
		return res, err
	}
	if state.LegacyExists {
		if err := m.removeFile(target.LegacyPath, target.System); err != nil {
			return res, fmt.Errorf("wrote %s but could not remove legacy config %s: %w", target.Path, target.LegacyPath, err)
		}
		res.RemovedLegacyPath = target.LegacyPath
	}

	if target.System {
		if err := m.cleanUserOverrides(entries, &res); err != nil {
			return res, err
		}
	} else {
		system := m.readScope("system", m.systemPath(), m.systemLegacyPath(), true)
		res.SystemUncertain = system.Err != nil
		res.Shadowed = make(map[string]MirrorSource)
		for registry := range mirrors {
			if source, ok := system.Sources[registry]; ok {
				res.Shadowed[registry] = source
			}
		}
	}
	return res, nil
}

func (m *Manager) cleanUserOverrides(systemEntries map[string]string, res *SetResult) error {
	userPath, userLegacyPath, err := m.userPaths()
	if errors.Is(err, ErrNoHome) {
		return nil
	}
	if err != nil {
		return err
	}
	if userPath == res.Path {
		return nil
	}
	user := m.readScope("user", userPath, userLegacyPath, false)
	if !user.Exists() {
		return nil
	}

	if user.Err != nil {
		// The user file cannot be parsed, so its overrides cannot be merged
		// safely. Remove the managed files; set overwrites unparseable state.
		if user.NewExists {
			if err := m.removeFile(userPath, false); err != nil {
				return fmt.Errorf("system mirror config was written to %s, but the user-level config at %s could not be removed and may still override it: %w", res.Path, userPath, err)
			}
			res.RemovedUserPaths = append(res.RemovedUserPaths, userPath)
		}
		if user.LegacyExists {
			if err := m.removeFile(userLegacyPath, false); err != nil {
				return fmt.Errorf("system mirror config was written to %s, but the legacy user-level config at %s could not be removed and may still override it: %w", res.Path, userLegacyPath, err)
			}
			res.RemovedUserPaths = append(res.RemovedUserPaths, userLegacyPath)
		}
		return nil
	}

	var removed []string
	remaining := make(map[string]string)
	for registry, host := range user.Entries {
		if _, conflict := systemEntries[registry]; conflict {
			removed = append(removed, registry)
			continue
		}
		remaining[registry] = host
	}
	if len(removed) == 0 {
		return nil
	}
	res.RemovedUserMirrors = orderRegistries(removed)

	if len(remaining) == 0 {
		if user.NewExists {
			if err := m.removeFile(userPath, false); err != nil {
				return fmt.Errorf("system mirror config was written to %s, but the user-level config at %s could not be removed and may still override it: %w", res.Path, userPath, err)
			}
			res.RemovedUserPaths = append(res.RemovedUserPaths, userPath)
		}
		if user.LegacyExists {
			if err := m.removeFile(userLegacyPath, false); err != nil {
				return fmt.Errorf("system mirror config was written to %s, but the legacy user-level config at %s could not be removed and may still override it: %w", res.Path, userLegacyPath, err)
			}
			res.RemovedUserPaths = append(res.RemovedUserPaths, userLegacyPath)
		}
		return nil
	}

	data, err := Render(remaining)
	if err != nil {
		return err
	}
	if err := m.writeFile(userPath, data, false); err != nil {
		return fmt.Errorf("system mirror config was written to %s, but the user-level config at %s could not be rewritten and may still override it: %w", res.Path, userPath, err)
	}
	res.RewrittenUserPath = userPath
	if user.LegacyExists {
		if err := m.removeFile(userLegacyPath, false); err != nil {
			return fmt.Errorf("system mirror config was written to %s, but the legacy user-level config at %s could not be removed and may still override it: %w", res.Path, userLegacyPath, err)
		}
		res.RemovedUserPaths = append(res.RemovedUserPaths, userLegacyPath)
	}
	return nil
}

// Show reads both managed scopes and determines the effective mirrors.
func (m *Manager) Show() (ShowResult, error) {
	system := m.readScope("system", m.systemPath(), m.systemLegacyPath(), true)
	user := ScopeState{Scope: "user", Entries: map[string]string{}, Sources: map[string]MirrorSource{}}
	userPath, userLegacyPath, err := m.userPaths()
	if err == nil {
		user = m.readScope("user", userPath, userLegacyPath, false)
	} else if !errors.Is(err, ErrNoHome) {
		return ShowResult{}, err
	}

	res := ShowResult{
		System:    system,
		User:      user,
		Uncertain: system.Err != nil || user.Err != nil,
	}
	for _, registry := range supportedRegistries {
		status := MirrorStatus{Registry: registry}
		if source, ok := user.Sources[registry]; ok {
			status.Host = source.Host
			status.Scope = "user"
			status.Path = source.Path
			status.Legacy = source.Legacy
			if systemSource, ok := system.Sources[registry]; ok {
				shadowed := systemSource
				status.Shadowed = &shadowed
			}
		} else if source, ok := system.Sources[registry]; ok {
			status.Host = source.Host
			status.Scope = "system"
			status.Path = source.Path
			status.Legacy = source.Legacy
		}
		res.Mirrors = append(res.Mirrors, status)
	}
	return res, nil
}

// Clean removes all managed files when registries is empty. Otherwise it
// removes only the requested registry mirrors from both scopes.
func (m *Manager) Clean(registries []string) (CleanResult, error) {
	if len(registries) == 0 {
		return m.cleanAll()
	}
	wanted, err := normalizeRegistries(registries)
	if err != nil {
		return CleanResult{}, err
	}

	system := m.readScope("system", m.systemPath(), m.systemLegacyPath(), true)
	userPath, userLegacyPath, err := m.userPaths()
	user := ScopeState{Scope: "user", Entries: map[string]string{}, Sources: map[string]MirrorSource{}}
	if err == nil {
		user = m.readScope("user", userPath, userLegacyPath, false)
	} else if !errors.Is(err, ErrNoHome) {
		return CleanResult{}, err
	}

	if system.Exists() && system.Err != nil {
		return CleanResult{}, fmt.Errorf("cannot parse system-level registry mirror config: %w; run \"pg mirror clean\" to remove all managed config", system.Err)
	}
	if user.Exists() && user.Err != nil {
		return CleanResult{}, fmt.Errorf("cannot parse user-level registry mirror config: %w; run \"pg mirror clean\" to remove all managed config", user.Err)
	}

	systemMatches := intersectEntries(system.Entries, wanted)
	if len(systemMatches) > 0 && !m.Priv.CanPrivilege() {
		return CleanResult{}, fmt.Errorf("system-level registry mirror config for %s exists at %s; removing it requires root: run \"sudo pg mirror clean %s\"",
			strings.Join(systemMatches, ", "), m.systemPath(), strings.Join(systemMatches, " "))
	}

	res := CleanResult{}
	systemRemoved, err := m.removeFromScope(system, wanted, true, &res)
	if err != nil {
		return res, err
	}
	userRemoved, err := m.removeFromScope(user, wanted, false, &res)
	if err != nil {
		return res, err
	}
	res.RemovedRegistries = orderRegistries(append(systemRemoved, userRemoved...))
	return res, nil
}

func (m *Manager) cleanAll() (CleanResult, error) {
	res := CleanResult{}
	system := m.readScope("system", m.systemPath(), m.systemLegacyPath(), true)
	if system.Exists() && !m.Priv.CanPrivilege() {
		return res, fmt.Errorf("system-level registry mirror config exists at %s; removing it requires root: run \"sudo pg mirror clean\"", m.systemPath())
	}
	remove := func(path string, exists bool, system bool) error {
		if !exists {
			return nil
		}
		if err := m.removeFile(path, system); err != nil {
			return err
		}
		res.Removed = append(res.Removed, path)
		return nil
	}
	if err := remove(m.systemPath(), system.NewExists, true); err != nil {
		return res, err
	}
	if err := remove(m.systemLegacyPath(), system.LegacyExists, true); err != nil {
		return res, err
	}

	userPath, userLegacyPath, err := m.userPaths()
	if err != nil && !errors.Is(err, ErrNoHome) {
		return res, err
	}
	if err == nil {
		user := m.readScope("user", userPath, userLegacyPath, false)
		if err := remove(userPath, user.NewExists, false); err != nil {
			return res, err
		}
		if err := remove(userLegacyPath, user.LegacyExists, false); err != nil {
			return res, err
		}
	}
	return res, nil
}

func (m *Manager) removeFromScope(state ScopeState, wanted map[string]bool, system bool, res *CleanResult) ([]string, error) {
	if !state.Exists() {
		return nil, nil
	}
	var removed []string
	remaining := make(map[string]string)
	for registry, host := range state.Entries {
		if wanted[registry] {
			removed = append(removed, registry)
			continue
		}
		remaining[registry] = host
	}
	if len(removed) == 0 {
		return nil, nil
	}
	sort.Slice(removed, func(i, j int) bool {
		return registryIndex(removed[i]) < registryIndex(removed[j])
	})

	if len(remaining) == 0 {
		if state.NewExists {
			if err := m.removeFile(state.Path, system); err != nil {
				return nil, err
			}
			res.Paths = append(res.Paths, state.Path)
		}
		if state.LegacyExists {
			if err := m.removeFile(state.LegacyPath, system); err != nil {
				return nil, err
			}
			res.Paths = append(res.Paths, state.LegacyPath)
		}
		return removed, nil
	}

	data, err := Render(remaining)
	if err != nil {
		return nil, err
	}
	if err := m.writeFile(state.Path, data, system); err != nil {
		return nil, err
	}
	res.Paths = append(res.Paths, state.Path)
	if state.LegacyExists {
		if err := m.removeFile(state.LegacyPath, system); err != nil {
			return nil, err
		}
		res.Paths = append(res.Paths, state.LegacyPath)
	}
	return removed, nil
}

func (m *Manager) readScope(scope, path, legacyPath string, system bool) ScopeState {
	state := ScopeState{
		Scope:      scope,
		Path:       path,
		LegacyPath: legacyPath,
		Entries:    make(map[string]string),
		Sources:    make(map[string]MirrorSource),
	}
	read := func(file string, legacy bool) {
		data, err := m.readFile(file, system)
		if err != nil {
			if os.IsNotExist(err) {
				return
			}
			if legacy {
				state.LegacyExists = true
			} else {
				state.NewExists = true
			}
			state.Err = errors.Join(state.Err, fmt.Errorf("%s: %w", file, err))
			return
		}
		if legacy {
			state.LegacyExists = true
		} else {
			state.NewExists = true
		}
		entries, err := ParseEntries(data)
		if err != nil {
			state.Err = errors.Join(state.Err, fmt.Errorf("%s: %w", file, err))
			return
		}
		for registry, host := range entries {
			state.Entries[registry] = host
			state.Sources[registry] = MirrorSource{Host: host, Path: file, Legacy: legacy}
		}
	}
	read(legacyPath, true)
	read(path, false)
	return state
}

func (m *Manager) readFile(path string, system bool) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, nil
	}
	if system && m.Priv.Sudo {
		if out, sudoErr := runSudoOutput("cat", path); sudoErr == nil {
			return out, nil
		}
	}
	return nil, err
}

func (m *Manager) writeFile(path string, data []byte, system bool) error {
	if !system || m.Priv.Root {
		return writeFileDirect(path, data)
	}
	if m.Priv.Sudo {
		return writeFileSudo(path, data)
	}
	return fmt.Errorf("writing %s requires root", path)
}

func (m *Manager) removeFile(path string, system bool) error {
	if !system || m.Priv.Root {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing %s: %w", path, err)
		}
		return nil
	}
	if m.Priv.Sudo {
		if err := runSudo("rm", "-f", path); err != nil {
			return fmt.Errorf("removing %s: %w", path, err)
		}
		return nil
	}
	return fmt.Errorf("removing %s requires root", path)
}

func writeFileDirect(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".pgcli-mirror-*")
	if err != nil {
		return fmt.Errorf("creating temporary file in %s: %w", dir, err)
	}
	tmp := f.Name()
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}
	if _, err := f.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := f.Chmod(0o644); err != nil {
		cleanup()
		return fmt.Errorf("setting mode on %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("closing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}

func writeFileSudo(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err := runSudo("mkdir", "-p", dir); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.pgcli.tmp.%d", path, os.Getpid())
	_ = runSudo("rm", "-f", tmp)
	defer func() {
		if err != nil {
			_ = runSudo("rm", "-f", tmp)
		}
	}()
	if err := runSudoInput(data, "tee", tmp); err != nil {
		return err
	}
	if err := runSudo("chmod", "0644", tmp); err != nil {
		return err
	}
	if err := runSudo("mv", "-f", tmp, path); err != nil {
		return err
	}
	return nil
}

func runSudo(args ...string) error {
	cmd := exec.Command("sudo", append([]string{"-n"}, args...)...)
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return sudoError(args, stderr.String(), err)
	}
	return nil
}

func runSudoInput(data []byte, args ...string) error {
	cmd := exec.Command("sudo", append([]string{"-n"}, args...)...)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return sudoError(args, stderr.String(), err)
	}
	return nil
}

func runSudoOutput(args ...string) ([]byte, error) {
	cmd := exec.Command("sudo", append([]string{"-n"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, sudoError(args, stderr.String(), err)
	}
	return out, nil
}

func sudoError(args []string, stderr string, err error) error {
	command := "sudo -n " + strings.Join(args, " ")
	if msg := strings.TrimSpace(stderr); msg != "" {
		return fmt.Errorf("%s: %s", command, msg)
	}
	return fmt.Errorf("%s: %w", command, err)
}

func normalizeRegistries(registries []string) (map[string]bool, error) {
	wanted := make(map[string]bool)
	for _, registry := range registries {
		if !IsSupportedRegistry(registry) {
			return nil, fmt.Errorf("unsupported registry %q: supported registries are %s", registry, strings.Join(supportedRegistries, ", "))
		}
		wanted[registry] = true
	}
	return wanted, nil
}

func intersectEntries(entries map[string]string, wanted map[string]bool) []string {
	var matches []string
	for registry := range entries {
		if wanted[registry] {
			matches = append(matches, registry)
		}
	}
	return orderRegistries(matches)
}

func orderRegistries(registries []string) []string {
	seen := make(map[string]bool)
	ordered := make([]string, 0, len(registries))
	for _, known := range supportedRegistries {
		for _, registry := range registries {
			if registry == known && !seen[registry] {
				ordered = append(ordered, registry)
				seen[registry] = true
			}
		}
	}
	return ordered
}

func registryIndex(registry string) int {
	for i, known := range supportedRegistries {
		if known == registry {
			return i
		}
	}
	return len(supportedRegistries)
}
