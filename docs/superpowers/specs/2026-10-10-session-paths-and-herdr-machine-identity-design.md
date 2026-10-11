# Session paths and Herdr machine identity

## Summary

Bivouac should keep a session's remote checkout path shaped like the
repository's host identity instead of flattening it into one directory name,
and Herdr saved machines should represent bivouac instances rather than
individual sessions.

Today a session for `github.com/jskswamy/bivouac` named `auth` is checked out
on the instance under a flattened path like:

```text
~/sessions/auth/jskswamy-bivouac
```

and, when attached from inside Herdr, bivouac registers a separate saved Herdr
machine for each session. Two sessions on one instance therefore show up as two
machines:

```text
auth
 docs
```

The desired model is:

```text
~/sessions/auth/jskswamy/bivouac
```

and one Herdr saved machine per bivouac instance, with sessions nested beneath
it as ordinary Herdr workspaces:

```text
jskswamy-bivouac
  auth
  docs
```

The machine name provides the instance namespace, so session/workspace labels
should remain the simple bivouac session names.

## Current behavior

### Remote session paths

`internal/lifecycle/gitpaths.go` defines:

- `SessionDir(user, session)` as `/home/<user>/sessions/<session>`.
- `RemoteRepoPath(user, session, repo)` as
  `/home/<user>/sessions/<session>/<repo>`.

`cmd/run_session.go` passes the resolved instance name as `repoName` when
starting a session and records that same value in `state.Session.RepoName`.
For a repository whose instance name is `jskswamy-bivouac`, all later pull,
merge, delete, ssh, beads, and Herdr flows address:

```text
/home/<user>/sessions/<session>/jskswamy-bivouac
```

`state.Session.RepoName` is intentionally persisted because later commands may
run from a different directory or after an instance rename, so callers must use
the path identity that was chosen when the session was seeded.

### Herdr saved machines

`cmd/run_attach.go` routes `bivouac herdr` differently depending on whether it
is already running inside Herdr.

Outside Herdr, bivouac launches a Herdr remote client:

```text
herdr --remote ssh://<user>@<ip> --session <session>
```

Inside Herdr, bivouac calls `lifecycle.AttachMachine`, which currently:

1. Requires a resolved bivouac session.
2. Saves or reuses a Herdr machine matched by `target + session`.
3. Labels that machine with the session name, qualified only on sidebar label
   clashes.
4. Creates or reuses a workspace on that machine, also labeled with the session
   name and rooted at `RemoteRepoPath(user, session, repoName)`.
5. Records the saved Herdr machine id on `state.Session.HerdrMachineID` so
   teardown can remove exactly that profile later.

This makes the saved-machine profile itself session-scoped, even though all
sessions for one bivouac instance share the same SSH target.

## Desired behavior

### Remote path shape

New sessions should use a repository path made of separate owner and repository
segments:

```text
/home/<user>/sessions/<session>/<owner>/<repo>
```

For example:

```text
/home/subram/sessions/auth/jskswamy/bivouac
```

rather than:

```text
/home/subram/sessions/auth/jskswamy-bivouac
```

This path is for the session checkout only. The local worktree path remains:

```text
<local-repo>/.worktrees/<session>
```

### Herdr machine and workspace model

Inside Herdr, `bivouac herdr` should register or reuse one saved Herdr machine
per bivouac instance. The machine label should be the bivouac instance name,
for example:

```text
jskswamy-bivouac
```

Sessions should be represented underneath that machine as Herdr workspaces with
simple labels:

```text
auth
docs
```

The machine supplies the instance namespace, so session labels should not be
qualified with the instance name in the normal case.

For an instance `jskswamy-bivouac` with sessions `auth` and `docs`, the desired
shape is:

```text
Herdr machine: jskswamy-bivouac
  workspace: auth  cwd ~/sessions/auth/jskswamy/bivouac
  workspace: docs  cwd ~/sessions/docs/jskswamy/bivouac
```

not:

```text
Herdr machine: auth
Herdr machine: docs
```

## Design

### Persist a path identity separate from the display instance name

The existing `state.Session.RepoName` already persists the remote checkout
identity chosen at session creation. Keep that property: every later operation
must continue to use the session's recorded path identity rather than
re-deriving it from the current working directory.

The stored value, however, should be able to contain a relative path such as:

```text
jskswamy/bivouac
```

rather than only a single path segment such as:

```text
jskswamy-bivouac
```

`RemoteRepoPath` should continue to be the single function that joins the
remote session directory and this recorded path identity. Its documentation and
tests should make clear that the `repo` argument is a slash-separated relative
repository path, not necessarily one directory name.

The implementation must validate or construct this value so it cannot escape
the session directory. It must not accept absolute paths, `..` components, or
empty path segments.

### Derive the nested repository path at session start

`bivouac session start` is the only moment when the remote checkout path for a
session is chosen. It should derive a nested repository path from the local
repository identity and record it in `state.Session.RepoName` before any remote
work begins.

For common git origins, the path should preserve the final two repository
identity segments:

| origin/path | session repo path |
| --- | --- |
| `git@github.com:jskswamy/bivouac.git` | `jskswamy/bivouac` |
| `https://github.com/jskswamy/bivouac.git` | `jskswamy/bivouac` |
| no origin, local repo folder `bivouac` | `bivouac` |

The existing instance name derivation can stay flattened, because provider VM
names and bivouac instance names still need a safe single-token name. The new
session path derivation is only for remote filesystem layout.

### Preserve old sessions

Existing state files may contain sessions whose `RepoName` is a flattened value
such as `jskswamy-bivouac`, and sessions predating `RepoName` may have an empty
value.

Do not migrate them in place. Existing sessions should keep using the path they
were seeded under:

- If `RepoName` is set, use it exactly as recorded after validating it is a
  safe relative path.
- If `RepoName` is empty, keep the existing fallback to the instance name.

This avoids breaking active sessions whose remote repositories already exist at
the old flattened path.

### Make Herdr saved machines instance-scoped

Change saved-machine lookup from `target + session` to the instance-scoped
machine identity. The desired match should be the SSH target for the instance,
with bivouac-owned disambiguation using recorded state where needed.

The saved machine label should be the instance name. `MachineLabel` should take
or return the instance name, not the session name.

`machine add` should no longer pass `--remote-session <session>` for the saved
machine profile. The profile should represent the instance generally. Session
selection then happens through workspaces and remote Herdr commands routed to
that machine.

Workspace creation remains session-scoped:

- workspace label: `<session>`
- workspace cwd: `RemoteRepoPath(user, session, session.RepoNameOr(record.Name))`

### Revisit Herdr cleanup ownership

Today `state.Session.HerdrMachineID` records a machine profile id per session,
and session teardown removes that profile. With one machine per instance, a
session teardown must not remove the shared machine while other sessions still
use it.

The state model should change so the Herdr machine id is instance-scoped rather
than session-scoped. A suitable shape is a new field on `state.Record`, for
example:

```go
HerdrMachineID string `json:"herdr_machine_id,omitempty"`
```

Cleanup should then become:

- Session merge/delete: stop/delete the remote Herdr session or workspace for
  that session if bivouac created one, but do not remove the saved machine
  unless no sessions remain and the product decision is to hide idle machines.
- Instance down: remove the instance-scoped saved machine profile locally,
  because the VM is being destroyed.

If Herdr has no API to remove just a workspace safely, session teardown may
leave the workspace behind until the instance is destroyed. That is preferable
to deleting a shared saved machine that still represents other live sessions.

### Outside-Herdr behavior

Outside Herdr, `bivouac herdr` may continue to launch:

```text
herdr --remote ssh://<user>@<ip> --session <session>
```

That preserves the current reconnect behavior for a direct Herdr client. The
instance-scoped saved-machine change applies to the inside-Herdr attach path,
where the Herdr sidebar is available and machine/workspace nesting matters.

If later Herdr supports selecting/focusing nested workspaces directly from the
outside client path, that can be a separate change.

## Backward compatibility

- Existing active sessions keep their recorded `RepoName` and therefore keep
  using their existing remote paths.
- Existing sessions without `RepoName` keep falling back to the instance name,
  as today.
- Existing state may contain per-session `HerdrMachineID`. The new code should
  treat it as legacy cleanup data only. On session teardown, removing that
  legacy profile is still safe because it was created under the old
  one-profile-per-session model.
- New inside-Herdr attaches should record the instance-scoped machine id on the
  instance record, not on the session.

## Testing strategy

Add or update tests before implementation.

Remote path tests:

- `RemoteRepoPath` accepts a safe nested repo path and returns
  `/home/<user>/sessions/<session>/<owner>/<repo>`.
- Path derivation from HTTPS and SSH origins returns `owner/repo`.
- Local/no-origin repositories still get a safe single-segment fallback.
- Unsafe path identities such as absolute paths, `..`, and empty segments are
  rejected before any SSH command is built.
- Existing session fallback behavior still uses the instance name when
  `RepoName` is empty.

Session start tests:

- `runSessionStart` records `RepoName: "owner/repo"` for a GitHub-style
  origin while continuing to resolve the instance name as `owner-repo`.
- Start output and later helper calls use the nested path.

Herdr machine tests:

- `EnsureMachine` matches/reuses a machine by instance target rather than by
  session.
- `machine add` for the shared saved machine omits `--remote-session`.
- The saved machine label is the instance name.
- Attaching `auth` and then `docs` on the same instance reuses the same machine
  id and creates/focuses separate workspaces labeled `auth` and `docs`.
- Session teardown does not remove the shared machine while other sessions
  remain.
- Instance `down` removes the shared machine profile.
- Legacy per-session `HerdrMachineID` is still cleaned up for old sessions.

Integration-style tests:

- Pull, merge, delete, ssh default directory, beads sync, and Herdr workspace
  setup all address the same recorded nested remote path.

## Non-goals

- Do not rename existing remote session directories in place.
- Do not change local worktree layout.
- Do not change bivouac instance naming rules or provider VM naming rules.
- Do not introduce multi-repository sessions beyond making the remote path
  layout compatible with an owner/repo directory structure.
- Do not require Herdr saved machines for non-Herdr usage; direct remote client
  usage can keep its current behavior.
