package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/reconcile"
	"github.com/jskswamy/cloudlab/internal/shellcmd"
	"github.com/jskswamy/cloudlab/internal/tool"
)

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
// looking at, and returns the profile id and the sidebar entry to pick.
//
// Three steps, in this order. The machine has to exist and be enabled before
// its server can be addressed; the workspace has to exist before it can be
// focused; and focusing is last because it is the only step that moves
// anyone.
//
// The id comes back so the caller can record it. Teardown removes exactly
// that profile and nothing else, which is the only way to tell cloudlab's
// own entries from ones the user added by hand -- herdr profiles carry no
// owner field.
//
// It stops short of selecting the machine. That is client state with no CLI,
// no API operation and no keybinding -- verified against herdr 0.9.0 -- so
// the label comes back for the caller to name, and the user picks it.
func AttachMachine(ctx context.Context, instance, ip, user, session, repoName string, owned OwnedMachines) (string, string, error) {
	// No cloudlab session means there is nowhere to record the id, and a
	// machine standing for "the instance in general" is not what this is
	// for: it could never be cleaned up, because nothing would remember it.
	if session == "" {
		return "", "", fmt.Errorf("no session resolved -- `cloudlab herdr` attaches a session, so " +
			"start one with `cloudlab session start <name>`, or run it from outside herdr for a plain remote client")
	}
	if _, err := tool.Require("herdr"); err != nil {
		return "", "", err
	}
	target := MachineTarget(user, ip)

	id, label, err := EnsureMachine(localHerdr{ctx: ctx}, instance, target, session, owned)
	if err != nil {
		return "", "", err
	}

	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		return id, label, fmt.Errorf("machine %s is saved, but the instance could not be reached to prepare its workspace: %w", label, err)
	}
	defer func() { _ = client.Close() }()

	wsID, err := EnsureWorkspace(client, session, RemoteRepoPath(user, session, repoName), session)
	if err != nil {
		return id, label, err
	}
	if err := FocusWorkspace(client, session, wsID); err != nil {
		return id, label, err
	}
	return id, label, nil
}

// herdrSessionStopCmd and herdrSessionDeleteCmd retire the session server on
// the instance. Addressed by name, because a name is all herdr gives us --
// cloudlab owns that namespace, since it is the same name it created
// ~/sessions/<name> under.
func herdrSessionStopCmd(session string) string {
	return shellcmd.LoginShell("herdr session stop " + shellcmd.Quote(session))
}

func herdrSessionDeleteCmd(session string) string {
	return shellcmd.LoginShell("herdr session delete " + shellcmd.Quote(session))
}

// CleanupHerdr removes what `cloudlab herdr` left behind for a session: the
// saved machine on this side, and the session server on the instance.
//
// The two halves are independent on purpose. Forgetting the profile is a
// local operation and must still happen when the instance is unreachable --
// otherwise a box that has gone away leaves an entry in the sidebar forever,
// which is the failure this whole change exists to stop.
//
// Best-effort, and never fatal. Teardown runs after a merge has landed and
// verified the user's commits, or after a delete has torn the session down;
// a leftover sidebar entry is untidy, and failing at that point would be
// worse than untidy.
//
// client may be nil when the instance could not be reached, and then only
// the local half runs.
func CleanupHerdr(ctx context.Context, machineID, session string, client remoteRunner) {
	// No recorded id means cloudlab never attached this session -- and then
	// there is nothing of its making on either side. Starting a cloudlab
	// session does not create a herdr session; only attaching does. Trying
	// anyway stopped a server that never existed and told the user to go
	// and remove it by hand.
	if machineID == "" {
		return
	}
	if err := RemoveMachine(localHerdr{ctx: ctx}, machineID); err != nil {
		provider.ReportWarning(ctx, "herdr: "+err.Error()+
			"\nremove it by hand: herdr machine remove "+machineID)
	}
	if client == nil || session == "" {
		return
	}
	// Stop before delete: herdr refuses to delete a running session.
	if out, err := client.Run(herdrSessionStopCmd(session)); err != nil {
		provider.ReportWarning(ctx, "herdr: could not stop the "+session+
			" session on the instance: "+err.Error()+"\n"+out+
			"\nremove it by hand: herdr session stop "+session+" && herdr session delete "+session)
		return
	}
	if out, err := client.Run(herdrSessionDeleteCmd(session)); err != nil {
		provider.ReportWarning(ctx, "herdr: could not delete the "+session+
			" session on the instance: "+err.Error()+"\n"+out+
			"\nremove it by hand: herdr session delete "+session)
	}
}
