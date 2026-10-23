package cmd

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2/google"

	"github.com/jskswamy/bivouac/internal/provider/gcloud/iap"
	"github.com/jskswamy/bivouac/internal/secrets"
)

func newProxyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:    "proxy <instance>",
		Short:  "Internal helper to bridge standard I/O over an IAP tunnel to a Google Cloud VM",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			_, record, err := resolveInstance(cmd, name)
			if err != nil {
				return err
			}
			if record.Provider != "gcloud" {
				return fmt.Errorf("instance %q is on %s, proxy command is only for gcloud", name, record.Provider)
			}

			ctx := cmd.Context()
			token, err := resolveGCloudAccessToken(ctx)
			if err != nil {
				return fmt.Errorf("resolving gcloud access token: %w", err)
			}

			var conn net.Conn
			if raw := os.Getenv("GCLOUD_IAP_URL"); raw != "" {
				conn, err = iap.DialWithURL(ctx, raw, token)
			} else {
				conn, err = iap.Dial(ctx, record.Project, record.Region, record.Name, 22, token)
			}
			if err != nil {
				return fmt.Errorf("connecting via iap: %w", err)
			}
			defer func() { _ = conn.Close() }()

			in := cmd.InOrStdin()
			out := cmd.OutOrStdout()

			errc := make(chan error, 2)
			go func() {
				_, copyErr := io.Copy(conn, in)
				errc <- copyErr
			}()
			go func() {
				_, copyErr := io.Copy(out, conn)
				errc <- copyErr
			}()

			select {
			case <-ctx.Done():
				return ctx.Err()
			case err := <-errc:
				if err != nil && err != io.EOF {
					return err
				}
				return nil
			}
		},
	}
	return c
}

func resolveGCloudAccessToken(ctx context.Context) (string, error) {
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
