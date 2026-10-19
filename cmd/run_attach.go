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
	"github.com/jskswamy/bivouac/internal/reconcile"
	"github.com/jskswamy/bivouac/internal/state"
)

func runSSH(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(cmd, name)
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

// herdrTabsFor reads the tab layout to lay the session's workspace out
// with: the project's merged with the personal base's, or the base's
// alone when the repository declares no config of its own.
//
// The base is read separately rather than left to Resolve, which is
// anchored on the project file and so never sees it without one. Tabs
// declared once in base.pkl are meant to make every instance look the
// same; a repository that happens to have no bivouac.pkl is not a
// statement that the user wants a different layout there.
//
// Best-effort throughout, like beadsModeFor: `bivouac herdr` has never
// needed a config to resolve, and the layout must not become the reason
// it fails.
func herdrTabsFor(ctx context.Context, localRepo string) []config.HerdrTab {
	path := ""
	if localRepo != "" {
		candidate := filepath.Join(localRepo, "bivouac.pkl")
		if _, err := os.Stat(candidate); err == nil {
			path = candidate
		}
	}

	if path == "" {
		cfg, err := config.ResolveBase(ctx)
		if err != nil {
			provider.ReportWarning(ctx, "herdr: could not resolve your base config ("+err.Error()+"); attaching without its tabs")
			return nil
		}
		return cfg.HerdrTabs
	}

	cfg, err := config.Resolve(ctx, path)
	if err != nil {
		provider.ReportWarning(ctx, "herdr: could not resolve "+path+" ("+err.Error()+"); attaching without its tabs")
		return nil
	}
	return cfg.HerdrTabs
}

func runHerdr(cmd *cobra.Command, name string, args []string) error {
	store, record, err := resolveInstance(cmd, name)
	if err != nil {
		return err
	}
	ctx := progressCtx(cmd)
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
	generation := ""
	repoName := record.Name
	localRepo := ""
	if sess, err := resolveSessionInteractive(cmd, record, nil); err == nil {
		session = sess.Name
		generation = sess.Generation
		repoName = sess.RepoNameOr(record.Name)
		localRepo = sess.LocalRepo
	}

	// Inside herdr, saving the instance as a machine puts it in the sidebar
	// the user is already looking at -- one window holding local work and
	// every instance, which is what 0.9.0 added machines for. Outside it
	// there is nothing to attach to, so launching a client stays right.
	if lifecycle.InsideHerdr() {
		if session == "" {
			return errors.New("no session resolved -- start one with `bivouac session start <name>`")
		}
		attachment, err := lifecycle.EnsureSessionAttachment(ctx, store, record.Name, session)
		if err != nil {
			return err
		}
		if tabs := herdrTabsFor(ctx, localRepo); len(tabs) > 0 {
			if client, err := reconcile.Connect(ctx, record.IP, record.User); err != nil {
				provider.ReportWarning(ctx, "herdr: laying out tabs: connecting to the instance: "+err.Error())
			} else {
				err := lifecycle.LayOutTabs(client, attachment.Address.Session, attachment.Handle,
					lifecycle.RemoteRepoPath(record.User, session, repoName), tabs, true)
				_ = client.Close()
				if err != nil {
					provider.ReportWarning(ctx, "herdr: laying out tabs: "+err.Error())
				}
				// The layout may have closed the root pane the proof names.
				if _, err := lifecycle.RefreshWorkspaceProof(ctx, store, record.Name, generation, attachment); err != nil {
					provider.ReportWarning(ctx, "herdr: recording workspace ownership: "+err.Error())
				}
			}
		}
		cmd.Printf("%s is in your herdr sidebar — select it to attach\n", record.Name)
		return nil
	}
	if session == "" {
		return lifecycle.Herdr(ctx, record.IP, record.User, session)
	}
	attachment, err := lifecycle.BeginDirectSessionAttachment(ctx, store, record.Name, session)
	if err != nil {
		return err
	}
	if err := lifecycle.Herdr(ctx, record.IP, record.User, session); err != nil {
		return err
	}
	return lifecycle.CompleteDirectSessionAttachment(store, record.Name, generation, attachment)
}

// recordHerdrMachine remembers which profile belongs to an instance, so
// teardown can remove the shared machine once the instance goes away.
func recordHerdrMachine(store *state.Store, instance, id string) error {
	if id == "" {
		return nil
	}
	return store.Update(instance, func(record *state.Record) error {
		record.HerdrMachineID = id
		return nil
	})
}

func runPair(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(cmd, name)
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
	return askPairHost(cmd, tsIP, record.IP, isInteractive())
}

// askPairHost asks which of the two addresses the QR should advertise.
//
// Without a terminal it takes the default rather than reading: a prompt
// nobody can answer is a hang, not a question (see isInteractive), and
// this one is the last in the package still missing that guard. Unlike
// the port pickers, which refuse because no candidate is more right than
// another, pair has a default worth defending -- the tailnet address
// keeps the session off the public internet -- so the unattended run
// completes instead of failing. It says which address it took, because a
// choice made on the user's behalf should not be a silent one. --host
// overrides it either way.
//
// interactive is a parameter rather than an isInteractive() call so the
// prompt can be exercised against a buffer, the same split the listener
// pickers use.
func askPairHost(cmd *cobra.Command, tsIP, publicIP string, interactive bool) (string, error) {
	if !interactive {
		cmd.Printf("Advertising %s in the QR (Tailscale; no terminal to ask on — pass --host to choose)\n", tsIP)
		return tsIP, nil
	}

	cmd.Printf("Which address should the QR advertise to the phone?\n")
	cmd.Printf("  1) %s  (Tailscale -- private, needs the phone on your tailnet)\n", tsIP)
	cmd.Printf("  2) %s  (public IP -- reachable anywhere)\n", publicIP)
	cmd.Print("Choose [1]: ")

	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if strings.TrimSpace(line) == "2" {
		return publicIP, nil
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
	_, record, err := resolveInstance(cmd, name)
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
