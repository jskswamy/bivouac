package cmd

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "cloudlab",
		Short: "Ephemeral, declarative dev instances in the cloud",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	root.PersistentFlags().String("repo", "", "path to the repo to resolve identity from (overrides cwd-based resolution)")
	root.PersistentFlags().String("name", "", "instance name override")
	root.SilenceUsage = true

	root.AddCommand(newListCmd())
	root.AddCommand(newUpCmd())
	root.AddCommand(newProvisionCmd())
	root.AddCommand(newSecretsCmd())
	root.AddCommand(newLookupCommands()...)

	return root
}

// Execute builds a fresh command tree and runs it. Called once from main.
//
// The context is cancelled on SIGINT/SIGTERM so that every child process
// started with exec.CommandContext is reaped when cloudlab goes down.
// Without it those children outlive their parent: `connect`'s ssh forward
// was observed still holding a local port after cloudlab itself had exited,
// which then makes the next connect on that port fail for a reason nothing
// on screen explains. Ctrl-C in a terminal already signals the whole
// foreground process group and so appeared to work -- this covers every
// other way the process can be told to stop.
func Execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return newRootCmd().ExecuteContext(ctx)
}
