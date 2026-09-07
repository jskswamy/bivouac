package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// runLocalGit runs a git command on this machine inside localRepo.
func runLocalGit(ctx context.Context, localRepo string, args ...string) (string, error) {
	full := append([]string{"-C", localRepo}, args...)
	// #nosec G204 -- argv-array exec.Command, no shell; localRepo is the
	// user's own repo path and args are built by this package.
	out, err := exec.CommandContext(ctx, "git", full...).CombinedOutput()
	return string(out), err
}

// StartSession creates a session: a repository on the instance for the agent
// to work in, and the matching worktree on this machine to review and run it
// in. The local worktree is created here and only here -- pull updates it but
// never creates it -- so a session always has somewhere to land before any
// work exists.
//
// Every step is retry-safe, because a start that fails partway through --
// most easily at the push or fetch, the first real network operations -- must
// be fixable by running the same command again.
func StartSession(ctx context.Context, ip, user, localRepo, repoName, session string) error {
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

	if err := seedSession(ctx, ip, user, localRepo, repo, branch, url); err != nil {
		return err
	}
	return trackSession(ctx, localRepo, session, branch, url)
}

// seedSession creates the session's repository on the instance and publishes
// the Mac's current commit into it as the session branch.
//
// The order -- init, push, then checkout -- is what keeps this working
// without configuring receive.denyCurrentBranch. A fresh `git init` leaves
// HEAD on an unborn branch, so the pushed branch is not the checked-out one
// and the push is legal; the checkout afterwards gives the agent its files.
// Reversing those two steps makes every seed fail.
func seedSession(ctx context.Context, ip, user, localRepo, repo, branch, url string) error {
	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

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
	return nil
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

// trimLine returns s without surrounding whitespace or a trailing newline.
func trimLine(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

// RescueSession makes every scrap of a session's work durable on this
// machine: it commits whatever the agent left uncommitted, fetches the
// session branch, and verifies the fetched tip is genuinely in the local
// object store. Returns the local ref the work landed on and the
// instance-side tip that was verified -- a caller that goes on to delete
// anything needs the tip to confirm the session has not moved since (see
// MergeSession).
//
// Idempotent by construction -- the checkpoint is a no-op on a clean tree
// and the fetch moves only missing objects -- so retrying after fixing one
// problem is always safe.
func RescueSession(ctx context.Context, ip, user, localRepo, repoName, session string) (string, string, error) {
	if err := CheckSessionName(session); err != nil {
		return "", "", err
	}
	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = client.Close() }()

	repo := RemoteRepoPath(user, session, repoName)
	if out, err := client.Run(checkpointCmd(repo, checkpointMessage(session))); err != nil {
		return "", "", fmt.Errorf("checkpointing work on instance: %w\n%s", err, out)
	}

	branch := SessionBranch(session)
	tip, err := client.Run(remoteGitCmd(repo, "rev-parse", branch))
	if err != nil {
		return "", "", fmt.Errorf("reading session tip on instance: %w\n%s", err, tip)
	}
	tip = trimLine(tip)

	remote := sessionRemote(session)
	if out, err := runLocalGit(ctx, localRepo, fetchRemoteArgs(remote)...); err != nil {
		return "", "", fmt.Errorf("fetching session %s: %w\n%s", session, err, out)
	}
	ref := remote + "/" + branch

	if err := verifyFetched(ctx, localRepo, tip); err != nil {
		return "", "", err
	}
	return ref, tip, nil
}

// checkpointMessage is the subject the instance-side checkpoint commits
// under. Shared so MergeSession's pre-delete re-checkpoint is
// indistinguishable from the rescue's own.
func checkpointMessage(session string) string {
	return "cloudlab: checkpoint " + session
}

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

// nonEmptyLines splits s on newlines, dropping blanks.
func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if trimmed := trimLine(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
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

// MergeSession accepts a session's work: it rescues anything outstanding,
// replays each commit onto the current branch with a signature, verifies
// the result, and only then deletes the session from both machines.
//
// The ordering is the safety property. Deletion is strictly downstream of
// verification, so nothing is ever removed from the instance before its
// commits are provably in this machine's object store.
func MergeSession(ctx context.Context, ip, user, localRepo, repoName, session, sessionBase string) ([]string, error) {
	if err := CheckSessionName(session); err != nil {
		return nil, err
	}

	if err := requireBaseStillReachable(ctx, localRepo, sessionBase, session); err != nil {
		return nil, err
	}

	// Tracked modifications only. The gate exists because a cherry-pick can
	// fail against a dirty tree, and untracked files only collide when a
	// replayed commit adds that same path -- which git refuses on its own,
	// with a better message than this one could give. Counting all untracked
	// content made merge unusable in any repository holding a stray file:
	// cloudlab's own cloudlab.pkl is untracked and not gitignored, so cloudlab
	// shipped a file that broke cloudlab merge.
	status, err := runLocalGit(ctx, localRepo, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return nil, fmt.Errorf("checking working tree: %w\n%s", err, status)
	}
	if trimLine(status) != "" {
		return nil, fmt.Errorf("your working tree has uncommitted changes — commit or stash them before merging a session")
	}

	local := LocalWorktreePath(localRepo, session)
	// The session worktree is the user's to work in -- they are told to cd
	// into it and run the agent's code -- so merge must not delete it out
	// from under uncommitted edits, which is exactly what pull refuses to do.
	// Checked up front so a refusal leaves both machines untouched.
	if err := requireCleanSessionWorktree(ctx, local); err != nil {
		return nil, err
	}

	ref, tip, err := RescueSession(ctx, ip, user, localRepo, repoName, session)
	if err != nil {
		return nil, err
	}

	branch, err := runLocalGit(ctx, localRepo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("reading current branch: %w\n%s", err, branch)
	}
	onto := trimLine(branch)
	// A SHA, not the branch name: the cherry-pick moves the branch, so
	// afterwards "<name>..HEAD" is empty and would verify nothing at all.
	// The name is kept separately, for the messages that name it.
	head, err := runLocalGit(ctx, localRepo, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("reading current commit: %w\n%s", err, head)
	}
	base := trimLine(head)

	count, err := runLocalGit(ctx, localRepo, countRangeArgs("HEAD.."+ref)...)
	if err != nil {
		return nil, fmt.Errorf("counting session commits: %w\n%s", err, count)
	}

	var signed []string
	// An agent that produced nothing, or a session already merged, still has
	// to be retirable -- cherry-pick on an empty range exits 128.
	if trimLine(count) != "0" {
		provider.ReportProgress(ctx, "replaying and signing "+session)
		if err := replaySigned(ctx, localRepo, ref, session, onto); err != nil {
			return nil, err
		}
		// Re-read HEAD rather than trusting the count above. rev-list has no
		// patch-id awareness, so it still counts commits that already landed
		// on this branch from an earlier merge, and --empty=drop then
		// correctly drops every one of them and leaves HEAD exactly where it
		// was. That is a success, not the empty range verifySignatures
		// refuses; without this check re-running merge could never succeed
		// and the session could only be retired by down.
		//
		// Skipping verification here is only sound because a failed
		// verification below rewinds the branch. An unmoved HEAD therefore
		// means the commits were replayed AND verified by an earlier run --
		// never that an earlier run left unverified commits behind.
		after, err := runLocalGit(ctx, localRepo, "rev-parse", "HEAD")
		if err != nil {
			return nil, fmt.Errorf("reading replayed commit: %w\n%s", err, after)
		}
		if trimLine(after) != base {
			signed, err = verifySignatures(ctx, localRepo, base+"..HEAD")
			if err != nil {
				// Undo the replay. Leaving unverified commits on the branch
				// is the exact outcome the gate exists to prevent, and it is
				// worse than failing: the next `cloudlab merge` would find
				// them already applied, drop them all as empty, see an
				// unmoved HEAD, skip verification and retire the session --
				// turning a refusal into a silent pass. The work is not at
				// risk, since nothing on the instance has been touched yet.
				if out, resetErr := runLocalGit(ctx, localRepo, "reset", "--hard", base); resetErr != nil {
					return nil, fmt.Errorf("%w\n\nthe replayed commits could not be rolled back either (%v)\n%s\nyour branch is at %s and holds unverified commits — reset to %s by hand before merging again", err, resetErr, out, trimLine(after), base)
				}
				return nil, err
			}
		}
	}

	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()

	repo := RemoteRepoPath(user, session, repoName)

	// The agent keeps running while the replay happens, and signing can
	// block for minutes on a hardware key touch. Anything it committed or
	// wrote in that window is not in the verified fetch, so re-checkpoint
	// and re-read the tip: if it moved, nothing is deleted and re-running
	// merge picks the new work up.
	if out, err := client.Run(checkpointCmd(repo, checkpointMessage(session))); err != nil {
		return nil, fmt.Errorf("re-checking session %s before deleting it: %w\n%s", session, err, out)
	}
	now, err := client.Run(remoteGitCmd(repo, "rev-parse", SessionBranch(session)))
	if err != nil {
		return nil, fmt.Errorf("re-reading session tip on instance: %w\n%s", err, now)
	}
	if trimLine(now) != tip {
		return nil, fmt.Errorf("session %s moved on the instance while merging (%s → %s) — nothing was deleted and the new work is safe; run `cloudlab session merge %s` again to take it too", session, tip, trimLine(now), session)
	}

	if out, err := client.Run(removeRepoCmd(repo)); err != nil {
		return nil, fmt.Errorf("removing session repo on instance: %w\n%s", err, out)
	}

	// No --force: the cleanliness check above is the decision, and git
	// refusing here as well is a second gate rather than a contradiction.
	// prune afterwards so a worktree the user deleted by hand does not
	// leave a stale entry that blocks starting the session again.
	if _, err := os.Stat(local); err == nil {
		if out, err := runLocalGit(ctx, localRepo, "worktree", "remove", local); err != nil {
			return nil, fmt.Errorf("removing local worktree %s: %w\n%s", local, err, out)
		}
	}
	_, _ = runLocalGit(ctx, localRepo, "worktree", "prune")

	// Once the cherry-pick has landed, a cleanup failure below must say the
	// merge succeeded -- retrying `cloudlab merge` against an already-deleted
	// session would otherwise fail confusingly inside RescueSession.
	if out, err := runLocalGit(ctx, localRepo, remoteRemoveArgs(sessionRemote(session))...); err != nil {
		return nil, fmt.Errorf("merge succeeded and the work is on %s, but removing remote %s failed: %w\n%s", onto, sessionRemote(session), err, out)
	}
	return signed, nil
}

// replaySigned replays every commit the session added onto the current branch,
// signing each one and stripping the attribution trailers an agent wrote.
//
// One commit at a time rather than one cherry-pick of the whole range, because
// a message can only be corrected once its commit exists. That also makes a
// conflict name the commit it happened on, which the range form could not.
//
// --empty=drop still does the work that makes a re-run safe: commits already
// present are dropped rather than stopping the pick, so HEAD simply does not
// move and the caller reads that as "already landed".
func replaySigned(ctx context.Context, localRepo, ref, session, onto string) error {
	list, err := runLocalGit(ctx, localRepo, "rev-list", "--reverse", "HEAD.."+ref)
	if err != nil {
		return fmt.Errorf("listing session commits: %w\n%s", err, list)
	}

	for _, sha := range nonEmptyLines(list) {
		before, err := HeadCommit(ctx, localRepo)
		if err != nil {
			return err
		}
		if out, err := runLocalGit(ctx, localRepo, cherryPickSignArgs(sha)...); err != nil {
			return fmt.Errorf("replaying %s from session %s onto %s: %w\n%s\nresolve the conflict then `git cherry-pick --continue`, or `git cherry-pick --abort` to back out", sha[:min(len(sha), 8)], session, onto, err, out)
		}
		after, err := HeadCommit(ctx, localRepo)
		if err != nil {
			return err
		}
		if after == before {
			continue // dropped as already applied
		}
		if err := cleanReplayedMessage(ctx, localRepo); err != nil {
			return err
		}
	}
	return nil
}

// cleanReplayedMessage rewrites HEAD's message without its AI attribution,
// re-signing as it goes.
//
// merge re-signs everything it replays, which is the human asserting they
// vouch for the commit. Leaving a trailer that credits an AI makes that
// signature attest to something the signer did not write -- and it silently
// reintroduces exactly what a human might have stripped from the branch by
// hand. Amending only when the message actually changes keeps the common case
// free.
func cleanReplayedMessage(ctx context.Context, localRepo string) error {
	msg, err := runLocalGit(ctx, localRepo, "log", "-1", "--format=%B")
	if err != nil {
		return fmt.Errorf("reading replayed message: %w\n%s", err, msg)
	}
	cleaned := stripAgentTrailers(msg)
	if cleaned == msg {
		return nil
	}
	if out, err := runLocalGit(ctx, localRepo, "commit", "--amend", "--quiet", "-S", "-m", cleaned); err != nil {
		return fmt.Errorf("stripping agent attribution from the replayed message: %w\n%s", err, out)
	}
	return nil
}

// HeadCommit returns the SHA localRepo's HEAD points at. Exported so session
// start can record the commit a session branches from; merge needs it to tell
// the session's own work apart from history that was rewritten underneath it.
func HeadCommit(ctx context.Context, localRepo string) (string, error) {
	out, err := runLocalGit(ctx, localRepo, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("reading HEAD in %s: %w\n%s", localRepo, err, out)
	}
	return trimLine(out), nil
}

// requireBaseStillReachable refuses when the branch a session was started from
// has been rewritten underneath it.
//
// merge computes its replay range as HEAD..<session ref>, which is only
// correct while the session's base is still an ancestor of HEAD. A rebase,
// amend, squash or re-sign gives every base commit a new SHA, so the recorded
// base becomes unreachable and the range silently widens to include a
// pre-rewrite copy of the whole branch history. Nothing about that is visible
// in the commit count, so merge would replay it.
//
// An empty base means the session predates this field; there is nothing to
// check, and refusing would strand sessions that are otherwise fine.
func requireBaseStillReachable(ctx context.Context, localRepo, sessionBase, session string) error {
	if sessionBase == "" {
		return nil
	}
	// cat-file first: after a rewrite plus gc the base can be gone entirely,
	// and merge-base would then fail with git's own opaque message.
	if _, err := runLocalGit(ctx, localRepo, "cat-file", "-e", sessionBase+"^{commit}"); err != nil {
		return fmt.Errorf("session %s started from %s, which no longer exists in this repository — the branch was rewritten and then garbage collected\nthe session's own commits are still on the instance; `cloudlab session pull %s` fetches them so you can replay them by hand", session, sessionBase, session)
	}
	if _, err := runLocalGit(ctx, localRepo, "merge-base", "--is-ancestor", sessionBase, "HEAD"); err != nil {
		return fmt.Errorf("session %s started from %s, which is no longer an ancestor of HEAD — this branch was rewritten (rebase, amend, squash or re-sign) while the session was open\nreplaying now would re-apply the pre-rewrite history as well as the session's own work\n`cloudlab session pull %s` fetches the session safely; cherry-pick its commits onto the rewritten branch by hand, then `cloudlab down` to retire the instance", session, sessionBase, session)
	}
	return nil
}

// requireCleanSessionWorktree refuses when the local session worktree holds
// uncommitted work. A worktree the user has already deleted is not an
// obstacle, so an absent path passes.
//
// Untracked files DO count here, unlike the check on the user's own repository
// above. The asymmetry is deliberate: merge deletes this worktree, so an
// untracked file in it is destroyed rather than merely stepped around. git
// worktree remove refuses for the same reason; this check just gets there
// first, with a message that names the path.
func requireCleanSessionWorktree(ctx context.Context, local string) error {
	if _, err := os.Stat(local); err != nil {
		return nil
	}
	status, err := runLocalGit(ctx, local, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("checking session worktree %s: %w\n%s", local, err, status)
	}
	if trimLine(status) != "" {
		return fmt.Errorf("the session worktree at %s has uncommitted changes — merge would delete it; commit or discard them there first", local)
	}
	return nil
}

// verifySignatures reports each commit in revRange with its signature
// state, and fails if any is unsigned. Signing was the reason to cherry-pick
// rather than squash, so an unsigned result is a failure, not a detail.
//
// An empty range is also a failure. It is only ever called where commits are
// known to have been replayed, so no lines means the range was computed
// wrongly -- and a verification that inspects nothing must never be mistaken
// for one that passed, since deletion is downstream of it.
func verifySignatures(ctx context.Context, localRepo, revRange string) ([]string, error) {
	out, err := runLocalGit(ctx, localRepo, signatureStatusArgs(revRange)...)
	if err != nil {
		return nil, fmt.Errorf("checking signatures: %w\n%s", err, out)
	}
	lines := nonEmptyLines(out)
	if len(lines) == 0 {
		return nil, fmt.Errorf("no commits found in range %s — refusing to treat an empty signature check as a pass", revRange)
	}
	for _, line := range lines {
		if !strings.HasSuffix(line, " G") {
			// %G? reports what git can *verify*, not what was signed, so the
			// usual cause is a missing trust store rather than a missing key:
			// ssh signing needs gpg.ssh.allowedSignersFile to exist and list
			// your own key, and without it every commit reports N even though
			// cherry-pick -S signed it. Naming only gpgsign/signingkey here
			// sends people to settings that are already correct.
			return nil, fmt.Errorf("git cannot verify commit %q\ncheck that commit.gpgsign and user.signingkey are set, and — for ssh signing — that gpg.ssh.allowedSignersFile points at a file listing your own signing key\nthe replayed commits have been rolled back; fix the config and re-run", line)
		}
	}
	return lines, nil
}

// localIdentity reads the git identity that applies in localRepo.
//
// Plain `git config` rather than `--local`, so the value is resolved the way
// git itself resolves it -- through the repository, then the user's global
// config, then system. Most people set this once in ~/.gitconfig and never
// per-repo, so reading only the local scope would find nothing for almost
// everyone.
func localIdentity(ctx context.Context, localRepo string) (string, string, error) {
	name, err := runLocalGit(ctx, localRepo, "config", "user.name")
	if err != nil {
		return "", "", fmt.Errorf("reading user.name: %w\nset it with `git config --global user.name \"Your Name\"` -- the instance needs an identity to commit with, and without one git invents one from the hostname", err)
	}
	email, err := runLocalGit(ctx, localRepo, "config", "user.email")
	if err != nil {
		return "", "", fmt.Errorf("reading user.email: %w\nset it with `git config --global user.email you@example.com` -- the instance needs an identity to commit with, and without one git invents one from the hostname", err)
	}
	return trimLine(name), trimLine(email), nil
}
