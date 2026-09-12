package lifecycle

import (
	"strings"

	"github.com/jskswamy/cloudlab/internal/shellcmd"
)

// remoteGitCmd builds a git invocation to run on the instance. See
// shellcmd.LoginShell for why the login shell is mandatory and
// shellcmd.Remote for why every argument is quoted.
//
// beads.instanceCmd is the same builder with a different prefix.
func remoteGitCmd(dir string, args ...string) string {
	return shellcmd.Remote([]string{"git", "-C", shellcmd.Quote(dir)}, args...)
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
	inner := "mkdir -p " + shellcmd.Quote(repo) +
		" && git init --quiet " + shellcmd.Quote(repo)
	return shellcmd.LoginShell(inner)
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
//
// --no-verify because this is the safety net, not a contribution. The
// repository's own pre-commit hooks are written for a developer's machine
// and run here under a non-interactive `bash -lc` over SSH, where a Go
// linter cannot find go on PATH -- so they fail, and without this they took
// the rescue down with them. Gating the checkpoint on lint means the messier
// the tree, the more likely the thing that saves it refuses.
//
// It decides more than a failed pull. DeleteSession and Down both rescue by
// checkpointing first, so hooks that cannot pass on the instance would turn
// teardown into a refusal, or into discarding work with --force that was
// never rescuable. Quality gates belong on the commits a human authors.
func checkpointCmd(repo, message string) string {
	inner := "cd " + shellcmd.Quote(repo) +
		" && git add -A" +
		" && { git diff --cached --quiet || git commit --quiet --no-verify -m " + shellcmd.Quote(message) + "; }"
	return shellcmd.LoginShell(inner)
}

// removeRepoCmd tears a session down on the instance. The repository is the
// session, so removing the directory removes the branch, the working tree and
// the object store together -- there is no shared store left needing a
// separate branch delete or worktree prune.
//
// Called once the session's commits are verified present in the Mac's
// object store (see MergeSession, where that ordering is the safety
// property the whole design rests on) -- or, via DeleteSession --force,
// when the caller has deliberately chosen to discard unverified work
// instead.
func removeRepoCmd(repo string) string {
	q := shellcmd.Quote
	// rmdir, not rm -rf, on the parent: a session can hold more than one
	// repository once multi-repo lands, and rmdir refuses a directory that
	// still has something in it. So the empty case is cleaned up and the
	// sibling case is left alone, without this having to know which it is.
	// The `|| true` keeps a non-empty parent from failing the whole teardown.
	inner := "rm -rf " + q(repo) +
		" && rmdir " + q(remoteSessionDir(repo)) + " 2>/dev/null || true"
	return shellcmd.LoginShell(inner)
}

// remoteSessionDir is the directory holding a session's repositories --
// RemoteRepoPath minus its last element.
func remoteSessionDir(repo string) string {
	if i := strings.LastIndex(repo, "/"); i > 0 {
		return repo[:i]
	}
	return repo
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
	q := shellcmd.Quote
	inner := "git -C " + q(repo) + " config user.name " + q(name) +
		" && git -C " + q(repo) + " config user.email " + q(email)
	return shellcmd.LoginShell(inner)
}

// beadsDirPattern is the ignore entry that keeps a session's issue database
// out of the instance's checkpoint commits. A sibling of worktreeDirPattern,
// which does the same job on this machine.
const beadsDirPattern = "/.beads/"

// excludeBeadsCmd makes sure .beads/ is ignored in the instance's repository.
//
// Three facts compound into the reason this exists. checkpointCmd runs `git
// add -A` on every pull. `bd init` creates .beads/embeddeddolt inside the
// session checkout -- 3.8 MB in this repository today. And .git/info/exclude
// does not travel over `git push`, so the Mac's own exclusions are absent
// from the repository `git init` just made over here. Unexcluded, the first
// pull commits a multi-megabyte database and merge cherry-picks it onto the
// user's branch under their signature.
//
// Written by cloudlab before `bd init` runs, rather than relying on bd to
// write it: setup is fail-safe, so a `bd init` that fails partway is
// tolerated -- but it can still leave .beads/ behind, and by then the guard
// has to already be in place.
//
// .git/info/exclude rather than .gitignore, matching excludeWorktreeDir: it
// is cloudlab's own bookkeeping, not something to add to a file the user
// commits and reviews. grep -qxF makes it idempotent, which session start's
// retry-safety requires.
func excludeBeadsCmd(repo string) string {
	q := shellcmd.Quote
	// --absolute-git-dir, not --git-dir: the latter answers ".git", relative
	// to the -C directory, and this command never cd's -- so the exclusion
	// would land under the login shell's own home directory instead of the
	// session repository, silently doing nothing.
	exclude := "\"$(git -C " + q(repo) + " rev-parse --absolute-git-dir)\"/info/exclude"
	inner := "set -e" +
		"; e=" + exclude +
		"; mkdir -p \"$(dirname \"$e\")\"" +
		"; touch \"$e\"" +
		"; grep -qxF " + q(beadsDirPattern) + " \"$e\"" +
		" || printf '\\n# cloudlab session issue database\\n%s\\n' " + q(beadsDirPattern) + " >> \"$e\""
	return shellcmd.LoginShell(inner)
}
