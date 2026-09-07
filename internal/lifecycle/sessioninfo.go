package lifecycle

import (
	"context"
	"os"
	"strings"

	"github.com/jskswamy/cloudlab/internal/state"
)

// SessionInfo is what `session list` and `status` display. Every field is
// answerable from the local repository alone: listing must not fetch or
// contact an instance, so it stays fast and still works when the instance is
// down -- which is exactly when you want to know what is on it.
type SessionInfo struct {
	Instance string
	Name     string
	Branch   string
	Unmerged int
	// UnmergedKnown reports whether Unmerged was actually computed. A
	// listing tolerates not knowing; a destructive command must not, or a
	// git failure silently reads as "nothing to lose".
	UnmergedKnown  bool
	WorktreeExists bool
	WorktreeDirty  bool
}

// DescribeSession reports a session's local state. Never returns an error:
// a session whose worktree was deleted by hand, or whose repository has gone
// away, should still appear in a listing rather than failing the whole command.
func DescribeSession(ctx context.Context, instance string, s state.Session) SessionInfo {
	info := SessionInfo{
		Instance: instance,
		Name:     s.Name,
		Branch:   SessionBranch(s.Name),
	}

	local := LocalWorktreePath(s.LocalRepo, s.Name)
	if fi, err := os.Stat(local); err == nil && fi.IsDir() {
		info.WorktreeExists = true
		if out, err := runLocalGit(ctx, local, "status", "--porcelain"); err == nil {
			info.WorktreeDirty = trimLine(out) != ""
		}
	}

	info.Unmerged, info.UnmergedKnown = countUnmerged(ctx, s.LocalRepo, SessionBranch(s.Name))
	return info
}

// countUnmerged reports how many of rev's commits have no equivalent on HEAD,
// and whether git could answer at all -- a listing tolerates not knowing, a
// destructive command must not.
//
// git cherry, not rev-list --count: after a merge the session's commits exist
// on the branch under different SHAs, and only patch identity sees that they
// landed. A count would report merged work as outstanding. Lines starting "+"
// are the ones with no equivalent upstream.
//
// rev is a parameter rather than always the session branch because the local
// branch and the instance are two different answers: the branch only moves
// when pull fast-forwards it, so delete asks the freshly fetched ref as well.
func countUnmerged(ctx context.Context, localRepo, rev string) (int, bool) {
	out, err := runLocalGit(ctx, localRepo, "cherry", "HEAD", rev)
	if err != nil {
		return 0, false
	}
	n := 0
	for _, line := range nonEmptyLines(out) {
		if strings.HasPrefix(line, "+") {
			n++
		}
	}
	return n, true
}
