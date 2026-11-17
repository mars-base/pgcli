package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type benchOptions struct {
	DSN             string
	Image           string
	BenchDB         string
	NoAutoDB        bool
	Init            bool
	Scale           int
	Partitions      int
	PartitionMethod string
	Fillfactor      int
	ForeignKeys     bool
	NoVacuum        bool
	Clients         int
	Threads         int
	Time            int
	Transactions    int
	Mode            string
	SelectOnly      bool
	SimpleUpdate    bool
	Builtin         []string
	Files           []string
	Define          []string
	Connect         bool
	Rate            float64
	LatencyLimit    int
	Progress        int
	PerCommand      bool
	Log             bool
	LogDir          string
}

type benchPlan struct {
	Args    []string
	Mounts  []string
	WorkDir string
	LogDir  string
}

var benchRunOnlyFlags = []string{
	"clients", "threads", "time", "transactions", "mode",
	"select-only", "simple-update", "builtin", "file", "define",
	"connect", "rate", "latency-limit", "progress", "per-command-report",
	"log", "log-dir",
}

var benchInitOnlyFlags = []string{
	"scale", "partitions", "partition-method", "fillfactor", "foreign-keys",
}

func validateBenchOptions(o benchOptions, changed map[string]bool) error {
	if o.Init {
		for _, name := range benchRunOnlyFlags {
			if changed[name] {
				return fmt.Errorf("--%s is a run-only flag; remove it or drop --init", name)
			}
		}
	} else {
		for _, name := range benchInitOnlyFlags {
			if changed[name] {
				return fmt.Errorf("--%s requires --init", name)
			}
		}
	}

	if changed["time"] && changed["transactions"] {
		return fmt.Errorf("--time and --transactions are mutually exclusive")
	}
	if o.SelectOnly && o.SimpleUpdate {
		return fmt.Errorf("--select-only and --simple-update are mutually exclusive")
	}
	if changed["latency-limit"] && !changed["rate"] {
		return fmt.Errorf("--latency-limit requires --rate")
	}
	if changed["partition-method"] && !(changed["partitions"] && o.Partitions > 0) {
		return fmt.Errorf("--partition-method requires --partitions greater than 0")
	}
	if changed["log-dir"] && !o.Log {
		return fmt.Errorf("--log-dir requires --log")
	}

	if o.Scale <= 0 {
		return fmt.Errorf("--scale must be greater than 0")
	}
	if o.Clients <= 0 {
		return fmt.Errorf("--clients must be greater than 0")
	}
	if o.Threads <= 0 {
		return fmt.Errorf("--threads must be greater than 0")
	}
	if o.Time <= 0 {
		return fmt.Errorf("--time must be greater than 0")
	}
	if changed["transactions"] && o.Transactions <= 0 {
		return fmt.Errorf("--transactions must be greater than 0")
	}
	if o.Progress < 0 {
		return fmt.Errorf("--progress must be 0 or greater")
	}
	if o.LatencyLimit < 0 {
		return fmt.Errorf("--latency-limit must be 0 or greater")
	}
	if o.Partitions < 0 {
		return fmt.Errorf("--partitions must be 0 or greater")
	}
	if changed["fillfactor"] && (o.Fillfactor < 10 || o.Fillfactor > 100) {
		return fmt.Errorf("--fillfactor must be between 10 and 100")
	}
	if changed["rate"] && o.Rate <= 0 {
		return fmt.Errorf("--rate must be greater than 0")
	}
	switch o.Mode {
	case "simple", "extended", "prepared":
	default:
		return fmt.Errorf("--mode must be simple, extended or prepared (got %q)", o.Mode)
	}
	switch o.PartitionMethod {
	case "", "range", "hash":
	default:
		return fmt.Errorf("--partition-method must be range or hash (got %q)", o.PartitionMethod)
	}
	if !o.NoAutoDB {
		if err := validateBenchDBName(o.BenchDB); err != nil {
			return err
		}
	}
	return nil
}

func buildBenchPlan(o benchOptions) (benchPlan, error) {
	var plan benchPlan
	if o.Init {
		plan.Args = []string{"-i", "-s", strconv.Itoa(o.Scale)}
		if o.NoVacuum {
			plan.Args = append(plan.Args, "--no-vacuum")
		}
		if o.Partitions > 0 {
			plan.Args = append(plan.Args, "--partitions", strconv.Itoa(o.Partitions))
		}
		if o.PartitionMethod != "" {
			plan.Args = append(plan.Args, "--partition-method", o.PartitionMethod)
		}
		if o.Fillfactor > 0 {
			plan.Args = append(plan.Args, "--fillfactor", strconv.Itoa(o.Fillfactor))
		}
		if o.ForeignKeys {
			plan.Args = append(plan.Args, "--foreign-keys")
		}
		return plan, nil
	}

	plan.Args = []string{
		"-c", strconv.Itoa(o.Clients),
		"-j", strconv.Itoa(o.Threads),
		"-M", o.Mode,
	}
	if o.Transactions > 0 {
		plan.Args = append(plan.Args, "-t", strconv.Itoa(o.Transactions))
	} else {
		plan.Args = append(plan.Args, "-T", strconv.Itoa(o.Time))
	}
	if o.Progress > 0 {
		plan.Args = append(plan.Args, "-P", strconv.Itoa(o.Progress))
	}
	if o.SelectOnly {
		plan.Args = append(plan.Args, "-S")
	}
	if o.SimpleUpdate {
		plan.Args = append(plan.Args, "-N")
	}
	for _, b := range o.Builtin {
		plan.Args = append(plan.Args, "-b", b)
	}

	fileArgs, fileMounts, err := resolveBenchFiles(o.Files)
	if err != nil {
		return benchPlan{}, err
	}
	for _, f := range fileArgs {
		plan.Args = append(plan.Args, "-f", f)
	}
	plan.Mounts = append(plan.Mounts, fileMounts...)

	for _, d := range o.Define {
		plan.Args = append(plan.Args, "-D", d)
	}
	if o.Connect {
		plan.Args = append(plan.Args, "-C")
	}
	if o.Rate > 0 {
		plan.Args = append(plan.Args, "-R", strconv.FormatFloat(o.Rate, 'g', -1, 64))
	}
	if o.LatencyLimit > 0 {
		plan.Args = append(plan.Args, "-L", strconv.Itoa(o.LatencyLimit))
	}
	if o.PerCommand {
		plan.Args = append(plan.Args, "-r")
	}
	if o.Log {
		plan.Args = append(plan.Args, "-l")
	}
	if o.NoVacuum {
		plan.Args = append(plan.Args, "-n")
	}
	if o.Log {
		logDir, err := resolveBenchLogDir(o.LogDir)
		if err != nil {
			return benchPlan{}, err
		}
		plan.LogDir = logDir
		plan.WorkDir = "/pgcli-bench/log"
		plan.Mounts = append(plan.Mounts, logDir+":/pgcli-bench/log:Z")
	}
	return plan, nil
}

func resolveBenchFiles(specs []string) ([]string, []string, error) {
	var args, mounts []string
	for i, spec := range specs {
		path, weight := splitWeightedBenchSpec(spec)
		if path == "" {
			return nil, nil, fmt.Errorf("--file %q has an empty path", spec)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving script %q: %w", path, err)
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
		fi, err := os.Stat(abs)
		if err != nil {
			return nil, nil, fmt.Errorf("script %q: %w", path, err)
		}
		if !fi.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("script %q is not a regular file", path)
		}
		if strings.Contains(abs, ":") {
			return nil, nil, fmt.Errorf("script path %q must not contain ':'", path)
		}
		container := fmt.Sprintf("/pgcli-bench/files/%d-%s", i, sanitizeBenchMountName(filepath.Base(abs)))
		mounts = append(mounts, abs+":"+container+":ro,Z")
		if weight != "" {
			args = append(args, container+"@"+weight)
		} else {
			args = append(args, container)
		}
	}
	return args, mounts, nil
}

// splitWeightedBenchSpec splits path@weight only when the suffix is a positive
// integer, so filenames containing '@' are left alone.
func splitWeightedBenchSpec(spec string) (string, string) {
	at := strings.LastIndex(spec, "@")
	if at < 0 || at == len(spec)-1 {
		return spec, ""
	}
	weight := spec[at+1:]
	for _, r := range weight {
		if r < '0' || r > '9' {
			return spec, ""
		}
	}
	n, err := strconv.Atoi(weight)
	if err != nil || n <= 0 {
		return spec, ""
	}
	return spec[:at], weight
}

func resolveBenchLogDir(dir string) (string, error) {
	if dir == "" {
		dir = "./pgbench-logs"
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolving --log-dir %q: %w", dir, err)
	}
	if strings.Contains(abs, ":") {
		return "", fmt.Errorf("--log-dir %q must not contain ':'", dir)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", fmt.Errorf("creating --log-dir %q: %w", dir, err)
	}
	return abs, nil
}

var benchShortArgOpts = map[byte]bool{
	'b': true, 'c': true, 'd': true, 'D': true, 'f': true, 'F': true,
	'h': true, 'I': true, 'j': true, 'L': true, 'M': true, 'p': true,
	'P': true, 'R': true, 's': true, 't': true, 'T': true, 'U': true,
}

var benchShortNoArgOpts = map[byte]bool{
	'C': true, 'i': true, 'l': true, 'n': true, 'N': true, 'q': true,
	'r': true, 'S': true, 'v': true, 'V': true, '?': true,
}

// validateBenchPassthrough rejects raw pgbench options that would override the
// selected target or make mounted files/logs silently unusable.
func validateBenchPassthrough(args []string) error {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return nil
		}
		if strings.HasPrefix(arg, "--") {
			name, _, _ := strings.Cut(arg[2:], "=")
			if reject, ok := benchLongOptionReject(name); ok {
				switch reject {
				case "file":
					return fmt.Errorf("use --file instead of raw --file in passthrough so pgcli mounts the script")
				case "log":
					return fmt.Errorf("use --log instead of raw --log in passthrough so pgcli mounts the log directory")
				default:
					return fmt.Errorf("--%s is controlled by -i/--dsn; remove it from passthrough", reject)
				}
			}
			continue
		}
		if len(arg) < 2 || arg[0] != '-' {
			continue
		}
		for j := 1; j < len(arg); j++ {
			ch := arg[j]
			switch ch {
			case 'h', 'p', 'U', 'd', 'i':
				return fmt.Errorf("-%c is controlled by -i/--dsn; remove it from passthrough", ch)
			case 'f':
				return fmt.Errorf("use --file instead of raw -f in passthrough so pgcli mounts the script")
			case 'l':
				return fmt.Errorf("use --log instead of raw -l in passthrough so pgcli mounts the log directory")
			}
			if benchShortArgOpts[ch] {
				if j == len(arg)-1 {
					i++ // the next token is this option's value; do not scan it
				}
				break
			}
			if !benchShortNoArgOpts[ch] {
				return fmt.Errorf("unsupported short option -%c in passthrough; use the wrapper flags or the long form", ch)
			}
		}
	}
	return nil
}

func benchLongOptionReject(name string) (string, bool) {
	targets := map[string]bool{
		"host": true, "port": true, "username": true, "dbname": true, "initialize": true,
	}
	for candidate := range targets {
		if candidate == name || strings.HasPrefix(candidate, name) || strings.HasPrefix(name, candidate) {
			return candidate, true
		}
	}
	for _, candidate := range []string{"file", "log"} {
		if candidate == name || strings.HasPrefix(candidate, name) || strings.HasPrefix(name, candidate) {
			return candidate, true
		}
	}
	return "", false
}
