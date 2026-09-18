package lifecycle

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionBranch_IsNamespaced(t *testing.T) {
	if got := SessionBranch("auth-refactor"); got != "bivouac/auth-refactor" {
		t.Errorf("SessionBranch() = %q, want bivouac/auth-refactor", got)
	}
}

func TestSessionDir_IsTheRepoPathsParent(t *testing.T) {
	got := SessionDir("devuser", "auth-refactor")
	want := "/home/devuser/sessions/auth-refactor"
	if got != want {
		t.Errorf("SessionDir() = %q, want %q", got, want)
	}
	if RemoteRepoPath("devuser", "auth-refactor", "bivouac") != got+"/bivouac" {
		t.Errorf("RemoteRepoPath() and SessionDir() disagree; they must derive from the same path")
	}
}

func TestRemoteRepoPath_IsPerSession(t *testing.T) {
	got := RemoteRepoPath("devuser", "auth-refactor", "bivouac")
	want := "/home/devuser/sessions/auth-refactor/bivouac"
	if got != want {
		t.Errorf("RemoteRepoPath() = %q, want %q", got, want)
	}
}

// The local worktree must live inside the user's repository: sandboxing
// tools scope themselves to the project directory and cannot reach a
// worktree created anywhere else, the home directory included.
func TestLocalWorktreePath_IsInsideTheProject(t *testing.T) {
	repo := filepath.Join("Users", "dev", "src", "bivouac")
	got := LocalWorktreePath(repo, "auth-refactor")
	want := filepath.Join(repo, ".worktrees", "auth-refactor")
	if got != want {
		t.Errorf("LocalWorktreePath() = %q, want %q", got, want)
	}
}

func TestLocalWorktreePath_StaysUnderTheRepoForEverySession(t *testing.T) {
	repo := filepath.Join("Users", "dev", "src", "bivouac")
	for _, session := range []string{"auth", "docs", "a-b-c"} {
		got := LocalWorktreePath(repo, session)
		rel, err := filepath.Rel(repo, got)
		if err != nil {
			t.Fatalf("Rel(%q, %q) error = %v", repo, got, err)
		}
		if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Errorf("session %q escaped the repo: %q", session, got)
		}
	}
}
