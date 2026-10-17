package image

import (
	"context"
	"fmt"
	"slices"

	"github.com/jskswamy/bivouac/internal/provider"
)

// fakeImages is an in-memory provider.ImageManager. It records every
// call so tests can prove what Resolve did (and did not) ask the cloud.
type fakeImages struct {
	images []provider.Image
	disks  map[string]int
	calls  []string
	// deleteErr makes DeleteImage of that ID fail without removing it.
	deleteErr map[string]error
	// onGet runs before GetImage looks the image up, so a test can model
	// another actor changing the account between plan and delete.
	onGet func(f *fakeImages, id string)
}

var _ provider.ImageManager = (*fakeImages)(nil)

func (f *fakeImages) ListImages(_ context.Context, tag string) ([]provider.Image, error) {
	f.calls = append(f.calls, "ListImages "+tag)
	var out []provider.Image
	for _, img := range f.images {
		if tag == "" || slices.Contains(img.Tags, tag) {
			out = append(out, img)
		}
	}
	return out, nil
}

func (f *fakeImages) GetImage(_ context.Context, id string) (provider.Image, error) {
	f.calls = append(f.calls, "GetImage "+id)
	if f.onGet != nil {
		f.onGet(f, id)
	}
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
	if err := f.deleteErr[id]; err != nil {
		return err
	}
	for i, img := range f.images {
		if img.ID == id {
			f.images = append(f.images[:i], f.images[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("image %s: %w", id, provider.ErrImageNotFound)
}

func (f *fakeImages) count(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if len(c) >= len(prefix) && c[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

func (f *fakeImages) SizeDiskGB(_ context.Context, slug string) (int, error) {
	f.calls = append(f.calls, "SizeDiskGB "+slug)
	d, ok := f.disks[slug]
	if !ok {
		return 0, fmt.Errorf("unknown size %q", slug)
	}
	return d, nil
}
