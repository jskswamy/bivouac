# 0007: Command surface — `up` owns the repo, `sync`/`download` are for everything else

## Status

Accepted

## Context

An instance is permanently tied to one git repo (ADR-0003), and file
content is meant to always stay in sync once an instance exists — there's no
useful "instance exists but repo isn't synced yet" state worth exposing as
its own step. A design that makes the user run `up` and then a separate
`sync` before the repo is actually usable adds a step that doesn't earn its
keep.

Separately, real workflows need to move things that *aren't* the repo onto
an instance — a dataset directory, model weights, anything that shouldn't
live under continuous two-way watch (Mutagen watching a large, mostly-static
directory is pure overhead) or in git at all.

A `start`/`stop` naming scheme was considered and rejected in favor of verbs
that read naturally for what each one does here: `up`/`down` ("bring this
instance up/down"), `ssh`, `status` — none of whose meaning shifts just
because multi-instance (ADR-0002) exists.

## Decision

- **`up`** does the full bring-up in one command: create the VM, wait for
  it to be reachable, reconcile home-manager with the project's
  `cloudlab.pkl` exactly once (not twice — see ADR-0004), rsync the repo
  to `~/reponame` (independent of reconcile), start `watch`. On return,
  the instance is fully live — no separate step required.
- **`provision [name]`** reconciles home-manager with the current
  `cloudlab.pkl` and does nothing else — no VM creation, no subshell, no
  repo rsync. For the common case of "I only changed `cloudlab.pkl`,
  apply it now" without either of `up`'s or `shell`'s side effects.
- **`shell`** is distinct from `ssh`: it never opens a remote session. It
  reconciles home-manager (idempotent — ADR-0005) then drops you into a
  **local** subshell with instance-specific env vars injected
  (`DOCKER_HOST=ssh://...` for docker template, `SOPS_AGE_KEY` for aide —
  ADR-0006), mirroring `devbox shell`'s "always current on entry" contract.
  `ssh` is an actual interactive session on the VM itself.
- **`sync <local-dir> [remote-dir]`** / **`download <remote-dir>
  [local-dir]`** are one-shot rsync transfers for paths *outside* the repo.
  They take explicit paths (no implicit "the repo" default, since that's
  `up`'s job now), and they never touch `cloudlab.yaml` or trigger a
  reconcile.
- **`watch`** is its own command — auto-started by `up`, but exposed so a
  stopped/dead Mutagen session can be restarted without recreating the
  instance.
- **`list`** is global — an overview across every instance, necessary once
  more than one can be alive at once.
- `down` stops `watch` before destroying the VM.

## Consequences

- `[name]` is optional on every per-instance command, defaulting to the
  current repo's derived instance name (ADR-0003); `list` is the one
  global, no-name command.
- Reconciliation triggers narrow to `{up, shell, provision}` — `sync`/
  `download` are fully orthogonal to `cloudlab.pkl`/home-manager, which is
  a deliberate simplification: they're a plain file-transfer utility, not
  a provisioning hook. All three reconciliation triggers share the exact
  same `Reconcile` function, never duplicated per command.
- No command forces a full repo re-sync short of `watch` (restart the
  session) or `down` + `up` (recreate outright) — accepted, since a
  from-scratch instance is cheap.
- `connect` remains python-template-specific; docker-template instances have
  no `connect` equivalent, `shell`/`ssh` are how you interact with them.
