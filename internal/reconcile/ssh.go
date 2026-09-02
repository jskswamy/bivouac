// Package reconcile brings a live instance's home-manager environment
// up to date with its local cloudlab.pkl: resolve the config, render a
// per-instance wrapper flake if needed, ship it over SSH, and run
// home-manager switch. It is the one piece up, shell, and provision all
// share.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Client wraps an established SSH connection to an instance.
type Client struct {
	conn *ssh.Client
}

// Connect dials ip on port 22 (or, if ip already has an explicit port —
// used by tests pointed at a fake server on an arbitrary port — that
// port instead), authenticates via the running ssh-agent
// (SSH_AUTH_SOCK), and verifies the host key on a trust-on-first-connect
// basis against the user's real ~/.ssh/known_hosts: an unknown host is
// accepted and recorded; a host presenting a different key than what's
// already recorded is rejected.
func Connect(ctx context.Context, ip string) (*Client, error) {
	addr := ip
	if _, _, err := net.SplitHostPort(ip); err != nil {
		addr = net.JoinHostPort(ip, "22")
	}

	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, fmt.Errorf("SSH_AUTH_SOCK not set — is ssh-agent running?")
	}
	agentConn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("connecting to ssh-agent: %w", err)
	}
	agentClient := agent.NewClient(agentConn)

	knownHostsPath, err := defaultKnownHostsPath()
	if err != nil {
		return nil, err
	}
	hostKeyCallback, err := trustOnFirstConnectCallback(knownHostsPath)
	if err != nil {
		return nil, err
	}

	clientConfig := &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.PublicKeysCallback(agentClient.Signers)},
		HostKeyCallback: hostKeyCallback,
		Timeout:         10 * time.Second,
	}

	dialer := net.Dialer{Timeout: clientConfig.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", addr, err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientConfig)
	if err != nil {
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) && len(keyErr.Want) > 0 {
			return nil, fmt.Errorf("host key for %s has changed — if you recreated this instance (down + up), remove its stale entry from ~/.ssh/known_hosts and try again: %w", addr, err)
		}
		return nil, fmt.Errorf("ssh handshake with %s: %w", addr, err)
	}
	return &Client{conn: ssh.NewClient(sshConn, chans, reqs)}, nil
}

// defaultKnownHostsPath returns ~/.ssh/known_hosts, creating an empty
// file there first if none exists yet (knownhosts.New requires the file
// to already exist).
func defaultKnownHostsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "known_hosts")
	if err := touchFile(path); err != nil {
		return "", err
	}
	return path, nil
}

// touchFile creates path if it doesn't already exist, leaving any
// existing content untouched.
func touchFile(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// trustOnFirstConnectCallback wraps knownhosts.New(path)'s callback: an
// unknown host (a *knownhosts.KeyError with an empty Want) is accepted
// and appended to path; a host presenting a different key than what's
// already recorded (Want non-empty) is rejected, surfaced verbatim.
func trustOnFirstConnectCallback(path string) (ssh.HostKeyCallback, error) {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		verify, err := knownhosts.New(path)
		if err != nil {
			return fmt.Errorf("reading known_hosts %s: %w", path, err)
		}
		verifyErr := verify(hostname, remote, key)
		if verifyErr == nil {
			return nil
		}
		var keyErr *knownhosts.KeyError
		if errors.As(verifyErr, &keyErr) && len(keyErr.Want) == 0 {
			return appendKnownHost(path, hostname, key)
		}
		return verifyErr
	}, nil
}

func appendKnownHost(path, hostname string, key ssh.PublicKey) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
	_, err = fmt.Fprintln(f, line)
	return err
}

// WriteFile writes content to remotePath on the instance, creating its
// parent directory first.
func (c *Client) WriteFile(remotePath, content string) error {
	session, err := c.conn.NewSession()
	if err != nil {
		return fmt.Errorf("opening session: %w", err)
	}
	defer func() { _ = session.Close() }()

	session.Stdin = strings.NewReader(content)
	dir := filepath.Dir(remotePath)
	cmd := fmt.Sprintf("mkdir -p %s && cat > %s", shellQuote(dir), shellQuote(remotePath))
	if out, err := session.CombinedOutput(cmd); err != nil {
		return fmt.Errorf("writing %s: %w\n%s", remotePath, err, out)
	}
	return nil
}

// Run executes cmd on the instance and returns its combined stdout and
// stderr. A non-zero exit becomes a non-nil error; output is still
// populated so the caller can include it in its own error message.
func (c *Client) Run(cmd string) (output string, err error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return "", fmt.Errorf("opening session: %w", err)
	}
	defer func() { _ = session.Close() }()

	out, runErr := session.CombinedOutput(cmd)
	return string(out), runErr
}

// Close closes the underlying SSH connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// shellQuote wraps s in single quotes for safe inclusion in a remote
// shell command, escaping any embedded single quotes. Used for every
// value that isn't a fixed constant string written in this package.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
