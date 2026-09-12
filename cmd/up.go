package cmd

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jskswamy/cloudlab/internal/config"
	"github.com/jskswamy/cloudlab/internal/lifecycle"
)

func newUpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "up [name]",
		Short: "Create the VM, provision it, and bring the instance fully live",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, name, err := instanceIdentity(cmd, args)
			if err != nil {
				return err
			}

			cloudlabPath := filepath.Join(root, "cloudlab.pkl")
			cfg, err := resolveOrInit(cmd, cloudlabPath, root)
			if err != nil {
				return err
			}

			ctx := progressCtx(cmd)
			// Resolved before the confirmation prompt, as the inline env
			// read this replaces was: there is no point asking whether to
			// create an instance that cannot be created. The cost is that a
			// secrets-file token is decrypted even when the answer turns out
			// to be no -- the cheaper order of the two, since up is confirmed
			// far more often than it is aborted.
			p, err := resolveProvider(ctx)
			if err != nil {
				return fmt.Errorf("%w (needed to create instance %q)", err, name)
			}

			ok, err := confirm(cmd, upSummary(name, cfg))
			if err != nil {
				return err
			}
			if !ok {
				cmd.Println("Aborted.")
				return nil
			}

			if err := lifecycle.Up(ctx, p, lifecycle.DefaultSteps(), name, cloudlabPath, root); err != nil {
				return err
			}
			cmd.Printf("Instance %s is up\n", name)
			return nil
		},
	}
	return cmd
}

// resolveOrInit loads the project's config, falling back to the
// interactive flow when the repository has none yet and then loading
// what that wrote.
//
// This does not contradict the earlier decision that cloudlab.pkl must
// exist with no implicit fallback: the file still exists before
// Reconcile runs. Creating it by asking is only friendlier than
// refusing -- and refusing is still what happens with no terminal to
// ask on, because a form blocked on stdin that never arrives looks
// exactly like a hang.
func resolveOrInit(cmd *cobra.Command, cloudlabPath, root string) (config.Config, error) {
	cfg, err := config.Resolve(cmd.Context(), cloudlabPath)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}
	if terr := requireTerminal(isInteractive()); terr != nil {
		return config.Config{}, fmt.Errorf("no cloudlab.pkl in %s: %w", root, terr)
	}
	cmd.Printf("No cloudlab.pkl in %s yet — let's create one.\n", root)
	if err := runInitFlow(cmd.Context(), cmd.OutOrStdout(), newFormPrompter(), root); err != nil {
		return config.Config{}, err
	}
	return config.Resolve(cmd.Context(), cloudlabPath)
}

// upSummary describes the instance up is about to create, for
// confirmation before anything billable happens.
func upSummary(name string, cfg config.Resolved) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This will create instance %q:\n", name)
	fmt.Fprintf(&b, "  Region:   %s\n", cfg.Region)
	fmt.Fprintf(&b, "  Size:     %s\n", cfg.Size)
	fmt.Fprintf(&b, "  Template: %s\n", cfg.Template)
	if cfg.Image != "" {
		fmt.Fprintf(&b, "  Image:    %s\n", cfg.Image)
	}
	b.WriteString("Proceed? [y/N]: ")
	return b.String()
}
