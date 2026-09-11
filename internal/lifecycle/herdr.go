package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// herdrArgs builds the argv Herdr passes to the herdr binary: a thin
// client connecting to ip as user, over herdr's own SSH bridge (see
// https://herdr.dev/docs/how-to-work/). session, when non-empty, is passed
// as herdr's own --session so each cloudlab session gets its own named
// herdr session -- reconnecting to the same cloudlab session lands back in
// the same herdr session instead of everyone sharing one anonymous session.
// herdr's --remote has no concept of a starting directory (its only cwd
// knob, --cwd, belongs to `herdr workspace create`, a server-side operation
// a --remote attach can't reach) -- landing in the session's checkout is
// cloudlab-7y1's job, done by having `session start` create that workspace.
func herdrArgs(ip, user, session string) []string {
	args := []string{"--remote", "ssh://" + user + "@" + ip}
	if session != "" {
		args = append(args, "--session", session)
	}
	return args
}

// Herdr opens an interactive herdr thin-client session against the
// instance at ip as user, execing the real herdr binary with stdio
// passed straight through -- same shape as SSH. herdr's own SSH
// bridge handles authentication and host-key trust itself. session
// names the herdr session to use/create (see herdrArgs); empty means
// herdr's own default, unnamed session.
func Herdr(ctx context.Context, ip, user, session string) error {
	// Inside herdr this must not exec a client: herdr refuses nested
	// sessions, and doing it anyway surfaces its own generic "remote
	// client exited with exit status: 1". That refusal is now the machine
	// path's cue instead -- see AttachMachine, and the router in the
	// command layer that chooses between them.
	if InsideHerdr() {
		return fmt.Errorf("already inside a herdr session -- attach the instance as a saved machine instead, or run `cloudlab ssh`/`cloudlab tmux`")
	}
	if _, err := exec.LookPath("herdr"); err != nil {
		return fmt.Errorf("herdr not found on PATH (install it: https://herdr.dev/): %w", err)
	}
	// #nosec G204 -- argv-array exec.Command, no shell; ip is
	// provider-assigned, never attacker-controlled, and session is a
	// cloudlab session name already checked by lifecycle.CheckSessionName.
	cmd := exec.CommandContext(ctx, "herdr", herdrArgs(ip, user, session)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
