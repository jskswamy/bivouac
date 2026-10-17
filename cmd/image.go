package cmd

import "github.com/spf13/cobra"

// newImageCmd builds the `bivouac image` group: the user's own cloud
// images, built through a generated Packer bundle. Like `secrets`, none
// of its verbs touch an instance or its state.
func newImageCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "image",
		Short: "Build and manage your own cloud images",
		Long: "A bivouac image is a DigitalOcean snapshot with Nix, your login shell and\n" +
			"your configuration's store closure already in place, so `bivouac up` from\n" +
			"it skips the slow part of first boot. Images are built by Packer from a\n" +
			"generated bundle you can also commit and run yourself.",
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	c.AddCommand(newImageGenerateCmd())
	c.AddCommand(newImageBuildCmd())
	c.AddCommand(newImageListCmd())
	c.AddCommand(newImageTagCmd())
	c.AddCommand(newImagePruneCmd())
	return c
}
