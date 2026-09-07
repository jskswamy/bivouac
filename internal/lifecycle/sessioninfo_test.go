package lifecycle

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jskswamy/cloudlab/internal/state"
)

// list is offline: it must not fetch or contact an instance, so it stays fast
// and works when the instance is down -- which is when you most want to know
// what is on it.
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
