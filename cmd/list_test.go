package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/state"
)

func TestListCommand_NoInstances(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	root := newRootCmd()
	root.SetArgs([]string{"list"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "no instances") {
		t.Errorf("output = %q, want mention of 'no instances'", out.String())
	}
}

func TestListCommand_WithInstances(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	store, err := state.Open()
	if err != nil {
		t.Fatalf("state.Open() error = %v", err)
	}
	if err := store.Put(state.Record{Name: "myrepo", Provider: "digitalocean", IP: "1.2.3.4"}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"list"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "myrepo") {
		t.Errorf("output = %q, want it to mention %q", out.String(), "myrepo")
	}
}
