// Package digitalocean implements provider.Provider on top of
// DigitalOcean's godo client.
package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/digitalocean/godo"
	"github.com/jskswamy/cloudlab/internal/provider"
)

// Provider implements provider.Provider against the DigitalOcean API.
type Provider struct {
	client       *godo.Client
	pollInterval time.Duration
}

// New builds a Provider authenticated with token. It makes no network
// call and does not validate the token — auth failures surface on the
// first real API call.
func New(token string) *Provider {
	return &Provider{
		client:       godo.NewFromToken(token),
		pollInterval: 5 * time.Second,
	}
}

// Get returns the current state of the droplet identified by id.
func (p *Provider) Get(ctx context.Context, id string) (provider.VM, error) {
	n, err := parseID(id)
	if err != nil {
		return provider.VM{}, err
	}
	d, _, err := p.client.Droplets.Get(ctx, n)
	if err != nil {
		if isNotFound(err) {
			return provider.VM{}, fmt.Errorf("getting droplet %s: %w", id, provider.ErrNotFound)
		}
		return provider.VM{}, fmt.Errorf("getting droplet %s: %w", id, err)
	}
	return toVM(d), nil
}

// parseID converts a VM ID string (a DigitalOcean droplet ID) to the int
// godo's Droplets service expects.
func parseID(id string) (int, error) {
	n, err := strconv.Atoi(id)
	if err != nil {
		return 0, fmt.Errorf("invalid droplet id %q: %w", id, err)
	}
	return n, nil
}

// isNotFound reports whether err is a DigitalOcean 404 response.
func isNotFound(err error) bool {
	var errResp *godo.ErrorResponse
	if errors.As(err, &errResp) && errResp.Response != nil {
		return errResp.Response.StatusCode == http.StatusNotFound
	}
	return false
}

// Destroy deletes the droplet identified by id.
func (p *Provider) Destroy(ctx context.Context, id string) error {
	n, err := parseID(id)
	if err != nil {
		return err
	}
	if _, err := p.client.Droplets.Delete(ctx, n); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("destroying droplet %s: %w", id, provider.ErrNotFound)
		}
		return fmt.Errorf("destroying droplet %s: %w", id, err)
	}
	return nil
}

// List returns every droplet on the account.
func (p *Provider) List(ctx context.Context) ([]provider.VM, error) {
	var droplets []godo.Droplet
	err := paginate(&droplets, func(opt *godo.ListOptions) ([]godo.Droplet, *godo.Response, error) {
		return p.client.Droplets.List(ctx, opt)
	})
	if err != nil {
		return nil, fmt.Errorf("listing droplets: %w", err)
	}

	vms := make([]provider.VM, 0, len(droplets))
	for i := range droplets {
		vms = append(vms, toVM(&droplets[i]))
	}
	return vms, nil
}

// paginate loops through every page of a godo list call, appending each
// page's results to acc. Shared by List and, later, the discovery
// methods (ListRegions/ListSizes/ListImages).
func paginate[T any](acc *[]T, fetch func(*godo.ListOptions) ([]T, *godo.Response, error)) error {
	opt := &godo.ListOptions{}
	for {
		page, resp, err := fetch(opt)
		if err != nil {
			return err
		}
		*acc = append(*acc, page...)

		if resp.Links == nil || resp.Links.IsLastPage() {
			return nil
		}
		current, err := resp.Links.CurrentPage()
		if err != nil {
			return err
		}
		opt.Page = current + 1
	}
}

// toVM converts a godo.Droplet to a provider.VM. A droplet with no
// network assigned yet (still booting) or no public IPv4 is a normal,
// non-error state — PublicIPv4's error case (Networks == nil) and its
// empty-string case (no public entry yet) both just mean VM.IP is "".
func toVM(d *godo.Droplet) provider.VM {
	ip, _ := d.PublicIPv4()
	region := ""
	if d.Region != nil {
		region = d.Region.Slug
	}
	return provider.VM{
		ID:     strconv.Itoa(d.ID),
		Name:   d.Name,
		IP:     ip,
		Region: region,
		Size:   d.SizeSlug,
		Status: d.Status,
	}
}
