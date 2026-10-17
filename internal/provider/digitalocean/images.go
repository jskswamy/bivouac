package digitalocean

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/digitalocean/godo"
	"github.com/jskswamy/bivouac/internal/provider"
)

var _ provider.ImageManager = (*Provider)(nil)

// ListImages returns the account's private images, or every image
// carrying tag when tag is set.
func (p *Provider) ListImages(ctx context.Context, tag string) ([]provider.Image, error) {
	var imgs []godo.Image
	err := paginate("listing images", &imgs, func(opt *godo.ListOptions) ([]godo.Image, *godo.Response, error) {
		if tag == "" {
			return p.client.Images.ListUser(ctx, opt)
		}
		return p.client.Images.ListByTag(ctx, tag, opt)
	})
	if err != nil {
		return nil, err
	}
	out := make([]provider.Image, 0, len(imgs))
	for i := range imgs {
		out = append(out, toImage(&imgs[i]))
	}
	return out, nil
}

// GetImage returns the image identified by id.
func (p *Provider) GetImage(ctx context.Context, id string) (provider.Image, error) {
	n, err := parseImageID(id)
	if err != nil {
		return provider.Image{}, err
	}
	img, resp, err := p.client.Images.GetByID(ctx, n)
	if err != nil {
		return provider.Image{}, imageError("getting image "+id, resp, err)
	}
	return toImage(img), nil
}

// TagImage attaches tag to the image identified by id.
//
// Images.Update changes only name, distribution and description, so the
// tags API is the only way to add one. Attaching needs the tag to exist;
// creating it first is idempotent in practice, and an "already exists"
// refusal is tolerated in case the API ever stops being so.
func (p *Provider) TagImage(ctx context.Context, id, tag string) error {
	what := fmt.Sprintf("tagging image %s with %q", id, tag)
	if _, resp, err := p.client.Tags.Create(ctx, &godo.TagCreateRequest{Name: tag}); err != nil {
		if !hasStatus(resp, http.StatusUnprocessableEntity, http.StatusConflict) {
			return imageError(what, resp, err)
		}
	}
	resp, err := p.client.Tags.TagResources(ctx, tag, &godo.TagResourcesRequest{
		Resources: []godo.Resource{{ID: id, Type: godo.ImageResourceType}},
	})
	if err != nil {
		return imageError(what, resp, err)
	}
	return nil
}

// DeleteImage deletes the image identified by id.
func (p *Provider) DeleteImage(ctx context.Context, id string) error {
	n, err := parseImageID(id)
	if err != nil {
		return err
	}
	if resp, err := p.client.Images.Delete(ctx, n); err != nil {
		return imageError("deleting image "+id, resp, err)
	}
	return nil
}

// SizeDiskGB returns the disk, in GB, that sizeSlug provides.
func (p *Provider) SizeDiskGB(ctx context.Context, sizeSlug string) (int, error) {
	var sizes []godo.Size
	err := paginate("listing sizes", &sizes, func(opt *godo.ListOptions) ([]godo.Size, *godo.Response, error) {
		return p.client.Sizes.List(ctx, opt)
	})
	if err != nil {
		return 0, err
	}
	for _, s := range sizes {
		if s.Slug == sizeSlug {
			return s.Disk, nil
		}
	}
	return 0, fmt.Errorf("unknown droplet size %q", sizeSlug)
}

func parseImageID(id string) (int, error) {
	n, err := strconv.Atoi(id)
	if err != nil {
		return 0, fmt.Errorf("invalid image id %q: %w", id, err)
	}
	return n, nil
}

func hasStatus(resp *godo.Response, codes ...int) bool {
	if resp == nil || resp.Response == nil {
		return false
	}
	return slices.Contains(codes, resp.StatusCode)
}

// imageError wraps a godo failure the way keyError does, and also marks
// a 404 as provider.ErrImageNotFound: prune treats an image deleted
// out from under it as done, and resolution reports a vanished ID
// differently from a failed call.
func imageError(what string, resp *godo.Response, err error) error {
	if hasStatus(resp, http.StatusNotFound) {
		return fmt.Errorf("%s: %w: %v", what, provider.ErrImageNotFound, err)
	}
	return keyError(what, resp, err)
}

// toImage converts a godo.Image. An unparsable created_at leaves Created
// zero, as toVM does, rather than failing the whole listing.
func toImage(img *godo.Image) provider.Image {
	out := provider.Image{
		ID:        strconv.Itoa(img.ID),
		Name:      img.Name,
		Regions:   img.Regions,
		MinDiskGB: img.MinDiskSize,
		Status:    img.Status,
		Tags:      img.Tags,
		Type:      img.Type,
	}
	if t, err := time.Parse(time.RFC3339, img.Created); err == nil {
		out.Created = t
	}
	return out
}
