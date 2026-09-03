package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// sshArgs builds the argv SSH passes to the ssh binary: an
// interactive session as root on ip's default port.
func sshArgs(ip string) []string {
	return []string{"root@" + ip}
}

// SSH opens an interactive session on the instance at ip, execing the
// real ssh binary with stdio passed straight through -- no PTY/raw-mode
// handling of our own. Reuses whatever trust-on-first-connect entry
// already exists in the user's real ~/.ssh/known_hosts from up's
// WaitReady/Connect call.
func SSH(ctx context.Context, ip string) error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh not found on PATH: %w", err)
	}
	cmd := exec.CommandContext(ctx, "ssh", sshArgs(ip)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
