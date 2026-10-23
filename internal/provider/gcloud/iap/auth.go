package iap

import (
	"context"

	"github.com/jskswamy/bivouac/internal/provider/gcloud"
)

// ResolveAccessToken discovers a Google Cloud access token from environment variables,
// bivouac secrets.yaml, or Application Default Credentials (ADC).
func ResolveAccessToken(ctx context.Context) (string, error) {
	creds, err := gcloud.ResolveCredentials(ctx)
	if err != nil {
		return "", err
	}
	return creds.ResolveAccessToken(ctx)
}
