package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/state"
)

func TestDownSummary_WarnsDestructionIsUnrecoverable(t *testing.T) {
	record := state.Record{
		Name: "myrepo", Provider: "digitalocean", Region: "nyc3",
		Size: "s-1vcpu-1gb", Template: "python", IP: "203.0.113.5",
	}

	got := downSummary(record)

	for _, want := range []string{`"myrepo"`, "nyc3", "s-1vcpu-1gb", "python", "203.0.113.5", "cannot be undone", "unsaved work", "Proceed?"} {
		if !strings.Contains(got, want) {
			t.Errorf("downSummary() = %q, want it to contain %q", got, want)
		}
	}
}

func TestSyncLocalDir_UsesDirFlagOrCwd(t *testing.T) {
	if got, err := syncLocalDir("/explicit/path"); err != nil || got != "/explicit/path" {
		t.Errorf("syncLocalDir(%q) = (%q, %v), want (%q, nil)", "/explicit/path", got, err, "/explicit/path")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := syncLocalDir(""); err != nil || got != cwd {
		t.Errorf("syncLocalDir(\"\") = (%q, %v), want (%q, nil)", got, err, cwd)
	}
}

func TestSyncRemoteDir_UsesFirstArgOrRemotePath(t *testing.T) {
	if got, err := syncRemoteDir([]string{"~/custom"}, "/whatever", "devuser"); err != nil || got != "~/custom" {
		t.Errorf("syncRemoteDir([\"~/custom\"], ...) = (%q, %v), want (%q, nil)", got, err, "~/custom")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	local := filepath.Join(home, "project")
	got, err := syncRemoteDir(nil, local, "devuser")
	if err != nil {
		t.Fatalf("syncRemoteDir(nil, ...) error = %v", err)
	}
	want := "/home/devuser/project"
	if got != want {
		t.Errorf("syncRemoteDir(nil, ...) = %q, want %q", got, want)
	}
}

func TestDefaultLocalDir_UsesBasenameInCwd(t *testing.T) {
	cases := map[string]string{
		"~/results":       "./results",
		"/root/dataset":   "./dataset",
		"~/nested/output": "./output",
	}
	for remote, want := range cases {
		if got := defaultLocalDir(remote); got != want {
			t.Errorf("defaultLocalDir(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestTmuxSession_UsesFirstArgOrDefault(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{nil, "main"},
		{[]string{"myrepo"}, "myrepo"},
	}
	for _, tc := range cases {
		if got := tmuxSession(tc.args); got != tc.want {
			t.Errorf("tmuxSession(%v) = %q, want %q", tc.args, got, tc.want)
		}
	}
}
