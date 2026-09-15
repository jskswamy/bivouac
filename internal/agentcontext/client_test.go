package agentcontext

import (
	"errors"
	"strings"
	"testing"
)

// A file that is not there yet is the ordinary case on a fresh instance,
// and must read as empty rather than as an error -- otherwise the very
// first write fails.
func TestRemote_TreatsAMissingFileAsEmpty(t *testing.T) {
	r := Remote(
		func(cmd string) (string, error) { return "cat: no such file", errors.New("exit status 1") },
		func(path, content string) error { return nil },
		"/home/devuser",
	)
	got, err := r.ReadFile(".claude/CLAUDE.md")
	if err != nil {
		t.Fatalf("ReadFile() error = %v, want nil", err)
	}
	if got != "" {
		t.Errorf("ReadFile() = %q, want empty", got)
	}
}

func TestRemote_ReadsThroughTheRunner(t *testing.T) {
	var asked string
	r := Remote(
		func(cmd string) (string, error) { asked = cmd; return "contents", nil },
		func(path, content string) error { return nil },
		"/home/devuser",
	)
	got, err := r.ReadFile(".codex/AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	if got != "contents" {
		t.Errorf("ReadFile() = %q", got)
	}
	if !strings.Contains(asked, "/home/devuser/.codex/AGENTS.md") {
		t.Errorf("command %q does not name the path", asked)
	}
}

// The home directory is joined here rather than left to the shell: the
// client quotes every path it is given, so a "$HOME/..." would arrive at
// the remote shell as a literal and create a directory called $HOME.
func TestRemote_WritesUnderTheRemoteHome(t *testing.T) {
	var gotPath, gotContent string
	r := Remote(
		func(cmd string) (string, error) { return "", nil },
		func(path, content string) error { gotPath, gotContent = path, content; return nil },
		"/home/devuser",
	)
	if err := r.WriteFile(".config/opencode/AGENTS.md", "block"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/home/devuser/.config/opencode/AGENTS.md" {
		t.Errorf("WriteFile() path = %q", gotPath)
	}
	if gotContent != "block" {
		t.Errorf("WriteFile() content = %q", gotContent)
	}
}
