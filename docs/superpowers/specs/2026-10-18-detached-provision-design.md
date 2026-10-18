# Provisioning that survives a dropped connection

Status: implemented
Date: 2026-10-18

## Context

`Reconcile` holds one SSH connection for the whole of `up` and `provision`
and runs `home-manager switch` inside a session on it. Nothing recovers
when that connection dies. It dies routinely on a laptop: moving between
Wi-Fi and a hotspot changes the source address, and a TCP connection tied
to the old address does not reset, it goes silent until the OS reports
`read: operation timed out`. Every later `NewSession` then fails on the
same dead socket, which is why one drop surfaced as the same error once
per validated module.

A keepalive only shortens the wait. The switch itself is the exposure: it
runs for minutes inside the session, so a drop can end it part-way.

## Design

Run the switch under systemd on the instance, and treat the SSH connection
as a window onto it that can be reopened.

### Starting

```
sudo -n systemd-run --unit=bivouac-provision --uid=$USER \
  --working-directory=$HOME --property=RemainAfterExit=yes \
  bash -lc '<the existing home-manager switch>'
```

A system unit running as the user, not `systemd-run --user`: the switch
reloads and restarts the user's own units, and a unit living in that
manager is one of them. `RemainAfterExit` keeps a finished unit loaded, so
its result outlives the connection. Without it a successful unit is
collected at once and "succeeded" cannot be told from "never ran".

The unit name is the lock. If the unit is `running`, attach to it. If it
is `exited` or `failed`, `stop` and `reset-failed` it, then start afresh.

### Following

Poll, reconnecting as needed:

- output: `journalctl -u bivouac-provision -o cat --after-cursor=<c>
  --show-cursor`, so a reconnect resumes with no repeated or missing lines;
- state: `systemctl show bivouac-provision -p LoadState,SubState,Result,
  ExecMainStatus`.

Done when `SubState` is `exited` or `failed`. Success is `exited` with
`Result=success`. A `LoadState` of `not-found` after the start means the
unit vanished (a reboot) and is an error.

A failed poll is redialled with backoff. The run gives up only when it
has not reached the instance for a bounded time, and says so: the switch
may well still be running, and `bivouac provision` attaches to it.

### Keepalive

A `keepalive@openssh.com` request on a timer, redialling when it goes
unanswered, so a dead socket is noticed in seconds instead of minutes.

### Limit

A forwarded agent lives and dies with the session that requested it, so a
detached unit cannot use it. A config with a `git+ssh` flake therefore
keeps the in-session path, as before.

## Out of scope

Resuming `up`'s earlier steps, a `provision --detach` flag, and a log file
on the instance: the journal already is one.

## Verified on a real instance (Ubuntu 24.04, systemd 255)

`sudo -n systemd-run --uid` runs as the user with `HOME` set and a login
shell. A failing unit leaves `Result=exit-code` and its output in the
journal. Starting an existing unit name fails with `already loaded`.
Not yet verified: a real switch, or a connection killed mid-run.
