package cmd

import (
	"bufio"
	"errors"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// confirm prints prompt to cmd's output, reads one line from cmd's
// input, and reports whether it was "y" or "yes" (case-insensitive,
// surrounding whitespace ignored). Anything else -- including a blank
// line on bare Enter, or no input at all -- is treated as "no", so
// declining is always the safe default.
func confirm(cmd *cobra.Command, prompt string) (bool, error) {
	cmd.Print(prompt)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}
