package identity

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func resolved(t *testing.T, dir string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) error = %v", dir, err)
	}
	return p
}

func TestRepoRoot_FromRootDir(t *testing.T) {
	root := initRepo(t)
	got, err := RepoRoot(root, "")
	if err != nil {
		t.Fatalf("RepoRoot() error = %v", err)
	}
	if want := resolved(t, root); got != want {
		t.Errorf("RepoRoot() = %q, want %q", got, want)
	}
}

func TestRepoRoot_FromSubdir(t *testing.T) {
	root := initRepo(t)
	sub := filepath.Join(root, "src", "utils")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := RepoRoot(sub, "")
	if err != nil {
		t.Fatalf("RepoRoot() error = %v", err)
	}
	if want := resolved(t, root); got != want {
		t.Errorf("RepoRoot() = %q, want %q", got, want)
	}
}

func TestRepoRoot_RepoFlagOverridesCwd(t *testing.T) {
	other := initRepo(t)
	notARepo := t.TempDir()
	got, err := RepoRoot(notARepo, other)
	if err != nil {
		t.Fatalf("RepoRoot() error = %v", err)
	}
	if want := resolved(t, other); got != want {
		t.Errorf("RepoRoot() = %q, want %q", got, want)
	}
}

func TestRepoRoot_RepoFlagWalksUpFromSubdir(t *testing.T) {
	other := initRepo(t)
	sub := filepath.Join(other, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := RepoRoot(t.TempDir(), sub)
	if err != nil {
		t.Fatalf("RepoRoot() error = %v", err)
	}
	if want := resolved(t, other); got != want {
		t.Errorf("RepoRoot() = %q, want %q", got, want)
	}
}

func TestRepoRoot_NotAGitRepoNoFlag(t *testing.T) {
	_, err := RepoRoot(t.TempDir(), "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "use --repo") {
		t.Errorf("error = %q, want mention of --repo", err.Error())
	}
}

func TestRepoRoot_NotAGitRepoWithFlag(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "nope")
	_, err := RepoRoot(t.TempDir(), bad)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), bad) {
		t.Errorf("error = %q, want it to name %q", err.Error(), bad)
	}
}
