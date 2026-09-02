# `up`'s VM/Reconcile/Rsync/Watch Lifecycle

## Status

Proposed

## Context

`up` is still a stub — it resolves the instance name and returns
"not implemented yet" immediately. Everything it needs already exists as
building blocks (`internal/provider/digitalocean` creates VMs,
`internal/reconcile.Reconcile` resolves config and runs `home-manager
switch` over SSH), but nothing wires them together, and two real pieces
are still missing entirely: waiting for a freshly created VM to actually
be usable (cloud-init installing Nix takes real time after boot), and
continuous two-way repo sync (`watch`, via Mutagen — not integrated at
all yet).

This spec covers `up`'s full flow only: create the VM, wait until it's
genuinely ready, reconcile, rsync the repo in, start watch. `shell`'s
remaining piece (reconcile, then a local subshell with
`DOCKER_HOST`/`SOPS_AGE_KEY` injected) is a separate, smaller spec —
it shares nothing novel with `up`'s VM-readiness/rsync/watch work beyond
the already-built `Reconcile`.

The revised flow itself — single-pass reconcile, rsync independent of
reconcile — was already decided in `cloudlab-g42`:

```
1. Create VM (cloud-init installs Nix only).
2. Wait until Nix is installed and the VM is reachable over SSH.
3. Reconcile once, with the fully-resolved local config.
4. Rsync the repo (independent of step 3).
5. Start watch.
```

This spec designs the mechanics of steps 1-2 and 4-5 (step 3 already has
its own spec) and settles a few gaps that surfaced along the way.

## Decisions

- **Readiness: retry `Connect`, then `cloud-init status --wait` over
  that connection.** Covers both "SSH not up yet" (retry loop) and
  "cloud-init still installing Nix" (cloud-init's own built-in
  readiness command, run remotely once SSH is reachable) without
  inventing a custom sentinel file or conflating "still booting" with
  "genuinely broken" the way a bare retry-Reconcile-until-it-works
  approach would.
- **Mutagen is a required binary, not optional for this phase.**
  Checked via `exec.LookPath("mutagen")` with a clear install-hint
  error, matching the existing `pkl`/`nix` pattern
  (`config.Resolve`'s own `pkl`-not-found error). `watch` is core to
  the documented `up` contract ("instance is fully live, no separate
  step required") — skipping it would mean shipping `up` with silently
  degraded behavior instead of a clear either/or story.
- **Watch session name = instance name; no PID tracking.** Mutagen
  manages its own background daemon and named sessions
  (`mutagen sync create/list/terminate`), not a one-PID-per-instance
  model. `state.Record`'s existing `WatchPID` field (reserved before
  Mutagen was actually integrated) doesn't fit this tool and goes
  unused for watch — sessions are looked up by name (`--name=<instance>`)
  wherever they're referenced later (`status`, `down`, `watch` restart).
- **New `cloudlab.pkl` field: `image`**, defaulting to
  `"ubuntu-24-04-x64"` (current DigitalOcean Ubuntu LTS slug). No
  image/OS concept existed anywhere in the schema or `InstanceSpec`
  before this; per ADR-0004 a single Ubuntu image is enough today
  (all template differentiation lives in the Nix flake, not the base
  image) — but this project's own maintainer preference is to make it
  user-overridable in `cloudlab.pkl` from the start rather than
  hardcoding it, so it lands as a real schema field.
- **`rsync`/`mutagen` invoked via `os/exec`, matching `nix`/`pkl`.**
  Both are C/Go binaries with their own well-established remote-shell
  and sync-protocol implementations — reimplementing either in Go would
  be pure waste. `rsync -e ssh` and Mutagen's own SSH transport both
  rely on the system `ssh` binary (not this project's Go SSH client),
  so both depend on `WaitReady`'s `Connect` call having already
  completed trust-on-first-connect against `~/.ssh/known_hosts` — by
  the time `Rsync`/`StartWatch` run, the host key is already recorded.
- **State is written immediately after `Create` succeeds**, before any
  of the following steps — a failure in readiness/reconcile/rsync/watch
  still leaves the VM tracked and destroyable via `down`, rather than
  orphaned.
- **No idempotent re-run handling.** Re-running `up` against a name
  whose VM/watch-session already exists (without `down` first) is
  deliberately unguarded — this project's own established recovery
  path is `down` + `up` (recreate from scratch, cheap for an ephemeral
  instance), not repairing a partially-up instance in place.

## Architecture

A new `internal/lifecycle` package holds everything `up` needs beyond
what already exists. `cmd/up.go` orchestrates the full sequence
directly — this is the only command that needs all of it (unlike
`Reconcile`, shared by three commands), so there's no shared
orchestration function to extract it into; keeping `cmd/up.go`'s own
`RunE` thin instead means pulling the *orchestration itself* out into
`lifecycle.Up`, so `RunE` only resolves flags/identity and makes one
call.

```
internal/lifecycle/
  lifecycle.go   Up(ctx, p provider.Provider, name, cloudlabPath, repoRoot string) error
  ready.go       WaitReady(ctx, ip string, timeout time.Duration) error
  rsync.go       Rsync(ctx, ip, localRepoRoot, remoteName string) error
  watch.go       StartWatch(ctx, ip, name, localRepoRoot string) error

internal/config/Config.pkl   + image: String = "ubuntu-24-04-x64"
cmd/up.go                    resolve identity → lifecycle.Up(...)
```

`lifecycle.Up`'s own body is a straight six-step sequence (create →
record state → wait ready → reconcile → rsync → start watch), each a
single delegated call with error wrapping naming which step failed —
no branching logic of its own beyond that.

## Components

- **`internal/lifecycle/lifecycle.go`** —
  `Up(ctx context.Context, p provider.Provider, name, cloudlabPath, repoRoot string) error`:
  1. `cfg, err := config.Resolve(ctx, cloudlabPath)`.
  2. `spec := provider.InstanceSpec{Name: name, Region: *cfg.Region, Size: *cfg.Size, Image: cfg.Image, SSHKeys: *cfg.SshKeys, UserData: provisioning.CloudInitUserData}`.
  3. `vm, err := p.Create(ctx, spec)`.
  4. `state.Open()` then `.Put(Record{Name: name, Provider: "digitalocean", VMID: vm.ID, IP: vm.IP, Region: vm.Region, Size: vm.Size, Template: *cfg.Template})` — if this fails, best-effort `p.Destroy(ctx, vm.ID)` before returning, naming the VM ID in the error either way.
  5. `WaitReady(ctx, vm.IP, 5*time.Minute)`.
  6. `reconcile.Reconcile(ctx, name, cloudlabPath)` — re-resolves `cfg` internally; its existing signature is unchanged, not threaded through from step 1.
  7. `Rsync(ctx, vm.IP, repoRoot, name)`.
  8. `StartWatch(ctx, vm.IP, name, repoRoot)`.
- **`internal/lifecycle/ready.go`** —
  `WaitReady(ctx context.Context, ip string, timeout time.Duration) error`:
  loops calling `reconcile.Connect(ctx, ip)` until it succeeds or
  `timeout` elapses (clear timeout error naming the IP); once connected,
  runs `cloud-init status --wait` over that connection via `Client.Run`
  and treats a non-zero exit as a genuine failure (not a retry
  condition), surfacing cloud-init's own status output.
- **`internal/lifecycle/rsync.go`** —
  `Rsync(ctx context.Context, ip, localRepoRoot, remoteName string) error`:
  `exec.LookPath("rsync")` guard, then
  `rsync -az -e ssh <localRepoRoot>/ root@<ip>:~/<remoteName>/` via
  `exec.CommandContext`, wrapping any failure with rsync's own stderr.
- **`internal/lifecycle/watch.go`** —
  `StartWatch(ctx context.Context, ip, name, localRepoRoot string) error`:
  `exec.LookPath("mutagen")` guard, then
  `mutagen sync create --name=<name> <localRepoRoot> root@<ip>:~/<name>`,
  wrapping any failure with mutagen's own output.
- **`internal/config/Config.pkl`**: add `image: String = "ubuntu-24-04-x64"`;
  regenerate `Config.pkl.go` via the existing `pkl-gen-go` pipeline;
  update `docs/config.md`.
- **`cmd/up.go`**: resolves repo root/name (existing pattern, unchanged),
  constructs the DigitalOcean provider, calls
  `lifecycle.Up(cmd.Context(), provider, name, cloudlabPath, root)`,
  prints a one-line success message on completion.

## Data Flow

```
up [name]
  → identity.RepoRoot(cwd, --repo) / derive name          (existing pattern)
  → cloudlabPath := <root>/cloudlab.pkl
  → lifecycle.Up(ctx, digitalOceanProvider, name, cloudlabPath, root)
       1. cfg := config.Resolve(ctx, cloudlabPath)          (now includes cfg.Image)
       2. spec := provider.InstanceSpec{...}
       3. vm := p.Create(ctx, spec)                         → VM{ID, IP, ...}
       4. state.Open().Put(Record{...})                     — before any step below
       5. WaitReady(ctx, vm.IP, 5*time.Minute)               — retry Connect, then cloud-init status --wait
       6. reconcile.Reconcile(ctx, name, cloudlabPath)       — resolves cfg again internally
       7. Rsync(ctx, vm.IP, root, name)
       8. StartWatch(ctx, vm.IP, name, root)
  → on full success: cmd prints a one-line summary
```

Step 4's early state write is the key reliability property: every later
step can fail without leaving an orphaned, untracked VM.

## Error Handling

- `config.Resolve`/`provider.Create` failures pass through unchanged —
  both already produce clear, actionable errors.
- `state.Put` failing after a successful `Create`: best-effort `Destroy`
  the just-created VM before returning; if that also fails, the error
  names the VM ID explicitly for manual cleanup.
- `WaitReady` timeout (5 minutes covering both SSH-not-up and
  cloud-init-still-running): a clear error naming the instance/IP,
  suggesting `cloudlab status`/manual SSH to investigate.
- `WaitReady`'s `cloud-init status --wait` returning non-zero: a
  genuine failure, surfaced immediately with cloud-init's own status
  output, not retried.
- `Reconcile` failure: unchanged from its own spec — the instance is
  already in state by this point, so its "not found" branch never
  triggers here.
- `Rsync`/`StartWatch` missing binary: `exec.LookPath` guard with an
  install hint.
- `Rsync`/`StartWatch` command failure: wrapped with the underlying
  tool's own stderr/output.
- Deliberately not handled: re-running `up` against a name whose
  VM/session already exists and wasn't torn down first (see Decisions).

## Testing

- **`WaitReady`**: a real, local, in-process fake SSH server (same
  pattern as `internal/reconcile`'s own tests — duplicated rather than
  extracted into a shared test-support package, since this is only the
  second consumer). Covers the retry-until-reachable path and both
  cloud-init outcomes (success, non-zero exit) with a short injected
  `timeout` so the timeout-path test doesn't need to wait 5 real
  minutes.
- **`StartWatch`**: a genuine integration test against the real
  `mutagen` binary — `t.Setenv("MUTAGEN_DATA_DIRECTORY", t.TempDir())`
  gives each test run an isolated daemon instance (documented Mutagen
  behavior for exactly this purpose), never touching a real
  `~/.mutagen`. Local-to-local sync (both endpoints local paths, no
  SSH) is an officially documented Mutagen use case, not an
  implementation accident — a test creates a session between two real
  local temp directories, writes a file on one side, polls for it to
  appear on the other, then terminates the session. The production
  `user@host:path` remote-argument form gets a narrower, separate
  assertion on the constructed command string, since a real SSH-backed
  sync would need a real sshd to test end-to-end.
- **`Rsync`**: same approach — a real `rsync` invocation between two
  local temp directories proves a written file genuinely round-trips;
  the production `-e ssh` remote form gets the same narrower
  command-string assertion.
- **`lifecycle.Up`**: tested against a fake `provider.Provider` (a
  small test double implementing the 4-method interface, returning a
  canned `VM`) plus the same fake SSH server, asserting the full
  sequence runs in order and that `state.Put` happens before
  `WaitReady` (a fake `WaitReady` failure still leaves the record
  behind).
- **New `image` field**: same test pattern as `arch` in the
  Provisioning plan's Task 1 — default value, override.
- `pkgs.mutagen` (nixpkgs has it, v0.18.0) is added to `flake.nix`'s
  devShell alongside the existing `go`/`pkl` tools, so it's available
  for both local dev and CI.

## Out of Scope

- `shell`'s remaining flow (reconcile + local subshell with env
  injection) — separate, smaller spec.
- Live progress streaming during any of `up`'s steps —
  `cloudlab-b90`'s separate, already-ready epic.
- A second provider, or per-provider image configuration beyond a
  single string field.
- Idempotent re-run handling for `up` against an existing, not-torn-down
  instance (see Decisions) — `down` + `up` is the supported recovery
  path.
- `watch`, `status`, `down`, `ssh`, `sync`, `download`, `connect` as
  standalone commands — `watch`'s underlying session-management pieces
  are built here (`StartWatch`), but wiring a standalone `watch`
  command (restart a stopped/dead session) or `down` (terminate it) is
  separate follow-on work, not part of `up` itself.
