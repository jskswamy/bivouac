package lifecycle

import (
	"context"
	"fmt"
	"strings"

	"github.com/jskswamy/cloudlab/internal/state"
)

// AmbiguousError reports that no rule could pick a session and names what was
// available. Returned rather than resolved so the caller decides what to do:
// pull, merge and delete refuse, while ssh, tmux and herdr offer a picker.
// Putting that choice here would drag a prompt into the code path an
// unattended run takes.
type AmbiguousError struct {
	Candidates []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("several sessions are live (%s) and none is selected\ncd .worktrees/<name> to work on one, or name it explicitly",
		strings.Join(e.Candidates, ", "))
}

// SessionFromCwd reports the session whose worktree cwd is inside.
//
// Reads the checked-out branch rather than matching on the path: a worktree
// created by hand or moved elsewhere still carries cloudlab/<name>, while a
// path match on .worktrees/ would miss it and would also match a directory
// that merely looks like one.
func SessionFromCwd(ctx context.Context, cwd string) (string, bool) {
	out, err := runLocalGit(ctx, cwd, "branch", "--show-current")
	if err != nil {
		return "", false
	}
	branch := trimLine(out)
	name, found := strings.CutPrefix(branch, SessionBranch(""))
	if !found || name == "" {
		return "", false
	}
	return name, true
}

// ResolveSession picks the session a command should act on.
//
//  1. explicit argument
//  2. cwd inside a session worktree
//  3. the only session, if there is exactly one
//  4. otherwise *AmbiguousError
//
// Rule 2 beats rule 3 deliberately: standing in a worktree is unambiguous
// intent, even when it is the only session.
func ResolveSession(ctx context.Context, cwd string, record state.Record, explicit string) (state.Session, error) {
	if explicit != "" {
		s, ok := record.FindSession(explicit)
		if !ok {
			return state.Session{}, fmt.Errorf("instance %s has no session %q%s", record.Name, explicit, availableSuffix(record))
		}
		return s, nil
	}

	if name, ok := SessionFromCwd(ctx, cwd); ok {
		s, found := record.FindSession(name)
		if !found {
			return state.Session{}, fmt.Errorf("this worktree is on branch %s, but instance %s has no session by that name%s", SessionBranch(name), record.Name, availableSuffix(record))
		}
		return s, nil
	}

	switch len(record.Sessions) {
	case 0:
		return state.Session{}, fmt.Errorf("instance %s has no sessions — start one with `cloudlab session start <name>`", record.Name)
	case 1:
		return record.Sessions[0], nil
	default:
		names := make([]string, 0, len(record.Sessions))
		for _, s := range record.Sessions {
			names = append(names, s.Name)
		}
		return state.Session{}, &AmbiguousError{Candidates: names}
	}
}

func availableSuffix(record state.Record) string {
	if len(record.Sessions) == 0 {
		return " (it has none)"
	}
	names := make([]string, 0, len(record.Sessions))
	for _, s := range record.Sessions {
		names = append(names, s.Name)
	}
	return " (available: " + strings.Join(names, ", ") + ")"
}
