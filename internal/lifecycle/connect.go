package lifecycle

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
)

// ErrLocalPortBusy is returned when a caller asked for a specific local
// port and something already holds it. Distinct from the auto-resolving
// path: an explicit request is honoured or refused, never redirected,
// because the caller chose that number for a reason cloudlab cannot see.
var ErrLocalPortBusy = fmt.Errorf("local port is already in use")

// FreeLocalPort returns a local port to forward through.
//
// preferred is tried first, so the usual case keeps the local and remote
// numbers the same and the URL stays predictable. When it is taken --
// which happens most often because the same service is running locally,
// exactly the case of forwarding a remote 24543 while a local one holds
// that port -- the OS is asked for any free port instead of failing.
//
// mustUse makes the preferred port mandatory: the caller named it, so a
// collision is an error rather than an invitation to pick something else.
//
// The port is closed before it is returned, so a race remains where
// something else claims it first. That is unavoidable without holding
// the socket and handing the descriptor to ssh; ssh's own
// ExitOnForwardFailure still catches the loser of that race, and this
// turns the common case from a stack of ssh diagnostics into a number
// that works.
func FreeLocalPort(preferred int, mustUse bool) (int, error) {
	if free := tryListen(preferred); free {
		return preferred, nil
	}
	if mustUse {
		return 0, fmt.Errorf("%w: %d", ErrLocalPortBusy, preferred)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("finding a free local port: %w", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// tryListen reports whether port can be bound on 127.0.0.1 right now.
// The bind address matches forwardArgs's, so this tests the same thing
// ssh is about to attempt rather than something adjacent to it.
func tryListen(port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// ConnectTarget decides how to reach a listener and returns the URL to
// use plus whether an SSH forward is required first.
//
// Two conditions force a forward, and the second is the one that is
// easy to miss: no tailnet address at all, or a service bound to
// loopback. A loopback-bound service is not reachable at the instance's
// tailnet address no matter how healthy the tailnet is -- and binding
// to 127.0.0.1 is the default for Jupyter and most dev servers, so this
// is the common path rather than an edge case.
// The scheme is only printed for ports that plausibly speak HTTP; for
// the rest the bare host:port is the honest answer, since a URL invites
// a client the service will not understand.
//
// localPort is the near side of the forward and is only used on that
// branch. It is usually the same as the listener's port but need not
// be, so the URL is built from it rather than from l.Port -- printing
// the remote number would send the user to a port nothing is listening
// on locally whenever the two differ.
func ConnectTarget(tailnetIP string, l Listener, localPort int) (string, bool) {
	scheme := ""
	if l.HTTP() {
		scheme = "http://"
	}
	if tailnetIP == "" || l.LoopbackOnly() {
		return scheme + "localhost:" + strconv.Itoa(localPort), true
	}
	return scheme + tailnetIP + ":" + strconv.Itoa(l.Port), false
}

// VisibleListeners drops the machine's own sockets unless all is set.
//
// Filtering here rather than in Listeners keeps discovery honest: the
// full list is what --all shows and what an error message can count,
// and only the choice offered to the user is narrowed.
func VisibleListeners(listeners []Listener, all bool) []Listener {
	if all {
		return listeners
	}
	var visible []Listener
	for _, l := range listeners {
		if !l.Infrastructure() {
			visible = append(visible, l)
		}
	}
	return visible
}

// forwardArgs builds the argv Forward passes to the ssh binary: a
// silent (-N) local forward of port to the same port on the remote's
// own localhost. ExitOnForwardFailure turns a failed local bind into a
// failed command instead of a silently-running one -- without it, ssh
// keeps running when the local port is already taken, the caller's
// "forwarding over SSH" line stays on screen looking successful, and
// the URL it just printed quietly resolves to whatever already owned
// that port (plausibly the user's own local service of the same kind).
//
// The 127.0.0.1 bind address is what makes that guarantee hold. Written
// as a bare "port:localhost:port", ssh binds every address family it
// can and treats the forward as set up if ANY of them succeeded -- so a
// local service already on 127.0.0.1 leaves the IPv6 bind working, ssh
// stays up, and ExitOnForwardFailure never fires. Measured: bare spec
// survived a collision for 20s, the explicit one failed in 1s. Binding
// one family also matches what the printed http://localhost URL
// resolves to, instead of leaving it to depend on resolution order.
func forwardArgs(host, user string, localPort, remotePort int) []string {
	spec := fmt.Sprintf("127.0.0.1:%d:localhost:%d", localPort, remotePort)
	return []string{"-N", "-o", "ExitOnForwardFailure=yes", "-L", spec, user + "@" + host}
}

// Forward runs `ssh -L` in the foreground until the user interrupts it.
//
// Foreground deliberately: a backgrounded tunnel is a process someone
// has to find and kill later, which is what state.Record.TunnelPID
// existed for and why the original design stalled half-built. Ctrl-C
// ends this one, and nothing is written to state.
//
// host, not the instance's public IP: the caller passes the tailnet
// address when there is one. A forward is only ever reached after the
// tailnet has already been resolved, so sending the traffic over the
// public internet at that point would be routing around a link the
// caller has just proven is up.
func Forward(ctx context.Context, host, user string, localPort, remotePort int) error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh not found on PATH: %w", err)
	}
	// #nosec G204 -- argv-array exec.Command, no shell. host is
	// provider-assigned or tailnet-resolved and the ports are ints, so
	// none of them can inject.
	cmd := exec.CommandContext(ctx, "ssh", forwardArgs(host, user, localPort, remotePort)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
