package identity

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// RepoRoot walks up from repoFlag (if set) or cwd to find a git repo
// root, via `git rev-parse --show-toplevel`. It is used only by commands
// that need actual repo content (currently: up).
func RepoRoot(cwd, repoFlag string) (string, error) {
	start := cwd
	if repoFlag != "" {
		start = repoFlag
	}

	out, err := exec.Command("git", "-C", start, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		if repoFlag != "" {
			return "", fmt.Errorf("not a git repository: %s", repoFlag)
		}
		return "", fmt.Errorf("not inside a git repository; use --repo <path>")
	}
	return strings.TrimSpace(string(out)), nil
}

// DeriveName derives an instance name from a resolved repo root: the
// slugified owner/repo from its origin remote, or the root folder's name
// if there's no origin remote configured.
func DeriveName(root string) (string, error) {
	out, err := exec.Command("git", "-C", root, "remote", "get-url", "origin").Output()
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
func InstanceName(cwd, repoFlag, positional, nameFlag string) (string, error) {
	if positional != "" {
		return positional, nil
	}
	if nameFlag != "" {
		return nameFlag, nil
	}

	root, err := RepoRoot(cwd, repoFlag)
	if err != nil {
		if repoFlag != "" {
			return "", err
		}
		return "", fmt.Errorf("no instance name given; use --name or run from inside a repo")
	}
	return DeriveName(root)
}
