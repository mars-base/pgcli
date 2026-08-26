package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	// Injected by main via -ldflags
	Version   = "dev"
	BuildTime = "unknown"
)

func init() {
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("pg %s (built %s)\n", Version, BuildTime)
	},
}
