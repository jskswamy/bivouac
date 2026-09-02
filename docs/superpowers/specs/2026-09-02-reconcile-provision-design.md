# Reconcile + `provision` Command

## Status

Proposed

## Context

The Provisioning sub-project (`internal/provisioning`) built every artifact
a live instance needs — template-ref resolution, the render-trigger rule,
per-instance flake rendering, offline validation — but deliberately stayed
artifacts-only, producing Go values and strings without ever touching SSH
or a live instance (`cloudlab-du7`). Two decisions already on record
(`cloudlab-998`, `cloudlab-g42`) settled the high-level shape of what comes
next: a single shared `Reconcile` function, called exactly once by `up`
(after VM creation) and by `shell` (before dropping into a subshell), plus
a new standalone `provision` command for the common case of "I only
changed `cloudlab.pkl`, apply it now" without either side effect.

`up` and `shell` are both still stub commands today — `up` returns
"not implemented yet" immediately, and `shell` (like every other
lookup-only command) does the same. Filling in `up`'s full flow needs VM
creation (exists), rsync, and `watch` (Mutagen — doesn't exist yet).
Filling in `shell`'s full flow needs a local subshell with
`DOCKER_HOST`/`SOPS_AGE_KEY` environment injection (doesn't exist yet,
undesigned). Both of those are separate, larger, not-yet-designed
sub-projects. This spec scopes down to the one piece all three commands
share: `Reconcile` itself, wired into a real, fully working `provision`
command. `up` and `shell` remain stubs after this work — they are not
touched.

## Decisions

- **New `internal/reconcile` package**, not an addition to
  `internal/provisioning`. `internal/provisioning`'s own package doc
  states it never touches Pkl, SSH, or a live instance — `Reconcile`
  needs all three, so it stays a separate consumer rather than blurring
  that boundary.
- **SSH via `golang.org/x/crypto/ssh`, a real Go client library** — not
  shelling out to the system `ssh`/`scp` binaries the way `nix`/`pkl`/`git`
  are invoked elsewhere in this codebase. Trades the free `~/.ssh/config`
  and `known_hosts` handling the system binary gives away for direct
  control over the connection and, importantly, real end-to-end testing
  against an in-process fake SSH server (see Testing) instead of needing
  a live VM or a subprocess-mocking layer.
- **Auth via `ssh-agent`** (`golang.org/x/crypto/ssh/agent` against
  `SSH_AUTH_SOCK`), not a directly-loaded private key file. Matches how
  the system `ssh` binary itself authenticates, works with
  passphrase-protected and hardware-backed keys, and needs no config to
  say which key file corresponds to which registered DigitalOcean SSH
  key.
- **Trust-on-first-connect host key verification**, writing to the user's
  real `~/.ssh/known_hosts`. A freshly created VM's host key is never
  already known; this accepts it on first connect (same as answering
  "yes" to `ssh`'s interactive prompt, non-interactively) and verifies
  strictly on every later connection to the same instance. A changed key
  (e.g. `down` + `up` recreated the VM at the same IP) is caught rather
  than silently accepted, unlike blanket `InsecureIgnoreHostKey`.
- **Ship the rendered flake via `cat > path`, not the SCP protocol** —
  already decided in `cloudlab-998`. One SSH session, content piped to
  its stdin; no separate file-transfer protocol/subsystem needed.
- **No streaming output in this phase.** `Reconcile` runs to completion
  and returns success or a wrapped error with the tail of combined
  output. Live progress streaming is `cloudlab-b90`'s separate, already
  ready epic — not designed or built here. Nothing in this design
  precludes `Reconcile` later accepting an `io.Writer` for live output
  (a non-invasive addition via `io.MultiWriter`), but that's `b90`'s call
  to make, not this spec's.

## Architecture

```
internal/reconcile/
  ssh.go            SSH client wrapper (connect, write file, run command)
  ssh_test.go       tests against a real in-process fake SSH server
  reconcile.go       Reconcile(ctx, name string) error
  reconcile_test.go  end-to-end tests against the same fake server

cmd/
  provision.go       new `provision [name]` command
```

`internal/reconcile` depends on `internal/config` (resolve `cloudlab.pkl`),
`internal/provisioning` (template resolution, render-trigger rule,
rendering — all pure/local, already built), and `internal/state` (look up
the instance's IP, recorded by `up` at VM creation time). It does not
depend on `internal/provider` — VM creation/destruction is out of scope
here.

## Components

- **`internal/reconcile/ssh.go`**
  - `Connect(ctx context.Context, ip string) (*Client, error)` — dials
    `ip:22`, authenticates via `ssh-agent`, verifies the host key via a
    trust-on-first-connect `known_hosts` callback.
  - `(*Client) WriteFile(remotePath, content string) error` — opens a
    session, runs `cat > <remotePath>` (creating parent directories via a
    preceding `mkdir -p` in the same command), pipes `content` to its
    stdin.
  - `(*Client) Run(cmd string) (output string, err error)` — opens a
    session, runs `cmd`, returns combined stdout+stderr; a non-zero exit
    becomes a non-nil `err` with `output` still populated for the caller
    to include in its own error message.
  - `(*Client) Close() error`.

- **`internal/reconcile/reconcile.go`**
  - `Reconcile(ctx context.Context, name string) error`:
    1. `state.Open()` then `.Get(name)` → the instance's `Record` (its
       `IP`). Not found: `fmt.Errorf("instance %q not found — run \"cloudlab up %s\" first", name, name)`.
    2. `config.Resolve(ctx, cloudlab.pkl path)` → `cfg`. Errors pass
       through unchanged — `config.Resolve` already produces clear,
       actionable errors.
    3. `templateRef := provisioning.ResolveTemplateRef(cfg.Template, cfg.Arch)`.
    4. `Connect(ctx, record.IP)`.
    5. If `provisioning.NeedsRender(cfg)`: `provisioning.Render(cfg, templateRef)`,
       `WriteFile("/root/.cache/cloudlab/flake.nix", content)`, and the
       flake argument for the next step becomes
       `"path:/root/.cache/cloudlab#default"` (matches `Render`'s own
       `homeConfigurations.default` output name). Otherwise the flake
       argument is `templateRef` directly — it already resolves to the
       right `homeConfigurations.<name>-<system>` output, no file to ship.
    6. `Run("nix run home-manager -- switch --flake " + flakeArg)`.
    7. Non-zero exit → wrapped error (see Error Handling); zero exit →
       `nil`.

- **`cmd/provision.go`**
  - `provision [name]` — resolves the instance name via
    `identity.InstanceName` (same pattern as the existing `shell`/`ssh`/
    `status` stubs in `cmd/lookup.go`), calls `reconcile.Reconcile(ctx, name)`,
    prints a one-line success message or returns the error as-is (cobra
    prints it).

## Data Flow

```
provision [name]
  → identity.InstanceName(cwd, --repo, positional, --name)   (existing pattern)
  → reconcile.Reconcile(ctx, name)
       1. state.Open().Get(name)                    → Record{IP, ...}
       2. config.Resolve(ctx, cloudlab.pkl path)     → cfg
       3. templateRef := provisioning.ResolveTemplateRef(cfg.Template, cfg.Arch)
       4. Connect(ctx, record.IP)
       5. if provisioning.NeedsRender(cfg):
            content, _ := provisioning.Render(cfg, templateRef)
            client.WriteFile("/root/.cache/cloudlab/flake.nix", content)
            flakeArg := "path:/root/.cache/cloudlab#default"
          else:
            flakeArg := templateRef
       6. client.Run("nix run home-manager -- switch --flake " + flakeArg)
       7. non-zero exit → error wrapping combined output; zero exit → nil
```

Steps 1-3 run entirely on the local machine, before any network access —
the same "resolve everything locally, then ship the result" principle
`cloudlab-g42` already established for the eventual `up` flow. Steps 4-6
are the only ones that touch the network.

## Error Handling

- **Instance not found**: `fmt.Errorf("instance %q not found — run \"cloudlab up %s\" first", name, name)`.
- **SSH connect failure** (agent unreachable, no identities loaded,
  connection refused/timeout): wrapped with which IP/instance it was
  trying to reach.
- **Host key mismatch on reconnect**: surfaced verbatim from the
  `knownhosts` callback, with a hint that recreating the instance via
  `down` + `up` requires removing the stale `known_hosts` entry.
- **Remote command failure** (`home-manager switch` exits non-zero):
  unlike `provisioning.Validate`'s `nix eval`, there's no single
  `error:`-prefixed line to extract — home-manager's failure output can
  be long. The error wraps the combined output capped to its last ~40
  lines, plus which step failed (file write vs. switch).
- **Config resolution failure**: passes through unchanged.

## Testing

- **`internal/reconcile/ssh_test.go`**: a real, local, in-process SSH
  server via `golang.org/x/crypto/ssh`'s server-side API — a `localhost`
  listener, a throwaway host key generated at test time, a test client
  keypair for auth. Tests connect to `localhost:<port>` instead of a real
  VM. This mirrors `provisioning.Validate`'s own precedent of testing
  against real behavior (`nix eval`) rather than a mock: a real SSH
  protocol handshake and real channels, just not a real cloud VM. Covers
  `WriteFile` (server-side handler records what was piped to the
  `cat >` session, asserts exact content), `Run` (server-side handler
  echoes a canned exit code/output, asserts `Run` surfaces both), and
  trust-on-first-connect behavior (first connect writes a `known_hosts`
  entry; a second connect presenting a *different* host key is
  rejected).
- **`internal/reconcile/reconcile_test.go`**: the same fake SSH server,
  plus a temp `state.Store` seeded with a fake `Record{IP: "127.0.0.1", ...}`
  pointing at the fake server's port, and a temp `cloudlab.pkl`. Drives
  `Reconcile` end-to-end and asserts against what the fake server's
  handler recorded: a no-`packages` config invokes `Run` with the bare
  template ref and never calls `WriteFile`; a config with `packages` calls
  `WriteFile` with content matching `provisioning.Render`'s own output,
  then invokes `Run` with the `path:...#default` flake argument.
- No real DigitalOcean VM or network access needed for any test — the
  same "zero credentials in CI" property the `digitalocean` provider
  tests already have.

## Out of Scope

- `up`'s full flow (VM creation, rsync, `watch`) — VM creation already
  exists in `internal/provider/digitalocean`; rsync and `watch` (Mutagen)
  don't exist yet and are separate, undesigned sub-projects. `up` remains
  a stub after this work.
- `shell`'s local subshell with `DOCKER_HOST`/`SOPS_AGE_KEY` environment
  injection — undesigned, separate sub-project. `shell` remains a stub
  after this work.
- Live progress streaming during `Reconcile` — `cloudlab-b90`'s epic,
  already ready, not touched here.
- ADR updates: `docs/adr/0004-nix-home-manager-provisioning.md` (still
  describes cloud-init running `home-manager switch` directly — stale
  against the single-pass `Reconcile` design) and
  `docs/adr/0007-command-surface.md` (needs a `provision` row per
  `cloudlab-998`) both need amending as part of the implementation plan
  for this spec, but are documentation updates, not new design decisions.
