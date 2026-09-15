package agentcontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRemote is an in-memory RemoteFiles. The SSH client is the one thing
// no in-process harness here can stand in for, so everything above it is
// given nothing to decide.
type fakeRemote struct {
	files map[string]string
}

func (f *fakeRemote) ReadFile(path string) (string, error) {
	return f.files[path], nil
}

func (f *fakeRemote) WriteFile(path, content string) error {
	if f.files == nil {
		f.files = map[string]string{}
	}
	f.files[path] = content
	return nil
}

func TestWrite_WritesOnePerConfiguredHarness(t *testing.T) {
	dir := t.TempDir()
	workflow := filepath.Join(dir, "w.md")
	if err := os.WriteFile(workflow, []byte("# Workflow\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	remote := &fakeRemote{}
	unreachable, err := Write(remote, []string{"claude", "codex"}, []string{workflow})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if len(unreachable) != 0 {
		t.Errorf("unreachable = %v, want none", unreachable)
	}
	for _, p := range []string{".claude/CLAUDE.md", ".codex/AGENTS.md"} {
		if !strings.Contains(remote.files[p], "# Workflow") {
			t.Errorf("%s did not get the workflow:\n%s", p, remote.files[p])
		}
		if !strings.Contains(remote.files[p], "Commits are what survive") {
			t.Errorf("%s did not get the session facts", p)
		}
	}
}

// reconcile writes on every up and provision, and session start writes
// again. Twice must equal once.
func TestWrite_IsIdempotent(t *testing.T) {
	remote := &fakeRemote{}
	if _, err := Write(remote, []string{"claude"}, nil); err != nil {
		t.Fatal(err)
	}
	once := remote.files[".claude/CLAUDE.md"]
	if _, err := Write(remote, []string{"claude"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := remote.files[".claude/CLAUDE.md"]; got != once {
		t.Errorf("second write differed:\nfirst:\n%s\nsecond:\n%s", once, got)
	}
}

func TestWrite_PreservesContentTheUserPutThere(t *testing.T) {
	remote := &fakeRemote{files: map[string]string{
		".claude/CLAUDE.md": "# Mine\nkeep me\n",
	}}
	if _, err := Write(remote, []string{"claude"}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(remote.files[".claude/CLAUDE.md"], "keep me") {
		t.Errorf("user content lost:\n%s", remote.files[".claude/CLAUDE.md"])
	}
}

func TestWrite_ReportsUnreachableHarnesses(t *testing.T) {
	remote := &fakeRemote{}
	unreachable, err := Write(remote, []string{"cursor"}, nil)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if len(unreachable) != 1 || unreachable[0] != "cursor" {
		t.Errorf("unreachable = %v, want [cursor]", unreachable)
	}
}
