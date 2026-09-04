package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jskswamy/cloudlab/internal/config"
	"github.com/jskswamy/cloudlab/internal/identity"
	"github.com/jskswamy/cloudlab/internal/lifecycle"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/provider/digitalocean"
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

			token := os.Getenv("DIGITALOCEAN_TOKEN")
			if token == "" {
				return fmt.Errorf("DIGITALOCEAN_TOKEN not set (needed to create instance %q)", name)
			}

			cloudlabPath := filepath.Join(root, "cloudlab.pkl")
			cfg, err := config.Resolve(cmd.Context(), cloudlabPath)
			if err != nil {
				return err
			}

			ok, err := confirm(cmd, upSummary(name, cfg))
			if err != nil {
				return err
			}
			if !ok {
				cmd.Println("Aborted.")
				return nil
			}

			p := digitalocean.New(token)
			ctx := provider.WithProgress(cmd.Context(), func(status string) {
				cmd.Printf("→ %s\n", status)
			})
			if err := lifecycle.Up(ctx, p, lifecycle.DefaultSteps(), name, cloudlabPath, root); err != nil {
				return err
			}
			cmd.Printf("Instance %s is up\n", name)
			return nil
		},
	}
	return cmd
}

// upSummary describes the instance up is about to create, for
// confirmation before anything billable happens.
func upSummary(name string, cfg config.Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This will create instance %q:\n", name)
	fmt.Fprintf(&b, "  Region:   %s\n", *cfg.Region)
	fmt.Fprintf(&b, "  Size:     %s\n", *cfg.Size)
	fmt.Fprintf(&b, "  Template: %s\n", *cfg.Template)
	if cfg.Image != "" {
		fmt.Fprintf(&b, "  Image:    %s\n", cfg.Image)
	}
	b.WriteString("Proceed? [y/N]: ")
	return b.String()
}
