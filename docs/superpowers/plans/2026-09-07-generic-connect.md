# Generic Connect Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn `connect` from a Jupyter-shaped stub into a command that reaches any service on an instance — printing a tailnet URL when the service is routable, forwarding over SSH when it is not, and asking what is listening when given no port.

**Architecture:** `connect` resolves before it tunnels. It asks the instance what is listening (`ss -tlnp`), decides from the *bind address* whether the tailnet can reach it, and either prints a URL or runs a foreground `ssh -L`. Nothing is written to state on either path.

**Tech Stack:** Go 1.x, `golang.org/x/crypto/ssh` (already vendored, used by `internal/reconcile`), the system `ssh` binary for the forward.

**Spec:** `docs/superpowers/specs/2026-09-07-presets-and-connect-design.md`

## Global Constraints

- A service bound to `127.0.0.1`, `::1`, or a `127.x`/`%lo` address is **not** reachable at the tailnet IP. It needs the SSH forward regardless of whether a tailnet exists. This is the common case, not an edge one — Jupyter binds loopback by default.
- The SSH fallback runs in the **foreground** and ends on Ctrl-C. Nothing is stored in state.
- `state.Record.TunnelPID` is **deleted**, not kept. See Task 4 — a foreground process needs no recorded PID, and the spec's claim that it survives for the fallback path is wrong.
- `connect` prints the URL. It does not shell out to a browser: `cloudlab` is routinely run over SSH, where opening a local browser is meaningless.
- Go tests run via `nix develop --command go test ./...`. Never run bare `go test`.
- Commit style is classic: imperative subject, ≤50 chars, capitalised, no trailing period, no `type:` prefix. Never add an AI co-author trailer.

---

### Task 1: Parse `ss -tlnp` output

**Files:**
- Create: `internal/lifecycle/listeners.go`
- Create: `internal/lifecycle/listeners_test.go`
- Create: `internal/lifecycle/testdata/ss-tlnp.txt`

**Interfaces:**
- Consumes: nothing.
- Produces:

```go
type Listener struct {
	Addr    string // bind address exactly as ss reported it
	Port    int
	Process string // "" when ss could not see it (root-owned, not running as root)
}

func parseListeners(out string) []Listener
func (l Listener) LoopbackOnly() bool
```

- [ ] **Step 1: Create the fixture**

Create `internal/lifecycle/testdata/ss-tlnp.txt` with this captured output
(real output from a provisioned instance — the trailing whitespace and
alignment are as `ss` produced them):

```
State  Recv-Q Send-Q               Local Address:Port  Peer Address:PortProcess
LISTEN 0      4096                 127.0.0.53%lo:53         0.0.0.0:*
LISTEN 0      4096                       0.0.0.0:22         0.0.0.0:*
LISTEN 0      4096                     127.0.0.1:24543      0.0.0.0:*    users:(("moshi-hook",pid=10954,fd=7))
LISTEN 0      4096                 100.81.106.84:35669      0.0.0.0:*
LISTEN 0      4096                    127.0.0.54:53         0.0.0.0:*
LISTEN 0      4096   [fd7a:115c:a1e0::db38:6a55]:52076         [::]:*
LISTEN 0      4096                          [::]:22            [::]:*
```

- [ ] **Step 2: Write the failing test**

Create `internal/lifecycle/listeners_test.go`:

```go
package lifecycle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseListeners_RealSSOutput(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "ss-tlnp.txt"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}

	got := parseListeners(string(raw))

	want := []Listener{
		{Addr: "127.0.0.53%lo", Port: 53},
		{Addr: "0.0.0.0", Port: 22},
		{Addr: "127.0.0.1", Port: 24543, Process: "moshi-hook"},
		{Addr: "100.81.106.84", Port: 35669},
		{Addr: "127.0.0.54", Port: 53},
		{Addr: "fd7a:115c:a1e0::db38:6a55", Port: 52076},
		{Addr: "::", Port: 22},
	}
	if len(got) != len(want) {
		t.Fatalf("parseListeners() returned %d listeners, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("listener %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseListeners_SkipsHeader(t *testing.T) {
	got := parseListeners("State  Recv-Q Send-Q Local Address:Port  Peer Address:Port\n")
	if len(got) != 0 {
		t.Errorf("parseListeners() = %+v, want none — the header is not a listener", got)
	}
}

func TestListener_LoopbackOnly(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.53%lo", true},
		{"::1", true},
		{"0.0.0.0", false},
		{"::", false},
		{"100.81.106.84", false},
	}
	for _, c := range cases {
		if got := (Listener{Addr: c.addr}).LoopbackOnly(); got != c.want {
			t.Errorf("Listener{Addr: %q}.LoopbackOnly() = %v, want %v", c.addr, got, c.want)
		}
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `nix develop --command go test ./internal/lifecycle/ -run 'TestParseListeners|TestListener_Loopback' -v`
Expected: FAIL — `parseListeners` and `Listener` undefined.

- [ ] **Step 4: Write the implementation**

Create `internal/lifecycle/listeners.go`:

```go
package lifecycle

import (
	"regexp"
	"strconv"
	"strings"
)

// Listener is one listening socket reported by `ss -tlnp`.
//
// Addr is kept verbatim rather than normalised because it is the whole
// point: a service on 127.0.0.1 is not reachable at the instance's
// tailnet address, and connect has to be able to tell.
type Listener struct {
	Addr    string
	Port    int
	Process string
}

// loopback matches the addresses that are reachable only from the
// instance itself. 127.0.0.0/8 is matched by prefix rather than parsed:
// ss writes interface-scoped forms like "127.0.0.53%lo" that net.ParseIP
// rejects outright.
func (l Listener) LoopbackOnly() bool {
	return strings.HasPrefix(l.Addr, "127.") || l.Addr == "::1"
}

// processName pulls the program name out of ss's process column,
// users:(("moshi-hook",pid=10954,fd=7)). Absent for sockets owned by
// another user, which is every root-owned service when ss runs
// unprivileged -- so an empty name is normal, not an error.
var processName = regexp.MustCompile(`users:\(\("([^"]+)"`)

// parseListeners turns `ss -tlnp` output into Listeners, skipping the
// header and any line that is not a LISTEN row.
func parseListeners(out string) []Listener {
	var listeners []Listener
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "LISTEN" {
			continue
		}
		// fields[3] is "Local Address:Port". The address may itself
		// contain colons (IPv6), so the port is after the LAST one.
		local := fields[3]
		i := strings.LastIndex(local, ":")
		if i < 0 {
			continue
		}
		port, err := strconv.Atoi(local[i+1:])
		if err != nil {
			continue
		}
		addr := strings.Trim(local[:i], "[]")

		var proc string
		if m := processName.FindStringSubmatch(line); m != nil {
			proc = m[1]
		}
		listeners = append(listeners, Listener{Addr: addr, Port: port, Process: proc})
	}
	return listeners
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `nix develop --command go test ./internal/lifecycle/ -run 'TestParseListeners|TestListener_Loopback' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/lifecycle/listeners.go internal/lifecycle/listeners_test.go internal/lifecycle/testdata/ss-tlnp.txt
git commit -m "Parse listening sockets from ss output"
```

---

### Task 2: Query listeners over SSH

**Files:**
- Modify: `internal/lifecycle/listeners.go`
- Modify: `internal/lifecycle/listeners_test.go`

**Interfaces:**
- Consumes: `parseListeners` from Task 1; `reconcile.Connect(ctx, ip, user) (*reconcile.Client, error)` and `(*Client).Run(cmd string) (string, error)`, both already used by `JoinTailscale`.
- Produces: `func Listeners(ctx context.Context, ip, user string) ([]Listener, error)`.

- [ ] **Step 1: Write the failing test**

Add to `internal/lifecycle/listeners_test.go`. `startFakeSSHServer` and
`startFakeAgent` already exist in this package's test helpers — see
`internal/reconcile/ssh_test.go` for the signature and copy the calling
convention used by an existing lifecycle test that needs a server.

```go
func TestListeners_RunsSSAndParses(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	raw, err := os.ReadFile(filepath.Join("testdata", "ss-tlnp.txt"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}

	var gotCmd string
	addr := startFakeSSHServer(t, func(cmd string, _ []byte) (string, uint32) {
		gotCmd = cmd
		return string(raw), 0
	})

	got, err := Listeners(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Listeners() error = %v", err)
	}
	if !strings.Contains(gotCmd, "ss -tlnp") {
		t.Errorf("Listeners() ran %q, want it to invoke `ss -tlnp`", gotCmd)
	}
	if len(got) != 7 {
		t.Errorf("Listeners() returned %d listeners, want 7", len(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `nix develop --command go test ./internal/lifecycle/ -run TestListeners_Runs -v`
Expected: FAIL — `Listeners` undefined.

- [ ] **Step 3: Write the implementation**

Add to `internal/lifecycle/listeners.go`:

```go
// Listeners asks the instance what is listening.
//
// `ss` rather than `lsof` or `netstat`: it is in iproute2, which is
// present on every Ubuntu image cloudlab boots, and needs no package
// from the template. Run unprivileged, so the process column is empty
// for sockets this user does not own -- the port is what connect
// needs, and the name is a convenience when it happens to be visible.
func Listeners(ctx context.Context, ip, user string) ([]Listener, error) {
	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()

	out, err := client.Run("bash -lc " + reconcile.ShellQuote("ss -tlnp"))
	if err != nil {
		return nil, fmt.Errorf("listing listening sockets: %w\n%s", err, out)
	}
	return parseListeners(out), nil
}
```

Add `context`, `fmt` and the `reconcile` import to the file's import block.

- [ ] **Step 4: Run tests to verify they pass**

Run: `nix develop --command go test ./internal/lifecycle/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lifecycle/listeners.go internal/lifecycle/listeners_test.go
git commit -m "Ask an instance what is listening"
```

---

### Task 3: Resolve an address or forward a port

**Files:**
- Create: `internal/lifecycle/connect.go`
- Create: `internal/lifecycle/connect_test.go`

**Interfaces:**
- Consumes: `Listener` from Task 1; `TailscaleIP(ctx, ip, user) (string, error)` from `internal/lifecycle/tailscale.go:67`.
- Produces:

```go
func ConnectTarget(tailnetIP string, l Listener) (url string, mustForward bool)
func Forward(ctx context.Context, ip, user string, port int) error
```

`ConnectTarget` is split out as a pure function precisely because the
routability rule is the part worth testing exhaustively, and it needs no
network to test.

- [ ] **Step 1: Write the failing test**

Create `internal/lifecycle/connect_test.go`:

```go
package lifecycle

import "testing"

func TestConnectTarget(t *testing.T) {
	cases := []struct {
		name        string
		tailnetIP   string
		listener    Listener
		wantURL     string
		wantForward bool
	}{
		{
			name:        "routable service on a tailnet instance",
			tailnetIP:   "100.81.106.84",
			listener:    Listener{Addr: "0.0.0.0", Port: 8888},
			wantURL:     "http://100.81.106.84:8888",
			wantForward: false,
		},
		{
			name:        "loopback service on a tailnet instance still needs a forward",
			tailnetIP:   "100.81.106.84",
			listener:    Listener{Addr: "127.0.0.1", Port: 8888},
			wantURL:     "http://localhost:8888",
			wantForward: true,
		},
		{
			name:        "no tailnet means forward regardless",
			tailnetIP:   "",
			listener:    Listener{Addr: "0.0.0.0", Port: 3000},
			wantURL:     "http://localhost:3000",
			wantForward: true,
		},
		{
			name:        "ipv6 wildcard is routable",
			tailnetIP:   "100.81.106.84",
			listener:    Listener{Addr: "::", Port: 3000},
			wantURL:     "http://100.81.106.84:3000",
			wantForward: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			url, forward := ConnectTarget(c.tailnetIP, c.listener)
			if url != c.wantURL {
				t.Errorf("url = %q, want %q", url, c.wantURL)
			}
			if forward != c.wantForward {
				t.Errorf("mustForward = %v, want %v", forward, c.wantForward)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `nix develop --command go test ./internal/lifecycle/ -run TestConnectTarget -v`
Expected: FAIL — `ConnectTarget` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/lifecycle/connect.go`:

```go
package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// ConnectTarget decides how to reach a listener and returns the URL to
// use plus whether an SSH forward is required first.
//
// Two conditions force a forward, and the second is the one that is
// easy to miss: no tailnet address at all, or a service bound to
// loopback. A loopback-bound service is not reachable at the instance's
// tailnet address no matter how healthy the tailnet is -- and binding
// to 127.0.0.1 is the default for Jupyter and most dev servers, so this
// is the common path rather than an edge case.
func ConnectTarget(tailnetIP string, l Listener) (string, bool) {
	if tailnetIP == "" || l.LoopbackOnly() {
		return "http://localhost:" + strconv.Itoa(l.Port), true
	}
	return "http://" + tailnetIP + ":" + strconv.Itoa(l.Port), false
}

// Forward runs `ssh -L` in the foreground until the user interrupts it.
//
// Foreground deliberately: a backgrounded tunnel is a process someone
// has to find and kill later, which is what state.Record.TunnelPID
// existed for and why the original design stalled half-built. Ctrl-C
// ends this one, and nothing is written to state.
func Forward(ctx context.Context, ip, user string, port int) error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh not found on PATH: %w", err)
	}
	spec := fmt.Sprintf("%d:localhost:%d", port, port)
	// #nosec G204 -- argv-array exec.Command, no shell. ip is
	// provider-assigned and port is an int, so neither can inject.
	cmd := exec.CommandContext(ctx, "ssh", "-N", "-L", spec, user+"@"+ip)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `nix develop --command go test ./internal/lifecycle/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lifecycle/connect.go internal/lifecycle/connect_test.go
git commit -m "Decide between a tailnet URL and an SSH forward"
```

---

### Task 4: Wire the `connect` command

**Files:**
- Modify: `cmd/lookup.go:107-112` (the stub spec)
- Modify: `cmd/lookup_run.go` (add `runConnect`)
- Modify: `internal/state/state.go:23` (delete `TunnelPID`)
- Test: `cmd/lookup_run_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `Listeners`, `ConnectTarget`, `Forward` from Tasks 2-3; `TailscaleIP` from `tailscale.go:67`; `resolveInstance(name)` and `isInteractive()`, both already in `cmd/`.
- Produces: nothing consumed by later tasks.

**`TunnelPID` is deleted here.** The spec says it "survives, finally used, but
only on the fallback path". That is wrong: `Forward` runs in the foreground
and ends on Ctrl-C, so there is no PID for anyone to record or later kill. The
field has never been read (`grep -rn TunnelPID` finds only its declaration),
and this plan removes it rather than leaving dead weight the spec justified on
a false premise. Removing a field changes the on-disk JSON shape, but
`encoding/json` ignores unknown keys on read, so existing `state.json` files
load fine and simply drop the value on the next write.

- [ ] **Step 1: Write the failing test**

Add to `cmd/lookup_run_test.go`, following the table style of the neighbouring
`pickSession` tests:

```go
func TestPickListener_ValidChoice(t *testing.T) {
	listeners := []lifecycle.Listener{
		{Addr: "0.0.0.0", Port: 8888, Process: "python3.12"},
		{Addr: "127.0.0.1", Port: 3000, Process: "node"},
	}
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("2\n"))
	var out bytes.Buffer
	cmd.SetOut(&out)

	got, err := pickListener(cmd, listeners)
	if err != nil {
		t.Fatalf("pickListener() error = %v", err)
	}
	if got.Port != 3000 {
		t.Errorf("pickListener() = port %d, want 3000", got.Port)
	}
	if !strings.Contains(out.String(), "8888") || !strings.Contains(out.String(), "node") {
		t.Errorf("pickListener() did not list the candidates:\n%s", out.String())
	}
}

func TestPickListener_OutOfRange(t *testing.T) {
	listeners := []lifecycle.Listener{{Addr: "0.0.0.0", Port: 8888}}
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("7\n"))
	cmd.SetOut(&bytes.Buffer{})

	if _, err := pickListener(cmd, listeners); err == nil {
		t.Error("pickListener() error = nil, want an error for a choice outside 1-1")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `nix develop --command go test ./cmd/ -run TestPickListener -v`
Expected: FAIL — `pickListener` undefined.

- [ ] **Step 3: Add the picker**

In `cmd/picker.go`, beside `pickSession`:

```go
// pickListener prints a numbered list of listening ports and reads one
// choice. Same shape as pickSession -- a numbered prompt rather than a
// TUI, testable by writing to a buffer.
func pickListener(cmd *cobra.Command, listeners []lifecycle.Listener) (lifecycle.Listener, error) {
	cmd.Println("Listening on the instance:")
	for i, l := range listeners {
		proc := l.Process
		if proc == "" {
			proc = "-"
		}
		cmd.Printf("  %d) %-6d %-12s %s\n", i+1, l.Port, proc, l.Addr)
	}
	cmd.Print("Which one? ")

	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return lifecycle.Listener{}, fmt.Errorf("reading choice: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(listeners) {
		return lifecycle.Listener{}, fmt.Errorf("%q is not one of 1-%d", strings.TrimSpace(line), len(listeners))
	}
	return listeners[n-1], nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `nix develop --command go test ./cmd/ -run TestPickListener -v`
Expected: PASS.

- [ ] **Step 5: Implement `runConnect`**

Add to `cmd/lookup_run.go`:

```go
// runConnect reaches a service on the instance. With a port it goes
// straight there; without one it asks the instance what is listening.
func runConnect(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}

	// args[0] is the instance name (named: true), so a port comes from
	// the flag rather than a positional -- same reason runSSH takes
	// --dir rather than a second positional.
	port, err := cmd.Flags().GetInt("port")
	if err != nil {
		return err
	}

	// Discovery is best-effort. `ss` comes from iproute2 and is present
	// on every image cloudlab boots, but an unusual base image or a
	// locked-down PATH should not make connect unusable when the caller
	// already knows the port.
	listeners, lerr := lifecycle.Listeners(cmd.Context(), record.IP, record.User)
	if lerr != nil && port == 0 {
		return fmt.Errorf("%w\npass --port to connect without discovery", lerr)
	}

	var chosen lifecycle.Listener
	switch {
	case lerr != nil:
		// No listing to check against, so the bind address is unknown.
		// Forward rather than guess: a forward reaches a service on any
		// address, while a tailnet URL reaches only a routable one.
		chosen = lifecycle.Listener{Addr: "127.0.0.1", Port: port}
	case port != 0:
		for _, l := range listeners {
			if l.Port == port {
				chosen = l
				break
			}
		}
		if chosen.Port == 0 {
			return fmt.Errorf("nothing is listening on port %d — run `cloudlab connect` with no --port to see what is", port)
		}
	case len(listeners) == 0:
		return fmt.Errorf("nothing is listening on %s", name)
	case !isInteractive():
		return fmt.Errorf("several ports are listening; pass --port (no terminal to ask on)")
	default:
		if chosen, err = pickListener(cmd, listeners); err != nil {
			return err
		}
	}

	tailnetIP, err := lifecycle.TailscaleIP(cmd.Context(), record.IP, record.User)
	if err != nil {
		tailnetIP = ""
	}

	url, mustForward := lifecycle.ConnectTarget(tailnetIP, chosen)
	if !mustForward {
		cmd.Println(url)
		return nil
	}
	cmd.Printf("%s (forwarding over SSH — Ctrl-C to stop)\n", url)
	return lifecycle.Forward(cmd.Context(), record.IP, record.User, chosen.Port)
}
```

- [ ] **Step 6: Wire the command spec**

In `cmd/lookup.go`, replace the `connect` entry:

```go
	{
		use:   "connect [name]",
		short: "Reach a service on the instance by port, over the tailnet or an SSH forward",
		verb:  "connect",
		args:  cobra.MaximumNArgs(1),
		named: true,
		flags: func(c *cobra.Command) {
			c.Flags().Int("port", 0, "remote port to reach (omit to choose from what is listening)")
		},
		run: runConnect,
	},
```

- [ ] **Step 7: Delete `TunnelPID`**

In `internal/state/state.go`, delete the `TunnelPID int \`json:"tunnel_pid"\`` field
and amend the `Record` doc comment, which currently says the struct holds
"the PIDs of its background sync/tunnel processes" — it holds neither now.

Run: `nix develop --command grep -rn TunnelPID .` to confirm nothing else
refers to it.

- [ ] **Step 8: Run the full suite**

Run: `nix develop --command bash -c 'go build ./... && go vet ./... && go test ./...'`
Expected: PASS.

- [ ] **Step 9: Update the README**

In `README.md`, the command list still describes `connect` as a Jupyter
tunnel and the intro names it among the unimplemented stubs. Replace with:

```bash
cloudlab connect --port 8888   # reach a service on the instance
```

and remove `connect` from the sentence listing stubs, leaving `shell`.

- [ ] **Step 10: Commit**

```bash
git add cmd/ internal/state/state.go README.md
git commit -m "Reach any service on an instance with connect"
```

---

## Verification

```bash
nix develop --command bash -c 'go build ./... && go vet ./... && go test ./...'
nix develop --command pre-commit run --all-files
```

Against a real instance — the routability rule is the part unit tests cannot
prove:

```bash
cloudlab connect                     # picker lists sshd, moshi-hook, ...
cloudlab connect --port 22           # 0.0.0.0 -> prints http://100.x:22
cloudlab connect --port 24543        # 127.0.0.1 -> forwards, says so
cloudlab connect --port 9999         # nothing there -> refuses clearly
```

The second and third are the ones that matter: same instance, same tailnet,
different answers, decided purely by the bind address.
