# GitHub Actions CI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a GitHub Actions workflow (`.github/workflows/ci.yml`) that runs the exact same Nix-based checks (`nix flake check` + `go build`/`go vet`/`go test`) already used locally, on both `ubuntu-latest` and `macos-latest`, on every push to `main` and every pull request.

**Architecture:** One workflow file, one job, matrixed over two OS runners. Nix is installed via `DeterminateSystems/nix-installer-action`, the Nix store cached via `DeterminateSystems/magic-nix-cache-action`, then two steps run: `nix flake check` (build + every `git-hooks.nix` hook) and `nix develop --command bash -c 'go build ./... && go vet ./... && go test ./...'` (actual test-suite correctness, which `nix flake check` alone doesn't cover).

**Tech Stack:** GitHub Actions YAML, `DeterminateSystems/nix-installer-action@main`, `DeterminateSystems/magic-nix-cache-action@main`, `actions/checkout@v4`. No new Go/Nix dependencies.

## Global Constraints

- Workflow file path: `.github/workflows/ci.yml` (GitHub only discovers workflows under `.github/workflows/`).
- No secrets required anywhere in this workflow — the `digitalocean` provider's tests use `httptest` mocks exclusively, never a real DigitalOcean API call.
- Do not add `actionlint` or any new tool to `flake.nix` for this plan — the existing `check-yaml` `git-hooks.nix` hook already validates this file's YAML syntax locally via `nix flake check`, and the real verification is watching the workflow actually run on GitHub (per the design spec's own Testing section — there's no meaningful unit test for a CI workflow definition beyond that).
- Match the design spec's exact YAML (`docs/superpowers/specs/2026-09-01-github-actions-ci-design.md`) — trigger on `push: branches: [main]` and `pull_request`, a `concurrency` block keyed on `${{ github.workflow }}-${{ github.ref }}` with `cancel-in-progress: true`, and the `os: [ubuntu-latest, macos-latest]` matrix.

---

### Task 1: Add the CI workflow and a status badge

**Files:**
- Create: `.github/workflows/ci.yml`
- Modify: `README.md` (add a CI status badge near the top)

**Interfaces:**
- Consumes: nothing from other tasks — this is the only task in this plan.
- Produces: nothing consumed by later tasks in this plan; Task 2 is verification of what this task produces.

- [ ] **Step 1: Write the workflow file**

Create `.github/workflows/ci.yml`:

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

- [ ] **Step 2: Validate the new file locally via the existing check-yaml hook**

Run (inside `nix develop`, from the repo root): `nix flake check --print-build-logs`

Expected: PASS — `check-yaml` (already configured in `flake.nix`) parses `.github/workflows/ci.yml` as part of the same `pre-commit-check` run as every other hook. This is the same command Task 2 will later confirm passes on GitHub's own runners; running it locally first catches a YAML typo before ever pushing.

If `check-yaml` fails: the error names the exact line/column of the syntax problem — fix it and rerun this step before continuing.

- [ ] **Step 3: Add a CI status badge to README.md**

Open `README.md` and add this line immediately after the `# cloudlab` title (before the first paragraph):

```markdown
[![CI](https://github.com/jskswamy/cloudlab/actions/workflows/ci.yml/badge.svg)](https://github.com/jskswamy/cloudlab/actions/workflows/ci.yml)
```

- [ ] **Step 4: Run the full local verification once more**

Run (inside `nix develop`, from the repo root):

```bash
nix flake check --print-build-logs
go build ./... && go vet ./... && go test ./...
```

Expected: both PASS (aside from `TestLoadFromPath_MinimalFixture` in `internal/config`, which is expected to fail on this specific machine only — see Global Constraints in the design spec's Testing section — and is not something this task's changes affect).

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/ci.yml README.md
git commit -m "Add GitHub Actions CI workflow"
```

---

### Task 2: Push and verify the workflow runs green on both platforms

**Files:** none created or modified — this task is verification only.

**Interfaces:**
- Consumes: the committed `.github/workflows/ci.yml` from Task 1.
- Produces: confirmation (a passing GitHub Actions run) that this plan is complete. Nothing later in this repo depends on this task's output programmatically.

- [ ] **Step 1: Push the commit**

```bash
git push
```

main is already tracking `origin/main` (confirmed earlier this session), so a bare `git push` is sufficient — no `-u`/upstream flag needed.

- [ ] **Step 2: Open the workflow run and watch both matrix legs**

```bash
gh run watch --exit-status
```

If `gh` isn't usable in the current environment (this session hit a `gh: operation not permitted` config-read error earlier when trying to read `~/.config/gh/config.yml` — a sandbox restriction, not a `gh` installation problem), report that plainly and ask the user to check the Actions tab on GitHub directly (`https://github.com/jskswamy/cloudlab/actions`) instead of guessing at the result.

- [ ] **Step 3: Confirm both matrix legs (`ubuntu-latest`, `macos-latest`) show green**

Expected: both legs pass, including `TestLoadFromPath_MinimalFixture` — which has been failing locally all session due to this one dev machine's endpoint-security lock on `~/.pkl`, and should pass cleanly on GitHub's unrestricted runners. A green run here is the confirmation that failure really was local-machine-specific, not a latent bug.

If either leg fails: read the specific failing step's log (not just the red X) before proposing any fix — the failure could be anything from a real code bug to a runner-specific Nix quirk neither of us has hit locally.
