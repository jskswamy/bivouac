package cmd

import (
	"fmt"
	"os"

	"github.com/jskswamy/bivouac/internal/identity"
	"github.com/spf13/cobra"
)

// lookupCommandSpec describes one lookup-only command: one that only
// needs to look up an already-existing instance in state, never repo
// content. named is true when the command's first positional arg is the
// instance name (false for sync/download, whose positionals are paths).
type lookupCommandSpec struct {
	use, short, verb string
	// long is the help text cobra shows for `bivouac <verb> --help`, on top
	// of short. Left empty for every command whose flags speak for
	// themselves; start's flags need the extra explanation of how --task and
	// --issue relate.
	long     string
	example  string
	metadata commandMetadata
	args     cobra.PositionalArgs
	named    bool
	// parent groups this command under a noun instead of putting it at the
	// top level. Session verbs live under "session" so bivouac's top level
	// stays about the instance -- up, down, ssh, status, provision -- and a
	// new verb is a new command rather than another string comparison.
	parent string
	// spansInstances marks a verb that acts across every instance rather than
	// one, so the shared dispatch must not resolve an instance name for it.
	// named is a different question: it decides whether the first positional
	// FEEDS resolution, not whether resolution happens at all.
	spansInstances bool
	flags          func(c *cobra.Command)
	run            func(cmd *cobra.Command, name string, args []string) error
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
			c.Flags().Bool("forward-agent", false, "forward your local SSH agent to the instance for this connection")
		},
		run: runSSH,
	},
	{
		use:   "herdr [name]",
		short: "Open an interactive herdr session on the instance (background session that survives disconnects)",
		long: "Open an interactive Herdr session on the instance.\n\n" +
			"The optional [name] is an instance name, not a session name. Bivouac\n" +
			"resolves the session from the current worktree, from the single live\n" +
			"session on the instance, or by asking you to choose when a terminal is\n" +
			"available.\n\n" +
			"When run outside Herdr, bivouac launches Herdr against the instance's\n" +
			"SSH endpoint and passes the resolved bivouac session as Herdr's session\n" +
			"name when one can be found. When run inside Herdr, bivouac registers or\n" +
			"focuses a saved machine for the instance, then creates or focuses a\n" +
			"workspace named for the bivouac session.\n\n" +
			"If no bivouac session exists, run `bivouac session start <name>` first\n" +
			"or run Herdr outside Herdr to attach to its default remote session.",
		example: "bivouac herdr\n" +
			"bivouac --name jskswamy-bivouac herdr\n" +
			"bivouac session list\n" +
			"bivouac session start auth",
		metadata: commandMetadata{
			Title:            "Attach Herdr to a bivouac instance/session",
			Interactive:      true,
			Network:          true,
			Destructive:      false,
			RequiresInstance: true,
			MayPrompt:        true,
			Arguments: []argumentMetadata{{
				Name:        "name",
				Description: "Optional bivouac instance name, not session name",
				Schema:      map[string]any{"type": "string"},
			}},
			SideEffects: []string{
				"may register a Herdr saved machine",
				"may create or focus a Herdr workspace",
			},
			Related: []string{"session list", "session start", "ssh", "tmux"},
		},
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
		flags: func(c *cobra.Command) {
			c.Flags().Bool("forward-agent", false, "forward your local SSH agent to the instance for this connection")
		},
		run: runTmux,
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
			c.Flags().Bool("forward-agent", false, "forward your local SSH agent to the instance for this connection")
		},
		run: runPair,
	},
	{
		use:   "start <name>",
		short: "Start a named agent session on the instance",
		long: "Start a named agent session on the instance.\n\n" +
			"--task and --issue say what the session is for, so the agent working\n" +
			"in it knows without being told again. They are mutually exclusive:\n" +
			"--task takes free text in your own words; --issue names a beads issue\n" +
			"by ID and, where this repository has beads, claims it in the session's\n" +
			"own database. Passing neither is fine -- both are optional, and a\n" +
			"session started before you have decided what it is for is still a\n" +
			"normal session. Re-running start without either leaves an existing\n" +
			"TASK.md untouched.",
		metadata: commandMetadata{
			Title:            "Start a named agent session on the instance",
			Network:          true,
			RequiresInstance: true,
			RequiresRepo:     true,
			Arguments: []argumentMetadata{{
				Name:        "name",
				Required:    true,
				Description: "Session name to create",
				Schema:      map[string]any{"type": "string"},
			}},
		},
		verb:   "session start",
		args:   cobra.ExactArgs(1),
		named:  false,
		parent: "session",
		flags: func(c *cobra.Command) {
			c.Flags().String("task", "", "What this session is for, in your own words")
			c.Flags().String("issue", "", "The beads issue this session is for, e.g. cloudlab-bv5")
		},
		run: runSessionStart,
	},
	{
		use:   "list",
		short: "List agent sessions across all instances",
		metadata: commandMetadata{
			Title:        "List agent sessions across all instances",
			SupportsJSON: true,
		},
		verb:           "session list",
		args:           cobra.NoArgs,
		named:          false,
		parent:         "session",
		spansInstances: true,
		run:            runSessionList,
	},
	{
		use:   "connect [name]",
		short: "Reach a service on the instance by port, over the tailnet or an SSH forward",
		verb:  "connect",
		args:  cobra.MaximumNArgs(1),
		named: true,
		flags: func(c *cobra.Command) {
			c.Flags().Int("port", 0, "remote port to reach (omit to choose from what is listening)")
			c.Flags().Int("local-port", 0, "local port to forward through (default: the remote port, or a free one if that is taken)")
			c.Flags().Bool("all", false, "include the instance's own sockets (sshd, resolved, tailscaled) in the choices")
		},
		run: runConnect,
	},
	{
		use:   "serve [port]",
		short: "Publish a service on your tailnet, where it outlives this command",
		verb:  "serve",
		args:  cobra.MaximumNArgs(1),
		named: false,
		flags: func(c *cobra.Command) {
			c.Flags().Bool("all", false, "include the instance's own sockets (sshd, resolved, tailscaled) in the choices")
		},
		run: runServe,
	},
	{
		use:   "unserve [port]",
		short: "Stop publishing a service on your tailnet",
		verb:  "unserve",
		args:  cobra.MaximumNArgs(1),
		named: false,
		run:   runUnserve,
	},
	{
		use:   "status [name]",
		short: "Show instance detail: IP, uptime, cost, live status",
		metadata: commandMetadata{
			Title:            "Show instance status",
			Network:          true,
			RequiresInstance: true,
			SupportsJSON:     true,
			Arguments: []argumentMetadata{{
				Name:        "name",
				Description: "Optional bivouac instance name",
				Schema:      map[string]any{"type": "string"},
			}},
		},
		verb:  "status",
		args:  cobra.MaximumNArgs(1),
		named: true,
		run:   runStatus,
	},
	{
		use:   "down [name]",
		short: "Rescue any session's work, destroy the VM, and clear state",
		verb:  "down",
		args:  cobra.MaximumNArgs(1),
		named: true,
		flags: func(c *cobra.Command) {
			c.Flags().Bool("force", false, "destroy without rescuing work from the instance")
		},
		run: runDown,
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
	{
		use:    "pull [session]",
		short:  "Fetch an agent session's work without merging it",
		verb:   "session pull",
		args:   cobra.MaximumNArgs(1),
		named:  false,
		parent: "session",
		run:    runPull,
	},
	{
		use:    "merge [session]",
		short:  "Replay, sign and accept an agent session's work, then retire the session",
		verb:   "session merge",
		args:   cobra.MaximumNArgs(1),
		named:  false,
		parent: "session",
		run:    runMerge,
	},
	{
		use:    "delete [session]",
		short:  "Discard a session without merging its work, checking the instance first",
		verb:   "session delete",
		args:   cobra.MaximumNArgs(1),
		named:  false,
		parent: "session",
		flags: func(c *cobra.Command) {
			c.Flags().Bool("force", false, "delete even when the session has unmerged work")
		},
		run: runSessionDelete,
	},
}

// newLookupCommands builds every lookup-only command from
// lookupCommandSpecs. Flag handling, identity resolution, and the
// stub/exit-code behavior are shared across all of them — only Use/Short
// text, the Args validator, the per-command flags, and whether the verb
// takes an instance name or spans every instance differ per spec.
func newLookupCommands() []*cobra.Command {
	cmds := make([]*cobra.Command, 0, len(lookupCommandSpecs))
	parents := map[string]*cobra.Command{}

	for _, spec := range lookupCommandSpecs {
		c := &cobra.Command{
			Use:     spec.use,
			Short:   spec.short,
			Long:    spec.long,
			Example: spec.example,
			Args:    spec.args,
			RunE: func(cmd *cobra.Command, args []string) error {
				if spec.spansInstances {
					if spec.run != nil {
						return spec.run(cmd, "", args)
					}
					return stubErr(spec.verb, "")
				}
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
		if spec.metadata.SupportsJSON {
			c.Flags().Bool("json", false, "print machine-readable JSON")
		}
		setCommandMetadata(c, spec.metadata)

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
// `bivouac session` wants.
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
	return identity.InstanceName(cmd.Context(), cwd, repoFlag, positional, nameFlag)
}

func stubErr(verb, name string) error {
	return fmt.Errorf("%s: not implemented yet (instance %q)", verb, name)
}
