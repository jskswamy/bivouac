package cmd

import (
	"fmt"
	"io"
	"net"

	"github.com/spf13/cobra"

	"github.com/jskswamy/bivouac/internal/provider/gcloud/iap"
	"github.com/jskswamy/bivouac/internal/sshidentity"
)

func newProxyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:    "proxy <instance>",
		Short:  "Internal helper to bridge standard I/O over Tailscale or an IAP tunnel to an instance",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			_, record, err := resolveInstance(cmd, name)
			if err != nil {
				return err
			}

			ctx := record.ConnectionContext(cmd.Context())
			selected, hasSelected := sshidentity.FromContext(ctx)
			useIAP := (hasSelected && selected.UseIAP) || (record.Provider == "gcloud" && !record.TailscaleJoined)
			if useIAP && record.Provider != "gcloud" {
				return fmt.Errorf("instance %q is on %s, proxy command is only for gcloud", name, record.Provider)
			}

			var conn net.Conn
			if hasSelected && selected.HasOverlay() {
				if overlayConn, dialErr := selected.DialOverlay(); dialErr == nil {
					conn = overlayConn
				}
			}
			if conn == nil {
				if useIAP {
					iapConn, dialErr := iap.DialTarget(ctx, record.Project, record.Region, record.Name, 22)
					if dialErr != nil {
						return fmt.Errorf("connecting via iap: %w", dialErr)
					}
					conn = iapConn
				} else {
					if record.Provider != "gcloud" && (!hasSelected || !selected.HasOverlay()) {
						return fmt.Errorf("instance %q is on %s, proxy command is only for gcloud", name, record.Provider)
					}
					target := record.IP
					if _, _, splitErr := net.SplitHostPort(target); splitErr != nil {
						target = net.JoinHostPort(target, "22")
					}
					var d net.Dialer
					tcpConn, dialErr := d.DialContext(ctx, "tcp", target)
					if dialErr != nil {
						return fmt.Errorf("connecting to %s: %w", target, dialErr)
					}
					conn = tcpConn
				}
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
