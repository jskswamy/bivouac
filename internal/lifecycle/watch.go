package lifecycle

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/jskswamy/cloudlab/internal/provider"
)

// mutagenCreateArgs builds the argv StartWatch passes to the mutagen
// binary: a named two-way sync session between localRepoRoot and ip's
// remote ~/<name> directory.
func mutagenCreateArgs(ip, name, localRepoRoot string) []string {
	remote := fmt.Sprintf("root@%s:~/%s", ip, name)
	return []string{"sync", "create", "--name=" + name, localRepoRoot, remote}
}

// StartWatch starts a continuous two-way sync session between
// localRepoRoot and ip's remote ~/<name> directory, named after the
// instance. Idempotent: any existing session by that name is
// terminated first, so this doubles as a restart for a stopped or
// dead watch. Relies on WaitReady's Connect call having already
// completed trust-on-first-connect against ~/.ssh/known_hosts, since
// Mutagen's own SSH transport is a separate implementation from this
// project's Go SSH client.
func StartWatch(ctx context.Context, ip, name, localRepoRoot string) error {
	provider.ReportProgress(ctx, "starting continuous watch")
	if _, err := exec.LookPath("mutagen"); err != nil {
		return fmt.Errorf("mutagen not found on PATH (run inside `nix develop`, or install it: https://mutagen.io/documentation/introduction/installation): %w", err)
	}
	terminateWatch(ctx, name)
	// #nosec G204 -- argv-array exec.Command, no shell; ip is
	// provider-assigned, name/localRepoRoot are local identifiers, none
	// attacker-controlled.
	cmd := exec.CommandContext(ctx, "mutagen", mutagenCreateArgs(ip, name, localRepoRoot)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("starting watch for %s failed: %w\n%s", name, err, out)
	}
	return nil
}
