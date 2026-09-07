package lifecycle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The fake SSH server cannot speak git's smart transport (see
// TestCreateSessionWorktree_RegistersTheRemote for why), but nothing about
// the two critical bugs on this branch needed it: the worktree start point
// and the signature gate are both pure local git. These tests therefore use
// real repositories on disk -- real `git init --bare`, real commits, real
// signatures -- and reach for the fake SSH server only for the handful of
// instance-side commands that are genuinely remote.

// runShell executes one of the `bash -lc ...` strings this package builds for
// the instance, here on this machine, so the command itself is under test
// rather than a paraphrase of it.
func runShell(t *testing.T, command string) (string, error) {
	t.Helper()
	// #nosec G204 -- test-only, command is built by this package.
	out, err := exec.Command("bash", "-c", command).CombinedOutput()
	return string(out), err
}

// configureSigning makes repo sign commits with a freshly generated ssh key.
// When verifiable is false the allowed-signers file is left unconfigured,
// which is how git reports a commit whose signature it cannot vouch for
// (%G? == N) -- the exact case the signature gate exists to catch.
func configureSigning(t *testing.T, repo string, verifiable bool) {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "id")
	// #nosec G204 -- test-only, all paths from t.TempDir().
	if out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "t@example.com", "-f", key, "-q").CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	mustGit(t, repo, "config", "gpg.format", "ssh")
	mustGit(t, repo, "config", "user.signingkey", key+".pub")
	if !verifiable {
		return
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(dir, "allowed_signers")
	if err := os.WriteFile(allowed, []byte(`t@example.com namespaces="git" `+string(pub)), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "config", "gpg.ssh.allowedSignersFile", allowed)
}

// initRepo creates a repository on branch main with one commit. main rather
// than master on purpose: `git init --bare` sets the bare store's HEAD to
// master, and that mismatch is what C1 was.
func initRepo(t *testing.T, repo string) {
	t.Helper()
	if err := os.MkdirAll(repo, 0o750); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "init", "--quiet")
	mustGit(t, repo, "checkout", "--quiet", "-b", "main")
	mustGit(t, repo, "config", "user.email", "t@example.com")
	mustGit(t, repo, "config", "user.name", "t")
	writeAndCommit(t, repo, "README", "first", "first")
}

func writeAndCommit(t *testing.T, repo, name, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "--quiet", "-m", message)
}

func gitOut(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := runLocalGit(context.Background(), repo, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return trimLine(out)
}

// seedLikeSessionStart runs the instance-side and Mac-side halves of seeding
// in the order StartSession runs them, against real git.
func seedLikeSessionStart(t *testing.T, repo, sessionRepo, branch string) {
	t.Helper()
	if out, err := runShell(t, ensureRepoCmd(sessionRepo)); err != nil {
		t.Fatalf("ensureRepoCmd: %v\n%s", err, out)
	}
	name, email, err := localIdentity(context.Background(), repo)
	if err != nil {
		t.Fatalf("localIdentity: %v", err)
	}
	if out, err := runShell(t, setIdentityCmd(sessionRepo, name, email)); err != nil {
		t.Fatalf("setIdentityCmd: %v\n%s", err, out)
	}
	mustGit(t, repo, pushArgs(sessionRepo, "HEAD", branch)...)
	if out, err := runShell(t, checkoutSessionCmd(sessionRepo, branch)); err != nil {
		t.Fatalf("checkoutSessionCmd: %v\n%s", err, out)
	}
}

// Nothing configured a git identity on the instance, so git fabricated one
// from the OS -- gecos name plus $USER@$(hostname) -- and cherry-pick
// preserves the AUTHOR, so that invention landed permanently on the user's
// branch and would not link to their account. Where the hostname does not
// resolve, git errors instead and pull/merge break outright.
func TestSeeding_GivesTheSessionRepoTheUsersGitIdentity(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	initRepo(t, repo)
	mustGit(t, repo, "config", "user.name", "Real Human")
	mustGit(t, repo, "config", "user.email", "real@example.com")

	sessionRepo := filepath.Join(base, "session")
	seedLikeSessionStart(t, repo, sessionRepo, "cloudlab/auth")

	// The agent commits without configuring anything itself -- exactly what
	// checkpointCmd does on a fresh instance.
	writeAndCommit(t, sessionRepo, "agent.txt", "work", "agent: did the work")

	got := gitOut(t, sessionRepo, "log", "-1", "--pretty=%an <%ae>")
	if got != "Real Human <real@example.com>" {
		t.Errorf("commit author = %q, want the user's own identity", got)
	}
}

// Reading the identity must go through git's own resolution, so a value
// inherited from ~/.gitconfig counts -- most people never set it per-repo.
func TestLocalIdentity_ResolvesThroughGitConfig(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)
	mustGit(t, repo, "config", "user.name", "Configured Name")
	mustGit(t, repo, "config", "user.email", "configured@example.com")

	name, email, err := localIdentity(context.Background(), repo)
	if err != nil {
		t.Fatalf("localIdentity() error = %v", err)
	}
	if name != "Configured Name" || email != "configured@example.com" {
		t.Errorf("localIdentity() = %q <%q>, want Configured Name <configured@example.com>", name, email)
	}
}

// C1's successor. The old bare store's HEAD was whatever `git init --bare`
// chose and a push never moved it, so a session could not be started from a
// branch by any other name. Seeding must not care what the Mac's branch is
// called.
func TestSeeding_WorksFromAnyLocalBranchName(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	initRepo(t, repo)
	mustGit(t, repo, "checkout", "--quiet", "-b", "some-feature-branch")
	writeAndCommit(t, repo, "f.txt", "x", "on the feature branch")

	sessionRepo := filepath.Join(base, "session")
	seedLikeSessionStart(t, repo, sessionRepo, "cloudlab/auth")

	if got := gitOut(t, sessionRepo, "rev-parse", "--abbrev-ref", "HEAD"); got != "cloudlab/auth" {
		t.Errorf("session repo is on %q, want cloudlab/auth", got)
	}
	if got, want := gitOut(t, sessionRepo, "rev-parse", "HEAD"), gitOut(t, repo, "rev-parse", "HEAD"); got != want {
		t.Errorf("session branch starts at %s, want the Mac's commit %s", got, want)
	}
	// The seeded content has to actually be on disk for the agent to edit.
	if _, err := os.Stat(filepath.Join(sessionRepo, "f.txt")); err != nil {
		t.Errorf("seeded file missing from the agent's working tree: %v", err)
	}
}

// The push must precede the checkout. git refuses a push to a checked-out
// branch, so doing it the other way round breaks every seed -- and the fix
// would be configuring receive.denyCurrentBranch, which this ordering avoids
// needing at all.
func TestSeeding_CheckoutBeforePushIsWhatWeAvoid(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	initRepo(t, repo)

	sessionRepo := filepath.Join(base, "session")
	if out, err := runShell(t, ensureRepoCmd(sessionRepo)); err != nil {
		t.Fatalf("ensureRepoCmd: %v\n%s", err, out)
	}
	// Seed once so the branch exists, then check it out -- the state a retry
	// of session start finds.
	mustGit(t, repo, pushArgs(sessionRepo, "HEAD", "cloudlab/auth")...)
	if out, err := runShell(t, checkoutSessionCmd(sessionRepo, "cloudlab/auth")); err != nil {
		t.Fatalf("checkoutSessionCmd: %v\n%s", err, out)
	}

	writeAndCommit(t, repo, "second.txt", "more", "second")
	out, err := runLocalGit(context.Background(), repo, pushArgs(sessionRepo, "HEAD", "cloudlab/auth")...)
	if err == nil {
		t.Fatal("push to the checked-out session branch succeeded; the init-push-checkout ordering is load-bearing and this documents why")
	}
	if !strings.Contains(out, "checked out") {
		t.Errorf("push failed for an unexpected reason: %s", out)
	}
}

// I8's successor. A start that failed after the instance repo was created has
// to be fixable by running the same command again, and the retry must not
// touch the agent's work -- committed or not.
func TestSeeding_RetryLeavesExistingWorkAlone(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	initRepo(t, repo)

	sessionRepo := filepath.Join(base, "session")
	seedLikeSessionStart(t, repo, sessionRepo, "cloudlab/auth")

	mustGit(t, sessionRepo, "config", "user.email", "agent@example.com")
	mustGit(t, sessionRepo, "config", "user.name", "agent")
	writeAndCommit(t, sessionRepo, "agent.txt", "work", "agent work")
	agentTip := gitOut(t, sessionRepo, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(sessionRepo, "wip.txt"), []byte("uncommitted"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The two idempotent halves re-run. The push between them is the one step
	// that legitimately refuses on a retry, because the agent has moved the
	// branch on -- that refusal is the subject of the test above.
	if out, err := runShell(t, ensureRepoCmd(sessionRepo)); err != nil {
		t.Fatalf("retried ensureRepoCmd: %v\n%s", err, out)
	}
	if out, err := runShell(t, checkoutSessionCmd(sessionRepo, "cloudlab/auth")); err != nil {
		t.Fatalf("retried checkoutSessionCmd: %v\n%s", err, out)
	}

	if got := gitOut(t, sessionRepo, "rev-parse", "HEAD"); got != agentTip {
		t.Errorf("session tip after retry = %s, want the agent's commit %s left alone", got, agentTip)
	}
	if _, err := os.Stat(filepath.Join(sessionRepo, "wip.txt")); err != nil {
		t.Errorf("retry destroyed the agent's uncommitted file: %v", err)
	}
}
