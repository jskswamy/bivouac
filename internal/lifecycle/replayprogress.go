package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// replayProgress is how far a merge's replay of one session got.
//
// It exists because the replay range cannot be re-derived from git
// alone. A cherry-picked commit gets a new SHA, so nothing about an
// already-landed one is recognisable in HEAD..<session ref>, and the
// only thing that saves a re-run is cherry-pick dropping a commit whose
// diff is a no-op against the current tree. That holds while one run
// replays the whole range in order. It stops holding the moment a
// commit lands out of band -- a conflict the user resolved by hand,
// say, which moves the tree past what the earlier diffs assume. Starting
// over then produces a fresh conflict on work that is already merged,
// on every retry, forever. Seen on a 26-commit session that conflicted
// on #22 and could only be finished with rebase --onto by hand.
//
// Written into the repository's own git directory rather than into
// bivouac's state: every field describes this checkout's branch, it has
// to survive a crash between two runs, and it becomes meaningless the
// moment the repository does -- which is exactly the lifetime of the
// directory it sits in.
type replayProgress struct {
	// Ref is the session tip the progress was computed against. A
	// different tip means the commit list has changed underneath the
	// record and the rest of it describes a plan that no longer exists.
	Ref string `json:"ref"`
	// Base is the local commit the replay started from, across every
	// run. The signature gate verifies from here, so commits an earlier
	// run landed are checked by the run that finishes the job rather
	// than going unverified because they predate its own HEAD.
	Base string `json:"base"`
	// Done is the last original session commit the replay accounted
	// for; everything before it in the list has landed.
	Done string `json:"done,omitempty"`
	// Stopped is the original commit whose cherry-pick did not
	// complete, when the replay ended on a conflict.
	Stopped string `json:"stopped,omitempty"`
	// Onto is the local commit HEAD was at when this was written. It is
	// both the proof the replay is still on the branch -- a rewind
	// leaves it unreachable, and the record with it -- and the mark a
	// hand-finished conflict is detected against.
	Onto string `json:"onto"`
}

// replayProgressPath is where a session's progress record lives.
//
// --absolute-git-dir rather than localRepo/.git: the path is a file in
// a linked worktree, and asking git is the only answer that holds for
// both shapes.
func replayProgressPath(ctx context.Context, localRepo, session string) (string, error) {
	out, err := runLocalGit(ctx, localRepo, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", fmt.Errorf("locating the git directory of %s: %w\n%s", localRepo, err, out)
	}
	return filepath.Join(trimLine(out), "bivouac", "replay-"+session+".json"), nil
}

// loadReplayProgress reads a session's record. A missing one is not an
// error: no record is the ordinary state, and it means "replay the
// whole range", which is what every run did before this existed.
//
// An unreadable or corrupt one is treated the same way. The record is
// an optimisation over correctness that git already guarantees --
// replaying from the top is always safe, only sometimes painful -- so
// failing the merge over it would trade a working fallback for an
// outage.
func loadReplayProgress(ctx context.Context, localRepo, session string) (replayProgress, bool) {
	path, err := replayProgressPath(ctx, localRepo, session)
	if err != nil {
		return replayProgress{}, false
	}
	// #nosec G304 -- path is built from this repository's own git
	// directory and a session name CheckSessionName has already
	// restricted to letters, digits and hyphens.
	raw, err := os.ReadFile(path)
	if err != nil {
		return replayProgress{}, false
	}
	var p replayProgress
	if err := json.Unmarshal(raw, &p); err != nil || p.Ref == "" || p.Base == "" {
		return replayProgress{}, false
	}
	return p, true
}

// saveReplayProgress records where the replay has reached.
//
// Errors are returned so a caller that wants to know can look, but the
// replay itself ignores them: a record that cannot be written costs the
// next run a replay from the top, which is the behaviour it is
// improving on, and refusing to merge over it would be worse than the
// bug.
func saveReplayProgress(ctx context.Context, localRepo, session string, p replayProgress) error {
	path, err := replayProgressPath(ctx, localRepo, session)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("making %s: %w", filepath.Dir(path), err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// clearReplayProgress forgets a session's record, once the replay it
// describes is finished with.
func clearReplayProgress(ctx context.Context, localRepo, session string) {
	path, err := replayProgressPath(ctx, localRepo, session)
	if err != nil {
		return
	}
	_ = os.Remove(path)
}

// restoreReplayProgress puts the record back to what it said before a
// run that has since been rewound, clearing it when there was none.
//
// The rewind undoes this run's picks, so a record pointing past them
// would fail its own reachability check on the next run and send it
// back to the top of the range -- re-conflicting on whatever an earlier
// run landed by hand, which is the failure the record exists to
// prevent.
func restoreReplayProgress(ctx context.Context, localRepo, session string, p replayProgress, had bool) {
	if !had {
		clearReplayProgress(ctx, localRepo, session)
		return
	}
	_ = saveReplayProgress(ctx, localRepo, session, p)
}

// resumeAt returns the index in commits the replay should start from,
// the local commit the whole replay is anchored on, and whether HEAD is
// a conflict the user resolved and committed themselves.
//
// Every way the record can be stale ends the same way: start from the
// beginning, anchored on this run's HEAD, which is what merge did
// before any of this existed. The record only ever skips work it can
// still see on the branch.
func resumeAt(ctx context.Context, localRepo, session, ref, head string, commits []string) (start int, base string, handFinished bool) {
	p, ok := loadReplayProgress(ctx, localRepo, session)
	switch {
	case !ok:
		return 0, head, false
	case p.Ref != ref:
		// The session moved on the instance, so the list this record
		// indexes into is not the list in hand.
		return 0, head, false
	case !isAncestor(ctx, localRepo, p.Onto, head):
		// The branch no longer holds what the record says it landed --
		// a failed verification rewound it, or the user did. Whatever
		// it described is gone.
		return 0, head, false
	}

	last := p.Done
	// A cherry-pick that stopped on a conflict is finished by hand, so
	// the record cannot say whether it landed -- but HEAD can.
	// `git cherry-pick --abort` rewinds to exactly where the pick
	// started, which is what Onto holds; `--continue` moves past it. A
	// branch that has moved therefore says the conflict was resolved
	// and the commit is on it, and replaying it again is precisely the
	// re-conflict this record exists to prevent.
	if p.Stopped != "" && head != p.Onto {
		last = p.Stopped
		// Only when it is the single commit the resolution produced is
		// HEAD known to be that commit and nothing else, which is what
		// makes amending it safe. Anything further and the caller leaves
		// it alone; the signature gate still has the last word.
		handFinished = countCommits(ctx, localRepo, p.Onto+".."+head) == 1
	}
	if last == "" {
		return 0, p.Base, handFinished
	}
	for i, sha := range commits {
		if sha == last {
			return i + 1, p.Base, handFinished
		}
	}
	// The commit the record stopped at is not in this range at all.
	// Nothing here can say which of it landed, so start over.
	return 0, head, false
}

// countCommits returns how many commits revRange holds, or -1 when git
// cannot say.
func countCommits(ctx context.Context, localRepo, revRange string) int {
	out, err := runLocalGit(ctx, localRepo, countRangeArgs(revRange)...)
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(trimLine(out))
	if err != nil {
		return -1
	}
	return n
}

// cherryPickInProgress reports whether the repository is sitting in the
// middle of a cherry-pick -- a conflict raised by an earlier merge that
// nobody has finished or backed out of yet.
func cherryPickInProgress(ctx context.Context, localRepo string) bool {
	out, err := runLocalGit(ctx, localRepo, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return false
	}
	_, statErr := os.Stat(filepath.Join(trimLine(out), "CHERRY_PICK_HEAD"))
	return statErr == nil
}

// isAncestor reports whether commit is reachable from descendant.
// A failure to answer reads as "no", which costs a replay from the top
// rather than skipping commits that may not be there.
func isAncestor(ctx context.Context, localRepo, commit, descendant string) bool {
	if commit == "" {
		return false
	}
	_, err := runLocalGit(ctx, localRepo, "merge-base", "--is-ancestor", commit, descendant)
	return err == nil
}
