package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// pairArgs builds the argv Pair passes to the ssh binary: a PTY
// session as user on sshHost, running moshi-hook's own Easy Pair QR
// flow with --host pinned to advertiseHost, rather than falling into
// moshi-hook's own interactive address-selector prompt. Runs inside a
// login shell for the same PATH reason tmuxArgs does -- moshi-hook
// lives in the instance user's home-manager profile, not a system path.
//
// The two hosts are deliberately separate. sshHost is how *this*
// machine reaches the instance to run the flow, and is always the
// public address, which is reachable unconditionally. advertiseHost is
// what the QR tells the phone to connect back to, and may be the
// instance's Tailscale address -- so pairing keeps working even when
// this machine is not currently on the tailnet, while still handing the
// phone a private address to use.
func pairArgs(sshHost, advertiseHost, user string) []string {
	inner := "moshi-hook host setup --host " + reconcile.ShellQuote(advertiseHost)
	return []string{"-t", user + "@" + sshHost, "bash -lc " + reconcile.ShellQuote(inner)}
}

// Pair runs moshi-hook's Easy Pair QR flow on the instance, connecting
// over sshHost and advertising advertiseHost in the QR, execing the
// real ssh binary with stdio passed straight through -- same shape as
// Tmux/SSH. Blocks in the foreground showing the QR until
// scanned/claimed, or until Ctrl+C.
func Pair(ctx context.Context, sshHost, advertiseHost, user string) error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh not found on PATH: %w", err)
	}
	// #nosec G204 -- argv-array exec.Command locally, no local shell;
	// the remote command IS shell-interpreted by sshd's login shell,
	// but advertiseHost is shell-quoted via reconcile.ShellQuote before
	// being embedded, and the hosts/user are never attacker-controlled.
	cmd := exec.CommandContext(ctx, "ssh", pairArgs(sshHost, advertiseHost, user)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
