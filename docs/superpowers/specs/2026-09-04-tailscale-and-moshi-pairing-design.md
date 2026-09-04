# Tailscale Auto-Join and Moshi Pairing Design

## Context

The base template (`templates/modules/common.nix`) installs `tailscale` and
`moshi-hook`, but both still need a manual step after `cloudlab up` before
they're actually useful: `tailscale up` to join the tailnet (no auth key
wired in), and `moshi-hook host setup` to pair the getmoshi.app mobile app
via its QR flow. Neither is automated today.

Two separate concerns, addressed together because they were designed in the
same pass:

1. **Joining Tailscale** needs a secret (an auth key) that must reach the
   instance without ever touching disk in plaintext, a local/remote process
   list, or cloudlab's own logs.
2. **Pairing Moshi** needs an interactive QR scan and has no secret-handling
   concerns at all — `host setup` handles its own key exchange.

Both become new, independent `cloudlab` commands rather than steps buried
inside `up` — matching the existing `herdr`/`tmux` precedent (see
`docs/superpowers/specs/2026-09-04-tmux-herdr-design.md`): auto-wireable
where it makes sense, always available on demand regardless.

## Secrets: `~/.config/cloudlab/secrets.yaml`

A personal, global, sops-encrypted file (age/YubiKey recipients via
`age-plugin-yubikey`, already on the developer's machine) — not per-repo,
since a Tailscale auth key is account-level, not project-specific. Mirrors
the existing personal-config precedent (`~/.config/cloudlab/base.pkl`, see
`docs/config.md`) without reusing that file, since Pkl config and encrypted
secrets are different concerns.

```yaml
tailscale_authkey: tskey-...
```

Format is YAML, not TOML: `sops --help` documents its supported formats as
"currently json, yaml, dotenv, ini and binary" — TOML isn't one of them.
Verified directly: encrypting a `.toml` file falls back to sops's `binary`
mode (the entire file becomes one opaque blob under a synthetic `data`
key), which breaks the whole "keys stay readable, only values are
encrypted, extract one at a time" design this section depends on. YAML
does exactly what's needed, confirmed against a real encrypted file:

```
$ sops -d --extract '["tailscale_authkey"]' secrets.enc.yaml
tskey-abc123-example
```

**Decryption is just-in-time and minimal-exposure**, not a design nicety —
the explicit requirement driving this section:

- Shell out to the `sops` CLI (already on PATH, matches this codebase's
  existing convention of invoking external tools — `tailscale`, `mutagen`,
  `nix`, `ssh`, `rsync` — as subprocesses rather than linking their Go
  libraries): `sops -d --extract '["tailscale_authkey"]' secrets.yaml`.
  `age-plugin-yubikey` handles the actual hardware interaction underneath,
  whether invoked via the `sops` CLI or a Go sops library — that choice
  doesn't change what touches the YubiKey.
- Captured as `[]byte`, not `string` — Go strings are immutable and can't
  be scrubbed from memory; a byte slice can be explicitly zeroed.
- Never passed as a literal CLI argument, locally or remotely (see Join
  Mechanics below) — it only ever moves over SSH stdin.
- Never cached, logged, or returned from a function further than the one
  call site that consumes it. Zeroed via `defer` immediately after its one
  use.
- `up`/`cloudlab tailscale` fail with a clear, specific error if
  `tailscale = true` but `secrets.yaml` or the key inside it is missing —
  not a generic decrypt-failure crash.

## Config: `cloudlab.pkl`

```pkl
tailscale: Boolean = false
```

Off by default — auto-joining requires `secrets.yaml` to exist with a real
key, so it can't sensibly default to on. When `true`, `up` runs the same
join logic as `cloudlab tailscale` (see below) as an additional step after
`Reconcile` (once `pkgs.tailscale`/`tailscaled` actually exist on the
instance) and before `Rsync`/`StartWatch`. Wired into `lifecycle.Steps` as
a new field (e.g. `JoinTailscale`), following the same
substitutable-for-testing shape as `WaitReady`/`Reconcile`/`Rsync`.

On success, both `up`'s step and the standalone `cloudlab tailscale`
command set a new `TailscaleJoined bool` field on the instance's
`state.Record` (alongside `Name`/`IP`/`User`/etc.). This — not
`cloudlab.pkl`'s current `tailscale` value — is what `down` checks (see
Teardown below): `down.go`'s `Down()` only ever receives a `state.Record`,
never a resolved `config.Config`, and re-reading `cloudlab.pkl` at
teardown time would be wrong regardless (the toggle could've changed, or
the instance could've been joined manually via `cloudlab tailscale` with
the config still `false`). Recording what actually happened, not what the
config currently says, is the only way `down` can know correctly.

## Command: `cloudlab tailscale [name]`

Standalone command — join/rejoin an already-provisioned instance's
tailnet on demand, independent of `up`. Same shape as `herdr`/`tmux`: a
pure argv/steps builder plus a thin execution wrapper, wired into
`cmd/lookup_run.go`'s `lookupCommandSpecs` table.

**Mechanics** (shared by both the standalone command and `up`'s optional
step):

1. Locally: decrypt `tailscale_authkey` per the Secrets section above.
2. Over the existing SSH connection, stream those bytes via **stdin** into
   a tmpfs path on the instance — `/run/user/<uid>/cloudlab-ts-authkey`,
   mode 0600. RAM-backed, never touches the VM's disk, never appears as a
   literal argument in any command run to create it.
3. Remote-side, in one shell invocation:
   ```bash
   trap 'rm -f "$f"' EXIT
   tailscale up --auth-key=file:"$f"
   ```
   `--auth-key=file:<path>` (confirmed via `tailscale up --help` against
   the live test instance) means the key never appears in `tailscale`'s
   own argv either, so it's invisible to `ps` on the remote box too. The
   `trap` guarantees the tmpfs file is removed whether `up` succeeds or
   fails.
4. Locally, zero the `[]byte` from step 1 immediately after step 2's write
   completes.
5. On success, set `TailscaleJoined = true` on the instance's
   `state.Record` and persist it (see Config section above).

No `--hostname` flag needed: DigitalOcean already sets the instance's OS
hostname from `godo.DropletCreateRequest.Name` (`spec.Name`, i.e. the
cloudlab instance name) — confirmed both in
`internal/provider/digitalocean/digitalocean.go:131` and empirically
(`uname -a` on the live test instance already reports its cloudlab
instance name). `tailscale up`'s default (OS hostname) is already correct.

Re-running `tailscale up --auth-key=...` against an already-joined node is
safe — `--auth-key` is explicitly exempted from the "complete set of
desired settings" restriction `tailscale up --help` documents for its
other flags, so this needs no special idempotency handling.

## Teardown: `cloudlab down`

In `internal/lifecycle/down.go`, before `p.Destroy` (once the VM is gone,
nothing can run on it — order matters), best-effort SSH in and run
`tailscale logout` — same style as the existing `terminateWatch`: errors
are swallowed, never blocking VM teardown. Skipped entirely if
`record.TailscaleJoined` is false (nothing to leave — see the Config
section above for why this reads from state, not `cloudlab.pkl`).

This is deliberately not the only cleanup mechanism. As a safety net for
paths where that logout never runs (VM killed outside cloudlab, `down`
failing partway through, etc.), setup documentation should tell users to
generate the auth key as **Ephemeral** in the Tailscale admin console —
ephemeral nodes auto-remove themselves after going offline, so the device
list self-heals even without cloudlab's explicit cleanup. This addresses
the original motivation directly: free-tier Tailscale accounts have a
device limit, and ephemeral cloud instances must not accumulate stale
entries against it.

## Command: `cloudlab pair [name]`

Standalone command, **not** part of `up` at all — pairing is a deliberate,
interactive, one-phone-at-a-time action, not something that should fire
automatically every time an instance comes up.

Execs `moshi-hook host setup --host <instance's public IP, from
state.Record>` over SSH with a PTY (`-t`), full stdio passthrough — the
same thin-client pattern already established for `ssh`/`herdr`/`tmux`
(exec the real binary, no cloudlab-side rendering/PTY logic of its own).
Blocks in the foreground showing the QR until scanned/claimed, or until
the user Ctrl+C's out — no timeout handling on cloudlab's side, since it's
a pure passthrough.

Independent of the `tailscale` config toggle: always uses the known public
IP. Tailscale is an optional privacy/reachability layer for *SSH*, not a
dependency for moshi's own SSH/Mosh pairing, which already works over the
public IP today. (A later enhancement could prefer the Tailscale address
when joined, but nobody has asked for that yet — YAGNI.)

No `secrets.yaml` involvement — `host setup`'s own QR/claim flow generates
and exchanges its keys itself; cloudlab supplies no pre-shared secret.

## Command: `cloudlab secrets <subcommand>`

Global commands — unlike `tailscale`/`pair`, these never touch an
instance or `state.Record`; `secrets.yaml` is purely local to the
developer's machine.

- **`cloudlab secrets init --age <recipient> [--age <recipient>...]`** —
  creates `~/.config/cloudlab/secrets.yaml`, encrypted for the given
  recipient(s) (an age public key or a YubiKey-plugin recipient like
  `age1yubikey1...`). Repeatable so a YubiKey plus a backup key both work
  — losing the hardware key shouldn't mean losing the secrets. Fails if
  the file already exists (`edit` is how you change it after that).
- **`cloudlab secrets edit`** — thin passthrough exec of
  `sops ~/.config/cloudlab/secrets.yaml`, full stdio passthrough (same
  thin-client pattern as `ssh`/`herdr`/`tmux`/`pair`). sops itself
  handles the decrypt → `$EDITOR` → re-encrypt cycle; no cloudlab-side
  parsing or temp-file handling.
- **`cloudlab secrets keys`** — lists key *names* only (e.g.
  `tailscale_authkey`), never values. sops's YAML encryption leaves keys
  in cleartext (only values become `ENC[...]`, confirmed against the
  same real encrypted file above) — so this reads the file directly and
  lists its top-level keys, no decryption needed at all for this
  command specifically.

## Error Handling

- **Missing/incomplete secrets.yaml** when `tailscale = true`: fail with a
  specific error naming the expected path and key, not a raw `sops`
  stderr dump.
- **Missing `sops`/`age-plugin-yubikey` on PATH**: `exec.LookPath` check
  first, same precedent as `Herdr`/`Tmux`'s "not found on PATH" handling.
- **`tailscale up` failure on the remote** (bad/expired key, network
  issue): surfaced as-is from the remote command's exit status/output —
  no special retry logic.
- **`cloudlab down`'s `tailscale logout`**: always best-effort, never
  fails the overall `down`.
- **`cloudlab pair`**: exit codes and stdio pass straight through from
  `moshi-hook`, same as `cloudlab ssh` today.
- **`cloudlab secrets init`** when the file already exists: clear error
  telling the user to use `edit` instead, not an accidental overwrite.

## Testing

- Argv/script builders for the tailscale join mechanics: pure-function
  tests asserting the exact remote shell snippet and tmpfs path, same
  style as `TestSSHArgs_BuildsExpectedCommand`.
- The decrypt-and-zero helper: unit tested with a fake/small in-memory
  secrets source rather than a real `sops` invocation, asserting the byte
  slice is zeroed after use.
- `cmd/lookup_test.go`'s existing table-driven tests extend to cover
  `tailscale [name]` and `pair [name]` resolving/erroring the same way
  every other entry does.
- `lifecycle_test.go`'s existing `Steps`-substitution pattern extends with
  a fake `JoinTailscale`, verified called (or not) based on
  `cfg.Tailscale`.
- No new test for the interactive PTY passthrough itself (`cloudlab
  pair`'s exec, `cloudlab secrets edit`'s exec), same precedent as
  `Herdr`/`Tmux`/`SSH`.
- `cloudlab secrets init`'s file-exists check and argument parsing
  (`--age` repeatable, at least one required): plain unit tests, no
  sops invocation needed to test the guard itself.

## Not Built (deliberately)

- No `--hostname` flag on `tailscale up` — already covered by
  DigitalOcean's own hostname behavior.
- No pre-shared secret or `secrets.yaml` entry for moshi pairing —
  `host setup` doesn't need one.
- No preference for the Tailscale address in `cloudlab pair` — always the
  public IP, regardless of the `tailscale` toggle.
- No `cloudlab tailscale --leave`/manual-disconnect command — leaving is
  handled automatically and only by `down`.
- No `cloudlab secrets rotate`/`list` — unlike `aide secrets`, cloudlab
  manages exactly one fixed secrets file, not an arbitrary named
  collection, so there's nothing to list and no recipient-rotation
  workflow to build until multiple files actually exist.
- No YubiKey auto-detection for `secrets init` — recipients are always
  explicit `--age` flags.
