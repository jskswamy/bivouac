package reconcile

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// startFakeAgent runs an in-process fake ssh-agent (a real
// golang.org/x/crypto/ssh/agent.Keyring served over a unix socket)
// seeded with one freshly generated ed25519 key, and points
// SSH_AUTH_SOCK at it for the duration of the test. Connect authenticates
// against this exactly as it would a real ssh-agent.
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

	dir, err := os.MkdirTemp("", "cloudlab-agent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "agent.sock")
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

// lastConnectedUsername records the SSH username of the most recent
// connection any fake server started via startFakeSSHServer accepted,
// for tests asserting Connect passed the right one. Tests in this
// package run sequentially (no t.Parallel()), so a shared var is safe.
var lastConnectedUsername string

// sessionResult is what a fake SSH server's exec handler received.
type sessionResult struct {
	Command string
	Stdin   []byte
}

// startFakeSSHServer runs a real, local, in-process SSH server on
// 127.0.0.1:0. For every "exec" request on a session channel, it calls
// handler with the received command string and full stdin, writes
// handler's returned output back to the client, and sends the returned
// exit code as the channel's exit-status. Returns the server's address
// ("127.0.0.1:<port>").
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
			lastConnectedUsername = conn.User()
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

func TestConnect_UsesGivenUsername(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) { return "", 0 })

	client, err := Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	if lastConnectedUsername != "devuser" {
		t.Errorf("SSH username = %q, want %q", lastConnectedUsername, "devuser")
	}
}

func TestClient_WriteFile_SendsContentViaCatRedirect(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var got sessionResult
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		got = sessionResult{Command: cmd, Stdin: stdin}
		return "", 0
	})

	client, err := Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	if err := client.WriteFile("/root/.cache/cloudlab/flake.nix", "hello flake"); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if !strings.Contains(got.Command, "cat >") {
		t.Errorf("command = %q, want it to contain a cat redirect", got.Command)
	}
	if !strings.Contains(got.Command, "/root/.cache/cloudlab/flake.nix") {
		t.Errorf("command = %q, want it to reference the target path", got.Command)
	}
	if string(got.Stdin) != "hello flake" {
		t.Errorf("stdin = %q, want %q", got.Stdin, "hello flake")
	}
}

func TestClient_WriteSecretFile_SendsContentViaStdinWithRestrictedMode(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var got sessionResult
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		got = sessionResult{Command: cmd, Stdin: stdin}
		return "", 0
	})

	client, err := Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	secret := []byte("tskey-abc123-example")
	if err := client.WriteSecretFile("/run/user/1000/cloudlab-ts-authkey", secret); err != nil {
		t.Fatalf("WriteSecretFile() error = %v", err)
	}

	if !strings.Contains(got.Command, "install -m 600") {
		t.Errorf("command = %q, want it to use install -m 600", got.Command)
	}
	if !strings.Contains(got.Command, "/run/user/1000/cloudlab-ts-authkey") {
		t.Errorf("command = %q, want it to reference the target path", got.Command)
	}
	if string(got.Stdin) != "tskey-abc123-example" {
		t.Errorf("stdin sent = %q, want %q (the secret content)", got.Stdin, "tskey-abc123-example")
	}
	if strings.Contains(got.Command, "tskey-abc123-example") {
		t.Error("command string contains the secret literal -- it must only travel via stdin")
	}
}

func TestClient_Run_ReturnsOutputOnSuccess(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		return "switch complete\n", 0
	})

	client, err := Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	output, err := client.Run("nix run home-manager -- switch --flake x")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if output != "switch complete\n" {
		t.Errorf("output = %q, want %q", output, "switch complete\n")
	}
}

func TestClient_Run_ReturnsErrorOnNonZeroExit(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		return "boom\n", 1
	})

	client, err := Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	output, err := client.Run("false")
	if err == nil {
		t.Fatal("Run() error = nil, want non-nil for a non-zero exit")
	}
	if output != "boom\n" {
		t.Errorf("output = %q, want %q even on error", output, "boom\n")
	}
}

func TestClient_RunStreaming_WritesLiveAndReturnsSameOutput(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		return "building...\nswitch complete\n", 0
	})

	client, err := Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	var out, errOut bytes.Buffer
	output, err := client.RunStreaming("nix run home-manager -- switch --flake x", &out, &errOut)
	if err != nil {
		t.Fatalf("RunStreaming() error = %v", err)
	}
	if output != "building...\nswitch complete\n" {
		t.Errorf("output = %q, want %q", output, "building...\nswitch complete\n")
	}
	if out.String() != "building...\nswitch complete\n" {
		t.Errorf("live stdout = %q, want the same content streamed live", out.String())
	}
}

func TestClient_RunStreaming_ReturnsErrorOnNonZeroExit(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		return "boom\n", 1
	})

	client, err := Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	var out, errOut bytes.Buffer
	output, err := client.RunStreaming("false", &out, &errOut)
	if err == nil {
		t.Fatal("RunStreaming() error = nil, want non-nil for a non-zero exit")
	}
	if output != "boom\n" {
		t.Errorf("output = %q, want %q even on error", output, "boom\n")
	}
}

func TestTrustOnFirstConnect_AcceptsUnknownThenRejectsDifferentKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	if err := touchFile(path); err != nil {
		t.Fatal(err)
	}

	callback, err := trustOnFirstConnectCallback(path)
	if err != nil {
		t.Fatalf("trustOnFirstConnectCallback() error = %v", err)
	}

	_, priv1, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer1, err := ssh.NewSignerFromKey(priv1)
	if err != nil {
		t.Fatal(err)
	}

	addr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 22}

	if err := callback("testhost:22", addr, signer1.PublicKey()); err != nil {
		t.Fatalf("first connect (unknown host) error = %v, want nil (accept and record)", err)
	}

	if err := callback("testhost:22", addr, signer1.PublicKey()); err != nil {
		t.Fatalf("second connect (same key) error = %v, want nil (matches recorded key)", err)
	}

	_, priv2, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer2, err := ssh.NewSignerFromKey(priv2)
	if err != nil {
		t.Fatal(err)
	}

	if err := callback("testhost:22", addr, signer2.PublicKey()); err == nil {
		t.Fatal("third connect (different key) error = nil, want rejection")
	}
}

func TestConnect_HostKeyMismatch_ErrorHasRecreateHint(t *testing.T) {
	startFakeAgent(t)
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		return "", 0
	})

	// Pre-seed known_hosts with a DIFFERENT key for this exact address,
	// simulating a recreated instance whose host key changed.
	knownHostsDir := filepath.Join(homeDir, ".ssh")
	if err := os.MkdirAll(knownHostsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, wrongPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongSigner, err := ssh.NewSignerFromKey(wrongPriv)
	if err != nil {
		t.Fatal(err)
	}
	line := knownhosts.Line([]string{knownhosts.Normalize(addr)}, wrongSigner.PublicKey())
	knownHostsPath := filepath.Join(knownHostsDir, "known_hosts")
	if err := os.WriteFile(knownHostsPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = Connect(context.Background(), addr, "devuser")
	if err == nil {
		t.Fatal("Connect() error = nil, want rejection for a mismatched host key")
	}
	if !strings.Contains(err.Error(), "known_hosts") {
		t.Errorf("error = %q, want a hint about removing the stale known_hosts entry", err.Error())
	}
}
