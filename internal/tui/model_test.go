package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

var errBoom = errors.New("boom")

func TestModel_View_ShowsSpinnerHeaderBeforeReady(t *testing.T) {
	m := newModel("Reconciling environment")
	if !strings.Contains(m.View(), "Reconciling environment") {
		t.Errorf("View() = %q, want it to contain the label", m.View())
	}
	if strings.Contains(m.View(), "✓") {
		t.Errorf("View() = %q, want no checkmark before anything completed", m.View())
	}
}

func TestModel_Update_AccumulatesOutputIntoViewport(t *testing.T) {
	m := newModel("Reconciling environment")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	updated, _ = m.Update(outputMsg("line one\nline two\n"))
	m = updated.(model)

	if !strings.Contains(m.View(), "line one") || !strings.Contains(m.View(), "line two") {
		t.Errorf("View() = %q, want it to contain the streamed lines", m.View())
	}
}

func TestModel_Update_CarriageReturnSpinner_KeepsOnlyFinalState(t *testing.T) {
	m := newModel("Reconciling environment")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	// Nix (and many CLIs) redraw a spinner line in place with bare \r,
	// not \n -- e.g. "unpacking '...' 10%\runpacking '...' 55%\runpacking '...' done\n".
	// Feeding that raw into the viewport leaves stray \r bytes that
	// corrupt the real terminal's cursor position on render.
	updated, _ = m.Update(outputMsg("unpacking 'foo' 10%\runpacking 'foo' 55%\runpacking 'foo' done\n"))
	m = updated.(model)

	if strings.Contains(m.View(), "\r") {
		t.Errorf("View() = %q, want no raw carriage returns reaching the terminal", m.View())
	}
	if !strings.Contains(m.View(), "unpacking 'foo' done") {
		t.Errorf("View() = %q, want only the final spinner state kept", m.View())
	}
	if strings.Contains(m.View(), "10%") || strings.Contains(m.View(), "55%") {
		t.Errorf("View() = %q, want intermediate spinner states dropped", m.View())
	}
}

func TestModel_Update_DoneWithNilErr_CollapsesToCheckmark(t *testing.T) {
	m := newModel("Reconciling environment")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	updated, _ = m.Update(outputMsg("lots of build output\n"))
	m = updated.(model)

	updated, cmd := m.Update(doneMsg{err: nil})
	m = updated.(model)

	if cmd == nil {
		t.Error("Update(doneMsg{}) cmd = nil, want tea.Quit")
	}
	view := m.View()
	if !strings.Contains(view, "✓") || !strings.Contains(view, "Reconciling environment") {
		t.Errorf("View() = %q, want a collapsed checkmark line", view)
	}
	if strings.Contains(view, "lots of build output") {
		t.Errorf("View() = %q, want the viewport collapsed away on success", view)
	}
}

func TestModel_Update_DoneWithErr_StaysExpanded(t *testing.T) {
	m := newModel("Reconciling environment")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	updated, _ = m.Update(outputMsg("error: attribute 'cli' missing\n"))
	m = updated.(model)

	updated, cmd := m.Update(doneMsg{err: errBoom})
	m = updated.(model)

	if cmd == nil {
		t.Error("Update(doneMsg{err}) cmd = nil, want tea.Quit")
	}
	view := m.View()
	if !strings.Contains(view, "error: attribute 'cli' missing") {
		t.Errorf("View() = %q, want the failing output still visible, not collapsed", view)
	}
	if strings.Contains(view, "✓") {
		t.Errorf("View() = %q, want no success checkmark on failure", view)
	}
}
