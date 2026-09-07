# Git-Aware Sync: Sessions, Worktrees, and Signed Integration

## Status

Accepted

## Context

cloudlab began as a way to move heavy work off the laptop: an ephemeral VM
per git repo, the repo seeded by rsync, kept live by a continuous two-way
Mutagen session. The mental model was "your Mac is the workbench, the
droplet is a pair of hands" -- `cloudlab shell` hands you a *local* subshell
with `DOCKER_HOST` pointing at the instance, which says plainly where the
work was expected to happen.

That is no longer what cloudlab is for. Claude Code, Codex, Cursor, Copilot
and pi are installed on every instance; mosh, Moshi phone pairing, a
persistent herdr server and Tailscale all exist so a session survives your
laptop closing. cloudlab has become **a place where agents work and you
supervise**, reachable from whatever device you happen to hold.

The current sync design has no answer for that. `.git` is excluded from both
the rsync seed and the Mutagen session, so an agent on the instance can edit
files but its commits reach nothing. This is not hypothetical: a commit
authored by an agent on an instance sat there unreachable, and was
recovered only because a *stale* Mutagen session -- created before
the exclusion landed and therefore still syncing `.git` -- happened to carry
it back. The next `cloudlab up` or `cloudlab watch` would have created a
correctly-filtered session and that path would have closed silently.

Syncing `.git` is not the fix. Mutagen's own documentation is explicit that
VCS directories "can be synchronized... but they shouldn't be", and that VCS
commands should run on one side only. The reasons are the index being a
machine-keyed stat cache, the object store being mutable and gc-managed, git
not supporting concurrent `.git` modification, and -- the one that decides
it -- `.git/hooks/` being executable code, making a synced `.git` a remote
code execution path from the instance onto your Mac. Mutagen calls ignoring
VCS directories "mandatory, not optional". Their recommended model assumes a
human on the master and a dumb runtime on the slave; cloudlab inverts that,
which is why this needed designing rather than looking up.

If git is the thing that moves code, the unit that crosses the boundary is
the commit -- which is also the unit of supervision.

## Decisions

1. **The Mac fetches from the instance. The instance never pushes.**
   Every cloudlab operation is already Mac-initiated over SSH; the reverse
   would need sshd on the Mac, the Mac awake and on the tailnet, and a key
   on a disposable VM. Pull-direction also means an agent *cannot* push
   anywhere, enforcing "no unreviewed pushes" structurally rather than by
   policy. Critically, it is the only direction that works for the primary
   workflow: an agent running overnight against a closed laptop.

2. **The instance never talks to a shared remote.** The Mac is the hub, the
   instance a spoke. Repos are pushed out from the Mac and fetched back to
   it. No GitHub credentials, host keys, or signing keys ever reach the VM.

3. **A session is the unit of work.** Named by the user (`auth-refactor`),
   branch `cloudlab/<session>`, one ordinary repository per declared repo on
   the instance and a matching worktree inside the user's own repo on the
   Mac. One session per instance for now; see Architecture for why the
   instance side is a plain repository rather than a worktree of a shared
   bare store.

4. **Sessions are merged once, then deleted.** Because a session does not
   outlive its merge, no long-lived branch diverges from the re-signed
   history on the Mac. This removes the need for a marker ref, an
   instance-reset step, and any reasoning about a "second merge".

5. **Uncommitted work is rescued by checkpointing, not by a second
   transport.** Before any fetch, cloudlab commits outstanding changes on
   the instance. The invariant is: *anything on an instance is committed,
   ignored, or explicitly not wanted.*

6. **Integration replays and signs; it does not squash.**
   `git cherry-pick --empty=drop -S` preserves each atomic commit and signs
   it individually. Squashing would destroy the atomicity this project
   enforces elsewhere (see the `/commit` workflow). Not `rebase`: on a ref
   that already descends from the target branch, `rebase --onto` reports
   "up to date", re-signs nothing, and detaches HEAD while the branch never
   moves -- appearing to succeed.

7. **Mutagen is removed entirely.** Its only job was continuously syncing
   the repo, which git now owns. This deletes the `watch`/`watchstatus`
   subsystem, the mutagen dependency, the ignore-rule duplication between
   rsync and mutagen, and the `.git`-exclusion hazard.

8. **rsync is retained for non-git data only.** `sync` and `download` remain
   the transport for datasets, weights, fixtures and results -- content that
   is deliberately not in git. rsync leaves the critical path of `up`.

9. **`down` rescues before destroying, and refuses if it cannot.**

### Rejected alternatives

**Continue syncing `.git`.** It works today and costs nothing to keep, which
made it genuinely tempting. Rejected on the hooks RCE path, the auto-gc
object-pruning risk, permanent index churn, and 562 MB scans against a
filtered 17 MB. Contradicts Mutagen's explicit guidance.

**Dual channel (Mutagen for the working tree, git for commits).** The same
edits arrive twice -- once as loose files, once inside a commit -- leaving
the Mac's worktree dirty with byte-identical content and `git merge`
refusing. `git reset --hard` is safe only *because* it is identical, an
assumption that fails silently the day it is not.

**One-way replica Mac to instance, git back.** Unsafe: one-way replica
forces the instance to match the Mac, so an agent's uncommitted edits are
overwritten on the next scan. It survives only if the agent commits faster
than Mutagen scans.

**Signing on the instance.** Requires either a private signing key at rest
on a disposable box, or agent forwarding -- which exists only while you are
connected and therefore fails during the overnight workflow that motivates
the design. A leaked signing key is worse than a leaked access token:
revocation does not invalidate forgeries already pushed. Signing at
integration is also semantically correct, since the badge asserts that a
human vouches for the code.

**A signed merge commit instead of rebasing.** Preserves atomicity with one
signature, but the atomic commits land as unsigned ancestors, and GitHub's
"require signed commits" rule checks every commit in a push.

**Force-pushing re-signed commits back to the instance.** Would eliminate
divergence, but rewrites a branch under a running agent. Made unnecessary by
decision 4.

## Architecture

```
   Mac (hub)                                 Instance (spoke)
   ─────────                                 ────────────────
   working repo           push at start ──▶  ~/sessions/<session>/<repo>
     any branch, signed                        ordinary repo, checked out
                                               on branch cloudlab/<session>
   <repo>/.worktrees/<session>
     local worktree       ◀── fetch ────────

   rsync ────────── datasets / results ──────▶
```

Both git directions are Mac-initiated. Nothing on the instance is configured
with a remote pointing anywhere except itself, so an agent cannot push to a
shared remote even if it tries -- there is nowhere for it to push.

**One session, one ordinary repository.** The agent's repository is not a
worktree of a shared bare store. That alternative moves fewer objects when a
second session starts, but it costs a second instance-side path, a seeded
base branch for worktrees to start from, and a whole failure class: `git init
--bare` leaves HEAD on a branch a push never moves, so `worktree add` without
an explicit start point resolves a reference that does not exist. Sessions are
one per instance today; revisit if several ever run side by side.

**Seeding order is load-bearing: init, push, then checkout.** git refuses a
push to a checked-out branch. A fresh `git init` leaves HEAD on an unborn
branch, so the session branch is not yet current and the push is legal; the
checkout afterwards gives the agent its files. Reversing the last two steps
breaks every seed, and the workaround for that -- setting
`receive.denyCurrentBranch=updateInstead` -- is exactly what this ordering
avoids needing.

**The remote prefers the tailnet address.** `session start` resolves the
instance's Tailscale IPv4 and uses it in the remote URL, falling back to the
public address when Tailscale is absent or logged out. Git traffic then does
not cross the public internet, and the remote survives a reboot that changes
the public IP.

**The local worktree lives inside the repository**, at
`<repo>/.worktrees/<session>`, not under the home directory. Sandboxing tools
scope themselves to the project directory and cannot reach a worktree created
outside it; a worktree the agent's sandbox cannot see is one it cannot use.
`session start` adds `/.worktrees/` to `.git/info/exclude` -- git's local
ignore file, not the user's committed `.gitignore` -- because otherwise the
worktree reads as untracked content and every cleanliness check in `merge`
fails forever.

## Components

### Config (`cloudlab.pkl`)

A new `repos` listing declares additional repositories beyond the one
cloudlab's identity is derived from, for microservice and multi-repo work:

```pkl
repos {
  "../service-a"
  "../service-b"
}
```

Entries are paths relative to the primary repo root, resolved on the Mac.
Sibling checkouts are the common layout for multi-repo applications. Merged
additively from the personal base config, like `packages` and `agents`.

### Session management

- `cloudlab session start <name>` -- creates the repository per repo on the
  instance on branch `cloudlab/<name>`, and the matching local worktree at
  `<repo>/.worktrees/<name>`. The local worktree is created **here and only
  here**; `pull` updates it but never creates it, so a session always has
  somewhere to land before any work exists.
- `cloudlab up` does **not** create a session implicitly. An unnamed default
  is how the design ends up back at a single long-lived branch named after
  the instance rather than the work. The cost is one extra command in the
  simplest case, which is acceptable given a session is always started with
  intent.
- `cloudlab session list` -- sessions, their repos, unfetched commit counts,
  and whether the instance worktree is dirty.
- `cloudlab session` state is derived from git on both sides, not stored
  separately, so it cannot drift.

### `cloudlab session pull [session]`

Checkpoint on the instance, fetch every repo's session branch, and update
the local worktree at `<repo>/.worktrees/<session>`. Idempotent,
non-destructive, safe against a dirty local tree, and safe to run repeatedly
while the agent is still working.

### `cloudlab session merge [session]`

Implies a fresh `pull`, then per repo: `git cherry-pick --empty=drop -S` the
session branch onto the current local branch, verify every resulting commit
is signed, then delete the session from both machines. Refuses on a dirty
local tree, and on a dirty local session worktree -- `merge` deletes that
worktree, so it must not do so over uncommitted edits.

Re-runnable. A merge that fails after the replay -- during cleanup, or via
the conflict-resolution path its own error message recommends -- must be
fixable by running the same command again. Two things make that work:
`--empty=drop` skips commits already applied (cherry-pick has no patch-id
dedup; that is a rebase behaviour), and the signature gate reads HEAD after
the replay rather than trusting a commit count, since `rev-list --count` has
no patch-id awareness either and still counts commits that already landed.
An unmoved HEAD after a replay means everything was already applied, which is
success, not the empty range the gate otherwise refuses.

### `cloudlab down`

Iterates every session on the instance: checkpoint, fetch, verify. Destroys
only once all work is proven present on the Mac.

### Removed

`internal/lifecycle/watch.go`, `watchstatus.go`, the `watch` command, the
Mutagen dependency and its ignore-translation helpers.

### Retained

`internal/lifecycle/rsync.go` for `sync` and `download`, including the
rsync-version check (macOS ships openrsync, which supports neither
`--info=progress2` nor `--mkpath`).

## Data Flow

**Starting work**

1. `cloudlab up` provisions the instance and, for each declared repo, pushes
   the Mac's current commit into a fresh repo on the instance.
2. `cloudlab session start auth-refactor` creates `cloudlab/auth-refactor`
   and a worktree per repo, on both sides.
3. The agent is started in a herdr session pointed at
   `~/sessions/auth-refactor/<repo>`.

Design documents ride along inside the repos, so no separate channel is
needed to give an agent its instructions.

**While work happens**

The agent commits atomically to `cloudlab/auth-refactor`. Nothing requires
the Mac to be awake, reachable, or open. Progress is observable from a phone
through Moshi against the same herdr session.

**Bringing work back**

4. `cloudlab session pull auth-refactor` -- checkpoint, fetch, update local worktree.
5. Review: `git log main..cloudlab/auth-refactor`, or run the code in the
   local worktree.
6. `cloudlab session merge auth-refactor` -- rebase and sign, verify, delete the
   session on both sides.
7. `git push` -- yours alone, always.

**Afterwards**

The instance is either reused for a new session, branching from the
now-merged `main`, or destroyed. It never holds stale history, because
sessions do not outlive their merge.

## Error Handling

**Verification is the gate, not exit codes.** A successful `git fetch` is
not proof. Before anything destructive -- deleting a worktree, destroying an
instance -- cloudlab enumerates the instance's refs with `git ls-remote` and
confirms each object exists locally with `git cat-file -e`. Only that check
authorises deletion.

**Partial failure is the normal failure** once multiple repos and sessions
are involved. Every operation reports per repo and per session. A failure in
one repo does not abort the others, but any unrescued work aborts a destroy.

**Rescue is idempotent.** Checkpoint is a no-op on a clean tree; fetch moves
only missing objects. Retrying after fixing one problem is always safe,
which matters because retrying is the natural response to the error message.

**Merge conflicts** stop that repo and leave it in git's normal conflicted
state. Other repos still merge. Re-running is safe, but *not* via patch-id
dedup: that is a `rebase` behaviour and `cherry-pick` does not do it. Replaying
an already-applied range with a plain cherry-pick stops at the first commit
with "The previous cherry-pick is now empty" and leaves the repository
mid-cherry-pick. What makes the re-run work is `--empty=drop`, which skips the
commits that already landed, plus an explicit empty-range guard
(`git rev-list --count`) so a session with nothing new never reaches
cherry-pick at all -- an empty range exits 128. Both are load-bearing, so both
need an explicit test.

**Signature verification is bounded by a SHA, not a branch name.** The
cherry-pick advances the checked-out branch, so `<branch>..HEAD` afterwards is
empty and would verify nothing while reporting success. The pre-replay
`git rev-parse HEAD` is captured and used instead, and an empty range is
itself treated as a verification failure -- a check that inspected nothing
must never be mistaken for one that passed, because deletion is downstream
of it.

**The window between the verified fetch and the delete is re-checked.** The
agent keeps running while the replay happens, and signing can block for
minutes on a hardware key touch. Before removing anything, `merge`
re-checkpoints on the instance and re-reads the session tip; if it moved,
nothing is deleted and the user re-runs `merge` to take the new work too.

**OPEN QUESTION -- git's own transport binaries on the instance.** Every
command cloudlab issues over SSH is wrapped in `bash -lc`, because git lives
in `~/.nix-profile/bin` and a non-login shell cannot find it. But git's smart
transport does not go through that wrapper: it runs `git-receive-pack` /
`git-upload-pack` through sshd and the login shell's `-c` itself. cloud-init
sets the login shell to `/bin/bash`, and Ubuntu's skel `.bashrc` returns
immediately when non-interactive, so those two binaries may not be on PATH --
in which case push and fetch only work if the base image ships a system
`/usr/bin/git`. This has not been checked against a live instance and nothing
was changed speculatively. The check is one command:

```
ssh <instance> 'command -v git-receive-pack git-upload-pack'
```

If they are absent, the fix is `--upload-pack` / `--receive-pack` pointing at
a `bash -lc` wrapper. The same note is in `internal/lifecycle/gitlocal.go`
next to `sshGitURL`.

**An unreachable instance** cannot be checked. `down` must still work, since
a broken box must be destroyable, so it degrades to an explicit warning and
names `--force` in the message. Refusing to destroy a VM that is still being
billed is a worse failure than the one being prevented, so the escape hatch
is stated every time.

**`--force` is the only path that loses work**, by design, on `down` and on
session deletion.

## Testing

Following the established pattern: pure argv/script builders are unit
tested; anything touching a real instance is exercised against the fake SSH
server in `internal/lifecycle`.

- Argv builders for clone, worktree add/remove, checkpoint, fetch, rebase
  and verification, including paths containing spaces.
- Checkpoint is a no-op on a clean tree and produces exactly one commit on a
  dirty one; respects `.gitignore`.
- The verification gate fails when a ref is present remotely but its object
  is absent locally -- the case a fetch exit code would miss.
- `down` refuses with unfetched commits, refuses with uncommitted changes,
  proceeds when both are clean, and proceeds under `--force` regardless.
- `down` iterates every session, not just the first.
- Merge conflicts leave other repos merged and the conflicted repo
  recoverable; a re-run does not duplicate already-applied commits.
- Merge refuses on a dirty local tree.
- Rebase output is verified as signed, and an unsigned result is reported
  rather than silently accepted.
- Config: `repos` parses, merges additively base-then-project, and rejects
  paths that escape the repo root.

## Delivery

This spec is deliberately larger than one implementation plan. It should be
built in three stages, each independently shippable and each leaving
cloudlab in a working state.

**Stage 1 -- git replaces Mutagen.** One repo, one session at a time.
`session start` seeds the repository on the instance; `session pull` and
`session merge` (checkpoint, fetch, verify, rebase and sign, delete);
`down` rescues before destroying. Mutagen, `watch` and `watchstatus` are
removed. This stage alone closes the hole that strands an agent's commits
on the instance, and is the only stage that changes existing behaviour
rather than adding to it.

**Stage 2 -- concurrent sessions.** Multiple named sessions on one
instance: `session list`, `down` iterating every session, and the worktree
isolation that lets two agents run without contending. Stage 1's structures
already assume sessions, so this widens rather than reworks them.

**Stage 3 -- multi-repo.** The `repos` listing in `cloudlab.pkl`, and every
per-repo operation becoming a loop with per-repo reporting. Deferred last
because it multiplies the surface of stages 1 and 2 rather than introducing
new concepts, and because single-repo work is the common case.

Each stage gets its own implementation plan. Stage 1 is the one to write
first.

## Out of Scope

- **Signing on the instance.** Revisit only if per-commit verification
  during a session proves necessary; the path would follow the existing
  tailscale auth-key pattern (sops, tmpfs, wipe after use) with a dedicated
  revocable key, never a personal one.
- **Automatic background fetching.** Deleting `watch` deliberately removes
  the last daemon. `pull` and `status` are explicit.
- **Instances pushing to the Mac.** Superseded by decision 1.
- **Continuous sync for non-git directories.** `sync`/`download` remain
  one-shot.
- **Conflict resolution assistance.** git's own conflicted state is the
  interface.

## Notes for implementation

**Branch protection is the real guarantee.** Everything cloudlab does about
signing is local discipline, and local discipline fails on the day you are
tired. GitHub's "Require signed commits" branch protection makes the server
reject unsigned commits, so the guarantee stops depending on cloudlab, on a
hook, or on you. The docs should say so plainly rather than implying the
tooling is sufficient.

**`status` doing a network round-trip per repo** makes it slower and
fail-prone when the instance is down. Unfetched counts should either be
opt-in behind a flag or degrade gracefully to "unknown" when unreachable.

**`down` cost grows with sessions**: N checkpoint-fetch-verify round trips
rather than one. Correct, but not free, and worth reporting progress for.

**Checkpoint commits can capture secrets.** An agent writing a `.env` that
is not gitignored will have it committed and fetched to the Mac. It never
reaches a shared remote, but it will be in a branch's history before a
squash-merge. Worth a documented warning.
