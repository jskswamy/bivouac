# 0001: Go, as a standalone open-source repo

## Status

Accepted

## Context

cloudlab is a CLI tool intended for public, open-source use from the start —
people `go install` it or download a binary, not clone a language runtime
and manage a virtualenv just to run a provisioning tool.

## Decision

Implement in Go, in its own repo (`jskswamy/cloudlab`), scoped to exactly
this tool.

- Go: single static binary, no interpreter/dependency setup burden for
  users installing a CLI tool.
- Standalone repo, from the start: an open-source CLI needs its own issues,
  releases, and README as a front door — not nested inside an unrelated
  project's history and tooling.

## Consequences

- Every command, the provider abstraction (ADR-0008), and the Nix/flake
  tooling are implemented fresh, guided by the design in this repo's `docs/`.
- Local `git init` only for now; the GitHub repo is created when there's
  enough here to actually show.
