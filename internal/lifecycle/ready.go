// Package lifecycle brings a newly created instance from "VM exists" to
// "fully live": waiting for it to genuinely be ready, reconciling
// home-manager, syncing the repo in, and starting continuous watch. It
// is the piece cmd/up.go orchestrates on top of the already-built
// provider, config, reconcile, and state packages.
package lifecycle

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

const retryInterval = 500 * time.Millisecond

// WaitReady waits until ip is reachable over SSH and cloud-init has
// finished installing Nix, retrying the SSH connection every
// retryInterval until timeout elapses. A non-zero cloud-init exit is
// treated as a genuine failure and returned immediately, never retried.
// The cloud-init check itself is also bounded by timeout: Client.Run
// takes no context of its own, so a stalled remote command is unblocked
// by forcibly closing the connection once the deadline passes.
func WaitReady(ctx context.Context, ip string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	deadlineCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	var lastErr error
	for {
		if !time.Now().Before(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s to become reachable: %w", timeout, ip, lastErr)
		}

		client, err := reconcile.Connect(ctx, ip)
		if err != nil {
			lastErr = err
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryInterval):
			}
			continue
		}

		output, err := runBounded(deadlineCtx, client, "cloud-init status --wait")
		_ = client.Close()
		if err != nil {
			return fmt.Errorf("cloud-init failed on %s: %w\n%s", ip, err, strings.TrimSpace(output))
		}
		return nil
	}
}

// runBounded runs cmd on client, but forcibly closes client (unblocking
// the underlying SSH session) if deadlineCtx is done before Run
// returns. Client.Run itself takes no context, so this is the only way
// to bound a stalled remote command.
func runBounded(deadlineCtx context.Context, client *reconcile.Client, cmd string) (string, error) {
	type result struct {
		output string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := client.Run(cmd)
		done <- result{output, err}
	}()

	select {
	case r := <-done:
		return r.output, r.err
	case <-deadlineCtx.Done():
		_ = client.Close()
		<-done
		return "", fmt.Errorf("cloud-init check did not complete before the deadline: %w", deadlineCtx.Err())
	}
}
