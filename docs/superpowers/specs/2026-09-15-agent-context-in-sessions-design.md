# Agent context in sessions

Status: implemented
Date: 2026-09-15
Tracks: `cloudlab-bsw` (this design)
Builds on: `docs/superpowers/specs/2026-09-07-beads-in-sessions-design.md`,
`docs/superpowers/specs/2026-09-14-session-development-workflow-design.md`

## The problem

An agent in a session has the repository and nothing else. It does not
know it is on an ephemeral VM, that there is nowhere to push, that only
commits survive, or that anything untracked will be swept into a
checkpoint. It also does not know what the session was started for.

`cloudlab-bsw` names the first half. The 2026-09-14 workflow spec named
a workflow but delivered it only to this repository's own `AGENTS.md`,
which reaches an agent working on cloudlab and nothing else. A session
started against any other repository sees none of it.

## Two kinds of content, and only one of them is cloudlab's

The tempting design writes one file containing both the session facts
and a development workflow. That is wrong, and the reason decides the
whole shape.

| Author | Content | Why |
|---|---|---|
| cloudlab | Session facts: no push, commits survive, untracked is swept, the VM is ephemeral | cloudlab is the only thing that knows them. True of every session regardless of harness or process. |
| The user | The workflow: whichever skills, plugins and process they want | Not cloudlab's business, and cloudlab cannot know it. |

The 2026-09-14 spec's workflow names `refactor:scan` and
`commit-tools:review-commits`. Those are one person's Claude Code
plugins. Shipping them from cloudlab would make a harness-agnostic tool
distribute one harness's ecosystem as policy, to users who may run
`codex` or `pi` and have never heard of either.

**So cloudlab authors the facts and transports the workflow.** With no
workflow configured, a session gets the facts and nothing else. cloudlab
ships no opinion about how anyone works.

The session facts are not optional in the same way. They are not
opinion: they describe the environment cloudlab built, and an agent that
does not know "commits are what survive" will lose work. They are the
floor.

## Delivery

**Write to each configured harness's global instruction file on the
instance. Never into the checkout.**

### Why not the checkout

The obvious design creates or appends to `AGENTS.md` at the repository
root. It cannot be made safe.

`.git/info/exclude` hides only *untracked* files, and `checkpointCmd`
runs `git add -A` on every pull. A repository that already tracks
`AGENTS.md` -- this one does -- would have cloudlab's edit committed onto
the user's branch and cherry-picked under their signature at merge.
This is the failure `excludeBeadsCmd` exists to prevent, and exclude
cannot prevent it for a tracked file.

`git update-index --skip-worktree` would suppress it, at the cost of
silently discarding any legitimate edit the agent later makes to that
file. In a tool whose promise is that commits survive, a file that
quietly stops surviving is worse than the problem.

Writing outside the checkout dissolves all of it. Whether the repository
has `AGENTS.md`, `CLAUDE.md`, both or neither, the behaviour is
identical, and no exclusion, no marker-stripping at merge and no
skip-worktree is needed anywhere.

### The global slot is the only portable mechanism

Per-directory discovery is not portable. Claude Code and opencode
traverse upward from the working directory; Codex begins its chain at
the git repository root and walks *down*. So a file placed above the
checkout reaches two harnesses and is invisible to a third.

The global slot is the one location every harness that has one agrees
on, and it is loaded by the tool's own documented rules rather than by
anything cloudlab arranges.

| `agents` entry | Global instruction file |
|---|---|
| `claude` | `~/.claude/CLAUDE.md` |
| `codex` | `~/.codex/AGENTS.md` |
| `opencode` | `~/.config/opencode/AGENTS.md` |
| `copilot` | `~/.copilot/copilot-instructions.md` |
| `pi` | `~/.pi/agent/AGENTS.md` |
| `cursor` | **none -- see below** |

Written only for the harnesses `cloudlab.pkl`'s `agents` listing names,
which cloudlab already has.

Two details worth knowing rather than rediscovering. opencode falls back
to `~/.claude/CLAUDE.md` when it finds no rules file of its own, so a
claude+opencode instance is covered by one file either way. Copilot's
user-level path moves with `COPILOT_HOME` when that is set.

### Cursor has no global file

Cursor's CLI reads `AGENTS.md` and `CLAUDE.md` at the project root and
`.cursor/rules`, but cross-project instructions live in User Rules,
configured in the GUI. There is no `~/.cursor/AGENTS.md` to write.

cloudlab cannot deliver to cursor without writing into the checkout,
which the previous section rules out. So `cursor` in `agents` earns a
warning naming the limitation, in the manner of
`warnDolthubWithoutExternalRemote`. Named, not silently skipped: an
instruction that did not arrive is indistinguishable from one that was
ignored, and the user is the only one who can decide what to do about
it.

## The `instructions` field

```pkl
instructions: Listing<String> = new Listing {}
```

Paths to Markdown files, resolved relative to the config file declaring
them, concatenated in order after cloudlab's own block.

`Listing<String>` is chosen so it **merges additively exactly as
`packages` does** -- semantics the schema already has and
`docs/examples/with-base/cloudlab.pkl` already documents. That produces
the personal/project split for free, with no new merge rule to explain:

```pkl
# ~/.config/cloudlab/base.pkl -- personal, every project
instructions { "instructions/my-workflow.md" }
```
```pkl
# ./cloudlab.pkl -- this project, committed
instructions { "docs/agent-workflow.md" }
```

A missing file is a hard error at config resolution, not a warning. A
silently absent workflow is the failure this whole design exists to
prevent, and unlike a missing optional credential there is no degraded
mode worth continuing into.

cloudlab never parses the content. It does not validate skill names,
does not care which harness they belong to, and does not rewrite them.

The field is what stops the 2026-09-14 workflow spec being hardcoded
policy: any repository wanting it points `instructions` at it, and
cloudlab ships none of its own. This repository does not yet do so --
see "Dogfooding is deferred" below for what that would take.

## Where the wizard fits

`instructions` is a schema field, so `cloudlab init` has to know about it
whether or not it asks.

### Registration is not optional

`declaredFields` filters through `KnownField`, so a field the writer does
not know is invisible to `ReadValues`. Adding `instructions` to
`Config.pkl` without also registering it in `FieldInstructions`,
`fieldOrder`, `fieldKinds`, `fieldValue` and `KnownField` would make
`cloudlab init` in edit mode silently drop the line it cannot see -- data
loss, in the one command whose job is to rewrite that file. The field
constants already carry a comment saying they are spelled once so the
renderer, the wizard's routing table and the preset store cannot drift.
This is that comment being load-bearing.

### One question, in the personal set

Appended to `PersonalQuestions()` after region and size, routed to
`base.pkl`. Not a new flow: the 2026-09-09 design is one flow with four
entry conditions, and a second would break the sharing that keeps
`preset edit` from drifting.

That places it under the existing "personal questions are asked once"
rule. It appears on the first run ever, and afterwards only when the user
picks `[change]` against the `base.pkl` summary. A routine `init`, and
the `up` fallback, never see it.

It joins `PersonalQuestions()` rather than taking its own step the way
`sshKeys` does. `sshKeys` is separate because its choices are discovered,
and a free-text answer would misrepresent them. A path has nothing to
enumerate.

Skipping writes nothing, per "Only what was chosen is written". The field
remains editable by hand in either file; the question exists so the
feature is discoverable, not because it is the only way in.

### The one question validated in the loop

The wizard deliberately does not validate. Region and size take typed
slugs, and their comment says so: a wrong-but-valid slug is caught by the
provider at `up`.

`instructions` breaks that rule, and the reason is not a different
appetite for checking but what the wizard has on hand to check with. A
region or size slug needs a provider token to know if it is real, and
the first run may not have one -- so the wizard cannot tell a wrong slug
from a right one, and the provider is left to reject it at `up`. A path
is one `os.Stat` away with nothing the first run does not already have,
so there is no reason to defer a check the wizard is fully equipped to
make.

It also matters more when it is wrong. A wrong slug is a provider
rejection: `up` fails once, with a message naming the field, and a
retry with the right value succeeds. A missing `instructions` path is a
hard error out of `config.InstructionFiles`, which fails every later
`up` and `session start` over a file the user could have been told
about here, with the question in front of them -- not a degraded mode
worth continuing into.

So this question checks that the path exists when it is answered.

### Presets never capture it

Like `sshKeys`. A project path such as `docs/agent-workflow.md` means
nothing stamped into another repository, and a personal one belongs to
the user rather than to the shape.

## What cloudlab writes

A managed block, so re-running never duplicates it and anything the user
added by hand survives:

```markdown
<!-- cloudlab:begin -->
# Working in a cloudlab session

You are a coding agent on an ephemeral cloud VM created by cloudlab.
Your checkout is at ~/sessions/<name>/<repo> on branch cloudlab/<name>.
Run `pwd` and `git branch --show-current` for the actual values.

- **Do not push anywhere.** You have no credentials, no GitHub access
  and no network git remote.
- **Commits are what survive.** Work returns to the user's machine via
  `cloudlab session pull` and `cloudlab session merge`, run from their
  end. Uncommitted changes are swept into a checkpoint commit, so commit
  deliberately rather than relying on that.
- **Anything untracked gets committed** by that checkpoint (`git add
  -A`). Leave no scratch files, archives or databases in the working
  tree.
- The VM is destroyed on `cloudlab down`. Nothing outside the repository
  survives.

## What this session is for

1. If `../TASK.md` exists beside your checkout, read it -- that is what
   you were started for.
2. Otherwise, if the repository uses beads, check `bd list --status
   in_progress`, then `bd ready`.
3. Otherwise, ask before starting work.

The repository's own AGENTS.md, CLAUDE.md or CONTRIBUTING.md still apply
and win wherever they disagree with this file.

<!-- user `instructions` files follow, verbatim -->
<!-- cloudlab:end -->
```

The closing precedence line exists because global files load *before*
project ones. Without it cloudlab's generic advice reads as outranking
the repository's actual conventions, which it must not.

## Session context

`session start` gains two optional, mutually exclusive flags:

```bash
cloudlab session start ship-context --task "Implement bv5; spec in docs/..."
cloudlab session start ship-context --issue cloudlab-bv5
```

Either writes `~/sessions/<name>/TASK.md`, a sibling of the checkout.
Passing both is an error naming both flags, rather than a precedence
rule nobody would remember.

`--task` writes its text verbatim. `--issue` writes the ID and the one
command that expands it -- `bd show <id>` -- rather than a copy of the
issue's body. A copy would be stale the moment anyone edited the issue,
and the agent has `bd` on the instance. `--issue` additionally marks the
issue in progress in the session's beads database.

### Why a file rather than beads alone

Beads is optional in cloudlab and must stay that way: `seedBeads`
returns early on `BeadsOff` and again on `ModeAbsent`. Routing task
context through beads alone would turn an optional dependency into a
required one.

The file works because **auto-loading is not required**. No harness
loads a file at this path, but the instruction block -- which is
guaranteed to load -- points at it, and the path is *relative to the
checkout*, so one generic instruction reaches a per-session file without
naming the session. Discovery rules never enter into it.

**A sibling of the checkout, not a directory inside it.** The obvious
placement is `.cloudlab/TASK.md` in the checkout, excluded the way
`/.beads/` is. `cloudlab-bv5` establishes the better location:
`~/sessions/<name>/` is where `checkpointCmd` never looks, so nothing
has to be excluded and nothing can be swept.

That difference matters because an exclusion can fail. `seedBeads`
already has to guard for it -- if `excludeBeadsCmd` errors it abandons
beads entirely rather than risk committing the database. A sibling
location removes that failure class instead of guarding against it. It
is an input to the session, so it does not need to come home.

Beads therefore enhances rather than gates. With it, the agent's status
updates and notes ride `refs/dolt/data` home on pull. Without it, the
task still arrives. `--issue` against a repository with no beads warns
and still writes the file.

### With neither flag

- No `TASK.md` is written; no placeholder.
- Any existing one is left alone. Re-running `session start` is a
  retry -- `StartSession`'s own comment requires every step to be
  re-runnable -- so a retry must not wipe the task given the first time.
  A new `--task` overwrites; nothing preserves. This matches
  `trackSession`, which already leaves an existing worktree alone.
- No warning. The flag is optional and starting a session before
  deciding what it is for is legitimate; warnings here are for things
  that went wrong.

The agent's fallback is ordered and total, so the outcome is defined in
every combination. The case that never occurs is the agent inventing a
task: step 3 is ask, not guess.

## Components

| Unit | Responsibility |
|---|---|
| `internal/config` | the `instructions` field, registered across `fieldOrder`/`fieldKinds`/`fieldValue`/`KnownField`; resolve paths relative to the declaring file |
| `internal/wizard` | one personal question, with an existence check |
| `internal/agentcontext` | render cloudlab's block, concatenate user files, and the managed-block splice. No network, no SSH. |
| `internal/agentcontext`'s harness table | `agents` entry to global path; the cursor gap |
| `internal/reconcile` | write the rendered block to each path on the instance, idempotent |
| `internal/lifecycle` | `TASK.md` beside the checkout, and the instructions write at session start |
| `cmd/run_session.go` | `--task` and `--issue` |

### Instructions are written at both `reconcile` and session start

One idempotent function, two call sites. Not a duplicated
responsibility -- the same write, run at both moments it matters.

`reconcile` alone is not enough. It runs on `up` and `provision`, while
`seedSession` only borrows `reconcile.Connect` for an SSH client. A user
who edits their workflow file and runs `session start` would get the
instructions as they stood at the last `up`, silently, with no way to
tell. That is the failure this design exists to prevent, reintroduced at
a different moment.

This departs from the beads precedent, which puts credential placement
in `reconcile` and leaves session commands to wire only the per-session
parts. The difference is rate of change: a credential is placed once and
is then correct, whereas `instructions` names files the user is actively
editing between sessions. The write is a few kilobytes over a connection
`seedSession` already holds, so running it again costs nothing worth
saving.

`TASK.md` is per-session by nature and belongs with `seedBeads`.

## Two rules the implementation must not lose

**Nothing is written into the checkout at all.** No creating
`AGENTS.md`, no appending to `CLAUDE.md`, no `skip-worktree`, and no
per-session file inside the working tree. The moment cloudlab writes
where `git add -A` can see it, every argument above stops holding.

**A harness cloudlab cannot reach is named, never skipped.** Today that
is cursor. The failure mode for silence is a user believing their
workflow reached an agent that never saw it.

## Testing

- Block rendering is a pure function of (session facts, user
  instructions): golden tests, including the empty-`instructions` case
  that must yield facts only.
- The managed-block splice: absent file creates it; present block is
  replaced not duplicated; content outside the markers survives. Table
  test over file contents, no SSH.
- Harness path mapping, including cursor yielding no path and a warning.
- `instructions` merge: base plus project concatenate in order, against
  a temp XDG dir; a missing path fails resolution with the path named.
- `TASK.md`: written with `--task` and with `--issue`; absent with
  neither; an existing one preserved on re-run and overwritten by a new
  `--task`. It lands beside the checkout, never inside it -- assert a
  checkpoint's `git add -A` cannot see it.
- `--issue` against a repository with no beads warns and still writes.
- Both flags together is an error naming both.
- Reconcile writing the same block twice is idempotent.
- `KnownField` round trip: a config declaring `instructions` survives
  `ReadValues` then `Render` unchanged. This is the regression test for
  the silent-drop above, and it fails if the field is added to the schema
  but not to the writer.
- The wizard question: asked on a first run and on `[change]`, absent
  otherwise; a path that does not exist is rejected in the loop; skipping
  writes no line; a preset saved from a run that answered it does not
  carry it.
- Session start rewrites the instructions: edit an `instructions` file
  between two starts and assert the second carries the new content.

## Out of scope

**Per-harness instruction variants.** One set of files reaches every
configured harness. A user running several writes neutral content. A
per-harness map is easy to add later and unjustified now.

**Delivering to cursor.** Needs a checkout write; revisit if cursor
gains a global file.

**Teaching the *local* agent to drive `session start`** -- that is
`cloudlab-ilr`, and this design works whether or not it ships.

**Creating beads issues from `session start`.** Writing a good issue is
thinking work, not transport. A `session start` that invented one from a
session name would produce exactly the placeholder issues that make a
tracker useless. The agent can file one on the instance if asked.

## Rejected alternatives

**Commit the workflow into each repository root.** Every repository ever
sessioned needs preparing, and cloudlab's content ends up inside the
user's repositories.

**Create or append to `AGENTS.md` at session start.** The tracked-file
problem above, with no safe resolution.

**`git update-index --skip-worktree` on a tracked entry point.**
Suppresses the commit by silently discarding legitimate edits to that
file.

**A file in the directory above the checkout.** Reaches claude and
opencode; invisible to codex, whose chain starts at the repository root.

**Reference cloudlab's file from `AGENTS.md` by a prose pointer.**
`AGENTS.md` has no import mechanism, so the pointer is prose an agent
may or may not act on. The requirement was a guarantee.

**Ship a default workflow when `instructions` is empty.** Reintroduces
the thing this design exists to remove.

**Route task context through beads alone.** Makes an optional dependency
required.

**An imperative `cloudlab instructions` command group.** `preset` and
`secrets` each have one because cloudlab owns a store -- named files
under XDG, sops blobs -- with something to enumerate. `instructions` is
one field naming files the user owns, so the commands would be a worse
`$EDITOR` and a worse `cloudlab init`. The question such a command would
answer -- what did the agent actually see -- is answered more truthfully
on the instance by `cloudlab ssh -- cat ~/.claude/CLAUDE.md` than by a
local render of what cloudlab would have sent, which is precisely the
thing that might differ. If it earns a place later, the shape is a single
subcommand-free `cloudlab instructions` printing the assembled block.

## As implemented (2026-09-15)

Built as designed. Five notes for anyone reading the code against this
document.

**`offerMove` excludes `instructions`.** Routing it to `Personal` in the
destinations table made it a member of `wizard.PersonalFields()`, and
`cmd/init.go`'s `offerMove` uses that list to offer lifting fields out of
a project file that predates the split, into `base.pkl`. That offer is
safe for every other personal field this design or the 2026-09-09 one
routes there, because those fields read the same regardless of which
file holds them. `instructions` does not: its value is a path, resolved
against the file that declares it, so `"docs/agent-workflow.md"` names
the repository root while the line sits in `cloudlab.pkl` and
`~/.config/cloudlab` once the same line sits in `base.pkl`. Accepting the
offer would silently repoint the field at a file that is not there, and
the next `up` or `session start` fails over it. This is the first field
for which the 2026-09-09 spec's "moving a field between the two files is
behaviour-preserving" is false, so the offer is now built from
`wizard.MovablePersonalFields()` -- `PersonalFields()` minus
`instructions` -- rather than from `PersonalFields()` itself, and derived
from it so the two lists cannot drift apart by hand-editing one.

**Remote paths are absolute, never `$HOME`-relative.** `reconcile
.Client.WriteFile` passes every path it is given through
`shellcmd.Quote`, and a quoted `$HOME` reaches the remote shell as a
literal four characters, not an expansion -- it would have created a
directory actually named `$HOME`. `agentcontext.Remote` takes the
instance's home as a plain string and joins paths under it instead, and
every caller passes `/home/<user>`, which `internal/lifecycle`'s
`gitpaths.go` already composes the same way and `remotepath.go`
documents as always exact: `cloud-init.sh` creates the remote user with
a plain `useradd --create-home` and no `--home-dir` override, so the
assumption costs nothing to rely on.

**`Splice` treats a begin marker with no end as a truncated block.** The
first implementation paired the first begin marker with the first end
marker found anywhere in the file, so a file already holding an orphaned
begin -- left by an interrupted write -- took the append path on the next
write, producing a second begin with no end of its own between the
orphan and the new block. The write after *that* one then paired the
orphan's begin with the new block's end and deleted everything between
them, including whatever the user had added in the meantime. `Splice`
now searches for the end marker only after the begin it is closing, and
treats a begin with nothing to close it as cloudlab's own interrupted
write -- never the user's, since nothing but cloudlab ever writes a
begin marker -- and discards wholesale from that marker to end of file.
This is not an edge case worth a comment and no more: `Splice` runs on
every `up`, every `provision` and every `session start`, so repeated
application against whatever the previous run left behind is the normal
case, not a retry path. For the same reason `Splice` recognises a marker
only when it stands alone on its own line: a Markdown instructions file
may quote either marker as prose -- this document quotes both, in order
-- and a substring search would take that quotation for a managed block,
splice cloudlab's block into the middle of the user's sentence and leave
the real block below it untouched, so the facts would never update
again. cloudlab always writes its own markers on lines of their own, so
nothing it wrote is missed.

**Dogfooding is deferred.** This repository does not declare
`instructions`, and a first attempt to make it do so was reverted.
Pointing cloudlab at its own 2026-09-14 workflow needs more than the one
field: `cloudlab.pkl` must also carry `region`, `size` and `template`,
because `resolveOrInit` falls back to the first-run wizard only on
`os.ErrNotExist` and hands back a validation failure for a file that
exists but is incomplete -- so a partial config turns `cloudlab up` in
this repository from a wizard into an error. It must also carry at least
one `agents` entry, and both of those are the repository author's
choices to make rather than something this design can settle.

The `agents` requirement is the part worth stating plainly, because it
is silent: `reconcile.WriteAgentContext` returns early when
`len(agents) == 0`, before it ever calls `config.InstructionFiles`. A
config that names `instructions` but no `agents` therefore delivers
nothing, with no warning and no error -- the field is accepted, merged
and resolved by nothing. That is exactly what made the first attempt
inert. The short-circuit is deliberate, since it saves a pkl run on the
common path, but anyone configuring `instructions` should know that
`agents` is what turns it on.

**One exported `reconcile.WriteAgentContext`, called from both
`Reconcile` and `seedSession`**, rather than the same write duplicated
into a second helper. `internal/lifecycle` already imports
`internal/reconcile` for `Connect`, so exporting the function adds no
new dependency edge between the packages. Duplicating it instead would
have meant copying the unreachable-harness warning text -- and the
`agents`-empty short-circuit that skips a pkl run on the common path --
into two places free to drift the next time either changed.
