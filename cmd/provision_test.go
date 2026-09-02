package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestProvisionCommand_NotInRepoErrors(t *testing.T) {
	chdir(t, t.TempDir())

	root := newRootCmd()
	root.SetArgs([]string{"provision"})
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

func TestProvisionCommand_InstanceNotFoundErrors(t *testing.T) {
	chdir(t, initTestRepo(t))
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	root := newRootCmd()
	root.SetArgs([]string{"provision"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err.Error())
	}
}

func TestProvisionCommand_PositionalNameOverridesDerivedName(t *testing.T) {
	chdir(t, initTestRepo(t))
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	root := newRootCmd()
	root.SetArgs([]string{"provision", "somename"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `"somename"`) {
		t.Errorf("error = %q, want it to name instance %q, not the derived repo name", err.Error(), "somename")
	}
}
