package lifecycle

import (
	"testing"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

func TestTmuxArgs_BuildsExpectedCommand(t *testing.T) {
	got := tmuxArgs("203.0.113.5", "devuser", "main")
	inner := "tmux new-session -A -s " + reconcile.ShellQuote("main")
	want := []string{"-t", "devuser@203.0.113.5", "bash -lc " + reconcile.ShellQuote(inner)}
	if len(got) != len(want) {
		t.Fatalf("tmuxArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tmuxArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestTmuxArgs_QuotesSessionNameWithSpaces(t *testing.T) {
	got := tmuxArgs("203.0.113.5", "devuser", "my session")
	inner := "tmux new-session -A -s " + reconcile.ShellQuote("my session")
	want := []string{"-t", "devuser@203.0.113.5", "bash -lc " + reconcile.ShellQuote(inner)}
	if len(got) != len(want) {
		t.Fatalf("tmuxArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tmuxArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
