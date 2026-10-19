package lifecycle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/shellcmd"
	"github.com/jskswamy/bivouac/internal/state"
)

func TestRetirementRemovalFailureCannotBeMasked(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "session", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// Let later cleanup succeed so it cannot accidentally preserve the
	// failure status of checkout removal when its guard is missing.
	if err := os.WriteFile(filepath.Join(bin, "rm"), []byte("#!/bin/sh\nif [ \"$1\" = -rf ]; then exit 9; fi\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Login profiles can reset PATH, so install the failing rm after those
	// profiles run rather than letting the host's real rm remove the fixture.
	envFile := filepath.Join(root, "bash-env")
	if err := os.WriteFile(envFile, []byte("export PATH="+shellcmd.Quote(bin)+":\"$PATH\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BASH_ENV", envFile)
	cmd := exec.Command("bash", "-c", removeRepoCheckedCmd(filepath.Dir(repo), repo, "branch", "tip", true))
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 9 {
		t.Fatalf("checked removal error = %v, want rm exit 9: %s", err, out)
	}
	if _, err := os.Stat(repo); err != nil {
		t.Fatalf("failed removal lost checkout: %v", err)
	}
}

func TestRetirementDetectsTipMovementBeforeRemoval(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "session", "repo")
	initRepo(t, repo)
	branch := gitOut(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
	cmd := exec.Command("bash", "-c", removeRepoCheckedCmd(filepath.Dir(repo), repo, branch, "not-the-tip", false))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "session tip moved") {
		t.Fatalf("command err=%v out=%s", err, out)
	}
	if _, err := os.Stat(repo); err != nil {
		t.Fatalf("tip movement removed checkout: %v", err)
	}
}

// A repository identity can be owner/repo, which puts the checkout two levels
// below the session directory where TASK.md lives. Retirement must tear down
// the session directory itself, not the repo's immediate parent.
func TestRetirementRemovesSessionDirForNestedRepo(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "sessions", "auth")
	repo := filepath.Join(sessionDir, "owner", "repo")
	initRepo(t, repo)
	if err := os.WriteFile(filepath.Join(sessionDir, "TASK.md"), []byte("task"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", removeRepoCheckedCmd(sessionDir, repo, "branch", "tip", true))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checked removal: %v: %s", err, out)
	}
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(sessionDir)
		t.Fatalf("session dir left behind (stat err=%v, entries=%v)", err, entries)
	}
}

func TestRetirementKeepsSessionDirHoldingSiblingRepo(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "sessions", "auth")
	repo := filepath.Join(sessionDir, "owner", "repo")
	sibling := filepath.Join(sessionDir, "other", "repo")
	initRepo(t, repo)
	initRepo(t, sibling)
	cmd := exec.Command("bash", "-c", removeRepoCheckedCmd(sessionDir, repo, "branch", "tip", true))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checked removal: %v: %s", err, out)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("removal touched a sibling repository: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(repo)); !os.IsNotExist(err) {
		t.Fatalf("emptied owner dir left behind: %v", err)
	}
}

func TestRetirementAlreadyMissingResourceIsIdempotent(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "missing", "repo")
	cmd := exec.Command("bash", "-c", removeRepoCheckedCmd(filepath.Dir(repo), repo, "branch", "tip", false))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("missing checkout: %v: %s", err, out)
	}
}

func TestForcedOfflineDeleteKeepsRemoteObligations(t *testing.T) {
	store, _, _ := retirementCoordinatorFixture(t)
	var events []string
	withRetirementSeams(t, &events, "remote unavailable")
	result, err := RetireSession(context.Background(), store, "instance", "auth", RetirementOptions{Operation: "delete", Force: true})
	if err == nil || result.Completed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	record, _, getErr := store.Get("instance")
	if getErr != nil {
		t.Fatal(getErr)
	}
	sess, ok := record.FindSession("auth")
	if !ok || sess.Retirement == nil || sess.Retirement.Effects["remote-checkout"].Status == state.EffectDone {
		t.Fatalf("forced offline retirement forgot remote obligation: %+v", sess)
	}
}

// A retry after git has already dropped the worktree registration -- the
// removal got as far as unregistering and then stopped on a file it could not
// delete -- must finish the job instead of failing on "is not a working tree"
// forever while the effect stays intended.
func TestRemoveLocalWorktreeFinishesAnAlreadyUnregisteredCheckout(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)
	if err := excludeWorktreeDir(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	local := LocalWorktreePath(repo, "auth")
	mustGit(t, repo, "worktree", "add", "--quiet", "-B", SessionBranch("auth"), local)
	writeAndCommit(t, local, "leftover.txt", "x", "work")
	// The state git leaves behind: directory present, registration gone.
	if err := os.RemoveAll(filepath.Join(repo, ".git", "worktrees")); err != nil {
		t.Fatal(err)
	}
	sess := state.Session{Name: "auth", LocalRepo: repo, Retirement: &state.Retirement{Operation: state.RetirementDelete, Force: true}}

	if err := removeLocalWorktree(context.Background(), sess); err != nil {
		t.Fatalf("removeLocalWorktree() = %v, want the leftover directory removed", err)
	}
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Errorf("the checkout directory is still there: %v", err)
	}
}
