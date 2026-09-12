package lifecycle

import (
	"context"
	"fmt"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// PullSession brings a session's work to this machine and refreshes the
// local worktree, without touching the user's branch. Safe to run
// repeatedly, including while the agent is still working and while the
// user's own tree is dirty. Returns one "<sha> <subject>" line per commit
// not yet on the current branch.
func PullSession(ctx context.Context, ip, user, localRepo, repoName, session, sessionBase string) ([]string, error) {
	provider.ReportProgress(ctx, "checkpointing and fetching "+session)

	ref, _, err := RescueSession(ctx, ip, user, localRepo, repoName, session)
	if err != nil {
		return nil, err
	}

	// After the rescue, so a commit the agent made alongside an issue edit is
	// already safe if the issue sync then fails. A second connection rather
	// than a signature change on RescueSession: delete and down call that too,
	// and they gate on issues through requireBeadsLanded instead -- syncing
	// there as well would cost two round trips per teardown and report one
	// cause as both a warning and an error.
	if client, err := reconcile.Connect(ctx, ip, user); err == nil {
		pullBeads(ctx, client, localRepo, RemoteRepoPath(user, session, repoName), session)
		_ = client.Close()
	}

	local := LocalWorktreePath(localRepo, session)
	// Fast-forward rather than reset --hard. The user is told to cd into
	// this worktree and run the agent's code, so it can legitimately hold
	// their own edits -- and pull is the safe verb. --ff-only refuses
	// loudly when the worktree has diverged instead of silently eating
	// whatever is there.
	if out, err := runLocalGit(ctx, local, "merge", "--ff-only", ref); err != nil {
		return nil, fmt.Errorf("updating local worktree %s: %w\n%s\nthe worktree has diverged from the session — commit or discard your changes there, then pull again", local, err, out)
	}

	out, err := runLocalGit(ctx, localRepo, "log", "--pretty=%h %s", reportRange(ctx, localRepo, sessionBase)+".."+ref)
	if err != nil {
		return nil, fmt.Errorf("listing new commits: %w\n%s", err, out)
	}
	return nonEmptyLines(out), nil
}

// reportRange picks the left side of the range pull lists commits from.
//
// HEAD is right while the session's base is still an ancestor of it. Once the
// branch has been rewritten it is not: the range widens to include a
// pre-rewrite copy of the branch, and pull names commits the user already has
// while giving no hint that merge is about to refuse. The recorded base is the
// honest left edge in that case -- it still reaches exactly the session's own
// work.
//
// Falls back to HEAD when there is no base (a session predating the field) or
// when the base object is gone, since a range against a missing commit fails
// outright and a slightly wide list beats no list at all.
func reportRange(ctx context.Context, localRepo, sessionBase string) string {
	if sessionBase == "" {
		return "HEAD"
	}
	if _, err := runLocalGit(ctx, localRepo, "cat-file", "-e", sessionBase+"^{commit}"); err != nil {
		return "HEAD"
	}
	if _, err := runLocalGit(ctx, localRepo, "merge-base", "--is-ancestor", sessionBase, "HEAD"); err == nil {
		return "HEAD"
	}
	return sessionBase
}

// verifyFetched confirms sha is present in localRepo's object store. This,
// not a fetch's exit code, is what authorises deleting a worktree or
// destroying an instance.
func verifyFetched(ctx context.Context, localRepo, sha string) error {
	if _, err := runLocalGit(ctx, localRepo, "cat-file", "-e", sha+"^{commit}"); err != nil {
		return fmt.Errorf("commit %s is on the instance but not in your local repository — refusing to treat it as rescued", sha)
	}
	return nil
}
