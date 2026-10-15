package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/reconcile"
	"github.com/jskswamy/bivouac/internal/shellcmd"
	"github.com/jskswamy/bivouac/internal/sshidentity"
	"github.com/jskswamy/bivouac/internal/state"
)

// deregisterTailscale best-effort logs the instance out of its
// tailnet before it's destroyed -- once destroyed, nothing can run on
// it anymore, so this must happen first. Errors are swallowed: a
// failed logout must never block VM teardown. Skipped entirely if
// this instance never actually joined, checked
// via record.TailscaleJoined rather than a freshly-resolved
// bivouac.pkl -- Down never receives a config.Config, and the
// config's current value could differ from what actually happened
// (the toggle could've changed, or the instance could've been joined
// manually via `bivouac tailscale` with the config still false).
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
	tailscaleBin, err := RemoteTailscaleBin(ctx, client)
	if err != nil {
		return
	}
	cmd := shellcmd.LoginShell("sudo " + shellcmd.Quote(tailscaleBin) + " logout")
	_, _ = client.Run(cmd)
}

// Down tears an instance down: rescues any session's work (unless
// force), destroys the VM, and clears its state record. A VM that's
// already gone (destroyed outside bivouac) is treated as success,
// not an error -- state is cleared either way so bivouac's view
// converges with reality. If Destroy fails for any other reason,
// state is still cleared (so a stuck record can't block a retry), but
// the error is still returned so the user knows to check the
// provider's dashboard. Selected-identity records instead retain both state
// and profile on a failed destroy, so a retry cannot lose their credentials.
func Down(ctx context.Context, p provider.Provider, store *state.Store, record state.Record, force bool) error {
	release, err := store.LockInstance(ctx, record.Name, true)
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	latest, ok, err := store.Get(record.Name)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	record = latest
	ctx = record.ConnectionContext(ctx)
	if !force {
		if err := rescueBeforeDestroy(ctx, record); err != nil {
			return err
		}
	}

	// Legacy state historically clears even a failed destroy, so its saved
	// ids must be tidied first. Selected records retain recoverable state
	// on failure; their profiles are removed only after confirmed destruction.
	if record.SSH == nil {
		forgetHerdrMachines(ctx, record)
	}

	deregisterTailscale(ctx, record)

	if err := p.Destroy(ctx, record.VMID); err != nil && !errors.Is(err, provider.ErrNotFound) {
		if record.SSH != nil {
			return fmt.Errorf("destroying VM %s: %w (state and SSH profile retained -- retry down or check the provider dashboard)", record.VMID, err)
		}
		_ = store.Delete(record.Name)
		return fmt.Errorf("destroying VM %s: %w (state cleared -- check the provider dashboard)", record.VMID, err)
	}
	var cleanupErr error
	if record.SSH != nil {
		forgetHerdrMachines(ctx, record)
		profiles, err := sshidentity.DefaultProfileStore()
		if err != nil {
			cleanupErr = err
		} else if err := profiles.Remove(ctx, record.Name); err != nil {
			cleanupErr = fmt.Errorf("VM destroyed; removing SSH alias %s: %w; repair only this alias and its profiles.json entry under %s", record.SSH.Alias, err, filepath.Join(profiles.ConfigDir, "ssh"))
		}
	}
	return errors.Join(store.Delete(record.Name), cleanupErr)
}

// rescueBeforeDestroy makes every session's work durable before the instance
// is destroyed. All of them, not the first: destroying a droplet holding three
// sessions after rescuing one is the data loss this design exists to prevent.
//
// Any failure stops the destroy. A partial rescue is worse than none, because
// it looks like success. One session failing to rescue does not skip the
// rest, though: stopping early would leave a later session's work unchecked
// and unreported, on top of the one that already failed.
//
// Session and LocalRepo come from the state record rather than from the
// caller's surroundings: down resolves an instance by name and can be run
// from any directory, so neither the session's name nor the repository it
// belongs to is derivable from where the command happens to be typed.
func rescueBeforeDestroy(ctx context.Context, record state.Record) error {
	ctx = record.ConnectionContext(ctx)
	if len(record.Sessions) == 0 {
		// No session was ever started on this instance, so there is
		// nothing an agent could have left behind.
		return nil
	}
	var firstErr error
	for _, s := range record.Sessions {
		provider.ReportProgress(ctx, "rescuing "+s.Name+" before destroy")
		if _, _, err := RescueSession(ctx, record.IP, record.User, s.LocalRepo, s.RepoNameOr(record.Name), s.Name); err != nil {
			// Skip unconditionally, whether or not this is the first failure:
			// a session whose own rescue just failed is a box already known
			// to be a problem, and checking its issues too would only spend
			// a second, doomed round trip on it. Only which error gets
			// reported is limited to the first.
			if firstErr == nil {
				firstErr = fmt.Errorf("could not rescue session %s from %s: %w\n\nthe instance still exists and is still being billed.\n%d of %d sessions were checked; none were removed.\nfix and retry, or destroy anyway with: bivouac down --force", s.Name, record.Name, err, len(record.Sessions), len(record.Sessions))
			}
			continue
		}
		// Issues are the other half of what an agent produced, and down is
		// the verb that makes their loss permanent. RescueSession's own
		// beads step warns rather than failing, because a broken issue sync
		// must not stop a pull -- but it must stop a destroy.
		if err := checkBeadsLanded(ctx, record.IP, record.User, s.LocalRepo, RemoteRepoPath(record.User, s.Name, s.RepoNameOr(record.Name)), s.Name); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// forgetHerdrMachines removes every saved machine this instance's sessions
// registered.
//
// Only the local half. The droplet is about to be destroyed, so its herdr
// sessions go with it and there is nothing on the far side worth stopping
// first -- but the entries in the user's sidebar would otherwise point at a
// host that no longer exists.
func forgetHerdrMachines(ctx context.Context, record state.Record) {
	if err := removeHerdrMachine(localHerdr{ctx: ctx}, record.HerdrMachineID); err != nil {
		provider.ReportWarning(ctx, "herdr: "+err.Error()+
			"\nremove it by hand: herdr machine remove "+record.HerdrMachineID)
	}
	for _, s := range record.Sessions {
		CleanupHerdr(ctx, s.HerdrMachineID, "", nil)
	}
}
