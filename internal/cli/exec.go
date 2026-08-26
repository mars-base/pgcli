package cli

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(execCmd)
}

var execCmd = &cobra.Command{
	Use:   "exec [flags] -- <command> [args...]",
	Short: "Execute a command inside the PostgreSQL container",
	Long: `exec runs a command inside the running PostgreSQL container via podman exec.
Use -- to separate pg flags from the container command.

Examples:
  pg exec -- psql -U pgcli_user -d pgcli_db -c "SELECT 1"
  pg exec -i myinst -- pg_isready
  pg exec -- bash -c "cat /var/lib/postgresql/data/postgresql.conf"`,
	DisableFlagParsing: false,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := loadConfig(); err != nil {
			return err
		}
		if len(args) == 0 {
			return fmt.Errorf("no command specified. Usage: pg exec -- <command> [args...]")
		}

		containerName := cfg.Podman.ContainerName

		podmanArgs := []string{"exec", "-i", containerName}
		podmanArgs = append(podmanArgs, args...)

		podmanBin := "podman"
		execCmd := exec.Command(podmanBin, podmanArgs...)
		execCmd.Stdin = os.Stdin
		execCmd.Stdout = os.Stdout
		execCmd.Stderr = os.Stderr
		return execCmd.Run()
	},
}
