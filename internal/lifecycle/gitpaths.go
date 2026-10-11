package lifecycle

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// SessionBranch is the branch an agent commits to for a session. Namespaced
// under bivouac/ so it can never collide with a branch a human made, and
// named for the work rather than the instance -- the instance is where it
// happened to run, which is not information worth keeping.
func SessionBranch(session string) string {
	return "bivouac/" + session
}

// SessionDir is a session's own directory on the instance -- the parent
// RemoteRepoPath's checkout sits inside. Extracted so anything that belongs
// beside the checkout rather than in it, like TASK.md, has a name for that
// directory instead of hand-assembling RemoteRepoPath's path and lopping off
// the last segment.
func SessionDir(user, session string) string {
	return "/home/" + user + "/sessions/" + session
}

// RemoteRepoPath is a session's repository on the instance. This is the
// directory an agent is pointed at, and it is an ordinary git repository
// rather than a worktree of a shared store: one session, one clone.
//
// A shared bare store with a worktree per session would move fewer objects
// for a second session, but it costs a second instance-side path, a seeded
// base branch to start worktrees from, and the whole class of "bare HEAD
// names a branch nothing pushed" failure. Several sessions do run side by
// side now, and a clone each still buys the isolation for less: the extra
// objects are copied once, on a link that is already moving the whole repo.
func RemoteRepoPath(user, session, repo string) string {
	if err := CheckRemoteRepoPath(repo); err != nil {
		panic(err)
	}
	return SessionDir(user, session) + "/" + repo
}

// CheckRemoteRepoPath rejects repository path identities that could escape a
// session directory or address an ambiguous place inside it.
func CheckRemoteRepoPath(repo string) error {
	if repo == "" {
		return fmt.Errorf("remote repo path is empty")
	}
	if path.IsAbs(repo) {
		return fmt.Errorf("remote repo path %q must be relative", repo)
	}
	for _, part := range strings.Split(repo, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("remote repo path %q contains invalid segment %q", repo, part)
		}
	}
	return nil
}

// LocalWorktreePath is the Mac-side worktree for reviewing and running a
// session's work. Inside the user's own repository under .worktrees/, which
// is already gitignored and already where this project's worktrees live.
//
// Inside the project rather than under the home directory because sandboxing
// tools scope themselves to the project directory -- github.com/jskswamy/aide
// among them -- and cannot reach a session worktree created outside it. A
// worktree an agent's sandbox cannot see is a worktree the agent cannot use.
func LocalWorktreePath(localRepo, session string) string {
	return filepath.Join(localRepo, ".worktrees", session)
}
