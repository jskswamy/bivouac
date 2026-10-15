## Unreleased

### Fixed

- Session pull, merge, and delete now use the session's recorded repository
  path rather than the instance name, including nested `owner/repo` checkouts.
  Older sessions without a recorded path retain the instance-name fallback.

### Changed

- Session merge and delete now use one resumable retirement workflow. Cleanup
  closes only ownership-proven Herdr resources, records each destructive step,
  preserves other sessions and shared machines, and resumes with the same
  command after partial failure. Unreachable resources remain pending rather
  than being treated as absent.
- **Breaking:** `sshKeys` now accepts public-key file paths only, not MD5/SHA256
  fingerprints or numeric account IDs. Relative paths retain their declaring
  config's directory; base/project lists merge additively. `bivouac init`
  repairs old entries in their original files. See [migration](docs/config.md#ssh-keys).
- `up` checks selected files, local signing capability, account matches and SSH
  settings before final confirmation. Approved registrations upload public
  material only and never rename/delete account keys. Tokens need key-read
  access and, when additions are needed, key-create access.

### Added

- Recorded machine identities and one persistent `bivouac-<name>` SSH alias
  shared across sessions, provisioning, SSH/tmux/pair/forwarding, rsync, Git
  remotes and Herdr targets. Ordinary Git fetch and separate-process Herdr
  0.9.4 saved-machine reconnection are verified with disposable SSH fixtures.
- Explicit guidance for missing/changed identities and conflicting inherited
  SSH/Git settings, without silently trying unrelated agent keys. Old state
  without identity metadata keeps legacy authentication behavior.
- Machine-profile cleanup only after confirmed destruction; failed selected
  destroys retain state for retry. Session cleanup leaves the shared alias.

Live cloud writes and physical hardware keys have not been exercised. Beads
remotes store the shared alias, but actual Dolt interoperability remains an
external verification gap. See the [execution record](docs/superpowers/plans/2026-10-14-selected-ssh-key-execution-notes.md)
for evidence and limitations.

## v0.1.0

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
