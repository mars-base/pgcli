package tune

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// KV is one name/value pair in apply order.
type KV struct {
	Name  string
	Value string
}

// ParseConf reads a postgresql.conf-style tuning file: `name = value` lines,
// # comments (also inline), blank lines ignored, one optional layer of single
// quotes stripped. Duplicate names keep the last value, first position.
func ParseConf(r io.Reader) ([]KV, error) {
	var out []KV
	seen := map[string]int{}
	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		s := strings.TrimSpace(sc.Text())
		if i := strings.IndexByte(s, '#'); i >= 0 {
			s = strings.TrimSpace(s[:i])
		}
		if s == "" {
			continue
		}
		name, val, ok := strings.Cut(s, "=")
		if !ok {
			return nil, fmt.Errorf("tune conf line %d: expected key = value, got %q", line, s)
		}
		name = strings.TrimSpace(name)
		val = strings.Trim(strings.TrimSpace(val), "'")
		if name == "" || val == "" {
			return nil, fmt.Errorf("tune conf line %d: empty %s in %q", line,
				map[bool]string{true: "key", false: "value"}[name == ""], s)
		}
		if at, dup := seen[name]; dup {
			out[at].Value = val
			continue
		}
		seen[name] = len(out)
		out = append(out, KV{Name: name, Value: val})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("tune conf: %w", err)
	}
	return out, nil
}

// FormatConf renders kvs back into a postgresql.conf fragment with a header.
func FormatConf(w io.Writer, in Inputs, kvs []KV) error {
	fmt.Fprintf(w, "# pg tune — generated inputs: type=%s hd=%s ram=%s cpus=%d version=%d\n",
		in.Type, in.Disk, formatValue(floorKb(in.ramKB())), in.CPUs, in.major())
	for _, kv := range kvs {
		if _, err := fmt.Fprintf(w, "%s = '%s'\n", kv.Name, kv.Value); err != nil {
			return err
		}
	}
	return nil
}

// ParamsToKV converts recommendations to plain pairs.
func ParamsToKV(ps []Param) []KV {
	out := make([]KV, 0, len(ps))
	for _, p := range ps {
		out = append(out, KV{p.Name, p.Value})
	}
	return out
}

// KVsToMap collapses pairs for storage; later duplicates win.
func KVsToMap(kvs []KV) map[string]string {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		m[kv.Name] = kv.Value
	}
	return m
}
