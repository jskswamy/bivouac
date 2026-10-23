package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/image"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/provider/gcloud"
	"github.com/spf13/cobra"
)

type imageBuildResult struct {
	ImageID       string   `json:"image_id"`
	Reference     string   `json:"reference"`
	ConfigPath    string   `json:"config_path"`
	ConfigKind    string   `json:"config_kind"`
	BundleDir     string   `json:"bundle_dir"`
	ConfigUpdated bool     `json:"config_updated"`
	WritebackSkip string   `json:"writeback_skipped,omitempty"`
	Snippet       string   `json:"snippet,omitempty"`
	Pruned        []string `json:"pruned"`
	PrunePending  []string `json:"prune_pending,omitempty"`
}

// imageBuildError is the --json form of a failed build: the OpError's
// phase and what already exists, plus where the bundle was kept.
type imageBuildError struct {
	Error string `json:"error"`
	*image.OpError
	BundleDir    string   `json:"bundle_dir,omitempty"`
	Pruned       []string `json:"pruned,omitempty"`
	PrunePending []string `json:"prune_pending,omitempty"`
}

type packerFunc func(ctx context.Context, dir, token string, progress io.Writer) (image.Artifact, error)

func newImageBuildCmd() *cobra.Command {
	return newImageBuildCmdWithProvider(
		nil,
		image.RunPacker,
	)
}

// The provider is built from a token rather than resolved on its own, as
// up's is, because the build needs the token twice -- for the provider
// and for Packer -- and resolving it twice could mean two security-key
// touches for one build. Both seams keep tests off a live account.
func newImageBuildCmdWithProvider(resolve func(context.Context, string) (provider.Provider, error), packer packerFunc) *cobra.Command {
	c := &cobra.Command{
		Use:   "build",
		Short: "Build an image with Packer and point your config at it",
		Long: "Generate a bundle for the selected config, build it with Packer, and check\n" +
			"the snapshot it made is one `bivouac up` will launch. Then set image =\n" +
			"\"tag:<tag>\" in that config, and delete older images with the same tag,\n" +
			"build region and architecture beyond --keep-previous.\n\n" +
			"The config is chosen as for `image generate`. Needs packer on PATH and a\n" +
			"DigitalOcean token (DIGITALOCEAN_TOKEN, else digitalocean_token in the\n" +
			"secrets file), which reaches Packer only through its environment and\n" +
			"is never written into the bundle. Each build's bundle is kept under the\n" +
			"bivouac cache, or in --output, for inspection. A config that is a\n" +
			"symlink, read-only or in the Nix store is never edited; the line to add\n" +
			"is printed instead.",
		Example: "bivouac image build --tag my-dev\n" +
			"bivouac image build --config ./custom.pkl --tag my-dev\n" +
			"bivouac image build --tag my-dev --keep-previous 3\n" +
			"bivouac image build --tag my-dev --no-prune --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImageBuild(cmd, resolve, packer)
		},
	}
	c.Flags().String("config", "", "config file to build from (default: project bivouac.pkl, else base.pkl)")
	c.Flags().String("tag", "", "your tag for the image, e.g. my-dev (launch it with image = \"tag:<tag>\")")
	c.Flags().String("output", "", "directory for the bundle; must be empty or absent (default: a new directory under the bivouac cache)")
	c.Flags().Int("keep-previous", 1, "older images to keep besides the new one; 0 keeps only the new one")
	c.Flags().Bool("no-prune", false, "keep every older image")
	c.Flags().Bool("json", false, "print machine-readable JSON")
	_ = c.MarkFlagRequired("tag")
	setCommandMetadata(c, commandMetadata{
		Title:        "Build a bivouac image with Packer",
		Network:      true,
		Destructive:  true,
		SupportsJSON: true,
		SideEffects: []string{
			"writes bundle directory", "creates DigitalOcean snapshot",
			"edits config image", "deletes older snapshots",
		},
		Related: []string{"bivouac_image_generate", "bivouac_up"},
	})
	return c
}

func runImageBuild(cmd *cobra.Command, resolve func(context.Context, string) (provider.Provider, error), packer packerFunc) error {
	explicit, _ := cmd.Flags().GetString("config")
	tag, _ := cmd.Flags().GetString("tag")
	output, _ := cmd.Flags().GetString("output")
	keep, _ := cmd.Flags().GetInt("keep-previous")
	noPrune, _ := cmd.Flags().GetBool("no-prune")
	repoFlag, _ := cmd.Flags().GetString("repo")
	nameFlag, _ := cmd.Flags().GetString("name")

	// All rejected before any config is loaded or token decrypted: each
	// would otherwise be silently ignored, and a build is too slow and
	// too costly to discover that afterwards.
	switch {
	case noPrune && cmd.Flags().Changed("keep-previous"):
		return errors.New("--no-prune and --keep-previous contradict each other; pass one")
	case keep < 0:
		return fmt.Errorf("--keep-previous must be zero or more, got %d", keep)
	}
	if err := checkConfigFlags(explicit, repoFlag, nameFlag, "build"); err != nil {
		return err
	}
	if err := image.ValidUserTag(tag); err != nil {
		return err
	}

	ctx := cmd.Context()
	projectDir, err := imageProjectDir(cmd, explicit, repoFlag)
	if err != nil {
		return err
	}
	sel, err := config.LoadSelected(ctx, explicit, projectDir)
	if err != nil {
		return err
	}
	outDir := output
	if outDir == "" {
		outDir, err = image.ManagedBundleDir()
	} else {
		outDir, err = filepath.Abs(outDir)
	}
	if err != nil {
		return err
	}
	var token string
	if sel.Config.Provider == "gcloud" {
		cleanup, err := setupGCloudCredentials(ctx)
		if err != nil {
			return err
		}
		if cleanup != nil {
			defer cleanup()
		}
	} else {
		var err error
		token, err = resolveToken(ctx)
		if err != nil {
			return err
		}
	}
	images, err := imageManagerFrom(ctx, func(ctx context.Context) (provider.Provider, error) {
		if resolve != nil {
			return resolve(ctx, token)
		}
		return resolveProviderForConfig(ctx, sel.Config)
	})
	if err != nil {
		return err
	}

	progress := cmd.ErrOrStderr()
	printf(progress, "Using %s config %s\n", sel.Kind, sel.Path)
	printf(progress, "Writing bundle to %s\n", outDir)
	printf(progress, "%s\n", retentionPolicy(tag, sel.Config, keep, !noPrune))

	res, err := image.Build(ctx, image.BuildDeps{Images: images, Packer: packer, SetImage: config.SetImage}, image.BuildOptions{
		Bundle: image.BundleOptions{
			OutputDir:      outDir,
			Selected:       sel,
			Tag:            tag,
			BivouacVersion: cmd.Root().Version,
		},
		Token:        token,
		KeepPrevious: keep,
		Prune:        !noPrune,
		Progress:     progress,
	})
	if res.Snippet != "" && res.ImageID != "" {
		printf(progress, "warning: %s was not edited (%s). Add this line to it yourself:\n  %s\n",
			res.ConfigPath, writebackWhy(res.Writeback, err), res.Snippet)
	}
	if err != nil {
		return reportBuildFailure(cmd, res, err)
	}

	out := imageBuildResult{
		ImageID:       res.ImageID,
		Reference:     res.Reference,
		ConfigPath:    res.ConfigPath,
		ConfigKind:    string(sel.Kind),
		BundleDir:     res.BundleDir,
		ConfigUpdated: res.Writeback.Written,
		WritebackSkip: res.Writeback.Reason,
		Snippet:       res.Snippet,
		Pruned:        []string{},
	}
	if res.Prune != nil {
		out.Pruned = append(out.Pruned, res.Prune.Deleted...)
	}
	if jsonFlag(cmd) {
		return writeJSON(cmd, out)
	}
	updated := "updated"
	if !out.ConfigUpdated {
		updated = "not updated: " + out.WritebackSkip
	}
	pruned := "none"
	if len(out.Pruned) > 0 {
		pruned = strings.Join(out.Pruned, ", ")
	} else if noPrune {
		pruned = "skipped (--no-prune)"
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Built image %s\n  reference: %s\n  config:    %s (%s)\n  bundle:    %s\n  pruned:    %s\n",
		out.ImageID, out.Reference, out.ConfigPath, updated, out.BundleDir, pruned)
	return err
}

// retentionPolicy states up front what the build will delete, since it
// does so without a second prompt once the image is verified.
func retentionPolicy(tag string, cfg config.Resolved, keep int, prune bool) string {
	if !prune {
		return fmt.Sprintf("Retention: off (--no-prune); older %s images are kept", tag)
	}
	arch := cfg.Arch
	if arch == "" {
		arch = image.SupportedArch
	}
	return fmt.Sprintf("Retention: after a verified build, keep the newest %d %s images built in %s for %s "+
		"(this one and %d previous) and delete older ones; --no-prune keeps them all",
		keep+1, tag, cfg.Region, arch, keep)
}

func writebackWhy(o config.EditOutcome, err error) string {
	var op *image.OpError
	if errors.As(err, &op) && op.Phase == "writeback" {
		return op.Err.Error()
	}
	switch o.Reason {
	case config.SkipSymlink:
		return "it is a symlink, likely managed by Home Manager"
	case config.SkipNixStore:
		return "it is in the Nix store"
	case config.SkipReadOnly:
		return "it is read-only"
	}
	return o.Reason
}

// reportBuildFailure says what exists despite the failure. A build can
// fail after the snapshot was made -- in verification, writeback or
// retention -- and the user is paying for that image whether or not the
// command succeeded, so its ID and the bundle are always named.
func reportBuildFailure(cmd *cobra.Command, res image.BuildResult, err error) error {
	var op *image.OpError
	if !errors.As(err, &op) {
		return err
	}
	if res.BundleDir != "" {
		printf(cmd.ErrOrStderr(), "Bundle kept at %s\n", res.BundleDir)
	}
	if jsonFlag(cmd) {
		out := imageBuildError{Error: err.Error(), OpError: op, BundleDir: res.BundleDir}
		if res.Prune != nil {
			out.Pruned, out.PrunePending = res.Prune.Deleted, res.Prune.Pending
		}
		if werr := writeJSON(cmd, out); werr != nil {
			return errors.Join(err, werr)
		}
	}
	return err
}

func setupGCloudCredentials(ctx context.Context) (func(), error) {
	creds, err := gcloud.ResolveCredentials(ctx)
	if err != nil {
		return nil, err
	}
	return creds.SetupEnv()
}
