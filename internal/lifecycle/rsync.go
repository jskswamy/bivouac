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
// "~/dataset" -- callers resolve any default before calling this) as
// user, using the system ssh binary as rsync's remote shell.
// --info=progress2 gives one continuously-updating overall-transfer
// line rather than spamming a line per file. --mkpath creates every
// missing destination path component using rsync's own real
// permission check -- correct whether remote is under the remote
// user's home (always writable) or an arbitrary absolute path (only
// writable if they already own something in that chain).
// --exclude=.git plus one --exclude per entry in excludes (see
// gitIgnoredExcludes) keep local's full history and build caches off
// an ephemeral instance that never needed them.
func rsyncPushArgs(ip, user, local, remote string, excludes []string) []string {
	args := []string{"-az", "--info=progress2", "--mkpath", "--exclude=.git"}
	for _, e := range excludes {
		args = append(args, "--exclude="+e)
	}
	src := local + "/"
	dst := fmt.Sprintf("%s@%s:%s/", user, ip, remote)
	return append(args, "-e", "ssh", src, dst)
}

// Push copies local's contents to ip's remote directory remote as
// user, streaming rsync's own progress output live to the terminal.
func Push(ctx context.Context, ip, user, local, remote string) error {
	if _, err := exec.LookPath("rsync"); err != nil {
		return fmt.Errorf("rsync not found on PATH (run inside `nix develop`, or install it): %w", err)
	}
	// #nosec G204 -- argv-array exec.Command, no shell; ip is
	// provider-assigned, user/local/remote are local identifiers/path
	// args, none attacker-controlled.
	cmd := exec.CommandContext(ctx, "rsync", rsyncPushArgs(ip, user, local, remote, gitIgnoredExcludes(local))...)
	var buf bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
	cmd.Stderr = io.MultiWriter(os.Stderr, &buf)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rsync to %s failed: %w\n%s\nif %s doesn't exist or isn't writable, create it and chown it to %s on the instance, then retry", ip, err, buf.String(), remote, user)
	}
	return nil
}

// rsyncPullArgs builds the argv for copying ip's remote directory
// remote (already the full path) back to local as user. See
// rsyncPushArgs for why --info=progress2 is used.
func rsyncPullArgs(ip, user, remote, local string) []string {
	src := fmt.Sprintf("%s@%s:%s/", user, ip, remote)
	dst := local + "/"
	return []string{"-az", "--info=progress2", "-e", "ssh", src, dst}
}

// Pull copies ip's remote directory remote back to local as user,
// streaming rsync's own progress output live to the terminal.
func Pull(ctx context.Context, ip, user, remote, local string) error {
	if _, err := exec.LookPath("rsync"); err != nil {
		return fmt.Errorf("rsync not found on PATH (run inside `nix develop`, or install it): %w", err)
	}
	// #nosec G204 -- argv-array exec.Command, no shell; ip is
	// provider-assigned, user/remote/local are local identifiers/path
	// args, none attacker-controlled.
	cmd := exec.CommandContext(ctx, "rsync", rsyncPullArgs(ip, user, remote, local)...)
	var buf bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
	cmd.Stderr = io.MultiWriter(os.Stderr, &buf)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rsync from %s failed: %w\n%s", ip, err, buf.String())
	}
	return nil
}

// Rsync copies localRepoRoot's contents to ~/<remoteName> on the
// instance at ip as user -- up's one-shot initial seed of the repo. A
// thin wrapper over Push with up's own path convention.
func Rsync(ctx context.Context, ip, user, localRepoRoot, remoteName string) error {
	provider.ReportProgress(ctx, "syncing repo to instance")
	return Push(ctx, ip, user, localRepoRoot, "~/"+remoteName)
}
