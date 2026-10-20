package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mars-base/pgcli/internal/tune"
)

// runWebTune writes the self-contained --web-tune site to path and, with --open,
// tries to launch it. The form is prefilled from a host probe (RAM/CPUs/OS); the
// target version defaults to 18 since the site runs standalone — no database is
// consulted. The page recomputes live in JS, guarded by embedded golden
// fixtures, so there is nothing else to plumb here.
func runWebTune(path string, doOpen bool, out io.Writer) error {
	prefill := tune.Inputs{
		RAMBytes: hostRAM(),
		CPUs:     hostCPUs(),
		OS:       probeOS(),
	}
	html, err := tune.WebHTML(prefill)
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("tune: cannot write web site: %w", err)
	}
	if _, err := io.WriteString(f, html); err != nil {
		f.Close()
		return fmt.Errorf("tune: writing web site: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if doOpen {
		if err := openBrowser(abs); err != nil {
			fmt.Fprintf(out, "wrote %s (open it manually — browser launch failed: %v)\n", abs, err)
			return nil
		}
		fmt.Fprintf(out, "opened %s in the browser\n", abs)
		return nil
	}
	fmt.Fprintf(out, "wrote interactive tuning site to %s\n", abs)
	return nil
}
