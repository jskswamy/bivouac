package lifecycle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/jskswamy/cloudlab/internal/config"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// StartSession creates a session: a repository on the instance for the agent
// to work in, and the matching worktree on this machine to review and run it
// in. The local worktree is created here and only here -- pull updates it but
// never creates it -- so a session always has somewhere to land before any
// work exists.
//
// Every step is retry-safe, because a start that fails partway through --
// most easily at the push or fetch, the first real network operations -- must
// be fixable by running the same command again.
//
// task is TaskText's already-built content, or "" -- resolved by the caller
// before any of this runs, so the --task/--issue conflict TaskText refuses
// costs nothing.
func StartSession(ctx context.Context, ip, user, localRepo, repoName, session string, beadsMode config.BeadsMode, task string) error {
	if err := CheckSessionName(session); err != nil {
		return err
	}
	provider.ReportProgress(ctx, "creating session "+session)

	// Resolved before the seeding SSH connection so the remote registered
	// below and the push above it agree on one address.
	host := gitHost(ctx, ip, user)
	repo := RemoteRepoPath(user, session, repoName)
	branch := SessionBranch(session)
	url := sshGitURL(user, host, repo)

	if err := seedSession(ctx, ip, user, localRepo, repo, branch, url, host, session, beadsMode, task); err != nil {
		return err
	}
	return trackSession(ctx, localRepo, session, branch, url)
}

// seedSession creates the session's repository on the instance and publishes
// the Mac's current commit into it as the session branch, then hands the
// result to seedBeads for the (optional, best-effort) issue tracker.
//
// The order -- init, push, then checkout -- is what keeps this working
// without configuring receive.denyCurrentBranch. A fresh `git init` leaves
// HEAD on an unborn branch, so the pushed branch is not the checked-out one
// and the push is legal; the checkout afterwards gives the agent its files.
// Reversing those two steps makes every seed fail.
func seedSession(ctx context.Context, ip, user, localRepo, repo, branch, url, host, session string, beadsMode config.BeadsMode, task string) error {
	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	if err := client.EnableAgentForwarding(); err != nil {
		provider.ReportWarning(ctx, "agent forwarding: "+err.Error())
	}

	if out, err := client.Run(ensureRepoCmd(repo)); err != nil {
		return fmt.Errorf("creating session repo on instance: %w\n%s", err, out)
	}

	// Before anything can commit there. checkpointCmd runs `git commit` on
	// every pull and merge, and a repo with no identity does not fail -- it
	// invents one from the hostname and cherry-pick preserves it.
	name, email, err := localIdentity(ctx, localRepo)
	if err != nil {
		return err
	}
	if out, err := client.Run(setIdentityCmd(repo, name, email)); err != nil {
		return fmt.Errorf("setting git identity on instance: %w\n%s", err, out)
	}

	// HEAD, not the branch name: a detached HEAD or an unusual checkout is
	// still a commit worth seeding, and the session branch is named for the
	// session regardless of what the Mac calls the branch it came from.
	if out, err := runLocalGit(ctx, localRepo, pushArgs(url, "HEAD", branch)...); err != nil {
		return fmt.Errorf("seeding session onto instance: %w\n%s\nif the session already has commits, pull or merge it instead of starting it again", err, out)
	}
	if out, err := client.Run(checkoutSessionCmd(repo, branch)); err != nil {
		return fmt.Errorf("checking out %s on instance: %w\n%s", branch, err, out)
	}

	// Beside the checkout, never inside it -- WriteTask's own doc comment
	// says why. A warning rather than a failed session for the same reason
	// writeAgentContext below is one: the session is already usable, and a
	// failed TASK.md must not strand an instance the user now has to clean
	// up by hand.
	if err := WriteTask(client, user, session, task); err != nil {
		provider.ReportWarning(ctx, "task: "+err.Error()+
			"\nthe session is usable, but its agent may not know what it is for")
	}

	// Again here, not only in reconcile. instructions names files the user
	// edits between sessions, so a session started after an edit must carry
	// the edit rather than whatever the last up delivered.
	//
	// A warning rather than an error, for seedBeads' reason: the session
	// itself is already created and usable, and a failed instruction write
	// must not strand an instance the user now has to clean up by hand.
	if err := writeAgentContext(ctx, client, localRepo, user); err != nil {
		provider.ReportWarning(ctx, "instructions: "+err.Error()+
			"\nthe session is usable, but its agents may be working from older instructions;"+
			" fix this and run `cloudlab provision` to deliver them")
	}

	// Last, deliberately. Beads never fails a session, so this returns
	// nothing -- but it also has to run after the push and checkout above,
	// because pushing dolt data into a git remote with no branches fails.
	seedBeads(ctx, client, localRepo, repo, user, host, session, beadsMode)
	return nil
}

// writeAgentContext resolves the repository's config and delivers its
// instructions to the session's harnesses.
//
// The config is resolved here rather than threaded down from the command,
// which resolves it once already for the beads mode. A second pkl run costs
// less than widening StartSession's signature through every caller, and it
// keeps the instructions the session gets tied to the config as it stands
// now -- which is the whole point of writing them again at session start.
func writeAgentContext(ctx context.Context, client *reconcile.Client, localRepo, user string) error {
	cloudlabPath := filepath.Join(localRepo, "cloudlab.pkl")
	// Absent and broken are different answers, the distinction
	// config.Resolve and config.InstructionFiles already draw for the base
	// config: a repository with no config declares no agents and no
	// instructions, so there is nothing that failed to arrive. A config
	// that exists and will not resolve does warn, below -- that one may
	// well name instructions nobody got.
	if _, err := os.Stat(cloudlabPath); os.IsNotExist(err) {
		return nil
	}
	cfg, err := config.Resolve(ctx, cloudlabPath)
	if err != nil {
		return err
	}
	return reconcile.WriteAgentContext(ctx, client, user, cfg.Agents, cloudlabPath)
}

// trackSession registers the session's remote on this machine and creates the
// local worktree that mirrors it. Split from seedSession so it can be
// exercised without a real git push to a real instance, which no in-process
// test harness here can satisfy (see this function's test for why).
func trackSession(ctx context.Context, localRepo, session, branch, url string) error {
	remote := sessionRemote(session)
	// Re-adding an existing remote is an error, and a session may be
	// recreated after a failed start, so drop any stale one first.
	_, _ = runLocalGit(ctx, localRepo, remoteRemoveArgs(remote)...)
	if out, err := runLocalGit(ctx, localRepo, remoteAddArgs(remote, url)...); err != nil {
		return fmt.Errorf("registering remote %s: %w\n%s", remote, err, out)
	}
	if out, err := runLocalGit(ctx, localRepo, fetchRemoteArgs(remote)...); err != nil {
		return fmt.Errorf("fetching %s: %w\n%s", remote, err, out)
	}

	if err := excludeWorktreeDir(ctx, localRepo); err != nil {
		return err
	}

	local := LocalWorktreePath(localRepo, session)
	// Prune the administrative entry a hand-deleted directory leaves behind,
	// then leave an existing worktree alone rather than re-adding over
	// whatever the user has in it.
	_, _ = runLocalGit(ctx, localRepo, "worktree", "prune")
	if _, err := os.Stat(local); err == nil {
		return nil
	}
	tracking := remote + "/" + branch
	if out, err := runLocalGit(ctx, localRepo, "worktree", "add", "--track", "-B", branch, local, tracking); err != nil {
		return fmt.Errorf("creating local worktree at %s: %w\n%s", local, err, out)
	}
	return nil
}

// worktreeDirPattern is the ignore entry that keeps session worktrees out of
// the user's git status.
const worktreeDirPattern = "/.worktrees/"

// excludeWorktreeDir makes sure .worktrees/ is ignored in localRepo.
//
// Session worktrees live inside the repository so sandboxed agents can reach
// them, which means git sees them as untracked content. Left alone that
// pollutes the user's `git status` and, worse, makes merge's
// working-tree-clean check fail forever on any repository that has not
// already ignored the directory.
//
// Written to .git/info/exclude rather than .gitignore deliberately: it is
// cloudlab's own bookkeeping, not something to add to a file the user commits
// and reviews. Idempotent, and a repository that already ignores the
// directory some other way is left untouched.
func excludeWorktreeDir(ctx context.Context, localRepo string) error {
	dir, err := runLocalGit(ctx, localRepo, "rev-parse", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("locating the git directory: %w\n%s", err, dir)
	}
	gitDir := trimLine(dir)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(localRepo, gitDir)
	}

	exclude := filepath.Join(gitDir, "info", "exclude")
	// #nosec G304 -- path derived from git's own --git-common-dir, same
	// as the OpenFile below which already carries this annotation.
	if existing, err := os.ReadFile(exclude); err == nil {
		if slices.Contains(nonEmptyLines(string(existing)), worktreeDirPattern) {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(exclude), 0o750); err != nil {
		return fmt.Errorf("preparing %s: %w", exclude, err)
	}
	// #nosec G304 -- path derived from git's own --git-common-dir.
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", exclude, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString("\n# cloudlab session worktrees\n" + worktreeDirPattern + "\n"); err != nil {
		return fmt.Errorf("writing %s: %w", exclude, err)
	}
	return nil
}
