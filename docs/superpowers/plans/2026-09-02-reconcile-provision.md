# Reconcile + `provision` Command Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `internal/reconcile.Reconcile(ctx, name, cloudlabPath) error` — the function `up`, `shell`, and a new `provision` command will all share — and wire it into a real, fully working `provision` command.

**Architecture:** A new `internal/reconcile` package holds a small SSH client wrapper (connect via ssh-agent auth + trust-on-first-connect host keys, write a file via `cat >`, run a remote command) and the `Reconcile` orchestration function on top of it, consuming the already-built `internal/config`, `internal/provisioning`, and `internal/state` packages. A new `cmd/provision.go` wires it to a cobra command using the same name-resolution pattern `up` already uses.

**Tech Stack:** Go 1.24 stdlib plus one new dependency, `golang.org/x/crypto` (`ssh`, `ssh/agent`, `ssh/knownhosts`) — the first non-stdlib dependency this project adds specifically for SSH; `nix`/`pkl` continue to be invoked via `os/exec` as before, unrelated to this addition.

## Global Constraints

- VM user is hardcoded `root` (matches the Provisioning plan's Global Constraints — no per-user config).
- The rendered per-instance flake, when one is needed, always ships to the fixed remote path `/root/.cache/cloudlab/flake.nix` and is switched via `path:/root/.cache/cloudlab#default` (matches `provisioning.Render`'s `homeConfigurations.default` output name).
- `up` and `shell` remain stub commands after this plan — only `provision` becomes real. Do not touch `cmd/up.go` or the `shell` entry in `cmd/lookup.go`.
- No live progress streaming — `Reconcile` runs synchronously to completion and returns success or an error. Streaming is `cloudlab-b90`'s separate epic.
- Every SSH-related test uses a real, local, in-process fake SSH server and fake ssh-agent (both via `golang.org/x/crypto/ssh`'s own client/server/agent APIs) — never a live VM, never a subprocess-mocking layer.

---

### Task 1: SSH client wrapper

**Files:**
- Create: `internal/reconcile/ssh.go`
- Test: `internal/reconcile/ssh_test.go`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `Connect(ctx context.Context, ip string) (*Client, error)`, `(*Client) WriteFile(remotePath, content string) error`, `(*Client) Run(cmd string) (output string, err error)`, `(*Client) Close() error`, `shellQuote(s string) string` — all consumed by Task 2.

- [ ] **Step 1: Add the new dependency**

Run (from repo root, inside `nix develop` — network access to the Go module proxy is required, same as `pkl`/`nix` elsewhere in this project):

```bash
go get golang.org/x/crypto@latest
```

Expected: `go.mod` gains a new direct `require golang.org/x/crypto v...` line, `go.sum` is updated.

- [ ] **Step 2: Write the failing tests**

Create `internal/reconcile/ssh_test.go`:

```go
package reconcile

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
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

	sockPath := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go agent.ServeAgent(keyring, conn)
		}
	}()

	t.Setenv("SSH_AUTH_SOCK", sockPath)
}

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
			return &ssh.Permissions{}, nil
		},
	}
	config.AddHostKey(hostSigner)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })

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
				defer sshConn.Close()
				go ssh.DiscardRequests(reqs)
				for newChannel := range chans {
					if newChannel.ChannelType() != "session" {
						newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
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
				req.Reply(false, nil)
			}
			continue
		}
		var payload struct{ Command string }
		ssh.Unmarshal(req.Payload, &payload)
		req.Reply(true, nil)

		stdin := readAllChannel(channel)
		output, exitCode := handler(payload.Command, stdin)
		channel.Write([]byte(output))
		exitMsg := struct{ ExitStatus uint32 }{exitCode}
		channel.SendRequest("exit-status", false, ssh.Marshal(exitMsg))
		channel.Close()
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

func TestClient_WriteFile_SendsContentViaCatRedirect(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var got sessionResult
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		got = sessionResult{Command: cmd, Stdin: stdin}
		return "", 0
	})

	client, err := Connect(context.Background(), addr)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer client.Close()

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

func TestClient_Run_ReturnsOutputOnSuccess(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		return "switch complete\n", 0
	})

	client, err := Connect(context.Background(), addr)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer client.Close()

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

	client, err := Connect(context.Background(), addr)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer client.Close()

	output, err := client.Run("false")
	if err == nil {
		t.Fatal("Run() error = nil, want non-nil for a non-zero exit")
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
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/reconcile/... -v`
Expected: FAIL — compile errors, `undefined: Connect` / `Client` / `trustOnFirstConnectCallback` / `touchFile` (package doesn't exist yet).

- [ ] **Step 4: Write the minimal implementation**

Create `internal/reconcile/ssh.go`:

```go
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
	verify, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("reading known_hosts %s: %w", path, err)
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
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
	defer f.Close()
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
	defer session.Close()

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
	defer session.Close()

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
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/reconcile/... -v`
Expected: PASS — all 5 tests in the package.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/reconcile/ssh.go internal/reconcile/ssh_test.go
git commit -m "Add SSH client wrapper for instance reconciliation"
```

---

### Task 2: `Reconcile` orchestration

**Files:**
- Create: `internal/reconcile/reconcile.go`
- Test: `internal/reconcile/reconcile_test.go`

**Interfaces:**
- Consumes: `Connect`, `(*Client) WriteFile`, `(*Client) Run`, `(*Client) Close`, `shellQuote` (Task 1); `config.Resolve(ctx, path) (config.Config, error)`, `config.Config{Template *string, Arch string, Packages []string, Flakes []config.Flake}` (existing); `provisioning.ResolveTemplateRef(template, arch string) string`, `provisioning.NeedsRender(cfg config.Config) bool`, `provisioning.Render(cfg config.Config, templateRef string) (string, error)` (existing); `state.Open() (*state.Store, error)`, `(*state.Store) Get(name string) (state.Record, bool, error)`, `state.Record{IP string, ...}` (existing).
- Produces: `Reconcile(ctx context.Context, name, cloudlabPath string) error` — consumed by Task 3.

- [ ] **Step 1: Write the failing tests**

Create `internal/reconcile/reconcile_test.go`:

```go
package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/state"
)

func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing fixture %s: %v", path, err)
	}
}

// seedInstance points state at a temp dir, records a Record for name
// whose IP is the fake SSH server's full "host:port" address (Connect
// only appends its default port when one isn't already present, so
// tests can point it at a fake server's arbitrary port this way), and
// points XDG_CONFIG_HOME at a nonexistent dir so no ambient personal
// base config interferes.
func seedInstance(t *testing.T, name, addr string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-such-config"))

	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(state.Record{Name: name, IP: addr}); err != nil {
		t.Fatal(err)
	}
}

func TestReconcile_NoPackages_SwitchesBareTemplateRef_NoFileShipped(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var gotCmd string
	fileWritten := false
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		if strings.Contains(cmd, "cat >") {
			fileWritten = true
		}
		gotCmd = cmd
		return "", 0
	})
	seedInstance(t, "myinstance", addr)

	dir := t.TempDir()
	cloudlabPath := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, cloudlabPath, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
	}, "\n")+"\n")

	if err := Reconcile(context.Background(), "myinstance", cloudlabPath); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if fileWritten {
		t.Error("no packages/flakes set, but a file was shipped — should switch the bare template ref instead")
	}
	if !strings.Contains(gotCmd, "home-manager") || !strings.Contains(gotCmd, "switch") {
		t.Errorf("command = %q, want a home-manager switch invocation", gotCmd)
	}
	if !strings.Contains(gotCmd, "python-x86_64-linux") {
		t.Errorf("command = %q, want it to reference the resolved template ref", gotCmd)
	}
}

func TestReconcile_WithPackages_ShipsRenderedFlakeThenSwitchesIt(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var gotWriteCmd, gotWriteStdin, gotSwitchCmd string
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		if strings.Contains(cmd, "cat >") {
			gotWriteCmd = cmd
			gotWriteStdin = string(stdin)
		} else {
			gotSwitchCmd = cmd
		}
		return "", 0
	})
	seedInstance(t, "myinstance", addr)

	dir := t.TempDir()
	cloudlabPath := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, cloudlabPath, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`packages { "ripgrep" }`,
	}, "\n")+"\n")

	if err := Reconcile(context.Background(), "myinstance", cloudlabPath); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if gotWriteCmd == "" {
		t.Fatal("packages set, but no file was shipped")
	}
	if !strings.Contains(gotWriteCmd, "/root/.cache/cloudlab/flake.nix") {
		t.Errorf("write command = %q, want it to target the fixed remote flake path", gotWriteCmd)
	}
	if !strings.Contains(gotWriteStdin, `pkgs."ripgrep"`) {
		t.Errorf("shipped content = %q, want it to reference the configured package", gotWriteStdin)
	}
	if !strings.Contains(gotSwitchCmd, "path:/root/.cache/cloudlab#default") {
		t.Errorf("switch command = %q, want it to target the shipped flake's default output", gotSwitchCmd)
	}
}

func TestReconcile_InstanceNotFound_ReturnsClearError(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	err := Reconcile(context.Background(), "nosuchinstance", "/unused/cloudlab.pkl")
	if err == nil {
		t.Fatal("Reconcile() error = nil, want error for an instance not in state")
	}
	if !strings.Contains(err.Error(), "not found") || !strings.Contains(err.Error(), "nosuchinstance") {
		t.Errorf("error = %q, want it to name the missing instance", err.Error())
	}
}

func TestReconcile_HomeManagerSwitchFails_ErrorIncludesOutput(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		return "error: attribute 'cli' missing\n", 1
	})
	seedInstance(t, "myinstance", addr)

	dir := t.TempDir()
	cloudlabPath := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, cloudlabPath, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
	}, "\n")+"\n")

	err := Reconcile(context.Background(), "myinstance", cloudlabPath)
	if err == nil {
		t.Fatal("Reconcile() error = nil, want error for a failed home-manager switch")
	}
	if !strings.Contains(err.Error(), "attribute 'cli' missing") {
		t.Errorf("error = %q, want it to include the remote command's output", err.Error())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/reconcile/... -run TestReconcile -v`
Expected: FAIL — compile error, `undefined: Reconcile`.

- [ ] **Step 3: Write the minimal implementation**

Create `internal/reconcile/reconcile.go`:

```go
package reconcile

import (
	"context"
	"fmt"
	"strings"

	"github.com/jskswamy/cloudlab/internal/config"
	"github.com/jskswamy/cloudlab/internal/provisioning"
	"github.com/jskswamy/cloudlab/internal/state"
)

const (
	remoteFlakeDir  = "/root/.cache/cloudlab"
	remoteFlakePath = remoteFlakeDir + "/flake.nix"
)

// Reconcile brings name's home-manager environment up to date with its
// local cloudlab.pkl at cloudlabPath: resolves the config, renders a
// per-instance wrapper flake if packages/flakes require one, ships it to
// the instance over SSH, and runs home-manager switch. This is the one
// piece up, shell, and provision all share.
func Reconcile(ctx context.Context, name, cloudlabPath string) error {
	store, err := state.Open()
	if err != nil {
		return err
	}
	record, ok, err := store.Get(name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("instance %q not found — run \"cloudlab up %s\" first", name, name)
	}

	cfg, err := config.Resolve(ctx, cloudlabPath)
	if err != nil {
		return err
	}

	templateRef := provisioning.ResolveTemplateRef(*cfg.Template, cfg.Arch)

	client, err := Connect(ctx, record.IP)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", record.IP, err)
	}
	defer client.Close()

	flakeArg := templateRef
	if provisioning.NeedsRender(cfg) {
		content, err := provisioning.Render(cfg, templateRef)
		if err != nil {
			return fmt.Errorf("rendering per-instance flake: %w", err)
		}
		if err := client.WriteFile(remoteFlakePath, content); err != nil {
			return fmt.Errorf("shipping per-instance flake: %w", err)
		}
		flakeArg = "path:" + remoteFlakeDir + "#default"
	}

	cmd := "nix run home-manager -- switch --flake " + shellQuote(flakeArg)
	output, err := client.Run(cmd)
	if err != nil {
		return fmt.Errorf("home-manager switch failed: %w\n%s", err, tail(output, 40))
	}
	return nil
}

// tail returns s's last n lines, unchanged if it has n or fewer.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/reconcile/... -v`
Expected: PASS — all 9 tests in the package (5 from Task 1, 4 from this task).

- [ ] **Step 5: Commit**

```bash
git add internal/reconcile/reconcile.go internal/reconcile/reconcile_test.go
git commit -m "Add Reconcile orchestration"
```

---

### Task 3: `provision` command and ADR updates

**Files:**
- Create: `cmd/provision.go`
- Test: `cmd/provision_test.go`
- Modify: `cmd/root.go`
- Modify: `docs/adr/0004-nix-home-manager-provisioning.md`
- Modify: `docs/adr/0007-command-surface.md`

**Interfaces:**
- Consumes: `reconcile.Reconcile(ctx, name, cloudlabPath string) error` (Task 2); `identity.RepoRoot(cwd, repoFlag string) (string, error)`, `identity.DeriveName(root string) (string, error)` (existing, same functions `up` already uses).
- Produces: nothing — this is the final integration task.

- [ ] **Step 1: Write the failing tests**

Create `cmd/provision_test.go`:

```go
package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestProvisionCommand_NotInRepoErrors(t *testing.T) {
	chdir(t, t.TempDir())

	root := newRootCmd()
	root.SetArgs([]string{"provision"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "use --repo") {
		t.Errorf("error = %q, want mention of --repo", err.Error())
	}
}

func TestProvisionCommand_InstanceNotFoundErrors(t *testing.T) {
	chdir(t, initTestRepo(t))
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	root := newRootCmd()
	root.SetArgs([]string{"provision"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err.Error())
	}
}

func TestProvisionCommand_PositionalNameOverridesDerivedName(t *testing.T) {
	chdir(t, initTestRepo(t))
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	root := newRootCmd()
	root.SetArgs([]string{"provision", "somename"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `"somename"`) {
		t.Errorf("error = %q, want it to name instance %q, not the derived repo name", err.Error(), "somename")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/... -run TestProvisionCommand -v`
Expected: FAIL — compile error, `undefined: newProvisionCmd` (not registered yet, command doesn't exist).

- [ ] **Step 3: Write the command**

Create `cmd/provision.go`:

```go
package cmd

import (
	"os"
	"path/filepath"

	"github.com/jskswamy/cloudlab/internal/identity"
	"github.com/jskswamy/cloudlab/internal/reconcile"
	"github.com/spf13/cobra"
)

func newProvisionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provision [name]",
		Short: "Reconcile home-manager with the current cloudlab.pkl",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoFlag, _ := cmd.Flags().GetString("repo")
			nameFlag, _ := cmd.Flags().GetString("name")

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			root, err := identity.RepoRoot(cwd, repoFlag)
			if err != nil {
				return err
			}

			name := nameFlag
			if len(args) > 0 {
				name = args[0]
			}
			if name == "" {
				name, err = identity.DeriveName(root)
				if err != nil {
					return err
				}
			}

			cloudlabPath := filepath.Join(root, "cloudlab.pkl")
			return reconcile.Reconcile(cmd.Context(), name, cloudlabPath)
		},
	}
	return cmd
}
```

- [ ] **Step 4: Register the command**

Modify `cmd/root.go` — add one line after the existing `root.AddCommand(newUpCmd())`:

```go
	root.AddCommand(newListCmd())
	root.AddCommand(newUpCmd())
	root.AddCommand(newProvisionCmd())
	root.AddCommand(newLookupCommands()...)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/... -v`
Expected: PASS — every test in the package, including the 3 new ones.

- [ ] **Step 6: Update ADR-0004**

`docs/adr/0004-nix-home-manager-provisioning.md` currently says cloud-init itself runs `nix run home-manager -- switch --flake <ref>#<template>` — stale against the single-pass `Reconcile` design (`cloudlab-g42`), where cloud-init only installs Nix and the CLI runs `home-manager switch` over SSH afterward.

Replace this bullet in the Decision section:

```markdown
- Cloud-init's job shrinks to a template-agnostic bootstrap: install Nix
  (Determinate Systems installer — non-interactive, flakes enabled), then
  run `nix run home-manager -- switch --flake <ref>#<template>`.
```

with:

```markdown
- Cloud-init's job shrinks to a template-agnostic bootstrap: install Nix
  (Determinate Systems installer — non-interactive, flakes enabled) and
  nothing else. `home-manager switch` is not run by cloud-init — it runs
  over SSH from the CLI side, as part of `Reconcile` (see ADR-0007's
  `provision` addition), so the exact same code path handles both the
  instance's first activation and every later reconcile.
```

And replace this line in the Consequences section:

```markdown
- Reconciling packages later (ADR-0005) becomes "re-run home-manager switch"
  rather than "re-run arbitrary shell commands," which is what makes
  idempotent, safe-to-repeat reconciliation from `up`/`shell` practical.
```

with:

```markdown
- Reconciling packages later (ADR-0005) becomes "re-run home-manager switch"
  rather than "re-run arbitrary shell commands," which is what makes
  idempotent, safe-to-repeat reconciliation from `up`/`shell`/`provision`
  practical — all three call the same `Reconcile` function.
```

- [ ] **Step 7: Update ADR-0007**

`docs/adr/0007-command-surface.md`'s `up` bullet and Consequences section
describe the old two-pass reconcile and the superseded `cloudlab.yaml`
filename, and don't mention the new `provision` command.

Replace this bullet in the Decision section:

```markdown
- **`up`** does the full bring-up in one command: create the VM, run
  cloud-init/home-manager, rsync the repo to `~/reponame`, reconcile
  home-manager again with the repo's `cloudlab.yaml`, start `watch`. On
  return, the instance is fully live — no separate step required.
```

with:

```markdown
- **`up`** does the full bring-up in one command: create the VM, wait for
  it to be reachable, reconcile home-manager with the project's
  `cloudlab.pkl` exactly once (not twice — see ADR-0004), rsync the repo
  to `~/reponame` (independent of reconcile), start `watch`. On return,
  the instance is fully live — no separate step required.
- **`provision [name]`** reconciles home-manager with the current
  `cloudlab.pkl` and does nothing else — no VM creation, no subshell, no
  repo rsync. For the common case of "I only changed `cloudlab.pkl`,
  apply it now" without either of `up`'s or `shell`'s side effects.
```

And replace this line in the Consequences section:

```markdown
- Reconciliation triggers narrow to `{up, shell}` — `sync`/`download` are
  fully orthogonal to `cloudlab.yaml`/home-manager, which is a deliberate
  simplification: they're a plain file-transfer utility, not a provisioning
  hook.
```

with:

```markdown
- Reconciliation triggers narrow to `{up, shell, provision}` — `sync`/
  `download` are fully orthogonal to `cloudlab.pkl`/home-manager, which is
  a deliberate simplification: they're a plain file-transfer utility, not
  a provisioning hook. All three reconciliation triggers share the exact
  same `Reconcile` function, never duplicated per command.
```

- [ ] **Step 8: Commit**

```bash
git add cmd/provision.go cmd/provision_test.go cmd/root.go docs/adr/0004-nix-home-manager-provisioning.md docs/adr/0007-command-surface.md
git commit -m "Add provision command, amend ADR-0004 and ADR-0007"
```

---

## Final check

Run the full suite once more from the repo root (inside `nix develop`):

```bash
go build ./...
go vet ./...
go test ./... -v
nix flake check --print-build-logs
```

Expected: everything builds, every test across the repo passes (this
plan's `internal/reconcile` and `cmd` tests plus every untouched earlier
phase's tests — aside from `TestLoadFromPath_MinimalFixture`, the known,
expected, machine-specific exception documented throughout this project's
history), and `nix flake check` is clean.
