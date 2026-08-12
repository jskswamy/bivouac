# Architecture

## Overview

cloudlab is a Go CLI that manages **instances**: named, ephemeral cloud VMs
provisioned from a **template** (`python` or `docker`) and, on top of that,
whatever a repo's own `cloudlab.yaml` adds. Multiple named instances can
exist at once, each started, shelled/ssh'd into, and torn down
independently. The compute backend is a cloud VM reached over the network,
not a process on your machine, and provisioning is done with Nix +
home-manager instead of ad hoc package installs.

DigitalOcean is the only provider implemented so far, but the provider
boundary is deliberately not DO-specific — see
[Provider abstraction](#provider-abstraction) and
[ADR-0008](adr/0008-provider-abstraction.md).

```
 local repo                          cloud VM ("instance")
┌──────────────┐         up        ┌───────────────────────────┐
│ cloudlab.yaml│ ──boot+bootstrap──▶│ Nix installed               │
│ (optional)   │   (cloud-init)     │ home-manager switch runs    │
└──────────────┘                    │  (template profile only)    │
                                     │ repo rsynced to ~/reponame  │
                                     │ home-manager switch re-runs │
                                     │  (template + cloudlab.yaml) │
                                     │ watch starts (mutagen)      │
                                     └───────────────────────────┘
```

`up` is the only step that seeds the repo — everything from "repo rsynced"
onward happens automatically as part of bringing the instance up, not as a
separate command. See [Reconciliation triggers](#reconciliation-triggers).

## Instance identity

An instance is identified by the git repo it belongs to, not by the
directory you happen to be standing in:

1. Every command resolves the repo root via `git rev-parse --show-toplevel`
   from `cwd`, walking up through subfolders automatically.
2. The instance name is derived from `git remote get-url origin`
   (slugified `owner/repo`), falling back to the folder name only for
   repos with no remote configured.
3. Outside a git repo entirely, commands require an explicit path
   (`--repo <path>`) instead of inferring one.
4. `--name` overrides the derived name when you want more than one
   instance against the same repo (e.g. a python and a docker instance
   side by side).

This means `up` always seeds the repo root, regardless of which subfolder
you invoked it from, and two clones of the same repo (different paths, same
`origin`) share one instance rather than silently creating two VMs.

## Provider abstraction

An instance's identity, template, and state are provider-independent; only
the mechanics of "create/destroy/list a VM, get its IP, run cloud-init on
boot" are provider-specific. Those live behind a `Provider` interface:

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
concepts (droplet sizes and regions on DO; instance types and
regions/zones elsewhere) stay behind the same interface as
provider-specific spec fields rather than being modeled generically —
there's no meaningful shared vocabulary across providers worth abstracting
until a second provider actually exists.

Only one provider is implemented today. See
[ADR-0008](adr/0008-provider-abstraction.md) for why the seam exists anyway.

## Provisioning: cloud-init as a thin trigger

A freshly booted VM doesn't start with Nix installed, so cloud-init's job
is reduced to the minimum needed to hand off to Nix:

1. Install Nix (Determinate Systems installer — non-interactive, flakes
   enabled by default).
2. Run `nix run home-manager -- switch --flake <flake-ref>#<template>`.

That's the entire cloud-init payload, and it's identical across templates —
the actual differentiation (Python toolchain vs. Docker+minikube, plus the
packages every template shares: git, age, aide, Claude Code) lives in Nix
flake outputs, not in per-template bash/YAML. See
[ADR-0004](adr/0004-nix-home-manager-provisioning.md).

`up` runs `home-manager switch` twice: once immediately after cloud-init
completes (template profile only — the repo isn't present yet), and again
once the repo has been rsynced to `~/<reponame>`, this time with the repo's
`cloudlab.yaml` merged in — see
[Package and flake composition](#package-and-flake-composition) below. Only
after that second reconcile does `up` start `watch` (continuous two-way
Mutagen sync) and return control.

## Package and flake composition

`cloudlab.yaml` (repo root, optional):

```yaml
packages:
  - nodejs_22

flakes:
  - url: github:someorg/custom-tool
    packages: [cli]
```

Internally, reconciliation builds a **list of home-manager modules** and lets
home-manager merge them — it does not append directly to a single
`home.packages` list. A plain package name becomes a tiny synthetic module
(`{ home.packages = [ pkgs.nodejs_22 ]; }`); each `flakes[]` entry becomes
another synthetic module the same way. The template's own profile is just
another module in that same list.

This is deliberately more machinery than "packages only" strictly requires
today — see [ADR-0005](adr/0005-module-based-package-composition.md) for why:
it's the seam that lets a later `flakes[].modules: [...]` (importing a
flake's own `homeManagerModules.default` directly — arbitrary home-manager
config, not just a package list) slot into the exact same pipeline with no
restructuring.

## Reconciliation triggers

`home-manager switch` re-runs (idempotent — a no-op if `cloudlab.yaml`
hasn't changed since the last run) at two points:

- **`up`** — once the repo is rsynced in, as described above.
- **`shell`** — before dropping you into the local subshell, so entering the
  environment always guarantees it's current (mirrors `devbox shell`'s
  contract). See [ADR-0007](adr/0007-command-surface.md).

There's no file-watcher wired to `cloudlab.yaml` specifically — Mutagen
`watch` (running continuously from the end of `up` onward) handles two-way
*repo file content* sync only; package/environment changes go through
`shell`, or a fresh `up` on the same instance name if you've torn one down.
`sync`/`download` (one-shot transfers for paths outside the repo — see
[Command surface](#command-surface)) never touch `cloudlab.yaml` or trigger
a reconcile.

## Credentials: aide, not credential sync

Claude Code and aide are installed on every instance by default, but no
Claude Code credential file is synced to the (ephemeral, destroy-and-recreate)
VM. Instead, `shell`/`ssh` inject `SOPS_AGE_KEY` into the remote session
the same way aide already documents for CI/Docker use — aide decrypts the
relevant secret in-process and exposes it only to the agent process's
environment, never touching disk on the VM. See
[ADR-0006](adr/0006-credentials-via-aide-secrets.md).

## Command surface

| Command | Scope | Purpose |
|---------|-------|---------|
| `up [name]` | per-instance | Create the VM, run cloud-init/home-manager, rsync the repo in, reconcile, start `watch` — one command, instance is fully live on return |
| `shell [name]` | per-instance | Reconcile home-manager, then open a **local** subshell with instance envs injected (`DOCKER_HOST`, `SOPS_AGE_KEY`, ...) |
| `ssh [name]` | per-instance | Interactive **remote** shell on the VM itself |
| `sync <local-dir> [remote-dir]` | per-instance | One-shot rsync of an arbitrary local directory *outside the repo* to the instance (e.g. a dataset). `remote-dir` defaults to the local dir's basename under the instance home. Not involved in repo sync or reconciliation. |
| `download <remote-dir> [local-dir]` | per-instance | One-shot rsync pulling files back from the instance. `local-dir` defaults to the current directory. |
| `watch [name]` | per-instance | Restart continuous two-way repo sync if it's stopped/dead. Auto-started by `up`; rarely invoked directly. |
| `connect [name]` | per-instance, python template | Jupyter tunnel |
| `status [name]` | per-instance | Instance detail: IP, uptime, cost, sync/watch state |
| `down [name]` | per-instance | Stop watch, destroy VM, clear state |
| `list` | global | All instances across all repos |

`[name]` is optional everywhere and defaults to the current repo's derived
instance name; see [Instance identity](#instance-identity). `sync` and
`download` take an explicit path first since they no longer operate on the
repo implicitly.

`shell` vs `ssh`: `shell` never touches the network to open an interactive
session — it configures your **local** shell to talk to the remote instance
(e.g. pointing the local `docker` CLI at the remote daemon over SSH
transport). `ssh` drops you into an actual remote session on the VM.

## State

Each instance gets its own state entry (instance name → provider, VM id, IP,
region, size, template, watch/tunnel PIDs) rather than a single global state
file — necessary once more than one instance can be alive at a time. `list`
reads across all entries.

## Not built (deliberately)

- No second provider implementation. The interface exists (see
  [Provider abstraction](#provider-abstraction)) so adding one later doesn't
  require touching instance identity, templates, or state — but only
  DigitalOcean is implemented.
- No custom-template loading (arbitrary local/remote template YAML/URLs).
  Two templates, embedded in the binary, is what's been asked for; this is
  easy to add later without disturbing the module composition model above.
- No auto-reconciliation from `watch`'s file-change events — package changes
  go through `up`/`shell`, not a bespoke second watcher.
- No "force a full repo re-sync" command beyond `watch` (restart the Mutagen
  session) — if an instance's repo state ever gets into a state worse than
  that fixes, `down` + `up` recreates it from scratch, which is cheap enough
  for an ephemeral instance that a dedicated repair command isn't worth
  building yet.
