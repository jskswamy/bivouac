package cmd

import (
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jskswamy/bivouac/internal/lifecycle"
)

func newProvisionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provision [name]",
		Short: "Reconcile home-manager with the current bivouac.pkl",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, name, err := instanceIdentity(cmd, args)
			if err != nil {
				return err
			}

			ctx := progressCtx(cmd)
			bivouacPath := filepath.Join(root, "bivouac.pkl")
			// The same step `up` runs, called rather than rebuilt: it was
			// a second copy of the label and the closure, and the only
			// reason this file reached for tui and reconcile directly.
			if err := lifecycle.DefaultSteps().Reconcile(ctx, name, bivouacPath); err != nil {
				return err
			}
			cmd.Printf("Provisioned %s\n", name)
			return nil
		},
	}
	return cmd
}
