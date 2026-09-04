# Remote Path Mirroring, `sync`/`ssh` Ergonomics, and Watch Status Design

## Context

Three related gaps surfaced from live use:

1. `cloudlab sync <local-dir> [remote-dir]` requires an explicit local
   directory every time, even for the common case of "sync whatever
   directory I'm standing in right now."
2. Every remote path cloudlab computes today is a flat, disconnected
   name: the main repo (synced by `up`, kept live by `watch`) always
   lands at `~/<instance-name>`, and `cloudlab sync`'s own default
   target is `~/<basename(local)>`. Neither has any relationship to
   where the thing actually lives locally, which makes an instance's
   filesystem layout unpredictable and unfamiliar once you're SSHed in.
3. Nothing shows whether `watch` (the continuous two-way Mutagen sync
   `up` starts automatically) is actually working. `cloudlab status`'s
   own `Short` text already promises "sync/watch state," but
   `runStatus` (`cmd/lookup_run.go`) never implements it — confirmed by
   reading the code.

`mutagen sync list <name> --template='...'` was confirmed live (real
mutagen 0.18.1) to expose exactly the missing signal: `.Status` (a
live state like `Watching`/`ConnectingBeta`/`Reconciling`), per-endpoint
`.Alpha.Connected`/`.Beta.Connected`, `.Conflicts`, and `.LastError` —
all machine-parseable via `--template`, no text-scraping needed. A
nonexistent session reports a distinct, non-error condition ("did not
match any sessions") that's cleanly distinguishable from a real
failure.

`--mkpath` (rsync ≥ 3.2.3; confirmed present both locally at 3.5.0 and
on Ubuntu 24.04's packaged 3.2.7) creates every missing destination
path component in one flag, using rsync's own real permission check —
no separate SSH round-trip or `--rsync-path` shell trick needed.

## Architecture

A single new helper, `lifecycle.RemotePath(localPath, remoteUser
string) (string, error)`, computes where any local path lands on an
instance: mirrored under the remote user's home if the local path is
under the local user's home, otherwise used as-is (identical absolute
path on both ends). Every place that currently invents its own remote
path — the main repo sync, `watch`'s Mutagen session, `sync`'s default
target, `ssh`'s auto-`cd` target — calls this one function instead.
`Push` (the shared rsync wrapper) always passes `--mkpath`, so the
happy path (destination under something the remote user owns) needs no
special-casing; the unhappy path (genuinely unwritable) surfaces as a
wrapped rsync error with an actionable hint.

## Components

| Component | Where | What it does |
|---|---|---|
| `RemotePath(localPath, remoteUser string) (string, error)` | `internal/lifecycle/remotepath.go` (new) | `os.UserHomeDir()` for the *local* side; if `localPath` is under it, returns `/home/<remoteUser>/<localPath minus local home>`; otherwise returns `localPath` unchanged. Pure path logic, no network/SSH involved. |
| Main repo location | `internal/lifecycle/lifecycle.go` (`Up`), `watch.go` (`StartWatch`/`mutagenCreateArgs`), `internal/state/state.go` | `Up` computes `RemotePath(repoRoot, remoteUser)` once and both uses it for the initial rsync target and stores it as a new `state.Record.RepoPath` field, alongside the existing `User` field. `StartWatch`'s Mutagen session also targets it. Storing it (rather than having every later command recompute it from a local cwd) is what makes `ssh`'s auto-`cd` work when invoked from outside the repo checkout — see below. The instance name keeps being used for Mutagen's own `--name=<instance>` session identity — only the destination *directory* changes. |
| `Push` gains `--mkpath` | `internal/lifecycle/rsync.go` | Added unconditionally to `rsyncPushArgs`. On failure, `Push`'s wrapped error gains one appended line: `if <remote-path> doesn't exist or isn't writable, create it and chown it to <remote-user> on the instance, then retry`. |
| `cloudlab sync [remote-dir] --dir <local-dir>` | `cmd/lookup.go` (spec/flag), `cmd/lookup_run.go` (`runSync`) | New `--dir` flag, defaulting to `os.Getwd()`. `remote-dir` stays an optional positional; when omitted, defaults via `RemotePath(local, record.User)` instead of the old basename-only scheme. |
| `cloudlab ssh [name] [--dir <path>]` | `cmd/lookup.go`, `cmd/lookup_run.go` (`runSSH`), `internal/lifecycle/ssh.go` | Default: `cd`s into `record.RepoPath` (read from state, not recomputed — `ssh` doesn't require running from inside the repo checkout, unlike `watch`) before an interactive login shell. `--dir <path>` overrides the target. An instance created *before* this change has an empty `RepoPath` (new field, zero value on old records) — treated the same as `--dir ""`: no `cd`, falls back to today's plain-login-session behavior, not an error. Uses the same `-t <user@ip> "cd <dir> && exec $SHELL -l"` shape `tmux.go` already established for a remote-command-plus-interactive-shell. |
| `lifecycle.WatchStatus(ctx, name string) (WatchStatus, error)` | `internal/lifecycle/watch.go` | Runs `mutagen sync list <name> --template='...'`, parsing `Status`, `AlphaConnected`, `BetaConnected`, `Conflicts`, `LastError` via a pipe-delimited template. A "did not match any sessions" failure is mapped to `WatchStatus{Running: false}`, not an error. |
| `runStatus` gains a `Watch:` line | `cmd/lookup_run.go` | Calls `WatchStatus` and prints `Watch:    not running` or `Watch:    <status> (conflicts: N)` (plus a `LastError:` line when non-empty), alongside the existing fields. |

## Data Flow & Error Handling

- **`RemotePath`**: string/path manipulation only — `filepath.Clean` both
  sides before comparing, use `filepath.Rel` (or an equivalent prefix
  check on cleaned, separator-bounded paths — never a raw `strings.HasPrefix`,
  which would wrongly match `/Users/subramk2` against home `/Users/subramk`)
  to detect containment. No SSH, no provider calls — this only needs
  `os.UserHomeDir()` and the two path strings, so it's trivially unit
  testable.
- **`Push` with `--mkpath`**: rsync's own remote-side permission check
  decides success/failure; cloudlab adds no pre-flight check of its
  own. A failure's wrapped error always appends the create-and-chown
  hint (not conditional on parsing rsync's stderr for a specific
  pattern — simpler, and rsync's error text isn't a stable thing to
  pattern-match against).
- **`cloudlab sync`**: `--dir` resolution failure (`os.Getwd()`
  erroring) surfaces directly, same as any other command that resolves
  cwd today.
- **`cloudlab ssh --dir`**: no validation of the given path before
  connecting — if it doesn't exist, the remote shell's own `cd` failure
  is what the user sees, same as typing a bad path into any other `cd`.
- **Instances created before this change**: `state.Record.RepoPath` is a
  new field, so an old record decodes it as `""`. `ssh` treats that the
  same as no `--dir` override being possible — falls back to today's
  plain login session, not an error. No migration path, matching how
  the earlier root-to-sudo-user switch was also new-instances-only.
- **`WatchStatus`**: `mutagen sync list <name>` exiting non-zero with
  its own "did not match any sessions" message is the *only* case
  mapped to `Running: false` — any other non-zero exit (mutagen not on
  PATH, daemon not running, a real Mutagen-side error) is returned as a
  genuine error from `WatchStatus`, surfaced by `runStatus` the same
  way `Status`'s own `LiveErr` already is (printed inline, not fatal to
  the rest of the command's output).

## Testing

- `RemotePath`: pure-function table tests — under home (nested path),
  exactly equal to home, not under home (absolute elsewhere), and a
  path that merely shares a string prefix with home without being a
  real subdirectory (`/Users/subramk2/x` vs home `/Users/subramk`) to
  prove the containment check isn't a naive string prefix match.
- `rsyncPushArgs`: extend the existing argv-builder test to assert
  `--mkpath` is present.
- `Push` error wrapping: extend `TestPush_FailureIncludesRsyncOutputInError`
  to also assert the create-and-chown hint text is present.
- `cloudlab sync`/`cloudlab ssh` flag wiring: cmd-level tests following
  this codebase's existing pattern (`cmd/lookup_test.go`'s table tests)
  — asserting `--dir` overrides cwd, and that omitting `remote-dir`
  resolves through `RemotePath` rather than the old basename scheme.
- `WatchStatus`: unit tests against a fake/stubbed `mutagen` invocation
  are impractical (this project shells out to the real binary
  everywhere else, per `rsync_test.go`/`watch_test.go`'s existing
  precedent) — cover it with a real local Mutagen session the same way
  `TestMutagenSync_RealLocalToLocalSession` and
  `TestStartWatch_TerminatesExistingSessionFirst` already do (skip if
  `mutagen` isn't on `PATH`), plus a dedicated case asserting the
  "no session" condition parses to `Running: false`, not an error.
