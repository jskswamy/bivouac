# Resumable Session Retirement Execution Notes

These notes record implementation evidence for
`2026-10-15-resumable-session-retirement.md`. They distinguish opt-in
real-Herdr checks from the ordinary fake/Go test suite and do not claim live
cloud coverage.

## Task 1: Herdr ownership and closure capability gate

Tested on x86_64 Linux with Herdr 0.9.4. The public release binary used for the
opt-in test reported `herdr 0.9.4` and had SHA256:

```
493304fbefb0ed0ea0455f373b47f48d24e8774bc7974f4c916ad2e937675b23
```

The fixture starts `herdr server` directly in a unique short `/tmp` root. It
sets isolated HOME, XDG config/runtime/state/data/cache paths, an explicit
`HERDR_CONFIG_PATH`, and an explicit default-session `HERDR_SOCKET_PATH`.
Every inherited `HERDR_*` variable is removed before the server or client is
started. It neither discovers nor addresses the hosting socket. Cleanup sends
`herdr server stop` only with that same isolated environment and waits for the
fixture process to exit.

### Proven ownership representation

The capability tests establish ownership scheme
`herdr-workspace-root-terminal-v1`:

- Persist a cryptographically random creation-intent token before mutation and
  use it as the workspace's temporary label.
- After create, or after a lost create reply, list the intended server and
  require exactly one matching temporary label. Query that workspace's panes
  and record the returned workspace id, root pane id, and opaque `terminal_id`.
- Rename to the friendly session label only after recording those values.
- Before close, query the exact recorded server and workspace. Authorise close
  only when the recorded root pane id still has the recorded opaque terminal
  id. Labels do not authorise close.

The intent label is recovery evidence only at the create/lost-reply boundary;
it is not cleanup authority after friendly rename. The production adapter in
Task 4 must encode the recorded workspace/root-terminal tuple in the opaque
`OwnershipProof.Data` and retain the exact server address separately.

Two simultaneously running fixture servers both allocated `w1`, and both
workspaces were renamed `friendly`. The recorded terminal proof matched only
the creating server. Closing that workspace left the colliding workspace and
server untouched.

A normal server stop/restart restored the public workspace and pane handles
from `session.json`, but launched a new terminal with a different opaque
`terminal_id`. A stale proof therefore refused to close the restored resource
and left it running. This is the accepted fail-closed restart limit: Herdr
0.9.4 does not persist the opaque terminal identity needed to distinguish a
restored process from replacement state. Bivouac must report ownership cleanup
pending rather than fall back to workspace id or label after such a restart.

`workspace close` terminated the pane process. A shell trapping EXIT/HUP/TERM
wrote a sentinel, and the post-close checkpoint observed it. Thus the close
reply is not treated as sufficient evidence by itself; the test also queries
that the workspace is absent and observes the process shutdown side effect.

### Commands and results

Red run, before `internal/testenv.StartHerdr` existed:

```
BIVOUAC_HERDR_BIN=/tmp/bivouac-herdr-0.9.4 nix develop -c sh -lc \
  "go test ./internal/lifecycle -run 'TestHerdrRetirementCapabilities|TestHerdrOwnershipAfterRestart' -count=1 > /tmp/retirement-capabilities.log 2>&1"
```

Exit 1 with compile errors for the missing fixture, rather than contacting a
live Herdr server.

Green capability and existing selected-identity transport run:

```
BIVOUAC_HERDR_BIN=/tmp/bivouac-herdr-0.9.4 nix develop -c sh -lc \
  "go test ./internal/lifecycle -run 'TestHerdrRetirementCapabilities|TestHerdrOwnershipAfterRestart|TestHerdrPersistentSelectedTransport' -count=1 -v > /tmp/retirement-herdr-task1.log 2>&1"
```

Exit 0. Results:

- `TestHerdrRetirementCapabilities`: PASS (1.03s)
- `TestHerdrOwnershipAfterRestart`: PASS (1.11s)
- `TestHerdrPersistentSelectedTransport`: PASS (2.28s)

The transport row created a saved machine and reconnected from a subsequent
client process through the selected SSH alias. Both its remote daemon and the
two capability servers were disposable fixtures. No existing saved machine,
server, session, cloud instance, or user SSH key was mutated.

Without `BIVOUAC_HERDR_BIN`, all three real-Herdr tests skip with an explicit
message; an ordinary suite result must report those skips rather than count
them as integration passes.

## Tasks 2–9: durable state and shared retirement workflow

The implementation records a random session generation, exact Herdr address
and ownership proof, immutable retirement intent, acceptance checkpoints,
write-ahead effect status, and a bounded completion receipt. State replacement
and replay-journal writes use atomic rename and operation locks serialize
session work with instance destruction.

Both `session merge` and `session delete` now call `RetireSession`. Tests inject
failure at coordinator boundaries and establish that retries preserve the
operation/force mode, skip proven-complete closure, repeat uncertain effects,
run a final work gate after closure, and publish a receipt only after all
residue effects are observed complete. A completed command retry succeeds from
the receipt. `down` refuses pending retirement unless `--force` explicitly
abandons its obligations.

The ordinary Go suite covers late remote writes, destination branch changes,
source extension, corrupt replay evidence, unreachable issue state, failed
remote removal, missing resources, foreign Herdr resources, shared-machine
preservation, and residue removal. The final validation command and result are
recorded in the handoff commit history. Live cloud destruction was not run.
The Herdr 0.9.4 restart limitation described above remains intentionally
fail-closed.

Final local checks passed: `go test ./...`, `go test -race ./internal/state
./internal/lifecycle ./cmd`, `pre-commit run --all-files`, and the isolated
Herdr ownership/attachment rows with the 0.9.4 binary above. `nix flake check
--option sandbox false --print-build-logs` did not pass: the daemon reported
that `sandbox` is restricted for this untrusted user, then the sandboxed
pre-commit derivation could not download uncached Go modules because DNS/network
access was unavailable. The same pre-commit hooks passed in the populated
project development environment; no daemon trust setting was changed.
