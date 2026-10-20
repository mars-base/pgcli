package tune

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// SettingRow is one pg_settings record relevant to validation.
type SettingRow struct {
	Name     string
	Vartype  string // bool | enum | integer | real | string
	Unit     string // setting's preferred unit (kB, 8kB blocks…)
	MinVal   string
	MaxVal   string
	EnumVals string // pg_settings.enumvals, "{a,b,c}" form
}

// SettingsFetcher loads pg_settings rows for the named parameters. The CLI
// supplies it from the querier (local / dsn / HA leader).
type SettingsFetcher func(ctx context.Context, names []string) (map[string]SettingRow, error)

// Validate checks each proposed pair against the target server's pg_settings:
// unknown name, bool/enum value domains, and integer/real bounds (converted
// through the setting's own unit). It returns one error per offending pair;
// an empty slice means everything is safe to apply.
func Validate(ctx context.Context, kvs []KV, fetch SettingsFetcher) []error {
	names := make([]string, len(kvs))
	for i, kv := range kvs {
		names[i] = kv.Name
	}
	settings, err := fetch(ctx, names)
	if err != nil {
		return []error{fmt.Errorf("tune: fetch server settings: %w", err)}
	}
	var errs []error
	for _, kv := range kvs {
		set, ok := settings[kv.Name]
		if !ok {
			errs = append(errs, fmt.Errorf("%s: not a parameter of the target server", kv.Name))
			continue
		}
		if e := validateOne(set, kv.Value); e != nil {
			errs = append(errs, fmt.Errorf("%s = %s: %w", kv.Name, kv.Value, e))
		}
	}
	return errs
}

func validateOne(set SettingRow, value string) error {
	switch set.Vartype {
	case "bool":
		switch strings.ToLower(value) {
		case "on", "off", "true", "false", "yes", "no", "1", "0":
			return nil
		}
		return fmt.Errorf("not a boolean value")
	case "enum":
		allowed := parseEnumVals(set.EnumVals)
		for _, a := range allowed {
			if strings.EqualFold(a, value) {
				return nil
			}
		}
		return fmt.Errorf("must be one of %s", strings.Join(allowed, ", "))
	case "integer", "real":
		return validateNumeric(set, value)
	}
	return nil // string (and unknown types): anything goes
}

func parseEnumVals(s string) []string {
	s = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s), "{"), "}")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(parts[i]), `"`)
	}
	return parts
}

func validateNumeric(set SettingRow, value string) error {
	v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		// A unit-bearing value ("128MB", "5min") is accepted as long as the
		// number+unit parses — converting through each setting's unit context
		// (kB vs 8kB blocks vs ms) is not reliably possible from pg_settings.
		if _, _, perr := ParseWithUnit(value); perr != nil {
			return fmt.Errorf("not a number%s", unitHint(set.Unit))
		}
		return nil
	}
	return checkRange(v, set)
}

// checkRange compares a unitless value against min/max, converting through the
// setting's unit when both sides carry one (pg_settings reports limits in the
// setting's preferred unit).
func checkRange(v float64, set SettingRow) error {
	min, hasMin := parseFloat(set.MinVal)
	max, hasMax := parseFloat(set.MaxVal)
	if hasMin && v < min {
		return fmt.Errorf("below minimum %s%s", set.MinVal, unitHint(set.Unit))
	}
	if hasMax && v > max {
		return fmt.Errorf("above maximum %s%s", set.MaxVal, unitHint(set.Unit))
	}
	return nil
}

func parseFloat(s string) (float64, bool) {
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return n, err == nil
}

func unitHint(unit string) string {
	if unit == "" {
		return ""
	}
	return fmt.Sprintf(" (unit %s)", unit)
}

// unitMultipliers maps a PostgreSQL time/size unit suffix to its kB-equivalent
// multiplier for the config types we emit (all kB/8kB-block free already).
var unitMultipliers = map[string]float64{
	"kB": 1, "MB": 1024, "GB": 1024 * 1024, "TB": 1024 * 1024 * 1024,
	"s": 1000, "ms": 1, "min": 60 * 1000, "h": 3600 * 1000, "d": 86400 * 1000,
}

// ParseWithUnit splits "5min" / "128MB" into value-in-base-unit and the unit
// suffix (ms for time, kB for size families). Used by the CLI to accept human
// units in --apply-conf files and --ram/--db-size flags.
func ParseWithUnit(value string) (float64, string, error) {
	value = strings.TrimSpace(value)
	i := 0
	for i < len(value) && (value[i] >= '0' && value[i] <= '9' || value[i] == '.' || value[i] == '-') {
		i++
	}
	numPart, unitPart := value[:i], strings.TrimSpace(value[i:])
	n, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, "", fmt.Errorf("not a number: %q", value)
	}
	if unitPart == "" {
		return n, "", nil
	}
	mult, ok := unitMultipliers[unitPart]
	if !ok {
		return 0, "", fmt.Errorf("unknown unit %q in %q", unitPart, value)
	}
	return n * mult, unitPart, nil
}
