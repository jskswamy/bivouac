package lifecycle

import (
	"context"
	"fmt"
	"os"

	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/reconcile"
	"github.com/jskswamy/bivouac/internal/state"
)

// MergeSession accepts a session's work: it rescues anything outstanding,
// replays each commit onto the current branch with a signature, verifies
// the result, and only then deletes the session from both machines.
//
// The ordering is the safety property. Deletion is strictly downstream of
// verification, so nothing is ever removed from the instance before its
// commits are provably in this machine's object store.
func MergeSession(ctx context.Context, ip, user, repoName string, sess state.Session) ([]string, error) {
	// Unpacked once rather than threaded as four more parameters: merge
	// needs the recorded herdr machine id for its cleanup, and passing the
	// record is smaller than passing everything in it.
	localRepo, session, sessionBase := sess.LocalRepo, sess.Name, sess.Base
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
	// bivouac's own bivouac.pkl is untracked and not gitignored, so bivouac
	// shipped a file that broke bivouac merge.
	//
	// An open cherry-pick is checked first, because it trips that gate too
	// and "commit or stash them" is advice nobody can follow mid-pick --
	// it hides the one fact that explains the state.
	if cherryPickInProgress(ctx, localRepo) {
		return nil, fmt.Errorf("a cherry-pick from an earlier `bivouac session merge %s` is still open in %s — resolve it and `git cherry-pick --continue`, or `git cherry-pick --abort` to back out, then run merge again; it picks up from there rather than replaying what has already landed", session, localRepo)
	}

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

	// Before the replay, so the issue state matching the commits about to
	// land is already local. merge then removes the session repository --
	// which is where refs/dolt/data lives -- so a failed sync here is issue
	// work about to be destroyed, not a deferred problem. It still does not
	// refuse: merge has no --force, and a beads failure must never be the
	// thing that strands a user's commits on a box they then have to salvage
	// by hand. Saying plainly what is at stake is the honest middle.
	if client, err := reconcile.Connect(ctx, ip, user); err == nil {
		synced := pullBeads(ctx, client, localRepo, RemoteRepoPath(user, session, repoName), session)
		_ = client.Close()
		if !synced {
			provider.ReportWarning(ctx, fmt.Sprintf("beads: merge removes the session repository, and its issue database with it — any issue edits that did not sync are about to be lost; fix the problem and `bivouac session pull %s` first to keep them", session))
		}
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

	// Snapshotted before the replay so a rollback can put it back. The
	// rewind undoes this run's picks, and a record pointing past them
	// would send the next run to the top of the range -- re-conflicting
	// on whatever an earlier run landed by hand.
	resume, resuming := loadReplayProgress(ctx, localRepo, session)

	var signed []string
	// An agent that produced nothing, or a session already merged, still has
	// to be retirable -- cherry-pick on an empty range exits 128.
	if trimLine(count) != "0" {
		provider.ReportProgress(ctx, "replaying and signing "+session)
		// replayBase, not base: a resumed replay is anchored where the run
		// that started it was, so the commits an interrupted run landed are
		// verified by the run that finishes the job instead of going
		// unchecked because they predate its own HEAD.
		replayBase, err := replaySigned(ctx, localRepo, ref, session, onto)
		if err != nil {
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
		if trimLine(after) != replayBase {
			signed, err = verifySignatures(ctx, localRepo, replayBase+"..HEAD")
			if err != nil {
				// Undo the replay. Leaving unverified commits on the branch
				// is the exact outcome the gate exists to prevent, and it is
				// worse than failing: the next `bivouac merge` would find
				// them already applied, drop them all as empty, see an
				// unmoved HEAD, skip verification and retire the session --
				// turning a refusal into a silent pass. The work is not at
				// risk, since nothing on the instance has been touched yet.
				//
				// Only this run's own picks are rewound, even though the
				// verified range may be wider. Commits an earlier run landed
				// are not this run's to discard -- one of them may be a
				// conflict the user resolved by hand -- and they stay
				// covered, because the restored record makes the next run
				// verify from the same anchor again.
				if out, resetErr := runLocalGit(ctx, localRepo, "reset", "--hard", base); resetErr != nil {
					return nil, fmt.Errorf("%w\n\nthe replayed commits could not be rolled back either (%v)\n%s\nyour branch is at %s and holds unverified commits — reset to %s by hand before merging again", err, resetErr, out, trimLine(after), base)
				}
				restoreReplayProgress(ctx, localRepo, session, resume, resuming)
				return nil, err
			}
		}
	}
	// The replay is done with, verified, and about to be retired: nothing
	// is left for a later run to pick up. Cleared here rather than at the
	// end of replaySigned so that a verification failure above still has a
	// record to put back.
	clearReplayProgress(ctx, localRepo, session)

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
		return nil, fmt.Errorf("session %s moved on the instance while merging (%s → %s) — nothing was deleted and the new work is safe; run `bivouac session merge %s` again to take it too", session, tip, trimLine(now), session)
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

	dropBeadsRemote(ctx, localRepo, session)

	// Same shape again. A fresh connection because the one above was closed
	// before the replay -- stopping the session server had to wait until the
	// commits had actually landed, since a rolled-back replay leaves the
	// session very much alive.
	var onInstance remoteRunner
	if client, err := reconcile.Connect(ctx, ip, user); err == nil {
		onInstance = client
		defer func() { _ = client.Close() }()
	}
	CleanupHerdr(ctx, sess.HerdrMachineID, session, onInstance)

	// Once the cherry-pick has landed, a cleanup failure below must say the
	// merge succeeded -- retrying `bivouac merge` against an already-deleted
	// session would otherwise fail confusingly inside RescueSession.
	if out, err := runLocalGit(ctx, localRepo, remoteRemoveArgs(sessionRemote(session))...); err != nil {
		return nil, fmt.Errorf("merge succeeded and the work is on %s, but removing remote %s failed: %w\n%s", onto, sessionRemote(session), err, out)
	}
	return signed, nil
}

// replaySigned replays every commit the session added onto the current branch,
// signing each one and stripping the attribution trailers an agent wrote. It
// returns the local commit the whole replay is anchored on -- this run's HEAD
// for a fresh replay, the earlier run's for a resumed one -- which is the
// range the caller verifies.
//
// One commit at a time rather than one cherry-pick of the whole range, because
// a message can only be corrected once its commit exists. That also makes a
// conflict name the commit it happened on, which the range form could not.
//
// --empty=drop still does the work that makes a re-run safe: commits already
// present are dropped rather than stopping the pick, so HEAD simply does not
// move and the caller reads that as "already landed". That is enough on its
// own only while one run replays the whole range in order; once a commit lands
// out of band -- a conflict resolved by hand -- the tree has moved past what
// the earlier diffs assume and they conflict instead of dropping. The progress
// record is what carries a replay across runs through that; see
// replayProgress.
func replaySigned(ctx context.Context, localRepo, ref, session, onto string) (string, error) {
	list, err := runLocalGit(ctx, localRepo, "rev-list", "--reverse", "HEAD.."+ref)
	if err != nil {
		return "", fmt.Errorf("listing session commits: %w\n%s", err, list)
	}
	commits := nonEmptyLines(list)

	head, err := HeadCommit(ctx, localRepo)
	if err != nil {
		return "", err
	}
	start, base, handFinished := resumeAt(ctx, localRepo, session, ref, head, commits)
	if start > 0 {
		provider.ReportProgress(ctx, fmt.Sprintf("resuming %s at commit %d of %d", session, start+1, len(commits)))
	}
	if handFinished {
		if err := resignHandFinished(ctx, localRepo); err != nil {
			return "", err
		}
		if head, err = HeadCommit(ctx, localRepo); err != nil {
			return "", err
		}
	}

	progress := replayProgress{Ref: ref, Base: base, Onto: head}
	if start > 0 {
		progress.Done = commits[start-1]
	}
	if handFinished {
		// Saved before the loop, not left to the first pick: the amend
		// moved HEAD past the Onto the record was matched against, and a
		// merge that fails after this point for some unrelated reason
		// would otherwise see the same mismatch next time and amend an
		// already-signed commit, churning the branch on every run.
		_ = saveReplayProgress(ctx, localRepo, session, progress)
	}

	for _, sha := range commits[start:] {
		before, err := HeadCommit(ctx, localRepo)
		if err != nil {
			return "", err
		}
		if out, err := runLocalGit(ctx, localRepo, cherryPickSignArgs(sha)...); err != nil {
			// Written before the error is returned, and naming the commit
			// that stopped rather than one that landed: the next run reads
			// it to find out whether the conflict was resolved and
			// continued, or aborted, which is the difference between
			// carrying on and picking this commit up again.
			progress.Stopped = sha
			progress.Onto = before
			_ = saveReplayProgress(ctx, localRepo, session, progress)
			return "", fmt.Errorf("replaying %s from session %s onto %s: %w\n%s\nresolve the conflict then `git cherry-pick --continue`, or `git cherry-pick --abort` to back out\nthen `bivouac session merge %s` again -- it picks up from here rather than replaying what has already landed", sha[:min(len(sha), 8)], session, onto, err, out, session)
		}
		after, err := HeadCommit(ctx, localRepo)
		if err != nil {
			return "", err
		}
		if after != before {
			if err := cleanReplayedMessage(ctx, localRepo); err != nil {
				return "", err
			}
			after, err = HeadCommit(ctx, localRepo)
			if err != nil {
				return "", err
			}
		}
		// Recorded per commit, not per run: a crash or a Ctrl-C between
		// two picks is exactly the interruption this has to survive, and
		// the only thing that makes it survivable is that the record was
		// already on disk.
		progress.Done, progress.Stopped, progress.Onto = sha, "", after
		_ = saveReplayProgress(ctx, localRepo, session, progress)
	}
	return base, nil
}

// resignHandFinished signs and cleans the commit a user made to finish a
// conflict the replay stopped on.
//
// `git cherry-pick --continue` does not carry the -S the interrupted
// pick was given: measured, not assumed -- the commit it writes reports
// %G? = N. The signature is the entire reason merge cherry-picks rather
// than squashes, so a commit that arrived through the documented
// conflict-recovery path cannot be the one exception to it, and the
// alternative is a merge that refuses at the gate for doing exactly what
// it was told to do.
//
// Amending unconditionally, unlike cleanReplayedMessage: there is no
// message change to key off, and the thing being fixed is the signature.
func resignHandFinished(ctx context.Context, localRepo string) error {
	msg, err := runLocalGit(ctx, localRepo, "log", "-1", "--format=%B")
	if err != nil {
		return fmt.Errorf("reading the resolved commit's message: %w\n%s", err, msg)
	}
	if out, err := runLocalGit(ctx, localRepo, "commit", "--amend", "--quiet", "-S", "-m", stripAgentTrailers(msg)); err != nil {
		return fmt.Errorf("signing the conflict you resolved by hand: %w\n%s", err, out)
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
		return fmt.Errorf("session %s started from %s, which no longer exists in this repository — the branch was rewritten and then garbage collected\nthe session's own commits are still on the instance; `bivouac session pull %s` fetches them so you can replay them by hand", session, sessionBase, session)
	}
	if _, err := runLocalGit(ctx, localRepo, "merge-base", "--is-ancestor", sessionBase, "HEAD"); err != nil {
		return fmt.Errorf("session %s started from %s, which is no longer an ancestor of HEAD — this branch was rewritten (rebase, amend, squash or re-sign) while the session was open\nreplaying now would re-apply the pre-rewrite history as well as the session's own work\n`bivouac session pull %s` fetches the session safely; cherry-pick its commits onto the rewritten branch by hand, then `bivouac down` to retire the instance", session, sessionBase, session)
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
