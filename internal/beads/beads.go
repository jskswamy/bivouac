// Package beads wires a repository's beads issue database into a cloudlab
// session, so the agent on the instance can read the issue it was given,
// close it, and file the follow-ups it finds.
//
// Everything here shells out to the `bd` binary; it is never linked as a
// library, matching how this codebase already treats sops, tailscale, nix
// and git.
//
// The transport is the session's own git repository. A dolt remote whose URL
// carries the git+ scheme stores the database as refs/dolt/data inside an
// ordinary git repository, and cloudlab already maintains exactly such a
// repository per session, reachable over the SSH channel the session's code
// already uses. No credential ever reaches the instance for this to work.
package beads

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
)

// Available reports whether bd is on this machine's PATH. Callers use it to
// skip rather than to fail: a Mac without beads installed is a supported
// configuration, it just gets a session with no issue tracker.
func Available() bool {
	_, err := exec.LookPath("bd")
	return err == nil
}

// Version reports this machine's `bd version` banner
// ("bd version 1.1.2 (20e493e56: HEAD@20e493e569c9)"), compared against the
// instance's because a shared Dolt database is the one place a version gap
// does real damage. Read next to Available so both live where a caller
// checks the same binary two different ways -- whether it exists here at
// all, versus what version it reports.
//
// No repo argument: `bd version` answers regardless of directory, and
// nothing here needs one.
func Version(ctx context.Context) (string, error) {
	out, err := run(ctx, "", "version")
	if err != nil {
		return "", fmt.Errorf("bd version: %w\n%s", err, out)
	}
	return out, nil
}

// versionNumberPattern picks the version number out of a `bd version`
// banner, discarding the build hash and ref that change on every release
// even when the version itself has not.
var versionNumberPattern = regexp.MustCompile(`\d+\.\d+\.\d+`)

// VersionsMatch reports whether two `bd version` banners name the same
// version number -- the part that matters for a shared Dolt database -- not
// whether the banners are identical. Empty or unparseable input never
// matches, including against itself: a banner this cannot read is not
// evidence that two machines agree.
func VersionsMatch(a, b string) bool {
	na, nb := versionNumberPattern.FindString(a), versionNumberPattern.FindString(b)
	return na != "" && na == nb
}

// Present reports whether repo has a beads database at all. Checked before
// invoking bd, so the overwhelmingly common case -- a repository that has
// never used beads -- costs a stat rather than a process.
func Present(repo string) bool {
	info, err := os.Stat(filepath.Join(repo, ".beads"))
	return err == nil && info.IsDir()
}

// run executes bd inside repo and returns its combined output.
//
// CombinedOutput, not Output: bd reports the interesting part of a failure on
// stderr, and every caller here puts that text into the warning or error it
// surfaces.
func run(ctx context.Context, repo string, args ...string) (string, error) {
	// #nosec G204 -- argv-array exec.Command, no shell; repo is the user's
	// own repository path and args are built by this package.
	cmd := exec.CommandContext(ctx, "bd", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	return string(out), err
}
