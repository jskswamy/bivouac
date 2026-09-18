package lifecycle

import (
	"fmt"
	"strings"
	"testing"
)

// fakeRouted answers herdr commands routed to a saved machine, so the
// workspace and layout flows can run without herdr.
//
// Replies are queued per verb ("workspace list", "pane split") and the last
// one repeats, which is how a listing can differ before and after a create.
type fakeRouted struct {
	replies  map[string][]string
	fail     string     // verb to fail, e.g. "pane split"; empty means never
	calls    [][]string // argv after the --machine prefix
	machines []string   // the selector each call was routed to
}

func (f *fakeRouted) Run(args ...string) (string, error) {
	if len(args) < 4 || args[0] != "--machine" {
		return "", fmt.Errorf("not routed through --machine: %v", args)
	}
	f.machines = append(f.machines, args[1])
	rest := args[2:]
	f.calls = append(f.calls, rest)
	verb := rest[0] + " " + rest[1]
	if verb == f.fail {
		return "boom", fmt.Errorf("herdr %s failed", verb)
	}
	queue := f.replies[verb]
	if len(queue) == 0 {
		return "", nil
	}
	if len(queue) > 1 {
		f.replies[verb] = queue[1:]
	}
	return queue[0], nil
}

// ran returns every call of verb, each without the verb itself.
func (f *fakeRouted) ran(verb string) [][]string {
	var out [][]string
	for _, c := range f.calls {
		if c[0]+" "+c[1] == verb {
			out = append(out, c[2:])
		}
	}
	return out
}

// Every routed command names the machine and never a session: herdr's docs
// forbid combining them, since the profile already carries its session.
func TestWorkspaceArgs_RouteThroughTheMachineNotASession(t *testing.T) {
	for name, got := range map[string][]string{
		"list":   workspaceListArgs("mid"),
		"create": workspaceCreateArgs("mid", "/home/u/sessions/auth/repo", "auth"),
		"focus":  workspaceFocusArgs("mid", "w2"),
		"close":  workspaceCloseArgs("mid", "w1"),
	} {
		if len(got) < 2 || got[0] != "--machine" || got[1] != "mid" {
			t.Errorf("%s = %v, want it to start with --machine mid", name, got)
		}
		if strings.Contains(strings.Join(got, " "), "--session") {
			t.Errorf("%s = %v, must not combine --machine with --session", name, got)
		}
	}
}

func TestEnsureWorkspace_CreatesOneRootedInTheCheckout(t *testing.T) {
	h := &fakeRouted{replies: map[string][]string{"workspace list": {
		`{"result":{"workspaces":[{"workspace_id":"w1","label":"~","pane_count":3}]}}`,
		`{"result":{"workspaces":[{"workspace_id":"w1","label":"~","pane_count":3},` +
			`{"workspace_id":"w9","label":"auth","pane_count":1}]}}`,
	}}}

	id, err := EnsureWorkspace(h, "mid", "/home/u/sessions/auth/repo", "auth")
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	if id != "w9" {
		t.Errorf("id = %q, want w9 read back from the listing", id)
	}
	created := h.ran("workspace create")
	if len(created) != 1 {
		t.Fatalf("created %v, want exactly one workspace", created)
	}
	got := strings.Join(created[0], " ")
	for _, want := range []string{"--cwd /home/u/sessions/auth/repo", "--label auth", "--no-focus"} {
		if !strings.Contains(got, want) {
			t.Errorf("create = %q, want %q", got, want)
		}
	}
	for _, m := range h.machines {
		if m != "mid" {
			t.Errorf("routed to %q, want every call on the session's machine", m)
		}
	}
}

// Reconnecting to a session that already has its workspace must reuse it,
// not stack up a new one every time.
func TestEnsureWorkspace_ReusesTheExistingOne(t *testing.T) {
	h := &fakeRouted{replies: map[string][]string{"workspace list": {
		`{"result":{"workspaces":[{"workspace_id":"w1","label":"~","pane_count":1},` +
			`{"workspace_id":"w2","label":"auth","pane_count":1}]}}`,
	}}}

	id, err := EnsureWorkspace(h, "mid", "/home/u/sessions/auth/repo", "auth")
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	if id != "w2" {
		t.Errorf("id = %q, want the existing w2", id)
	}
	if len(h.ran("workspace create")) != 0 {
		t.Error("created a second workspace for a session that already had one")
	}
	// Nor tidy: a reconnect must not close workspaces the user has since made.
	if len(h.ran("workspace close")) != 0 {
		t.Error("closed a workspace on a plain reconnect")
	}
}

// A named session starts with herdr's own "~" workspace; once ours exists
// the empty default is closed, in the run that created ours only.
func TestEnsureWorkspace_ClosesHerdrsEmptyDefault(t *testing.T) {
	h := &fakeRouted{replies: map[string][]string{"workspace list": {
		`{"result":{"workspaces":[{"workspace_id":"w1","label":"~","pane_count":1}]}}`,
		`{"result":{"workspaces":[{"workspace_id":"w1","label":"~","pane_count":1},` +
			`{"workspace_id":"w9","label":"auth","pane_count":1}]}}`,
	}}}

	if _, err := EnsureWorkspace(h, "mid", "/home/u/sessions/auth/repo", "auth"); err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	closed := h.ran("workspace close")
	if len(closed) != 1 || closed[0][0] != "w1" {
		t.Errorf("closed %v, want exactly the empty ~ (w1)", closed)
	}
}

// More than one pane is the cheapest evidence someone is working in it.
func TestEnsureWorkspace_LeavesADefaultThatIsInUse(t *testing.T) {
	h := &fakeRouted{replies: map[string][]string{"workspace list": {
		`{"result":{"workspaces":[{"workspace_id":"w1","label":"~","pane_count":3}]}}`,
		`{"result":{"workspaces":[{"workspace_id":"w1","label":"~","pane_count":3},` +
			`{"workspace_id":"w9","label":"auth","pane_count":1}]}}`,
	}}}

	if _, err := EnsureWorkspace(h, "mid", "/home/u/sessions/auth/repo", "auth"); err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	if len(h.ran("workspace close")) != 0 {
		t.Error("closed a default workspace that had panes in it")
	}
}

// The first routed call is the capability probe: forwarding never falls
// back to a local server, so a failure here means the instance's herdr
// cannot be driven this way, and the fix is to provision it.
func TestEnsureWorkspace_NamesProvisionWhenTheMachineCannotBeDriven(t *testing.T) {
	h := &fakeRouted{fail: "workspace list"}

	_, err := EnsureWorkspace(h, "mid", "/home/u/sessions/auth/repo", "auth")
	if err == nil {
		t.Fatal("EnsureWorkspace() error = nil when the machine could not be reached")
	}
	if !strings.Contains(err.Error(), "bivouac provision") {
		t.Errorf("error = %q, want it to name bivouac provision", err)
	}
	if len(h.ran("workspace create")) != 0 {
		t.Error("went on to create after the probe failed")
	}
}

func TestFocusWorkspace_FocusesByIDThroughTheMachine(t *testing.T) {
	h := &fakeRouted{}
	if err := FocusWorkspace(h, "mid", "w9"); err != nil {
		t.Fatalf("FocusWorkspace() error = %v", err)
	}
	focused := h.ran("workspace focus")
	if len(focused) != 1 || focused[0][0] != "w9" {
		t.Errorf("focused %v, want w9", focused)
	}
}

func TestFocusWorkspace_ReportsAFailure(t *testing.T) {
	h := &fakeRouted{fail: "workspace focus"}
	if err := FocusWorkspace(h, "mid", "w9"); err == nil {
		t.Error("FocusWorkspace() error = nil after focus failed")
	}
}
