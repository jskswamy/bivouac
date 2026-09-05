// Package reconcile brings a live instance's home-manager environment
// up to date with its local cloudlab.pkl: resolve the config, render a
// per-instance wrapper flake if needed, ship it over SSH, and run
// home-manager switch. It is the one piece up, shell, and provision all
// share.
package reconcile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
// port instead) as user, authenticates with the running ssh-agent's keys
// followed by the default on-disk identity files, and verifies the host
// key on a trust-on-first-connect basis against the user's real
// ~/.ssh/known_hosts: an unknown host is accepted and recorded; a host
// presenting a different key than what's already recorded is rejected.
//
// Both key sources are offered because either one alone is routinely
// incomplete. An agent may hold keys that exist nowhere on disk (a
// smartcard/YubiKey), while a key may sit in ~/.ssh without ever being
// added to the agent -- and if SSH_AUTH_SOCK points at gpg-agent, it
// typically serves *only* the smartcard key, so the plain ~/.ssh/id_*
// key registered with the cloud provider is invisible to it. Trying the
// agent alone made cloudlab fail to authenticate against instances that
// `ssh` itself could log into fine, which is both baffling to diagnose
// and, because the caller then retries, escalates into the instance
// blackholing this host (see lifecycle.WaitReady).
func Connect(ctx context.Context, ip, user string) (*Client, error) {
	addr := ip
	if _, _, err := net.SplitHostPort(ip); err != nil {
		addr = net.JoinHostPort(ip, "22")
	}

	auth, err := authMethods()
	if err != nil {
		return nil, err
	}

	knownHostsPath, err := defaultKnownHostsPath()
	if err != nil {
		return nil, err
	}
	hostKeyCallback, err := trustOnFirstConnectCallback(knownHostsPath)
	if err != nil {
		return nil, err
	}

	clientConfig := &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
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

// defaultIdentityFiles are the private keys OpenSSH itself tries by
// default (its IdentityFile defaults), in the same order. Deliberately
// only these: every key offered is a separate authentication attempt,
// and sshd's MaxAuthTries (6 by default) disconnects once they run out,
// so sweeping in every ~/.ssh/id_* a user happens to have would risk
// exhausting the budget before reaching the key that actually works.
var defaultIdentityFiles = []string{
	"id_ed25519",
	"id_ecdsa",
	"id_ecdsa_sk",
	"id_ed25519_sk",
	"id_rsa",
}

// authMethods returns the public-key auth to offer: every agent key
// first (an agent can hold keys with no on-disk private half, such as a
// smartcard's) followed by the default identity files.
//
// All of them go into a single ssh.PublicKeys method rather than one
// method per source, which matters more than it looks. Split across two
// methods, a first method that fails takes the whole authentication down
// with it -- observed with gpg-agent serving only a smartcard key: the
// handshake was abandoned reporting "no supported methods remain"
// without the perfectly good ~/.ssh/id_ed25519 in the second method ever
// being offered. Within one method the client walks the signer list,
// cheaply querying each key until the server accepts one, so a rejected
// or unusable leading key is skipped rather than fatal.
func authMethods() ([]ssh.AuthMethod, error) {
	var signers []ssh.Signer

	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		// #nosec G704 -- sock is SSH_AUTH_SOCK, a local unix-domain-socket
		// path the user's own ssh-agent set in their own environment; this
		// is local IPC, not a network request, so SSRF doesn't apply.
		if agentConn, err := net.Dial("unix", sock); err == nil {
			// A broken or empty agent is not fatal: the on-disk keys
			// below may still authenticate.
			if agentSigners, err := agent.NewClient(agentConn).Signers(); err == nil {
				signers = append(signers, agentSigners...)
			}
		}
	}

	signers = append(signers, defaultIdentitySigners()...)

	if len(signers) == 0 {
		return nil, fmt.Errorf("no SSH keys available — start an ssh-agent (SSH_AUTH_SOCK is unset or unreachable) or add a key at ~/.ssh/id_ed25519")
	}
	return []ssh.AuthMethod{ssh.PublicKeys(signers...)}, nil
}

// defaultIdentitySigners loads whichever of defaultIdentityFiles exist
// and are usable. Unreadable, malformed, and passphrase-protected keys
// are skipped rather than failing the connection: there is no terminal
// to prompt on here, and such a key is exactly what the agent is for --
// dropping it silently leaves the agent's copy to do the work.
func defaultIdentitySigners() []ssh.Signer {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var signers []ssh.Signer
	for _, name := range defaultIdentityFiles {
		// #nosec G304 -- path is ~/.ssh/<fixed name> from the
		// defaultIdentityFiles constant, never external input.
		pem, err := os.ReadFile(filepath.Join(home, ".ssh", name))
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(pem)
		if err != nil {
			continue
		}
		signers = append(signers, signer)
	}
	return signers
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
	// #nosec G304 -- path is always ~/.ssh/known_hosts, built from
	// os.UserHomeDir() in defaultKnownHostsPath, never external input.
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
	// #nosec G304 -- path is always ~/.ssh/known_hosts, built from
	// os.UserHomeDir() in defaultKnownHostsPath, never external input.
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
	cmd := fmt.Sprintf("mkdir -p %s && cat > %s", ShellQuote(dir), ShellQuote(remotePath))
	if out, err := session.CombinedOutput(cmd); err != nil {
		return fmt.Errorf("writing %s: %w\n%s", remotePath, err, out)
	}
	return nil
}

// WriteSecretFile writes content to remotePath on the instance with
// permissions restricted to the owning user from the moment the file
// exists (mode 600 via install, rather than cat followed by a
// separate chmod, which would briefly leave default-umask
// permissions). content is streamed over the SSH session's stdin --
// unlike WriteFile's content-as-string approach (fine for a rendered
// flake.nix, wrong for anything sensitive), it never appears as a
// literal command argument.
func (c *Client) WriteSecretFile(remotePath string, content []byte) error {
	session, err := c.conn.NewSession()
	if err != nil {
		return fmt.Errorf("opening session: %w", err)
	}
	defer func() { _ = session.Close() }()

	session.Stdin = bytes.NewReader(content)
	cmd := fmt.Sprintf("install -m 600 /dev/stdin %s", ShellQuote(remotePath))
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

// RunStreaming executes cmd on the instance, writing its stdout/stderr
// to out/errOut live as they arrive rather than only once the command
// exits, while still returning the combined output for the caller's
// own error message -- for long-running commands (home-manager switch)
// where silence until completion reads as a hang.
func (c *Client) RunStreaming(cmd string, out, errOut io.Writer) (output string, err error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return "", fmt.Errorf("opening session: %w", err)
	}
	defer func() { _ = session.Close() }()

	var buf bytes.Buffer
	session.Stdout = io.MultiWriter(out, &buf)
	session.Stderr = io.MultiWriter(errOut, &buf)
	runErr := session.Run(cmd)
	return buf.String(), runErr
}

// Close closes the underlying SSH connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// ShellQuote wraps s in single quotes for safe inclusion in a remote
// shell command, escaping any embedded single quotes. Used for every
// value that isn't a fixed constant string written in this package.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
