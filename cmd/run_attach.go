package cmd

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/lifecycle"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/state"
)

func runSSH(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	dir, err := cmd.Flags().GetString("dir")
	if err != nil {
		return err
	}
	forwardAgent, err := cmd.Flags().GetBool("forward-agent")
	if err != nil {
		return err
	}
	if dir == "" {
		// record.RepoPath is a mirror of the local checkout path that rsync
		// used to create; nothing creates it now, so it silently landed the
		// user in $HOME. The session's repository is where the work is.
		//
		// nil, not args: for ssh and herdr args[0] is the INSTANCE name
		// (named: true), and for tmux it is a tmux session name -- a
		// different namespace. Passing either through would resolve it as a
		// bivouac session and fail with "instance X has no session X".
		// Resolution here comes from the cwd, the single session, or the
		// picker; naming one explicitly is what `cd` into its worktree is for.
		if sess, err := resolveSessionInteractive(cmd, record, nil); err == nil {
			dir = lifecycle.RemoteRepoPath(record.User, sess.Name, sess.RepoNameOr(record.Name))
		}
	}
	return lifecycle.SSH(cmd.Context(), record.IP, record.User, dir, forwardAgent)
}

// herdrTabsFor reads the tab layout the session's repository asks for.
//
// Best-effort, like beadsModeFor: `bivouac herdr` has never needed a
// config to resolve, and the layout must not become the reason it fails.
// A repository with no bivouac.pkl has nothing to lay out -- base.pkl's
// tabs included, since Resolve is anchored on the project file.
func herdrTabsFor(ctx context.Context, localRepo string) []config.HerdrTab {
	if localRepo == "" {
		return nil
	}
	path := filepath.Join(localRepo, "bivouac.pkl")
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	cfg, err := config.Resolve(ctx, path)
	if err != nil {
		provider.ReportWarning(ctx, "herdr: could not resolve "+path+" ("+err.Error()+"); attaching without its tabs")
		return nil
	}
	return cfg.HerdrTabs
}

func runHerdr(cmd *cobra.Command, name string, args []string) error {
	store, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	// nil, not args: args[0] here is the INSTANCE name (see the nil-args
	// comment on runSSH). herdr does not accept a starting directory for a
	// --remote session (that's cloudlab-7y1, via `workspace create --cwd` at
	// `session start` time) -- but it does accept --session, so the resolved
	// bivouac session still buys a per-session herdr session: reconnecting
	// to the same session name lands back in the same place. Inside herdr,
	// AttachMachine roots the workspace in the checkout itself, so the
	// starting directory is not missing there. With no session resolvable,
	// connect anyway with herdr's own default session -- connecting is not
	// destructive and must degrade, not refuse.
	session := ""
	repoName := record.Name
	localRepo := ""
	if sess, err := resolveSessionInteractive(cmd, record, nil); err == nil {
		session = sess.Name
		repoName = sess.RepoNameOr(record.Name)
		localRepo = sess.LocalRepo
	}

	// Inside herdr, saving the instance as a machine puts it in the sidebar
	// the user is already looking at -- one window holding local work and
	// every instance, which is what 0.9.0 added machines for. Outside it
	// there is nothing to attach to, so launching a client stays right.
	if lifecycle.InsideHerdr() {
		id, label, err := lifecycle.AttachMachine(cmd.Context(), record.Name, record.IP,
			record.User, session, repoName, herdrTabsFor(cmd.Context(), localRepo), ownedMachines(store))
		// Recorded before the error is looked at: AttachMachine hands back a
		// saved profile's id even when a later step failed, and teardown
		// removes this exact profile -- one bivouac created but did not
		// record is one nothing will ever clean up.
		if rerr := recordHerdrMachine(store, record.Name, session, id); rerr != nil {
			return errors.Join(err, rerr)
		}
		if err != nil {
			return err
		}
		// Named, not selected. Which machine is current is client state
		// with no CLI behind it, so the last step is the user's keypress.
		cmd.Printf("%s is in your herdr sidebar — select it to attach\n", label)
		return nil
	}
	return lifecycle.Herdr(cmd.Context(), record.IP, record.User, session)
}

// ownedMachines gathers the herdr profiles bivouac registered, across every
// instance, mapped to the instance that registered each.
//
// This is the only honest answer to "is this profile ours?". herdr stores no
// owner field, so without it bivouac would have to match on label or target
// -- and would then rename or delete profiles the user added by hand.
func ownedMachines(store *state.Store) lifecycle.OwnedMachines {
	owned := lifecycle.OwnedMachines{}
	records, err := store.List()
	if err != nil {
		// Best-effort: without this map bivouac qualifies its own label and
		// renames nothing, which is the conservative half of the behaviour.
		return owned
	}
	for _, r := range records {
		for _, s := range r.Sessions {
			if s.HerdrMachineID != "" {
				owned[s.HerdrMachineID] = r.Name
			}
		}
	}
	return owned
}

// recordHerdrMachine remembers which profile belongs to a session, so
// teardown can remove exactly that one.
func recordHerdrMachine(store *state.Store, instance, session, id string) error {
	if id == "" {
		return nil
	}
	record, ok, err := store.Get(instance)
	if err != nil || !ok {
		return err
	}
	for i := range record.Sessions {
		if record.Sessions[i].Name == session {
			if record.Sessions[i].HerdrMachineID == id {
				return nil
			}
			record.Sessions[i].HerdrMachineID = id
			return store.Put(record)
		}
	}
	return nil
}

func runPair(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}

	// The QR hands the phone an address to connect back to, so which one
	// it advertises is a real choice: the public IP works from anywhere,
	// the tailnet address keeps the session off the public internet but
	// only works from a device on the same tailnet. bivouac itself
	// always connects over the public IP regardless (see lifecycle.Pair).
	advertise, err := cmd.Flags().GetString("host")
	if err != nil {
		return err
	}
	if advertise == "" {
		advertise, err = choosePairHost(cmd, record)
		if err != nil {
			return err
		}
	}
	forwardAgent, err := cmd.Flags().GetBool("forward-agent")
	if err != nil {
		return err
	}
	return lifecycle.Pair(cmd.Context(), record.IP, advertise, record.User, forwardAgent)
}

// choosePairHost returns the address the pairing QR should advertise.
// With no tailnet address available there is nothing to choose and the
// public IP is returned without prompting; otherwise the user picks,
// defaulting to the tailnet address as the more private of the two.
func choosePairHost(cmd *cobra.Command, record state.Record) (string, error) {
	if !record.TailscaleJoined {
		return record.IP, nil
	}
	tsIP, err := lifecycle.TailscaleIP(cmd.Context(), record.IP, record.User)
	if err != nil || tsIP == "" {
		// Not reachable over the tailnet is not a pairing failure --
		// fall back to the address that always works.
		return record.IP, nil
	}

	cmd.Printf("Which address should the QR advertise to the phone?\n")
	cmd.Printf("  1) %s  (Tailscale -- private, needs the phone on your tailnet)\n", tsIP)
	cmd.Printf("  2) %s  (public IP -- reachable anywhere)\n", record.IP)
	cmd.Print("Choose [1]: ")

	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if strings.TrimSpace(line) == "2" {
		return record.IP, nil
	}
	return tsIP, nil
}

// defaultTmuxSession is the session bivouac tmux creates-or-attaches
// to when no session-name argument is given.
const defaultTmuxSession = "main"

// tmuxSession returns the session name bivouac tmux should
// create-or-attach: args[0] if given, else defaultTmuxSession.
func tmuxSession(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return defaultTmuxSession
}

func runTmux(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	forwardAgent, err := cmd.Flags().GetBool("forward-agent")
	if err != nil {
		return err
	}
	session := tmuxSession(args)
	// nil, not args: args[0] here is a tmux session name, a different
	// namespace from a bivouac session name (see the nil-args comment on
	// runSSH). Only substitute the bivouac session's name when the caller
	// didn't already ask for a specific tmux session -- reconnecting to that
	// same name is what lands back in the same place.
	if len(args) == 0 {
		if sess, err := resolveSessionInteractive(cmd, record, nil); err == nil {
			session = sess.Name
		}
	}
	return lifecycle.Tmux(cmd.Context(), record.IP, record.User, session, forwardAgent)
}
