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

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// Retries back off exponentially from initialRetryInterval up to
// maxRetryInterval rather than running at a fixed sub-second rate.
// This is not just politeness: a tight retry loop against port 22 looks
// exactly like an SSH brute-force attempt, and roughly a minute of it
// (~50-60 connections) is enough for an intermediary -- an ISP's or the
// provider's edge -- to blackhole port 22 between this host and that
// instance. Once that happens the port stops answering entirely (packets
// dropped, not refused) while the instance itself stays perfectly
// healthy, so `up` fails with an i/o timeout that looks like a broken
// image or a broken cloud-init and is not. Backing off keeps the whole
// wait comfortably under that threshold.
const (
	initialRetryInterval = 2 * time.Second
	maxRetryInterval     = 15 * time.Second
)

// WaitReady waits until ip is reachable over SSH as user and cloud-init
// has finished installing Nix, retrying the SSH connection with
// exponential backoff until timeout elapses. A non-zero cloud-init exit
// is treated as a genuine failure and returned immediately, never
// retried. The cloud-init check itself is also bounded by timeout:
// Client.Run takes no context of its own, so a stalled remote command is
// unblocked by forcibly closing the connection once the deadline passes.
//
// Connects as the instance's own non-root user, not root. cloud-init.sh
// creates that user early and then, as its very last step, disables root
// SSH login -- so root is the one login guaranteed to STOP working while
// this function is still waiting. Polling as root therefore raced a
// closing window: miss it, and every subsequent retry was a failed root
// auth, which is both futile and the exact traffic pattern that gets
// port 22 blackholed (see the retry constants above). The provisioned
// user is monotonic instead -- once cloud-init has created it and
// installed its authorized_keys it stays reachable -- so waiting on it
// is both race-free and authenticates successfully.
func WaitReady(ctx context.Context, ip, user string, timeout time.Duration) error {
	provider.ReportProgress(ctx, "waiting for instance to be ready (SSH + cloud-init)")
	deadline := time.Now().Add(timeout)
	deadlineCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	var lastErr error
	backoff := initialRetryInterval
	for {
		if !time.Now().Before(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s to become reachable: %w", timeout, ip, lastErr)
		}

		client, err := reconcile.Connect(ctx, ip, user)
		if err != nil {
			lastErr = err
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-deadlineCtx.Done():
				// Deadline hit mid-sleep; the check at the top of the
				// loop turns this into the timeout error above rather
				// than sleeping out the rest of the interval.
			case <-time.After(backoff):
			}
			if backoff *= 2; backoff > maxRetryInterval {
				backoff = maxRetryInterval
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
