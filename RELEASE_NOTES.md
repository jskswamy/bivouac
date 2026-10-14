## Unreleased

First public release of bivouac: disposable, Nix-provisioned cloud
workspaces for coding agents, with a git-based session workflow that makes the
agent's work reviewable and recoverable on your machine.

### ✨ New

#### Disposable DigitalOcean workspaces

- Create, provision, inspect, and destroy named cloud instances with
  `bivouac up`, `bivouac provision`, `bivouac status`, `bivouac list`, and
  `bivouac down`.
- Instances are derived from the current git repository and configured from
  `bivouac.pkl`, with optional personal defaults from
  `~/.config/bivouac/base.pkl`.
- Provisioning is declarative through Nix and home-manager, including curated
  presets, extra packages, flake modules, settings overrides, and configurable
  login shells.
- Built-in status output reports live instance details, session state, served
  ports, and accumulated DigitalOcean cost.

#### Agent sessions as git branches

- Start isolated coding-agent sessions with `bivouac session start <name>`.
  Each session gets its own checkout on the instance and a matching local
  worktree on branch `bivouac/<name>`.
- Bring work home with `bivouac session pull` and land it with
  `bivouac session merge`, which replays commits locally, signs them with the
  user's identity, verifies signatures, and only then retires the session.
- Safety checks prevent destroying or deleting sessions whose commits or issue
  state have not been rescued.
- Beads issue data can travel with sessions, including DoltHub-backed mode for
  projects that opt into shared issue storage.

#### Remote development commands

- Connect to instances with `bivouac ssh`, `bivouac tmux`, `bivouac herdr`, and
  `bivouac pair`, including optional SSH agent forwarding.
- Herdr integration supports saved instance machines, per-session workspaces,
  workspace focus, and configured tabs/panes.
- Reach services running on the instance with `bivouac connect`, publish them
  on the tailnet with `bivouac serve`, and remove them with `bivouac unserve`.
- Move files outside git with `bivouac sync` and `bivouac download`.

#### Secrets, Tailscale, and GitHub tooling

- Manage encrypted personal secrets with `bivouac secrets init`, `edit`, and
  `keys`, backed by sops/age.
- Join instances to Tailscale automatically from `bivouac.pkl` or manually with
  `bivouac tailscale`.
- Optionally place GitHub CLI credentials on instances for workflows that need
  `gh` access.

#### LLM-friendly CLI surfaces

- `bivouac manifest --format mcp-tools` exposes the command surface as an
  MCP-shaped tool manifest for coding agents.
- `bivouac list --json`, `bivouac session list --json`, and
  `bivouac status --json` provide explicit machine-readable output without
  changing the default human output.
- Agent-context detection adds help hints without silently switching output
  formats.

#### Installation and releases

- Tagged releases publish macOS and Linux binaries for amd64 and arm64 via
  GoReleaser, including `sha256sums.txt`.
- `install.sh` installs a release archive after verifying its checksum.
- The repository flake now exposes a default `bivouac` package for `nix build`.
- `bivouac --version` reports release version, commit, and build date.

### 🔧 Internal

- CI and local checks run with Go 1.26.9 to pick up the standard-library
  security fixes required by `govulncheck`.
- Pre-commit covers formatting, linting, gosec, govulncheck, Nix formatting,
  conflict markers, large files, trailing whitespace, and secret scanning.
