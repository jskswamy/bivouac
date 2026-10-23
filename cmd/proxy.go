package cmd

import (
	"fmt"
	"io"

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
			conn, err := iap.DialTarget(ctx, record.Project, record.Region, record.Name, 22)
			if err != nil {
				return fmt.Errorf("connecting via iap: %w", err)
			}
			defer func() { _ = conn.Close() }()

			in := cmd.InOrStdin()
			out := cmd.OutOrStdout()

			errc := make(chan error, 1)
			go func() {
				_, copyErr := io.Copy(conn, in)
				if copyErr != nil && copyErr != io.EOF {
					errc <- copyErr
				}
			}()
			go func() {
				_, copyErr := io.Copy(out, conn)
				errc <- copyErr
			}()

			select {
			case <-ctx.Done():
				return nil
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
