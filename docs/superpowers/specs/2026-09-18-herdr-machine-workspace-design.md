# cloudlab herdr: `--machine` workspace/focus and configurable tabs

Status: implemented
Date: 2026-09-18
Builds on: `docs/superpowers/specs/2026-09-10-herdr-machines-design.md` (already
implemented: `EnsureMachine`, `CleanupHerdr`)
Requires: herdr 0.9.1 on the instance (pinned in `81c19c5`)

## The problem

herdr 0.9.1 adds `--machine <label>`: a global flag that routes any client
command to a saved machine's server instead of the local one, without an SSH
session. `AttachMachine` (`internal/lifecycle/herdrattach.go`) already saves
the machine via `EnsureMachine`, but still reaches the workspace it just
registered the slow way:

```go
client, err := reconcile.Connect(ctx, ip, user)
...
wsID, err := EnsureWorkspace(client, session, RemoteRepoPath(user, session, repoName), session)
...
err := FocusWorkspace(client, session, wsID)
```

`EnsureWorkspace`/`FocusWorkspace` (`internal/lifecycle/herdrworkspace.go`) shell
out over a fresh SSH connection to run `herdr workspace list/create/focus` on
the instance. `--machine` reaches the same server without opening that
connection at all, since the profile already carries the target.

This is scoped narrowly. Session start/stop, `EnsureMachine`, and
`CleanupHerdr`'s session teardown are unrelated to this change and are not
touched — confirmed with the user, who wanted the workspace/focus steps gained
without folding session lifecycle into `--machine` as well.

## What was verified

Run live against the `bivouac-rename` session's instance (`142.93.212.175`),
using the machine profile cloudlab already had saved for it.

| Command | Result |
|---|---|
| `herdr machine list --json` | Returns saved profiles: `id`, `label`, `target`, `session`, `enabled`, `selected` — same shape the 0.9.0 design doc already documented. |
| `herdr --machine bivouac-rename workspace list` | No `--json` flag on `workspace list` itself; returns `{"id":"cli:workspace:list","result":{"type":"workspace_list","workspaces":[...]}}` unconditionally. Routed to the instance's server, no SSH connection opened. |
| `herdr --machine bivouac-rename workspace create --cwd <path> --label test-ws --no-focus` | Returns `{"id":"cli:workspace:create","result":{"type":"workspace_created","workspace":{...},"tab":{...},"root_pane":{...}}}`. |
| `herdr --machine bivouac-rename workspace close <id>` | Returns `{"id":"cli:workspace:close","result":{"type":"ok"}}`. |

Two things this settles, both taken from herdr's own `--skill` documentation
rather than guessed:

1. **`--machine` and `--session` are mutually exclusive.** The saved profile
   already carries its recorded session; passing `--session` alongside
   `--machine` is not a supported combination.
2. **The capability gate is `herdr status`, not a version comparison.** herdr's
   own guidance is to check the running server's advertised capabilities
   before relying on a feature, not to hardcode "0.9.1 or newer" — a server
   that reports the capability is usable regardless of its version string.

## Design

### 1. Replace the SSH-exec'd workspace/focus steps

`AttachMachine` drops its `reconcile.Connect` call entirely. In its place,
`EnsureWorkspace`/`FocusWorkspace` gain `--machine`-routed equivalents that
run through the existing `herdrRunner` interface (`herdrprofile.go`) — the
same seam `EnsureMachine` already uses — instead of `remoteRunner`:

```go
func EnsureWorkspaceViaMachine(h herdrRunner, label, cwd, wsLabel string) (string, error) {
    // herdr --machine <label> workspace list, find by wsLabel, else
    // herdr --machine <label> workspace create --cwd <cwd> --label <wsLabel> --no-focus
}

func FocusWorkspaceViaMachine(h herdrRunner, label, wsID string) error {
    // herdr --machine <label> workspace focus <wsID>
}
```

The list-first, match-by-label idempotency `EnsureWorkspace` already has
(`findWorkspace`) carries over unchanged — only the transport (`herdrRunner`
argv calls instead of `remoteRunner` SSH strings) changes. Parsing also
changes: the SSH path parsed plain-text `workspace list` output; the
`--machine` path gets structured JSON (`workspace_list`/`workspace_created`)
for free, so the new parsing is a `json.Unmarshal`, not a text scanner.

`AttachMachine`'s three-step shape (`EnsureMachine` → ensure workspace →
focus) is unchanged; only steps two and three's transport moves off SSH.

### 2. Configurable tabs and panes: `herdrTabs`

The original request was to gain workspace creation and focus; extending that
to the tabs and panes inside the workspace is a natural next step, driven
entirely by user configuration rather than a fixed layout cloudlab decides
for everyone.

`internal/config/Config.pkl` gains:

```pkl
herdrTabs: Listing<HerdrTab> = new Listing {}

class HerdrTab {
  label: String
  panes: Listing<HerdrPane> = new Listing {}
}

class HerdrPane {
  label: String
  command: String?
  cwd: String?
}
```

This follows the same shape `Flake` already uses in this file — a class
referenced by a top-level `Listing` property — so it merges the same way
`packages`, `agents`, and `flakes` do: additively across `base.pkl` and the
project's `cloudlab.pkl`. A user's personal `~/.config/cloudlab/base.pkl` can
declare tabs they want on every instance (e.g. an `agent` tab); a project's
`cloudlab.pkl` adds project-specific ones (e.g. a `logs` tab tailing a
project-specific file). Base's tabs are created before the project's, same
ordering `packages` and `instructions` already use.

Sample project `cloudlab.pkl`:

```pkl
template = "docker"
tailscale = true

herdrTabs {
  new HerdrTab {
    label = "editor"
    panes {
      new HerdrPane { label = "code"; command = "nvim ." }
    }
  }
  new HerdrTab {
    label = "run"
    panes {
      new HerdrPane { label = "server"; command = "npm run dev" }
      new HerdrPane { label = "logs"; command = "tail -f server.log"; cwd = "logs" }
    }
  }
}
```

**Topology.** Panes within a tab chain sequentially: the first pane is the
tab's root pane (created with the tab), and each subsequent pane splits off
the previous one. This matches how a human builds up a herdr tab by hand and
needs no additional config to express — no explicit split direction or pane
tree, just declaration order.

**Idempotency.** Tabs and panes are matched by label, the same pattern
`EnsureWorkspace` already uses for workspaces: list first, and a label
already present is left alone rather than duplicated. herdr's `create`
operations don't take a label directly for tabs/panes the way `workspace
create` does, so a create is immediately followed by `tab rename`/`pane
rename` to the configured label — this is also how the label becomes the
matching key on the next `cloudlab herdr` run.

> Superseded — see Implementation notes.

**Command execution.** A pane's `command` runs only the first time that pane
is created. Re-running `cloudlab herdr` against an already-populated
workspace must not re-run `npm run dev` into a pane that's already running
it — the whole point of idempotency here is that attaching a second time
looks at what's there and adds nothing.

**cwd.** Relative to the workspace root (`RemoteRepoPath`), matching how a
human would `cd` a pane after opening it. Omitted means "inherit the
workspace's cwd," which is also the workspace root.

### 3. Scope boundaries (unchanged from the 2026-09-10 design)

- `EnsureMachine` — untouched. Registering/enabling the profile is already
  local client state; `--machine` is what reads it, not what writes it.
- `CleanupHerdr`'s `herdr session stop`/`herdr session delete` — untouched.
  `session` is not in `--machine`'s supported command surface, so this stays
  on its existing SSH-exec path.
- The outside-herdr `--remote` path (`InsideHerdr() == false`) — untouched.
  There is no window to attach a machine's sidebar entry to when herdr isn't
  already running locally, so `herdr --remote ssh://...` remains correct
  there exactly as it is today.

### 4. Capability gate

Before running any `--machine`-routed workspace/tab/pane command, check
`herdr status` on the local client against the profile's target rather than
comparing version strings — this is what herdr's own `--skill` guidance
recommends, and what the 0.9.0 design's capability-gate section was already
gesturing at before 0.9.1's docs made it explicit. An instance whose server
doesn't advertise the needed capability is refused with an instruction to
`cloudlab provision`, the same behavior the existing `EnsureMachine` gate
already has for `machine add`.

> Superseded — see Implementation notes.

## Testing

- `EnsureWorkspaceViaMachine`/`FocusWorkspaceViaMachine`: table tests over the
  `herdrRunner` fake already used for `EnsureMachine`, covering no-match
  (create + rename), exact-label-match (no-op), and focus.
- JSON parsing of `workspace_list`/`workspace_created`: unit tests against the
  literal payloads captured during live verification above.
- `herdrTabs` merge ordering (base before project): a Pkl-level test the same
  way `packages`/`instructions` merge ordering is already covered.
- Tab/pane sequential-chaining and first-creation-only command execution:
  table tests over the runner fake, parameterized on "tab/pane already
  exists" vs. "does not exist yet."
- No test drives a real herdr client, matching the 2026-09-10 design's
  existing testing philosophy.

## Rejected alternatives

**Keep the SSH-exec path and add `--machine` as a second, parallel path.**
Rejected: two ways to do the same thing means two things that can drift out
of sync, and `--machine` is strictly better once the profile exists — no
reason to keep the slower path once the workspace has been registered as a
machine anyway.

**A fixed tab/pane layout cloudlab decides for everyone.** Rejected per user
direction — layout preferences are workflow-specific (a Python project's
"run" tab looks nothing like a Docker project's), and `herdrTabs` is exactly
the kind of thing `packages`/`agents`/`instructions` already model well:
opt-in, additive, user-declared.

**Encoding pane tree/split direction explicitly in config.** Rejected as
unnecessary complexity: sequential chaining from declaration order covers
every case seen so far and needs no additional schema.

## Implementation notes

Where the implementation departs from the design above, verified against
herdr 0.9.1 while planning:

- `tab create` accepts `--label`, so tabs are labelled at creation. Only
  panes need a `pane rename` after `pane split`.
- `herdr --machine <x> status` is refused ("not an API-backed machine
  command"), so the capability gate cannot be a status call. The first
  routed call, `workspace list`, is the probe; forwarding never installs,
  restarts or falls back to a local server, so asking with a real command
  is safe. Its failure names `bivouac provision`.
- The `--machine` selector is the profile id, not the label: herdr accepts
  a label only while it is unique, and the user may rename it.
- The SSH path already parsed JSON; `parseWorkspaceList` is reused
  unchanged. With the SSH path gone, the functions keep the names
  `EnsureWorkspace`/`FocusWorkspace` rather than gaining a `ViaMachine`
  suffix.
- Tab and pane failures warn rather than fail the attach.
- herdr's default tab ("1") is left in place; configured tabs follow it.
- `herdrTabs` is read from the session's local `bivouac.pkl`; a repository
  without one gets no layout, including base.pkl's tabs.
- Two tabs with the same label, or two panes with the same label inside
  one tab, resolve to the first declaration; the later one is skipped,
  since label is the only identity a re-attach can match against.
- A pane is labelled before its command is sent, so a failed command is
  never retried on a later attach -- it is found by label and left alone,
  and the failure is surfaced as a warning naming the pane.
- Tabs and panes do not go through `--machine`: `EnsureTabs` runs
  `herdr --session <name> ...` over one SSH connection (the `remoteRunner`
  seam). `--machine` measured about 5s a call against about 24ms for a
  local one, paid on every call with no amortization, and herdr has no
  batching or persistent-connection mode. Workspace and focus make two or
  three calls, which is tolerable; a layout makes one per tab plus one per
  pane after a tab's first, roughly a minute for a small config. So
  `AttachMachine` does open an SSH connection again, but only when tabs are
  configured; `EnsureWorkspace`/`FocusWorkspace` stay on `--machine`.
