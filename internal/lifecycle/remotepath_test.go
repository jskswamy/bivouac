package lifecycle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemotePath_UnderHome_Mirrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	local := filepath.Join(home, "source", "github.com", "jskswamy", "cloudlab")

	got, err := RemotePath(local, "devuser")
	if err != nil {
		t.Fatalf("RemotePath() error = %v", err)
	}
	want := "/home/devuser/source/github.com/jskswamy/cloudlab"
	if got != want {
		t.Errorf("RemotePath() = %q, want %q", got, want)
	}
}

func TestRemotePath_ExactlyHome_ReturnsRemoteHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := RemotePath(home, "devuser")
	if err != nil {
		t.Fatalf("RemotePath() error = %v", err)
	}
	if got != "/home/devuser" {
		t.Errorf("RemotePath() = %q, want %q", got, "/home/devuser")
	}
}

func TestRemotePath_NotUnderHome_UsesAsIs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	local := filepath.Join(t.TempDir(), "elsewhere")

	got, err := RemotePath(local, "devuser")
	if err != nil {
		t.Fatalf("RemotePath() error = %v", err)
	}
	if got != local {
		t.Errorf("RemotePath() = %q, want %q (used as-is)", got, local)
	}
}

func TestRemotePath_SiblingWithSharedPrefix_NotTreatedAsUnderHome(t *testing.T) {
	// A directory like /tmp/xyz/home2/project must NOT be treated as
	// "under" /tmp/xyz/home just because it shares a string prefix --
	// proves the containment check is separator-bounded, not a naive
	// strings.HasPrefix.
	parent := t.TempDir()
	home := filepath.Join(parent, "home")
	sibling := filepath.Join(parent, "home2", "project")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	got, err := RemotePath(sibling, "devuser")
	if err != nil {
		t.Fatalf("RemotePath() error = %v", err)
	}
	if got != sibling {
		t.Errorf("RemotePath() = %q, want %q (sibling dir sharing a string prefix must not be mirrored)", got, sibling)
	}
}
