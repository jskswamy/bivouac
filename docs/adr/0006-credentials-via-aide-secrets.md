# 0006: Claude Code credentials via aide's secrets system, not credential sync

## Status

Accepted, not implemented — premise has lapsed.

Nothing in this ADR exists in the code: `shell` is still a stub, no
`SOPS_AGE_KEY` injection exists anywhere, and `aide` is not among the
packages any template installs. The premise it rests on has also changed —
Claude Code is no longer installed by default. Agent harnesses became
opt-in through `cloudlab.pkl`'s `agents` field (see
[`docs/config.md`](../config.md#agents)), so an instance only has one if
you asked for it, and authenticating it there is currently manual.

The decision below is still the intended direction for agent credentials,
and the reasoning against credential sync still stands.
[ADR-0009](0009-general-secrets-via-sops-nix.md) proposes the broader
instance-secrets design that would subsume this; it is likewise not
implemented. What *does* exist today is the personal, sops-encrypted
secrets file `cloudlab secrets` manages — but that lives on your machine
and holds the Tailscale auth key, not agent credentials on the instance.

## Context

Claude Code and aide were to be installed by default on every instance (see
the project brief). Instances are ephemeral — destroyed on `down`, recreated
fresh on the next `up` — so if Claude Code needed an interactive `claude
login` every time, "installed by default" wouldn't actually reduce friction
at the point of use.

The first option considered was syncing local Claude Code credentials
(`~/.claude/.credentials.json` or equivalent) to the remote alongside SSH
key registration. That was flagged explicitly as security-sensitive rather
than defaulted silently: it means auth material leaving the local machine
and landing on disk on a cloud VM, even a short-lived one.

aide already solves this exact problem for CI/Docker use: `SOPS_AGE_KEY` is
passed as an environment variable at runtime, aide decrypts the relevant
`sops`/`age`-encrypted secret in-process, and the plaintext (e.g.
`ANTHROPIC_API_KEY`) exists only in the agent process's memory/environment
for the lifetime of that invocation — never written to disk. See aide's
`docs/secrets.md`, "CI and Docker" section.

## Decision

Reuse aide's existing mechanism instead of building or accepting a
credential-sync path:

- `SOPS_AGE_KEY` gets injected into remote sessions opened via `shell`/`ssh`,
  the same way `DOCKER_HOST` is injected for the docker template.
- Every remote agent invocation runs through `aide` (which wraps and secures
  the `claude` invocation), not bare `claude`.
- No Claude Code credential file is synced to the VM, ever.

## Consequences

- Zero-friction reuse on a fresh instance depends on the instance having
  aide + the relevant secret's age recipient already set up — same
  precondition as using aide anywhere else, nothing cloudlab-specific to
  configure.
- No plaintext credential material persists on VM disk at any point,
  including across `down`/`up` cycles.
- This only covers Claude Code auth. Any other credential a user's workflow
  needs on the instance is a separate, not-yet-designed concern.
