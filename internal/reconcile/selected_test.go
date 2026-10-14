package reconcile

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/jskswamy/bivouac/internal/sshidentity"
	"github.com/jskswamy/bivouac/internal/sshkeys"
	"github.com/jskswamy/bivouac/internal/testenv"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func TestConnectUsesOnlySelectedNondefaultKey(t *testing.T) {
	testenv.Isolate(t)
	selected := testenv.SSHKey(t)
	ring := agent.NewKeyring()
	for i := 0; i < 8; i++ {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if err := ring.Add(agent.AddedKey{PrivateKey: priv}); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := os.MkdirTemp("", "bivouac-filter-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	sock := filepath.Join(dir, "a.sock")
	agentListener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agentListener.Close() }()
	go func() {
		for {
			c, err := agentListener.Accept()
			if err != nil {
				return
			}
			go func() { defer func() { _ = c.Close() }(); _ = agent.ServeAgent(ring, c) }()
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)
	public, _, _, _, err := ssh.ParseAuthorizedKey([]byte(selected.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	_, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.NewSignerFromKey(hostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	offered := make(chan string, 16)
	server := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		offered <- ssh.FingerprintSHA256(key)
		if !bytes.Equal(key.Marshal(), public.Marshal()) {
			return nil, fmt.Errorf("unselected key")
		}
		return nil, nil
	}}
	server.AddHostKey(host)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		conn, chans, reqs, err := ssh.NewServerConn(c, server)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		go ssh.DiscardRequests(reqs)
		for ch := range chans {
			_ = ch.Reject(ssh.Prohibited, "fixture")
		}
	}()
	ids, err := sshidentity.Select(context.Background(), []sshkeys.Key{selected})
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	ctx := sshidentity.WithConnection(context.Background(), sshidentity.Connection{Name: "machine", Alias: "bivouac-machine", PublicIP: addr, PreferredHost: addr, User: "me", Keys: ids})
	client, err := Connect(ctx, addr, "me")
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	<-done
	close(offered)
	for fp := range offered {
		if fp != ssh.FingerprintSHA256(public) {
			t.Fatalf("unrelated key offered: %s", fp)
		}
	}
	if _, err := Connect(ctx, "192.0.2.1", "me"); err == nil {
		t.Fatal("identity used outside recorded endpoint")
	}
}
