package lifecycle

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// fakeAttach answers both halves attachMachine drives: `herdr machine
// list`/`add` (unrouted, EnsureMachine's own commands) and the
// `--machine`-routed workspace/tab calls that follow. A single fake for
// both, since attachMachine's own tests are about what it does once a
// machine already exists, not about EnsureMachine's own behaviour --
// covered separately in herdrmachine_test.go.
type fakeAttach struct {
	machines string // `herdr machine list --json` reply
	fail     string // routed verb to fail, e.g. "workspace list"
	calls    [][]string
}

func (f *fakeAttach) Run(args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if args[0] == "--machine" {
		rest := args[2:]
		verb := rest[0] + " " + rest[1]
		if verb == f.fail {
			return "boom", fmt.Errorf("herdr %s failed", verb)
		}
		if verb == "workspace list" {
			return `{"result":{"workspaces":[{"workspace_id":"w1","label":"auth","pane_count":1}]}}`, nil
		}
		return "", nil
	}
	if len(args) > 1 && args[1] == "list" {
		return f.machines, nil
	}
	return "", nil
}

// A profile already saved and enabled for this target and session, so
// EnsureMachine returns without an add -- what attachMachine sees once
// AttachMachine's own AGENTS.md-level concerns (session name, herdr on
// PATH) are already satisfied.
const existingMachine = `[{"id":"mid","label":"auth","target":"ssh://u@1.2.3.4","session":"auth","enabled":true}]`

// The machine step succeeding must not be silent about a later step
// failing: the sidebar entry exists, and the user needs to know that,
// spelled with the label they will see rather than the profile id.
func TestAttachMachine_NamesTheSidebarLabelWhenWorkspaceFails(t *testing.T) {
	h := &fakeAttach{machines: existingMachine, fail: "workspace list"}

	_, label, err := attachMachine(context.Background(), h, "inst", "1.2.3.4", "u", "ssh://u@1.2.3.4", "auth", "/root", nil, nil)
	if label != "auth" {
		t.Fatalf("label = %q, want auth even though workspace failed", label)
	}
	if err == nil {
		t.Fatal("attachMachine() error = nil when the workspace step failed")
	}
	if !strings.Contains(err.Error(), "auth") || !strings.Contains(err.Error(), "saved in your herdr sidebar") {
		t.Errorf("error = %q, want it to name the auth sidebar entry", err)
	}
}

func TestAttachMachine_NamesTheSidebarLabelWhenFocusFails(t *testing.T) {
	h := &fakeAttach{machines: existingMachine, fail: "workspace focus"}

	_, label, err := attachMachine(context.Background(), h, "inst", "1.2.3.4", "u", "ssh://u@1.2.3.4", "auth", "/root", nil, nil)
	if label != "auth" {
		t.Fatalf("label = %q, want auth even though focus failed", label)
	}
	if err == nil {
		t.Fatal("attachMachine() error = nil when the focus step failed")
	}
	if !strings.Contains(err.Error(), "auth") || !strings.Contains(err.Error(), "saved in your herdr sidebar") {
		t.Errorf("error = %q, want it to name the auth sidebar entry", err)
	}
}

// The machine step itself failing has nowhere to record a sidebar entry --
// there isn't one -- so it must stay unwrapped, plain from EnsureMachine.
func TestAttachMachine_LeavesTheMachineStepErrorUnwrapped(t *testing.T) {
	h := &fakeAttach{machines: "not json"}

	_, _, err := attachMachine(context.Background(), h, "inst", "1.2.3.4", "u", "ssh://u@1.2.3.4", "auth", "/root", nil, nil)
	if err == nil {
		t.Fatal("attachMachine() error = nil when listing machines failed")
	}
	if strings.Contains(err.Error(), "saved in your herdr sidebar") {
		t.Errorf("error = %q, want the machine step's own error, not the sidebar wrapper", err)
	}
}
