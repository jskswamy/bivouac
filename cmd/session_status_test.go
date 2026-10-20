package cmd

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jskswamy/bivouac/internal/beads"
	"github.com/jskswamy/bivouac/internal/lifecycle"
	"github.com/jskswamy/bivouac/internal/state"
)

func TestSessionStatus_MetadataAndManifest(t *testing.T) {
	root := newRootCmd()
	c, _, err := root.Find([]string{"session", "status"})
	if err != nil || c.Name() != "status" {
		t.Fatalf("session status not registered: %v", err)
	}
	meta := metadataFor(c)
	if !meta.SupportsJSON || !meta.Network || !meta.RequiresInstance || meta.Destructive {
		t.Errorf("session status metadata = %+v, want SupportsJSON, Network, RequiresInstance, not Destructive", meta)
	}
	if c.Flags().Lookup("json") == nil {
		t.Error("session status missing --json")
	}
	tool := findTool(t, manifestFromCommand(t, []string{"manifest"}), "bivouac_session_status")
	if !tool.Metadata.JsonOutput {
		t.Error("session status must advertise jsonOutput")
	}
}

var statusInfo = lifecycle.SessionInfo{Name: "pi", RemoteKnown: true, UnmergedKnown: true, Unmerged: 7, WorktreeExists: true}

var progress = lifecycle.SessionProgress{
	Info: statusInfo, Recorded: true, Issue: "e", StartCommit: "c0", InstanceCommit: "c1",
	Planned: []lifecycle.PlannedIssue{
		{ID: "e.1", Title: "Discover keys", Status: "closed"},
		{ID: "e.2", Title: "Use key", Status: "in_progress"},
		{ID: "e.3", Title: "Later", Status: "open"},
		{ID: "e.4", Status: "removed", Removed: true},
	},
	ClosedPlanned: 1,
	Discovered: []beads.Change{
		{Issue: beads.Issue{ID: "d1", Title: "Found", Status: "open"}, Kind: beads.Created},
		{Issue: beads.Issue{ID: "d2", Title: "Fixed", Status: "closed"}, Kind: beads.Created},
	},
	Updated:   4,
	Attention: []string{"base branch was rewritten since this session started"},
}

func TestPrintSessionProgress(t *testing.T) {
	for _, tc := range []struct {
		name     string
		progress lifecycle.SessionProgress
		tracked  bool
		want     string
	}{
		{"epic", progress, true,
			"pi  active · 7 unmerged · worktree clean\n" +
				"issues (live from instance):\n" +
				"  epic e  1 of 4 planned closed\n" +
				"    ✓ e.1  Discover keys\n" +
				"    ◐ e.2  Use key\n" +
				"    ○ e.3  Later\n" +
				"    ✗ e.4  removed\n" +
				"  discovered this session: 2 (1 closed) · updated: 4\n" +
				"needs attention: base branch was rewritten since this session started\n"},
		{"no epic", lifecycle.SessionProgress{Info: statusInfo, Recorded: true, Closed: 3, Updated: 1,
			Discovered: progress.Discovered}, true,
			"pi  active · 7 unmerged · worktree clean\n" +
				"issues (live from instance):\n" +
				"  created: 2 · closed: 3 · updated: 1\n"},
		{"predates", lifecycle.SessionProgress{Info: lifecycle.SessionInfo{Name: "pi", RemoteKnown: true, UnmergedKnown: true, WorktreeExists: true, WorktreeDirty: true}}, true,
			"pi  active · 0 unmerged · worktree dirty\n" +
				"this session predates issue reporting\n"},
		{"no tracking", lifecycle.SessionProgress{Info: statusInfo}, false,
			"pi  active · 7 unmerged · worktree clean\n" +
				"no issue tracking in this session\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			printSessionProgress(&buf, tc.progress, tc.tracked)
			if buf.String() != tc.want {
				t.Fatalf("got\n%s\nwant\n%s", buf.String(), tc.want)
			}
		})
	}
}

func TestSessionProgressJSON(t *testing.T) {
	sess := state.Session{
		Name:       "pi",
		Retirement: &state.Retirement{Phase: state.RetirementPaused, PausedOn: state.PausedOnIssues},
	}
	raw, err := json.Marshal(sessionProgressJSON(progress, sess))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"session": "pi", "issue": "e", "start_commit": "c0", "instance_commit": "c1", "recorded": true,
		"retirement_phase": "paused", "paused_on": "issues", "closed_planned": float64(1),
		"updated": float64(4),
	} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
	planned := got["planned"].([]any)
	if !reflect.DeepEqual(planned[3], map[string]any{"id": "e.4", "title": "", "status": "removed"}) {
		t.Errorf("planned[3] = %v", planned[3])
	}
	if !reflect.DeepEqual(got["attention"], []any{"base branch was rewritten since this session started"}) {
		t.Errorf("attention = %v", got["attention"])
	}
	if d := got["discovered"].([]any); len(d) != 2 {
		t.Errorf("discovered = %v", d)
	}
}
