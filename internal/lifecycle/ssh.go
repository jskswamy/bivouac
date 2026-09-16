package lifecycle

import (
	"context"

	"github.com/jskswamy/cloudlab/internal/shellcmd"
	"github.com/jskswamy/cloudlab/internal/tool"
)

// sshArgs builds the argv SSH passes to the ssh binary: an
// interactive session as user on ip's default port. When dir is
// non-empty, forces PTY allocation (-t) and runs a remote command
// that cds into dir before handing off to an interactive login shell
// -- the same shellcmd.LoginShell wrapper tmuxArgs uses, so profile
// scripts (and PATH) are set up the same way.
// dir is shell-quoted via shellcmd.Quote since ssh concatenates
// the trailing argv into one string the remote shell parses. An empty
// dir returns today's plain form: no remote command, ssh's own
// default session handling.
func sshArgs(ip, user, dir string, forwardAgent bool) []string {
	var flags []string
	if forwardAgent {
		flags = append(flags, "-A")
	}
	if dir == "" {
		return append(flags, user+"@"+ip)
	}
	inner := "[[ -d " + shellcmd.Quote(dir) + " ]] && cd " + shellcmd.Quote(dir) + "; exec \"$SHELL\" -l"
	return append(flags, "-t", user+"@"+ip, shellcmd.LoginShell(inner))
}

// SSH opens an interactive session on the instance at ip as user,
// cd'ing into dir first if it's non-empty, execing the real ssh
// binary with stdio passed straight through -- no PTY/raw-mode
// handling of our own beyond -t when dir is set. Reuses whatever
// trust-on-first-connect entry already exists in the user's real
// ~/.ssh/known_hosts from up's WaitReady/Connect call.
func SSH(ctx context.Context, ip, user, dir string, forwardAgent bool) error {
	if _, err := tool.Require("ssh"); err != nil {
		return err
	}
	// Argv-array exec, no local shell; ip is
	// provider-assigned, never attacker-controlled. When dir is set,
	// the remote command IS shell-interpreted by sshd's login shell,
	// but dir is shell-quoted via shellcmd.Quote before being
	// embedded.
	return tool.Passthrough(ctx, "ssh", sshArgs(ip, user, dir, forwardAgent)...)
}
