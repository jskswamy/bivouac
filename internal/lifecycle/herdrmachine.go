package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// machineProfile is one entry from `herdr machine list --json`: a saved SSH
// machine, which herdr shows in the same sidebar as local workspaces.
//
// The id is opaque and assigned by herdr. It is the only handle enable and
// remove accept, and herdr's own documentation says to read it from the
// listing rather than derive it from a hostname or label -- so nothing here
// ever constructs one.
type machineProfile struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Target  string `json:"target"`
	Session string `json:"session"`
	Enabled bool   `json:"enabled"`
}

// parseMachineList reads `herdr machine list --json`.
//
// An empty catalog is "[]" and means nobody has saved a machine yet, which
// is the ordinary first run rather than a failure.
func parseMachineList(out string) ([]machineProfile, error) {
	var profiles []machineProfile
	if err := json.Unmarshal([]byte(out), &profiles); err != nil {
		return nil, fmt.Errorf("reading the herdr machine list: %w\n%s", err, out)
	}
	return profiles, nil
}

// findMachine locates the profile addressing this session on this instance.
//
// Matched on target and session together, because that pair is what a
// profile is: herdr's documentation is explicit that "a machine profile
// targets one remote session; it does not combine every session on the
// host", so two cloudlab sessions on one instance are two profiles sharing a
// target and differing only here.
//
// Label is deliberately not part of the match. The user may rename a profile
// from the sidebar, and a rename must not make cloudlab believe the machine
// is gone and add a duplicate.
func findMachine(profiles []machineProfile, target, session string) (machineProfile, bool) {
	for _, p := range profiles {
		if p.Target == target && p.Session == session {
			return p, true
		}
	}
	return machineProfile{}, false
}

// MachineLabel is what the machine is called in herdr's sidebar.
//
// Qualified by instance because session names are only unique within one:
// two instances can each hold a session called "auth", and the label is the
// only thing distinguishing them to someone scanning the sidebar.
func MachineLabel(instance, session string) string {
	if session == "" {
		return instance
	}
	return instance + "/" + session
}

// machineListArgs reads the saved machines. --json because the human format
// is a table this would have to parse by column.
func machineListArgs() []string {
	return []string{"machine", "list", "--json"}
}

// machineAddArgs registers the instance as a saved machine.
//
// The target goes first, immediately after the verb. herdr's --help
// documents the opposite order ("[OPTIONS] --label <LABEL> <SSH_TARGET>")
// but the binary rejects that with a usage error and exit 2, so the runtime
// usage string is the one to trust.
//
// session empty means herdr's own default session on that host, and then
// --remote-session is omitted rather than passed empty: naming a session
// would ask herdr to start one that need not exist.
func machineAddArgs(target, label, session string) []string {
	args := []string{"machine", "add", target, "--label", label}
	if session != "" {
		args = append(args, "--remote-session", session)
	}
	return args
}

func machineEnableArgs(id string) []string {
	return []string{"machine", "enable", id}
}

func machineRemoveArgs(id string) []string {
	return []string{"machine", "remove", id}
}

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

// findWorkspace locates the workspace cloudlab created for a session.
//
// By label, because that is the only part cloudlab chose. Ids are assigned
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
		inner += " --session " + reconcile.ShellQuote(session)
	}
	for _, a := range args {
		inner += " " + reconcile.ShellQuote(a)
	}
	return "bash -lc " + reconcile.ShellQuote(inner)
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

// herdrRunner runs a herdr command on this machine.
//
// A seam, for the same reason instanceRunner is one: the whole flow below
// is decisions about what to run next, and without this none of it could be
// exercised without a herdr installed and a client attached.
type herdrRunner interface {
	Run(args ...string) (output string, err error)
}

// EnsureMachine makes the instance's session present and enabled in the
// user's herdr sidebar, and returns the label it goes by there.
//
// Lists first, always. `herdr machine add` does not deduplicate, so running
// this twice would otherwise leave two profiles addressing one session, and
// nothing downstream could tell which to remove later.
//
// A disabled profile is enabled rather than re-added: adding would create a
// second profile and strand the first, still disabled, under the same label.
func EnsureMachine(h herdrRunner, instance, target, session string) (string, error) {
	label := MachineLabel(instance, session)

	out, err := h.Run(machineListArgs()...)
	if err != nil {
		return "", fmt.Errorf("listing herdr machines: %w\n%s", err, out)
	}
	profiles, err := parseMachineList(out)
	if err != nil {
		return "", err
	}

	existing, found := findMachine(profiles, target, session)
	switch {
	case !found:
		if out, err := h.Run(machineAddArgs(target, label, session)...); err != nil {
			return "", fmt.Errorf("saving herdr machine %s: %w\n%s", label, err, out)
		}
	case !existing.Enabled:
		if out, err := h.Run(machineEnableArgs(existing.ID)...); err != nil {
			return "", fmt.Errorf("enabling herdr machine %s: %w\n%s", existing.Label, err, out)
		}
		label = existing.Label
	default:
		// Present and enabled. Its label is whatever the user last called
		// it, which is what they will be looking for in the sidebar.
		label = existing.Label
	}
	return label, nil
}

// RemoveMachine forgets the profile addressing this session, so profiles do
// not outlive the sessions they point at.
//
// Silent when there is nothing to remove. Teardown runs this on paths that
// may never have registered anything, and the user can delete a profile from
// the sidebar themselves -- neither is a reason to fail a merge or a delete
// that has already done its real work.
func RemoveMachine(h herdrRunner, target, session string) error {
	out, err := h.Run(machineListArgs()...)
	if err != nil {
		return fmt.Errorf("listing herdr machines: %w\n%s", err, out)
	}
	profiles, err := parseMachineList(out)
	if err != nil {
		return err
	}
	existing, found := findMachine(profiles, target, session)
	if !found {
		return nil
	}
	if out, err := h.Run(machineRemoveArgs(existing.ID)...); err != nil {
		return fmt.Errorf("removing herdr machine %s: %w\n%s", existing.Label, err, out)
	}
	return nil
}

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
// This is the only part of "switch to my session" cloudlab can perform.
// Selecting the machine itself is client state with no API, so the caller
// still has to name the sidebar entry for the user to pick.
func FocusWorkspace(r remoteRunner, session, id string) error {
	if out, err := r.Run(workspaceFocusCmd(session, id)); err != nil {
		return fmt.Errorf("focusing workspace %s on the instance: %w\n%s", id, err, out)
	}
	return nil
}

// insideHerdr reports whether this is running in a pane herdr hosts.
//
// herdr sets HERDR_ENV=1 in every pane it hosts. It used to mean "refuse":
// herdr blocks nested sessions, so exec'ing a --remote client here failed
// with herdr's own opaque "remote client exited with exit status: 1".
//
// With saved machines it means the opposite. Registering is not a nested
// session, and being inside herdr is exactly when a machine belongs in the
// sidebar the user is already looking at.
func InsideHerdr() bool {
	return os.Getenv("HERDR_ENV") != ""
}

// MachineTarget is the SSH target a machine profile stores.
//
// Built one way and only here, because profiles are matched on it: a target
// spelled differently between runs reads as a different machine and adds a
// duplicate rather than finding the existing one.
func MachineTarget(user, ip string) string {
	return "ssh://" + user + "@" + ip
}

// localHerdr runs herdr on this machine, which is where the saved-machine
// catalog lives -- machines are client state, not something any server holds.
type localHerdr struct{ ctx context.Context }

func (l localHerdr) Run(args ...string) (string, error) {
	// #nosec G204 -- argv-array exec.Command, no shell. Every argument is
	// built by this package from a provider-assigned address, a cloudlab
	// session name already checked by CheckSessionName, or an id herdr
	// itself handed back.
	out, err := exec.CommandContext(l.ctx, "herdr", args...).CombinedOutput()
	return string(out), err
}

// AttachMachine puts a session in the herdr window the user is already
// looking at, and returns the sidebar entry to pick.
//
// Three steps, in this order. The machine has to exist and be enabled before
// its server can be addressed; the workspace has to exist before it can be
// focused; and focusing is last because it is the only step that moves
// anyone.
//
// It stops short of selecting the machine. That is client state with no CLI,
// no API operation and no keybinding -- verified against herdr 0.9.0 -- so
// the label comes back for the caller to name, and the user picks it.
func AttachMachine(ctx context.Context, instance, ip, user, session, repoName string) (string, error) {
	if _, err := exec.LookPath("herdr"); err != nil {
		return "", fmt.Errorf("herdr not found on PATH (install it: https://herdr.dev/): %w", err)
	}
	target := MachineTarget(user, ip)

	label, err := EnsureMachine(localHerdr{ctx: ctx}, instance, target, session)
	if err != nil {
		return "", err
	}
	// No cloudlab session means herdr's own default server, which has no
	// session checkout to root a workspace in. The machine alone is the
	// whole job there.
	if session == "" {
		return label, nil
	}

	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		return label, fmt.Errorf("machine %s is saved, but the instance could not be reached to prepare its workspace: %w", label, err)
	}
	defer func() { _ = client.Close() }()

	id, err := EnsureWorkspace(client, session, RemoteRepoPath(user, session, repoName), session)
	if err != nil {
		return label, err
	}
	if err := FocusWorkspace(client, session, id); err != nil {
		return label, err
	}
	return label, nil
}
