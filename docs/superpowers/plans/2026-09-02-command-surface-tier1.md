# Command Surface Tier 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `cmd/lookup.go`'s stub dispatch for `down`, `status`, `watch`, `sync`, `download`, and `ssh` with real implementations, so `up` can be tested end to end and torn back down.

**Architecture:** `cmd/lookup.go`'s existing table-driven scaffold (name resolution, flag handling, `Args` validation) is untouched except for one new `run` field on `lookupCommandSpec`. Each of the six commands gets a small `run` function in a new `cmd/lookup_run.go`, which resolves the instance's `state.Record` (shared helper, one error message for "no such instance" across all six) and calls into `internal/lifecycle`. `shell`/`connect` keep calling the existing `stubErr` — untouched.

**Tech Stack:** Go, cobra, the existing `internal/lifecycle`/`internal/state`/`internal/provider` packages, real `rsync`/`mutagen`/`ssh` binaries via `os/exec` (same pattern as `Rsync`/`StartWatch` already established).

## Global Constraints

- Only six commands get real implementations this plan: `down`, `ssh`, `status`, `watch`, `sync`, `download`. `shell` and `connect` stay stubbed (separate spec).
- Shared error for an unresolvable instance name: `no instance named %q (run cloudlab up first)` — every one of the six fails identically on an unknown name.
- `down`: best-effort `mutagen sync terminate <name>` (errors swallowed), then `provider.Destroy` (`provider.ErrNotFound` = success), then `state.Store.Delete` always runs last — even if `Destroy` fails for a real reason, state is still cleared and the error is still returned to the user.
- `ssh`: execs the real `ssh` binary as `root@<ip>` with stdio passed straight through — no PTY handling of our own.
- `status`: local state fields always print; a `provider.Get` failure is reported inline, not as a command failure.
- `watch`: derives the local repo root from cwd via `identity.RepoRoot` (must run from the repo checkout) and is idempotent — restarting an already-running session must not error.
- `sync <local> [remote]` / `download <remote> [local]`: default remote = `~/<basename(local)>`; default local = `./<basename(remote)>`. `Rsync`'s existing argv-building generalizes into `Push`/`Pull` with no duplicated logic between `up`'s existing call and the new commands.
- Every new/changed test that touches `state.Open()` sets `XDG_STATE_HOME` to a temp dir — none may touch the real developer machine's state file.

---

### Task 1: `down`

**Files:**
- Modify: `internal/lifecycle/lifecycle_test.go:32` (extend `fakeProvider`)
- Create: `internal/lifecycle/down.go`
- Create: `internal/lifecycle/down_test.go`
- Modify: `cmd/lookup.go` (add `run` field + wire `down`)
- Create: `cmd/lookup_run.go`
- Modify: `cmd/lookup_test.go` (down's row in the name-flag-resolves table)

**Interfaces:**
- Consumes: `provider.Provider.Destroy(ctx, id) error`, `provider.ErrNotFound`, `state.Store.Delete(name) error`, `state.Record{Name, VMID, ...}` (all already exist).
- Produces: `terminateWatch(ctx context.Context, name string)` (unexported, package `lifecycle`) — reused by Task 3 (`watch`). `Down(ctx context.Context, p provider.Provider, store *state.Store, record state.Record) error` — used by `cmd/lookup_run.go`. `resolveInstance(name string) (*state.Store, state.Record, error)` and `resolveProvider() (provider.Provider, error)` (both package `cmd`, unexported) — reused by every later task's `run` function.

- [ ] **Step 1: Extend `fakeProvider.Destroy` to track calls and support a configurable error**

In `internal/lifecycle/lifecycle_test.go`, add two fields to the `fakeProvider` struct (right after the existing `gotSpec provider.InstanceSpec` field on line 24):

```go
type fakeProvider struct {
	vm      provider.VM
	err     error
	created bool
	gotSpec provider.InstanceSpec
	destroyedID string
	destroyErr  error
}
```

Replace line 32:

```go
func (f *fakeProvider) Destroy(ctx context.Context, id string) error { return nil }
```

with:

```go
func (f *fakeProvider) Destroy(ctx context.Context, id string) error {
	f.destroyedID = id
	return f.destroyErr
}
```

- [ ] **Step 2: Write the failing tests for `Down`**

Create `internal/lifecycle/down_test.go`:

```go
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/state"
)

// setupDownTest isolates state and Mutagen. Down's terminateWatch call
// starts a real Mutagen daemon even when terminating a session that
// doesn't exist (confirmed empirically against the real binary), so
// every Down test needs an isolated MUTAGEN_DATA_DIRECTORY and a
// cleanup that stops it -- otherwise tests leak a daemon rooted in the
// real developer machine's default data directory.
func setupDownTest(t *testing.T) *state.Store {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("MUTAGEN_DATA_DIRECTORY", t.TempDir())
	t.Cleanup(func() {
		_ = exec.Command("mutagen", "daemon", "stop").Run()
	})
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestDown_DestroysAndClearsState(t *testing.T) {
	store := setupDownTest(t)
	record := state.Record{Name: "myinstance", VMID: "vm-1", IP: "127.0.0.1"}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{}
	if err := Down(context.Background(), p, store, record); err != nil {
		t.Fatalf("Down() error = %v", err)
	}

	if p.destroyedID != "vm-1" {
		t.Errorf("Destroy called with %q, want %q", p.destroyedID, "vm-1")
	}
	if _, ok, err := store.Get("myinstance"); err != nil || ok {
		t.Errorf("state record still present after Down (ok=%v, err=%v)", ok, err)
	}
}

func TestDown_MissingVMStillClearsState(t *testing.T) {
	store := setupDownTest(t)
	record := state.Record{Name: "myinstance", VMID: "vm-1"}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{destroyErr: fmt.Errorf("wrapped: %w", provider.ErrNotFound)}
	if err := Down(context.Background(), p, store, record); err != nil {
		t.Fatalf("Down() error = %v, want nil (not-found is success)", err)
	}
	if _, ok, _ := store.Get("myinstance"); ok {
		t.Error("state record still present after Down with ErrNotFound")
	}
}

func TestDown_RealDestroyErrorStillClearsStateButIsReturned(t *testing.T) {
	store := setupDownTest(t)
	record := state.Record{Name: "myinstance", VMID: "vm-1"}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{destroyErr: errors.New("network error")}
	err := Down(context.Background(), p, store, record)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if _, ok, _ := store.Get("myinstance"); ok {
		t.Error("state record still present after Down, want cleared even on real destroy error")
	}
}
```

- [ ] **Step 2b: Run to verify it fails**

Run: `nix develop --command go test ./internal/lifecycle/... -run TestDown -v`
Expected: FAIL with `undefined: Down`

- [ ] **Step 3: Implement `Down`**

Create `internal/lifecycle/down.go`:

```go
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/state"
)

// terminateWatch best-effort terminates any existing Mutagen sync
// session named name. Errors -- including "no such session" -- are
// swallowed: the absence of a session to terminate isn't a failure
// for Down or for a Watch restart, both of which call this first.
func terminateWatch(ctx context.Context, name string) {
	_ = exec.CommandContext(ctx, "mutagen", "sync", "terminate", name).Run()
}

// Down tears an instance down: stops its watch session (best-effort),
// destroys the VM, and clears its state record. A VM that's already
// gone (destroyed outside cloudlab) is treated as success, not an
// error -- state is cleared either way so cloudlab's view converges
// with reality. If Destroy fails for any other reason, state is still
// cleared (so a stuck record can't block a retry), but the error is
// still returned so the user knows to check the provider's dashboard.
func Down(ctx context.Context, p provider.Provider, store *state.Store, record state.Record) error {
	terminateWatch(ctx, record.Name)

	if err := p.Destroy(ctx, record.VMID); err != nil && !errors.Is(err, provider.ErrNotFound) {
		_ = store.Delete(record.Name)
		return fmt.Errorf("destroying VM %s: %w (state cleared -- check the provider dashboard)", record.VMID, err)
	}
	return store.Delete(record.Name)
}
```

- [ ] **Step 4: Run to verify the new tests pass**

Run: `nix develop --command go test ./internal/lifecycle/... -run TestDown -v`
Expected: PASS (all three)

- [ ] **Step 5: Wire `down` into the cobra command**

Add the `run` field to `lookupCommandSpec` in `cmd/lookup.go` (replace lines 15-19):

```go
type lookupCommandSpec struct {
	use, short, verb string
	args             cobra.PositionalArgs
	named            bool
	run              func(cmd *cobra.Command, name string, args []string) error
}
```

Add `run: runDown,` to the `down` entry (lines 57-63):

```go
	{
		use:   "down [name]",
		short: "Stop watch, destroy the VM, and clear state",
		verb:  "down",
		args:  cobra.MaximumNArgs(1),
		named: true,
		run:   runDown,
	},
```

Replace the `RunE` body (lines 92-102) to dispatch to `spec.run` when set:

```go
			RunE: func(cmd *cobra.Command, args []string) error {
				positional := ""
				if spec.named && len(args) > 0 {
					positional = args[0]
				}
				name, err := resolveLookupIdentity(cmd, positional)
				if err != nil {
					return err
				}
				if spec.run != nil {
					return spec.run(cmd, name, args)
				}
				return stubErr(spec.verb, name)
			},
```

Create `cmd/lookup_run.go`:

```go
package cmd

import (
	"fmt"
	"os"

	"github.com/jskswamy/cloudlab/internal/lifecycle"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/provider/digitalocean"
	"github.com/jskswamy/cloudlab/internal/state"
	"github.com/spf13/cobra"
)

// resolveInstance opens the state store, looks up name, and returns
// the same "no instance named" error every command below list/up
// reports identically when the name doesn't resolve to a known
// instance.
func resolveInstance(name string) (*state.Store, state.Record, error) {
	store, err := state.Open()
	if err != nil {
		return nil, state.Record{}, err
	}
	record, ok, err := store.Get(name)
	if err != nil {
		return nil, state.Record{}, err
	}
	if !ok {
		return nil, state.Record{}, fmt.Errorf("no instance named %q (run cloudlab up first)", name)
	}
	return store, record, nil
}

// resolveProvider builds a DigitalOcean provider from
// DIGITALOCEAN_TOKEN, for commands that need to call the live API
// (down, status).
func resolveProvider() (provider.Provider, error) {
	token := os.Getenv("DIGITALOCEAN_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("DIGITALOCEAN_TOKEN not set")
	}
	return digitalocean.New(token), nil
}

func runDown(cmd *cobra.Command, name string, args []string) error {
	store, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	p, err := resolveProvider()
	if err != nil {
		return err
	}
	if err := lifecycle.Down(cmd.Context(), p, store, record); err != nil {
		return err
	}
	cmd.Printf("Instance %s is down\n", name)
	return nil
}
```

- [ ] **Step 6: Update the down row in the name-flag-resolves table**

In `cmd/lookup_test.go`, replace `TestLookupCommands_NameFlagResolves` in full so every case carries its own expected substring (later tasks each update exactly one row):

```go
func TestLookupCommands_NameFlagResolves(t *testing.T) {
	cases := []struct {
		args []string
		verb string
		want string
	}{
		{[]string{"shell", "--name", "myrepo"}, "shell", "shell: not implemented yet"},
		{[]string{"ssh", "--name", "myrepo"}, "ssh", "ssh: not implemented yet"},
		{[]string{"watch", "--name", "myrepo"}, "watch", "watch: not implemented yet"},
		{[]string{"connect", "--name", "myrepo"}, "connect", "connect: not implemented yet"},
		{[]string{"status", "--name", "myrepo"}, "status", "status: not implemented yet"},
		{[]string{"down", "--name", "myrepo"}, "down", `no instance named "myrepo"`},
		{[]string{"sync", "./data", "--name", "myrepo"}, "sync", "sync: not implemented yet"},
		{[]string{"download", "./results", "--name", "myrepo"}, "download", "download: not implemented yet"},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
			root := newRootCmd()
			root.SetArgs(tc.args)
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)

			err := root.Execute()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
		})
	}
}
```

Add `"path/filepath"` to the existing import block at the top of `cmd/lookup_test.go`:

```go
import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)
```

- [ ] **Step 7: Run the full test suite**

Run: `nix develop --command go build ./... && nix develop --command go test ./... 2>&1 | grep -v pkl-go`
Expected: PASS (the pre-existing `TestLoadFromPath_MinimalFixture` Pkl-cache failure is a known machine-specific issue, unrelated to this change)

- [ ] **Step 8: Commit**

```bash
git add internal/lifecycle/lifecycle_test.go internal/lifecycle/down.go internal/lifecycle/down_test.go cmd/lookup.go cmd/lookup_run.go cmd/lookup_test.go
git commit -m "Add down command"
```

---

### Task 2: `status`

**Files:**
- Modify: `internal/lifecycle/lifecycle_test.go:33` (extend `fakeProvider.Get`)
- Create: `internal/lifecycle/status.go`
- Create: `internal/lifecycle/status_test.go`
- Modify: `cmd/lookup.go` (wire `status`)
- Modify: `cmd/lookup_run.go` (add `runStatus`)
- Modify: `cmd/lookup_test.go` (status's row)

**Interfaces:**
- Consumes: `provider.Provider.Get(ctx, id) (provider.VM, error)`, `provider.VM{Status string, ...}` (already exists), `resolveInstance`/`resolveProvider` from Task 1.
- Produces: `InstanceStatus{Record state.Record, LiveStatus string, LiveErr error}` and `Status(ctx context.Context, p provider.Provider, record state.Record) InstanceStatus` (package `lifecycle`) — no error return: a live-check failure is captured in `LiveErr`, never fails the call itself.

- [ ] **Step 1: Extend `fakeProvider.Get` to be configurable**

In `internal/lifecycle/lifecycle_test.go`, add two fields to the `fakeProvider` struct (alongside the ones added in Task 1):

```go
type fakeProvider struct {
	vm      provider.VM
	err     error
	created bool
	gotSpec provider.InstanceSpec
	destroyedID string
	destroyErr  error
	getVM  provider.VM
	getErr error
}
```

Replace the `Get` method (currently):

```go
func (f *fakeProvider) Get(ctx context.Context, id string) (provider.VM, error) {
	return provider.VM{}, nil
}
```

with:

```go
func (f *fakeProvider) Get(ctx context.Context, id string) (provider.VM, error) {
	return f.getVM, f.getErr
}
```

- [ ] **Step 2: Write the failing tests for `Status`**

Create `internal/lifecycle/status_test.go`:

```go
package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/state"
)

func TestStatus_ReportsLiveStatusOnSuccess(t *testing.T) {
	record := state.Record{Name: "myinstance", VMID: "vm-1", IP: "127.0.0.1"}
	p := &fakeProvider{getVM: provider.VM{ID: "vm-1", Status: "active"}}

	got := Status(context.Background(), p, record)

	if got.Record.Name != "myinstance" {
		t.Errorf("Record.Name = %q, want %q", got.Record.Name, "myinstance")
	}
	if got.LiveStatus != "active" {
		t.Errorf("LiveStatus = %q, want %q", got.LiveStatus, "active")
	}
	if got.LiveErr != nil {
		t.Errorf("LiveErr = %v, want nil", got.LiveErr)
	}
}

func TestStatus_RecordFieldsSurviveLiveCheckFailure(t *testing.T) {
	record := state.Record{Name: "myinstance", VMID: "vm-1", IP: "127.0.0.1"}
	p := &fakeProvider{getErr: errors.New("network error")}

	got := Status(context.Background(), p, record)

	if got.Record.Name != "myinstance" {
		t.Errorf("Record.Name = %q, want %q -- local fields must survive a live-check failure", got.Record.Name, "myinstance")
	}
	if got.LiveErr == nil {
		t.Error("LiveErr = nil, want the Get error")
	}
}
```

- [ ] **Step 2b: Run to verify it fails**

Run: `nix develop --command go test ./internal/lifecycle/... -run TestStatus -v`
Expected: FAIL with `undefined: Status`

- [ ] **Step 3: Implement `Status`**

Create `internal/lifecycle/status.go`:

```go
package lifecycle

import (
	"context"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/state"
)

// InstanceStatus combines an instance's local state record with a live
// check of its current provider-side status.
type InstanceStatus struct {
	Record     state.Record
	LiveStatus string
	LiveErr    error
}

// Status reports record alongside a live provider.Get check. A Get
// failure (network error, VM destroyed outside cloudlab, etc.) is
// captured in LiveErr rather than failing the call -- local state is
// always more useful than nothing.
func Status(ctx context.Context, p provider.Provider, record state.Record) InstanceStatus {
	vm, err := p.Get(ctx, record.VMID)
	if err != nil {
		return InstanceStatus{Record: record, LiveErr: err}
	}
	return InstanceStatus{Record: record, LiveStatus: vm.Status}
}
```

- [ ] **Step 4: Run to verify the new tests pass**

Run: `nix develop --command go test ./internal/lifecycle/... -run TestStatus -v`
Expected: PASS (both)

- [ ] **Step 5: Wire `status` into the cobra command**

In `cmd/lookup.go`, add `run: runStatus,` to the `status` entry:

```go
	{
		use:   "status [name]",
		short: "Show instance detail: IP, uptime, cost, sync/watch state",
		verb:  "status",
		args:  cobra.MaximumNArgs(1),
		named: true,
		run:   runStatus,
	},
```

Append to `cmd/lookup_run.go`:

```go
func runStatus(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	p, err := resolveProvider()
	if err != nil {
		return err
	}

	st := lifecycle.Status(cmd.Context(), p, record)
	cmd.Printf("Name:     %s\n", st.Record.Name)
	cmd.Printf("Provider: %s\n", st.Record.Provider)
	cmd.Printf("Region:   %s\n", st.Record.Region)
	cmd.Printf("Size:     %s\n", st.Record.Size)
	cmd.Printf("Template: %s\n", st.Record.Template)
	cmd.Printf("IP:       %s\n", st.Record.IP)
	if st.LiveErr != nil {
		cmd.Printf("Status:   unknown (live check failed: %v)\n", st.LiveErr)
	} else {
		cmd.Printf("Status:   %s\n", st.LiveStatus)
	}
	return nil
}
```

- [ ] **Step 6: Update the status row**

In `cmd/lookup_test.go`'s `TestLookupCommands_NameFlagResolves` cases, change:

```go
		{[]string{"status", "--name", "myrepo"}, "status", "status: not implemented yet"},
```

to:

```go
		{[]string{"status", "--name", "myrepo"}, "status", `no instance named "myrepo"`},
```

- [ ] **Step 7: Run the full test suite**

Run: `nix develop --command go build ./... && nix develop --command go test ./... 2>&1 | grep -v pkl-go`
Expected: PASS (same known Pkl-cache exception as Task 1)

- [ ] **Step 8: Commit**

```bash
git add internal/lifecycle/lifecycle_test.go internal/lifecycle/status.go internal/lifecycle/status_test.go cmd/lookup.go cmd/lookup_run.go cmd/lookup_test.go
git commit -m "Add status command"
```

---

### Task 3: `watch`

**Files:**
- Modify: `internal/lifecycle/watch.go`
- Modify: `internal/lifecycle/watch_test.go`
- Modify: `cmd/lookup.go` (wire `watch`)
- Modify: `cmd/lookup_run.go` (add `runWatch`)
- Modify: `cmd/lookup_test.go` (watch's row)

**Interfaces:**
- Consumes: `terminateWatch(ctx, name)` from Task 1 (`internal/lifecycle/down.go`), `identity.RepoRoot(cwd, repoFlag string) (string, error)` (already exists), `resolveInstance` from Task 1.
- Produces: nothing new exported — `StartWatch`'s existing signature is unchanged, now idempotent.

- [ ] **Step 1: Write the failing test for terminate-before-create**

Empirically, mutagen does **not** error on a duplicate `--name` (two
sessions can share a name; `--name` is a label, not a unique key), and
a *failed* `sync create` leaves no stray session behind. So the only
reliable way to prove `StartWatch` terminates an existing session
first is to count sessions by that name afterward, not to check for a
"name already in use" error (verified directly against the real
binary -- no such error exists for this case).

Append to `internal/lifecycle/watch_test.go`:

```go
func TestStartWatch_TerminatesExistingSessionFirst(t *testing.T) {
	if _, err := exec.LookPath("mutagen"); err != nil {
		t.Skip("mutagen not on PATH")
	}

	t.Setenv("MUTAGEN_DATA_DIRECTORY", t.TempDir())

	src := t.TempDir()
	dst := t.TempDir()
	name := "cloudlab-restart-test"

	// Seed a pre-existing session under name via a real local-to-local
	// sync (guaranteed to succeed, no sshd required) -- mirrors "a
	// watch session already exists" the way `watch` needs to restart it.
	create := exec.Command("mutagen", "sync", "create", "--name="+name, src, dst)
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("seeding existing session: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("mutagen", "sync", "terminate", name).Run()
		_ = exec.Command("mutagen", "daemon", "stop").Run()
	})

	// StartWatch's remote target (root@127.0.0.1) has no reachable
	// sshd in a test environment, so this call is expected to fail on
	// the SSH connection -- but that failure must happen AFTER the
	// pre-existing session was already terminated. A failed create
	// leaves no new session behind (confirmed empirically), so zero
	// remaining sessions named name proves the old one is gone.
	_ = StartWatch(context.Background(), "127.0.0.1", name, src)

	out, err := exec.Command("mutagen", "sync", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("mutagen sync list: %v\n%s", err, out)
	}
	if strings.Count(string(out), "Name: "+name) != 0 {
		t.Errorf("session %q still present after StartWatch -- terminateWatch did not run before create", name)
	}
}
```

Add `"context"` and `"strings"` to the import block at the top of `internal/lifecycle/watch_test.go`:

```go
import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)
```

- [ ] **Step 2: Run to verify it fails**

Run: `nix develop --command go test ./internal/lifecycle/... -run TestStartWatch_TerminatesExistingSessionFirst -v`
Expected: FAIL — without a `terminateWatch` call, the pre-existing local-to-local session is untouched by `StartWatch`'s failed create attempt, so it's still present afterward (count = 1, not 0).

- [ ] **Step 3: Make `StartWatch` idempotent**

In `internal/lifecycle/watch.go`, update the doc comment and insert a `terminateWatch` call before creating the session:

```go
// StartWatch starts a continuous two-way sync session between
// localRepoRoot and ip's remote ~/<name> directory, named after the
// instance. Idempotent: any existing session by that name is
// terminated first, so this doubles as a restart for a stopped or
// dead watch. Relies on WaitReady's Connect call having already
// completed trust-on-first-connect against ~/.ssh/known_hosts, since
// Mutagen's own SSH transport is a separate implementation from this
// project's Go SSH client.
func StartWatch(ctx context.Context, ip, name, localRepoRoot string) error {
	if _, err := exec.LookPath("mutagen"); err != nil {
		return fmt.Errorf("mutagen not found on PATH (run inside `nix develop`, or install it: https://mutagen.io/documentation/introduction/installation): %w", err)
	}
	terminateWatch(ctx, name)
	cmd := exec.CommandContext(ctx, "mutagen", mutagenCreateArgs(ip, name, localRepoRoot)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("starting watch for %s failed: %w\n%s", name, err, out)
	}
	return nil
}
```

- [ ] **Step 4: Run to verify the new test passes**

Run: `nix develop --command go test ./internal/lifecycle/... -run TestStartWatch -v`
Expected: PASS (both `TestStartWatch_TerminatesExistingSessionFirst` and the pre-existing `TestMutagenSync_RealLocalToLocalSession`)

- [ ] **Step 5: Wire `watch` into the cobra command**

In `cmd/lookup.go`, add `run: runWatch,` to the `watch` entry:

```go
	{
		use:   "watch [name]",
		short: "Restart continuous two-way repo sync if it's stopped or dead",
		verb:  "watch",
		args:  cobra.MaximumNArgs(1),
		named: true,
		run:   runWatch,
	},
```

Add `"github.com/jskswamy/cloudlab/internal/identity"` to `cmd/lookup_run.go`'s import block, and append:

```go
func runWatch(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	repoFlag, _ := cmd.Flags().GetString("repo")
	root, err := identity.RepoRoot(cwd, repoFlag)
	if err != nil {
		return err
	}

	if err := lifecycle.StartWatch(cmd.Context(), record.IP, name, root); err != nil {
		return err
	}
	cmd.Printf("Watch restarted for %s\n", name)
	return nil
}
```

- [ ] **Step 6: Update the watch row**

In `cmd/lookup_test.go`'s `TestLookupCommands_NameFlagResolves` cases, change:

```go
		{[]string{"watch", "--name", "myrepo"}, "watch", "watch: not implemented yet"},
```

to:

```go
		{[]string{"watch", "--name", "myrepo"}, "watch", `no instance named "myrepo"`},
```

(This test never reaches `identity.RepoRoot` — `resolveInstance` fails first since no such instance exists in the isolated state dir.)

- [ ] **Step 7: Run the full test suite**

Run: `nix develop --command go build ./... && nix develop --command go test ./... 2>&1 | grep -v pkl-go`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/lifecycle/watch.go internal/lifecycle/watch_test.go cmd/lookup.go cmd/lookup_run.go cmd/lookup_test.go
git commit -m "Add watch command, make StartWatch idempotent"
```

---

### Task 4: `sync`

**Files:**
- Modify: `internal/lifecycle/rsync.go` (generalize into `Push`)
- Modify: `internal/lifecycle/rsync_test.go`
- Modify: `cmd/lookup.go` (wire `sync`)
- Modify: `cmd/lookup_run.go` (add `runSync`, `defaultRemoteDir`)
- Create: `cmd/lookup_run_test.go`
- Modify: `cmd/lookup_test.go` (sync's rows in both tables)

**Interfaces:**
- Consumes: `resolveInstance` from Task 1.
- Produces: `rsyncPushArgs(ip, local, remote string) []string` and `Push(ctx context.Context, ip, local, remote string) error` (package `lifecycle`) — `remote` is the full remote path (caller resolves any default), unlike the old `rsyncArgs`'s implicit `~/` prefix. `Rsync`'s existing signature and behavior (used by `up`) is unchanged — it's now a thin wrapper over `Push`. `defaultRemoteDir(local string) string` (package `cmd`) — used by Task 5's `cmd/lookup_run_test.go` additions too.

- [ ] **Step 1: Write the failing test for the generalized argv builder**

In `internal/lifecycle/rsync_test.go`, replace `TestRsyncArgs_BuildsExpectedCommand`:

```go
func TestRsyncPushArgs_BuildsExpectedCommand(t *testing.T) {
	got := rsyncPushArgs("203.0.113.5", "/home/user/myrepo", "~/myrepo")
	want := []string{"-az", "-e", "ssh", "/home/user/myrepo/", "root@203.0.113.5:~/myrepo/"}
	if len(got) != len(want) {
		t.Fatalf("rsyncPushArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rsyncPushArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `nix develop --command go test ./internal/lifecycle/... -run TestRsyncPushArgs -v`
Expected: FAIL with `undefined: rsyncPushArgs`

- [ ] **Step 3: Generalize `rsync.go`**

Replace `internal/lifecycle/rsync.go` in full:

```go
package lifecycle

import (
	"context"
	"fmt"
	"os/exec"
)

// rsyncPushArgs builds the argv for copying local's contents to ip's
// remote directory remote (already the full path, e.g. "~/myrepo" or
// "~/dataset" -- callers resolve any default before calling this),
// using the system ssh binary as rsync's remote shell.
func rsyncPushArgs(ip, local, remote string) []string {
	src := local + "/"
	dst := fmt.Sprintf("root@%s:%s/", ip, remote)
	return []string{"-az", "-e", "ssh", src, dst}
}

// Push copies local's contents to ip's remote directory remote.
func Push(ctx context.Context, ip, local, remote string) error {
	if _, err := exec.LookPath("rsync"); err != nil {
		return fmt.Errorf("rsync not found on PATH (run inside `nix develop`, or install it): %w", err)
	}
	cmd := exec.CommandContext(ctx, "rsync", rsyncPushArgs(ip, local, remote)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rsync to %s failed: %w\n%s", ip, err, out)
	}
	return nil
}

// Rsync copies localRepoRoot's contents to ~/<remoteName> on the
// instance at ip -- up's one-shot initial seed of the repo. A thin
// wrapper over Push with up's own path convention.
func Rsync(ctx context.Context, ip, localRepoRoot, remoteName string) error {
	return Push(ctx, ip, localRepoRoot, "~/"+remoteName)
}
```

- [ ] **Step 4: Run to verify the test passes, and that `up`'s existing behavior is unchanged**

Run: `nix develop --command go test ./internal/lifecycle/... -v`
Expected: PASS, including `TestUp_RunsFullSequenceInOrder` (which exercises `Rsync` via the `Steps` struct) and `TestRsync_CopiesFilesBetweenLocalDirs` (unchanged, still exercises the real binary directly)

- [ ] **Step 5: Wire `sync` into the cobra command**

In `cmd/lookup.go`, add `run: runSync,` to the `sync` entry:

```go
	{
		use:   "sync <local-dir> [remote-dir]",
		short: "One-shot push of a directory outside the repo to the instance",
		verb:  "sync",
		args:  cobra.RangeArgs(1, 2),
		named: false,
		run:   runSync,
	},
```

Add `"path/filepath"` to `cmd/lookup_run.go`'s import block, and append:

```go
// defaultRemoteDir returns the instance-side directory sync uses when
// no remote-dir is given: ~/<basename(local)>.
func defaultRemoteDir(local string) string {
	return "~/" + filepath.Base(filepath.Clean(local))
}

func runSync(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}

	local := args[0]
	remote := defaultRemoteDir(local)
	if len(args) > 1 {
		remote = args[1]
	}

	if err := lifecycle.Push(cmd.Context(), record.IP, local, remote); err != nil {
		return err
	}
	cmd.Printf("Synced %s to %s:%s\n", local, name, remote)
	return nil
}
```

- [ ] **Step 6: Write the failing test for `defaultRemoteDir`**

Create `cmd/lookup_run_test.go`:

```go
package cmd

import "testing"

func TestDefaultRemoteDir_UsesBasenameUnderHome(t *testing.T) {
	cases := map[string]string{
		"./dataset":        "~/dataset",
		"/abs/path/models":  "~/models",
		"relative/nested":   "~/nested",
	}
	for local, want := range cases {
		if got := defaultRemoteDir(local); got != want {
			t.Errorf("defaultRemoteDir(%q) = %q, want %q", local, got, want)
		}
	}
}
```

(This step is written after Step 5's implementation exists, so there's no red step here — `defaultRemoteDir` already exists by the time this test is added. Run it once to confirm it passes.)

Run: `nix develop --command go test ./cmd/... -run TestDefaultRemoteDir -v`
Expected: PASS

- [ ] **Step 7: Update sync's rows and restructure the name-flag-wins test**

In `cmd/lookup_test.go`'s `TestLookupCommands_NameFlagResolves` cases, change:

```go
		{[]string{"sync", "./data", "--name", "myrepo"}, "sync", "sync: not implemented yet"},
```

to:

```go
		{[]string{"sync", "./data", "--name", "myrepo"}, "sync", `no instance named "myrepo"`},
```

Replace `TestLookupCommands_NameFlagWinsOverLeadingPathArg` in full so each row carries its own expected substring (Task 5 will update only the `download` row):

```go
func TestLookupCommands_NameFlagWinsOverLeadingPathArg(t *testing.T) {
	cases := []struct {
		args []string
		verb string
		want string
	}{
		{[]string{"sync", "./data", "--name", "myrepo"}, "sync", `no instance named "myrepo"`},
		{[]string{"download", "./results", "--name", "myrepo"}, "download", `instance "myrepo"`},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
			root := newRootCmd()
			root.SetArgs(tc.args)
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)

			err := root.Execute()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
		})
	}
}
```

- [ ] **Step 8: Run the full test suite**

Run: `nix develop --command go build ./... && nix develop --command go test ./... 2>&1 | grep -v pkl-go`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/lifecycle/rsync.go internal/lifecycle/rsync_test.go cmd/lookup.go cmd/lookup_run.go cmd/lookup_run_test.go cmd/lookup_test.go
git commit -m "Add sync command, generalize Rsync into Push"
```

---

### Task 5: `download`

**Files:**
- Modify: `internal/lifecycle/rsync.go` (add `Pull`)
- Modify: `internal/lifecycle/rsync_test.go`
- Modify: `cmd/lookup.go` (wire `download`)
- Modify: `cmd/lookup_run.go` (add `runDownload`, `defaultLocalDir`)
- Modify: `cmd/lookup_run_test.go`
- Modify: `cmd/lookup_test.go` (download's rows in both tables)

**Interfaces:**
- Consumes: `resolveInstance` from Task 1, the `Push`/`rsyncPushArgs` pattern from Task 4 (mirrored for the pull direction).
- Produces: `rsyncPullArgs(ip, remote, local string) []string` and `Pull(ctx context.Context, ip, remote, local string) error` (package `lifecycle`). `defaultLocalDir(remote string) string` (package `cmd`).

- [ ] **Step 1: Write the failing test for the pull argv builder**

Append to `internal/lifecycle/rsync_test.go`:

```go
func TestRsyncPullArgs_BuildsExpectedCommand(t *testing.T) {
	got := rsyncPullArgs("203.0.113.5", "~/results", "/home/user/results")
	want := []string{"-az", "-e", "ssh", "root@203.0.113.5:~/results/", "/home/user/results/"}
	if len(got) != len(want) {
		t.Fatalf("rsyncPullArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rsyncPullArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `nix develop --command go test ./internal/lifecycle/... -run TestRsyncPullArgs -v`
Expected: FAIL with `undefined: rsyncPullArgs`

- [ ] **Step 3: Add `Pull`**

Append to `internal/lifecycle/rsync.go`:

```go
// rsyncPullArgs builds the argv for copying ip's remote directory
// remote (already the full path) back to local.
func rsyncPullArgs(ip, remote, local string) []string {
	src := fmt.Sprintf("root@%s:%s/", ip, remote)
	dst := local + "/"
	return []string{"-az", "-e", "ssh", src, dst}
}

// Pull copies ip's remote directory remote back to local.
func Pull(ctx context.Context, ip, remote, local string) error {
	if _, err := exec.LookPath("rsync"); err != nil {
		return fmt.Errorf("rsync not found on PATH (run inside `nix develop`, or install it): %w", err)
	}
	cmd := exec.CommandContext(ctx, "rsync", rsyncPullArgs(ip, remote, local)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rsync from %s failed: %w\n%s", ip, err, out)
	}
	return nil
}
```

- [ ] **Step 4: Run to verify the test passes**

Run: `nix develop --command go test ./internal/lifecycle/... -run TestRsyncPullArgs -v`
Expected: PASS

- [ ] **Step 5: Wire `download` into the cobra command**

In `cmd/lookup.go`, add `run: runDownload,` to the `download` entry:

```go
	{
		use:   "download <remote-dir> [local-dir]",
		short: "One-shot pull of files back from the instance",
		verb:  "download",
		args:  cobra.RangeArgs(1, 2),
		named: false,
		run:   runDownload,
	},
```

Add `"path"` to `cmd/lookup_run.go`'s import block, and append:

```go
// defaultLocalDir returns the local directory download uses when no
// local-dir is given: ./<basename(remote)>. remote is always a POSIX
// path (the instance is always Linux), so path.Base is used rather
// than filepath.Base.
func defaultLocalDir(remote string) string {
	return "./" + path.Base(path.Clean(remote))
}

func runDownload(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}

	remote := args[0]
	local := defaultLocalDir(remote)
	if len(args) > 1 {
		local = args[1]
	}

	if err := lifecycle.Pull(cmd.Context(), record.IP, remote, local); err != nil {
		return err
	}
	cmd.Printf("Downloaded %s:%s to %s\n", name, remote, local)
	return nil
}
```

- [ ] **Step 6: Write the failing test for `defaultLocalDir`**

Append to `cmd/lookup_run_test.go`:

```go
func TestDefaultLocalDir_UsesBasenameInCwd(t *testing.T) {
	cases := map[string]string{
		"~/results":       "./results",
		"/root/dataset":   "./dataset",
		"~/nested/output": "./output",
	}
	for remote, want := range cases {
		if got := defaultLocalDir(remote); got != want {
			t.Errorf("defaultLocalDir(%q) = %q, want %q", remote, got, want)
		}
	}
}
```

Run: `nix develop --command go test ./cmd/... -run TestDefaultLocalDir -v`
Expected: PASS

- [ ] **Step 7: Update download's rows**

In `cmd/lookup_test.go`'s `TestLookupCommands_NameFlagResolves` cases, change:

```go
		{[]string{"download", "./results", "--name", "myrepo"}, "download", "download: not implemented yet"},
```

to:

```go
		{[]string{"download", "./results", "--name", "myrepo"}, "download", `no instance named "myrepo"`},
```

In `TestLookupCommands_NameFlagWinsOverLeadingPathArg` (restructured in Task 4), change the `download` row:

```go
		{[]string{"download", "./results", "--name", "myrepo"}, "download", `instance "myrepo"`},
```

to:

```go
		{[]string{"download", "./results", "--name", "myrepo"}, "download", `no instance named "myrepo"`},
```

- [ ] **Step 8: Run the full test suite**

Run: `nix develop --command go build ./... && nix develop --command go test ./... 2>&1 | grep -v pkl-go`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/lifecycle/rsync.go internal/lifecycle/rsync_test.go cmd/lookup.go cmd/lookup_run.go cmd/lookup_run_test.go cmd/lookup_test.go
git commit -m "Add download command"
```

---

### Task 6: `ssh`

**Files:**
- Create: `internal/lifecycle/ssh.go`
- Create: `internal/lifecycle/ssh_test.go`
- Modify: `cmd/lookup.go` (wire `ssh`)
- Modify: `cmd/lookup_run.go` (add `runSSH`)
- Modify: `cmd/lookup_test.go` (ssh's row, and `TestLookupCommands_PositionalNameResolves`)

**Interfaces:**
- Consumes: `resolveInstance` from Task 1.
- Produces: `sshArgs(ip string) []string` and `SSH(ctx context.Context, ip string) error` (package `lifecycle`).

- [ ] **Step 1: Write the failing test for the argv builder**

Create `internal/lifecycle/ssh_test.go`:

```go
package lifecycle

import "testing"

func TestSSHArgs_BuildsExpectedCommand(t *testing.T) {
	got := sshArgs("203.0.113.5")
	want := []string{"root@203.0.113.5"}
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

- [ ] **Step 2: Run to verify it fails**

Run: `nix develop --command go test ./internal/lifecycle/... -run TestSSHArgs -v`
Expected: FAIL with `undefined: sshArgs`

- [ ] **Step 3: Implement `SSH`**

Create `internal/lifecycle/ssh.go`:

```go
package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// sshArgs builds the argv SSH passes to the ssh binary: an
// interactive session as root on ip's default port.
func sshArgs(ip string) []string {
	return []string{"root@" + ip}
}

// SSH opens an interactive session on the instance at ip, execing the
// real ssh binary with stdio passed straight through -- no PTY/raw-mode
// handling of our own. Reuses whatever trust-on-first-connect entry
// already exists in the user's real ~/.ssh/known_hosts from up's
// WaitReady/Connect call.
func SSH(ctx context.Context, ip string) error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh not found on PATH: %w", err)
	}
	cmd := exec.CommandContext(ctx, "ssh", sshArgs(ip)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
```

- [ ] **Step 4: Run to verify the test passes**

Run: `nix develop --command go test ./internal/lifecycle/... -run TestSSHArgs -v`
Expected: PASS

- [ ] **Step 5: Wire `ssh` into the cobra command**

In `cmd/lookup.go`, add `run: runSSH,` to the `ssh` entry:

```go
	{
		use:   "ssh [name]",
		short: "Open an interactive remote shell on the instance",
		verb:  "ssh",
		args:  cobra.MaximumNArgs(1),
		named: true,
		run:   runSSH,
	},
```

Append to `cmd/lookup_run.go`:

```go
func runSSH(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	return lifecycle.SSH(cmd.Context(), record.IP)
}
```

- [ ] **Step 6: Update ssh's rows**

In `cmd/lookup_test.go`'s `TestLookupCommands_NameFlagResolves` cases, change:

```go
		{[]string{"ssh", "--name", "myrepo"}, "ssh", "ssh: not implemented yet"},
```

to:

```go
		{[]string{"ssh", "--name", "myrepo"}, "ssh", `no instance named "myrepo"`},
```

Replace `TestLookupCommands_PositionalNameResolves` in full (it hardcodes `ssh`, which is now real, so its assertion changes from the old stub-error shape to the shared not-found error):

```go
func TestLookupCommands_PositionalNameResolves(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	root := newRootCmd()
	root.SetArgs([]string{"ssh", "myrepo"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `no instance named "myrepo"`) {
		t.Errorf("error = %q, want it to name instance %q", err.Error(), "myrepo")
	}
}
```

- [ ] **Step 7: Run the full test suite**

Run: `nix develop --command go build ./... && nix develop --command go test ./... 2>&1 | grep -v pkl-go`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/lifecycle/ssh.go internal/lifecycle/ssh_test.go cmd/lookup.go cmd/lookup_run.go cmd/lookup_test.go
git commit -m "Add ssh command"
```

---

## Final Verification

After Task 6, run the whole suite once more end to end:

```bash
nix develop --command go build ./...
nix develop --command go vet ./...
nix develop --command go test ./... 2>&1 | grep -v pkl-go
nix flake check --print-build-logs
```

All six commands (`down`, `ssh`, `status`, `watch`, `sync`, `download`) should now fail with `no instance named "X" (run cloudlab up first)` when run against an unknown name, and succeed against a real instance created by `up`. `shell` and `connect` remain `"not implemented yet"`, unchanged.
