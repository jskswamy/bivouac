package lifecycle

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/beads"
	"github.com/jskswamy/cloudlab/internal/state"
)

// The headline data-loss regression. The local session branch only moves when
// pull fast-forwards it, so a session that was never pulled measures as empty
// however many commits the agent actually made -- and delete threw away every
// one of them with exit 0. The gate has to measure the instance, not the last
// thing that happened to be fetched into a branch.
func TestDeleteSession_RefusesWorkThatWasNeverPulled(t *testing.T) {
	f := newSessionFixture(t, 0)
	// Committed after the fixture's fetch, exactly like an agent working
	// overnight on a session nobody has pulled since it started.
	writeAndCommit(t, f.agent, "overnight.txt", "work", "agent commit nobody pulled")

	_, err := DeleteSession(context.Background(), f.addr, "devuser", f.repoName,
		state.Session{Name: f.session, LocalRepo: f.repo, Base: f.base}, false)
	if err == nil {
		t.Fatal("DeleteSession() = nil with unpulled work on the instance, want a refusal")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error = %q, want it to mention --force", err.Error())
	}
	if f.sessionRemoved() {
		t.Error("the instance-side session was removed despite the refusal")
	}
}

// delete discards work rather than landing it. Refusing while commits are
// unmerged is the difference between a cleanup command and a data-loss one.
func TestDeleteSession_RefusesWhileWorkIsUnmerged(t *testing.T) {
	f := newSessionFixture(t, 1)

	_, err := DeleteSession(context.Background(), f.addr, "devuser", f.repoName,
		state.Session{Name: f.session, LocalRepo: f.repo, Base: f.base}, false)
	if err == nil {
		t.Fatal("DeleteSession() = nil with unmerged work, want a refusal")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error = %q, want it to mention --force", err.Error())
	}
	if f.sessionRemoved() {
		t.Error("the instance-side session was removed despite the refusal")
	}
	if _, statErr := os.Stat(f.local); statErr != nil {
		t.Error("the local worktree was removed despite the refusal")
	}
}

func TestDeleteSession_RemovesBothSidesOnceMerged(t *testing.T) {
	f := newSessionFixture(t, 1)
	if _, err := f.merge(t); err != nil {
		t.Fatalf("merge to set up the merged state: %v", err)
	}
	// merge already removed it; recreate the local half to delete.
	mustGit(t, f.repo, remoteAddArgs(sessionRemote(f.session), f.agent)...)
	mustGit(t, f.repo, fetchRemoteArgs(sessionRemote(f.session))...)
	mustGit(t, f.repo, "worktree", "add", "--quiet", "--track", "-B", SessionBranch(f.session),
		f.local, sessionRemote(f.session)+"/"+SessionBranch(f.session))

	warning, err := DeleteSession(context.Background(), f.addr, "devuser", f.repoName,
		state.Session{Name: f.session, LocalRepo: f.repo, Base: f.base}, false)
	if err != nil {
		t.Fatalf("DeleteSession() error = %v, want deletion once the work has landed", err)
	}
	if warning != "" {
		t.Errorf("warning = %q, want none when both sides came down cleanly", warning)
	}
	if _, statErr := os.Stat(f.local); statErr == nil {
		t.Error("the local worktree survived the delete")
	}
	if out, err := runLocalGit(context.Background(), f.repo, "remote", "get-url", sessionRemote(f.session)); err == nil {
		t.Errorf("the remote survived the delete: %s", out)
	}
}

func TestDeleteSession_ForceDiscardsUnmergedWork(t *testing.T) {
	f := newSessionFixture(t, 1)

	_, err := DeleteSession(context.Background(), f.addr, "devuser", f.repoName,
		state.Session{Name: f.session, LocalRepo: f.repo, Base: f.base}, true)
	if err != nil {
		t.Fatalf("DeleteSession(force) error = %v", err)
	}
	if !f.sessionRemoved() {
		t.Error("--force did not remove the instance-side session")
	}
}

// Regression for a fail-open: DescribeSession is documented to never return
// an error, and its cherry check silently leaves Unmerged at 0 when git
// itself fails (branch deleted by hand, unreadable repo, corrupt index).
// Gating on Unmerged > 0 alone reads that as "nothing to lose" and deletes
// the only copy of possibly-unmerged work with exit 0. The gate must refuse
// when the count was never actually computed, not just when it is positive.
//
// DeleteSession rescues before it measures, so a LocalRepo that is not a git
// repository at all now fails in RescueSession and never reaches the cherry
// check this test is named for. To reach it, rescue must succeed -- the
// fixture's remote stays intact -- while only the second measurement, the
// local branch cloudlab/<session>, is made to fail: delete it (after
// removing the worktree that holds it checked out) and `git cherry HEAD
// cloudlab/<session>` has nothing to compare against.
func TestDeleteSession_RefusesWhenUnmergedStatusCannotBeDetermined(t *testing.T) {
	f := newSessionFixture(t, 0)

	mustGit(t, f.repo, "worktree", "remove", "--force", f.local)
	mustGit(t, f.repo, "branch", "-D", SessionBranch(f.session))
	if _, err := runLocalGit(context.Background(), f.repo, "rev-parse", "--verify", SessionBranch(f.session)); err == nil {
		t.Fatal("local branch cloudlab/auth survived deletion; fixture setup is broken")
	}

	sess := state.Session{Name: f.session, LocalRepo: f.repo, Base: f.base}

	_, err := DeleteSession(context.Background(), f.addr, "devuser", f.repoName, sess, false)
	if err == nil {
		t.Fatal("DeleteSession() = nil when git could not determine unmerged status, want a refusal")
	}
	if !strings.Contains(err.Error(), "cannot tell") {
		t.Errorf("error = %q, want it to say cloudlab could not tell whether work is unmerged", err.Error())
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error = %q, want it to mention --force", err.Error())
	}

	if _, err := DeleteSession(context.Background(), f.addr, "devuser", f.repoName, sess, true); err != nil {
		t.Fatalf("DeleteSession(force) error = %v, want --force to proceed despite the unknown status", err)
	}
}

// An unreachable instance must never become the cheap way onto the
// destructive path. The session here has nothing outstanding locally, so the
// old local-only gate waved it straight through and deleted a session whose
// contents nobody could see. Not knowing is a refusal, not a pass.
func TestDeleteSession_RefusesWhenTheInstanceCannotBeReached(t *testing.T) {
	f := newSessionFixture(t, 0)
	// Nothing listens here; connection is refused immediately rather than
	// hanging out the dial timeout.
	unreachable := "127.0.0.1:1"

	_, err := DeleteSession(context.Background(), unreachable, "devuser", f.repoName,
		state.Session{Name: f.session, LocalRepo: f.repo, Base: f.base}, false)
	if err == nil {
		t.Fatal("DeleteSession() = nil against an unreachable instance, want a refusal rather than a fall-through to the stale local measurement")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error = %q, want it to mention --force", err.Error())
	}
	if _, statErr := os.Stat(f.local); statErr != nil {
		t.Error("the local worktree was removed despite the refusal")
	}
	if out, err := runLocalGit(context.Background(), f.repo, "remote", "get-url", sessionRemote(f.session)); err != nil {
		t.Errorf("the remote was removed despite the refusal: %v %s", err, out)
	}
}

// A dead droplet must still not make a session permanently undeletable. With
// --force the local half goes and the caller gets a warning rather than an
// error, because an error here would strand the record entry -- and an entry
// whose local remote has already been torn down can never be rescued again.
func TestDeleteSession_ForcedAgainstAnUnreachableInstanceWarnsInsteadOfFailing(t *testing.T) {
	f := newSessionFixture(t, 1)
	unreachable := "127.0.0.1:1"

	warning, err := DeleteSession(context.Background(), unreachable, "devuser", f.repoName,
		state.Session{Name: f.session, LocalRepo: f.repo, Base: f.base}, true)
	if err != nil {
		t.Fatalf("DeleteSession(force) error = %v, want a warning and nil so the caller still drops the record entry", err)
	}
	wantPath := RemoteRepoPath("devuser", f.session, f.repoName)
	if !strings.Contains(warning, wantPath) {
		t.Errorf("warning = %q, want it to name the leftover instance directory %s", warning, wantPath)
	}
	if _, statErr := os.Stat(f.local); statErr == nil {
		t.Error("the local worktree survived a forced delete despite the instance being unreachable")
	}
	if out, err := runLocalGit(context.Background(), f.repo, "remote", "get-url", sessionRemote(f.session)); err == nil {
		t.Errorf("the remote survived a forced delete despite the instance being unreachable: %s", out)
	}
}

// The end-to-end version of TestRequireBeadsLanded_RefusesWhenTheSyncCannotConfirmIssuesLanded:
// this exercises the actual call site inside DeleteSession, including the
// RemoteRepoPath argument order and the reconcile.Connect that feeds it,
// rather than the guard function in isolation.
func TestDeleteSession_RefusesWhenIssuesHaveNotLanded(t *testing.T) {
	requireBd(t)
	f := newSessionFixture(t, 0)
	bdInitStealth(t, f.repo)
	if err := beads.Seed(context.Background(), f.repo, f.session, beads.FileURL(f.agent)); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}
	// Every other instance command (checkpoint, rev-parse, rm -rf) keeps
	// working; only the dolt push -- the half of the sync that would prove
	// the agent's issues are safe -- fails.
	f.cmdOverride = func(cmd string) (string, uint32, bool) {
		if strings.Contains(cmd, "dolt") && strings.Contains(cmd, "push") {
			return "dolt push exploded", 1, true
		}
		return "", 0, false
	}

	warning, err := DeleteSession(context.Background(), f.addr, "devuser", f.repoName,
		state.Session{Name: f.session, LocalRepo: f.repo, Base: f.base}, false)
	if err == nil {
		t.Fatal("DeleteSession() = nil when the session's issues had not landed, want a refusal")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error = %q, want it to mention --force", err.Error())
	}
	if warning != "" {
		t.Errorf("warning = %q, want none -- a refusal is an error, not a warning", warning)
	}
	if f.sessionRemoved() {
		t.Error("the instance-side session was removed despite unlanded issues")
	}
	if _, statErr := os.Stat(f.local); statErr != nil {
		t.Error("the local worktree was removed despite unlanded issues")
	}
}

// The other half: once the sync completes, delete must still proceed. Without
// this, TestDeleteSession_RefusesWhenIssuesHaveNotLanded could be "explained"
// by a guard that refuses unconditionally once beads is wired at all.
func TestDeleteSession_ProceedsWhenIssuesHaveLanded(t *testing.T) {
	requireBd(t)
	f := newSessionFixture(t, 0)
	bdInitStealth(t, f.repo)
	if err := beads.Seed(context.Background(), f.repo, f.session, beads.FileURL(f.agent)); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}
	// No override: every instance command, dolt push included, succeeds.

	_, err := DeleteSession(context.Background(), f.addr, "devuser", f.repoName,
		state.Session{Name: f.session, LocalRepo: f.repo, Base: f.base}, false)
	if err != nil {
		t.Fatalf("DeleteSession() error = %v, want deletion once beads' issues have landed", err)
	}
	if !f.sessionRemoved() {
		t.Error("the instance-side session was not removed despite landed issues")
	}
}
