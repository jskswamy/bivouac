package lifecycle

import (
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

func TestPairArgs_BuildsExpectedCommand(t *testing.T) {
	got := pairArgs("203.0.113.5", "203.0.113.5", "devuser")
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

// The QR may advertise the tailnet address while cloudlab itself still
// reaches the instance over the public one -- otherwise pairing would
// break whenever this machine happens to be off the tailnet.
func TestPairArgs_SSHesOverPublicWhileAdvertisingTailnet(t *testing.T) {
	got := pairArgs("203.0.113.5", "100.87.102.110", "devuser")

	if got[1] != "devuser@203.0.113.5" {
		t.Errorf("ssh target = %q, want the public address", got[1])
	}
	remote := got[len(got)-1]
	if !strings.Contains(remote, "100.87.102.110") {
		t.Errorf("remote command = %q, want --host advertising the tailnet address", remote)
	}
	if strings.Contains(remote, "203.0.113.5") {
		t.Errorf("remote command = %q, must not advertise the public address when a tailnet one was chosen", remote)
	}
}
