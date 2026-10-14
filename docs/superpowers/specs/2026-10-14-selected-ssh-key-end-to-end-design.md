# Use the selected SSH key end to end

Status: approved by the user on 2026-10-14
Date: 2026-10-14
Issue: https://github.com/jskswamy/bivouac/issues/1
Tracks: cloudlab-1em (implementation epic; children mirror plan Tasks 1–10)

## Intent

The user selects a public key and bivouac uses that key for the complete
instance workflow. The user does not find, type, or maintain fingerprints.
Bivouac handles provider identifiers internally, registers missing public
keys with permission, and uses the matching local signing capability for
provisioning and subsequent connections.

The user explicitly clarified that authentication is the point of selecting
an SSH key, not an optional addition to public-key registration. This is not
a general-purpose account key manager.

## Requirements confirmed by the user

- Select a public key rather than manage its fingerprint.
- Accept public-key paths only in `sshKeys`. Remove fingerprint and numeric
  key-ID inputs; existing entries fail with a clear migration message so
  the user can replace them with the actual public-key file path.
- Remember the choice and use it for both instance creation and login.
- Cover provisioning and later connections, not only `up`.
- Retain the issue's confirmation, actionable-error, and add-only guardrails.
- Use one bivouac-owned SSH host alias per machine, shared by its sessions.
  Keep entries in a separate managed file and reference it through one
  marked `Include` in the user's SSH config, preserving existing settings.

The following detailed policies are proposed design calls, not additional
user decisions. Approval of this document approves these defaults.

## Current implementation and the gap

- `cmd/up.go`: `ensureSSHKeys` accepts a non-empty list without checking it.
  `upSummary` does not show keys or registration actions.
- `internal/lifecycle/lifecycle.go`: `Up` resolves config again and sends
  raw `sshKeys` to `InstanceSpec`.
- `internal/provider/digitalocean/digitalocean.go`: numeric strings become
  IDs; every other string becomes a fingerprint. A path therefore cannot
  currently be sent through this boundary unchanged.
- `internal/sshkeys/sshkeys.go`: discovery already reads agent keys and
  `~/.ssh/*.pub`, deduplicates, and computes DigitalOcean-compatible MD5
  fingerprints. It does not retain a public-key file path in `Key`.
- `internal/provider/digitalocean/keys.go`: paginated listing and creation
  already exist behind `provider.KeyRegistry`.
- `cmd/init_keys.go`: the wizard already checks account keys when available,
  prompts for individual uploads, and handles create races by re-listing.
  It stores fingerprints and can upload before `up`'s final confirmation.
- `internal/reconcile/ssh.go`: Go SSH tries agent keys and a fixed list of
  default private-key files. Selecting an arbitrary `.pub` does not select
  its corresponding signer.
- External SSH, rsync, git session transport, and herdr have separate paths.
  Fixing Go SSH alone does not fulfill this requirement.

### Changes to existing design contracts

This design supersedes the fingerprint-only storage and rejection of
checking every `up` in
`docs/superpowers/specs/2026-09-13-ssh-key-discovery-design.md`.

It also intentionally changes the GitHub issue's proposed backward
compatibility: the user subsequently chose to remove fingerprint inputs
rather than retain both workflows. Numeric account IDs are removed too.

It preserves that design's credential-free setup, explicit account-write
consent, smartcard support, and race-safe registration. The old design's
per-key upload prompts are not used during `up`; all account additions
belong in the final creation confirmation.

Presets remain project shapes and do not start storing personal SSH keys.
The issue's `init`/`preset` wording means that any setup path asking for
personal keys uses the shared check, not that `preset edit` gains a personal
key-management question.

## User-facing configuration and selection

Keep `sshKeys` as a string listing:

```pkl
sshKeys {
  "~/.ssh/id_ed25519.pub"
  "~/.ssh/id_rsa_yubikey.pub"
}
```

- Public-key file paths are the only accepted entries. Do not accept inline
  key material, fingerprints, numeric account IDs, or agent identity tokens.
- Expand `~/`; resolve relative paths against the config file declaring
  them, not the process working directory. Preserve base/project origins
  through their additive merge. Do not expand arbitrary shell expressions.
- Recognize old numeric IDs and fingerprint formats explicitly and reject
  them with the migration guidance below, not a misleading file-not-found
  error. Recognize both MD5 and SHA256 fingerprint forms.
- Hardware/agent-backed keys use a public-key file too; their private half
  may remain entirely on the hardware device or in the agent. If only the
  agent currently exposes a key, the user exports its public material to a
  `.pub` file and selects that path. Discovery can help identify it, but
  must not write another fingerprint-like reference into config.
- Preserve original path/reference and declaring config for diagnostics.
  Fingerprints may appear in diagnostic detail, but are not required input
  in the normal workflow.
- Display disk keys by filename, with comment and agent availability as
  useful detail. Deduplicate identical material across disk and agent;
  keep an available disk path rather than losing it to agent-first dedupe.
- An explicitly selected missing, unreadable, malformed, or private-key
  file is an error, not a discovery entry to silently skip. A selected file
  must contain one public key; reject ambiguous multi-key files.

## Breaking change: migrate existing fingerprints and key IDs

**Fingerprint and numeric key-ID inputs are removed.** This is the user's
explicit decision, not a deprecation period. Provider fingerprints remain
an internal implementation detail for lookup and droplet requests.

Validate entries during local config resolution, before provider lookup,
account writes, or droplet creation. A fingerprint remains invalid even if
already registered in DigitalOcean or matched by local discovery. Do not
silently accept, convert, or drop it; the user replaces the config entry.
A mixed path/fingerprint list fails as a whole with migration guidance.

The error identifies each unsupported entry and the config declaring it,
and shows the replacement form, for example:

```text
sshKeys entry "10:3e:47:…" in ~/.config/bivouac/base.pkl is a fingerprint.
sshKeys now accepts public-key file paths only.
Replace this entry with the matching public key, for example:
  sshKeys { "~/.ssh/id_ed25519.pub" }
Or run `bivouac init` to select a public-key file.
```

An example path is a placeholder, not a guessed match. Local discovery may
suggest a verified matching file, but may not rewrite config automatically.
The setup flow must allow replacing invalid entries even though normal
config resolution rejects them; it must not be blocked by the migration
error it is meant to remedy. Keep this recovery local and credential-free.

Already-registered keys are reused when their public-key paths are supplied;
there is no need to delete or re-import account keys just to migrate config.
Numeric IDs receive the same guidance, without implying an ID contains key
material.

Old instance state without identity metadata retains its connection fallback
behavior where no config resolution is needed. Operations that resolve an
old config receive the migration error until it is updated. This fallback
is state compatibility, not continued support for fingerprint config inputs.

## One shared planning model

Separate read-only planning from account writes and VM creation.

The plan records original references, normalized public material, internal
provider fingerprint/ID, registration state, proposed account name, and
local authentication references. Private bytes and live signers are never
serialized into a plan or state.

1. Resolve the selected public-key files; reject unsupported legacy inputs.
2. Find matching local signing capabilities: selected file's private
   counterpart and/or a matching SSH-agent key.
3. List the account's keys using the same provider resolved for `up`.
4. Deduplicate by public-key identity and classify:
   - registered: use existing identifier; leave name unchanged;
   - missing with available public material: propose registration;
   - missing with no public material: fail with an actionable error.
5. Validate local authentication availability before any account write or
   droplet creation. A public file alone is not proof of a usable signer.

### Authentication validation

For a path entry, strip `.pub` to locate the sibling private file; verify
its public identity matches the selected public key. Prefer a matching
agent signer, supporting hardware keys and encrypted identities without
reading a private file. A plaintext private key may be read locally when
needed; it never leaves the machine.

An encrypted private file without a matching agent key produces an error
with `ssh-add <private-path>` as the fix. Do not add a new private-key
passphrase store or silently generate keys. Agent signing may require a
hardware touch: show progress before any signing check, honor cancellation,
and do not silently wait during credential-free setup.

A creation plan requires at least one selected key usable for local login.
Additional selected public keys may authorize other people or machines;
they need not all have local private counterparts. If none is usable, stop
and name the selected references and remedies. An old config must first
be migrated to public-key paths; it does not bypass these checks.

This validates local capability, not a guarantee that a future network,
agent, hardware device, or sshd will succeed. Preserve existing readiness
and connection error handling for those failures.

## Confirmation and execution

Before the existing `Proceed? [y/N]` prompt, show selected names, whether
they are registered or will be registered, and any authentication caveat
for extra keys that are not usable locally.

Proposed names are `bivouac-<public-file-stem>`, including public files for
hardware-backed keys. Do not rename existing keys to impose the prefix.
Colliding names must not cause replacement.

Declining confirmation makes no account-key or droplet writes. In the
`up` path, missing-key selection and implicit init may persist config
choices but must not perform uploads before the final confirmation.

After approval:

1. Register only missing public keys listed in the plan.
2. On a create error, re-list and accept success if that fingerprint is
   present, regardless of account name. Otherwise report the failure and
   stop before droplet creation.
3. Create the instance with identifiers from the approved plan.
4. Persist the selected connection identities alongside instance state
   immediately, using the existing create/state rollback safety behavior.
5. Use those identities for readiness and provisioning.

Lifecycle must consume the approved resolved configuration/key plan;
re-reading raw paths and recreating a different key list is forbidden.
Check existing-instance and local creation prerequisites before account
writes where possible. Partial account registration is retained on failure;
explain it, retry idempotently, and never delete newly registered keys.

## Setup, permissions, and errors

`init` validates selected files and available authentication locally. If
credentials are already available, it uses the shared account check. It
may offer explicit registration when run on its own; it must not do so
when invoked as part of `up` before the final creation confirmation.

Credential-free setup remains supported: save the selection and clearly
say account verification is deferred until `up`. Do not implicitly decrypt
sops credentials or wait for a YubiKey while presenting setup choices.

`up` must obtain an authoritative account listing. A read-permission failure
is not an empty account and not permission to attempt a droplet anyway.
Report the needed SSH-key read permission and stop early. Missing keys need
create permission; failures name the key and offer manual import or a token
with permission. Never claim to have registered a key after an API error.

A file-resolution error includes the original path, declaring config, and
how to supply a valid public-key file. Legacy inputs receive the migration
error above, not an import instruction that implies they remain supported.
For account registration failures, show an appropriate
`doctl compute ssh-key import` command using the resolved public-key path
and link to DigitalOcean's account security page. Quote paths safely.

## Connection identity is instance state

Persist absolute public/private path references as applicable, public-key
identity, and agent-selection identity, but never private material,
passphrases, tokens, or transient socket paths. Reacquire agent signers
from the current agent for each command and match the stored identity.

Later config edits do not silently change which key authenticates to an
existing VM: its authorized keys were set at creation. Revalidate stored
references and fail clearly if a file disappears, its key changes, or the
agent no longer supplies the selected key. Do not silently fall back to
unrelated keys. Old instance records without identity metadata retain the
existing default behavior; new metadata is optional for state compatibility.

Apply one connection-identity abstraction across:

- Go SSH readiness, reconciliation, sessions, rescue, teardown, port and
  Tailscale operations (`internal/reconcile/ssh.go` and its callers).
- External SSH: shell/ssh, tmux, pair, and forwarding/connect commands.
- Rsync upload/download/sync, including paths containing spaces.
- Local git push/fetch/ls-remote and fallback URLs for session and beads
  transport, preserving timeout and batch-mode behavior.
- Herdr direct remote attach and saved-machine connections, including
  connections initiated later by herdr rather than this process.

Filter agent keys to the selected identities and constrain external SSH
identity selection rather than merely appending `-i` while unrelated agent
keys exhaust MaxAuthTries. Preserve existing host-key verification and
forwarding policy. Do not change authentication for unrelated GitHub
remotes, user repositories, or globally configured SSH hosts.

Ordinary `git fetch bivouac-<session>` must also keep working with the
selected non-default identity; a process-only git override is insufficient.
Any persistent transport configuration must be scoped to bivouac-owned
remotes/hosts and must not overwrite user-owned SSH configuration.

### Shared OpenSSH aliases: feasibility investigation resolved

Herdr 0.9.4 source (`herdrdev/herdr`, tag `v0.9.4`,
`src/remote/attach.rs` and `src/remote/saved.rs`) launches the external `ssh`
binary for direct and saved-machine connections. Its managed temporary
config includes the user's SSH config before its fallback settings. No
invented identity-file flag or upstream change is necessary.

The user accepted the shared alias approach after discussing multiple
machines and sessions:

- Each machine has a stable `bivouac-<instance>` alias. All its sessions
  share that alias and differ by remote repository/session path.
- `~/.config/bivouac/ssh/config` (under the applicable XDG config root)
  contains only bivouac-owned host entries: address, user, selected identity
  paths, and `IdentitiesOnly yes`.
- One marked include in `~/.ssh/config` references this managed file. Add
  it only after confirmation; preserve user content and reset `Host *`
  scope before the original content so an enclosing Host/Match stanza
  cannot accidentally restrict the include.
- New machines add entries, new sessions reuse entries, session deletion
  leaves entries alone, and successful machine destruction removes only
  that machine's owned entry. Keep the shared include for other machines.
- Recreating a machine updates its address without disabling host-key
  verification. Use the real endpoint's known-host identity consistently
  across Go SSH and OpenSSH rather than trusting an old alias blindly.
- Use the alias in persisted Git remotes and Herdr targets, so identities
  remain available after bivouac exits. Preserve public/tailnet routing and
  explicit fallback behavior; an alias is not itself a resolvable IP.

A local `ssh -G` check with isolated temporary config verified alias,
identity-path quoting, and inclusion behavior; no live VM/Herdr connection
has been tested yet. Integration verification remains part of implementation.
Existing broad user IdentityFile directives can add unrelated identities
(OpenSSH treats them additively). Check effective settings and report
conflicts rather than silently weakening selected-key isolation or rewriting
user settings. Persistent files must be safely quoted, private-permissioned,
atomically updated, and protected against concurrent machine updates.

## Non-goals and guardrails

- No account key deletion, renaming, replacement, or cleanup on `down`.
- No key generation, private-key upload, private-key copying to VMs, or
  passphrase persistence.
- No fingerprint management workflow, personal keys in presets, or broad
  user SSH-configuration management.
- No automatic key rotation on existing instances.
- No new requirement that hardware-backed identities have private files.

## Verification contract

Tests come before implementation. Use temp public/private fixtures, fake
agents and SSH servers, fake registries, HTTP-backed provider tests, and
recording external-command adapters; no real cloud account is required.

Cover:

- Path selection is stored as a path and round-trips through Pkl; relative
  base/project paths retain declaring-file origin and tilde expansion.
- Known internal fingerprint fixtures, custom filenames, public files for
  agent/hardware-backed keys, deduplication, and malformed/private/multi-key
  input rejection.
- Registered and missing-but-local account states, missing/unknown selected
  files, forbidden read/create, and paginated listing.
- MD5/SHA256 fingerprints and numeric IDs fail locally with migration
  guidance even when registered or locally matched. Mixed lists fail without
  provider calls or writes; credential-free init can replace old entries.
- The summary lists pending additions; decline causes zero remote writes;
  acceptance registers before create; registration races are idempotent.
- `up`'s implicit setup never registers before its final confirmation.
- Matching custom private files and matching agent/hardware signers work;
  mismatched keys and encrypted keys without an agent fail before creation.
- Multiple keys require at least one usable local signer, not every key.
- A fake SSH server accepting only the selected non-default key proves
  readiness and provisioning authenticate with that key.
- Later commands use recorded identities even after config edits; changed
  files, missing agent keys, and legacy state have specified behavior.
- External SSH/rsync/git arguments or scoped transport configuration use
  selected identities with correct quoting, timeouts, and host-key policy.
- Ordinary git fetch and herdr's persistent saved-machine transport retain
  the identity. Document any unavoidable external integration test and do
  not substitute argument-only tests for an unverified transport guarantee.

Run the Go suite redirected to a file, pre-commit checks, and flake checks
in the development environment during implementation. This spec changes
only documentation; it does not claim any of these behaviors are built.

## Builder discretion

The builder chooses helper/type names, state-field layout, and
signature-threading strategy. Reuse `sshkeys`,
`KeyRegistry`, registration race handling, and existing command builders.
Do not introduce a separate SSH implementation for each transport.

The shared-alias transport choice is resolved above. Any change to the
confirmed end-to-end requirement returns to the user for approval.
