# tmux + herdr Design

## Context

Instances only offer one way to keep a persistent remote session going:
`cloudlab ssh` into a plain shell, which dies with the connection. Two tools
address this from different angles — `pkgs.herdr` (already in
`templates/modules/common.nix`'s `home.packages`, unused until now) is a
background server built specifically for coding-agent sessions that survive
disconnects; `gpakosz/.tmux` is a popular, opinionated tmux configuration
bundle. The user wants both wired up as first-class, no-manual-steps
options: `cloudlab herdr`/`cloudlab tmux` should connect the same way
`cloudlab ssh` does, and tmux's config should arrive via home-manager, not a
manual `git clone`.

Herdr's own docs (`herdr.dev/docs/how-to-work/`) confirm the thin-client
invocation for a remote host:

```
herdr --remote ssh://you@server
```

This bridges over SSH itself — cloudlab only needs to supply the right
`ssh://user@ip` target, the same `user`/`ip` every other remote call site
already gets from `state.Record`.

## Architecture

Both new commands follow the exact shape `internal/lifecycle/ssh.go`
already established: a pure `xArgs(...)` argv-builder (unit tested
directly) plus a thin `X(ctx, ...)` wrapper that `exec.LookPath`s the real
binary and execs it with stdio passed straight through — no PTY/raw-mode
handling of cloudlab's own, no new abstractions. Both wire into
`cmd/lookup_run.go`'s existing `lookupCommandSpecs` table, the same
mechanism every other lookup-only command (`ssh`, `watch`, `status`, ...)
already uses.

tmux itself (the binary and the gpakosz/.tmux config) is a home-manager
addition to `templates/modules/common.nix`, not a Go change — every
instance gets it by default, same tier as `herdr`/`tailscale`/`fish`.

## Components

| Piece | Where | What it does |
|---|---|---|
| `cloudlab herdr [name]` | `internal/lifecycle/herdr.go` (new) | `herdrArgs(ip, user)` → `["--remote", "ssh://" + user + "@" + ip]`; `Herdr(ctx, ip, user)` execs `herdr` with those args, stdio passthrough |
| `cloudlab tmux [session-name]` | `internal/lifecycle/tmux.go` (new) | `tmuxArgs(ip, user, session)` → `["-t", user + "@" + ip, "tmux", "new-session", "-A", "-s", session]`; `Tmux(ctx, ip, user, session)` execs the real `ssh` binary with those args, stdio passthrough. `-t` forces a PTY (tmux needs one); `new-session -A -s <name>` is tmux's own create-or-attach primitive — no cloudlab-side session bookkeeping |
| tmux + gpakosz/.tmux | `templates/modules/common.nix` | `pkgs.tmux` added to `home.packages`; `tmuxDotfiles = pkgs.fetchFromGitHub { owner = "gpakosz"; repo = ".tmux"; rev = <latest upstream commit SHA>; hash = <its nix hash>; }`, both values computed the standard way (`nix-prefetch-github gpakosz .tmux`) when the code is written, then updated the same way any pinned dependency in this repo gets bumped later; `home.file.".tmux.conf".source` and `home.file.".tmux.conf.local".source` symlinked straight from `tmuxDotfiles` — both upstream files used as-is, nothing hand-copied or reproduced |
| `.tmux.conf.local` override hook | `templates/modules/common.nix` | its `home.file` definition wrapped in `lib.mkDefault`, so a user's own `base.pkl`-declared flake (the existing personal-customization path — see `packages`/`flakes` in `docs/config.md`) can set the same `home.file.".tmux.conf.local".source` to their own config and win, with no new `cloudlab.pkl` schema field needed |

## Data Flow & Error Handling

- **herdr/tmux command resolution**: identical to every existing lookup
  command — `identity.InstanceName` (positional → `--name` → cwd-derived),
  then `state.Store.Get(name)`, then the shared "no instance named %q"
  error on a miss. `herdr` is `named: true` (its optional positional is the
  instance name, matching `ssh`). `tmux` is `named: false` (its optional
  positional is the *session* name, matching how `sync`/`download`'s
  positionals are paths, not instance names) — the instance itself always
  resolves via `--name`/cwd, never the session-name positional.
- **tmux session name default**: `"main"` when no positional is given.
- **Missing binary**: both `Herdr` and `Tmux` check `exec.LookPath` first
  (`herdr`/`ssh` respectively) and return a clear "not found on PATH"
  error, matching `SSH()`'s existing behavior — no silent fallback.
- **Exit codes**: stdio and exit status pass straight through from the
  subprocess, same as `cloudlab ssh` today.
- **`.tmux.conf.local` override**: purely a Nix module-priority mechanism
  (`mkDefault` vs. a plain value) — no cloudlab code path involved, no
  runtime behavior to get wrong.

## Testing

- `herdrArgs`/`tmuxArgs`: pure-function tests asserting exact argv, same
  style as `TestSSHArgs_BuildsExpectedCommand`.
- `Herdr`/`Tmux` themselves: not unit tested beyond the argv builders,
  same precedent as `SSH()` — an interactive exec with stdio passthrough
  isn't meaningfully testable without a real terminal.
- `cmd/lookup_test.go`'s existing table-driven tests extend to cover
  `herdr [name]` and `tmux [session-name]` resolving/erroring the same way
  every other entry does (`no instance named %q` in a state-less temp
  dir).
- The Nix-side changes (tmux package, `home.file` wiring) are covered by
  the existing `TestTemplates_AllBuildCleanly` (Linux-only, runs in CI) —
  no new Go test needed for the Nix module itself, consistent with how
  `herdr`/`tailscale`/`fish` were added without dedicated tests.
