package identity

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jskswamy/bivouac/internal/tool"
)

// RepoRoot walks up from repoFlag (if set) or cwd to find the MAIN git
// repository root -- not a linked worktree. Used by every command that needs
// actual repo content: up, provision, session start, and resolveSessionArg,
// which fronts pull, merge, delete, ssh, tmux and herdr.
func RepoRoot(ctx context.Context, cwd, repoFlag string) (string, error) {
	start := cwd
	if repoFlag != "" {
		start = repoFlag
	}

	// --git-common-dir, not --show-toplevel: inside a linked worktree the
	// latter returns the worktree, and every session command needs the main
	// repository -- merge replays onto the user's branch, which lives there.
	// The common dir is the main repo's .git, so its parent is the main tree.
	// Stdout only: the result is used as a filesystem path, so a git
	// warning on stderr must not be able to corrupt it. start is a local
	// filesystem path (cwd or --repo), never attacker-controlled.
	out, err := tool.Run(ctx, start, "git", "rev-parse", "--git-common-dir")
	if err != nil {
		if repoFlag != "" {
			return "", fmt.Errorf("not a git repository: %s", repoFlag)
		}
		return "", fmt.Errorf("not inside a git repository; use --repo <path>")
	}
	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		// Relative (".git") when already at the main tree's root.
		abs, err := filepath.Abs(filepath.Join(start, gitDir))
		if err != nil {
			return "", err
		}
		gitDir = abs
	}
	root := filepath.Dir(gitDir)
	// git resolves symlinks in its own output (e.g. --show-toplevel), but a
	// relative --git-common-dir joined above keeps whatever symlinks were in
	// start (macOS's /var -> /private/var). Resolve here so callers -- and
	// the existing tests -- see the same real path either way.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return root, nil
}

// DeriveName derives an instance name from a resolved repo root: the
// slugified owner/repo from its origin remote, or the root folder's name
// if there's no origin remote configured.
func DeriveName(ctx context.Context, root string) (string, error) {
	// Stdout only: the result is slugified into an instance name. root is a
	// resolved local repo path, never attacker-controlled.
	out, err := tool.Run(ctx, root, "git", "remote", "get-url", "origin")
	if err != nil {
		return filepath.Base(root), nil
	}
	return slugify(string(out)), nil
}

// slugify turns a git remote URL (https://host/owner/repo.git,
// git@host:owner/repo.git, or ssh://git@host/owner/repo.git) into
// "owner-repo" by taking the last two path segments.
func slugify(remoteURL string) string {
	url := strings.TrimSuffix(strings.TrimSpace(remoteURL), ".git")
	tokens := strings.FieldsFunc(url, func(r rune) bool {
		return r == '/' || r == ':'
	})
	if len(tokens) < 2 {
		return strings.ToLower(strings.Join(tokens, "-"))
	}
	last := tokens[len(tokens)-2:]
	return strings.ToLower(strings.Join(last, "-"))
}

// InstanceName resolves a name for lookup-only commands (everything
// except up): positional arg, then --name, then (if cwd or repoFlag is
// inside a git repo) that repo's derived name. Unlike RepoRoot, it
// succeeds without any git repo present as long as positional or
// nameFlag is given — lookup commands only need a name to find an
// already-existing instance in state, they never touch repo content.
func InstanceName(ctx context.Context, cwd, repoFlag, positional, nameFlag string) (string, error) {
	if positional != "" {
		return positional, nil
	}
	if nameFlag != "" {
		return nameFlag, nil
	}

	root, err := RepoRoot(ctx, cwd, repoFlag)
	if err != nil {
		if repoFlag != "" {
			return "", err
		}
		return "", fmt.Errorf("no instance name given; use --name or run from inside a repo")
	}
	return DeriveName(ctx, root)
}

// InstanceIdentity resolves both the repository root and the instance name,
// for the commands that need the repository itself -- up and provision read
// bivouac.pkl out of it.
//
// Not a widening of InstanceName, and the difference is the point: this
// resolves the root FIRST and returns that error even when a positional name
// or --name was given, because a named instance still has no bivouac.pkl
// outside a repository. InstanceName is for the lookup commands, which only
// need a key into state and succeed with no repository at all.
func InstanceIdentity(ctx context.Context, cwd, repoFlag, positional, nameFlag string) (root, name string, err error) {
	root, err = RepoRoot(ctx, cwd, repoFlag)
	if err != nil {
		return "", "", err
	}
	name = nameFlag
	if positional != "" {
		name = positional
	}
	if name == "" {
		if name, err = DeriveName(ctx, root); err != nil {
			return "", "", err
		}
	}
	return root, name, nil
}
