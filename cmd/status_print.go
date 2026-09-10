package cmd

import (
	"fmt"
	"io"
	"strconv"

	"github.com/jskswamy/cloudlab/internal/lifecycle"
	"github.com/jskswamy/cloudlab/internal/state"
	"github.com/spf13/cobra"
)

// Column widths for the status field block. Labels are the longest of
// them plus a gap; the left column is wide enough for a size slug, which
// is the longest short value any of the paired fields carries.
const (
	statusLabelWidth = 10
	statusValueWidth = 17
	// Pads the common cases ("0 unmerged", "3 unmerged") into a column.
	// The degraded labels are longer than this on purpose and simply run
	// past it: an instance nobody could reach is worth a ragged edge.
	statusUnmergedWidth = 13
)

// printStatus renders the instance report: a heading naming the instance
// and its live state, then the fields, then the cost.
//
// Split out of runStatus so it can be tested without a provider -- the
// same reason printSessions and printServing below are separate.
func printStatus(cmd *cobra.Command, st lifecycle.InstanceStatus) {
	out := cmd.OutOrStdout()
	s := newStyles(out)
	r := st.Record

	live := st.LiveStatus
	if st.LiveErr != nil {
		live = "unknown"
	}
	_, _ = fmt.Fprintf(out, "\n%s  %s\n\n", s.value.Render(r.Name), s.statusDot(live))

	// Short fields go two to a line so the block stays compact enough to
	// take in at a glance; anything that can be long gets its own line
	// rather than being truncated or wrapping into the next column.
	printPair(out, s, "Provider", r.Provider, "Region", r.Region)
	printPair(out, s, "Size", r.Size, "Template", r.Template)
	printPair(out, s, "User", r.User, "IP", r.IP)

	if st.LiveErr != nil {
		printField(out, s, "Cost", fmt.Sprintf("unknown (%v)", st.LiveErr))
	} else {
		printField(out, s, "Cost", costSummary(st.Cost))
	}

	repoPath := r.RepoPath
	if repoPath == "" {
		repoPath = "unknown (provisioned before this field existed)"
	}
	printField(out, s, "RepoPath", repoPath)
}

// printField writes one full-width label/value line.
func printField(out io.Writer, s styles, label, value string) {
	_, _ = fmt.Fprintf(out, "  %s%s\n", s.label.Width(statusLabelWidth).Render(label), s.value.Render(value))
}

// printPair writes two label/value cells on one line. An empty second
// label leaves the line half-used rather than printing a stray label.
func printPair(out io.Writer, s styles, l1, v1, l2, v2 string) {
	left := s.label.Width(statusLabelWidth).Render(l1) + s.value.Width(statusValueWidth).Render(v1)
	_, _ = fmt.Fprintf(out, "  %s%s%s\n", left, s.label.Width(statusLabelWidth).Render(l2), s.value.Render(v2))
}

// printServing renders the instance's published ports.
//
// An error is reported as unknown rather than returned. status is a
// read-only report and an unreachable instance is an expected state for
// it -- the live provider check above already renders that way, and a
// serve lookup must not be the thing that makes status fail.
func printServing(cmd *cobra.Command, entries []lifecycle.ServeEntry, tailnetIP string, err error) {
	out := cmd.OutOrStdout()
	s := newStyles(out)

	if err != nil {
		_, _ = fmt.Fprintf(out, "\n  %s  %s\n", s.header.Render("SERVING"), s.dim.Render("unknown (instance unreachable)"))
		return
	}
	if len(entries) == 0 {
		_, _ = fmt.Fprintf(out, "\n  %s  %s\n", s.header.Render("SERVING"), s.dim.Render("none"))
		return
	}
	_, _ = fmt.Fprintf(out, "\n  %s\n", s.header.Render("SERVING"))
	for _, e := range entries {
		// The ports are known even when the tailnet lookup that would
		// address them is not -- worth showing rather than discarding,
		// but never as a bare ":8888" with no host.
		addr := s.dim.Render("(tailnet address unknown)")
		if tailnetIP != "" {
			addr = s.value.Render(tailnetIP + ":" + strconv.Itoa(e.Port))
		}
		_, _ = fmt.Fprintf(out, "  %s%s\n", s.value.Width(statusLabelWidth).Render(strconv.Itoa(e.Port)), addr)
	}
}

// printSessions renders an instance's sessions. Separate from runStatus so it
// can be tested without a provider: runStatus reaches lifecycle.Status, which
// makes a live API call, and the session section has nothing to do with that.
func printSessions(cmd *cobra.Command, record state.Record) {
	out := cmd.OutOrStdout()
	s := newStyles(out)

	if len(record.Sessions) == 0 {
		_, _ = fmt.Fprintf(out, "\n  %s %s\n", s.header.Render("SESSIONS"), s.dim.Render("none"))
		return
	}
	_, _ = fmt.Fprintf(out, "\n  %s\n", s.header.Render("SESSIONS"))
	// DescribeSessions, not a DescribeSession loop: each one probes its
	// instance over the network, so in sequence the report costs the sum of
	// every session's timeout.
	for _, info := range lifecycle.DescribeSessions(cmd.Context(), record.Name, record.Sessions) {
		wtState := "clean"
		if !info.WorktreeExists {
			wtState = "no worktree"
		} else if info.WorktreeDirty {
			wtState = "dirty"
		}
		_, _ = fmt.Fprintf(out, "  %s%s%s%s\n",
			s.value.Width(statusLabelWidth).Render(info.Name),
			s.dim.Width(statusValueWidth).Render(info.Branch),
			s.label.Width(statusUnmergedWidth).Render(unmergedLabel(info)),
			s.dim.Render(wtState))
	}
}

// unmergedLabel renders what a session's work amounts to, in the one column
// a user scans to decide whether to pull.
//
// Three things can be true and the label has to keep them apart, because
// collapsing any two of them is the bug this replaced: an exact count, a
// known-ahead session whose commits have not been fetched so cannot be
// counted, and an instance that never answered.
//
// The unreachable case keeps its number when it has one -- the local branch
// is still the freshest thing this machine holds -- but says where it came
// from. A bare "0" from an instance nobody could reach is precisely how a
// waiting commit went unnoticed.
func unmergedLabel(i lifecycle.SessionInfo) string {
	count := "?"
	if i.UnmergedKnown {
		count = fmt.Sprintf("%d", i.Unmerged)
	}
	if !i.RemoteKnown {
		return count + " unmerged (instance unreachable)"
	}
	if !i.UnmergedKnown {
		if i.RemoteAhead {
			return "ahead (pull to count)"
		}
		return "? unmerged"
	}
	return count + " unmerged"
}
