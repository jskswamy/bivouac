# Selected SSH Key End-to-End Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Select a public-key file once and use its matching identity for DigitalOcean creation, provisioning, and every later instance connection, rejecting old fingerprint inputs with migration guidance.

**Architecture:** Preserve public-key paths and their declaring-file origins during config resolution. Separate read-only key/creation planning from confirmed execution; carry selected identity metadata in instance state and request context. A shared SSH-identity package supplies Go signers and persistent, machine-scoped OpenSSH aliases used by SSH, rsync, Git, and Herdr.

**Tech Stack:** Go 1.26.9, Pkl/pkl-go 0.14.0, x/crypto/ssh and agent, existing godo KeyRegistry, OpenSSH, rsync, Git, Herdr 0.9.4; Darwin and Linux.

**Spec:** `docs/superpowers/specs/2026-10-14-selected-ssh-key-end-to-end-design.md`

## Global Constraints

- `sshKeys` accepts public-key file paths only; reject MD5/SHA256 fingerprints and numeric account IDs before provider lookup. Do not automatically rewrite old entries.
- Relative paths use the declaring config's directory; expand `~/`, not shell expressions. Base/project lists merge additively.
- No account writes or droplet creation before the final `up` confirmation. Implicit init must defer registration.
- Only add account keys, prefix new names with `bivouac-`, and never rename, replace, or delete them, including on `down`.
- At least one selected identity must be locally usable. Extra recipient public keys need not have local private counterparts.
- No private-key upload, generation, VM copying, or passphrase persistence. Hardware keys work through a matching agent and public-key file.
- Credential-free init remains available; no implicit sops decryption or silent hardware wait while selecting keys.
- Selected identities and instance-specific aliases persist in state. Config edits do not switch authentication to a key the VM never received.
- One alias per machine, shared by sessions; one marked SSH-config include. Preserve host-key checks, user config, forwarding policy, and unrelated Git remotes.
- Old state without identity metadata keeps existing connection behavior. Unsupported old config inputs still produce migration errors when resolved.
- All Go test commands redirect to a file, never pipe; read the resulting file separately. Work inside `nix develop`/direnv.
- Commit subjects are capitalized imperative, at most 50 characters, no `type:` prefix or AI co-author trailer.

## Review Focus

1. A broad user `Host *`/`Match` stanza or extra IdentityFile must not leak unrelated identities or change destinations; Task 4 checks effective `ssh -G` settings and scope reset.
2. A public file may change after confirmation or after VM creation; Tasks 2, 5, and 6 revalidate identity, never silently substitute it.
3. Two independent `up`/`down` processes must not lose each other's aliases or edit user content; Task 4 covers process locking, atomic publication, and retries.
4. Alias names are not DNS addresses, and public/tailnet fallbacks must retain the selected key; Task 8 tests endpoint resolution, probes, and fallback transport.
5. Old project fingerprints may remain when the user declines moving personal fields; Task 3 makes migration reachable for both original files, rather than only base.pkl.

## Investigation completed

Herdr 0.9.4 source at `https://github.com/herdrdev/herdr/tree/v0.9.4`:
`src/remote/attach.rs` (`RemoteSsh::base_command`, `write_managed_ssh_config`)
launches external OpenSSH and includes the user's SSH config before fallback
settings. `src/remote/saved.rs` uses this same mechanism for persistent saved
machines. No identity-file CLI flag is needed. The user accepted a managed
config/include and one alias per machine.

An isolated `ssh -G -F <temporary-user-config> bivouac-project-a` check
confirmed the alias's destination, user, quoted custom identity path, and
`IdentitiesOnly yes`. This is configuration evidence, not a claim of live
Herdr or VM authentication; Tasks 7–9 must test transport behavior.

## Beads remote-session handoff

Epic: **cloudlab-1em** (GitHub issue #1). Start the remote session with
`--issue cloudlab-1em`; read the spec and plan, then use
`bd ready --parent cloudlab-1em` to select the next unblocked task.

| Plan task | Beads ID | Prerequisites |
|---|---|---|
| 1 | cloudlab-1em.1 | None |
| 2 | cloudlab-1em.2 | 1 |
| 3 | cloudlab-1em.3 | 1, 2 |
| 4 | cloudlab-1em.4 | 2 |
| 5 | cloudlab-1em.5 | 1, 2 |
| 6 | cloudlab-1em.6 | 3, 4, 5 |
| 7 | cloudlab-1em.7 | 6 |
| 8 | cloudlab-1em.8 | 7 |
| 9 | cloudlab-1em.9 | 8 |
| 10 | cloudlab-1em.10 | 9 |

Related existing route/host-key work is linked to the epic for context,
not as blockers: cloudlab-91x.1, cloudlab-91x.3, cloudlab-dne, cloudlab-51x.
Check its current state before overlapping changes; do not silently close
or broaden those issues.

The operator must commit the spec, plan, and matching release-note entry
before starting the session: bivouac seeds committed HEAD, not untracked
documents. The remote agent may lack local hardware keys, cloud credentials,
and GitHub access. Unit tests must use isolated fixtures; live cloud writes
require explicit operator consent. Commit deliberately and never push.

## File and dependency map

- `internal/sshkeys/files.go`: strict public-file reading and entry classification; extend discovery's `Key` with its disk path. No provider/config/state dependencies.
- `internal/config/sshkeys.go`: declaring-file origins and migration errors; `Resolved.SSHKeyFiles` preserves references. `ReadValues` stays permissive for recovery.
- `internal/sshidentity/{identity,agent,context,openssh,profiles}.go`: serializable identity descriptors, current signers, context scoping, OpenSSH profile lifecycle and effective-settings verification. No config/provider/state imports.
- `internal/keyplan/plan.go`: account matching, pending registrations, race-safe execution; consumes sshkeys/sshidentity and provider.KeyRegistry.
- `internal/state/state.go`: optional `SSH *sshidentity.Connection` field.
- `cmd/init_keys.go`, `internal/wizard/keys.go`, `cmd/prompt.go`: public-path selection, free-text path input, migration recovery, explicit upload policy.
- `internal/lifecycle/up_plan.go`, `lifecycle.go`, `cmd/up.go`: creation prerequisites, approved key plan, final confirmation, persisted identity and provisioning.
- Existing Go/external transport files and command boundaries: consume scoped connection identity; do not build another independent auth policy.
- `docs/config.md`, README, RELEASE_NOTES, and approved spec: migration and completed behavior documentation.

This is one plan: config, registration, authentication, and transport depend
on the same identity contract and cannot be shipped as independent features.
Each task below is a reviewable test cycle; the feature is complete only
after all tasks pass.

### Task 1: Public-file config and migration diagnostics

**Files:** Create `internal/sshkeys/files.go`, `files_test.go`, `internal/config/sshkeys.go`, `sshkeys_test.go`; modify `internal/sshkeys/sshkeys.go`, `internal/config/config.go`, `resolved.go`, `Config.pkl`; update existing config tests that use arbitrary key/fingerprint placeholders.

**Interfaces:**
- `sshkeys.FileRef { Entry, ConfigPath, Path string }` (original entry, declaring file, absolute path).
- `sshkeys.UnsupportedReference(entry string) bool` recognizes numeric IDs and bare/prefixed MD5 or SHA256 fingerprints; explicit filesystem paths are not rejected merely because their basename is numeric.
- `sshkeys.ReadPublicFile(ref FileRef) (Key, error)`; `Key` gains `Path string` and retains canonical public material, comment, and internal MD5 fingerprint.
- `config.Resolved` gains `SSHKeyFiles []sshkeys.FileRef`.
- `config.resolveSSHKeyFiles(configPath string, entries []string) ([]sshkeys.FileRef, error)` normalizes origins and joins migration errors.
- `config.ReadValues` does not apply migration validation; `Resolve` does, before any provider is resolved.

- [ ] **Write failing tests:** `TestResolveSSHKeys_DeclaringFileOrigins` asserts base `keys/base.pub` and project `keys/project.pub` resolve under different directories in base-first order. `TestResolveSSHKeys_RejectsLegacyReferences` covers full valid MD5/SHA256 and numeric IDs, mixed lists, and errors containing original entry, declaring file, `public-key file paths only`, and a replacement example. `TestReadValues_LegacyKeysRemainEditable` proves raw values are readable. `TestReadPublicFile_StrictInput` rejects private files, multiple public keys, missing files, and empty input; one valid public key preserves its canonical identity and filename. Test `~/`, `./12345`, and paths with spaces.
- [ ] **Run red:** `go test ./internal/config ./internal/sshkeys > /tmp/bivouac-task1.log 2>&1`. Expect new assertions to fail for migration/origins, not unrelated Pkl setup.
- [ ] **Implement:** Resolve per-file references before additive merge loses origin; populate the new resolved field without changing raw config values. Keep schema `Listing<String>?` so recovery can evaluate old configs; document path-only semantics and enforce them in Go. Reject unsupported references locally; use `ssh.ParseAuthorizedKey` and reject any remaining second key/non-comment content.
- [ ] **Run green:** repeat the command, read the log, require PASS. Keep required-field errors and optional empty-key behavior unchanged.
- [ ] **Commit:** `Accept public-key paths in SSH configuration`.

### Task 2: Select and revalidate actual local signing capabilities

**Files:** Create `internal/sshidentity/identity.go`, `agent.go`, `context.go`, `identity_test.go`, `agent_test.go`; extend `internal/sshkeys/sshkeys_test.go` for path-preserving discovery deduplication.

**Interfaces:**
- `sshidentity.Identity { PublicPath, PrivatePath, Fingerprint string; Agent bool }`; `Fingerprint` is stable public identity, not user input; `Agent` records the selected auth mode, not a socket path.
- `sshidentity.Connection { Name, Alias, PublicIP, PreferredHost, User string; Keys []Identity }` is JSON-safe; `PreferredHost` initially equals PublicIP.
- `sshidentity.Select(ctx context.Context, keys []sshkeys.Key) ([]Identity, error)` requires at least one usable signer, returning only locally usable identities. Extra public keys remain in the account plan.
- `sshidentity.Signers(ctx context.Context, identities []Identity) ([]ssh.Signer, func(), error)` returns matching current signers plus connection cleanup; never offers unrelated agent keys.
- `sshidentity.WithConnection(ctx context.Context, c Connection) context.Context`, `FromContext(ctx context.Context) (Connection, bool)` scope metadata without globals.

- [ ] **Write failing tests:** fake agent holding selected/unrelated keys returns only selected signers; fake signer rejects challenge => unusable. Custom `my_key.pub` uses matching `my_key`; mismatch never authenticates. Encrypted file without matching agent produces `ssh-add` guidance. Hardware/agent-backed public file works with no private file. Two selected public files, one usable => success; neither => error naming paths. Changed public/private files or missing recorded agent key => clear error, not default-key fallback. Verify connection JSON contains no private bytes, passphrase, or socket path; cleanup closes agent IPC.
- [ ] **Run red:** `go test ./internal/sshidentity ./internal/sshkeys > /tmp/bivouac-task2.log 2>&1`; require failures of the new cases.
- [ ] **Implement:** Match public material before accepting signers; prefer matching agent, otherwise the unencrypted sibling of a `.pub` file. Use a local challenge/verification when planning authentication; report progress before hardware signing and honor context cancellation/IPC deadlines. Retain descriptor identity for later revalidation; do not persist signer objects or secrets.
- [ ] **Run green:** repeat and inspect log. Add cancellation and descriptor mutation tests before committing.
- [ ] **Commit:** `Resolve selected keys to local SSH signers`.

### Task 3: Path-based wizard and reachable migration recovery

**Files:** Modify `cmd/init_keys.go`, `init.go`, `prompt.go`, corresponding tests, and `internal/wizard/keys.go`, `keys_test.go`.

**Interfaces:**
- `keySetupPolicy` constants `keySetupStandalone`, `keySetupForUp`.
- `askSSHKeys(ctx context.Context, out io.Writer, p prompter, sources keySources, current []string, policy keySetupPolicy) ([]string, error)` returns file paths, not fingerprints.
- `runInitFlowWith(ctx context.Context, out io.Writer, p prompter, root string, keys keySources, policy keySetupPolicy) error`; public `init` passes standalone, implicit `up` passes for-up.
- Wizard `KeyChoice` gains `Value string` (path); `Fingerprint` remains internal matching only. Free-text input is a public-file path. Retire account-only ID/fingerprint choices and sentinel wording advertising them.

- [ ] **Write failing tests:** `TestAskSSHKeys_StoresSelectedPath` checks generated `base.pkl` contains the selected `.pub` path, not its fingerprint. No credentials => setup succeeds with deferred-verification text. Standalone confirmed upload uses `bivouac-<stem>`; for-up => zero CreateKey calls. `TestInit_ReplacesLegacyKeysInOriginalFiles` covers base and project fingerprints, including declining the move-personal-fields prompt. Raw invalid values are not carried through "use existing" or read-back unnoticed; user explicitly chooses replacements. Agent-only discovery explains public-file export instead of storing an agent reference. Empty selection warns during init, and up continues to refuse it.
- [ ] **Run red:** `go test ./cmd ./internal/wizard > /tmp/bivouac-task3.log 2>&1`; inspect failures.
- [ ] **Implement:** Preserve disk paths when agent/disk identities duplicate. Validate manually entered paths as strictly as choices. Detect migration entries in `ReadValues` and explicitly re-ask keys in the declaring file before normal `Resolve`; keep unrelated settings and user decisions intact. Replace fingerprint-specific warning/help copy. Thread upload policy through implicit setup. Account lookup remains optional and env-token-only in standalone setup.
- [ ] **Run green:** repeat and inspect log; update wizard fixture assertions to paths, not fake fingerprints.
- [ ] **Commit:** `Select public-key files in the setup wizard`.

### Task 4: Persistent machine-scoped OpenSSH profiles

**Files:** Create `internal/sshidentity/openssh.go`, `profiles.go`, `openssh_test.go`, `profiles_test.go`; use existing XDG helpers; promote existing `golang.org/x/sys` to direct dependency if needed for advisory locks.

**Interfaces:**
- `sshidentity.Alias(name string) (string, error)` yields `bivouac-<valid-instance-name>`.
- `sshidentity.ProfileStore { ConfigDir, SSHDir string }` keeps tests off real user files.
- `DefaultProfileStore() (ProfileStore, error)` respects XDG config and the user's home.
- `ProfileStore.Check(ctx context.Context, c Connection) error`: read-only preflight; uses temporary config and `ssh -G`.
- `ProfileStore.Upsert(ctx context.Context, c Connection) error`: confirmed write, atomic profile publication and one marked include.
- `ProfileStore.Remove(ctx context.Context, name string) error`: remove only the named owned profile, preserve include and other machines.
- `sshidentity.TransportHost(ctx context.Context, host, user string) (string, error)`: returns recorded alias only for its matching preferred endpoint/user; absent metadata preserves the original host.

- [ ] **Write failing tests:** two machines produce independent aliases/identity sets; repeated Upsert adds one include only. Existing bytes after the marked block stay unchanged; prepend include with `Host *` scope reset. Symlinked/unwritable user config fails with actionable guidance rather than replacing its target. Concurrent subprocess upserts/removes preserve every other machine. Interrupted update is retryable and never publishes half a Host stanza. Selected agent identity uses its public-file path as OpenSSH IdentityFile; disk signer uses private path. Reject newline/control/path directive injection and quote spaces/percent expansion safely. Existing explicit unrelated IdentityFile causes preflight conflict; do not pretend `IdentitiesOnly` clears additive settings.
- [ ] **Run red:** `go test ./internal/sshidentity > /tmp/bivouac-task4.log 2>&1`; require new tests to fail.
- [ ] **Implement:** Render owned entries deterministically with actual host, user, identities, `IdentitiesOnly yes`, and real-endpoint `HostKeyAlias`. Lock shared publication using Darwin/Linux advisory locking with context-aware acquisition; use 0700 directories, 0600 files, temporary-write/rename, and preserve existing user file mode/content. Keep a recoverable owned profile index so retries regenerate a consistent managed config. Check effective hostname, user, selected identities, and host-key settings using isolated temporary includes of existing user/system config. Report conflicting user settings instead of rewriting them. No files are added by Check.
- [ ] **Run green:** repeat; require PASS including real `ssh -G` fixture checks. No fake config parser may substitute for effective OpenSSH evaluation.
- [ ] **Commit:** `Persist machine-scoped SSH identity aliases`.

### Task 5: Read-only account key plans and confirmed registration

**Files:** Create `internal/keyplan/plan.go`, `plan_test.go`; modify `internal/provider/digitalocean/keys.go`, `keys_test.go`; refactor shared registration behavior out of cmd without coupling the wizard to Cobra.

**Interfaces:**
- `keyplan.Entry { Key sshkeys.Key; Registered bool; AccountName string }`.
- `keyplan.Plan { Entries []Entry; Identities []sshidentity.Identity }` (only public material and serializable auth metadata).
- `keyplan.Build(ctx context.Context, registry provider.KeyRegistry, keys []sshkeys.Key) (Plan, error)` selects local signers and lists account keys, makes no writes.
- `keyplan.Register(ctx context.Context, registry provider.KeyRegistry, plan Plan) ([]string, error)` revalidates files/signers, registers pending entries, returns identifiers in deduplicated order.

- [ ] **Write failing tests:** registered key => no create; missing local key => pending `bivouac-<filename-without-.pub>`; duplicate paths/material => one pending create. Forbidden/read/network failures => not an empty account. Create race followed by matching fingerprint => success; failed create with no match => stop with path, name and safely quoted doctl import hint. Second registration fails => first remains, no deletion; retry is idempotent. Selected file changed after Build => no writes. Paginated listing includes later-page keys; malformed pagination is an error, not a partial authoritative list.
- [ ] **Run red:** `go test ./internal/keyplan ./internal/provider/digitalocean > /tmp/bivouac-task5.log 2>&1`; require new tests to fail.
- [ ] **Implement:** Use existing KeyRegistry; match internal MD5 identities and never rename account entries. Re-list on create failure and ask whether the fingerprint now exists. Canonical public material comes from the selected file. Unknown local files and absent signing capability fail before listing/writing; errors distinguish read vs create permissions. Fix ListKeys' current partial-success return on pagination metadata errors.
- [ ] **Run green:** repeat, inspect logs, require no accidental droplet or delete API calls in fixtures.
- [ ] **Commit:** `Plan and register selected account SSH keys`.

### Task 6: Approved creation plan, state, and one confirmation

**Files:** Create `internal/lifecycle/up_plan.go`, `up_plan_test.go`; modify `lifecycle.go`, lifecycle creation tests, `internal/state/state.go`, `state_test.go`, `cmd/up.go`, `up_test.go`, `up_confirm_test.go`.

**Interfaces:**
- `state.Record` gains `SSH *sshidentity.Connection` with JSON tag `ssh,omitempty`.
- `lifecycle.UpPlan { Name, ConfigPath, RepoRoot string; Config config.Resolved; Spec provider.InstanceSpec; Keys keyplan.Plan; Connection sshidentity.Connection }`.
- `lifecycle.PrepareUp(ctx context.Context, name, configPath, repoRoot string, cfg config.Resolved, keys keyplan.Plan) (UpPlan, error)` validates name/state/cloud-init/user/path prerequisites, makes no account/droplet/profile writes.
- `lifecycle.Up(ctx context.Context, p provider.Provider, steps Steps, plan UpPlan) error` replaces the raw config-path creation API; no second Resolve. Rechecks existing-instance state before registering.
- `upSummary(plan lifecycle.UpPlan) string` includes keys, pending account additions, alias/include action, and existing VM details.

- [ ] **Write failing tests:** declined summary => no CreateKey, droplet, or profile writes. Approved missing key => registration before droplet creation and selected identity persisted before readiness. Registration failure => zero droplets. Existing instance/local prerequisite failure => no account writes. State-write failure destroys the created VM using current rollback behavior. Profile-write failure after state persistence leaves the VM tracked and gives retry instructions; never reports fully up. Changing config after prompt does not change approved region/size/keys. Changing selected public material after prompt stops before writes. Legacy state round-trips without new fields.
- [ ] **Run red:** `go test ./cmd ./internal/lifecycle ./internal/state > /tmp/bivouac-task6.log 2>&1`; inspect failure reasons.
- [ ] **Implement:** resolve config/keys and provider before summary; Check profiles read-only using a documentation-only `192.0.2.1` destination until the provider assigns an address (`ssh -G` makes no connection). Include pending registrations and local include action in the existing final prompt. Up type-asserts KeyRegistry, registers approved keys, creates with resolved identifiers, fills Connection.PublicIP/PreferredHost from the returned VM, stores Connection, publishes profile, and invokes readiness/provisioning with scoped Connection. Retain late duplicate-state check and cleanup guarantees. Update fixtures currently declaring dummy fingerprints to real temporary public keys and recording registries.
- [ ] **Run green:** repeat and inspect output; preserve no-terminal refusal and existing duplicate-instance safety tests.
- [ ] **Commit:** `Create instances from approved SSH key plans`.

### Task 7: Go SSH and command identity propagation

**Files:** Modify `internal/reconcile/ssh.go`, `reconcile.go`, SSH tests; `cmd/run_instance.go`, `run_attach.go`, `run_session.go`, `run_transfer.go`, `run_ports.go`; `internal/lifecycle/down.go`, `cmd/list.go`, and record-based connection call sites found by `rg`.

**Interfaces:**
- Keep `reconcile.Connect(ctx context.Context, ip, user string) (*Client, error)`; when FromContext is present use Signers exclusively and verify endpoint/user scope; no unrelated default fallback.
- Change `cmd.resolveInstance(cmd *cobra.Command, name string) (*state.Store, state.Record, error)` to install record SSH metadata in `cmd.Context()` without resolving unrelated configs or touching signers. Update the helper's callers, including session lookup wrappers.
- Record-owning internal entrypoints (`Reconcile`, `Down`, per-instance list/status workers) call WithConnection themselves; contexts derived by progress/report/timeouts preserve identity.

- [ ] **Write failing tests:** fake SSH server accepts only a custom selected key while the agent holds >6 unrelated keys; Connect succeeds and only selected signers are offered. Changed/missing identity returns clear path/agent guidance. Absent metadata retains current default-auth tests. Host-key change remains rejected, unknown-host recording uses real endpoint, and forwarding stays optional. Two machines in one list/status operation use independent identities, never a mutable global/context inherited from the previous record. `down --force` can destroy without eagerly loading a missing signer, while ordinary rescue still requires authentication.
- [ ] **Run red:** `go test ./internal/reconcile ./internal/lifecycle ./cmd > /tmp/bivouac-task7.log 2>&1`; require new auth/context cases to fail.
- [ ] **Implement:** connect with selected signers, close agent resources after handshake, preserve existing host-key logic and readiness cancellation. Audit every `reconcile.Connect`, state lookup, and per-record connection; bind metadata at ownership boundaries, not a process-global cache. Standalone provision derives Connection from state even when config changed.
- [ ] **Run green:** repeat; require existing rescue, provisioning, forwarding, and host-key tests plus new selected-key server tests to pass.
- [ ] **Commit:** `Use recorded identities for Go SSH connections`.

### Task 8: External SSH, rsync, and persistent Git transport

**Files:** Modify `internal/lifecycle/ssh.go`, `tmux.go`, `pair.go`, `connect.go`, `rsync.go`, `gitlocal.go`, `start.go`, `rescue.go`, `sessioninfo.go`, `beads.go` and matching tests; add `internal/lifecycle/transport.go`, `transport_test.go` for endpoint-aware Git fallback.

**Interfaces:**
- Existing argv builders remain pure; their runtime wrappers pass TransportHost's result rather than raw IP for new records.
- `lifecycle.instanceGitURL(ctx context.Context, user, address, path string) (string, error)` emits alias-based URLs for recorded identities; legacy contexts preserve sshGitURL behavior.
- `lifecycle.gitEndpoint(ctx context.Context, gitURL string) (string, error)` resolves an owned alias to the actual preferred endpoint for reachability probes without DNS lookup of the alias.
- `lifecycle.runInstanceGit(ctx context.Context, localRepo, endpoint string, args ...string) (string, error)` scopes `HostName` fallback and short connect/batch flags to this instance transfer; leave generic runLocalGit/signing and unrelated remotes unchanged.

- [ ] **Write failing tests:** SSH/tmux/pair/forwarding and rsync use one machine alias regardless of session; preserve remote command quoting and optional `-A`. Git session remote persists alias + actual repository path. A real temporary Git/SSH fixture fetches a local bare repository through the alias with `GIT_SSH_COMMAND` unset and no bivouac wrapper. Address probe resolves alias to tailnet IP; unreachable tailnet fallback uses public HostName with the same alias/identity and host-key policy. Test >6 unrelated agent keys, custom identity paths with spaces, and that an unrelated GitHub remote/auth config is unchanged. Conflicting caller GIT_SSH_COMMAND/core.sshCommand settings produce explicit guidance instead of silently substituting keys.
- [ ] **Run red:** `go test ./internal/lifecycle ./cmd > /tmp/bivouac-task8.log 2>&1`; inspect new failures.
- [ ] **Implement:** persist alias URLs for new sessions; legacy remotes are still readable with process-scoped selected transport when a record has metadata. Keep reachability/timeouts and fallbacks working with actual endpoints. After Tailscale is joined/discovered, update recorded PreferredHost and owned profile with the same real-address host-key identity policy; retain PublicIP for fallback. Do not remove public known-host entries or disable StrictHostKeyChecking to make fallback work. Use argv and existing shell quoting for nested SSH/Git command strings.
- [ ] **Run green:** repeat, inspect log, require ordinary Git fetch fixture and fallback assertions to pass.
- [ ] **Commit:** `Share selected identities across SSH transports`.

### Task 9: Herdr targets and machine teardown

**Files:** Modify `internal/lifecycle/herdr.go`, `herdrattach.go`, `herdrprofile.go`, their tests, `down.go`, `down_test.go`; `cmd/run_attach.go` and tests as needed.

**Interfaces:**
- `lifecycle.MachineTarget(ctx context.Context, ip, user string) (string, error)` replaces target construction with alias-aware targets; legacy metadata preserves IP targets.
- Saved-machine lookup uses recorded profile ID first, then owned alias target; retain user-renamed labels and exact ownership cleanup.
- Down calls ProfileStore.Remove only after confirmed provider destruction or ErrNotFound. Session cleanup never removes a machine SSH alias.

- [ ] **Write failing tests:** three sessions on a machine use one alias/saved-machine target; two machines never share identities. Direct attach and saved-machine add both pass `ssh://user@bivouac-<name>`. A restarted Herdr transport resolves its saved target using persistent config, not bivouac-only environment overrides. Removing one session leaves profile untouched; successful down removes only one machine entry; failed destroy keeps its alias. Recreate updates address and rejects stale changed host keys rather than accepting them silently. Preserve saved-machine rename/ID ownership tests.
- [ ] **Run red:** `go test ./internal/lifecycle ./cmd > /tmp/bivouac-task9.log 2>&1`; require new target/cleanup cases to fail.
- [ ] **Implement:** use the same alias for direct Herdr and saved machines, never invent a key flag. Verify actual Herdr 0.9.4 OpenSSH invocation with a PATH-recording adapter under isolated HOME/XDG/HERDR_CONFIG_PATH, inspecting its managed include as well as argv. Add an opt-in integration check against a disposable local SSH fixture running Herdr to prove saved-machine reconnection after the creating process exits; if unavailable, explicitly report the external verification limitation, not complete end-to-end success. Cleanup errors name local repair steps and do not delete cloud-account keys.
- [ ] **Run green:** repeat and inspect logs; run the opt-in Herdr test where installed. Require actual transport evidence before claiming Herdr end-to-end coverage.
- [ ] **Commit:** `Honor selected identities in Herdr machines`.

### Task 10: Whole-flow regression and user documentation

**Files:** Create `cmd/ssh_workflow_test.go`; modify `docs/config.md`, README, RELEASE_NOTES, approved spec's implementation notes; use existing lifecycle/provider fixtures rather than adding a second production orchestration seam.

**Interfaces:** No new product API; tests compose the interfaces above.

- [ ] **Write whole-flow regression test:** setup selects a custom `.pub`; up summary lists registration and include action; approval registers/creates; fake SSH server proves readiness; stored metadata and aliases serve a subsequent provision/session/transfer. A fingerprint config instead stops with original-file migration guidance and no provider calls, and init lets the user replace it. Multiple machines/sessions remain isolated through one machine's destruction.
- [ ] **Run regression:** `go test ./cmd -run 'TestSSHWorkflow' > /tmp/bivouac-task10.log 2>&1`; this integration test may already pass after Tasks 1–9. If it fails, ensure the gap is a behavior failure and fix it in its owning component with a focused red/green regression test first.
- [ ] **Document:** path-only config and breaking migration, confirmed registration, agent/encrypted/custom-key behavior, one-per-machine alias/include, old-state fallback, manual repair and credential scopes. Replace Planned release copy with implemented behavior only when all claimed transports have evidence. Record any outstanding external verification honestly.
- [ ] **Run verification:** `go test ./... > /tmp/bivouac-go-tests.log 2>&1`, `pre-commit run --all-files > /tmp/bivouac-precommit.log 2>&1`, and `nix flake check > /tmp/bivouac-flake-check.log 2>&1`; inspect each log and exit status. Run `git diff --check`. Record actual results, including environmental blockers.
- [ ] **Review:** perform refactor scan against changed code, fix validated findings; review commits to fold TDD churn into logical units. If semantic scan or fresh-reviewer tools are unavailable, state that limitation and perform a source/diff review; never fabricate a review result.
- [ ] **Commit:** `Document selected SSH keys and migration`.

## Self-review and execution handoff

All spec sections map to tasks: config/migration (1,3), signers (2,7), planning
and permissions (5,6), setup (3), state (6,7), shared aliases (4,8,9), all
transports and cleanup (7–9), and verification/docs (10). Review Focus cases
are included in owning tests. Agent-reference support and legacy-input
acceptance are deliberately absent; only legacy state fallback remains.

The user chose remote implementation and testing in a bivouac session.
That session checkout is already isolated; do not create a second worktree
unnecessarily. Read CONTRIBUTING.md, AGENTS.md, the approved spec, and this
plan before coding. Use executing-plans for inline execution or supported
subagents if available on the VM, with task-by-task red/green evidence and
explicit review limitations. Do not push as part of this work.

Do not claim tests, transport behavior, or feature completion based only on
this plan. Before closing each child, record verification evidence in Beads;
close the epic only after all required behaviors have evidence.
