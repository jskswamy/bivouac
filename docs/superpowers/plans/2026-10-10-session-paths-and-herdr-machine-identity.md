# Session Paths and Herdr Machine Identity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Store new session checkouts under `~/sessions/<session>/<owner>/<repo>` and make Herdr saved machines represent bivouac instances, with sessions as nested workspaces.

**Architecture:** Add a repository-path identity separate from the flattened instance name, persist it on each new session, and keep all later remote operations using the recorded value. Replace session-scoped Herdr machine profiles with an instance-scoped profile recorded on `state.Record`, while preserving legacy per-session profile cleanup.

**Tech Stack:** Go 1.26, Cobra, git CLI, Herdr CLI, existing `internal/state`, `internal/identity`, and `internal/lifecycle` packages.

**Spec:** `docs/superpowers/specs/2026-10-10-session-paths-and-herdr-machine-identity-design.md`

## Global Constraints

- New remote session paths are `/home/<user>/sessions/<session>/<owner>/<repo>` for common remotes.
- Existing active sessions must keep using their recorded `state.Session.RepoName` path.
- Sessions with empty `RepoName` must keep falling back to the instance name.
- The local worktree remains `<local-repo>/.worktrees/<session>`.
- The bivouac instance name and provider VM name remain flattened single-token names.
- Herdr saved-machine labels for new attaches are bivouac instance names.
- Herdr workspace labels remain simple bivouac session names like `auth` and `docs`.
- New shared Herdr saved machines must omit `--remote-session`.
- Session teardown must not remove a shared instance-scoped Herdr machine while other sessions may still use it.
- Run `go test ./...` by redirecting output to a file, never through a pipe.

## Review Focus

- Unsafe recorded `RepoName` values: absolute paths, `..`, and empty segments must not be allowed to build remote commands.
- Legacy sessions: flattened `RepoName` and empty `RepoName` must still work.
- Herdr ownership: a user-created saved machine must not be removed as if bivouac owned it.
- Multi-session Herdr attach: `auth` and `docs` on one instance must reuse one machine id and create separate workspaces.
- Teardown ordering: session merge/delete should clean legacy session-scoped Herdr profiles, while `down` removes the new instance-scoped profile.

---

## File Structure

- `internal/identity/identity.go`: add repository path derivation from git origin, distinct from flattened instance name derivation.
- `internal/identity/identity_test.go`: tests for nested repo path derivation and fallback behavior.
- `internal/lifecycle/gitpaths.go`: validate and join recorded remote repository path identities safely.
- `internal/lifecycle/gitpaths_test.go`: nested path and path-safety tests.
- `cmd/run_session.go`: record the nested repo path on new sessions and use it for session start/beads paths.
- `cmd/lookup_run_test.go`: command-level tests for recorded nested `RepoName` and default path consumers where practical.
- `internal/state/state.go`: add instance-scoped Herdr machine id on `Record`, keep per-session `HerdrMachineID` as legacy data.
- `internal/state/state_test.go`: state round-trip and legacy session field tests.
- `internal/lifecycle/herdrprofile.go`: replace session-scoped machine profile creation with instance-scoped creation/reuse by recorded id.
- `internal/lifecycle/herdrmachine_test.go`: update machine label/add/reuse tests.
- `internal/lifecycle/herdrattach.go`: pass recorded instance machine id into attach flow and root workspace at the recorded session repo path.
- `internal/lifecycle/herdrattach_test.go`: assert shared machine label with session workspace labels.
- `cmd/run_attach.go`: record shared Herdr machine id on the instance record instead of the session.
- `internal/lifecycle/down.go`, `internal/lifecycle/delete.go`, `internal/lifecycle/merge.go`: update cleanup call sites for shared-vs-legacy Herdr cleanup.
- `internal/lifecycle/down_test.go`, `internal/lifecycle/delete_test.go`, `internal/lifecycle/merge_test.go`: cleanup behavior tests.

---

### Task 1: Nested repository path identity

**Files:**
- Modify: `internal/identity/identity.go`
- Test: `internal/identity/identity_test.go`
- Modify: `internal/lifecycle/gitpaths.go`
- Test: `internal/lifecycle/gitpaths_test.go`

**Interfaces:**
- Produces: `identity.DeriveRepoPath(ctx context.Context, root string) (string, error)` returning `owner/repo` for remotes or `repo` for no-origin repositories.
- Produces: `lifecycle.CheckRemoteRepoPath(repo string) error` rejecting unsafe relative paths.
- Produces: `lifecycle.RemoteRepoPath(user, session, repo string) string` that joins safe slash-separated repo paths under `SessionDir`.
- Consumes: existing `tool.Run`, `SessionDir`, and git origin parsing conventions from `identity.DeriveName`.

- [ ] **Step 1: Write failing identity tests**

Add tests in `internal/identity/identity_test.go`:

```go
func TestDeriveRepoPath_FromHTTPSOrigin(t *testing.T) {
    // repo origin https://github.com/jskswamy/bivouac.git
    // want "jskswamy/bivouac"
}

func TestDeriveRepoPath_FromSSHOrigin(t *testing.T) {
    // repo origin git@github.com:jskswamy/bivouac.git
    // want "jskswamy/bivouac"
}

func TestDeriveRepoPath_NoOriginFallsBackToFolder(t *testing.T) {
    // repo with no origin in folder bivouac
    // want "bivouac"
}
```

- [ ] **Step 2: Run identity tests to verify failure**

Run: `go test ./internal/identity > /tmp/bivouac-identity.log 2>&1`

Expected: FAIL because `DeriveRepoPath` does not exist.

- [ ] **Step 3: Implement `identity.DeriveRepoPath(ctx context.Context, root string) (string, error)`**

In `internal/identity/identity.go`, factor a helper from the existing remote URL tokenization so `DeriveName` still returns `owner-repo` and `DeriveRepoPath` returns `owner/repo`. Lowercase the remote-derived path. If `git remote get-url origin` fails, return `filepath.Base(root)`.

- [ ] **Step 4: Run identity tests to verify pass**

Run: `go test ./internal/identity > /tmp/bivouac-identity.log 2>&1`

Expected: PASS.

- [ ] **Step 5: Write failing remote path safety tests**

Update `internal/lifecycle/gitpaths_test.go`:

```go
func TestRemoteRepoPath_AllowsNestedRepoPath(t *testing.T) {
    got := RemoteRepoPath("devuser", "auth", "jskswamy/bivouac")
    want := "/home/devuser/sessions/auth/jskswamy/bivouac"
    if got != want { t.Fatalf("RemoteRepoPath() = %q, want %q", got, want) }
}

func TestCheckRemoteRepoPathRejectsEscapes(t *testing.T) {
    for _, repo := range []string{"", "/abs", "../repo", "owner/../repo", "owner//repo"} {
        if err := CheckRemoteRepoPath(repo); err == nil { t.Errorf("%q accepted", repo) }
    }
}
```

- [ ] **Step 6: Run lifecycle path tests to verify failure**

Run: `go test ./internal/lifecycle -run 'TestRemoteRepoPath|TestCheckRemoteRepoPath|TestSessionDir' > /tmp/bivouac-gitpaths.log 2>&1`

Expected: FAIL because `CheckRemoteRepoPath` does not exist and/or nested path behavior is not documented.

- [ ] **Step 7: Implement `CheckRemoteRepoPath(repo string) error` in `internal/lifecycle/gitpaths.go`**

Reject empty strings, absolute paths, `.` or `..` components, and empty components. Update `RemoteRepoPath` to call it and panic on invalid input, because callers must validate before persisting or command construction; tests for user-facing validation are in later tasks.

- [ ] **Step 8: Run lifecycle path tests to verify pass**

Run: `go test ./internal/lifecycle -run 'TestRemoteRepoPath|TestCheckRemoteRepoPath|TestSessionDir' > /tmp/bivouac-gitpaths.log 2>&1`

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/identity/identity.go internal/identity/identity_test.go internal/lifecycle/gitpaths.go internal/lifecycle/gitpaths_test.go
git commit -m "Add nested session repo paths"
```

---

### Task 2: Record nested repo paths when starting sessions

**Files:**
- Modify: `cmd/run_session.go`
- Test: `cmd/lookup_run_test.go`
- Modify: path consumers only if tests expose old assumptions: `cmd/run_attach.go`, `cmd/run_session.go`

**Interfaces:**
- Consumes: `identity.DeriveRepoPath(ctx, root) (string, error)` from Task 1.
- Consumes: `lifecycle.CheckRemoteRepoPath(repo string) error` from Task 1.
- Produces: new sessions store `state.Session.RepoName` as `owner/repo` when origin has owner/repo.

- [ ] **Step 1: Write failing command-level session start test**

In `cmd/lookup_run_test.go`, add a test near existing `runSessionStart` tests that creates a local git repository with origin `git@github.com:jskswamy/bivouac.git`, invokes `runSessionStart` against an unreachable fake instance, and asserts the state record was written before the network failure with:

```go
sess, ok := got.FindSession("auth")
if !ok { t.Fatal("session not recorded") }
if sess.RepoName != "jskswamy/bivouac" { t.Fatalf("RepoName = %q", sess.RepoName) }
```

Also assert the instance name argument remains flattened, e.g. record name `jskswamy-bivouac`.

- [ ] **Step 2: Run the new command test to verify failure**

Run: `go test ./cmd -run TestRunSessionStart_RecordsNestedRepoPath > /tmp/bivouac-session-start.log 2>&1`

Expected: FAIL because `RepoName` is still the flattened instance name.

- [ ] **Step 3: Update `runSessionStart` to derive and validate the session repo path**

In `cmd/run_session.go`, after resolving `root` and before `record.PutSession`, call `identity.DeriveRepoPath(ctx, root)`. Validate the result with `lifecycle.CheckRemoteRepoPath`. Record that value in `state.Session.RepoName` and pass it as the `repoName` argument to `lifecycle.StartSession` and `markIssueStarted`.

- [ ] **Step 4: Run the command test to verify pass**

Run: `go test ./cmd -run TestRunSessionStart_RecordsNestedRepoPath > /tmp/bivouac-session-start.log 2>&1`

Expected: PASS.

- [ ] **Step 5: Run path consumer regression tests**

Run: `go test ./cmd ./internal/lifecycle > /tmp/bivouac-session-consumers.log 2>&1`

Expected: PASS, confirming pull/merge/delete/ssh/herdr callers still use `sess.RepoNameOr(record.Name)` and now accept nested values.

- [ ] **Step 6: Commit**

```bash
git add cmd/run_session.go cmd/lookup_run_test.go
git commit -m "Record nested repo paths for sessions"
```

---

### Task 3: Add instance-scoped Herdr machine state

**Files:**
- Modify: `internal/state/state.go`
- Test: `internal/state/state_test.go`
- Modify: `cmd/run_attach.go`
- Test: `cmd/lookup_run_test.go`

**Interfaces:**
- Produces: `state.Record.HerdrMachineID string` with JSON key `herdr_machine_id,omitempty`.
- Produces: `recordHerdrMachine(store *state.Store, instance, id string) error` or equivalent updated helper that records the machine id on the instance record.
- Keeps: `state.Session.HerdrMachineID` as legacy per-session cleanup data.

- [ ] **Step 1: Write failing state round-trip test**

In `internal/state/state_test.go`, add:

```go
func TestRecord_HerdrMachineIDRoundTrips(t *testing.T) {
    // Put/Get a Record with HerdrMachineID: "mid" and assert it round-trips.
    // Also keep an existing Session.HerdrMachineID in the same record to prove legacy data is preserved.
}
```

- [ ] **Step 2: Run state test to verify failure**

Run: `go test ./internal/state -run TestRecord_HerdrMachineIDRoundTrips > /tmp/bivouac-state.log 2>&1`

Expected: FAIL because `Record.HerdrMachineID` does not exist.

- [ ] **Step 3: Add `HerdrMachineID` to `state.Record`**

In `internal/state/state.go`, add:

```go
HerdrMachineID string `json:"herdr_machine_id,omitempty"`
```

Do not remove `state.Session.HerdrMachineID`.

- [ ] **Step 4: Run state test to verify pass**

Run: `go test ./internal/state -run TestRecord_HerdrMachineIDRoundTrips > /tmp/bivouac-state.log 2>&1`

Expected: PASS.

- [ ] **Step 5: Write failing attach recording test**

In `cmd/lookup_run_test.go` or the nearest existing `runHerdr` tests, add a focused unit test for the recording helper that starts with a record containing session `auth`, calls the updated helper with id `mid`, and asserts:

```go
got.HerdrMachineID == "mid"
got.Sessions[0].HerdrMachineID == ""
```

- [ ] **Step 6: Run attach recording test to verify failure**

Run: `go test ./cmd -run TestRecordHerdrMachine_RecordsOnInstance > /tmp/bivouac-herdr-record.log 2>&1`

Expected: FAIL because the helper still records on the session.

- [ ] **Step 7: Update `cmd/run_attach.go` recording path**

Change `recordHerdrMachine` to take `(store, instance, id)` and write `record.HerdrMachineID = id`. Update `runHerdr` to pass `record.HerdrMachineID` into the attach flow in Task 4; for this task, keep compilation by threading the value through any new signature as needed.

- [ ] **Step 8: Run attach recording test to verify pass**

Run: `go test ./cmd -run TestRecordHerdrMachine_RecordsOnInstance > /tmp/bivouac-herdr-record.log 2>&1`

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/state/state.go internal/state/state_test.go cmd/run_attach.go cmd/lookup_run_test.go
git commit -m "Store Herdr machines on instances"
```

---

### Task 4: Make Herdr machine profiles instance-scoped

**Files:**
- Modify: `internal/lifecycle/herdrprofile.go`
- Test: `internal/lifecycle/herdrmachine_test.go`
- Modify: `internal/lifecycle/herdrattach.go`
- Test: `internal/lifecycle/herdrattach_test.go`
- Modify: `cmd/run_attach.go`

**Interfaces:**
- Consumes: `state.Record.HerdrMachineID` from Task 3.
- Produces: `EnsureMachine(h herdrRunner, instance, target, recordedID string) (id string, label string, error)` or a clearly named equivalent.
- Produces: `AttachMachine(ctx, instance, ip, user, session, repoName, machineID string, tabs []config.HerdrTab) (id string, label string, error)` or equivalent signature.
- Keeps: workspace labels equal to the bivouac session name.

- [ ] **Step 1: Write failing Herdr machine tests**

Update `internal/lifecycle/herdrmachine_test.go`:

```go
func TestMachineLabel_IsTheInstanceName(t *testing.T) {
    if got := MachineLabel("jskswamy-bivouac"); got != "jskswamy-bivouac" { t.Fatal(got) }
}

func TestMachineAddArgs_OmitsRemoteSessionForSharedMachine(t *testing.T) {
    got := strings.Join(machineAddArgs("ssh://u@1.2.3.4", "jskswamy-bivouac"), " ")
    if strings.Contains(got, "--remote-session") { t.Fatal(got) }
}

func TestEnsureMachine_ReusesRecordedInstanceMachineID(t *testing.T) {
    // listing contains id "mid", label may be user-renamed, target matches.
    // EnsureMachine(..., recordedID: "mid") returns id "mid" and does not add.
}

func TestEnsureMachine_AddsWithInstanceLabelWhenNoRecordedID(t *testing.T) {
    // empty listing, after-add listing has id "mid", label "jskswamy-bivouac", session "default".
}
```

Remove or rewrite old tests asserting session labels and target+session identity.

- [ ] **Step 2: Run Herdr machine tests to verify failure**

Run: `go test ./internal/lifecycle -run 'TestMachineLabel|TestMachineAddArgs|TestEnsureMachine' > /tmp/bivouac-herdr-machine.log 2>&1`

Expected: FAIL because machines are still session-scoped.

- [ ] **Step 3: Update Herdr machine profile logic**

In `internal/lifecycle/herdrprofile.go`:

- Change `MachineLabel` to return the instance name.
- Change `machineAddArgs` to accept only `(target, label string)` and never append `--remote-session`.
- Replace `findMachine(target, session)` usage for new shared machines with recorded-id lookup when `recordedID != ""`.
- If `recordedID == ""`, add a new profile labeled with the instance name and read its id back from the listing.
- Enable a disabled recorded profile before returning it.
- Keep `RemoveMachine` unchanged for exact-id cleanup.

- [ ] **Step 4: Run Herdr machine tests to verify pass**

Run: `go test ./internal/lifecycle -run 'TestMachineLabel|TestMachineAddArgs|TestEnsureMachine' > /tmp/bivouac-herdr-machine.log 2>&1`

Expected: PASS.

- [ ] **Step 5: Write failing attach tests for shared machine with session workspaces**

Update `internal/lifecycle/herdrattach_test.go` so `existingMachine` represents an instance machine:

```json
[{"id":"mid","label":"jskswamy-bivouac","target":"ssh://u@1.2.3.4","session":"default","enabled":true}]
```

Add a test asserting `attachMachine` called for session `auth` returns label `jskswamy-bivouac` while the routed workspace lookup/create/focus uses `--machine mid` and workspace label `auth`.

- [ ] **Step 6: Run attach tests to verify failure**

Run: `go test ./internal/lifecycle -run TestAttachMachine > /tmp/bivouac-herdr-attach.log 2>&1`

Expected: FAIL until attach uses shared machine semantics.

- [ ] **Step 7: Update `AttachMachine` and `attachMachine` signatures**

In `internal/lifecycle/herdrattach.go`, pass the recorded machine id into `EnsureMachine`. Keep the workspace root as `RemoteRepoPath(user, session, repoName)` and the workspace label as `session`. Update `cmd/run_attach.go` to call the new signature with `record.HerdrMachineID`.

- [ ] **Step 8: Run attach tests to verify pass**

Run: `go test ./internal/lifecycle -run TestAttachMachine > /tmp/bivouac-herdr-attach.log 2>&1`

Expected: PASS.

- [ ] **Step 9: Run command attach tests**

Run: `go test ./cmd -run 'TestRunHerdr|TestRecordHerdrMachine' > /tmp/bivouac-cmd-herdr.log 2>&1`

Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/lifecycle/herdrprofile.go internal/lifecycle/herdrmachine_test.go internal/lifecycle/herdrattach.go internal/lifecycle/herdrattach_test.go cmd/run_attach.go cmd/lookup_run_test.go
git commit -m "Use one Herdr machine per instance"
```

---

### Task 5: Update Herdr cleanup for shared machines and legacy profiles

**Files:**
- Modify: `internal/lifecycle/herdrattach.go`
- Modify: `internal/lifecycle/delete.go`
- Modify: `internal/lifecycle/merge.go`
- Modify: `internal/lifecycle/down.go`
- Test: `internal/lifecycle/herdrmachine_test.go`
- Test: `internal/lifecycle/delete_test.go`
- Test: `internal/lifecycle/merge_test.go`
- Test: `internal/lifecycle/down_test.go`

**Interfaces:**
- Produces: `CleanupLegacyHerdr(ctx context.Context, machineID, session string, client remoteRunner)` or keeps `CleanupHerdr` for legacy per-session IDs only.
- Produces: `RemoveMachine(localHerdr{ctx}, record.HerdrMachineID)` on instance down.
- Consumes: `state.Session.HerdrMachineID` as legacy cleanup data.
- Consumes: `state.Record.HerdrMachineID` as shared machine cleanup data.

- [ ] **Step 1: Write failing lifecycle cleanup tests**

Add or update tests to assert:

```go
func TestDeleteSession_DoesNotRemoveSharedHerdrMachine(t *testing.T) {
    // session has empty legacy HerdrMachineID; delete/merge cleanup should not call herdr machine remove for record-level id.
}

func TestCleanupHerdr_RemovesLegacySessionMachine(t *testing.T) {
    // existing behavior remains for non-empty session.HerdrMachineID.
}

func TestDown_RemovesInstanceHerdrMachine(t *testing.T) {
    // record.HerdrMachineID = "mid" leads to local herdr machine remove mid during down cleanup.
}
```

Use existing fake Herdr/remote patterns in the lifecycle tests rather than a live Herdr binary.

- [ ] **Step 2: Run cleanup tests to verify failure**

Run: `go test ./internal/lifecycle -run 'TestDeleteSession_DoesNotRemoveSharedHerdrMachine|TestCleanupHerdr_RemovesLegacySessionMachine|TestDown_RemovesInstanceHerdrMachine' > /tmp/bivouac-herdr-cleanup.log 2>&1`

Expected: FAIL because cleanup still assumes per-session machine ids only and down does not remove the record-level id.

- [ ] **Step 3: Split cleanup semantics**

In `internal/lifecycle/herdrattach.go`, make the existing cleanup function explicitly legacy-session-scoped, or leave its name and document that callers pass only `state.Session.HerdrMachineID`. Do not call it with `state.Record.HerdrMachineID` from merge/delete.

In `internal/lifecycle/delete.go` and `internal/lifecycle/merge.go`, keep cleaning `sess.HerdrMachineID` for legacy sessions and remote Herdr session state, but do not remove `record.HerdrMachineID`.

In `internal/lifecycle/down.go`, remove `record.HerdrMachineID` once for the instance, in addition to best-effort legacy per-session cleanup for any `Session.HerdrMachineID` values.

- [ ] **Step 4: Run cleanup tests to verify pass**

Run: `go test ./internal/lifecycle -run 'TestDeleteSession_DoesNotRemoveSharedHerdrMachine|TestCleanupHerdr_RemovesLegacySessionMachine|TestDown_RemovesInstanceHerdrMachine' > /tmp/bivouac-herdr-cleanup.log 2>&1`

Expected: PASS.

- [ ] **Step 5: Run broader lifecycle tests**

Run: `go test ./internal/lifecycle > /tmp/bivouac-lifecycle.log 2>&1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/lifecycle/herdrattach.go internal/lifecycle/delete.go internal/lifecycle/merge.go internal/lifecycle/down.go internal/lifecycle/*_test.go
git commit -m "Clean shared Herdr machines on down"
```

---

### Task 6: Documentation and full verification

**Files:**
- Modify: `docs/architecture.md`
- Modify if needed: `README.md`
- Modify if needed: tests touched by changed command output.

**Interfaces:**
- Consumes: completed behavior from Tasks 1-5.
- Produces: docs that describe `~/sessions/<session>/<owner>/<repo>` and one Herdr machine per instance.

- [ ] **Step 1: Write failing documentation check if an existing test covers docs examples**

Search for tests asserting architecture/README strings. If none exist, skip this step and update docs directly in Step 3.

- [ ] **Step 2: Run relevant docs/example tests if any**

Run the package owning those tests with output redirected. Expected: FAIL if stale path strings are asserted.

- [ ] **Step 3: Update documentation**

In `docs/architecture.md`, update the session path examples from flattened `<repo>`/`<org-repo>` to `<owner>/<repo>`. Update the Herdr command surface/description to say saved machines are instance-scoped and sessions are workspaces beneath them when attaching from inside Herdr.

Update `README.md` only if it contains concrete conflicting path or Herdr machine descriptions.

- [ ] **Step 4: Run full Go suite**

Run: `go test ./... > /tmp/bivouac-go-test.log 2>&1`

Expected: PASS.

- [ ] **Step 5: Run pre-commit**

Run: `pre-commit run --all-files`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add docs/architecture.md README.md cmd internal docs/superpowers/specs/2026-10-10-session-paths-and-herdr-machine-identity-design.md docs/superpowers/plans/2026-10-10-session-paths-and-herdr-machine-identity.md
git commit -m "Document session paths and Herdr machines"
```
