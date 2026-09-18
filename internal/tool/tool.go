// Package tool runs the external binaries bivouac shells out to.
//
// Shelling out rather than linking libraries is a deliberate, codebase-wide
// choice; this is where the mechanics of doing so live. A leaf package with
// stdlib imports only -- internal/identity imports no internal package at
// all, and it is one of the callers, so anything else here would put this
// out of its reach.
//
// Three shapes, because there are three: a guard that says whether a binary
// is usable and how to get it, a runner that captures what a tool printed,
// and a passthrough that hands the terminal over to it.
package tool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// install describes a binary bivouac depends on: how to name it in an error
// and what to tell the user who does not have it.
//
// A table rather than a sentence at each call site, because the hints had
// already drifted apart -- pkl, nix and rsync named the devshell, while sops,
// which the same devshell supplies and whose absence blocks every secrets
// command, gave the least useful message of the three.
type install struct {
	name string // how the binary is named in the message
	hint string // the actionable half, in parentheses; may be empty
}

const devshell = "run inside `nix develop`, or install it"

var known = map[string]install{
	"pkl":   {"pkl CLI", devshell + ": https://pkl-lang.org/main/current/pkl-cli/index.html#installation"},
	"nix":   {"nix CLI", devshell + ": https://nixos.org/download"},
	"sops":  {"sops", devshell},
	"rsync": {"rsync", devshell},
	"herdr": {"herdr", "install it: https://herdr.dev/"},
	// ssh and bd get no hint: OpenSSH comes with the platform, and a Mac
	// without bd is a supported configuration that skips rather than fails.
	"ssh": {"ssh", ""},
	"bd":  {"bd", ""},
}

// Require reports whether bin is on PATH, returning the resolved path.
//
// The path is returned, not discarded, because a caller that wants to ask
// the binary its version needs the thing LookPath already found rather than
// a second search for it.
func Require(bin string) (string, error) {
	path, err := exec.LookPath(bin)
	if err == nil {
		return path, nil
	}
	desc, ok := known[bin]
	if !ok {
		return "", fmt.Errorf("%s not found on PATH: %w", bin, err)
	}
	if desc.hint == "" {
		return "", fmt.Errorf("%s not found on PATH: %w", desc.name, err)
	}
	return "", fmt.Errorf("%s not found on PATH (%s): %w", desc.name, desc.hint, err)
}

// Run runs bin in dir and returns what it wrote to stdout.
//
// Stdout alone, because every caller of this parses the result as data: a
// repository path, a remote URL, a list of ignored files. Merging stderr in
// would let a git warning corrupt a value that is about to be used as a
// path. Use RunCombined where the output is for a human instead.
//
// An empty dir runs in the current working directory.
func Run(ctx context.Context, dir, bin string, args ...string) (string, error) {
	out, err := command(ctx, dir, bin, args...).Output()
	return string(out), err
}

// RunCombined runs bin in dir and returns stdout and stderr together.
//
// For output that is going into an error or a warning rather than being
// parsed: the tools bivouac runs report the interesting part of a failure
// on stderr, and dropping it leaves the user with an exit status and
// nothing else.
func RunCombined(ctx context.Context, dir, bin string, args ...string) (string, error) {
	out, err := command(ctx, dir, bin, args...).CombinedOutput()
	return string(out), err
}

// Passthrough runs bin with this process's own stdin, stdout and stderr, for
// the commands that hand the terminal over to another program -- ssh, tmux,
// herdr. No PTY or raw-mode handling of bivouac's own; the child talks to
// the real terminal.
func Passthrough(ctx context.Context, bin string, args ...string) error {
	cmd := command(ctx, "", bin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// command builds the exec.Cmd the three runners share.
//
// Always the argv-array form, never a shell: callers that need a remote
// shell build that string explicitly with internal/shellcmd, and each such
// call site documents its own quoting where it happens.
func command(ctx context.Context, dir, bin string, args ...string) *exec.Cmd {
	// #nosec G204 -- bin and args are argv elements, not a shell string, so
	// nothing here is interpreted. What each caller passes, and why it is
	// safe to pass it, is documented at the call site.
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	return cmd
}
