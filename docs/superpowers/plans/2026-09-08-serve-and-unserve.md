# Serve And Unserve Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `cloudlab serve` and `cloudlab unserve`, which publish an instance's loopback-bound service on the tailnet and stop publishing it, plus a `Serving:` block in `status`.

**Architecture:** Three thin wrappers around `tailscale serve` run over SSH, following how `internal/lifecycle/listeners.go` wraps `ss`. Argument handling reuses `connect`'s existing discovery, filtering and picker rather than reimplementing them. `connect` itself is not modified.

**Tech Stack:** Go 1.x, `tailscale serve` on the instance, `encoding/json`, cobra.

**Spec:** `docs/superpowers/specs/2026-09-08-serve-and-unserve-design.md`

## Global Constraints

- `connect` must not change. No new flag, no `disconnect`. Persistence is `serve`'s concern.
- **Never** run `tailscale serve reset`. It clears every entry including ones the user made by hand. Per-entry teardown is `tailscale serve --tcp <port> off`, verified to leave other entries intact.
- All three tailscale calls need `sudo` — tailscaled runs as root and cloud-init grants the instance user passwordless sudo.
- Resolve the tailscale binary with `lifecycle.RemoteTailscaleBin(client)`, never a hardcoded path.
- `status` must keep working when the instance is unreachable. Print `Serving:  unknown (instance unreachable)` and return success, matching how a failed live check already renders as `Status:   unknown (live check failed: ...)`.
- Serving an already-routable service (`0.0.0.0`, `::`) creates **no** serve entry — print the URL and say it needs no serving.
- Go tests run via `nix develop --command go test ...`. NEVER bare `go test` — the toolchain comes from a nix flake and is not otherwise on PATH.
- Commit style is classic: imperative subject, **50 characters or fewer**, capitalised, no trailing period, no `type:` prefix. Verify BEFORE committing:
  `bash /Users/subramk/.claude/plugins/cache/jskswamy-plugins/commit-tools/1.0.2/skills/review-commits/lib/style-check.sh "<subject>" /Users/subramk/.claude/plugins/cache/jskswamy-plugins/commit-tools/1.0.2/styles/classic.md`
- NEVER add an AI co-author trailer. Hard project rule.
- Do NOT run `git commit` directly — a hook blocks it. Use `__GIT_COMMIT_PLUGIN__=1 git commit -m "..."`.
- Comments explain *why*, not *what*. Match the density of `internal/lifecycle/listeners.go`.

## Existing API this builds on

```go
// internal/lifecycle
func Listeners(ctx context.Context, ip, user string) ([]Listener, error)
func VisibleListeners(listeners []Listener, all bool) []Listener
func RemoteTailscaleBin(client *reconcile.Client) (string, error)
type Listener struct{ Addr string; Port int; Process string }
func (l Listener) LoopbackOnly() bool
func (l Listener) Infrastructure() bool

// internal/reconcile
func Connect(ctx context.Context, ip, user string) (*Client, error)
func (c *Client) Run(cmd string) (string, error)
func ShellQuote(s string) string

// cmd
func chooseListener(listeners []lifecycle.Listener, port int, discoveryFailed, interactive bool) (lifecycle.Listener, error)
func pickListener(cmd *cobra.Command, listeners []lifecycle.Listener) (lifecycle.Listener, error)
func readIndex(cmd *cobra.Command, max int) (int, error)
func isInteractive() bool
func resolveInstance(name string) (*state.Store, state.Record, error)
var errDiscoveryNoPort, errNoListeners, errAskUser error
```

`internal/lifecycle/tailscale.go` (`JoinTailscale`) is the reference for the connect → `RemoteTailscaleBin` → `sudo <bin> ...` sequence.

---

### Task 1: Parse and build the tailscale serve commands

**Files:**
- Create: `internal/lifecycle/serve.go`
- Create: `internal/lifecycle/serve_test.go`
- Create: `internal/lifecycle/testdata/serve-status-empty.json`
- Create: `internal/lifecycle/testdata/serve-status-tcp.json`

**Interfaces:**
- Consumes: nothing.
- Produces:

```go
type ServeEntry struct {
    Port    int
    Forward string // "localhost:9876", as tailscale reports it
}
func parseServeStatus(jsonOut string) ([]ServeEntry, error)
func serveArgs(bin string, port int) string
func unserveArgs(bin string, port int) string
```

`serveArgs`/`unserveArgs` return a full shell command string (not argv) because these run through `client.Run`, which takes one string — same as `listeners.go`'s `ss` call.

- [ ] **Step 1: Create the fixtures**

`internal/lifecycle/testdata/serve-status-empty.json` — real output when nothing is served:

```json
{}
```

`internal/lifecycle/testdata/serve-status-tcp.json` — real output with one TCP entry, captured from a provisioned instance:

```json
{
  "TCP": {
    "9876": {
      "TCPForward": "localhost:9876"
    }
  }
}
```

- [ ] **Step 2: Write the failing tests**

Create `internal/lifecycle/serve_test.go`:

```go
package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseServeStatus_Empty(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "serve-status-empty.json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	got, err := parseServeStatus(string(raw))
	if err != nil {
		t.Fatalf("parseServeStatus() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("parseServeStatus(%q) = %+v, want none", "{}", got)
	}
}

func TestParseServeStatus_OneTCPEntry(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "serve-status-tcp.json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	got, err := parseServeStatus(string(raw))
	if err != nil {
		t.Fatalf("parseServeStatus() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("parseServeStatus() returned %d entries, want 1: %+v", len(got), got)
	}
	if got[0].Port != 9876 {
		t.Errorf("Port = %d, want 9876", got[0].Port)
	}
	if got[0].Forward != "localhost:9876" {
		t.Errorf("Forward = %q, want %q", got[0].Forward, "localhost:9876")
	}
}

func TestParseServeStatus_SortedByPort(t *testing.T) {
	// Map iteration order is random in Go, so without an explicit sort
	// the picker would renumber its choices between runs.
	in := `{"TCP":{"9877":{"TCPForward":"localhost:9877"},"9876":{"TCPForward":"localhost:9876"}}}`
	got, err := parseServeStatus(in)
	if err != nil {
		t.Fatalf("parseServeStatus() error = %v", err)
	}
	if len(got) != 2 || got[0].Port != 9876 || got[1].Port != 9877 {
		t.Errorf("parseServeStatus() = %+v, want ports ascending [9876 9877]", got)
	}
}

func TestParseServeStatus_Garbage(t *testing.T) {
	if _, err := parseServeStatus("not json"); err == nil {
		t.Error("parseServeStatus(\"not json\") error = nil, want an error")
	}
}

func TestServeArgs(t *testing.T) {
	got := serveArgs("/usr/bin/tailscale", 8888)
	for _, want := range []string{"sudo", "/usr/bin/tailscale", "serve", "--bg", "--tcp 8888", "tcp://localhost:8888"} {
		if !strings.Contains(got, want) {
			t.Errorf("serveArgs() = %q, missing %q", got, want)
		}
	}
}

func TestUnserveArgs(t *testing.T) {
	got := unserveArgs("/usr/bin/tailscale", 8888)
	if !strings.Contains(got, "--tcp 8888 off") {
		t.Errorf("unserveArgs() = %q, want it to end the entry with `--tcp 8888 off`", got)
	}
	// reset clears entries the user created by hand; it must never appear.
	if strings.Contains(got, "reset") {
		t.Errorf("unserveArgs() = %q, must not use `serve reset`", got)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `nix develop --command go test ./internal/lifecycle/ -run 'TestParseServeStatus|TestServeArgs|TestUnserveArgs' -v`
Expected: FAIL — `parseServeStatus`, `serveArgs`, `unserveArgs` undefined.

- [ ] **Step 4: Write the implementation**

Create `internal/lifecycle/serve.go`:

```go
package lifecycle

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// ServeEntry is one port published on the tailnet by `tailscale serve`.
type ServeEntry struct {
	Port    int
	Forward string
}

// serveStatusJSON is the shape `tailscale serve status --json` returns.
// Only the TCP section is read: cloudlab publishes with --tcp, and the
// HTTP/HTTPS sections describe a different kind of entry it does not
// create. An empty config is "{}", so every field must tolerate absence.
type serveStatusJSON struct {
	TCP map[string]struct {
		TCPForward string
	}
}

// parseServeStatus turns that JSON into entries, sorted by port.
//
// Sorted because Go randomises map iteration: unsorted, the numbered
// picker would list the same entries in a different order on each run,
// so choice 1 would not mean the same thing twice.
func parseServeStatus(jsonOut string) ([]ServeEntry, error) {
	var raw serveStatusJSON
	if err := json.Unmarshal([]byte(jsonOut), &raw); err != nil {
		return nil, fmt.Errorf("parsing `tailscale serve status --json`: %w", err)
	}
	var entries []ServeEntry
	for portStr, v := range raw.TCP {
		port, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}
		entries = append(entries, ServeEntry{Port: port, Forward: v.TCPForward})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Port < entries[j].Port })
	return entries, nil
}

// serveArgs publishes the instance's own localhost:port on the tailnet.
//
// --bg because the point is to outlive the command; without it tailscale
// holds the terminal, which is what the ssh forward already does and
// what this exists to avoid.
func serveArgs(bin string, port int) string {
	return fmt.Sprintf("sudo %s serve --bg --tcp %d tcp://localhost:%d", bin, port, port)
}

// unserveArgs removes exactly one entry.
//
// Per-entry rather than `serve reset`: reset clears every entry on the
// instance, including any the user set up by hand, and `serve status`
// does not record which ones cloudlab added.
func unserveArgs(bin string, port int) string {
	return fmt.Sprintf("sudo %s serve --tcp %d off", bin, port)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `nix develop --command go test ./internal/lifecycle/ -run 'TestParseServeStatus|TestServeArgs|TestUnserveArgs' -v`
Expected: PASS, all six.

- [ ] **Step 6: Commit**

```bash
git add internal/lifecycle/serve.go internal/lifecycle/serve_test.go internal/lifecycle/testdata/
__GIT_COMMIT_PLUGIN__=1 git commit -m "Parse and build the tailscale serve commands"
```

---

### Task 2: Run serve, unserve and status over SSH

**Files:**
- Modify: `internal/lifecycle/serve.go`
- Modify: `internal/lifecycle/serve_test.go`

**Interfaces:**
- Consumes: `parseServeStatus`, `serveArgs`, `unserveArgs` from Task 1.
- Produces:

```go
func Serve(ctx context.Context, ip, user string, port int) error
func Unserve(ctx context.Context, ip, user string, port int) error
func ServeStatus(ctx context.Context, ip, user string) ([]ServeEntry, error)
```

The package's fake SSH helpers are `startFakeAgent(t)` and `startFakeSSHServer(t, handler)`, both defined in **`internal/lifecycle/ready_test.go`** (lines 27 and 75). `internal/lifecycle/tailscale_test.go` shows the calling convention against a `RemoteTailscaleBin`-using function — read it first, since these three functions have the same shape.

- [ ] **Step 1: Write the failing tests**

Append to `internal/lifecycle/serve_test.go`. Note the handler must answer BOTH the `command -v tailscale` probe and the serve command, because `RemoteTailscaleBin` runs first:

```go
func TestServe_RunsTailscaleServe(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var got []string
	addr := startFakeSSHServer(t, func(cmd string, _ []byte) (string, uint32) {
		got = append(got, cmd)
		if strings.Contains(cmd, "command -v tailscale") {
			return "/usr/bin/tailscale\n", 0
		}
		return "", 0
	})

	if err := Serve(context.Background(), addr, "devuser", 8888); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "serve --bg --tcp 8888 tcp://localhost:8888") {
		t.Errorf("Serve() ran %q, want the --bg --tcp publish command", joined)
	}
}

func TestUnserve_TurnsOffOneEntry(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var got []string
	addr := startFakeSSHServer(t, func(cmd string, _ []byte) (string, uint32) {
		got = append(got, cmd)
		if strings.Contains(cmd, "command -v tailscale") {
			return "/usr/bin/tailscale\n", 0
		}
		return "", 0
	})

	if err := Unserve(context.Background(), addr, "devuser", 8888); err != nil {
		t.Fatalf("Unserve() error = %v", err)
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "--tcp 8888 off") {
		t.Errorf("Unserve() ran %q, want the per-entry off command", joined)
	}
	if strings.Contains(joined, "serve reset") {
		t.Errorf("Unserve() ran %q, which would clear entries cloudlab did not create", joined)
	}
}

func TestServeStatus_ParsesRemoteJSON(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	raw, err := os.ReadFile(filepath.Join("testdata", "serve-status-tcp.json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	addr := startFakeSSHServer(t, func(cmd string, _ []byte) (string, uint32) {
		if strings.Contains(cmd, "command -v tailscale") {
			return "/usr/bin/tailscale\n", 0
		}
		return string(raw), 0
	})

	entries, err := ServeStatus(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("ServeStatus() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Port != 9876 {
		t.Errorf("ServeStatus() = %+v, want one entry on 9876", entries)
	}
}
```

Add `"context"` to the test file's imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `nix develop --command go test ./internal/lifecycle/ -run 'TestServe_|TestUnserve_|TestServeStatus_' -v`
Expected: FAIL — `Serve`, `Unserve`, `ServeStatus` undefined.

- [ ] **Step 3: Write the implementation**

Append to `internal/lifecycle/serve.go`, and add `"context"` plus the `reconcile` import:

```go
// serveSession connects, resolves the tailscale binary, and hands both
// to fn. All three exported calls need exactly this preamble, and
// resolving the binary rather than assuming a path is what makes them
// work on an instance where tailscale came from nix.
func serveSession(ctx context.Context, ip, user string, fn func(client *reconcile.Client, bin string) error) error {
	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	bin, err := RemoteTailscaleBin(client)
	if err != nil {
		return err
	}
	return fn(client, bin)
}

// Serve publishes the instance's localhost:port on the tailnet.
//
// sudo: tailscaled runs as root and gates serve config changes on it;
// cloud-init grants this user passwordless sudo (see cloud-init.sh).
func Serve(ctx context.Context, ip, user string, port int) error {
	return serveSession(ctx, ip, user, func(client *reconcile.Client, bin string) error {
		if out, err := client.Run("bash -lc " + reconcile.ShellQuote(serveArgs(bin, port))); err != nil {
			return fmt.Errorf("serving port %d on the tailnet: %w\n%s", port, err, out)
		}
		return nil
	})
}

// Unserve stops publishing one port, leaving every other entry alone.
func Unserve(ctx context.Context, ip, user string, port int) error {
	return serveSession(ctx, ip, user, func(client *reconcile.Client, bin string) error {
		if out, err := client.Run("bash -lc " + reconcile.ShellQuote(unserveArgs(bin, port))); err != nil {
			return fmt.Errorf("stopping serve on port %d: %w\n%s", port, err, out)
		}
		return nil
	})
}

// ServeStatus reports every port published on the instance, including
// entries cloudlab did not create -- tailscale does not record who added
// one, and showing only a subset would make `unserve` look broken.
func ServeStatus(ctx context.Context, ip, user string) ([]ServeEntry, error) {
	var entries []ServeEntry
	err := serveSession(ctx, ip, user, func(client *reconcile.Client, bin string) error {
		out, err := client.Run("bash -lc " + reconcile.ShellQuote("sudo "+bin+" serve status --json"))
		if err != nil {
			return fmt.Errorf("reading serve status: %w\n%s", err, out)
		}
		parsed, err := parseServeStatus(out)
		if err != nil {
			return err
		}
		entries = parsed
		return nil
	})
	return entries, err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `nix develop --command go test ./internal/lifecycle/ -v 2>&1 | tail -20`
Expected: PASS. The package suite takes ~1 minute.

- [ ] **Step 5: Commit**

```bash
git add internal/lifecycle/serve.go internal/lifecycle/serve_test.go
__GIT_COMMIT_PLUGIN__=1 git commit -m "Run serve and unserve over SSH"
```

---

### Task 3: Wire the serve command

**Files:**
- Modify: `cmd/lookup.go` (the `lookupCommandSpecs` slice)
- Modify: `cmd/lookup_run.go`
- Test: `cmd/lookup_run_test.go`

**Interfaces:**
- Consumes: `lifecycle.Serve` from Task 2; `chooseListener`, `pickListener`, `isInteractive`, `resolveInstance`, and the `errDiscoveryNoPort`/`errNoListeners`/`errAskUser` sentinels, all already in `cmd/`.
- Produces: `runServe`, and `serveTarget` for Task 5's tests.

- [ ] **Step 1: Write the failing test**

Add to `cmd/lookup_run_test.go`:

```go
func TestServeTarget_RoutableNeedsNoEntry(t *testing.T) {
	// Already reachable at the tailnet address, so a serve entry would
	// be clutter someone has to clean up later.
	url, needsEntry := serveTarget("100.81.106.84", lifecycle.Listener{Addr: "0.0.0.0", Port: 3000})
	if needsEntry {
		t.Error("needsEntry = true, want false for a routable service")
	}
	if url != "http://100.81.106.84:3000" {
		t.Errorf("url = %q, want the tailnet URL", url)
	}
}

func TestServeTarget_LoopbackNeedsAnEntry(t *testing.T) {
	url, needsEntry := serveTarget("100.81.106.84", lifecycle.Listener{Addr: "127.0.0.1", Port: 8888})
	if !needsEntry {
		t.Error("needsEntry = false, want true — loopback is not reachable on the tailnet")
	}
	if url != "http://100.81.106.84:8888" {
		t.Errorf("url = %q, want the tailnet URL it will be reachable at once served", url)
	}
}

func TestServeTarget_NonHTTPPortGetsNoScheme(t *testing.T) {
	url, _ := serveTarget("100.81.106.84", lifecycle.Listener{Addr: "127.0.0.1", Port: 22})
	if url != "100.81.106.84:22" {
		t.Errorf("url = %q, want no http:// on a port that does not speak HTTP", url)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `nix develop --command go test ./cmd/ -run TestServeTarget -v`
Expected: FAIL — `serveTarget` undefined.

- [ ] **Step 3: Implement `serveTarget` and `runServe`**

Add to `cmd/lookup_run.go`:

```go
// serveTarget returns the tailnet URL a listener will answer at, and
// whether publishing it requires a serve entry at all.
//
// Split out as a pure function for the same reason ConnectTarget was:
// the routable-versus-loopback rule is the part worth testing, and it
// needs no network to test.
func serveTarget(tailnetIP string, l lifecycle.Listener) (string, bool) {
	scheme := ""
	if l.HTTP() {
		scheme = "http://"
	}
	return scheme + tailnetIP + ":" + strconv.Itoa(l.Port), l.LoopbackOnly()
}

// runServe publishes a service on the tailnet, where it outlives this
// command. Discovery is identical to connect's: the two commands differ
// in how long the result lasts, not in how you name what you want.
func runServe(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	// Checked before any SSH work: serving is meaningless without a
	// tailnet, and this says so instead of surfacing a tailscale error.
	if !record.TailscaleJoined {
		return fmt.Errorf("%s is not on a tailnet — serving publishes there, so run `cloudlab tailscale` first", name)
	}

	port := 0
	if len(args) > 0 {
		if port, err = strconv.Atoi(args[0]); err != nil {
			return fmt.Errorf("%q is not a port number", args[0])
		}
	}
	all, err := cmd.Flags().GetBool("all")
	if err != nil {
		return err
	}

	listeners, lerr := lifecycle.Listeners(cmd.Context(), record.IP, record.User)
	if lerr != nil {
		return lerr
	}
	offered := lifecycle.VisibleListeners(listeners, all || port != 0)

	chosen, err := chooseListener(offered, port, false, isInteractive())
	switch {
	case errors.Is(err, errNoListeners):
		if hidden := len(listeners) - len(offered); hidden > 0 {
			return fmt.Errorf("nothing of yours is listening on %s (%d infrastructure socket(s) hidden — pass --all to see them)", name, hidden)
		}
		return fmt.Errorf("nothing is listening on %s", name)
	case errors.Is(err, errAskUser):
		if chosen, err = pickListener(cmd, offered); err != nil {
			return err
		}
	case err != nil:
		return err
	}

	tailnetIP, err := lifecycle.TailscaleIP(cmd.Context(), record.IP, record.User)
	if err != nil || tailnetIP == "" {
		return fmt.Errorf("could not resolve %s's tailnet address — is tailscaled running there?", name)
	}

	url, needsEntry := serveTarget(tailnetIP, chosen)
	if !needsEntry {
		cmd.Printf("%s is already reachable on the tailnet, no serving needed\n", url)
		return nil
	}
	if err := lifecycle.Serve(cmd.Context(), record.IP, record.User, chosen.Port); err != nil {
		return err
	}
	cmd.Printf("Serving 127.0.0.1:%d on your tailnet\n", chosen.Port)
	cmd.Printf("  %s\n", url)
	cmd.Printf("Stop with: cloudlab unserve %d\n", chosen.Port)
	return nil
}
```

`strconv` is already imported by `cmd/lookup_run.go`; confirm before adding it.

- [ ] **Step 4: Register the command**

In `cmd/lookup.go`, add to `lookupCommandSpecs` immediately after the `connect` entry:

```go
	{
		use:   "serve [port]",
		short: "Publish a service on your tailnet, where it outlives this command",
		verb:  "serve",
		args:  cobra.MaximumNArgs(1),
		named: false,
		flags: func(c *cobra.Command) {
			c.Flags().Bool("all", false, "include the instance's own sockets (sshd, resolved, tailscaled) in the choices")
		},
		run: runServe,
	},
```

`named: false` because the positional is a port, not an instance name — the same reason `tmux` and `sync` set it false. The instance comes from `--name` or the cwd.

- [ ] **Step 5: Run tests to verify they pass**

Run: `nix develop --command go test ./cmd/ -run TestServeTarget -v`
Then: `nix develop --command bash -c 'go build ./... && go vet ./...'`
Expected: PASS and a clean build.

- [ ] **Step 6: Commit**

```bash
git add cmd/lookup.go cmd/lookup_run.go cmd/lookup_run_test.go
__GIT_COMMIT_PLUGIN__=1 git commit -m "Publish a service with cloudlab serve"
```

---

### Task 4: Wire the unserve command

**Files:**
- Modify: `cmd/lookup.go`
- Modify: `cmd/lookup_run.go`
- Modify: `cmd/picker.go`
- Test: `cmd/lookup_run_test.go`

**Interfaces:**
- Consumes: `lifecycle.ServeStatus`, `lifecycle.Unserve`, `lifecycle.ServeEntry` from Task 2; `readIndex` from `cmd/picker.go`.
- Produces: `chooseServeEntry`, `pickServeEntry`, `runUnserve`.

- [ ] **Step 1: Write the failing test**

Add to `cmd/lookup_run_test.go`:

```go
func TestChooseServeEntry(t *testing.T) {
	two := []lifecycle.ServeEntry{{Port: 8888}, {Port: 3000}}
	one := []lifecycle.ServeEntry{{Port: 8888}}

	cases := []struct {
		name        string
		entries     []lifecycle.ServeEntry
		port        int
		interactive bool
		wantPort    int
		wantErr     error
		wantErrMsg  string
	}{
		{name: "nothing served", entries: nil, wantErr: errNoServed},
		{name: "sole entry needs no choosing", entries: one, wantPort: 8888},
		{name: "explicit port matches", entries: two, port: 3000, wantPort: 3000},
		{name: "explicit port not served", entries: two, port: 9999,
			wantErrMsg: "port 9999 is not being served"},
		{name: "several with a terminal", entries: two, interactive: true, wantErr: errAskUser},
		{name: "several without a terminal", entries: two,
			wantErrMsg: "several ports are being served; name one (no terminal to ask on)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := chooseServeEntry(c.entries, c.port, c.interactive)
			switch {
			case c.wantErr != nil:
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("error = %v, want %v", err, c.wantErr)
				}
			case c.wantErrMsg != "":
				if err == nil || !strings.Contains(err.Error(), c.wantErrMsg) {
					t.Fatalf("error = %v, want it to contain %q", err, c.wantErrMsg)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error = %v", err)
				}
				if got.Port != c.wantPort {
					t.Errorf("port = %d, want %d", got.Port, c.wantPort)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `nix develop --command go test ./cmd/ -run TestChooseServeEntry -v`
Expected: FAIL — `chooseServeEntry` and `errNoServed` undefined.

- [ ] **Step 3: Implement the chooser and picker**

Add the sentinel beside the existing ones in `cmd/lookup_run.go`:

```go
	errNoServed = errors.New("nothing is being served")
```

Add `chooseServeEntry` next to `chooseListener`:

```go
// chooseServeEntry is chooseListener's sibling for ports that are
// already published. Same branch structure, different candidates:
// unserve chooses from what is served, not from what is listening.
func chooseServeEntry(entries []lifecycle.ServeEntry, port int, interactive bool) (lifecycle.ServeEntry, error) {
	switch {
	case len(entries) == 0:
		return lifecycle.ServeEntry{}, errNoServed
	case port != 0:
		for _, e := range entries {
			if e.Port == port {
				return e, nil
			}
		}
		return lifecycle.ServeEntry{}, fmt.Errorf("port %d is not being served — run `cloudlab unserve` with no port to see what is", port)
	case len(entries) == 1:
		return entries[0], nil
	case !interactive:
		return lifecycle.ServeEntry{}, fmt.Errorf("several ports are being served; name one (no terminal to ask on)")
	default:
		return lifecycle.ServeEntry{}, errAskUser
	}
}
```

Add `pickServeEntry` to `cmd/picker.go`, beside `pickListener`:

```go
// pickServeEntry prints the served ports and reads one choice. Shares
// readIndex with the other pickers; only the rendering differs.
func pickServeEntry(cmd *cobra.Command, entries []lifecycle.ServeEntry) (lifecycle.ServeEntry, error) {
	cmd.Println("Serving on the instance:")
	for i, e := range entries {
		cmd.Printf("  %d) %-6d %s\n", i+1, e.Port, e.Forward)
	}
	cmd.Print("Which one? ")

	n, err := readIndex(cmd, len(entries))
	if err != nil {
		return lifecycle.ServeEntry{}, err
	}
	return entries[n-1], nil
}
```

- [ ] **Step 4: Implement `runUnserve`**

Add to `cmd/lookup_run.go`:

```go
// runUnserve stops publishing a port. It can only stop SERVED entries —
// a running `connect` forward is a foreground process in another
// terminal with no PID recorded, so the empty-state message says so
// rather than leaving someone to wonder why unserve did nothing.
func runUnserve(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}

	port := 0
	if len(args) > 0 {
		if port, err = strconv.Atoi(args[0]); err != nil {
			return fmt.Errorf("%q is not a port number", args[0])
		}
	}

	entries, err := lifecycle.ServeStatus(cmd.Context(), record.IP, record.User)
	if err != nil {
		return err
	}

	chosen, err := chooseServeEntry(entries, port, isInteractive())
	switch {
	case errors.Is(err, errNoServed):
		return fmt.Errorf("nothing is being served on %s — a running `cloudlab connect` forward stops with Ctrl-C in its own terminal", name)
	case errors.Is(err, errAskUser):
		if chosen, err = pickServeEntry(cmd, entries); err != nil {
			return err
		}
	case err != nil:
		return err
	}

	if err := lifecycle.Unserve(cmd.Context(), record.IP, record.User, chosen.Port); err != nil {
		return err
	}
	cmd.Printf("Stopped serving %d\n", chosen.Port)
	return nil
}
```

- [ ] **Step 5: Register the command**

In `cmd/lookup.go`, immediately after the `serve` entry:

```go
	{
		use:   "unserve [port]",
		short: "Stop publishing a service on your tailnet",
		verb:  "unserve",
		args:  cobra.MaximumNArgs(1),
		named: false,
		run:   runUnserve,
	},
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `nix develop --command go test ./cmd/ -v 2>&1 | tail -15`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/lookup.go cmd/lookup_run.go cmd/picker.go cmd/lookup_run_test.go
__GIT_COMMIT_PLUGIN__=1 git commit -m "Stop publishing with cloudlab unserve"
```

---

### Task 5: Show served ports in status, and document all three

**Files:**
- Modify: `cmd/lookup_run.go` (`runStatus` around line 125, and a new `printServing`)
- Test: `cmd/lookup_run_test.go`
- Modify: `README.md`, `docs/architecture.md`

**Interfaces:**
- Consumes: `lifecycle.ServeStatus`, `lifecycle.ServeEntry`.
- Produces: nothing later tasks rely on.

This is the one task that changes a command which already works, so the unreachable case is the point rather than an afterthought.

- [ ] **Step 1: Write the failing test**

Add to `cmd/lookup_run_test.go`:

```go
func TestPrintServing_ListsEntries(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	printServing(cmd, []lifecycle.ServeEntry{{Port: 8888, Forward: "localhost:8888"}}, "100.81.106.84", nil)

	got := out.String()
	if !strings.Contains(got, "Serving:") || !strings.Contains(got, "8888") {
		t.Errorf("printServing() = %q, want a Serving block naming 8888", got)
	}
}

func TestPrintServing_UnreachableDoesNotFail(t *testing.T) {
	// status must keep working when the instance is down; it already
	// renders a failed live check as "unknown" rather than erroring.
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	printServing(cmd, nil, "", errors.New("dial tcp: connection refused"))

	got := out.String()
	if !strings.Contains(got, "unknown") {
		t.Errorf("printServing() = %q, want it to report unknown rather than the raw error", got)
	}
}

func TestPrintServing_NoneServed(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	printServing(cmd, nil, "100.81.106.84", nil)

	if got := out.String(); !strings.Contains(got, "none") {
		t.Errorf("printServing() = %q, want it to say none", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `nix develop --command go test ./cmd/ -run TestPrintServing -v`
Expected: FAIL — `printServing` undefined.

- [ ] **Step 3: Implement `printServing`**

Add to `cmd/lookup_run.go`, beside `printSessions`:

```go
// printServing renders the instance's published ports. Separate from
// runStatus for the same reason printSessions is: it can then be tested
// without a provider or an instance.
//
// An error is reported as unknown rather than returned. status is a
// read-only report and an unreachable instance is an expected state for
// it -- the live provider check above already renders that way, and a
// serve lookup must not be the thing that makes status fail.
func printServing(cmd *cobra.Command, entries []lifecycle.ServeEntry, tailnetIP string, err error) {
	if err != nil {
		cmd.Printf("Serving:  unknown (instance unreachable)\n")
		return
	}
	if len(entries) == 0 {
		cmd.Printf("Serving:  none\n")
		return
	}
	cmd.Printf("Serving:\n")
	for _, e := range entries {
		cmd.Printf("  %-6d %s\n", e.Port, tailnetIP+":"+strconv.Itoa(e.Port))
	}
}
```

- [ ] **Step 4: Call it from `runStatus`**

In `runStatus`, replace the `printSessions(cmd, record)` line with:

```go
	printSessions(cmd, record)

	// Only when state says there is a tailnet: serving is tailnet-only,
	// so an instance that never joined has nothing to report and should
	// not pay an SSH round trip to learn that.
	if record.TailscaleJoined {
		entries, serveErr := lifecycle.ServeStatus(cmd.Context(), record.IP, record.User)
		tailnetIP, _ := lifecycle.TailscaleIP(cmd.Context(), record.IP, record.User)
		printServing(cmd, entries, tailnetIP, serveErr)
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `nix develop --command go test ./cmd/ -v 2>&1 | tail -12`
Expected: PASS.

- [ ] **Step 6: Update the docs**

In `README.md`, add to the command list beside `cloudlab connect`:

```bash
cloudlab serve 8888            # publish it on your tailnet; outlives the command
cloudlab unserve 8888          # stop publishing it
```

In `docs/architecture.md`, add two rows to the command-surface table, and state the distinction plainly: `connect` lasts as long as the command and is reachable only from the machine that ran it; `serve` lasts until `unserve` and is reachable from the whole tailnet. Note that `serve` never uses `tailscale serve reset`, because that would clear entries the user created by hand.

- [ ] **Step 7: Run the full suite**

Run: `nix develop --command bash -c 'go build ./... && go vet ./... && go test ./...'`
Then: `nix develop --command pre-commit run --all-files`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add cmd/lookup_run.go cmd/lookup_run_test.go README.md docs/architecture.md
__GIT_COMMIT_PLUGIN__=1 git commit -m "Show served ports in status"
```

---

## Verification

```bash
nix develop --command bash -c 'go build ./... && go vet ./... && go test ./...'
nix develop --command pre-commit run --all-files
```

Then against a real instance, which is the only thing that exercises tailscale:

```bash
# on the instance, something bound to loopback:
ssh <instance> 'nohup python3 -m http.server 9876 --bind 127.0.0.1 >/dev/null 2>&1 &'

cloudlab serve 9876          # publishes, prints the tailnet URL, exits
curl http://<tailnet-ip>:9876/   # reachable with NO forward running
cloudlab status              # Serving: lists 9876
cloudlab unserve 9876        # stops it
cloudlab unserve             # "nothing is being served ... Ctrl-C in its own terminal"
```

The teardown check that matters, because getting it wrong destroys user config:

```bash
ssh <instance> 'sudo $(command -v tailscale) serve --bg --tcp 9999 tcp://localhost:9999'
cloudlab serve 9876
cloudlab unserve 9876
ssh <instance> 'sudo $(command -v tailscale) serve status'   # 9999 must survive
ssh <instance> 'sudo $(command -v tailscale) serve --tcp 9999 off'
```
