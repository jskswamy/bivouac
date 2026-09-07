package lifecycle

import (
	"slices"
	"strings"
	"testing"
)

func TestSSHGitURL_IsAnSSHURLNotAScpPath(t *testing.T) {
	got := sshGitURL("devuser", "203.0.113.5", "/home/devuser/sessions/auth/cloudlab")
	want := "ssh://devuser@203.0.113.5/home/devuser/sessions/auth/cloudlab"
	if got != want {
		t.Errorf("sshGitURL() = %q, want %q", got, want)
	}
}

// The Mac's branch can be called anything; on the instance it is always the
// session branch, so the refspec has to map one to the other.
func TestPushArgs_MapsTheLocalCommitOntoTheSessionBranch(t *testing.T) {
	got := pushArgs("ssh://devuser@203.0.113.5/sessions/auth/repo", "HEAD", "cloudlab/auth")
	if got[0] != "push" {
		t.Errorf("pushArgs()[0] = %q, want push", got[0])
	}
	if !slices.Contains(got, "HEAD:refs/heads/cloudlab/auth") {
		t.Errorf("pushArgs() = %v, want HEAD mapped onto the session branch", got)
	}
	// Never force: a non-fast-forward here means the instance has work we
	// have not seen, and silently overwriting it is the failure this whole
	// design exists to prevent.
	if slices.Contains(got, "--force") || slices.Contains(got, "-f") {
		t.Errorf("pushArgs() = %v, must never force-push", got)
	}
}

func TestSessionRemote_IsNamespacedPerSession(t *testing.T) {
	if got := sessionRemote("auth"); got != "cloudlab-auth" {
		t.Errorf("sessionRemote() = %q, want cloudlab-auth", got)
	}
}

// The whole point of a named remote is that plain git works against it.
func TestFetchRemoteArgs_IsAnOrdinaryFetch(t *testing.T) {
	got := fetchRemoteArgs("cloudlab-auth")
	if got[0] != "fetch" || !slices.Contains(got, "cloudlab-auth") {
		t.Errorf("fetchRemoteArgs() = %v, want a plain fetch of the named remote", got)
	}
}

// cherry-pick advances the checked-out branch. rebase --onto with a
// non-branch ref does not: it reports "up to date", re-signs nothing, and
// leaves HEAD detached while the user's branch never moves.
func TestCherryPickSignArgs_SignsAndTakesARange(t *testing.T) {
	got := cherryPickSignArgs("HEAD..cloudlab-auth/cloudlab/auth")
	if got[0] != "cherry-pick" {
		t.Errorf("cherryPickSignArgs()[0] = %q, want cherry-pick", got[0])
	}
	if !slices.Contains(got, "-S") {
		t.Errorf("cherryPickSignArgs() = %v, want -S so each replayed commit is signed", got)
	}
	if !slices.Contains(got, "HEAD..cloudlab-auth/cloudlab/auth") {
		t.Errorf("cherryPickSignArgs() = %v, want the range preserved", got)
	}
}

func TestSignatureStatusArgs_AsksGitForTheSignatureFlag(t *testing.T) {
	got := signatureStatusArgs("main..HEAD")
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "%G?") {
		t.Errorf("signatureStatusArgs() = %v, want the %%G? signature placeholder", got)
	}
}
