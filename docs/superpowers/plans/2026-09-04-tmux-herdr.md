# tmux + herdr Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add two new lookup-only commands, `cloudlab herdr` and `cloudlab tmux [session-name]`, and wire `tmux` + `gpakosz/.tmux`'s config into every instance's home-manager setup.

**Architecture:** `cloudlab herdr`/`cloudlab tmux` each get a pure `xArgs(...)` argv-builder (unit tested) plus a thin `X(ctx, ...)` wrapper in `internal/lifecycle` that execs the real binary with stdio passed straight through — the exact shape `internal/lifecycle/ssh.go` already established. Both wire into `cmd/lookup_run.go`/`cmd/lookup.go`'s existing `lookupCommandSpecs` table, the same mechanism every other lookup command (`ssh`, `watch`, `status`, ...) uses. `tmux` itself (binary + `gpakosz/.tmux` config) is a `templates/modules/common.nix` addition — no Go code involved there.

**Tech Stack:** Go (cobra CLI, `os/exec`), Nix/home-manager (`pkgs.fetchFromGitHub`, `home.file`).

## Global Constraints

- Follow `internal/lifecycle/ssh.go`'s exact pattern: pure argv-builder function (lowercase, unexported, directly unit tested) + a thin exported wrapper that does `exec.LookPath` first, then `exec.CommandContext` with stdio (`Stdin`/`Stdout`/`Stderr`) set to `os.Stdin`/`os.Stdout`/`os.Stderr` and returns `cmd.Run()`'s error directly. No PTY/raw-mode handling of our own.
- Every `exec.CommandContext` call needs a `// #nosec G204 -- argv-array exec.Command, no shell; ...` comment matching the style already used in `ssh.go`/`rsync.go`/`watch.go`, explaining why the args aren't attacker-controlled.
- New lookup commands wire into `cmd/lookup_run.go` (the `run` function) and `cmd/lookup.go` (the `lookupCommandSpecs` table entry) — never a new top-level `cmd/*.go` file, matching how `ssh`/`watch`/`down` etc. are done.
- TDD: write the failing test first, run it, confirm it fails for the right reason, then implement, then confirm it passes.
- Full verification after every task: `nix develop --command bash -c "go build ./... && go vet ./... && golangci-lint run ./... && go test ./..."` must be clean before committing.
- Commits follow this repo's `/commit-tools:commit` convention (classic style, no AI co-author line) — the plan below shows the intended message; use the skill/plugin to actually create the commit rather than raw `git commit`.

---

## Task 1: `cloudlab herdr`

**Files:**
- Create: `internal/lifecycle/herdr.go`
- Create: `internal/lifecycle/herdr_test.go`
- Modify: `cmd/lookup_run.go` (add `runHerdr`, after `runSSH`)
- Modify: `cmd/lookup.go` (add a `lookupCommandSpecs` entry, after the `ssh` entry)
- Modify: `cmd/lookup_test.go` (extend `TestLookupCommands_NameFlagResolves`'s `cases` table, after the `ssh` row)

**Interfaces:**
- Consumes: `resolveInstance(name string) (*state.Store, state.Record, error)` (`cmd/lookup_run.go`, already exists) — gives `record.IP string`, `record.User string`.
- Produces: `lifecycle.Herdr(ctx context.Context, ip, user string) error` and package-internal `herdrArgs(ip, user string) []string`, for Task 2/3 or any later caller to reference by these exact names.

- [ ] **Step 1: Write the failing test**

Create `internal/lifecycle/herdr_test.go`:

```go
package lifecycle

import "testing"

func TestHerdrArgs_BuildsExpectedCommand(t *testing.T) {
	got := herdrArgs("203.0.113.5", "devuser")
	want := []string{"--remote", "ssh://devuser@203.0.113.5"}
	if len(got) != len(want) {
		t.Fatalf("herdrArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("herdrArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `nix develop --command go test ./internal/lifecycle/... -run TestHerdrArgs -v`
Expected: FAIL to compile — `herdrArgs` is undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/lifecycle/herdr.go`:

```go
package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// herdrArgs builds the argv Herdr passes to the herdr binary: a thin
// client connecting to ip's default background session as user, over
// herdr's own SSH bridge (see https://herdr.dev/docs/how-to-work/).
func herdrArgs(ip, user string) []string {
	return []string{"--remote", "ssh://" + user + "@" + ip}
}

// Herdr opens an interactive herdr thin-client session against the
// instance at ip as user, execing the real herdr binary with stdio
// passed straight through -- same shape as SSH. herdr's own SSH
// bridge handles authentication and host-key trust itself.
func Herdr(ctx context.Context, ip, user string) error {
	if _, err := exec.LookPath("herdr"); err != nil {
		return fmt.Errorf("herdr not found on PATH: %w", err)
	}
	// #nosec G204 -- argv-array exec.Command, no shell; ip is
	// provider-assigned, never attacker-controlled.
	cmd := exec.CommandContext(ctx, "herdr", herdrArgs(ip, user)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `nix develop --command go test ./internal/lifecycle/... -run TestHerdrArgs -v`
Expected: PASS

- [ ] **Step 5: Wire the command into `cmd/lookup_run.go`**

In `cmd/lookup_run.go`, add this function immediately after `runSSH` (the existing function ending `return lifecycle.SSH(cmd.Context(), record.IP, record.User)\n}`):

```go
func runHerdr(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	return lifecycle.Herdr(cmd.Context(), record.IP, record.User)
}
```

- [ ] **Step 6: Register the command in `cmd/lookup.go`**

In `cmd/lookup.go`'s `lookupCommandSpecs` slice, add this entry immediately after the existing `ssh` entry (the one with `verb: "ssh"`):

```go
	{
		use:   "herdr [name]",
		short: "Open an interactive herdr session on the instance (background session that survives disconnects)",
		verb:  "herdr",
		args:  cobra.MaximumNArgs(1),
		named: true,
		run:   runHerdr,
	},
```

Also update `newLookupCommands`'s doc comment just above it — it currently says `shared across all eight`; this entry makes it nine:

```go
// newLookupCommands builds every lookup-only command from
// lookupCommandSpecs. Flag handling, identity resolution, and the
// stub/exit-code behavior are shared across all nine — only Use/Short
// text, the Args validator, and whether the first positional arg is the
// instance name differ per spec.
```

- [ ] **Step 7: Extend the cmd-level table test**

In `cmd/lookup_test.go`'s `TestLookupCommands_NameFlagResolves`, add this row to the `cases` slice immediately after the `ssh` row (`{[]string{"ssh", "--name", "myrepo"}, "ssh", ...}`):

```go
			{[]string{"herdr", "--name", "myrepo"}, "herdr", `no instance named "myrepo"`},
```

- [ ] **Step 8: Run the full test suite**

Run: `nix develop --command bash -c "go build ./... && go vet ./... && golangci-lint run ./... && go test ./..."`
Expected: all packages `ok`, `golangci-lint`: `0 issues`.

- [ ] **Step 9: Commit**

```bash
git add internal/lifecycle/herdr.go internal/lifecycle/herdr_test.go cmd/lookup_run.go cmd/lookup.go cmd/lookup_test.go
```

Use the `/commit-tools:commit` flow (present this message, confirm, then commit):

```
Add cloudlab herdr command

herdr (https://herdr.dev/) is a background server that hosts
persistent terminal sessions for coding agents, surviving
disconnects -- it's been in every instance's home-manager packages
since an earlier session but had no way to actually connect to it.
cloudlab herdr execs the real herdr binary with --remote
ssh://<user>@<ip>, herdr's own documented thin-client invocation for
a remote host (herdr.dev/docs/how-to-work) -- same exec-with-stdio-
passthrough shape as cloudlab ssh, herdr's own SSH bridge handles
the rest.
```

---

## Task 2: `cloudlab tmux [session-name]`

**Files:**
- Create: `internal/lifecycle/tmux.go`
- Create: `internal/lifecycle/tmux_test.go`
- Modify: `cmd/lookup_run.go` (add `defaultTmuxSession` const + `runTmux`, after `runHerdr`)
- Modify: `cmd/lookup.go` (add a `lookupCommandSpecs` entry, after the `herdr` entry added in Task 1)
- Modify: `cmd/lookup_test.go` (extend `TestLookupCommands_NameFlagResolves`'s `cases` table, after the `herdr` row)

**Interfaces:**
- Consumes: `resolveInstance` (as Task 1).
- Produces: `lifecycle.Tmux(ctx context.Context, ip, user, session string) error` and package-internal `tmuxArgs(ip, user, session string) []string`.

- [ ] **Step 1: Write the failing test**

Create `internal/lifecycle/tmux_test.go`:

```go
package lifecycle

import "testing"

func TestTmuxArgs_BuildsExpectedCommand(t *testing.T) {
	got := tmuxArgs("203.0.113.5", "devuser", "main")
	want := []string{"-t", "devuser@203.0.113.5", "tmux", "new-session", "-A", "-s", "main"}
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

Run: `nix develop --command go test ./internal/lifecycle/... -run TestTmuxArgs -v`
Expected: FAIL to compile — `tmuxArgs` is undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/lifecycle/tmux.go`:

```go
package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// tmuxArgs builds the argv Tmux passes to the ssh binary: a PTY
// session as user on ip's default port, immediately running tmux's
// own create-or-attach primitive for session ("new-session -A": if
// the named session already exists, attach; otherwise create it).
func tmuxArgs(ip, user, session string) []string {
	return []string{"-t", user + "@" + ip, "tmux", "new-session", "-A", "-s", session}
}

// Tmux opens an interactive tmux session named session on the
// instance at ip as user (creating it first if it doesn't already
// exist), execing the real ssh binary with stdio passed straight
// through -- same shape as SSH. -t forces PTY allocation, which tmux
// requires.
func Tmux(ctx context.Context, ip, user, session string) error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh not found on PATH: %w", err)
	}
	// #nosec G204 -- argv-array exec.Command, no shell; ip is
	// provider-assigned, user/session are local identifiers, none
	// attacker-controlled.
	cmd := exec.CommandContext(ctx, "ssh", tmuxArgs(ip, user, session)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `nix develop --command go test ./internal/lifecycle/... -run TestTmuxArgs -v`
Expected: PASS

- [ ] **Step 5: Wire the command into `cmd/lookup_run.go`**

In `cmd/lookup_run.go`, add this immediately after the `runHerdr` function added in Task 1:

```go
// defaultTmuxSession is the session cloudlab tmux creates-or-attaches
// to when no session-name argument is given.
const defaultTmuxSession = "main"

func runTmux(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	session := defaultTmuxSession
	if len(args) > 0 {
		session = args[0]
	}
	return lifecycle.Tmux(cmd.Context(), record.IP, record.User, session)
}
```

- [ ] **Step 6: Register the command in `cmd/lookup.go`**

In `cmd/lookup.go`'s `lookupCommandSpecs` slice, add this entry immediately after the `herdr` entry added in Task 1. Note `named: false` — the optional positional here is the *session* name, not the instance name (same reasoning as `sync`/`download`, whose positionals are paths, not instance names); the instance itself always resolves via `--name`/cwd:

```go
	{
		use:   "tmux [session-name]",
		short: "Open a tmux session on the instance, creating it if needed (default session: \"main\")",
		verb:  "tmux",
		args:  cobra.MaximumNArgs(1),
		named: false,
		run:   runTmux,
	},
```

Also update `newLookupCommands`'s doc comment again — Task 1 changed it to `nine`; this entry makes it ten:

```go
// newLookupCommands builds every lookup-only command from
// lookupCommandSpecs. Flag handling, identity resolution, and the
// stub/exit-code behavior are shared across all ten — only Use/Short
// text, the Args validator, and whether the first positional arg is the
// instance name differ per spec.
```

- [ ] **Step 7: Extend the cmd-level table test**

In `cmd/lookup_test.go`'s `TestLookupCommands_NameFlagResolves`, add this row immediately after the `herdr` row added in Task 1:

```go
			{[]string{"tmux", "--name", "myrepo"}, "tmux", `no instance named "myrepo"`},
```

- [ ] **Step 8: Run the full test suite**

Run: `nix develop --command bash -c "go build ./... && go vet ./... && golangci-lint run ./... && go test ./..."`
Expected: all packages `ok`, `golangci-lint`: `0 issues`.

- [ ] **Step 9: Commit**

```bash
git add internal/lifecycle/tmux.go internal/lifecycle/tmux_test.go cmd/lookup_run.go cmd/lookup.go cmd/lookup_test.go
```

Use the `/commit-tools:commit` flow:

```
Add cloudlab tmux command

Lets you jump straight into a persistent tmux session on the
instance without a separate ssh + manual `tmux` step. Execs the real
ssh binary with -t (PTY, which tmux needs) running `tmux new-session
-A -s <name>` -- tmux's own create-or-attach primitive, so no
cloudlab-side session bookkeeping is needed. Defaults to a session
named "main"; an optional positional picks a different one
(cloudlab tmux <name>), the same way sync/download's positionals are
paths rather than the instance name.
```

---

## Task 3: Wire tmux + gpakosz/.tmux into home-manager

**Files:**
- Modify: `templates/modules/common.nix`

**Interfaces:**
- Consumes: nothing from Tasks 1–2 (independent).
- Produces: `pkgs.tmux` on every instance's `PATH`, `~/.tmux.conf` and `~/.tmux.conf.local` symlinked from `gpakosz/.tmux`.

- [ ] **Step 1: Add the `lib` module argument and the `tmuxDotfiles` fetch**

`templates/modules/common.nix` currently starts:

```nix
{ pkgs, ... }:
let
  # The instance's own non-root user (see internal/identity.RemoteUser
  # and cloud-init.sh, which creates it) -- read from the SSH session's
  # own environment rather than hardcoded, since this same checked-in
  # template is shared across every instance, each provisioned under a
  # different user. Requires --impure on `home-manager switch` (see
  # internal/reconcile/reconcile.go).
  username = builtins.getEnv "USER";
in
```

Replace it with:

```nix
{ pkgs, lib, ... }:
let
  # The instance's own non-root user (see internal/identity.RemoteUser
  # and cloud-init.sh, which creates it) -- read from the SSH session's
  # own environment rather than hardcoded, since this same checked-in
  # template is shared across every instance, each provisioned under a
  # different user. Requires --impure on `home-manager switch` (see
  # internal/reconcile/reconcile.go).
  username = builtins.getEnv "USER";

  # Pinned to gpakosz/.tmux's master branch HEAD at the time this was
  # added (the repo has no tags/releases to pin to instead). sha256
  # captured via `nix-prefetch-url --unpack
  # https://github.com/gpakosz/.tmux/archive/<rev>.tar.gz` -- bump
  # both rev and sha256 together the same way if a newer commit is
  # ever wanted.
  tmuxDotfiles = pkgs.fetchFromGitHub {
    owner = "gpakosz";
    repo = ".tmux";
    rev = "58a3dcc0d718ec0fa1c0d5a2fddd640a1ad7a5b7";
    sha256 = "0zky4qkndrs645xnxh6498zc8yj7y581sg72hh0h7b31a5jxng30";
  };
in
```

- [ ] **Step 2: Add `pkgs.tmux` to `home.packages`**

Find this block in `templates/modules/common.nix`:

```nix
  home.packages = [
    pkgs.git
    pkgs.age
    # Self-manages its own server lifecycle (launches/attaches on
    # demand, per `herdr --help`) -- no systemd unit needed, unlike
    # tailscaled below.
    pkgs.herdr
    pkgs.tailscale
  ];
```

Replace it with:

```nix
  home.packages = [
    pkgs.git
    pkgs.age
    # Self-manages its own server lifecycle (launches/attaches on
    # demand, per `herdr --help`) -- no systemd unit needed, unlike
    # tailscaled below.
    pkgs.herdr
    pkgs.tailscale
    pkgs.tmux
  ];
```

- [ ] **Step 3: Symlink gpakosz/.tmux's config files**

Immediately after the `programs.fish.enable = true;` / `programs.starship.enable = true;` lines (before the `systemd.user.services.tailscaled` block), add:

```nix
  # gpakosz/.tmux's own config, used exactly as upstream ships it --
  # both files symlinked straight from the fetched repo, nothing
  # hand-copied or reproduced. .tmux.conf.local is wrapped in
  # mkDefault so a personal base.pkl-declared flake module can set
  # its own home.file.".tmux.conf.local".source later and win -- the
  # same personal-customization path packages/flakes already use (see
  # docs/config.md), no new cloudlab.pkl field needed for this.
  home.file.".tmux.conf".source = "${tmuxDotfiles}/.tmux.conf";
  home.file.".tmux.conf.local".source = lib.mkDefault "${tmuxDotfiles}/.tmux.conf.local";
```

- [ ] **Step 4: Sanity-check the module evaluates (works on macOS too — this is eval, not build)**

Run: `nix develop --command nix eval --impure "path:./templates#homeConfigurations.docker-x86_64-linux.activationPackage.drvPath"`
Expected: prints a `/nix/store/...drv` path, no error. (Uses the `docker` template since it already imports `common.nix`; any built-in template works the same way.)

- [ ] **Step 5: Run the full test suite**

Run: `nix develop --command bash -c "go build ./... && go vet ./... && golangci-lint run ./... && go test ./..."`
Expected: all packages `ok` (`TestTemplates_AllBuildCleanly` will `SKIP` on macOS — that's expected and matches existing behavior; it runs for real in CI on Linux).

- [ ] **Step 6: Commit**

```bash
git add templates/modules/common.nix
```

Use the `/commit-tools:commit` flow:

```
Add tmux + gpakosz/.tmux to every instance

gpakosz/.tmux (github.com/gpakosz/.tmux) is a popular, opinionated
tmux config bundle, normally installed by hand-cloning the repo and
symlinking its two files. Wired in declaratively instead: pkgs.tmux
plus a pinned pkgs.fetchFromGitHub of the repo, with both
.tmux.conf and .tmux.conf.local symlinked straight from it -- no
manual clone step, no reproduced/copied config. .tmux.conf.local is
wrapped in lib.mkDefault so a personal base.pkl flake can override
just that one file later without touching this shared template,
matching how packages/flakes already separate shared defaults from
personal preference.
```

---

## Task 4: Final end-to-end verification

**Files:** none (verification only).

**Interfaces:** none.

- [ ] **Step 1: Full repo verification**

Run: `nix develop --command bash -c "go build ./... && go vet ./... && golangci-lint run ./... && go test ./..."`
Expected: every package `ok`, `golangci-lint`: `0 issues`.

- [ ] **Step 2: Confirm `cloudlab --help` lists both new commands**

Run: `nix develop --command go run . --help`
Expected: the command list includes `herdr [name]` and `tmux [session-name]` with the `Short` text from Tasks 1–2.

- [ ] **Step 3: Confirm git log shows all three commits**

Run: `git log --oneline -3`
Expected: the three commits from Tasks 1–3, in order (herdr, tmux, home-manager wiring).

No commit for this task — it's verification only, nothing to stage.
