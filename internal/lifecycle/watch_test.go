package lifecycle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/cloudlab/internal/provider"
)

func TestMutagenCreateArgs_BuildsExpectedCommand(t *testing.T) {
	got := mutagenCreateArgs("203.0.113.5", "devuser", "myrepo", "/home/user/myrepo")
	want := []string{"sync", "create", "--name=myrepo", "/home/user/myrepo", "devuser@203.0.113.5:~/myrepo"}
	if len(got) != len(want) {
		t.Fatalf("mutagenCreateArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mutagenCreateArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMutagenSync_RealLocalToLocalSession(t *testing.T) {
	if _, err := exec.LookPath("mutagen"); err != nil {
		t.Skip("mutagen not on PATH")
	}

	t.Setenv("MUTAGEN_DATA_DIRECTORY", t.TempDir())

	src := t.TempDir()
	dst := t.TempDir()
	name := "cloudlab-test-session"

	create := exec.Command("mutagen", "sync", "create", "--name="+name, src, dst)
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("mutagen sync create error = %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("mutagen", "sync", "terminate", name).Run()
		_ = exec.Command("mutagen", "daemon", "stop").Run()
	})

	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join(dst, "hello.txt"))
		if err == nil {
			if string(data) != "hello" {
				t.Fatalf("synced content = %q, want %q", data, "hello")
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("file never synced to destination within 10s")
}

func TestStartWatch_ReportsProgressBeforeStarting(t *testing.T) {
	var got []string
	ctx := provider.WithProgress(context.Background(), func(status string) { got = append(got, status) })

	// mutagen need not be on PATH: progress is reported before
	// exec.LookPath, so this assertion holds regardless.
	_ = StartWatch(ctx, "203.0.113.5", "devuser", "myrepo", t.TempDir())

	if len(got) == 0 || !strings.Contains(got[0], "watch") {
		t.Errorf("progress = %v, want a first entry mentioning watch", got)
	}
}

func TestStartWatch_TerminatesExistingSessionFirst(t *testing.T) {
	if _, err := exec.LookPath("mutagen"); err != nil {
		t.Skip("mutagen not on PATH")
	}

	t.Setenv("MUTAGEN_DATA_DIRECTORY", t.TempDir())

	src := t.TempDir()
	dst := t.TempDir()
	name := "cloudlab-restart-test"

	// Seed a pre-existing session under name via a real local-to-local
	// sync (guaranteed to succeed, no sshd required) -- mirrors "a
	// watch session already exists" the way `watch` needs to restart it.
	create := exec.Command("mutagen", "sync", "create", "--name="+name, src, dst)
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("seeding existing session: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("mutagen", "sync", "terminate", name).Run()
		_ = exec.Command("mutagen", "daemon", "stop").Run()
	})

	// StartWatch's remote target (root@127.0.0.1) has no reachable
	// sshd in a test environment, so this call is expected to fail on
	// the SSH connection -- but that failure must happen AFTER the
	// pre-existing session was already terminated. A failed create
	// leaves no new session behind (confirmed empirically), so zero
	// remaining sessions named name proves the old one is gone.
	_ = StartWatch(context.Background(), "127.0.0.1", "devuser", name, src)

	out, err := exec.Command("mutagen", "sync", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("mutagen sync list: %v\n%s", err, out)
	}
	if strings.Count(string(out), "Name: "+name) != 0 {
		t.Errorf("session %q still present after StartWatch -- terminateWatch did not run before create", name)
	}
}
