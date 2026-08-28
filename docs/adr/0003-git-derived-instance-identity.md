# 0003: Instance identity derived from the git repo

## Status

Accepted

## Context

With multi-instance support (ADR-0002), every command needs a way to resolve
"which instance." A generic default name (falling back to something like
`default` with explicit `--name` for others) was one option, but cloudlab's
usage pattern is more specific: it's always used from inside a project, and
the natural unit of "a project" here is a git repo — the repo gets rsynced
to `~/reponame` on the instance.

Two follow-on questions came up during design:

1. Should instance identity be tied to the invoking *directory* or the *git
   repo root*? Directory-based naming breaks the moment you run a command
   from a subfolder rather than the repo root.
2. Should the name be the folder name, or something more collision-resistant?
   Two different clones with the same folder name (forks, or the same repo
   checked out twice) would otherwise collide onto one instance.

## Decision

- Every command resolves the git repo root via `git rev-parse
  --show-toplevel` starting from `cwd`, so it works identically from any
  subfolder.
- The instance name is derived from `git remote get-url origin`
  (slugified `owner/repo`, e.g. `jskswamy-cloudlab`), falling back to the
  folder name only when there's no remote configured.
- Running outside any git repo doesn't error asking you to `cd` somewhere —
  it requires an explicit `--repo <path>` instead of inferring one.
- `--name` overrides the derived name, for the cases where you want more
  than one instance against the same repo (e.g. python and docker instances
  side by side).

## Consequences

- `up` always seeds the repo root, not `cwd` — running it from a subfolder
  syncs the whole repo correctly regardless of where the command was
  invoked.
- Two clones of the same repo (different local paths, same `origin`) share
  one instance rather than silently creating two VMs — this is a
  deliberate collision, not a bug: it matches "one instance per project"
  intent even across multiple checkouts.
- A local-only repo (no `origin` remote) falls back to folder-name naming,
  which reintroduces the collision risk that remote-derived naming was meant
  to avoid — accepted as an edge case, not worth solving further until it's
  an actual problem.
- Similarly, a git worktree resolves `--show-toplevel` to the worktree's own
  path, so the no-`origin` folder-name fallback names the instance after the
  worktree directory rather than the main project — same accepted-edge-case
  category as above.
