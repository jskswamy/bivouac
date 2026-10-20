package cmd

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/bivouac/internal/lifecycle"
	"github.com/jskswamy/bivouac/internal/state"
)

func TestSessionGenerationSurvivesStartRetry(t *testing.T) {
	repo := newTestRepo(t)
	record := state.Record{Name: "instance", IP: "127.0.0.1:1", User: "devuser"}
	store := sessionTestStore(t, record)
	cmd := sessionTestCmd()
	if err := cmd.Flags().Set("repo", repo); err != nil {
		t.Fatal(err)
	}
	if err := runSessionStart(cmd, "instance", []string{"auth"}); err == nil {
		t.Fatal("first start unexpectedly succeeded")
	}
	first, _, err := store.Get("instance")
	if err != nil {
		t.Fatal(err)
	}
	sess, ok := first.FindSession("auth")
	if !ok || sess.Generation == "" || !sess.AttachmentsKnown {
		t.Fatalf("first start session = %+v, %v", sess, ok)
	}
	generation := sess.Generation
	sess.Attachments = []state.HerdrAttachment{{
		ID: "owned", Kind: state.AttachmentWorkspace,
		Address: state.HerdrAddress{MachineID: "mid", Session: "default", ConfigRoot: "~/.config/herdr"},
		Handle:  "w1", Owned: true, Phase: state.AttachmentActive,
		Proof: state.OwnershipProof{Scheme: "herdr-workspace-root-terminal-v1", Data: json.RawMessage(`{"terminalId":"term"}`)},
	}}
	first.PutSession(sess)
	if err := store.Put(first); err != nil {
		t.Fatal(err)
	}

	if err := runSessionStart(cmd, "instance", []string{"auth"}); err == nil {
		t.Fatal("retry unexpectedly succeeded")
	}
	got, _, err := store.Get("instance")
	if err != nil {
		t.Fatal(err)
	}
	retried, ok := got.FindSession("auth")
	if !ok || retried.Generation != generation {
		t.Fatalf("retry generation = %q, want %q", retried.Generation, generation)
	}
	if len(retried.Attachments) != 1 || retried.Attachments[0].ID != "owned" {
		t.Fatalf("retry erased lifecycle state: %+v", retried)
	}
}

func TestRecreatedSessionDoesNotUseOldReceipt(t *testing.T) {
	// Receipts written by the CLI use the resolved repository root, including
	// macOS's /var -> /private/var symlink.
	repo, err := filepath.EvalSymlinks(newTestRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	oldGeneration := "00112233445566778899aabbccddeeff"
	record := state.Record{
		Name: "instance", IP: "127.0.0.1:1", User: "devuser",
		RetirementReceipts: []state.RetirementReceipt{{
			Session: "auth", Generation: oldGeneration, LocalRepo: repo,
			Operation: state.RetirementMerge, CompletedAt: time.Now(),
		}},
	}
	store := sessionTestStore(t, record)
	cmd := sessionTestCmd()
	if err := cmd.Flags().Set("repo", repo); err != nil {
		t.Fatal(err)
	}
	if err := runSessionStart(cmd, "instance", []string{"auth"}); err == nil {
		t.Fatal("start unexpectedly succeeded")
	}
	got, _, err := store.Get("instance")
	if err != nil {
		t.Fatal(err)
	}
	active, ok := got.FindSession("auth")
	if !ok {
		t.Fatal("new active generation missing")
	}
	if active.Generation == "" || active.Generation == oldGeneration {
		t.Fatalf("active generation = %q, old = %q", active.Generation, oldGeneration)
	}
	if active.LocalRepo != repo {
		t.Fatalf("active repository = %q, want %q", active.LocalRepo, repo)
	}
	if _, ok := got.FindRetirementReceipt("auth", repo); ok {
		t.Fatal("old completion receipt survived active generation replacement")
	}
}

func TestCompletedRetirementCommandIsIdempotent(t *testing.T) {
	repo := newTestRepo(t)
	record := state.Record{
		Name: "instance", IP: "127.0.0.1:1", User: "devuser",
		RetirementReceipts: []state.RetirementReceipt{{
			Session: "auth", Generation: "00112233445566778899aabbccddeeff", LocalRepo: repo,
			Operation: state.RetirementMerge, CompletedAt: time.Now(),
		}},
	}
	sessionTestStore(t, record)
	cmd := sessionTestCmd()
	if err := runMerge(cmd, "wrong-cwd-derived-name", []string{"auth"}); err != nil {
		t.Fatalf("completed merge retry = %v", err)
	}
	if !strings.Contains(cmd.OutOrStdout().(*bytes.Buffer).String(), "already merged") {
		t.Fatal("completed retry did not report prior completion")
	}
}

func TestSessionStartRejectsPendingRetirement(t *testing.T) {
	repo := newTestRepo(t)
	record := state.Record{Name: "instance", IP: "127.0.0.1:1", User: "devuser"}
	record.PutSession(state.Session{
		Name: "auth", LocalRepo: repo, Base: "base", RepoName: "repo",
		Generation: "00112233445566778899aabbccddeeff", AttachmentsKnown: true,
		Retirement: &state.Retirement{
			Version: state.SessionLifecycleVersion, Operation: state.RetirementDelete,
			Phase:       state.RetirementPaused,
			Destination: state.Destination{LocalRepo: repo, GitDir: repo + "/.git", Branch: "main"},
			Effects:     map[string]state.RetirementEffect{},
		},
	})
	store := sessionTestStore(t, record)
	cmd := sessionTestCmd()
	if err := cmd.Flags().Set("repo", repo); err != nil {
		t.Fatal(err)
	}
	err := runSessionStart(cmd, "instance", []string{"auth"})
	if err == nil || !strings.Contains(err.Error(), "retirement") {
		t.Fatalf("runSessionStart() = %v, want pending retirement refusal", err)
	}
	got, _, getErr := store.Get("instance")
	if getErr != nil {
		t.Fatal(getErr)
	}
	sess, _ := got.FindSession("auth")
	if sess.Retirement == nil || sess.Generation != "00112233445566778899aabbccddeeff" {
		t.Fatalf("pending lifecycle was overwritten: %+v", sess)
	}
}

func TestRetirementLabelSaysWhenPausedOnIssues(t *testing.T) {
	got := retirementLabel(lifecycle.SessionInfo{
		Operation: "merge", RetirementPhase: "paused", PausedOn: state.PausedOnIssues,
		LastFailure: "issues not synced: a, b",
	})
	if !strings.HasPrefix(got, "merge paused: issues not synced") {
		t.Fatalf("label = %q", got)
	}
}

func hhmm(t *testing.T, s string) string {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v.Local().Format("15:04")
}

func TestPrintIssueReceipt(t *testing.T) {
	straggler := state.UnsyncedIssue{
		ID: "cloudlab-1em.6", Title: "Add retry to herdr add", Sync: "not-synced",
		InstanceUpdated: "2026-10-19T14:02:00Z", LocalUpdated: "2026-10-19T13:40:00Z",
	}
	missing := state.UnsyncedIssue{ID: "x", Title: "Gone", Sync: "not-synced", InstanceUpdated: "2026-10-19T14:02:00Z"}
	unknown := state.UnsyncedIssue{ID: "y", Title: "Who knows", Sync: "unknown", InstanceUpdated: "2026-10-19T14:02:00Z"}
	inst, local := hhmm(t, straggler.InstanceUpdated), hhmm(t, straggler.LocalUpdated)
	for _, tc := range []struct {
		name    string
		receipt state.IssueReceipt
		want    string
	}{
		{"all synced", state.IssueReceipt{Created: 3, Closed: 4, Updated: 2},
			"issues from pi: 3 created · 4 closed · 2 updated — all 9 synced ✓\n"},
		{"one straggler", state.IssueReceipt{Created: 3, Closed: 4, Updated: 2, Unsynced: []state.UnsyncedIssue{straggler}},
			"issues from pi: 3 created · 4 closed · 2 updated — 1 NOT synced\n" +
				"  ✗ cloudlab-1em.6  Add retry to herdr add  (instance " + inst + ", local " + local + ")\n"},
		{"missing and unknown", state.IssueReceipt{Updated: 2, Unsynced: []state.UnsyncedIssue{missing, unknown}},
			"issues from pi: 0 created · 0 closed · 2 updated — 2 NOT synced\n" +
				"  ✗ x  Gone  (instance " + inst + ", local missing)\n" +
				"  ✗ y  Who knows  (instance " + inst + ", local unknown)\n"},
		{"accepted", state.IssueReceipt{Updated: 1, Unsynced: []state.UnsyncedIssue{straggler}, Accepted: true},
			"issues from pi: 0 created · 0 closed · 1 updated — 1 NOT synced\n" +
				"  ✗ cloudlab-1em.6  Add retry to herdr add  (instance " + inst + ", local " + local + ") (accepted)\n"},
		{"removed counted", state.IssueReceipt{Created: 1, Removed: 1},
			"issues from pi: 1 created · 0 closed · 0 updated · 1 removed — all 2 synced ✓\n"},
		{"history rewritten, accepted", state.IssueReceipt{Accepted: true},
			"issues from pi: changes could not be listed — accepted unverified\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			r := tc.receipt
			printIssueReceipt(&buf, "pi", &r)
			if buf.String() != tc.want {
				t.Fatalf("got\n%s\nwant\n%s", buf.String(), tc.want)
			}
		})
	}
	var buf bytes.Buffer
	printIssueReceipt(&buf, "pi", nil)
	if buf.Len() != 0 {
		t.Fatalf("nil receipt printed %q", buf.String())
	}
}
