package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/mars-base/pgcli/internal/podman"
)

func init() {
	f := benchCmd.Flags()
	f.String("dsn", "", "remote target as postgres://user:pass@host:port/db")
	f.String("image", "", "client container image containing pgbench (default: config image_tag)")
	f.String("bench-db", "bench_db", "dedicated benchmark database when the account has CREATEDB")
	f.Bool("no-auto-db", false, "use the target database instead of selecting/creating a benchmark database")
	f.Bool("init", false, "initialize pgbench tables with pgbench -i")
	f.Bool("cleanup", false, "initialize, run, then drop the pgbench tables (the database is kept)")
	f.Int("scale", 1, "pgbench scale factor for --init")
	f.Int("partitions", 0, "number of partitions for --init")
	f.String("partition-method", "", "partition method: range or hash (requires --partitions)")
	f.Int("fillfactor", 0, "table fillfactor 10-100 for --init")
	f.Bool("foreign-keys", false, "add foreign keys to pgbench tables on --init")
	f.Bool("no-vacuum", false, "skip vacuum during --init, and pass -n when running")
	f.Int("clients", 16, "number of concurrent clients")
	f.IntP("threads", "j", 4, "number of worker threads")
	f.IntP("time", "T", 60, "run duration in seconds")
	f.IntP("transactions", "t", 0, "number of transactions per client (overrides --time)")
	f.StringP("mode", "M", "prepared", "query mode: simple, extended or prepared")
	f.BoolP("select-only", "S", false, "run only SELECT transactions")
	f.BoolP("simple-update", "N", false, "run simple-update transactions")
	f.StringArrayP("builtin", "b", nil, "builtin script name[@weight] (repeatable)")
	f.StringArrayP("file", "f", nil, "custom script path[@weight] on the host (repeatable)")
	f.StringArrayP("define", "D", nil, "script variable K=V (repeatable)")
	f.BoolP("connect", "C", false, "reconnect for each transaction")
	f.Float64P("rate", "R", 0, "target transactions per second")
	f.IntP("latency-limit", "L", 0, "latency limit in milliseconds (requires --rate)")
	f.IntP("progress", "P", 10, "progress report interval in seconds; 0 disables")
	f.BoolP("per-command-report", "r", false, "report per-command latencies")
	f.BoolP("log", "l", false, "write per-transaction logs to the host")
	f.String("log-dir", "./pgbench-logs", "host directory for --log output")
	rootCmd.AddCommand(benchCmd)
}

var benchCmd = &cobra.Command{
	Use:   "bench [flags] [-- <pgbench-args>...]",
	Short: "Run a pgbench load test against a local or remote PostgreSQL",
	Long: `bench runs pgbench inside a one-shot container, so neither pgbench nor a
client install is needed on the host.

It targets a local pgcli instance by default, or a remote database with --dsn.
When the account can create databases, a dedicated benchmark database
(bench_db by default) is selected automatically; otherwise the target database
is used.

--cleanup runs the whole lifecycle in one command: initialize the pgbench
tables, run the benchmark, then drop those tables. This is convenient for
one-off tests and avoids leaving benchmark data behind. The database itself is
never dropped.

Additional pgbench arguments may be passed after --. Options that would change
the connection target (-h, -p, -U, -d, -i), script files (-f) and logging (-l)
are rejected there; use the wrapper flags instead.

Examples:
  pg bench -i proj01 --init --scale 1
  pg bench -i proj01
  pg bench -i proj01 --cleanup -T 60
  pg bench -i proj01 -T 60 --clients 16 --threads 4
  pg bench -i proj01 -S -T 30 --progress 5
  pg bench --dsn 'postgres://user:pass@10.0.0.8:5432/appdb' --cleanup
  pg bench --dsn 'postgres://user:pass@10.0.0.8:5432/appdb' -T 60
  pg bench -i proj01 -f ./bench.sql@2 -T 30
  pg bench -i proj01 --init --no-vacuum --scale 10
  pg bench -i proj01 -- --sampling-rate 0.1`,
	RunE: runBench,
}

func runBench(cmd *cobra.Command, args []string) error {
	o := readBenchOptions(cmd.Flags())
	changed := map[string]bool{}
	cmd.Flags().Visit(func(f *pflag.Flag) { changed[f.Name] = true })

	if err := validateBenchOptions(o, changed); err != nil {
		return err
	}

	if dash := cmd.ArgsLenAtDash(); len(args) > 0 && dash != 0 {
		positional := args
		if dash > 0 {
			positional = args[:dash]
		}
		return fmt.Errorf("unexpected positional arguments %q before --; instance selection uses -i/--instance", positional)
	}
	if err := validateBenchPassthrough(args); err != nil {
		return err
	}

	target, pm, controlDB, err := resolveBenchTarget(cmd, o)
	if err != nil {
		return err
	}

	image := o.Image
	if image == "" {
		image = cfg.Podman.ImageTag
	}

	if o.NoAutoDB {
		fmt.Printf("-> benchmark database: %s (auto-selection disabled)\n", target.Database)
	} else {
		result, err := resolveBenchDatabase(pm, target, controlDB, image, o.BenchDB)
		if err != nil {
			return err
		}
		target = result.Target
		switch {
		case result.AutoSelected && result.Created:
			fmt.Printf("-> benchmark database: %s (created)\n", target.Database)
		case result.AutoSelected:
			fmt.Printf("-> benchmark database: %s (auto-selected)\n", target.Database)
		default:
			fmt.Printf("-> benchmark database: %s (account cannot create databases)\n", target.Database)
		}
		if result.Created && !o.Init && !o.Cleanup {
			return fmt.Errorf("benchmark database %q was created but not initialized; run \"pg bench --init\" first", target.Database)
		}
	}

	runTest := !o.Init || o.Cleanup
	var initPlan, runPlan benchPlan
	if o.Init || o.Cleanup {
		initOpts := o
		initOpts.Init = true
		initPlan, err = buildBenchPlan(initOpts)
		if err != nil {
			return err
		}
	}
	if runTest {
		runOpts := o
		runOpts.Init = false
		runPlan, err = buildBenchPlan(runOpts)
		if err != nil {
			return err
		}
		if runPlan.LogDir != "" {
			fmt.Printf("-> pgbench logs: %s\n", runPlan.LogDir)
		}
	}

	if o.Init || o.Cleanup {
		err := pm.RunPgbench(podman.BenchContainerOptions{
			Image:      image,
			Entrypoint: "pgbench",
			Args:       initPlan.Args,
			Env:        target.env(),
			Mounts:     append(append([]string{}, target.Mounts...), initPlan.Mounts...),
			WorkDir:    initPlan.WorkDir,
		})
		if err != nil {
			initErr := fmt.Errorf("initializing pgbench tables: %w", err)
			if o.Cleanup {
				cleanupErr := cleanupBenchTables(pm, target, image)
				if cleanupErr != nil {
					fmt.Println(benchCleanupHint(target.Database, benchExecTargetFlags(cmd, o)))
				}
				return errors.Join(initErr, cleanupErr)
			}
			fmt.Println(benchCleanupHint(target.Database, benchExecTargetFlags(cmd, o)))
			return initErr
		}
	}
	if !runTest {
		fmt.Println(benchCleanupHint(target.Database, benchExecTargetFlags(cmd, o)))
		return nil
	}

	pgArgs := append(append([]string{}, runPlan.Args...), args...)
	mounts := append(append([]string{}, target.Mounts...), runPlan.Mounts...)
	var logBefore map[string]bool
	if runPlan.LogDir != "" {
		logBefore = benchLogFileSet(runPlan.LogDir)
	}
	runErr := pm.RunPgbench(podman.BenchContainerOptions{
		Image:      image,
		Entrypoint: "pgbench",
		Args:       pgArgs,
		Env:        target.env(),
		Mounts:     mounts,
		WorkDir:    runPlan.WorkDir,
	})
	if runPlan.LogDir != "" {
		for _, path := range benchNewLogFiles(runPlan.LogDir, logBefore) {
			fmt.Printf("-> pgbench log file: %s\n", path)
		}
	}
	if !o.Cleanup {
		if runErr != nil {
			if tablesExist, probeErr := benchTablesExist(pm, target, image); probeErr == nil && !tablesExist {
				fmt.Println(benchInitHint(target.Database, benchHintTargetFlags(cmd, o)))
				return runErr
			}
		}
		fmt.Println(benchCleanupHint(target.Database, benchExecTargetFlags(cmd, o)))
		return runErr
	}

	fmt.Printf("-> dropping pgbench tables in %s\n", target.Database)
	cleanupErr := cleanupBenchTables(pm, target, image)
	if runErr != nil {
		runErr = fmt.Errorf("benchmark run failed: %w", runErr)
	}
	if cleanupErr != nil {
		fmt.Println(benchCleanupHint(target.Database, benchExecTargetFlags(cmd, o)))
	}
	return errors.Join(runErr, cleanupErr)
}

func resolveBenchTarget(cmd *cobra.Command, o benchOptions) (benchTarget, *podman.Manager, string, error) {
	if o.DSN != "" {
		if err := checkDSNInstanceConflict(cmd); err != nil {
			return benchTarget{}, nil, "", err
		}
		if err := loadConfigForDSN(); err != nil {
			return benchTarget{}, nil, "", err
		}
		target, err := parseBenchDSN(o.DSN)
		if err != nil {
			return benchTarget{}, nil, "", err
		}
		pm, err := newPodman()
		if err != nil {
			return benchTarget{}, nil, "", err
		}
		return target, pm, target.Database, nil
	}

	if err := loadConfig(); err != nil {
		return benchTarget{}, nil, "", err
	}
	pm, err := newPodman()
	if err != nil {
		return benchTarget{}, nil, "", err
	}
	if err := pm.CheckContainerRunning(); err != nil {
		return benchTarget{}, nil, "", err
	}
	return benchTargetFromConfig(cfg), pm, "postgres", nil
}

func benchHintTargetFlags(cmd *cobra.Command, o benchOptions) string {
	parts := []string{benchExecTargetFlags(cmd, o)}
	if o.BenchDB != "bench_db" {
		parts = append(parts, "--bench-db", o.BenchDB)
	}
	if o.NoAutoDB {
		parts = append(parts, "--no-auto-db")
	}
	if o.Image != "" {
		parts = append(parts, "--image", o.Image)
	}
	return strings.Join(parts, " ")
}

// benchExecTargetFlags selects the same target for a follow-up pg exec
// command, which does not understand bench-only flags.
func benchExecTargetFlags(cmd *cobra.Command, o benchOptions) string {
	var parts []string
	if cmd.Flags().Changed("config") {
		parts = append(parts, "-c", cfgPath)
	}
	if o.DSN != "" {
		parts = append(parts, "--dsn", `"$DSN"`)
	} else {
		parts = append(parts, "-i", cfgInstance)
	}
	return strings.Join(parts, " ")
}

func readBenchOptions(fs *pflag.FlagSet) benchOptions {
	var o benchOptions
	o.DSN, _ = fs.GetString("dsn")
	o.Image, _ = fs.GetString("image")
	o.BenchDB, _ = fs.GetString("bench-db")
	o.NoAutoDB, _ = fs.GetBool("no-auto-db")
	o.Init, _ = fs.GetBool("init")
	o.Cleanup, _ = fs.GetBool("cleanup")
	o.Scale, _ = fs.GetInt("scale")
	o.Partitions, _ = fs.GetInt("partitions")
	o.PartitionMethod, _ = fs.GetString("partition-method")
	o.Fillfactor, _ = fs.GetInt("fillfactor")
	o.ForeignKeys, _ = fs.GetBool("foreign-keys")
	o.NoVacuum, _ = fs.GetBool("no-vacuum")
	o.Clients, _ = fs.GetInt("clients")
	o.Threads, _ = fs.GetInt("threads")
	o.Time, _ = fs.GetInt("time")
	o.Transactions, _ = fs.GetInt("transactions")
	o.Mode, _ = fs.GetString("mode")
	o.SelectOnly, _ = fs.GetBool("select-only")
	o.SimpleUpdate, _ = fs.GetBool("simple-update")
	o.Builtin, _ = fs.GetStringArray("builtin")
	o.Files, _ = fs.GetStringArray("file")
	o.Define, _ = fs.GetStringArray("define")
	o.Connect, _ = fs.GetBool("connect")
	o.Rate, _ = fs.GetFloat64("rate")
	o.LatencyLimit, _ = fs.GetInt("latency-limit")
	o.Progress, _ = fs.GetInt("progress")
	o.PerCommand, _ = fs.GetBool("per-command-report")
	o.Log, _ = fs.GetBool("log")
	o.LogDir, _ = fs.GetString("log-dir")
	return o
}
