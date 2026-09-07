package lifecycle

import (
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
