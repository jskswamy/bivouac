# cloudlab herdr and saved machines

Status: designed, not implemented
Date: 2026-09-10
Tracks: `cloudlab-7y1` (subsumed by this), `cloudlab-9s4`
Requires: herdr 0.9.0 on the instance (pinned in `2c81f89`)

## The problem

`cloudlab herdr` launches a separate herdr client per instance:

```go
func herdrArgs(ip, user, session string) []string {
    args := []string{"--remote", "ssh://" + user + "@" + ip}
    if session != "" {
        args = append(args, "--session", session)
    }
    return args
}
```

Two consequences. Each instance gets its own window, so watching three agent
sessions means three windows and no combined view. And running it from inside
herdr is refused outright, because herdr blocks nested sessions:

```
already inside a herdr session -- herdr disables nested sessions by default
```

herdr 0.9.0 adds saved SSH machines, which keep *"Local and several SSH
machines in one window"* with a combined agent list. That makes being inside
herdr the case cloudlab should serve best, not the one it refuses.

## What was verified

Everything below was run against a live instance rather than read from docs.
Three add/remove cycles left the instance's `herdr.service` on the same PID
throughout.

| Behaviour | Result |
|---|---|
| `machine add` non-interactively, capable server | exit 0, no prompt, no TTY needed |
| `machine add` against an unreachable host | exit 1, `machine was not saved` |
| `--remote-session <name>` | starts that session on the remote (`stopped` → `running`) |
| Effect on cloudlab's `herdr.service` | none — same PID before and after |
| Side-loaded binary with a capable server | none |
| `machine remove` | client-side only; remote sessions and agents keep running |
| Local `workspace list` / `agent list` with a machine registered | unchanged — local only |
| `ssh <target> herdr workspace create --cwd … --label …` | works |
| `ssh <target> herdr workspace focus <id>` | works — remote focus moves |

The profile shape, which is everything the lifecycle needs:

```json
{ "id": "6410458fe77e…", "label": "cloudlab/status-reporting",
  "target": "ssh://subramk@206.189.140.27", "session": "status-reporting",
  "enabled": true, "selected": false }
```

## The one thing cloudlab cannot do

**Selecting a machine.** There is no CLI, no API operation, and no keybinding.
Checked four ways: `herdr machine` exposes only list/add/rename/remove/
enable/disable; the 275 KB API schema contains no machine operation at all;
the default config has no machine binding; and `agent list` returns only local
agents even with a machine registered and selected.

The reason is structural. Every herdr subcommand is a client of a **server**
socket, and the docs are explicit that *"selecting a machine doesn't retarget
CLI commands in existing panes; they still use that pane's inherited session
and socket."* Machines are **client** state — the profile list and the
selection live in `~/.local/state/herdr/client/`, and the client is the TUI,
which exposes no socket.

So `cloudlab herdr` guarantees the entry **exists, is enabled, and is
correctly labelled**, then names it. Picking it stays a keypress.

This is worth an upstream request: `--select` on `add`/`enable`, plus a way to
select an already-enabled machine. `selected_profile` is already persisted in
`endpoint-selection.json`, so the concept is modelled — it just is not
reachable.

## Design

### The guard becomes a router

`HERDR_ENV` currently triggers a refusal. It becomes the branch:

```
HERDR_ENV set    → the machine path (below)
HERDR_ENV unset  → herdr --remote, exactly as today
```

Outside herdr there is no window to attach to, so launching one remains
right. Inside herdr, registering is what the user wants and what 0.9.0 is for.

### The machine path

```
1. herdr machine list --json
     no profile with this target+session  → machine add --label <instance>/<session>
                                              --remote-session <session> <target>
     profile present but disabled         → machine enable <id>
     profile present and enabled          → nothing
2. ssh <target> herdr --session <session> workspace list
     no workspace for this session        → workspace create --cwd <RemoteRepoPath>
                                              --label <session>
3. ssh <target> herdr --session <session> workspace focus <id>
4. print which sidebar entry to select
```

Step 2 is not what the original request asked for and is the larger prize: the
workspace is rooted in the session's checkout, so attaching lands in the code
rather than `$HOME`. That is `cloudlab-7y1`, whose open question — *"does the
workspace get created on the instance's herdr server, or locally?"* — is
answered: on the instance's server, addressed over SSH.

### Labels

`<instance>/<session>`. Two instances can each hold a session named `auth`,
and the label is the only thing distinguishing them in the sidebar. Profiles
are keyed by opaque ID, so nothing else prevents the collision.

### Idempotency

Always list first. `machine add` does not deduplicate, so running
`cloudlab herdr` twice would otherwise leave two profiles for the same target
and session. Match on `target` + `session`, never on label — the user may
rename a profile, and the docs warn against deriving IDs.

### The capability gate

An instance whose herdr predates 0.9.0 lacks the `surface_interest` and
`health_check` capabilities saved machines require. `machine add` then stops
the running server, deploys its own binary to `~/.local/bin` outside nix, and
serves from there — observed on a live instance, which was left with
`herdr.service` inactive and PATH resolving a different version than the one
actually serving.

So the mode must be checked before `machine add` runs, and refused with an
instruction to provision, rather than discovered afterwards. Versions do not
need to match — the docs say so explicitly, and warn against replacing a
server merely because its version differs — but the instance must be new
enough to have the capabilities.

### Lifecycle

`session delete`, `session merge` and `down` remove the profiles they created:
list, match on target+session, `machine remove <id>`. Without this, profiles
outlive what they point at — the same leak already visible as herdr sessions
stranded on instances whose cloudlab sessions were retired days ago.

`down` removes every profile for the instance, not just one, since the droplet
is gone.

## Scenarios

| Situation | Behaviour |
|---|---|
| Inside herdr, no profile | add, create workspace, focus, name the entry |
| Inside herdr, profile enabled | ensure workspace, focus, name the entry |
| Inside herdr, profile disabled | enable, then as above |
| Outside herdr | `herdr --remote`, as today |
| Instance unreachable | `machine add` fails cleanly; report it, change nothing |
| Instance below 0.9.0 | refuse, name `cloudlab provision` |
| Instance IP changed | profile's `target` is stale; remove and re-add |
| Multiple sessions | one profile each — a profile targets one remote session and cannot combine them |
| No session at all | register the instance's default session |
| Session merged or deleted | remove the profile |
| `down` | remove every profile for that instance |
| Nested inside a pane **on the instance** | refuse: registering would add the instance to itself |
| Outside herdr, no client running | `--remote` launches one, so this is already handled |

### Which address

Register the address `cloudlab ssh` already uses for that instance. Baking in
a tailnet address means the machine sits in Attention whenever the tailnet is
down, which was observed repeatedly. This interacts with `cloudlab-ulf` and
should follow whatever that settles rather than inventing a second rule.

## Testing

- `herdrArgs` keeps its current tests; the router adds one per branch of
  `HERDR_ENV`.
- Profile matching (target+session → add / enable / nothing) is a pure
  function over `machine list --json` output: table test.
- Command construction for `machine add`, `machine remove`, `workspace
  create` and `workspace focus` is string-building, tested like `serveArgs`
  and `herdrArgs` already are.
- The capability check and the SSH calls go behind the existing
  `reconcile.Client` seam, so they can be driven by the fake SSH server the
  lifecycle tests already use.
- No test drives a real herdr client. The units beneath it are what carry the
  behaviour.

## Rejected alternatives

**Writing `endpoint-selection.json` to force a switch.** It is another tool's
private state, the running client did not re-read it when tested, and cloudlab
would be racing herdr's own writes.

**Disabling other machines so only one remains.** Programmatic, but it
disconnects them — destroying the combined agent list, which is the reason to
use machines at all.

**Dropping `herdr.service` from `common.nix`.** Considered when the takeover
looked unavoidable. Unnecessary: with a capable server the unit and `machine
add` coexist, verified across three cycles. The unit still earns its place by
guaranteeing a server before first attach, which `cloudlab pair` depends on.

**Keeping the nested-session refusal.** It exists because `--remote` cannot
nest. Registering a machine is not a nested session, so the reason does not
apply to the new path.
