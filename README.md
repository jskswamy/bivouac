# bivouac

[![CI](https://github.com/jskswamy/bivouac/actions/workflows/ci.yml/badge.svg)](https://github.com/jskswamy/bivouac/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/jskswamy/bivouac)](https://github.com/jskswamy/bivouac/releases/latest)

Run coding agents on disposable cloud machines without giving them your laptop,
your GitHub credentials, or an unreviewed path back to `main`.

A bivouac is a shelter improvised for one stay: useful while you need it,
struck afterward. This is that for agent work. `bivouac` boots a temporary
DigitalOcean VM, provisions it from a declarative Nix/home-manager config, lets
agents work there, then brings their work home as ordinary git commits that you
review, sign, and merge locally.

## Why bivouac?

Coding agents are most useful when they can run builds, tests, browsers,
services, package managers, and long-lived terminals for hours at a time. That
is also exactly why running them directly on your laptop can be uncomfortable:

- long-running agent sessions stop when your laptop sleeps, loses network, or
  needs to move with you;
- local agents burn battery, memory, disk, CPU, and ports while you are trying
  to use the same machine;
- dangerous commands run beside your SSH keys, cloud credentials, browser
  profile, and personal files;
- cloud devboxes drift unless their setup is declared somewhere;
- an agent's work is hard to trust if it lands as a pile of untracked files;
- giving a VM GitHub credentials makes cleanup and blast radius harder.

Bivouac's answer is a disposable, reproducible Linux workspace with a git
handoff boundary:

- **Disposable cloud compute:** start a VM for this repo, use it, destroy it.
- **Declarative setup:** provision with Nix/home-manager from `bivouac.pkl` plus
  optional personal defaults.
- **Agent sessions as branches:** each session runs in its own checkout on the
  instance and its own local worktree.
- **Local merge boundary:** the VM never pushes to GitHub; you pull, replay,
  sign, verify, and merge commits on your machine.
- **Explicit secrets:** provider credentials stay local; optional instance
  secrets are decrypted just in time and streamed over SSH.
- **LLM-readable surfaces:** selected commands have explicit JSON output, and
  `bivouac manifest --format mcp-tools` exposes the CLI as tool metadata.

## When to use it

Use bivouac when you want to:

- run Claude, Codex, Copilot, Cursor, OpenCode, Pi, or other tools away from
  your laptop;
- give an agent a beefier Linux machine with Docker, Python, devbox, Tailscale,
  or custom Nix modules;
- isolate risky agent work while still getting a clean git review path;
- run several named agent sessions against one repo and merge them one at a
  time;
- preserve only the work you deliberately bring home.

It is not trying to be:

- a permanent dev environment manager;
- a Kubernetes or multi-cloud orchestrator;
- a live collaborative editor;
- a general file-sync tool for source code;
- provider-agnostic yet — DigitalOcean is the first implemented backend.

## Install

```bash
# Install latest release to ./bin
curl -sSfL https://raw.githubusercontent.com/jskswamy/bivouac/main/install.sh | sh

# Install latest release system-wide
curl -sSfL https://raw.githubusercontent.com/jskswamy/bivouac/main/install.sh | sudo INSTALL_DIR=/usr/local/bin sh

# Install a specific release
curl -sSfL https://raw.githubusercontent.com/jskswamy/bivouac/main/install.sh | VERSION=v0.1.0 sh

# Or build from source with Go
go install github.com/jskswamy/bivouac@latest
```

You also need a DigitalOcean API token, SSH access to your account's droplets,
and the external tools used by the workflows you choose (`sops`, `age`,
`tailscale`, `herdr`, `bd`, etc.). The repository's `nix develop` shell has the
development/test toolchain.

Select **public-key file paths**, for example `sshKeys { "~/.ssh/id_ed25519.pub" }`,
not DigitalOcean fingerprints or numeric IDs. `bivouac init` can migrate old
entries. At least one selected key needs a matching local private file or an
unlocked agent key; `up` lists any public-key registrations before asking for
final approval. A persistent, machine-scoped SSH alias carries the selection
into later SSH, rsync, Git and Herdr connections without changing unrelated
hosts. See [SSH keys and migration](docs/config.md#ssh-keys) for path rules,
credential permissions, conflict repair and verification limits.

## Quickstart

From any git repository:

```bash
# 1. Create or edit personal secrets and defaults.
bivouac secrets init

# 2. Create this repo's instance config.
bivouac init

# 3. Boot and provision the VM.
bivouac up

# 4. Start an isolated agent session on the instance.
bivouac session start agent

# 5. Attach however you work.
bivouac herdr agent       # saved Herdr machine + session workspace
# or: bivouac ssh agent
# or: bivouac tmux agent

# 6. Bring work home without merging yet.
bivouac session pull agent

# 7. Review, replay, sign, verify, and retire the session.
bivouac session merge agent

# 8. Tear the VM down when you are done.
bivouac down
```

Day to day, the common commands are:

```bash
bivouac status                # this instance, sessions, served ports, cost
bivouac list --cost           # all instances and accumulated cost
bivouac provision             # re-apply bivouac.pkl after editing it
bivouac connect --port 8888   # reach a service on the instance
bivouac serve 8888            # publish a service on your tailnet
bivouac unserve 8888          # stop publishing it
bivouac sync --dir ./dataset  # one-shot rsync push for non-git data
bivouac download ~/results    # one-shot rsync pull for outputs
```

`session pull`/`merge`/`delete` all take the session name as an optional
argument: give it explicitly, or leave it off and it resolves from the worktree
you are standing in, or from being the instance's only session.

## The safety model

`bivouac up` does not copy your repository to the VM. It only creates or updates
the instance. Code first reaches the instance when you run
`bivouac session start <name>`.

A session start:

1. pushes your current commit to a fresh repository at
   `~/sessions/<name>/<owner>/<repo>` on the instance;
2. checks it out on branch `bivouac/<name>`;
3. creates a local worktree at `<repo>/.worktrees/<name>` tracking the session;
4. records enough state to pull, merge, and clean it up later.

A session merge:

1. checkpoints anything the agent left uncommitted;
2. fetches the session branch;
3. cherry-picks the session's commits onto your current branch **with your
   signature**;
4. verifies every replayed commit has a good signature (`%G? = G`);
5. closes only Herdr workspaces or sessions whose recorded server and opaque
   ownership proof still match;
6. rechecks for work written while those resources were closing; and
7. removes remote/local checkout residue through recorded, retryable effects.

Merge and delete share this retirement workflow. If any cleanup step is
unreachable or uncertain, the command exits nonzero, keeps the session's
recovery record, and prints the same command to resume. Re-running a completed
command succeeds from its completion receipt. `session delete --force` records
explicit authority to discard work, but it does not turn an unreachable remote
resource into proof that the resource is gone. Legacy sessions with unknown
Herdr ownership must be interactively adopted before retirement.

The VM never pushes to GitHub and does not need your signing key, GitHub token,
or host credentials. If a merge conflicts, resolve it and run
`git cherry-pick --continue`; `bivouac session merge` resumes where it stopped.

`bivouac down` rescues session work before destroying the VM. It refuses to
discard unmerged commits or unlanded issue state unless you explicitly force the
destructive operation.

The one credential carve-out is opt-in: `beads = "dolthub"` in `bivouac.pkl`
places a DoltHub credential on the instance, in tmpfs, so the agent's issue
database can sync against DoltHub directly. The default, `beads = "session"`,
needs no DoltHub credential; issues ride the session's own git remote over SSH.

## Configuring an instance

Run `bivouac init` and answer the questions. It writes `bivouac.pkl` for the
repo. `bivouac preset list/show/edit/delete` manage saved shapes that the
wizard can offer.

A minimal hand-written config looks like this:

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

Settings you repeat in every repo — usual region, droplet size, SSH keys,
packages, agents, Herdr tabs, flake modules, home-manager settings — can live
once in `~/.config/bivouac/base.pkl`. The project config layers on top.

Coding agent harnesses are opt-in through `agents` rather than plain
`packages`, because the nixpkgs attribute is not guessable from the tool name
(`claude` → `claude-code`, `pi` → `pi-coding-agent`) and several are unfree.
Selecting one permits exactly that package, not unfree software in general.

See [`docs/config.md`](docs/config.md) for every field, merge rules, examples,
and error messages.

## What gets installed

Templates are Nix/home-manager modules. The built-in templates are:

| Template | What it adds |
|----------|--------------|
| `python` | `python312`, `uv` |
| `docker` | `docker` + `minikube`, with a `dockerd` systemd --user unit and `docker` group membership wired up |

Both build on a shared base module with `git`, `age`, `devbox`, `herdr`, `mosh`,
`moshi-hook`, `tailscale`, `tmux` (installed, not configured), `fish`, and
`starship`. A `template` value that is not `python` or `docker` is treated as a
complete flake reference, so you can bring your own.

## Secrets and provider credentials

`bivouac secrets init/edit/keys` manage a personal,
[sops](https://github.com/getsops/sops)-encrypted file at
`~/.config/bivouac/secrets.yaml`. It can hold:

- `digitalocean_token` for local provider API calls;
- `tailscale_authkey` for joining the instance to your tailnet;
- optional `github_token` for `gh` on the instance;
- optional DoltHub credentials for `beads = "dolthub"`.

`digitalocean_token` authenticates bivouac's local API calls and never reaches
an instance. `DIGITALOCEAN_TOKEN` in the environment takes precedence, which is
useful for unattended runs.

Tailscale, GitHub, and DoltHub secrets are decrypted just in time, streamed to
the instance over SSH stdin, and placed only where the selected feature needs
them.

## Machine-readable output for agents

Human output is the default. Bivouac does not silently switch formats because it
thinks an LLM is calling it.

Use explicit structured surfaces instead:

```bash
bivouac manifest --format mcp-tools
bivouac list --json
bivouac session list --json
bivouac status --json
```

The manifest is MCP-shaped tool metadata for discovering commands, flags, side
effects, and JSON-output availability. It is not a full MCP server yet; it is a
stable surface agents can read without scraping human help.

## Documentation by task

- **Configure an instance:** [`docs/config.md`](docs/config.md)
- **Understand the architecture:** [`docs/architecture.md`](docs/architecture.md)
- **See examples:** [`docs/examples/`](docs/examples/)
- **Understand design decisions:** [`docs/adr/`](docs/adr/)
- **Read design specs and plans:** [`docs/superpowers/`](docs/superpowers/)
- **Contribute:** [`CONTRIBUTING.md`](CONTRIBUTING.md)

## License

MIT
