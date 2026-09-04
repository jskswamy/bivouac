# Remote Path Mirroring, sync/ssh Ergonomics, and Watch Status Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace every flat `~/<name>` remote path with a shared `RemotePath` helper that mirrors a local path under the remote user's home (or uses it as-is when it isn't under local home), wire it into the main repo's location, `cloudlab sync`'s new `--dir`-driven shape, `cloudlab ssh`'s new auto-`cd`, and surface real Mutagen sync/watch state through `cloudlab status`.

**Architecture:** One new pure helper (`lifecycle.RemotePath`) computes remote paths; `Up()` computes it once per instance and stores the result in `state.Record.RepoPath` so later commands (`ssh`) read it instead of recomputing from a local cwd that might not even be the right checkout. `Push` gets `--mkpath` (native rsync flag, handles directory creation) plus a clearer error hint on failure. A new `lifecycle.GetWatchStatus` wraps `mutagen sync list --template=...` for machine-parseable sync/watch state.

**Tech Stack:** Go (cobra CLI, `os/exec`), rsync `--mkpath` (≥3.2.3, confirmed present locally at 3.5.0 and on Ubuntu 24.04's packaged 3.2.7), Mutagen `--template` (Go template output).

## Global Constraints

- `RemotePath`'s home-containment check must be separator-bounded (e.g. via `filepath.Rel` or a prefix check anchored on `filepath.Separator`), never a naive `strings.HasPrefix` — `/Users/subramk2/x` must NOT be treated as under home `/Users/subramk`.
- `cloud-init.sh` creates the remote user via a plain `useradd --create-home` with no `--home-dir` override, so the remote home is always exactly `/home/<remoteUser>` — hardcode that prefix, no need to query it remotely.
- `state.Record.RepoPath` is a new field; instances created before this change decode it as `""`. Treat that the same as "no path known" everywhere it's read (see Task 5) — never an error.
- `GetWatchStatus`'s "no session" case (mutagen's own "did not match any sessions" message) must map to `WatchStatus{Running: false}, nil` — not an error. Any other mutagen failure is a real error.
- Full verification after every task: `nix develop --command bash -c "go build ./... && go vet ./... && golangci-lint run ./... && go test ./..."` must be clean before committing.
- Commits follow this repo's `/commit-tools:commit` convention (classic style, no AI co-author line) — the plan below shows the intended message; use the skill/plugin to actually create the commit rather than raw `git commit`.

---

## Task 1: `lifecycle.RemotePath` helper

**Files:**
- Create: `internal/lifecycle/remotepath.go`
- Create: `internal/lifecycle/remotepath_test.go`

**Interfaces:**
- Produces: `lifecycle.RemotePath(localPath, remoteUser string) (string, error)`, used by every later task in this plan.

- [ ] **Step 1: Write the failing tests**

Create `internal/lifecycle/remotepath_test.go`:

```go
package lifecycle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemotePath_UnderHome_Mirrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	local := filepath.Join(home, "source", "github.com", "jskswamy", "cloudlab")

	got, err := RemotePath(local, "devuser")
	if err != nil {
		t.Fatalf("RemotePath() error = %v", err)
	}
	want := "/home/devuser/source/github.com/jskswamy/cloudlab"
	if got != want {
		t.Errorf("RemotePath() = %q, want %q", got, want)
	}
}

func TestRemotePath_ExactlyHome_ReturnsRemoteHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := RemotePath(home, "devuser")
	if err != nil {
		t.Fatalf("RemotePath() error = %v", err)
	}
	if got != "/home/devuser" {
		t.Errorf("RemotePath() = %q, want %q", got, "/home/devuser")
	}
}

func TestRemotePath_NotUnderHome_UsesAsIs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	local := filepath.Join(t.TempDir(), "elsewhere")

	got, err := RemotePath(local, "devuser")
	if err != nil {
		t.Fatalf("RemotePath() error = %v", err)
	}
	if got != local {
		t.Errorf("RemotePath() = %q, want %q (used as-is)", got, local)
	}
}

func TestRemotePath_SiblingWithSharedPrefix_NotTreatedAsUnderHome(t *testing.T) {
	// A directory like /tmp/xyz/home2/project must NOT be treated as
	// "under" /tmp/xyz/home just because it shares a string prefix --
	// proves the containment check is separator-bounded, not a naive
	// strings.HasPrefix.
	parent := t.TempDir()
	home := filepath.Join(parent, "home")
	sibling := filepath.Join(parent, "home2", "project")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	got, err := RemotePath(sibling, "devuser")
	if err != nil {
		t.Fatalf("RemotePath() error = %v", err)
	}
	if got != sibling {
		t.Errorf("RemotePath() = %q, want %q (sibling dir sharing a string prefix must not be mirrored)", got, sibling)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `nix develop --command go test ./internal/lifecycle/... -run TestRemotePath -v`
Expected: FAIL to compile — `RemotePath` is undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/lifecycle/remotepath.go`:

```go
package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
)

// RemotePath computes where localPath lands on the instance as user:
// if localPath is under the local user's home directory, it's mirrored
// under the remote user's home (e.g. local
// /Users/subramk/source/cloudlab with remote user "subramk" becomes
// /home/subramk/source/cloudlab); otherwise localPath is used as-is,
// the identical absolute path on both ends. cloud-init.sh creates the
// remote user via a plain `useradd --create-home` with no --home-dir
// override, so its home is always exactly /home/<user> -- the standard
// Debian/Ubuntu default.
func RemotePath(localPath, remoteUser string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	local, err := filepath.Abs(localPath)
	if err != nil {
		return "", err
	}
	home = filepath.Clean(home)

	if local == home {
		return "/home/" + remoteUser, nil
	}
	if rel, ok := strings.CutPrefix(local, home+string(filepath.Separator)); ok {
		return "/home/" + remoteUser + "/" + rel, nil
	}
	return local, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `nix develop --command go test ./internal/lifecycle/... -run TestRemotePath -v`
Expected: PASS (all 4 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/lifecycle/remotepath.go internal/lifecycle/remotepath_test.go
```

Use the `/commit-tools:commit` flow:

```
Add RemotePath: mirror local paths under the remote home

Every remote path cloudlab computes today is a flat, disconnected
name (~/<instance-name>, ~/<basename>) with no relationship to where
the thing actually lives locally. RemotePath mirrors a local path
under the remote user's home when it's under the local user's home
(cloud-init.sh's plain `useradd --create-home` always puts that at
/home/<user>), or uses the identical absolute path on both ends
otherwise. Not wired into anything yet -- just the helper and its
tests, including the sibling-directory case that proves the home
containment check is separator-bounded, not a naive string prefix
match.
```

---

## Task 2: `Push` gains `--mkpath` and a clearer failure hint

**Files:**
- Modify: `internal/lifecycle/rsync.go`
- Modify: `internal/lifecycle/rsync_test.go`

**Interfaces:**
- Consumes: nothing from Task 1 (independent).
- Produces: no new exported names — `rsyncPushArgs`/`Push` keep their existing signatures, only their argv/error text changes.

- [ ] **Step 1: Write the failing tests**

In `internal/lifecycle/rsync_test.go`, update `TestRsyncPushArgs_BuildsExpectedCommand` and `TestRsyncPushArgs_IncludesGivenExcludes` to expect `--mkpath`:

```go
func TestRsyncPushArgs_BuildsExpectedCommand(t *testing.T) {
	got := rsyncPushArgs("203.0.113.5", "devuser", "/home/user/myrepo", "~/myrepo", nil)
	want := []string{"-az", "--info=progress2", "--mkpath", "--exclude=.git", "-e", "ssh", "/home/user/myrepo/", "devuser@203.0.113.5:~/myrepo/"}
	if len(got) != len(want) {
		t.Fatalf("rsyncPushArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rsyncPushArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRsyncPushArgs_IncludesGivenExcludes(t *testing.T) {
	got := rsyncPushArgs("203.0.113.5", "devuser", "/home/user/myrepo", "~/myrepo", []string{".gocache/", ".envrc"})
	want := []string{"-az", "--info=progress2", "--mkpath", "--exclude=.git", "--exclude=.gocache/", "--exclude=.envrc", "-e", "ssh", "/home/user/myrepo/", "devuser@203.0.113.5:~/myrepo/"}
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

Update `TestPush_FailureIncludesRsyncOutputInError` to also assert the new hint:

```go
func TestPush_FailureIncludesRsyncOutputInError(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not on PATH")
	}

	// A local dir that doesn't exist makes rsync fail immediately
	// (no network needed) while still writing a real error to stderr,
	// proving Push still captures output for the error message even
	// though it's now also streamed live to the terminal.
	err := Push(context.Background(), "127.0.0.1", "devuser", "/nonexistent/does-not-exist", "~/dest")
	if err == nil {
		t.Fatal("Push() error = nil, want an error for a nonexistent local dir")
	}
	if !strings.Contains(err.Error(), "rsync to 127.0.0.1 failed") {
		t.Errorf("error = %q, want it to name the failure", err.Error())
	}
	if !strings.Contains(err.Error(), "create it and chown it to devuser") {
		t.Errorf("error = %q, want the create-and-chown hint", err.Error())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `nix develop --command go test ./internal/lifecycle/... -run 'TestRsyncPushArgs|TestPush_FailureIncludesRsyncOutputInError' -v`
Expected: FAIL — `--mkpath` missing from actual argv; hint text missing from error.

- [ ] **Step 3: Write the implementation**

In `internal/lifecycle/rsync.go`, update `rsyncPushArgs`:

```go
// rsyncPushArgs builds the argv for copying local's contents to ip's
// remote directory remote (already the full path, e.g. "~/myrepo" or
// "~/dataset" -- callers resolve any default before calling this) as
// user, using the system ssh binary as rsync's remote shell.
// --info=progress2 gives one continuously-updating overall-transfer
// line rather than spamming a line per file. --mkpath creates every
// missing destination path component using rsync's own real
// permission check -- correct whether remote is under the remote
// user's home (always writable) or an arbitrary absolute path (only
// writable if they already own something in that chain).
// --exclude=.git plus one --exclude per entry in excludes (see
// gitIgnoredExcludes) keep local's full history and build caches off
// an ephemeral instance that never needed them.
func rsyncPushArgs(ip, user, local, remote string, excludes []string) []string {
	args := []string{"-az", "--info=progress2", "--mkpath", "--exclude=.git"}
	for _, e := range excludes {
		args = append(args, "--exclude="+e)
	}
	src := local + "/"
	dst := fmt.Sprintf("%s@%s:%s/", user, ip, remote)
	return append(args, "-e", "ssh", src, dst)
}
```

Update `Push`'s error wrap:

```go
// Push copies local's contents to ip's remote directory remote as
// user, streaming rsync's own progress output live to the terminal.
func Push(ctx context.Context, ip, user, local, remote string) error {
	if _, err := exec.LookPath("rsync"); err != nil {
		return fmt.Errorf("rsync not found on PATH (run inside `nix develop`, or install it): %w", err)
	}
	// #nosec G204 -- argv-array exec.Command, no shell; ip is
	// provider-assigned, user/local/remote are local identifiers/path
	// args, none attacker-controlled.
	cmd := exec.CommandContext(ctx, "rsync", rsyncPushArgs(ip, user, local, remote, gitIgnoredExcludes(local))...)
	var buf bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
	cmd.Stderr = io.MultiWriter(os.Stderr, &buf)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rsync to %s failed: %w\n%s\nif %s doesn't exist or isn't writable, create it and chown it to %s on the instance, then retry", ip, err, buf.String(), remote, user)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `nix develop --command go test ./internal/lifecycle/... -v`
Expected: PASS (full package, including the other rsync tests that reference `rsyncPushArgs`'s trailing-4-elements stripping in `TestPush_RespectsGitignoreAndExcludesGitDir` — unaffected since `--mkpath` is added near the front, not the end)

- [ ] **Step 5: Commit**

```bash
git add internal/lifecycle/rsync.go internal/lifecycle/rsync_test.go
```

Use the `/commit-tools:commit` flow:

```
Push: create missing destination paths, clearer failure hint

--mkpath (rsync >= 3.2.3, confirmed present locally at 3.5.0 and on
Ubuntu 24.04's packaged 3.2.7) creates every missing destination path
component using rsync's own real permission check -- no separate SSH
round-trip or --rsync-path shell trick needed, and it's correct
whether the target is under the remote user's home (always writable)
or an arbitrary path (only writable if they already own something in
that chain). When rsync still fails, the wrapped error now always
appends a one-line hint: create the target and chown it to the
remote user, then retry.
```

---

## Task 3: Wire `RemotePath` into the main repo's location

**Files:**
- Modify: `internal/state/state.go`
- Modify: `internal/lifecycle/rsync.go`
- Modify: `internal/lifecycle/watch.go`
- Modify: `internal/lifecycle/lifecycle.go`
- Modify: `internal/lifecycle/rsync_test.go`
- Modify: `internal/lifecycle/watch_test.go`
- Modify: `internal/lifecycle/lifecycle_test.go`

**Interfaces:**
- Consumes: `lifecycle.RemotePath(localPath, remoteUser string) (string, error)` (Task 1).
- Produces: `state.Record.RepoPath string` (new field); `Rsync`/`Steps.Rsync`'s and `StartWatch`/`Steps.StartWatch`'s last parameter(s) now carry a fully-resolved remote path rather than an instance name to flatten.

- [ ] **Step 1: Write the failing tests**

In `internal/lifecycle/rsync_test.go`, `Rsync`'s last parameter changes meaning from "instance name" to "remote path" — update `TestRsync_ReportsProgressBeforeSyncing` to pass a realistic path:

```go
func TestRsync_ReportsProgressBeforeSyncing(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not on PATH")
	}

	var got []string
	ctx := provider.WithProgress(context.Background(), func(status string) { got = append(got, status) })

	// The rsync itself fails fast (no such local dir, no network
	// needed) -- progress is reported before that attempt regardless.
	_ = Rsync(ctx, "127.0.0.1", "devuser", "/nonexistent/does-not-exist", "/home/devuser/myrepo")

	if len(got) == 0 || !strings.Contains(got[0], "repo") {
		t.Errorf("progress = %v, want a first entry mentioning repo", got)
	}
}
```

In `internal/lifecycle/watch_test.go`, `mutagenCreateArgs`/`StartWatch` gain a `remotePath` parameter — update every call site:

```go
func TestMutagenCreateArgs_BuildsExpectedCommand(t *testing.T) {
	got := mutagenCreateArgs("203.0.113.5", "devuser", "myrepo", "/home/user/myrepo", "/home/devuser/source/myrepo")
	want := []string{"sync", "create", "--name=myrepo", "/home/user/myrepo", "devuser@203.0.113.5:/home/devuser/source/myrepo"}
	if len(got) != len(want) {
		t.Fatalf("mutagenCreateArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mutagenCreateArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
```

```go
func TestStartWatch_ReportsProgressBeforeStarting(t *testing.T) {
	var got []string
	ctx := provider.WithProgress(context.Background(), func(status string) { got = append(got, status) })

	// mutagen need not be on PATH: progress is reported before
	// exec.LookPath, so this assertion holds regardless.
	_ = StartWatch(ctx, "203.0.113.5", "devuser", "myrepo", t.TempDir(), "/home/devuser/myrepo")

	if len(got) == 0 || !strings.Contains(got[0], "watch") {
		t.Errorf("progress = %v, want a first entry mentioning watch", got)
	}
}
```

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

	// StartWatch's remote target (devuser@127.0.0.1) has no reachable
	// sshd in a test environment, so this call is expected to fail on
	// the SSH connection -- but that failure must happen AFTER the
	// pre-existing session was already terminated. A failed create
	// leaves no new session behind (confirmed empirically), so zero
	// remaining sessions named name proves the old one is gone.
	_ = StartWatch(context.Background(), "127.0.0.1", "devuser", name, src, "/home/devuser/"+name)

	out, err := exec.Command("mutagen", "sync", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("mutagen sync list: %v\n%s", err, out)
	}
	if strings.Count(string(out), "Name: "+name) != 0 {
		t.Errorf("session %q still present after StartWatch -- terminateWatch did not run before create", name)
	}
}
```

In `internal/lifecycle/lifecycle_test.go`, update `TestUp_RunsFullSequenceInOrder`'s fake `Steps` for the new arities, and assert `record.RepoPath`:

```go
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

	wantUser, err := identity.RemoteUser()
	if err != nil {
		t.Fatalf("identity.RemoteUser() error = %v", err)
	}
	wantRemotePath, err := RemotePath(repoRoot, wantUser)
	if err != nil {
		t.Fatalf("RemotePath() error = %v", err)
	}

	var order []string
	steps := Steps{
		WaitReady: WaitReady,
		Reconcile: reconcile.Reconcile,
		Rsync: func(ctx context.Context, ip, user, localRepoRoot, remotePath string) error {
			order = append(order, "rsync")
			if ip != addr || user != wantUser || localRepoRoot != repoRoot || remotePath != wantRemotePath {
				t.Errorf("Rsync called with (%q, %q, %q, %q)", ip, user, localRepoRoot, remotePath)
			}
			return nil
		},
		StartWatch: func(ctx context.Context, ip, user, name, localRepoRoot, remotePath string) error {
			order = append(order, "watch")
			if ip != addr || user != wantUser || name != "myinstance" || localRepoRoot != repoRoot || remotePath != wantRemotePath {
				t.Errorf("StartWatch called with (%q, %q, %q, %q, %q)", ip, user, name, localRepoRoot, remotePath)
			}
			return nil
		},
	}

	var progress []string
	ctx := provider.WithProgress(context.Background(), func(status string) { progress = append(progress, status) })

	if err := Up(ctx, p, steps, "myinstance", cloudlabPath, repoRoot); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	if len(order) != 2 || order[0] != "rsync" || order[1] != "watch" {
		t.Errorf("call order = %v, want [rsync watch]", order)
	}
	if len(progress) == 0 || !strings.Contains(progress[0], "creating") {
		t.Errorf("progress = %v, want a first entry mentioning creating the instance", progress)
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
	if record.User != wantUser {
		t.Errorf("record.User = %q, want %q", record.User, wantUser)
	}
	if record.RepoPath != wantRemotePath {
		t.Errorf("record.RepoPath = %q, want %q", record.RepoPath, wantRemotePath)
	}

	if p.gotSpec.Name != "myinstance" {
		t.Errorf("InstanceSpec.Name = %q, want %q", p.gotSpec.Name, "myinstance")
	}
	if p.gotSpec.Image != "ubuntu-24-04-x64" {
		t.Errorf("InstanceSpec.Image = %q, want %q", p.gotSpec.Image, "ubuntu-24-04-x64")
	}
	if p.gotSpec.Region != "nyc3" {
		t.Errorf("InstanceSpec.Region = %q, want %q", p.gotSpec.Region, "nyc3")
	}
	if p.gotSpec.Size != "s-1vcpu-1gb" {
		t.Errorf("InstanceSpec.Size = %q, want %q", p.gotSpec.Size, "s-1vcpu-1gb")
	}
}
```

Update `TestUp_StateRecordedBeforeWaitReady`'s fake `Steps.StartWatch` to the new 6-arg shape (its `Rsync` stub's arity is unchanged — still 5 params, only the last param's *meaning* changed, which a no-op stub doesn't care about):

```go
	steps := Steps{
		WaitReady: func(ctx context.Context, ip string, timeout time.Duration) error {
			return errors.New("simulated unreachable")
		},
		Reconcile:  func(ctx context.Context, name, cloudlabPath string) error { return nil },
		Rsync:      func(ctx context.Context, ip, user, localRepoRoot, remotePath string) error { return nil },
		StartWatch: func(ctx context.Context, ip, user, name, localRepoRoot, remotePath string) error { return nil },
	}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `nix develop --command go build ./...`
Expected: FAIL to compile — `Steps.Rsync`/`Steps.StartWatch`'s field types don't match the fake closures' new signatures yet, `mutagenCreateArgs`/`StartWatch`/`Rsync` don't accept the new arg yet, `state.Record` has no `RepoPath` field yet.

- [ ] **Step 3: Write the implementation**

In `internal/state/state.go`, add `RepoPath` to `Record` (after `User`):

```go
type Record struct {
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	VMID      string `json:"vm_id"`
	IP        string `json:"ip"`
	Region    string `json:"region"`
	Size      string `json:"size"`
	Template  string `json:"template"`
	User      string `json:"user"`
	RepoPath  string `json:"repo_path"`
	WatchPID  int    `json:"watch_pid"`
	TunnelPID int    `json:"tunnel_pid"`
}
```

In `internal/lifecycle/rsync.go`, update `Rsync`:

```go
// Rsync copies localRepoRoot's contents to remotePath on the instance
// at ip as user -- up's one-shot initial seed of the repo. A thin
// wrapper over Push with up's own progress reporting.
func Rsync(ctx context.Context, ip, user, localRepoRoot, remotePath string) error {
	provider.ReportProgress(ctx, "syncing repo to instance")
	return Push(ctx, ip, user, localRepoRoot, remotePath)
}
```

In `internal/lifecycle/watch.go`, update `mutagenCreateArgs` and `StartWatch`:

```go
// mutagenCreateArgs builds the argv StartWatch passes to the mutagen
// binary: a named two-way sync session between localRepoRoot and
// remotePath on ip, connecting as user.
func mutagenCreateArgs(ip, user, name, localRepoRoot, remotePath string) []string {
	remote := fmt.Sprintf("%s@%s:%s", user, ip, remotePath)
	return []string{"sync", "create", "--name=" + name, localRepoRoot, remote}
}

// StartWatch starts a continuous two-way sync session between
// localRepoRoot and remotePath on ip (connecting as user), named
// after the instance. Idempotent: any existing session by that name
// is terminated first, so this doubles as a restart for a stopped or
// dead watch. Relies on WaitReady's Connect call having already
// completed trust-on-first-connect against ~/.ssh/known_hosts, since
// Mutagen's own SSH transport is a separate implementation from this
// project's Go SSH client.
func StartWatch(ctx context.Context, ip, user, name, localRepoRoot, remotePath string) error {
	provider.ReportProgress(ctx, "starting continuous watch")
	if _, err := exec.LookPath("mutagen"); err != nil {
		return fmt.Errorf("mutagen not found on PATH (run inside `nix develop`, or install it: https://mutagen.io/documentation/introduction/installation): %w", err)
	}
	terminateWatch(ctx, name)
	// #nosec G204 -- argv-array exec.Command, no shell; ip is
	// provider-assigned, user/name/localRepoRoot/remotePath are local
	// identifiers/path args, none attacker-controlled.
	cmd := exec.CommandContext(ctx, "mutagen", mutagenCreateArgs(ip, user, name, localRepoRoot, remotePath)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("starting watch for %s failed: %w\n%s", name, err, out)
	}
	return nil
}
```

In `internal/lifecycle/lifecycle.go`, update `Steps`, and `Up`:

```go
type Steps struct {
	WaitReady  func(ctx context.Context, ip string, timeout time.Duration) error
	Reconcile  func(ctx context.Context, name, cloudlabPath string) error
	Rsync      func(ctx context.Context, ip, user, localRepoRoot, remotePath string) error
	StartWatch func(ctx context.Context, ip, user, name, localRepoRoot, remotePath string) error
}
```

`DefaultSteps` itself needs no code change (`Rsync`/`StartWatch` are assigned as function values, matching the new field types automatically once the above edits land).

In `Up`, insert the `RemotePath` computation right after deriving `remoteUser` (before rendering cloud-init), add `RepoPath` to the `record` literal, and pass `remotePath` to the two step calls at the bottom:

```go
	// remoteUser is the instance's own non-root login, derived once
	// here and stored in its state record -- not re-derived per
	// command, since a later command may run as a different local user
	// or on a different machine and must keep talking to whichever
	// user this instance was actually provisioned with.
	remoteUser, err := identity.RemoteUser()
	if err != nil {
		return fmt.Errorf("deriving remote username: %w", err)
	}
	// remotePath is likewise computed once and stored (state.Record.RepoPath)
	// rather than re-derived: a later `cloudlab ssh` may run from
	// somewhere that isn't this repo's checkout at all, and needs to
	// know where THIS instance's repo actually landed.
	remotePath, err := RemotePath(repoRoot, remoteUser)
	if err != nil {
		return fmt.Errorf("computing remote repo path: %w", err)
	}
	cloudInit, err := provisioning.RenderCloudInit(remoteUser)
	if err != nil {
		return fmt.Errorf("rendering cloud-init: %w", err)
	}
```

```go
	record := state.Record{
		Name:     name,
		Provider: "digitalocean",
		VMID:     vm.ID,
		IP:       vm.IP,
		Region:   vm.Region,
		Size:     vm.Size,
		Template: *cfg.Template,
		User:     remoteUser,
		RepoPath: remotePath,
	}
```

```go
	if err := steps.Rsync(ctx, vm.IP, remoteUser, repoRoot, remotePath); err != nil {
		return err
	}

	if err := steps.StartWatch(ctx, vm.IP, remoteUser, name, repoRoot, remotePath); err != nil {
		return err
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `nix develop --command bash -c "go build ./... && go test ./internal/lifecycle/... ./internal/state/... -v"`
Expected: PASS (all lifecycle and state tests)

- [ ] **Step 5: Commit**

```bash
git add internal/state/state.go internal/lifecycle/rsync.go internal/lifecycle/rsync_test.go internal/lifecycle/watch.go internal/lifecycle/watch_test.go internal/lifecycle/lifecycle.go internal/lifecycle/lifecycle_test.go
```

Use the `/commit-tools:commit` flow:

```
Move the main repo off ~/<instance-name> to a mirrored path

Up now computes RemotePath(repoRoot, remoteUser) once, uses it for
both the initial rsync target and StartWatch's Mutagen session
(replacing the old ~/<instance-name> flattening), and stores it as a
new state.Record.RepoPath field so later commands -- ssh's upcoming
auto-cd in particular -- read the same value instead of recomputing
it from whatever the local cwd happens to be. Intentional breaking
change for the repo's remote location on new instances; existing
already-provisioned ones are unaffected (no migration, matching how
the earlier root-to-sudo-user switch was also new-instances-only).
```

---

## Task 4: `cloudlab sync [remote-dir] --dir <local-dir>`

**Files:**
- Modify: `cmd/lookup.go`
- Modify: `cmd/lookup_run.go`
- Modify: `cmd/lookup_run_test.go`

**Interfaces:**
- Consumes: `lifecycle.RemotePath` (Task 1).
- Produces: `syncLocalDir(dirFlag string) (string, error)`, `syncRemoteDir(args []string, local, remoteUser string) (string, error)` — pure helpers, no later task depends on them.

- [ ] **Step 1: Write the failing tests**

In `cmd/lookup_run_test.go`, remove `TestDefaultRemoteDir_UsesBasenameUnderHome` (the old basename-only scheme it tested no longer exists) and add:

```go
func TestSyncLocalDir_UsesDirFlagOrCwd(t *testing.T) {
	if got, err := syncLocalDir("/explicit/path"); err != nil || got != "/explicit/path" {
		t.Errorf("syncLocalDir(%q) = (%q, %v), want (%q, nil)", "/explicit/path", got, err, "/explicit/path")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := syncLocalDir(""); err != nil || got != cwd {
		t.Errorf("syncLocalDir(\"\") = (%q, %v), want (%q, nil)", got, err, cwd)
	}
}

func TestSyncRemoteDir_UsesFirstArgOrRemotePath(t *testing.T) {
	if got, err := syncRemoteDir([]string{"~/custom"}, "/whatever", "devuser"); err != nil || got != "~/custom" {
		t.Errorf("syncRemoteDir([\"~/custom\"], ...) = (%q, %v), want (%q, nil)", got, err, "~/custom")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	local := filepath.Join(home, "project")
	got, err := syncRemoteDir(nil, local, "devuser")
	if err != nil {
		t.Fatalf("syncRemoteDir(nil, ...) error = %v", err)
	}
	want := "/home/devuser/project"
	if got != want {
		t.Errorf("syncRemoteDir(nil, ...) = %q, want %q", got, want)
	}
}
```

This file already imports `"os"` and needs `"path/filepath"` added to its import block (check the current import list — add `"path/filepath"` if it's not already there for this file specifically; `cmd/lookup_run.go` already imports it, but `_test.go` files have their own import blocks).

- [ ] **Step 2: Run tests to verify they fail**

Run: `nix develop --command go test ./cmd/... -run 'TestSyncLocalDir|TestSyncRemoteDir' -v`
Expected: FAIL to compile — `syncLocalDir`/`syncRemoteDir` undefined.

- [ ] **Step 3: Write the implementation**

In `cmd/lookup.go`, add a `flags` field to `lookupCommandSpec` and use it on the `sync` entry:

```go
type lookupCommandSpec struct {
	use, short, verb string
	args             cobra.PositionalArgs
	named            bool
	flags            func(c *cobra.Command)
	run              func(cmd *cobra.Command, name string, args []string) error
}
```

Replace the `sync` entry:

```go
	{
		use:   "sync [remote-dir]",
		short: "One-shot push of a directory outside the repo to the instance",
		verb:  "sync",
		args:  cobra.MaximumNArgs(1),
		named: false,
		flags: func(c *cobra.Command) {
			c.Flags().String("dir", "", "local directory to sync (defaults to the current directory)")
		},
		run: runSync,
	},
```

Update `newLookupCommands` to call `spec.flags` when set:

```go
func newLookupCommands() []*cobra.Command {
	cmds := make([]*cobra.Command, 0, len(lookupCommandSpecs))
	for _, spec := range lookupCommandSpecs {
		c := &cobra.Command{
			Use:   spec.use,
			Short: spec.short,
			Args:  spec.args,
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
		}
		if spec.flags != nil {
			spec.flags(c)
		}
		cmds = append(cmds, c)
	}
	return cmds
}
```

The `newLookupCommands` doc comment already says "ten" (from earlier work) — leave it unchanged, since this task adds a flag to an existing command rather than adding or removing one.

In `cmd/lookup_run.go`, replace `defaultRemoteDir` and `runSync`:

```go
// syncLocalDir returns the local directory sync should push: the
// --dir flag's value if set, else the current working directory.
func syncLocalDir(dirFlag string) (string, error) {
	if dirFlag != "" {
		return dirFlag, nil
	}
	return os.Getwd()
}

// syncRemoteDir returns the remote directory sync should target:
// args[0] if given, else local's path mirrored under remoteUser's
// home (or local itself, unchanged, if it isn't under the local
// user's home) via lifecycle.RemotePath.
func syncRemoteDir(args []string, local, remoteUser string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	return lifecycle.RemotePath(local, remoteUser)
}

func runSync(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}

	dirFlag, err := cmd.Flags().GetString("dir")
	if err != nil {
		return err
	}
	local, err := syncLocalDir(dirFlag)
	if err != nil {
		return err
	}
	remote, err := syncRemoteDir(args, local, record.User)
	if err != nil {
		return err
	}

	if err := lifecycle.Push(cmd.Context(), record.IP, record.User, local, remote); err != nil {
		return err
	}
	cmd.Printf("Synced %s to %s:%s\n", local, name, remote)
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `nix develop --command bash -c "go build ./... && go test ./cmd/... -v"`
Expected: PASS (full `cmd` package)

- [ ] **Step 5: Commit**

```bash
git add cmd/lookup.go cmd/lookup_run.go cmd/lookup_run_test.go
```

Use the `/commit-tools:commit` flow:

```
cloudlab sync: default to cwd, remote target via RemotePath

sync used to require an explicit local-dir positional every time.
It's now `sync [remote-dir] --dir <local-dir>` -- --dir defaults to
the current directory, and remote-dir, when omitted, resolves through
the same RemotePath helper the main repo's location now uses, instead
of the old basename-only scheme. syncLocalDir/syncRemoteDir are
extracted as small tested pure helpers, matching this file's existing
defaultLocalDir/tmuxSession precedent for default-picking logic.
```

---

## Task 5: `cloudlab ssh [--dir <path>]` auto-`cd`

**Files:**
- Modify: `internal/lifecycle/ssh.go`
- Modify: `internal/lifecycle/ssh_test.go`
- Modify: `cmd/lookup.go`
- Modify: `cmd/lookup_run.go`

**Interfaces:**
- Consumes: `record.RepoPath` (Task 3, `state.Record`), `reconcile.ShellQuote` (already exported from an earlier session's work in `internal/reconcile/ssh.go`).
- Produces: `lifecycle.SSH(ctx context.Context, ip, user, dir string) error` — signature gains a 4th parameter; `sshArgs(ip, user, dir string) []string` likewise.

- [ ] **Step 1: Write the failing tests**

Replace `TestSSHArgs_BuildsExpectedCommand` in `internal/lifecycle/ssh_test.go` with:

```go
package lifecycle

import (
	"testing"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

func TestSSHArgs_NoDir_PlainSession(t *testing.T) {
	got := sshArgs("203.0.113.5", "devuser", "")
	want := []string{"devuser@203.0.113.5"}
	if len(got) != len(want) {
		t.Fatalf("sshArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sshArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSSHArgs_WithDir_CdsBeforeInteractiveShell(t *testing.T) {
	got := sshArgs("203.0.113.5", "devuser", "/home/devuser/project")
	inner := "cd " + reconcile.ShellQuote("/home/devuser/project") + " && exec bash -l"
	want := []string{"-t", "devuser@203.0.113.5", "bash -lc " + reconcile.ShellQuote(inner)}
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

- [ ] **Step 2: Run tests to verify they fail**

Run: `nix develop --command go test ./internal/lifecycle/... -run TestSSHArgs -v`
Expected: FAIL to compile — `sshArgs` still takes 2 args, not 3.

- [ ] **Step 3: Write the implementation**

Replace `internal/lifecycle/ssh.go` in full:

```go
package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// sshArgs builds the argv SSH passes to the ssh binary: an
// interactive session as user on ip's default port. When dir is
// non-empty, forces PTY allocation (-t) and runs a remote command
// that cds into dir before handing off to an interactive login shell
// -- the same outer "bash -lc <quoted-inner>" shape tmux.go already
// established, so profile scripts (and PATH) are set up the same way.
// dir is shell-quoted via reconcile.ShellQuote since ssh concatenates
// the trailing argv into one string the remote shell parses. An empty
// dir returns today's plain form: no remote command, ssh's own
// default session handling.
func sshArgs(ip, user, dir string) []string {
	if dir == "" {
		return []string{user + "@" + ip}
	}
	inner := "cd " + reconcile.ShellQuote(dir) + " && exec bash -l"
	return []string{"-t", user + "@" + ip, "bash -lc " + reconcile.ShellQuote(inner)}
}

// SSH opens an interactive session on the instance at ip as user,
// cd'ing into dir first if it's non-empty, execing the real ssh
// binary with stdio passed straight through -- no PTY/raw-mode
// handling of our own beyond -t when dir is set. Reuses whatever
// trust-on-first-connect entry already exists in the user's real
// ~/.ssh/known_hosts from up's WaitReady/Connect call.
func SSH(ctx context.Context, ip, user, dir string) error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh not found on PATH: %w", err)
	}
	// #nosec G204 -- argv-array exec.Command, no shell; ip is
	// provider-assigned, never attacker-controlled. When dir is set,
	// the remote command IS shell-interpreted by sshd's login shell,
	// but dir is shell-quoted via reconcile.ShellQuote before being
	// embedded.
	cmd := exec.CommandContext(ctx, "ssh", sshArgs(ip, user, dir)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
```

In `cmd/lookup.go`, add a `flags` entry to the `ssh` spec:

```go
	{
		use:   "ssh [name]",
		short: "Open an interactive remote shell on the instance",
		verb:  "ssh",
		args:  cobra.MaximumNArgs(1),
		named: true,
		flags: func(c *cobra.Command) {
			c.Flags().String("dir", "", "remote directory to cd into (defaults to the synced repo's location)")
		},
		run: runSSH,
	},
```

In `cmd/lookup_run.go`, update `runSSH`:

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
	if dir == "" {
		dir = record.RepoPath
	}
	return lifecycle.SSH(cmd.Context(), record.IP, record.User, dir)
}
```

(An instance created before Task 3 has `record.RepoPath == ""`, so `dir` stays `""` and `sshArgs` returns today's plain no-`cd` form — no separate backward-compat branch needed.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `nix develop --command bash -c "go build ./... && go test ./internal/lifecycle/... ./cmd/... -v"`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/lifecycle/ssh.go internal/lifecycle/ssh_test.go cmd/lookup.go cmd/lookup_run.go
```

Use the `/commit-tools:commit` flow:

```
cloudlab ssh: auto-cd into the synced repo, --dir to override

Now that the repo lands at a deep, predictable path (RemotePath's
mirrored-or-as-is scheme) instead of a flat ~/<name>, ssh defaults to
cd'ing straight into it -- reading record.RepoPath rather than
recomputing from a local cwd that may not even be this instance's
checkout. --dir overrides the target. Instances created before
RepoPath existed get an empty value, which sshArgs already treats the
same as no override: today's plain login session, no special-casing
needed. Uses the same outer "bash -lc <quoted-inner>" shape tmux.go
already established for a remote-command-plus-interactive-shell.
```

---

## Task 6: `lifecycle.GetWatchStatus` and `cloudlab status`'s `Watch:` line

**Files:**
- Create: `internal/lifecycle/watchstatus.go`
- Create: `internal/lifecycle/watchstatus_test.go`
- Modify: `cmd/lookup_run.go`

**Interfaces:**
- Consumes: nothing from earlier tasks (independent).
- Produces: `lifecycle.WatchStatus` struct, `lifecycle.GetWatchStatus(ctx context.Context, name string) (WatchStatus, error)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/lifecycle/watchstatus_test.go`:

```go
package lifecycle

import (
	"context"
	"os/exec"
	"testing"
)

func TestMutagenListArgs_BuildsExpectedCommand(t *testing.T) {
	got := mutagenListArgs("myrepo")
	want := []string{"sync", "list", "myrepo", "--template={{range .}}{{.Status}}|{{.Alpha.Connected}}|{{.Beta.Connected}}|{{len .Conflicts}}|{{.LastError}}{{end}}"}
	if len(got) != len(want) {
		t.Fatalf("mutagenListArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mutagenListArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseWatchStatus_ParsesAllFields(t *testing.T) {
	got, err := parseWatchStatus("Watching|true|false|2|scan failed")
	if err != nil {
		t.Fatalf("parseWatchStatus() error = %v", err)
	}
	want := WatchStatus{Running: true, Status: "Watching", AlphaConnected: true, BetaConnected: false, Conflicts: 2, LastError: "scan failed"}
	if got != want {
		t.Errorf("parseWatchStatus() = %+v, want %+v", got, want)
	}
}

func TestParseWatchStatus_MalformedLine_ReturnsError(t *testing.T) {
	if _, err := parseWatchStatus("not enough fields"); err == nil {
		t.Fatal("parseWatchStatus() error = nil, want an error for a malformed line")
	}
}

func TestGetWatchStatus_NoSession_ReturnsNotRunningNoError(t *testing.T) {
	if _, err := exec.LookPath("mutagen"); err != nil {
		t.Skip("mutagen not on PATH")
	}
	t.Setenv("MUTAGEN_DATA_DIRECTORY", t.TempDir())

	got, err := GetWatchStatus(context.Background(), "cloudlab-watchstatus-test-nonexistent")
	if err != nil {
		t.Fatalf("GetWatchStatus() error = %v, want nil for a nonexistent session", err)
	}
	if got.Running {
		t.Errorf("GetWatchStatus().Running = true, want false for a nonexistent session")
	}
}

func TestGetWatchStatus_RealSession_ReturnsRunning(t *testing.T) {
	if _, err := exec.LookPath("mutagen"); err != nil {
		t.Skip("mutagen not on PATH")
	}
	t.Setenv("MUTAGEN_DATA_DIRECTORY", t.TempDir())

	src := t.TempDir()
	dst := t.TempDir()
	name := "cloudlab-watchstatus-test-real"
	create := exec.Command("mutagen", "sync", "create", "--name="+name, src, dst)
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("mutagen sync create: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("mutagen", "sync", "terminate", name).Run()
		_ = exec.Command("mutagen", "daemon", "stop").Run()
	})

	got, err := GetWatchStatus(context.Background(), name)
	if err != nil {
		t.Fatalf("GetWatchStatus() error = %v", err)
	}
	if !got.Running {
		t.Error("GetWatchStatus().Running = false, want true for a real session")
	}
	if got.Status == "" {
		t.Error("GetWatchStatus().Status is empty, want a real Mutagen status string")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `nix develop --command go test ./internal/lifecycle/... -run 'TestMutagenListArgs|TestParseWatchStatus|TestGetWatchStatus' -v`
Expected: FAIL to compile — `mutagenListArgs`, `parseWatchStatus`, `WatchStatus`, `GetWatchStatus` all undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/lifecycle/watchstatus.go`:

```go
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// WatchStatus is a snapshot of an instance's watch (Mutagen sync)
// session, as reported by `mutagen sync list`.
type WatchStatus struct {
	Running        bool
	Status         string
	AlphaConnected bool
	BetaConnected  bool
	Conflicts      int
	LastError      string
}

// mutagenListArgs builds the argv GetWatchStatus passes to the
// mutagen binary: a pipe-delimited Go template selecting exactly the
// fields WatchStatus needs, for name's session.
func mutagenListArgs(name string) []string {
	template := "{{range .}}{{.Status}}|{{.Alpha.Connected}}|{{.Beta.Connected}}|{{len .Conflicts}}|{{.LastError}}{{end}}"
	return []string{"sync", "list", name, "--template=" + template}
}

// GetWatchStatus reports name's current watch session state. A
// session that doesn't exist (watch never started, or was stopped) is
// reported as WatchStatus{Running: false}, nil -- not an error. Any
// other mutagen failure (not on PATH, daemon unreachable, a real
// Mutagen-side error) is returned as a genuine error.
func GetWatchStatus(ctx context.Context, name string) (WatchStatus, error) {
	if _, err := exec.LookPath("mutagen"); err != nil {
		return WatchStatus{}, fmt.Errorf("mutagen not found on PATH (run inside `nix develop`, or install it: https://mutagen.io/documentation/introduction/installation): %w", err)
	}
	// #nosec G204 -- argv-array exec.Command, no shell; name is a local
	// identifier, never attacker-controlled.
	out, err := exec.CommandContext(ctx, "mutagen", mutagenListArgs(name)...).Output()
	if err != nil {
		if isNoSessionError(err) {
			return WatchStatus{Running: false}, nil
		}
		return WatchStatus{}, fmt.Errorf("checking watch status for %s: %w", name, err)
	}
	return parseWatchStatus(strings.TrimSpace(string(out)))
}

// isNoSessionError reports whether err is mutagen's own "no matching
// sessions" exit, distinguishing "watch was never started" from a
// genuine failure (mutagen missing, daemon down, ...).
func isNoSessionError(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	return strings.Contains(string(exitErr.Stderr), "did not match any sessions")
}

func parseWatchStatus(line string) (WatchStatus, error) {
	fields := strings.SplitN(line, "|", 5)
	if len(fields) != 5 {
		return WatchStatus{}, fmt.Errorf("unexpected mutagen sync list output: %q", line)
	}
	conflicts, err := strconv.Atoi(fields[3])
	if err != nil {
		return WatchStatus{}, fmt.Errorf("parsing conflict count from mutagen output %q: %w", line, err)
	}
	return WatchStatus{
		Running:        true,
		Status:         fields[0],
		AlphaConnected: fields[1] == "true",
		BetaConnected:  fields[2] == "true",
		Conflicts:      conflicts,
		LastError:      fields[4],
	}, nil
}
```

In `cmd/lookup_run.go`, extend `runStatus` with a `Watch:` line (add right after the existing `Status:`/`LiveErr` block, before `return nil`):

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
	cmd.Printf("User:     %s\n", st.Record.User)
	cmd.Printf("IP:       %s\n", st.Record.IP)
	if st.LiveErr != nil {
		cmd.Printf("Status:   unknown (live check failed: %v)\n", st.LiveErr)
	} else {
		cmd.Printf("Status:   %s\n", st.LiveStatus)
	}

	watch, err := lifecycle.GetWatchStatus(cmd.Context(), name)
	if err != nil {
		cmd.Printf("Watch:    unknown (check failed: %v)\n", err)
	} else if !watch.Running {
		cmd.Printf("Watch:    not running\n")
	} else {
		cmd.Printf("Watch:    %s (conflicts: %d)\n", watch.Status, watch.Conflicts)
		if watch.LastError != "" {
			cmd.Printf("LastError: %s\n", watch.LastError)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `nix develop --command bash -c "go build ./... && go test ./internal/lifecycle/... ./cmd/... -v"`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/lifecycle/watchstatus.go internal/lifecycle/watchstatus_test.go cmd/lookup_run.go
```

Use the `/commit-tools:commit` flow:

```
Surface real sync/watch state in cloudlab status

status's own help text has promised "sync/watch state" since it was
first written, but never actually showed it. GetWatchStatus wraps
`mutagen sync list <name> --template=...` (Go-template output, not
text-scraping) for the live Status string, per-endpoint Connected
state, and conflict count. A session that doesn't exist (watch never
started, or was stopped) reports cleanly as "not running" rather than
an error -- mutagen's own "did not match any sessions" exit is the
only case mapped that way; anything else (mutagen missing, daemon
down, a real failure) still surfaces as a genuine error.
```

---

## Task 7: Final end-to-end verification

**Files:** none (verification only).

**Interfaces:** none.

- [ ] **Step 1: Full repo verification**

Run: `nix develop --command bash -c "go build ./... && go vet ./... && golangci-lint run ./... && go test ./..."`
Expected: every package `ok`, `golangci-lint`: `0 issues`.

- [ ] **Step 2: Confirm `cloudlab sync --help` and `cloudlab ssh --help` show the new `--dir` flag**

Run: `nix develop --command go run . sync --help` and `nix develop --command go run . ssh --help`
Expected: both show a `--dir` flag with the description text from Tasks 4/5.

- [ ] **Step 3: Confirm `cloudlab status --help` text still matches reality**

Run: `nix develop --command go run . status --help`
Expected: `Short` text still reads "Show instance detail: IP, uptime, cost, sync/watch state" — now actually true.

- [ ] **Step 4: Confirm git log shows all six commits**

Run: `git log --oneline -6`
Expected: the six commits from Tasks 1–6, in order.

No commit for this task — it's verification only, nothing to stage.
