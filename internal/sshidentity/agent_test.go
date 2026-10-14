package sshidentity

import (
	"context"
	"encoding/pem"
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

func TestAgentCleanupClosesIPC(t *testing.T) {
	key, priv := fixture(t, false)
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	id := Identity{PublicPath: key.Path, Fingerprint: ssh.FingerprintSHA256(pub), Agent: true}
	ring := agent.NewKeyring()
	if err := ring.Add(agent.AddedKey{PrivateKey: priv}); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "bivouac-close-")
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
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = agent.ServeAgent(ring, conn)
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)
	_, cleanup, err := Signers(context.Background(), []Identity{id})
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	cleanup()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("agent IPC leaked")
	}
}

func TestRecordedPrivateIdentityNeverSubstitutes(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	key, _ := fixture(t, true)
	ids, err := Select(context.Background(), []sshkeys.Key{key})
	if err != nil {
		t.Fatal(err)
	}
	_, priv := fixture(t, true)
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	write(t, ids[0].PrivatePath, pem.EncodeToMemory(block))
	if _, cleanup, err := Signers(context.Background(), ids); err == nil {
		cleanup()
		t.Fatal("changed private key accepted")
	}
	if err := os.Remove(ids[0].PrivatePath); err != nil {
		t.Fatal(err)
	}
	if _, cleanup, err := Signers(context.Background(), ids); err == nil {
		cleanup()
		t.Fatal("missing private key accepted")
	} else if !strings.Contains(err.Error(), key.Path) {
		t.Fatal(err)
	}
	if err := os.Remove(key.Path); err != nil {
		t.Fatal(err)
	}
	if _, cleanup, err := Signers(context.Background(), ids); err == nil {
		cleanup()
		t.Fatal("missing public key accepted")
	}
}
