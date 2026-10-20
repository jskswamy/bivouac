package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Issue is the subset of a bd issue object that reporting needs.
type Issue struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ChangeKind says what a session did to an issue.
type ChangeKind string

const (
	Created ChangeKind = "created"
	Closed  ChangeKind = "closed"
	Updated ChangeKind = "updated"
	Removed ChangeKind = "removed"
)

// Change is one issue's net effect over a session. Issue is the new value,
// or the old one for a removed issue, which has no new value.
type Change struct {
	Issue Issue
	Kind  ChangeKind
}

// ErrHistoryRewritten means the start commit no longer resolves on the
// instance, which happens when the agent ran bd compact, flatten or gc.
// There is no fallback: callers must treat the session's changes as unknown
// rather than guess.
var ErrHistoryRewritten = errors.New("beads history was rewritten on the instance; can't list this session's changes")

// decodeJSON decodes from the first [ or { in out. bd can print warnings
// ahead of its JSON, and the login shell's profile can too.
func decodeJSON(out string, v any) error {
	i := strings.IndexAny(out, "[{")
	if i < 0 {
		return fmt.Errorf("no JSON in bd output: %q", out)
	}
	return json.NewDecoder(strings.NewReader(out[i:])).Decode(v)
}

// InstanceCommit returns the instance database's current commit.
func InstanceCommit(client instanceRunner, repo string) (string, error) {
	out, err := client.Run(vcStatusCmd(repo))
	if err != nil {
		return "", fmt.Errorf("bd vc status: %w\n%s", err, out)
	}
	var st struct {
		Commit string `json:"commit"`
	}
	if err := decodeJSON(out, &st); err != nil {
		return "", fmt.Errorf("bd vc status: %w", err)
	}
	if st.Commit == "" {
		return "", fmt.Errorf("bd vc status reported no commit: %q", out)
	}
	return st.Commit, nil
}

type diffEntry struct {
	DiffType string
	OldValue *Issue
	NewValue *Issue
}

// SessionChanges lists what changed between two instance database commits.
func SessionChanges(client instanceRunner, repo, from, to string) ([]Change, error) {
	out, err := client.Run(diffCmd(repo, from, to))
	if err != nil {
		// bd reports the unresolvable ref in its output, not its exit
		// status, so the output is the only thing to match on.
		if strings.Contains(out, "commit not found") {
			return nil, fmt.Errorf("%w: %w", ErrHistoryRewritten, err)
		}
		return nil, fmt.Errorf("bd diff: %w\n%s", err, out)
	}
	// With nothing to report bd prints prose instead of an empty array.
	if strings.HasPrefix(strings.TrimSpace(out), "No changes") {
		return []Change{}, nil
	}
	var entries []diffEntry
	if err := decodeJSON(out, &entries); err != nil {
		return nil, fmt.Errorf("bd diff: %w", err)
	}
	changes := make([]Change, 0, len(entries))
	for _, e := range entries {
		if c, ok := classifyDiff(e); ok {
			changes = append(changes, c)
		}
	}
	var ids []string
	for _, c := range changes {
		if c.Kind != Removed && c.Issue.UpdatedAt.IsZero() {
			ids = append(ids, c.Issue.ID)
		}
	}
	if len(ids) == 0 {
		return changes, nil
	}
	// bd 1.3 diff omits timestamps. Read them in one batch at the pin,
	// never at the live head where an agent may already have edited again.
	out, err = client.Run(showAtCmd(repo, to, ids))
	if err != nil {
		return nil, fmt.Errorf("bd show --as-of %s: %w\n%s", to, err, out)
	}
	var issues []Issue
	if err := decodeJSON(out, &issues); err != nil {
		return nil, fmt.Errorf("bd show --as-of %s: %w", to, err)
	}
	byID := make(map[string]Issue, len(issues))
	for _, is := range issues {
		byID[is.ID] = is
	}
	for i, c := range changes {
		if c.Kind == Removed || !c.Issue.UpdatedAt.IsZero() {
			continue
		}
		is, ok := byID[c.Issue.ID]
		if !ok {
			return nil, fmt.Errorf("bd show --as-of %s: missing issue %s", to, c.Issue.ID)
		}
		changes[i].Issue = is
	}
	return changes, nil
}

// classifyDiff maps a diff entry to a Change. Closing wins over a plain update,
// since a status flip is the headline of an edit that also touched fields.
func classifyDiff(e diffEntry) (Change, bool) {
	switch {
	case e.DiffType == "added" && e.NewValue != nil:
		return Change{*e.NewValue, Created}, true
	case e.DiffType == "removed" && e.OldValue != nil:
		return Change{*e.OldValue, Removed}, true
	case e.DiffType == "modified" && e.NewValue != nil:
		if e.NewValue.Status == "closed" && (e.OldValue == nil || e.OldValue.Status != "closed") {
			return Change{*e.NewValue, Closed}, true
		}
		return Change{*e.NewValue, Updated}, true
	}
	return Change{}, false
}

// Descendants returns id and everything beneath it, root first, breadth
// first. bd children includes closed issues, which is what makes "x of y
// closed" countable.
func Descendants(client instanceRunner, repo, id string) ([]Issue, error) {
	out, err := client.Run(showCmd(repo, id))
	if err != nil {
		return nil, fmt.Errorf("bd show %s: %w\n%s", id, err, out)
	}
	var root []Issue
	if err := decodeJSON(out, &root); err != nil {
		return nil, fmt.Errorf("bd show %s: %w", id, err)
	}
	if len(root) == 0 {
		return nil, fmt.Errorf("bd show %s: no such issue", id)
	}
	all := []Issue{root[0]}
	for i := 0; i < len(all); i++ {
		out, err := client.Run(childrenCmd(repo, all[i].ID))
		if err != nil {
			return nil, fmt.Errorf("bd children %s: %w\n%s", all[i].ID, err, out)
		}
		var kids []Issue
		if err := decodeJSON(out, &kids); err != nil {
			return nil, fmt.Errorf("bd children %s: %w", all[i].ID, err)
		}
		all = append(all, kids...)
	}
	return all, nil
}

// SyncState says whether the local database already holds a session change.
type SyncState string

const (
	Synced    SyncState = "synced"
	NotSynced SyncState = "not-synced"
	// Unknown means the local database could not be read, so nothing can be
	// claimed either way.
	Unknown SyncState = "unknown"
)

// IssueSync is a session change plus whether it has landed locally.
type IssueSync struct {
	Change
	Sync  SyncState
	Local *Issue
}

// LocalIssues lists every issue in the local database, closed ones included
// (a session's closes must be findable), keyed by id. --limit 0 lifts bd's
// default cap, which would otherwise hide issues and read as not-synced.
func LocalIssues(ctx context.Context, localRepo string) (map[string]Issue, error) {
	out, err := run(ctx, localRepo, "list", "--all", "--limit", "0", "--json")
	if err != nil {
		return nil, fmt.Errorf("bd list: %w\n%s", err, out)
	}
	var issues []Issue
	if err := decodeJSON(out, &issues); err != nil {
		return nil, fmt.Errorf("bd list: %w", err)
	}
	byID := make(map[string]Issue, len(issues))
	for _, is := range issues {
		byID[is.ID] = is
	}
	return byID, nil
}

// CheckSynced decides, per change, whether the local database has caught up.
// A change has landed when the local copy was updated at or after the
// instance's: time.Time.Before compares instants, so differing zone offsets
// in bd's output do not matter. A removed issue has landed when it is gone.
// If the local database could not be read, every answer is Unknown.
func CheckSynced(changes []Change, local map[string]Issue, localErr error) []IssueSync {
	out := make([]IssueSync, 0, len(changes))
	for _, c := range changes {
		s := IssueSync{Change: c, Sync: Unknown}
		if localErr == nil {
			l, ok := local[c.Issue.ID]
			if ok {
				s.Local = &l
			}
			if c.Kind == Removed {
				s.Sync = syncIf(!ok)
			} else if !ok {
				s.Sync = NotSynced
			} else if !c.Issue.UpdatedAt.IsZero() && !l.UpdatedAt.IsZero() {
				s.Sync = syncIf(!l.UpdatedAt.Before(c.Issue.UpdatedAt))
			}
		}
		out = append(out, s)
	}
	return out
}

func syncIf(landed bool) SyncState {
	if landed {
		return Synced
	}
	return NotSynced
}
