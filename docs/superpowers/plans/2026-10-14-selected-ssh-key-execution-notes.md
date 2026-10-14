# Selected SSH key execution notes

## Scope and status

This session is on `bivouac/selected-ssh-key`. The user authorised work on
`cloudlab-1em` and asked for decisions to be recorded for morning review.
The approved design and ten-task plan remain the implementation contract.

Task 1 (`cloudlab-1em.1`) is implemented and tested. Tasks 2–10 remain
unimplemented. **This is an intermediate implementation, not a completed
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

Next unblocked task: Task 2 (`cloudlab-1em.2`), selected local signing
capabilities and revalidation. Use matching fake agents/custom private
fixtures, cancellation tests, and path-preserving discovery deduplication.
Later tasks must prove ordinary Git and persistent Herdr transport, not only
argv construction. No external transport integration has been verified here.
