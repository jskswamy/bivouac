package lifecycle

import (
	"context"
	"fmt"
	"strings"

	"github.com/jskswamy/cloudlab/internal/tool"
)

// runLocalGit runs a git command on this machine inside localRepo.
//
// Combined output: every caller puts the text into a warning or an error,
// and git says what actually went wrong on stderr. localRepo is the user's
// own repo path and args are built by this package.
func runLocalGit(ctx context.Context, localRepo string, args ...string) (string, error) {
	return tool.RunCombined(ctx, localRepo, "git", args...)
}

// trimLine returns s without surrounding whitespace or a trailing newline.
func trimLine(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

// nonEmptyLines splits s on newlines, dropping blanks.
func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if trimmed := trimLine(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// HeadCommit returns the SHA localRepo's HEAD points at. Exported so session
// start can record the commit a session branches from; merge needs it to tell
// the session's own work apart from history that was rewritten underneath it.
func HeadCommit(ctx context.Context, localRepo string) (string, error) {
	out, err := runLocalGit(ctx, localRepo, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("reading HEAD in %s: %w\n%s", localRepo, err, out)
	}
	return trimLine(out), nil
}

// verifySignatures reports each commit in revRange with its signature
// state, and fails if any is unsigned. Signing was the reason to cherry-pick
// rather than squash, so an unsigned result is a failure, not a detail.
//
// An empty range is also a failure. It is only ever called where commits are
// known to have been replayed, so no lines means the range was computed
// wrongly -- and a verification that inspects nothing must never be mistaken
// for one that passed, since deletion is downstream of it.
func verifySignatures(ctx context.Context, localRepo, revRange string) ([]string, error) {
	out, err := runLocalGit(ctx, localRepo, signatureStatusArgs(revRange)...)
	if err != nil {
		return nil, fmt.Errorf("checking signatures: %w\n%s", err, out)
	}
	lines := nonEmptyLines(out)
	if len(lines) == 0 {
		return nil, fmt.Errorf("no commits found in range %s — refusing to treat an empty signature check as a pass", revRange)
	}
	for _, line := range lines {
		if !strings.HasSuffix(line, " G") {
			// %G? reports what git can *verify*, not what was signed, so the
			// usual cause is a missing trust store rather than a missing key:
			// ssh signing needs gpg.ssh.allowedSignersFile to exist and list
			// your own key, and without it every commit reports N even though
			// cherry-pick -S signed it. Naming only gpgsign/signingkey here
			// sends people to settings that are already correct.
			return nil, fmt.Errorf("git cannot verify commit %q\ncheck that commit.gpgsign and user.signingkey are set, and — for ssh signing — that gpg.ssh.allowedSignersFile points at a file listing your own signing key\nthe replayed commits have been rolled back; fix the config and re-run", line)
		}
	}
	return lines, nil
}

// localIdentity reads the git identity that applies in localRepo.
//
// Plain `git config` rather than `--local`, so the value is resolved the way
// git itself resolves it -- through the repository, then the user's global
// config, then system. Most people set this once in ~/.gitconfig and never
// per-repo, so reading only the local scope would find nothing for almost
// everyone.
func localIdentity(ctx context.Context, localRepo string) (string, string, error) {
	name, err := runLocalGit(ctx, localRepo, "config", "user.name")
	if err != nil {
		return "", "", fmt.Errorf("reading user.name: %w\nset it with `git config --global user.name \"Your Name\"` -- the instance needs an identity to commit with, and without one git invents one from the hostname", err)
	}
	email, err := runLocalGit(ctx, localRepo, "config", "user.email")
	if err != nil {
		return "", "", fmt.Errorf("reading user.email: %w\nset it with `git config --global user.email you@example.com` -- the instance needs an identity to commit with, and without one git invents one from the hostname", err)
	}
	return trimLine(name), trimLine(email), nil
}
