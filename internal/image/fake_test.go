package image

import (
	"context"
	"fmt"

	"github.com/jskswamy/bivouac/internal/provider"
)

// fakeImages is an in-memory provider.ImageManager. It records every
// call so tests can prove what Resolve did (and did not) ask the cloud.
type fakeImages struct {
	images []provider.Image
	disks  map[string]int
	calls  []string
}

var _ provider.ImageManager = (*fakeImages)(nil)

func (f *fakeImages) ListImages(_ context.Context, tag string) ([]provider.Image, error) {
	f.calls = append(f.calls, "ListImages "+tag)
	var out []provider.Image
	for _, img := range f.images {
		if tag == "" || hasTag(img.Tags, tag) {
			out = append(out, img)
		}
	}
	return out, nil
}

func (f *fakeImages) GetImage(_ context.Context, id string) (provider.Image, error) {
	f.calls = append(f.calls, "GetImage "+id)
	for _, img := range f.images {
		if img.ID == id {
			return img, nil
		}
	}
	return provider.Image{}, fmt.Errorf("image %s: %w", id, provider.ErrImageNotFound)
}

func (f *fakeImages) TagImage(_ context.Context, id, tag string) error {
	f.calls = append(f.calls, "TagImage "+id+" "+tag)
	return nil
}

func (f *fakeImages) DeleteImage(_ context.Context, id string) error {
	f.calls = append(f.calls, "DeleteImage "+id)
	return nil
}

func (f *fakeImages) SizeDiskGB(_ context.Context, slug string) (int, error) {
	f.calls = append(f.calls, "SizeDiskGB "+slug)
	d, ok := f.disks[slug]
	if !ok {
		return 0, fmt.Errorf("unknown size %q", slug)
	}
	return d, nil
}
