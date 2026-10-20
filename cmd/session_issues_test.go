package cmd

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/beads"
	"github.com/jskswamy/bivouac/internal/lifecycle"
	"github.com/jskswamy/bivouac/internal/state"
)

func TestSessionIssues_MetadataAndManifest(t *testing.T) {
	root := newRootCmd()
	c, _, err := root.Find([]string{"session", "issues"})
	if err != nil || c.Name() != "issues" {
		t.Fatalf("session issues not registered: %v", err)
	}
	meta := metadataFor(c)
	if !meta.SupportsJSON || !meta.Network || !meta.RequiresInstance || meta.Destructive {
		t.Errorf("session issues metadata = %+v, want SupportsJSON, Network, RequiresInstance, not Destructive", meta)
	}
	if c.Flags().Lookup("json") == nil {
		t.Error("session issues missing --json")
	}
	tool := findTool(t, manifestFromCommand(t, []string{"manifest"}), "bivouac_session_issues")
	if !tool.Metadata.JsonOutput {
		t.Error("session issues must advertise jsonOutput")
	}
}

func change(id, title string, kind beads.ChangeKind, sync beads.SyncState) beads.IssueSync {
	return beads.IssueSync{Change: beads.Change{Issue: beads.Issue{ID: id, Title: title, Status: "open"}, Kind: kind}, Sync: sync}
}

var listing = lifecycle.IssueListing{
	Session: "pi", Issue: "e", IssueStatus: "in_progress", StartCommit: "c0", InstanceCommit: "c1", Recorded: true,
	Changes: []beads.IssueSync{
		change("e.1", "Planned work", beads.Closed, beads.Synced),
		change("x", "Stray find", beads.Created, beads.NotSynced),
		change("y", "Who knows", beads.Updated, beads.Unknown),
	},
	OutsideEpic: []string{"x"},
}

func TestPrintIssueListing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		listing lifecycle.IssueListing
		tracked bool
		want    string
	}{
		{"recorded", listing, true,
			"✓ e.1  Planned work  closed\n" +
				"✗ x  Stray find  created\n" +
				"? y  Who knows  updated\n" +
				"started for e: in_progress\n" +
				"outside e: x\n"},
		{"no changes", lifecycle.IssueListing{Session: "pi", Recorded: true}, true,
			"no issue changes this session\n"},
		{"history rewritten", lifecycle.IssueListing{Session: "pi", Recorded: true, HistoryRewritten: true}, true,
			beads.ErrHistoryRewritten.Error() + "\n"},
		{"predates", lifecycle.IssueListing{Session: "pi"}, true,
			"this session predates issue reporting\n"},
		{"not wired", lifecycle.IssueListing{Session: "pi"}, false,
			"no issue tracking in this session\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			printIssueListing(&buf, tc.listing, tc.tracked)
			if buf.String() != tc.want {
				t.Fatalf("got\n%s\nwant\n%s", buf.String(), tc.want)
			}
		})
	}
}

func TestIssueListingJSON(t *testing.T) {
	sess := state.Session{
		Name:       "pi",
		Beads:      &state.SessionBeads{StartCommit: "c0", Issue: "e", Planned: []string{"e", "e.1"}},
		Retirement: &state.Retirement{Phase: state.RetirementPaused, PausedOn: state.PausedOnIssues},
	}
	raw, err := json.Marshal(issueListingJSON(listing, sess))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"session": "pi", "start_commit": "c0", "instance_commit": "c1", "recorded": true,
		"history_rewritten": false, "retirement_phase": "paused", "paused_on": "issues",
	} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
	issues := got["issues"].([]any)
	first := issues[0].(map[string]any)
	want := map[string]any{"id": "e.1", "title": "Planned work", "status": "open",
		"classification": "closed", "planned": true, "sync": "synced"}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("issue = %v, want %v", first, want)
	}
	if issues[1].(map[string]any)["planned"] != false || issues[1].(map[string]any)["sync"] != "not-synced" {
		t.Fatalf("issue = %v, want unplanned and not-synced", issues[1])
	}
}

// An empty listing is still a list, so a script can range over it without
// a null check.
func TestIssueListingJSONEmptyIsArray(t *testing.T) {
	raw, err := json.Marshal(issueListingJSON(lifecycle.IssueListing{Session: "pi"}, state.Session{Name: "pi"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"issues":[]`) {
		t.Fatalf("got %s, want an empty issues array", raw)
	}
}
