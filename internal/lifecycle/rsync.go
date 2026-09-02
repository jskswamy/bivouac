package lifecycle

import (
	"context"
	"fmt"
	"os/exec"
)

// rsyncArgs builds the argv Rsync passes to the rsync binary: sync
// ip's remote ~/<remoteName> directory from localRepoRoot's contents,
// using the system ssh binary as rsync's remote shell.
func rsyncArgs(ip, localRepoRoot, remoteName string) []string {
	src := localRepoRoot + "/"
	dst := fmt.Sprintf("root@%s:~/%s/", ip, remoteName)
	return []string{"-az", "-e", "ssh", src, dst}
}

// Rsync copies localRepoRoot's contents to ~/<remoteName> on the
// instance at ip. Relies on WaitReady's Connect call having already
// completed trust-on-first-connect against ~/.ssh/known_hosts, since
// rsync -e ssh spawns the system ssh binary, not this project's Go SSH
// client.
func Rsync(ctx context.Context, ip, localRepoRoot, remoteName string) error {
	if _, err := exec.LookPath("rsync"); err != nil {
		return fmt.Errorf("rsync not found on PATH (run inside `nix develop`, or install it): %w", err)
	}
	cmd := exec.CommandContext(ctx, "rsync", rsyncArgs(ip, localRepoRoot, remoteName)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rsync to %s failed: %w\n%s", ip, err, out)
	}
	return nil
}
