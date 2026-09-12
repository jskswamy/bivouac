package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	term "github.com/charmbracelet/x/term"
	"github.com/jskswamy/cloudlab/internal/lifecycle"
	"github.com/jskswamy/cloudlab/internal/state"
	"github.com/spf13/cobra"
)

// isInteractive reports whether stdin is a terminal.
//
// Guards every prompt. A picker offered to CI, a script, or an agent driving
// cloudlab is not a prompt -- it is a hang, and unattended runs are the
// workflow this tool exists to serve.
//
// term.IsTerminal, not a character-device check: /dev/null is a
// character device, so `cloudlab up < /dev/null` -- CI's usual shape,
// and `go test`'s -- read as interactive and opened a form that then
// failed on /dev/tty with an error naming huh rather than the command
// to run.
func isInteractive() bool {
	return term.IsTerminal(os.Stdin.Fd())
}

// readIndex reads one 1-based choice, validating it against max.
// Shared by the pickers so the parse-and-validate half lives once;
// each picker owns only how it renders its own candidates.
func readIndex(cmd *cobra.Command, max int) (int, error) {
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return 0, fmt.Errorf("reading choice: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > max {
		return 0, fmt.Errorf("%q is not one of 1-%d", strings.TrimSpace(line), max)
	}
	return n, nil
}

// pick prints a numbered list and reads one choice.
//
// A numbered prompt rather than a full TUI: it is a handful of lines, needs
// no dependency, and is testable by writing to a buffer. bubbletea is already
// available if this ever deserves to be prettier.
//
// Generic over the item because the three pickers differed in exactly two
// things -- the heading and how one row renders -- and in nothing else. The
// numbering, the "Which one? " prompt and the range check are the convention,
// and a fourth picker should not get to invent its own.
func pick[T any](cmd *cobra.Command, heading string, items []T, render func(T) string) (T, error) {
	cmd.Println(heading)
	for i, item := range items {
		cmd.Printf("  %d) %s\n", i+1, render(item))
	}
	cmd.Print("Which one? ")

	n, err := readIndex(cmd, len(items))
	if err != nil {
		var zero T
		return zero, err
	}
	return items[n-1], nil
}

// pickSession asks which of several live sessions was meant.
func pickSession(cmd *cobra.Command, candidates []string) (string, error) {
	return pick(cmd, "Several sessions are live:", candidates,
		func(name string) string { return name })
}

// pickListener asks which listening port was meant.
func pickListener(cmd *cobra.Command, listeners []lifecycle.Listener) (lifecycle.Listener, error) {
	return pick(cmd, "Listening on the instance:", listeners, func(l lifecycle.Listener) string {
		proc := l.Process
		if proc == "" {
			// A dash rather than an empty column, which would shift the
			// address left and break the alignment for that row alone.
			proc = "-"
		}
		return fmt.Sprintf("%-6d %-12s %s", l.Port, proc, l.Addr)
	})
}

// pickServeEntry asks which served port was meant.
func pickServeEntry(cmd *cobra.Command, entries []lifecycle.ServeEntry) (lifecycle.ServeEntry, error) {
	return pick(cmd, "Serving on the instance:", entries, func(e lifecycle.ServeEntry) string {
		return fmt.Sprintf("%-6d %s", e.Port, e.Forward)
	})
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
