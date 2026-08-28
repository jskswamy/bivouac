# Foundation: CLI Scaffold, Instance Identity, State Store Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up cloudlab's foundational plumbing — a Cobra CLI with all ten commands from ADR-0007 registered, git-derived instance identity resolution (ADR-0003, refined), and a JSON-backed instance state store — so every later phase (provider, provisioning, real command behavior) has something to build on.

**Architecture:** `cmd/` holds a Cobra command tree built via constructor functions (`newXCmd()`), assembled by `newRootCmd()`. `internal/identity/` resolves git repo root and instance names via two independent, composable functions. `internal/state/` is a small JSON-backed key-value store over the XDG state path. Every per-instance command resolves identity through one of two paths — repo-dependent (`up` only) or lookup-only (everything else) — before doing anything else; only `list` has real behavior this phase, every other command stub-errors with `"<verb>: not implemented yet"` once identity resolves successfully.

**Tech Stack:** Go, `github.com/spf13/cobra` (no Viper — see spec), Go standard library only otherwise (`encoding/json`, `os/exec`, `testing`).

## Global Constraints

- Module path: `github.com/jskswamy/cloudlab`
- Go version: 1.22 or later
- CLI framework: Cobra only, no Viper
- State file location: `$XDG_STATE_HOME/cloudlab/state.json` if set, else `~/.local/state/cloudlab/state.json` — same shape on macOS as Linux, not `os.UserConfigDir()`'s macOS-specific default
- State file format: JSON
- Command shape: flat verbs, no noun grouping (`cloudlab up`, never `cloudlab instance up`)
- All ten commands from ADR-0007 are registered this phase; only `list` has real behavior. Every other command resolves identity for real, then returns the error `"<verb>: not implemented yet"` (optionally with more detail appended)
- Cobra commands are built via `newXCmd() *cobra.Command` constructor functions, not package-level `var xCmd = &cobra.Command{...}` + `init()` — this keeps every test able to build a fresh, isolated command tree instead of sharing mutable global flag state across tests

---

### Task 1: CLI scaffold and root command

**Files:**
- Create: `go.mod`, `go.sum`
- Create: `main.go`
- Create: `cmd/root.go`
- Test: `cmd/root_test.go`

**Interfaces:**
- Produces: `cmd.Execute() error` (used by `main.go`); `cmd.newRootCmd() *cobra.Command` (unexported, used by every later `cmd` package test and by `Execute`)

- [ ] **Step 1: Initialize the Go module**

Run: `go mod init github.com/jskswamy/cloudlab`
Expected: creates `go.mod` with `module github.com/jskswamy/cloudlab` and `go 1.22` (adjust the `go` directive to `1.22` if the toolchain writes a newer one).

- [ ] **Step 2: Add the Cobra dependency**

Run: `go get github.com/spf13/cobra@latest`
Expected: `go.mod`/`go.sum` updated with `github.com/spf13/cobra` and its transitive deps (`pflag`, etc.).

- [ ] **Step 3: Write the failing test**

Create `cmd/root_test.go`:

```go
package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootCommandHelp(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"--help"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "cloudlab") {
		t.Errorf("expected help output to mention cloudlab, got: %s", out.String())
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `go test ./cmd/... -run TestRootCommandHelp -v`
Expected: FAIL — compile error, `undefined: newRootCmd` (package `cmd` has no non-test files yet).

- [ ] **Step 5: Write the minimal implementation**

Create `cmd/root.go`:

```go
package cmd

import "github.com/spf13/cobra"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "cloudlab",
		Short: "Ephemeral, declarative dev instances in the cloud",
	}
	root.PersistentFlags().String("repo", "", "path to the repo to resolve identity from (overrides cwd-based resolution)")
	root.PersistentFlags().String("name", "", "instance name override")
	root.SilenceUsage = true

	return root
}

// Execute builds a fresh command tree and runs it. Called once from main.
func Execute() error {
	return newRootCmd().Execute()
}
```

Create `main.go`:

```go
package main

import (
	"os"

	"github.com/jskswamy/cloudlab/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./cmd/... -run TestRootCommandHelp -v`
Expected: PASS

- [ ] **Step 7: Verify the binary builds and runs**

Run: `go build -o /dev/null . && go run . --help`
Expected: builds cleanly; help output shows `cloudlab` usage with `--repo`/`--name` flags and no subcommands yet.

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum main.go cmd/root.go cmd/root_test.go
git commit -m "Add CLI scaffold with root command"
```

---

### Task 2: Git-derived repo root resolution

**Files:**
- Create: `internal/identity/identity.go`
- Test: `internal/identity/identity_test.go`

**Interfaces:**
- Produces: `identity.RepoRoot(cwd, repoFlag string) (string, error)`

- [ ] **Step 1: Write the failing tests**

Create `internal/identity/identity_test.go`:

```go
package identity

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func resolved(t *testing.T, dir string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) error = %v", dir, err)
	}
	return p
}

func TestRepoRoot_FromRootDir(t *testing.T) {
	root := initRepo(t)
	got, err := RepoRoot(root, "")
	if err != nil {
		t.Fatalf("RepoRoot() error = %v", err)
	}
	if want := resolved(t, root); got != want {
		t.Errorf("RepoRoot() = %q, want %q", got, want)
	}
}

func TestRepoRoot_FromSubdir(t *testing.T) {
	root := initRepo(t)
	sub := filepath.Join(root, "src", "utils")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := RepoRoot(sub, "")
	if err != nil {
		t.Fatalf("RepoRoot() error = %v", err)
	}
	if want := resolved(t, root); got != want {
		t.Errorf("RepoRoot() = %q, want %q", got, want)
	}
}

func TestRepoRoot_RepoFlagOverridesCwd(t *testing.T) {
	other := initRepo(t)
	notARepo := t.TempDir()
	got, err := RepoRoot(notARepo, other)
	if err != nil {
		t.Fatalf("RepoRoot() error = %v", err)
	}
	if want := resolved(t, other); got != want {
		t.Errorf("RepoRoot() = %q, want %q", got, want)
	}
}

func TestRepoRoot_RepoFlagWalksUpFromSubdir(t *testing.T) {
	other := initRepo(t)
	sub := filepath.Join(other, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := RepoRoot(t.TempDir(), sub)
	if err != nil {
		t.Fatalf("RepoRoot() error = %v", err)
	}
	if want := resolved(t, other); got != want {
		t.Errorf("RepoRoot() = %q, want %q", got, want)
	}
}

func TestRepoRoot_NotAGitRepoNoFlag(t *testing.T) {
	_, err := RepoRoot(t.TempDir(), "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "use --repo") {
		t.Errorf("error = %q, want mention of --repo", err.Error())
	}
}

func TestRepoRoot_NotAGitRepoWithFlag(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "nope")
	_, err := RepoRoot(t.TempDir(), bad)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), bad) {
		t.Errorf("error = %q, want it to name %q", err.Error(), bad)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/identity/... -v`
Expected: FAIL — compile error, `undefined: RepoRoot`.

- [ ] **Step 3: Write the minimal implementation**

Create `internal/identity/identity.go`:

```go
package identity

import (
	"fmt"
	"os/exec"
	"strings"
)

// RepoRoot walks up from repoFlag (if set) or cwd to find a git repo
// root, via `git rev-parse --show-toplevel`. It is used only by commands
// that need actual repo content (currently: up).
func RepoRoot(cwd, repoFlag string) (string, error) {
	start := cwd
	if repoFlag != "" {
		start = repoFlag
	}

	out, err := exec.Command("git", "-C", start, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		if repoFlag != "" {
			return "", fmt.Errorf("not a git repository: %s", repoFlag)
		}
		return "", fmt.Errorf("not inside a git repository; use --repo <path>")
	}
	return strings.TrimSpace(string(out)), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/identity/... -v`
Expected: PASS (all 6 subtests)

- [ ] **Step 5: Commit**

```bash
git add internal/identity/identity.go internal/identity/identity_test.go
git commit -m "Add git-derived repo root resolution"
```

---

### Task 3: Instance name derivation from origin remote

**Files:**
- Modify: `internal/identity/identity.go`
- Modify: `internal/identity/identity_test.go`

**Interfaces:**
- Consumes: nothing from Task 2 directly (calls `git` itself)
- Produces: `identity.DeriveName(root string) (string, error)`

- [ ] **Step 1: Write the failing tests**

Append to `internal/identity/identity_test.go`:

```go
func TestDeriveName_FromHTTPSOrigin(t *testing.T) {
	root := initRepo(t)
	runGit(t, root, "remote", "add", "origin", "https://github.com/jskswamy/cloudlab.git")

	got, err := DeriveName(root)
	if err != nil {
		t.Fatalf("DeriveName() error = %v", err)
	}
	if got != "jskswamy-cloudlab" {
		t.Errorf("DeriveName() = %q, want %q", got, "jskswamy-cloudlab")
	}
}

func TestDeriveName_FromSSHOrigin(t *testing.T) {
	root := initRepo(t)
	runGit(t, root, "remote", "add", "origin", "git@github.com:jskswamy/cloudlab.git")

	got, err := DeriveName(root)
	if err != nil {
		t.Fatalf("DeriveName() error = %v", err)
	}
	if got != "jskswamy-cloudlab" {
		t.Errorf("DeriveName() = %q, want %q", got, "jskswamy-cloudlab")
	}
}

func TestDeriveName_NoOriginFallsBackToFolderName(t *testing.T) {
	root := initRepo(t)

	got, err := DeriveName(root)
	if err != nil {
		t.Fatalf("DeriveName() error = %v", err)
	}
	if want := filepath.Base(root); got != want {
		t.Errorf("DeriveName() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/identity/... -run TestDeriveName -v`
Expected: FAIL — compile error, `undefined: DeriveName`.

- [ ] **Step 3: Write the minimal implementation**

Modify `internal/identity/identity.go` — add `"path/filepath"` to imports, then append:

```go
// DeriveName derives an instance name from a resolved repo root: the
// slugified owner/repo from its origin remote, or the root folder's name
// if there's no origin remote configured.
func DeriveName(root string) (string, error) {
	out, err := exec.Command("git", "-C", root, "remote", "get-url", "origin").Output()
	if err != nil {
		return filepath.Base(root), nil
	}
	return slugify(string(out)), nil
}

// slugify turns a git remote URL (https://host/owner/repo.git,
// git@host:owner/repo.git, or ssh://git@host/owner/repo.git) into
// "owner-repo" by taking the last two path segments.
func slugify(remoteURL string) string {
	url := strings.TrimSuffix(strings.TrimSpace(remoteURL), ".git")
	tokens := strings.FieldsFunc(url, func(r rune) bool {
		return r == '/' || r == ':'
	})
	if len(tokens) < 2 {
		return strings.ToLower(strings.Join(tokens, "-"))
	}
	last := tokens[len(tokens)-2:]
	return strings.ToLower(strings.Join(last, "-"))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/identity/... -v`
Expected: PASS (all subtests, including Task 2's)

- [ ] **Step 5: Commit**

```bash
git add internal/identity/identity.go internal/identity/identity_test.go
git commit -m "Add instance name derivation from origin remote"
```

---

### Task 4: Instance name resolution for lookup commands

**Files:**
- Modify: `internal/identity/identity.go`
- Modify: `internal/identity/identity_test.go`

**Interfaces:**
- Consumes: `identity.RepoRoot` (Task 2), `identity.DeriveName` (Task 3)
- Produces: `identity.InstanceName(cwd, repoFlag, positional, nameFlag string) (string, error)`

- [ ] **Step 1: Write the failing tests**

Append to `internal/identity/identity_test.go`:

```go
func TestInstanceName_PositionalWins(t *testing.T) {
	got, err := InstanceName(t.TempDir(), "", "explicit-name", "flag-name")
	if err != nil {
		t.Fatalf("InstanceName() error = %v", err)
	}
	if got != "explicit-name" {
		t.Errorf("InstanceName() = %q, want %q", got, "explicit-name")
	}
}

func TestInstanceName_NameFlagWinsOverCwd(t *testing.T) {
	root := initRepo(t)
	got, err := InstanceName(root, "", "", "flag-name")
	if err != nil {
		t.Fatalf("InstanceName() error = %v", err)
	}
	if got != "flag-name" {
		t.Errorf("InstanceName() = %q, want %q", got, "flag-name")
	}
}

func TestInstanceName_DerivedFromCwdRepo(t *testing.T) {
	root := initRepo(t)
	runGit(t, root, "remote", "add", "origin", "https://github.com/jskswamy/cloudlab.git")

	got, err := InstanceName(root, "", "", "")
	if err != nil {
		t.Fatalf("InstanceName() error = %v", err)
	}
	if got != "jskswamy-cloudlab" {
		t.Errorf("InstanceName() = %q, want %q", got, "jskswamy-cloudlab")
	}
}

func TestInstanceName_DerivedFromRepoFlagOutsideAnyRepo(t *testing.T) {
	other := initRepo(t)
	runGit(t, other, "remote", "add", "origin", "https://github.com/jskswamy/cloudlab.git")

	got, err := InstanceName(t.TempDir(), other, "", "")
	if err != nil {
		t.Fatalf("InstanceName() error = %v", err)
	}
	if got != "jskswamy-cloudlab" {
		t.Errorf("InstanceName() = %q, want %q", got, "jskswamy-cloudlab")
	}
}

func TestInstanceName_ErrorsWithNothingAvailable(t *testing.T) {
	_, err := InstanceName(t.TempDir(), "", "", "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no instance name given") {
		t.Errorf("error = %q, want mention of 'no instance name given'", err.Error())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/identity/... -run TestInstanceName -v`
Expected: FAIL — compile error, `undefined: InstanceName`.

- [ ] **Step 3: Write the minimal implementation**

Append to `internal/identity/identity.go`:

```go
// InstanceName resolves a name for lookup-only commands (everything
// except up): positional arg, then --name, then (if cwd or repoFlag is
// inside a git repo) that repo's derived name. Unlike RepoRoot, it
// succeeds without any git repo present as long as positional or
// nameFlag is given — lookup commands only need a name to find an
// already-existing instance in state, they never touch repo content.
func InstanceName(cwd, repoFlag, positional, nameFlag string) (string, error) {
	if positional != "" {
		return positional, nil
	}
	if nameFlag != "" {
		return nameFlag, nil
	}

	root, err := RepoRoot(cwd, repoFlag)
	if err != nil {
		return "", fmt.Errorf("no instance name given; use --name or run from inside a repo")
	}
	return DeriveName(root)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/identity/... -v`
Expected: PASS (all subtests)

- [ ] **Step 5: Commit**

```bash
git add internal/identity/identity.go internal/identity/identity_test.go
git commit -m "Add instance name resolution for lookup commands"
```

---

### Task 5: JSON-backed instance state store

**Files:**
- Create: `internal/state/state.go`
- Test: `internal/state/state_test.go`

**Interfaces:**
- Consumes: none from earlier tasks
- Produces: `state.Record` struct; `state.Open() (*Store, error)`; `(*Store).List() ([]Record, error)`; `(*Store).Get(name string) (Record, bool, error)`; `(*Store).Put(r Record) error`; `(*Store).Delete(name string) error`

- [ ] **Step 1: Write the failing tests**

Create `internal/state/state_test.go`:

```go
package state

import (
	"os"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, err := Open()
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return s
}

func TestList_EmptyWhenFileMissing(t *testing.T) {
	s := openTestStore(t)
	records, err := s.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 0 {
		t.Errorf("List() = %v, want empty", records)
	}
}

func TestPutGetList_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	rec := Record{Name: "myrepo", Provider: "digitalocean", VMID: "123", IP: "1.2.3.4"}

	if err := s.Put(rec); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	got, ok, err := s.Get("myrepo")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok {
		t.Fatal("Get() ok = false, want true")
	}
	if got != rec {
		t.Errorf("Get() = %+v, want %+v", got, rec)
	}

	all, err := s.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(all) != 1 || all[0] != rec {
		t.Errorf("List() = %+v, want [%+v]", all, rec)
	}
}

func TestDelete_RemovesRecord(t *testing.T) {
	s := openTestStore(t)
	if err := s.Put(Record{Name: "myrepo"}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if err := s.Delete("myrepo"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	_, ok, err := s.Get("myrepo")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if ok {
		t.Error("Get() ok = true after Delete, want false")
	}
}

func TestList_CorruptFileErrors(t *testing.T) {
	s := openTestStore(t)
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := s.List(); err == nil {
		t.Fatal("expected error, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/state/... -v`
Expected: FAIL — compile error, `undefined: Store`/`Record`/`Open`.

- [ ] **Step 3: Write the minimal implementation**

Create `internal/state/state.go`:

```go
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Record is one instance's state: which provider created it, its VM and
// network details, and the PIDs of its background sync/tunnel processes.
type Record struct {
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	VMID      string `json:"vm_id"`
	IP        string `json:"ip"`
	Region    string `json:"region"`
	Size      string `json:"size"`
	Template  string `json:"template"`
	WatchPID  int    `json:"watch_pid"`
	TunnelPID int    `json:"tunnel_pid"`
}

// Store is a JSON-backed key-value store of instance Records, keyed by
// name, at the XDG state path.
type Store struct {
	path string
}

// Open resolves the state file path ($XDG_STATE_HOME/cloudlab/state.json,
// else ~/.local/state/cloudlab/state.json on every OS) without requiring
// the file to exist yet.
func Open() (*Store, error) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return &Store{path: filepath.Join(dir, "cloudlab", "state.json")}, nil
}

// List returns every stored Record, sorted by name. An empty slice (not
// an error) is returned if the state file doesn't exist yet.
func (s *Store) List() ([]Record, error) {
	records, err := s.all()
	if err != nil {
		return nil, err
	}
	result := make([]Record, 0, len(records))
	for _, r := range records {
		result = append(result, r)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// Get looks up a single Record by name.
func (s *Store) Get(name string) (Record, bool, error) {
	records, err := s.all()
	if err != nil {
		return Record{}, false, err
	}
	r, ok := records[name]
	return r, ok, nil
}

// Put creates or replaces the Record for r.Name.
func (s *Store) Put(r Record) error {
	records, err := s.all()
	if err != nil {
		return err
	}
	records[r.Name] = r
	return s.save(records)
}

// Delete removes the Record for name, if present.
func (s *Store) Delete(name string) error {
	records, err := s.all()
	if err != nil {
		return err
	}
	delete(records, name)
	return s.save(records)
}

func (s *Store) all() (map[string]Record, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return map[string]Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	var records map[string]Record
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("corrupt state file %s: %w", s.path, err)
	}
	return records, nil
}

func (s *Store) save(records map[string]Record) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/state/... -v`
Expected: PASS (all 4 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/state/state.go internal/state/state_test.go
git commit -m "Add JSON-backed instance state store"
```

---

### Task 6: Wire the `list` command to the state store

**Files:**
- Create: `cmd/list.go`
- Modify: `cmd/root.go`
- Test: `cmd/list_test.go`

**Interfaces:**
- Consumes: `state.Open`, `(*state.Store).List` (Task 5); `newRootCmd()` (Task 1)
- Produces: `newListCmd() *cobra.Command`

- [ ] **Step 1: Write the failing tests**

Create `cmd/list_test.go`:

```go
package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/state"
)

func TestListCommand_NoInstances(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	root := newRootCmd()
	root.SetArgs([]string{"list"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "no instances") {
		t.Errorf("output = %q, want mention of 'no instances'", out.String())
	}
}

func TestListCommand_WithInstances(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	store, err := state.Open()
	if err != nil {
		t.Fatalf("state.Open() error = %v", err)
	}
	if err := store.Put(state.Record{Name: "myrepo", Provider: "digitalocean", IP: "1.2.3.4"}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"list"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "myrepo") {
		t.Errorf("output = %q, want it to mention %q", out.String(), "myrepo")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/... -run TestListCommand -v`
Expected: FAIL — `cloudlab list` is an unknown command (not yet registered).

- [ ] **Step 3: Write the minimal implementation**

Create `cmd/list.go`:

```go
package cmd

import (
	"fmt"

	"github.com/jskswamy/cloudlab/internal/state"
	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all instances across all repos",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := state.Open()
			if err != nil {
				return err
			}
			records, err := store.List()
			if err != nil {
				return err
			}
			if len(records) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no instances")
				return nil
			}
			for _, r := range records {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", r.Name, r.Provider, r.IP)
			}
			return nil
		},
	}
}
```

Modify `cmd/root.go` — add `newListCmd()` to `newRootCmd()`, right before the `return root`:

```go
	root.AddCommand(newListCmd())

	return root
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/... -v`
Expected: PASS (all tests in the package)

- [ ] **Step 5: Commit**

```bash
git add cmd/list.go cmd/root.go cmd/list_test.go
git commit -m "Wire list command to the state store"
```

---

### Task 7: `up` command with repo-dependent identity

**Files:**
- Create: `cmd/up.go`
- Modify: `cmd/root.go`
- Test: `cmd/up_test.go`

**Interfaces:**
- Consumes: `identity.RepoRoot`, `identity.DeriveName` (Tasks 2–3); `newRootCmd()` (Task 1)
- Produces: `newUpCmd() *cobra.Command`; test helpers `chdir(t, dir)` and `initTestRepo(t) string` in the `cmd` package, reused by Task 8

- [ ] **Step 1: Write the failing tests**

Create `cmd/up_test.go`:

```go
package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// chdir switches the process's working directory to dir for the
// duration of the test, restoring it afterward.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// initTestRepo creates a fresh git repo in a temp dir and returns its path.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
	return dir
}

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

func TestUpCommand_StubsInsideRepo(t *testing.T) {
	chdir(t, initTestRepo(t))

	root := newRootCmd()
	root.SetArgs([]string{"up"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "up: not implemented yet") {
		t.Errorf("error = %q, want 'up: not implemented yet'", err.Error())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/... -run TestUpCommand -v`
Expected: FAIL — `cloudlab up` is an unknown command.

- [ ] **Step 3: Write the minimal implementation**

Create `cmd/up.go`:

```go
package cmd

import (
	"fmt"
	"os"

	"github.com/jskswamy/cloudlab/internal/identity"
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

			return fmt.Errorf("up: not implemented yet (instance %q, repo %q)", name, root)
		},
	}
	cmd.Flags().String("template", "python", "provisioning template (python or docker)")
	return cmd
}
```

Modify `cmd/root.go` — add `newUpCmd()` alongside `newListCmd()`:

```go
	root.AddCommand(newListCmd())
	root.AddCommand(newUpCmd())

	return root
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/... -v`
Expected: PASS (all tests in the package)

- [ ] **Step 5: Commit**

```bash
git add cmd/up.go cmd/root.go cmd/up_test.go
git commit -m "Add up command with repo-dependent identity"
```

---

### Task 8: Remaining lookup-only stub commands

**Files:**
- Create: `cmd/lookup.go`
- Create: `cmd/shell.go`, `cmd/ssh.go`, `cmd/sync.go`, `cmd/download.go`, `cmd/watch.go`, `cmd/connect.go`, `cmd/status.go`, `cmd/down.go`
- Modify: `cmd/root.go`
- Test: `cmd/lookup_test.go`

**Interfaces:**
- Consumes: `identity.InstanceName` (Task 4); `newRootCmd()` (Task 1); `chdir` test helper (Task 7)
- Produces: `resolveLookupIdentity(cmd *cobra.Command, positional string) (string, error)`; `stubErr(verb, name string) error`; `newShellCmd`, `newSSHCmd`, `newSyncCmd`, `newDownloadCmd`, `newWatchCmd`, `newConnectCmd`, `newStatusCmd`, `newDownCmd` (all `*cobra.Command`)

- [ ] **Step 1: Write the failing tests**

Create `cmd/lookup_test.go`:

```go
package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestLookupCommands_NameFlagResolves(t *testing.T) {
	cases := []struct {
		args []string
		verb string
	}{
		{[]string{"shell", "--name", "myrepo"}, "shell"},
		{[]string{"ssh", "--name", "myrepo"}, "ssh"},
		{[]string{"watch", "--name", "myrepo"}, "watch"},
		{[]string{"connect", "--name", "myrepo"}, "connect"},
		{[]string{"status", "--name", "myrepo"}, "status"},
		{[]string{"down", "--name", "myrepo"}, "down"},
		{[]string{"sync", "./data", "--name", "myrepo"}, "sync"},
		{[]string{"download", "./results", "--name", "myrepo"}, "download"},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			root := newRootCmd()
			root.SetArgs(tc.args)
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)

			err := root.Execute()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			want := tc.verb + ": not implemented yet"
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), want)
			}
		})
	}
}

func TestLookupCommands_PositionalNameResolves(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"ssh", "myrepo"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `instance "myrepo"`) {
		t.Errorf("error = %q, want it to name instance %q", err.Error(), "myrepo")
	}
}

func TestLookupCommands_ErrorOutsideRepoNoName(t *testing.T) {
	chdir(t, t.TempDir())

	root := newRootCmd()
	root.SetArgs([]string{"status"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no instance name given") {
		t.Errorf("error = %q, want mention of 'no instance name given'", err.Error())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/... -run TestLookupCommands -v`
Expected: FAIL — `shell`/`ssh`/etc. are unknown commands.

- [ ] **Step 3: Write the minimal implementation**

Create `cmd/lookup.go`:

```go
package cmd

import (
	"fmt"
	"os"

	"github.com/jskswamy/cloudlab/internal/identity"
	"github.com/spf13/cobra"
)

// resolveLookupIdentity resolves an instance name for commands that only
// need to look up an already-existing instance in state, never repo
// content. positional is the instance-name positional arg if the command
// has one ("" for sync/download, whose positionals are paths).
func resolveLookupIdentity(cmd *cobra.Command, positional string) (string, error) {
	repoFlag, _ := cmd.Flags().GetString("repo")
	nameFlag, _ := cmd.Flags().GetString("name")

	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return identity.InstanceName(cwd, repoFlag, positional, nameFlag)
}

func stubErr(verb, name string) error {
	return fmt.Errorf("%s: not implemented yet (instance %q)", verb, name)
}
```

Create `cmd/shell.go`:

```go
package cmd

import "github.com/spf13/cobra"

func newShellCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "shell [name]",
		Short: "Reconcile home-manager, then open a local subshell with instance envs injected",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			positional := ""
			if len(args) > 0 {
				positional = args[0]
			}
			name, err := resolveLookupIdentity(cmd, positional)
			if err != nil {
				return err
			}
			return stubErr("shell", name)
		},
	}
}
```

Create `cmd/ssh.go`:

```go
package cmd

import "github.com/spf13/cobra"

func newSSHCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh [name]",
		Short: "Open an interactive remote shell on the instance",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			positional := ""
			if len(args) > 0 {
				positional = args[0]
			}
			name, err := resolveLookupIdentity(cmd, positional)
			if err != nil {
				return err
			}
			return stubErr("ssh", name)
		},
	}
}
```

Create `cmd/watch.go`:

```go
package cmd

import "github.com/spf13/cobra"

func newWatchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "watch [name]",
		Short: "Restart continuous two-way repo sync if it's stopped or dead",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			positional := ""
			if len(args) > 0 {
				positional = args[0]
			}
			name, err := resolveLookupIdentity(cmd, positional)
			if err != nil {
				return err
			}
			return stubErr("watch", name)
		},
	}
}
```

Create `cmd/connect.go`:

```go
package cmd

import "github.com/spf13/cobra"

func newConnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "connect [name]",
		Short: "Open a Jupyter tunnel to the instance (python template only)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			positional := ""
			if len(args) > 0 {
				positional = args[0]
			}
			name, err := resolveLookupIdentity(cmd, positional)
			if err != nil {
				return err
			}
			return stubErr("connect", name)
		},
	}
}
```

Create `cmd/status.go`:

```go
package cmd

import "github.com/spf13/cobra"

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [name]",
		Short: "Show instance detail: IP, uptime, cost, sync/watch state",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			positional := ""
			if len(args) > 0 {
				positional = args[0]
			}
			name, err := resolveLookupIdentity(cmd, positional)
			if err != nil {
				return err
			}
			return stubErr("status", name)
		},
	}
}
```

Create `cmd/down.go`:

```go
package cmd

import "github.com/spf13/cobra"

func newDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down [name]",
		Short: "Stop watch, destroy the VM, and clear state",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			positional := ""
			if len(args) > 0 {
				positional = args[0]
			}
			name, err := resolveLookupIdentity(cmd, positional)
			if err != nil {
				return err
			}
			return stubErr("down", name)
		},
	}
}
```

Create `cmd/sync.go`:

```go
package cmd

import "github.com/spf13/cobra"

func newSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync <local-dir> [remote-dir]",
		Short: "One-shot push of a directory outside the repo to the instance",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveLookupIdentity(cmd, "")
			if err != nil {
				return err
			}
			return stubErr("sync", name)
		},
	}
}
```

Create `cmd/download.go`:

```go
package cmd

import "github.com/spf13/cobra"

func newDownloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "download <remote-dir> [local-dir]",
		Short: "One-shot pull of files back from the instance",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveLookupIdentity(cmd, "")
			if err != nil {
				return err
			}
			return stubErr("download", name)
		},
	}
}
```

Modify `cmd/root.go` — register all eight in `newRootCmd()`:

```go
	root.AddCommand(newListCmd())
	root.AddCommand(newUpCmd())
	root.AddCommand(newShellCmd())
	root.AddCommand(newSSHCmd())
	root.AddCommand(newSyncCmd())
	root.AddCommand(newDownloadCmd())
	root.AddCommand(newWatchCmd())
	root.AddCommand(newConnectCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newDownCmd())

	return root
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/... -v`
Expected: PASS (all tests, including `TestLookupCommands_NameFlagResolves`'s 8 subtests)

- [ ] **Step 5: Run the full test suite and build**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: builds cleanly, `go vet` reports nothing, all tests across `cmd`, `internal/identity`, `internal/state` pass.

- [ ] **Step 6: Verify the full command surface by hand**

Run: `go run . --help`
Expected: all ten commands (`up`, `shell`, `ssh`, `sync`, `download`, `watch`, `connect`, `status`, `down`, `list`) listed, matching the README's documented UX.

Run: `go run . list`
Expected: `no instances` (fresh XDG state dir, nothing created yet).

- [ ] **Step 7: Commit**

```bash
git add cmd/lookup.go cmd/shell.go cmd/ssh.go cmd/sync.go cmd/download.go \
        cmd/watch.go cmd/connect.go cmd/status.go cmd/down.go \
        cmd/root.go cmd/lookup_test.go
git commit -m "Add remaining lookup-only stub commands"
```
