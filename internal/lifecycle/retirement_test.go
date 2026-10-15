package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/state"
	"github.com/jskswamy/bivouac/internal/testenv"
)

func retirementCoordinatorFixture(t *testing.T) (*state.Store, state.Record, state.Session) {
	t.Helper()
	testenv.Isolate(t)
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	proof := []byte(`{"paneId":"w1:p1","terminalId":"term"}`)
	sess := state.Session{
		Name: "auth", LocalRepo: repo, Base: gitOut(t, repo, "rev-parse", "HEAD"), RepoName: "repo",
		Generation: "00112233445566778899aabbccddeeff", AttachmentsKnown: true,
		Attachments: []state.HerdrAttachment{{
			ID: "owned", Kind: state.AttachmentWorkspace,
			Address: state.HerdrAddress{MachineID: "mid", Session: "default", ConfigRoot: herdrConfigRoot},
			Handle:  "w1", Proof: state.OwnershipProof{Scheme: workspaceTerminalProof, Data: proof},
			Owned: true, Phase: state.AttachmentActive,
		}},
	}
	record := state.Record{Name: "instance", IP: "host", User: "u", HerdrMachineID: "mid", Sessions: []state.Session{sess}}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}
	return store, record, sess
}

func withRetirementSeams(t *testing.T, events *[]string, failEffect string) {
	t.Helper()
	oldCheck, oldValidate := checkRetirementWork, validateRetirementWork
	oldClose, oldEffects := closeRetirementAttachment, runRetirementEffectsFn
	checkRetirementWork = func(_ retirementPolicy, _ context.Context, _ state.Record, sess state.Session, final bool) (state.Acceptance, []string, error) {
		stage := "initial"
		if final {
			stage = "final"
		}
		*events = append(*events, stage)
		head := gitOut(t, sess.LocalRepo, "rev-parse", "HEAD")
		return state.Acceptance{SourceTip: "source-" + stage, ReplayBase: head, VerifiedHead: head, IssueEvidence: "safe"}, []string{stage}, nil
	}
	validateRetirementWork = func(_ retirementPolicy, _ context.Context, _ state.Record, _ state.Session, _ state.Acceptance) error {
		*events = append(*events, "validate")
		return nil
	}
	closeRetirementAttachment = func(_ context.Context, _ state.Record, attachment state.HerdrAttachment) error {
		*events = append(*events, "close:"+attachment.ID)
		return nil
	}
	runRetirementEffectsFn = func(_ context.Context, store *state.Store, record state.Record, sess state.Session) error {
		*events = append(*events, "remove")
		if failEffect != "" {
			return errors.New(failEffect)
		}
		return updateRetirement(store, record.Name, sess.Generation, func(current *state.Session) error {
			for _, attachment := range current.Attachments {
				current.Retirement.Effects["herdr:"+attachment.ID] = state.RetirementEffect{Status: state.EffectDone}
			}
			for _, key := range retirementEffectOrder {
				current.Retirement.Effects[key] = state.RetirementEffect{Status: state.EffectDone}
			}
			return nil
		})
	}
	t.Cleanup(func() {
		checkRetirementWork, validateRetirementWork = oldCheck, oldValidate
		closeRetirementAttachment, runRetirementEffectsFn = oldClose, oldEffects
	})
}

func TestRetireSessionUsesOneWorkflowForMergeAndDelete(t *testing.T) {
	for _, tc := range []struct {
		name      string
		operation state.RetirementOperation
		force     bool
	}{
		{"merge", state.RetirementMerge, false},
		{"delete", state.RetirementDelete, false},
		{"forced delete", state.RetirementDelete, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _, _ := retirementCoordinatorFixture(t)
			var events []string
			withRetirementSeams(t, &events, "")
			result, err := RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: tc.operation, Force: tc.force})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Completed {
				t.Fatalf("result = %+v", result)
			}
			want := []string{"initial", "close:owned", "final", "validate", "remove"}
			if len(events) != len(want) {
				t.Fatalf("events = %v, want %v", events, want)
			}
			for i := range want {
				if events[i] != want[i] {
					t.Fatalf("events = %v, want %v", events, want)
				}
			}
		})
	}
}

func TestRetirementResumesEveryBoundary(t *testing.T) {
	store, _, _ := retirementCoordinatorFixture(t)
	var events []string
	withRetirementSeams(t, &events, "injected cleanup failure")
	result, err := RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementMerge})
	if err == nil || result.Completed {
		t.Fatalf("first result=%+v err=%v", result, err)
	}
	// Replace only the effect seam; durable close and acceptance evidence must
	// make the retry skip initial work and the already-closed attachment.
	runRetirementEffectsFn = func(_ context.Context, store *state.Store, record state.Record, sess state.Session) error {
		events = append(events, "remove-retry")
		return updateRetirement(store, record.Name, sess.Generation, func(current *state.Session) error {
			for _, attachment := range current.Attachments {
				current.Retirement.Effects["herdr:"+attachment.ID] = state.RetirementEffect{Status: state.EffectDone}
			}
			for _, key := range retirementEffectOrder {
				current.Retirement.Effects[key] = state.RetirementEffect{Status: state.EffectDone}
			}
			return nil
		})
	}
	result, err = RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementMerge})
	if err != nil || !result.Completed {
		t.Fatalf("retry result=%+v err=%v events=%v", result, err, events)
	}
	closeCount := 0
	for _, event := range events {
		if event == "close:owned" {
			closeCount++
		}
	}
	if closeCount != 1 {
		t.Fatalf("attachment closed %d times: %v", closeCount, events)
	}
}

func TestRetirementModeAndGenerationAreImmutable(t *testing.T) {
	store, _, _ := retirementCoordinatorFixture(t)
	var events []string
	withRetirementSeams(t, &events, "pause")
	_, _ = RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementDelete, Force: true})
	_, err := RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementDelete, Force: false})
	if err == nil {
		t.Fatal("forced intent was downgraded")
	}
}

func TestCompletedRetirementLeavesNoSessionResidue(t *testing.T) {
	f := newSessionFixture(t, 1)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	sess := state.Session{
		Name: f.session, LocalRepo: f.repo, Base: f.base, RepoName: f.repoName,
		Generation: "00112233445566778899aabbccddeeff", AttachmentsKnown: true,
	}
	other := state.Session{
		Name: "docs", LocalRepo: f.repo, Base: f.base, RepoName: f.repoName,
		Generation: "ffeeddccbbaa99887766554433221100", AttachmentsKnown: true,
	}
	record := state.Record{Name: f.repoName, IP: f.addr, User: "devuser", HerdrMachineID: "shared-machine", Sessions: []state.Session{sess, other}}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}
	result, err := RetireSession(context.Background(), store, record.Name, sess.Name, RetirementOptions{Operation: state.RetirementMerge})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed || !f.sessionRemoved() {
		t.Fatalf("result=%+v remoteRemoved=%t", result, f.sessionRemoved())
	}
	got, _, err := store.Get(record.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.FindSession(sess.Name); ok {
		t.Fatal("active session state remains")
	}
	if _, ok := got.FindSession(other.Name); !ok {
		t.Fatal("unrelated session was removed")
	}
	if got.HerdrMachineID != "shared-machine" {
		t.Fatal("shared Herdr machine registration was removed")
	}
	if len(got.RetirementReceipts) != 1 {
		t.Fatalf("receipts = %+v", got.RetirementReceipts)
	}
	if _, err := os.Stat(f.local); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("local worktree remains: %v", err)
	}
	if out, _ := runLocalGit(context.Background(), f.repo, "remote"); strings.Contains(out, sessionRemote(sess.Name)) {
		t.Fatalf("session remote remains: %s", out)
	}
	if out, _ := runLocalGit(context.Background(), f.repo, "branch", "--list", SessionBranch(sess.Name)); strings.TrimSpace(out) != "" {
		t.Fatalf("session branch remains: %s", out)
	}
	journal, err := replayProgressPath(context.Background(), f.repo, sess.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(journal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replay journal remains: %v", err)
	}
}

func TestMissingJournalEvidenceFailsClosed(t *testing.T) {
	store, _, _ := retirementCoordinatorFixture(t)
	var events []string
	withRetirementSeams(t, &events, "pause")
	_, _ = RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementMerge})
	if err := store.Update("instance", func(record *state.Record) error {
		sess, _ := record.FindSession("auth")
		sess.Retirement.Accepted = nil
		sess.Retirement.Phase = state.RetirementRemoving
		record.PutSession(sess)
		return nil
	}); err == nil {
		// Store validation itself is allowed to reject this impossible state.
		_, err = RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementMerge})
		if err == nil {
			t.Fatal("missing acceptance evidence completed")
		}
	}
}
