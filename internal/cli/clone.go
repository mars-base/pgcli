package cli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

// progressWriter wraps a pipe writer and reports transferred bytes to
// stderr once per second, so long clones show liveness.
type progressWriter struct {
	w    io.Writer
	done int64
	last time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.done += int64(n)
	if time.Since(p.last) >= time.Second {
		fmt.Fprintf(os.Stderr, "\r  Transferred: %s", formatBytes(p.done))
		p.last = time.Now()
	}
	return n, err
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

var cloneBaseDir string

func init() {
	cloneCmd.Flags().String("dsn", "", "source database connection string for remote clone (postgres://user:pass@host:port/db)")
	cloneCmd.Flags().StringVar(&cloneBaseDir, "base-dir", "", "custom base directory for the new instance (overrides config base_dir)")
	rootCmd.AddCommand(cloneCmd)
}

var cloneCmd = &cobra.Command{
	Use:   "clone <new-instance-name>",
	Short: "Clone an instance into a new one via logical dump",
	Long: `Clone copies data from a source instance (or remote database via --dsn)
into a newly created instance, streamed as a pg_dump | pg_restore pipe.

The new instance gets a random password and its own container, data directory
and port. The source instance must be running.

Examples:
  pg clone test02                          # clone the default instance
  pg clone test02 -i proj01                # clone a specific instance
  pg clone test02 --dsn postgres://user:pass@host:5432/db  # clone a remote database`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dsn, _ := cmd.Flags().GetString("dsn")
		if dsn != "" {
			if err := checkDSNInstanceConflict(cmd); err != nil {
				return err
			}
		}
		return runClone(args[0], dsn)
	},
}

func runClone(newName, dsn string) error {
	// Config file must exist
	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s -- run \"pg config init\" first", path)
	}

	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Target instance must not exist yet
	if _, ok := cfg.Instances[newName]; ok {
		return fmt.Errorf("instance %q already exists in config", newName)
	}

	// Pre-check source connectivity BEFORE creating or starting anything,
	// so a bad source leaves no side effects behind.
	sourceDesc := cfgInstance
	var sourcePM *podman.Manager
	var sourceDB string
	if dsn != "" {
		sourcePM, err = podman.New(cfg)
		if err != nil {
			return err
		}
		fmt.Printf("Checking source connectivity...\n")
		if err := sourcePM.CheckDSNReachable(dsn); err != nil {
			return err
		}
		fmt.Printf("Checking source dump privileges...\n")
		if err := sourcePM.CheckDSNDumpPrivilege(dsn); err != nil {
			return err
		}
		sourceDesc = dsn
	} else {
		if _, ok := cfg.Instances[cfgInstance]; !ok {
			return fmt.Errorf("source instance %q not found in config", cfgInstance)
		}
		srcCfg := *cfg
		if err := srcCfg.SetInstance(cfgInstance); err != nil {
			return err
		}
		sourcePM, err = podman.New(&srcCfg)
		if err != nil {
			return err
		}
		fmt.Printf("Checking source connectivity...\n")
		if err := sourcePM.CheckContainerRunning(); err != nil {
			return err
		}
		sourceDB = srcCfg.Postgres.Database
	}

	// Create the new instance in config (same logic as pg create)
	password, err := generatePassword(16)
	if err != nil {
		return fmt.Errorf("failed to generate password: %w", err)
	}
	origBaseDir := cfg.BaseDir
	if cloneBaseDir != "" {
		cfg.BaseDir = cloneBaseDir
	}
	inst := cfg.InstanceDefaults(newName)
	inst.Postgres.Database = newName + "_db"
	inst.Postgres.Password = password
	cfg.BaseDir = origBaseDir
	cfg.Instances[newName] = *inst
	cfg.ApplyDefaults()
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("Created instance %q\n", newName)

	// startSingle/doStart persist config via the global cfgPath
	cfgPath = path

	// Start the new instance
	if err := startSingle(cfg, newName); err != nil {
		return fmt.Errorf("starting new instance %q: %w", newName, err)
	}

	tgtCfg := *cfg
	if err := tgtCfg.SetInstance(newName); err != nil {
		return err
	}
	targetPM, err := podman.New(&tgtCfg)
	if err != nil {
		return err
	}

	// Stream: source -> pipe -> target
	pr, pw := io.Pipe()
	prog := &progressWriter{w: pw, last: time.Now()}
	exportErr := make(chan error, 1)
	go func() {
		if dsn != "" {
			exportErr <- sourcePM.ExportFromDSNPipe(prog, dsn)
		} else {
			exportErr <- sourcePM.ExportDatabasePipe(prog, sourceDB)
		}
		pw.Close()
	}()

	fmt.Printf("Cloning %s -> %s...\n", sourceDesc, newName)
	importErr := targetPM.ImportDatabasePipe(pr, tgtCfg.Postgres.Database)
	pr.Close()
	werr := <-exportErr
	fmt.Fprintf(os.Stderr, "\r  Transferred: %s    \n", formatBytes(prog.done))

	// Clone failure leaves the target instance behind (it was created and
	// started before the transfer). Tell the user how to retry cleanly.
	cloneFailureHint := func() {
		fmt.Fprintf(os.Stderr, "\n!  Clone instance %q was created but is incomplete. If the failure\n", newName)
		fmt.Fprintf(os.Stderr, "   is caused by extensions, permissions or data issues, fix the source\n")
		fmt.Fprintf(os.Stderr, "   (e.g. install the missing extension) and run:\n")
		fmt.Fprintf(os.Stderr, "     pg destroy -i %s --clean-data --force\n", newName)
		if dsn != "" {
			fmt.Fprintf(os.Stderr, "   then retry: pg clone %s --dsn \"...\"\n", newName)
		} else {
			fmt.Fprintf(os.Stderr, "   then retry: pg clone %s -i %s\n", newName, sourceDesc)
		}
	}

	if importErr != nil {
		cloneFailureHint()
		return fmt.Errorf("clone failed while importing into %q: %w", newName, importErr)
	}
	if werr != nil {
		cloneFailureHint()
		return fmt.Errorf("clone failed while exporting from source: %w", werr)
	}

	fmt.Printf("✓ Clone complete: %s -> %s\n", sourceDesc, newName)
	fmt.Printf("  database:   %s\n", inst.Postgres.Database)
	fmt.Printf("  password:   %s\n", inst.Postgres.Password)
	fmt.Printf("  container:  %s\n", inst.Podman.ContainerName)
	fmt.Printf("  port:       %d\n", cfg.Podman.HostPort)
	return nil
}
