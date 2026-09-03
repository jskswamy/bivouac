package cmd

import (
	"os"
	"path/filepath"

	"github.com/jskswamy/cloudlab/internal/identity"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/reconcile"
	"github.com/spf13/cobra"
)

func newProvisionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provision [name]",
		Short: "Reconcile home-manager with the current cloudlab.pkl",
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

			ctx := provider.WithProgress(cmd.Context(), func(status string) {
				cmd.Printf("→ %s\n", status)
			})
			cloudlabPath := filepath.Join(root, "cloudlab.pkl")
			if err := reconcile.Reconcile(ctx, name, cloudlabPath); err != nil {
				return err
			}
			cmd.Printf("Provisioned %s\n", name)
			return nil
		},
	}
	return cmd
}
