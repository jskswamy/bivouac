package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// sshArgs builds the argv SSH passes to the ssh binary: an
// interactive session as user on ip's default port. When dir is
// non-empty, forces PTY allocation (-t) and runs a remote command
// that cds into dir before handing off to an interactive login shell
// -- the same outer "bash -lc <quoted-inner>" shape tmux.go already
// established, so profile scripts (and PATH) are set up the same way.
// dir is shell-quoted via reconcile.ShellQuote since ssh concatenates
// the trailing argv into one string the remote shell parses. An empty
// dir returns today's plain form: no remote command, ssh's own
// default session handling.
func sshArgs(ip, user, dir string) []string {
	if dir == "" {
		return []string{user + "@" + ip}
	}
	inner := "[[ -d " + reconcile.ShellQuote(dir) + " ]] && cd " + reconcile.ShellQuote(dir) + "; exec \"$SHELL\" -l"
	return []string{"-t", user + "@" + ip, "bash -lc " + reconcile.ShellQuote(inner)}
}

// SSH opens an interactive session on the instance at ip as user,
// cd'ing into dir first if it's non-empty, execing the real ssh
// binary with stdio passed straight through -- no PTY/raw-mode
// handling of our own beyond -t when dir is set. Reuses whatever
// trust-on-first-connect entry already exists in the user's real
// ~/.ssh/known_hosts from up's WaitReady/Connect call.
func SSH(ctx context.Context, ip, user, dir string) error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh not found on PATH: %w", err)
	}
	// #nosec G204 -- argv-array exec.Command, no shell; ip is
	// provider-assigned, never attacker-controlled. When dir is set,
	// the remote command IS shell-interpreted by sshd's login shell,
	// but dir is shell-quoted via reconcile.ShellQuote before being
	// embedded.
	cmd := exec.CommandContext(ctx, "ssh", sshArgs(ip, user, dir)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
