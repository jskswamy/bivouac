package lifecycle

import (
	"context"
	"errors"
	"fmt"

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
	warnDolthubWithoutExternalRemote(ctx, beadsMode, detected)

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
	bootstrapBeads(ctx, client, localRepo, repo, session, external)
}

// bootstrapBeads clones the seeded database into the instance's checkout,
// and removes the Mac's dolt remote only when that leaves the instance with
// no database at all.
//
// Split out of seedBeads so it can be exercised without a real ssh:// dolt
// remote, which needs a real git/dolt-speaking SSH server that no in-process
// test harness here can satisfy -- the same reason trackSession is split
// from seedSession.
//
// The removal matters because Wired keys entirely on the remote Seed just
// registered: a Bootstrap whose `bd init` step fails -- most commonly
// because the instance predates beads and has no `bd` on PATH -- otherwise
// leaves that remote in place, and every later requireBeadsLanded call then
// runs `bd dolt push` against an instance with no .beads/ at all, which
// fails forever and makes session delete and `cloudlab down` refuse
// permanently. Removing it turns a warned-and-continued setup failure back
// into a known nothing, which is what the fail-closed teardown guard is
// built to pass on.
//
// A failure in the later "dolthub" remote-add step is a different animal:
// `bd init` already succeeded, so the instance has a real database, and the
// session's own git+ssh transport is intact and will still carry the
// agent's issues home. Removing the remote there would silently drop the
// one protection an account-wide DoltHub credential is paid for -- Wired
// would read false, and pullBeads and requireBeadsLanded would both pass
// without a word -- exactly the silent loss the design spec's "the modes
// compose; they do not replace each other" is describing when it says the
// session's own SSH sync must survive even when the DoltHub side does not.
// errors.Is against beads.ErrInitFailed is what tells the two failures
// apart.
func bootstrapBeads(ctx context.Context, client *reconcile.Client, localRepo, repo, session, external string) {
	if err := beads.Bootstrap(client, repo, beads.FileURL(repo), external); err != nil {
		provider.ReportWarning(ctx, "beads: "+err.Error())
		if !errors.Is(err, beads.ErrInitFailed) {
			// The instance has a real database; the session's git+ssh
			// transport still carries issues home, so it stays wired.
			return
		}
		if rmErr := beads.RemoveRemote(ctx, localRepo, session); rmErr != nil {
			provider.ReportWarning(ctx, "beads: bootstrap failed and the stale dolt remote could not be removed either: "+rmErr.Error()+
				"\nyou may need to remove it by hand: bd dolt remote remove "+beads.RemoteName(session))
		}
		return
	}
}

// warnDolthubWithoutExternalRemote tells the user when beads = "dolthub" buys
// them nothing: seedBeads only ever wires the instance-side "dolthub" remote
// from a repository already in ModeExternal, so asking for external sync
// from a repository that is unsynced or git-only still seeds the session
// remote and silently gets none of the sharing the user asked for.
func warnDolthubWithoutExternalRemote(ctx context.Context, beadsMode string, detected beads.Detection) {
	if beadsMode == "dolthub" && detected.Mode != beads.ModeExternal {
		provider.ReportWarning(ctx, "beads: beads = \"dolthub\" but this repository has no external dolt remote to reuse (mode: "+
			detected.Mode.String()+"); issues will sync over the session remote only")
	}
}

// pullBeads brings the agent's issue edits home, reporting whether it
// managed to. Like seedBeads it never returns an error: a failed issue sync
// must never stop a pull that is otherwise making the agent's commits
// durable. The bool exists for merge, which is about to delete the
// repository the issues live in and has to say so.
//
// Silent when beads was never wired for this session. The session's dolt
// remote is the whole test -- pull, merge, delete and down never resolve a
// config, and a session started while beads was off must stay off for the
// rest of its life whatever cloudlab.pkl says now.
func pullBeads(ctx context.Context, client *reconcile.Client, localRepo, repo, session string) bool {
	if !beads.Wired(ctx, localRepo, session) {
		// Nothing to sync is not a failed sync: a caller weighing what it is
		// about to destroy must not be told issues are at risk when the
		// session never had any.
		return true
	}
	provider.ReportProgress(ctx, "pulling issues from "+session)
	if err := beads.Pull(ctx, localRepo, session, client, repo); err != nil {
		provider.ReportWarning(ctx, "beads: "+err.Error())
		return false
	}
	return true
}

// checkBeadsLanded connects to the instance and runs requireBeadsLanded,
// shared by delete and down so a Connect failure is handled identically in
// both: it warns rather than skipping the issue check silently. Both
// call sites run this immediately after RescueSession, which would already
// have failed loudly against an unreachable instance -- so this stays
// best-effort and never turns into a refusal on its own -- but silence was
// the bug: an unknown was treated as safe inside the one guard whose
// contract is the opposite.
func checkBeadsLanded(ctx context.Context, ip, user, localRepo, repo, session string) error {
	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		provider.ReportWarning(ctx, "beads: could not connect to check whether session "+session+"'s issues have landed ("+err.Error()+"); skipping the issue check")
		return nil
	}
	defer func() { _ = client.Close() }()
	return requireBeadsLanded(ctx, client, localRepo, repo, session)
}

// requireBeadsLanded refuses when the instance may still hold issue work this
// machine does not have.
//
// The one place beads is fail-closed, and it inverts everything else in this
// file deliberately. An error from Unpulled means cloudlab does not know
// whether the agent's issues are safe, and delete and down are the two verbs
// that make an unknown permanent -- so an unknown is treated as unsafe, the
// same stance 47118eb took for commits.
//
// A session beads was never wired for passes: that is a known nothing, not an
// unknown, and refusing on it would make every session in every repository
// without beads undeletable.
func requireBeadsLanded(ctx context.Context, client *reconcile.Client, localRepo, repo, session string) error {
	if !beads.Wired(ctx, localRepo, session) {
		return nil
	}
	unpulled, err := beads.Unpulled(ctx, localRepo, session, client, repo)
	if err != nil {
		return fmt.Errorf("cannot confirm session %s's issues have landed: %w\nrefusing to discard issue work cloudlab cannot see — fix the instance and retry, or pass --force to discard it anyway", session, err)
	}
	// Unreachable under Unpulled's current contract -- true is only ever
	// returned alongside a non-nil err, which the branch above already
	// catches. Kept anyway: if that contract ever loosens, this is what
	// stands between the change and a silent fail-open regression here.
	if unpulled {
		return fmt.Errorf("session %s still holds issue work that is not on this machine — `cloudlab session pull %s` takes it, or --force throws it away", session, session)
	}
	return nil
}
