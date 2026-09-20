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
			// Named for the same reason up names it in its prompt: root is
			// the main checkout, so a bivouac.pkl beside a caller standing
			// in a linked worktree is not the one being applied. provision
			// has no confirmation to carry the path, so it says it here.
			cmd.Printf("Reconciling %s from %s\n", name, bivouacPath)
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
