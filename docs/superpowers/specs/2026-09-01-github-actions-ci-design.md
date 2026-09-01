# GitHub Actions CI

## Status

Proposed

## Context

Pre-commit hooks (`git-hooks.nix`, wired up alongside the dev-tooling
work) are the only automated gate on this repo's code quality today —
and on the primary dev machine, the local git hook itself can't even
auto-install: Santa/SentinelOne (this machine's endpoint security
tooling) blocks writes to `.git/hooks/`. `pre-commit run --all-files`
and `nix flake check` both work correctly when run by hand, but
nothing forces that to happen before a commit or push lands.

cloudlab is now pushed to a real GitHub remote
(`jskswamy/cloudlab`), so a CI workflow is the first thing that
reliably enforces build/lint/test on every change, independent of
whichever machine or security tooling a contributor's local machine
happens to have. This is worth doing now, before the Provisioning
sub-project adds more surface area to regress.

The CLI itself is meant to run on both Linux and macOS end-user
machines (not just macOS, which is only the primary dev machine
today), so CI needs to actually verify both, not just the platform
most convenient to develop on.

## Decisions

- **Trigger: push to `main` and every pull request.** Works whether
  changes land via direct push (today's workflow) or a PR-based flow
  later, without needing to touch the workflow file when that changes.
- **Toolchain: the same Nix flake used locally, not a lighter
  native Go+Pkl setup.** Zero drift between what CI checks and what
  `nix develop` gives a contributor locally — the exact same Pkl
  version, the exact same lint config, because it's literally the same
  `flake.nix`. The alternative (`actions/setup-go` + a separately
  installed Pkl binary) would mean two toolchain definitions to keep in
  sync, and a version bump in one that's forgotten in the other
  silently diverges CI from local dev. This choice has one real cost,
  not discovered until implementation: `nix flake check`'s build
  sandbox has no network access, and on Linux (unlike macOS, where the
  sandbox defaults off) that blocks `golangci-lint`'s hook from
  resolving this repo's Go module dependencies (`pkl-go`, `godo`,
  `cobra`, etc.), since `HOME`/`GOMODCACHE` start empty on every
  sandboxed build. The fix is `sandbox = false` via `extra-conf` on the
  Nix installer action — worth documenting here because it's a direct
  consequence of reusing the local flake as-is in CI, not an unrelated
  workaround.
- **Platform matrix: `ubuntu-latest` and `macos-latest`, not just
  one.** cloudlab's own CLI is meant to run on both Linux and macOS end
  users' machines — this isn't about developer convenience, it's
  validating the actual supported deployment targets. `flake.nix`
  already supports 4 systems; CI covers the two GitHub-hosted runner
  platforms among them.
- **Cache the Nix store between runs**, via a Nix-caching action
  (e.g. `DeterminateSystems/magic-nix-cache-action`) alongside the Nix
  installer action. Avoids re-fetching and rebuilding the same
  nixpkgs/pkl/git-hooks.nix derivations from scratch on every run.
  Meaningfully faster after the first run, on both matrix legs.
- **Two separate steps, not one combined command**: `nix flake check`
  (builds the devShell, runs every configured `git-hooks.nix` hook —
  `gofmt`, `golangci-lint`, `deadnix`, `nixfmt-rfc-style`,
  `trufflehog`, general hygiene) and, separately,
  `nix develop --command bash -c 'go build ./... && go vet ./... && go test ./...'`.
  `go test` isn't one of the configured pre-commit hooks, so `nix
  flake check` alone doesn't exercise the Go test suite — the second
  step is what actually proves behavioral correctness.
- **No secrets required.** The `digitalocean` provider's tests all use
  `httptest` mock servers, never real DigitalOcean API calls — the
  full test suite runs with zero credentials configured anywhere in
  CI.
- **`concurrency` cancellation, keyed on workflow + ref.** A new commit
  on the same branch/PR cancels whatever CI run was already in
  progress for it, rather than letting a stale run finish and waste
  minutes. Free to add, standard practice.

## Architecture

```
.github/workflows/ci.yml
```

One workflow file, one job (`test`), matrixed over `os: [ubuntu-latest,
macos-latest]`. No separate lint/test jobs — small enough a project
that splitting them would only add coordination overhead (two log
tabs to check instead of one, a second job's worth of Nix-install
cold-start) without a real benefit yet.

## Components

```yaml
name: CI

on:
  push:
    branches: [main]
  pull_request:

concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

jobs:
  test:
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: DeterminateSystems/nix-installer-action@main
      - uses: DeterminateSystems/magic-nix-cache-action@main
      - run: nix flake check --print-build-logs
      - run: nix develop --command bash -c 'go build ./... && go vet ./... && go test ./...'
```

`--print-build-logs` on `nix flake check` so a hook failure's actual
output (e.g. a `golangci-lint` finding) is visible in the Action's log
directly, not just a bare "check failed."

## Data Flow

1. A push to `main`, or a pull request (opened/updated), triggers the
   workflow.
2. `concurrency` cancels any still-running instance of this workflow
   for the same ref, if one exists.
3. Each matrix leg (`ubuntu-latest`, `macos-latest`) runs independently
   and in parallel: checkout, install Nix, restore/prime the Nix
   store cache, run `nix flake check`, then run the Go build/vet/test
   step.
4. Either matrix leg failing fails the overall workflow run; GitHub
   surfaces per-leg status separately (so a Linux-only or macOS-only
   failure is distinguishable at a glance).

## Error Handling

- A `git-hooks.nix` hook failure (e.g. `gofmt` finds unformatted code,
  `golangci-lint` finds a real issue) fails `nix flake check` with the
  specific hook's own output visible in the log — the same message a
  contributor would see running `pre-commit run --all-files` locally.
- A Go test failure fails the second step with the normal `go test`
  output — same as running `go test ./...` locally.
- Nothing in this workflow depends on network access to anything
  other than public endpoints already required for local development
  (nixpkgs binary cache, `github.com/apple/pkl` release assets,
  `pkg.pkl-lang.org`'s `pkl.golang` package) — GitHub-hosted runners
  have full internet access, so none of the network-related
  workarounds this session needed for a sandboxed local shell apply
  here.

## Testing

There's no unit-testable "test" for a CI workflow definition beyond
watching it actually run. Verification is: push this file, open the
resulting Action run, and confirm both matrix legs go green.

That original prediction turned out to be only half right, caught
during review rather than by an actual CI run: the `ubuntu-latest`
leg was going to fail `nix flake check` on the very first push,
because the sandboxed build's lack of network access blocks
`golangci-lint`'s Go module resolution (see Decisions) — a gap this
spec didn't anticipate. `sandbox = false` on the Nix installer step
fixes it; `macos-latest` was never going to hit this, since its
sandbox defaults off. This was verified locally as far as this
machine (macOS) can: `nix flake check` passes here both before and
after the fix, which only proves the YAML and hook config are sound,
not that the Linux leg itself goes green — that still needs an actual
push and a real Action run to confirm, same as the original plan
called for. `TestLoadFromPath_MinimalFixture` (internal/config) is
still expected to pass cleanly in CI on both platforms, for the
reason already stated above — that part hasn't changed.

## Out of scope

- Publishing build artifacts / a release workflow — this spec is CI
  (verify every change), not CD (ship a binary). A release workflow is
  a separate, later concern once there's something worth tagging a
  release of.
- Branch protection rules requiring this check to pass before merge —
  a GitHub repo-settings change, not something expressed in the
  workflow file itself. Worth doing once this workflow's been observed
  running green at least once.
