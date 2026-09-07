# 0010: One instance per repo — sessions scale, instances do not

## Status

Accepted

## Context

ADR-0003 derives an instance's name from its git repo, so "which instance"
never has to be typed. The git-aware sync design then made a *session* the
unit of agent work, and the multi-session design let an instance run several
at once. Sessions became the axis that scales; the instance stayed one per
repo.

That raised an obvious question: if a droplet sized for one agent is usually
sized for two, why is it not also host to two *projects*? A single box shared
across every repo you work in would cost less than one box each, and the
session workflow would be unchanged — `start`, `pull`, `merge` already carry
their own repository in `state.Session.LocalRepo`, so the state shape could
describe it without modification.

The mechanics were never the obstacle. Sharing was designed far enough to
find where it actually costs:

- **Provisioning has one input and would need m.** `reconcile` renders one
  `~/.cache/cloudlab/flake.nix` from one `cloudlab.pkl`. A second repo on the
  same box gets the first repo's environment and none of its own. Fixing that
  means the instance's environment becomes the union of every attached repo's
  config, recomputed on every reconcile — so `provision` reads files in
  directories you are not standing in, and a package appears or vanishes
  because of a repo you are not working on.
- **Attachment becomes a lifecycle with no natural end.** A repo joins by
  starting a session, but nothing says when it leaves. `down` on a box three
  repos depend on has to speak for all three.
- **Every resolution rule gains a repo dimension.** "The instance's only
  session" and "cd into the worktree" both assume the sessions belong to the
  repo you are in. Shared, they do not, and the failure is silent: the
  command resolves a real session belonging to a repo you never mentioned.

## Decision

An instance belongs to exactly one repo. Sessions are the axis that scales;
instances are not multiplexed across projects.

`n` sessions per repo, one instance per repo, one repo per instance. Working
on two projects at once means two instances — which is what ephemeral
compute is for.

The escape hatches ADR-0003 already provides stay as they are: `--name` for
more than one instance against the same repo (a python and a docker box side
by side), and two clones of one repo deliberately sharing an instance.

## Consequences

- The design language stays small. "Which instance" has one answer, derived
  where it always was, and no command needs a repo picker.
- `cloudlab.pkl` remains the whole story for an instance's environment: one
  file, one box, no union to reason about.
- **Cross-repo sessions are not supported.** A single unit of work spanning
  `service-a` and `service-b` — one branch name, several repositories,
  merged together — has no representation. That is Stage 3 of
  [the git-aware sync design](../superpowers/specs/2026-09-05-git-aware-sync-design.md),
  and this ADR does not close the door on it: `state.Session` already carries
  its own repository, so widening it to a list stays additive. What is
  rejected here is *sharing an instance between unrelated projects*, not
  multi-repo sessions.
- Running m projects costs m droplets. Accepted: instances are ephemeral and
  destroyed when idle, and a second droplet is cheaper than the concepts
  sharing one would add.
- Two clones of one repo still share an instance (ADR-0003), so "the repo the
  session belongs to" and "the repo you are standing in" remain two different
  answers. Session commands must read the repository from
  `state.Session.LocalRepo`, never from the caller's working directory —
  `pull` and `merge` did the latter until this decision was recorded.

## Rejected alternatives

**Instances named independently of repos, repos attaching at `session
start`.** The most flexible shape, and the one that motivated this ADR: name
a box `devbox`, attach any repo to it. Rejected because every cost above is
still owed, and the flexibility buys only the droplet.

**A primary repo declaring the others in `cloudlab.pkl`.** Config rather than
runtime attachment, so provisioning has one input again. Rejected because it
makes one repo permanently senior to the others — its checkout must exist to
provision the box, and the sibling repos it names cannot be worked on
independently.
