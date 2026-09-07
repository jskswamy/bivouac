package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jskswamy/cloudlab/internal/lifecycle"
	"github.com/jskswamy/cloudlab/internal/state"
	"github.com/spf13/cobra"
)

// isInteractive reports whether stdin is a terminal.
//
// Guards every prompt. A picker offered to CI, a script, or an agent driving
// cloudlab is not a prompt -- it is a hang, and unattended runs are the
// workflow this tool exists to serve.
func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && (fi.Mode()&os.ModeCharDevice) != 0
}

// pickSession prints a numbered list and reads one choice.
//
// A numbered prompt rather than a full TUI: it is a handful of lines, needs no
// dependency, and is testable by writing to a buffer. bubbletea is already
// available if this ever deserves to be prettier.
func pickSession(cmd *cobra.Command, candidates []string) (string, error) {
	cmd.Println("Several sessions are live:")
	for i, name := range candidates {
		cmd.Printf("  %d) %s\n", i+1, name)
	}
	cmd.Print("Which one? ")

	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("reading choice: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(candidates) {
		return "", fmt.Errorf("%q is not one of 1-%d", strings.TrimSpace(line), len(candidates))
	}
	return candidates[n-1], nil
}

// resolveSessionInteractive is resolveSessionArg for commands that are about
// to hand over the terminal. Ambiguity asks instead of refusing, which is
// continuous with what the user requested -- but only with a terminal to ask
// on.
func resolveSessionInteractive(cmd *cobra.Command, record state.Record, args []string) (state.Session, error) {
	sess, err := resolveSessionArg(cmd, record, args)
	if err == nil {
		return sess, nil
	}
	var amb *lifecycle.AmbiguousError
	if !errors.As(err, &amb) || !isInteractive() {
		return state.Session{}, err
	}
	chosen, perr := pickSession(cmd, amb.Candidates)
	if perr != nil {
		return state.Session{}, perr
	}
	return resolveSessionArg(cmd, record, []string{chosen})
}
