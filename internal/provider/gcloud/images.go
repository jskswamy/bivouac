package gcloud

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"

	"github.com/jskswamy/bivouac/internal/image"
	"github.com/jskswamy/bivouac/internal/provider"
)

var _ provider.ImageManager = (*Provider)(nil)

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var gErr *googleapi.Error
	if errors.As(err, &gErr) && gErr.Code == 404 {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "404") || strings.Contains(msg, "notFound") || strings.Contains(msg, "was not found")
}

// ListImages returns every private image when tag is "", else every image carrying tag.
func (p *Provider) ListImages(ctx context.Context, tag string) ([]provider.Image, error) {
	req := &computepb.ListImagesRequest{
		Project: p.project,
	}
	it := p.imagesClient.List(ctx, req)
	var out []provider.Image
	for {
		img, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing gcloud images: %w", err)
		}
		pImg := toProviderImage(img, p.zone)
		if tag != "" && !slices.Contains(pImg.Tags, tag) {
			continue
		}
		out = append(out, pImg)
	}
	return out, nil
}

// GetImage returns the image identified by id/name.
func (p *Provider) GetImage(ctx context.Context, id string) (provider.Image, error) {
	req := &computepb.GetImageRequest{
		Project: p.project,
		Image:   id,
	}
	img, err := p.imagesClient.Get(ctx, req)
	if err != nil {
		if isNotFound(err) {
			return provider.Image{}, fmt.Errorf("getting image %s: %w", id, provider.ErrImageNotFound)
		}
		return provider.Image{}, fmt.Errorf("getting gcloud image %s: %w", id, err)
	}
	return toProviderImage(img, p.zone), nil
}

// TagImage attaches tag to the image identified by id.
func (p *Provider) TagImage(ctx context.Context, id, tag string) error {
	req := &computepb.GetImageRequest{
		Project: p.project,
		Image:   id,
	}
	img, err := p.imagesClient.Get(ctx, req)
	if err != nil {
		if isNotFound(err) {
			return fmt.Errorf("tagging image %s: %w", id, provider.ErrImageNotFound)
		}
		return fmt.Errorf("getting image %s for tagging: %w", id, err)
	}

	labels := make(map[string]string)
	for k, v := range img.Labels {
		labels[k] = v
	}
	labels["bivouac-tag"] = tag

	setReq := &computepb.SetLabelsImageRequest{
		Project:  p.project,
		Resource: id,
		GlobalSetLabelsRequestResource: &computepb.GlobalSetLabelsRequest{
			LabelFingerprint: img.LabelFingerprint,
			Labels:           labels,
		},
	}
	op, err := p.imagesClient.SetLabels(ctx, setReq)
	if err != nil {
		return fmt.Errorf("tagging image %s: %w", id, err)
	}
	if err := op.Wait(ctx); err != nil {
		return fmt.Errorf("waiting for image %s tagging: %w", id, err)
	}
	return nil
}

// DeleteImage deletes the image identified by id.
func (p *Provider) DeleteImage(ctx context.Context, id string) error {
	req := &computepb.DeleteImageRequest{
		Project: p.project,
		Image:   id,
	}
	op, err := p.imagesClient.Delete(ctx, req)
	if err != nil {
		if isNotFound(err) {
			return fmt.Errorf("deleting image %s: %w", id, provider.ErrImageNotFound)
		}
		return fmt.Errorf("deleting gcloud image %s: %w", id, err)
	}
	if err := op.Wait(ctx); err != nil {
		return fmt.Errorf("waiting for image %s deletion: %w", id, err)
	}
	return nil
}

// SizeDiskGB returns the disk, in GB, that sizeSlug provides.
// On GCP, machine types do not have fixed disk sizes, so this returns
// the configured diskSizeGb (defaulting to 50 GB).
func (p *Provider) SizeDiskGB(ctx context.Context, sizeSlug string) (int, error) {
	if p.diskSizeGb > 0 {
		return p.diskSizeGb, nil
	}
	return 50, nil
}

func toProviderImage(img *computepb.Image, zone string) provider.Image {
	var created time.Time
	if img.CreationTimestamp != nil {
		t, _ := time.Parse(time.RFC3339, *img.CreationTimestamp)
		created = t
	}
	status := "off"
	if img.GetStatus() == "READY" {
		status = "available"
	}

	var tags []string
	if img.Labels != nil {
		if img.Labels["bivouac-image"] == "true" {
			tags = append(tags, image.MarkerManaged)
		}
		if fmtVer := img.Labels["bivouac-format"]; fmtVer != "" {
			tags = append(tags, "bivouac-format-"+fmtVer)
		}
		if arch := img.Labels["bivouac-arch"]; arch != "" {
			tags = append(tags, "bivouac-arch-"+arch)
		}
		if reg := img.Labels["bivouac-region"]; reg != "" {
			tags = append(tags, "bivouac-region-"+reg)
		}
		if sh := img.Labels["bivouac-shell"]; sh != "" {
			tags = append(tags, "bivouac-shell-"+sh)
		}
		if userTag := img.Labels["bivouac-tag"]; userTag != "" {
			tags = append(tags, userTag)
		}
	}

	diskSize := int(img.GetDiskSizeGb())
	if diskSize == 0 {
		diskSize = 50
	}

	return provider.Image{
		ID:        img.GetName(),
		Name:      img.GetName(),
		Created:   created,
		Regions:   []string{zone},
		MinDiskGB: diskSize,
		Status:    status,
		Tags:      tags,
		Type:      "snapshot",
	}
}
