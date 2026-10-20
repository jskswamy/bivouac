package cmd

import (
	"fmt"
	"io"

	"github.com/jskswamy/bivouac/internal/lifecycle"
	"github.com/jskswamy/bivouac/internal/state"
	"github.com/spf13/cobra"
)

func runSessionStatus(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstanceForSession(cmd, name, args)
	if err != nil {
		return err
	}
	ctx := progressCtx(cmd)
	sess, err := resolveSessionArg(cmd, record, args)
	if err != nil {
		return err
	}
	progress, err := lifecycle.DescribeProgress(ctx, record.IP, record.User, record.Name, sess)
	if err != nil {
		return err
	}
	if jsonFlag(cmd) {
		return writeJSON(cmd, sessionProgressJSON(progress, sess))
	}
	printSessionProgress(cmd.OutOrStdout(), progress, issuesTracked(ctx, progress.Recorded, sess))
	return nil
}

// plannedMark is one planned issue's state at a glance. Anything not
// closed, in progress or removed reads as not started, so a status bd adds
// later still prints.
func plannedMark(p lifecycle.PlannedIssue) string {
	switch {
	case p.Removed:
		return "✗"
	case p.Status == "closed":
		return "✓"
	case p.Status == "in_progress":
		return "◐"
	}
	return "○"
}

func worktreeLabel(i lifecycle.SessionInfo) string {
	switch {
	case !i.WorktreeExists:
		return "worktree missing"
	case i.WorktreeDirty:
		return "worktree dirty"
	}
	return "worktree clean"
}

// printSessionProgress prints the spec's one-screen layout: the header
// `session list` would show, the issue progress, then anything that needs
// the user. tracked picks the true "nothing to show" line for an
// unrecorded session, as in session issues.
func printSessionProgress(w io.Writer, p lifecycle.SessionProgress, tracked bool) {
	_, _ = fmt.Fprintf(w, "%s  %s · %s · %s\n", p.Info.Name, retirementLabel(p.Info), unmergedLabel(p.Info), worktreeLabel(p.Info))
	switch {
	case !p.Recorded:
		_, _ = fmt.Fprintln(w, unrecordedLine(tracked))
	default:
		_, _ = fmt.Fprintln(w, "issues (live from instance):")
		printIssueProgress(w, p)
	}
	for _, a := range p.Attention {
		_, _ = fmt.Fprintf(w, "needs attention: %s\n", a)
	}
}

// printIssueProgress prints "x of y" against the target when there is one.
// Without one there is nothing to be x of, so only the counts are honest.
func printIssueProgress(w io.Writer, p lifecycle.SessionProgress) {
	if p.Issue == "" {
		_, _ = fmt.Fprintf(w, "  created: %d · closed: %d · updated: %d\n", len(p.Discovered), p.Closed, p.Updated)
		return
	}
	_, _ = fmt.Fprintf(w, "  epic %s  %d of %d planned closed\n", p.Issue, p.ClosedPlanned, len(p.Planned))
	for _, is := range p.Planned {
		title := is.Title
		if is.Removed {
			title = is.Status
		}
		_, _ = fmt.Fprintf(w, "    %s %s  %s\n", plannedMark(is), is.ID, title)
	}
	closed := 0
	for _, d := range p.Discovered {
		if d.Issue.Status == "closed" {
			closed++
		}
	}
	_, _ = fmt.Fprintf(w, "  discovered this session: %d (%d closed) · updated: %d\n", len(p.Discovered), closed, p.Updated)
}

// sessionStatusJSON is session status' --json shape.
type sessionStatusJSON struct {
	sessionReportJSON
	Planned       []plannedIssueJSON `json:"planned"`
	ClosedPlanned int                `json:"closed_planned"`
	Discovered    []plannedIssueJSON `json:"discovered"`
	Closed        int                `json:"closed"`
	Updated       int                `json:"updated"`
	Attention     []string           `json:"attention"`
}

type plannedIssueJSON struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

func sessionProgressJSON(p lifecycle.SessionProgress, sess state.Session) sessionStatusJSON {
	out := sessionStatusJSON{
		sessionReportJSON: sessionReport(sess, p.Issue, p.StartCommit, p.InstanceCommit, p.Recorded),
		ClosedPlanned:     p.ClosedPlanned, Closed: p.Closed, Updated: p.Updated,
		Planned:    make([]plannedIssueJSON, 0, len(p.Planned)),
		Discovered: make([]plannedIssueJSON, 0, len(p.Discovered)),
		Attention:  append([]string{}, p.Attention...),
	}
	for _, is := range p.Planned {
		out.Planned = append(out.Planned, plannedIssueJSON{ID: is.ID, Title: is.Title, Status: is.Status})
	}
	for _, d := range p.Discovered {
		out.Discovered = append(out.Discovered, plannedIssueJSON{ID: d.Issue.ID, Title: d.Issue.Title, Status: d.Issue.Status})
	}
	return out
}
