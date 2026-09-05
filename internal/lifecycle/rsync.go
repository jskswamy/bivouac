package lifecycle

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/jskswamy/cloudlab/internal/provider"
)

// minRsyncVersion is the floor the flags below require: --mkpath landed
// in 3.2.3, and --protect-args (space-safe argv passing, which
// rsyncPushArgs relies on rather than quoting itself) became the default
// in 3.2.4.
var minRsyncVersion = [2]int{3, 2}

// rsyncVersionRe matches the version in `rsync --version`'s first line.
// GNU rsync prints "rsync  version 3.5.0  protocol version 32";
// openrsync prints "openrsync: protocol version 29" followed by "rsync
// version 2.6.9 compatible", which this matches as 2.6 -- correctly
// below the floor, since it is a 2.6.9-era feature set.
var rsyncVersionRe = regexp.MustCompile(`version (\d+)\.(\d+)`)

// checkRsync reports whether the rsync on PATH is one whose flags this
// package actually uses, returning an actionable error if not.
//
// Worth checking rather than discovering at transfer time: macOS ships
// openrsync (a 2.6.9-compatible reimplementation) as /usr/bin/rsync,
// which supports neither --info=progress2 nor --mkpath. Without this,
// the first sign of trouble is rsync dumping its entire usage text and
// exiting 1, which reads like a cloudlab bug rather than "the rsync you
// have is too old". The failure is also confusingly intermittent: a
// shell inside this repo's nix develop gets rsync 3.x and works, while
// the same command in any other directory does not.
func checkRsync() error {
	path, err := exec.LookPath("rsync")
	if err != nil {
		return fmt.Errorf("rsync not found on PATH (run inside `nix develop`, or install it): %w", err)
	}
	// #nosec G204 -- path comes from exec.LookPath, not user input.
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		// Unreadable version output is not itself fatal: let the real
		// transfer report whatever is actually wrong.
		return nil
	}
	return rsyncVersionError(string(out), path)
}

// rsyncVersionError inspects `rsync --version` output and returns an
// error if that rsync is below minRsyncVersion. Split out from
// checkRsync so the version reasoning is testable without a real binary
// of each vintage on the machine running the tests. Output it cannot
// parse is accepted rather than rejected -- an unknown rsync that works
// is better than a false refusal.
func rsyncVersionError(versionOutput, path string) error {
	m := rsyncVersionRe.FindStringSubmatch(versionOutput)
	if m == nil {
		return nil
	}
	major, minor := atoiOr(m[1], 0), atoiOr(m[2], 0)
	if major > minRsyncVersion[0] || (major == minRsyncVersion[0] && minor >= minRsyncVersion[1]) {
		return nil
	}

	flavour := fmt.Sprintf("rsync %d.%d", major, minor)
	if strings.Contains(versionOutput, "openrsync") {
		flavour = fmt.Sprintf("openrsync (%d.%d-compatible), macOS's built-in rsync", major, minor)
	}
	return fmt.Errorf("%s at %s is too old: cloudlab needs GNU rsync %d.%d+ for --mkpath and --info=progress2.\ninstall a newer one and put it ahead on PATH, e.g. `nix profile install nixpkgs#rsync` or `brew install rsync`",
		flavour, path, minRsyncVersion[0], minRsyncVersion[1])
}

func atoiOr(s string, fallback int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

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
// Relies on rsync's default --protect-args (space-safe argv passing,
// on by default since rsync 3.2.4) for local/remote paths that
// contain spaces -- the same >=3.2.3 floor --mkpath already requires
// in practice guarantees this too.
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
	if err := checkRsync(); err != nil {
		return err
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
	if err := checkRsync(); err != nil {
		return err
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

// Rsync copies localRepoRoot's contents to remotePath on the instance
// at ip as user -- up's one-shot initial seed of the repo. A thin
// wrapper over Push with up's own progress reporting.
func Rsync(ctx context.Context, ip, user, localRepoRoot, remotePath string) error {
	provider.ReportProgress(ctx, "syncing repo to instance")
	return Push(ctx, ip, user, localRepoRoot, remotePath)
}
