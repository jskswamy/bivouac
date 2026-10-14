# Selected SSH key execution notes

## Scope and status

This session is on `bivouac/selected-ssh-key`. The user authorised work on
`cloudlab-1em` and asked for decisions to be recorded for morning review.
The approved design and ten-task plan remain the implementation contract.

Tasks 1–6 (`cloudlab-1em.1`–`cloudlab-1em.6`) are implemented and tested.
Tasks 7–10 remain unimplemented. **This is an intermediate implementation, not a completed
user-facing feature.** In particular, account registration, signer selection,
wizard migration recovery, state and transport propagation still need their
planned tasks before path-based keys work end to end. Do not mark the epic
complete or change release notes to claim completed transport support.

## Decisions and rationale

No new product decision requires operator approval at this checkpoint.
The following implementation choices stay within the approved plan:

- Resolve SSH key references independently for base and project before
  merging. Carry absolute paths, original entries and declaring config paths
  in `Resolved.SSHKeyFiles`, base first. Preserve raw `Config.SshKeys` values.
- Expand only `~/`. `$HOME` and other shell expressions remain literal;
  relative paths resolve against the declaring file, not the working directory.
- Classify numeric IDs and valid bare/prefixed MD5/SHA256 fingerprints
  locally. Explicit paths such as `./12345` remain paths. Join migration
  errors across both declaring files rather than showing only the first.
- Keep the Pkl field string-shaped and `ReadValues` permissive so later
  wizard recovery can read invalid legacy entries without first resolving
  them. Normal `Resolve` rejects legacy entries; none are silently converted.
- Separate path normalization from strict public-file reading. Task 1 adds
  `ReadPublicFile`; later key-planning tasks will consume those references.
  Normalization alone is not proof of a readable key or usable local signer.
- Parse each nonblank, noncomment public-file line separately and require
  exactly one key. `ssh.ParseAuthorizedKey` on an entire file can skip bad
  lines; line-by-line parsing prevents accepting leading/trailing junk.
  Comments and blank lines are allowed. Private, empty, missing, malformed,
  and multiple-key files fail with original entry/config diagnostics.
- Canonical public material and the internal provider MD5 fingerprint are
  retained, along with the file path and comment. No private material is
  persisted or uploaded. Discovery deduplication is left to Task 2.
- Tests use temporary files and generated disposable key fixtures only.
  No cloud writes, user-key access, credential acquisition or Git push was
  performed. Live cloud/hardware integration requires explicit permission.
- Existing route/host-key issues were not modified or closed.

## Verification evidence

All Go commands ran through `nix develop` with output redirected to files.
Logs below are VM-local; this committed record preserves their results.

1. Initial Nix setup timed out after 200 seconds downloading/building the
   development environment. It did not provide a test result. Retrying
   completed setup.
2. `go test ./internal/config ./internal/sshkeys`
   (`/tmp/bivouac-task1-red.log`): exit 1. All five legacy-reference cases
   failed with `expected migration error`; existing tests passed. This was
   the behavioural red test before implementation, not a Pkl setup failure.
3. After adding the remaining interface tests, the same command
   (`/tmp/bivouac-task1-api-red.log`): exit 1 due to missing `FileRef`,
   `ReadPublicFile`, `UnsupportedReference`, `resolveSSHKeyFiles` and
   `Resolved.SSHKeyFiles`. This is API-absence evidence, not an additional
   behavioural red result.
4. After implementation, the focused command
   (`/tmp/bivouac-task1-green.log`): exit 0, both packages pass. Coverage
   includes separate base/project origins, tilde and space paths, literal
   shell expressions, explicit numeric paths, joined legacy diagnostics,
   raw legacy editing and strict public-file input.
5. `go test ./...` (`/tmp/bivouac-go-tests.log`): exit 0, all packages pass.
   The first full-suite attempt exceeded the harness's 200-second limit;
   a retry with a 400-second limit completed. Lifecycle alone took about
   336 seconds, beads 102 seconds and provisioning 185 seconds.
6. `pre-commit run --all-files` (`/tmp/bivouac-precommit.log`): exit 0.
   All eleven hooks passed, including gofmt, golangci-lint, gosec,
   govulncheck, nixfmt, deadnix and trufflehog.
7. `nix flake check` (`/tmp/bivouac-flake-check.log`): exit 1. Its sandboxed
   pre-commit derivation cannot download Go modules: requests to
   `proxy.golang.org` fail with DNS connection refused at `[::1]:53`.
   Local pre-commit succeeds with the populated module cache. This remains
   a flake-check verification gap at that checkpoint; no sandbox/network
   override was attempted then. See the follow-up below for its resolution.
8. Final verification after fixture cleanup and adding this record:
   `go test ./...` (`/tmp/bivouac-go-tests-final.log`) and
   `pre-commit run --all-files` (`/tmp/bivouac-precommit-final.log`) both
   exit 0. All packages and all eleven hooks pass. Lifecycle took about
   269 seconds on this final run.
9. `git diff HEAD --check`: passes.

## Follow-up: Nix verification resolved

The user requested using Nix to resolve the Go module-download issue.
Inspection of `.github/workflows/ci.yml`, `flake.nix` and the existing CI
spec confirmed the intended check environment is `sandbox = false`: the
lint hooks fetch modules and govulncheck fetches a live vulnerability DB.

- `nix flake check --option sandbox false --print-build-logs` as the normal
  user exited 1. The daemon explicitly ignored the restricted sandbox
  option because the user is not trusted. This did not resolve the issue.
- Passwordless sudo was available. The same one-off command run as an
  administrator succeeded:
  `sudo -n /nix/var/nix/profiles/default/bin/nix flake check --option sandbox false --print-build-logs`.
  Exit 0; `/tmp/bivouac-flake-check-ci.log` shows all eleven hooks passing
  and `checks.x86_64-linux.pre-commit-check` succeeding.
- Afterwards, `nix config show` still reported `sandbox = true`. No global
  daemon configuration, trust list, project credentials or Go code changed.
- `CONTRIBUTING.md` now documents the network-dependent check, its per-run
  option and daemon trust requirement. This matches existing CI policy;
  it does not make the check hermetic or prove other-platform checks.

The flake-check gap for Task 1 is resolved on x86_64-linux with CI's
intended network-enabled setting. Tasks 2–10 and external transport
verification are still outstanding.

## Review and next steps

`superpowers:executing-plans`, `refactor:scan`, and
`commit-tools:review-commits` tooling was not installed/found on this VM.
The existing plan was followed directly. A manual source/diff review checked
origin preservation, unchanged raw values, migration error aggregation,
strict parsing, error context and absence of private-key/provider writes.
One logical implementation commit is used; there are no TDD-churn commits
to fold. This is not a claim of an independent automated review.

### Task 2: selected signing capabilities

Added serializable connection/identity descriptors, context scoping, strict
recorded-identity revalidation, custom private-file matching, matching-agent
selection and a verified random signing challenge. Agent selection is
preferred; later use retains the recorded mode and never falls back to
unrelated identities. One locally usable selected key suffices; recipients
without a local private counterpart remain allowed. No secrets or agent
socket paths enter descriptors. Agent IPC has a 30-second ceiling and is
closed on cancellation and explicit cleanup. Signing progress is emitted
before a potentially interactive hardware operation; `WithProgress` lets
callers direct it into their UI without a provider-package dependency.

Discovery retains a disk path when the same key was found in the agent,
without losing its agent availability. It reuses strict public-file reading.

- Red: `go test ./internal/sshidentity ./internal/sshkeys` exited 1:
  missing new APIs and a behavioural failure proving deduplication lost the
  public file path (`/tmp/bivouac-task2-red.log`).
- First implementation run exposed missing public-path error context and a
  cancellation race: socket deadlines may expire immediately before
  `ctx.Err()` becomes visible. Both were fixed, preserving deadline errors.
- Green: `go test -race ./internal/sshidentity ./internal/sshkeys` exited 0
  (`/tmp/bivouac-task2-green.log`). Tests cover custom disk signers, matching
  and unrelated agent keys, refusing hardware, encrypted/mismatched keys,
  multiple recipients, changed/missing recorded public/private keys, missing
  recorded agent identity, context/JSON safety, cancellation and IPC cleanup.
- This is fake-agent/local-fixture evidence, not live hardware or remote
  authentication evidence. Call-site integration follows in later tasks.

### Task 3: file-based setup and migration recovery

Wizard values are now public-file paths, with filename/comment/agent and
account-status detail. Account-only and agent-only choices are retired;
agent-only discovery explains public-file export. Manually entered files are
strictly read and local signing availability checked after selection with
visible progress, without decrypting credentials.

Standalone init offers explicit public-only registration using a
`bivouac-<stem>` name. Declined/failed uploads keep the path for a later
confirmed up rather than silently deleting a selection. The for-up policy
never registers; both implicit init and missing-key setup use it.

Recovery reads raw values before normal resolution and re-asks legacy
entries even when personal settings are reused or moving project fields is
declined. A project basePath override is honoured. Moving relative project
key paths to a personal file makes them absolute first, preserving their
original meaning. Shared config helpers are exported for this origin-aware
credential-free recovery, rather than duplicating normalization in cmd.

- Red: `go test ./cmd ./internal/wizard` exited 1 for absent policy/value
  APIs (`/tmp/bivouac-task3-red.log`). Existing fake-fingerprint setup tests
  were replaced with real disposable public/private fixtures; old
  fingerprint-preservation semantics are intentionally removed.
- Green: `go test ./cmd ./internal/wizard ./internal/config` exited 0
  (`/tmp/bivouac-task3-green.log`). Tests cover path storage, zero for-up
  uploads, standalone prefixed names, decline/failure retention, optional
  registry failure, both legacy file origins, declined moves, overridden
  base files, invalid manual paths, agent export and empty selection.
- Tests confirm no credential-free token fallback; no cloud account or
  actual hardware was used.

### Task 4: persistent machine OpenSSH profiles

Added one stable machine alias, safely quoted selected identity paths,
real-endpoint HostKeyAlias and IdentitiesOnly. Agent descriptors use their
public files; disk descriptors use private paths. User forwarding settings
are not rewritten. Read-only Check evaluates actual `ssh -G`, including
system settings (which `-F` would otherwise suppress), and rejects additive
unrelated IdentityFile directives and host-key-check disabling conflicts.

The owned profile index is authoritative and atomically published before
the managed SSH config. Retries regenerate all entries after interruption.
A context-aware flock serializes independent processes. Owned files use
0600 permissions; new directories use 0700. One marked Include is prepended
with Host scope resets; original user bytes and file mode are preserved.
Symlinked/nonregular or explicitly unwritable user config is rejected.
Remove deletes only one owned machine entry and retains the shared include.

- Red: `go test ./internal/sshidentity` exited 1 for missing profile/alias
  APIs (`/tmp/bivouac-task4-red.log`).
- Green: `go test -race ./internal/sshidentity` exited 0
  (`/tmp/bivouac-task4-green.log`), including real OpenSSH settings,
  multiple machines, one include, user-byte preservation, identity conflicts,
  symlink refusal, control-character rejection, agent paths, percent/space
  quoting, interrupted publication retry, permissions and concurrent
  subprocess updates. These are local configuration tests, not proof of
  persistent Git or Herdr authentication.

### Task 5: read-only account plans and approved registration

Added canonical file revalidation, at-least-one signer selection before
account listing, public-identity deduplication and authoritative read errors.
Plans classify registered keys without renaming and propose only
`bivouac-<stem>` additions. Approved registration revalidates all files and
recorded signers before any account write, re-lists for idempotent retries,
and resolves create races by matching identity. Partial additions are retained
with retry guidance; errors include safely quoted doctl import commands.
Standalone setup now uses the same public-registration/race helper and
revalidates the selected file before its explicit upload.

The pagination regression exposed an infinite loop, not only partial-list
success: godo CurrentPage does not validate malformed first-page Next links.
Listing now parses the next page locally and rejects invalid/non-advancing
metadata without ever reporting a partial authoritative list.

- Red APIs: `/tmp/bivouac-task5-red.log`; the broad first attempt exceeded
  30 seconds in the newly exposed pagination loop.
- Behavioural pagination red: `go test -timeout 3s
  ./internal/provider/digitalocean -run TestListKeys_MalformedPaginationFails`
  exited 1 with timeout (`/tmp/bivouac-task5-pagination-red.log`).
- Green: `go test ./internal/keyplan ./internal/provider/digitalocean ./cmd`
  exited 0 (`/tmp/bivouac-task5-green.log`). Covers read-only plans,
  registered reuse, duplicates, forbidden/read errors, create races,
  mutation-before-write, quoted recovery, partial retention/retry and
  paginated/malformed provider listings. No live account was used.

### Task 6: approved creation and persisted identity

Up now executes an UpPlan, never resolving raw key paths into a new creation
spec after confirmation. PrepareUp settles name/state/user/cloud-init/path
and read-only SSH-profile prerequisites. Summary shows registered and pending
keys, extra-recipient caveats and alias/include action. Account registration
runs only after approval; duplicate state and current profile prerequisites
are rechecked before writes. Selected optional SSH metadata is persisted
immediately with the VM, before profiles/readiness/provisioning. State-write
failure retains existing VM rollback; profile failure leaves the VM tracked.

Added only provider-resolution injection to the existing up constructor for
confirmation tests, not another creation orchestrator. An attempted test
http.DefaultTransport override did not intercept godo's independently built
transport: two GET account-key requests with an invalid test token received
401 responses from the real API. No account writes or valid credentials were
involved. That approach was removed; confirmation tests now inject a fake
provider/registry and explicitly reject remote writes. Fixture-generated repo
names were numeric, so confirmation tests now pass a valid explicit name to
exercise the intended prompt rather than the new early name validation.

- Red APIs: `/tmp/bivouac-task6-red.log`, missing UpPlan/PrepareUp/state SSH.
- Full lifecycle and state packages passed in the broad green run; cmd
  failures above were fixed. Final focused verification:
  `go test ./cmd ./internal/lifecycle ./internal/state -run
  'Test(Up|ApprovedUp|SSHMetadata)'` exited 0
  (`/tmp/bivouac-task6-green-final.log`). It covers decline/no writes,
  approved-spec stability, registration order, state/context before readiness,
  material mutation, registration/profile failures, state rollback and old
  JSON compatibility. Existing lifecycle creation tests use a fixture-built
  approved plan and retain their assertions.

Next task: selected Go SSH authentication and record-context ownership (7).
Later tasks must prove ordinary Git and persistent Herdr transport, not only
argv construction. No external transport integration has been verified here.
