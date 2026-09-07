package cmd

import (
	"fmt"
	"os"

	"github.com/jskswamy/cloudlab/internal/identity"
	"github.com/spf13/cobra"
)

// lookupCommandSpec describes one lookup-only command: one that only
// needs to look up an already-existing instance in state, never repo
// content. named is true when the command's first positional arg is the
// instance name (false for sync/download, whose positionals are paths).
type lookupCommandSpec struct {
	use, short, verb string
	args             cobra.PositionalArgs
	named            bool
	// parent groups this command under a noun instead of putting it at the
	// top level. Session verbs live under "session" so cloudlab's top level
	// stays about the instance -- up, down, ssh, status, provision -- and a
	// new verb is a new command rather than another string comparison.
	parent string
	flags  func(c *cobra.Command)
	run    func(cmd *cobra.Command, name string, args []string) error
}

var lookupCommandSpecs = []lookupCommandSpec{
	{
		use:   "shell [name]",
		short: "Reconcile home-manager, then open a local subshell with instance envs injected",
		verb:  "shell",
		args:  cobra.MaximumNArgs(1),
		named: true,
	},
	{
		use:   "ssh [name]",
		short: "Open an interactive remote shell on the instance",
		verb:  "ssh",
		args:  cobra.MaximumNArgs(1),
		named: true,
		flags: func(c *cobra.Command) {
			c.Flags().String("dir", "", "remote directory to cd into (defaults to the synced repo's location)")
		},
		run: runSSH,
	},
	{
		use:   "herdr [name]",
		short: "Open an interactive herdr session on the instance (background session that survives disconnects)",
		verb:  "herdr",
		args:  cobra.MaximumNArgs(1),
		named: true,
		run:   runHerdr,
	},
	{
		use:   "tmux [session-name]",
		short: "Open a tmux session on the instance, creating it if needed (default session: \"main\")",
		verb:  "tmux",
		args:  cobra.MaximumNArgs(1),
		named: false,
		run:   runTmux,
	},
	{
		use:   "tailscale [name]",
		short: "Join the instance to your personal Tailscale network",
		verb:  "tailscale",
		args:  cobra.MaximumNArgs(1),
		named: true,
		run:   runTailscale,
	},
	{
		use:   "pair [name]",
		short: "Pair the getmoshi.app mobile app with the instance via QR code",
		verb:  "pair",
		args:  cobra.MaximumNArgs(1),
		named: true,
		flags: func(c *cobra.Command) {
			c.Flags().String("host", "", "address the QR advertises to the phone (defaults to prompting when a Tailscale address is available)")
		},
		run: runPair,
	},
	{
		use:   "watch [name]",
		short: "Restart continuous two-way repo sync if it's stopped or dead",
		verb:  "watch",
		args:  cobra.MaximumNArgs(1),
		named: true,
		run:   runWatch,
	},
	{
		use:    "start <name>",
		short:  "Start a named agent session on the instance",
		verb:   "session start",
		args:   cobra.ExactArgs(1),
		named:  false,
		parent: "session",
		run:    runSessionStart,
	},
	{
		use:   "connect [name]",
		short: "Open a Jupyter tunnel to the instance (python template only)",
		verb:  "connect",
		args:  cobra.MaximumNArgs(1),
		named: true,
	},
	{
		use:   "status [name]",
		short: "Show instance detail: IP, uptime, cost, sync/watch state",
		verb:  "status",
		args:  cobra.MaximumNArgs(1),
		named: true,
		run:   runStatus,
	},
	{
		use:   "down [name]",
		short: "Stop watch, destroy the VM, and clear state",
		verb:  "down",
		args:  cobra.MaximumNArgs(1),
		named: true,
		run:   runDown,
	},
	{
		use:   "sync [remote-dir]",
		short: "One-shot push of a local directory to the instance (defaults to the current directory)",
		verb:  "sync",
		args:  cobra.MaximumNArgs(1),
		named: false,
		flags: func(c *cobra.Command) {
			c.Flags().String("dir", "", "local directory to sync (defaults to the current directory)")
		},
		run: runSync,
	},
	{
		use:   "download <remote-dir> [local-dir]",
		short: "One-shot pull of files back from the instance",
		verb:  "download",
		args:  cobra.RangeArgs(1, 2),
		named: false,
		run:   runDownload,
	},
}

// newLookupCommands builds every lookup-only command from
// lookupCommandSpecs. Flag handling, identity resolution, and the
// stub/exit-code behavior are shared across all of them — only Use/Short
// text, the Args validator, the per-command flags, and whether the verb is
// a top-level command or hangs off a noun differ per spec.
func newLookupCommands() []*cobra.Command {
	cmds := make([]*cobra.Command, 0, len(lookupCommandSpecs))
	parents := map[string]*cobra.Command{}

	for _, spec := range lookupCommandSpecs {
		c := &cobra.Command{
			Use:   spec.use,
			Short: spec.short,
			Args:  spec.args,
			RunE: func(cmd *cobra.Command, args []string) error {
				positional := ""
				if spec.named && len(args) > 0 {
					positional = args[0]
				}
				name, err := resolveLookupIdentity(cmd, positional)
				if err != nil {
					return err
				}
				if spec.run != nil {
					return spec.run(cmd, name, args)
				}
				return stubErr(spec.verb, name)
			},
		}
		if spec.flags != nil {
			spec.flags(c)
		}

		if spec.parent == "" {
			cmds = append(cmds, c)
			continue
		}
		parent, ok := parents[spec.parent]
		if !ok {
			parent = newGroupCmd(spec.parent)
			parents[spec.parent] = parent
			cmds = append(cmds, parent)
		}
		parent.AddCommand(c)
	}
	return cmds
}

// newGroupCmd builds the noun a set of verbs hangs off. It runs nothing
// itself: invoked bare it prints help, which is what someone typing
// `cloudlab session` wants.
func newGroupCmd(name string) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: "Manage agent " + name + "s on the instance",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
}

// resolveLookupIdentity resolves an instance name for lookup-only
// commands: positional arg, then --name, then (if cwd or --repo is
// inside a git repo) that repo's derived name.
func resolveLookupIdentity(cmd *cobra.Command, positional string) (string, error) {
	repoFlag, _ := cmd.Flags().GetString("repo")
	nameFlag, _ := cmd.Flags().GetString("name")

	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return identity.InstanceName(cwd, repoFlag, positional, nameFlag)
}

func stubErr(verb, name string) error {
	return fmt.Errorf("%s: not implemented yet (instance %q)", verb, name)
}
