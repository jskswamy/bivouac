package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// pairArgs builds the argv Pair passes to the ssh binary: a PTY
// session as user on ip, running moshi-hook's own Easy Pair QR flow
// with --host pinned to ip itself so the QR always advertises the
// instance's real public address, rather than falling into
// moshi-hook's own interactive address-selector prompt (which would
// have no terminal to prompt on anyway, running non-interactively
// like this). Runs inside a login shell for the same PATH reason
// tmuxArgs does -- moshi-hook lives in the instance user's
// home-manager profile, not a system path.
func pairArgs(ip, user string) []string {
	inner := "moshi-hook host setup --host " + reconcile.ShellQuote(ip)
	return []string{"-t", user + "@" + ip, "bash -lc " + reconcile.ShellQuote(inner)}
}

// Pair runs moshi-hook's Easy Pair QR flow on the instance at ip as
// user, execing the real ssh binary with stdio passed straight
// through -- same shape as Tmux/SSH. Blocks in the foreground showing
// the QR until scanned/claimed, or until Ctrl+C.
func Pair(ctx context.Context, ip, user string) error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh not found on PATH: %w", err)
	}
	// #nosec G204 -- argv-array exec.Command locally, no local shell;
	// the remote command IS shell-interpreted by sshd's login shell,
	// but ip is shell-quoted via reconcile.ShellQuote before being
	// embedded, and ip/user are never attacker-controlled regardless.
	cmd := exec.CommandContext(ctx, "ssh", pairArgs(ip, user)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
