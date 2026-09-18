package lifecycle

import (
	"encoding/json"
	"fmt"

	"github.com/jskswamy/bivouac/internal/shellcmd"
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

// remoteHerdrCmd builds a herdr command to run on the instance.
//
// --session always, because a machine profile targets one named session and
// these commands must reach that server rather than the instance's default
// one. Wrapped in `bash -lc` for the same reason the tailscale commands are:
// herdr comes from the user's nix profile, and a bare non-login shell is not
// guaranteed to have it on PATH.
func remoteHerdrCmd(session string, args ...string) string {
	inner := "herdr"
	if session != "" {
		inner += " --session " + shellcmd.Quote(session)
	}
	for _, a := range args {
		inner += " " + shellcmd.Quote(a)
	}
	return shellcmd.LoginShell(inner)
}

func workspaceListCmd(session string) string {
	return remoteHerdrCmd(session, "workspace", "list")
}

// workspaceCreateCmd roots a workspace in the session's checkout, which is
// the whole point: attaching then lands in the code rather than in $HOME.
//
// --no-focus because focusing is a separate, later step -- creating a
// workspace should not move anyone who happens to be attached already.
func workspaceCreateCmd(session, cwd, label string) string {
	return remoteHerdrCmd(session, "workspace", "create", "--cwd", cwd, "--label", label, "--no-focus")
}

func workspaceFocusCmd(session, id string) string {
	return remoteHerdrCmd(session, "workspace", "focus", id)
}

func workspaceCloseCmd(session, id string) string {
	return remoteHerdrCmd(session, "workspace", "close", id)
}

// herdrDefaultWorkspaceLabel is what herdr calls the workspace it opens for
// a session it has just started: the user's home directory, shown as "~".
const herdrDefaultWorkspaceLabel = "~"

// remoteRunner runs a command on the instance. *reconcile.Client satisfies
// it structurally; the interface exists so the flow below can be driven by a
// fake instead of a live SSH connection.
type remoteRunner interface {
	Run(cmd string) (output string, err error)
}

// EnsureWorkspace makes sure the session has a workspace on the instance
// rooted in its checkout, and returns that workspace's id.
//
// Addressed over SSH against the instance's own herdr server, because the
// local CLI cannot reach it: herdr commands always talk to the socket their
// pane inherited, so a workspace list run here describes this machine no
// matter which machine is selected.
//
// Reused when it already exists. Reconnecting to a session is the common
// case, and creating unconditionally would stack up a workspace per attach.
func EnsureWorkspace(r remoteRunner, session, repo, label string) (string, error) {
	out, err := r.Run(workspaceListCmd(session))
	if err != nil {
		return "", fmt.Errorf("listing workspaces on the instance: %w\n%s", err, out)
	}
	workspaces, err := parseWorkspaceList(out)
	if err != nil {
		return "", err
	}
	if existing, found := findWorkspace(workspaces, label); found {
		return existing.ID, nil
	}

	if out, err := r.Run(workspaceCreateCmd(session, repo, label)); err != nil {
		return "", fmt.Errorf("creating the %s workspace on the instance: %w\n%s", label, err, out)
	}
	// Read the id back rather than parsing the create reply: one shape to
	// know instead of two, and the listing is authoritative either way.
	out, err = r.Run(workspaceListCmd(session))
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
		_, _ = r.Run(workspaceCloseCmd(session, def.ID))
	}
	return created.ID, nil
}

// FocusWorkspace switches the instance's herdr to the session's workspace.
//
// This is the only part of "switch to my session" bivouac can perform.
// Selecting the machine itself is client state with no API, so the caller
// still has to name the sidebar entry for the user to pick.
func FocusWorkspace(r remoteRunner, session, id string) error {
	if out, err := r.Run(workspaceFocusCmd(session, id)); err != nil {
		return fmt.Errorf("focusing workspace %s on the instance: %w\n%s", id, err, out)
	}
	return nil
}
