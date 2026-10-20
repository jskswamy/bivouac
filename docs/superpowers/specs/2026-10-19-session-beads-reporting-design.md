# Session beads reporting

Status: draft for approval. Source: GitHub issue jskswamy/bivouac#3. Builds on
`2026-09-07-beads-in-sessions-design.md` (how issues travel) and
`2026-10-15-resumable-session-retirement-design.md` (merge's resumable
states).

## Intent

`session pull` says "11 new commits" and nothing about the issues it brings
back. The user has two moments where that hurts:

1. **Mid-session, "where are we?"** -- which tasks of the work the session was
   started for are done, and which are left. Answered *live* from the
   instance, not from whatever was last pulled.
2. **At merge, "did every issue come home?"** -- a record that each issue the
   session created, updated or closed is now in the local database. When one
   is not, merge must stop in a visible, resumable state rather than retire
   the session and lose it.

The user called this an MVP: get the two moments right, refine later.

## Goals

- `bivouac session status <name>`: one screen, live from the instance.
- `bivouac session issues <name>`: the full list of issues the session touched,
  with per-issue sync state.
- A one-line issue receipt at the end of `session pull` and `session merge`.
- Merge verifies, per issue, that the pull landed everything, and pauses with
  the reason "issues not synced" when it did not. Re-running
  `bivouac session merge <name>` re-pulls, re-verifies and continues -- the
  same idempotency every other retirement stop already has.
- `bivouac session merge <name> --accept-unsynced` is the way out when an
  issue can never sync: merge proceeds and records what it accepted.
- `session start` records the session's starting point so "planned" can be
  told apart from "discovered".
- `--json` on both new commands.

## Non-goals

- No change for repositories without beads, or for sessions beads was never
  wired for. Both new commands print a one-line "no issue tracking in this
  session" and exit 0.
- No beads logic inside bivouac beyond reading `bd` JSON. bivouac shells out to
  `bd` on both machines, as it does everywhere.
- No change to `session delete` or `down`; they keep `requireBeadsLanded`.
- No conflict resolution. `bd dolt pull` conflicts are reported as today.
- No push of the local database. Merge only *reminds* about `bd dolt push`.
- No reporting for sessions started before this ships beyond saying so (see
  *Sessions without a start record*).
- No "closed with no commit" detection. Dropped from the MVP by the user.

## Anti-goals

- A receipt that says "all synced" when bivouac could not actually check. An
  unknown is reported as unknown, never as success -- the same stance
  `requireBeadsLanded` takes.
- `session status` mutating anything: it must not pull, push, or commit on
  either side.
- Work the agent added mid-session silently inflating "remaining" so progress
  looks like it went backwards without saying why.
- A merge that a busy agent can keep stuck forever just by editing issues.

## Background: what exists

- In the default `session` beads mode, `session start` pushes the Mac's
  database into the session repository's `refs/dolt/data`; the instance runs
  `bd init` from that file URL (`beads.Bootstrap`) and has its own copy.
  `dolthub` mode is the same plus a second remote. `beads.Pull` = instance
  `bd dolt push` into the session repo, then Mac `bd dolt pull`.
- `bd` 1.3 exposes what this needs:
  - `bd vc status --json` → `{"branch": ..., "commit": "<hash>"}`.
  - `bd diff <from> <to> --json` → list of
    `{IssueID, DiffType: added|modified|removed, OldValue, NewValue}` where the
    values are full issue objects (`status`, `title`, `updated_at`, ...).
    Refs must be commit hashes or branch names (`HEAD~n` is rejected).
  - `bd list --json` for current state (builder: confirm the flag that
    includes closed issues).
- `session start --issue <id>` claims the issue on the instance
  (`markIssueStarted`) but the id is not persisted in bivouac state.
- `beads.Wired` returns false when the local `bd` is unavailable, so today a
  merge on a Mac whose `bd` has gone missing skips the beads pull entirely
  and then deletes the session repository that holds the issues.
- Merge runs through the retirement coordinator (`RetireSession`,
  `retirementPolicy.checkMerge`). `checkMerge` calls `pullBeadsRequired`
  before the replay on every run, and a failure pauses retirement via
  `pauseRetirement` (phase `paused`, unless already `removing`); success
  records `IssueEvidence: "pulled"`. Phases are matched by name in the state
  validator (`internal/state/session_lifecycle.go`), the resume checks
  (`internal/lifecycle/retirement.go`) and the attach guards
  (`internal/lifecycle/herdrownership.go`). `MergeSession` in `merge.go` is an
  older path the CLI no longer calls (builder: confirm, and leave it alone or
  remove it in a separate commit).
- Retirement removes the checkout and `TASK.md`, then `rmdir`s the session
  directory; any other file left there leaks the directory on the VM
  (cloudlab-8ak). Nothing in this design writes to the session directory.

## Design

### 1. Recording the starting point (`session start`)

After `bootstrapBeads` succeeds, bivouac records on the session's state entry
(`state.Session`, new optional field, e.g. `Beads *SessionBeads`):

| Field | Source | Purpose |
| --- | --- | --- |
| `StartCommit` | instance `bd vc status --json` after bootstrap | the exact "before" for every diff |
| `Issue` | `--issue` flag (empty if none) | the work the session was started for |
| `Planned` | instance: `--issue` id plus all its descendants at start | planned vs discovered |

Read on the instance, not the Mac: the instance's database is what every later
diff runs against, so its own commit is the one guaranteed to resolve there.

The claim from `markIssueStarted` must happen **after** `StartCommit` is
read, so the claim itself shows up as the session's first change. (Builder:
check the current ordering in `runSessionStart`.)

Recording is best-effort like everything else in `seedBeads`: a failure warns
and the session starts anyway, and is then treated as a session without a
start record.

The record is also the wiring proof for merge (section 6): a session that
has one had beads set up, whatever the local `bd` can say today.

### 2. The session diff (shared core)

One function computes "what this session did" up to a given instance commit:

1. On the instance: `bd diff <StartCommit> <to> --json`.
2. Classify each issue **created / closed / updated / removed** (closed wins
   over updated).

`<to>` is the instance's current commit (`bd vc status --json`) for `status`
and `issues`, and the **pinned** commit for merge and pull (section 5).

If `StartCommit` no longer resolves on the instance (the agent ran
`bd compact`/`flatten`/`gc`), there is no fallback: report "beads history was
rewritten on the instance; can't list this session's changes". For merge
that is an unknown, so it stops (section 6). (My call -- a start snapshot
kept on the VM would leak the session directory and covers a rare case.)

Caveat for the builder: if the instance's `bd` runs with
`dolt.auto-commit=batch`, uncommitted working-set changes are invisible to a
commit-to-commit diff. bivouac does not set that, and the default is `on`;
detect it (`bd vc status` reporting uncommitted changes, if it does) and say
so rather than under-report.

### 3. `bivouac session status <name>`

Live, read-only. Connects to the instance and prints:

```
pi  active · 7 commits ahead of main · worktree clean · agent running
issues (live from instance):
  epic cloudlab-1em  2 of 5 planned closed
    ✓ cloudlab-1em.1  Discover selected keys
    ✓ cloudlab-1em.2  Thread key through seed push
    ◐ cloudlab-1em.3  Use key for herdr add
    ○ cloudlab-1em.4  ...
    ○ cloudlab-1em.5  ...
  discovered this session: 2 (1 closed) · updated: 4
needs attention: base branch was rewritten since this session started
```

- Header line reuses what `session list` / `SessionInfo` already computes
  (state, commits ahead of base, worktree clean/dirty, agent running/idle).
  A paused retirement shows here with its reason, e.g. "merge paused: issues
  not synced (3)".
- **Planned** = `Planned` ids, with their *current* status on the instance.
  "x of y planned closed" counts only those.
- **Discovered** = issues created this session (from the diff), listed under
  the epic when parented to it, otherwise flagged "no parent epic".
- **Needs attention** (MVP set): base branch rewritten (existing
  `requireBaseExists` signal); beads history rewritten on the instance
  (section 2); `--issue` target itself closed while planned children are
  still open.
- No `--issue` recorded: show created / closed / updated counts and lists, no
  "x of y" line. (My call.)
- Instance unreachable: say so and exit nonzero; never print last-known
  figures as if live.

### 4. `bivouac session issues <name>`

The drill-down: every issue in the session diff, one line each with ID,
title, classification, and **sync state**:

- `synced` -- local database has the issue with `updated_at` ≥ the instance's
  (or, for `removed`, the local copy is gone too).
- `not synced` -- missing locally, or local `updated_at` older. Printed loudly.
- `unknown` -- local `bd` unavailable or errored.

Plus the `--issue` target and its final status, and created issues with no
parent epic. Sync state compares against the local database **as it is now**;
`issues` does not pull. (Running it before a pull is how you see what a pull
would bring.)

Local state is read with one `bd list --json` (closed included) and looked up
by id, not one `bd show` per issue.

### 5. Receipt on `pull` and `merge`

`beads.Pull` reads the instance's commit (`bd vc status --json`) immediately
**before** its `bd dolt push` and returns it. That pinned commit `C` is what
the receipt and merge's verification diff against: everything up to `C` was
pushed and pulled, so anything still missing locally is a real gap, while an
issue the agent edits after `C` is simply not in this round. Without the pin,
an agent that keeps editing issues makes every re-run of merge see a fresh
"not synced" edit and never finish.

After the beads pull, both commands print one line from the same data:

```
issues from pi: 3 created · 4 closed · 2 updated — all 9 synced ✓
```

or, naming each straggler:

```
issues from pi: 3 created · 4 closed · 2 updated — 1 NOT synced
  ✗ cloudlab-1em.6  Add retry to herdr add  (instance 14:02, local 13:40)
```

`pull` only reports. `merge` acts on it (next section). When the local
repository's beads mode has a remote of its own (`beads.Detect` → git or
external), merge's final output adds: "issues are only local until you `bd
dolt push`". A reminder, not a check -- bivouac cannot cheaply tell whether
that push has happened. (My call.)

### 6. Merge: paused on unsynced issues

**Wiring.** Merge's beads gate becomes: the session has a start record
(section 1) **or** `beads.Wired` is true. With a record and no usable local
`bd`, the verification result is `unknown` and merge pauses -- it no longer
skips beads and deletes the issue database. `beads.Wired` itself, and every
other caller of it, is unchanged; sessions without a record behave exactly as
today.

**Verification.** In `checkMerge`, `pullBeadsRequired` becomes pull **then
verify**: diff `StartCommit..C` (section 5) and compute per-issue sync state
(section 4).

- All synced → continue as today; `IssueEvidence` records
  `verified:<C>` instead of `"pulled"`.
- Pull failed, `StartCommit` unresolvable, or any `not synced` / `unknown` →
  retirement pauses through `pauseRetirement` with a new reason field on the
  retirement record (e.g. `PausedOn: "issues"`) and `LastFailure` naming the
  unsynced ids. The error names the resume command,
  `bivouac session merge <name>`, and the override (below).

**Not a new phase.** The user asked for a distinct state; it is delivered as
`paused` plus the reason, which `session status` and `session list` print as
"merge paused: issues not synced (N)". A new phase name would have to be
taught to the validator, both resume checks and both attach guards, and an
older bivouac binary would reject the state file. (My call.)

**Resume.** Re-running merge from this pause is the existing paused-resume
path: `checkMerge` runs again from the top, re-rescuing, re-pulling and
re-verifying. Nothing is skipped because an earlier run reached it. A
successful verification clears the reason.

**Where it can stop.** The first check runs before any replay, so a pause
there leaves the session completely untouched. The late-work re-check (after
Herdr resources close, `final-check`) re-runs `checkMerge` and so
re-verifies issues too; a pause there happens after the replay and after the
panes closed, which `pauseRetirement` already handles as a `paused` from
`final-check`. The message must say what is still intact -- the session
repository and its issue database on the instance -- and not claim nothing
was touched.

**Override: `--accept-unsynced`.** Some gaps never close: an issue deleted
or rewritten locally will always differ from the instance's copy, and merge
has no `--force`. With `--accept-unsynced`, a verification that finds
`not synced` or `unknown` issues does not pause: merge records
`IssueEvidence: accepted-unsynced:<C>:<ids>`, lists the accepted ids in the
receipt, and continues. The flag only affects the issue check; every commit
safety gate stays. Pull failures that leave nothing to compare (instance
unreachable) still pause. Chosen by the user over making `session delete
--force` the only exit.

## Sessions without a start record

Sessions started before this ships, or whose recording failed:

- `session status` shows the header line and "this session predates issue
  reporting".
- `session issues` says the same and exits 0.
- `pull` and `merge` behave exactly as today: pull-only, `IssueEvidence:
  "pulled"`, no receipt.

These disappear as old sessions retire. (My call -- state records no start
time, so an "approximate" mode would have to invent one.)

## Output formats

`--json` on `session status` and `session issues`, registered in the command
table in `cmd/lookup.go` with `SupportsJSON` and the LLM-readable metadata
other commands carry. `--accept-unsynced` is registered on `session merge`
the same way. JSON shape is the builder's call; it must include, per issue,
`id`, `title`, `status`, `classification`, `planned` (bool), `sync`
(`synced|not-synced|unknown`), and at the top level the session name,
`start_commit`, `instance_commit` and the retirement phase and reason.

## Left to the builder

- Exact `bd` invocations and flags, verified against the pinned instance `bd`
  (`templates/modules/beads-pkg.nix`) as well as the Mac's.
- Whether `session list` gains a `closed/total` column. It costs an SSH round
  trip per session; if added, it is best-effort and must not slow the list
  when an instance is down. Optional for the MVP.
- How "agent running/idle" is detected, if `SessionInfo` doesn't already know.
- Test seams: follow `instanceRunner` in `internal/beads` -- every instance
  command behind the fake, so the diff, pinning and verification logic are
  unit-tested without SSH. Required tests include: the agent editing an issue
  after the pinned commit does not cause a pause; a session with a start
  record and no local `bd` pauses rather than skipping; the paused-on-issues
  resume path and `--accept-unsynced` in coordinator tests alongside
  `retirement_test.go`.

## Release notes

User-facing: two new commands, a receipt on pull/merge, a new merge pause and
its override. Entry appended to `RELEASE_NOTES.md` under `## Unreleased` in
the same commit as this spec.
