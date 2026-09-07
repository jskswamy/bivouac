package lifecycle

import (
	"context"

	"github.com/jskswamy/cloudlab/internal/beads"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// seedBeads gives the session's agent the repository's issue tracker: the
// dolt database rides refs/dolt/data on the session's own git remote, over
// the SSH channel its code already uses.
//
// Returns nothing. Every step here warns and continues, and a signature with
// no error is how that policy is enforced rather than merely documented -- a
// session start with a broken beads setup still produces a working session,
// which is the whole point of keeping this additive.
//
// Runs last in seedSession, after the branch has been pushed and checked out.
// Pushing dolt data into a git remote with no branches fails outright, so the
// existing init -> push -> checkout order is a precondition, not a
// coincidence.
func seedBeads(ctx context.Context, client *reconcile.Client, localRepo, repo, user, host, session, beadsMode string) {
	if beadsMode == "off" {
		return
	}
	detected, err := beads.Detect(ctx, localRepo)
	if err != nil {
		provider.ReportWarning(ctx, "beads: could not read this repository's dolt remotes, skipping: "+err.Error())
		return
	}
	if detected.Mode == beads.ModeAbsent {
		return
	}

	// Before bd init, not after. Setup is fail-safe, so a bd init that fails
	// partway is tolerated -- but it can still leave .beads/ behind, and an
	// unexcluded .beads/ is swallowed by the next checkpoint's `git add -A`
	// and cherry-picked onto the user's branch by merge.
	//
	// repo is always RemoteRepoPath: the instance repository seedSession
	// just created with a plain `git init`, never a linked worktree. That
	// matters because excludeBeadsCmd resolves the exclude file via
	// --absolute-git-dir, which disagrees with --git-common-dir inside a
	// linked worktree, and git does not honour info/exclude under a
	// worktree's private git-dir.
	if out, err := client.Run(excludeBeadsCmd(repo)); err != nil {
		provider.ReportWarning(ctx, "beads: could not exclude .beads/ on the instance, skipping to avoid committing the database: "+err.Error()+"\n"+out)
		return
	}

	provider.ReportProgress(ctx, "seeding issues into "+session)
	if err := beads.Seed(ctx, localRepo, session, beads.RemoteURL(user, host, repo)); err != nil {
		provider.ReportWarning(ctx, "beads: "+err.Error())
		return
	}

	// Only in "dolthub" mode, and only from a repository that actually has an
	// external remote to reuse -- cloudlab.pkl says whether to ship a
	// credential, never what the URL is.
	external := ""
	if beadsMode == "dolthub" && detected.Mode == beads.ModeExternal {
		external = detected.ExternalURL
	}
	if err := beads.Bootstrap(client, repo, beads.FileURL(repo), external); err != nil {
		provider.ReportWarning(ctx, "beads: "+err.Error())
	}
}
