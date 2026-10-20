package cmd

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/jskswamy/bivouac/internal/beads"
	"github.com/jskswamy/bivouac/internal/lifecycle"
	"github.com/jskswamy/bivouac/internal/state"
	"github.com/spf13/cobra"
)

func runSessionIssues(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstanceForSession(cmd, name, args)
	if err != nil {
		return err
	}
	ctx := progressCtx(cmd)
	sess, err := resolveSessionArg(cmd, record, args)
	if err != nil {
		return err
	}
	listing, err := lifecycle.SessionIssues(ctx, record.IP, record.User, record.Name, sess)
	if err != nil {
		return err
	}
	if jsonFlag(cmd) {
		return writeJSON(cmd, issueListingJSON(listing, sess))
	}
	printIssueListing(cmd.OutOrStdout(), listing, issuesTracked(ctx, listing.Recorded, sess))
	return nil
}

// issuesTracked is only worth asking when there is no start record: it is
// what tells a session that predates reporting from one beads never
// touched. A missing local bd cannot answer, and saying "no tracking" then
// would be a claim nobody checked.
func issuesTracked(ctx context.Context, recorded bool, sess state.Session) bool {
	return recorded || (beads.Present(sess.LocalRepo) &&
		(!beads.Available() || beads.Wired(ctx, sess.LocalRepo, sess.Name)))
}

// syncMark is the one-character sync verdict. Unknown gets its own mark
// rather than borrowing ✗: nothing is known to be missing, only unchecked.
var syncMark = map[beads.SyncState]string{
	beads.Synced:    "✓",
	beads.NotSynced: "✗",
	beads.Unknown:   "?",
}

// printIssueListing prints one line per change, then the target's status
// and any discovered work filed outside it. tracked only matters for an
// unrecorded listing, to choose which of the two "nothing to show" lines
// is true.
func printIssueListing(w io.Writer, l lifecycle.IssueListing, tracked bool) {
	switch {
	case !l.Recorded:
		_, _ = fmt.Fprintln(w, unrecordedLine(tracked))
		return
	case l.HistoryRewritten:
		_, _ = fmt.Fprintln(w, beads.ErrHistoryRewritten.Error())
		return
	}
	if len(l.Changes) == 0 {
		_, _ = fmt.Fprintln(w, "no issue changes this session")
	}
	for _, c := range l.Changes {
		_, _ = fmt.Fprintf(w, "%s %s  %s  %s\n", syncMark[c.Sync], c.Issue.ID, c.Issue.Title, c.Kind)
	}
	if l.Issue == "" {
		return
	}
	_, _ = fmt.Fprintf(w, "started for %s: %s\n", l.Issue, l.IssueStatus)
	if len(l.OutsideEpic) > 0 {
		_, _ = fmt.Fprintf(w, "outside %s: %s\n", l.Issue, strings.Join(l.OutsideEpic, ", "))
	}
}

// unrecordedLine is what both session commands print for a session with
// no start record: tracked tells one that predates reporting from one
// beads never touched.
func unrecordedLine(tracked bool) string {
	if tracked {
		return "this session predates issue reporting"
	}
	return "no issue tracking in this session"
}

// sessionReportJSON is the top level session issues and session status
// share, so a script reads both the same way. The retirement fields come
// from the session record, not the instance.
type sessionReportJSON struct {
	Session         string `json:"session"`
	Issue           string `json:"issue,omitempty"`
	StartCommit     string `json:"start_commit,omitempty"`
	InstanceCommit  string `json:"instance_commit,omitempty"`
	Recorded        bool   `json:"recorded"`
	RetirementPhase string `json:"retirement_phase,omitempty"`
	PausedOn        string `json:"paused_on,omitempty"`
}

func sessionReport(sess state.Session, issue, start, instance string, recorded bool) sessionReportJSON {
	out := sessionReportJSON{Session: sess.Name, Issue: issue, StartCommit: start, InstanceCommit: instance, Recorded: recorded}
	if r := sess.Retirement; r != nil {
		out.RetirementPhase, out.PausedOn = string(r.Phase), r.PausedOn
	}
	return out
}

// sessionIssuesJSON is session issues' --json shape. Its own type rather
// than IssueListing's: the field names are a contract with scripts.
type sessionIssuesJSON struct {
	sessionReportJSON
	IssueStatus      string             `json:"issue_status,omitempty"`
	HistoryRewritten bool               `json:"history_rewritten"`
	Issues           []sessionIssueJSON `json:"issues"`
	OutsideEpic      []string           `json:"outside_epic,omitempty"`
}

type sessionIssueJSON struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	Classification string `json:"classification"`
	Planned        bool   `json:"planned"`
	Sync           string `json:"sync"`
}

func issueListingJSON(l lifecycle.IssueListing, sess state.Session) sessionIssuesJSON {
	out := sessionIssuesJSON{
		sessionReportJSON: sessionReport(sess, l.Issue, l.StartCommit, l.InstanceCommit, l.Recorded),
		IssueStatus:       l.IssueStatus, HistoryRewritten: l.HistoryRewritten,
		Issues: make([]sessionIssueJSON, 0, len(l.Changes)), OutsideEpic: l.OutsideEpic,
	}
	var planned []string
	if sess.Beads != nil {
		planned = sess.Beads.Planned
	}
	for _, c := range l.Changes {
		out.Issues = append(out.Issues, sessionIssueJSON{
			ID: c.Issue.ID, Title: c.Issue.Title, Status: c.Issue.Status,
			Classification: string(c.Kind), Planned: slices.Contains(planned, c.Issue.ID),
			Sync: string(c.Sync),
		})
	}
	return out
}
