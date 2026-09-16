package lifecycle

import (
	"context"

	"github.com/jskswamy/cloudlab/internal/shellcmd"
	"github.com/jskswamy/cloudlab/internal/tool"
)

// tmuxArgs builds the argv Tmux passes to the ssh binary: a PTY
// session as user on ip's default port, running tmux's own
// create-or-attach primitive for session ("new-session -A": if the
// named session already exists, attach; otherwise create it) inside a
// login shell, so tmux -- installed into the instance user's
// home-manager profile, not a system path -- is actually on PATH; see
// shellcmd.LoginShell. session is shell-quoted since ssh concatenates
// the trailing argv into one string that sshd hands to the remote
// shell to parse.
func tmuxArgs(ip, user, session string, forwardAgent bool) []string {
	var flags []string
	if forwardAgent {
		flags = append(flags, "-A")
	}
	inner := "tmux new-session -A -s " + shellcmd.Quote(session)
	return append(flags, "-t", user+"@"+ip, shellcmd.LoginShell(inner))
}

// Tmux opens an interactive tmux session named session on the
// instance at ip as user (creating it first if it doesn't already
// exist), execing the real ssh binary with stdio passed straight
// through -- same shape as SSH. -t forces PTY allocation, which tmux
// requires.
func Tmux(ctx context.Context, ip, user, session string, forwardAgent bool) error {
	if _, err := tool.Require("ssh"); err != nil {
		return err
	}
	// Argv-array exec locally, no local shell;
	// the remote command IS shell-interpreted by sshd's login shell,
	// but session is shell-quoted via shellcmd.Quote before
	// being embedded, and ip/user are never attacker-controlled.
	return tool.Passthrough(ctx, "ssh", tmuxArgs(ip, user, session, forwardAgent)...)
}
