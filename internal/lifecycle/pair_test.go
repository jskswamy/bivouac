package lifecycle

import (
	"testing"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

func TestPairArgs_BuildsExpectedCommand(t *testing.T) {
	got := pairArgs("203.0.113.5", "devuser")
	inner := "moshi-hook host setup --host " + reconcile.ShellQuote("203.0.113.5")
	want := []string{"-t", "devuser@203.0.113.5", "bash -lc " + reconcile.ShellQuote(inner)}
	if len(got) != len(want) {
		t.Fatalf("pairArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pairArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
