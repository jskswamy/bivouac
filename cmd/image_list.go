package cmd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jskswamy/bivouac/internal/image"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/spf13/cobra"
)

type providerResolver func(context.Context) (provider.Provider, error)

type imageListRow struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Tags    []string  `json:"tags"`
	Created time.Time `json:"created"`
	Regions []string  `json:"regions"`
	Status  string    `json:"status"`
	Managed bool      `json:"managed"`
}

func newImageListCmd() *cobra.Command { return newImageListCmdWithProvider(resolveProvider) }

func newImageListCmdWithProvider(resolve providerResolver) *cobra.Command {
	c := &cobra.Command{
		Use:   "list",
		Short: "List your images and whether bivouac manages them",
		Long: "List the account's private images, newest first, with their tags, build\n" +
			"time, the regions they are available in and a status:\n\n" +
			"  managed    a ready bivouac image; `up` can launch it and prune may delete it\n" +
			"  not ready  a bivouac image still being built or that failed\n" +
			"  foreign    anything else, even when it carries your tag; never launched or pruned\n\n" +
			"Works by tag alone and reads no project config, so it runs anywhere.",
		Example: "bivouac image list\n" +
			"bivouac image list --tag my-dev\n" +
			"bivouac image list --tag my-dev --region blr1 --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return runImageList(cmd, resolve) },
	}
	c.Flags().String("tag", "", "only images carrying this tag")
	c.Flags().String("region", "", "only images built in or available in this region")
	c.Flags().String("arch", "", "only bivouac images built for this architecture, e.g. x86_64")
	c.Flags().Bool("json", false, "print machine-readable JSON")
	setCommandMetadata(c, commandMetadata{
		Title:        "List bivouac images",
		Network:      true,
		SupportsJSON: true,
		Related:      []string{"bivouac_image_tag", "bivouac_image_prune"},
	})
	return c
}

func runImageList(cmd *cobra.Command, resolve providerResolver) error {
	tag, _ := cmd.Flags().GetString("tag")
	region, _ := cmd.Flags().GetString("region")
	arch, _ := cmd.Flags().GetString("arch")
	if err := rejectProjectFlags(cmd, "list"); err != nil {
		return err
	}
	ctx := cmd.Context()
	m, err := imageManagerFrom(ctx, resolve)
	if err != nil {
		return err
	}
	imgs, err := m.ListImages(ctx, tag)
	if err != nil {
		return err
	}
	image.SortNewest(imgs)
	rows := []imageListRow{}
	for _, img := range imgs {
		mk, marked := image.ParseMarkers(img.Tags)
		// The tag is re-checked here because ListImages with no tag is
		// every private image, and a filter that silently stopped
		// filtering would look exactly like an account full of matches.
		if tag != "" && !slices.Contains(img.Tags, tag) {
			continue
		}
		if region != "" && mk.Region != region && !slices.Contains(img.Regions, region) {
			continue
		}
		if arch != "" && (!marked || mk.Arch != arch) {
			continue
		}
		status := imageStatus(img)
		rows = append(rows, imageListRow{
			ID: img.ID, Name: img.Name, Tags: nonNil(img.Tags), Created: img.Created,
			Regions: nonNil(img.Regions), Status: status, Managed: status == "managed",
		})
	}
	if jsonFlag(cmd) {
		return writeJSON(cmd, rows)
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "no images")
		return err
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tTAGS\tCREATED\tREGIONS\tSTATUS")
	for _, r := range rows {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.ID, orDash(userTags(r.Tags)),
			r.Created.UTC().Format(time.RFC3339), orDash(r.Regions), r.Status)
	}
	return w.Flush()
}

// imageStatus applies the launch rule to each image in its own build
// scope, so "managed" means exactly what `up` and prune would accept.
// An image with markers that is not ready is reported as such rather
// than as foreign: it is ours, and most likely a build still running.
func imageStatus(img provider.Image) string {
	mk, ok := image.ParseMarkers(img.Tags)
	switch {
	case !ok:
		return "foreign"
	case img.Status != "available":
		return "not ready"
	case image.Eligible(img, image.Scope{Arch: mk.Arch, Region: mk.Region}):
		return "managed"
	}
	return "foreign"
}

// userTags drops the marker tags from the table, where they would bury
// the user's own; --json keeps every tag.
func userTags(tags []string) []string {
	var out []string
	for _, t := range tags {
		if !strings.HasPrefix(t, "bivouac-") {
			out = append(out, t)
		}
	}
	return out
}

func orDash(s []string) string {
	if len(s) == 0 {
		return "-"
	}
	return strings.Join(s, ",")
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// rejectProjectFlags refuses --repo and --name on the image verbs that
// work by tag and ID. Neither chooses anything for them -- the token is
// the account's, not the repo's -- so accepting them would only let a
// user believe a prune was confined to one project when it was not.
func rejectProjectFlags(cmd *cobra.Command, verb string) error {
	for _, f := range []string{"repo", "name"} {
		if v, _ := cmd.Flags().GetString(f); v != "" {
			return fmt.Errorf("--%s does not apply to image %s: it works by tag and image ID across the account, not per project", f, verb)
		}
	}
	return nil
}

func imageManagerFrom(ctx context.Context, resolve providerResolver) (provider.ImageManager, error) {
	p, err := resolve(ctx)
	if err != nil {
		return nil, err
	}
	m, ok := p.(provider.ImageManager)
	if !ok {
		return nil, errors.New("provider does not support images")
	}
	return m, nil
}
