# SSH Agent Forwarding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a cloudlab-managed instance borrow the local SSH agent, so a private git input (a `flakes` entry pointing at `git+ssh://...`) can authenticate when `nix` fetches it on the instance.

**Architecture:** Two independent mechanisms, matched to how each call site already talks to the instance. `internal/reconcile.Client` is a pure-Go SSH client (`golang.org/x/crypto/ssh`); it gains a `EnableAgentForwarding` method using `golang.org/x/crypto/ssh/agent`'s `ForwardToAgent`/`RequestAgentForwarding`, wired in automatically (no flag) wherever `up`/`provision` and `session start` connect. `cloudlab ssh`/`tmux`/`pair` all just exec the real `ssh` binary via `tool.Passthrough` — for these, forwarding is a `--forward-agent` flag that prepends `-A` to the argv, no new code beyond that.

**Tech Stack:** Go, `golang.org/x/crypto/ssh` and `golang.org/x/crypto/ssh/agent` (already a direct dependency, no new module), `spf13/cobra`.

## Global Constraints

- A missing or unreachable local agent (`SSH_AUTH_SOCK` unset, or the socket refuses the dial) is never an error — every code path here degrades silently, exactly like `authMethods()`'s existing fallback to on-disk keys.
- The automatic tier (`up`/`provision`, `session start`) has no config flag and no on/off switch — it is always attempted. It costs nothing when the remote side never asks to use it.
- A failure to enable forwarding on the automatic tier must not fail the surrounding `Reconcile`/`seedSession` call — report it with `provider.ReportWarning` and continue.
- The opt-in tier (`ssh`, `tmux`, `pair`) is a `--forward-agent` bool flag, default `false`, no other gating.
- `herdr`, `connect`, and `shell` are explicitly out of scope: `herdr` manages its own SSH bridge with no forwarding flag on its CLI today; `connect`'s `Forward` never executes a remote command; `shell` has no implementation yet (`lookupCommandSpecs`' entry has no `run`).
- No new dependency. `golang.org/x/crypto/ssh/agent` is already imported by `internal/reconcile/ssh.go`.

---

### Task 1: `Client.EnableAgentForwarding` in `internal/reconcile`

**Files:**
- Modify: `internal/reconcile/ssh.go`
- Modify: `internal/reconcile/ssh_test.go`

**Interfaces:**
- Produces: `(*Client) EnableAgentForwarding() error` — call once after `Connect` succeeds. Returns `nil` when there's no local agent to forward, or a non-nil error only when `agent.ForwardToAgent` itself fails. Once called successfully, every session `RunContext`/`RunStreaming` opens afterward requests agent forwarding on it.

- [ ] **Step 1: Write the failing test**

Add to `internal/reconcile/ssh_test.go` (same package, so it can reuse `startFakeAgent`, `readAllChannel`, `requireIsolatedHome`, and `testenv.Isolate` already defined there):

```go
// startFakeSSHServerWithAgentForwarding is startFakeSSHServer, plus:
// when a session requests "auth-agent-req@openssh.com" before its
// exec, the server opens an "auth-agent@openssh.com" channel back to
// the client and hands onForwardedAgent an agent.Agent wrapping it --
// letting a test prove the client actually served its local agent
// over the forwarded channel, not just that it replied ok to the
// forwarding request.
func startFakeSSHServerWithAgentForwarding(
	t *testing.T,
	onForwardedAgent func(agent.Agent),
	handler func(cmd string, stdin []byte) (output string, exitCode uint32),
) string {
	requireIsolatedHome(t)
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
					go handleFakeSessionWithAgentForwarding(sshConn, channel, requests, onForwardedAgent, handler)
				}
			}()
		}
	}()

	return listener.Addr().String()
}

func handleFakeSessionWithAgentForwarding(
	sshConn *ssh.ServerConn,
	channel ssh.Channel,
	requests <-chan *ssh.Request,
	onForwardedAgent func(agent.Agent),
	handler func(cmd string, stdin []byte) (string, uint32),
) {
	for req := range requests {
		if req.Type == "auth-agent-req@openssh.com" {
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
			agentChannel, agentReqs, err := sshConn.OpenChannel("auth-agent@openssh.com", nil)
			if err == nil {
				go ssh.DiscardRequests(agentReqs)
				onForwardedAgent(agent.NewClient(agentChannel))
			}
			continue
		}
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

func TestClient_EnableAgentForwarding_ServesLocalAgentOverForwardedChannel(t *testing.T) {
	startFakeAgent(t)
	testenv.Isolate(t)

	forwarded := make(chan agent.Agent, 1)
	addr := startFakeSSHServerWithAgentForwarding(t,
		func(ag agent.Agent) { forwarded <- ag },
		func(cmd string, stdin []byte) (string, uint32) { return "", 0 },
	)

	client, err := Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	if err := client.EnableAgentForwarding(); err != nil {
		t.Fatalf("EnableAgentForwarding() error = %v", err)
	}

	if _, err := client.Run("anything"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	select {
	case ag := <-forwarded:
		keys, err := ag.List()
		if err != nil {
			t.Fatalf("forwarded agent List() error = %v", err)
		}
		if len(keys) != 1 {
			t.Errorf("forwarded agent listed %d keys, want 1 (the fake local agent's key)", len(keys))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received a forwarded agent channel -- RunContext did not request forwarding")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/reconcile/... -run TestClient_EnableAgentForwarding -v`
Expected: FAIL to compile — `client.EnableAgentForwarding undefined (type *Client has no field or method EnableAgentForwarding)`.

- [ ] **Step 3: Implement `EnableAgentForwarding` and wire it into every session `RunContext`/`RunStreaming` opens**

In `internal/reconcile/ssh.go`, add a field to `Client`:

```go
// Client wraps an established SSH connection to an instance.
type Client struct {
	conn *ssh.Client
	// forwardAgent is set by EnableAgentForwarding; when non-nil, every
	// session RunContext/RunStreaming opens requests agent forwarding
	// on it.
	forwardAgent agent.Agent
}
```

Add the method, right after `Connect`:

```go
// EnableAgentForwarding dials the local ssh-agent (SSH_AUTH_SOCK) and
// arranges for it to be forwarded to the instance: every session
// RunContext/RunStreaming opens afterward requests forwarding, so a
// remote git+ssh fetch (a private flake input) can authenticate
// against the same agent this process already uses for its own auth.
//
// A missing or unreachable local agent is not an error -- same as
// authMethods()'s own fallback to on-disk keys, a command that never
// attempts a private fetch is unaffected either way.
func (c *Client) EnableAgentForwarding() error {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil
	}
	// #nosec G704 -- sock is SSH_AUTH_SOCK, a local unix-domain-socket
	// path the user's own ssh-agent set in their own environment; see
	// the identical note on authMethods' own dial.
	agentConn, err := net.Dial("unix", sock)
	if err != nil {
		return nil
	}
	ag := agent.NewClient(agentConn)
	if err := agent.ForwardToAgent(c.conn, ag); err != nil {
		return fmt.Errorf("enabling agent forwarding: %w", err)
	}
	c.forwardAgent = ag
	return nil
}
```

In `RunContext`, request forwarding right after opening the session (before the `type result struct` block):

```go
	session, err := c.conn.NewSession()
	if err != nil {
		return "", fmt.Errorf("opening session: %w", err)
	}
	defer func() { _ = session.Close() }()

	if c.forwardAgent != nil {
		_ = agent.RequestAgentForwarding(session)
	}
```

In `RunStreaming`, the same, right after opening its session (before `var buf bytes.Buffer`):

```go
	session, err := c.conn.NewSession()
	if err != nil {
		return "", fmt.Errorf("opening session: %w", err)
	}
	defer func() { _ = session.Close() }()

	if c.forwardAgent != nil {
		_ = agent.RequestAgentForwarding(session)
	}
```

`Run` already delegates to `RunContext`, so it's covered without a separate change. `WriteFile`/`WriteSecretFile` don't touch this — neither ever fetches anything.

Both `os`, `net`, `fmt`, and `golang.org/x/crypto/ssh/agent` are already imported in `ssh.go` (used by `authMethods`) — no import changes needed.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/reconcile/... -run TestClient_EnableAgentForwarding -v`
Expected: PASS

- [ ] **Step 5: Run the full package's tests to confirm nothing else broke**

Run: `go test ./internal/reconcile/...`
Expected: PASS — every existing test in this package calls `startFakeAgent(t)` already, and `RequestAgentForwarding` against `startFakeSSHServer`'s plain handler (which replies `false` to anything but `exec`) is a no-op that's silently ignored, same as before this change.

- [ ] **Step 6: Commit**

```bash
git add internal/reconcile/ssh.go internal/reconcile/ssh_test.go
git commit -m "Add agent forwarding to the reconcile SSH client"
```

---

### Task 2: Wire agent forwarding into `Reconcile` (`up`/`provision`)

**Files:**
- Modify: `internal/reconcile/reconcile.go`
- Modify: `internal/reconcile/reconcile_test.go`

**Interfaces:**
- Consumes: `(*Client) EnableAgentForwarding() error` (Task 1); `provider.ReportWarning(ctx context.Context, msg string)` (already used elsewhere in this file).

- [ ] **Step 1: Write the failing test**

Add to `internal/reconcile/reconcile_test.go`. This package already has `writeFixture`, `seedInstance`, and (from Task 1) `startFakeSSHServerWithAgentForwarding`:

```go
func TestReconcile_ForwardsAgentDuringSwitch(t *testing.T) {
	startFakeAgent(t)
	testenv.Isolate(t)

	forwarded := make(chan agent.Agent, 1)
	addr := startFakeSSHServerWithAgentForwarding(t,
		func(ag agent.Agent) { forwarded <- ag },
		func(cmd string, stdin []byte) (string, uint32) { return "", 0 },
	)
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

	select {
	case ag := <-forwarded:
		if _, err := ag.List(); err != nil {
			t.Errorf("forwarded agent List() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("home-manager switch's session never received a forwarded agent channel")
	}
}
```

Add `"time"` and `"golang.org/x/crypto/ssh/agent"` to this file's import block.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/reconcile/... -run TestReconcile_ForwardsAgentDuringSwitch -v`
Expected: FAIL — times out waiting on `forwarded` (the switch's session never requests forwarding yet).

- [ ] **Step 3: Call `EnableAgentForwarding` right after connecting**

In `internal/reconcile/reconcile.go`'s `Reconcile`, right after the existing:

```go
	client, err := Connect(ctx, record.IP, record.User)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", record.IP, err)
	}
	defer func() { _ = client.Close() }()
```

add:

```go
	if err := client.EnableAgentForwarding(); err != nil {
		provider.ReportWarning(ctx, "agent forwarding: "+err.Error())
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/reconcile/... -run TestReconcile_ForwardsAgentDuringSwitch -v`
Expected: PASS

- [ ] **Step 5: Run the full package's tests**

Run: `go test ./internal/reconcile/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/reconcile/reconcile.go internal/reconcile/reconcile_test.go
git commit -m "Forward the local agent during up/provision's home-manager switch"
```

---

### Task 3: Wire agent forwarding into `session start`

**Files:**
- Modify: `internal/lifecycle/start.go`
- Modify: `internal/lifecycle/ready_test.go` (add the forwarding-capable fake server, mirroring Task 1's copy)
- Modify: `internal/lifecycle/session_test.go`

**Interfaces:**
- Consumes: `(*Client) EnableAgentForwarding() error` (Task 1, same method — `internal/lifecycle` imports `internal/reconcile`); `provider.ReportWarning` (already imported in `start.go`).

- [ ] **Step 1: Write the failing test**

`internal/lifecycle` already keeps its own copy of `startFakeAgent`/`startFakeSSHServer`/`handleFakeSession` in `ready_test.go` (a deliberate per-package duplication, per that file's own comment). Add the forwarding-capable variant there too, identical to Task 1's addition to `internal/reconcile/ssh_test.go` (same three functions: `startFakeSSHServerWithAgentForwarding`, `handleFakeSessionWithAgentForwarding`, using this file's own `requireIsolatedHome`/`readAllChannel`). `time` and `golang.org/x/crypto/ssh/agent` are already imported in `ready_test.go`.

Then add to `internal/lifecycle/session_test.go`:

```go
func TestSeedSession_ForwardsAgentToTheInstance(t *testing.T) {
	noGitSSH(t)
	startFakeAgent(t)
	testenv.Isolate(t)

	forwarded := make(chan agent.Agent, 1)
	addr := startFakeSSHServerWithAgentForwarding(t,
		func(ag agent.Agent) { forwarded <- ag },
		func(cmd string, stdin []byte) (string, uint32) { return "", 0 },
	)

	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)

	remoteRepo := RemoteRepoPath("devuser", "auth", "cloudlab")
	// seedSession fails at the push -- the fake server has no real git
	// backend -- but forwarding is requested on the very first session
	// it opens, before that push ever runs. See
	// TestSeedSession_CreatesTheRepoThenPushes for the same shape.
	_ = seedSession(context.Background(), addr, "devuser", repo, remoteRepo,
		SessionBranch("auth"), sshGitURL("devuser", addr, remoteRepo), addr, "auth", "session", "")

	select {
	case ag := <-forwarded:
		if _, err := ag.List(); err != nil {
			t.Errorf("forwarded agent List() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("seedSession never requested agent forwarding on its first remote session")
	}
}
```

Add `"time"` and `"golang.org/x/crypto/ssh/agent"` to `session_test.go`'s import block.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lifecycle/... -run TestSeedSession_ForwardsAgentToTheInstance -v`
Expected: FAIL — times out waiting on `forwarded`.

- [ ] **Step 3: Call `EnableAgentForwarding` right after connecting in `seedSession`**

In `internal/lifecycle/start.go`, right after the existing:

```go
	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
```

add:

```go
	if err := client.EnableAgentForwarding(); err != nil {
		provider.ReportWarning(ctx, "agent forwarding: "+err.Error())
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lifecycle/... -run TestSeedSession_ForwardsAgentToTheInstance -v`
Expected: PASS

- [ ] **Step 5: Run the full package's tests**

Run: `go test ./internal/lifecycle/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/lifecycle/start.go internal/lifecycle/ready_test.go internal/lifecycle/session_test.go
git commit -m "Forward the local agent when a session's repo is seeded"
```

---

### Task 4: `--forward-agent` flag for `cloudlab ssh`

**Files:**
- Modify: `internal/lifecycle/ssh.go`
- Modify: `internal/lifecycle/ssh_test.go`
- Modify: `cmd/lookup.go`
- Modify: `cmd/run_attach.go`

**Interfaces:**
- Produces: `sshArgs(ip, user, dir string, forwardAgent bool) []string`, `SSH(ctx context.Context, ip, user, dir string, forwardAgent bool) error` — both gain a trailing `forwardAgent bool` parameter.

- [ ] **Step 1: Write the failing test**

In `internal/lifecycle/ssh_test.go`, update the two existing calls to pass `false`, and add a new test:

```go
func TestSSHArgs_NoDir_PlainSession(t *testing.T) {
	got := sshArgs("203.0.113.5", "devuser", "", false)
	// ... rest unchanged
}

func TestSSHArgs_WithDir_CdsBeforeInteractiveShell(t *testing.T) {
	got := sshArgs("203.0.113.5", "devuser", "/home/devuser/project", false)
	// ... rest unchanged
}

func TestSSHArgs_ForwardAgent_PrependsDashA(t *testing.T) {
	got := sshArgs("203.0.113.5", "devuser", "", true)
	want := []string{"-A", "devuser@203.0.113.5"}
	if len(got) != len(want) {
		t.Fatalf("sshArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sshArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lifecycle/... -run TestSSHArgs -v`
Expected: FAIL to compile — `not enough arguments in call to sshArgs`.

- [ ] **Step 3: Implement**

In `internal/lifecycle/ssh.go`:

```go
func sshArgs(ip, user, dir string, forwardAgent bool) []string {
	var flags []string
	if forwardAgent {
		flags = append(flags, "-A")
	}
	if dir == "" {
		return append(flags, user+"@"+ip)
	}
	inner := "[[ -d " + shellcmd.Quote(dir) + " ]] && cd " + shellcmd.Quote(dir) + "; exec \"$SHELL\" -l"
	return append(flags, "-t", user+"@"+ip, shellcmd.LoginShell(inner))
}

func SSH(ctx context.Context, ip, user, dir string, forwardAgent bool) error {
	if _, err := tool.Require("ssh"); err != nil {
		return err
	}
	return tool.Passthrough(ctx, "ssh", sshArgs(ip, user, dir, forwardAgent)...)
}
```

In `cmd/lookup.go`, the `"ssh [name]"` spec's `flags` func gains a line:

```go
flags: func(c *cobra.Command) {
	c.Flags().String("dir", "", "remote directory to cd into (defaults to the synced repo's location)")
	c.Flags().Bool("forward-agent", false, "forward your local SSH agent to the instance for this connection")
},
```

In `cmd/run_attach.go`'s `runSSH`, read the flag and pass it through:

```go
func runSSH(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	dir, err := cmd.Flags().GetString("dir")
	if err != nil {
		return err
	}
	forwardAgent, err := cmd.Flags().GetBool("forward-agent")
	if err != nil {
		return err
	}
	if dir == "" {
		if sess, err := resolveSessionInteractive(cmd, record, nil); err == nil {
			dir = lifecycle.RemoteRepoPath(record.User, sess.Name, sess.RepoNameOr(record.Name))
		}
	}
	return lifecycle.SSH(cmd.Context(), record.IP, record.User, dir, forwardAgent)
}
```

(Only the new `forwardAgent` lookup and the final return line change; everything else in the function is unchanged.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lifecycle/... -run TestSSHArgs -v`
Expected: PASS

- [ ] **Step 5: Build and run the full test suite**

Run: `go build ./... && go test ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/lifecycle/ssh.go internal/lifecycle/ssh_test.go cmd/lookup.go cmd/run_attach.go
git commit -m "Add --forward-agent to cloudlab ssh"
```

---

### Task 5: `--forward-agent` flag for `cloudlab tmux`

**Files:**
- Modify: `internal/lifecycle/tmux.go`
- Modify: `internal/lifecycle/tmux_test.go`
- Modify: `cmd/lookup.go`
- Modify: `cmd/run_attach.go`

**Interfaces:**
- Produces: `tmuxArgs(ip, user, session string, forwardAgent bool) []string`, `Tmux(ctx context.Context, ip, user, session string, forwardAgent bool) error`.

- [ ] **Step 1: Write the failing test**

In `internal/lifecycle/tmux_test.go`, update the two existing calls to pass `false`, and add:

```go
func TestTmuxArgs_ForwardAgent_PrependsDashA(t *testing.T) {
	got := tmuxArgs("203.0.113.5", "devuser", "main", true)
	if got[0] != "-A" {
		t.Fatalf("tmuxArgs()[0] = %q, want -A first", got[0])
	}
	inner := "tmux new-session -A -s " + shellcmd.Quote("main")
	want := []string{"-A", "-t", "devuser@203.0.113.5", shellcmd.LoginShell(inner)}
	if len(got) != len(want) {
		t.Fatalf("tmuxArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tmuxArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lifecycle/... -run TestTmuxArgs -v`
Expected: FAIL to compile — `not enough arguments in call to tmuxArgs`.

- [ ] **Step 3: Implement**

In `internal/lifecycle/tmux.go`:

```go
func tmuxArgs(ip, user, session string, forwardAgent bool) []string {
	var flags []string
	if forwardAgent {
		flags = append(flags, "-A")
	}
	inner := "tmux new-session -A -s " + shellcmd.Quote(session)
	return append(flags, "-t", user+"@"+ip, shellcmd.LoginShell(inner))
}

func Tmux(ctx context.Context, ip, user, session string, forwardAgent bool) error {
	if _, err := tool.Require("ssh"); err != nil {
		return err
	}
	return tool.Passthrough(ctx, "ssh", tmuxArgs(ip, user, session, forwardAgent)...)
}
```

In `cmd/lookup.go`, the `"tmux [session-name]"` spec (which has no `flags` func today) gains one:

```go
{
	use:   "tmux [session-name]",
	short: "Open a tmux session on the instance, creating it if needed (default session: \"main\")",
	verb:  "tmux",
	args:  cobra.MaximumNArgs(1),
	named: false,
	flags: func(c *cobra.Command) {
		c.Flags().Bool("forward-agent", false, "forward your local SSH agent to the instance for this connection")
	},
	run: runTmux,
},
```

In `cmd/run_attach.go`'s `runTmux`:

```go
func runTmux(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	forwardAgent, err := cmd.Flags().GetBool("forward-agent")
	if err != nil {
		return err
	}
	session := tmuxSession(args)
	if len(args) == 0 {
		if sess, err := resolveSessionInteractive(cmd, record, nil); err == nil {
			session = sess.Name
		}
	}
	return lifecycle.Tmux(cmd.Context(), record.IP, record.User, session, forwardAgent)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lifecycle/... -run TestTmuxArgs -v`
Expected: PASS

- [ ] **Step 5: Build and run the full test suite**

Run: `go build ./... && go test ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/lifecycle/tmux.go internal/lifecycle/tmux_test.go cmd/lookup.go cmd/run_attach.go
git commit -m "Add --forward-agent to cloudlab tmux"
```

---

### Task 6: `--forward-agent` flag for `cloudlab pair`

**Files:**
- Modify: `internal/lifecycle/pair.go`
- Modify: `internal/lifecycle/pair_test.go`
- Modify: `cmd/lookup.go`
- Modify: `cmd/run_attach.go`

**Interfaces:**
- Produces: `pairArgs(sshHost, advertiseHost, user string, forwardAgent bool) []string`, `Pair(ctx context.Context, sshHost, advertiseHost, user string, forwardAgent bool) error`.

- [ ] **Step 1: Write the failing test**

In `internal/lifecycle/pair_test.go`, update the two existing calls to pass `false`, and add:

```go
func TestPairArgs_ForwardAgent_PrependsDashA(t *testing.T) {
	got := pairArgs("203.0.113.5", "203.0.113.5", "devuser", true)
	inner := "moshi-hook host setup --host " + shellcmd.Quote("203.0.113.5")
	want := []string{"-A", "-t", "devuser@203.0.113.5", shellcmd.LoginShell(inner)}
	if len(got) != len(want) {
		t.Fatalf("pairArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pairArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lifecycle/... -run TestPairArgs -v`
Expected: FAIL to compile — `not enough arguments in call to pairArgs`.

- [ ] **Step 3: Implement**

In `internal/lifecycle/pair.go`:

```go
func pairArgs(sshHost, advertiseHost, user string, forwardAgent bool) []string {
	var flags []string
	if forwardAgent {
		flags = append(flags, "-A")
	}
	inner := "moshi-hook host setup --host " + shellcmd.Quote(advertiseHost)
	return append(flags, "-t", user+"@"+sshHost, shellcmd.LoginShell(inner))
}

func Pair(ctx context.Context, sshHost, advertiseHost, user string, forwardAgent bool) error {
	if _, err := tool.Require("ssh"); err != nil {
		return err
	}
	return tool.Passthrough(ctx, "ssh", pairArgs(sshHost, advertiseHost, user, forwardAgent)...)
}
```

In `cmd/lookup.go`, the `"pair [name]"` spec's `flags` func gains a line:

```go
flags: func(c *cobra.Command) {
	c.Flags().String("host", "", "address the QR advertises to the phone (defaults to prompting when a Tailscale address is available)")
	c.Flags().Bool("forward-agent", false, "forward your local SSH agent to the instance for this connection")
},
```

In `cmd/run_attach.go`'s `runPair`:

```go
func runPair(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}

	advertise, err := cmd.Flags().GetString("host")
	if err != nil {
		return err
	}
	if advertise == "" {
		advertise, err = choosePairHost(cmd, record)
		if err != nil {
			return err
		}
	}
	forwardAgent, err := cmd.Flags().GetBool("forward-agent")
	if err != nil {
		return err
	}
	return lifecycle.Pair(cmd.Context(), record.IP, advertise, record.User, forwardAgent)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lifecycle/... -run TestPairArgs -v`
Expected: PASS

- [ ] **Step 5: Build and run the full test suite**

Run: `go build ./... && go test ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/lifecycle/pair.go internal/lifecycle/pair_test.go cmd/lookup.go cmd/run_attach.go
git commit -m "Add --forward-agent to cloudlab pair"
```
