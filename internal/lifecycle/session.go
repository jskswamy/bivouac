package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// runLocalGit runs a git command on this machine inside localRepo.
func runLocalGit(ctx context.Context, localRepo string, args ...string) (string, error) {
	full := append([]string{"-C", localRepo}, args...)
	// #nosec G204 -- argv-array exec.Command, no shell; localRepo is the
	// user's own repo path and args are built by this package.
	out, err := exec.CommandContext(ctx, "git", full...).CombinedOutput()
	return string(out), err
}

// StartSession creates a session: a repository on the instance for the agent
// to work in, and the matching worktree on this machine to review and run it
// in. The local worktree is created here and only here -- pull updates it but
// never creates it -- so a session always has somewhere to land before any
// work exists.
//
// Every step is retry-safe, because a start that fails partway through --
// most easily at the push or fetch, the first real network operations -- must
// be fixable by running the same command again.
func StartSession(ctx context.Context, ip, user, localRepo, repoName, session string) error {
	if err := CheckSessionName(session); err != nil {
		return err
	}
	provider.ReportProgress(ctx, "creating session "+session)

	// Resolved before the seeding SSH connection so the remote registered
	// below and the push above it agree on one address.
	host := gitHost(ctx, ip, user)
	repo := RemoteRepoPath(user, session, repoName)
	branch := SessionBranch(session)
	url := sshGitURL(user, host, repo)

	if err := seedSession(ctx, ip, user, localRepo, repo, branch, url); err != nil {
		return err
	}
	return trackSession(ctx, localRepo, session, branch, url)
}

// seedSession creates the session's repository on the instance and publishes
// the Mac's current commit into it as the session branch.
//
// The order -- init, push, then checkout -- is what keeps this working
// without configuring receive.denyCurrentBranch. A fresh `git init` leaves
// HEAD on an unborn branch, so the pushed branch is not the checked-out one
// and the push is legal; the checkout afterwards gives the agent its files.
// Reversing those two steps makes every seed fail.
func seedSession(ctx context.Context, ip, user, localRepo, repo, branch, url string) error {
	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	if out, err := client.Run(ensureRepoCmd(repo)); err != nil {
		return fmt.Errorf("creating session repo on instance: %w\n%s", err, out)
	}

	// Before anything can commit there. checkpointCmd runs `git commit` on
	// every pull and merge, and a repo with no identity does not fail -- it
	// invents one from the hostname and cherry-pick preserves it.
	name, email, err := localIdentity(ctx, localRepo)
	if err != nil {
		return err
	}
	if out, err := client.Run(setIdentityCmd(repo, name, email)); err != nil {
		return fmt.Errorf("setting git identity on instance: %w\n%s", err, out)
	}

	// HEAD, not the branch name: a detached HEAD or an unusual checkout is
	// still a commit worth seeding, and the session branch is named for the
	// session regardless of what the Mac calls the branch it came from.
	if out, err := runLocalGit(ctx, localRepo, pushArgs(url, "HEAD", branch)...); err != nil {
		return fmt.Errorf("seeding session onto instance: %w\n%s\nif the session already has commits, pull or merge it instead of starting it again", err, out)
	}
	if out, err := client.Run(checkoutSessionCmd(repo, branch)); err != nil {
		return fmt.Errorf("checking out %s on instance: %w\n%s", branch, err, out)
	}
	return nil
}

// trackSession registers the session's remote on this machine and creates the
// local worktree that mirrors it. Split from seedSession so it can be
// exercised without a real git push to a real instance, which no in-process
// test harness here can satisfy (see this function's test for why).
func trackSession(ctx context.Context, localRepo, session, branch, url string) error {
	remote := sessionRemote(session)
	// Re-adding an existing remote is an error, and a session may be
	// recreated after a failed start, so drop any stale one first.
	_, _ = runLocalGit(ctx, localRepo, remoteRemoveArgs(remote)...)
	if out, err := runLocalGit(ctx, localRepo, remoteAddArgs(remote, url)...); err != nil {
		return fmt.Errorf("registering remote %s: %w\n%s", remote, err, out)
	}
	if out, err := runLocalGit(ctx, localRepo, fetchRemoteArgs(remote)...); err != nil {
		return fmt.Errorf("fetching %s: %w\n%s", remote, err, out)
	}

	if err := excludeWorktreeDir(ctx, localRepo); err != nil {
		return err
	}

	local := LocalWorktreePath(localRepo, session)
	// Prune the administrative entry a hand-deleted directory leaves behind,
	// then leave an existing worktree alone rather than re-adding over
	// whatever the user has in it.
	_, _ = runLocalGit(ctx, localRepo, "worktree", "prune")
	if _, err := os.Stat(local); err == nil {
		return nil
	}
	tracking := remote + "/" + branch
	if out, err := runLocalGit(ctx, localRepo, "worktree", "add", "--track", "-B", branch, local, tracking); err != nil {
		return fmt.Errorf("creating local worktree at %s: %w\n%s", local, err, out)
	}
	return nil
}

// worktreeDirPattern is the ignore entry that keeps session worktrees out of
// the user's git status.
const worktreeDirPattern = "/.worktrees/"

// excludeWorktreeDir makes sure .worktrees/ is ignored in localRepo.
//
// Session worktrees live inside the repository so sandboxed agents can reach
// them, which means git sees them as untracked content. Left alone that
// pollutes the user's `git status` and, worse, makes merge's
// working-tree-clean check fail forever on any repository that has not
// already ignored the directory.
//
// Written to .git/info/exclude rather than .gitignore deliberately: it is
// cloudlab's own bookkeeping, not something to add to a file the user commits
// and reviews. Idempotent, and a repository that already ignores the
// directory some other way is left untouched.
func excludeWorktreeDir(ctx context.Context, localRepo string) error {
	dir, err := runLocalGit(ctx, localRepo, "rev-parse", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("locating the git directory: %w\n%s", err, dir)
	}
	gitDir := trimLine(dir)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(localRepo, gitDir)
	}

	exclude := filepath.Join(gitDir, "info", "exclude")
	if existing, err := os.ReadFile(exclude); err == nil {
		if slices.Contains(nonEmptyLines(string(existing)), worktreeDirPattern) {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(exclude), 0o750); err != nil {
		return fmt.Errorf("preparing %s: %w", exclude, err)
	}
	// #nosec G304 -- path derived from git's own --git-common-dir.
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", exclude, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString("\n# cloudlab session worktrees\n" + worktreeDirPattern + "\n"); err != nil {
		return fmt.Errorf("writing %s: %w", exclude, err)
	}
	return nil
}

// trimLine returns s without surrounding whitespace or a trailing newline.
func trimLine(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

// nonEmptyLines splits s on newlines, dropping blanks.
func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if trimmed := trimLine(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// HeadCommit returns the SHA localRepo's HEAD points at. Exported so session
// start can record the commit a session branches from; merge needs it to tell
// the session's own work apart from history that was rewritten underneath it.
func HeadCommit(ctx context.Context, localRepo string) (string, error) {
	out, err := runLocalGit(ctx, localRepo, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("reading HEAD in %s: %w\n%s", localRepo, err, out)
	}
	return trimLine(out), nil
}

// localIdentity reads the git identity that applies in localRepo.
//
// Plain `git config` rather than `--local`, so the value is resolved the way
// git itself resolves it -- through the repository, then the user's global
// config, then system. Most people set this once in ~/.gitconfig and never
// per-repo, so reading only the local scope would find nothing for almost
// everyone.
func localIdentity(ctx context.Context, localRepo string) (string, string, error) {
	name, err := runLocalGit(ctx, localRepo, "config", "user.name")
	if err != nil {
		return "", "", fmt.Errorf("reading user.name: %w\nset it with `git config --global user.name \"Your Name\"` -- the instance needs an identity to commit with, and without one git invents one from the hostname", err)
	}
	email, err := runLocalGit(ctx, localRepo, "config", "user.email")
	if err != nil {
		return "", "", fmt.Errorf("reading user.email: %w\nset it with `git config --global user.email you@example.com` -- the instance needs an identity to commit with, and without one git invents one from the hostname", err)
	}
	return trimLine(name), trimLine(email), nil
}
