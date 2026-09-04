// Package tui renders a long-running subprocess's live output inside a
// collapsible, bounded viewport instead of letting it scroll the whole
// terminal: expanded while running, collapsed to a single checkmark
// line on success, left expanded on failure so the user can see where
// it went wrong. Falls back to plain passthrough when stdout isn't a
// terminal.
package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// viewportHeight bounds how many lines of output are visible at once
// while running or after a failure -- a "bounded, scrollable pane",
// not an unbounded dump of everything the subprocess ever printed.
const viewportHeight = 15

// outputMsg is one Write() call's worth of raw subprocess output.
type outputMsg string

// doneMsg signals the wrapped function has returned, with its error
// (nil on success). Triggers the model to quit and render its final
// collapsed-or-expanded state.
type doneMsg struct{ err error }

type model struct {
	label    string
	lines    []string
	viewport viewport.Model
	ready    bool
	done     bool
	err      error
}

func newModel(label string) model {
	return model{label: label}
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if !m.ready {
			m.viewport = viewport.New(msg.Width, viewportHeight)
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
		}
		m.syncViewport()
	case outputMsg:
		for line := range strings.SplitSeq(strings.TrimRight(string(msg), "\n"), "\n") {
			// Tools like nix redraw a spinner line in place with bare \r
			// instead of \n; only the text after the last \r is what a
			// real terminal would actually show, so keep just that.
			if i := strings.LastIndexByte(line, '\r'); i >= 0 {
				line = line[i+1:]
			}
			m.lines = append(m.lines, line)
		}
		m.syncViewport()
	case doneMsg:
		m.done = true
		m.err = msg.err
		return m, tea.Quit
	}
	return m, nil
}

func (m *model) syncViewport() {
	if !m.ready {
		return
	}
	m.viewport.SetContent(strings.Join(m.lines, "\n"))
	m.viewport.GotoBottom()
}

func (m model) View() string {
	if m.done && m.err == nil {
		return "✓ " + m.label + "\n"
	}
	header := "⏳ " + m.label + "\n"
	if !m.ready {
		return header
	}
	return header + m.viewport.View() + "\n"
}
