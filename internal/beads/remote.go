package beads

import (
	"context"
	"fmt"
	"strings"
)

// RemoteName is the dolt remote pointing at a session's repository on the
// instance. Deliberately the same name lifecycle.sessionRemote gives the git
// remote: they address the same repository over the same channel, and one
// name for one thing is what makes `git remote -v` and `bd dolt remote list`
// legible side by side.
func RemoteName(session string) string {
	return "cloudlab-" + session
}

// RemoteURL is the dolt remote URL the Mac pushes issue data to: the
// session's own repository, over the SSH channel its code already uses.
//
// The git+ prefix is written out rather than left for bd to infer. `bd dolt
// remote add` normalises a bare filesystem path to git+file:// only when no
// external remote is configured; with a DoltHub sync.remote present it
// instead appends the path to the DoltHub base URL and produces a nonsense
// remote that fails at push. Establishing that cost a rehearsal, so it is
// recorded here rather than rediscovered.
func RemoteURL(user, host, repoPath string) string {
	return "git+ssh://" + user + "@" + host + repoPath
}

// FileURL is the same repository addressed from the instance itself. The
// instance bootstraps from this rather than from DoltHub even in "dolthub"
// mode: there is one bootstrap path in both modes -- local, offline, needing
// no credential -- for a database the session repository already holds.
func FileURL(repoPath string) string {
	return "git+file://" + repoPath
}

func addRemoteArgs(name, url string) []string {
	return []string{"dolt", "remote", "add", name, url}
}

func removeRemoteArgs(name string) []string {
	return []string{"dolt", "remote", "remove", name}
}

// RemoveRemote drops this machine's dolt remote for session, if one exists.
//
// Exported for two callers that need it best-effort, after Seed already runs
// it unexported and silently as a pre-add safety wipe: a session whose
// instance-side Bootstrap failed must not leave Wired reporting true for a
// database that was never actually cloned there (seedBeads), and a session
// that has ended must not leave a stale remote behind to make Wired report
// true for an unrelated later session started at the same name (merge,
// delete).
//
// Silent, like Wired, when there is no beads database here at all or bd is
// not on PATH: merge and delete run on every session regardless of whether
// beads was ever used in the repository, and a repository with neither must
// stay completely inert rather than surfacing a warning about a remote that
// was never a possibility. Also silent when the database exists but this
// session's remote specifically was never registered -- "unknown remote" is
// bd's own wording for that, read from its output rather than its exit code,
// which it does not distinguish from any other remote-command failure.
func RemoveRemote(ctx context.Context, localRepo, session string) error {
	if !Present(localRepo) || !Available() {
		return nil
	}
	remote := RemoteName(session)
	out, err := run(ctx, localRepo, removeRemoteArgs(remote)...)
	if err != nil && !strings.Contains(out, "unknown remote") {
		return fmt.Errorf("removing dolt remote %s: %w\n%s", remote, err, out)
	}
	return nil
}

func pushArgs(name string) []string {
	return []string{"dolt", "push", "--remote", name}
}

func pullArgs(name string) []string {
	return []string{"dolt", "pull", "--remote", name}
}
