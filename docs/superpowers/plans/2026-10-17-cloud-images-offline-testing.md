# Cloud Images Offline Testing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Test every DigitalOcean interaction of the cloud images feature without DigitalOcean: real commands through the real client against a stateful fake API (tier 1), then real Packer and QEMU (tier 2).

**Architecture:** A shared in-memory DigitalOcean API (`dofake`) served over `httptest`, speaking godo's own types. Production gains a `DIGITALOCEAN_API_URL` override so the real binary and Packer can be pointed at it. Tier 2 adds a QEMU backend behind the same fake.

**Tech Stack:** Go, godo v1.205.0, net/http/httptest, cobra; tier 2 adds QEMU, Packer 1.15.4 and the DigitalOcean plugin 1.4.1.

**Spec:** [`docs/superpowers/specs/2026-10-17-cloud-images-testing-design.md`](../specs/2026-10-17-cloud-images-testing-design.md), decisions D1–D12. The feature spec it tests is [`2026-10-17-cloud-images-design.md`](../specs/2026-10-17-cloud-images-design.md).

## Global Constraints

- No real DigitalOcean calls from any test. Tier 1 tests use only `dofake`.
- `go test ./...` stays fast: no test in the default suite sleeps on a 5 s poll; pass a millisecond poll interval.
- An unknown route reaching `dofake` fails the test (D2).
- Wire bodies are marshalled from godo types inside DigitalOcean's envelopes (D3).
- Never pipe `go test`; redirect to a file. Commit messages classic style, no AI trailer, no bead IDs in code.

## Review Focus

1. A request the code makes that `dofake` does not model must fail loudly, not return an empty 200.
2. Pagination: an image or key listing longer than one page must be read to the end through the real client.
3. Errors with real status codes (403, 404, 429, 500) surface through the real provider as the sentinel errors the commands branch on (`provider.ErrForbidden`, `provider.ErrImageNotFound`, `provider.ErrNotFound`).
4. `up` from `tag:`, numeric ID and stock slug sends the right `image` field (ID vs slug) and the right boot script (baked vs stock) in the create request body.
5. Retention deletes over HTTP only what the plan said, oldest first, and survives an image vanishing between plan and delete.

---

### Task 16: Stateful fake DigitalOcean API

**Files:**
- Create: `internal/provider/digitalocean/dofake/dofake.go`, `routes.go`, `dofake_test.go`
- Modify: `internal/provider/digitalocean/digitalocean.go` (options on `New`)
- Modify: callers of `digitalocean.New`

**Interfaces:**
- Produces, in `digitalocean`:
  - `type Option func(*Provider) error`
  - `func WithBaseURL(raw string) Option` — must end up with a trailing `/`; rejects a URL without scheme and host.
  - `func WithPollInterval(d time.Duration) Option` — rejects `d <= 0`.
  - `func New(token string, opts ...Option) (*Provider, error)`
- Produces, in `dofake`:
  - `func New(t testing.TB) *Server` — starts the server, registers `t.Cleanup` that closes it and calls `t.Errorf` for each unknown request.
  - `func (s *Server) URL() string`
  - `func (s *Server) Provider(t testing.TB) *digitalocean.Provider` — real provider on this server, poll interval 1 ms.
  - Seeding and inspection: `AddImage(godo.Image) godo.Image` (assigns an ID if zero), `AddSize(godo.Size)`, `AddKey(godo.Key) godo.Key`, `Images() []godo.Image`, `Droplets() []godo.Droplet`, `Keys() []godo.Key`, `Requests() []Request` where `type Request struct{ Method, Path, Query string; Body []byte }`.
  - Snapshot as the Packer plugin produces one: `func (s *Server) Snapshot(dropletID int, name string) (godo.Image, error)` — creates a `type: snapshot` image of the droplet in its region, `status: available` unless `SetSnapshotPending(n)` asked for `n` reads of `pending` first.
  - Failure injection: `func (s *Server) Fail(method, pattern string, status, times int)`; `pattern` is a Go 1.22 `ServeMux` path pattern such as `/v2/images/{id}`; 429 responses carry `Retry-After: 1` and DigitalOcean's `ratelimit-*` headers.
  - `func (s *Server) Before(method, pattern string, hook func(*Server))` — runs once before the next matching request.
  - `type Backend interface { Create(d godo.Droplet, userData string, keys []godo.Key) (ip string, err error); PowerOff(id int) error; Snapshot(id int, imageID int) (minDiskGB int, err error); Destroy(id int) error }` and `func (s *Server) SetBackend(Backend)`; the default backend allocates `192.0.2.N` and does nothing else. Tier 2 supplies the QEMU one.
- Routes (all under `/v2`; list routes honour `page`/`per_page` and return `links.pages.next` and `meta.total`):
  - droplets: `POST /droplets`, `GET /droplets`, `GET /droplets/{id}`, `DELETE /droplets/{id}`, `POST /droplets/{id}/actions` (`power_off`, `shutdown`, `power_on`, `snapshot` with `name`), `GET /droplets/{id}/actions/{aid}`, `GET /actions/{aid}`. Created droplets are `active` with a public v4 network immediately.
  - images: `GET /images` (`private=true`, `tag_name=`, `type=`), `GET /images/{id}`, `PUT /images/{id}` (name), `DELETE /images/{id}` (204; 404 when gone).
  - keys: `GET /account/keys`, `POST /account/keys`, `GET /account/keys/{id}`, `DELETE /account/keys/{id}`.
  - tags: `POST /tags`, `GET /tags/{name}`, `POST /tags/{name}/resources` (resource type `image` adds the tag to the image's `tags`).
  - `GET /sizes`, `GET /regions`, `GET /account`.
  - Error bodies use DigitalOcean's `{"id": "...", "message": "..."}`.

- [ ] **Step 1: Write the failing tests** in `dofake_test.go`, each through the real `digitalocean.Provider` from `s.Provider(t)`:
  - `TestFake_CreateGetDestroyDroplet` — create returns an IP, `Get` finds it, `Destroy` then `Get` is `provider.ErrNotFound`.
  - `TestFake_ListImagesPaginates` — seed 45 tagged snapshots; `ListImages(ctx, tag)` returns 45; `Requests()` shows more than one page fetched.
  - `TestFake_TagImageThroughTagsAPI` — after `TagImage`, the image's tags include the new tag and `ListImages` by that tag finds it.
  - `TestFake_DeleteMissingImageIsNotFound` — `DeleteImage` twice; the second is `provider.ErrImageNotFound`.
  - `TestFake_FailInjects` — `Fail("DELETE", "/v2/images/{id}", 403, 1)`: first delete is `provider.ErrForbidden`, second succeeds.
  - `TestFake_UnknownRouteFailsTest` — uses a recording `testing.TB` stub; a `GET /v2/volumes` is reported as unknown.
  - `TestFake_SnapshotPendingThenAvailable` — `SetSnapshotPending(2)`; `GetImage` reads `pending`, `pending`, then `available`.
- [ ] **Step 2: Run them; they fail to compile** (`dofake` does not exist).
- [ ] **Step 3: Implement** `digitalocean.New(token, opts...)` and update its callers (`cmd/run_instance.go` `resolveProvider` and any other), then `dofake`. Existing provider tests keep passing unchanged.
- [ ] **Step 4: Run** `nix develop -c go test ./internal/provider/... > /tmp/out.log 2>&1; echo $?` → 0, then the full suite.
- [ ] **Step 5: Commit** "Add a stateful fake DigitalOcean API".

### Task 17: Contract tests through the real client

**Files:**
- Modify: `cmd/run_instance.go` (honour `DIGITALOCEAN_API_URL`, D5)
- Create: `cmd/contract_up_test.go`, `cmd/contract_image_test.go`
- Modify: `cmd/up.go` only if `up` needs a seam for its post-create steps (it hard-codes `lifecycle.DefaultSteps()`, whose `WaitReady` would dial SSH); add it the way `newUpCmdWithProvider` injects the provider.

**Interfaces:**
- Consumes: `dofake` from Task 16; `newUpCmdWithProvider`, `newImageBuildCmdWithProvider(resolve, packer)`, `newImageListCmdWithProvider`, `newImageTagCmdWithProvider`, `newImagePruneCmdWithProvider(resolve, interactive)`.
- Produces: `resolveProvider` builds `digitalocean.New(token, digitalocean.WithBaseURL(v))` when `DIGITALOCEAN_API_URL` is set and non-empty, and prints `Using DigitalOcean API at <url>` to stderr once. An invalid URL is an error naming the variable.

- [ ] **Step 1: Write the failing tests.** Every test resolves a real provider from `dofake` (`s.Provider(t)`) and seeds snapshots with real marker tags (`image.SnapshotTags`). Assertions read the fake's state and `Requests()`, not provider return values.
  - `TestResolveProvider_HonoursAPIURL` — `DIGITALOCEAN_API_URL` set to the fake, `DIGITALOCEAN_TOKEN` dummy: the provider lists the fake's images; stderr names the URL. `TestResolveProvider_RejectsBadAPIURL` — `"::"` errors mentioning `DIGITALOCEAN_API_URL`.
  - `TestContractUp_TagSendsImageIDAndBakedScript` — config `image = "tag:my-dev"`, two eligible snapshots: the `POST /v2/droplets` body has `image` equal to the newer ID (a number) and `user_data` without the Nix installer.
  - `TestContractUp_StockSlugSendsSlugAndFullScript` — `image = "ubuntu-24-04-x64"`: body `image` is the slug string, `user_data` contains the Nix installer, and no `/v2/images` request was made.
  - `TestContractUp_NumericIDInOtherRegionFailsBeforeCreate`, `TestContractUp_TagBuiltElsewhereNamesRegion`, `TestContractUp_SizeTooSmallFailsBeforeCreate` — no `POST /v2/droplets` in `Requests()`.
  - `TestContractUp_ForbiddenListingIsReported` — `Fail("GET", "/v2/images", 403, 1)`: error mentions permission; no create.
  - `TestContractImageBuild_EndToEnd` — the injected packer func acts as the plugin does against the fake: create a droplet, `Snapshot` it, tag it through `POST /v2/tags/{name}/resources` with the bundle's marker and user tags, delete the droplet, and return the artifact. With three older builds and `--keep-previous 1`: the config file now says `image = "tag:my-dev"`, two old snapshots are deleted oldest first over `DELETE /v2/images/{id}`, the new one and the newest old one remain, and no droplet is left.
  - `TestContractImageBuild_PendingSnapshotIsNotCounted` — snapshot stays `pending`: build fails in `verify`, nothing deleted.
  - `TestContractImageList_Paginates` — 45 snapshots: `--json` lists 45.
  - `TestContractImageTag_AddsTagOverHTTP` — the image's tags in the fake gain the new tag.
  - `TestContractImagePrune_RaceAndForbidden` — `Before("DELETE", "/v2/images/{id}", ...)` deletes the target first: reported as already gone, run continues; then `Fail(... 403, 1)` on the next: partial result with deleted and pending IDs.
  - `TestContractImagePrune_RateLimited` — `Fail("GET", "/v2/images", 429, 1)`: the error mentions the rate limit, nothing deleted.
- [ ] **Step 2: Run them and confirm they fail** for the stated reasons (missing env support; any real bug found here is fixed test-first in its own commit, and reported).
- [ ] **Step 3: Implement** the `DIGITALOCEAN_API_URL` support and any `up` seam.
- [ ] **Step 4: Run** the full suite and `pre-commit run --all-files`.
- [ ] **Step 5: Commit** "Test image commands through the real client" (plus separate commits for any bug fixes).

### Task 18: QEMU end-to-end harness (tier 2)

Done by hand after Tasks 16–17, not by the workflow: it depends on this
host (sudo, KVM, bridge) and needs iterative debugging. Package `e2e`,
gated on `BIVOUAC_E2E_QEMU=1`, implementing D6–D11: a `dofake.Backend`
that boots QEMU VMs on a bridge from the pinned Ubuntu image with a
NoCloud seed, real Packer with `DIGITALOCEAN_API_URL` pointed at the fake,
then the real `bivouac up` from the resulting `tag:` image. Implemented
in `e2e/`; how to run it is on `TestImageBuildAndLaunch`, and what it
found is under Findings in the spec.
