package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/reconcile"
	"github.com/jskswamy/cloudlab/internal/state"
)

// terminateWatch best-effort terminates any existing Mutagen sync
// session named name. Errors -- including "no such session" -- are
// swallowed: the absence of a session to terminate isn't a failure
// for Down or for a Watch restart, both of which call this first.
func terminateWatch(ctx context.Context, name string) {
	// #nosec G204 -- argv-array exec.Command, no shell; name is the
	// instance name, a local identifier never attacker-controlled.
	_ = exec.CommandContext(ctx, "mutagen", "sync", "terminate", name).Run()
}

// deregisterTailscale best-effort logs the instance out of its
// tailnet before it's destroyed -- once destroyed, nothing can run on
// it anymore, so this must happen first. Errors are swallowed, same
// as terminateWatch: a failed logout must never block VM teardown.
// Skipped entirely if this instance never actually joined, checked
// via record.TailscaleJoined rather than a freshly-resolved
// cloudlab.pkl -- Down never receives a config.Config, and the
// config's current value could differ from what actually happened
// (the toggle could've changed, or the instance could've been joined
// manually via `cloudlab tailscale` with the config still false).
func deregisterTailscale(ctx context.Context, record state.Record) {
	if !record.TailscaleJoined {
		return
	}
	client, err := reconcile.Connect(ctx, record.IP, record.User)
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()
	// Absolute path, not a bare name: sudo resets PATH to its own
	// secure_path and tailscale lives in the instance user's
	// home-manager profile, so `sudo tailscale logout` would fail with
	// "command not found" (see RemoteTailscaleBin). sudo itself is
	// needed because tailscaled's LocalAPI gates logout on
	// root-or-operator, same as tailscale up.
	tailscaleBin, err := RemoteTailscaleBin(client)
	if err != nil {
		return
	}
	cmd := "bash -lc " + reconcile.ShellQuote("sudo "+reconcile.ShellQuote(tailscaleBin)+" logout")
	_, _ = client.Run(cmd)
}

// Down tears an instance down: stops its watch session (best-effort),
// destroys the VM, and clears its state record. A VM that's already
// gone (destroyed outside cloudlab) is treated as success, not an
// error -- state is cleared either way so cloudlab's view converges
// with reality. If Destroy fails for any other reason, state is still
// cleared (so a stuck record can't block a retry), but the error is
// still returned so the user knows to check the provider's dashboard.
func Down(ctx context.Context, p provider.Provider, store *state.Store, record state.Record) error {
	terminateWatch(ctx, record.Name)
	deregisterTailscale(ctx, record)

	if err := p.Destroy(ctx, record.VMID); err != nil && !errors.Is(err, provider.ErrNotFound) {
		_ = store.Delete(record.Name)
		return fmt.Errorf("destroying VM %s: %w (state cleared -- check the provider dashboard)", record.VMID, err)
	}
	return store.Delete(record.Name)
}
