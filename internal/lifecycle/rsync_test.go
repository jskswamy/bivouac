package lifecycle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/provider"
)

func TestRsyncPushArgs_BuildsExpectedCommand(t *testing.T) {
	got := rsyncPushArgs("203.0.113.5", "devuser", "/home/user/myrepo", "~/myrepo", nil)
	want := []string{"-az", "--info=progress2", "--mkpath", "--exclude=.git", "-e", "ssh", "/home/user/myrepo/", "devuser@203.0.113.5:~/myrepo/"}
	if len(got) != len(want) {
		t.Fatalf("rsyncPushArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rsyncPushArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRsyncPushArgs_IncludesGivenExcludes(t *testing.T) {
	got := rsyncPushArgs("203.0.113.5", "devuser", "/home/user/myrepo", "~/myrepo", []string{".gocache/", ".envrc"})
	want := []string{"-az", "--info=progress2", "--mkpath", "--exclude=.git", "--exclude=.gocache/", "--exclude=.envrc", "-e", "ssh", "/home/user/myrepo/", "devuser@203.0.113.5:~/myrepo/"}
	if len(got) != len(want) {
		t.Fatalf("rsyncPushArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rsyncPushArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRsyncPullArgs_BuildsExpectedCommand(t *testing.T) {
	got := rsyncPullArgs("203.0.113.5", "devuser", "~/results", "/home/user/results")
	want := []string{"-az", "--info=progress2", "-e", "ssh", "devuser@203.0.113.5:~/results/", "/home/user/results/"}
	if len(got) != len(want) {
		t.Fatalf("rsyncPullArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rsyncPullArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPush_FailureIncludesRsyncOutputInError(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not on PATH")
	}

	// A local dir that doesn't exist makes rsync fail immediately
	// (no network needed) while still writing a real error to stderr,
	// proving Push still captures output for the error message even
	// though it's now also streamed live to the terminal.
	err := Push(context.Background(), "127.0.0.1", "devuser", "/nonexistent/does-not-exist", "~/dest")
	if err == nil {
		t.Fatal("Push() error = nil, want an error for a nonexistent local dir")
	}
	if !strings.Contains(err.Error(), "rsync to 127.0.0.1 failed") {
		t.Errorf("error = %q, want it to name the failure", err.Error())
	}
	if !strings.Contains(err.Error(), "create it and chown it to devuser") {
		t.Errorf("error = %q, want the create-and-chown hint", err.Error())
	}
}

// gitInit makes dir a real git repo so gitIgnoredExcludes has
// something to query -- `git ls-files --others --ignored` requires an
// actual repository, not just a file named .gitignore lying around.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "test"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestGitIgnoredExcludes_ListsIgnoredPathsAsDirectories(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	dir := t.TempDir()
	gitInit(t, dir)
	write := func(rel, content string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "ignored.txt\n/.gocache/\n")
	write("tracked.txt", "keep me")
	write("ignored.txt", "drop me")
	write(".gocache/nested/deep.bin", "drop me too")

	got := gitIgnoredExcludes(dir)

	want := map[string]bool{"ignored.txt": true, ".gocache/": true}
	if len(got) != len(want) {
		t.Fatalf("gitIgnoredExcludes() = %v, want entries for %v", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("gitIgnoredExcludes() contains unexpected entry %q", g)
		}
	}
}

func TestGitIgnoredExcludes_NonGitDir_ReturnsNil(t *testing.T) {
	if got := gitIgnoredExcludes(t.TempDir()); got != nil {
		t.Errorf("gitIgnoredExcludes() on a non-git dir = %v, want nil", got)
	}
}

func TestPush_RespectsGitignoreAndExcludesGitDir(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not on PATH")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	src := t.TempDir()
	gitInit(t, src)
	write := func(rel, content string) {
		full := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "ignored.txt\n/.gocache/\n")
	write("tracked.txt", "keep me")
	write("ignored.txt", "drop me")
	write(".gocache/nested/deep.bin", "drop me too")

	dst := t.TempDir()

	// Exercises the real argv rsyncPushArgs builds (via the same
	// gitIgnoredExcludes computation Push uses), locally -- bypassing
	// -e ssh/the remote target, same pattern as
	// TestRsync_CopiesFilesBetweenLocalDirs -- to prove the exclusion
	// actually works end to end, not just that the argv contains flags.
	full := rsyncPushArgs("unused", "unused", src, "unused", gitIgnoredExcludes(src))
	flags := full[:len(full)-4] // drop the trailing "-e", "ssh", <src>, <remote dst>
	args := append(append([]string{}, flags...), src+"/", dst+"/")
	cmd := exec.CommandContext(context.Background(), "rsync", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rsync error = %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(dst, "tracked.txt")); err != nil {
		t.Errorf("tracked.txt was not synced: %v", err)
	}
	for _, excluded := range []string{"ignored.txt", ".gocache", ".git"} {
		if _, err := os.Stat(filepath.Join(dst, excluded)); !os.IsNotExist(err) {
			t.Errorf("%s should have been excluded, but exists at dst (err = %v)", excluded, err)
		}
	}
}

func TestRsync_ReportsProgressBeforeSyncing(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not on PATH")
	}

	var got []string
	ctx := provider.WithProgress(context.Background(), func(status string) { got = append(got, status) })

	// The rsync itself fails fast (no such local dir, no network
	// needed) -- progress is reported before that attempt regardless.
	_ = Rsync(ctx, "127.0.0.1", "devuser", "/nonexistent/does-not-exist", "/home/devuser/myrepo")

	if len(got) == 0 || !strings.Contains(got[0], "repo") {
		t.Errorf("progress = %v, want a first entry mentioning repo", got)
	}
}

func TestRsync_CopiesFilesBetweenLocalDirs(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not on PATH")
	}

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()

	// Exercises real rsync directly between two local paths -- proves
	// the underlying sync mechanic works. Rsync's own -e ssh/remote
	// argument form is covered separately by
	// TestRsyncArgs_BuildsExpectedCommand, since a real SSH-backed
	// transfer would need a real sshd to test end-to-end.
	cmd := exec.CommandContext(context.Background(), "rsync", "-az", src+"/", dst+"/")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rsync error = %v\n%s", err, out)
	}

	got, err := os.ReadFile(filepath.Join(dst, "hello.txt"))
	if err != nil {
		t.Fatalf("reading synced file: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("synced content = %q, want %q", got, "hello")
	}
}
