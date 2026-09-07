package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// ConnectTarget decides how to reach a listener and returns the URL to
// use plus whether an SSH forward is required first.
//
// Two conditions force a forward, and the second is the one that is
// easy to miss: no tailnet address at all, or a service bound to
// loopback. A loopback-bound service is not reachable at the instance's
// tailnet address no matter how healthy the tailnet is -- and binding
// to 127.0.0.1 is the default for Jupyter and most dev servers, so this
// is the common path rather than an edge case.
//
// The scheme is only printed for ports that plausibly speak HTTP; for
// the rest the bare host:port is the honest answer, since a URL invites
// a client the service will not understand.
func ConnectTarget(tailnetIP string, l Listener) (string, bool) {
	scheme := ""
	if l.HTTP() {
		scheme = "http://"
	}
	if tailnetIP == "" || l.LoopbackOnly() {
		return scheme + "localhost:" + strconv.Itoa(l.Port), true
	}
	return scheme + tailnetIP + ":" + strconv.Itoa(l.Port), false
}

// Forward runs `ssh -L` in the foreground until the user interrupts it.
//
// Foreground deliberately: a backgrounded tunnel is a process someone
// has to find and kill later, which is what state.Record.TunnelPID
// existed for and why the original design stalled half-built. Ctrl-C
// ends this one, and nothing is written to state.
func Forward(ctx context.Context, ip, user string, port int) error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh not found on PATH: %w", err)
	}
	spec := fmt.Sprintf("%d:localhost:%d", port, port)
	// #nosec G204 -- argv-array exec.Command, no shell. ip is
	// provider-assigned and port is an int, so neither can inject.
	cmd := exec.CommandContext(ctx, "ssh", "-N", "-L", spec, user+"@"+ip)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
