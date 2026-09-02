# `up` Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `internal/lifecycle` (VM readiness wait, rsync, Mutagen watch, and the `Up` orchestration tying them together with the already-built provider/config/reconcile/state packages) and wire it into a real, fully working `up` command.

**Architecture:** A new `internal/lifecycle` package holds four small, independently testable pieces (`WaitReady`, `Rsync`, `StartWatch`, `Up`). `Up` takes its four post-creation steps as an injectable `Steps` struct so its own tests can substitute recording fakes for the two steps (`Rsync`, `StartWatch`) that would otherwise need a real remote target, while still exercising `WaitReady`/`Reconcile` for real against an in-process fake SSH server. `cmd/up.go` stays thin: resolve identity, read `DIGITALOCEAN_TOKEN`, construct the provider, call `lifecycle.Up`.

**Tech Stack:** Go 1.26 stdlib (`os/exec`, `context`, `time`) plus two new external binaries this project now depends on: `rsync` and `mutagen` (both added to the Nix devShell), invoked via `os/exec` the same way `nix`/`pkl` already are. No new Go dependencies.

## Global Constraints

- VM user is hardcoded `root` (matches every earlier phase's Global Constraints).
- Readiness: retry `reconcile.Connect` until it succeeds, then run `cloud-init status --wait` over that connection; a non-zero exit is a genuine failure, never retried. Overall timeout: 5 minutes (`readyTimeout` in `internal/lifecycle`).
- Mutagen watch session name = instance name (`--name=<instance>`), no PID tracking — Mutagen manages its own daemon/session model.
- `rsync`/`mutagen` are required on `PATH`, checked via `exec.LookPath` with a clear install-hint error, matching the existing `pkl`/`nix` pattern. Neither is optional for this phase.
- New `cloudlab.pkl` field: `image: String = "ubuntu-24-04-x64"`.
- State (`state.Record`) is written immediately after `provider.Create` succeeds, before any later step — a later failure must still leave the VM destroyable via `down`.
- No idempotent re-run handling: re-running `up` against a name whose VM/watch-session already exists (without `down` first) is deliberately unguarded.
- `shell`'s remaining flow and live progress streaming are explicitly out of scope for this plan.

---

### Task 1: `image` field in `cloudlab.pkl`

**Files:**
- Modify: `internal/config/Config.pkl`
- Modify: `internal/config/Config.pkl.go` (regenerated)
- Modify: `internal/config/config.go:109-119` (`mergeConfig`)
- Modify: `internal/config/config_test.go`
- Modify: `docs/config.md`

**Interfaces:**
- Consumes: nothing new.
- Produces: `config.Config.Image string` (schema-defaulted, like `Arch`) — consumed by Task 5 (`lifecycle.Up`, via `provider.InstanceSpec.Image`).

- [ ] **Step 1: Write the failing tests**

Add to `internal/config/config_test.go` (place near the existing `TestLoad_Arch*` tests):

```go
func TestLoad_ImageDefaultsToUbuntu2404(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, project, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
	}, "\n")+"\n")

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Image != "ubuntu-24-04-x64" {
		t.Errorf("Image = %q, want %q", cfg.Image, "ubuntu-24-04-x64")
	}
}

func TestLoad_ImageOverride(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, project, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`image = "ubuntu-22-04-x64"`,
	}, "\n")+"\n")

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Image != "ubuntu-22-04-x64" {
		t.Errorf("Image = %q, want %q", cfg.Image, "ubuntu-22-04-x64")
	}
}

func TestLoad_ImageSurvivesBaseMerge(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.pkl")
	writeFixture(t, base, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
	}, "\n")+"\n")

	project := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, project, strings.Join([]string{
		`basePath = "./base.pkl"`,
		`template = "python"`,
		`image = "ubuntu-22-04-x64"`,
	}, "\n")+"\n")

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Image != "ubuntu-22-04-x64" {
		t.Errorf("Image = %q, want %q (project overrides base)", cfg.Image, "ubuntu-22-04-x64")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/... -run TestLoad_Image -v`
Expected: FAIL — `cfg.Image` doesn't compile (`Config` has no field `Image`).

- [ ] **Step 3: Extend the schema**

In `internal/config/Config.pkl`, add one line right after `arch`:

```pkl
arch: "x86_64"|"arm64" = "x86_64"
image: String = "ubuntu-24-04-x64"
sshKeys: Listing<String>?
```

- [ ] **Step 4: Regenerate the Go bindings**

Run (from repo root, inside `nix develop`):

```bash
go generate ./internal/config/...
```

Expected: `internal/config/Config.pkl.go` gains an `Image string` field (`pkl:"image"`), same non-pointer shape as `Arch`.

- [ ] **Step 5: Wire `Image` into `mergeConfig`**

`internal/config/config.go`'s `mergeConfig` (around line 109) must assign `Image` the same way it assigns `Arch` — a plain pass-through, no base-fallback (per the same reasoning as `Arch`: no meaningful "personal base default" for a per-project setting, and there's no pointer type here to distinguish "unset"). Find:

```go
		Arch:     project.Arch,
```

Add right after it:

```go
		Image:    project.Image,
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/config/... -v`
Expected: PASS — every test in the package, including the 3 new ones (aside from `TestLoadFromPath_MinimalFixture`, the known, expected, machine-specific exception documented throughout this project's history).

- [ ] **Step 7: Update `docs/config.md`**

Add a row right after the `arch` row in the field table:

```markdown
| `image` | `String` | No | `"ubuntu-24-04-x64"` | Base VM image (DigitalOcean slug). Maps directly to `Provider.Create`'s `Image`. |
```

- [ ] **Step 8: Commit**

```bash
git add internal/config/Config.pkl internal/config/Config.pkl.go internal/config/config.go internal/config/config_test.go docs/config.md
git commit -m "Add image field to cloudlab.pkl schema"
```

---

### Task 2: `WaitReady`

**Files:**
- Create: `internal/lifecycle/ready.go`
- Test: `internal/lifecycle/ready_test.go`

**Interfaces:**
- Consumes: `reconcile.Connect(ctx, ip) (*reconcile.Client, error)`, `(*reconcile.Client) Run(cmd string) (string, error)`, `(*reconcile.Client) Close() error` (existing, from `internal/reconcile`).
- Produces: `WaitReady(ctx context.Context, ip string, timeout time.Duration) error` — consumed by Task 5 (`lifecycle.Up`/`Steps`).

- [ ] **Step 1: Write the failing tests**

Create `internal/lifecycle/ready_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/lifecycle/... -v`
Expected: FAIL — compile error, `undefined: WaitReady` (package doesn't exist yet).

- [ ] **Step 3: Write the minimal implementation**

Create `internal/lifecycle/ready.go`:

```go
// Package lifecycle brings a newly created instance from "VM exists" to
// "fully live": waiting for it to genuinely be ready, reconciling
// home-manager, syncing the repo in, and starting continuous watch. It
// is the piece cmd/up.go orchestrates on top of the already-built
// provider, config, reconcile, and state packages.
package lifecycle

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

const retryInterval = 500 * time.Millisecond

// WaitReady waits until ip is reachable over SSH and cloud-init has
// finished installing Nix, retrying the SSH connection every
// retryInterval until timeout elapses. A non-zero cloud-init exit is
// treated as a genuine failure and returned immediately, never retried.
func WaitReady(ctx context.Context, ip string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if !time.Now().Before(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s to become reachable: %w", timeout, ip, lastErr)
		}

		client, err := reconcile.Connect(ctx, ip)
		if err != nil {
			lastErr = err
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryInterval):
			}
			continue
		}

		output, err := client.Run("cloud-init status --wait")
		client.Close()
		if err != nil {
			return fmt.Errorf("cloud-init failed on %s: %w\n%s", ip, err, strings.TrimSpace(output))
		}
		return nil
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/lifecycle/... -v`
Expected: PASS — all 3 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/lifecycle/ready.go internal/lifecycle/ready_test.go
git commit -m "Add WaitReady for post-create VM readiness"
```

---

### Task 3: `Rsync`

**Files:**
- Create: `internal/lifecycle/rsync.go`
- Test: `internal/lifecycle/rsync_test.go`
- Modify: `flake.nix`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `Rsync(ctx context.Context, ip, localRepoRoot, remoteName string) error` — consumed by Task 5 (`lifecycle.Up`/`Steps`).

- [ ] **Step 1: Add `rsync` to the dev shell**

In `flake.nix`, add one line to the `packages` list (around line 103-108):

```nix
            packages = [
              pkgs.go
              pkgs.gopls
              pkgs.rsync
              (pklFor system pkgs)
              direnv-instant.packages.${system}.default
            ]
```

- [ ] **Step 2: Write the failing tests**

Create `internal/lifecycle/rsync_test.go`:

```go
package lifecycle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRsyncArgs_BuildsExpectedCommand(t *testing.T) {
	got := rsyncArgs("203.0.113.5", "/home/user/myrepo", "myrepo")
	want := []string{"-az", "-e", "ssh", "/home/user/myrepo/", "root@203.0.113.5:~/myrepo/"}
	if len(got) != len(want) {
		t.Fatalf("rsyncArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rsyncArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRsync_CopiesFilesBetweenLocalDirs(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not on PATH")
	}

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()

	// Exercises real rsync directly between two local paths -- proves
	// the underlying sync mechanic works. Rsync's own -e ssh/remote
	// argument form is covered separately by
	// TestRsyncArgs_BuildsExpectedCommand, since a real SSH-backed
	// transfer would need a real sshd to test end-to-end.
	cmd := exec.CommandContext(context.Background(), "rsync", "-az", src+"/", dst+"/")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rsync error = %v\n%s", err, out)
	}

	got, err := os.ReadFile(filepath.Join(dst, "hello.txt"))
	if err != nil {
		t.Fatalf("reading synced file: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("synced content = %q, want %q", got, "hello")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/lifecycle/... -run TestRsync -v`
Expected: FAIL — compile error, `undefined: rsyncArgs` / `Rsync`.

- [ ] **Step 4: Write the minimal implementation**

Create `internal/lifecycle/rsync.go`:

```go
package lifecycle

import (
	"context"
	"fmt"
	"os/exec"
)

// rsyncArgs builds the argv Rsync passes to the rsync binary: sync
// ip's remote ~/<remoteName> directory from localRepoRoot's contents,
// using the system ssh binary as rsync's remote shell.
func rsyncArgs(ip, localRepoRoot, remoteName string) []string {
	src := localRepoRoot + "/"
	dst := fmt.Sprintf("root@%s:~/%s/", ip, remoteName)
	return []string{"-az", "-e", "ssh", src, dst}
}

// Rsync copies localRepoRoot's contents to ~/<remoteName> on the
// instance at ip. Relies on WaitReady's Connect call having already
// completed trust-on-first-connect against ~/.ssh/known_hosts, since
// rsync -e ssh spawns the system ssh binary, not this project's Go SSH
// client.
func Rsync(ctx context.Context, ip, localRepoRoot, remoteName string) error {
	if _, err := exec.LookPath("rsync"); err != nil {
		return fmt.Errorf("rsync not found on PATH (run inside `nix develop`, or install it): %w", err)
	}
	cmd := exec.CommandContext(ctx, "rsync", rsyncArgs(ip, localRepoRoot, remoteName)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rsync to %s failed: %w\n%s", ip, err, out)
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/lifecycle/... -v`
Expected: PASS — every test in the package (aside from the pre-existing `ready_test.go` tests, unaffected).

- [ ] **Step 6: Commit**

```bash
git add internal/lifecycle/rsync.go internal/lifecycle/rsync_test.go flake.nix
git commit -m "Add Rsync for one-shot repo sync to the instance"
```

---

### Task 4: `StartWatch`

**Files:**
- Create: `internal/lifecycle/watch.go`
- Test: `internal/lifecycle/watch_test.go`
- Modify: `flake.nix`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `StartWatch(ctx context.Context, ip, name, localRepoRoot string) error` — consumed by Task 5 (`lifecycle.Up`/`Steps`).

- [ ] **Step 1: Add `mutagen` to the dev shell**

In `flake.nix`, add one line to the `packages` list (the same block Task 3 just edited):

```nix
            packages = [
              pkgs.go
              pkgs.gopls
              pkgs.rsync
              pkgs.mutagen
              (pklFor system pkgs)
              direnv-instant.packages.${system}.default
            ]
```

- [ ] **Step 2: Write the failing tests**

Create `internal/lifecycle/watch_test.go`:

```go
package lifecycle

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestMutagenCreateArgs_BuildsExpectedCommand(t *testing.T) {
	got := mutagenCreateArgs("203.0.113.5", "myrepo", "/home/user/myrepo")
	want := []string{"sync", "create", "--name=myrepo", "/home/user/myrepo", "root@203.0.113.5:~/myrepo"}
	if len(got) != len(want) {
		t.Fatalf("mutagenCreateArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mutagenCreateArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMutagenSync_RealLocalToLocalSession(t *testing.T) {
	if _, err := exec.LookPath("mutagen"); err != nil {
		t.Skip("mutagen not on PATH")
	}

	t.Setenv("MUTAGEN_DATA_DIRECTORY", t.TempDir())

	src := t.TempDir()
	dst := t.TempDir()
	name := "cloudlab-test-session"

	create := exec.Command("mutagen", "sync", "create", "--name="+name, src, dst)
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("mutagen sync create error = %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("mutagen", "sync", "terminate", name).Run()
	})

	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join(dst, "hello.txt"))
		if err == nil {
			if string(data) != "hello" {
				t.Fatalf("synced content = %q, want %q", data, "hello")
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("file never synced to destination within 10s")
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/lifecycle/... -run TestMutagen -v`
Expected: FAIL — compile error, `undefined: mutagenCreateArgs` / `StartWatch`.

- [ ] **Step 4: Write the minimal implementation**

Create `internal/lifecycle/watch.go`:

```go
package lifecycle

import (
	"context"
	"fmt"
	"os/exec"
)

// mutagenCreateArgs builds the argv StartWatch passes to the mutagen
// binary: a named two-way sync session between localRepoRoot and ip's
// remote ~/<name> directory.
func mutagenCreateArgs(ip, name, localRepoRoot string) []string {
	remote := fmt.Sprintf("root@%s:~/%s", ip, name)
	return []string{"sync", "create", "--name=" + name, localRepoRoot, remote}
}

// StartWatch starts a continuous two-way sync session between
// localRepoRoot and ip's remote ~/<name> directory, named after the
// instance. Relies on WaitReady's Connect call having already
// completed trust-on-first-connect against ~/.ssh/known_hosts, since
// Mutagen's own SSH transport is a separate implementation from this
// project's Go SSH client.
func StartWatch(ctx context.Context, ip, name, localRepoRoot string) error {
	if _, err := exec.LookPath("mutagen"); err != nil {
		return fmt.Errorf("mutagen not found on PATH (run inside `nix develop`, or install it: https://mutagen.io/documentation/introduction/installation): %w", err)
	}
	cmd := exec.CommandContext(ctx, "mutagen", mutagenCreateArgs(ip, name, localRepoRoot)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("starting watch for %s failed: %w\n%s", name, err, out)
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/lifecycle/... -v`
Expected: PASS — every test in the package.

- [ ] **Step 6: Commit**

```bash
git add internal/lifecycle/watch.go internal/lifecycle/watch_test.go flake.nix
git commit -m "Add StartWatch for continuous Mutagen repo sync"
```

---

### Task 5: `Up` orchestration

**Files:**
- Create: `internal/lifecycle/lifecycle.go`
- Test: `internal/lifecycle/lifecycle_test.go`

**Interfaces:**
- Consumes: `WaitReady`, `Rsync`, `StartWatch` (Tasks 2-4); `provider.Provider` interface, `provider.InstanceSpec{Name, Region, Size, Image, SSHKeys, UserData string/[]string}`, `provider.VM{ID, Name, IP, Region, Size, Status string}` (existing); `config.Resolve(ctx, path) (config.Config, error)`, `config.Config{Region, Size, Template *string, Image string, SshKeys *[]string}` (existing, extended in Task 1); `provisioning.CloudInitUserData string` (existing); `reconcile.Reconcile(ctx, name, cloudlabPath string) error` (existing); `state.Open() (*state.Store, error)`, `(*state.Store) Put(r state.Record) error`, `(*state.Store) Get(name string) (state.Record, bool, error)`, `state.Record{Name, Provider, VMID, IP, Region, Size, Template string}` (existing).
- Produces: `Steps` struct, `DefaultSteps() Steps`, `Up(ctx context.Context, p provider.Provider, steps Steps, name, cloudlabPath, repoRoot string) error` — consumed by Task 6 (`cmd/up.go`).

- [ ] **Step 1: Write the failing tests**

Create `internal/lifecycle/lifecycle_test.go`:

```go
package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/reconcile"
	"github.com/jskswamy/cloudlab/internal/state"
)

// fakeProvider is a minimal provider.Provider test double: Create
// returns a canned VM (or err, if set); the other three methods are
// unused by Up but must exist to satisfy the interface.
type fakeProvider struct {
	vm  provider.VM
	err error
}

func (f *fakeProvider) Create(ctx context.Context, spec provider.InstanceSpec) (provider.VM, error) {
	return f.vm, f.err
}
func (f *fakeProvider) Destroy(ctx context.Context, id string) error { return nil }
func (f *fakeProvider) Get(ctx context.Context, id string) (provider.VM, error) {
	return provider.VM{}, nil
}
func (f *fakeProvider) List(ctx context.Context) ([]provider.VM, error) { return nil, nil }

func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing fixture %s: %v", path, err)
	}
}

func minimalCloudlabPkl(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, path, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
	}, "\n")+"\n")
	return path
}

func TestUp_RunsFullSequenceInOrder(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-such-config"))

	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		return "", 0
	})

	repoRoot := t.TempDir()
	cloudlabPath := minimalCloudlabPkl(t, repoRoot)

	p := &fakeProvider{vm: provider.VM{ID: "vm-1", IP: addr, Region: "nyc3", Size: "s-1vcpu-1gb"}}

	var order []string
	steps := Steps{
		WaitReady: WaitReady,
		Reconcile: reconcile.Reconcile,
		Rsync: func(ctx context.Context, ip, localRepoRoot, remoteName string) error {
			order = append(order, "rsync")
			if ip != addr || localRepoRoot != repoRoot || remoteName != "myinstance" {
				t.Errorf("Rsync called with (%q, %q, %q)", ip, localRepoRoot, remoteName)
			}
			return nil
		},
		StartWatch: func(ctx context.Context, ip, name, localRepoRoot string) error {
			order = append(order, "watch")
			if ip != addr || name != "myinstance" || localRepoRoot != repoRoot {
				t.Errorf("StartWatch called with (%q, %q, %q)", ip, name, localRepoRoot)
			}
			return nil
		},
	}

	if err := Up(context.Background(), p, steps, "myinstance", cloudlabPath, repoRoot); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	if len(order) != 2 || order[0] != "rsync" || order[1] != "watch" {
		t.Errorf("call order = %v, want [rsync watch]", order)
	}

	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	record, ok, err := store.Get("myinstance")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("state.Get(myinstance) = not found, want recorded")
	}
	if record.VMID != "vm-1" {
		t.Errorf("record.VMID = %q, want %q", record.VMID, "vm-1")
	}
}

func TestUp_StateNotRecordedWhenCreateFails(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-such-config"))

	repoRoot := t.TempDir()
	cloudlabPath := minimalCloudlabPkl(t, repoRoot)

	p := &fakeProvider{err: errors.New("create failed")}

	err := Up(context.Background(), p, DefaultSteps(), "myinstance", cloudlabPath, repoRoot)
	if err == nil {
		t.Fatal("Up() error = nil, want error when Create fails")
	}

	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	_, ok, err := store.Get("myinstance")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("state.Get(myinstance) = found, want not recorded since Create failed")
	}
}

func TestUp_StateRecordedBeforeWaitReady(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-such-config"))

	repoRoot := t.TempDir()
	cloudlabPath := minimalCloudlabPkl(t, repoRoot)

	p := &fakeProvider{vm: provider.VM{ID: "vm-1", IP: "192.0.2.1", Region: "nyc3", Size: "s-1vcpu-1gb"}}

	steps := Steps{
		WaitReady: func(ctx context.Context, ip string, timeout time.Duration) error {
			return errors.New("simulated unreachable")
		},
		Reconcile:  func(ctx context.Context, name, cloudlabPath string) error { return nil },
		Rsync:      func(ctx context.Context, ip, localRepoRoot, remoteName string) error { return nil },
		StartWatch: func(ctx context.Context, ip, name, localRepoRoot string) error { return nil },
	}

	err := Up(context.Background(), p, steps, "myinstance", cloudlabPath, repoRoot)
	if err == nil {
		t.Fatal("Up() error = nil, want error when WaitReady fails")
	}

	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	_, ok, err := store.Get("myinstance")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("state.Get(myinstance) = not found, want recorded even though WaitReady failed")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/lifecycle/... -run TestUp -v`
Expected: FAIL — compile error, `undefined: Steps` / `Up` / `DefaultSteps`.

- [ ] **Step 3: Write the minimal implementation**

Create `internal/lifecycle/lifecycle.go`:

```go
package lifecycle

import (
	"context"
	"fmt"
	"time"

	"github.com/jskswamy/cloudlab/internal/config"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/provisioning"
	"github.com/jskswamy/cloudlab/internal/reconcile"
	"github.com/jskswamy/cloudlab/internal/state"
)

const readyTimeout = 5 * time.Minute

// Steps groups the lifecycle steps Up calls after creating the VM, so
// tests can substitute recording fakes for the steps that would
// otherwise need a real remote rsync/mutagen target. Production
// callers always use DefaultSteps.
type Steps struct {
	WaitReady  func(ctx context.Context, ip string, timeout time.Duration) error
	Reconcile  func(ctx context.Context, name, cloudlabPath string) error
	Rsync      func(ctx context.Context, ip, localRepoRoot, remoteName string) error
	StartWatch func(ctx context.Context, ip, name, localRepoRoot string) error
}

// DefaultSteps wires Steps to the real implementations.
func DefaultSteps() Steps {
	return Steps{
		WaitReady:  WaitReady,
		Reconcile:  reconcile.Reconcile,
		Rsync:      Rsync,
		StartWatch: StartWatch,
	}
}

// Up creates a new instance and brings it fully live: creates the VM,
// records its state immediately (so a later failure still leaves it
// destroyable), waits until it's genuinely ready, reconciles
// home-manager once, rsyncs the repo in, and starts continuous watch.
func Up(ctx context.Context, p provider.Provider, steps Steps, name, cloudlabPath, repoRoot string) error {
	cfg, err := config.Resolve(ctx, cloudlabPath)
	if err != nil {
		return err
	}

	var sshKeys []string
	if cfg.SshKeys != nil {
		sshKeys = *cfg.SshKeys
	}

	spec := provider.InstanceSpec{
		Name:     name,
		Region:   *cfg.Region,
		Size:     *cfg.Size,
		Image:    cfg.Image,
		SSHKeys:  sshKeys,
		UserData: provisioning.CloudInitUserData,
	}

	vm, err := p.Create(ctx, spec)
	if err != nil {
		return fmt.Errorf("creating instance: %w", err)
	}

	store, err := state.Open()
	if err != nil {
		return err
	}
	record := state.Record{
		Name:     name,
		Provider: "digitalocean",
		VMID:     vm.ID,
		IP:       vm.IP,
		Region:   vm.Region,
		Size:     vm.Size,
		Template: *cfg.Template,
	}
	if err := store.Put(record); err != nil {
		if destroyErr := p.Destroy(ctx, vm.ID); destroyErr != nil {
			return fmt.Errorf("recording instance state failed (%v), and cleanup also failed -- destroy VM %s manually: %w", err, vm.ID, destroyErr)
		}
		return fmt.Errorf("recording instance state: %w", err)
	}

	if err := steps.WaitReady(ctx, vm.IP, readyTimeout); err != nil {
		return fmt.Errorf("waiting for %s to be ready: %w", name, err)
	}

	if err := steps.Reconcile(ctx, name, cloudlabPath); err != nil {
		return err
	}

	if err := steps.Rsync(ctx, vm.IP, repoRoot, name); err != nil {
		return err
	}

	if err := steps.StartWatch(ctx, vm.IP, name, repoRoot); err != nil {
		return err
	}

	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/lifecycle/... -v`
Expected: PASS — every test in the package.

- [ ] **Step 5: Commit**

```bash
git add internal/lifecycle/lifecycle.go internal/lifecycle/lifecycle_test.go
git commit -m "Add Up lifecycle orchestration"
```

---

### Task 6: `up` command

**Files:**
- Modify: `cmd/up.go`
- Modify: `cmd/up_test.go`

**Interfaces:**
- Consumes: `lifecycle.Up(ctx, p provider.Provider, steps lifecycle.Steps, name, cloudlabPath, repoRoot string) error`, `lifecycle.DefaultSteps() lifecycle.Steps` (Task 5); `digitalocean.New(token string) *digitalocean.Provider` (existing); `identity.RepoRoot`, `identity.DeriveName` (existing, same functions already used).
- Produces: nothing — this is the final integration task.

- [ ] **Step 1: Write the failing tests**

Replace `cmd/up_test.go`'s three `Test*` functions (keep `chdir` and `initTestRepo` — other test files in this package use them) with:

```go
func TestUpCommand_NotInRepoErrors(t *testing.T) {
	chdir(t, t.TempDir())

	root := newRootCmd()
	root.SetArgs([]string{"up"})
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

func TestUpCommand_MissingTokenErrors(t *testing.T) {
	chdir(t, initTestRepo(t))
	t.Setenv("DIGITALOCEAN_TOKEN", "")

	root := newRootCmd()
	root.SetArgs([]string{"up"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "DIGITALOCEAN_TOKEN") {
		t.Errorf("error = %q, want mention of DIGITALOCEAN_TOKEN", err.Error())
	}
}

func TestUpCommand_PositionalNameOverridesDerivedName(t *testing.T) {
	chdir(t, initTestRepo(t))
	t.Setenv("DIGITALOCEAN_TOKEN", "")

	root := newRootCmd()
	root.SetArgs([]string{"up", "somename"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `instance "somename"`) {
		t.Errorf("error = %q, want it to name instance %q, not the derived repo name", err.Error(), "somename")
	}
}
```

(Deliberately not re-tested at this layer: the full create→ready→reconcile→rsync→watch sequence and positional-name threading into it — both are already proven by Task 5's `lifecycle.Up` tests against a fake provider. This layer only proves `cmd/up.go`'s own glue: repo resolution and the token precondition.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/... -run TestUpCommand -v`
Expected: FAIL — `TestUpCommand_MissingTokenErrors` fails (the old stub error never mentions `DIGITALOCEAN_TOKEN`, and old code never reads that env var at all). `TestUpCommand_NotInRepoErrors` and `TestUpCommand_PositionalNameOverridesDerivedName` both still pass at this point — the old stub error already happens to mention `instance %q`, so the latter isn't a true red step until Step 3 removes the stub. That's expected; only `TestUpCommand_MissingTokenErrors` needs to flip from FAIL to PASS to confirm Step 3 actually changed behavior.

- [ ] **Step 3: Rewrite the command**

Replace `cmd/up.go` in full:

```go
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jskswamy/cloudlab/internal/identity"
	"github.com/jskswamy/cloudlab/internal/lifecycle"
	"github.com/jskswamy/cloudlab/internal/provider/digitalocean"
	"github.com/spf13/cobra"
)

func newUpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "up [name]",
		Short: "Create the VM, provision it, and bring the instance fully live",
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

			token := os.Getenv("DIGITALOCEAN_TOKEN")
			if token == "" {
				return fmt.Errorf("DIGITALOCEAN_TOKEN not set (needed to create instance %q)", name)
			}
			p := digitalocean.New(token)

			cloudlabPath := filepath.Join(root, "cloudlab.pkl")
			if err := lifecycle.Up(cmd.Context(), p, lifecycle.DefaultSteps(), name, cloudlabPath, root); err != nil {
				return err
			}
			cmd.Printf("Instance %s is up\n", name)
			return nil
		},
	}
	return cmd
}
```

(Note: this removes the old, unused `--template` flag — `template` now comes exclusively from `cloudlab.pkl`, and nothing in the codebase referenced the flag's value.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/... -v`
Expected: PASS — every test in the package, including the 3 updated `up` tests.

- [ ] **Step 5: Commit**

```bash
git add cmd/up.go cmd/up_test.go
git commit -m "Wire up to lifecycle.Up"
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
plan's `internal/config`, `internal/lifecycle`, and `cmd` tests plus
every untouched earlier phase's tests — aside from
`TestLoadFromPath_MinimalFixture`, the known, expected,
machine-specific exception documented throughout this project's
history), and `nix flake check` is clean.
