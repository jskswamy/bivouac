# bivouac

[![CI](https://github.com/jskswamy/bivouac/actions/workflows/ci.yml/badge.svg)](https://github.com/jskswamy/bivouac/actions/workflows/ci.yml)

A bivouac is a shelter improvised for one stay: no built infrastructure,
struck completely afterward. This is that, for coding agents — a
disposable remote workspace, declared from scratch by Nix and
home-manager rather than hand-configured, where nothing survives `down`
except what you deliberately carried out as a signed git commit.

The backend is a cloud VM, not a process on your Mac, so Docker Desktop
or minikube isn't what's burning your battery. DigitalOcean is the first
(and for now, only) supported provider; the provider boundary is designed
to add others without touching the rest of the tool — see
[ADR-0008](docs/adr/0008-provider-abstraction.md).

> **Status:** `up`, `provision`, `down`, `list`, `status`, `ssh`, `tmux`,
> `herdr`, `pair`, `tailscale`, `secrets`, `connect`, `serve`/`unserve`,
> `sync`/`download` and the git session flow (`session start` / `pull` /
> `merge`) are implemented.
> `shell` is still a stub — it parses, resolves an instance, and exits
> with "not implemented yet". See [`docs/architecture.md`](docs/architecture.md)
> and [`docs/adr/`](docs/adr/) for the full design.

## What it does

```bash
cd myproject                  # any git repo
bivouac init                  # answer a few questions; writes bivouac.pkl
bivouac up                    # boots a VM (per bivouac.pkl) and reconciles
                              # its Nix/home-manager environment
bivouac session start agent   # seeds the repo onto the instance via git and
                              # checks it out on branch bivouac/agent
bivouac ssh                   # interactive shell on the instance
bivouac connect --port 8888   # reach a service on the instance
bivouac serve 8888            # publish it on your tailnet; outlives the command
bivouac unserve 8888          # stop publishing it
bivouac session list          # every session on every instance, at a glance
bivouac session pull          # fetch the session's commits without merging
bivouac session merge         # replay, sign, verify, then retire the session
bivouac provision             # re-apply bivouac.pkl after editing it
bivouac sync --dir ./dataset  # one-shot rsync push of anything outside git
bivouac download ~/results    # one-shot rsync pull back
bivouac status                # what this instance is, and what it has cost so far
bivouac list --cost           # every instance, with what each is running up
bivouac down                  # rescues every session's work, then destroys the VM
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
  branch `bivouac/<name>`, and creates a matching local worktree at
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
  the instance before its commits are provably in your local object store. A
  conflict stops the replay; resolve it, `git cherry-pick --continue`, and
  run merge again — it resumes where it stopped instead of replaying what
  has already landed.
- `session list` shows every session on every instance — name, branch,
  unmerged commit count, worktree state — for when you've lost track of
  what's running where. `status` shows the same detail scoped to one
  instance.

`status` also reports what the instance has cost so far: its uptime since the
provider created it, multiplied by the size's hourly price and capped at the
monthly one, which is how DigitalOcean actually bills. `list --cost` does the
same across every instance and totals it. Cost is the one thing in either
report that needs an API token — `status` degrades to "unknown" without one,
since its live check was always incidental, while `list --cost` fails, because
the token is exactly what you asked it to use. Plain `list` stays offline.

The instance never pushes anywhere and never talks to a shared remote — no
GitHub credentials, host keys or signing keys ever reach the VM.

The one carve-out is opt-in: `beads = "dolthub"` in `bivouac.pkl` places your
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
`tmux` (installed, not configured -- bring your own config through a flake
module), plus `fish` and `starship`. Template-specific modules only declare what's
actually different.

A `template` value that isn't `python` or `docker` is passed through as a
complete flake reference, so you can point at your own.

## Configuring an instance

Run `bivouac init` and answer the questions — it writes the files for
you, and `up` runs the same thing by itself in a repo that has no config
yet. `bivouac preset list/show/edit/delete` manage the shapes it offers
to save.

Or drop a `bivouac.pkl` in your repo root by hand:

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
`~/.config/bivouac/base.pkl` and merge with each project's file.

[`docs/config.md`](docs/config.md) documents every field, the merge rules,
and the errors you might see. Worked examples live in
[`docs/examples/`](docs/examples/).

## Secrets

`bivouac secrets init/edit/keys` manage a personal,
[sops](https://github.com/getsops/sops)-encrypted file at
`~/.config/bivouac/secrets.yaml`. It holds `tailscale_authkey`,
`digitalocean_token`, an optional `github_token` for `gh` on the instance,
and — for `beads = "dolthub"` — a DoltHub credential (`dolthub_creds` and
`dolthub_creds_id`).

`up`/`bivouac tailscale` and `beads = "dolthub"` decrypt the Tailscale and
DoltHub secrets just-in-time, stream them to the instance's tmpfs over SSH
stdin, and zero immediately after — never a command-line argument, never
plaintext on disk on either machine.

`digitalocean_token` is the exception: it authenticates bivouac's own API
calls from your machine and never reaches an instance. `DIGITALOCEAN_TOKEN`
in the environment takes precedence over it, so unattended agents never need
a hardware key — see [the token section in
`docs/config.md`](docs/config.md#the-digitalocean-token) for why the order
runs that way.

## Documentation

- [`docs/architecture.md`](docs/architecture.md) — components, data flow, instance lifecycle
- [`docs/config.md`](docs/config.md) — every `bivouac.pkl` field, base-config merging, errors
- [`docs/examples/`](docs/examples/) — a minimal config, and the base-merge pattern
- [`docs/adr/`](docs/adr/) — why things are shaped the way they are, one decision per file
- [`docs/superpowers/`](docs/superpowers/) — dated design specs and delivery plans
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — dev environment setup, pre-commit hooks

## License

MIT
