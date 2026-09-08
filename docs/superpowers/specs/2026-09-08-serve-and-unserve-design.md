# Serve And Unserve

## Status

Accepted, not implemented.

Resolves cloudlab-sx9, and supersedes the `connect --serve` / `connect
--serve=off` shape sketched on that issue.

## Context

`connect` reaches a service on an instance. A service bound to a routable
address is printed as a URL and the command exits; a service bound to
loopback is reached through a foreground `ssh -L` that ends on Ctrl-C.

The forward works, but it is the wrong tool for anything you want to leave
running. It occupies a local port, which collides whenever the same service
runs on both machines -- moshi-hook on 24543 hit this the first time anyone
tried it, and `--local-port` exists because of it. It holds a terminal. It
dies with that terminal.

Tailscale can do the same job without any of that. Verified on a live
instance:

    sudo tailscale serve --bg --tcp 9876 tcp://localhost:9876

With that in place and no forward running at all, the instance's
`127.0.0.1:9876` answered at `100.81.106.84:9876` and at
`jskswamy-cloudlab-1.jaguar-ide.ts.net:9876`.

The first sketch bolted this onto `connect` as `--serve`, with `--serve=off`
to undo it. That reads as a command wearing a flag's clothes, and it muddles
two things whose only real difference is how long they last.

### On the exposure

An earlier draft of this design treated publishing as security-sensitive
enough to warrant an explicit port and a confirmation prompt. That was
wrong twice over, and the correction is recorded here so it is not
re-derived.

`tailscale serve` publishes within the tailnet only -- the instance's own
output labels it `(tailnet only)`, and `tailscale serve --help` directs
anyone wanting public exposure to `tailscale funnel`. The audience is the
user's own devices, not the internet.

And requiring an explicit port would have made discovery worse than the risk
it addressed: a user who does not know the port would run `connect` to find
it, which *starts a forward*, then read the number, cancel it, and run
`serve`. Using one command as a lookup tool for another, with a side effect
to undo.

What remains is narrow and real: an accidental forward dies with Ctrl-C, an
accidental serve persists until noticed. `status` listing served ports
answers that directly.

## Decisions

**Three verbs, split by lifetime.** That is the only axis on which these
differ, so it is the only one the surface should encode.

| | lifetime | reachable by | stopped by |
|---|---|---|---|
| `connect [--port N]` | the command | this machine only | Ctrl-C |
| `serve [port]` | until stopped | the tailnet | `unserve` |
| `unserve [port]` | — | — | — |

**`connect` does not change.** No `--serve`, no `disconnect`. The reasoning
that deleted `state.Record.TunnelPID` still holds for it: foreground, no
state, nothing to tear down. Persistence is `serve`'s business and teardown
lives there.

**Flat verbs, not a `serve` noun.** Served ports are structurally the same
kind of thing as sessions -- persistent, listable, per-instance -- which is
the argument for grouping them the way `session start|list|pull|merge|delete`
is grouped. Rejected because a `serve` noun costs three words to publish one
port, and the listing that would justify it belongs in `status`, which
already answers "what is live on this instance?" for sessions. The top level
grows from 18 commands to 20; that is the price of the shorter path.

**`serve` and `unserve` take arguments exactly as `connect` does**: discover,
hide the machine's own sockets, auto-select a sole candidate, offer a picker
when there are several, `--all` to widen, an explicit port to skip all of it.
One convention across all three commands. `unserve` discovers from what is
*served* rather than what is listening -- a different list, the same shape.

**No confirmation prompt.** Typing `serve` is the request. Exposure is
tailnet-only and `status` makes a forgotten entry visible, so a prompt would
buy little and cost a `--yes` flag for scripts.

**Serving an already-routable service creates no entry.** It is reachable at
the tailnet address already; an entry would be clutter that someone has to
clean up later. The command prints the URL and says so.

**`tailscale serve reset` is never used.** It clears every entry, including
ones the user created by hand, and `tailscale serve status` does not record
which entries cloudlab added. Per-entry teardown is verified to work: with
9876 and 9877 both served, `--tcp 9876 off` removed only 9876.

### Rejected alternatives

**`connect --serve` / `connect --serve=off`.** The shape this replaces.
`=off` is a command spelled as a flag, and it puts a persistent side effect
behind a flag on a command that is otherwise entirely ephemeral.

**A `disconnect` command.** Reads as `connect`'s inverse but could only ever
stop *served* entries, never a running forward -- that is a foreground
process in another terminal with no PID recorded, deliberately. A verb whose
name promises more than it does.

**`unserve` as `serve stop`.** Would avoid inventing a word, and matches the
`session` precedent. Rejected with the noun it belongs to.

**Recording served ports in `state.json`.** Would keep `status` free of an
SSH round trip. Rejected because the record drifts: it cannot see entries
made by hand and would keep showing entries someone cleared. This is the
same bookkeeping the design already rejected once, when `TunnelPID` was
deleted for being state nobody read.

## Architecture

```
cloudlab connect [--port N] [--local-port N] [--all]      unchanged
    discover -> choose -> routable ? print URL : ssh -L (foreground)

cloudlab serve [port] [--all]
    discover -> choose -> routable ? print URL, no entry
                                   : tailscale serve --bg --tcp
                                     -> print tailnet URLs, exit

cloudlab unserve [port]
    serve status --json -> choose -> tailscale serve --tcp <port> off

cloudlab status
    ... Sessions: ...
    Serving:  <from serve status --json, or "unknown" if unreachable>
```

Discovery, filtering and selection are already built for `connect`
(`Listeners`, `VisibleListeners`, `chooseListener`, `pickListener`) and are
reused rather than reimplemented. `unserve` needs its own candidate list --
served ports, not listening ones -- but the same choose-and-pick shape.

## Components

### `internal/lifecycle/serve.go` (new)

```go
// A ServeEntry is one published port.
type ServeEntry struct {
    Port    int
    Forward string // "localhost:9876", as tailscale reports it
}

func Serve(ctx context.Context, ip, user string, port int) error
func Unserve(ctx context.Context, ip, user string, port int) error
func ServeStatus(ctx context.Context, ip, user string) ([]ServeEntry, error)

func parseServeStatus(jsonOut string) ([]ServeEntry, error)  // pure
func serveArgs(port int) []string                            // pure
func unserveArgs(port int) []string                          // pure
```

`tailscale serve status --json` returns `{}` when nothing is served and

```json
{ "TCP": { "9876": { "TCPForward": "localhost:9876" } } }
```

otherwise -- so parsing is `encoding/json` into a small struct, not text
scraping. Both shapes are captured as fixtures.

All three shell out over SSH via `reconcile.Connect` and `client.Run`,
following `Listeners`. They need `sudo`, because tailscaled runs as root and
cloud-init grants the instance user passwordless sudo.

The tailscale binary is resolved the way `JoinTailscale` already does it,
through `RemoteTailscaleBin`, rather than assuming a path.

### `cmd/`

`serve` and `unserve` join `lookupCommandSpecs` as `named: true` commands
with an optional positional port, matching how `connect` is declared. Both
reuse `chooseListener`/`pickListener`; `unserve` gets a sibling
`chooseServeEntry` with the same branch structure, since its candidates are
`ServeEntry` rather than `Listener`.

`printServing` joins `printSessions` in `runStatus`, and like it is a
separate function so it can be tested without a provider.

## Data flow

### `cloudlab serve 8888`

```
resolve instance
Listeners over SSH -> VisibleListeners -> chooseListener
  routable        -> print the tailnet URL, note it needs no serving, exit
  loopback        -> RemoteTailscaleBin
                     sudo <ts> serve --bg --tcp 8888 tcp://localhost:8888
                     print http://<tailnet-ip>:8888
                           http://<magicdns>:8888
                     exit
```

Nothing is written to state on either branch.

### `cloudlab unserve`

```
resolve instance
ServeStatus over SSH
  none          -> "nothing is being served on <instance>; a running
                    forward stops with Ctrl-C in its own terminal"
  one           -> that one
  several + tty -> picker
  several, none -> "pass a port"
sudo <ts> serve --tcp <port> off
```

The empty-state message names the forward explicitly. Someone with a
`connect` running who reaches for `unserve` is the predictable confusion in
this design, and the message is where it gets answered.

### `cloudlab status`

`ServeStatus` runs after `printSessions`. An error prints
`Serving:  unknown (instance unreachable)` and the command still succeeds,
matching how a failed live provider check already renders as
`Status:   unknown (live check failed: ...)`.

## Error handling

An instance not on a tailnet cannot serve: `serve` fails with that reason
rather than a tailscale error, checked via `record.TailscaleJoined` before
any SSH work, the same short-circuit `runConnect` and `choosePairHost` use.

`serve` on a port already served is not an error -- tailscale accepts it and
the result is what was asked for.

`unserve` on a port that is not served says so rather than reporting
tailscale's exit status.

`status` never fails because of serving. That is the one place this design
touches a command that already works, so its failure mode is the one worth
being careful about.

## Testing

`parseServeStatus` is table-driven over fixtures in `testdata/`: the empty
`{}`, a single TCP entry, and several. `serveArgs`/`unserveArgs` get argv
assertions, the pattern `forwardArgs` and `sshArgs` already follow.

`chooseServeEntry` is table-driven over its branches -- none, one, several
with a terminal, several without -- mirroring `TestChooseListener`, with
error messages asserted where they differ.

`printServing` is tested against a fake, including the unreachable case,
because "status keeps working when the instance is down" is the regression
this could introduce.

`Serve`, `Unserve` and `ServeStatus` are exercised against the package's
existing fake SSH server, asserting the command sent -- as
`TestListeners_RunsSSAndParses` does.

Live verification, which unit tests cannot cover: serve a loopback service,
reach it from another machine on the tailnet with no forward running,
confirm `status` shows it, `unserve` it, and confirm an unrelated hand-made
serve entry survives.

## Costs

Two commands that both reach a service, and the user has to know that
lifetime is what separates them. Mitigated by help text that says so, but it
is a real thing to learn.

The top level grows to 20 commands. `session` exists precisely because that
list should not grow without reason, and this grows it by two.

`status` gains an SSH round trip, so it is slower and now has a second way
to be partially unavailable.

"unserve" is an invented word. `serve stop` would not be, but it belongs to
the noun-grouped shape that was rejected.

## Out of scope

`tailscale funnel`, which publishes to the public internet. Same mechanism,
a different risk conversation, and no one has asked for it.

Serving a port that nothing is listening on yet -- publishing in advance of
starting the service. `tailscale serve` permits it, but discovery has
nothing to offer and the flow needs its own thought.

cloudlab-ulf, the public-IP-versus-tailnet sweep across the session
commands. `serve` is tailnet-only by nature so it does not add to that debt.
