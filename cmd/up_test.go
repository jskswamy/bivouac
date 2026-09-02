package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// chdir switches the process's working directory to dir for the
// duration of the test, restoring it afterward.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// initTestRepo creates a fresh git repo in a temp dir and returns its path.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
	return dir
}

func TestUpCommand_NotInRepoErrors(t *testing.T) {
	chdir(t, t.TempDir())

	root := newRootCmd()
	root.SetArgs([]string{"up"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "use --repo") {
		t.Errorf("error = %q, want mention of --repo", err.Error())
	}
}

func TestUpCommand_MissingTokenErrors(t *testing.T) {
	chdir(t, initTestRepo(t))
	t.Setenv("DIGITALOCEAN_TOKEN", "")

	root := newRootCmd()
	root.SetArgs([]string{"up"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "DIGITALOCEAN_TOKEN") {
		t.Errorf("error = %q, want mention of DIGITALOCEAN_TOKEN", err.Error())
	}
}

func TestUpCommand_PositionalNameOverridesDerivedName(t *testing.T) {
	chdir(t, initTestRepo(t))
	t.Setenv("DIGITALOCEAN_TOKEN", "")

	root := newRootCmd()
	root.SetArgs([]string{"up", "somename"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `instance "somename"`) {
		t.Errorf("error = %q, want it to name instance %q, not the derived repo name", err.Error(), "somename")
	}
}
