package digitalocean

import (
	"context"
	"fmt"

	"github.com/digitalocean/godo"
)

// Region, Size, and Image are DigitalOcean-specific discovery results —
// deliberately not part of the generic provider package, since region/
// size/image vocabulary doesn't generalize across cloud providers.
type Region struct {
	Slug, Name string
	Sizes      []string
	Available  bool
}

type Size struct {
	Slug, Description       string
	VCPUs, MemoryMB, DiskGB int
	PriceMonthly            float64
	Regions                 []string
	Available, GPU          bool
	GPUModel                string
}

type Image struct {
	Slug, Name, Distribution string
	Public                   bool
	Regions                  []string
}

// ListRegions returns every DigitalOcean region, including which size
// slugs are available in each.
func (p *Provider) ListRegions(ctx context.Context) ([]Region, error) {
	var raw []godo.Region
	err := paginate(&raw, func(opt *godo.ListOptions) ([]godo.Region, *godo.Response, error) {
		return p.client.Regions.List(ctx, opt)
	})
	if err != nil {
		return nil, fmt.Errorf("listing regions: %w", err)
	}

	regions := make([]Region, 0, len(raw))
	for _, r := range raw {
		regions = append(regions, Region{
			Slug:      r.Slug,
			Name:      r.Name,
			Sizes:     r.Sizes,
			Available: r.Available,
		})
	}
	return regions, nil
}

// ListSizes returns every DigitalOcean droplet size, including GPU
// sizes (GPU is true and GPUModel is set when godo reports GPUInfo).
func (p *Provider) ListSizes(ctx context.Context) ([]Size, error) {
	var raw []godo.Size
	err := paginate(&raw, func(opt *godo.ListOptions) ([]godo.Size, *godo.Response, error) {
		return p.client.Sizes.List(ctx, opt)
	})
	if err != nil {
		return nil, fmt.Errorf("listing sizes: %w", err)
	}

	sizes := make([]Size, 0, len(raw))
	for _, s := range raw {
		size := Size{
			Slug:         s.Slug,
			Description:  s.Description,
			VCPUs:        s.Vcpus,
			MemoryMB:     s.Memory,
			DiskGB:       s.Disk,
			PriceMonthly: s.PriceMonthly,
			Regions:      s.Regions,
			Available:    s.Available,
		}
		if s.GPUInfo != nil {
			size.GPU = true
			size.GPUModel = s.GPUInfo.Model
		}
		sizes = append(sizes, size)
	}
	return sizes, nil
}

// ListImages returns DigitalOcean's official base distribution images
// (not private snapshots/backups).
func (p *Provider) ListImages(ctx context.Context) ([]Image, error) {
	var raw []godo.Image
	err := paginate(&raw, func(opt *godo.ListOptions) ([]godo.Image, *godo.Response, error) {
		return p.client.Images.ListDistribution(ctx, opt)
	})
	if err != nil {
		return nil, fmt.Errorf("listing images: %w", err)
	}

	images := make([]Image, 0, len(raw))
	for _, img := range raw {
		images = append(images, Image{
			Slug:         img.Slug,
			Name:         img.Name,
			Distribution: img.Distribution,
			Public:       img.Public,
			Regions:      img.Regions,
		})
	}
	return images, nil
}
