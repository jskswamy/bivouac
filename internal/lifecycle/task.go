package lifecycle

import (
	"errors"
	"fmt"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// TaskText builds what ~/sessions/<name>/TASK.md should say, or "" when the
// user named no task -- session start must not touch an existing TASK.md on
// a name-less retry, and "" is what tells WriteTask to leave it alone.
//
// An issue contributes its ID and the command that expands it rather than a
// copy of its body: a copy is stale the moment anyone edits the issue, and
// the agent has bd on the instance anyway.
func TaskText(task, issue string) (string, error) {
	if task != "" && issue != "" {
		return "", errors.New("pass --task or --issue, not both")
	}
	switch {
	case task != "":
		return "# What this session is for\n\n" + task + "\n", nil
	case issue != "":
		return fmt.Sprintf("# What this session is for\n\nIssue %s. Read it with `bd show %s`.\n", issue, issue), nil
	default:
		return "", nil
	}
}

// WriteTask puts text beside the session's checkout, at SessionDir's path
// rather than inside RemoteRepoPath's -- checkpointCmd's `git add -A` runs
// there on every pull, and a file living inside the checkout would be
// committed onto the user's own branch. A sibling is somewhere that sweep
// never looks at all, which beats an excluded file inside the tree: an
// exclusion is a thing that can fail, the same reason seedBeads abandons
// beads outright when excludeBeadsCmd errors.
//
// A no-op on "": re-running session start is a retry, not a fresh start, and
// a retry with no --task or --issue must leave whatever the first run wrote
// untouched.
func WriteTask(client *reconcile.Client, user, session, text string) error {
	if text == "" {
		return nil
	}
	return client.WriteFile(SessionDir(user, session)+"/TASK.md", text)
}
