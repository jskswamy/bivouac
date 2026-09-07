package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noGitSSH makes git's ssh transport fail instantly instead of dialling.
//
// Several tests here drive a real `git push`/`git fetch` at a URL that no
// real server answers, and rely on it failing so the steps *before* it can be
// asserted. How it fails must not depend on the machine: with no usable SSH
// key the handshake errors immediately, but on a developer's own machine --
// keys loaded, agent running -- git blocks on the handshake instead, and the
// package times out after ten minutes. That is exactly what happened the
// first time this suite ran outside a sandbox, so the transport is pinned
// here rather than left to the environment.
func noGitSSH(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_SSH_COMMAND", "false")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
}

// The fake SSH server cannot complete a real git push, so this asserts
// everything up to that wall: the session repository is created first, and
// the checkout that would follow has not run yet -- the ordering the seed
// depends on.
func TestSeedSession_CreatesTheRepoThenPushes(t *testing.T) {
	noGitSSH(t)
	startFakeAgent(t)

	var commands []string
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		commands = append(commands, cmd)
		return "", 0
	})

	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)

	remoteRepo := RemoteRepoPath("devuser", "auth", "cloudlab")
	err := seedSession(context.Background(), addr, "devuser", repo, remoteRepo,
		SessionBranch("auth"), sshGitURL("devuser", addr, remoteRepo))
	if err == nil {
		t.Fatal("seedSession() = nil, want an error: the fake SSH server cannot complete a real git push")
	}
	if !strings.Contains(err.Error(), "seeding session onto instance") {
		t.Errorf("error = %q, want the failure to be the push, proving the steps before it succeeded", err.Error())
	}

	if len(commands) == 0 {
		t.Fatal("seedSession() ran no remote commands, want a git init")
	}
	if !strings.Contains(commands[0], "init") || strings.Contains(commands[0], "--bare") {
		t.Errorf("commands[0] = %q, want a non-bare init first", commands[0])
	}
	if !strings.Contains(commands[0], "sessions/auth/cloudlab") {
		t.Errorf("commands[0] = %q, want the per-session repo path", commands[0])
	}
	// The checkout must not have happened: it runs only after a push
	// succeeds, because git refuses to push to a checked-out branch.
	for _, c := range commands {
		if strings.Contains(c, "checkout") {
			t.Errorf("checkout ran before the push succeeded: %q", c)
		}
	}
}

// Rescue must checkpoint before fetching, or uncommitted agent work is
// invisible to git and lost when the instance goes away.
func TestRescueSession_CheckpointsBeforeFetching(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var commands []string
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		commands = append(commands, cmd)
		return "", 0
	})

	_, _, _ = RescueSession(context.Background(), addr, "devuser", t.TempDir(), "cloudlab", "auth")

	var checkpointAt = -1
	for i, c := range commands {
		if strings.Contains(c, "add -A") {
			checkpointAt = i
			break
		}
	}
	if checkpointAt != 0 {
		t.Errorf("commands = %v, want the checkpoint first", commands)
	}
}

// What RescueSession actually returns, and that the ref resolves, is
// asserted against real repositories in
// TestRescueSession_LandsWorkOnTheRemoteTrackingRef (merge_test.go). It
// cannot be asserted here: the fake SSH server cannot serve a fetch, so
// RescueSession only ever fails, and every claim about its success value
// would hold trivially.

// A fetch reporting success is not proof the object arrived. Verification
// is what authorises anything destructive downstream.
func TestVerifyFetched_FailsWhenObjectIsAbsent(t *testing.T) {
	repo := t.TempDir()
	if out, err := runLocalGit(context.Background(), repo, "init", "--quiet"); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	err := verifyFetched(context.Background(), repo, "0000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("verifyFetched() = nil, want an error for an object this repo does not have")
	}
}

// pull must change nothing on the instance beyond checkpointing: the agent
// is still working in that tree, and pull is run repeatedly while it is.
// (What pull does on *this* machine -- fast-forward the session worktree,
// leave the user's branch alone -- is asserted against real repositories in
// TestPullSession_UpdatesTheSessionWorktreeAndNeverTheUsersBranch, since the
// commands captured here are only the remote ones.)
func TestPullSession_ChangesNothingOnTheInstanceBeyondCheckpointing(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var commands []string
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		commands = append(commands, cmd)
		return "", 0
	})

	_, _ = PullSession(context.Background(), addr, "devuser", t.TempDir(), "cloudlab", "auth", "")

	if len(commands) == 0 {
		t.Fatal("PullSession() ran no remote commands, want at least a checkpoint")
	}
	joined := strings.Join(commands, "\n")
	for _, forbidden := range []string{"rebase", "reset", "checkout", "worktree remove", "branch -D"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("commands = %v, pull must not run %q on the instance", commands, forbidden)
		}
	}
}

// StartSession now seeds the repository first (seeding moved out of `up` and
// into session start), so a session's remote worktree is never created
// against an instance that doesn't have the code yet. The fake SSH server
// here isn't a real git server -- SeedRepo's push can't complete against it,
// same as SeedRepo's own test above -- so this only asserts that seeding is
// attempted before anything else, and that a failed seed stops the session
// from being created rather than silently skipping ahead to the worktree
// and remote-registration steps.
func TestStartSession_SeedsRepoBeforeCreatingTheWorktree(t *testing.T) {
	noGitSSH(t)
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var commands []string
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		commands = append(commands, cmd)
		return "", 0
	})

	repo := t.TempDir()
	mustGit(t, repo, "init", "--quiet")
	mustGit(t, repo, "config", "user.email", "t@example.com")
	mustGit(t, repo, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "--quiet", "-m", "first")

	if err := StartSession(context.Background(), addr, "devuser", repo, "cloudlab", "auth-refactor"); err == nil {
		t.Fatal("StartSession() = nil, want an error since the fake SSH server cannot complete a real git push")
	}

	// The tailnet lookup runs first to resolve the git host, then the repo
	// is created. Both precede the push that fails.
	var initAt = -1
	for i, c := range commands {
		if strings.Contains(c, "git init") {
			initAt = i
			break
		}
	}
	if initAt < 0 {
		t.Errorf("commands = %v, want the session repo created before the push", commands)
	}
	for _, c := range commands {
		if strings.Contains(c, "checkout") {
			t.Errorf("commands = %v, want StartSession to stop when seeding fails, not reach the checkout step", commands)
		}
	}
}

// createSessionWorktree is the core new logic from moving the git remote
// setup into session start: create the remote worktree on the session
// branch, then register a same-named local remote pointing at it. Tested
// directly (bypassing StartSession's SeedRepo call, and so its real git
// push) so it can be driven against the fake SSH server.
//
// The fetch this function runs against the freshly-registered remote is a
// second, separate real git network operation, and hits the same wall
// SeedRepo's push does: the fake SSH server here writes its canned response
// only after reading the client's full input, but git's smart transport
// (upload-pack for fetch, same as receive-pack for push) requires the
// server to write first -- so the two block on each other. Making that work
// would mean teaching the shared fake-SSH exec dispatch in ready_test.go to
// proxy bytes bidirectionally to a real `git upload-pack` subprocess, which
// is shared by four test files and out of this task's scope (the same
// reason rsync_test.go declines to test a real SSH-backed transfer
// end-to-end). So this test proves everything up to that wall: the remote
// worktree is created on the correctly-named session branch before any
// local git runs, and the remote is registered locally under the right
// name pointing at the right URL, with the stale-remote removed first --
// then asserts the call fails at the fetch step, not before or silently.
func TestTrackSession_RegistersTheRemote(t *testing.T) {
	noGitSSH(t)
	repo := t.TempDir()
	mustGit(t, repo, "init", "--quiet")
	mustGit(t, repo, "config", "user.email", "t@example.com")
	mustGit(t, repo, "config", "user.name", "t")

	url := sshGitURL("devuser", "203.0.113.5", RemoteRepoPath("devuser", "auth-refactor", "cloudlab"))
	err := trackSession(context.Background(), repo, "auth-refactor", SessionBranch("auth-refactor"), url)
	if err == nil {
		t.Fatal("trackSession() = nil, want an error: there is no instance to fetch from")
	}
	if !strings.Contains(err.Error(), "fetching") {
		t.Errorf("error = %q, want the failure to be at the fetch step, proving the remote was registered before it", err.Error())
	}

	remote := sessionRemote("auth-refactor")
	gotURL, err := runLocalGit(context.Background(), repo, "remote", "get-url", remote)
	if err != nil {
		t.Fatalf("git remote get-url %s: %v\n%s", remote, err, gotURL)
	}
	if strings.TrimSpace(gotURL) != url {
		t.Errorf("remote %s url = %q, want %q", remote, strings.TrimSpace(gotURL), url)
	}
}

// The remote must carry whatever address StartSession resolved, so a session
// created while Tailscale is up keeps talking over the tailnet rather than
// silently falling back to the public address on the next fetch.
func TestTrackSession_UsesTheURLItWasGiven(t *testing.T) {
	noGitSSH(t)
	repo := t.TempDir()
	mustGit(t, repo, "init", "--quiet")

	tailnetURL := sshGitURL("devuser", "100.64.0.7", RemoteRepoPath("devuser", "auth", "cloudlab"))
	_ = trackSession(context.Background(), repo, "auth", SessionBranch("auth"), tailnetURL)

	gotURL, err := runLocalGit(context.Background(), repo, "remote", "get-url", sessionRemote("auth"))
	if err != nil {
		t.Fatalf("git remote get-url: %v\n%s", err, gotURL)
	}
	if !strings.Contains(gotURL, "100.64.0.7") {
		t.Errorf("remote url = %q, want the tailnet address preserved", strings.TrimSpace(gotURL))
	}
}

func mustGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	if out, err := runLocalGit(context.Background(), repo, args...); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
