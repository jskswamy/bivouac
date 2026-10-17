package image

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/xdg"
)

// BuildOptions is one managed build: the bundle to generate, the token
// Packer gets, and the retention to apply afterwards.
type BuildOptions struct {
	Bundle       BundleOptions
	Token        string
	KeepPrevious int
	Prune        bool
	Progress     io.Writer
}

// BuildDeps are Build's side effects on the cloud, Packer and the config
// file, injected so the sequencing can be tested without any of them.
type BuildDeps struct {
	Images   provider.ImageManager
	Packer   func(ctx context.Context, dir, token string, progress io.Writer) (Artifact, error)
	SetImage func(path, ref string) (config.EditOutcome, error)
}

// BuildResult is what a build produced. It is filled in as far as the
// build got, so a caller handed an error can still say which image now
// exists and what retention did.
type BuildResult struct {
	ImageID, Reference, ConfigPath, BundleDir string
	Writeback                                 config.EditOutcome
	// Snippet is the line for the user to add when Writeback did not.
	Snippet string
	Prune   *PruneResult
}

// Build generates the bundle, runs Packer, checks the snapshot it made
// is one a launch would accept, points the selected config at the tag,
// and then applies retention. Errors are *OpError.
//
// The order is what keeps a failed build from costing the user their
// working image: retention runs only after the new snapshot has been
// verified through the provider, so a build that died part-way, or one
// whose snapshot lacks a marker and would never be launched, cannot
// push the last good image out of the kept set. The new image is the
// prune's protected ID, so retention never deletes it either.
//
// A writeback failure does not skip retention: the image is built and
// verified either way, and the user retries the edit by hand, not the
// build.
func Build(ctx context.Context, d BuildDeps, o BuildOptions) (BuildResult, error) {
	progress := o.Progress
	if progress == nil {
		progress = io.Discard
	}
	tag := o.Bundle.Tag
	res := BuildResult{
		Reference:  TagRef(tag),
		ConfigPath: o.Bundle.Selected.Path,
		BundleDir:  o.Bundle.OutputDir,
	}
	// Checked before anything is generated or built: discovering a bad
	// retention value only after a twenty-minute build would waste it.
	if o.KeepPrevious < 0 {
		return res, &OpError{Phase: "generate", Err: fmt.Errorf("keep-previous must be zero or more, got %d", o.KeepPrevious)}
	}

	bundle, err := Generate(o.Bundle)
	if err != nil {
		return res, &OpError{Phase: "generate", Err: err}
	}
	art, err := d.Packer(ctx, bundle.Dir, o.Token, progress)
	if err != nil {
		var op *OpError
		if !errors.As(err, &op) {
			op = &OpError{Phase: "packer-build", Err: err}
		}
		return res, op
	}
	res.ImageID = art.ID
	created := func(phase string, err error) *OpError {
		return &OpError{Phase: phase, ImageCreated: true, ImageID: art.ID, Err: err}
	}

	scope := Scope{Tag: tag, Arch: bundle.Metadata.Arch, Region: bundle.Metadata.Region}
	_, _ = fmt.Fprintf(progress, "Verifying image %s\n", art.ID)
	if err := verifyArtifact(ctx, d.Images, art.ID, scope, bundle.Metadata.Shell); err != nil {
		return res, created("verify", err)
	}

	var failed *OpError
	res.Writeback, err = d.SetImage(res.ConfigPath, res.Reference)
	if err != nil || !res.Writeback.Written {
		res.Snippet = fmt.Sprintf("image = %q", res.Reference)
	}
	if err != nil {
		failed = created("writeback", err)
	}

	if o.Prune {
		pr := prune(ctx, d.Images, scope, o.KeepPrevious, art.ID, progress)
		res.Prune = &pr
		if pr.Err != nil && failed == nil {
			failed = created("prune", pr.Err)
		}
		if failed != nil {
			failed.Cleanup = pr.Summary()
		}
	}
	if failed != nil {
		return res, failed
	}
	return res, nil
}

// verifyArtifact re-reads the snapshot through the provider rather than
// trusting the manifest: Packer can report an artifact whose tagging
// failed or which is still being processed, and an image a launch would
// reject must not be counted as the current build. The shell is checked
// too, because Eligible leaves it to launch resolution and a build that
// tagged a different shell than it was asked to bake is broken.
func verifyArtifact(ctx context.Context, m provider.ImageManager, id string, s Scope, shell string) error {
	img, err := m.GetImage(ctx, id)
	if err != nil {
		return fmt.Errorf("reading image %s back from the provider: %w", id, err)
	}
	if why := ineligible(img, s); why != "" {
		return fmt.Errorf("image %s is not a usable build: %s", id, why)
	}
	if mk, _ := ParseMarkers(img.Tags); mk.Shell != shell {
		return fmt.Errorf("image %s is not a usable build: tagged for shell %s, built for %s", id, mk.Shell, shell)
	}
	return nil
}

func prune(ctx context.Context, m provider.ImageManager, s Scope, keep int, protected string, progress io.Writer) PruneResult {
	imgs, err := m.ListImages(ctx, s.Tag)
	if err != nil {
		return PruneResult{Err: fmt.Errorf("listing images tagged %s: %w", s.Tag, err)}
	}
	plan, err := PlanPrune(imgs, s, keep, protected)
	if err != nil {
		return PruneResult{Err: err}
	}
	for _, img := range plan.Delete {
		_, _ = fmt.Fprintf(progress, "Pruning image %s\n", img.ID)
	}
	return ExecutePrune(ctx, m, plan)
}

// ManagedBundleDir is a fresh directory name for one managed build's
// bundle. Each build gets its own and keeps it afterwards, so the
// bundle behind any image -- including a failed one -- can still be
// inspected, and a rebuild never trips over the previous bundle. The
// timestamp sorts them; the random suffix separates two builds started
// in the same second, as concurrent CI jobs can be.
func ManagedBundleDir() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	name := time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
	return xdg.Path(xdg.Cache, "images", name)
}
