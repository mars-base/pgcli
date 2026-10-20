package tune

// minifyJS conservatively compacts the web page's JavaScript: it strips
// comments and collapses whitespace, but never rewrites identifiers or
// literals, so the output cannot change behaviour — unlike a real mangler.
// It understands the subset the template uses: // and /* */ comments, '…' and
// "…" strings (with backslash escapes), and / as plain division (the template
// carries no regex literals).
func minifyJS(src string) string {
	// Pass 1: blank out comments (keeping the source length and all string
	// literals intact, so a "//" inside a string never reads as a comment).
	rs := []rune(src)
	for i := 0; i < len(rs); {
		switch rs[i] {
		case '\'', '"':
			q := rs[i]
			i++
			for i < len(rs) {
				if rs[i] == '\\' {
					i += 2
					continue
				}
				if rs[i] == q {
					i++
					break
				}
				i++
			}
		case '/':
			if i+1 < len(rs) && rs[i+1] == '/' {
				for i < len(rs) && rs[i] != '\n' {
					rs[i] = ' '
					i++
				}
				continue
			}
			if i+1 < len(rs) && rs[i+1] == '*' {
				j := i + 2
				for j+1 < len(rs) && !(rs[j] == '*' && rs[j+1] == '/') {
					j++
				}
				for k := i; k < j+2 && k < len(rs); k++ {
					rs[k] = ' '
				}
				i = j + 2
				continue
			}
			i++
		default:
			i++
		}
	}

	// Pass 2: collapse whitespace runs. A run survives as a single space only
	// when it separates tokens that would otherwise fuse or form a comment
	// opener; otherwise it is dropped. Newlines are never structural here
	// (explicit ; and braces everywhere), so this is safe for ASI-free code.
	out := make([]rune, 0, len(rs))
	for i := 0; i < len(rs); {
		switch rs[i] {
		case '\'', '"': // copy the whole literal — its contents are opaque
			q := rs[i]
			out = append(out, q)
			i++
			for i < len(rs) {
				if rs[i] == '\\' && i+1 < len(rs) {
					out = append(out, rs[i], rs[i+1])
					i += 2
					continue
				}
				out = append(out, rs[i])
				if rs[i] == q {
					i++
					break
				}
				i++
			}
		default:
			if isJSWS(rs[i]) {
				j := i
				for j < len(rs) && isJSWS(rs[j]) {
					j++
				}
				if len(out) > 0 && j < len(rs) && needsSpace(out[len(out)-1], rs[j]) {
					out = append(out, ' ')
				}
				i = j
				continue
			}
			out = append(out, rs[i])
			i++
		}
	}
	return string(trimJS(out))
}

func isJSWS(r rune) bool {
	return r == ' ' || r == '\t' || r == '\r' || r == '\n'
}

func needsSpace(prev, next rune) bool {
	if isJSWordRune(prev) && isJSWordRune(next) {
		return true
	}
	// keep '/' from pairing with a neighbour into a comment opener
	return (prev == '/' && (next == '/' || next == '*')) ||
		(next == '/' && (prev == '/' || prev == '*'))
}

func isJSWordRune(r rune) bool {
	return r == '_' || r == '$' ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func trimJS(rs []rune) []rune {
	for len(rs) > 0 && isJSWS(rs[0]) {
		rs = rs[1:]
	}
	for len(rs) > 0 && isJSWS(rs[len(rs)-1]) {
		rs = rs[:len(rs)-1]
	}
	return rs
}
