package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/state"
)

func retirementFixtureState(f *sessionFixture) (state.Record, state.Session) {
	sess := state.Session{
		Name: f.session, LocalRepo: f.repo, Base: f.base, RepoName: f.repoName,
		Generation: "00112233445566778899aabbccddeeff", AttachmentsKnown: true,
	}
	return state.Record{Name: f.repoName, IP: f.addr, User: "devuser", Sessions: []state.Session{sess}}, sess
}

func TestMergePolicyExtendsAcceptedTipAfterResolvedConflict(t *testing.T) {
	f := newSessionFixture(t, 1)
	record, sess := retirementFixtureState(f)
	policy := mergeRetirementPolicy()
	first, signed, err := policy.Check(context.Background(), record, sess, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(signed) != 1 {
		t.Fatalf("signed = %v", signed)
	}
	branch := gitOut(t, f.repo, "rev-parse", "--abbrev-ref", "HEAD")
	sess.Retirement = &state.Retirement{
		Version: state.SessionLifecycleVersion, Operation: state.RetirementMerge, Phase: state.RetirementFinalCheck,
		Destination: state.Destination{LocalRepo: f.repo, GitDir: f.repo + "/.git", Branch: branch},
		Accepted:    []state.Acceptance{first}, Effects: map[string]state.RetirementEffect{},
	}
	writeAndCommit(t, f.agent, "later.txt", "later", "later agent commit")
	second, laterSigned, err := policy.Check(context.Background(), record, sess, true)
	if err != nil {
		t.Fatal(err)
	}
	if second.SourceTip == first.SourceTip || second.VerifiedHead == first.VerifiedHead {
		t.Fatalf("late source was not extended: first=%+v second=%+v", first, second)
	}
	if len(laterSigned) != 1 {
		t.Fatalf("late signed = %v, want only the new commit", laterSigned)
	}
}

func TestRetirementRejectsDestinationRewindOrBranchSwitch(t *testing.T) {
	f := newSessionFixture(t, 1)
	record, sess := retirementFixtureState(f)
	policy := mergeRetirementPolicy()
	accepted, _, err := policy.Check(context.Background(), record, sess, false)
	if err != nil {
		t.Fatal(err)
	}
	branch := gitOut(t, f.repo, "rev-parse", "--abbrev-ref", "HEAD")
	sess.Retirement = &state.Retirement{
		Version: state.SessionLifecycleVersion, Operation: state.RetirementMerge, Phase: state.RetirementChecking,
		Destination: state.Destination{LocalRepo: f.repo, GitDir: f.repo + "/.git", Branch: branch},
		Effects:     map[string]state.RetirementEffect{},
	}
	mustGit(t, f.repo, "checkout", "-q", "-b", "other")
	if err := policy.Validate(context.Background(), record, sess, accepted); err == nil || !strings.Contains(err.Error(), "branch changed") {
		t.Fatalf("Validate() = %v, want branch refusal", err)
	}
}

func TestFinalGateFindsCloseTimeWrites(t *testing.T) {
	f := newSessionFixture(t, 0)
	record, sess := retirementFixtureState(f)
	policy := mergeRetirementPolicy()
	first, _, err := policy.Check(context.Background(), record, sess, false)
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, f.agent, "late.txt", "late", "late")
	second, _, err := policy.Check(context.Background(), record, sess, true)
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceTip == second.SourceTip {
		t.Fatal("final gate missed late remote work")
	}
}

func TestRequiredReplayJournalCorruptionFailsClosed(t *testing.T) {
	f := newSessionFixture(t, 1)
	record, sess := retirementFixtureState(f)
	path, err := replayProgressPath(context.Background(), f.repo, f.session)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mergeRetirementPolicy().Check(context.Background(), record, sess, false); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("Check() = %v, want corrupt-journal refusal", err)
	}
}

func TestDeletePolicyForceIsExplicitDiscardAuthority(t *testing.T) {
	f := newSessionFixture(t, 1)
	record, sess := retirementFixtureState(f)
	if _, _, err := deleteRetirementPolicy(false).Check(context.Background(), record, sess, false); err == nil {
		t.Fatal("ordinary delete accepted unmerged work")
	}
	accepted, _, err := deleteRetirementPolicy(true).Check(context.Background(), record, sess, false)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.SourceTip != "discard-authorised" {
		t.Fatalf("forced acceptance = %+v", accepted)
	}
}

// The live merge path must not widen its range when the base was rewritten:
// folding or re-signing the user's branch after another session merged is the
// ordinary way to get here.
func TestMergePolicyReplaysOnlySessionCommitsAfterBaseRewrite(t *testing.T) {
	f := newSessionFixture(t, 1)
	record, sess := retirementFixtureState(f)
	mustGit(t, f.repo, "commit", "--amend", "--allow-empty", "-m", "first, rewritten")

	_, signed, err := mergeRetirementPolicy().Check(context.Background(), record, sess, false)
	if err != nil {
		t.Fatalf("Check() = %v, want the session's commit replayed", err)
	}
	if len(signed) != 1 {
		t.Errorf("signed = %v, want only the session's own commit", signed)
	}
	if got := gitOut(t, f.repo, "rev-list", "--count", "HEAD"); got != "2" {
		t.Errorf("HEAD has %s commits, want 2 (rewritten base + the session's one)", got)
	}
}
