package digitalocean

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/digitalocean/godo"
	"github.com/jskswamy/bivouac/internal/provider"
)

// ListKeys returns every SSH key on the account.
func (p *Provider) ListKeys(ctx context.Context) ([]provider.SSHKey, error) {
	var keys []provider.SSHKey
	opt := &godo.ListOptions{PerPage: 200}
	for {
		page, resp, err := p.client.Keys.List(ctx, opt)
		if err != nil {
			return nil, keyError("listing SSH keys", resp, err)
		}
		for _, k := range page {
			keys = append(keys, toSSHKey(&k))
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			return keys, nil
		}
		current, err := resp.Links.CurrentPage()
		if err != nil {
			return keys, nil
		}
		opt.Page = current + 1
	}
}

// CreateKey registers publicKey on the account under name.
func (p *Provider) CreateKey(ctx context.Context, name, publicKey string) (provider.SSHKey, error) {
	key, resp, err := p.client.Keys.Create(ctx, &godo.KeyCreateRequest{
		Name:      name,
		PublicKey: publicKey,
	})
	if err != nil {
		return provider.SSHKey{}, keyError(fmt.Sprintf("registering SSH key %q", name), resp, err)
	}
	return toSSHKey(key), nil
}

func toSSHKey(k *godo.Key) provider.SSHKey {
	return provider.SSHKey{
		ID:          strconv.Itoa(k.ID),
		Name:        k.Name,
		Fingerprint: k.Fingerprint,
		PublicKey:   k.PublicKey,
	}
}

// keyError wraps a godo failure, marking a 403 as provider.ErrForbidden
// so the caller can drop an optional capability instead of failing.
//
// A narrowly scoped token is what cloudlab-24k recommends, so "this
// token may not read keys" has to be an answer the flow can act on
// rather than an error it reports.
func keyError(what string, resp *godo.Response, err error) error {
	if resp != nil && resp.Response != nil && resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%s: %w: %v", what, provider.ErrForbidden, err)
	}
	return fmt.Errorf("%s: %w", what, err)
}
