package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

func init() {
	rootCmd.AddCommand(listCmd)
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all pg instances",
	Long:  `list shows all configured instances and their container status.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := cfgPath
		if path == "" {
			path = platform.DefaultConfigPath()
		}

		if _, err := os.Stat(path); os.IsNotExist(err) {
			return fmt.Errorf("config file not found: %s", path)
		}

		cfg, err := config.Load(path)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if len(cfg.Instances) == 0 {
			fmt.Println("No instances configured.")
			fmt.Printf("Run: pg config init --add <name>\n")
			return nil
		}

		fmt.Printf("%-12s %-30s\n", "NAME", "STATUS")
		fmt.Println(strings.Repeat("-", 43))

		for name := range cfg.Instances {
			// Work on a per-instance view of the config.
			if err := cfg.SetInstance(name); err != nil {
				printRow(name, "config error")
				continue
			}

			pm, err := podman.New(cfg)
			if err != nil {
				printRow(name, friendlyError(err))
				continue
			}

			cs, err := pm.Status()
			if err != nil {
				printRow(name, friendlyError(err))
				continue
			}

			printRow(name, cs.Status)
		}

		return nil
	},
}

func printRow(name, status string) {
	if len(status) > 28 {
		status = status[:25] + "..."
	}
	fmt.Printf("%-12s %-30s\n", name, status)
}

func friendlyError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "not installed"):
		return "podman not installed"
	case strings.Contains(msg, "podman service"):
		return "service unavailable"
	case strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "cannot connect"):
		return "daemon not running"
	case strings.Contains(msg, "system migrate"):
		return "need podman system migrate"
	default:
		// Strip wrapper prefixes to show the root cause.
		msg = strings.TrimPrefix(msg, "querying container status: ")
		// "podman <args>: <real error>" → extract real error after ": ".
		if _, after, ok := strings.Cut(msg, ": "); ok && strings.HasPrefix(msg, "podman ") {
			msg = after
		}
		// Extract msg="..." from logrus-style stderr.
		if m := extractLogrusMsg(msg); m != "" {
			msg = m
		}
		if len(msg) > 28 {
			msg = msg[:25] + "..."
		}
		return msg
	}
}

// extractLogrusMsg parses a logrus-style log line and returns the msg value.
// e.g. `time="..." level=error msg="some error"` → `some error`
func extractLogrusMsg(s string) string {
	_, after, ok := strings.Cut(s, `msg="`)
	if !ok {
		return ""
	}
	end := strings.LastIndex(after, `"`)
	if end == -1 {
		return after
	}
	return after[:end]
}
