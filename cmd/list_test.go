package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/state"
)

// errWriter always fails, so tests can assert that a write failure
// while printing list output is propagated as an error, not swallowed.
type errWriter struct{}

func (errWriter) Write(p []byte) (int, error) {
	return 0, errors.New("write failed")
}

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

func TestListCommand_NoInstances_WriteErrorPropagates(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	root := newRootCmd()
	root.SetArgs([]string{"list"})
	root.SetOut(errWriter{})
	root.SetErr(errWriter{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want error from failed write")
	}
}

func TestListCommand_WithInstances_WriteErrorPropagates(t *testing.T) {
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
	root.SetOut(errWriter{})
	root.SetErr(errWriter{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want error from failed write")
	}
}
