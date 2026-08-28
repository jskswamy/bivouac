# Foundation: CLI scaffold, instance identity, state store

## Status

Proposed

## Context

cloudlab's design is locked (see `docs/architecture.md` and `docs/adr/`), but
no code exists yet — no `go.mod`, nothing. This is the first implementation
sub-project: the plumbing every command depends on, before the provider
layer (DigitalOcean), provisioning (Nix/home-manager), or any command's real
behavior is built.

This spec covers:

- Project scaffold and CLI framework choice
- The full command surface, registered and stubbed
- Instance identity resolution (ADR-0003), refined beyond the ADR's wording
- The instance state store

It does not cover: the `Provider` interface/DigitalOcean implementation,
cloud-init/Nix provisioning, `cloudlab.yaml` parsing, or any command's real
logic beyond `list`. Those are later sub-projects.

## Decisions not settled by existing ADRs

The ADRs and `architecture.md` lock the *what* but leave several *how*
questions open. This spec settles them:

- **CLI framework: Cobra.** The de facto standard for Go CLIs (`kubectl`,
  `gh`, `hugo`). The command surface maps directly onto it — each verb is a
  `cobra.Command`, `[name]` is an optional positional arg, `--repo`/`--name`
  are persistent flags.
- **No Viper.** Viper earns its keep merging config from flags + env vars +
  multiple file formats. Nothing here needs that: `cloudlab.yaml` (a later
  phase) is a plain `yaml.Unmarshal`, and a future DigitalOcean token is a
  single `DIGITALOCEAN_TOKEN` env var (matching `doctl`'s own convention) —
  neither needs layered-precedence config.
- **State file location: XDG, Linux-shaped on every OS.** `$XDG_STATE_HOME`
  if set, else `~/.local/state/cloudlab/state.json` — on macOS too, not Go's
  `os.UserConfigDir()` default of `~/Library/Application Support`. Resolved
  by hand, not via a library that special-cases macOS.
- **State file format: JSON.** The state file is machine-written and
  machine-read only, never hand-edited — `encoding/json` is sufficient, and
  it's visually distinct from the human-authored `cloudlab.yaml`.
- **Command shape: flat verbs, no noun grouping.** ADR-0007 already
  specifies this (`cloudlab up`, not `cloudlab instance up`); noun grouping
  is only useful once a CLI manages more than one resource kind with
  colliding verb names. cloudlab manages exactly one kind — the instance —
  for its entire surface, so `instance` in front of every command would be a
  fixed word resolving no ambiguity. Matches `vagrant`/`minikube`, the
  closest prior art for this exact tool shape.

## Command surface

All ten commands from ADR-0007, flat verbs, registered from this phase
(matching the README's documented UX today):

| Command | Args | Flags | This phase's behavior |
|---|---|---|---|
| `up` | `[name]` | `--repo`, `--name`, `--template` | Resolve repo-dependent identity, then stub |
| `shell` | `[name]` | `--repo`, `--name` | Resolve lookup identity, then stub |
| `ssh` | `[name]` | `--repo`, `--name` | Resolve lookup identity, then stub |
| `sync` | `<local-dir> [remote-dir]` | `--repo`, `--name` | Resolve lookup identity, then stub |
| `download` | `<remote-dir> [local-dir]` | `--repo`, `--name` | Resolve lookup identity, then stub |
| `watch` | `[name]` | `--repo`, `--name` | Resolve lookup identity, then stub |
| `connect` | `[name]` | `--repo`, `--name` | Resolve lookup identity, then stub |
| `status` | `[name]` | `--repo`, `--name` | Resolve lookup identity, then stub |
| `down` | `[name]` | `--repo`, `--name` | Resolve lookup identity, then stub |
| `list` | *(none)* | *(none)* | Fully real — reads the state store |

"Stub" means: identity resolution runs for real (proving the shared
plumbing works), then the command prints `"<verb>: not implemented yet"`
and exits 1. This exercises every command's identity path without faking
behavior that doesn't exist yet.

## Architecture

```
cloudlab/
├── go.mod
├── main.go                    # calls cmd.Execute()
├── cmd/                       # cobra command tree
│   ├── root.go                # root command, persistent --repo/--name flags
│   ├── up.go                  # repo-dependent command
│   ├── list.go                # the one fully-real command
│   ├── lookup.go              # table-driven: shell, ssh, sync, download,
│   │                           # watch, connect, status, down
├── internal/
│   ├── identity/               # ADR-0003: repo root + instance name resolution
│   │   └── identity.go
│   └── state/                  # instance state store
│       └── state.go
```

## Components

### `identity`

Two independent functions, not one combined resolver — repo-root finding
never takes a name as input, and name derivation never re-walks the
filesystem:

```go
// RepoRoot walks up from repoFlag (if set) or cwd to find a git repo root.
// Used only by commands that need actual repo content (currently: up).
func RepoRoot(cwd, repoFlag string) (string, error)

// InstanceName resolves a name for lookup-only commands: positional arg,
// then --name, then (if cwd or repoFlag is inside a git repo) that repo's
// derived name. Does not require a git repo to succeed if positional or
// --name is given.
func InstanceName(cwd, repoFlag, positional, nameFlag string) (string, error)
```

Name derivation from a resolved repo root (shared by both paths): `--name`
if given, else slugified `owner/repo` from `git remote get-url origin`, else
the root directory's folder name if there's no `origin` remote.

Two identity categories, matching what each command actually needs:

- **Repo-dependent** (`up` only, since it seeds repo content onto the
  instance): calls `RepoRoot`, errors if no `.git` is found via `--repo` or
  `cwd`. Name is then derived from that root (`--name` override, else
  `origin` remote, else folder name).
- **Lookup-only** (`shell`, `ssh`, `sync`, `download`, `watch`, `connect`,
  `status`, `down`): calls `InstanceName` directly. Precedence: positional
  `[name]` → `--name` → derived from `cwd`'s repo if it happens to be one →
  error only if none apply. No repo is required outside a git directory as
  long as `--name` (or a positional name) is given — these commands look up
  an already-existing instance in state; they never touch repo content.
- **`list`**: no identity resolution at all.

`RepoRoot` and `InstanceName`'s cwd-derived path both run the identical
walk-up algorithm regardless of whether the starting point is the repo root
itself or a nested subdirectory, and regardless of whether that starting
point came from `cwd` or `--repo` — one code path, no special-casing.

### `state`

A `Store` over the XDG state file (`state.go`):

```go
type Record struct {
    Name     string
    Provider string
    VMID     string
    IP       string
    Region   string
    Size     string
    Template string
    WatchPID int
    TunnelPID int
}

type Store struct { path string }

func Open() (*Store, error)              // resolves XDG path, does not require file to exist
func (s *Store) List() ([]Record, error) // empty slice if file doesn't exist yet
func (s *Store) Get(name string) (Record, bool, error)
func (s *Store) Put(r Record) error      // unused this phase, exists for schema completeness
func (s *Store) Delete(name string) error // unused this phase
```

`Put`/`Delete` are included now so the schema (matching
`architecture.md`'s state description) is settled once, even though nothing
writes a record until `up`/`down` are built in a later phase.

### `cmd`

One file per verb. Each `RunE`:

1. For `up`: calls `identity.RepoRoot`, then derives name from the result.
2. For the other 8 per-instance commands: calls `identity.InstanceName`
   directly.
3. `list`: skips identity, calls `state.Store.List()`, prints results (or
   "no instances" if empty).
4. Non-`list` commands print the "not implemented yet" stub and exit 1
   after identity resolves.

## Error handling

- `up` outside a git repo, no `--repo` given: `"not inside a git
  repository; use --repo <path>"`.
- `up` with `--repo <path>` where no `.git` is found at or above that path:
  same error, naming the bad path.
- Lookup command with no positional name, no `--name`, and `cwd` not inside
  a git repo: `"no instance name given; use --name or run from inside a
  repo"`.
- No `origin` remote on a resolved repo: falls back to folder-name naming
  (ADR-0003's documented, accepted edge case) — not an error.
- State file missing (first run): `List()` returns an empty slice, not an
  error. The file is created lazily on first write — no writer exists yet
  in this phase.
- State file present but unparseable: hard error naming the file path, no
  silent reset.

## Testing

- `identity.RepoRoot` / `identity.InstanceName`: unit tests against real
  temp git repos (`git init`, with/without `origin`, nested subdirectories)
  rather than mocked git — this is a thin wrapper over real git plumbing,
  testing the real thing is more honest than mocking it.
- `state.Store`: unit tests against a temp XDG dir — empty state, populated
  state, corrupt-JSON error case.
- `cmd`: one test per command verifying identity resolution runs and the
  right stub/behavior fires, via cobra's `Command.Execute()` against a
  buffer — no subprocess spawning needed.

## Out of scope (deferred to later sub-projects)

- `Provider` interface and DigitalOcean implementation
- Cloud-init / Nix / home-manager provisioning
- `cloudlab.yaml` parsing and module composition
- Real behavior for any command besides `list`
- Credentials (`SOPS_AGE_KEY` injection, aide integration)
