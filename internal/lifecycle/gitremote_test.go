package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// git lives in the instance user's home-manager profile, not a system path,
// so every remote git command needs a login shell to find it. This is the
// same class of bug that made `sudo tailscale` fail with command not found.
func TestRemoteGitCmd_UsesLoginShell(t *testing.T) {
	got := remoteGitCmd("/home/devuser/sessions/s/repo", "status", "--porcelain")
	if !strings.HasPrefix(got, "bash -lc ") {
		t.Errorf("remoteGitCmd() = %q, want it wrapped in a login shell", got)
	}
	if !strings.Contains(got, "status") || !strings.Contains(got, "--porcelain") {
		t.Errorf("remoteGitCmd() = %q, want the git arguments present", got)
	}
	if !strings.Contains(got, "/home/devuser/sessions/s/repo") {
		t.Errorf("remoteGitCmd() = %q, want -C pointing at the working directory", got)
	}
}

func TestRemoteGitCmd_QuotesAwkwardPaths(t *testing.T) {
	got := remoteGitCmd("/home/devuser/it's here", "status")
	if strings.Contains(got, "; rm") {
		t.Errorf("remoteGitCmd() = %q, path escaped its quoting", got)
	}
	if !strings.Contains(got, `'\''`) {
		t.Errorf("remoteGitCmd() = %q, want the quote in the path escaped", got)
	}
}

func TestEnsureRepoCmd_IsIdempotentAndNotBare(t *testing.T) {
	got := ensureRepoCmd("/home/devuser/sessions/auth/cloudlab")
	if !strings.Contains(got, "init") {
		t.Errorf("ensureRepoCmd() = %q, want a git init", got)
	}
	// The agent needs a working tree to edit; a bare repo has none.
	if strings.Contains(got, "--bare") {
		t.Errorf("ensureRepoCmd() = %q, must not create a bare repo", got)
	}
	// `git init` on an existing repo is a no-op, which is what makes
	// re-running session start after a partial failure safe.
	if strings.Contains(got, "rm -rf") {
		t.Errorf("ensureRepoCmd() = %q, must not destroy an existing repo", got)
	}
}

// The seed pushes a branch the instance has not checked out yet, which is
// the only reason the push is legal without receive.denyCurrentBranch. So
// ensureRepoCmd must not check anything out.
func TestEnsureRepoCmd_LeavesTheRepoUncheckedOut(t *testing.T) {
	got := ensureRepoCmd("/home/devuser/sessions/auth/cloudlab")
	if strings.Contains(got, "checkout") || strings.Contains(got, "switch") {
		t.Errorf("ensureRepoCmd() = %q, must not check out a branch before the push", got)
	}
}

func TestCheckoutSessionCmd_UsesPlainCheckout(t *testing.T) {
	got := checkoutSessionCmd("/home/devuser/sessions/auth/cloudlab", "cloudlab/auth")
	if !strings.Contains(got, "checkout") || !strings.Contains(got, "cloudlab/auth") {
		t.Errorf("checkoutSessionCmd() = %q, want a checkout of the session branch", got)
	}
	// -B would reset a branch that already carries the agent's commits, so a
	// retry after the agent has worked would silently discard them.
	if strings.Contains(got, "-B") || strings.Contains(got, "--force") {
		t.Errorf("checkoutSessionCmd() = %q, must never move or force the session branch", got)
	}
}

func TestCheckpointCmd_StagesEverythingAndToleratesACleanTree(t *testing.T) {
	got := checkpointCmd("/home/devuser/sessions/s/repo", "cloudlab: checkpoint")
	if !strings.Contains(got, "add -A") {
		t.Errorf("checkpointCmd() = %q, want it to stage all changes", got)
	}
	// A clean tree must not be an error: pull runs this every time.
	if !strings.Contains(got, "diff --cached --quiet") {
		t.Errorf("checkpointCmd() = %q, want a guard so a clean tree is a no-op", got)
	}
}

// The checkpoint is the safety net, so nothing the repository configures may
// be able to refuse it. A session whose pre-commit hooks fail on the instance
// -- golangci-lint cannot find go under a non-interactive `bash -lc`, which
// is exactly how this runs -- otherwise cannot be rescued at all, and the
// messier the working tree the more likely the rescue is the thing that
// refuses.
//
// It also decides more than a failed pull. DeleteSession and Down rescue by
// checkpointing first, so hooks that cannot pass on the instance turn
// teardown into either a refusal or, with --force, the discarding of work
// that was never rescuable.
func TestCheckpointCmd_IsNotBlockedByTheRepositorysHooks(t *testing.T) {
	got := checkpointCmd("/home/devuser/sessions/s/repo", "cloudlab: checkpoint")
	if !strings.Contains(got, "--no-verify") {
		t.Errorf("checkpointCmd() = %q, want --no-verify -- quality gates belong on the "+
			"commits a human authors, not on the mechanism that stops work disappearing", got)
	}
}

// One session, one repository: removing the directory removes the branch,
// the working tree and the objects together. No shared store survives it, so
// there is nothing left to prune or delete a branch from.
func TestRemoveRepoCmd_RemovesTheSessionDirectory(t *testing.T) {
	repo := "/home/devuser/sessions/auth/cloudlab"
	got := removeRepoCmd(repo)
	if !strings.Contains(got, "rm -rf") {
		t.Errorf("removeRepoCmd() = %q, want the directory removed", got)
	}
	if !strings.Contains(got, repo) {
		t.Errorf("removeRepoCmd() = %q, want it to name the session repo", got)
	}
}

// The one command on either machine that deletes anything, so a quoting slip
// here is an rm -rf with an attacker-chosen argument. Run for real against a
// bystander directory rather than inspecting the string: the point is that
// the injected command does not execute, which only running it can show.
func TestRemoveRepoCmd_DoesNotExecuteInjectedCommands(t *testing.T) {
	base := t.TempDir()
	bystander := filepath.Join(base, "keepme")
	if err := os.MkdirAll(bystander, 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "session; rm -rf "+bystander)
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Fatal(err)
	}

	if out, err := runShell(t, removeRepoCmd(target)); err != nil {
		t.Fatalf("removeRepoCmd: %v\n%s", err, out)
	}
	if _, err := os.Stat(bystander); err != nil {
		t.Errorf("the injected rm -rf executed and destroyed %s: %v", bystander, err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Errorf("%s still exists; the session repo should have been removed", target)
	}
}

// merge removed the session repository but not the directory holding it, so
// every retired session left an empty ~/sessions/<name>/ behind and the
// instance accumulated them.
func TestRemoveRepoCmd_RemovesTheSessionDirectoryToo(t *testing.T) {
	base := t.TempDir()
	sessionDir := filepath.Join(base, "sessions", "auth")
	repo := filepath.Join(sessionDir, "myrepo")
	if err := os.MkdirAll(repo, 0o750); err != nil {
		t.Fatal(err)
	}

	if out, err := runShell(t, removeRepoCmd(repo)); err != nil {
		t.Fatalf("removeRepoCmd: %v\n%s", err, out)
	}
	if _, err := os.Stat(sessionDir); err == nil {
		t.Errorf("%s still exists; the session directory was left behind", sessionDir)
	}
}

// But it must not take a sibling with it. One session can hold several repos
// once multi-repo lands, and removing one must leave the others alone.
func TestRemoveRepoCmd_LeavesASiblingRepoAlone(t *testing.T) {
	base := t.TempDir()
	sessionDir := filepath.Join(base, "sessions", "auth")
	repo := filepath.Join(sessionDir, "myrepo")
	sibling := filepath.Join(sessionDir, "otherrepo")
	for _, d := range []string{repo, sibling} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}

	if out, err := runShell(t, removeRepoCmd(repo)); err != nil {
		t.Fatalf("removeRepoCmd: %v\n%s", err, out)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Errorf("the sibling repo was destroyed: %v", err)
	}
	if _, err := os.Stat(sessionDir); err != nil {
		t.Errorf("the session directory was removed while a sibling remained: %v", err)
	}
}
