package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(stopCmd)
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop pg services",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := loadConfig(); err != nil {
			return err
		}

		pm, err := newPodman()
		if err != nil {
			return err
		}

		fmt.Println("-> Stopping pg services...")
		if err := pm.StopContainer(); err != nil {
			return err
		}

		fmt.Println("[OK] pg stopped")
		return nil
	},
}
