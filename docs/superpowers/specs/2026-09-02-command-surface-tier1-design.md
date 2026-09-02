# Command Surface Tier 1 (down/ssh/status/watch/sync/download) Design

## Context

`up` and `provision` work end to end, but there's no way to tear an instance
back down, inspect it, or interact with it once it's live — `list` is the
only other real command. `cmd/lookup.go` already has a table-driven cobra
scaffold for the other eight commands (`shell`, `ssh`, `watch`, `connect`,
`status`, `down`, `sync`, `download`), including flag handling, `Args`
validation, and name/path resolution — every one of them currently just
returns `stubErr(verb, name)`, "not implemented yet".

ADR-0007 (`docs/adr/0007-command-surface.md`) already specifies what each
command does. This spec covers the six that ADR-0007 fully settles —
`down`, `ssh`, `status`, `watch`, `sync`, `download` — enough to round-trip
`up → use → down`. `shell` and `connect` each have an open design question
ADR-0007 doesn't answer (shell's `SOPS_AGE_KEY` source; connect's Jupyter
tunnel mechanics) and stay stubbed, deferred to their own smaller spec.

## Architecture

`lookupCommandSpec` gains a `run` field (a function taking the resolved
name/args and returning an error) instead of every spec sharing the same
`stubErr` call. The six Tier 1 specs get a real `run`; `shell` and
`connect` keep calling `stubErr`, unchanged. All name resolution, flag
parsing, and `Args` validation stay exactly as they are today — only the
dispatch target changes.

## Components

| Command | Where the logic lives | What it does |
|---|---|---|
| `down [name]` | `internal/lifecycle.Down` (new) | best-effort `mutagen sync terminate <name>`, then `provider.Destroy(vmID)` (not-found treated as success), then `state.Store.Delete(name)` — state is always cleared last, even if destroy fails for a real reason |
| `ssh [name]` | `cmd/ssh.go` (new, thin) | looks up IP from state, execs the real `ssh root@<ip>` binary with stdio passed straight through — no PTY/raw-mode handling of our own, reuses the trust-on-first-connect `~/.ssh/known_hosts` model `WaitReady` already established |
| `status [name]` | `internal/lifecycle.Status` (new) | reads the state record, calls `provider.Get(vmID)` for live status; a `Get` failure doesn't fail the whole command — local fields still print, with the live-check error reported inline |
| `watch [name]` | `internal/lifecycle.StartWatch` (extended) | derives the local repo root from cwd via `identity.RepoRoot` (must be run from the repo checkout — `--name` only overrides the instance *name*, not the local path), best-effort `mutagen sync terminate` then `StartWatch` (idempotent restart if a session by that name already exists) |
| `sync <local> [remote]` | `internal/lifecycle` (generalized `Rsync`) | push; default remote = `~/<basename(local)>` |
| `download <remote> [local]` | `internal/lifecycle` (generalized `Rsync`) | pull (reverse rsync direction); default local = `./<basename(remote)>` |

`Rsync`'s current argv-building (`rsyncArgs`) splits into `rsyncPushArgs`/
`rsyncPullArgs`; `up`'s existing call becomes a thin wrapper over the push
variant with its existing repo-root/`~/<name>` defaults, so `up` and the
new `sync`/`download` never duplicate argv-building logic.

## Data Flow & Error Handling

All six commands resolve the instance name exactly as they do today
(`identity.InstanceName`: positional → `--name` → cwd-derived), then look
up `state.Store.Get(name)`. A missing record is one shared error path:
`"no instance named %q (run cloudlab up first)"` — every command below
`list`/`up` fails identically on an unknown name.

- **down**: `mutagen sync terminate` errors are swallowed (a session
  that's already dead or never started isn't a failure). `provider.Destroy`
  returning `provider.ErrNotFound` is success, not surfaced. `state.Delete`
  always runs last — if `Destroy` fails for a real (non-not-found) reason,
  that error is still returned to the user ("VM destroy failed: %w, check
  the DO dashboard"), but state is cleared regardless so the user isn't
  stuck unable to retry.
- **ssh**: stdio connects straight through to the `ssh` subprocess; its
  exit code becomes cloudlab's exit code. No retry, no host-key handling
  of its own.
- **status**: a `provider.Get` failure (network error, not not-found)
  still prints local state fields, with the live-check failure reported
  inline rather than failing the whole command.
- **watch**: if `identity.RepoRoot` fails (cwd isn't inside a git repo),
  that error surfaces directly — watch cannot guess a local path.
- **sync/download**: no existence check on the local path beyond what
  `rsync` itself reports — a typo'd path just becomes rsync's own "no such
  file" error.

## Testing

- **down**: unit tests against a `fakeProvider` (extended to track
  `Destroy` calls) + a real `state.Store` pointed at a temp dir, covering
  normal destroy, `ErrNotFound` treated as success, and state cleared even
  when destroy fails. The `mutagen sync terminate` step runs against the
  real binary with an isolated `MUTAGEN_DATA_DIRECTORY` (same pattern as
  `watch`'s existing test) to confirm terminating a nonexistent session is
  swallowed.
- **ssh**: argv-building (`ssh root@<ip>`, port handling) is a small pure
  function tested directly, matching how `rsyncArgs`/`mutagenCreateArgs`
  are already tested separately from their `exec.Command` call.
- **status**: `fakeProvider` covering both a successful `Get` and a `Get`
  error (asserting local fields still print).
- **watch**: extends `StartWatch`'s existing real local-to-local Mutagen
  integration test to cover the restart path — create a session, call the
  restart logic again, assert no error from the name already being in use.
- **sync/download**: extends `Rsync`'s existing real local-rsync
  integration test to both directions, plus pure-function tests for
  default-path derivation (`~/<basename>` / `./<basename>`).
- **cmd-level**: `cmd/lookup_test.go`'s existing table tests update for
  the six real commands (no longer expect "not implemented yet" — expect
  the "no instance named" error in a state-less temp dir, same style as
  `up`'s own `MissingTokenErrors` test). `shell`/`connect` keep their
  current stub assertions unchanged.
