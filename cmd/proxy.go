package cmd

import (
	"fmt"
	"io"
	"net"
	"os"

	"github.com/spf13/cobra"

	"github.com/jskswamy/bivouac/internal/provider/gcloud/iap"
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
			token, err := iap.ResolveAccessToken(ctx)
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
