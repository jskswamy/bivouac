package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/identity"
	"github.com/jskswamy/bivouac/internal/image"
	"github.com/spf13/cobra"
)

const defaultImageOutput = "./bivouac-image"

type imageGenerateResult struct {
	OutputDir  string         `json:"output_dir"`
	ConfigPath string         `json:"config_path"`
	ConfigKind string         `json:"config_kind"`
	Tag        string         `json:"tag"`
	Metadata   image.Metadata `json:"metadata"`
}

func newImageGenerateCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "generate",
		Short: "Write a standalone Packer bundle that builds an image",
		Long: "Write a self-contained Packer bundle for the selected config into --output.\n\n" +
			"The config is --config if given, else the project's bivouac.pkl (from the\n" +
			"current repository or --repo), else your personal base.pkl. Generating\n" +
			"needs only the pkl CLI: no Packer, Nix, git or DigitalOcean token. The\n" +
			"bundle's README says how to build it with plain Packer.",
		Example: "bivouac image generate --tag my-dev\n" +
			"bivouac image generate --tag my-dev --output ./packer\n" +
			"bivouac image generate --config ./custom.pkl --tag my-dev --json",
		Args: cobra.NoArgs,
		RunE: runImageGenerate,
	}
	c.Flags().String("config", "", "config file to build from (default: project bivouac.pkl, else base.pkl)")
	c.Flags().String("tag", "", "your tag for the image, e.g. my-dev (launch it with image = \"tag:<tag>\")")
	c.Flags().String("output", defaultImageOutput, "directory to write the bundle into; must be empty or absent")
	c.Flags().Bool("json", false, "print machine-readable JSON")
	// Checked by cobra before RunE, so a missing tag is a usage error
	// reported before any config is loaded or directory created.
	_ = c.MarkFlagRequired("tag")
	setCommandMetadata(c, commandMetadata{
		Title:        "Generate a Packer bundle for a bivouac image",
		SupportsJSON: true,
		SideEffects:  []string{"writes bundle directory"},
		Related:      []string{"bivouac_up"},
	})
	return c
}

func runImageGenerate(cmd *cobra.Command, args []string) error {
	explicit, _ := cmd.Flags().GetString("config")
	tag, _ := cmd.Flags().GetString("tag")
	output, _ := cmd.Flags().GetString("output")
	repoFlag, _ := cmd.Flags().GetString("repo")
	nameFlag, _ := cmd.Flags().GetString("name")

	if err := checkConfigFlags(explicit, repoFlag, nameFlag, "generate"); err != nil {
		return err
	}
	if err := image.ValidUserTag(tag); err != nil {
		return err
	}

	projectDir, err := imageProjectDir(cmd, explicit, repoFlag)
	if err != nil {
		return err
	}
	sel, err := config.LoadSelected(cmd.Context(), explicit, projectDir)
	if err != nil {
		return err
	}
	outDir, err := filepath.Abs(output)
	if err != nil {
		return err
	}

	progress := cmd.ErrOrStderr()
	printf(progress, "Using %s config %s\n", sel.Kind, sel.Path)
	printf(progress, "Writing bundle to %s\n", outDir)
	bundle, err := image.Generate(image.BundleOptions{
		OutputDir:      outDir,
		Selected:       sel,
		Tag:            tag,
		BivouacVersion: cmd.Root().Version,
	})
	if err != nil {
		return err
	}

	if jsonFlag(cmd) {
		return writeJSON(cmd, imageGenerateResult{
			OutputDir:  bundle.Dir,
			ConfigPath: sel.Path,
			ConfigKind: string(sel.Kind),
			Tag:        tag,
			Metadata:   bundle.Metadata,
		})
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n\nBuild it with Packer from that directory:\n  packer init . && packer validate . && packer build .\n", bundle.Dir)
	return err
}

// checkConfigFlags refuses the flag combinations generate and build would
// otherwise silently ignore: --config names the file outright, so --repo
// cannot also choose it, and a bundle or image belongs to no instance.
func checkConfigFlags(explicit, repoFlag, nameFlag, verb string) error {
	if explicit != "" && repoFlag != "" {
		return errors.New("--config and --repo both choose the config; pass one")
	}
	if nameFlag != "" {
		return fmt.Errorf("--name does not apply to image %s: images belong to no instance", verb)
	}
	return nil
}

// imageProjectDir finds the repository whose bivouac.pkl is a candidate.
// An explicit config makes the question moot, and needing no repository
// is the point of generating from base.pkl, so outside one there is
// simply no project -- unless --repo named one, which must not be
// quietly dropped in favour of base.
func imageProjectDir(cmd *cobra.Command, explicit, repoFlag string) (string, error) {
	if explicit != "" {
		return "", nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root, err := identity.RepoRoot(cmd.Context(), cwd, repoFlag)
	if err != nil {
		if repoFlag != "" {
			return "", err
		}
		return "", nil
	}
	return root, nil
}
