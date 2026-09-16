package lifecycle

import (
	"testing"

	"github.com/jskswamy/cloudlab/internal/shellcmd"
)

func TestTmuxArgs_BuildsExpectedCommand(t *testing.T) {
	got := tmuxArgs("203.0.113.5", "devuser", "main", false)
	inner := "tmux new-session -A -s " + shellcmd.Quote("main")
	want := []string{"-t", "devuser@203.0.113.5", shellcmd.LoginShell(inner)}
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
	got := tmuxArgs("203.0.113.5", "devuser", "my session", false)
	inner := "tmux new-session -A -s " + shellcmd.Quote("my session")
	want := []string{"-t", "devuser@203.0.113.5", shellcmd.LoginShell(inner)}
	if len(got) != len(want) {
		t.Fatalf("tmuxArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tmuxArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestTmuxArgs_ForwardAgent_PrependsDashA(t *testing.T) {
	got := tmuxArgs("203.0.113.5", "devuser", "main", true)
	if got[0] != "-A" {
		t.Fatalf("tmuxArgs()[0] = %q, want -A first", got[0])
	}
	inner := "tmux new-session -A -s " + shellcmd.Quote("main")
	want := []string{"-A", "-t", "devuser@203.0.113.5", shellcmd.LoginShell(inner)}
	if len(got) != len(want) {
		t.Fatalf("tmuxArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tmuxArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
