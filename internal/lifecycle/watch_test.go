package lifecycle

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestMutagenCreateArgs_BuildsExpectedCommand(t *testing.T) {
	got := mutagenCreateArgs("203.0.113.5", "myrepo", "/home/user/myrepo")
	want := []string{"sync", "create", "--name=myrepo", "/home/user/myrepo", "root@203.0.113.5:~/myrepo"}
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
