package lifecycle

import (
	"encoding/json"
	"fmt"
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

// MachineLabel is what the machine is called in herdr's sidebar: the bare
// session name.
//
// The session is what the user thinks in, and the sidebar is narrow. An
// earlier scheme qualified every label with the instance and truncated to
// "jskswamy-cloudlab/su...", cutting the only part that says which session
// it is -- the disambiguator survived and the name did not.
//
// Cosmetic only. Identity is the recorded machine id, and matching is on
// target and session, so this can change again without touching cleanup.
func MachineLabel(session string) string {
	return session
}

// qualifiedMachineLabel disambiguates two instances that both hold a session
// of this name.
//
// A suffix, not a prefix, for the reason above: when it truncates, the
// instance is what gets cut and the session name survives.
func qualifiedMachineLabel(instance, session string) string {
	return session + " (" + instance + ")"
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

func machineRenameArgs(id, label string) []string {
	return []string{"machine", "rename", id, "--label", label}
}

// herdrRunner runs a herdr command on this machine.
//
// A seam, for the same reason instanceRunner is one: the whole flow below
// is decisions about what to run next, and without this none of it could be
// exercised without a herdr installed and a client attached.
type herdrRunner interface {
	Run(args ...string) (output string, err error)
}

// ownedMachines maps a herdr profile id to the cloudlab instance that
// registered it, built from what cloudlab recorded in its own state.
//
// It exists so cloudlab can tell its own profiles from ones added by hand.
// herdr stores no owner field, so this is the only honest answer to "did we
// make this?" -- and it decides both what may be renamed and what may be
// removed.
type OwnedMachines map[string]string

// EnsureMachine makes the instance's session present and enabled in the
// user's herdr sidebar, returning the profile id and the label it goes by.
//
// The id is what matters. Teardown removes exactly it, so it is returned on
// every path -- including the two where nothing was created. A session
// attached before cloudlab recorded ids would otherwise never get one and
// would leak when it was retired.
//
// Lists first, always. `herdr machine add` does not deduplicate, so running
// this twice would otherwise leave two profiles addressing one session.
//
// A disabled profile is enabled rather than re-added: adding would create a
// second profile and strand the first, still disabled, under the same label.
func EnsureMachine(h herdrRunner, instance, target, session string, owned OwnedMachines) (string, string, error) {
	profiles, err := listMachines(h)
	if err != nil {
		return "", "", err
	}

	if existing, found := findMachine(profiles, target, session); found {
		if !existing.Enabled {
			if out, err := h.Run(machineEnableArgs(existing.ID)...); err != nil {
				return "", "", fmt.Errorf("enabling herdr machine %s: %w\n%s", existing.Label, err, out)
			}
		}
		// Keep whatever the user last called it: that is what they will be
		// looking for in the sidebar.
		return existing.ID, existing.Label, nil
	}

	label := resolveLabel(h, profiles, owned, instance, session)
	if out, err := h.Run(machineAddArgs(target, label, session)...); err != nil {
		return "", "", fmt.Errorf("saving herdr machine %s: %w\n%s", label, err, out)
	}

	// Read the id back from the listing rather than scraping it out of the
	// add output: one shape to know instead of two, and the listing is
	// authoritative either way.
	profiles, err = listMachines(h)
	if err != nil {
		return "", label, err
	}
	created, found := findMachine(profiles, target, session)
	if !found {
		return "", label, fmt.Errorf("saved herdr machine %s but it is not in the listing", label)
	}
	return created.ID, created.Label, nil
}

// resolveLabel picks the sidebar name, qualifying both sides when another
// session already holds the bare one.
//
// Symmetric on purpose: if only the newcomer were qualified, a bare name
// would silently mean whichever session was registered first. Renaming is
// limited to profiles cloudlab recorded -- one the user added by hand keeps
// its name even when it is the thing in the way, because it is not
// cloudlab's to rename.
//
// Best-effort on the rename: a label that will not change is cosmetic, and
// no reason to refuse the attach the user asked for.
func resolveLabel(h herdrRunner, profiles []machineProfile, owned OwnedMachines, instance, session string) string {
	bare := MachineLabel(session)
	clash, found := findMachineByLabel(profiles, bare)
	if !found {
		return bare
	}
	if ownerInstance, ours := owned[clash.ID]; ours {
		_, _ = h.Run(machineRenameArgs(clash.ID, qualifiedMachineLabel(ownerInstance, clash.Session))...)
	}
	return qualifiedMachineLabel(instance, session)
}

func listMachines(h herdrRunner) ([]machineProfile, error) {
	out, err := h.Run(machineListArgs()...)
	if err != nil {
		return nil, fmt.Errorf("listing herdr machines: %w\n%s", err, out)
	}
	return parseMachineList(out)
}

func findMachineByLabel(profiles []machineProfile, label string) (machineProfile, bool) {
	for _, p := range profiles {
		if p.Label == label {
			return p, true
		}
	}
	return machineProfile{}, false
}

// RemoveMachine forgets the profile cloudlab registered for a session.
//
// By recorded id, never by matching. herdr profiles carry no owner field, so
// a label or target match would let teardown delete a profile the user added
// themselves -- and a rename in the sidebar would hide cloudlab's own. The
// id cloudlab wrote down at attach time is the only thing that says "this
// one is mine".
//
// An empty id means cloudlab never attached this session, and then teardown
// does nothing at all: it does not even list, because there is nothing it
// would be entitled to act on.
//
// Silent when the profile is already gone -- the user can remove one from
// the sidebar, and that is not a reason to fail a merge that has already
// landed their work.
func RemoveMachine(h herdrRunner, id string) error {
	if id == "" {
		return nil
	}
	if out, err := h.Run(machineRemoveArgs(id)...); err != nil {
		return fmt.Errorf("removing herdr machine %s: %w\n%s", id, err, out)
	}
	return nil
}
