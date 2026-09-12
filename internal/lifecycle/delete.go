package lifecycle

import (
	"context"
	"fmt"
	"os"

	"github.com/jskswamy/cloudlab/internal/beads"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/reconcile"
	"github.com/jskswamy/cloudlab/internal/state"
)

// DeleteSession discards a session without landing its work: the repository on
// the instance, the local worktree, the branch and the remote.
//
// The only destructive verb in the session set, so it refuses while commits
// have not landed. merge is how work is kept; delete is how it is thrown away,
// and the difference should never be discovered afterwards.
//
// Returns a warning rather than an error when the instance could not be
// reached but the local half came down anyway (only reachable under --force).
// A dead droplet must not make a session permanently undeletable, and an error
// here would strand the record entry: its local remote is already gone, so
// nothing could ever rescue it again and every later `down` would refuse.
func DeleteSession(ctx context.Context, ip, user, repoName string, s state.Session, force bool) (string, error) {
	if !force {
		// Rescue first, exactly as pull, merge and down do. The local session
		// branch only moves when pull fast-forwards it, so measuring it alone
		// reports a session nobody has pulled as empty however many commits
		// the agent actually made -- and deletes all of them with exit 0.
		ref, _, err := RescueSession(ctx, ip, user, s.LocalRepo, repoName, s.Name)
		if err != nil {
			return "", fmt.Errorf("cannot confirm what session %s still has on the instance: %w\nrefusing to delete work cloudlab cannot see — fix the instance and retry, or `cloudlab session delete %s --force` to discard it anyway", s.Name, err, s.Name)
		}
		// Both refs. ref is everything the agent has, now that it has been
		// checkpointed and fetched; the session branch additionally carries
		// anything the user committed in the local session worktree, which was
		// never pushed to the instance and so is on neither ref nor HEAD.
		for _, rev := range []string{ref, SessionBranch(s.Name)} {
			unmerged, known := countUnmerged(ctx, s.LocalRepo, rev)
			if !known {
				return "", fmt.Errorf("cannot tell whether session %s has unmerged work (git could not compare %s against your branch) — refusing to delete; use --force to discard it anyway", s.Name, rev)
			}
			if unmerged > 0 {
				return "", fmt.Errorf("session %s has %d commit(s) not on your branch — `cloudlab session merge %s` keeps them, or `cloudlab session delete %s --force` throws them away", s.Name, unmerged, s.Name, s.Name)
			}
		}

		// Issues too, on the same reasoning the commit checks above rest on:
		// delete is how work is thrown away, and the difference from merge
		// should never be discovered afterwards.
		if err := checkBeadsLanded(ctx, ip, user, s.LocalRepo, RemoteRepoPath(user, s.Name, repoName), s.Name); err != nil {
			return "", err
		}
	}

	var instanceErr error
	// Held rather than scoped to the branch: the herdr cleanup below needs
	// the same connection, and a nil one there means "could not reach the
	// instance", which it handles by doing only the local half.
	var onInstance remoteRunner
	if client, err := reconcile.Connect(ctx, ip, user); err != nil {
		instanceErr = err
	} else {
		onInstance = client
		defer func() { _ = client.Close() }()
		if out, err := client.Run(removeRepoCmd(RemoteRepoPath(user, s.Name, repoName))); err != nil {
			instanceErr = fmt.Errorf("%w\n%s", err, out)
		}
	}

	local := LocalWorktreePath(s.LocalRepo, s.Name)
	if _, err := os.Stat(local); err == nil {
		if out, err := runLocalGit(ctx, s.LocalRepo, "worktree", "remove", "--force", local); err != nil {
			return "", fmt.Errorf("removing local worktree %s: %w\n%s", local, err, out)
		}
	}
	_, _ = runLocalGit(ctx, s.LocalRepo, "worktree", "prune")
	_, _ = runLocalGit(ctx, s.LocalRepo, "branch", "-D", SessionBranch(s.Name))
	_, _ = runLocalGit(ctx, s.LocalRepo, remoteRemoveArgs(sessionRemote(s.Name))...)
	// Best-effort, same reasoning as merge's own cleanup: a stale dolt remote
	// left behind here would make Wired report true for an unrelated later
	// session started at the same name, including one started with beads =
	// "off" -- the fail-closed guard firing on an explicit opt-out.
	if err := beads.RemoveRemote(ctx, s.LocalRepo, s.Name); err != nil {
		provider.ReportWarning(ctx, "beads: "+err.Error()+
			"\nyou may need to remove it by hand: bd dolt remote remove "+beads.RemoteName(s.Name))
	}
	// Same best-effort shape, for the same reason: a saved machine left
	// pointing at a session that no longer exists is an entry in the
	// sidebar nothing will ever clean up.
	CleanupHerdr(ctx, s.HerdrMachineID, s.Name, onInstance)

	if instanceErr != nil {
		return fmt.Sprintf("session %s was removed locally, but the instance could not be reached: %v\nthe directory %s is still there; nothing references it now, and `cloudlab down` will take it with the instance", s.Name, instanceErr, RemoteRepoPath(user, s.Name, repoName)), nil
	}
	return "", nil
}
