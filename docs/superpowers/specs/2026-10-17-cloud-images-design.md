# User-owned cloud images through Packer

## Status

Draft for user approval. Tracked by beads `cloudlab-tjw`.
No implementation is authorized by this draft.

## Intent

Reduce time from `bivouac up` to an interactive, provisioned workspace.
The motivating configuration currently takes approximately 5–10 minutes.
Users build and own images in their own cloud account. Bivouac abstracts
Packer configuration and invocation, not image hosting or rebuild scheduling.

Start with DigitalOcean. Produce provider-native snapshots, not a universal
ISO. Every build starts from stock Ubuntu, never from the previous baked
image. Preserve normal Nix reconciliation on launch and `provision`.

## Agreed requirements

- Resolve an explicit Pkl file, otherwise project `bivouac.pkl`, otherwise
  personal `base.pkl`. Fail when none exists. Explicitly selected missing or
  invalid files fail without falling back.
- Project configuration retains normal base inheritance and overrides.
- Generate a self-contained Packer bundle that users can edit, commit and run
  without Bivouac. A generate-only command does not require Packer.
- Build mode requires an installed Packer binary and fails clearly if absent.
  Nix is installed inside the builder using the existing Determinate installer
  approach. No alternative provisioning engine is part of this feature.
- Users select their own image tag. A stable tag reference avoids rewriting
  `base.pkl` after each rebuild. Support explicit image IDs as well.
- List and tag images, and prune older images. Automatically prune after a
  successful build by default, with an option to disable it.
- Retention means current plus N previous successful images: N defaults to 1,
  so two images total. Users can increase it.
- If enabled automatic pruning fails, exit nonzero while reporting that the
  image build succeeded and identifying the new image. Do not destroy it as
  rollback; allow a separate prune retry.
- A requested image unavailable in the selected region fails before creating
  a VM. No copying, stock-image fallback or automatic build.
- Recreate machine identity and access on every launch. Software such as
  Tailscale can be baked; auth keys, enrollment and pairing cannot.
- Users can arrange rebuilds through their own CI. No input watching, drift
  detection, scheduled rebuilds, frozen launch revisions, or asynchronous
  provisioning redesign. Nix decides what existing paths it can reuse.

## Proposed implementation choices requiring approval

These choices fill in mechanics; they are recommendations, not additional
requirements attributed to the user.

1. Prebuild the configured Home Manager activation closure into the Nix store
   without activating an end-user home during the image build. Keep that
   closure rooted against GC. Perform normal activation on launch.
2. Default generate-only output to `./bivouac-image`; managed builds use a
   unique directory under the Bivouac XDG cache so repeated builds work without
   overwriting prior bundles. Both support `--output` and refuse to overwrite
   a nonempty destination. Retain bundles on success/failure for inspection;
   users can remove them. Paths inside each bundle are bundle-relative.
3. First provider support targets DigitalOcean's existing Ubuntu 24.04 x86_64
   path. Unsupported architecture/size/source combinations fail explicitly;
   do not invent ARM support based only on the Pkl schema accepting `arm64`.
4. Scope automatic retention to a user tag, architecture and build region,
   and to Bivouac-managed snapshots. CI must serialize builds/prunes for the
   same scope; no distributed image-management service is introduced.
5. Use CLI flags for build retention settings initially rather than adding an
   image-policy Pkl object. Persist only the launch reference in config.
6. Build the closure as the same account name a launch will use, not an
   arbitrary builder name. The template reads `$USER`/`$HOME` through
   `--impure` (`reconcile.go`), so `home.username` and `homeDirectory` are
   inputs to the activation package: a different builder name yields a
   different package and reuses only the shared package paths. The remote
   username comes from `identity.RemoteUser()`, so the bundle records it
   as a build parameter (default: the invoking user's value) and the temporary
   account is created with that name, then removed in cleanup.
7. The login shell is baked, because cloud-init decides it once and nothing
   applies it later (`cloud-init.sh`, `checkLoginShell`). Record the baked
   shell in the image metadata. A launch whose resolved `shell` differs from
   the image's fails before creating a VM, naming both values; it does not
   fall back or install a second shell silently.

The first choice avoids baking a CI runner's home or a personal account's
application state and prevents arbitrary Home Manager activation scripts from
joining networks or starting pairing services during image creation. It also
means activation-only downloads (for example Skillshare/plugin installs) can
still happen at launch. The initial speedup comes from Nix installation,
OS shell preparation and store population, not a claim that all user setup is
already active when SSH starts. Home-dependent generated derivations may also
need rebuilding for the real launch user; shared package paths can still be
reused. Measure before extending this boundary.

## CLI

The noun/action group matches `preset`, `secrets` and `session`:

```bash
bivouac image generate --tag my-dev --output ./packer
bivouac image build --tag my-dev
bivouac image build --config ./custom.pkl --tag my-dev
bivouac image build --tag my-dev --keep-previous 3
bivouac image build --tag my-dev --no-prune
bivouac image list --tag my-dev
bivouac image list --tag my-dev --json
bivouac image tag <image-id> my-dev
bivouac image prune --tag my-dev --dry-run
bivouac image prune --tag my-dev --keep-previous 1
```

`generate` and `build` accept `--config`, `--tag` and `--output`. Build accepts
`--keep-previous` and `--no-prune`; negative retention is invalid, zero means
current only. `prune` accepts `--keep-previous`, `--dry-run` and `--yes`.
Explicit prune previews IDs and asks for confirmation by default; CI uses
`--yes`. Build presents the automatic retention policy up front and performs
it without a second prompt. Reject contradictory flags, not silently ignore
them. Outside a terminal, never wait for an interactive confirmation.

Image operations work without a Git repository when an explicit config or
personal base suffices. Respect `--repo` when choosing a project config; it
must not redirect provider operations to another implicit identity. List/tag/
prune can operate by explicit tag/ID without loading an unrelated project.
Image list shows ID, tags, created time, region availability and management
status. List/prune accept optional `--region` and `--arch` filters. Explicit
prune without these filters groups matching images by their build-region and
architecture markers and applies retention independently per group; it never
chooses a region from unrelated project configuration. Managed build pruning
is restricted to that build's scope. Implement `--json` for generate/build/
list/tag/prune where applicable;
progress goes to stderr and stdout contains the structured result. Partial
success errors include `image_created`, image ID, phase and cleanup outcome.
Add command metadata, manifest coverage and usage examples consistently with
the existing CLI.

## Configuration selection and launch reference

Selection is based on file presence, not whether a higher-priority file happens
to validate. An invalid project config is an error, not permission to use base.
Explicit/custom project files use existing base resolution and path-relative
rules. A base file selected by itself is loaded exactly once: current
`config.Resolve` would otherwise risk merging the default base with itself
and duplicating additive lists. Validate region, size and template for image
creation without requiring personal SSH keys; Packer uses temporary keys.
Generate-only performs local Pkl validation, not live cloud calls or local Nix
builds. Remote Nix evaluation/build is part of the generated workflow.

Example stable launch reference, extending the existing string field:

```pkl
image = "tag:my-dev"
```

Retain stock image slugs for existing workflows and accept positive numeric
image IDs. Build source selection is separate and fixed to the supported
stock Ubuntu mapping, regardless of `image` in the selected config.

Presence-aware inheritance must allow `image` and the selected architecture
from base to survive when a project omits them. Today `mergeConfig` takes the
project schema-default `Image` and `Arch` unconditionally. Correct the relevant
presence/default handling and test explicit overrides; do not silently claim
the existing merge already supports the proposed base setting.

After a successful Bivouac-managed build, report the exact ID, stable reference,
selected config path and generated output directory. Set the stable reference
in the selected writable configuration when it can be changed safely. For a
read-only/Nix-store-backed config, print the exact snippet instead. Preserve
comments and unrelated declarations; never replace a Home Manager symlink or
change Nix store files. If a safe targeted edit cannot be made, print guidance
rather than re-render arbitrary Pkl and discard its expressions. On a genuine
write error, report the created image and return nonzero. Expected read-only
fallback is a warning, not a failed cloud build.

## Build bundle and execution

The bundle contains Packer HCL, a short standalone README, remote scripts,
the rendered Nix configuration, nonsecret metadata and any required immutable
local assets. It must not rely on Bivouac callbacks, the original config path,
local temporary directories, an installed Bivouac binary, or a local Nix
installation. If a config references local source trees/assets, bundle them
with explicit reviewable paths or fail with an actionable unsupported-input
error; do not leave dangling absolute references.

Credentials are runtime environment inputs, not rendered values. Build can
resolve the existing DigitalOcean token source and pass it only to the Packer
child process. Standalone runs use `DIGITALOCEAN_TOKEN`. The provider credential
stays with Packer on the invoking host; never forward it into the VM. Packer's
temporary SSH key is distinct from users' access/signing keys. Do not load or
forward GitHub, DoltHub, Tailscale or agent credentials during the build.
Private inputs requiring authentication are not silently granted a user's
SSH agent; failure names the input and leaves credential-bearing workflows
out of this initial design.

Run Packer through the existing external-tool convention with context
cancellation: init the declared provider plugin, validate the generated
bundle, then build. Declare and test a supported plugin version; do not use
an unbounded `main` dependency. Plugin installation by `packer init` is
normal Packer behavior, not installing the Packer binary itself.

The generated DigitalOcean workflow:

1. Start a temporary builder from stock Ubuntu in the selected region/size.
2. Install Nix and the requested shell using shared setup logic extracted from
   current cloud-init. Wait explicitly for bootstrap/cloud-init completion;
   Packer's SSH readiness alone does not imply user-data has finished.
3. Evaluate and build the same template/modules/packages/settings as normal
   provisioning, as an unprivileged temporary builder account. Build the
   activation package rather than invoking runtime reconciliation/activation.
4. Add an explicit persistent GC root directly to the resulting store closure,
   independent of the temporary account/home or any result symlink removed
   during cleanup. Record nonsecret source/config fingerprint, resolved revisions, source OS,
   architecture and Bivouac/bundle format version for diagnosis. This records
   the actual build; it does not freeze later launch reconciliation.
5. Sanitize builder identity/access/state and verify required cleanup.
6. Snapshot, attach the management/scope/user tags and emit a Packer manifest
   with the concrete artifact ID. Only complete, available, correctly marked
   snapshots are launch/prune candidates.

Stock Ubuntu installation plus Nix is not an activated snapshot of a user's
existing workstation. Arbitrary trusted Nix build expressions still execute
inside the builder; neither Nix nor cleanup is a general-purpose detector for
secrets deliberately embedded in user configurations. Document this trust
boundary prominently before users commit/share the generated bundle.

Standalone `packer init`, `packer validate` and `packer build` are documented
in the bundle. Tags are part of its Packer definition, so standalone builds
also produce selectable images. Retention and config writeback are Bivouac
operations: standalone Packer does not run them or require Bivouac. Users can
run `bivouac image prune` separately if desired.

## Identity, cleanup and launch

Preserve Nix, its store, required daemon configuration, the rooted environment
closure and public/nonsecret image metadata. Remove the temporary builder's
account/home and credentials, Packer access keys, SSH authorized/host keys,
cloud-init instance state, machine ID, histories, build logs and temporary
files that could contain sensitive values. Do not copy authenticated
Tailscale/Moshi/agent/Dolt/GitHub state. Do not create those enrollments in the
first place. Keep diagnostic metadata free of tokens and personal instructions.
Use Ubuntu/cloud-init-supported image preparation mechanisms; validate actual
commands on a disposable clone before claiming the image is clean.

A launched image runs a boot path that reuses installed Nix/shell software but
creates the real instance account, fresh authorized keys and sudo/lingering
configuration. Cloud-init regenerates SSH host identity and machine identity.
Only after valid user access exists is root SSH disabled. Retain the existing
SSH-key ownership and validation guarantees.

The current fresh-install script unconditionally installs Nix and calls
`useradd`; neither blindly rerun it nor skip all of it. Split reusable software
bootstrap from per-instance access initialization, and test both stock and
baked-image launches. This is necessary clone correctness, not a new package
optimization engine.

After readiness, use normal reconciliation: resolve current config, validate,
ship the wrapper and activate via Home Manager, then place runtime credentials
through existing mechanisms. Project additions and changed floating inputs
remain Nix's responsibility. Do not claim the VM is fully provisioned before
that completes or expand this feature into background provisioning.

## Selection, tagging and retention

DigitalOcean supports listing/tagging images, but a tag is not a unique alias.
Resolve the newest eligible image deterministically (creation time, then ID)
within the requested architecture/region. Require a complete management and
compatibility marker set for the Bivouac baked-image path. A tag with no ready
compatible image errors before creating a Droplet. Persist the resolved ID
alongside the requested reference in instance state, so later status reports
what was actually used. Numeric IDs resolve and validate explicitly; stock
slugs retain their existing behavior. Validate minimum disk/size constraints
before creating a VM where provider metadata allows it.

Use small provider tags for ownership/scope (for example `bivouac-image`,
`bivouac-arch-x86_64`, `bivouac-region-blr1`) plus the user-selected tag.
`image tag` adds the user tag to an existing compatible Bivouac image after
validation; it does not silently adopt an arbitrary image into automatic
pruning. User tags may match unrelated account resources; ownership markers
are essential before deletion. Detailed on-disk build metadata is diagnostic,
not a reason to pretend DigitalOcean exposes unverified fields in its API.

After a successful managed build, parse the Packer manifest rather than human
log text, validate the artifact through the provider, report/persist the stable
reference, then apply retention if enabled. If Packer fails after a snapshot
exists (for example manifest/tagging failure), report any known ID, do not
prune, and do not claim the build succeeded. Packer owns temporary builder
cleanup; report known leftovers on cancellation or error rather than claiming
that a killed process guarantees cleanup.

Prune lists all pages, filters to ready Bivouac-owned snapshots in the exact
scope, sorts deterministically, and retains the newest `1 + keepPrevious`.
Never delete the new build's artifact in that build's cleanup. Failed,
in-progress, foreign or differently scoped images are not prune candidates.
Insufficient retained candidates means no deletion. Explicit prune prints
its proposed deletions before confirmation; dry-run performs no mutations.
Re-check candidates before deletion and stop/report concurrent state changes
rather than expand the deletion set. Serialize same-scope CI operations; no
cross-host lock service is part of v1.

A snapshot deletion failure yields nonzero, lists completed/pending deletions
and leaves retained/new images intact. Treat already-missing candidates as
idempotent cleanup, but permission/API failures remain errors. Build failure
never triggers retention. Existing Droplets are never deleted by image prune;
provider restrictions are respected and surfaced. No automatic cross-region
copying, image transfer or unrelated image adoption.

## Existing code seams and scope

Verified against source at `d592856` (config, provisioning, provider,
lifecycle, reconcile and godo v1.205.0). Confirmed claims and corrections:

- `internal/config`
  - `mergeConfig` takes `project.Arch`, `project.Image`, `Tailscale` and
    `Beads` unconditionally, so a base value for any of them is silently
    dropped. The spec named only image and arch; all four have the defect.
    `declaredFields(raw)` in `read.go` already detects which fields a file
    declares and is the presence mechanism to reuse.
  - `Resolve(path)` loads `path`, then `resolveBasePath` falls back to
    `DefaultBasePath()`. Given `base.pkl` itself it merges the file with
    itself and doubles the additive lists, as the spec says. `ResolveBase`
    is unvalidated and default-path only, so neither fits. Add a
    single-file entry point that validates once.
  - `Resolve` and `loadResolved` need the `pkl` CLI. Generate-only therefore
    requires Pkl but not Packer, a token or Nix. State this.
  - `write.go` only renders a whole file from sparse `Values`; there is no
    targeted edit. Writeback for `image` is therefore a new, small
    primitive. Because the tag reference is set once and survives rebuilds,
    v1 may print the snippet and edit only a file where a top-level `image`
    line can be replaced or appended without touching other text.
  - `docs/config.md` documents `image` as a DigitalOcean slug; update it.
- `internal/provisioning`
  - `cloud-init.sh` confirms the spec: Determinate Nix install, apt shell
    install, `useradd`, key copy from root's `authorized_keys`, sudoers,
    lingering, then root login disabled. Root's keys are DigitalOcean-seeded
    at droplet creation, so sanitization must empty them and the baked boot
    path must still copy the fresh set.
  - `Render`, `NeedsRender`, `ResolveTemplateRef`, `NixSystem` and
    `Validate` (which takes a `Runner`) are pure or runner-based and reusable.
- `internal/reconcile`: `Reconcile` is not reusable (it opens instance
  state, connects over SSH, places credentials). Reuse only the provisioning
  collaborators above.
- `internal/provider` and `digitalocean`
  - `InstanceSpec.Image` is a string and `Create` sets
    `godo.DropletCreateImage{Slug: ...}` only. godo marshals the numeric ID
    only when `ID` is set and `Slug` is empty, so a numeric reference needs
    explicit parsing in `Create`.
  - Add image operations as an optional interface asserted at the call site,
    the pattern `KeyRegistry` already documents, not as methods on
    `Provider`. Reuse the generic `paginate` helper.
  - godo exposes `Image.Regions`, `MinDiskSize`, `Status`, `Tags`, `Type`,
    `ListByTag`, `GetByID`, `Delete`, and tagging through
    `Tags.TagResources` with `ImageResourceType`. `Images.Update` changes only
    name, distribution and description, so it cannot add tags. Disk
    validation needs the size's disk from the Sizes API, because the launch
    size's disk must be at least the snapshot's `MinDiskSize`.
- `internal/lifecycle` and `state`
  - `PrepareUp` renders cloud-init and builds `InstanceSpec{Image:
    cfg.Image}` and holds no provider. The boot path (stock or baked) is
    chosen in the rendered user data, so image resolution must happen before
    `RenderCloudInit`. Either resolve in the `up` command and pass the result
    in, or give `PrepareUp` an image resolver. The resolution API call must
    still precede any billable `Create`.
  - `state.Record` has no image fields. Add `omitempty` requested-reference
    and resolved-ID fields, so old records still load.
- `internal/image` (proposed): bundle generation, Packer invocation and
  workflow orchestration, with pure generation tested apart from paid cloud
  operations.
- `cmd`: thin `image` group, existing progress/table/JSON conventions,
  metadata and manifest registration; documentation and release notes.

Older provisioning specs describe cloud-init as Nix-only. Current code also
creates the user and configures access, and is authoritative for identifying
these seams.

## Verification required for implementation

Tests come first, matching repository conventions. Pure tests cover config
precedence, omitted versus explicit image overrides, base-only resolution,
retention math/sorting/scope and safe output/config writing. API fixture tests
cover pagination, image readiness/region/minimum disk, numeric ID handling,
tag validation, foreign-image exclusion, prune partial failure and no-create
on invalid selectors. Subprocess tests cover missing Packer, generate-only
without Packer/token/Nix, ordered init/validate/build, manifest parsing,
cancellation and no-prune-on-failure. Inspect generated bundles for absence
of secrets, private keys, Bivouac calls and dangling local paths.

Run syntax checks against the real supported Packer/plugin where available;
otherwise explicitly report them unrun, not replaced by fake-binary success.
Real Nix fixture evaluations verify equivalent package/module/settings inputs
and that the built environment closure is GC-rooted.

Before production use, an authorized paid-cloud smoke test must build a real
image and launch two clones. Verify unique machine IDs, unique SSH host keys,
only requested authorized keys, usable nonroot access, no builder account,
no enrolled Tailscale/Moshi identity, no leaked runtime credentials, preserved
store paths/GC roots, successful project-specific activation and compatible
stock-image behavior. Measure cold launch versus baked launch using phase
boundaries and time-to-interaction; no target time is promised without data.
All test Droplets/snapshots are explicitly cleaned up with costs disclosed.

Repository quality gates: `go test ./...` redirected to a file (never piped),
`pre-commit run --all-files`, and `nix flake check`. These validate an eventual
implementation, not completion of the feature merely because this spec exists.

## Non-goals

Shared images or hosting; additional providers now; a universal ISO; another
provisioning engine; Packer installation; a local Nix requirement; rebuild
scheduling; dependency watching; freezing ordinary launch inputs; automatic
region transfer; credential forwarding into builders; distributed locking;
transparent guarantees about arbitrary user activation scripts; or promising
that fresh store paths alone eliminate every launch-time download.

## Primary-source checks

The HashiCorp DigitalOcean builder documentation states that it starts a
Droplet from a source image, provisions it and snapshots it, and does not
manage resulting image retention:
https://developer.hashicorp.com/packer/integrations/digitalocean/digitalocean/latest/components/builder/digitalocean

Source inspected during drafting confirms `snapshot_tags`, `user_data_file`,
region/source/size parameters, and that Packer does not automatically wait for
user-data completion:
https://github.com/hashicorp/packer-plugin-digitalocean/blob/main/builder/digitalocean/config.go
https://github.com/hashicorp/packer-plugin-digitalocean/blob/main/builder/digitalocean/step_snapshot.go

The existing godo dependency exposes image tags, `Images.ListByTag`, numeric
ID lookup and image deletion (`images.go`, `tags.go`, v1.205.0). Availability
of an API method does not replace a live clone/sanitization test. The plugin
sources above were read from upstream main; implementation must pin and test
an actual plugin release rather than assume these observations lock a version.
