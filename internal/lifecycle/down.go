package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/state"
)

// terminateWatch best-effort terminates any existing Mutagen sync
// session named name. Errors -- including "no such session" -- are
// swallowed: the absence of a session to terminate isn't a failure
// for Down or for a Watch restart, both of which call this first.
func terminateWatch(ctx context.Context, name string) {
	_ = exec.CommandContext(ctx, "mutagen", "sync", "terminate", name).Run()
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

	if err := p.Destroy(ctx, record.VMID); err != nil && !errors.Is(err, provider.ErrNotFound) {
		_ = store.Delete(record.Name)
		return fmt.Errorf("destroying VM %s: %w (state cleared -- check the provider dashboard)", record.VMID, err)
	}
	return store.Delete(record.Name)
}
