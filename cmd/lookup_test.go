package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestLookupCommands_NameFlagResolves(t *testing.T) {
	cases := []struct {
		args []string
		verb string
	}{
		{[]string{"shell", "--name", "myrepo"}, "shell"},
		{[]string{"ssh", "--name", "myrepo"}, "ssh"},
		{[]string{"watch", "--name", "myrepo"}, "watch"},
		{[]string{"connect", "--name", "myrepo"}, "connect"},
		{[]string{"status", "--name", "myrepo"}, "status"},
		{[]string{"down", "--name", "myrepo"}, "down"},
		{[]string{"sync", "./data", "--name", "myrepo"}, "sync"},
		{[]string{"download", "./results", "--name", "myrepo"}, "download"},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			root := newRootCmd()
			root.SetArgs(tc.args)
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)

			err := root.Execute()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			want := tc.verb + ": not implemented yet"
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), want)
			}
		})
	}
}

func TestLookupCommands_NameFlagWinsOverLeadingPathArg(t *testing.T) {
	cases := []struct {
		args []string
		verb string
	}{
		{[]string{"sync", "./data", "--name", "myrepo"}, "sync"},
		{[]string{"download", "./results", "--name", "myrepo"}, "download"},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			root := newRootCmd()
			root.SetArgs(tc.args)
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)

			err := root.Execute()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), `instance "myrepo"`) {
				t.Errorf("error = %q, want it to name instance %q (from --name, not the leading path arg)", err.Error(), "myrepo")
			}
		})
	}
}

func TestLookupCommands_PositionalNameResolves(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"ssh", "myrepo"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `instance "myrepo"`) {
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
