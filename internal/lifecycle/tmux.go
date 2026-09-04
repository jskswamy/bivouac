package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// tmuxArgs builds the argv Tmux passes to the ssh binary: a PTY
// session as user on ip's default port, running tmux's own
// create-or-attach primitive for session ("new-session -A": if the
// named session already exists, attach; otherwise create it) inside a
// login shell (bash -lc) so tmux -- installed into the instance
// user's home-manager profile, not a system path -- is actually on
// PATH. A non-interactive ssh command otherwise runs a non-login
// shell, which never sources the profile scripts that put
// ~/.nix-profile/bin on PATH (see reconcile.go's own "nix run
// home-manager" invocation for the same requirement). session is
// shell-quoted since ssh concatenates the trailing argv into one
// string that sshd hands to the remote shell to parse.
func tmuxArgs(ip, user, session string) []string {
	inner := "tmux new-session -A -s " + reconcile.ShellQuote(session)
	return []string{"-t", user + "@" + ip, "bash -lc " + reconcile.ShellQuote(inner)}
}

// Tmux opens an interactive tmux session named session on the
// instance at ip as user (creating it first if it doesn't already
// exist), execing the real ssh binary with stdio passed straight
// through -- same shape as SSH. -t forces PTY allocation, which tmux
// requires.
func Tmux(ctx context.Context, ip, user, session string) error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh not found on PATH: %w", err)
	}
	// #nosec G204 -- argv-array exec.Command locally, no local shell;
	// the remote command IS shell-interpreted by sshd's login shell,
	// but session is shell-quoted via reconcile.ShellQuote before
	// being embedded, and ip/user are never attacker-controlled.
	cmd := exec.CommandContext(ctx, "ssh", tmuxArgs(ip, user, session)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
