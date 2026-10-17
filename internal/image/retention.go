package image

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jskswamy/bivouac/internal/provider"
)

// PrunePlan is what retention would do in one scope. Keep is newest
// first; Delete is oldest first, so an interrupted run has removed the
// least useful images before the ones closest to the retained set.
type PrunePlan struct {
	Scope        Scope
	Keep, Delete []provider.Image
}

// PlanPrune keeps the newest 1+keepPrevious eligible images in s and
// proposes deleting the rest. It is pure: dry-run and the confirmation
// preview are this plan, so neither can mutate anything.
//
// Only Eligible images are candidates. An image that is still pending,
// failed, foreign (sharing the user tag without the markers) or from
// another scope is neither deleted nor counted as retained, so it can
// never push a real build out of the kept set. protectedID, the image a
// build just produced, is kept whatever its position.
func PlanPrune(imgs []provider.Image, s Scope, keepPrevious int, protectedID string) (PrunePlan, error) {
	if keepPrevious < 0 {
		return PrunePlan{}, fmt.Errorf("keep-previous must be zero or more, got %d", keepPrevious)
	}
	// Eligible skips the tag check for an empty tag, so a scope missing
	// one would put every tag's builds in this arch and region up for
	// deletion. Callers validate first; this is the backstop.
	if err := ValidUserTag(s.Tag); err != nil {
		return PrunePlan{}, fmt.Errorf("prune scope: %w", err)
	}
	if s.Arch == "" || s.Region == "" {
		return PrunePlan{}, fmt.Errorf("prune scope for tag %s needs both an arch and a region", s.Tag)
	}
	var ok []provider.Image
	for _, img := range imgs {
		if Eligible(img, s) {
			ok = append(ok, img)
		}
	}
	SortNewest(ok)
	plan := PrunePlan{Scope: s}
	keep := 1 + keepPrevious
	for i, img := range ok {
		if i < keep || img.ID == protectedID {
			plan.Keep = append(plan.Keep, img)
		} else {
			plan.Delete = append(plan.Delete, img)
		}
	}
	slices.Reverse(plan.Delete)
	return plan, nil
}

// GroupScopes finds the scopes an explicit prune applies retention to
// independently: each build region and architecture that has eligible
// images carrying tag, narrowed by region and arch when they are set.
// Grouping by the markers rather than by the caller's defaults keeps a
// prune from comparing a blr1 build against an nyc3 one.
func GroupScopes(imgs []provider.Image, tag, region, arch string) []Scope {
	var out []Scope
	for _, img := range imgs {
		mk, ok := ParseMarkers(img.Tags)
		if !ok {
			continue
		}
		s := Scope{Tag: tag, Arch: mk.Arch, Region: mk.Region}
		if (region != "" && s.Region != region) || (arch != "" && s.Arch != arch) {
			continue
		}
		if Eligible(img, s) && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b Scope) int {
		return cmp.Or(cmp.Compare(a.Region, b.Region), cmp.Compare(a.Arch, b.Arch))
	})
	return out
}

// PruneResult reports which planned deletions happened. Pending holds
// every planned image not yet deleted when the run stopped, so a caller
// can say exactly what a retry would still remove.
type PruneResult struct {
	Deleted, Pending []string
	Err              error
}

// Summary is the counts an OpError's Cleanup reports, in the one wording
// both build and an explicit prune use.
func (r PruneResult) Summary() string {
	return fmt.Sprintf("deleted %d, pending %d", len(r.Deleted), len(r.Pending))
}

// ExecutePrune deletes the plan's images in order, re-reading each one
// first. The plan may be stale by the time it runs (a confirmation
// prompt, a concurrent build or a hand edit in the console), so a
// candidate that is no longer eligible in the plan's scope stops the
// run instead of being deleted: retention never removes something it
// did not show the user. An image that is already gone counts as
// deleted, since the outcome the plan wanted holds.
func ExecutePrune(ctx context.Context, m provider.ImageManager, p PrunePlan) PruneResult {
	var r PruneResult
	for i, cand := range p.Delete {
		stop := func(err error) PruneResult {
			r.Pending = IDs(p.Delete[i:])
			r.Err = err
			return r
		}
		cur, err := m.GetImage(ctx, cand.ID)
		switch {
		case errors.Is(err, provider.ErrImageNotFound):
			r.Deleted = append(r.Deleted, cand.ID)
			continue
		case err != nil:
			return stop(fmt.Errorf("re-check image %s: %w", cand.ID, err))
		}
		if why := ineligible(cur, p.Scope); why != "" {
			return stop(fmt.Errorf("image %s changed since the prune was planned (%s); stopping", cand.ID, why))
		}
		if err := m.DeleteImage(ctx, cand.ID); err != nil && !errors.Is(err, provider.ErrImageNotFound) {
			return stop(fmt.Errorf("delete image %s: %w", cand.ID, err))
		}
		r.Deleted = append(r.Deleted, cand.ID)
	}
	return r
}

// IDs lists the images' IDs, as an empty rather than nil slice so that
// --json output says [] for none.
func IDs(imgs []provider.Image) []string {
	out := []string{}
	for _, img := range imgs {
		out = append(out, img.ID)
	}
	return out
}
