package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	term "github.com/charmbracelet/x/term"
	"github.com/jskswamy/bivouac/internal/image"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/spf13/cobra"
)

type pruneGroup struct {
	Region string   `json:"region"`
	Arch   string   `json:"arch"`
	Keep   []string `json:"keep"`
	Delete []string `json:"delete"`
}

type imagePruneResult struct {
	Tag     string       `json:"tag"`
	DryRun  bool         `json:"dry_run"`
	Groups  []pruneGroup `json:"groups"`
	Deleted []string     `json:"deleted"`
	Pending []string     `json:"pending"`
}

// imagePruneError is the --json form of a prune that stopped part way:
// the OpError fields plus exactly which deletions happened and which a
// retry would still make.
type imagePruneError struct {
	Error string `json:"error"`
	*image.OpError
	Deleted []string `json:"deleted"`
	Pending []string `json:"pending"`
}

func newImagePruneCmd() *cobra.Command {
	return newImagePruneCmdWithProvider(resolveProvider, terminalIO)
}

// terminalIO is stricter than isInteractive: the preview and prompt go
// to the terminal and the answer comes from it, so a redirected stdout
// (a script capturing the output) is as unattended as a closed stdin.
func terminalIO() bool { return isInteractive() && term.IsTerminal(os.Stdout.Fd()) }

func newImagePruneCmdWithProvider(resolve providerResolver, interactive func() bool) *cobra.Command {
	c := &cobra.Command{
		Use:   "prune",
		Short: "Delete older images with a tag, keeping the newest",
		Long: "Delete older bivouac images carrying --tag, keeping the newest one plus\n" +
			"--keep-previous more in each build region and architecture. Without\n" +
			"--region and --arch every region and architecture the tag was built for\n" +
			"is pruned, each on its own, so a blr1 build never pushes out an nyc3 one.\n\n" +
			"Only ready bivouac images count: one still building, one that failed and\n" +
			"any foreign image that merely shares the tag are neither kept nor\n" +
			"deleted. The deletions are listed and confirmed first; outside a\n" +
			"terminal pass --yes, or --dry-run to only list them. Each image is\n" +
			"re-checked just before it is deleted, and the prune stops rather than\n" +
			"delete one that changed since it was listed.",
		Example: "bivouac image prune --tag my-dev --dry-run\n" +
			"bivouac image prune --tag my-dev --keep-previous 1\n" +
			"bivouac image prune --tag my-dev --region blr1 --arch x86_64 --yes --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return runImagePrune(cmd, resolve, interactive) },
	}
	c.Flags().String("tag", "", "tag whose older images to delete")
	c.Flags().Int("keep-previous", 1, "older images to keep besides the newest, per region and architecture; 0 keeps only the newest")
	c.Flags().Bool("dry-run", false, "list what would be deleted and delete nothing")
	c.Flags().Bool("yes", false, "delete without asking (required outside a terminal)")
	c.Flags().String("region", "", "only prune images built in this region")
	c.Flags().String("arch", "", "only prune images built for this architecture, e.g. x86_64")
	c.Flags().Bool("json", false, "print machine-readable JSON")
	_ = c.MarkFlagRequired("tag")
	setCommandMetadata(c, commandMetadata{
		Title:        "Prune older bivouac images",
		Network:      true,
		Destructive:  true,
		MayPrompt:    true,
		SupportsJSON: true,
		SideEffects:  []string{"deletes DigitalOcean snapshots"},
		Related:      []string{"bivouac_image_list", "bivouac_image_build"},
	})
	return c
}

func runImagePrune(cmd *cobra.Command, resolve providerResolver, interactive func() bool) error {
	tag, _ := cmd.Flags().GetString("tag")
	keep, _ := cmd.Flags().GetInt("keep-previous")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	yes, _ := cmd.Flags().GetBool("yes")
	region, _ := cmd.Flags().GetString("region")
	arch, _ := cmd.Flags().GetString("arch")

	switch {
	case dryRun && yes:
		return errors.New("--dry-run and --yes contradict each other; pass one")
	case keep < 0:
		return fmt.Errorf("--keep-previous must be zero or more, got %d", keep)
	}
	if err := image.ValidUserTag(tag); err != nil {
		return err
	}
	if err := rejectProjectFlags(cmd, "prune"); err != nil {
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
	res := imagePruneResult{Tag: tag, DryRun: dryRun, Groups: []pruneGroup{}, Deleted: []string{}, Pending: []string{}}
	var plans []image.PrunePlan
	for _, s := range image.GroupScopes(imgs, tag, region, arch) {
		plan, err := image.PlanPrune(imgs, s, keep, "")
		if err != nil {
			return err
		}
		plans = append(plans, plan)
		res.Groups = append(res.Groups, pruneGroup{Region: s.Region, Arch: s.Arch, Keep: imageIDs(plan.Keep), Delete: imageIDs(plan.Delete)})
		res.Pending = append(res.Pending, imageIDs(plan.Delete)...)
	}

	// The preview is the result of a human dry run, so it goes to stdout
	// then; with --json, stdout is reserved for the one JSON document.
	preview := cmd.OutOrStdout()
	if jsonFlag(cmd) {
		preview = cmd.ErrOrStderr()
	}
	printPrunePreview(preview, res)
	if dryRun || len(res.Pending) == 0 {
		return finishPrune(cmd, res)
	}
	if !yes {
		// Never wait on a prompt nobody can answer: a hang in CI is
		// worse than a refusal that names the flag to pass.
		if !interactive() {
			return errors.New("image prune needs confirmation and this is not a terminal: pass --yes to delete without asking, or --dry-run to only list")
		}
		printf(cmd.ErrOrStderr(), "Delete %d images? [y/N] ", len(res.Pending))
		ok, err := readYes(cmd)
		if err != nil {
			return err
		}
		if !ok {
			printf(cmd.ErrOrStderr(), "Nothing deleted.\n")
			return finishPrune(cmd, res)
		}
	}

	for i, plan := range plans {
		r := image.ExecutePrune(ctx, m, plan)
		res.Deleted = append(res.Deleted, r.Deleted...)
		if r.Err == nil {
			continue
		}
		res.Pending = r.Pending
		for _, rest := range plans[i+1:] {
			res.Pending = append(res.Pending, imageIDs(rest.Delete)...)
		}
		return reportPruneFailure(cmd, res, r.Err)
	}
	res.Pending = []string{}
	return finishPrune(cmd, res)
}

func printPrunePreview(w io.Writer, res imagePruneResult) {
	if len(res.Groups) == 0 {
		printf(w, "No ready bivouac images tagged %s.\n", res.Tag)
		return
	}
	for _, g := range res.Groups {
		del := "nothing"
		if len(g.Delete) > 0 {
			del = strings.Join(g.Delete, ", ")
		}
		printf(w, "%s %s in %s: keep %s; delete %s\n", res.Tag, g.Arch, g.Region, strings.Join(g.Keep, ", "), del)
	}
}

func finishPrune(cmd *cobra.Command, res imagePruneResult) error {
	if jsonFlag(cmd) {
		return writeJSON(cmd, res)
	}
	if len(res.Deleted) > 0 {
		printf(cmd.OutOrStdout(), "Deleted %s\n", strings.Join(res.Deleted, ", "))
	}
	return nil
}

// reportPruneFailure names both lists, because a prune that stopped
// part way has left the account in a state the user must know exactly
// to retry or clean up by hand.
func reportPruneFailure(cmd *cobra.Command, res imagePruneResult, cause error) error {
	op := &image.OpError{
		Phase:   "prune",
		Cleanup: fmt.Sprintf("deleted %d, pending %d", len(res.Deleted), len(res.Pending)),
		Err:     cause,
	}
	if jsonFlag(cmd) {
		if werr := writeJSON(cmd, imagePruneError{Error: op.Error(), OpError: op, Deleted: res.Deleted, Pending: res.Pending}); werr != nil {
			return errors.Join(op, werr)
		}
		return op
	}
	printf(cmd.ErrOrStderr(), "Deleted: %s\nPending: %s\n", orDash(res.Deleted), orDash(res.Pending))
	return op
}

func imageIDs(imgs []provider.Image) []string {
	out := []string{}
	for _, img := range imgs {
		out = append(out, img.ID)
	}
	return out
}
