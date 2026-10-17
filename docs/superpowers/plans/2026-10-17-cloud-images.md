# Cloud Images Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user build, select, list, tag and prune their own DigitalOcean
snapshot images through generated Packer bundles, and launch `bivouac up`
from them with a baked boot path.

**Architecture:** A new `internal/image` package owns markers, launch-image
resolution, retention, bundle generation, the Packer runner and build
orchestration. The provider gains an optional `ImageManager` interface (the
`KeyRegistry` pattern). Cloud-init splits into a reusable software bootstrap
and per-instance access init, so stock and baked launches share one script.
`cmd/image*.go` stays thin.

**Tech Stack:** Go, cobra, godo v1.205.0, Pkl (pkl-go), `text/template` +
`go:embed`, bash, Packer with the HashiCorp DigitalOcean plugin (shelled out).

**Spec:** `docs/superpowers/specs/2026-10-17-cloud-images-design.md`
(approved 2026-10-17, all seven proposed implementation choices included).
Beads: epic `cloudlab-tjw`; Task N below is `cloudlab-tjw.N`. Each bead's
description and acceptance criteria apply alongside its task here.

## Global Constraints

- Tests first in every task: write the failing test, run it, see it fail for
  the stated reason, implement, see it pass.
- Run Go tests redirected, never piped: `go test ./... > /tmp/out.log 2>&1; echo $?`.
- Work inside `nix develop` (needs `pkl`, `nix`). Packer is **not** added to
  the devshell; Bivouac never installs Packer.
- Every task ends with `pre-commit run --all-files` passing.
- Commit messages: imperative, capitalised subject of ≤50 chars, no type
  prefix, no period; body wrapped at 72 explaining why; **no AI co-author
  trailer**. No bead IDs in code or comments.
- Shell out to `packer`, `nix`, `pkl`; never link them.
- No paid-cloud calls in any task except Task 15, which needs the human's
  explicit go-ahead.
- Supported build source is fixed: `x86_64` → `ubuntu-24-04-x64`. `arm64`
  fails explicitly.
- Retention: keep `1 + keepPrevious`; `keepPrevious` default `1`; negative is
  invalid; `0` = current only.
- Ready image status is exactly `"available"`.
- Credentials never go into the bundle, argv or logs. The DigitalOcean token
  goes only into the Packer child process's environment as `DIGITALOCEAN_TOKEN`.

## Shared names (defined once, used everywhere)

`internal/image/markers.go` (Task 5):

```go
const (
    MarkerManaged = "bivouac-image"
    FormatVersion = 1                     // tag "bivouac-format-1"
    StockSourceX86 = "ubuntu-24-04-x64"
)
func ArchTag(arch string) string    // "bivouac-arch-x86_64"
func RegionTag(region string) string // "bivouac-region-blr1"
func ShellTag(shell string) string   // "bivouac-shell-fish"
func FormatTag() string              // "bivouac-format-1"
// SnapshotTags is the complete marker set plus the user tag, in this order.
func SnapshotTags(userTag, arch, region, shell string) []string
type Markers struct{ Arch, Region, Shell string; Format int }
// ParseMarkers succeeds only when every marker is present exactly once.
func ParseMarkers(tags []string) (Markers, bool)
type Scope struct{ Tag, Arch, Region string }
func ValidUserTag(tag string) error // DO tag charset [A-Za-z0-9:_-], ≤255, not "bivouac-" prefixed
```

`internal/provider/provider.go` (Task 4):

```go
type Image struct {
    ID          string    // numeric, as a string like VM.ID
    Name        string
    Created     time.Time
    Regions     []string
    MinDiskGB   int
    Status      string    // "available" is the only ready value
    Tags        []string
    Type        string    // "snapshot" for Bivouac builds
}
var ErrImageNotFound = errors.New("image not found")
type ImageManager interface {
    ListImages(ctx context.Context, tag string) ([]Image, error) // "" = all private images; all pages
    GetImage(ctx context.Context, id string) (Image, error)      // ErrImageNotFound when gone
    TagImage(ctx context.Context, id, tag string) error
    DeleteImage(ctx context.Context, id string) error            // ErrImageNotFound when gone
    SizeDiskGB(ctx context.Context, sizeSlug string) (int, error)
}
```

`internal/image` error type (Task 10), used by build, tag and prune for
partial-success reporting and `--json` errors:

```go
type OpError struct {
    Phase        string `json:"phase"`         // "generate","packer-init","packer-validate","packer-build","manifest","verify","writeback","prune"
    ImageCreated bool   `json:"image_created"`
    ImageID      string `json:"image_id,omitempty"`
    Cleanup      string `json:"cleanup,omitempty"` // human summary, e.g. "deleted 1, pending 2"
    Err          error  `json:"-"`
}
func (e *OpError) Error() string
func (e *OpError) Unwrap() error
```

## Review Focus

1. **The shell install failed during the build but the image is tagged with
   that shell.** The bootstrap falls back to bash rather than failing, so an
   image could carry `bivouac-shell-fish` while having no fish. The build must
   fail instead. Owned by Task 9 (`wait-bootstrap.sh` checks the shell).
2. **A user tag that collides with a foreign image or Droplet tag.** Resolve,
   tag and prune must ignore anything without the full marker set, even when
   the user tag matches. Owned by Tasks 5 and 12; each has a test for it.
3. **`image = "tag:x"` in base.pkl when a project sets `image` explicitly to
   the stock default.** The explicit project value must win, even though it
   equals the schema default. Owned by Task 1.
4. **Writeback to a Home Manager-managed base.pkl, which is a symlink into
   `/nix/store`.** It must never edit the file; print the snippet and exit 0.
   Owned by Task 11.
5. **A launch region the snapshot was not built in, or a launch size whose
   disk is smaller than `MinDiskGB`.** Fail before `Create` with both values
   named; never copy the image or fall back to stock. Owned by Task 5, with the
   no-Create assertion in Task 6.

---

### Task 1: Presence-aware mergeConfig

**Files:**
- Modify: `internal/config/config.go` (`Resolve`, `loadResolved`, `mergeConfig`)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `loadResolved(ctx, path) (Config, map[string]struct{}, error)`, where
  the set comes from `declaredFields(raw)` on the bytes it already reads.
  Also `mergeConfig(base, project Config, projectDeclared map[string]struct{}) Config`.

- [ ] **Step 1: Write failing tests.** `TestResolve_InheritsUndeclaredScalarsFromBase`
  is a table over `image`, `arch`, `tailscale`, `beads`, using temp
  `XDG_CONFIG_HOME` base files the way existing `Resolve` tests do:
  - base `image = "tag:my-dev"`, project omits it → `"tag:my-dev"`
  - base `image = "tag:my-dev"`, project `image = "ubuntu-24-04-x64"` → `"ubuntu-24-04-x64"`
  - base `tailscale = true`, project omits it → `true`; project `tailscale = false` → `false`
  - base `beads` non-default, project omits it → the base value
  - base `arch = "arm64"`, project omits it → `"arm64"`
  - neither declares → the schema defaults (`"x86_64"`, `"ubuntu-24-04-x64"`, `false`, the default beads mode)
- [ ] **Step 2:** `go test ./internal/config/ > /tmp/out.log 2>&1` → the
  inheritance rows fail (the project default wins).
- [ ] **Step 3:** Thread the declared set from `loadResolved` into
  `mergeConfig`. Each of the four fields takes the project value only when it
  is in the set. Update the doc comment to say why: these four have schema
  defaults, so "unset" can't be seen in the value.
- [ ] **Step 4:** Run the package tests, then the full suite → exit 0.
- [ ] **Step 5:** Commit `Let base config scalars survive omitting projects`.

### Task 2: Single-file config selection

**Files:**
- Modify: `internal/config/config.go`, `internal/config/basepath.go`
- Test: `internal/config/select_test.go` (new)

**Interfaces:**
- Consumes: Task 1's `loadResolved`, `mergeConfig`.
- Produces:
  ```go
  type SelectionKind string // "explicit", "project", "base"
  type Selected struct { Path string; Kind SelectionKind; Config Resolved }
  // LoadSelected needs the pkl CLI but not Packer, a cloud token, Nix or SSH keys.
  func LoadSelected(ctx context.Context, explicit, projectDir string) (Selected, error)
  ```
  `projectDir` may be `""` (no repo). `Resolved.SSHKeyFiles` is left empty.

- [ ] **Step 1: Write failing tests**, one each:
  - `TestLoadSelected_ExplicitWins`
  - `TestLoadSelected_ExplicitMissingNoFallback`: error contains the path; base is present but unused.
  - `TestLoadSelected_ExplicitInvalidNoFallback`
  - `TestLoadSelected_InvalidProjectIsErrorNotBase`
  - `TestLoadSelected_BaseOnlyLoadsOnce`: base with `packages = ["a"]` gives `Packages` of length 1.
  - `TestLoadSelected_ExplicitPathEqualToBaseLoadsOnce`
  - `TestLoadSelected_NoneFound`: the error names all three locations.
  - `TestLoadSelected_IgnoresMissingSSHKeyFiles`: `sshKeys = ["~/.ssh/nope.pub"]` still loads.
  - `TestLoadSelected_RequiresRegionSizeTemplate`
- [ ] **Step 2:** Run → compile failure.
- [ ] **Step 3:** Implement. Order: `explicit` if non-empty; else
  `projectDir/bivouac.pkl` if it exists; else `DefaultBasePath()` if it
  exists; else error. Compare paths with `filepath.Abs` + `EvalSymlinks` to
  decide whether the chosen file *is* the base. If it is, load it once and
  don't merge. Otherwise use Resolve's merge path without
  `configSSHKeyFiles`. Factor the shared part out of `Resolve` instead of
  copying it. Always call `validate` once.
- [ ] **Step 4:** Tests and full suite pass.
- [ ] **Step 5:** Commit `Add single-file config selection for images`.

### Task 3: Split cloud-init into bootstrap and access init

**Files:**
- Modify: `internal/provisioning/cloud-init.sh` → keep as the access part; create `internal/provisioning/bootstrap.sh`
- Modify: `internal/provisioning/cloud_init.go`, `internal/lifecycle/up_plan.go` (call site)
- Test: `internal/provisioning/cloud_init_test.go`

**Interfaces:**
- Produces:
  ```go
  type BootMode int
  const ( BootStock BootMode = iota; BootBaked )
  func RenderCloudInit(username, shell string, mode BootMode) (string, error)
  // RenderBootstrap is the Nix + login-shell install alone, with a shebang and set -euo pipefail.
  func RenderBootstrap(shell string) (string, error)
  ```
- Two embedded templates are combined with `{{template}}`. In stock mode the
  output is **byte-identical** to today's.

- [ ] **Step 1: Write failing tests.**
  - `TestRenderCloudInit_StockUnchanged`: save today's output for
    (`alice`,`fish`), (`alice`,`zsh`), (`alice`,`bash`) as golden files in
    `testdata/` *before* touching the script, then assert equality.
  - `TestRenderCloudInit_BakedSkipsInstall`: there is no
    `install.determinate.systems`, no `apt-get install`, no `add-apt-repository`.
    It still has `useradd`, the `authorized_keys` copy, sudoers, `enable-linger`,
    and `PermitRootLogin no` guarded by `[ -s ...authorized_keys ]`, in that order.
    `LOGIN_SHELL` is set to `/usr/bin/<shell>` only when `[ -x ]` and the
    `/etc/shells` check pass; otherwise it stays bash.
  - `TestRenderBootstrap`: it contains the Nix installer and the shell
    install, but no `useradd`, `authorized_keys` or `sshd_config`.
  - Invalid shell or user fails in both modes.
- [ ] **Step 2:** Run → fail.
- [ ] **Step 3:** Move the Nix install and login-shell block into
  `bootstrap.sh` as a `define`d template. Have `cloud-init.sh` include it
  when the mode is stock. In baked mode, emit only the shell-path detection.
  Update the `PrepareUp` call to pass `BootStock`; Task 6 changes it later.
- [ ] **Step 4:** Tests and full suite pass.
- [ ] **Step 5:** Commit `Split cloud-init bootstrap from access setup`.

### Task 4: DigitalOcean ImageManager and numeric image IDs

**Files:**
- Modify: `internal/provider/provider.go` (types in Shared names)
- Create: `internal/provider/digitalocean/images.go`, `images_test.go`
- Modify: `internal/provider/digitalocean/digitalocean.go` (`Create`)

**Interfaces:** Produces `provider.ImageManager`, `provider.Image`,
`provider.ErrImageNotFound`, and `*digitalocean.Provider` implementing them.

- [ ] **Step 1: Write failing tests** using `newTestProvider(t)` and its mux:
  - `TestProvider_IsAnImageManager`
  - `TestListImages_AllPages`: two pages via `links.pages.next`, then the
    count. Calls `ListByTag` when the tag is set, `ListUser` when it's `""`.
  - `TestListImages_MapsFields`: `regions`, `min_disk_size`, `status`, `tags`,
    `type`, and `created_at` parsed into `Created`.
  - `TestGetImage_NotFound` → `errors.Is(err, provider.ErrImageNotFound)`
  - `TestTagImage_UsesTagResources`: a `POST /v2/tags` create (it tolerates the
    tag already existing), then `POST /v2/tags/{tag}/resources` with
    `resource_type:"image"` and the ID.
  - `TestDeleteImage_NotFoundIsErrImageNotFound`
  - `TestDeleteImage_ForbiddenIsErrForbidden` → `provider.IsForbidden`
  - `TestSizeDiskGB`: it looks the slug up in `/v2/sizes` (all pages) and
    returns `disk`. An unknown slug is an error naming the slug.
  - `TestCreate_NumericImageSendsID`: the request body has `"image":123456`.
    `TestCreate_SlugImageSendsSlug`: it has `"image":"ubuntu-24-04-x64"`.
- [ ] **Step 2:** Run → fail.
- [ ] **Step 3:** Implement using `paginate`. In `Create`, a positive integer
  from `strconv.Atoi` becomes `godo.DropletCreateImage{ID: n}`; anything else
  becomes `Slug`. Map 404 to `ErrImageNotFound` and 403 to `ErrForbidden`,
  following `keyError`.
- [ ] **Step 4:** Tests and full suite pass.
- [ ] **Step 5:** Commit `Add DigitalOcean image operations`.

### Task 5: Image markers and launch resolution

**Files:**
- Create: `internal/image/markers.go`, `markers_test.go`, `resolve.go`, `resolve_test.go`, `fake_test.go` (an in-memory `provider.ImageManager` that records calls)

**Interfaces:**
- Consumes: `provider.ImageManager`, `provider.Image`.
- Produces: everything in Shared names → markers, plus
  ```go
  type Request struct{ Ref, Arch, Region, Size, Shell string }
  type Resolution struct {
      Requested string // the config string as written
      Image     string // what InstanceSpec.Image gets: slug or numeric ID
      Baked     bool
      Shell     string // the baked shell; "" when not baked
  }
  // m may be nil; it is only consulted for tag: and numeric refs.
  func Resolve(ctx context.Context, m provider.ImageManager, req Request) (Resolution, error)
  func Eligible(img provider.Image, s Scope) bool // ready, snapshot, full markers, arch+region match, has s.Tag when non-empty
  func SortNewest(imgs []provider.Image) // Created desc, then numeric ID desc
  ```

- [ ] **Step 1: Write failing tests.**
  - Markers: `SnapshotTags("my-dev","x86_64","blr1","fish")` returns
    `["bivouac-image","bivouac-format-1","bivouac-arch-x86_64","bivouac-region-blr1","bivouac-shell-fish","my-dev"]`.
    `ParseMarkers` rejects a missing or duplicated marker.
    `ValidUserTag("bivouac-x")` is an error; `"my dev"` is an error.
  - Resolve table, with the fake:
    - stock slug → `{Image: slug, Baked: false}` and **zero** fake calls, even when `m` is nil
    - `tag:` → the newest by `Created`, with ties broken by the higher ID
    - arch mismatch is skipped
    - launch region not in `Regions` is skipped
    - `Status:"pending"` is skipped
    - a missing marker is skipped
    - a foreign image carrying the user tag is skipped
    - none eligible → the error contains the tag, arch and region
    - `MinDiskGB 50` with size disk 25 → the error names both numbers and the size slug
    - a numeric ID that is valid and eligible resolves
    - a numeric ID that is foreign → error
    - a numeric ID that is gone → error wrapping `ErrImageNotFound`
    - `tag:` with `m == nil` → "provider does not support custom images"
    - `"tag:"` with an empty name → error
  - The baked result's `Shell` comes from `ParseMarkers`.
- [ ] **Step 2:** Run → fail.
- [ ] **Step 3:** Implement. Ref parsing: a `tag:` prefix; else a positive
  integer → ID; else a slug.
- [ ] **Step 4:** Tests and full suite pass.
- [ ] **Step 5:** Commit `Resolve launch images from tags and IDs`.

### Task 6: Launch from a resolved image

**Files:**
- Modify: `internal/state/state.go`, `internal/lifecycle/up_plan.go`, `internal/lifecycle/lifecycle.go` (record construction), `cmd/up.go`, `cmd/status_print.go`
- Test: `internal/state/state_test.go`, `internal/lifecycle/up_plan_test.go`, `cmd/status_print_test.go`, the existing `up` command tests

**Interfaces:**
- Consumes: `image.Resolve`, `image.Resolution`, `provisioning.BootMode`.
- Produces: `PrepareUp(ctx, name, configPath, repoRoot string, cfg config.Resolved, keys keyplan.Plan, img image.Resolution) (UpPlan, error)`.
  `UpPlan` gains `Image image.Resolution`. `state.Record` gains
  `ImageRef string \`json:"image_ref,omitempty"\`` and `ImageID string \`json:"image_id,omitempty"\``.
  `ImageID` is set only when the launch was baked.

- [ ] **Step 1: Write failing tests.**
  - State: a round trip keeps both fields. A golden old JSON record with
    neither field loads them as empty strings.
  - `PrepareUp` with a stock resolution renders `BootStock` (contains
    `install.determinate.systems`) and `Spec.Image == "ubuntu-24-04-x64"`.
  - A baked resolution `{Image:"123", Baked:true, Shell:"fish"}` with
    `cfg.Shell "fish"` renders `BootBaked` and `Spec.Image == "123"`.
  - Baked shell `"zsh"` with config `"fish"` → the error contains both
    `"zsh"` and `"fish"`, and no plan is returned.
  - `up` command test with a fake provider whose image list has no eligible
    image → error, and the fake's `Create` call count is 0.
  - `status` prints an `Image` line with `ref (id)` when the record has the
    fields, and omits it for old records. It is in the JSON as `image_ref`
    and `image_id`.
- [ ] **Step 2:** Run → fail.
- [ ] **Step 3:** In `cmd/up.go`, right after the provider `p` resolves and
  before `PrepareUp`: `m, _ := p.(provider.ImageManager)`, then
  `image.Resolve(ctx, m, image.Request{Ref: cfg.Image, Arch: cfg.Arch, Region: cfg.Region, Size: cfg.Size, Shell: cfg.Shell})`.
  `PrepareUp` does the shell check and chooses the boot mode. `Up` copies
  `plan.Image` into the record.
- [ ] **Step 4:** Tests and full suite pass.
- [ ] **Step 5:** Commit `Launch instances from custom images`.

### Task 7: Bundle generator

**Files:**
- Create: `internal/image/bundle.go`, `bundle_test.go`, `internal/image/bundle/` (embedded: `image.pkr.hcl.tmpl`, `README.md.tmpl`)

**Interfaces:**
- Consumes: `config.Selected`, `provisioning.Render/NeedsRender/ResolveTemplateRef/RenderBootstrap`, `SnapshotTags`, `StockSourceX86`.
- Produces:
  ```go
  const PackerPluginVersion = "<pinned>" // see Step 3
  type BundleOptions struct {
      OutputDir      string
      Selected       config.Selected
      Tag            string
      Username       string // default identity.RemoteUser(); validated with identity.ValidRemoteUser
      BivouacVersion string
  }
  type Metadata struct {
      FormatVersion int; BivouacVersion, SourceImage, Arch, Region, Size, Shell, Username, Tag, TemplateRef, FlakeRef, ConfigFingerprint string
  }
  type Bundle struct{ Dir string; Metadata Metadata }
  func Generate(opts BundleOptions) (Bundle, error)
  ```
  The bundle layout (relative paths are fixed and Task 9's scripts rely on them):
  ```
  image.pkr.hcl   README.md   metadata.json
  scripts/bootstrap.sh  scripts/wait-bootstrap.sh  scripts/build-closure.sh  scripts/sanitize.sh
  nix/flake.nix          # only when NeedsRender
  ```
  `FlakeRef` is `path:./nix#default` when rendered, otherwise the template ref.
  `ConfigFingerprint` is the sha256 of `FlakeRef` plus the rendered flake bytes.

- [ ] **Step 1: Write failing tests**, all writing into `t.TempDir()`:
  - A non-empty output directory → error, and nothing is written.
  - The file set matches the layout. Scripts are mode 0755; everything else is 0644.
  - The HCL contains `required_plugins`, with `source = "github.com/hashicorp/digitalocean"`
    and `version = "= <PackerPluginVersion>"`. It also contains
    `image = "ubuntu-24-04-x64"` even when the config's `image` is
    `"tag:my-dev"`, `snapshot_tags` equal to `SnapshotTags(...)`,
    `ssh_username = "root"`, `user_data_file = "scripts/bootstrap.sh"`, and
    the `manifest` post-processor writing `packer-manifest.json`.
  - The token is read only through `env("DIGITALOCEAN_TOKEN")`.
  - `arch = "arm64"` → an error naming arm64 and the supported arch.
  - A flake URL or template that is a local path (`path:`, `/`, `./`, `~`,
    `git+file:`) → an error naming the field and value.
  - Inspection test: walk every generated file and assert there is no
    `DIGITALOCEAN_TOKEN=`, no `PRIVATE KEY`, no `bivouac ` command invocation,
    and no absolute path from the test's temp dirs, `$HOME`, `/Users/` or `/home/`.
  - `README.md` contains `packer init .`, `packer validate .`, `packer build .`
    and a trust-boundary paragraph (it asserts the phrase `arbitrary Nix`).
  - If `packer` is on PATH, run `packer fmt -check` on the output;
    otherwise `t.Skip("packer not installed")`.
- [ ] **Step 2:** Run → fail.
- [ ] **Step 3:** Implement. Pin the plugin: run
  `git ls-remote --tags https://github.com/hashicorp/packer-plugin-digitalocean`
  and choose the newest `vX.Y.Z` release that is at least two weeks old.
  Record the version and its date in the constant's comment. The HCL
  variables are `region`, `size` and `username`, with defaults from the
  config. Packer runs `wait-bootstrap.sh`, then uploads `nix/` (if present)
  and `metadata.json` to `/var/lib/bivouac-image/`, then runs
  `build-closure.sh {{username}}`, then `sanitize.sh`. This task only
  creates the four script files as placeholders that exit non-zero; Task 9
  fills them in.
- [ ] **Step 4:** Tests and full suite pass.
- [ ] **Step 5:** Commit `Generate standalone Packer image bundles`.

### Task 8: `bivouac image generate`

**Files:**
- Create: `cmd/image.go` (group), `cmd/image_generate.go`, `cmd/image_generate_test.go`
- Modify: `cmd/root.go` (`root.AddCommand(newImageCmd())`)

**Interfaces:**
- Consumes: `config.LoadSelected`, `image.Generate`.
- Produces: `newImageCmd()`, with subcommands added by Tasks 11 and 13. The
  `--json` result is `{"output_dir","config_path","config_kind","tag","metadata"}`.

- [ ] **Step 1: Write failing tests.**
  - It is registered under `image`, with metadata `SupportsJSON: true`,
    `Network: false`, and the side effect `"writes bundle directory"`.
  - `TestBuildManifest_*` still passes, and the manifest includes `image generate`.
  - With a temp base.pkl and `--output`, the output dir is created and its
    path is printed.
  - `--json` puts a valid JSON object on stdout and progress on stderr.
  - A non-empty output dir → non-zero.
  - No `--tag` → a usage error.
  - It runs with `PATH` limited to the `pkl` directory (no packer or nix) and
    `DIGITALOCEAN_TOKEN` unset.
  - It works with a cwd outside any git repo when base.pkl exists.
  - `--repo <dir>` picks `<dir>/bivouac.pkl`.
- [ ] **Step 2:** Run → fail.
- [ ] **Step 3:** Implement. The default `--output` is `./bivouac-image`.
  The project dir comes from `identity.RepoRoot(ctx, cwd, repoFlag)`; outside
  a repo, treat the error as "no project". `Username` comes from
  `identity.RemoteUser()`.
- [ ] **Step 4:** Tests and full suite pass.
- [ ] **Step 5:** Commit `Add bivouac image generate`.

### Task 9: Builder scripts

**Files:**
- Replace the placeholders: `internal/image/bundle/scripts/{bootstrap.sh is generated, wait-bootstrap.sh, build-closure.sh, sanitize.sh}`
- Test: `internal/image/scripts_test.go`

**Interfaces:**
- Consumes: Task 7's layout and `/var/lib/bivouac-image/`.
- Produces, on the image:
  - `/var/lib/bivouac-image/metadata.json`: the bundle's metadata plus a
    `revisions` field from `nix flake metadata --json`
  - the GC root `/nix/var/nix/gcroots/bivouac-image/closure` → the activation
    package's store path

- [ ] **Step 1: Write failing tests**, using Go `exec` against bash:
  - Every script passes `bash -n`, and `shellcheck` too if it's on PATH
    (skip otherwise).
  - `sanitize.sh` honours `SANITIZE_ROOT=<tmp>`. Build a fake tree containing
    `home/<user>`, `root/.ssh/authorized_keys`, `etc/ssh/ssh_host_ed25519_key{,.pub}`,
    `etc/machine-id`, `var/lib/cloud/instances/x`, `root/.bash_history`,
    `var/log/syslog`, `tmp/x`, `nix/store/abc`,
    `nix/var/nix/gcroots/bivouac-image/closure` and
    `var/lib/bivouac-image/metadata.json`. After the run: the home is gone;
    `authorized_keys` exists and is empty; the host keys, cloud instance
    state, history and tmp contents are gone; `machine-id` exists and is
    empty; the store, gcroot and metadata remain. With `SANITIZE_ROOT` set,
    account removal is printed and not executed.
  - `build-closure.sh` rejects `"Bad User"` and `""` with a non-zero exit
    before running any command.
  - `wait-bootstrap.sh` exits non-zero when it runs with a stub `cloud-init`
    on PATH that reports `status: error`, or when the expected shell binary
    is missing (Review Focus 1).
  - A content test: no script contains `tailscale up`, `moshi`, `gh auth`,
    `dolt login` or `DIGITALOCEAN_TOKEN`.
- [ ] **Step 2:** Run → fail.
- [ ] **Step 3:** Implement with `set -euo pipefail` and a comment on each
  non-obvious choice.
  - `wait-bootstrap.sh`: `cloud-init status --wait`, then require
    `/nix/var/nix/profiles/default/bin/nix`, then require the metadata shell
    at `/usr/bin/<shell>` (or bash) in `/etc/shells`.
  - `build-closure.sh <user>`: `useradd --create-home`; as that user, from a
    login shell, run `nix build --impure --no-write-lock-file --refresh --no-link --print-out-paths "<FlakeRef-url>#homeConfigurations.<name>.activationPackage"`.
    These are the same flags `reconcile.go` passes to switch, minus `-b`.
    The flake ref is split on `#`. A `path:./nix` URL is rewritten to
    `path:/var/lib/bivouac-image/nix`, because the bundle stays
    bundle-relative and only the builder knows where it was uploaded.
    Then `nix-store --add-root /nix/var/nix/gcroots/bivouac-image/closure --realise <path>`
    as root, and append the revisions to the metadata. It never runs `activate`.
  - `sanitize.sh`: `userdel -r <user>`, `cloud-init clean --logs --machine-id --seed`,
    the removals listed in the test, `journalctl --rotate --vacuum-time=1s`, and
    finally `sync`.
- [ ] **Step 4:** Tests and full suite pass. Note in the bead close-out what
  Task 15 still has to verify.
- [ ] **Step 5:** Commit `Add image builder scripts`.

### Task 10: Packer runner

**Files:**
- Modify: `internal/tool/tool.go` (add `"packer"` to `known` with hint
  `install it: https://developer.hashicorp.com/packer/install`; add
  `RunEnv(ctx, dir string, env []string, stdout, stderr io.Writer, bin string, args ...string) error`)
- Create: `internal/image/packer.go`, `packer_test.go`, `testdata/fakepacker.sh`

**Interfaces:**
- Produces:
  ```go
  type Artifact struct{ ID, Region string }
  // RunPacker runs init, validate, build in dir; the token reaches only the child's env.
  func RunPacker(ctx context.Context, dir, token string, progress io.Writer) (Artifact, error) // errors are *OpError
  func ParseManifest(raw []byte) (Artifact, error) // last build's artifact_id "blr1:123456" → {ID:"123456", Region:"blr1"}
  ```

- [ ] **Step 1: Write failing tests.** Put the fake packer first on `PATH`
  (copy `testdata/fakepacker.sh` into a temp dir named `packer`). It logs its
  argv and the presence of `DIGITALOCEAN_TOKEN` to a file, and its behaviour
  is controlled by environment variables.
  - Missing packer → the error mentions `developer.hashicorp.com/packer/install`.
  - The calls are `init .`, `validate .`, `build .`, in that order.
  - A validate failure → build is never logged; `OpError.Phase == "packer-validate"`.
  - The token is in the child env and not in argv, and the progress output
    doesn't contain the token value.
  - Cancelling the context mid-build → `errors.Is(err, context.Canceled)`,
    the phase is `packer-build`, and `Cleanup` mentions possible leftover builder droplets.
  - `ParseManifest` with two builds takes the last; `"nyc3:123456"` →
    `"123456"`; malformed JSON is an error; a missing manifest after exit 0 is
    an error.
  - A build that exits non-zero after the manifest was written →
    `OpError{ImageCreated:true, ImageID:"123456"}`.
- [ ] **Step 2:** Run → fail.
- [ ] **Step 3:** Implement on top of `tool.Require("packer")` and `tool.RunEnv`.
  The env is `os.Environ()` with any `DIGITALOCEAN_TOKEN` removed and the
  supplied one added.
- [ ] **Step 4:** Tests and full suite pass. If a real `packer` exists, also
  run `packer init` and `packer validate -syntax-only` on a generated bundle.
  Otherwise **report that this was not run**.
- [ ] **Step 5:** Commit `Run Packer for image builds`.

### Task 12: Retention core

(Task 12 comes before Task 11 because the build depends on it.)

**Files:**
- Create: `internal/image/retention.go`, `retention_test.go`

**Interfaces:**
- Consumes: `Eligible`, `SortNewest`, `Scope`, `ParseMarkers`, `provider.ImageManager`.
- Produces:
  ```go
  type PrunePlan struct{ Scope Scope; Keep, Delete []provider.Image }
  func PlanPrune(imgs []provider.Image, s Scope, keepPrevious int, protectedID string) (PrunePlan, error)
  // Groups eligible images carrying tag by their region/arch markers, narrowed by non-empty filters.
  func GroupScopes(imgs []provider.Image, tag, region, arch string) []Scope
  type PruneResult struct{ Deleted, Pending []string; Err error }
  func ExecutePrune(ctx context.Context, m provider.ImageManager, p PrunePlan) PruneResult
  ```

- [ ] **Step 1: Write failing tests.**
  - `keepPrevious` of 0, 1 or 3 with 5 eligible images → 4, 3 or 1 deletions.
  - Fewer eligible images than `1+keep` → 0 deletions.
  - Equal `Created` → the lower ID is deleted first.
  - Scope filtering by tag, arch and region.
  - Foreign images with the user tag are never in `Delete` (Review Focus 2).
  - `pending` images are never in `Delete`.
  - `protectedID` is never in `Delete`, even when it is the oldest.
  - A negative keep → error.
  - `GroupScopes` with no filters over blr1 and nyc3 images → two scopes.
  - Execute: before each delete it re-gets the image. If the user tag has
    gone, the image is no longer eligible, or it no longer exists, the run
    stops: a missing image counts as `Deleted` and the run continues; the
    other cases stop with the rest marked `Pending`.
  - A 403 on delete → `Err` set, with `Deleted` and `Pending` correct.
  - Planning makes zero delete calls (the fake counts them).
- [ ] **Step 2:** Run → fail.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Tests and full suite pass.
- [ ] **Step 5:** Commit `Plan and execute image retention`.

### Task 11: `bivouac image build` and config writeback

**Files:**
- Create: `internal/config/edit.go`, `edit_test.go`; `internal/image/build.go`, `build_test.go`; `cmd/image_build.go`, `cmd/image_build_test.go`

**Interfaces:**
- Consumes: Tasks 2, 5, 7, 10 and 12.
- Produces:
  ```go
  // config
  type EditOutcome struct{ Written bool; Reason string } // Reason set when !Written: "symlink", "nix store", "read-only"
  func SetImage(path, ref string) (EditOutcome, error) // replaces a depth-0 `image = ...` line or appends one; atomic temp+rename, mode kept
  // image
  type BuildOptions struct{ Bundle BundleOptions; Token string; KeepPrevious int; Prune bool; Progress io.Writer }
  type BuildDeps struct {
      Images provider.ImageManager
      Packer func(ctx context.Context, dir, token string, progress io.Writer) (Artifact, error)
      SetImage func(path, ref string) (config.EditOutcome, error)
  }
  type BuildResult struct {
      ImageID, Reference, ConfigPath, BundleDir string
      Writeback config.EditOutcome; Snippet string
      Prune *PruneResult
  }
  func Build(ctx context.Context, d BuildDeps, o BuildOptions) (BuildResult, error) // errors are *OpError
  func ManagedBundleDir() (string, error) // xdg.Path(xdg.Cache, "bivouac", "images", <UTC timestamp>-<8 hex>)
  ```

- [ ] **Step 1: Write failing tests.**
  - `SetImage`:
    - It replaces `image = "ubuntu-24-04-x64"` and leaves the rest of the
      file byte-identical, comments included.
    - It appends when there is no `image` line.
    - It ignores `image =` inside a nested `{}` or a comment.
    - A symlink → `{Written:false, Reason:"symlink"}` and the target is unchanged.
    - A path under `/nix/store` → `"nix store"` (test it with an
      injectable prefix variable).
    - A 0444 file → `"read-only"`.
    - A failing rename in a read-only dir → an error.
  - `Build` with fakes:
    - The happy path runs generate → packer → `GetImage` verifies →
      `SetImage` → prune, in that order. The result has
      `Reference == "tag:my-dev"`.
    - `Prune:false` → no list calls.
    - A Packer error → no prune calls and the error is propagated.
    - The artifact is not `Eligible` (missing a marker or not available) →
      `OpError{Phase:"verify", ImageCreated:true}` and no prune.
    - A writeback skip → the result's `Snippet == "image = \"tag:my-dev\""`,
      with no error.
    - A writeback error → `OpError{Phase:"writeback", ImageCreated:true}`, and
      the prune still runs.
    - A prune failure → `OpError{Phase:"prune", ImageCreated:true, ImageID, Cleanup}`,
      and the new ID is never deleted.
    - `ManagedBundleDir` is unique across two calls.
  - Command:
    - `--no-prune` together with `--keep-previous` → error.
    - `--keep-previous -1` → error.
    - The retention policy line is printed to stderr before Packer runs.
    - `--json` on success puts the result on stdout.
    - On a prune failure, `--json` prints the `OpError` fields on stdout and
      the exit is non-zero.
    - The provider is injected through a `newImageBuildCmdWithProvider(resolve)` seam, like `up`.
- [ ] **Step 2:** Run → fail.
- [ ] **Step 3:** Implement. The token comes from the existing `resolveToken`
  and goes to `BuildOptions.Token`. Nothing else receives it.
- [ ] **Step 4:** Tests and full suite pass.
- [ ] **Step 5:** Commit `Add bivouac image build`.

### Task 13: `image list`, `tag`, `prune`

**Files:**
- Create: `cmd/image_list.go`, `cmd/image_tag.go`, `cmd/image_prune.go` and their tests

**Interfaces:** These consume `ImageManager`, `Eligible`, `ParseMarkers`,
`ValidUserTag`, `PlanPrune`, `GroupScopes` and `ExecutePrune`. None of them
calls `LoadSelected`.

- [ ] **Step 1: Write failing tests**, with a fake provider passed through
  `with...Provider` seams:
  - `list`: the table columns are `ID TAGS CREATED REGIONS STATUS`, where
    status is `managed`, `foreign` or `not ready`. `--tag`, `--region` and
    `--arch` filter the rows. `--json` gives an array of objects with
    `id,name,tags,created,regions,status,managed`.
  - `tag`: a foreign or non-ready image → error, and no `TagImage` call. A
    compatible image → one `TagImage` call. An invalid tag → error.
  - `prune`:
    - `--dry-run` previews and makes no deletes.
    - Not a terminal and no `--yes` → an error naming `--yes`, without
      prompting (`isInteractive` is injectable).
    - `--yes` deletes each group.
    - Without filters, it groups by region and arch.
    - `--dry-run` with `--yes` → error.
    - A partial failure exits non-zero, with `--json` printing the
      completed and pending lists.
  - Manifest tests include all three commands. `prune` has `Destructive: true`
    and `MayPrompt: true`.
- [ ] **Step 2:** Run → fail.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Tests and full suite pass.
- [ ] **Step 5:** Commit `Add bivouac image list, tag and prune`.

### Task 14: Docs and release notes

**Files:**
- Modify: `docs/config.md` (`image` field), `RELEASE_NOTES.md`, and wherever
  docs are indexed (README or docs index)
- Create: `docs/images.md`

- [ ] **Step 1:** Generate the `--help` text for each `image` command and
  write `docs/images.md` from it. Cover: generate vs build; selection order;
  prerequisites (pkl for generate; Packer and a token for build); using the
  bundle standalone; retention; tag, list and prune; what is baked vs done at
  launch; costs. Put the **trust boundary** warning near the top.
- [ ] **Step 2:** In `docs/config.md`, document the three `image` forms, the
  newest-eligible rule, failing before Create, and the baked-shell constraint.
- [ ] **Step 3:** In `RELEASE_NOTES.md`, replace the `### Design` cloud-image
  entry with a user-facing feature entry.
- [ ] **Step 4:** `pre-commit run --all-files` passes.
- [ ] **Step 5:** Commit `Document custom cloud images`.

### Task 15: Paid-cloud smoke test (human gate)

Not executable by an agent without the human's explicit go-ahead and a stated
budget. Follow bead `cloudlab-tjw.15` as written. Execution stops after
Task 14 and hands this back to the human.

## Final steps (after Task 14)

- [ ] `go test ./... > /tmp/out.log 2>&1; echo $?` → 0; `pre-commit run --all-files`; `nix flake check`.
- [ ] Run `refactor:scan` over the branch's changes and fix its findings.
- [ ] Run `commit-tools:review-commits` to fold any TDD churn.
- [ ] Close beads `cloudlab-tjw.1`–`.14`; leave `.15` and the epic open.
