package lifecycle

import (
	"testing"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

func TestSSHArgs_NoDir_PlainSession(t *testing.T) {
	got := sshArgs("203.0.113.5", "devuser", "")
	want := []string{"devuser@203.0.113.5"}
	if len(got) != len(want) {
		t.Fatalf("sshArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sshArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSSHArgs_WithDir_CdsBeforeInteractiveShell(t *testing.T) {
	got := sshArgs("203.0.113.5", "devuser", "/home/devuser/project")
	inner := "[[ -d " + reconcile.ShellQuote("/home/devuser/project") + " ]] && cd " + reconcile.ShellQuote("/home/devuser/project") + "; exec \"$SHELL\" -l"
	want := []string{"-t", "devuser@203.0.113.5", "bash -lc " + reconcile.ShellQuote(inner)}
	if len(got) != len(want) {
		t.Fatalf("sshArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sshArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
