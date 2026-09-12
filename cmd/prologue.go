package cmd

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/jskswamy/cloudlab/internal/identity"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/tui"
)

// instanceIdentity resolves the repository root and the instance name for a
// command that needs both -- the `cloudlab <verb> [name]` prologue that up
// and provision each had their own copy of.
func instanceIdentity(cmd *cobra.Command, args []string) (root, name string, err error) {
	repoFlag, _ := cmd.Flags().GetString("repo")
	nameFlag, _ := cmd.Flags().GetString("name")
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	positional := ""
	if len(args) > 0 {
		positional = args[0]
	}
	return identity.InstanceIdentity(cmd.Context(), cwd, repoFlag, positional, nameFlag)
}

// progressCtx returns cmd's context with progress reporting wired to cmd's
// own output.
//
// The arrow prefix is a presentation choice and stays in the cmd/tui layer;
// internal/provider carries the status text and knows nothing about how it
// is drawn. What this removes is nine hand-written copies of the same
// closure, not the ambient-progress seam itself, which is deliberate.
func progressCtx(cmd *cobra.Command) context.Context {
	return provider.WithProgress(cmd.Context(), func(status string) {
		cmd.Printf("%s%s\n", tui.ProgressPrefix, status)
	})
}
