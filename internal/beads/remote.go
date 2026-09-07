package beads

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

func pushArgs(name string) []string {
	return []string{"dolt", "push", "--remote", name}
}

func pullArgs(name string) []string {
	return []string{"dolt", "pull", "--remote", name}
}
