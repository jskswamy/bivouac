package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// herdrArgs builds the argv Herdr passes to the herdr binary: a thin
// client connecting to ip's default background session as user, over
// herdr's own SSH bridge (see https://herdr.dev/docs/how-to-work/).
func herdrArgs(ip, user string) []string {
	return []string{"--remote", "ssh://" + user + "@" + ip}
}

// Herdr opens an interactive herdr thin-client session against the
// instance at ip as user, execing the real herdr binary with stdio
// passed straight through -- same shape as SSH. herdr's own SSH
// bridge handles authentication and host-key trust itself.
func Herdr(ctx context.Context, ip, user string) error {
	// herdr sets HERDR_ENV=1 in every pane it hosts, and refuses to
	// nest a new session inside an existing one -- exec'ing anyway
	// would surface herdr's own generic "remote client exited with
	// exit status: 1", giving no hint why. Catching it here instead
	// gives a clear, specific error without even needing herdr on
	// PATH.
	if os.Getenv("HERDR_ENV") != "" {
		return fmt.Errorf("already inside a herdr session -- herdr disables nested sessions by default; use this session directly, or run `cloudlab ssh`/`cloudlab tmux` instead")
	}
	if _, err := exec.LookPath("herdr"); err != nil {
		return fmt.Errorf("herdr not found on PATH (install it: https://herdr.dev/): %w", err)
	}
	// #nosec G204 -- argv-array exec.Command, no shell; ip is
	// provider-assigned, never attacker-controlled.
	cmd := exec.CommandContext(ctx, "herdr", herdrArgs(ip, user)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
