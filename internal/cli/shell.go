package cli

import (
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(shellCmd)
}

var shellCmd = &cobra.Command{
	Use:   "shell [flags] [-- <shell-args>...]",
	Short: "Open interactive shell in container",
	Long: `shell opens an interactive bash shell inside the PostgreSQL container.

Additional shell arguments are passed through after --.

Examples:
  pg shell                             # interactive bash
  pg shell -i proj01                   # specific instance
  pg shell -- -c "ls -la /var/lib/postgresql/data"
  pg shell -- -c "cat /etc/postgresql/postgresql.conf"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := loadConfig(); err != nil {
			return err
		}

		pm, err := newPodman()
		if err != nil {
			return err
		}

		// Default to bash shell
		shellArgs := []string{"bash"}
		shellArgs = append(shellArgs, args...)

		return pm.ExecInteractive(shellArgs...)
	},
}
