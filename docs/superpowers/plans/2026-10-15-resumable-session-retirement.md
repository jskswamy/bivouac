# Resumable Session Retirement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make session merge/delete share an idempotent, durable retirement workflow that closes exactly the session's owned Herdr resources and resumes safely after interruption.

**Architecture:** Keep operation-specific Git/issue safety gates separate from one retirement coordinator. Persist session generations, attachment ownership and retirement progress in bivouac state, reuse the repository-local replay journal for per-commit progress, and reconcile live effects before resuming. Give Herdr operations one explicit server address, preserving selected SSH identities and shared-machine lifetime.

**Tech Stack:** Go 1.26, existing Cobra commands, JSON/XDG state, external Git/SSH/Herdr/bd commands, Nix development shell, existing real-Git and isolated SSH/Herdr fixtures. No new service or Herdr library.

**Spec:** `docs/superpowers/specs/2026-10-15-resumable-session-retirement-design.md` (approved; read completely before execution).

## Global Constraints

- Successful merge closes the session's Herdr workspace/session and its panes.
- Preserve the shared saved machine, SSH alias, and other sessions.
- Keep panes running during replay/signature verification.
- Merge and delete use one retirement workflow, not parallel implementations.
- Both commands are idempotent and resumable.
- Bivouac state is the ownership/workflow authority. Git proves commit safety; Herdr supplies live resource existence.
- Cleanup failure returns nonzero and reports the completed work, pending step, and same command to resume.
- Unreachable is not absent.
- Missing or ambiguous proof leaves recovery pending with actionable guidance; it must never turn into a label-based close.
- Do not redesign provisioning, SSH key selection, Git signing, or Herdr itself.
- Close panes only after the initial operation-specific gate; checkpoint/check issues again after close and handle late work before removal.
- Force authorises discarding work, not deleting unowned resources or forgetting unfinished cleanup.
- Tests first: run the new test red, implement, run green, then commit each coherent task. Subjects are capitalised imperative, at most 50 characters; no AI attribution or internal tracker IDs.
- Run in `nix develop`; redirect Go test output to `/tmp` logs, never pipe it. No shared-remote pushes. Do not exercise cleanup on the hosting Herdr server.
- Execution is remote in a new bivouac session, only after the user reviews this plan. Specification/plan creation is not authorisation to mutate the running CLI or live instances.

## Review Focus

1. A lost Herdr close reply followed by a server restart/ID reuse must not close a replacement resource (Tasks 1, 4, 9).
2. A source branch advancing after a hand-resolved replay conflict must not replay already accepted commits or bypass signatures (Tasks 5, 9).
3. An unsynced issue database, including a failed second SSH connection, must block ordinary destruction rather than pass as safe (Tasks 5, 9).
4. Forced delete while offline must retain remote cleanup obligations, even after some local effects have completed (Tasks 6–9).
5. A new session reusing a completed name, or an update to a second session, must not be consumed/overwritten by stale receipts or record snapshots (Tasks 2, 3, 8, 9).

## File Map and Dependency Order

| Files | Responsibility |
| --- | --- |
| `internal/state/session_lifecycle.go` (new) | Versioned generation, attachment, journal and receipt data; validation |
| `internal/state/atomic.go`, `locking.go` (new), `state.go` | Durable atomic persistence, short state transactions, process-safe session locks |
| `internal/testenv/herdr.go` (new), existing `herdr_transport_test.go` | One isolated real-Herdr fixture, shared by feasibility and lifecycle integration |
| `internal/lifecycle/herdraddress.go`, `herdrownership.go` (new) | Explicit server routing and tested ownership/recovery adapter |
| `herdrattach.go`, `herdr.go`, `herdrworkspace.go`, `herdrtabs.go` | Attachment creation/reuse/layout through the same address |
| `internal/lifecycle/retirement_work.go` (new), `merge.go`, `replayprogress.go`, `beads.go` | Shared work snapshot and merge/delete policy gates; existing replay/signature engine |
| `internal/lifecycle/retirement.go`, `retirement_effects.go` (new) | One coordinator, intent/effect reconciliation and ordered resource removal |
| `delete.go`, `merge.go` | Thin public operation wrappers; no duplicate teardown sequences |
| `cmd/run_attach.go`, `run_session.go`, `status_print.go`, `lookup.go` | Registry/state ownership, resumable commands, status/help/receipt resolution |
| `internal/lifecycle/down.go`, `sessioninfo.go` | Pending-retirement-aware down/list; shared-machine ownership stays instance-scoped |
| Matching `*_test.go`, `docs/architecture.md`, `docs/config.md`, `README.md`, `RELEASE_NOTES.md` | Lifecycle proof and accurate user documentation |

Tasks 1 → 2 → 3 → 4 establish capability/state/ownership. Task 5 establishes work policy. Tasks 6 → 7 join them; Task 8 wires CLI/down; Task 9 proves complete flows. Do not parallelise shared-state/interface changes merely because files differ.

## Shared Interface Decisions

These names are the cross-task contract; internal helper names/bodies remain builder decisions. Add JSON tags and typed string enums. New files are in existing packages, not new packages requiring import cycles.

### State data (Task 2)

- `Session.Generation string`: generated once with `NewSessionGeneration() (string, error)` using cryptographic randomness.
- `Session.AttachmentsKnown bool`, `Session.Attachments []HerdrAttachment`, `Session.Retirement *Retirement`.
- `Record.RetirementReceipts []RetirementReceipt`: latest receipt per local-repository/session-name pair on this instance.
- `HerdrAddress{MachineID, Session, ConfigRoot string}`: `Session` is explicit, including `"default"`; `ConfigRoot` identifies the remote configuration namespace. Never infer a session from a workspace ID.
- `OwnershipProof{Scheme string; Data json.RawMessage}`: opaque, versioned adapter proof. Task 1 establishes the actual supported scheme before Task 4; this is not permission to use unverified display metadata.
- `HerdrAttachment{ID string; Kind AttachmentKind; Address HerdrAddress; Handle string; Proof OwnershipProof; Owned bool; Phase AttachmentPhase}`. Kinds: `workspace`, `named-session`, `legacy-profile`. Phases: `creating`, `active`, `closing`, `closed`.
- `Destination{LocalRepo, GitDir, Branch string}`.
- `Acceptance{SourceTip, ReplayBase, VerifiedHead, IssueEvidence string}`: one entry per accepted source boundary; no source-to-destination SHA equality assumption after cherry-picking.
- `Retirement{Version int; Operation RetirementOperation; Force bool; Phase RetirementPhase; Destination Destination; Accepted []Acceptance; Effects map[string]RetirementEffect; LastFailure string}`.
- Operation enum: `merge`, `delete`. Phase enum: `checking`, `paused`, `closing`, `final-check`, `removing`.
- `RetirementEffect{Status EffectStatus}`: `intended` or `done`; absent means not attempted. Fixed effect keys: `herdr:<attachment-id>`, `remote-checkout`, `local-worktree`, `beads-remote`, `git-remote`, `git-branch`, `replay-journal`.
- `RetirementReceipt{Session, Generation, LocalRepo string; Operation RetirementOperation; Signed []string; CompletedAt time.Time}`.
- `ValidateSessionLifecycle(Session) error`: reject unsupported versions, malformed identities, illegal force/mode combinations and impossible phase/evidence combinations. Legacy missing fields remain readable.

### Store operations and locks (Task 3)

- `(*Store).Update(instance string, mutate func(*Record) error) error`: lock store, reload, mutate only latest record, atomically persist; callback errors do not write.
- `(*Store).LockSession(ctx context.Context, instance, session string) (release func() error, err error)`: process-safe lock on this name slot, covering generation replacement too; generation is revalidated after locking. Release after an operation pauses, not while waiting for the user to resolve conflicts.
- `(*Store).LockInstance(ctx context.Context, instance string, exclusive bool) (release func() error, err error)`: shared lease for normal session operations, exclusive lease for down; acquire before session locks. A long operation on one instance must not block unrelated instances.
- `(*Store).CompleteRetirement(instance, generation string, receipt RetirementReceipt) error`: transactionally validate/remove exactly that generation and update its receipt; never overwrite another session.
- Put/Delete/List/Get participate in compatible short state-file locking. Existing-record mutations use Update rather than stale whole-record Put. Do not hold a store lock through external processes.
- Durable write helper: `state.WriteFileAtomic(path string, data []byte, mode fs.FileMode) error`, including sync/rename/directory sync as supported and error propagation. Reuse it for required replay-journal writes.

### Herdr operations (Task 4)

- `herdrCommand(state.HerdrAddress, ...string) ([]string, error)`: local-machine CLI arguments for saved-machine forwarding; validate the profile's configured server before routing.
- `remoteHerdrCommand(state.HerdrAddress, ...string) (string, error)`: quoted SSH-exec command selecting the same explicit namespace/server.
- `EnsureSessionAttachment(ctx context.Context, store *state.Store, instance, session string) (state.HerdrAttachment, error)`: lock/reload, recover intent or create, record owned address/proof; no interactive client lifetime under the lock. Preserve existing inside/outside CLI presentation.
- `CloseSessionAttachment(ctx context.Context, record state.Record, attachment state.HerdrAttachment) error`: verify proof/address; close and observe absence. Already absent is success only with authoritative observation. It does not remove shared machines.
- Need local and SSH executors for tests: reuse `herdrRunner` and `remoteRunner`; pass explicit address through EnsureWorkspace/LayOutTabs helpers rather than making them reconstruct it from the session name.

### Retirement public API (Tasks 6–8)

- `RetirementOptions{Operation state.RetirementOperation; Force bool}`.
- `RetirementResult{Completed, AlreadyCompleted bool; Phase state.RetirementPhase; Signed []string}`.
- `RetirementError{Operation state.RetirementOperation; Phase state.RetirementPhase; ResumeCommand string; Err error}` with `Error()`/`Unwrap()`; never include credentials.
- `RetireSession(ctx context.Context, store *state.Store, instance, session string, options RetirementOptions) (RetirementResult, error)` is the only retirement coordinator.
- Final wrappers: `MergeSession(ctx context.Context, store *state.Store, instance, session string) (RetirementResult, error)` and `DeleteSession(ctx context.Context, store *state.Store, instance, session string, force bool) (RetirementResult, error)`.
- These are internal-package API changes: migrate callers/fixtures, do not keep an alternative non-journalled retirement API. Pull/Rescue stay usable, preserving recorded repository-path resolution.

---

### Task 1: Prove Herdr ownership and close semantics

**Files:** Create `internal/testenv/herdr.go`, `internal/lifecycle/herdr_retirement_integration_test.go`; modify `internal/lifecycle/herdr_transport_test.go`; create `docs/superpowers/plans/2026-10-15-resumable-session-retirement-execution-notes.md`.

**Interfaces:** Produces `testenv.StartHerdr(t *testing.T, binary string) *HerdrFixture`, with methods `Run(args ...string) (string, error)` and `Restart() error`; fixture fields `Home, ConfigRoot, Binary string`. Expose no hosting socket/config. Records the concrete `OwnershipProof.Scheme` and evidence needed by Task 4.

- [ ] **Write capability tests first.** `TestHerdrRetirementCapabilities` creates two isolated server namespaces with deliberately colliding IDs; asserts a resource is recoverable after an intent/create/lost-reply boundary, friendly rename does not authorise another workspace, close terminates its panes, and the other server/workspace survives. `TestHerdrOwnershipAfterRestart` proves restart/ID-reuse behavior; safe refusal on lost proof is acceptable, deletion of a replacement is not. Include a process writing a sentinel on shutdown and assert the post-close checkpoint sees it.
- [ ] **Run red.** `BIVOUAC_HERDR_BIN=/absolute/herdr go test ./internal/lifecycle -run 'TestHerdrRetirementCapabilities|TestHerdrOwnershipAfterRestart' -count=1 > /tmp/retirement-capabilities.log 2>&1`; confirm missing fixture/verification behavior, not accidental access to a live server. Without an explicit binary these opt-in tests skip honestly.
- [ ] **Extract the isolated harness and establish the adapter proof.** Reuse `herdr_transport_test.go`'s isolated HOME/XDG/HERDR_* handling and SSH server. Create under a persisted-intent-derived unique temporary label/name; inspect actual replies/snapshots and prove recovery before choosing persistent proof. Record installed client/server versions, proof representation, restart limits, process-exit semantics and exact commands. Do not assume display tokens are permanent, invent a server UUID, or treat temporary labels as sole authority after rename. If safe ownership/recovery cannot be demonstrated, stop the remaining tasks and report the precise limitation for design review.
- [ ] **Run green and existing transport coverage.** Repeat the capability command with a real supported binary and `TestHerdrPersistentSelectedTransport`; record passes/skips, not just exit zero. Ensure the hosting Herdr process/socket was never targeted.
- [ ] **Commit:** `Prove Herdr resource ownership and closure` (fixture, tests and execution evidence together).

### Task 2: Add versioned session lifecycle state

**Files:** Create `internal/state/session_lifecycle.go`, `internal/state/session_lifecycle_test.go`; modify `internal/state/state.go`, `internal/state/state_test.go`, `cmd/run_session.go`, `cmd/lookup_run_test.go`.

**Interfaces:** Produces all state data/validation listed above. New sessions explicitly initialise `Generation` and `AttachmentsKnown: true` before StartSession; old records retain unknown attachment status until reviewed.

- [ ] **Write failing round-trip and generation tests.** `TestSessionLifecycleRoundTrip` stores two attachments on different servers, a paused retirement, accepted tips and a receipt and compares Get output. `TestLegacyAttachmentStateRemainsUnknown` loads old JSON and asserts `!sess.AttachmentsKnown`, `sess.HerdrMachineID` preserved and `sess.RepoNameOr(instance)` unchanged. `TestSessionGenerationSurvivesStartRetry` asserts a retry reuses generation rather than replacing pending state. `TestRecreatedSessionDoesNotUseOldReceipt` asserts a new active generation wins over the old completion receipt.
- [ ] **Run red:** `go test ./internal/state ./cmd -run 'TestSessionLifecycle|TestLegacyAttachment|TestSessionGeneration|TestRecreatedSession' -count=1 > /tmp/retirement-state.log 2>&1`.
- [ ] **Implement types/validation and creation initialisation.** Do not use `record.PutSession` to erase an existing generation's attachments/journal on start retry. Reject start while retirement is pending; a completed name may start a new generation. Unsupported required journal versions/corruption are errors, not zero-value resets.
- [ ] **Run green:** repeat focused tests, then `go test ./internal/state ./cmd > /tmp/retirement-state-all.log 2>&1`.
- [ ] **Commit:** `Record session ownership and retirement state`.

### Task 3: Make required persistence transactional and durable

**Files:** Create `internal/state/atomic.go`, `locking.go`, `transactions_test.go`; modify `state.go`, `cmd/run_session.go`, `cmd/run_attach.go`, `cmd/run_instance.go`, `internal/lifecycle/lifecycle.go`, `transport.go`; tests beside callers.

**Interfaces:** Produces Store.Update/LockSession/LockInstance/CompleteRetirement/WriteFileAtomic above. Use OS/process-safe file locks supported on Linux/macOS without a new service; small platform-specific files are allowed where necessary.

- [ ] **Write failing durability/concurrency tests.** `TestStateUpdatePreservesConcurrentSessions` uses subprocesses updating auth/docs on one instance; assert both and unrelated instances survive. `TestStateAtomicWriteFailurePreservesPreviousJSON` injects temp-write/sync/rename failures and asserts old state remains readable. `TestSessionLockSerializesGenerationReplacement` blocks same-name attach/retire/start but allows a second session. `TestInstanceLeaseExcludesDownButNotOtherInstances` verifies down waits for active session effects, prevents new attachments/starts while destroying, and does not block another instance. `TestCompleteRetirementKeepsOtherSessions` asserts receipt update plus removal is one transaction and rejects stale generation.
- [ ] **Run red:** `go test ./internal/state -run 'TestStateUpdate|TestStateAtomic|TestSessionLock|TestInstanceLease|TestCompleteRetirement' -count=1 > /tmp/retirement-store.log 2>&1`.
- [ ] **Implement atomic writes and short transactions.** Preserve permissions; exclude credential/socket contents from journals. Update every production mutation of an existing record found by `rg '\.Put\(' cmd internal/lifecycle`: creation Put can remain, but recordHerdrMachine, address refresh, tailscale updates, sessions and provisioning changes must not clobber concurrent session journals. Test each migrated mutation with a second session update. Lock acquisition must honour context cancellation and process exit must release locks. Lock order is instance lease → session-name lock → short store transaction; never upgrade a shared lease in place.
- [ ] **Run green:** focused tests, `go test -race ./internal/state > /tmp/retirement-store-race.log 2>&1`, then cmd/lifecycle regression suites redirected to logs.
- [ ] **Commit:** `Persist session progress atomically`.

### Task 4: Unify Herdr addressing and record owned attachments

**Files:** Create `internal/lifecycle/herdraddress.go`, `herdrownership.go` and tests; modify `herdrattach.go`, `herdr.go`, `herdrworkspace.go`, `herdrtabs.go`, their tests, `cmd/run_attach.go`, `cmd/lookup_run_test.go`.

**Interfaces:** Produces EnsureSessionAttachment/CloseSessionAttachment and address builders above, using Task 1's verified proof. Update `EnsureWorkspace` and `LayOutTabs` to consume `state.HerdrAddress`; keep their existing reuse/layout algorithms.

- [ ] **Write failing lifecycle-address tests.** `TestAttachRoutesWorkspaceAndTabsToSameServer` uses auth/default servers both containing w1:p1; assert create/list/layout/focus all address the creator server. `TestAttachmentIntentRecoversLostCreateReply` and `TestAttachmentRenameKeepsOwnership` feed recorded intents/handles through attach and then close. `TestCloseAttachmentRefusesSameLabelForeignResource`, `TestCloseAttachmentAfterIDReuse`, `TestDirectAttachRecordsNamedServer`, and `TestUnknownLegacyAttachmentNeedsConfirmation` pin ownership, outside-Herdr and migration behavior.
- [ ] **Run red:** `go test ./cmd ./internal/lifecycle -run 'TestAttachRoutes|TestAttachment|TestCloseAttachment|TestDirectAttach|TestUnknownLegacy' -count=1 > /tmp/retirement-attach.log 2>&1`.
- [ ] **Implement one address/ownership adapter.** Validate the saved profile session/config namespace. SSH tab/pane commands use the same explicit server, not the bivouac session label. Register every owned resource, including both direct and shared resources if a session used both modes. Persist create intent before mutation; recover before creating duplicates. Do not hold the session lock for the duration of interactive Passthrough. On a paused pre-removal retirement, explicit reattach registers resources and invalidates final-work evidence; once removal begins it refuses. Legacy confirmation is interactive only: show exact observed server/resource and ask before adoption; non-interactive unknown ownership returns actionable nonzero without inventing a confirmation flag. A caller-selected user-created workspace remains unowned absent confirmation.
- [ ] **Run green** focused tests plus Task 1's real-Herdr fixture tests; update existing tab tests that encoded `--session auth` as an unconditional truth. Test selected-identity direct/saved transports remain unchanged.
- [ ] **Commit:** `Track Herdr attachments by server and owner`.

### Task 5: Extract reusable work gates and harden replay evidence

**Files:** Create `internal/lifecycle/retirement_work.go`, `retirement_work_test.go`; modify `merge.go`, `delete.go`, `replayprogress.go`, `beads.go`, matching tests.

**Interfaces:** Produces internal `retirementPolicy` with `Check(ctx context.Context, record state.Record, sess state.Session, final bool) (state.Acceptance, []string, error)` and `Validate(ctx context.Context, record state.Record, sess state.Session, accepted state.Acceptance) error`. Constructors `mergeRetirementPolicy()` and `deleteRetirementPolicy(force bool)` share checkpoint/fetch/issue and cleanliness helpers, not their distinct acceptance rules. Preserve existing merge entry behavior until the coordinator replaces it in Task 8.

- [ ] **Write failing policy tests using real Git.** `TestMergePolicyExtendsAcceptedTipAfterResolvedConflict` accepts source A, pauses/resolves, then accepts descendant B; assert A is not replayed/re-signed twice and every accepted destination range verifies. `TestRetirementRejectsDestinationRewindOrBranchSwitch` refuses stale evidence. `TestRequiredReplayProgressWriteFailureStopsReplay` and corrupt-journal cases must not downgrade to best-effort. `TestFinalGateFindsCloseTimeWrites` includes tracked/untracked remote edits, local worktree edits and issue-only changes. `TestRetirementIssueConnectionFailureIsUnsafe` fails the second connection and requires refusal, not the current warning-plus-nil. Table-test ordinary delete refusal versus forced-discard policy with identical snapshots.
- [ ] **Run red:** `go test ./internal/lifecycle -run 'TestMergePolicy|TestRetirementRejects|TestRequiredReplay|TestFinalGate|TestRetirementIssue|TestDeletePolicy' -count=1 > /tmp/retirement-work.log 2>&1`.
- [ ] **Extract, do not rewrite, replay/signature logic.** Reuse replaySigned/resignHandFinished/message cleanup/base checks and rollback. Bind replay journal to generation plus destination; strict atomic persistence/error propagation is required for retirement. Append source boundaries and destination evidence so a changed remote tip does not reset accepted progress. Keep protective local refs to accepted/fetched objects until retirement completion. Keep old benign fallback only for genuinely absent legacy progress before a new required retirement journal exists. For retirement, merge issue pull failures and issue-check connection failures are pending safety failures; pull's existing best-effort behavior need not change. Ordinary delete must protect local uncommitted/untracked work; force may discard it. Never infer issue safety solely from removal of its named remote.
- [ ] **Run green** focused policy tests, all existing merge/delete/replay tests, and bead-enabled tests when bd is available; report skips.
- [ ] **Commit:** `Reuse work gates with durable replay evidence`.

### Task 6: Build the shared retirement coordinator

**Files:** Create `internal/lifecycle/retirement.go`, `retirement_test.go`.

**Interfaces:** Produces RetireSession, RetirementOptions/Result/Error and fixed state transitions above. Task 7 supplies production removal effects; until then use private injected effect functions in tests. The test seam is for fixed effects, not a plugin/workflow framework.

- [ ] **Write the failing transition matrix.** `TestRetireSessionUsesOneWorkflowForMergeAndDelete` table-tests merge/delete/force-delete and asserts initial gate → close all owned attachments → final gate → removals → completion. `TestRetirementResumesEveryBoundary` injects failures before intent write, after intent, before/after each effect, and before/after progress write; retry with freshly opened state and assert no duplicate accepted commits or foreign closes. `TestRetirementModeAndGenerationAreImmutable` rejects delete-versus-merge/stale generation, while persisted forced-delete intent resumes without accidental downgrade. `TestLateWorkReturnsToPolicyWithoutReopeningPanes` loops to checking but leaves closed effects complete. `TestMissingJournalEvidenceFailsClosed` distinguishes corrupt from absent.
- [ ] **Run red:** `go test ./internal/lifecycle -run 'TestRetireSession|TestRetirementResumes|TestRetirementMode|TestLateWork|TestMissingJournal' -count=1 > /tmp/retirement-coordinator.log 2>&1`.
- [ ] **Implement the coordinator with write-ahead effects.** Take a shared instance lease, then lock/reload through the state store; assign a legacy generation once in a transaction before initialising a required journal, then initialise or resume immutable intent. Use `record.ConnectionContext(ctx)` for every external operation. Record nonzero pending failures before return where possible; if recording failure itself fails, return both errors and preserve previous durable intent. Revalidate accepted Git evidence even when its progress says done. Successful resource absence is not a general exemption from ownership checking on a reused ID. On missing remote/local halves, continue only where durable evidence makes the missing effect reconcilable. Never rerun an initial rescue after remote removal merely because a command was retried.
- [ ] **Run green** transition matrix, then `go test -race ./internal/state ./internal/lifecycle -run 'TestRetir|TestLateWork' > /tmp/retirement-coordinator-race.log 2>&1`.
- [ ] **Commit:** `Coordinate resumable session retirement`.

### Task 7: Implement observable shared cleanup effects

**Files:** Create `internal/lifecycle/retirement_effects.go`, `retirement_effects_test.go`; modify `gitremote.go`, `gitremote_test.go`, `beads.go` as needed.

**Interfaces:** Supplies production fixed effect handlers to RetireSession. Reuse existing Git argument builders, RemoveRemote and worktree checks. Add `removeRepoCheckedCmd(repo, branch, expectedTip string, discard bool) string`; normal removal checks final tip plus clean tracked/untracked status before removal. It returns real removal failure; optional parent `rmdir` cannot mask it. Force bypasses work-safety checks only.

- [ ] **Write failing removal/reconciliation tests.** `TestRetirementRemovalFailureCannotBeMasked` executes the built command in a disposable fixture with failing rm and successful/failed parent tidy; assert nonzero and checkout remains pending. `TestRetirementDetectsTipMovementBeforeRemoval` proves no deletion. `TestRetirementResumesAfterEachRemoval` simulates remote removal then local-worktree/beads/git-remote/branch failures; assert re-entry does not require the deleted remote or worktree. `TestForcedOfflineDeleteKeepsRemoteObligations` may complete independent local effects but leaves intent/remaining evidence and returns nonzero. `TestRetirementAlreadyMissingResourceIsIdempotent` uses successful existence queries rather than treating arbitrary command errors as absent.
- [ ] **Run red:** `go test ./internal/lifecycle -run 'TestRetirementRemoval|TestRetirementDetects|TestRetirementResumesAfter|TestForcedOffline|TestRetirementAlreadyMissing' -count=1 > /tmp/retirement-effects.log 2>&1`.
- [ ] **Implement effect observation/removal.** Use closed-Herdr confirmation before remote checkout removal. Keep local accepted/source refs and journal until dependent work/issue checks are done. Prefer remote checkout → local worktree → beads remote → Git remote → session branch → replay journal, adjusting only if tests expose an evidence dependency. Treat failure of owned remote/ref cleanup as pending, not the current swallowed error/warning. Keep cleanup namespace exact; preserve origin, unrelated remotes/refs, sibling checkouts and shared SSH/Herdr profiles. If retry finds external writers/new unknown work, stop rather than claim a process freeze that Herdr close does not supply.
- [ ] **Run green** focused effects tests plus existing nested-path, local-git, merge/delete safety, bead remote tests; verify the core path fix remains covered with `owner/repo` differing from instance name.
- [ ] **Commit:** `Reconcile shared session cleanup effects`.

### Task 8: Wire commands, receipts, status and down

**Files:** Modify `cmd/run_session.go`, `run_attach.go`, `lookup.go`, `status_print.go`, matching tests; modify `internal/lifecycle/merge.go`, `delete.go`, `sessioninfo.go`, `down.go`, matching tests.

**Interfaces:** Replaces MergeSession/DeleteSession signatures with the thin wrappers defined above. `runMerge`/`runSessionDelete` consume RetirementResult and no longer call forgetMergedSession/RemoveSession independently. Extend SessionInfo with `Operation`, `RetirementPhase`, `LastFailure` string fields; JSON fields `operation`, `retirementPhase`, `lastFailure` are additive.

- [ ] **Write failing command-level lifecycle tests.** `TestRunMergeResumesPendingCleanup` and `TestRunDeleteResumesPendingCleanup` run actual wrappers/store orchestration through failure/retry; assert nonzero, retained record, matching resume command and no repeated replay. `TestCompletedCommandFindsReceiptOutsideOriginalRepo` proves receipt lookup honours explicit --name/--repo and refuses cross-instance ambiguity. `TestActiveGenerationOverridesReceipt` proves name reuse cannot return old success. `TestSessionStatusShowsRetirementPendingWithoutRemote` verifies phase/failure fields and no misleading unmerged=0 claim after remote removal. `TestDownWithPartiallyRetiredSession` covers both successful destruction and failure preserving selected identity/state, with local cleanup still outstanding. `TestAttachRefusesRemovalButCanReopenPausedWork` exercises actual CLI registry and lock paths.
- [ ] **Run red:** `go test ./cmd ./internal/lifecycle -run 'TestRunMergeResumes|TestRunDeleteResumes|TestCompletedCommand|TestActiveGeneration|TestSessionStatusShows|TestDownWithPartially|TestAttachRefuses' -count=1 > /tmp/retirement-cli.log 2>&1`.
- [ ] **Wire one source of ownership and completion.** Resolve instance and session/receipt without a mandatory live worktree or origin remote. Fresh active generations win; default resolution never guesses among receipts on multiple instances. Use coordinator completion as the only retirement record-removal path. Show "merged; cleanup pending" separately from "retired". Down holds an exclusive instance lease, uses phase/evidence to rescue remaining work, refuses unsafe normal destruction, and after provider-confirmed destruction marks remote effects absent and drives remaining local effects using shared handlers without destroying shared infrastructure twice. Failed selected destroy retains profiles/journals. Update help/manifest semantics and migrate all fixture callers to store-backed wrappers; remove obsolete non-journalled teardown bodies. Do not let old helper-only tests stand in for command lifecycle tests.
- [ ] **Run green** focused tests, full cmd/state/lifecycle suites, and `go test ./... > /tmp/retirement-command-all.log 2>&1`.
- [ ] **Commit:** `Resume session commands from durable progress`.

### Task 9: Prove complete lifecycle, document and hand back

**Files:** Create `cmd/session_retirement_workflow_test.go`; extend real-Herdr integration tests and execution notes; update `README.md`, `docs/architecture.md`, `docs/config.md`, `RELEASE_NOTES.md` and approved spec evidence section if necessary.

**Interfaces:** Consumes all public/state/adapter contracts; no second test-only retirement implementation. Reuse fixtures from Tasks 1 and 5 and state created by the attach command.

- [ ] **Write whole-flow regressions before final fixes.** `TestSessionRetirementWorkflow` covers two sessions on one selected-key machine: CLI attach → configured layout → merge/delete one → retry injected failure → receipt; assert only its resources/remotes disappear, signatures verify and the other session fetch/attach remains usable. `TestSessionRetirementCrashMatrix` restarts the CLI process at each effect boundary, not just Go errors; include destination branch changes, late source/issue writes, lost-close reply with colliding/reused IDs, forced offline delete, legacy unknown ownership and a new generation reusing the name. Keep pure Go/fake-SSH command flow in the ordinary suite; real-Herdr fixture rows are explicit opt-in with honest skip reporting.
- [ ] **Run red:** `go test ./cmd ./internal/lifecycle -run 'TestSessionRetirementWorkflow|TestSessionRetirementCrashMatrix' -count=1 > /tmp/retirement-workflow.log 2>&1`; run corresponding real-Herdr rows with BIVOUAC_HERDR_BIN under isolated HOME/XDG. Fix discovered gaps in the task that owns them; do not waive the spec based on helper tests passing.
- [ ] **Document actual behavior.** Explain merge versus delete, recorded force authority, nonzero cleanup-pending outcomes, same-command resume, closed panes during late-work conflicts, legacy adoption, receipts, and limitations for detached writers/ownership loss. Replace Planned release copy with implemented behavior only after evidence. Record binary versions, exact tests/results and every unverified/skipped integration; do not claim live cloud coverage.
- [ ] **Run the release-quality checks.** `go test ./... > /tmp/retirement-all.log 2>&1`; `go test -race ./internal/state ./internal/lifecycle ./cmd > /tmp/retirement-race.log 2>&1`; `pre-commit run --all-files > /tmp/retirement-hooks.log 2>&1`; `nix flake check --option sandbox false --print-build-logs > /tmp/retirement-flake.log 2>&1` on Linux per CONTRIBUTING. If sandbox override is ignored, report the trust restriction; do not change global daemon settings. Run the isolated actual-Herdr lifecycle/ownership fixture with the supported binary explicitly. A timeout is not a pass.
- [ ] **Commit:** `Verify resumable session retirement workflows`. Run refactor-scan against changed code and fix findings, then review-commits to fold TDD churn into logical commits. If tools/index are unavailable, document the limitation and perform an explicit manual diff review rather than claiming the automated review ran. Verify clean tree, valid commit subjects, no AI trailers/internal tracker IDs, and no pushes.

## Spec-to-Test Coverage Map

| Spec contract | Tasks / decisive evidence |
| --- | --- |
| Same server for create/layout/focus/close | 1, 4, 9: colliding-ID servers and real Herdr |
| Owned resources only; rename/restart/lost reply | 1, 4, 9: intent recovery and fail-closed ownership |
| Shared machine/alias/other sessions survive | 4, 6, 9: two-session command lifecycle |
| Verify before close; handle late work afterwards | 5–7, 9: close-time sentinel and acceptance extension |
| One workflow, different merge/delete policies | 5, 6, 8: table-driven policy/coordinator + wrappers |
| Durable progress and repeated-command success | 2, 3, 6, 8, 9: atomic writes/crash matrix/receipts |
| No replay of accepted commits; changed destination refused | 5, 9: real Git conflict/rewind/source advance |
| Unknown is not safe: issues/unreachable/legacy | 4–8: connection failure/offline/confirmation tests |
| Partial resource removal and down recovery | 7, 8, 9: missing halves, successful/failed destroy |
| Concurrency and generation replacement | 2, 3, 4, 8, 9: subprocess locks, independent records |
| Honest status/help and completion reporting | 8, 9: actual command output/JSON and docs |
| No weakened selected-key or nested-path behavior | 4, 7–9: existing transport/path tests + whole flow |

## Remote Execution Handoff

After user review of this plan:

1. Commit the approved spec, this plan and matching Planned release entry locally. Do not start with uncommitted design documents that the remote checkout will not receive.
2. Inspect `bivouac manifest`, instance status and session list; rebuild/install the locally approved CLI if needed. Start a fresh session such as `session-retirement` on the explicitly selected instance. Do not reuse the closed `selected-ssh-key` generation or retire unrelated sessions.
3. Pass the approved spec/plan paths as the task; create/link a Beads issue if the user wants tracking. Verify those files and the starting revision on the VM. Align bd versions; verify a working agent provider/model before saying implementation is running. Never assume the model catalog proves actual account access.
4. Remote worker enters executing-plans directly. Begin Task 1 in disposable isolated fixtures, not the worker's hosting Herdr. No pushes; deliberate commits are the deliverable. Keep `/tmp` logs outside the checkout; record summaries in execution notes.
5. Stop for design review if Task 1 cannot prove ownership or later evidence contradicts the approved contract. Do not silently reduce requirements to make a task green.
6. Hand back evidence, clean commits and remaining limitations. Pull/review before using the new cleanup implementation against real user sessions. The worker must not close its own hosting session as a test.

## Concrete Assertion Anchors

These are assertions to embed in the named tests above, not substitutes for their fixture setup or full cross-boundary assertions.

```go
// Task 2: old state is not silently upgraded to known-unattached.
if sess.AttachmentsKnown { t.Fatal("legacy ownership must remain unknown") }
if sess.RepoNameOr("instance-name") != "owner/repo" { t.Fatal("lost checkout identity") }

// Task 3: another session survives a transactional update.
if _, ok := record.FindSession("docs"); !ok { t.Fatal("overwrote other session") }

// Task 6: failure cannot erase the recovery record or return completion.
if result.Completed || err == nil { t.Fatal("cleanup failure reported as success") }
if sess.Retirement == nil { t.Fatal("lost pending retirement") }

// Tasks 8–9: retrying a finished generation does no new replay.
if !result.Completed || !result.AlreadyCompleted { t.Fatal("did not return receipt") }
if gotHead != completedHead { t.Fatal("replayed accepted commits") }
```

## Plan Self-Review

Checked against every spec section and mapped above. The same coordinator owns both operations; no raw repo-name or session-machine-id parameter reintroduces the previous split. The feasibility gate precedes all claims about ownership, transaction hardening precedes destructive orchestration, and receipt lookup/down recovery have explicit tasks. Review Focus inputs each have named regressions. Exact production helper bodies and the proven native ownership scheme remain implementer choices within the stated gate, not omitted product decisions.
