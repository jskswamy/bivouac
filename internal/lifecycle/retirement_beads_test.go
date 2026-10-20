package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/beads"
	"github.com/jskswamy/bivouac/internal/state"
)

// These tests pin pullBeadsRequired's choice of branch, which is what keeps
// merge from deleting the only copy of a session's issues. The pieces it
// calls are tested on their own; what is tested here is that a start record,
// not beads.Wired, decides whether the issues are checked at all.

// fakeIssueClient is a scriptedBeads that can also be closed, standing in for
// the SSH connection pullBeadsRequired opens.
type fakeIssueClient struct {
	*scriptedBeads
	closed bool
}

func (f *fakeIssueClient) Close() error {
	f.closed = true
	return nil
}

// issueSeams replaces every outside dependency of pullBeadsRequired for one
// test and counts what was reached.
type issueSeams struct {
	client   *fakeIssueClient
	connects int
	pulls    int
	pullErr  error
}

func withIssueSeams(t *testing.T, outputs map[string]string, wired, available bool) *issueSeams {
	t.Helper()
	s := &issueSeams{client: &fakeIssueClient{scriptedBeads: &scriptedBeads{t: t, outputs: outputs}}}
	oldConnect, oldWired, oldAvailable, oldPull := connectIssues, beadsWired, beadsAvailable, pullIssues
	connectIssues = func(context.Context, string, string) (issueClient, error) {
		s.connects++
		return s.client, nil
	}
	beadsWired = func(context.Context, string, string) bool { return wired }
	beadsAvailable = func() bool { return available }
	pullIssues = func(context.Context, string, string, beadsRunner, string) (string, error) {
		s.pulls++
		if s.pullErr != nil {
			return "", s.pullErr
		}
		return "c1", nil
	}
	t.Cleanup(func() {
		connectIssues, beadsWired, beadsAvailable, pullIssues = oldConnect, oldWired, oldAvailable, oldPull
	})
	return s
}

var issueTestRecord = state.Record{Name: "repo", IP: "203.0.113.1", User: "dev"}

func recordedSession() state.Session {
	start := startRecord
	return state.Session{Name: "auth", LocalRepo: "/local", Beads: &start}
}

// No record and no wiring is a session that never had issues: it must not
// even connect, so repos without beads see no change.
func TestPullBeadsRequiredSkipsUnrecordedUnwiredSession(t *testing.T) {
	s := withIssueSeams(t, nil, false, true)
	evidence, receipt, err := pullBeadsRequired(context.Background(), issueTestRecord, state.Session{Name: "auth", LocalRepo: "/local"}, false, false)
	if err != nil || evidence != "pulled" || receipt != nil {
		t.Fatalf("evidence=%q receipt=%+v err=%v", evidence, receipt, err)
	}
	if s.connects != 0 || s.pulls != 0 {
		t.Fatalf("connects=%d pulls=%d, want none", s.connects, s.pulls)
	}
}

// A session from before start records keeps the old behaviour: pull, record
// "pulled", no receipt and no verification.
func TestPullBeadsRequiredUnrecordedWiredSessionPulls(t *testing.T) {
	s := withIssueSeams(t, nil, true, true)
	evidence, receipt, err := pullBeadsRequired(context.Background(), issueTestRecord, state.Session{Name: "auth", LocalRepo: "/local"}, false, false)
	if err != nil || evidence != "pulled" || receipt != nil {
		t.Fatalf("evidence=%q receipt=%+v err=%v", evidence, receipt, err)
	}
	if s.pulls != 1 || !s.client.closed {
		t.Fatalf("pulls=%d closed=%v", s.pulls, s.client.closed)
	}
}

// The spec's required case: a start record with no local bd pauses rather
// than skipping. Wired is false here too, as it is whenever bd is absent, so
// a gate on Wired alone would skip and merge would delete the issues.
func TestPullBeadsRequiredRecordWithoutLocalBdPauses(t *testing.T) {
	s := withIssueSeams(t, map[string]string{
		"vc status":   `{"commit":"c1"}`,
		"diff c0 c1 ": sessionDiff,
	}, false, false)
	withLocalIssues(t, nil, nil)
	_, _, err := pullBeadsRequired(context.Background(), issueTestRecord, recordedSession(), false, false)
	if !errors.Is(err, ErrIssuesUnsynced) {
		t.Fatalf("err = %v, want ErrIssuesUnsynced", err)
	}
	for _, id := range []string{"a", "b"} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("error %q does not name %s", err, id)
		}
	}
	if s.pulls != 0 {
		t.Fatalf("pulled %d time(s) with no local bd to pull into", s.pulls)
	}
}

// With a record and a local bd the pull goes through beads.Pull directly.
// pullBeads would ask Wired first and quietly skip when it says no, which is
// exactly what a start record has already ruled out.
func TestPullBeadsRequiredRecordPullsEvenWhenNotWired(t *testing.T) {
	s := withIssueSeams(t, map[string]string{"diff c0 c1 ": sessionDiff}, false, true)
	withLocalIssues(t, map[string]beads.Issue{
		"a": {ID: "a", UpdatedAt: at(t, "2026-10-19T14:02:00Z")},
		"b": {ID: "b", UpdatedAt: at(t, "2026-10-19T14:06:00Z")},
	}, nil)
	evidence, receipt, err := pullBeadsRequired(context.Background(), issueTestRecord, recordedSession(), false, false)
	if err != nil {
		t.Fatal(err)
	}
	if s.pulls != 1 {
		t.Fatalf("pulls = %d, want 1", s.pulls)
	}
	if evidence != "verified:c1" || receipt == nil || receipt.Created != 1 || receipt.Closed != 1 {
		t.Fatalf("evidence=%q receipt=%+v", evidence, receipt)
	}
}

// final has to reach the pause, so the late-work re-check says what is still
// intact on the instance.
func TestPullBeadsRequiredFinalPauseSaysWhatIsIntact(t *testing.T) {
	withIssueSeams(t, map[string]string{"diff c0 c1 ": sessionDiff}, true, true)
	withLocalIssues(t, map[string]beads.Issue{}, nil)
	_, _, err := pullBeadsRequired(context.Background(), issueTestRecord, recordedSession(), false, true)
	if !errors.Is(err, ErrIssuesUnsynced) || !strings.Contains(err.Error(), "still on the instance") {
		t.Fatalf("err = %v", err)
	}
}

// A failed pull leaves nothing to compare, so it pauses even with
// --accept-unsynced.
func TestPullBeadsRequiredPullFailureIgnoresAccept(t *testing.T) {
	s := withIssueSeams(t, nil, true, true)
	s.pullErr = errors.New("dolt push failed")
	_, _, err := pullBeadsRequired(context.Background(), issueTestRecord, recordedSession(), true, false)
	if !errors.Is(err, ErrIssuesUnsynced) {
		t.Fatalf("err = %v, want ErrIssuesUnsynced", err)
	}
}

// An unreachable instance is an unknown, never a skip.
func TestPullBeadsRequiredConnectFailurePauses(t *testing.T) {
	withIssueSeams(t, nil, true, true)
	connectIssues = func(context.Context, string, string) (issueClient, error) {
		return nil, errors.New("no route to host")
	}
	_, _, err := pullBeadsRequired(context.Background(), issueTestRecord, recordedSession(), false, false)
	if !errors.Is(err, ErrIssuesUnsynced) {
		t.Fatalf("err = %v, want ErrIssuesUnsynced", err)
	}
}
