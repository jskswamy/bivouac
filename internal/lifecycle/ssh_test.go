package lifecycle

import "testing"

func TestSSHArgs_BuildsExpectedCommand(t *testing.T) {
	got := sshArgs("203.0.113.5", "devuser")
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
