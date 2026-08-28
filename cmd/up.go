package cmd

import (
	"fmt"
	"os"

	"github.com/jskswamy/cloudlab/internal/identity"
	"github.com/spf13/cobra"
)

func newUpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "up [name]",
		Short: "Create the VM, provision it, and bring the instance fully live",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoFlag, _ := cmd.Flags().GetString("repo")
			nameFlag, _ := cmd.Flags().GetString("name")

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			root, err := identity.RepoRoot(cwd, repoFlag)
			if err != nil {
				return err
			}

			name := nameFlag
			if len(args) > 0 {
				name = args[0]
			}
			if name == "" {
				name, err = identity.DeriveName(root)
				if err != nil {
					return err
				}
			}

			return fmt.Errorf("up: not implemented yet (instance %q, repo %q)", name, root)
		},
	}
	cmd.Flags().String("template", "python", "provisioning template (python or docker)")
	return cmd
}
