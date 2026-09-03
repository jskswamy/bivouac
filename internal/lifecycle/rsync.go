package lifecycle

import (
	"context"
	"fmt"
	"os/exec"
)

// rsyncPushArgs builds the argv for copying local's contents to ip's
// remote directory remote (already the full path, e.g. "~/myrepo" or
// "~/dataset" -- callers resolve any default before calling this),
// using the system ssh binary as rsync's remote shell.
func rsyncPushArgs(ip, local, remote string) []string {
	src := local + "/"
	dst := fmt.Sprintf("root@%s:%s/", ip, remote)
	return []string{"-az", "-e", "ssh", src, dst}
}

// Push copies local's contents to ip's remote directory remote.
func Push(ctx context.Context, ip, local, remote string) error {
	if _, err := exec.LookPath("rsync"); err != nil {
		return fmt.Errorf("rsync not found on PATH (run inside `nix develop`, or install it): %w", err)
	}
	// #nosec G204 -- argv-array exec.Command, no shell; ip is
	// provider-assigned, local/remote are the user's own path args,
	// none attacker-controlled.
	cmd := exec.CommandContext(ctx, "rsync", rsyncPushArgs(ip, local, remote)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rsync to %s failed: %w\n%s", ip, err, out)
	}
	return nil
}

// rsyncPullArgs builds the argv for copying ip's remote directory
// remote (already the full path) back to local.
func rsyncPullArgs(ip, remote, local string) []string {
	src := fmt.Sprintf("root@%s:%s/", ip, remote)
	dst := local + "/"
	return []string{"-az", "-e", "ssh", src, dst}
}

// Pull copies ip's remote directory remote back to local.
func Pull(ctx context.Context, ip, remote, local string) error {
	if _, err := exec.LookPath("rsync"); err != nil {
		return fmt.Errorf("rsync not found on PATH (run inside `nix develop`, or install it): %w", err)
	}
	// #nosec G204 -- argv-array exec.Command, no shell; ip is
	// provider-assigned, remote/local are the user's own path args,
	// none attacker-controlled.
	cmd := exec.CommandContext(ctx, "rsync", rsyncPullArgs(ip, remote, local)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rsync from %s failed: %w\n%s", ip, err, out)
	}
	return nil
}

// Rsync copies localRepoRoot's contents to ~/<remoteName> on the
// instance at ip -- up's one-shot initial seed of the repo. A thin
// wrapper over Push with up's own path convention.
func Rsync(ctx context.Context, ip, localRepoRoot, remoteName string) error {
	return Push(ctx, ip, localRepoRoot, "~/"+remoteName)
}
