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
	run              func(cmd *cobra.Command, name string, args []string) error
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
		use:   "sync <local-dir> [remote-dir]",
		short: "One-shot push of a directory outside the repo to the instance",
		verb:  "sync",
		args:  cobra.RangeArgs(1, 2),
		named: false,
		run:   runSync,
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
// stub/exit-code behavior are shared across all eight — only Use/Short
// text, the Args validator, and whether the first positional arg is the
// instance name differ per spec.
func newLookupCommands() []*cobra.Command {
	cmds := make([]*cobra.Command, 0, len(lookupCommandSpecs))
	for _, spec := range lookupCommandSpecs {
		cmds = append(cmds, &cobra.Command{
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
		})
	}
	return cmds
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
