// Package sshkeys finds the SSH public keys this machine holds and
// fingerprints them the way DigitalOcean does.
//
// It talks to no provider and makes no network call. That is the point:
// a correct sshKeys value can be produced with no credentials at all,
// because DigitalOcean matches on the RFC 4716 MD5 fingerprint and
// x/crypto computes that locally. An account lookup only adds whether a
// key is registered -- it is not needed to name one.
package sshkeys

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Source is where a key was found.
type Source int

const (
	// Agent is the running ssh-agent. The stronger of the two: a key
	// the agent holds is one that can authenticate right now, and it is
	// the only place a smartcard-resident key appears at all.
	Agent Source = iota
	// Disk is a ~/.ssh/*.pub file, whose private half may be long gone.
	Disk
)

func (s Source) String() string {
	if s == Agent {
		return "in agent"
	}
	return "on disk"
}

// Key is one public key this machine holds.
type Key struct {
	// Fingerprint is the RFC 4716 MD5 presentation -- colon-separated
	// hex, no prefix -- which is the form DigitalOcean stores and
	// matches on.
	Fingerprint string
	// Comment labels the key for a human: its own comment if it has
	// one, else the file it came from.
	Comment string
	// Source is where it was found.
	Source Source
	// PublicKey is the authorized_keys line, which is what registering
	// the key with a provider sends.
	PublicKey string
}

// Discover returns the public keys this machine holds, agent first,
// deduplicated by fingerprint.
func Discover() ([]Key, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return discover(os.Getenv("SSH_AUTH_SOCK"), filepath.Join(home, ".ssh"))
}

// discover is Discover with its two environment inputs passed in.
//
// Neither source is allowed to fail the whole call. A machine with no
// agent, no ~/.ssh, or both, is a fresh machine -- which is exactly the
// audience the wizard exists for, so the answer there is an empty list
// rather than an error.
func discover(agentSock, sshDir string) ([]Key, error) {
	var keys []Key
	seen := map[string]bool{}

	add := func(k Key) {
		if seen[k.Fingerprint] {
			return
		}
		seen[k.Fingerprint] = true
		keys = append(keys, k)
	}

	// Agent first, so that a key held both places is reported as being
	// in the agent -- the stronger statement about what the user can do.
	for _, k := range agentKeys(agentSock) {
		add(k)
	}
	for _, k := range diskKeys(sshDir) {
		add(k)
	}
	return keys, nil
}

// agentKeys lists what the ssh-agent at sock holds. A missing,
// unreachable or broken agent yields nothing, never an error: the disk
// keys below may still be everything the user needs.
func agentKeys(sock string) []Key {
	if sock == "" {
		return nil
	}
	// #nosec G704 -- sock is SSH_AUTH_SOCK, a local unix-domain-socket
	// path the user's own agent set in their own environment; this is
	// local IPC, not a network request. Same call internal/reconcile's
	// authMethods makes.
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil
	}
	defer func() { _ = conn.Close() }()

	listed, err := agent.NewClient(conn).List()
	if err != nil {
		return nil
	}

	keys := make([]Key, 0, len(listed))
	for _, k := range listed {
		comment := strings.TrimSpace(k.Comment)
		if comment == "" {
			comment = k.Format
		}
		keys = append(keys, Key{
			Fingerprint: ssh.FingerprintLegacyMD5(k),
			Comment:     comment,
			Source:      Agent,
			PublicKey:   strings.TrimSpace(k.String()),
		})
	}
	return keys
}

// diskKeys reads every ~/.ssh/*.pub that parses as a public key.
//
// Every *.pub rather than OpenSSH's five default identity files, which
// is what internal/reconcile deliberately restricts itself to. That list
// is narrow because each key offered to an sshd spends one of six
// MaxAuthTries; a list of choices in a form spends no such budget, and a
// key missing from it sends the user back to typing a fingerprint by
// hand.
func diskKeys(sshDir string) []Key {
	matches, err := filepath.Glob(filepath.Join(sshDir, "*.pub"))
	if err != nil {
		return nil
	}
	sort.Strings(matches)

	var keys []Key
	for _, path := range matches {
		// #nosec G304 -- path comes from globbing the user's own ~/.ssh
		// for *.pub, and a public key is public by construction.
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		pub, comment, _, _, err := ssh.ParseAuthorizedKey(raw)
		if err != nil {
			// Not a public key. ~/.ssh holds config, known_hosts and
			// private keys too; none of them is a broken key worth
			// reporting.
			continue
		}
		if comment = strings.TrimSpace(comment); comment == "" {
			comment = strings.TrimSuffix(filepath.Base(path), ".pub")
		}
		keys = append(keys, Key{
			Fingerprint: ssh.FingerprintLegacyMD5(pub),
			Comment:     comment,
			Source:      Disk,
			PublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))),
		})
	}
	return keys
}

// Find returns the discovered key with the given fingerprint.
func Find(keys []Key, fingerprint string) (Key, error) {
	for _, k := range keys {
		if k.Fingerprint == fingerprint {
			return k, nil
		}
	}
	return Key{}, fmt.Errorf("no key with fingerprint %q on this machine", fingerprint)
}
