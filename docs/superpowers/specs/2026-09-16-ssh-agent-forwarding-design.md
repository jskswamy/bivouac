# SSH agent forwarding to instances

Status: proposed
Date: 2026-09-16

## Context

This grew out of a brainstorm on wiring `aide` into cloudlab. That thread
ended somewhere unexpected: `internal/provisioning/render.go` already lets
any `flakes` entry with `modules: true` contribute a
`homeManagerModules.default` to the per-instance flake — the same generic
path a user's own entry goes through. Once a tool ships a real
home-manager module (as `aide` will, upstream), installing it, managing its
config, and managing its secrets via `sops-nix` all fall out of that
mechanism for free. A user's own private config/secrets is just another
`flakes` entry in their personal `base.pkl` — no cloudlab-specific
"aide" field, wizard question, or Go package was ever load-bearing for
any of that.

What *is* missing: the private half. A `flakes` entry pointing at
`git+ssh://...` — the user's own module, or their own config repo as an
input to a public one — has nothing to authenticate with when `nix`
fetches it during `home-manager switch` on the instance. Seventeen
commits attempting to solve this at the aide layer were written, then
reset unpushed once this became clear — see git history around
2026-09-16 if the reasoning needs re-deriving. This spec is the one
capability actually needed: let the instance borrow the user's local
SSH agent, for exactly as long as a command that might fetch something
private is running.

## Design

Add agent forwarding to `internal/reconcile.Client`, wired in at two
different trust levels depending on the caller.

### The mechanism

`golang.org/x/crypto/ssh/agent` already provides this over the same
`*ssh.Client` connection `reconcile.Connect` returns:

```go
agentConn, _ := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK"))
ag := agent.NewClient(agentConn)
agent.ForwardToAgent(sshClient, ag)   // once, on the connection

session, _ := sshClient.NewSession()
agent.RequestAgentForwarding(session) // once per session that needs it
```

This is the same local agent socket `authMethods()` already dials for
its own auth — no new dependency, no new credential source. `sock == ""`
(no agent running) means forwarding is silently unavailable, same as
today's silent fallback to on-disk identity files for auth: a command
that doesn't need a private fetch is unaffected either way.

### Automatic, no flag

Two call sites always request forwarding, because both run a build that
might need to fetch something private:

- **`up` / `provision`** — wherever `reconcile` runs the remote
  `home-manager switch`.
- **`session start`** — session setup is also a point where a private
  input could be pulled in.

No config flag gates this. The forwarded channel only matters if the
remote side actually tries to use it; an instance with nothing private
to fetch just never asks.

### Opt-in `--forward-agent` flag elsewhere

The ad-hoc `lookupCommandSpecs` entries — `shell`, `ssh`, `herdr`,
`tmux`, `pair`, `connect` — get a `--forward-agent` bool flag via each
spec's existing `flags func(*cobra.Command)` hook. Off by default: these
are interactive/ad-hoc commands where forwarding the agent is a
deliberate, occasional choice (cloning something by hand mid-session),
not an implicit part of what the command does. No restriction on why —
the flag just does it.

### What this doesn't touch

Nothing in `internal/config`'s schema, no wizard question, no
tool-specific package. This is infrastructure any private flake input
benefits from, not an aide feature.

## Testing

- A fake-SSH-server test (the existing pattern in `internal/reconcile`'s
  test suite) asserting that, for the automatic paths, the forwarding
  channel is requested and the remote environment's `SSH_AUTH_SOCK`
  points at a working forwarded socket.
- A flag-wiring test per opt-in command: `--forward-agent` present and
  off by default; passing it results in the same forwarding request as
  the automatic paths.
- No test for "a private flake input actually resolves" — that's
  `nix`'s contract, not this code's.

## Rejected alternatives

**Forward the agent on every SSH connection cloudlab makes**, including
the plain interactive shell/tmux/pair paths by default. Rejected:
forwarding hands the remote host live signing access to the user's
agent for the connection's lifetime, and those commands are often
long-lived interactive sessions — a much bigger exposure window than a
one-shot build. Opt-in for those, automatic only where a build is
actually happening and short-lived.

**Gate the automatic paths behind a config flag** (e.g.
`sshAgentForwarding: true` in `cloudlab.pkl`). Rejected: forwarding is a
no-op if the remote side never asks for it, so a flag would only add a
step most users would need to remember to flip on before it could ever
help them.
