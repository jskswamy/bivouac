package identity

import (
	"fmt"
	"os/exec"
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
