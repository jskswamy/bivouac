# Bivouac Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a tag-driven release pipeline that publishes versioned bivouac binaries, checksums, release notes, an installer, and a Nix package.

**Architecture:** Keep runtime changes limited to build metadata flowing from `main` into the Cobra root command. Distribution is handled outside the binary by GoReleaser, GitHub Actions, a POSIX installer script, Make targets, and a flake package output.

**Tech Stack:** Go 1.26, Cobra, GoReleaser v2, GitHub Actions, POSIX `sh`, Nix flakes and `buildGoModule`.

**Spec:** `docs/superpowers/specs/2026-10-13-bivouac-release-design.md`

## Global Constraints

- Release tags match `v*`.
- GoReleaser builds `darwin` and `linux` for `amd64` and `arm64`.
- Release archive names are `bivouac_{{ .Version }}_{{ .Os }}_{{ .Arch }}.tar.gz`.
- Release archives include `LICENSE` and `README.md`.
- Checksums are published as `sha256sums.txt`.
- GoReleaser reads `RELEASE_NOTES.md` as the GitHub release header.
- Do not add Homebrew publishing in this implementation.
- Do not add a persistent `CHANGELOG.md` in this implementation.
- Do not require conventional commits.
- Keep human CLI behavior unchanged except `bivouac --version`.
- Run `go test ./...` with output redirected to a file, not piped.

## Review Focus

- Dirty or untagged local builds should show useful `--version` metadata rather than empty strings; Task 1 tests default and injected values.
- Installer OS and architecture mapping should fail clearly for unsupported platforms; Task 3 includes static shell validation and explicit case branches.
- Release notes must be curated prose, not only generated commits; Task 2 wires `RELEASE_NOTES.md` into GoReleaser and documents the workflow.
- Nix packaging must not break existing dev shells and checks; Task 5 runs `nix build` and `nix flake check`.
- Release workflow must not publish artifacts before verification succeeds; Task 6 makes GoReleaser depend on the verification job.

---

## File Structure

- `main.go`: owns ldflag-injected `version`, `commit`, and `date` variables and passes them into `cmd.Execute`.
- `cmd/root.go`: defines build metadata type/defaults, applies metadata to the Cobra root command, and keeps existing command registration unchanged.
- `cmd/root_test.go`: tests `--version` formatting for default and injected metadata.
- `.goreleaser.yml`: GoReleaser v2 release configuration.
- `RELEASE_NOTES.md`: hand-written release header source for GitHub releases.
- `AGENTS.md`: repo workflow rule for updating release notes with user-facing specs.
- `install.sh`: checksum-verifying installer for GitHub release tarballs.
- `README.md`: release badge and install instructions.
- `Makefile`: release-aware build/install targets and metadata ldflags.
- `flake.nix`: `packages.default` Go package output while preserving dev shells and checks.
- `.github/workflows/release.yml`: tag-triggered verification and GoReleaser publication.

---

### Task 1: Add version metadata to the CLI

**Files:**
- Modify: `main.go`
- Modify: `cmd/root.go`
- Test: `cmd/root_test.go`

**Interfaces:**
- Produces: `type BuildInfo struct { Version string; Commit string; Date string }` in package `cmd`.
- Produces: `func Execute(info BuildInfo) error` in package `cmd`.
- Produces: `func newRootCmdWithBuildInfo(info BuildInfo) *cobra.Command` for tests and `Execute`.
- Preserves: `func newRootCmd() *cobra.Command`, returning a root command with dev defaults for existing tests.

- [ ] **Step 1: Write the failing default version test**

Add this test to `cmd/root_test.go`:

```go
func TestRootCommandVersionDefault(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"--version"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	want := "bivouac dev (commit: none, built: unknown)\n"
	if out.String() != want {
		t.Fatalf("version output = %q, want %q", out.String(), want)
	}
}
```

- [ ] **Step 2: Write the failing injected version test**

Add this test to `cmd/root_test.go`:

```go
func TestRootCommandVersionInjected(t *testing.T) {
	root := newRootCmdWithBuildInfo(BuildInfo{Version: "v1.2.3", Commit: "abc1234", Date: "2026-10-13T10:00:00Z"})
	root.SetArgs([]string{"--version"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	want := "bivouac v1.2.3 (commit: abc1234, built: 2026-10-13T10:00:00Z)\n"
	if out.String() != want {
		t.Fatalf("version output = %q, want %q", out.String(), want)
	}
}
```

- [ ] **Step 3: Run the new tests and verify they fail**

Run: `go test ./cmd -run 'TestRootCommandVersion' > /tmp/bivouac-version-test.log 2>&1`

Expected: FAIL because `newRootCmdWithBuildInfo` and `BuildInfo` do not exist, or because `--version` is not configured.

- [ ] **Step 4: Implement build metadata in `cmd/root.go`**

Add `BuildInfo`, `defaultBuildInfo`, and `newRootCmdWithBuildInfo(info BuildInfo) *cobra.Command`. Move the current `newRootCmd` body into `newRootCmdWithBuildInfo`, set `root.Version`, and call `root.SetVersionTemplate("bivouac " + info.Version + " (commit: " + info.Commit + ", built: " + info.Date + ")\n")`. Keep `newRootCmd()` as a wrapper around `newRootCmdWithBuildInfo(defaultBuildInfo())`.

- [ ] **Step 5: Update `cmd.Execute` and `main.go`**

Change `cmd.Execute` to `func Execute(info BuildInfo) error` and have it call `newRootCmdWithBuildInfo(info)`. In `main.go`, add package variables `version`, `commit`, and `date` with dev defaults, then call `cmd.Execute(cmd.BuildInfo{Version: version, Commit: commit, Date: date})`.

- [ ] **Step 6: Run version and root command tests**

Run: `go test ./cmd -run 'TestRootCommand|TestRootCmd' > /tmp/bivouac-version-test.log 2>&1`

Expected: PASS.

- [ ] **Step 7: Commit**

Use the commit-action workflow with subject `Add release version metadata` and a wrapped body explaining that release builds now expose ldflag-injected version details without changing normal command behavior.

---

### Task 2: Add GoReleaser and release notes

**Files:**
- Create: `.goreleaser.yml`
- Create: `RELEASE_NOTES.md`
- Modify: `AGENTS.md`

**Interfaces:**
- Consumes: Task 1 `main.version`, `main.commit`, and `main.date` ldflag targets.
- Produces: GoReleaser archives named `bivouac_{{ .Version }}_{{ .Os }}_{{ .Arch }}.tar.gz` and release header from `RELEASE_NOTES.md`.

- [ ] **Step 1: Create `RELEASE_NOTES.md`**

Create the file with this initial content:

```markdown
## Unreleased

### ✨ New

- Bivouac now has a release pipeline: tagged releases publish binaries,
  checksums, release notes, and a checksum-verifying installer.

<!--
Add release notes here as specs ship. Group under the headings below. When
tagging, replace `## Unreleased` with `## vX.Y.Z`, commit, then tag.

Headings (use only what applies; drop empty sections):
  ### 💥 Breaking changes
  ### ✨ New
  ### 🐛 Fixed
  ### 🔧 Internal

After tagging, reset this file to this template.
-->
```

- [ ] **Step 2: Create `.goreleaser.yml`**

Create a GoReleaser v2 config with one build:

- `binary: bivouac`
- `main: .`
- `CGO_ENABLED=0`
- `goos: [darwin, linux]`
- `goarch: [amd64, arm64]`
- `mod_timestamp: "{{ .CommitTimestamp }}"`
- flags: `-trimpath`
- ldflags: `-s -w`, `-X main.version={{.Version}}`, `-X main.commit={{.ShortCommit}}`, `-X main.date={{.CommitDate}}`
- archives: `tar.gz`, `name_template: "bivouac_{{ .Version }}_{{ .Os }}_{{ .Arch }}"`, files `LICENSE` and `README.md`
- checksum name `sha256sums.txt`
- changelog sort `asc`
- snapshot version template `{{ .Version }}-next`
- release GitHub owner/name `jskswamy/bivouac`
- release header `{{ readFile "RELEASE_NOTES.md" }}`

- [ ] **Step 3: Document release-note rule in `AGENTS.md`**

Add a short `## Release Notes` section after the existing commit-message guidance. It must say user-facing changes go in `RELEASE_NOTES.md`, GoReleaser reads it as the release header, specs with user-facing changes update it in the same commit, and tag-time flow is stamp, tag, publish, reset.

- [ ] **Step 4: Validate GoReleaser config when available**

Run: `command -v goreleaser >/dev/null && goreleaser check || true`

Expected: If GoReleaser is installed, PASS. If it is not installed, no repository files change and the later GitHub workflow remains the real validation.

- [ ] **Step 5: Run YAML syntax validation**

Run: `pre-commit run check-yaml --files .goreleaser.yml > /tmp/bivouac-goreleaser-yaml.log 2>&1`

Expected: PASS.

- [ ] **Step 6: Commit**

Use the commit-action workflow with subject `Add release notes and GoReleaser` and a wrapped body explaining the hand-written release header plus generated commit audit trail.

---

### Task 3: Add the release installer and README install docs

**Files:**
- Create: `install.sh`
- Modify: `README.md`

**Interfaces:**
- Consumes: Task 2 archive naming and checksum file.
- Produces: installer contract with `INSTALL_DIR` and `VERSION` environment variables.

- [ ] **Step 1: Create `install.sh`**

Create a POSIX `sh` script with:

- `set -e`
- `REPO="jskswamy/bivouac"`
- `BINARY="bivouac"`
- `INSTALL_DIR="${INSTALL_DIR:-./bin}"`
- `log()` and `fail()` helpers prefixed with `bivouac-installer:`
- OS support for only `darwin` and `linux`
- arch mapping `x86_64 -> amd64`, `arm64|aarch64 -> arm64`
- latest release lookup from `https://api.github.com/repos/${REPO}/releases/latest` when `VERSION` is unset
- archive name `bivouac_${VERSION#v}_${OS}_${ARCH}.tar.gz`
- download from GitHub releases
- checksum download and verification with `sha256sum` or `shasum -a 256`
- extraction to a temp dir and copy to `${INSTALL_DIR}/bivouac`
- PATH hint when `INSTALL_DIR` is not on `PATH`

- [ ] **Step 2: Run shell syntax validation**

Run: `sh -n install.sh`

Expected: PASS.

- [ ] **Step 3: Update README badges and install section**

Add a GitHub release badge near the existing CI badge:

```markdown
[![Release](https://img.shields.io/github/v/release/jskswamy/bivouac)](https://github.com/jskswamy/bivouac/releases/latest)
```

Add an `## Install` section before the quickstart commands with the three installer examples from the spec and a `go install github.com/jskswamy/bivouac@latest` fallback.

- [ ] **Step 4: Check README references**

Run: `rg -n 'install.sh|github.com/jskswamy/bivouac/releases|go install github.com/jskswamy/bivouac' README.md`

Expected: output includes the installer URL, release URL via badge, and go install fallback.

- [ ] **Step 5: Commit**

Use the commit-action workflow with subject `Add release installer` and a wrapped body explaining the checksum verification and documented install paths.

---

### Task 4: Make local builds release-aware

**Files:**
- Modify: `Makefile`

**Interfaces:**
- Consumes: Task 1 ldflag targets.
- Consumes: Task 2 `.goreleaser.yml` for `make install`.
- Produces: `make build`, `make install`, and `make install-dev` behavior from the spec.

- [ ] **Step 1: Update Makefile metadata variables**

Add `VERSION`, `COMMIT`, `BUILD_DATE`, and `LDFLAGS` variables exactly as specified. Keep `NAME := bivouac`, `BIN := out/$(NAME)`, and `GOBIN ?= $(HOME)/go/bin`.

- [ ] **Step 2: Update build and install targets**

Change `build` to run `nix develop --command go build -ldflags "$(LDFLAGS)" -o $(BIN) .`. Change `install` to require `goreleaser`, run `goreleaser build --single-target --clean --output $(BIN)`, install `$(BIN)` to `$(GOBIN)/$(NAME)`, and print the installed path. Add `install-dev` that runs the fast ldflag `go build` path and installs it.

- [ ] **Step 3: Validate Makefile help includes install-dev**

Run: `make help > /tmp/bivouac-make-help.log && rg 'install-dev|install|build' /tmp/bivouac-make-help.log`

Expected: output includes `build`, `install`, and `install-dev`.

- [ ] **Step 4: Validate release-aware local build**

Run: `make build > /tmp/bivouac-make-build.log 2>&1 && ./out/bivouac --version`

Expected: command succeeds and prints `bivouac <git describe output> (commit: <short sha>, built: <commit date>)`.

- [ ] **Step 5: Commit**

Use the commit-action workflow with subject `Make builds release-aware` and a wrapped body explaining the difference between release-matching install and fast dev install.

---

### Task 5: Add a Nix package output

**Files:**
- Modify: `flake.nix`
- Modify: `flake.lock` only if Nix changes require it

**Interfaces:**
- Consumes: Task 1 ldflag targets.
- Produces: `packages.${system}.default` and keeps `devShells` and `checks` intact.

- [ ] **Step 1: Add `packages.default` to each system**

In the per-system attrset in `flake.nix`, add `packages.default = pkgs.buildGoModule { ... };` with:

- `pname = "bivouac"`
- `version = self.shortRev or self.dirtyShortRev or "dev"`
- `src = self`
- `vendorHash = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="` as a temporary placeholder
- `doCheck = false`
- ldflags `-s`, `-w`, `-X main.version=...`, `-X main.commit=...`
- meta description, homepage `https://github.com/jskswamy/bivouac`, and `mainProgram = "bivouac"`

- [ ] **Step 2: Capture the real vendor hash**

Run: `nix build .# > /tmp/bivouac-nix-build.log 2>&1 || true`

Expected: FAIL with a message containing the correct `got: sha256-...` vendor hash.

- [ ] **Step 3: Replace the vendor hash**

Update `vendorHash` in `flake.nix` to the hash from Step 2.

- [ ] **Step 4: Build the package**

Run: `nix build .# > /tmp/bivouac-nix-build.log 2>&1`

Expected: PASS and `./result/bin/bivouac --version` prints dev revision metadata.

- [ ] **Step 5: Run flake checks**

Run: `nix flake check > /tmp/bivouac-flake-check.log 2>&1`

Expected: PASS, or if an unrelated known `govulncheck` stdlib CVE blocks the check, capture the exact failure and stop for user decision before skipping anything.

- [ ] **Step 6: Commit**

Use the commit-action workflow with subject `Package bivouac in the flake` and a wrapped body explaining that the package output is for repository-flake installation, not Nixpkgs publication.

---

### Task 6: Add the release workflow and final verification

**Files:**
- Create: `.github/workflows/release.yml`
- Modify: `.github/workflows/ci.yml` only if extracting reusable jobs is simpler than duplication

**Interfaces:**
- Consumes: Task 2 `.goreleaser.yml`.
- Consumes: Task 5 flake package/checks.
- Produces: tag-triggered GitHub release publication after verification.

- [ ] **Step 1: Create `.github/workflows/release.yml`**

Create a workflow named `Release` triggered by:

```yaml
on:
  push:
    tags:
      - "v*"
```

Add permissions `contents: read` by default. Add a `verify` job on `ubuntu-latest` that checks out with full history, installs Nix with sandbox disabled, enables the magic Nix cache, runs `nix flake check --print-build-logs`, and runs `nix develop --command bash -c 'go build ./... && go vet ./... && go test ./...'`.

- [ ] **Step 2: Add the GoReleaser job**

Add a `goreleaser` job needing `verify`, running on `ubuntu-latest`, with `permissions.contents: write`. It must check out with `fetch-depth: 0`, set up Go with `go-version-file: go.mod`, and run `goreleaser/goreleaser-action` with `args: release --clean` and `GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}`.

- [ ] **Step 3: Validate workflow YAML syntax**

Run: `pre-commit run check-yaml --files .github/workflows/release.yml > /tmp/bivouac-release-yaml.log 2>&1`

Expected: PASS.

- [ ] **Step 4: Run the full Go test suite**

Run: `go test ./... > /tmp/bivouac-release-final-test.log 2>&1`

Expected: PASS.

- [ ] **Step 5: Run pre-commit**

Run: `pre-commit run --all-files > /tmp/bivouac-release-precommit.log 2>&1`

Expected: PASS. If `govulncheck` reports only unrelated current Go stdlib CVEs, record the output and ask before skipping that hook for a commit.

- [ ] **Step 6: Run final release surface checks**

Run these commands:

```bash
sh -n install.sh
go test ./cmd -run 'TestRootCommandVersion' > /tmp/bivouac-version-final.log 2>&1
rg -n 'RELEASE_NOTES.md|readFile "RELEASE_NOTES.md"|bivouac_{{ .Version }}_{{ .Os }}_{{ .Arch }}' .goreleaser.yml AGENTS.md
```

Expected: all commands pass and `rg` finds the release notes and archive naming references.

- [ ] **Step 7: Commit**

Use the commit-action workflow with subject `Release bivouac from tags` and a wrapped body explaining that tagged commits are verified before GoReleaser publishes artifacts.

---

## Final branch checks

- [ ] Run `refactor:scan` against the changed code and config, then address or document findings.
- [ ] Run `commit-tools:review-commits` to collapse any TDD churn into logical commits.
- [ ] Run `validate-commits --base <pre-feature-base>` or equivalent deterministic checks before push.
- [ ] Confirm `git status --short` is clean.
- [ ] Confirm `go test ./... > /tmp/bivouac-release-handoff-test.log 2>&1` passes.

## Self-Review Notes

- Spec coverage: version metadata, GoReleaser, installer, Makefile, Nix package, release workflow, README, and release notes each have an owning task.
- Step scan: each task has a specific file set, command, and expected result; code bodies are avoided except where exact file contents are workflow/template data.
- Type consistency: `BuildInfo`, `newRootCmdWithBuildInfo`, and `Execute(info BuildInfo)` are defined once in Task 1 and consumed by build tooling through ldflags only.
- Review Focus: all five listed failure modes map to tests or verification steps in Tasks 1, 2, 3, 5, and 6.
- Proportion: the plan is longer than the spec because release work spans code, CI, Nix, shell, docs, and workflow rules; implementation details are limited to values the spec pins.
