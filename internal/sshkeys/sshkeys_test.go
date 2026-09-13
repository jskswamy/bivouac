package sshkeys

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// newKey returns a fresh ed25519 keypair and its ssh public half.
func newKey(t *testing.T) (ed25519.PrivateKey, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return priv, signer.PublicKey()
}

// writePub writes an authorized_keys-format .pub file into dir.
func writePub(t *testing.T, dir, name string, pub ssh.PublicKey, comment string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if comment != "" {
		line += " " + comment
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// serveAgent starts an ssh-agent holding keys and returns its socket path.
func serveAgent(t *testing.T, keys ...agent.AddedKey) string {
	t.Helper()
	keyring := agent.NewKeyring()
	for _, k := range keys {
		if err := keyring.Add(k); err != nil {
			t.Fatal(err)
		}
	}
	// A short path: a unix socket's sun_path is ~104 bytes, and t.TempDir()
	// under a long test name can exceed it.
	sock := filepath.Join(t.TempDir(), "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = agent.ServeAgent(keyring, conn) }()
		}
	}()
	return sock
}

func fingerprintOf(pub ssh.PublicKey) string { return ssh.FingerprintLegacyMD5(pub) }

func TestDiscover_FindsDiskKeys(t *testing.T) {
	home := t.TempDir()
	_, pub := newKey(t)
	writePub(t, filepath.Join(home, ".ssh"), "id_ed25519.pub", pub, "me@laptop")

	keys, err := discover("", filepath.Join(home, ".ssh"))
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("found %d keys, want 1: %+v", len(keys), keys)
	}
	if keys[0].Fingerprint != fingerprintOf(pub) {
		t.Errorf("Fingerprint = %q, want %q", keys[0].Fingerprint, fingerprintOf(pub))
	}
	if keys[0].Comment != "me@laptop" {
		t.Errorf("Comment = %q, want me@laptop", keys[0].Comment)
	}
	if keys[0].Source != Disk {
		t.Errorf("Source = %v, want Disk", keys[0].Source)
	}
	if !strings.HasPrefix(keys[0].PublicKey, "ssh-ed25519 ") {
		t.Errorf("PublicKey = %q, want an authorized_keys line", keys[0].PublicKey)
	}
}

// The fingerprint has to be the one DigitalOcean matches on: RFC 4716
// MD5, colon-separated hex, no prefix.
func TestDiscover_FingerprintIsDigitalOceanFormat(t *testing.T) {
	home := t.TempDir()
	_, pub := newKey(t)
	writePub(t, filepath.Join(home, ".ssh"), "id_ed25519.pub", pub, "")

	keys, err := discover("", filepath.Join(home, ".ssh"))
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	fp := keys[0].Fingerprint
	if strings.HasPrefix(fp, "SHA256:") || strings.HasPrefix(fp, "md5:") {
		t.Errorf("Fingerprint = %q, want bare colon-hex with no prefix", fp)
	}
	if parts := strings.Split(fp, ":"); len(parts) != 16 {
		t.Errorf("Fingerprint = %q, want 16 colon-separated octets", fp)
	}
}

func TestDiscover_FindsAgentKeys(t *testing.T) {
	priv, pub := newKey(t)
	sock := serveAgent(t, agent.AddedKey{PrivateKey: priv, Comment: "yubikey"})

	keys, err := discover(sock, filepath.Join(t.TempDir(), "no-such-ssh-dir"))
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("found %d keys, want 1: %+v", len(keys), keys)
	}
	if keys[0].Fingerprint != fingerprintOf(pub) {
		t.Errorf("Fingerprint = %q, want the agent key's", keys[0].Fingerprint)
	}
	if keys[0].Source != Agent {
		t.Errorf("Source = %v, want Agent", keys[0].Source)
	}
	if keys[0].Comment != "yubikey" {
		t.Errorf("Comment = %q, want yubikey", keys[0].Comment)
	}
}

// A key in both places is one key. It is reported as being in the agent,
// because that is the stronger statement: the agent can sign with it now.
func TestDiscover_DedupesAgentOverDisk(t *testing.T) {
	priv, pub := newKey(t)
	home := t.TempDir()
	writePub(t, filepath.Join(home, ".ssh"), "id_ed25519.pub", pub, "me@laptop")
	sock := serveAgent(t, agent.AddedKey{PrivateKey: priv, Comment: "me@laptop"})

	keys, err := discover(sock, filepath.Join(home, ".ssh"))
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("found %d keys, want 1 after dedupe: %+v", len(keys), keys)
	}
	if keys[0].Source != Agent {
		t.Errorf("Source = %v, want Agent to win the dedupe", keys[0].Source)
	}
}

// Agent keys come first: they are the ones that can authenticate now.
func TestDiscover_AgentKeysComeFirst(t *testing.T) {
	home := t.TempDir()
	_, diskPub := newKey(t)
	writePub(t, filepath.Join(home, ".ssh"), "id_rsa.pub", diskPub, "old")

	agentPriv, agentPub := newKey(t)
	sock := serveAgent(t, agent.AddedKey{PrivateKey: agentPriv, Comment: "current"})

	keys, err := discover(sock, filepath.Join(home, ".ssh"))
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("found %d keys, want 2: %+v", len(keys), keys)
	}
	if keys[0].Fingerprint != fingerprintOf(agentPub) {
		t.Errorf("first key is not the agent's")
	}
	if keys[1].Fingerprint != fingerprintOf(diskPub) {
		t.Errorf("second key is not the disk one")
	}
}

// The .ssh directory collects plenty that is not a public key. None of it
// should be reported as a broken key.
func TestDiscover_IgnoresEverythingThatIsNotAPubFile(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	_, pub := newKey(t)
	writePub(t, sshDir, "id_ed25519.pub", pub, "me@laptop")

	for name, body := range map[string]string{
		"id_ed25519":  "-----BEGIN OPENSSH PRIVATE KEY-----\nnope\n",
		"known_hosts": "example.com ssh-ed25519 AAAA\n",
		"config":      "Host *\n  User me\n",
		"garbage.pub": "this is not a public key at all\n",
	} {
		if err := os.WriteFile(filepath.Join(sshDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	keys, err := discover("", sshDir)
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("found %d keys, want just the real one: %+v", len(keys), keys)
	}
}

// No agent, no .ssh, no keys -- an empty list, not an error. This is a
// fresh machine, which is the wizard's whole audience.
func TestDiscover_NothingFoundIsNotAnError(t *testing.T) {
	keys, err := discover("", filepath.Join(t.TempDir(), "no-such-ssh-dir"))
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("found %+v, want none", keys)
	}
}

// A dead SSH_AUTH_SOCK must not take the disk keys down with it.
func TestDiscover_UnreachableAgentStillReturnsDiskKeys(t *testing.T) {
	home := t.TempDir()
	_, pub := newKey(t)
	writePub(t, filepath.Join(home, ".ssh"), "id_ed25519.pub", pub, "me@laptop")

	keys, err := discover(filepath.Join(t.TempDir(), "not-a-socket"), filepath.Join(home, ".ssh"))
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	if len(keys) != 1 || keys[0].Source != Disk {
		t.Errorf("keys = %+v, want the disk key", keys)
	}
}

// A .pub with no comment still needs a usable label.
func TestDiscover_CommentFallsBackToTheFilename(t *testing.T) {
	home := t.TempDir()
	_, pub := newKey(t)
	writePub(t, filepath.Join(home, ".ssh"), "id_ed25519.pub", pub, "")

	keys, err := discover("", filepath.Join(home, ".ssh"))
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	if keys[0].Comment != "id_ed25519" {
		t.Errorf("Comment = %q, want the filename without .pub", keys[0].Comment)
	}
}
