package lifecycle

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/jskswamy/cloudlab/internal/provider"
)

// gitIgnoredExcludes returns local's ignored paths -- per git's own
// ignore rules (.gitignore, .git/info/exclude, global excludes), which
// correctly handles anchored patterns and local-only excludes that a
// naive rsync --filter=':- .gitignore' can't see -- each suitable for
// rsync's --exclude. Directories are returned as single entries (e.g.
// ".gocache/"), not descended into. Returns nil if local isn't a git
// repository or git itself is unavailable: callers then sync
// everything, same as if this didn't exist.
func gitIgnoredExcludes(local string) []string {
	// #nosec G204 -- argv-array exec.Command, no shell; local is the
	// user's own path arg, never attacker-controlled.
	out, err := exec.Command("git", "-C", local, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory").Output()
	if err != nil {
		return nil
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// rsyncPushArgs builds the argv for copying local's contents to ip's
// remote directory remote (already the full path, e.g. "~/myrepo" or
// "~/dataset" -- callers resolve any default before calling this),
// using the system ssh binary as rsync's remote shell. --info=progress2
// gives one continuously-updating overall-transfer line rather than
// spamming a line per file. --exclude=.git plus one --exclude per
// entry in excludes (see gitIgnoredExcludes) keep local's full history
// and build caches off an ephemeral instance that never needed them.
func rsyncPushArgs(ip, local, remote string, excludes []string) []string {
	args := []string{"-az", "--info=progress2", "--exclude=.git"}
	for _, e := range excludes {
		args = append(args, "--exclude="+e)
	}
	src := local + "/"
	dst := fmt.Sprintf("root@%s:%s/", ip, remote)
	return append(args, "-e", "ssh", src, dst)
}

// Push copies local's contents to ip's remote directory remote,
// streaming rsync's own progress output live to the terminal.
func Push(ctx context.Context, ip, local, remote string) error {
	if _, err := exec.LookPath("rsync"); err != nil {
		return fmt.Errorf("rsync not found on PATH (run inside `nix develop`, or install it): %w", err)
	}
	// #nosec G204 -- argv-array exec.Command, no shell; ip is
	// provider-assigned, local/remote are the user's own path args,
	// none attacker-controlled.
	cmd := exec.CommandContext(ctx, "rsync", rsyncPushArgs(ip, local, remote, gitIgnoredExcludes(local))...)
	var buf bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
	cmd.Stderr = io.MultiWriter(os.Stderr, &buf)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rsync to %s failed: %w\n%s", ip, err, buf.String())
	}
	return nil
}

// rsyncPullArgs builds the argv for copying ip's remote directory
// remote (already the full path) back to local. See rsyncPushArgs for
// why --info=progress2 is used.
func rsyncPullArgs(ip, remote, local string) []string {
	src := fmt.Sprintf("root@%s:%s/", ip, remote)
	dst := local + "/"
	return []string{"-az", "--info=progress2", "-e", "ssh", src, dst}
}

// Pull copies ip's remote directory remote back to local, streaming
// rsync's own progress output live to the terminal.
func Pull(ctx context.Context, ip, remote, local string) error {
	if _, err := exec.LookPath("rsync"); err != nil {
		return fmt.Errorf("rsync not found on PATH (run inside `nix develop`, or install it): %w", err)
	}
	// #nosec G204 -- argv-array exec.Command, no shell; ip is
	// provider-assigned, remote/local are the user's own path args,
	// none attacker-controlled.
	cmd := exec.CommandContext(ctx, "rsync", rsyncPullArgs(ip, remote, local)...)
	var buf bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
	cmd.Stderr = io.MultiWriter(os.Stderr, &buf)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rsync from %s failed: %w\n%s", ip, err, buf.String())
	}
	return nil
}

// Rsync copies localRepoRoot's contents to ~/<remoteName> on the
// instance at ip -- up's one-shot initial seed of the repo. A thin
// wrapper over Push with up's own path convention.
func Rsync(ctx context.Context, ip, localRepoRoot, remoteName string) error {
	provider.ReportProgress(ctx, "syncing repo to instance")
	return Push(ctx, ip, localRepoRoot, "~/"+remoteName)
}
