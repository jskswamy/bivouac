package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jskswamy/bivouac/internal/beads"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/state"
)

// ErrIssuesUnsynced marks a merge stopped because the session's issues are
// not all known to have landed locally. pauseRetirement keys the pause
// reason on it, so every route to that stop -- a failed pull, a gap, an
// unknown -- must wrap it.
var ErrIssuesUnsynced = errors.New("issues not synced")

// errLocalBdUnavailable stands in for the local database when bd is not on
// this machine, so CheckSynced answers unknown for every change rather than
// treating an absent database as an empty one.
var errLocalBdUnavailable = errors.New("bd is not available on this machine")

// localIssues is beads.LocalIssues, swappable so verification can be tested
// without a real local database.
var localIssues = beads.LocalIssues

// beadsRunner is the shape of reconcile.Client.Run. beads' own seam for it
// is unexported, but any value with this method satisfies it, so a
// lifecycle-local copy lets tests drive these readers with a fake.
type beadsRunner interface {
	Run(cmd string) (output string, err error)
}

// ReadBeadsStart records where a session's issue database stands before the
// agent touches it. Called before the --issue claim, so that claim is the
// session's first change rather than part of its baseline.
func ReadBeadsStart(client beadsRunner, repo, issue string) (state.SessionBeads, error) {
	commit, err := beads.InstanceCommit(client, repo)
	if err != nil {
		return state.SessionBeads{}, err
	}
	rec := state.SessionBeads{StartCommit: commit, Issue: issue}
	if issue == "" {
		return rec, nil
	}
	tree, err := beads.Descendants(client, repo, issue)
	if err != nil {
		return state.SessionBeads{}, err
	}
	for _, is := range tree {
		rec.Planned = append(rec.Planned, is.ID)
	}
	return rec, nil
}

// issueReceipt counts a session's changes by kind and lists every one not
// known to have landed locally, unknown included: for a merge an unknown is
// as unsafe as a gap.
func issueReceipt(changes []beads.IssueSync) state.IssueReceipt {
	var r state.IssueReceipt
	for _, c := range changes {
		switch c.Kind {
		case beads.Created:
			r.Created++
		case beads.Closed:
			r.Closed++
		case beads.Updated:
			r.Updated++
		case beads.Removed:
			r.Removed++
		}
		if c.Sync == beads.Synced {
			continue
		}
		u := state.UnsyncedIssue{
			ID: c.Issue.ID, Title: c.Issue.Title, Sync: string(c.Sync),
			InstanceUpdated: rfc3339(c.Issue.UpdatedAt),
		}
		if c.Local != nil {
			u.LocalUpdated = rfc3339(c.Local.UpdatedAt)
		}
		r.Unsynced = append(r.Unsynced, u)
	}
	return r
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// verifyIssues checks the session's changes up to pinned against the local
// database as it is now.
//
// The diff ends at pinned, never at the instance's current commit: pinned is
// what the pull brought home, and an issue the agent edits after it belongs
// to the next round. Diffing to the live commit would make every re-run of
// merge find a fresh gap and never finish.
//
// beads.ErrHistoryRewritten comes back unchanged so the gate can tell "the
// start is gone" (an unknown the user may accept) from any other failure.
func verifyIssues(ctx context.Context, client beadsRunner, localRepo, repo string, start state.SessionBeads, pinned string, localAvailable bool) (state.IssueReceipt, error) {
	changes, err := beads.SessionChanges(client, repo, start.StartCommit, pinned)
	if err != nil {
		return state.IssueReceipt{}, err
	}
	return issueReceipt(checkSynced(ctx, changes, localRepo, localAvailable)), nil
}

// checkSynced compares changes with the local database. Without a local bd
// every change is unknown rather than missing: nothing was looked at.
func checkSynced(ctx context.Context, changes []beads.Change, localRepo string, localAvailable bool) []beads.IssueSync {
	var local map[string]beads.Issue
	localErr := errLocalBdUnavailable
	if localAvailable {
		local, localErr = localIssues(ctx, localRepo)
	}
	return beads.CheckSynced(changes, local, localErr)
}

// gateIssues turns a verification into merge's verdict: the evidence to
// record, the receipt to print, or an error wrapping ErrIssuesUnsynced.
//
// accept (--accept-unsynced) lets a gap or a rewritten history through, and
// only those: it is for gaps that will never close, such as an issue
// deleted locally. Any other verification failure left nothing to compare,
// so it pauses whatever the flag says. A clean result records verified even
// with the flag, so the evidence never claims an override that was not
// needed.
//
// final is the late-work re-check, which runs after the replay and after the
// panes closed. Its message says what is still intact rather than implying
// nothing was touched.
func gateIssues(session string, receipt state.IssueReceipt, verifyErr error, pinned string, accept, final bool) (string, *state.IssueReceipt, error) {
	if verifyErr == nil && len(receipt.Unsynced) == 0 {
		return "verified:" + pinned, &receipt, nil
	}
	if verifyErr != nil && !errors.Is(verifyErr, beads.ErrHistoryRewritten) {
		return "", nil, issuePause(session, verifyErr, "", final)
	}
	ids := make([]string, 0, len(receipt.Unsynced))
	for _, u := range receipt.Unsynced {
		ids = append(ids, u.ID)
	}
	if accept {
		receipt.Accepted = true
		return "accepted-unsynced:" + pinned + ":" + strings.Join(ids, ","), &receipt, nil
	}
	cause := verifyErr
	if cause == nil {
		cause = fmt.Errorf("not on this machine: %s", strings.Join(ids, ", "))
	}
	return "", nil, issuePause(session, cause, ", or pass --accept-unsynced to merge without them", final)
}

// issuePause is the error every issue stop shares: what is missing, how to
// resume, and, after the replay, what is still safe.
func issuePause(session string, cause error, override string, final bool) error {
	intact := ""
	if final {
		intact = "\nthe session repository and its issue database are still on the instance"
	}
	return fmt.Errorf("session %s %w: %w\nrun `bivouac session merge %s` again once they land%s%s",
		session, ErrIssuesUnsynced, cause, session, override, intact)
}

// pullReceipt is what pull prints after bringing issues home. pull only
// reports, so anything that stops the receipt being built -- a rewritten
// history included -- is a warning and no receipt, never a failed pull.
func pullReceipt(ctx context.Context, client beadsRunner, localRepo, repo string, start state.SessionBeads, pinned string, localAvailable bool) *state.IssueReceipt {
	receipt, err := verifyIssues(ctx, client, localRepo, repo, start, pinned, localAvailable)
	if err != nil {
		provider.ReportWarning(ctx, "issue receipt: "+err.Error())
		return nil
	}
	return &receipt
}
