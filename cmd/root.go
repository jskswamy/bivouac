package cmd

import "github.com/spf13/cobra"

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
func Execute() error {
	return newRootCmd().Execute()
}
