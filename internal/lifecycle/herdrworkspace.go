package lifecycle

import (
	"encoding/json"
	"fmt"
)

// herdrWorkspace is one entry from `herdr workspace list` on an instance.
//
// The id is herdr's own and is scoped to that server -- the same session is
// w2 on one instance and w5 on another -- so it is read from the listing per
// call and never remembered.
type herdrWorkspace struct {
	ID    string `json:"workspace_id"`
	Label string `json:"label"`
	Panes int    `json:"pane_count"`
}

// parseWorkspaceList reads `herdr workspace list`, which answers over the
// socket API and so wraps its payload in the usual envelope.
func parseWorkspaceList(out string) ([]herdrWorkspace, error) {
	var reply struct {
		Result struct {
			Workspaces []herdrWorkspace `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		return nil, fmt.Errorf("reading the herdr workspace list: %w\n%s", err, out)
	}
	return reply.Result.Workspaces, nil
}

// findWorkspace locates the workspace bivouac created for a session.
//
// By label, because that is the only part bivouac chose. Ids are assigned
// by the instance's own herdr server and mean nothing anywhere else.
func findWorkspace(workspaces []herdrWorkspace, label string) (herdrWorkspace, bool) {
	for _, w := range workspaces {
		if w.Label == label {
			return w, true
		}
	}
	return herdrWorkspace{}, false
}

// herdrDefaultWorkspaceLabel is what herdr calls the workspace it opens for
// a session it has just started: the user's home directory, shown as "~".
const herdrDefaultWorkspaceLabel = "~"

// machineArgs routes a herdr command to a saved machine's server.
//
// herdr 0.9.1's --machine reaches the instance through the profile
// EnsureMachine already saved, so these commands need no SSH connection of
// bivouac's own. The selector is the profile id: herdr also accepts a
// label, but only a unique one, and the user may rename it from the
// sidebar.
//
// Never alongside --session. The profile already carries its session, and
// herdr's docs rule the combination out.
func machineArgs(machine string, args ...string) []string {
	return append([]string{"--machine", machine}, args...)
}

func workspaceListArgs(machine string) []string {
	return machineArgs(machine, "workspace", "list")
}

// workspaceCreateArgs roots a workspace in the session's checkout, which is
// the whole point: attaching then lands in the code rather than in $HOME.
//
// --no-focus because focusing is a separate, later step -- creating a
// workspace should not move anyone who happens to be attached already.
func workspaceCreateArgs(machine, cwd, label string) []string {
	return machineArgs(machine, "workspace", "create", "--cwd", cwd, "--label", label, "--no-focus")
}

func workspaceFocusArgs(machine, id string) []string {
	return machineArgs(machine, "workspace", "focus", id)
}

func workspaceCloseArgs(machine, id string) []string {
	return machineArgs(machine, "workspace", "close", id)
}

// EnsureWorkspace makes sure the session has a workspace on the instance
// rooted in its checkout, and returns that workspace's id.
//
// The listing is also the capability probe. `herdr --machine` refuses
// `status`, so there is no cheaper question to ask first -- and forwarding
// never installs, restarts or falls back to a local server, so asking with
// a real command is safe. A failure here has one of several causes -- an
// old herdr on either end, a session server that was never started or died
// with the instance, or the instance being unreachable at all -- and the
// error lists them rather than pointing at only the one `bivouac provision`
// fixes.
//
// Reused when it already exists. Reconnecting to a session is the common
// case, and creating unconditionally would stack up a workspace per attach.
func EnsureWorkspace(h herdrRunner, machine, cwd, label string) (string, error) {
	out, err := h.Run(workspaceListArgs(machine)...)
	if err != nil {
		return "", fmt.Errorf("reaching the instance's herdr through saved machine %s: %w\n%s\n"+
			"machine forwarding needs herdr 0.9.1 on both ends, and forwarding never starts a "+
			"session server that isn't already running -- possible causes: this machine's herdr "+
			"predates 0.9.1 (upgrade it); the instance's herdr predates 0.9.1 (run `bivouac "+
			"provision`); the session's herdr server on the instance is not running, e.g. after a "+
			"reboot; or the instance is unreachable",
			machine, err, out)
	}
	workspaces, err := parseWorkspaceList(out)
	if err != nil {
		return "", err
	}
	if existing, found := findWorkspace(workspaces, label); found {
		return existing.ID, nil
	}

	if out, err := h.Run(workspaceCreateArgs(machine, cwd, label)...); err != nil {
		return "", fmt.Errorf("creating the %s workspace on the instance: %w\n%s", label, err, out)
	}
	// Read the id back rather than parsing the create reply: one shape to
	// know instead of two, and the listing is authoritative either way.
	out, err = h.Run(workspaceListArgs(machine)...)
	if err != nil {
		return "", fmt.Errorf("listing workspaces after creating %s: %w\n%s", label, err, out)
	}
	workspaces, err = parseWorkspaceList(out)
	if err != nil {
		return "", err
	}
	created, found := findWorkspace(workspaces, label)
	if !found {
		return "", fmt.Errorf("created the %s workspace on the instance but it is not in the listing", label)
	}

	// Starting a named session gives it a default "~" workspace rooted at
	// home, and ours is the second -- so the sidebar showed two entries per
	// session, one of which does nothing. Close the default now that there
	// is somewhere better to be.
	//
	// Only in the run that created ours, and only while it is untouched: a
	// reconnect must not tidy workspaces the user has since made, and more
	// than one pane is the cheapest evidence someone is working in it.
	// Best-effort -- a workspace that will not close is untidy, not broken.
	//
	// herdr's own guidance for tools driving it is not to close workspaces
	// you did not create. This one is the exception the user asked for, so
	// the guards above are what keep it narrow: the default label, a single
	// pane, and only on the run that gave them somewhere better to be.
	if def, ok := findWorkspace(workspaces, herdrDefaultWorkspaceLabel); ok && def.Panes <= 1 {
		_, _ = h.Run(workspaceCloseArgs(machine, def.ID)...)
	}
	return created.ID, nil
}

// FocusWorkspace switches the instance's herdr to the session's workspace.
//
// This is the only part of "switch to my session" bivouac can perform.
// Selecting the machine itself is client state with no API, so the caller
// still has to name the sidebar entry for the user to pick.
func FocusWorkspace(h herdrRunner, machine, id string) error {
	if out, err := h.Run(workspaceFocusArgs(machine, id)...); err != nil {
		return fmt.Errorf("focusing workspace %s on the instance: %w\n%s", id, err, out)
	}
	return nil
}
