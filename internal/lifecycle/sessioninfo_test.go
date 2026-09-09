package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/cloudlab/internal/state"
)

// With no session remote configured there is nobody to ask, so this is the
// offline path: the local branch, counted by patch identity. It is the
// fallback the reachable cases below replace, not the normal answer.
func TestDescribeSession_CountsUnmergedByPatchIdentity(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	initRepo(t, repo)
	root := gitOut(t, repo, "rev-parse", "HEAD")

	wt := LocalWorktreePath(repo, "auth")
	mustGit(t, repo, "worktree", "add", "--quiet", wt, "-b", "cloudlab/auth")
	writeAndCommit(t, wt, "work.txt", "x", "agent work")

	info := DescribeSession(context.Background(), "inst", state.Session{
		Name: "auth", LocalRepo: repo, Base: root,
	})
	if info.Unmerged != 1 {
		t.Errorf("Unmerged = %d, want 1", info.Unmerged)
	}
	if !info.WorktreeExists {
		t.Error("WorktreeExists = false, want true")
	}
	if info.Branch != "cloudlab/auth" {
		t.Errorf("Branch = %q, want cloudlab/auth", info.Branch)
	}
}

// After the work lands on the user's branch its commits exist under different
// SHAs. A plain count would still say "unmerged"; patch identity sees the truth.
func TestDescribeSession_UnmergedIsZeroOnceTheWorkHasLanded(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	initRepo(t, repo)
	root := gitOut(t, repo, "rev-parse", "HEAD")

	wt := LocalWorktreePath(repo, "auth")
	mustGit(t, repo, "worktree", "add", "--quiet", wt, "-b", "cloudlab/auth")
	writeAndCommit(t, wt, "work.txt", "x", "agent work")

	// Land it on the user's branch as merge would: a new commit, new SHA.
	mustGit(t, repo, "cherry-pick", gitOut(t, wt, "rev-parse", "HEAD"))

	info := DescribeSession(context.Background(), "inst", state.Session{
		Name: "auth", LocalRepo: repo, Base: root,
	})
	if info.Unmerged != 0 {
		t.Errorf("Unmerged = %d after the work landed, want 0", info.Unmerged)
	}
}

// sessionWithRemote builds the shape session start leaves behind: a local
// repository, a session branch, and a git remote pointing at the session's
// own repository -- here an ordinary directory rather than an instance, so
// the ls-remote under test is a real one with no network in the way.
func sessionWithRemote(t *testing.T, name string) (repo, instance, root string) {
	t.Helper()
	base := t.TempDir()
	repo = filepath.Join(base, "repo")
	instance = filepath.Join(base, "instance")
	initRepo(t, repo)
	root = gitOut(t, repo, "rev-parse", "HEAD")

	mustGit(t, repo, "clone", "--quiet", repo, instance)
	mustGit(t, instance, "config", "user.email", "t@example.com")
	mustGit(t, instance, "config", "user.name", "t")
	mustGit(t, instance, "config", "commit.gpgsign", "false")
	mustGit(t, instance, "checkout", "--quiet", "-b", SessionBranch(name))
	mustGit(t, repo, "remote", "add", sessionRemote(name), instance)
	return repo, instance, root
}

// The bug this exists for: the instance had a commit, the local branch did
// not, and the listing reported 0 -- read as "nothing waiting" at the one
// moment the answer decides whether to pull. The local branch only moves
// when pull fast-forwards it, so it can never answer this on its own.
//
// ls-remote returns a SHA and no objects, so with nothing fetched there is
// nothing to count. "Ahead" is the whole answer, and it is the one that
// matters: it is the difference between pulling and not.
func TestDescribeSession_ReportsTheInstanceIsAheadWithoutFetching(t *testing.T) {
	repo, instance, root := sessionWithRemote(t, "auth")
	writeAndCommit(t, instance, "work.txt", "x", "agent work")

	info := DescribeSession(context.Background(), "inst", state.Session{
		Name: "auth", LocalRepo: repo, Base: root,
	})

	if !info.RemoteKnown {
		t.Fatal("RemoteKnown = false, want true -- the remote was reachable")
	}
	if !info.RemoteAhead {
		t.Error("RemoteAhead = false, want true -- the instance holds a commit this " +
			"machine does not have, which is exactly when the user must pull")
	}
	if info.UnmergedKnown {
		t.Errorf("UnmergedKnown = true (Unmerged = %d), want false -- the tip's object "+
			"was never fetched, so any number here would be invented", info.Unmerged)
	}
	// Nothing was fetched: asking must stay a read.
	if _, err := runLocalGit(context.Background(), repo, "cat-file", "-e",
		gitOut(t, instance, "rev-parse", "HEAD")); err == nil {
		t.Error("the tip's object is present locally -- DescribeSession fetched, and a " +
			"listing that mutates the repository is not a listing")
	}
}

// Once a fetch has happened -- pull, delete and merge all do one -- the
// objects are here and the count becomes exact. This is the common case, so
// the "ahead" fallback should be rare in practice.
func TestDescribeSession_CountsExactlyOnceTheObjectsArePresent(t *testing.T) {
	repo, instance, root := sessionWithRemote(t, "auth")
	writeAndCommit(t, instance, "work.txt", "x", "agent work")
	mustGit(t, repo, "fetch", "--quiet", sessionRemote("auth"))

	info := DescribeSession(context.Background(), "inst", state.Session{
		Name: "auth", LocalRepo: repo, Base: root,
	})

	if !info.UnmergedKnown || info.Unmerged != 1 {
		t.Errorf("Unmerged = %d (known %v), want 1", info.Unmerged, info.UnmergedKnown)
	}
	if !info.RemoteAhead {
		t.Error("RemoteAhead = false, want true")
	}
}

// An unreachable instance must not read as an empty one. Falling back to the
// local branch is fine -- it is still the freshest thing on this machine --
// but the answer has to carry that it came from there.
func TestDescribeSession_UnreachableInstanceIsNotZero(t *testing.T) {
	repo, instance, root := sessionWithRemote(t, "auth")
	writeAndCommit(t, instance, "work.txt", "x", "agent work")
	if err := os.RemoveAll(instance); err != nil {
		t.Fatal(err)
	}

	info := DescribeSession(context.Background(), "inst", state.Session{
		Name: "auth", LocalRepo: repo, Base: root,
	})

	if info.RemoteKnown {
		t.Error("RemoteKnown = true for an instance that is gone, want false -- a listing " +
			"that cannot ask must not present its fallback as the instance's answer")
	}
}

// Once the work has landed the instance and the user's branch hold the same
// patches under different SHAs. Asking the instance must not undo what patch
// identity already gets right.
func TestDescribeSession_RemoteCountIsZeroOnceTheWorkHasLanded(t *testing.T) {
	repo, instance, root := sessionWithRemote(t, "auth")
	writeAndCommit(t, instance, "work.txt", "x", "agent work")
	mustGit(t, repo, "fetch", "--quiet", sessionRemote("auth"))
	mustGit(t, repo, "cherry-pick", gitOut(t, instance, "rev-parse", "HEAD"))

	info := DescribeSession(context.Background(), "inst", state.Session{
		Name: "auth", LocalRepo: repo, Base: root,
	})

	if !info.UnmergedKnown || info.Unmerged != 0 {
		t.Errorf("Unmerged = %d (known %v), want 0 -- the same patch is already on HEAD",
			info.Unmerged, info.UnmergedKnown)
	}
}

func TestDescribeSession_MissingWorktreeIsNotAnError(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)

	info := DescribeSession(context.Background(), "inst", state.Session{
		Name: "gone", LocalRepo: repo, Base: gitOut(t, repo, "rev-parse", "HEAD"),
	})
	if info.WorktreeExists {
		t.Error("WorktreeExists = true for a worktree that was never created")
	}
	if info.Name != "gone" {
		t.Errorf("Name = %q, want gone", info.Name)
	}
}

// A listing that stops to ask for a passphrase, or waits out the OS default
// connect timeout on a host that is not there, is a listing nobody can use.
// Three unreachable sessions cost 2m30s before this was bounded.
func TestLsRemoteArgs_BoundsTheWaitAndNeverPrompts(t *testing.T) {
	got := strings.Join(lsRemoteArgs("cloudlab-auth", "cloudlab/auth"), " ")

	for _, want := range []string{"ConnectTimeout=", "BatchMode=yes", "ls-remote", "--heads"} {
		if !strings.Contains(got, want) {
			t.Errorf("lsRemoteArgs() = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "fetch") {
		t.Errorf("lsRemoteArgs() = %q, want no fetch -- a listing must not write objects "+
			"or refs into the user's repository", got)
	}
}

// The bound has to actually fire. A routable-but-dead address is the case
// that hurt: connect hangs rather than being refused, which is what turned a
// listing into a two-and-a-half-minute wait.
func TestDescribeSession_GivesUpOnAnUnreachableInstance(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)
	// TEST-NET-1 (RFC 5737): reserved for documentation, never routed, so
	// this blackholes rather than being refused.
	mustGit(t, repo, "remote", "add", sessionRemote("auth"), "ssh://user@192.0.2.1/repo")

	start := time.Now()
	info := DescribeSession(context.Background(), "inst", state.Session{
		Name: "auth", LocalRepo: repo, Base: gitOut(t, repo, "rev-parse", "HEAD"),
	})
	elapsed := time.Since(start)

	if info.RemoteKnown {
		t.Error("RemoteKnown = true for a blackholed address, want false")
	}
	if elapsed > 10*time.Second {
		t.Errorf("DescribeSession took %s for one unreachable session -- the probe bound "+
			"is not being applied", elapsed)
	}
}

// The probes must overlap. Run in sequence, a listing costs the sum of every
// session's timeout, and the person with the most sessions waits longest --
// which is backwards, since they are who the listing is for.
func TestDescribeSessions_ProbesConcurrentlyAndKeepsOrder(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)
	root := gitOut(t, repo, "rev-parse", "HEAD")

	var sessions []state.Session
	names := []string{"a", "b", "c", "d"}
	for _, n := range names {
		// Blackholed (RFC 5737), so every probe pays the full bound.
		mustGit(t, repo, "remote", "add", sessionRemote(n), "ssh://user@192.0.2.1/repo")
		sessions = append(sessions, state.Session{Name: n, LocalRepo: repo, Base: root})
	}

	start := time.Now()
	infos := DescribeSessions(context.Background(), "inst", sessions)
	elapsed := time.Since(start)

	if len(infos) != len(names) {
		t.Fatalf("got %d infos, want %d", len(infos), len(names))
	}
	for i, n := range names {
		if infos[i].Name != n {
			t.Errorf("infos[%d].Name = %q, want %q -- order must be stable between runs",
				i, infos[i].Name, n)
		}
	}
	// Four sequential probes would be at least 4x one. Well under that is
	// the only observable proof they overlapped.
	if elapsed > 2*remoteProbeTimeout {
		t.Errorf("four unreachable sessions took %s with a %s bound -- the probes are "+
			"running in sequence", elapsed, remoteProbeTimeout)
	}
}
