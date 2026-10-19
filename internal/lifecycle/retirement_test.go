package lifecycle

import (
	"context"
	"errors"
	"fmt"
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

// A merge refused before it accepted any work has done nothing its recorded
// intent needs to protect, so it must not strand the session: a forced delete
// retires it. Seen when the work was applied by hand after merge refused.
func TestRetirementIntentThatDidNothingCanBeReplaced(t *testing.T) {
	store, _, _ := retirementCoordinatorFixture(t)
	var events []string
	withRetirementSeams(t, &events, "")
	allow := checkRetirementWork
	checkRetirementWork = func(policy retirementPolicy, ctx context.Context, record state.Record, sess state.Session, final bool) (state.Acceptance, []string, error) {
		if policy.operation == state.RetirementMerge {
			return state.Acceptance{}, nil, errors.New("merge refused")
		}
		return allow(policy, ctx, record, sess, final)
	}
	if _, err := RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementMerge}); err == nil {
		t.Fatal("merge was meant to be refused")
	}

	result, err := RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementDelete, Force: true})
	if err != nil || !result.Completed {
		t.Fatalf("forced delete after a refused merge: result=%+v err=%v", result, err)
	}
}

// Once work has been accepted the intent is what resumption relies on, so a
// different operation is still refused.
func TestRetirementIntentWithAcceptedWorkStaysImmutable(t *testing.T) {
	store, _, _ := retirementCoordinatorFixture(t)
	var events []string
	withRetirementSeams(t, &events, "cleanup failed")
	if _, err := RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementMerge}); err == nil {
		t.Fatal("merge was meant to pause in cleanup")
	}

	_, err := RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementDelete, Force: true})
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("delete over accepted merge work = %v, want the immutable-intent refusal", err)
	}
}

// A plain delete that passed its work check has accepted work by the time a
// cleanup step pauses it, so the untouched-intent rule no longer applies. Giving
// it --force is the user adding authority, not removing it, so it must still be
// possible until the cleanup effects begin. Seen live: an unforced delete paused
// on a Herdr workspace it could not prove was its own, and the advice to use
// --force was then refused as an immutable intent.
func TestUnforcedDeletePausedInCleanupCanBeUpgradedToForce(t *testing.T) {
	store, _, _ := retirementCoordinatorFixture(t)
	var events []string
	withRetirementSeams(t, &events, "")
	attempts := 0
	closeRetirementAttachment = func(context.Context, state.Record, state.HerdrAttachment) error {
		attempts++
		if attempts == 1 {
			return errors.New("herdr unreachable")
		}
		return nil
	}
	if _, err := RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementDelete}); err == nil {
		t.Fatal("the plain delete was meant to pause on the failed close")
	}

	result, err := RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementDelete, Force: true})
	if err != nil || !result.Completed {
		t.Fatalf("forced delete over a paused plain delete: result=%+v err=%v", result, err)
	}
}

// Authority only ever goes up. Once the cleanup effects have begun the intent
// is what resumption relies on, so even the upgrade is refused, and so is
// taking force away.
func TestRetirementForceIsNotUpgradedOnceEffectsHaveBegun(t *testing.T) {
	store, _, _ := retirementCoordinatorFixture(t)
	var events []string
	withRetirementSeams(t, &events, "cleanup failed")
	if _, err := RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementDelete}); err == nil {
		t.Fatal("the plain delete was meant to pause in cleanup")
	}

	_, err := RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementDelete, Force: true})
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("upgrade after effects began = %v, want the immutable-intent refusal", err)
	}
}

// The consumer can close or replace panes by hand, so ownership of a Herdr
// workspace may be unprovable. A forced delete then leaves the workspace open
// rather than blocking on it -- skipping a close can never close something
// that is not ours -- while an unforced delete stays paused.
func TestForcedDeleteSkipsAnUnprovableWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name  string
		force bool
		want  bool
	}{
		{"forced", true, true},
		{"unforced", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _, _ := retirementCoordinatorFixture(t)
			var events []string
			withRetirementSeams(t, &events, "")
			closeRetirementAttachment = func(context.Context, state.Record, state.HerdrAttachment) error {
				return fmt.Errorf("workspace w1 no longer matches: %w", errOwnershipUnproven)
			}
			result, err := RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementDelete, Force: tc.force})
			if tc.want && (err != nil || !result.Completed) {
				t.Fatalf("forced delete: result=%+v err=%v", result, err)
			}
			if !tc.want && err == nil {
				t.Fatal("an unforced delete went past an unprovable workspace")
			}
		})
	}
}

// Anything other than "cannot prove it is ours" still blocks, forced or not:
// a failed close of a workspace that is ours must not be papered over.
func TestForcedDeleteStillStopsOnACloseFailure(t *testing.T) {
	store, _, _ := retirementCoordinatorFixture(t)
	var events []string
	withRetirementSeams(t, &events, "")
	closeRetirementAttachment = func(context.Context, state.Record, state.HerdrAttachment) error {
		return errors.New("herdr unreachable")
	}
	if _, err := RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementDelete, Force: true}); err == nil {
		t.Fatal("a forced delete ignored a real close failure")
	}
}

// Paused on an unprovable workspace without --force, the way out is named.
func TestUnforcedDeletePausedOnAnUnprovableWorkspaceSaysHowToProceed(t *testing.T) {
	store, _, _ := retirementCoordinatorFixture(t)
	var events []string
	withRetirementSeams(t, &events, "")
	closeRetirementAttachment = func(context.Context, state.Record, state.HerdrAttachment) error {
		return fmt.Errorf("workspace w1 no longer matches: %w", errOwnershipUnproven)
	}
	_, err := RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementDelete})
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("RetireSession() = %v, want a message that names --force", err)
	}
}

// A plain delete refused by its guard records an unforced intent before it
// checks anything. That must not make --force useless afterwards: cherry-picked
// work always trips the guard, and --force is the only way past it.
func TestRefusedUnforcedDeleteLeavesTheSessionDeletableWithForce(t *testing.T) {
	store, _, _ := retirementCoordinatorFixture(t)
	var events []string
	withRetirementSeams(t, &events, "")
	allow := checkRetirementWork
	checkRetirementWork = func(policy retirementPolicy, ctx context.Context, record state.Record, sess state.Session, final bool) (state.Acceptance, []string, error) {
		if !policy.force {
			return state.Acceptance{}, nil, errors.New("4 commit(s) not on your branch")
		}
		return allow(policy, ctx, record, sess, final)
	}
	if _, err := RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementDelete}); err == nil {
		t.Fatal("the unforced delete was meant to be refused")
	}

	result, err := RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementDelete, Force: true})
	if err != nil || !result.Completed {
		t.Fatalf("forced delete after a refused plain delete: result=%+v err=%v", result, err)
	}
}

// The case the unforced error message points at: paused on a workspace that
// cannot be proved to be the session's own, then retried with --force as the
// message says. It must leave the workspace open and finish.
func TestUnforcedDeletePausedOnAnUnprovableWorkspaceFinishesWithForce(t *testing.T) {
	store, _, _ := retirementCoordinatorFixture(t)
	var events []string
	withRetirementSeams(t, &events, "")
	closeRetirementAttachment = func(context.Context, state.Record, state.HerdrAttachment) error {
		return fmt.Errorf("workspace w1 no longer matches: %w", errOwnershipUnproven)
	}
	if _, err := RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementDelete}); err == nil {
		t.Fatal("the plain delete was meant to pause on the unprovable workspace")
	}

	result, err := RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: state.RetirementDelete, Force: true})
	if err != nil || !result.Completed {
		t.Fatalf("--force after the pause the message recommends: result=%+v err=%v", result, err)
	}
}
