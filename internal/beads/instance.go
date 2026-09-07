package beads

import (
	"strings"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// instanceCmd wraps a bd invocation for the instance.
//
// bash -lc is mandatory, not stylistic: bd comes from the instance user's
// home-manager profile (~/.nix-profile/bin/bd), and a non-interactive SSH
// command runs a non-login shell that never sources the profile scripts
// putting that on PATH. Every argument is shell-quoted because the login
// shell re-parses the whole string -- including repo, and including a
// fixed literal like "init" or "push", so there is exactly one escaping
// path to trust rather than a second hand-audited allow-list standing next
// to reconcile.ShellQuote, which this codebase already proves and uses
// everywhere else. Same shape as lifecycle.remoteGitCmd.
func instanceCmd(repo string, args ...string) string {
	quoted := make([]string, 0, len(args)+3)
	quoted = append(quoted, "cd", reconcile.ShellQuote(repo), "&&", "bd")
	for _, a := range args {
		quoted = append(quoted, reconcile.ShellQuote(a))
	}
	return "bash -lc " + reconcile.ShellQuote(strings.Join(quoted, " "))
}

// initCmd clones the session's issue database out of the git ref the Mac
// seeded, into the instance's checkout.
//
// --stealth regardless of what mode the Mac is in. bd's non-stealth init
// writes files bd expects to be tracked, and the instance's checkpoint runs
// `git add -A` on every pull -- so anything left in the working tree lands in
// a commit that merge then cherry-picks onto the user's branch under their
// signature. See lifecycle's excludeBeadsCmd for the other half of that
// guard.
//
// --remote adopts the project identity and issue prefix from the cloned
// database, so issues keep their cloudlab- names on the instance rather than
// being renamed after whatever the directory happens to be called.
func initCmd(repo, url string) string {
	return instanceCmd(repo, "init", "--stealth", "--remote", url)
}

// remoteAddCmd gives the instance a second destination to push to. Only used
// in "dolthub" mode, and only after the git+file:// bootstrap has succeeded.
func remoteAddCmd(repo, name, url string) string {
	return instanceCmd(repo, "dolt", "remote", "add", name, url)
}

// pushCmd publishes the agent's issue edits into the session repository's own
// refs/dolt/data, where the Mac's pull can reach them over SSH.
func pushCmd(repo string) string {
	return instanceCmd(repo, "dolt", "push")
}

// versionCmd reads the instance's bd version, compared against the Mac's
// because a shared Dolt database is the one place a version gap does real
// damage.
func versionCmd(repo string) string {
	return instanceCmd(repo, "version")
}
