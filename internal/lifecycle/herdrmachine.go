package lifecycle

import (
	"encoding/json"
	"fmt"

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
