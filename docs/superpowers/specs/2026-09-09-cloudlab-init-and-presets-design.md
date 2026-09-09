# cloudlab init and presets

Status: designed, not implemented
Date: 2026-09-09
Tracks: `cloudlab-pwg.1` (this design), `cloudlab-pwg.2`/`.3` (implementation)
Supersedes part of: `cloudlab-535`

## The problem

Configuring cloudlab means authoring `cloudlab.pkl` by hand against a schema
you have to go read. That is a fine second experience and a poor first one:
the fields are discoverable only from `internal/config/Config.pkl`, several
have non-obvious values (`size` is a DigitalOcean slug, `sshKeys` are
fingerprints), and `up` on a repository without the file simply refuses.

Two entry points, one flow: an explicit `cloudlab init`, and the same flow
launched automatically when `up` finds no config. Answers that recur across
projects can be saved as a **preset** and offered on the next run.

## Vocabulary

Three things were competing for two words, and nothing was implemented yet,
so the collision is resolved here before it reaches code.

| Term | Means | Was called |
|---|---|---|
| `profile` | a Nix toolchain bundle — docker, python, k8s | `template` today; `presets` in the 2026-09-07 spec |
| `preset` | a saved set of wizard answers | `preset` in `cloudlab-535` |

`cloudlab-535` reserved "preset" for saved answers *because* "template" was
already the Nix concept. The 2026-09-07 presets-and-connect spec then took
"preset" for the Nix concept as well. Both were unimplemented. "Profile" is
the more natural word for a toolchain bundle, so the Nix side moves and
`cloudlab-535`'s meaning of "preset" stands.

**The rename is not this project's work.** The wizard writes today's
`template` field. When the profiles rename lands, one question changes from
single-select to multi-select. Sequencing init behind a breaking schema
change would block a usable feature behind three unrelated issues
(`cloudlab-5uf`, `cloudlab-c2b`, and the rename itself), and shipping first
is also the better way to learn what the questions should be.

## Storage

```
~/.config/cloudlab/base.pkl              personal: region, size, sshKeys
~/.config/cloudlab/presets/<name>.pkl    reusable project shapes
./cloudlab.pkl                           this project, committed
```

Presets are ordinary partial pkl files, the same shape as `base.pkl`. They
are **read and stamped** into a new `cloudlab.pkl`, never referenced from it.

### Why stamp rather than link

`basePath` already exists and could point a project at a preset, which would
keep project files tiny and let a preset fix reach every project at once.
It is rejected because `cloudlab.pkl` is committed. A file reading
`basePath = "~/.config/cloudlab/presets/go-api.pkl"` is incomplete for anyone
who does not have that preset — and `config.Resolve` skips a missing base
rather than failing, so a teammate would silently provision a different
machine. Stamping keeps the committed file true on its own.

The cost is drift: editing a preset does not change projects already made
from it. That is the correct trade for a file other people clone.

### Why answers split across two files

Stamping every answer into `cloudlab.pkl` would commit the user's SSH key
fingerprint and their personal sizing, and a teammate provisioning from that
file would get the author's key rather than their own. So answers route by
nature, automatically, rather than by a "repo or user scope?" question:

| Personal → `base.pkl` | Project → `cloudlab.pkl` |
|---|---|
| `region`, `size`, `sshKeys` | `template`, `agents`, `packages`, `beads`, `tailscale` |

This supersedes `cloudlab-535`'s "offers saving the result at repo scope or
user scope". Routing by field nature gets the same outcome without asking a
question whose right answer never changes.

## The flow

One flow, four entry conditions:

| Situation | Behaviour |
|---|---|
| No `base.pkl` (first run ever) | Ask the personal questions, write it, continue |
| `base.pkl` exists | One-line summary with `[use]` / `[change]` |
| No `cloudlab.pkl` | Offer saved presets plus "start fresh", then project questions |
| `cloudlab.pkl` exists | Edit mode, fields pre-filled from the file |

Personal questions are asked once. Every later run is the project questions
plus a confirmation line.

### `up`'s fallback

`up` with no `cloudlab.pkl` runs this flow and then provisions, in one
invocation. This does not contradict the earlier "the file must exist, no
implicit fallback" decision: the file still exists before `Reconcile` runs.
Creating it interactively is just friendlier than refusing.

### Non-interactive is a hard stop

With no TTY the wizard must not open. It fails with a message naming
`cloudlab init`. A form blocked on stdin that will never arrive is
indistinguishable from a hang, and `up` is the command most likely to be
scripted or piped. This applies to both entry points.

### Existing files that predate the split

Most projects — including this repository's own `cloudlab.pkl` — hold
personal and project fields in one file. Editing one must not rewrite it.

The flow notices personal fields, says what it found (naming `sshKeys`
explicitly if it is committed), and offers to lift them into `base.pkl`.
Declining edits in place and changes nothing. Normalising automatically is
rejected: it turns "change one package" into a whole-file diff in a
repository someone else also works in.

Both outcomes are behaviour-preserving. Moving a field from `cloudlab.pkl`
to `base.pkl` resolves to the same config, because scalars in the project
file override base and `packages` merges additively — the existing semantics,
documented in `docs/examples/with-base/`.

## Presets

At the end of a run: *"Save this shape as a preset?"* with a name.

A preset captures `template`, `agents`, `packages` and `beads` always, and
`size`/`region` **only when the run overrode the base defaults**. It never
captures `sshKeys`.

The conditional sizing exists for one real case: a k8s profile that needs
headroom. A shape-only preset cannot express that, and picking it on a
1vcpu base fails later with nothing having warned. A preset that always
captured sizing would instead reset personal choices every time it is used.

```
cloudlab preset list              name, what it sets, when it changed
cloudlab preset show <name>       print the pkl
cloudlab preset edit <name>       the same flow, scoped to that file
cloudlab preset delete <name>     confirms
```

The "when it changed" column is the file's mtime. Tracking last-used would
mean writing metadata on every read, which is a lot of machinery for a
column nobody sorts by.

`delete` needs no dependency check. Presets are stamped, so no project can
depend on one — the one place link-versus-stamp visibly pays off.

## Components

The huh forms are the part that resists testing, so nothing else lives in
them.

| Unit | Responsibility | Depends on |
|---|---|---|
| `internal/preset` | locate, list, load, save, delete under XDG | `internal/config` |
| `internal/config/write.go` | render a config subset to pkl text | nothing |
| `internal/wizard` | question definitions; routing answers to destinations | `internal/config` |
| `cmd/init.go`, `cmd/preset.go` | forms and wiring | all of the above |

`write.go` is the counterpart to today's read-only `Resolve`, and pkl-go's
codegen does not generate writers, so this is hand-written text rendering.

**Every write is read back through `config.Resolve` before the flow exits.**
A wizard that emits pkl which does not parse is worse than no wizard, and
this is the only check that proves it did not.

## Testing

- Answer routing is a pure function over field names: table test.
- `write.go` renders text: golden tests, plus a round trip asserting every
  rendered file re-parses through `config.Resolve`.
- `internal/preset` CRUD against a temp XDG dir.
- The non-interactive stop: assert both entry points fail with a message
  naming `cloudlab init` when stdin is not a TTY, and that no form opens.
- The forms themselves are covered by the units beneath them, not driven.

## Out of scope

**SSH key discovery** (`cloudlab-pwg.4`). The wizard offers keys already in
`base.pkl` or accepts a fingerprint. Querying the DigitalOcean API for the
account's keys is its own design.

**The profiles rename.** Belongs to the 2026-09-07 presets-and-connect spec.

## Rejected alternatives

**Link projects to presets via `basePath`** — a committed file that is
incomplete without a file only the author has, failing silently. See above.

**Ask "repo scope or user scope?" at the end** — an extra decision on every
run whose right answer is determined by the field, not the user.

**A `profiles` alias shim so new files are future-shaped** — two spellings of
one field in a schema that is about to change anyway.

**Normalise file layout on every edit** — restructures a committed file as a
side effect of editing one setting.
