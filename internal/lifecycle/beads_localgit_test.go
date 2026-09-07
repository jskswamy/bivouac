package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExcludeBeadsCmd_KeepsTheDatabaseOutOfTheCheckpoint is the guard against
// the design's sharpest failure mode: checkpointCmd runs `git add -A`, bd
// writes a multi-megabyte .beads/embeddeddolt into the checkout, and
// .git/info/exclude does not travel over `git push` -- so the instance's
// repository has none of the Mac's exclusions unless cloudlab writes them.
func TestExcludeBeadsCmd_KeepsTheDatabaseOutOfTheCheckpoint(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)
	before := gitOut(t, repo, "rev-parse", "HEAD")

	if out, err := runShell(t, excludeBeadsCmd(repo)); err != nil {
		t.Fatalf("excludeBeadsCmd: %v\n%s", err, out)
	}

	// Stand in for what `bd init` leaves behind.
	db := filepath.Join(repo, ".beads", "embeddeddolt")
	if err := os.MkdirAll(db, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(db, "data"), []byte("dolt"), 0o600); err != nil {
		t.Fatal(err)
	}

	if out, err := runShell(t, checkpointCmd(repo, "cloudlab: checkpoint s")); err != nil {
		t.Fatalf("checkpointCmd: %v\n%s", err, out)
	}

	if after := gitOut(t, repo, "rev-parse", "HEAD"); after != before {
		files := gitOut(t, repo, "show", "--name-only", "--pretty=format:", "HEAD")
		t.Fatalf("the checkpoint committed the beads database:\n%s", files)
	}
}

// Idempotent: session start is retry-safe, so this runs again on every retry
// and must not accumulate entries.
func TestExcludeBeadsCmd_IsIdempotent(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)

	for range 3 {
		if out, err := runShell(t, excludeBeadsCmd(repo)); err != nil {
			t.Fatalf("excludeBeadsCmd: %v\n%s", err, out)
		}
	}

	content, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(content), beadsDirPattern); n != 1 {
		t.Errorf("exclude file has %d copies of %q, want 1:\n%s", n, beadsDirPattern, content)
	}
}

// A repository that never had .git/info/exclude, or never had the info
// directory at all, must still get the exclusion.
func TestExcludeBeadsCmd_CreatesTheExcludeFileWhenAbsent(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)
	if err := os.RemoveAll(filepath.Join(repo, ".git", "info")); err != nil {
		t.Fatal(err)
	}

	if out, err := runShell(t, excludeBeadsCmd(repo)); err != nil {
		t.Fatalf("excludeBeadsCmd: %v\n%s", err, out)
	}

	content, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("exclude file was not created: %v", err)
	}
	if !strings.Contains(string(content), beadsDirPattern) {
		t.Errorf("exclude file lacks %q:\n%s", beadsDirPattern, content)
	}
}
