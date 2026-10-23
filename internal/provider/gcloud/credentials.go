package gcloud

import (
	"context"
	"os"
	"strings"

	"golang.org/x/oauth2/google"

	"github.com/jskswamy/bivouac/internal/secrets"
)

// Credentials holds resolved Google Cloud credentials and API overrides.
type Credentials struct {
	BaseURL     string
	AccessToken string
	FilePath    string
	JSONData    []byte
}

// ResolveCredentials discovers Google Cloud credentials from environment variables,
// bivouac secrets.yaml, or Application Default Credentials.
func ResolveCredentials(ctx context.Context) (*Credentials, error) {
	c := &Credentials{
		BaseURL:     os.Getenv("GCLOUD_API_URL"),
		AccessToken: os.Getenv("GCLOUD_ACCESS_TOKEN"),
	}

	if path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); path != "" {
		c.FilePath = path
		return c, nil
	}

	if keyVal := os.Getenv("GCLOUD_SERVICE_ACCOUNT_KEY"); keyVal != "" {
		trimmed := strings.TrimSpace(keyVal)
		if strings.HasPrefix(trimmed, "{") {
			c.JSONData = []byte(trimmed)
		} else {
			c.FilePath = keyVal
		}
		return c, nil
	}

	secPath, err := secrets.Path()
	if err == nil {
		value, err := secrets.Decrypt(ctx, secPath, "gcloud_service_account_key")
		if err == nil && len(value) > 0 {
			c.JSONData = value
			return c, nil
		}
	}

	return c, nil
}

// ClientOptions converts resolved credentials into Option functions for gcloud.New.
func (c *Credentials) ClientOptions() []Option {
	var opts []Option
	if c.BaseURL != "" {
		opts = append(opts, WithBaseURL(c.BaseURL))
		return opts
	}
	if len(c.JSONData) > 0 {
		opts = append(opts, WithCredentialsJSON(c.JSONData))
	} else if c.FilePath != "" {
		opts = append(opts, WithCredentialsFile(c.FilePath))
	}
	return opts
}

// SetupEnv ensures GOOGLE_APPLICATION_CREDENTIALS is set in the environment,
// materializing a temporary file if credentials were supplied as JSON data.
// It returns a cleanup function to restore the environment and remove temp files.
func (c *Credentials) SetupEnv() (func(), error) {
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != "" {
		return nil, nil
	}
	if c.FilePath != "" {
		_ = os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", c.FilePath)
		return func() {
			_ = os.Unsetenv("GOOGLE_APPLICATION_CREDENTIALS")
		}, nil
	}
	if len(c.JSONData) > 0 {
		f, err := os.CreateTemp("", "bivouac-gcp-key-*.json")
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(c.JSONData); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			return nil, err
		}
		_ = f.Close()
		_ = os.Chmod(f.Name(), 0600)
		_ = os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", f.Name())
		return func() {
			_ = os.Unsetenv("GOOGLE_APPLICATION_CREDENTIALS")
			_ = os.Remove(f.Name())
		}, nil
	}
	return nil, nil
}

// ResolveAccessToken returns a valid Google Cloud OAuth2 access token.
func (c *Credentials) ResolveAccessToken(ctx context.Context) (string, error) {
	if c.AccessToken != "" {
		return c.AccessToken, nil
	}

	scopes := []string{"https://www.googleapis.com/auth/cloud-platform"}

	if len(c.JSONData) > 0 {
		creds, err := google.CredentialsFromJSONWithType(ctx, c.JSONData, google.ServiceAccount, scopes...)
		if err == nil {
			tok, err := creds.TokenSource.Token()
			if err == nil {
				return tok.AccessToken, nil
			}
		}
	} else if c.FilePath != "" {
		// #nosec G304,G703 -- Reading credentials file specified by user/operator in environment
		data, err := os.ReadFile(c.FilePath)
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
