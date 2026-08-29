# Provider layer: Provider interface + DigitalOcean implementation

## Status

Proposed

## Context

The Foundation phase (CLI scaffold, git-derived identity, state store) is
implemented and merged. This spec covers the second sub-project: the
`Provider` interface and its DigitalOcean implementation — VM lifecycle
mechanics (create, destroy, get, list) plus read-only region/size/image
discovery, per [ADR-0008](../../adr/0008-provider-abstraction.md) and
`architecture.md`'s Provider abstraction section, which already lock the
`Provider` interface shape:

```go
type Provider interface {
    Create(ctx context.Context, spec InstanceSpec) (VM, error)
    Destroy(ctx context.Context, id string) error
    Get(ctx context.Context, id string) (VM, error)
    List(ctx context.Context) ([]VM, error)
}
```

`digitalocean` (via `godo`) is the only implementation. DO-specific
concepts (droplet size, region, image) stay as direct fields on
`InstanceSpec` rather than a forced cross-provider abstraction — there's
no meaningful shared vocabulary across providers worth designing until a
second one actually exists (ADR-0008).

This spec does not cover: SSH key generation/registration (split into its
own follow-up sub-project — see below), cloud-init/Nix provisioning
content, or wiring `Create`/`Destroy` into the `up`/`down` commands. Those
are later sub-projects.

## Scope split: SSH key management

During brainstorming, "register SSH keys with the provider" grew into a
bigger ask: generating keypairs (with YubiKey/FIDO2 hardware-backed
generation when a key is present) and registering/listing/deleting them
across cloud providers, starting with DigitalOcean. That's a distinct
subsystem, not a detail of VM lifecycle — it gets its own brainstorm and
spec. This phase's `InstanceSpec.SSHKeys` is a plain `[]string` of
provider key IDs; it doesn't care how those IDs were obtained. The output
of the SSH key management sub-project (a registered key ID) is exactly
what feeds into `InstanceSpec.SSHKeys` when `up` is wired up later.

## Decisions not settled by existing ADRs

- **Authentication: `DIGITALOCEAN_TOKEN` env var.** Matches `doctl`'s own
  convention. `digitalocean.New(token string) *Provider` does no network
  call and no validation — auth failures surface naturally on the first
  real API call. Sourcing the env var (and deciding what to do if it's
  missing) is the job of whatever later phase constructs the provider,
  not this package.
- **Testing: `httptest`-server fake, matching godo's own test suite.**
  Verified directly against `godo`'s source
  (`github.com/digitalocean/godo/godo_test.go`): `godo.NewClient` exposes
  a public `BaseURL *url.URL` field specifically so tests can point it at
  a local `httptest.Server` backed by an `http.ServeMux`. This is a
  first-class, DO-maintainer-endorsed pattern, not a workaround — no real
  API calls, no credentials, no cost, runs in CI for free.
- **`Create()` blocks until the droplet is ready.** DO droplets take time
  to boot and get a public IP. `Create()` polls internally until the
  droplet is `active` with an IPv4 assigned (or the caller's `ctx`
  deadline is hit), returning a fully-populated `VM`. This gives callers
  (the `up` command, later) a simple synchronous contract instead of
  duplicating polling logic everywhere a VM gets created, and matches the
  "one command, instance is fully live" design philosophy already
  established for `up`.
- **Progress hook, UI-agnostic.** `Create()`'s internal poll loop is
  exactly where a spinner should hook in later (Charm libraries,
  requested for a richer CLI), but `internal/provider` stays free of any
  terminal-UI dependency — that couples backend logic to presentation and
  makes tests noisy. Instead: a context-carried callback,
  `provider.WithProgress(ctx, func(status string) {...})` /
  `provider.ReportProgress(ctx, status)`, no-op if the caller doesn't set
  one. The actual Charm-based rendering (spinner, progress bar) belongs
  to the `up` command phase, which orchestrates VM creation *and*
  cloud-init wait *and* rsync *and* home-manager reconcile — a real
  multi-step progress display, not just this one polling loop.
- **`Create()` has no hardcoded timeout.** It polls until the droplet is
  ready or the caller's `ctx` is done (via `context.WithTimeout` at the
  call site) — standard Go context convention, no timeout value baked
  into this package to argue about or tune later.
- **VM field set:** `ID`, `Name`, `IP`, `Region`, `Size`, `Status` —
  matches what `state.Record` (Foundation phase) already tracks, plus
  `Status` since `Create()` needs it internally to know when to stop
  polling, and a later `status`/`list` command will want it too.
- **Region/size/image discovery, added to this phase.** Nothing let a
  caller find out what `InstanceSpec.Region`/`Size`/`Image` values are
  actually valid — DO doesn't make every size available in every region
  (GPU sizes especially). Verified against `godo`'s source
  (`regions.go`/`sizes.go`/`images.go`): `godo.Region.Sizes []string` and
  `godo.Size.Regions []string` give the region↔size availability
  cross-reference directly, and `godo.Size.GPUInfo *GPUInfo` (nil for
  ordinary sizes) is the actual CPU-vs-GPU signal — confirmed via DO's
  own docs that GPU droplets use the identical `/v2/droplets` create
  endpoint and request shape as regular droplets, just a GPU-flavored
  `size` slug, so `InstanceSpec.Size` needs no separate GPU field.
  `digitalocean.Provider` grows three query-only methods
  (`ListRegions`/`ListSizes`/`ListImages`) wrapping `godo`'s own list
  calls (`ImagesService.ListDistribution`, not `List` — cloudlab boots
  from official base OS images, not private snapshots/backups) and
  translating to our own light DO-specific types, so `godo`'s types never
  leak out of this package. These are DO-specific methods on
  `digitalocean.Provider`, not part of the generic `Provider` interface —
  same reasoning as everything else DO-flavored. No selection UX here —
  building a picker on top of these (interactively, with Charm) is still
  the `up` command phase's job, same split as the progress hook.

## Architecture

```
internal/provider/
├── provider.go              # Provider interface, InstanceSpec, VM, ErrNotFound, progress hook
└── digitalocean/
    ├── digitalocean.go      # Provider implementation on godo
    └── digitalocean_test.go # httptest-server fake, mirrors godo's own test pattern
```

## Components

### `provider` (provider-agnostic)

```go
type Provider interface {
    Create(ctx context.Context, spec InstanceSpec) (VM, error)
    Destroy(ctx context.Context, id string) error
    Get(ctx context.Context, id string) (VM, error)
    List(ctx context.Context) ([]VM, error)
}

type InstanceSpec struct {
    Name     string
    Region   string   // DO region slug, e.g. "nyc3"
    Size     string   // DO droplet size slug, e.g. "s-1vcpu-1gb"
    Image    string   // DO image slug, e.g. "ubuntu-22-04-x64"
    SSHKeys  []string // DO SSH key IDs/fingerprints — sourced by a later sub-project
    UserData string   // cloud-init script — opaque here, filled by the Provisioning phase
}

type VM struct {
    ID, Name, IP, Region, Size, Status string
}

var ErrNotFound = errors.New("vm not found")

type ProgressFunc func(status string)

func WithProgress(ctx context.Context, fn ProgressFunc) context.Context
func ReportProgress(ctx context.Context, status string) // no-op if none set
```

### `digitalocean`

```go
type Provider struct {
    client       *godo.Client
    pollInterval time.Duration // defaults to 5s in New()
}

func New(token string) *Provider
```

- **`Create`**: builds a `godo.DropletCreateRequest` from `InstanceSpec`
  (each `SSHKeys` string parsed as a numeric ID first, else treated as a
  fingerprint) → `client.Droplets.Create` → `ReportProgress("droplet
  created, waiting for network...")` → polls `client.Droplets.Get` on
  `pollInterval` until status is `active` with a public IPv4 assigned, or
  `ctx.Done()` → `ReportProgress("active")` → converts to `VM`, returns.
- **`Get`/`Destroy`**: one-shot `client.Droplets.Get`/`Delete`; a 404 from
  DO maps to wrapped `ErrNotFound` so callers can distinguish "already
  gone" from a real failure (useful later when `down` treats
  already-destroyed as success via `errors.Is`).
- **`List`**: loops DO's pagination via `response.Links.IsLastPage()`
  rather than silently returning only the first page.

Discovery types and methods (query-only, same pagination handling as
`VM` `List()`):

```go
type Region struct {
    Slug, Name string
    Sizes      []string // size slugs available in this region
    Available  bool
}

type Size struct {
    Slug, Description       string
    VCPUs, MemoryMB, DiskGB int
    PriceMonthly            float64
    Regions                 []string
    Available, GPU          bool
    GPUModel                string // e.g. "H100"; empty if !GPU
}

type Image struct {
    Slug, Name, Distribution string
    Public                   bool
    Regions                  []string
}

func (p *Provider) ListRegions(ctx context.Context) ([]Region, error)
func (p *Provider) ListSizes(ctx context.Context) ([]Size, error)
func (p *Provider) ListImages(ctx context.Context) ([]Image, error) // wraps ImagesService.ListDistribution
```

`ListSizes` sets `GPU: true` and `GPUModel` from `godo.Size.GPUInfo` when
non-nil, `false`/`""` otherwise.

## Data Flow

1. Caller builds `InstanceSpec`, calls `Create(ctx, spec)` — `ctx` may
   carry a progress callback (`WithProgress`) and/or a deadline
   (`context.WithTimeout`).
2. `digitalocean.Provider.Create` translates the spec to a
   `godo.DropletCreateRequest` and calls `client.Droplets.Create`.
3. Reports progress, then polls `client.Droplets.Get` every
   `pollInterval` until the droplet is `active` with an IPv4, or `ctx` is
   done.
4. On ready: reports progress, converts `godo.Droplet` → `VM`, returns.
5. On timeout/cancellation: returns an error naming the droplet's ID
   (the droplet exists and costs money — the ID must not be lost) wrapped
   around `ctx.Err()`.

`Get`/`Destroy`/`List` are single-request translations with no polling.

## Error Handling

- 404 on `Get`/`Destroy` → wrapped `ErrNotFound`.
- `Create()` timeout → error explicitly names the droplet ID.
- Auth/rate-limit/other API errors from `godo` → wrapped with operation
  context (`"creating droplet: %w"`), no custom retry/backoff beyond the
  fixed poll interval.

## Testing

`digitalocean_test.go`, in-package, mirrors godo's own
`mux`/`httptest.Server`/`BaseURL` pattern and constructs
`&Provider{client: testClient, pollInterval: time.Millisecond}` directly
to avoid real wall-clock waits in polling tests. Covers:

- `Create` success, including a poll loop that genuinely loops (not-ready
  on first poll(s), ready on a later one).
- `Create` timeout — asserts the returned error names the droplet ID.
- `Get`/`Destroy` found and 404 (`ErrNotFound`) cases.
- `List` across multiple pages.
- Progress-callback invocation with the expected status strings.
- `ListRegions`/`ListSizes`/`ListImages`: correct translation from
  `godo`'s types (including a size with `GPUInfo` set vs. one without),
  and pagination across multiple pages.

## Out of scope (deferred to later sub-projects)

- SSH key generation, YubiKey/FIDO2 support, and multi-provider key
  registration (its own sub-project).
- Cloud-init/Nix/home-manager provisioning content.
- Wiring `Create`/`Destroy`/`Get`/`List` into the `up`/`down`/`status`/
  `list` commands.
- Charm-based (`bubbletea`/`lipgloss`) rendering of the progress hook,
  and any interactive region/size/image picker built on
  `ListRegions`/`ListSizes`/`ListImages` — both belong to the `up`
  command phase.
