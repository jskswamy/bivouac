package tui

import (
	"context"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	term "github.com/charmbracelet/x/term"

	"github.com/jskswamy/cloudlab/internal/provider"
)

// isTerminal is a var, not a call, so tests can override it without
// needing a real TTY.
var isTerminal = func() bool { return term.IsTerminal(os.Stdout.Fd()) }

// Run calls fn, rendering whatever it writes to provider.Output's
// writers (see provider.WithOutput) inside a collapsible viewport
// under label: expanded while fn runs, collapsed to a single
// checkmark line if fn returns nil, left expanded (showing where it
// failed) otherwise. fn's own return value is Run's return value.
//
// Falls back to calling fn directly, with no TUI, when stdout isn't a
// terminal -- bubbletea needs a real TTY, and piped/CI output should
// stay plain text.
func Run(ctx context.Context, label string, fn func(context.Context) error) error {
	if !isTerminal() {
		return fn(ctx)
	}

	p := tea.NewProgram(newModel(label))
	ctx = provider.WithOutput(ctx, &programWriter{p}, &programWriter{p})

	progDone := make(chan error, 1)
	go func() {
		_, err := p.Run()
		progDone <- err
	}()

	fnErr := fn(ctx)
	p.Send(doneMsg{err: fnErr})

	if progErr := <-progDone; progErr != nil {
		return progErr
	}
	return fnErr
}

// programWriter adapts a *tea.Program into an io.Writer: each Write
// becomes one outputMsg sent to the program.
type programWriter struct {
	program *tea.Program
}

func (w *programWriter) Write(p []byte) (int, error) {
	w.program.Send(outputMsg(string(p)))
	return len(p), nil
}
