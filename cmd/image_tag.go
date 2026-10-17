package cmd

import (
	"errors"
	"fmt"

	"github.com/jskswamy/bivouac/internal/image"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/spf13/cobra"
)

type imageTagResult struct {
	ImageID   string `json:"image_id"`
	Tag       string `json:"tag"`
	Reference string `json:"reference"`
}

func newImageTagCmd() *cobra.Command { return newImageTagCmdWithProvider(resolveProvider) }

func newImageTagCmdWithProvider(resolve providerResolver) *cobra.Command {
	c := &cobra.Command{
		Use:   "tag <image-id> <tag>",
		Short: "Add your tag to an existing bivouac image",
		Long: "Add a tag to a ready bivouac image, so `image = \"tag:<tag>\"` can launch\n" +
			"it -- to promote a build to \"stable\", say. Only images a bivouac build\n" +
			"made are accepted: tagging is how an image enters a tag's retention, so\n" +
			"an arbitrary snapshot is refused rather than silently adopted into\n" +
			"pruning. The image keeps its other tags.",
		Example: "bivouac image tag 123456789 stable\n" +
			"bivouac image tag 123456789 stable --json",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error { return runImageTag(cmd, resolve, args[0], args[1]) },
	}
	c.Flags().Bool("json", false, "print machine-readable JSON")
	setCommandMetadata(c, commandMetadata{
		Title:        "Tag a bivouac image",
		Network:      true,
		SupportsJSON: true,
		SideEffects:  []string{"adds a DigitalOcean image tag"},
		Related:      []string{"bivouac_image_list"},
		Arguments: []argumentMetadata{
			{Name: "image-id", Required: true, Description: "numeric image ID, as image list shows it", Schema: map[string]any{"type": "string"}},
			{Name: "tag", Required: true, Description: "tag to add", Schema: map[string]any{"type": "string"}},
		},
	})
	return c
}

func runImageTag(cmd *cobra.Command, resolve providerResolver, id, tag string) error {
	if err := image.ValidUserTag(tag); err != nil {
		return err
	}
	if err := rejectProjectFlags(cmd, "tag"); err != nil {
		return err
	}
	ctx := cmd.Context()
	m, err := imageManagerFrom(ctx, resolve)
	if err != nil {
		return err
	}
	img, err := m.GetImage(ctx, id)
	if errors.Is(err, provider.ErrImageNotFound) {
		return fmt.Errorf("image %s not found", id)
	}
	if err != nil {
		return err
	}
	mk, ok := image.ParseMarkers(img.Tags)
	switch {
	case !ok:
		return fmt.Errorf("image %s is not a bivouac image (it lacks the marker tags a bivouac build adds); "+
			"image tag only tags bivouac builds, so it never adopts another image into pruning", id)
	case img.Status != "available":
		return fmt.Errorf("image %s is %q, not available; tag it once it is ready", id, img.Status)
	case !image.Eligible(img, image.Scope{Arch: mk.Arch, Region: mk.Region}):
		return fmt.Errorf("image %s carries bivouac markers but is not one this bivouac can launch "+
			"(type %q, format %d, available in %v)", id, img.Type, mk.Format, img.Regions)
	}
	if err := m.TagImage(ctx, id, tag); err != nil {
		return err
	}
	res := imageTagResult{ImageID: id, Tag: tag, Reference: "tag:" + tag}
	if jsonFlag(cmd) {
		return writeJSON(cmd, res)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Tagged image %s with %s; launch it with image = %q\n", id, tag, res.Reference)
	return err
}
