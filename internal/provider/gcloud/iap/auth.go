package iap

import (
	"context"
	"os"
	"strings"

	"golang.org/x/oauth2/google"

	"github.com/jskswamy/bivouac/internal/secrets"
)

// ResolveAccessToken discovers a Google Cloud access token from environment variables,
// bivouac secrets.yaml, or Application Default Credentials (ADC).
func ResolveAccessToken(ctx context.Context) (string, error) {
	if tok := os.Getenv("GCLOUD_ACCESS_TOKEN"); tok != "" {
		return tok, nil
	}

	scopes := []string{"https://www.googleapis.com/auth/cloud-platform"}

	if keyVal := os.Getenv("GCLOUD_SERVICE_ACCOUNT_KEY"); keyVal != "" {
		if strings.HasPrefix(strings.TrimSpace(keyVal), "{") {
			creds, err := google.CredentialsFromJSONWithType(ctx, []byte(keyVal), google.ServiceAccount, scopes...)
			if err == nil {
				tok, err := creds.TokenSource.Token()
				if err == nil {
					return tok.AccessToken, nil
				}
			}
		} else {
			// #nosec G304,G703 -- Reading credentials file specified by user/operator in environment
			data, err := os.ReadFile(keyVal)
			if err == nil {
				creds, err := google.CredentialsFromJSONWithType(ctx, data, google.ServiceAccount, scopes...)
				if err == nil {
					tok, err := creds.TokenSource.Token()
					if err == nil {
						return tok.AccessToken, nil
					}
				}
			}
		}
	}

	if credsFile := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); credsFile != "" {
		// #nosec G304,G703 -- Reading credentials file specified by user/operator in environment
		data, err := os.ReadFile(credsFile)
		if err == nil {
			creds, err := google.CredentialsFromJSONWithType(ctx, data, google.ServiceAccount, scopes...)
			if err == nil {
				tok, err := creds.TokenSource.Token()
				if err == nil {
					return tok.AccessToken, nil
				}
			}
		}
	}

	secPath, err := secrets.Path()
	if err == nil {
		value, err := secrets.Decrypt(ctx, secPath, "gcloud_service_account_key")
		if err == nil && len(value) > 0 {
			creds, err := google.CredentialsFromJSONWithType(ctx, value, google.ServiceAccount, scopes...)
			if err == nil {
				tok, err := creds.TokenSource.Token()
				if err == nil {
					return tok.AccessToken, nil
				}
			}
		}
	}

	creds, err := google.FindDefaultCredentials(ctx, scopes...)
	if err != nil {
		return "", err
	}
	tok, err := creds.TokenSource.Token()
	if err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}
