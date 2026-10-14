package sshidentity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/bivouac/internal/sshkeys"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func fixture(t *testing.T, private bool) (sshkeys.Key, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "custom key.pub")
	write(t, path, ssh.MarshalAuthorizedKey(signer.PublicKey()))
	if private {
		block, err := ssh.MarshalPrivateKey(priv, "")
		if err != nil {
			t.Fatal(err)
		}
		write(t, strings.TrimSuffix(path, ".pub"), pem.EncodeToMemory(block))
	}
	key, err := sshkeys.ReadPublicFile(sshkeys.FileRef{Entry: path, Path: path, ConfigPath: "fixture.pkl"})
	if err != nil {
		t.Fatal(err)
	}
	return key, priv
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func agentFixture(t *testing.T, ag agent.Agent) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bivouac-agent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "a.sock")
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
			go func() { defer func() { _ = conn.Close() }(); _ = agent.ServeAgent(ag, conn) }()
		}
	}()
	return sock
}

func TestSelectCustomPrivateIdentityAndRevalidate(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	key, _ := fixture(t, true)
	ids, err := Select(context.Background(), []sshkeys.Key{key})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0].PublicPath != key.Path || ids[0].PrivatePath != strings.TrimSuffix(key.Path, ".pub") || ids[0].Agent {
		t.Fatalf("identities = %+v", ids)
	}
	signers, closeAgent, err := Signers(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	closeAgent()
	if len(signers) != 1 || ssh.FingerprintLegacyMD5(signers[0].PublicKey()) != key.Fingerprint {
		t.Fatal("wrong signer")
	}
	other, _ := fixture(t, true)
	raw, err := os.ReadFile(other.Path)
	if err != nil {
		t.Fatal(err)
	}
	write(t, key.Path, raw)
	if _, cleanup, err := Signers(context.Background(), ids); err == nil {
		cleanup()
		t.Fatal("changed public file accepted")
	} else if !strings.Contains(err.Error(), key.Path) {
		t.Fatal(err)
	}
}

func TestSelectAgentFiltersAndPersistsNoSocket(t *testing.T) {
	key, priv := fixture(t, false)
	_, unrelated := fixture(t, false)
	ring := agent.NewKeyring()
	for _, p := range []ed25519.PrivateKey{unrelated, priv} {
		if err := ring.Add(agent.AddedKey{PrivateKey: p}); err != nil {
			t.Fatal(err)
		}
	}
	sock := agentFixture(t, ring)
	t.Setenv("SSH_AUTH_SOCK", sock)
	var progress []string
	ctx := WithProgress(context.Background(), func(s string) { progress = append(progress, s) })
	ids, err := Select(ctx, []sshkeys.Key{key})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || !ids[0].Agent || ids[0].PrivatePath != "" || len(progress) == 0 {
		t.Fatalf("ids=%+v progress=%v", ids, progress)
	}
	signers, cleanup, err := Signers(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(signers) != 1 || ssh.FingerprintLegacyMD5(signers[0].PublicKey()) != key.Fingerprint {
		t.Fatal("unselected signer offered")
	}
	c := Connection{Name: "machine", Alias: "bivouac-machine", PublicIP: "192.0.2.1", PreferredHost: "192.0.2.1", User: "me", Keys: ids}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{sock, "PRIVATE KEY", "passphrase"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("serialized secret: %s", raw)
		}
	}
	got, ok := FromContext(WithConnection(ctx, c))
	if !ok || got.Alias != c.Alias {
		t.Fatal("context lost")
	}
	if err := ring.RemoveAll(); err != nil {
		t.Fatal(err)
	}
	if _, cleanup, err := Signers(ctx, ids); err == nil {
		cleanup()
		t.Fatal("missing agent key fell back")
	}
}

func TestSelectRequiresOneUsableKey(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	unavailable, _ := fixture(t, false)
	usable, _ := fixture(t, true)
	ids, err := Select(context.Background(), []sshkeys.Key{unavailable, usable})
	if err != nil || len(ids) != 1 || ids[0].PublicPath != usable.Path {
		t.Fatalf("ids=%+v err=%v", ids, err)
	}
	_, err = Select(context.Background(), []sshkeys.Key{unavailable})
	if err == nil || !strings.Contains(err.Error(), unavailable.Path) {
		t.Fatalf("error=%v", err)
	}
}

func TestSelectEncryptedAndMismatchedPrivateKeys(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	key, priv := fixture(t, false)
	path := strings.TrimSuffix(key.Path, ".pub")
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("test passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, pem.EncodeToMemory(block))
	_, err = Select(context.Background(), []sshkeys.Key{key})
	if err == nil || !strings.Contains(err.Error(), "ssh-add") || !strings.Contains(err.Error(), path) {
		t.Fatalf("error=%v", err)
	}
	other, _ := fixture(t, true)
	raw, err := os.ReadFile(strings.TrimSuffix(other.Path, ".pub"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, raw)
	if _, err := Select(context.Background(), []sshkeys.Key{key}); err == nil {
		t.Fatal("mismatched private key accepted")
	}
}

type rejectingAgent struct{ agent.Agent }

func (a rejectingAgent) Sign(ssh.PublicKey, []byte) (*ssh.Signature, error) {
	return nil, errors.New("hardware refused signing")
}

func TestSelectRejectsUnusableAgentSigner(t *testing.T) {
	key, priv := fixture(t, false)
	ring := agent.NewKeyring()
	if err := ring.Add(agent.AddedKey{PrivateKey: priv}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTH_SOCK", agentFixture(t, rejectingAgent{ring}))
	if _, err := Select(context.Background(), []sshkeys.Key{key}); err == nil {
		t.Fatal("unusable signer accepted")
	}
}

func TestSelectCancellationClosesBlockedAgent(t *testing.T) {
	key, _ := fixture(t, false)
	dir, err := os.MkdirTemp("", "bivouac-blocked-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	sock := filepath.Join(dir, "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Select(ctx, []sshkeys.Key{key})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("cancel error=%v duration=%v", err, time.Since(start))
	}
	select {
	case conn := <-accepted:
		defer func() { _ = conn.Close() }()
	case <-time.After(time.Second):
		t.Fatal("agent not contacted")
	}
}
