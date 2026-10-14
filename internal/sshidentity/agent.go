package sshidentity

import (
	"context"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// connectAgent bounds local IPC, including hardware waits. Cancellation closes
// the socket so a blocked agent call cannot outlive the command.
func connectAgent(ctx context.Context) ([]ssh.Signer, func(), error) {
	noop := func() {}
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, noop, nil
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	// #nosec G704 -- SSH_AUTH_SOCK is the user's local agent IPC socket.
	conn, err := dialer.DialContext(ctx, "unix", sock)
	if err != nil {
		return nil, noop, err
	}
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, noop, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	var once sync.Once
	cleanup := func() { once.Do(func() { stop(); _ = conn.Close() }) }
	signers, err := agent.NewClient(conn).Signers()
	if err != nil {
		cleanup()
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if d, ok := ctx.Deadline(); ok && !time.Now().Before(d) {
			err = context.DeadlineExceeded
		}
		return nil, noop, err
	}
	return signers, cleanup, nil
}
