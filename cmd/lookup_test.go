package cmd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestLookupCommands_NameFlagResolves(t *testing.T) {
	cases := []struct {
		args []string
		verb string
		want string
	}{
		{[]string{"shell", "--name", "myrepo"}, "shell", "shell: not implemented yet"},
		{[]string{"ssh", "--name", "myrepo"}, "ssh", `no instance named "myrepo"`},
		{[]string{"watch", "--name", "myrepo"}, "watch", `no instance named "myrepo"`},
		{[]string{"connect", "--name", "myrepo"}, "connect", "connect: not implemented yet"},
		{[]string{"status", "--name", "myrepo"}, "status", `no instance named "myrepo"`},
		{[]string{"down", "--name", "myrepo"}, "down", `no instance named "myrepo"`},
		{[]string{"sync", "./data", "--name", "myrepo"}, "sync", `no instance named "myrepo"`},
		{[]string{"download", "./results", "--name", "myrepo"}, "download", `no instance named "myrepo"`},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
			root := newRootCmd()
			root.SetArgs(tc.args)
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)

			err := root.Execute()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestLookupCommands_NameFlagWinsOverLeadingPathArg(t *testing.T) {
	cases := []struct {
		args []string
		verb string
		want string
	}{
		{[]string{"sync", "./data", "--name", "myrepo"}, "sync", `no instance named "myrepo"`},
		{[]string{"download", "./results", "--name", "myrepo"}, "download", `no instance named "myrepo"`},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
			root := newRootCmd()
			root.SetArgs(tc.args)
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)

			err := root.Execute()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestLookupCommands_PositionalNameResolves(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	root := newRootCmd()
	root.SetArgs([]string{"ssh", "myrepo"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `no instance named "myrepo"`) {
		t.Errorf("error = %q, want it to name instance %q", err.Error(), "myrepo")
	}
}

func TestLookupCommands_ErrorOutsideRepoNoName(t *testing.T) {
	chdir(t, t.TempDir())

	root := newRootCmd()
	root.SetArgs([]string{"status"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no instance name given") {
		t.Errorf("error = %q, want mention of 'no instance name given'", err.Error())
	}
}
