# Multiple Sessions Per Instance

## Status

Accepted

## Context

Stage 1 of the git-aware sync design carried one session at a time. That was
a delivery boundary rather than a design limit: nothing in the transport
assumes a single agent, and an instance sized for one agent is usually sized
for two.

Isolation is already in place. Every session gets its own clone at
`~/sessions/<name>/<repo>`, its own branch `cloudlab/<name>`, its own local
worktree at `.worktrees/<name>`, and its own remote `cloudlab-<name>`. Two
agents cannot corrupt each other's git state because they share none of it.

What several sessions require is a state record that can name them all. A
record describing one session as a flat name, repository and base cannot
describe two, and `down` learns what to rescue from the record alone -- so it
would destroy a droplet after rescuing only what it could see. The record has
to hold a collection before `start` can append to one.

The second problem is ergonomic. Naming a session on every `pull`, `merge` and
`delete` is ceremony while only one can exist; with several it becomes a
question the command should answer for itself.

## Decisions

1. **A session's state travels as one struct.** `Session{Name, LocalRepo,
   Base}` rather than parallel arrays. A base paired with the wrong session is a
   silently wrong replay range, which is the class of bug `cloudlab-j9c` was.

2. **The working directory selects the session.** Standing in
   `.worktrees/<name>` is how you work on a session, so it is also how you name
   one. No stored pointer, nothing to set, nothing to go stale and misdirect a
   merge. This mirrors how the instance name is already derived from the cwd.

3. **No `session use`.** Considered and dropped. It cannot change the parent
   shell's directory, so it would have to spawn a subshell or ship per-shell
   integration -- machinery for a `cd` the user performs anyway.

4. **Ambiguity resolves differently for interactive and scriptable commands.**
   Cloudlab never guesses which session is meant, but what it does instead
   depends on the command:

   - `ssh`, `tmux` and `herdr` already hand the terminal over -- they are
     interactive by definition -- so they offer a picker. Asking costs nothing
     when the next thing that happens is an interactive session anyway.
   - `pull`, `merge` and `delete` are scriptable and must stay so. They error,
     list the sessions, and say to `cd` into one. A picker here would block an
     unattended run waiting for input, which is precisely the workflow cloudlab
     exists to serve.

   A picker requires a TTY on stdin. Without one -- a script, CI, or an agent
   driving cloudlab -- even the interactive commands fall back to the error.

5. **`delete` discards; `merge` accepts.** They are the two ways a session ends.
   `delete` is the only destructive verb here, so it refuses unless the work is
   already on the user's branch.

6. **`down` is all-or-nothing.** It rescues every session and refuses if any
   rescue fails. A partial destroy is the failure this whole design exists to
   prevent.

### Rejected alternatives

**An `Active` session pointer in the record.** Adds state that can point at a
merged or deleted session, and a stale pointer redirecting a merge is a data
hazard. The cwd cannot go stale.

**Parallel arrays (`Sessions []string`, `Bases []string`).** Lets indices drift
apart silently.

**A shared bare store to make N sessions cheap on disk.** Rejected: it costs a
second instance-side path, a seeded base branch, and the "bare HEAD names a
branch nothing pushed" failure class. Sessions stay independent clones; see
Costs.

## Architecture

```
   Mac                                    Instance
   ───                                    ────────
   <repo>/                     push  ──▶  ~/sessions/auth/<repo>   (clone)
     .worktrees/auth   ◀── fetch ──────   ~/sessions/docs/<repo>   (clone)
     .worktrees/docs   ◀── fetch ──────   ~/sessions/perf/<repo>   (clone)
     .worktrees/perf   ◀── fetch ──────

   state.json: one Record per instance, holding []Session
```

Each column is independent: separate clone, separate branch, separate worktree,
separate remote, separate recorded base.

## Components

### State

```go
type Session struct {
    Name      string `json:"name"`
    LocalRepo string `json:"local_repo"`
    Base      string `json:"base"`
}

type Record struct {
    ...
    Sessions []Session `json:"sessions"`
}
```

A slice on the record from the outset, so there is no flat-field shape to
migrate away from later.

A slice rather than a map -- `list` wants a stable order, and there will never
be enough sessions for lookup cost to matter. A `find(name)` helper covers
lookup.

### Session resolution

One resolver, used by every session-aware command. Rules 1-3 are shared; only
what happens when they all miss differs:

```
1. explicit argument
2. cwd inside .worktrees/<name>  → that session
3. exactly one session exists    → that one
4. ambiguous:
     pull / merge / delete  → error, listing the sessions, with the cd tip
     ssh / tmux / herdr     → picker if stdin is a TTY, else the same error
```

Cwd beats "exactly one" deliberately: if you are standing in a worktree, that is
what you mean, even in the single-session case.

The split at rule 4 is the only place the commands differ, so it belongs to the
caller rather than the resolver. The resolver reports "ambiguous, here are the
candidates"; each command decides whether to prompt or refuse. That keeps the
picker out of the code path an unattended run takes.

Detection reads git rather than parsing paths: inside a linked worktree,
`git rev-parse --git-common-dir` gives the main repository (absolute) and
`git branch --show-current` gives `cloudlab/<name>`. A path-prefix match on
`.worktrees/` would break for a worktree moved or created by hand.

### `RepoRoot` correction

`identity.RepoRoot` uses `git rev-parse --show-toplevel`, which inside a linked
worktree returns *the worktree*, not the main repository. So a session command
run from inside a worktree resolves `localRepo` to the worktree and would replay
a session onto itself.

This is a live bug independent of this feature -- nothing stops a user cd-ing
there today -- and lands as its own commit. The fix is `--git-common-dir`, whose
parent is the main working tree.

### Commands

| Command | Behaviour |
|---|---|
| `session list` | Every session across every instance: name, instance, branch, commits not yet on the user's branch, local worktree state. Read-only and offline -- it does **not** fetch or contact any instance, so counts are as of the last `pull`. Listing must stay fast and must work with an instance that is down. |
| `session delete [name]` | Discards a session: instance directory, local worktree, branch, remote, record entry. Refuses unless the session has no unmerged work. `--force` overrides. |
| `status` | Gains a Sessions section listing this instance's sessions. |

`session start` stops refusing a second session.

### Connection commands are session-aware, not duplicated

`ssh`, `tmux` and `herdr` connect to the instance. With one session that was
unambiguous; with several, connecting has to answer *which session's directory*.

They stay where they are rather than gaining `session ssh` / `session tmux` /
`session herdr` twins. Each resolves a session by the same four rules and lands
in `RemoteRepoPath(user, session, repo)`.

This subsumes `cloudlab-h9d`: `ssh` currently targets `record.RepoPath`, a
mirror of the local checkout path that rsync used to create and nothing creates
now, so it silently lands in `$HOME`. Resolving a session gives it a directory
that exists.

Ambiguity here offers a picker rather than an error. These commands are about to
hand over the terminal, so a prompt is continuous with what the user asked for,
and it removes the last case where a session name has to be typed. `bubbletea`
is already a direct dependency, so this adds none.

The picker only appears with a TTY on stdin and more than one session. With
exactly one it connects without asking; with none it lands in the home
directory. A script, CI job or agent gets the same error the scriptable
commands give -- never a prompt it cannot answer, and never a hang.

`tmux` names its tmux session after the cloudlab session when one resolves, so
reconnecting lands in the same place. Its existing `[session-name]` positional
keeps overriding that.

The herdr workspace half is `cloudlab-7y1` and stays there; this spec only fixes
which directory a connection starts in.

### `down`

Loops every session in the record, rescuing each. Refuses to destroy if any
rescue fails, naming the session that failed. `--force` skips rescue entirely,
as today.

## Data flow

Starting a second session on a live instance:

```
session start docs
  → append Session{docs, <repo>, <HEAD>} to record.Sessions
  → seed ~/sessions/docs/<repo>, set identity, push, checkout
  → register remote cloudlab-docs, create .worktrees/docs
```

Merging one of several:

```
cd .worktrees/docs
session merge
  → resolution: cwd is a worktree → session "docs"
  → localRepo from --git-common-dir → the MAIN repo, not the worktree
  → base from Sessions[docs].Base
  → rescue, replay signed onto the main checkout, verify, delete
  → remove the docs entry from record.Sessions
```

## Error handling

| Situation | Response |
|---|---|
| Repo root, several sessions, no argument (`pull`/`merge`/`delete`) | List them; "cd .worktrees/<name> to work on one, or name it explicitly" |
| Repo root, several sessions, no argument (`ssh`/`tmux`/`herdr`) | Picker, when stdin is a TTY; otherwise the same error |
| Picker requested without a TTY | Fall back to the error -- never prompt where nothing can answer |
| Named session does not exist | Error naming it, list what does exist |
| `delete` on a session whose work is not on the branch | Refuse, naming the unmerged commit count, suggest `merge` or `--force`. "Unmerged" is measured with `git cherry <branch> <session ref>`, not a plain commit count: after a merge the session's commits exist under different SHAs, and only patch-id equivalence sees that they landed |
| `delete` where the instance is unreachable | Remove the local half -- worktree, branch, remote, record entry -- and warn that the instance directory was left behind. A dead instance must not make a session permanently undeletable |
| `down` where one rescue of several fails | Refuse to destroy; name the failing session; instance untouched |
| Session's base no longer an ancestor | Existing `cloudlab-j9c` behaviour, now per-session |

## Testing

- Resolution: each of the four rules, including cwd winning over
  exactly-one, exercised against real worktrees rather than fabricated paths
- `RepoRoot` from inside a linked worktree returns the main repository
- Two sessions on one instance: independent start, pull, merge; merging one
  leaves the other intact and still in the record
- `down` with two sessions where the second rescue fails destroys nothing
- `delete` refuses while work is unmerged and succeeds once it has landed
- `list` output with zero, one and several sessions across instances
- The picker appears only for interactive commands, only with several
  sessions, and only with a TTY -- asserted by driving the resolution with
  a non-TTY stdin and checking an error comes back rather than a prompt

## Costs

**Each session is a full clone.** Three sessions on a droplet is three copies of
the repository. This is the price of dropping the shared bare store and is the
practical ceiling on how many sessions to run at once -- not a limit cloudlab
enforces, but one worth knowing before starting five on a 2 GB box.

**CPU and RAM are shared.** Two agents building concurrently on 2 vCPU contend.

**Docker is shared.** One daemon per instance, so two agents binding the same
port or container name will collide. The only genuinely shared mutable resource.

**Merges are order-dependent.** Two sessions branched from the same base that
touch the same files will conflict on the second merge. Ordinary git, handled by
the existing conflict path, but new: with one session it could not happen.

## Out of scope

- Locking between concurrent merges. One person, one terminal.
- Disk accounting or a session cap.
- Multi-repo sessions. A session is still one repository.
- `session use` / shell integration. See Decisions.
