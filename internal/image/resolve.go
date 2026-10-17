package image

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jskswamy/bivouac/internal/provider"
)

const tagRefPrefix = "tag:"

// Request is a launch's image reference together with the target it
// must be compatible with.
type Request struct{ Ref, Arch, Region, Size, Shell string }

// Resolution is the concrete image a launch uses.
type Resolution struct {
	Requested string // the config string as written
	Image     string // what InstanceSpec.Image gets: slug or numeric ID
	Baked     bool
	Shell     string // the baked shell; "" when not baked
}

// Resolve turns the config image reference into a concrete image before
// anything billable happens. A tag: reference picks the newest eligible
// image, a positive integer is an image ID validated by the same rules,
// and anything else is a stock slug passed through untouched.
//
// m may be nil; it is only consulted for tag: and numeric refs, so a
// stock launch neither needs an image-capable provider nor makes a call.
func Resolve(ctx context.Context, m provider.ImageManager, req Request) (Resolution, error) {
	tag, isTag := strings.CutPrefix(req.Ref, tagRefPrefix)
	n, err := strconv.Atoi(req.Ref)
	isID := !isTag && err == nil && n > 0
	if !isTag && !isID {
		return Resolution{Requested: req.Ref, Image: req.Ref}, nil
	}
	if isTag {
		if err := ValidUserTag(tag); err != nil {
			return Resolution{}, fmt.Errorf("image %q: %w", req.Ref, err)
		}
	}
	if m == nil {
		return Resolution{}, fmt.Errorf("image %q: provider does not support custom images", req.Ref)
	}

	var img provider.Image
	if isTag {
		img, err = newestTagged(ctx, m, tag, req)
	} else {
		img, err = validatedID(ctx, m, req)
	}
	if err != nil {
		return Resolution{}, err
	}
	if err := checkDisk(ctx, m, img, req.Size); err != nil {
		return Resolution{}, err
	}
	markers, _ := ParseMarkers(img.Tags) // eligibility already required them
	return Resolution{Requested: req.Ref, Image: img.ID, Baked: true, Shell: markers.Shell}, nil
}

func newestTagged(ctx context.Context, m provider.ImageManager, tag string, req Request) (provider.Image, error) {
	all, err := m.ListImages(ctx, tag)
	if err != nil {
		return provider.Image{}, err
	}
	scope := Scope{Tag: tag, Arch: req.Arch, Region: req.Region}
	var ok []provider.Image
	for _, img := range all {
		if Eligible(img, scope) {
			ok = append(ok, img)
		}
	}
	if len(ok) == 0 {
		return provider.Image{}, fmt.Errorf("no ready Bivouac image tagged %q for arch %s in region %s", tag, req.Arch, req.Region)
	}
	SortNewest(ok)
	return ok[0], nil
}

func validatedID(ctx context.Context, m provider.ImageManager, req Request) (provider.Image, error) {
	img, err := m.GetImage(ctx, req.Ref)
	if err != nil {
		return provider.Image{}, fmt.Errorf("image %s: %w", req.Ref, err)
	}
	if why := ineligible(img, Scope{Arch: req.Arch, Region: req.Region}); why != "" {
		return provider.Image{}, fmt.Errorf("image %s cannot launch: %s", req.Ref, why)
	}
	return img, nil
}

// checkDisk fails early on a size too small for the snapshot, which the
// provider would otherwise only reject after a billable create request.
func checkDisk(ctx context.Context, m provider.ImageManager, img provider.Image, size string) error {
	if img.MinDiskGB == 0 {
		return nil
	}
	disk, err := m.SizeDiskGB(ctx, size)
	if err != nil {
		return err
	}
	if disk < img.MinDiskGB {
		return fmt.Errorf("image %s needs a %d GB disk but size %s has %d GB", img.ID, img.MinDiskGB, size, disk)
	}
	return nil
}

// Eligible reports whether img is a ready Bivouac snapshot in scope.
// Retention uses the same rule, so an image a launch would never pick is
// also never counted as a retained build, and a foreign image that
// merely shares the user tag is neither launched nor deleted.
func Eligible(img provider.Image, s Scope) bool { return ineligible(img, s) == "" }

func ineligible(img provider.Image, s Scope) string {
	if img.Status != "available" {
		return fmt.Sprintf("status is %q, not available", img.Status)
	}
	if img.Type != "snapshot" {
		return fmt.Sprintf("type is %q, not snapshot", img.Type)
	}
	mk, ok := ParseMarkers(img.Tags)
	if !ok {
		return "it is not a Bivouac image (marker tags missing or duplicated)"
	}
	if mk.Format != FormatVersion {
		return fmt.Sprintf("image format %d is not supported (want %d)", mk.Format, FormatVersion)
	}
	if mk.Arch != s.Arch {
		return fmt.Sprintf("built for arch %s, not %s", mk.Arch, s.Arch)
	}
	// Both the build-region marker and the provider's availability must
	// match: a launch never copies a snapshot to another region.
	if mk.Region != s.Region || !slices.Contains(img.Regions, s.Region) {
		return fmt.Sprintf("built in region %s and available in %s, not %s",
			mk.Region, strings.Join(img.Regions, ","), s.Region)
	}
	if s.Tag != "" && !hasTag(img.Tags, s.Tag) {
		return fmt.Sprintf("not tagged %q", s.Tag)
	}
	return ""
}

// SortNewest orders images newest first. Creation times can tie, so the
// higher numeric ID (the later one DigitalOcean issued) breaks the tie
// and keeps the choice deterministic.
func SortNewest(imgs []provider.Image) {
	slices.SortStableFunc(imgs, func(a, b provider.Image) int {
		if c := b.Created.Compare(a.Created); c != 0 {
			return c
		}
		return compareIDs(b.ID, a.ID)
	})
}

func compareIDs(a, b string) int {
	x, errA := strconv.Atoi(a)
	y, errB := strconv.Atoi(b)
	if errA != nil || errB != nil {
		return strings.Compare(a, b)
	}
	return cmp.Compare(x, y)
}
