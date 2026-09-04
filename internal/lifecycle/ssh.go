package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// sshArgs builds the argv SSH passes to the ssh binary: an
// interactive session as user on ip's default port.
func sshArgs(ip, user string) []string {
	return []string{user + "@" + ip}
}

// SSH opens an interactive session on the instance at ip as user,
// execing the real ssh binary with stdio passed straight through -- no
// PTY/raw-mode handling of our own. Reuses whatever trust-on-first-connect
// entry already exists in the user's real ~/.ssh/known_hosts from up's
// WaitReady/Connect call.
func SSH(ctx context.Context, ip, user string) error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh not found on PATH: %w", err)
	}
	// #nosec G204 -- argv-array exec.Command, no shell; ip is
	// provider-assigned, never attacker-controlled.
	cmd := exec.CommandContext(ctx, "ssh", sshArgs(ip, user)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
