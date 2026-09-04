package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/jskswamy/cloudlab/internal/provider"
)

// Exercising Run's actual bubbletea-Program path needs a real TTY,
// which go test doesn't have -- isTerminal lets tests force the
// non-terminal fallback deterministically instead of depending on
// whatever file descriptor happens to back os.Stdout during a test
// run. The Program/viewport rendering itself is covered by
// model_test.go's Update/View tests, which don't need a real terminal
// at all.

func TestRun_NonTerminal_CallsFnDirectlyNoTUI(t *testing.T) {
	old := isTerminal
	isTerminal = func() bool { return false }
	t.Cleanup(func() { isTerminal = old })

	var gotCtx context.Context
	called := false
	err := Run(context.Background(), "Reconciling environment", func(ctx context.Context) error {
		called = true
		gotCtx = ctx
		return nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !called {
		t.Fatal("Run() did not call fn when stdout isn't a terminal")
	}
	if gotCtx == nil {
		t.Fatal("Run() passed a nil context to fn")
	}
}

func TestRun_NonTerminal_PropagatesFnError(t *testing.T) {
	old := isTerminal
	isTerminal = func() bool { return false }
	t.Cleanup(func() { isTerminal = old })

	want := errors.New("boom")
	err := Run(context.Background(), "Reconciling environment", func(ctx context.Context) error {
		return want
	})
	if err != want {
		t.Errorf("Run() error = %v, want %v", err, want)
	}
}

func TestRun_NonTerminal_DoesNotOverrideCallerProgress(t *testing.T) {
	old := isTerminal
	isTerminal = func() bool { return false }
	t.Cleanup(func() { isTerminal = old })

	// Mirrors TestRun_NonTerminal_DoesNotOverrideCallerOutput: with no
	// TUI pane to route into, ReportProgress calls inside fn must still
	// reach whatever progress callback the caller already attached.
	var got []string
	ctx := provider.WithProgress(context.Background(), func(status string) { got = append(got, status) })
	_ = Run(ctx, "label", func(ctx context.Context) error {
		provider.ReportProgress(ctx, "hello")
		return nil
	})
	if len(got) != 1 || got[0] != "hello" {
		t.Errorf("progress = %v, want [%q] passed through unchanged in the non-TTY fallback", got, "hello")
	}
}

func TestRun_NonTerminal_DoesNotOverrideCallerOutput(t *testing.T) {
	old := isTerminal
	isTerminal = func() bool { return false }
	t.Cleanup(func() { isTerminal = old })

	// In the non-TTY fallback, Run must not attach its own writers --
	// whatever provider.Output the caller already set up (or the
	// os.Stdout/os.Stderr default) must reach fn unchanged.
	ctx := provider.WithOutput(context.Background(), nil, nil)
	var gotOut, gotErrOut any
	_ = Run(ctx, "label", func(ctx context.Context) error {
		gotOut, gotErrOut = provider.Output(ctx)
		return nil
	})
	if gotOut != nil || gotErrOut != nil {
		t.Errorf("Output() = (%v, %v), want the caller's attached (nil, nil) to pass through untouched", gotOut, gotErrOut)
	}
}
