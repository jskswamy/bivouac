package lifecycle

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/jskswamy/cloudlab/internal/provider"
)

// startFakeAgent runs an in-process fake ssh-agent (a real
// golang.org/x/crypto/ssh/agent.Keyring served over a unix socket)
// seeded with one freshly generated ed25519 key, and points
// SSH_AUTH_SOCK at it for the duration of the test. Duplicated from
// internal/reconcile's own test helper of the same name — this is only
// the second consumer, so a shared test-support package isn't worth
// extracting yet.
func startFakeAgent(t *testing.T) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: priv}); err != nil {
		t.Fatal(err)
	}

	sockPath := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { _ = agent.ServeAgent(keyring, c) }(conn)
		}
	}()

	t.Setenv("SSH_AUTH_SOCK", sockPath)
}

// startFakeSSHServer runs a real, local, in-process SSH server on
// 127.0.0.1:0. For every "exec" request on a session channel, it calls
// handler with the received command string and full stdin, writes
// handler's returned output back to the client, and sends the returned
// exit code as the channel's exit-status. Returns the server's address
// ("127.0.0.1:<port>"). Duplicated from internal/reconcile's own test
// helper of the same name.
func startFakeSSHServer(t *testing.T, handler func(cmd string, stdin []byte) (output string, exitCode uint32)) string {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}

	config := &ssh.ServerConfig{
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	config.AddHostKey(hostSigner)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				sshConn, chans, reqs, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer func() { _ = sshConn.Close() }()
				go ssh.DiscardRequests(reqs)
				for newChannel := range chans {
					if newChannel.ChannelType() != "session" {
						_ = newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
						continue
					}
					channel, requests, err := newChannel.Accept()
					if err != nil {
						continue
					}
					go handleFakeSession(channel, requests, handler)
				}
			}()
		}
	}()

	return listener.Addr().String()
}

func handleFakeSession(channel ssh.Channel, requests <-chan *ssh.Request, handler func(cmd string, stdin []byte) (string, uint32)) {
	for req := range requests {
		if req.Type != "exec" {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		var payload struct{ Command string }
		_ = ssh.Unmarshal(req.Payload, &payload)
		_ = req.Reply(true, nil)

		stdin := readAllChannel(channel)
		output, exitCode := handler(payload.Command, stdin)
		_, _ = channel.Write([]byte(output))
		exitMsg := struct{ ExitStatus uint32 }{exitCode}
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(exitMsg))
		_ = channel.Close()
		return
	}
}

func readAllChannel(channel ssh.Channel) []byte {
	buf := make([]byte, 0, 256)
	tmp := make([]byte, 256)
	for {
		n, err := channel.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf
		}
	}
}

func TestWaitReady_SucceedsOnceCloudInitFinishes(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		return "status: done\n", 0
	})

	if err := WaitReady(context.Background(), addr, 5*time.Second); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
}

func TestWaitReady_ReportsProgressBeforeWaiting(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		return "status: done\n", 0
	})

	var got []string
	ctx := provider.WithProgress(context.Background(), func(status string) { got = append(got, status) })

	if err := WaitReady(ctx, addr, 5*time.Second); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if len(got) == 0 || !strings.Contains(got[0], "ready") {
		t.Errorf("progress = %v, want a first entry mentioning ready", got)
	}
}

func TestWaitReady_CloudInitFailureIsNotRetried(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var calls int
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		calls++
		return "status: error\n", 1
	})

	err := WaitReady(context.Background(), addr, 5*time.Second)
	if err == nil {
		t.Fatal("WaitReady() error = nil, want error for cloud-init failure")
	}
	if calls != 1 {
		t.Errorf("cloud-init command ran %d times, want exactly 1 (a genuine failure must not be retried)", calls)
	}
}

func TestWaitReady_TimesOutIfNeverReachable(t *testing.T) {
	err := WaitReady(context.Background(), "127.0.0.1:1", time.Second)
	if err == nil {
		t.Fatal("WaitReady() error = nil, want timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want mention of timeout", err.Error())
	}
}

func TestWaitReady_CloudInitHangIsBoundedByTimeout(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		time.Sleep(3 * time.Second)
		return "status: done\n", 0
	})

	start := time.Now()
	err := WaitReady(context.Background(), addr, 500*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("WaitReady() error = nil, want error when cloud-init hangs past the timeout")
	}
	if elapsed > 2*time.Second {
		t.Errorf("WaitReady() took %s, want it bounded by the ~500ms timeout, not waiting for the full 3s hang", elapsed)
	}
}
