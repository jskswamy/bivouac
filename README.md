# cloudlab

[![CI](https://github.com/jskswamy/cloudlab/actions/workflows/ci.yml/badge.svg)](https://github.com/jskswamy/cloudlab/actions/workflows/ci.yml)

Ephemeral, declarative dev instances in the cloud — named instances,
provisioned from a template, worked in by coding agents, torn down when you
don't need them. The backend is a cloud VM, not a process running on your
Mac, so Docker Desktop or minikube isn't what's burning your battery.
DigitalOcean is the first (and for now, only) supported provider; the
provider boundary is designed to add others without touching the rest of the
tool — see [ADR-0008](docs/adr/0008-provider-abstraction.md).

> **Status:** `up`, `provision`, `down`, `list`, `status`, `ssh`, `tmux`,
> `herdr`, `pair`, `tailscale`, `secrets`, `connect`, `serve`/`unserve`,
> `sync`/`download` and the git session flow (`session start` / `pull` /
> `merge`) are implemented.
> `shell` is still a stub — it parses, resolves an instance, and exits
> with "not implemented yet". See [`docs/architecture.md`](docs/architecture.md)
> and [`docs/adr/`](docs/adr/) for the full design.

## What it does

```bash
cd myproject                   # any git repo
cloudlab up                    # boots a VM (per cloudlab.pkl) and reconciles
                               # its Nix/home-manager environment
cloudlab session start agent   # seeds the repo onto the instance via git and
                               # checks it out on branch cloudlab/agent
cloudlab ssh                   # interactive shell on the instance
cloudlab connect --port 8888   # reach a service on the instance
cloudlab serve 8888            # publish it on your tailnet; outlives the command
cloudlab unserve 8888          # stop publishing it
cloudlab session list          # every session on every instance, at a glance
cloudlab session pull          # fetch the session's commits without merging
cloudlab session merge         # replay, sign, verify, then retire the session
cloudlab provision             # re-apply cloudlab.pkl after editing it
cloudlab sync --dir ./dataset  # one-shot rsync push of anything outside git
cloudlab download ~/results    # one-shot rsync pull back
cloudlab down                  # rescues every session's work, then destroys the VM
```

`session pull`/`merge`/`delete` all take the session name as an optional
argument: give it explicitly, or leave it off and it resolves from the
worktree you're standing in, or from being the instance's only session.

Every instance is named after the git repo it belongs to (derived from the
`origin` remote, so two different clones of the same repo share an instance)
and provisioned via Nix + home-manager rather than hand-rolled cloud-init
package lists — so the same declarative config that keeps your Mac's
environment reproducible also keeps your ephemeral cloud boxes reproducible.

## Code moves as git commits

`up` never touches your repository. Code first reaches the instance when you
start a session:

- `session start <name>` pushes your current commit into a fresh git
  repository at `~/sessions/<name>/<repo>` on the instance, checks it out on
  branch `cloudlab/<name>`, and creates a matching local worktree at
  `<repo>/.worktrees/<name>` tracking it. An instance can run several named
  sessions at once.
- `session pull [name]` checkpoints whatever the agent left uncommitted,
  fetches the branch, and fast-forwards your worktree. Safe to run while the
  agent is still working. The name is optional with one session on the
  instance, or when run from inside its worktree.
- `session merge [name]` cherry-picks the session's commits onto your current
  branch **with your signature**, verifies every one of them verifies
  (`%G?` = `G`), and only then deletes the session from both machines.
  Deletion is strictly downstream of verification, so nothing is removed from
  the instance before its commits are provably in your local object store.
- `session list` shows every session on every instance — name, branch,
  unmerged commit count, worktree state — for when you've lost track of
  what's running where. `status` shows the same detail scoped to one
  instance.

The instance never pushes anywhere and never talks to a shared remote — no
GitHub credentials, host keys or signing keys ever reach the VM.

The one carve-out is opt-in: `beads = "dolthub"` in `cloudlab.pkl` places your
DoltHub credential on the instance, in tmpfs, so the agent's issue database can
sync against DoltHub directly. That credential is account-wide — it grants write
access to every Dolt repository on your account for the life of the instance.
The default, `beads = "session"`, needs no credential at all: issues ride the
session's own git remote over the SSH channel your code already uses. `sync`/
`download` remain one-shot rsync transfers for things that deliberately
aren't in git (datasets, weights, results); they never touch repo content or
trigger a reconcile.

See
[`docs/superpowers/specs/2026-09-05-git-aware-sync-design.md`](docs/superpowers/specs/2026-09-05-git-aware-sync-design.md)
for why code moves as commits rather than a live file sync.

## Templates

| Template | What it adds |
|----------|--------------|
| `python` | `python312`, `uv` |
| `docker` | `docker` + `minikube`, with a `dockerd` systemd --user unit and `docker` group membership wired up |

Both build on a shared `common` home-manager module, which every instance
gets: `git`, `age`, `devbox`, `herdr`, `mosh`, `moshi-hook`, `tailscale`,
`tmux` (preconfigured via [gpakosz/.tmux](https://github.com/gpakosz/.tmux)),
plus `fish` and `starship`. Template-specific modules only declare what's
actually different.

A `template` value that isn't `python` or `docker` is passed through as a
complete flake reference, so you can point at your own.

## Configuring an instance

Drop a `cloudlab.pkl` in your repo root:

```pkl
region = "nyc3"
size = "s-2vcpu-4gb"
template = "docker"
tailscale = true

agents {
  "claude"
  "codex"
}

packages {
  "nodejs_22"
  "postgresql_16"
}

flakes {
  new {
    url = "github:someorg/custom-tool"
    packages { "cli" }
  }
}
```

Coding agent harnesses are opt-in through `agents` rather than plain
`packages`, because the nixpkgs attribute isn't guessable from the tool's
name (`claude` → `claude-code`, `pi` → `pi-coding-agent`) and several are
unfree, which has to be permitted where nixpkgs is instantiated. Selecting
one permits exactly that package, nothing else.

Settings you'd otherwise repeat in every repo (SSH key, usual droplet size,
packages you always want) go once in a personal base config at
`~/.config/cloudlab/base.pkl` and merge with each project's file.

[`docs/config.md`](docs/config.md) documents every field, the merge rules,
and the errors you might see. Worked examples live in
[`docs/examples/`](docs/examples/).

## Secrets

`cloudlab secrets init/edit/keys` manage a personal,
[sops](https://github.com/getsops/sops)-encrypted file at
`~/.config/cloudlab/secrets.yaml`. Today it holds `tailscale_authkey`, which
`up`/`cloudlab tailscale` decrypt just-in-time, stream to the instance's
tmpfs over SSH stdin, and zero immediately after — never a command-line
argument, never plaintext on disk on either machine.

## Documentation

- [`docs/architecture.md`](docs/architecture.md) — components, data flow, instance lifecycle
- [`docs/config.md`](docs/config.md) — every `cloudlab.pkl` field, base-config merging, errors
- [`docs/examples/`](docs/examples/) — a minimal config, and the base-merge pattern
- [`docs/adr/`](docs/adr/) — why things are shaped the way they are, one decision per file
- [`docs/superpowers/`](docs/superpowers/) — dated design specs and delivery plans
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — dev environment setup, pre-commit hooks

## License

MIT
