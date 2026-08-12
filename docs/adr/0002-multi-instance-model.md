# 0002: Multi-instance model

## Status

Accepted

## Context

The simplest possible model is a single active instance — one VM at a time,
`up` refuses to run again until `down`. That's a real option (and how many
simple VM-management scripts work), but it doesn't match the intended usage:
being able to run a `docker` instance and a `python` instance side by side,
or several instances across different projects at once.

## Decision

Adopt full multi-instance support. Any number of named instances can be
running at once, each backed by its own VM. State is one entry per
instance, not a single global record.

## Consequences

- Every per-instance command (`up`, `down`, `shell`, `ssh`, `sync`,
  `download`, `watch`, `status`, `connect`) needs to resolve *which* instance
  it's targeting — see [ADR-0003](0003-git-derived-instance-identity.md).
- A `list` command is needed to get an overview across instances.
- `down` only tears down the named instance, not "the" VM.
