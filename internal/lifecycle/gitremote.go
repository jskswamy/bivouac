package lifecycle

import (
	"strings"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// remoteGitCmd builds a git invocation to run on the instance.
//
// bash -lc is mandatory, not stylistic: git comes from the instance user's
// home-manager profile (~/.nix-profile/bin/git), and a non-interactive SSH
// command runs a non-login shell that never sources the profile scripts
// putting that on PATH. Every argument is shell-quoted because the login
// shell re-parses the whole string.
func remoteGitCmd(dir string, args ...string) string {
	quoted := make([]string, 0, len(args)+3)
	quoted = append(quoted, "git", "-C", reconcile.ShellQuote(dir))
	for _, a := range args {
		quoted = append(quoted, reconcile.ShellQuote(a))
	}
	return "bash -lc " + reconcile.ShellQuote(strings.Join(quoted, " "))
}

// ensureRepoCmd creates a session's repository if absent. `git init` is a
// no-op on an existing repository, so re-running session start after a
// failure part-way through is safe.
//
// Left deliberately unchecked-out: a fresh `git init` leaves HEAD on an
// unborn branch, so the branch the Mac pushes next is not the current one
// and receive.denyCurrentBranch never fires. Checking out first would
// require setting denyCurrentBranch=updateInstead to make the push legal.
// See checkoutSessionCmd, which runs after the push for this reason.
func ensureRepoCmd(repo string) string {
	inner := "mkdir -p " + reconcile.ShellQuote(repo) +
		" && git init --quiet " + reconcile.ShellQuote(repo)
	return "bash -lc " + reconcile.ShellQuote(inner)
}

// checkoutSessionCmd puts the session's repository on the session branch,
// giving the agent a populated working tree.
//
// Must run AFTER the Mac has pushed that branch: git refuses a push to a
// checked-out branch, so checking out first would break seeding. Uses plain
// checkout rather than -B so it can never move a branch that already has the
// agent's commits on it; on a retry the branch exists and this is a no-op.
func checkoutSessionCmd(repo, branch string) string {
	return remoteGitCmd(repo, "checkout", branch)
}

// checkpointCmd commits whatever the agent left uncommitted. The
// diff --cached --quiet guard makes a clean tree exit 0 without creating an
// empty commit, so pull can run this unconditionally.
func checkpointCmd(repo, message string) string {
	inner := "cd " + reconcile.ShellQuote(repo) +
		" && git add -A" +
		" && { git diff --cached --quiet || git commit --quiet -m " + reconcile.ShellQuote(message) + "; }"
	return "bash -lc " + reconcile.ShellQuote(inner)
}

// setIdentityCmd gives the session repository the user's own git identity.
//
// Without it git does not fail -- it fabricates an identity from the OS, the
// gecos name plus $USER@$(hostname). cherry-pick preserves the AUTHOR, so that
// invention lands permanently on the user's branch and will not link to their
// account. Where the hostname does not resolve git errors instead, breaking
// pull and merge outright, since both commit on the instance.
//
// Authoring as the user is the honest choice: it is their work, run on their
// behalf, and merge re-signs each commit with their key anyway. Provenance is
// not lost -- the checkpoint subject names the session.
func setIdentityCmd(repo, name, email string) string {
	q := reconcile.ShellQuote
	inner := "git -C " + q(repo) + " config user.name " + q(name) +
		" && git -C " + q(repo) + " config user.email " + q(email)
	return "bash -lc " + reconcile.ShellQuote(inner)
}
