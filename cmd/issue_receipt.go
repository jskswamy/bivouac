package cmd

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jskswamy/bivouac/internal/beads"
	"github.com/jskswamy/bivouac/internal/state"
)

// printIssueReceipt prints what a pull or merge brought home from the
// session's issue database: one summary line, then one line per issue not
// known to have landed. Nil prints nothing, which is how a session without
// a start record keeps today's output.
//
// Stragglers are named rather than counted because the user's next step is
// to look at each one: a pull that missed it, or an issue deleted locally
// that will never match and is a case for --accept-unsynced.
func printIssueReceipt(w io.Writer, session string, r *state.IssueReceipt) {
	if r == nil {
		return
	}
	if r.Accepted && len(r.Unsynced) == 0 {
		// Only a rewritten history is accepted with nothing listed: a clean
		// result is recorded as verified, never as accepted.
		_, _ = fmt.Fprintf(w, "issues from %s: changes could not be listed — accepted unverified\n", session)
		return
	}
	counts := fmt.Sprintf("%d created · %d closed · %d updated", r.Created, r.Closed, r.Updated)
	if r.Removed > 0 {
		counts += fmt.Sprintf(" · %d removed", r.Removed)
	}
	if len(r.Unsynced) == 0 {
		total := r.Created + r.Closed + r.Updated + r.Removed
		_, _ = fmt.Fprintf(w, "issues from %s: %s — all %d synced ✓\n", session, counts, total)
		return
	}
	_, _ = fmt.Fprintf(w, "issues from %s: %s — %d NOT synced\n", session, counts, len(r.Unsynced))
	accepted := ""
	if r.Accepted {
		accepted = " (accepted)"
	}
	for _, u := range r.Unsynced {
		_, _ = fmt.Fprintf(w, "  ✗ %s  %s  (instance %s, local %s)%s\n",
			u.ID, u.Title, clock(u.InstanceUpdated, "?"), localClock(u), accepted)
	}
}

// localClock says what the local side holds: a time, nothing at all, or --
// when the local database could not be read -- that nobody knows.
func localClock(u state.UnsyncedIssue) string {
	if u.Sync == string(beads.Unknown) {
		return "unknown"
	}
	return clock(u.LocalUpdated, "missing")
}

// clock renders an RFC 3339 time as the user's wall-clock hh:mm, or empty
// when there is no time to show.
func clock(s, empty string) string {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return empty
	}
	return t.Local().Format("15:04")
}
