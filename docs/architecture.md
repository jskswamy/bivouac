# Architecture

## Overview

cloudlab is a Go CLI that manages **instances**: named, ephemeral cloud VMs
provisioned from a **template** (`python` or `docker`) plus whatever the
repo's own `cloudlab.pkl` adds on top. Multiple named instances can exist at
once, each brought up, worked in, and torn down independently. The compute
backend is a cloud VM reached over the network, not a process on your
machine, and provisioning is done with Nix + home-manager instead of ad hoc
package installs.

DigitalOcean is the only provider implemented so far, but the provider
boundary is deliberately not DO-specific — see
[Provider abstraction](#provider-abstraction) and
[ADR-0008](adr/0008-provider-abstraction.md).

```
 local repo                              cloud VM ("instance")
┌──────────────┐                        ┌────────────────────────────┐
│ cloudlab.pkl │ ──── up ──────────────▶ │ cloud-init: install Nix,   │
└──────┬───────┘   create VM, wait       │  create user, sudo, linger │
       │           for SSH               └────────────┬───────────────┘
       │                                              │
       │  ── reconcile (over SSH) ────────────────────▶│
       │     ship ~/.cache/cloudlab/flake.nix,        │
       │     run `home-manager switch`                │
       │                                              ▼
       │                                 ┌────────────────────────────┐
       └──── session start <name> ─────▶ │ ~/sessions/<name>/<repo>   │
             git push HEAD               │ on branch cloudlab/<name>  │
                                         └────────────┬───────────────┘
       ◀──── pull / merge (git fetch) ────────────────┘
       .worktrees/<name>
```

`up` never touches the repository — it only needs `cloudlab.pkl`, read from
the local checkout, to reconcile the Nix environment. Code first reaches the
instance when a session starts, and moves back as git commits. See
[Sessions](#sessions-code-moves-as-git-commits) below, and
[docs/superpowers/specs/2026-09-05-git-aware-sync-design.md](superpowers/specs/2026-09-05-git-aware-sync-design.md)
for why code moves as commits rather than a live sync.

## Instance identity

An instance is identified by the git repo it belongs to, not by the
directory you happen to be standing in:

1. Commands that need repo content resolve the repo root via
   `git rev-parse --show-toplevel` from `cwd`, walking up through subfolders
   automatically.
2. The instance name is derived from `git remote get-url origin` (the last
   two path segments, lowercased and joined with `-`, so
   `git@github.com:jskswamy/cloudlab.git` becomes `jskswamy-cloudlab`),
   falling back to the folder name for repos with no `origin` remote.
3. `--repo <path>` overrides where identity is resolved from. Lookup-only
   commands (everything that just needs to find an existing instance in
   state) accept a name directly instead and never need a repo at all.
4. `--name` overrides the derived name when you want more than one instance
   against the same repo (e.g. a python and a docker instance side by side).

This means `session start` always seeds the repo root, regardless of which
subfolder you invoked it from, and two clones of the same repo (different
paths, same `origin`) share one instance rather than silently creating two
VMs.

Instance names must start with a letter and contain only letters, digits and
hyphens — the name is also used as the repository directory name in paths on
both machines. `Up` checks this before anything billable happens.

The instance's **remote user** is derived once, at `up` time, from the local
OS user, sanitized to a valid Linux username (see
`internal/identity.RemoteUser`) and stored in state. It is never re-derived
per command: a later command may run as a different local user, or from a
different machine, and must keep talking to whichever user the instance was
actually provisioned with.

## Provider abstraction

An instance's identity, template, and state are provider-independent; only
the mechanics of "create/destroy/get/list a VM" are provider-specific. Those
live behind a `Provider` interface:

```go
type Provider interface {
    Create(ctx context.Context, spec InstanceSpec) (VM, error)
    Destroy(ctx context.Context, id string) error
    Get(ctx context.Context, id string) (VM, error)
    List(ctx context.Context) ([]VM, error)
}
```

`digitalocean` is the first (and, for now, only) implementation, built on
`godo`. Instance state records which provider created a VM, so `list`,
`status`, and `down` route to the right implementation without the rest of
the CLI needing to know or care which provider is in play. Sizing/region
concepts (droplet sizes and regions on DO; instance types and regions/zones
elsewhere) stay behind the same interface as provider-specific spec fields
rather than being modeled generically — there's no meaningful shared
vocabulary across providers worth abstracting until a second provider
actually exists.

See [ADR-0008](adr/0008-provider-abstraction.md) for why the seam exists
anyway.

## Provisioning: cloud-init as a thin trigger

A freshly booted VM doesn't start with Nix installed, so cloud-init's job is
reduced to the minimum needed to hand off to Nix. The payload
(`internal/provisioning/cloud-init.sh`, rendered with the remote username)
does exactly five things:

1. Install Nix (Determinate Systems installer — non-interactive, flakes
   enabled by default).
2. Create the instance's non-root user with `useradd --create-home`, seeded
   with root's own `authorized_keys` (DigitalOcean puts the account's
   registered SSH keys there at boot).
3. Grant that user passwordless sudo — cloudlab is fully automated with no
   interactive terminal on the remote side.
4. `loginctl enable-linger` for it, so home-manager's declared
   `systemd --user` services (the docker template's `dockerd`, the
   `tailscaled` unit) survive the SSH session that started them.
5. Disable root SSH login — last, and only once the new user's key-based
   login is confirmed in place, so a failure anywhere above never locks the
   instance out entirely.

That's the entire payload, and it's identical across templates. Notably it
does **not** run home-manager: cloudlab does that itself over SSH once the
instance is reachable, so it can ship a per-instance flake first and stream
the output back live. See [ADR-0004](adr/0004-nix-home-manager-provisioning.md).

## Reconciliation: the per-instance flake

`internal/reconcile.Reconcile` is the one piece `up` and `provision` share.
For an instance name and a local `cloudlab.pkl` path it:

1. Looks the instance up in state (IP, remote user).
2. Resolves the config (`config.Resolve` — project file merged with the
   personal base config).
3. Expands the template name to a flake ref. `python`/`docker` expand to
   `github:jskswamy/cloudlab?dir=templates#<name>-<system>`, floated on the
   default branch so template fixes don't need a cloudlab release. Anything
   else is assumed to already be a complete flake ref and passed through.
4. If the config adds anything beyond the bare template — `packages`,
   `agents`, `flakes`, or `tailscale = true` — renders a per-instance
   wrapper flake and writes it to `/home/<user>/.cache/cloudlab/flake.nix`
   on the instance, then targets that directory
   (`path:/home/<user>/.cache/cloudlab#default`) instead of the template ref
   directly.
5. Runs `nix run home-manager -- switch --no-write-lock-file --refresh
   --impure --flake <ref>` inside a login shell, streaming output live.

`--refresh` forces Nix to re-fetch flake inputs rather than serve the
floating `github:` ref from its tarball cache (default TTL: one hour), so a
just-pushed template fix takes effect immediately. `--impure` lets
`common.nix` read `$USER`/`$HOME` via `builtins.getEnv` to set
`home.username`/`homeDirectory` — those vary per instance, so they can't be
hardcoded in a shared, checked-in template.

Everything the wrapper flake embeds is validated first: package and module
names against a narrow nixpkgs-attribute charset, flake URLs against `"` and
`${` (Nix string interpolation), so nothing in a `cloudlab.pkl` can break out
of the Nix string literal it lands in.

## Templates

`templates/flake.nix` exposes `homeManagerModules.{python,docker}` and
`homeConfigurations.<name>-<system>` for `x86_64-linux` and `aarch64-linux`.

Both templates import `modules/common.nix`, which every instance gets:

- **Packages:** `git`, `age`, `devbox`, `herdr`, `mosh`, `moshi-hook`,
  `tailscale`, `tmux`.
- **Programs:** `fish`, `starship`.
- **tmux config:** [gpakosz/.tmux](https://github.com/gpakosz/.tmux), pinned
  by revision and hash, symlinked as upstream ships it. `.tmux.conf.local`
  is wrapped in `mkDefault` so a personal flake module can override it.
- **`cloudlab.tailscale` option:** when true (set by the rendered wrapper
  flake from `cloudlab.pkl`), installs a `tailscaled` `systemd --user` unit
  running under sudo. Off by default, because tailscaled starting during
  provisioning would block SSH.
- **moshi-hook service:** installed via a home-manager activation script;
  `moshi-hook` writes and enables its own `systemd --user` unit.

On top of that:

| Template | Adds |
|----------|------|
| `python` | `python312`, `uv` |
| `docker` | `docker`, `minikube`, a `dockerd` `systemd --user` unit (run under sudo), and `docker` group creation + membership via an activation script |

`herdr` is deliberately not given a systemd unit: it self-manages its server
lifecycle, and a second `herdr server` would fail on the socket.

## Package, agent and flake composition

Everything beyond the template comes from `cloudlab.pkl`:

```pkl
agents { "claude" }

packages { "nodejs_22" }

flakes {
  new {
    url = "github:someorg/custom-tool"
    packages { "cli" }
    modules = true
  }
}
```

Reconciliation builds a **list of home-manager modules** and lets
home-manager merge them — it does not append to a single `home.packages`
list. The template's own profile is one module; `packages` becomes a
synthetic module; `agents` becomes another; each `flakes[]` entry
contributes its packages, and its own `homeManagerModules.default` when
`modules = true`.

This is more machinery than a package list strictly requires — see
[ADR-0005](adr/0005-module-based-package-composition.md) for why: it's the
seam that lets a flake's own home-manager module (arbitrary config, not just
a package list) slot into the same pipeline with no restructuring. That seam
is now used: `flakes[].modules` is implemented.

### Why `agents` is its own field

Coding agent harnesses (`claude`, `codex`, `copilot`, `cursor`, `opencode`,
`pi`) are a curated union in the Pkl schema rather than free-form `packages`
entries, for two reasons. The nixpkgs attribute isn't guessable from the
tool's name — `claude` ships as `claude-code`, `pi` as `pi-coding-agent`,
`copilot` as `github-copilot-cli`. And several are unfree, which nixpkgs
refuses to build unless permitted where nixpkgs is *instantiated*; a
home-manager module can't set that, so `packages` alone could never install
them.

The rendered flake instantiates nixpkgs with an `allowUnfreePredicate`
naming **exactly** the unfree agents you selected. Unfree software is not
switched on in general — an unfree package listed in `packages` is still
refused. See [`docs/config.md`](config.md#agents) for the full table.

## Reconciliation triggers

`home-manager switch` is idempotent, and runs at exactly two points:

- **`up`** — after the VM is reachable, before the command returns.
- **`provision`** — on demand, for "I only changed `cloudlab.pkl`, apply it
  now" with none of `up`'s side effects.

There's no file-watcher of any kind, and no live sync of repository content:
code moves as git commits (`session start`, `pull`, `merge`).
`sync`/`download` are plain one-shot rsync transfers for data that
deliberately isn't in git; they never touch `cloudlab.pkl` or trigger a
reconcile.

(`shell` is designed as a third trigger — reconcile, then drop into a local
subshell with instance envs injected, mirroring `devbox shell`'s
"always current on entry" contract. It is not implemented yet; see
[ADR-0007](adr/0007-command-surface.md).)

## Sessions: code moves as git commits

A **session** is the unit of agent work. It is named by the user, lives on
branch `cloudlab/<session>`, and is one ordinary git repository on the
instance — not a worktree of a shared bare store. One session, one clone.

**`session start <name>`:**

1. Resolves the git host, preferring the instance's tailnet address over its
   public IP — git traffic then never crosses the public internet, and the
   remote survives a reboot that reassigns the public IP. Falls back
   silently when Tailscale isn't in play.
2. Records the session name and local repo path in state **before** creating
   anything, so a start that fails halfway still leaves a session `down` can
   see and rescue.
3. `git init` at `~/sessions/<name>/<repo>` on the instance, pushes local
   `HEAD` to `refs/heads/cloudlab/<name>`, *then* checks that branch out.
   The order matters: a fresh `git init` leaves HEAD unborn, so the pushed
   branch isn't the checked-out one and the push is legal without
   `receive.denyCurrentBranch=updateInstead`.
4. Registers a local remote `cloudlab-<name>` and creates the tracking
   worktree at `<repo>/.worktrees/<name>`, adding `/.worktrees/` to
   `.git/info/exclude` (cloudlab's own bookkeeping, not the user's
   `.gitignore`).

The worktree lives inside the repository rather than under `$HOME` because
sandboxing tools scope themselves to the project directory — a worktree the
agent's sandbox can't see is a worktree the agent can't use.

Every step is retry-safe: re-running the same session name fixes a failed
start and never overwrites the agent's work. An instance can run several
sessions at once — the state record holds a list, not a single session — so
starting a new one never disturbs the others; `session delete` or `merge`
retires one without touching the rest.

With more than one session live, `session pull`/`session merge`/`session
delete` need to know which session they act on. Naming it explicitly always
works; leaving it off resolves from the current worktree (running from
inside `.worktrees/<name>` picks `<name>`) or, failing that, from having
exactly one session on the instance. Two or more candidates and no way to
tell them apart is a refusal, not a guess.

**`session pull [session]`** checkpoints uncommitted work on the instance
(`git add -A` plus a guarded commit, so a clean tree makes no empty commit),
fetches the branch, verifies the fetched tip is genuinely in the local
object store, and fast-forwards the worktree with `merge --ff-only` — which
refuses loudly on divergence rather than eating local edits. Safe to run
repeatedly, and while the agent is still working.

**`session merge [session]`** is the accept path, and its ordering is the
safety property:

1. Refuse if the user's working tree, or the session worktree, is dirty.
2. Rescue (checkpoint + fetch + verify) as above.
3. `git cherry-pick --empty=drop -S HEAD..<ref>` onto the current branch.
   Cherry-pick rather than rebase because the session ref lives outside
   `refs/heads`, where `rebase --onto` reports "up to date", re-signs
   nothing, and detaches HEAD. `--empty=drop` makes a re-run after a
   conflict work, since cherry-pick has no patch-id dedup.
4. Verify every replayed commit reports `G` from `%G?`. A failed
   verification **rewinds the branch** — leaving unverified commits behind
   would let the next merge drop them as empty, see an unmoved HEAD, skip
   verification, and silently pass.
5. Re-checkpoint and re-read the instance-side tip. The agent keeps running
   while the replay happens, and signing can block for minutes on a hardware
   key touch; if the tip moved, nothing is deleted and re-running merge
   takes the new work too.
6. Only then: delete the session repo on the instance, remove the local
   worktree and remote, and clear the session from state.

**`session delete [session]`** rescues (checkpoint + fetch) before it
measures, exactly like merge, and for the same reason: the local session
branch only moves when `pull` fast-forwards it, so measuring that branch
alone read a session nobody had pulled as empty and deleted it with exit 0.
Two consequences follow from rescuing first. It needs the instance
reachable, and refuses when it is not — `--force` skips both the rescue and
the refusal. And because rescue checkpoints whatever the agent left
uncommitted, a session with dirty-but-uncommitted instance-side work now
refuses to delete instead of going quietly: the checkpoint turns that dirt
into a commit the unmerged gate counts.

**`down`** rescues every session's work before destroying anything, using the
session names and local repo paths from the state record — `down` resolves an
instance by name and may run from any directory, so neither is derivable
from where the command is typed. A rescue failure aborts the destroy and
says so (the instance is still billing); `--force` skips the rescue.

The instance never pushes and never talks to a shared remote. Pull-direction
means an agent *cannot* push anywhere, enforcing "no unreviewed pushes"
structurally rather than by policy, and it's the only direction that works
for an agent running overnight against a closed laptop.

## Secrets: a personal, sops-encrypted file

`cloudlab secrets init/edit/keys` manage a personal file at
`$XDG_CONFIG_HOME/cloudlab/secrets.yaml` (else
`~/.config/cloudlab/secrets.yaml`), encrypted with
[sops](https://github.com/getsops/sops). It holds the Tailscale auth key
today, whatever else needs one later.

`internal/secrets` shells out to the `sops` binary for every operation —
same as tailscale/nix elsewhere in this codebase — rather than linking sops
as a Go library. The file is YAML, not TOML, because sops has no TOML
support.

Values are decrypted and zeroed just-in-time. `JoinTailscale` decrypts
`tailscale_authkey`, resolves the instance's own `$XDG_RUNTIME_DIR` (a
tmpfs) with a real round-trip, streams the key there over SSH **stdin** —
never a command-line argument — runs `tailscale up` against it, and zeroes
the local copy immediately after the remote write, whether or not
`tailscale up` succeeded.

`down` best-effort logs the instance out of the tailnet before destroying
it, gated on `record.TailscaleJoined` rather than the current config (the
config's value may have changed, or the instance may have been joined
manually by `cloudlab tailscale`).

> **Note on agent credentials.** [ADR-0006](adr/0006-credentials-via-aide-secrets.md)
> describes injecting `SOPS_AGE_KEY` into `shell`/`ssh` sessions so aide can
> decrypt an agent's API key in-process. None of that is implemented: `shell`
> is a stub, no env injection exists, and aide is not among the packages any
> template installs. Agent harnesses are installed on request via the
> `agents` field; authenticating them on the instance is currently manual.
> [ADR-0009](adr/0009-general-secrets-via-sops-nix.md) proposes the
> longer-term design and is likewise not implemented.

## Command surface

| Command | Scope | Purpose |
|---------|-------|---------|
| `up [name]` | per-instance | Create the VM, run cloud-init, wait for SSH, reconcile home-manager, optionally join the tailnet. Confirms before anything billable. Seeds no repository — see `session start` |
| `provision [name]` | per-instance | Reconcile home-manager with the current `cloudlab.pkl`, nothing else |
| `ssh [name]` | per-instance | Interactive **remote** shell on the VM. `--dir` picks the directory to land in; without it, the resolved session's repository on the instance. Where several sessions are live and none is implied, it asks — connecting is not destructive, so with no session at all it still connects and lands in `$HOME` |
| `herdr [name]` | per-instance | Interactive **remote** session via [herdr](https://herdr.dev/) — a background session that survives disconnects. Names its herdr session after the resolved cloudlab session, so reconnecting returns to the same one. It cannot start in the session's directory: `herdr --remote` takes no working directory, only `herdr workspace create --cwd` does. Refuses to nest inside an existing herdr session |
| `tmux [session-name]` | per-instance | Remote tmux session, create-or-attach (`new-session -A`). Named after the resolved cloudlab session so reconnecting returns to the same one; the positional overrides that, and the default is `main` when no session resolves. It does not start in the session's directory — `tmuxArgs` passes no `-c` |
| `pair [name]` | per-instance | Pair the getmoshi.app mobile app with the instance via its QR flow. Prompts for which address the QR advertises (tailnet vs public) when both are available; `--host` sets it directly |
| `tailscale [name]` | per-instance | Join the instance to your tailnet. Also runs as part of `up` when `cloudlab.pkl` sets `tailscale = true` |
| `session start <name>` | per-instance | Start a named agent session: seed a fresh repository on the instance on branch `cloudlab/<name>`, plus the local tracking worktree at `<repo>/.worktrees/<name>`. An instance can run several sessions at once |
| `session pull [session]` | per-instance | Checkpoint and fetch a session's commits without merging them, and fast-forward the local worktree. `[session]` is optional — resolved from the current worktree, or the instance's only session |
| `session merge [session]` | per-instance | Replay a session's commits onto the current branch with your signature, verify every one, then retire the session from both sides. `[session]` optional, same resolution as `session pull` |
| `session delete [session]` | per-instance | Discard a session without merging its work. Rescues the instance first (checkpoint + fetch), so it needs the instance reachable and refuses when it isn't; refuses while it has unmerged commits unless `--force` |
| `session list` | global | Every session on every instance, with its branch, unmerged count and worktree state |
| `sync [remote-dir] --dir <local-dir>` | per-instance | One-shot rsync push of a local directory. `--dir` defaults to the current directory; `remote-dir` defaults to the mirrored remote path. For data that isn't in git |
| `download <remote-dir> [local-dir]` | per-instance | One-shot rsync pull. `local-dir` defaults to `./<basename>` |
| `status [name]` | per-instance | Instance detail from state, a live provider check, and the instance's own sessions |
| `down [name]` | per-instance | Rescue every session's work, deregister from the tailnet, destroy the VM, clear state. Confirms first; `--force` skips the rescue |
| `list` | global | All instances across all repos (name, provider, IP) |
| `secrets init/edit/keys` | global | Manage the personal, sops-encrypted secrets file |
| `shell [name]` | per-instance | **Not implemented.** Reconcile, then open a *local* subshell with instance envs injected (`DOCKER_HOST`, ...) |
| `connect [name]` | per-instance, python | **Not implemented.** Jupyter tunnel |

`[name]` is optional on every per-instance command and defaults to the
current repo's derived instance name; see
[Instance identity](#instance-identity). `--repo` and `--name` are
persistent flags on the root command.

`shell` vs `ssh`: `shell` is designed never to touch the network to open an
interactive session — it configures your **local** shell to talk to the
remote instance (e.g. pointing the local `docker` CLI at the remote daemon
over SSH transport). `ssh` drops you into an actual remote session on the
VM. Only `ssh` exists today.

`up` and `down` both prompt for confirmation, printing what they're about to
create or destroy. Anything other than `y`/`yes` — including a bare Enter,
or no input at all — declines.

## Remote paths

`RemotePath` mirrors a local path under the remote user's home:

- Under your local `$HOME` → mirrored relative to it
  (`/Users/you/source/repo` → `/home/you/source/repo`).
- Anywhere else → nested under the remote home
  (`/opt/work/repo` → `/home/you/opt/work/repo`).

Never an as-is passthrough, which would often point at a path the
unprivileged remote user can't create — failing only after the instance is
already provisioned. cloud-init creates the user with a plain
`useradd --create-home`, so its home is always exactly `/home/<user>`.

This is the path shape `up` records as `RepoPath`, and the same shape `sync`
derives for its remote side. Session repositories use their own:
`/home/<user>/sessions/<session>/<repo>`.

`RepoPath` is no longer where `ssh` lands. rsync used to create that mirrored
directory during `up`; since the git transport replaced it, nothing does, so
defaulting to it silently dropped the user in `$HOME`. `ssh` resolves a session
and lands in its repository instead.

The stored field now has exactly one reader: `status`, which displays it.
`sync` does not read it — `syncRemoteDir` recomputes the path from the caller's
working directory each time. So the field is reported, not resolved against.

## State

State is a JSON file at `$XDG_STATE_HOME/cloudlab/state.json` (else
`~/.local/state/cloudlab/state.json`), holding one entry per instance keyed
by name — not a single global record, which is necessary once more than one
instance can be alive at a time. `list` reads across all entries.

Each record holds: name, provider, VM id, IP, region, size, template, remote
user, remote repo path, tunnel PID, whether it joined the tailnet, and
`Sessions []Session` — one entry per live session, each holding the
session's name, the local repository it belongs to, and the commit it
branched from (`Base`). A list rather than a single session, because an
instance can run several at once; `PutSession`/`RemoveSession` add, replace
and drop entries by name without disturbing the rest.

The record is written **immediately** after `Create` returns and before
anything else can fail, so a later failure still leaves an instance `down`
can destroy. If writing it fails, `Up` destroys the VM it just made rather
than leaking it.

`Sessions` exists for `down`, which resolves an instance by name and may run
from any directory, and for `session pull`/`merge`/`delete`, which resolve
which session they act on from it when no name is given (see
[Sessions](#sessions-code-moves-as-git-commits)). `session start` appends to
it *before* creating anything; `merge` and `session delete` remove the entry
once the session is gone from both machines — leaving it would make the next
`down` try to rescue a worktree that no longer exists, refuse to destroy,
and recommend `--force`, training the user into the one flag that loses
work.

## Not built (deliberately)

- **`shell` and `connect`** are stubs. They resolve an instance and exit with
  "not implemented yet".
- **No second provider implementation.** The interface exists so adding one
  later doesn't require touching instance identity, templates, or state.
- **No custom-template catalog.** Two built-in templates, plus the escape
  hatch of passing a full flake ref as `template`.
- **No file-watcher of any kind.** Environment changes go through
  `up`/`provision`; code changes go through git.
- **No "force a full repo re-sync" command.** If a session's worktree on the
  instance gets into a state worse than a fresh `pull` fixes, deleting and
  restarting the session (or `down` + `up`) recreates it from scratch —
  cheap enough for ephemeral compute that a repair command isn't worth
  building.
