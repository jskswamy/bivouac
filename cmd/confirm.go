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
	return readYes(cmd)
}

// readYes is confirm's answer half, for a caller that must put the
// prompt somewhere other than cmd's output -- stderr, when stdout is
// reserved for a --json result.
func readYes(cmd *cobra.Command) (bool, error) {
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}
