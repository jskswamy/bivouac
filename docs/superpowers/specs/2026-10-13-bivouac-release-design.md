# Bivouac release design

## Intent

Bivouac should have a first-class release pipeline: a maintainer tags a
version, GitHub Actions verifies the tagged commit, GoReleaser publishes binary
archives and checksums, and users can install the latest release with a small
curlable installer. The local development path should keep working, but release
artifacts should carry real version metadata instead of always reporting a dev
build.

This is about making bivouac usable outside this checkout. Today a user can
build from source, but there is no tagged binary distribution path, no release
workflow, no install script, no release-notes workflow, and no Nix package
output. That makes onboarding and automation depend on cloning the repository
and knowing the development environment.

## Current state

Bivouac currently has the pieces needed for development, but not distribution:

- `main.go` delegates directly to `cmd.Execute()` and has no build metadata.
- `cmd/root.go` does not set Cobra `Version` or a version template.
- The Makefile builds into `out/bivouac` through `nix develop` and installs that
  local build, but does not inject version metadata or use GoReleaser.
- The flake exposes dev shells and checks, but no installable package.
- CI is Nix-based (`nix flake check`, then `nix develop --command go build ./...
  && go vet ./... && go test ./...`). Security scanning is a separate workflow.
- There is no `.goreleaser.yml`, `install.sh`, release workflow,
  `RELEASE_NOTES.md`, or release badge in the README.

The release pipeline should be conventional for a Go CLI:

- GoReleaser builds `darwin` and `linux` binaries for `amd64` and `arm64`,
  archives them as tarballs, includes `LICENSE` and `README.md`, and publishes
  `sha256sums.txt`.
- A tag-triggered GitHub Actions workflow verifies the tag before running
  GoReleaser with `release --clean`.
- An installer detects OS and architecture, fetches the latest GitHub release
  when `VERSION` is unset, downloads the matching tarball, verifies the archive
  against `sha256sums.txt`, and copies the binary into `INSTALL_DIR`.
- The build system has release-aware `VERSION`, `COMMIT`, and `BUILD_DATE`
  variables, an ldflags string matching GoReleaser, and separate `install` and
  `install-dev` paths.
- `flake.nix` exposes `packages.default` with a Go module build and ldflags, so
  `nix build` can produce the CLI binary.
- Release notes are hand-written while features are designed, then read by
  GoReleaser as the GitHub release header at tag time.

## Goals

1. A maintainer can cut a release by pushing a `v*` tag.
2. GitHub Actions verifies the tag before publishing artifacts.
3. GoReleaser publishes bivouac binaries for macOS and Linux on amd64 and arm64.
4. Release artifacts include checksums and enough project context to inspect the
   archive after download.
5. `bivouac --version` reports the release version, commit, and build date for
   release builds, and useful dev metadata for local builds.
6. Users can install with a curlable `install.sh`.
7. `make install` installs a GoReleaser-built single-target binary so local
   installs match release artifacts as closely as possible.
8. `make install-dev` remains available for fast local iteration.
9. `nix build` produces a bivouac package from the flake.
10. README documents the supported install paths.
11. User-facing changes have curated release notes that appear at the top of
    GitHub releases.

## Non-goals

- Do not build a full release CLI command inside bivouac.
- Do not change bivouac's runtime behavior apart from adding version output.
- Do not replace the existing Nix-based CI workflow unless the release workflow
  needs a smaller reusable step.
- Do not add Homebrew tap publishing until the target tap repository and formula
  update policy are known. The GitHub release tarballs are compatible with a
  future Homebrew formula, but this feature does not guess where to publish one.
- Do not publish Nixpkgs or flake registry entries. The repository flake should
  be enough for `nix build github:jskswamy/bivouac` style use.
- Do not require conventional commits. This repository uses classic commit
  messages, so release-note prose should not depend on commit prefixes.
- Do not maintain a separate long-form `CHANGELOG.md` in the first release
  implementation. GitHub release history plus `RELEASE_NOTES.md` is the source
  of release prose for now.

## Release behavior

### Version metadata

Add package-level variables in `main.go`:

```go
var (
    version = "dev"
    commit  = "none"
    date    = "unknown"
)
```

Pass those values into the command tree, either by changing `cmd.Execute()` to
accept a small build-info struct or by adding a `cmd.SetBuildInfo` style helper.
The command tree sets:

- `root.Version = version`
- `root.SetVersionTemplate("bivouac " + version + " (commit: " + commit + ", built: " + date + ")\n")`

The default `--help` output remains human-focused. Only `--version` changes.

### GoReleaser

Add `.goreleaser.yml`:

- `version: 2`
- binary name: `bivouac`
- main package: `.`
- `CGO_ENABLED=0`
- targets: `darwin/linux` and `amd64/arm64`
- `-trimpath`, `-s -w`
- ldflags for `main.version`, `main.commit`, and `main.date`
- tarball names: `bivouac_{{ .Version }}_{{ .Os }}_{{ .Arch }}`
- archive files: `LICENSE`, `README.md`
- checksum file: `sha256sums.txt`
- changelog sorted oldest-to-newest with GoReleaser's default commit list
- GitHub release owner/name: `jskswamy/bivouac`
- release header read from `RELEASE_NOTES.md`

The release header should use the same simple mechanism as the proven pattern:

```yaml
release:
  github:
    owner: jskswamy
    name: bivouac
  header: '{{ readFile "RELEASE_NOTES.md" }}'
```

GoReleaser appends its generated commit changelog below that header. The
hand-written notes explain what changed for users; the generated list remains
as the audit trail.

Do not add Homebrew publishing to this config yet.

### Release notes

Add `RELEASE_NOTES.md` at the repository root. It is hand-edited and starts in
this reset state:

```markdown
## Unreleased

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

Workflow rule: when a spec describes a user-facing change, the same commit that
adds the spec also appends a matching entry to `RELEASE_NOTES.md` under the
appropriate heading. User-facing includes removed flags, renamed commands, new
commands, changed defaults, prompts, output format changes, user-visible error
messages, environment-variable contracts, file-format changes, installation
changes, and release/distribution changes. Internal refactors, test scaffolding,
and documentation-only edits do not require notes unless they reflect a
behavior change. When in doubt, add the note.

Tag flow:

1. Confirm `RELEASE_NOTES.md` reflects everything that shipped since the last
   tag by cross-checking `git log <last-tag>..HEAD`.
2. Replace `## Unreleased` with `## vX.Y.Z` for the version being tagged.
3. Drop empty section headings.
4. Commit the change with a classic message such as `Stamp vX.Y.Z release notes`.
5. Tag and push: `git tag -a vX.Y.Z -m "Release vX.Y.Z"` and push the tag.
6. The release workflow runs GoReleaser, which publishes the GitHub release with
   `RELEASE_NOTES.md` as the header and the generated commit changelog below it.
7. After the release ships, reset `RELEASE_NOTES.md` to the template for the
   next release and commit that reset on `main`.

This deliberately avoids a persistent `CHANGELOG.md` and avoids generated
release prose from commit-message conventions. The release notes are written by
humans while context is fresh; GoReleaser's generated changelog is only the
commit audit trail.

### GitHub release workflow

Add `.github/workflows/release.yml` on `push.tags: ["v*"]`.

The workflow should fit bivouac's existing CI:

1. Check out the tag with full history.
2. Install Nix.
3. Run the existing Nix verification path, or call a reusable workflow if the
   implementation extracts one from `ci.yml`.
4. Run GoReleaser only after verification succeeds.

The GoReleaser job needs `contents: write`. Other jobs should keep least
privilege permissions.

Because bivouac's existing security workflow currently uses Nix and may fail on
new stdlib CVEs independent of a release change, the release gate should run the
same checks CI is expected to keep green for pushes. If security scans are
included directly, their failure mode must be intentional and documented in the
plan.

### Installer

Add `install.sh`:

- `REPO="jskswamy/bivouac"`
- `BINARY="bivouac"`
- default `INSTALL_DIR="./bin"`
- support `darwin` and `linux`
- map `x86_64 -> amd64`, `arm64|aarch64 -> arm64`
- fetch latest release via GitHub API when `VERSION` is unset
- download `bivouac_${VERSION#v}_${OS}_${ARCH}.tar.gz`
- download and verify `sha256sums.txt`
- extract and copy `bivouac` into `INSTALL_DIR`
- print a PATH hint when needed

README should show:

```sh
curl -sSfL https://raw.githubusercontent.com/jskswamy/bivouac/main/install.sh | sh
curl -sSfL https://raw.githubusercontent.com/jskswamy/bivouac/main/install.sh | sudo INSTALL_DIR=/usr/local/bin sh
curl -sSfL https://raw.githubusercontent.com/jskswamy/bivouac/main/install.sh | VERSION=v0.1.0 sh
```

### Makefile

Update the Makefile to add release-aware builds while preserving the current
Nix-first development environment:

- `VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)`
- `COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)`
- `BUILD_DATE ?= $(shell git log -1 --format=%cI 2>/dev/null || echo unknown)`
- `LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(BUILD_DATE)`
- `build` uses those ldflags.
- `install` requires GoReleaser and runs `goreleaser build --single-target
  --clean --output out/bivouac`, then installs that binary.
- `install-dev` builds quickly with `go build -ldflags "$(LDFLAGS)"` and
  installs the result.

The existing `test`, `vet`, `lint`, and `generate` targets can keep using
`nix develop`.

### Nix package

Extend `flake.nix` with `packages.default` for each supported system using
`buildGoModule`:

- `pname = "bivouac"`
- `version = self.shortRev or self.dirtyShortRev or "dev"`
- `src = self`
- `vendorHash` pinned from `nix build`
- `doCheck = false` because tests remain covered by CI/pre-commit and several
  tests require tools already modeled in the dev shell rather than the package
  closure.
- ldflags set `main.version` and `main.commit` from the flake revision.
- `meta.mainProgram = "bivouac"`

This is not a Nixpkgs release. It just makes the repository flake produce the
binary.

## Testing and verification

The implementation should be test-driven where code behavior changes:

- Add a command test that `--version` prints `bivouac <version> (commit: <commit>, built: <date>)`
  when test build metadata is passed into the command tree.
- Add an installer shell syntax check with `sh -n install.sh`.
- Add a GoReleaser config validation step if `goreleaser` is available in the
  development environment; otherwise use `go run github.com/goreleaser/goreleaser/v2@latest check`
  only if the plan decides the network cost is acceptable.
- Run `go test ./...` with output redirected to a file, not piped.
- Run `nix flake check` after updating the package output and vendor hash.
- Run `pre-commit run --all-files` before final commit, noting any unrelated
  govulncheck failures explicitly instead of hiding them.

## Open decisions made by the builder

- Use the tarball and installer naming scheme `bivouac_{{ version }}_{{ os }}_{{ arch }}.tar.gz`.
- Keep Homebrew out of the first implementation because no tap target is
  specified. The release tarball shape should make a future formula
  straightforward.
- Use hand-written `RELEASE_NOTES.md` as the GitHub release header and
  GoReleaser's generated changelog as the audit trail beneath it.
- Do not add a persistent `CHANGELOG.md` until users need an in-repo release
  timeline separate from GitHub releases.
