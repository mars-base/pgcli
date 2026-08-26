package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// confirmPrompt asks the user for a yes/no confirmation on stdin. It returns
// true only for an affirmative answer (y, Y, yes, YES).
//
// It reads a full line with bufio.NewReader rather than fmt.Scanln, because
// fmt.Scanln can be unreliable in some terminal environments (it can return
// EOF or fail to register typed characters when the console input mode is not
// in the expected cooked/line-buffered state). Reading a line directly from
// os.Stdin works consistently across PowerShell, cmd.exe, and Unix terminals.
func confirmPrompt(prompt string) bool {
	fmt.Print(prompt)

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		// stdin closed or EOF without input -> treat as non-confirming.
		fmt.Println()
		return false
	}
	answer := strings.TrimSpace(line)
	switch strings.ToLower(answer) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
