# Rename cloudlab to bivouac

## Status

Accepted

## Context

cloudlab's name describes its mechanism (a cloud VM) but not its point. The
actual value is that the instance is disposable by design: it is declared
from scratch by Nix/home-manager rather than hand-configured, and nothing
survives a `down` except what was deliberately captured as a signed git
commit through the session/merge flow. "Cloud" says nothing about that.
"Lab" implies a scratch sandbox, which undersells the discipline built into
the merge path (every commit is signature-verified before a session is
deleted). The name also doesn't read as a sibling to `aide` (a local,
OS-native sandbox for the same "run a coding agent safely" problem) and
collides conceptually with the existing CloudLab.us academic testbed.

**bivouac** — a shelter improvised for one stay, with no built
infrastructure, struck completely afterward — matches the actual model: a
temporary remote camp for an agent session, where nothing but what you
carry out (your commits) persists.

## Decision

Rename the project, repo, and binary to **bivouac** — the full word, not a
clipped `biv`. Precedent: `vagrant`, `terraform`, and `ansible` are full
words in the same "ephemeral dev/infra lifecycle CLI" space and are typed
just as often. Discoverability and brand clarity matter more than shaving a
few keystrokes tab-completion already erases. `biv` remains available as an
unofficial personal shell alias.

## Scope & principle

Pure rename — no functional or behavioral change to the tool. Hard cutover:
no dual-reading of old and new config filenames, XDG paths, or session
branch prefixes. Explicitly out of scope for this epic:

- The `aide` repo (separate repository; a follow-up there if wanted later).
- The shared portfolio/domain question (e.g. a domain to hold aide, bivouac,
  and future projects) — parked separately, no concrete work defined yet.
- Dated historical docs (`docs/adr/*`, `docs/superpowers/specs/*`,
  `docs/superpowers/plans/*` other than this file) — these are timestamped
  decision records of what was true when the project was named cloudlab, and
  keep reading that way, the same reasoning as not rewriting old commit
  messages.

## Sequencing

Independent of this epic: pending sessions (`aide`, `session-workflow`,
`gh-cli`) get merged first, on their own schedule.

Within the rename itself, the GitHub repo rename is the **last** step, not
the first — every local change lands and is verified while the repo is
still physically named `jskswamy/cloudlab`, and only then does the actual
GitHub rename flip the remote to match. This avoids a window where
`go.mod`'s module path points at a repo name that doesn't exist yet.

## Task breakdown

1. **Go module & source rename** — `go.mod` module path
   `github.com/jskswamy/cloudlab` → `.../bivouac`, all importing files, the
   Cobra `Use: "cloudlab"` in `cmd/root.go`, and comments mentioning
   cloudlab. Mechanical (sed + `goimports`), verified by `go build ./...`.
2. **Runtime identity rename** — the XDG segment in `internal/xdg/xdg.go`
   (`"cloudlab"` → `"bivouac"`, drives `~/.config/bivouac`,
   `~/.cache/bivouac`, `$XDG_RUNTIME_DIR/bivouac/dolt`), the hardcoded
   config filename in `internal/wizard/wizard.go` (`cloudlab.pkl` →
   `bivouac.pkl`), and the session branch prefix in
   `internal/lifecycle/gitpaths.go` (`"cloudlab/"` → `"bivouac/"`) —
   including their existing unit tests (`internal/xdg/xdg_test.go`, etc.).
3. **Config files & examples** — root `cloudlab.pkl` → `bivouac.pkl`,
   `docs/examples/*/cloudlab.pkl` → `bivouac.pkl`, references inside
   `internal/config/Config.pkl`.
4. **Build/env plumbing** — `Makefile` (`NAME := cloudlab`), `flake.nix`
   description string and comment, `internal/provisioning/cloud-init.sh`
   comment and the `99-cloudlab-disable-root.conf` filename.
5. **Docs refresh** — separate tasks per file: `README.md`, `AGENTS.md`,
   `CONTRIBUTING.md`, `docs/architecture.md`, `docs/config.md`. README gets
   the new framing: disposable, declared-from-scratch remote workspaces for
   agent sessions, nothing outlives the VM but your commits.
6. **Verification** — `go test ./...`, `pre-commit run --all-files`,
   `nix flake check` all green after the rename.
7. **Repo rename** — `gh repo rename bivouac` on `jskswamy/cloudlab`, then
   update the local git remote. Last, once 1–6 are committed and verified.

## Out of scope

- Backward-compatibility shims of any kind.
- The `aide` repo's README/docs.
- Buying or configuring a shared portfolio domain.
