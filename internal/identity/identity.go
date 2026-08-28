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
