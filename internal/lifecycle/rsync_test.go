package lifecycle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRsyncPushArgs_BuildsExpectedCommand(t *testing.T) {
	got := rsyncPushArgs("203.0.113.5", "/home/user/myrepo", "~/myrepo")
	want := []string{"-az", "-e", "ssh", "/home/user/myrepo/", "root@203.0.113.5:~/myrepo/"}
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
	got := rsyncPullArgs("203.0.113.5", "~/results", "/home/user/results")
	want := []string{"-az", "-e", "ssh", "root@203.0.113.5:~/results/", "/home/user/results/"}
	if len(got) != len(want) {
		t.Fatalf("rsyncPullArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rsyncPullArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
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
