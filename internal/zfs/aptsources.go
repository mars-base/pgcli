package zfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// APTComponent returns the archive component that contains ZFS packages.
func APTComponent(family DistroFamily) string {
	switch family {
	case FamilyDebian:
		return "contrib"
	case FamilyUbuntu:
		return "universe"
	default:
		return ""
	}
}

// APTArchiveURI reports whether a source URI points at the distro's own
// archive. Third-party repositories are deliberately left alone.
func APTArchiveURI(family DistroFamily, uri string) bool {
	value := strings.ToLower(strings.TrimSpace(uri))
	if strings.HasPrefix(value, "mirror+file:") {
		return true
	}
	switch family {
	case FamilyDebian:
		return strings.Contains(value, "deb.debian.org") ||
			strings.Contains(value, "security.debian.org")
	case FamilyUbuntu:
		return strings.Contains(value, "archive.ubuntu.com") ||
			strings.Contains(value, "security.ubuntu.com") ||
			strings.Contains(value, "ports.ubuntu.com")
	default:
		return false
	}
}

// EnableComponentInDeb822 adds a component to every matching deb822 stanza.
func EnableComponentInDeb822(content, component string, match func(string) bool) (string, bool) {
	lines := strings.Split(content, "\n")
	var out []string
	var block []string
	changed := false
	flush := func() {
		if len(block) == 0 {
			return
		}
		uris, hasURIs := findDeb822Field(block, "URIs")
		if hasURIs && anyURIMatches(uris.value, match) {
			components, hasComponents := findDeb822Field(block, "Components")
			var list []string
			if hasComponents {
				list = strings.Fields(components.value)
			}
			if len(list) == 0 {
				list = []string{"main"}
			}
			if !slices.Contains(list, component) {
				list = append(list, component)
				line := "Components: " + strings.Join(list, " ")
				if hasComponents {
					block = replaceLineSpan(block, components, []string{line})
				} else {
					block = append(block, line)
				}
				changed = true
			}
		}
		out = append(out, block...)
		block = nil
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			flush()
			out = append(out, line)
			continue
		}
		block = append(block, line)
	}
	flush()
	return strings.Join(out, "\n"), changed
}

// EnableComponentInList adds a component to every matching classic `.list`
// line, preserving full-line comments.
func EnableComponentInList(content, component string, match func(string) bool) (string, bool) {
	lines := strings.Split(content, "\n")
	changed := false
	for i, line := range lines {
		if updated, ok := enableComponentInListLine(line, component, match); ok {
			lines[i] = updated
			changed = true
		}
	}
	return strings.Join(lines, "\n"), changed
}

type lineSpan struct {
	start int
	end   int
	value string
}

func findDeb822Field(lines []string, key string) (lineSpan, bool) {
	prefix := strings.ToLower(key) + ":"
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		lower := strings.ToLower(trimmed)
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		_, fieldValue, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		values := []string{strings.TrimSpace(fieldValue)}
		j := i + 1
		for j < len(lines) {
			if !strings.HasPrefix(lines[j], " ") && !strings.HasPrefix(lines[j], "\t") {
				break
			}
			values = append(values, strings.TrimSpace(lines[j]))
			j++
		}
		var nonEmpty []string
		for _, value := range values {
			if value != "" {
				nonEmpty = append(nonEmpty, value)
			}
		}
		return lineSpan{start: i, end: j, value: strings.Join(nonEmpty, " ")}, true
	}
	return lineSpan{}, false
}

func anyURIMatches(value string, match func(string) bool) bool {
	return slices.ContainsFunc(strings.Fields(value), match)
}

func replaceLineSpan(lines []string, span lineSpan, replacement []string) []string {
	out := make([]string, 0, len(lines)-(span.end-span.start)+len(replacement))
	out = append(out, lines[:span.start]...)
	out = append(out, replacement...)
	out = append(out, lines[span.end:]...)
	return out
}

func enableComponentInListLine(line, component string, match func(string) bool) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return line, false
	}
	tokens := tokenizeListLine(trimmed)
	if len(tokens) < 3 || (tokens[0] != "deb" && tokens[0] != "deb-src") {
		return line, false
	}
	index := 1
	if strings.HasPrefix(tokens[index], "[") {
		for index < len(tokens) && !strings.HasSuffix(tokens[index], "]") {
			index++
		}
		index++
	}
	if index+1 >= len(tokens) {
		return line, false
	}
	uri := tokens[index]
	if !match(uri) {
		return line, false
	}
	components := append([]string{}, tokens[index+2:]...)
	if len(components) == 0 {
		components = []string{"main"}
	}
	if slices.Contains(components, component) {
		return line, false
	}
	components = append(components, component)
	updated := append([]string{}, tokens[:index+2]...)
	updated = append(updated, components...)
	return strings.Join(updated, " "), true
}

func tokenizeListLine(line string) []string {
	var tokens []string
	var current strings.Builder
	inBracket := false
	for _, r := range line {
		switch {
		case r == '[':
			inBracket = true
			current.WriteRune(r)
		case r == ']':
			inBracket = false
			current.WriteRune(r)
		case (r == ' ' || r == '\t') && !inBracket:
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

// EnableAPTComponent edits Debian/Ubuntu archive sources so the ZFS packages
// are visible. It returns whether any file changed. Third-party sources are
// untouched, each file is backed up once, and writes are atomic.
func EnableAPTComponent(ctx context.Context, r *Runner, distro Distro) (bool, error) {
	component := APTComponent(distro.Family)
	if component == "" {
		return false, fmt.Errorf("no APT component mapping for %s", distro.Family)
	}
	match := func(uri string) bool { return APTArchiveURI(distro.Family, uri) }
	files, err := aptSourceFiles()
	if err != nil {
		return false, err
	}
	changedAny := false
	for _, path := range files {
		data, err := r.ReadFile(ctx, path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return changedAny, fmt.Errorf("reading %s: %w", path, err)
		}
		var updated string
		var changed bool
		switch {
		case strings.HasSuffix(path, ".sources"):
			updated, changed = EnableComponentInDeb822(string(data), component, match)
		case strings.HasSuffix(path, ".list"):
			updated, changed = EnableComponentInList(string(data), component, match)
		default:
			continue
		}
		if !changed {
			continue
		}
		mode := os.FileMode(0644)
		if fi, statErr := os.Stat(path); statErr == nil {
			mode = fi.Mode()
		}
		if err := r.BackupFile(ctx, path); err != nil {
			return changedAny, fmt.Errorf("backing up %s: %w", path, err)
		}
		if err := r.WriteFileAtomic(ctx, path, []byte(updated), mode); err != nil {
			return changedAny, fmt.Errorf("writing %s: %w", path, err)
		}
		fmt.Printf("  [OK] Enabled %s in %s\n", component, path)
		changedAny = true
	}
	return changedAny, nil
}

func aptSourceFiles() ([]string, error) {
	patterns := []string{
		"/etc/apt/sources.list",
		"/etc/apt/sources.list.d/*.sources",
		"/etc/apt/sources.list.d/*.list",
	}
	seen := map[string]bool{}
	var files []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		for _, path := range matches {
			if !seen[path] {
				seen[path] = true
				files = append(files, path)
			}
		}
	}
	return files, nil
}
