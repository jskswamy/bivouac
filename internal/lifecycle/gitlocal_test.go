package lifecycle

import (
	"net"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSSHGitURL_IsAnSSHURLNotAScpPath(t *testing.T) {
	got := sshGitURL("devuser", "203.0.113.5", "/home/devuser/sessions/auth/bivouac")
	want := "ssh://devuser@203.0.113.5/home/devuser/sessions/auth/bivouac"
	if got != want {
		t.Errorf("sshGitURL() = %q, want %q", got, want)
	}
}

// The Mac's branch can be called anything; on the instance it is always the
// session branch, so the refspec has to map one to the other.
func TestPushArgs_MapsTheLocalCommitOntoTheSessionBranch(t *testing.T) {
	got := pushArgs("ssh://devuser@203.0.113.5/sessions/auth/repo", "HEAD", "bivouac/auth")
	if got[0] != "push" {
		t.Errorf("pushArgs()[0] = %q, want push", got[0])
	}
	if !slices.Contains(got, "HEAD:refs/heads/bivouac/auth") {
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
	if got := sessionRemote("auth"); got != "bivouac-auth" {
		t.Errorf("sessionRemote() = %q, want bivouac-auth", got)
	}
}

// The whole point of a named remote is that plain git works against it.
func TestFetchRemoteArgs_IsAnOrdinaryFetch(t *testing.T) {
	got := fetchRemoteArgs("bivouac-auth")
	if got[0] != "fetch" || !slices.Contains(got, "bivouac-auth") {
		t.Errorf("fetchRemoteArgs() = %v, want a plain fetch of the named remote", got)
	}
}

// cherry-pick advances the checked-out branch. rebase --onto with a
// non-branch ref does not: it reports "up to date", re-signs nothing, and
// leaves HEAD detached while the user's branch never moves.
func TestCherryPickSignArgs_SignsAndTakesARange(t *testing.T) {
	got := cherryPickSignArgs("HEAD..bivouac-auth/bivouac/auth")
	if got[0] != "cherry-pick" {
		t.Errorf("cherryPickSignArgs()[0] = %q, want cherry-pick", got[0])
	}
	if !slices.Contains(got, "-S") {
		t.Errorf("cherryPickSignArgs() = %v, want -S so each replayed commit is signed", got)
	}
	if !slices.Contains(got, "HEAD..bivouac-auth/bivouac/auth") {
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

func TestDialTarget_UsesTheURLsPortOrSSHsDefault(t *testing.T) {
	tests := []struct {
		name, url, want string
	}{
		{"explicit port, as test fixtures use", "ssh://user@127.0.0.1:54321/repo", "127.0.0.1:54321"},
		{"no port, as a real instance's URL never has one", "ssh://user@203.0.113.5/repo", "203.0.113.5:22"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dialTarget(tc.url)
			if err != nil {
				t.Fatalf("dialTarget(%q) error = %v", tc.url, err)
			}
			if got != tc.want {
				t.Errorf("dialTarget(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

func TestDialTarget_RejectsAURLWithNoHost(t *testing.T) {
	if _, err := dialTarget("ssh:///repo"); err == nil {
		t.Error("dialTarget() error = nil for a URL with no host, want an error")
	}
}

// newListeningPort and newClosedPort give reachable a real answer to
// each question without touching the network: a bound-and-listening
// socket must accept a connection, and a port nothing is listening on
// (found the same way, then released) must refuse one immediately --
// no timeout to wait out either way.
func newListeningPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().String()
}

func newClosedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestReachable_TrueForAListeningPort(t *testing.T) {
	if !reachable(newListeningPort(t), time.Second) {
		t.Error("reachable() = false for a real listening port, want true")
	}
}

func TestReachable_FalseForAClosedPort(t *testing.T) {
	if reachable(newClosedPort(t), time.Second) {
		t.Error("reachable() = true for a closed port, want false")
	}
}

// The refused case above proves the false path but not the bound:
// refusal is immediate, so a probe with no timeout at all would still
// pass that test. A filtered/blackholed address (RFC 5737 TEST-NET-1)
// is what actually stalls, and this is what the fetch bound was
// added to fix -- see gitlocal.go's fastSSHCommand.
func TestReachable_FalseForABlackholedAddress_WithinBound(t *testing.T) {
	start := time.Now()
	if reachable("192.0.2.1:22", 2*time.Second) {
		t.Error("reachable() = true for a blackholed address, want false")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("reachable() took %s against a blackholed address, want it bounded by its timeout", elapsed)
	}
}

func TestChooseReachableAddress_PrefersStoredWhenBothAnswer(t *testing.T) {
	stored := "ssh://user@" + newListeningPort(t) + "/repo"
	fallback := "ssh://user@" + newListeningPort(t) + "/repo"

	storedOK, fallbackOK := chooseReachableAddress(stored, fallback, time.Second)
	if !storedOK {
		t.Error("chooseReachableAddress() storedOK = false, want true when the stored address answers")
	}
	_ = fallbackOK // both may legitimately be true; only storedOK gates the caller's choice
}

func TestChooseReachableAddress_FallsBackWhenStoredIsDead(t *testing.T) {
	stored := "ssh://user@" + newClosedPort(t) + "/repo"
	fallback := "ssh://user@" + newListeningPort(t) + "/repo"

	storedOK, fallbackOK := chooseReachableAddress(stored, fallback, time.Second)
	if storedOK {
		t.Error("chooseReachableAddress() storedOK = true for a closed port, want false")
	}
	if !fallbackOK {
		t.Error("chooseReachableAddress() fallbackOK = false, want true when the fallback answers")
	}
}

func TestChooseReachableAddress_NeitherAnswers(t *testing.T) {
	stored := "ssh://user@" + newClosedPort(t) + "/repo"
	fallback := "ssh://user@" + newClosedPort(t) + "/repo"

	storedOK, fallbackOK := chooseReachableAddress(stored, fallback, time.Second)
	if storedOK || fallbackOK {
		t.Errorf("chooseReachableAddress() = (%v, %v), want (false, false) when neither answers", storedOK, fallbackOK)
	}
}

// A remote that is a local filesystem path, not a network address --
// what this package's own real-fetch test fixtures use to get a real
// fetch without a real sshd -- has nothing to probe. Must be trusted
// as-is, not treated as unreachable just because it can't be dialed.
func TestChooseReachableAddress_TrustsANonNetworkStoredRemote(t *testing.T) {
	storedOK, fallbackOK := chooseReachableAddress("/some/local/path", "ssh://user@"+newListeningPort(t)+"/repo", time.Second)
	if !storedOK {
		t.Error("chooseReachableAddress() storedOK = false for a local-path remote, want true (trust it, do not probe)")
	}
	if fallbackOK {
		t.Error("chooseReachableAddress() fallbackOK = true, want the fallback never probed once the stored remote is trusted")
	}
}

// The whole reason to probe concurrently rather than one after the
// other: two dead addresses must not cost the sum of their timeouts.
func TestChooseReachableAddress_ProbesConcurrently(t *testing.T) {
	stored := "ssh://user@" + newClosedPort(t) + "/repo"
	fallback := "ssh://user@192.0.2.1/repo"

	start := time.Now()
	chooseReachableAddress(stored, fallback, 2*time.Second)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("chooseReachableAddress() took %s for two probes at a 2s bound, want them run concurrently (~2s), not in sequence (~4s)", elapsed)
	}
}
