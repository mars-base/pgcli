package inspect

import (
	"regexp"
	"strings"
	"testing"
)

var (
	blockRe      = regexp.MustCompile(`(?s)/\*.*?\*/`)
	singleQuoted = regexp.MustCompile(`'(?:[^']|'')*'`)
)

// Every diagnostic query must be wrapped in json_agg (so one psql -tA call
// yields a single JSON array) and must be read-only. These files are the only
// SQL `pg inspect` ever sends; assert they contain no write/DDL statement and
// each returns a set the runner can decode. Comments are stripped first — the
// prose ("…never vacuumed…") must not be mistaken for SQL.
func TestSQLFilesAreReadOnlyAndJSONWrapped(t *testing.T) {
	files := []string{"health.sql", "limits.sql", "queries.sql", "indexes.sql", "tables.sql", "settings.sql", "activity.sql"}
	keywords := []string{"insert", "update", "delete", "drop", "alter", "create", "truncate", "vacuum", "analyze", "grant", "revoke", "copy", "call", "set", "reset", "commit", "begin"}
	for _, name := range files {
		src, err := loadSQL(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		code := stripSQLComments(src)
		lower := strings.ToLower(code)
		if !strings.Contains(lower, "json_agg(") {
			t.Errorf("%s: must wrap the result in json_agg(t)", name)
		}
		for _, kw := range keywords {
			if hasKeyword(lower, kw) {
				t.Errorf("%s: contains write/DDL keyword %q in executable SQL", name, kw)
			}
		}
	}
}

// stripSQLComments removes block comments, then per-line "--" comments, and
// finally single-quoted string literals — in that order. Comments must go
// first: an apostrophe in prose ("it's", "cluster's") would otherwise pair with
// a real quote and let the string regex eat executable SQL like json_agg(t).
func stripSQLComments(s string) string {
	s = blockRe.ReplaceAllString(s, " ")
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		lines = append(lines, line)
	}
	s = singleQuoted.ReplaceAllString(strings.Join(lines, "\n"), "''")
	return s
}

// hasKeyword reports a standalone-word match: keyword letters not glued to an
// identifier char (so "last_analyze" / "stats_reset" / "offset" don't count).
func hasKeyword(hay, word string) bool {
	for i := 0; i < len(hay); {
		j := strings.Index(hay[i:], word)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(word)
		beforeOK := start == 0 || !isIdent(hay[start-1])
		afterOK := end == len(hay) || !isIdent(hay[end])
		if beforeOK && afterOK {
			return true
		}
		i = end
	}
	return false
}

func isIdent(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}
