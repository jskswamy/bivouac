package beads

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// scriptedRunner answers instance commands by substring match, unlike
// fakeRunner which answers "ok" to everything. The readers here parse bd's
// JSON, so each test must say exactly what bd would have printed. A command
// nothing matches fails the test: a silent empty answer would let a wrong
// command line pass.
type scriptedRunner struct {
	t       *testing.T
	outputs map[string]string
	errs    map[string]error
}

func (s *scriptedRunner) Run(cmd string) (string, error) {
	s.t.Helper()
	// The command is quoted twice (per argument, then as the login shell's
	// -c string), so strip quoting and match on the plain words.
	cmd = strings.NewReplacer("'", "", `\`, "").Replace(cmd)
	for key, err := range s.errs {
		if strings.Contains(cmd, key) {
			return s.outputs[key], err
		}
	}
	// Longest key wins so "children 'e.1'" is not shadowed by "children 'e'".
	best := ""
	for key := range s.outputs {
		if strings.Contains(cmd, key) && len(key) > len(best) {
			best = key
		}
	}
	if best == "" {
		s.t.Fatalf("unscripted command: %s", cmd)
	}
	return s.outputs[best], nil
}

// Captured from bd 1.3.0; bd 1.3.1 emits the same zero diff timestamps.
func TestSessionChangesRestoresPinnedTimestamps(t *testing.T) {
	diff, err := os.ReadFile("testdata/diff-zero-timestamps.json")
	if err != nil {
		t.Fatal(err)
	}
	r := &scriptedRunner{t: t, outputs: map[string]string{
		"diff start pinned ":   string(diff),
		"show --as-of pinned ": `[{"id":"reporttest-351","title":"Reporting validation epic","status":"in_progress","updated_at":"2026-10-20T06:30:36Z"}]`,
	}}
	changes, err := SessionChanges(r, "/repo", "start", "pinned")
	if err != nil {
		t.Fatal(err)
	}
	local := map[string]Issue{"reporttest-351": issueAt(t, "reporttest-351", "2026-10-20T06:30:05Z")}
	got := CheckSynced(changes, local, nil)
	if len(got) != 1 || got[0].Sync != NotSynced {
		t.Fatalf("remote claim has not landed: got %+v", got)
	}
	if got[0].Issue.UpdatedAt.IsZero() {
		t.Fatal("diff's zero timestamp was not replaced with the pinned issue value")
	}
	local["reporttest-351"] = got[0].Issue
	if got := CheckSynced(changes, local, nil); got[0].Sync != Synced {
		t.Fatalf("pinned issue has landed: got %+v", got)
	}
}

func TestSessionChangesCannotReadPinnedIssues(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		err          error
	}{
		{"command failed", "", errors.New("connection lost")},
		{"invalid JSON", "warning only", nil},
		{"missing issue", "[]", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &scriptedRunner{t: t, outputs: map[string]string{
				"diff":                 `[{"DiffType":"modified","NewValue":{"id":"a","status":"open"}}]`,
				"show --as-of pinned ": tc.output,
			}, errs: map[string]error{}}
			if tc.err != nil {
				r.errs["show --as-of pinned "] = tc.err
			}
			if _, err := SessionChanges(r, "/repo", "start", "pinned"); err == nil {
				t.Fatal("an unreadable pinned issue must not produce sync evidence")
			}
		})
	}
}

func TestCheckSyncedUnknownWithoutTimestamps(t *testing.T) {
	valid := issueAt(t, "a", "2026-10-20T06:30:36Z")
	for _, tc := range []struct {
		name          string
		remote, local Issue
	}{
		{"remote", Issue{ID: "a"}, valid},
		{"local", valid, Issue{ID: "a"}},
		{"both", Issue{ID: "a"}, Issue{ID: "a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckSynced([]Change{{Issue: tc.remote, Kind: Updated}}, map[string]Issue{"a": tc.local}, nil)
			if got[0].Sync != Unknown {
				t.Fatalf("missing timestamp is not sync evidence: got %s", got[0].Sync)
			}
		})
	}
}

func TestInstanceCommitReadsVcStatus(t *testing.T) {
	r := &scriptedRunner{t: t, outputs: map[string]string{
		"vc status": `{"branch":"main","commit":"abc","schema_version":1}`,
	}}
	got, err := InstanceCommit(r, "/repo")
	if err != nil || got != "abc" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestDecodeJSONSkipsLeadingNoise(t *testing.T) {
	var issues []Issue
	if err := decodeJSON("warning: x\n[{\"id\":\"a\"}]", &issues); err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].ID != "a" {
		t.Fatalf("got %+v", issues)
	}
}

func TestSessionChangesClassifies(t *testing.T) {
	diff := `[
	 {"IssueID":"n","DiffType":"added","OldValue":null,"NewValue":{"id":"n","title":"new","status":"open"}},
	 {"IssueID":"r","DiffType":"removed","OldValue":{"id":"r","title":"gone","status":"open"},"NewValue":null},
	 {"IssueID":"c","DiffType":"modified","OldValue":{"id":"c","title":"t","status":"open"},"NewValue":{"id":"c","title":"t","status":"closed"}},
	 {"IssueID":"u","DiffType":"modified","OldValue":{"id":"u","title":"old","status":"open"},"NewValue":{"id":"u","title":"new","status":"open"}}
	]`
	r := &scriptedRunner{t: t, outputs: map[string]string{
		"diff": diff,
		"show --as-of b ": `[
		 {"id":"u","title":"new","status":"open","updated_at":"2026-10-20T06:30:36Z"},
		 {"id":"c","title":"t","status":"closed","updated_at":"2026-10-20T06:30:36Z"},
		 {"id":"n","title":"new","status":"open","updated_at":"2026-10-20T06:30:36Z"}
		]`,
	}}
	got, err := SessionChanges(r, "/repo", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		id   string
		kind ChangeKind
	}{{"n", Created}, {"r", Removed}, {"c", Closed}, {"u", Updated}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, w := range want {
		if got[i].Issue.ID != w.id || got[i].Kind != w.kind {
			t.Errorf("change %d: got %s/%s, want %s/%s", i, got[i].Issue.ID, got[i].Kind, w.id, w.kind)
		}
	}
}

func TestSessionChangesNoChangesText(t *testing.T) {
	r := &scriptedRunner{t: t, outputs: map[string]string{"diff": "No changes between a and b"}}
	got, err := SessionChanges(r, "/repo", "a", "b")
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestSessionChangesMissingStartIsHistoryRewritten(t *testing.T) {
	out := `{"error":"failed to get diff: failed to get diff: Error 1105: target commit not found"}`
	r := &scriptedRunner{t: t,
		outputs: map[string]string{"diff": out},
		errs:    map[string]error{"diff": errors.New("exit status 1")}}
	_, err := SessionChanges(r, "/repo", "a", "b")
	if !errors.Is(err, ErrHistoryRewritten) {
		t.Fatalf("got %v", err)
	}
}

func TestDescendantsWalksChildren(t *testing.T) {
	r := &scriptedRunner{t: t, outputs: map[string]string{
		"show":            `[{"id":"e"}]`,
		"children e ":     `[{"id":"e.1"},{"id":"e.2"}]`,
		"children e.1 ":   `[{"id":"e.1.1"}]`,
		"children e.2 ":   `[]`,
		"children e.1.1 ": `[]`,
	}}
	got, err := Descendants(r, "/repo", "e")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, i := range got {
		ids = append(ids, i.ID)
	}
	if strings.Join(ids, ",") != "e,e.1,e.2,e.1.1" {
		t.Fatalf("got %v", ids)
	}
}

// issueAt builds an Issue through json.Unmarshal so the timestamp goes down
// the same parsing path bd's output does.
func issueAt(t *testing.T, id, updated string) Issue {
	t.Helper()
	var is Issue
	raw := fmt.Sprintf(`{"id":%q,"title":"t","status":"open","updated_at":%q}`, id, updated)
	if err := json.Unmarshal([]byte(raw), &is); err != nil {
		t.Fatal(err)
	}
	return is
}

func TestCheckSynced(t *testing.T) {
	inst := issueAt(t, "a", "2026-10-19T14:00:00Z")
	local := map[string]Issue{
		"newer": issueAt(t, "newer", "2026-10-19T15:00:00Z"),
		"equal": issueAt(t, "equal", "2026-10-19T14:00:00Z"),
		"older": issueAt(t, "older", "2026-10-19T13:00:00Z"),
		"kept":  issueAt(t, "kept", "2026-10-19T13:00:00Z"),
	}
	with := func(id string) Change {
		c := inst
		c.ID = id
		return Change{Issue: c, Kind: Updated}
	}
	changes := []Change{
		with("newer"), with("equal"), with("older"), with("missing"),
		{Issue: Issue{ID: "gone"}, Kind: Removed},
		{Issue: Issue{ID: "kept"}, Kind: Removed},
	}
	want := []SyncState{Synced, Synced, NotSynced, NotSynced, Synced, NotSynced}
	got := CheckSynced(changes, local, nil)
	for i, w := range want {
		if got[i].Sync != w {
			t.Errorf("%s: got %s, want %s", changes[i].Issue.ID, got[i].Sync, w)
		}
	}
	if got[0].Local == nil || got[0].Local.ID != "newer" {
		t.Errorf("Local not populated: %+v", got[0].Local)
	}
	if got[3].Local != nil {
		t.Errorf("missing issue has Local %+v", got[3].Local)
	}
}

func TestCheckSyncedUnknownOnLocalError(t *testing.T) {
	got := CheckSynced([]Change{{Issue: Issue{ID: "a"}, Kind: Created}}, nil, errors.New("boom"))
	if len(got) != 1 || got[0].Sync != Unknown {
		t.Fatalf("got %+v", got)
	}
}

func TestCheckSyncedComparesInstants(t *testing.T) {
	inst := issueAt(t, "a", "2026-10-19T14:02:00Z")
	// Same instant plus half a second, written in another zone.
	local := map[string]Issue{"a": issueAt(t, "a", "2026-10-19T19:32:00.5+05:30")}
	got := CheckSynced([]Change{{Issue: inst, Kind: Updated}}, local, nil)
	if got[0].Sync != Synced {
		t.Fatalf("got %s", got[0].Sync)
	}
}
