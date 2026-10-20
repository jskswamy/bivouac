package lifecycle

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/bivouac/internal/beads"
	"github.com/jskswamy/bivouac/internal/state"
)

// scriptedBeads answers instance bd commands by substring match on the
// unquoted command, and records what ran. A command nothing matches fails
// the test, so an unexpected bd call cannot pass silently.
type scriptedBeads struct {
	t       *testing.T
	outputs map[string]string
	errs    map[string]error
	ran     []string
}

func (s *scriptedBeads) Run(cmd string) (string, error) {
	s.t.Helper()
	cmd = strings.NewReplacer("'", "", `\`, "").Replace(cmd)
	s.ran = append(s.ran, cmd)
	for key, err := range s.errs {
		if strings.Contains(cmd, key) {
			return s.outputs[key], err
		}
	}
	best := ""
	for key := range s.outputs {
		if strings.Contains(cmd, key) && len(key) > len(best) {
			best = key
		}
	}
	if best == "" {
		s.t.Fatalf("unscripted command: %s", cmd)
	}
	return s.outputs[best], nil
}

func TestReadBeadsStartWithIssue(t *testing.T) {
	r := &scriptedBeads{t: t, outputs: map[string]string{
		"vc status":     `{"commit":"c0"}`,
		"show e ":       `[{"id":"e"}]`,
		"children e ":   `[{"id":"e.1"}]`,
		"children e.1 ": `[]`,
	}}
	got, err := ReadBeadsStart(r, "/repo", "e")
	if err != nil {
		t.Fatal(err)
	}
	want := state.SessionBeads{StartCommit: "c0", Issue: "e", Planned: []string{"e", "e.1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestReadBeadsStartWithoutIssue(t *testing.T) {
	r := &scriptedBeads{t: t, outputs: map[string]string{
		"vc status": `{"commit":"c0"}`,
	}}
	got, err := ReadBeadsStart(r, "/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.StartCommit != "c0" || got.Issue != "" || got.Planned != nil {
		t.Fatalf("got %+v", got)
	}
	if len(r.ran) != 1 {
		t.Fatalf("ran %v, want only vc status", r.ran)
	}
}

// withLocalIssues replaces the local bd list for one test. A nil map with a
// nil error stands for "bd must not be asked at all".
func withLocalIssues(t *testing.T, issues map[string]beads.Issue, err error) {
	t.Helper()
	old := localIssues
	localIssues = func(context.Context, string) (map[string]beads.Issue, error) {
		if issues == nil && err == nil {
			t.Fatal("local bd was consulted")
		}
		return issues, err
	}
	t.Cleanup(func() { localIssues = old })
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// sessionDiff is one created issue and one closed one, as bd diff prints them.
const sessionDiff = `[
 {"IssueID":"a","DiffType":"added","NewValue":{"id":"a","title":"New","status":"open","updated_at":"2026-10-19T14:02:00Z"}},
 {"IssueID":"b","DiffType":"modified","OldValue":{"id":"b","status":"open"},"NewValue":{"id":"b","title":"Done","status":"closed","updated_at":"2026-10-19T14:05:00Z"}}
]`

var startRecord = state.SessionBeads{StartCommit: "c0"}

func TestVerifyIssuesAllSynced(t *testing.T) {
	r := &scriptedBeads{t: t, outputs: map[string]string{"diff c0 c1 ": sessionDiff}}
	withLocalIssues(t, map[string]beads.Issue{
		"a": {ID: "a", UpdatedAt: at(t, "2026-10-19T14:02:00Z")},
		"b": {ID: "b", UpdatedAt: at(t, "2026-10-19T14:06:00Z")},
	}, nil)
	got, err := verifyIssues(context.Background(), r, "/local", "/repo", startRecord, "c1", true)
	if err != nil {
		t.Fatal(err)
	}
	want := state.IssueReceipt{Created: 1, Closed: 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestVerifyIssuesReportsStragglers(t *testing.T) {
	r := &scriptedBeads{t: t, outputs: map[string]string{"diff c0 c1 ": sessionDiff}}
	withLocalIssues(t, map[string]beads.Issue{
		"a": {ID: "a", UpdatedAt: at(t, "2026-10-19T14:02:00Z")},
		"b": {ID: "b", UpdatedAt: at(t, "2026-10-19T13:40:00Z")},
	}, nil)
	got, err := verifyIssues(context.Background(), r, "/local", "/repo", startRecord, "c1", true)
	if err != nil {
		t.Fatal(err)
	}
	want := []state.UnsyncedIssue{{
		ID: "b", Title: "Done", Sync: string(beads.NotSynced),
		InstanceUpdated: "2026-10-19T14:05:00Z", LocalUpdated: "2026-10-19T13:40:00Z",
	}}
	if !reflect.DeepEqual(got.Unsynced, want) {
		t.Fatalf("unsynced = %+v, want %+v", got.Unsynced, want)
	}
}

// A session with a start record and no usable local bd must come out
// unknown, never synced: merge pauses on unknown instead of skipping beads
// and deleting the only copy of the issues.
func TestVerifyIssuesWithoutLocalBdIsUnknown(t *testing.T) {
	r := &scriptedBeads{t: t, outputs: map[string]string{"diff c0 c1 ": sessionDiff}}
	withLocalIssues(t, nil, nil)
	got, err := verifyIssues(context.Background(), r, "/local", "/repo", startRecord, "c1", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Unsynced) != 2 {
		t.Fatalf("unsynced = %+v, want both changes", got.Unsynced)
	}
	for _, u := range got.Unsynced {
		if u.Sync != string(beads.Unknown) || u.LocalUpdated != "" {
			t.Fatalf("got %+v, want unknown with no local time", u)
		}
	}
}

// The agent may keep editing issues after the pull pinned its commit. Those
// edits belong to the next round, so verification must diff up to the pin
// and never re-read the instance's current commit.
func TestAgentEditAfterPinDoesNotPause(t *testing.T) {
	r := &scriptedBeads{t: t, outputs: map[string]string{
		"vc status":   `{"commit":"c9"}`,
		"diff c0 c1 ": `No changes between c0 and c1`,
		"diff c0 c9 ": sessionDiff,
	}}
	withLocalIssues(t, map[string]beads.Issue{}, nil)
	got, err := verifyIssues(context.Background(), r, "/local", "/repo", startRecord, "c1", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Unsynced) != 0 {
		t.Fatalf("unsynced = %+v, want none", got.Unsynced)
	}
	evidence, _, err := gateIssues("auth", got, nil, "c1", false, false)
	if err != nil || evidence != "verified:c1" {
		t.Fatalf("gate = %q, %v", evidence, err)
	}
	for _, cmd := range r.ran {
		if strings.Contains(cmd, "vc status") {
			t.Fatalf("verification read the live commit: %v", r.ran)
		}
	}
}

func TestVerifyIssuesPassesHistoryRewrittenThrough(t *testing.T) {
	r := &scriptedBeads{t: t,
		outputs: map[string]string{"diff c0 c1 ": `Error 1105: target commit not found`},
		errs:    map[string]error{"diff c0 c1 ": errors.New("exit 1")},
	}
	withLocalIssues(t, nil, nil)
	_, err := verifyIssues(context.Background(), r, "/local", "/repo", startRecord, "c1", true)
	if !errors.Is(err, beads.ErrHistoryRewritten) {
		t.Fatalf("err = %v, want ErrHistoryRewritten", err)
	}
}

// --accept-unsynced is permission to proceed past a gap, not a label: when
// nothing is missing the evidence must say verified.
func TestAcceptUnsyncedOnCleanMergeRecordsVerified(t *testing.T) {
	evidence, receipt, err := gateIssues("auth", state.IssueReceipt{Created: 2}, nil, "c1", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if evidence != "verified:c1" || receipt == nil || receipt.Accepted {
		t.Fatalf("evidence=%q receipt=%+v", evidence, receipt)
	}
}

func stragglers() state.IssueReceipt {
	return state.IssueReceipt{Updated: 2, Unsynced: []state.UnsyncedIssue{
		{ID: "a", Sync: "not-synced"}, {ID: "b", Sync: "unknown"},
	}}
}

func TestGateIssuesPausesOnStragglers(t *testing.T) {
	_, _, err := gateIssues("auth", stragglers(), nil, "c1", false, false)
	if !errors.Is(err, ErrIssuesUnsynced) {
		t.Fatalf("err = %v, want ErrIssuesUnsynced", err)
	}
	for _, want := range []string{"a, b", "bivouac session merge auth", "--accept-unsynced"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "still on the instance") {
		t.Errorf("first-check pause claims something was touched: %q", err)
	}
}

// After the replay and the closed panes nothing is untouched any more, so
// the message says what is still safe instead.
func TestGateIssuesFinalPauseSaysWhatIsIntact(t *testing.T) {
	_, _, err := gateIssues("auth", stragglers(), nil, "c1", false, true)
	if err == nil || !strings.Contains(err.Error(), "the session repository and its issue database are still on the instance") {
		t.Fatalf("err = %v", err)
	}
}

func TestGateIssuesAcceptsStragglers(t *testing.T) {
	evidence, receipt, err := gateIssues("auth", stragglers(), nil, "c1", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if evidence != "accepted-unsynced:c1:a,b" || receipt == nil || !receipt.Accepted || len(receipt.Unsynced) != 2 {
		t.Fatalf("evidence=%q receipt=%+v", evidence, receipt)
	}
}

func TestGateIssuesHistoryRewritten(t *testing.T) {
	_, _, err := gateIssues("auth", state.IssueReceipt{}, beads.ErrHistoryRewritten, "c1", false, false)
	if !errors.Is(err, ErrIssuesUnsynced) || !errors.Is(err, beads.ErrHistoryRewritten) {
		t.Fatalf("err = %v", err)
	}
	evidence, receipt, err := gateIssues("auth", state.IssueReceipt{}, beads.ErrHistoryRewritten, "c1", true, false)
	if err != nil || evidence != "accepted-unsynced:c1:" || receipt == nil || !receipt.Accepted {
		t.Fatalf("accepted: evidence=%q receipt=%+v err=%v", evidence, receipt, err)
	}
}

// A diff that failed for any other reason left nothing to compare, and the
// override does not reach past that.
func TestGateIssuesOtherFailureIgnoresAccept(t *testing.T) {
	_, _, err := gateIssues("auth", state.IssueReceipt{}, errors.New("ssh: broken pipe"), "c1", true, false)
	if !errors.Is(err, ErrIssuesUnsynced) {
		t.Fatalf("err = %v", err)
	}
}

func TestPullReceipt(t *testing.T) {
	r := &scriptedBeads{t: t, outputs: map[string]string{"diff c0 c1 ": sessionDiff}}
	withLocalIssues(t, map[string]beads.Issue{
		"a": {ID: "a", UpdatedAt: at(t, "2026-10-19T14:02:00Z")},
		"b": {ID: "b", UpdatedAt: at(t, "2026-10-19T14:06:00Z")},
	}, nil)
	got := pullReceipt(context.Background(), r, "/local", "/repo", startRecord, "c1", true)
	if got == nil || got.Created != 1 || got.Closed != 1 || len(got.Unsynced) != 0 {
		t.Fatalf("got %+v, want 1 created, 1 closed, all synced", got)
	}
}

// pull only reports, so a receipt it cannot build is a warning and no
// receipt, never a failed pull.
func TestPullReceiptFailureIsNil(t *testing.T) {
	r := &scriptedBeads{t: t,
		outputs: map[string]string{"diff c0 c1 ": `target commit not found`},
		errs:    map[string]error{"diff c0 c1 ": errors.New("exit 1")},
	}
	withLocalIssues(t, nil, nil)
	if got := pullReceipt(context.Background(), r, "/local", "/repo", startRecord, "c1", true); got != nil {
		t.Fatalf("got %+v, want nil", got)
	}
}
