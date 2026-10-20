package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jskswamy/bivouac/internal/beads"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/reconcile"
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

// IssueListing is every issue a session has touched so far, with whether
// each has reached the local database. Recorded false means the session has
// no start record and every other field is empty.
type IssueListing struct {
	Session, Issue, IssueStatus, StartCommit, InstanceCommit string
	Recorded, HistoryRewritten                               bool
	Changes                                                  []beads.IssueSync
	// OutsideEpic is the issues created this session that are not under
	// Issue now: discovered work the agent filed somewhere else.
	OutsideEpic []string
}

// SessionIssues lists a session's issue changes live from the instance,
// against the local database as it is now. It never pulls: run before a
// pull, it shows what the pull would bring.
//
// The diff ends at the instance's current commit rather than a pin, since
// nothing here acts on the answer and the latest edits are what the user
// asked to see.
func SessionIssues(ctx context.Context, ip, user, instanceName string, sess state.Session) (IssueListing, error) {
	if sess.Beads == nil {
		return IssueListing{Session: sess.Name}, nil
	}
	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		return IssueListing{}, fmt.Errorf("connecting to %s: %w", instanceName, err)
	}
	defer func() { _ = client.Close() }()
	repo := RemoteRepoPath(user, sess.Name, sess.RepoNameOr(instanceName))
	return listIssues(ctx, client, sess.LocalRepo, repo, sess.Name, *sess.Beads, beads.Available())
}

// listIssues is SessionIssues past the connection, so it can run against a
// fake instance.
func listIssues(ctx context.Context, client beadsRunner, localRepo, repo, session string, start state.SessionBeads, localAvailable bool) (IssueListing, error) {
	l := IssueListing{Session: session, Issue: start.Issue, StartCommit: start.StartCommit, Recorded: true}
	now, err := beads.InstanceCommit(client, repo)
	if err != nil {
		return IssueListing{}, err
	}
	l.InstanceCommit = now
	changes, err := beads.SessionChanges(client, repo, start.StartCommit, now)
	if errors.Is(err, beads.ErrHistoryRewritten) {
		// A finding to show, not a failure: the commits above are still
		// worth reporting, and there is nothing to list beside them.
		l.HistoryRewritten = true
		return l, nil
	}
	if err != nil {
		return IssueListing{}, err
	}
	l.Changes = checkSynced(ctx, changes, localRepo, localAvailable)
	if start.Issue == "" {
		return l, nil
	}
	// The tree as it stands now, not Planned: an issue the agent created
	// and then parented under the target is inside it, not discovered
	// elsewhere.
	tree, err := beads.Descendants(client, repo, start.Issue)
	if err != nil {
		return IssueListing{}, err
	}
	l.IssueStatus = tree[0].Status
	under := make(map[string]bool, len(tree))
	for _, is := range tree {
		under[is.ID] = true
	}
	for _, c := range changes {
		if c.Kind == beads.Created && !under[c.Issue.ID] {
			l.OutsideEpic = append(l.OutsideEpic, c.Issue.ID)
		}
	}
	return l, nil
}

// PlannedIssue is one issue the session was started to do, as it stands on
// the instance now.
type PlannedIssue struct {
	ID, Title, Status string
	// Removed means the issue no longer exists on the instance. It stays in
	// the list so the planned total never shrinks without saying why.
	Removed bool
}

// SessionProgress is `session status`: the session's header facts plus its
// issue progress, read live from the instance. Recorded false means the
// session has no start record and only Info and Attention are filled.
type SessionProgress struct {
	Info                        SessionInfo
	Recorded                    bool
	Issue                       string
	StartCommit, InstanceCommit string
	// Planned excludes Issue itself: the target is what the progress is
	// towards, not one of its steps.
	Planned       []PlannedIssue
	ClosedPlanned int
	// Discovered is every issue created this session; Closed and Updated
	// count the session's changes to issues that existed before it.
	Discovered      []beads.Change
	Closed, Updated int
	Attention       []string
}

// Attention lines, exact: scripts reading --json match on them.
const (
	attentionBaseRewritten    = "base branch was rewritten since this session started"
	attentionHistoryRewritten = "beads history was rewritten on the instance"
	attentionClosedTarget     = "%s is closed but planned work under it is still open"
)

// DescribeProgress reports a session's progress live from the instance, and
// never mutates either side: no pull, push or commit.
//
// An instance that cannot be read is an error rather than last-known
// figures. Status is consulted to decide what to do next, and stale numbers
// presented as live are worse than none.
func DescribeProgress(ctx context.Context, ip, user, instanceName string, sess state.Session) (SessionProgress, error) {
	info := DescribeSessions(ctx, instanceName, []state.Session{sess})[0]
	var attention []string
	if requireBaseExists(ctx, sess.LocalRepo, sess.Base, sess.Name) != nil {
		attention = append(attention, attentionBaseRewritten)
	}
	if sess.Beads == nil {
		return SessionProgress{Info: info, Attention: attention}, nil
	}
	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		return SessionProgress{}, fmt.Errorf("connecting to %s: %w", instanceName, err)
	}
	defer func() { _ = client.Close() }()
	p, err := readProgress(client, RemoteRepoPath(user, sess.Name, sess.RepoNameOr(instanceName)), *sess.Beads)
	if err != nil {
		return SessionProgress{}, err
	}
	p.Info = info
	p.Attention = append(attention, p.Attention...)
	return p, nil
}

// readProgress is DescribeProgress's instance half, so it can run against a
// fake instance.
func readProgress(client beadsRunner, repo string, start state.SessionBeads) (SessionProgress, error) {
	p := SessionProgress{Recorded: true, Issue: start.Issue, StartCommit: start.StartCommit}
	now, err := beads.InstanceCommit(client, repo)
	if err != nil {
		return SessionProgress{}, err
	}
	p.InstanceCommit = now
	changes, err := beads.SessionChanges(client, repo, start.StartCommit, now)
	switch {
	case errors.Is(err, beads.ErrHistoryRewritten):
		// The diff is lost, the tree is not: planned progress is still
		// live and worth showing beside the warning.
		p.Attention = append(p.Attention, attentionHistoryRewritten)
	case err != nil:
		return SessionProgress{}, err
	}
	for _, c := range changes {
		switch c.Kind {
		case beads.Created:
			p.Discovered = append(p.Discovered, c)
		case beads.Closed:
			p.Closed++
		case beads.Updated:
			p.Updated++
		}
	}
	if start.Issue == "" {
		return p, nil
	}
	tree, err := beads.Descendants(client, repo, start.Issue)
	if err != nil {
		return SessionProgress{}, err
	}
	// A planned issue moved out from under the target is missing from the
	// tree but not gone; the diff still holds its current value, so it is
	// not reported removed.
	current := tree
	for _, c := range changes {
		if c.Kind != beads.Removed {
			current = append(current, c.Issue)
		}
	}
	var steps []string
	for _, id := range start.Planned {
		if id != start.Issue {
			steps = append(steps, id)
		}
	}
	p.Planned = plannedStatus(steps, current)
	p.ClosedPlanned = closedPlanned(p.Planned)
	if tree[0].Status == "closed" && p.ClosedPlanned < len(p.Planned)-removedCount(p.Planned) {
		p.Attention = append(p.Attention, fmt.Sprintf(attentionClosedTarget, start.Issue))
	}
	return p, nil
}

// plannedStatus looks each planned id up in now, first match winning. An id
// absent from now was deleted on the instance.
func plannedStatus(planned []string, now []beads.Issue) []PlannedIssue {
	byID := make(map[string]beads.Issue, len(now))
	for _, is := range now {
		if _, seen := byID[is.ID]; !seen {
			byID[is.ID] = is
		}
	}
	out := make([]PlannedIssue, 0, len(planned))
	for _, id := range planned {
		is, ok := byID[id]
		if !ok {
			out = append(out, PlannedIssue{ID: id, Status: string(beads.Removed), Removed: true})
			continue
		}
		out = append(out, PlannedIssue{ID: id, Title: is.Title, Status: is.Status})
	}
	return out
}

func closedPlanned(planned []PlannedIssue) int {
	n := 0
	for _, p := range planned {
		if !p.Removed && p.Status == "closed" {
			n++
		}
	}
	return n
}

func removedCount(planned []PlannedIssue) int {
	n := 0
	for _, p := range planned {
		if p.Removed {
			n++
		}
	}
	return n
}
