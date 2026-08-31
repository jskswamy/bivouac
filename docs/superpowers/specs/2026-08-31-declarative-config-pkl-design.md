# Declarative config: cloudlab.pkl replaces cloudlab.yaml

## Status

Proposed — amends [ADR-0001](../../adr/0001-go-implementation.md) and
supersedes [ADR-0005](../../adr/0005-module-based-package-composition.md)

## Context

cloudlab currently plans for two ways of driving it: imperative CLI
commands (`up`, `down`, etc. — built in the Foundation phase) and a
declarative per-repo config file, `cloudlab.yaml` (ADR-0005), covering
packages and flakes only. Everything else — instance region, size,
template, SSH keys — has no declarative representation yet; those were
always going to be CLI flags or defaults.

This spec replaces `cloudlab.yaml` with `cloudlab.pkl`, written in
[Pkl](https://pkl-lang.org) (Apple's configuration-as-code language),
and expands its scope to cover instance parameters as well as
packages/flakes — one file, richer than YAML, with real reuse and
validation. The imperative CLI commands are unaffected by this spec;
this is purely about the declarative file's format and scope.

**This sub-project produces a standalone, testable `internal/config`
Go package with a `Load()` function, plus a foundational config-structure
doc and worked examples — not wired into any CLI command.** Wiring
`Load()`'s output into `up` (building a `provider.InstanceSpec` and a
Nix package list from it) is a later sub-project, matching how the
Provider layer landed without CLI wiring.

## Why Pkl, and what it costs

Plain YAML can't express what's wanted here: reusable base
configuration across projects (a personal `size`/SSH-key/package
default, layered under every project — the same idea as Terraform
modules or Helm values-layering), and validated instance parameters
(catching a bad size/region slug at config-parse time instead of after
a failed API call). YAML has no native mechanism for either; both
would need bolt-on tooling (Helm-style text templating, a
separately-maintained JSON Schema) that doesn't compose the same way
Pkl's typed constraints do natively.

The cost is real and was verified directly against Pkl's own docs and
an actual GitHub issue hitting this exact question: **Pkl is not a
pure-Go, in-process parser.** `pkl-go`'s generated `Load`/`LoadFromPath`
functions spawn the `pkl` CLI as a subprocess for every evaluation —
confirmed via `pkl-go`'s Evaluator API docs ("Evaluation occurs by
spawning the `pkl` cli as a child process") and via
[apple/pkl#375](https://github.com/apple/pkl/issues/375), where someone
hit precisely this "why does `LoadFromPath()` need the `.pkl` file (and
CLI) at runtime, even after codegen" confusion. Codegen (`pkl-gen-go`)
is genuinely dev-time-only, like `gomock` — it only produces Go *type
definitions* from a schema. But loading actual *data* from a specific
`.pkl` file — which is what reading a user's own `cloudlab.pkl` requires
— means executing that file (Pkl's `amends`/imports/computed values
make a `.pkl` file a small program, not inert data), and that execution
happens inside the Pkl runtime, not in the Go binary.

This directly conflicts with [ADR-0001](../../adr/0001-go-implementation.md)'s
stated reason for choosing Go: "single static binary, no interpreter/
dependency setup burden for users installing a CLI tool." **This spec
amends ADR-0001**: cloudlab now requires the `pkl` CLI to be installed
on any machine that runs a command reading `cloudlab.pkl` (every
`up`/`shell` invocation, once wired in a later phase). Accepted as a
deliberate tradeoff — the composition/validation power is worth the
dependency.

An authoring-time-only alternative was considered (author in `.pkl`,
render to plain YAML via `pkl eval -f yaml`, commit the rendered file,
cloudlab never touches Pkl) — rejected because it doesn't give
cloudlab's own validation logic (e.g. checking a `size` against live
`ListSizes` output) anything to run against at the point that matters.

## Why the personal base is a Go-side merge, not Pkl's `amends`

The original idea was for a project's `cloudlab.pkl` to do
`amends "~/.config/cloudlab/base.pkl"`, inheriting personal defaults
and overriding only what differs — Pkl's own composition mechanism,
used directly. Two facts verified against Pkl's own language reference
kill that approach:

1. **Pkl does not expand `~`.** Its module-URI resolution supports
   `file:`, `https:`, `package:`, `pkl:`, and relative paths — no
   home-directory shorthand.
2. **`amends` targets must be a static string literal**, not a computed
   expression. Pkl supports reading environment variables
   (`read("env:HOME")`) and string interpolation for regular
   properties, but not inside an `amends` clause.

Combined, there is no way to write `amends "<dynamic per-user home
path>"` in a `.pkl` file. Two paths were considered: hardcode an
absolute `file://` path to one specific machine (works, but breaks the
moment a teammate reads the same committed file on a different
machine — `cloudlab.pkl` is meant to be shared, matching how
`cloudlab.yaml` worked), or move the reuse mechanism out of Pkl
entirely. **This spec takes the second path**: `internal/config.Load`
evaluates the project's `cloudlab.pkl` and, separately, a personal base
file if one exists, then merges the two *in Go*. `amends` remains
available within Pkl for anything a user wants to compose *inside*
their own file (or files they control the paths to) — it's simply not
the mechanism for this cross-machine, cross-repo feature.

This also fixes the `~`-expansion gap outright: since Go resolves the
base path, not Pkl, `~` expansion is a few lines of `os.UserHomeDir()`
string handling, not a language limitation to work around.

## Decisions not settled by existing ADRs

- **Personal base config: `$XDG_CONFIG_HOME/cloudlab/base.pkl`, or
  `~/.config/cloudlab/base.pkl` if unset** (Linux-shaped on every OS,
  matching the Foundation phase's XDG convention for state). Not
  versioned by cloudlab — user-authored, machine-local. Checked for
  existence (`os.Stat`); if absent, the project's `cloudlab.pkl` is
  evaluated standalone with no error.
- **Per-project override: `basePath: String?` in the schema itself.**
  A project can set `basePath = "~/.config/cloudlab/work-base.pkl"` (or
  any other path) to point at a different base than the XDG default —
  satisfies "specify a different base folder if needed" without
  needing `amends` to be dynamic, since it's a plain string property
  Go reads and resolves (including `~` expansion) itself.
- **Merge semantics.** Scalars (`region`, `size`, `template`): the
  project's value wins if set, else the base's. Lists (`packages`,
  `flakes`, `sshKeys`): additive — the base's entries first, then the
  project's appended, not one replacing the other. Matches how
  `cloudlab.yaml` packages were always meant to merge (ADR-0005) rather
  than override.
- **Required fields become nullable in the schema, validated after
  merge, not during Pkl evaluation.** `region`/`size`/`template` are
  `String?` (not bare `String`) specifically so a project file can
  validly omit them and rely on the base to supply them — Pkl would
  otherwise refuse to evaluate an incomplete file before Go ever gets a
  chance to merge in the base's values. `Load` returns a clear error
  naming which field is still unset after merging both files, if any
  are.
- **`cloudlab.pkl` must exist in the repo, even if trivial.** No
  implicit "no file present → read the personal base directly"
  fallback. A project relying entirely on personal defaults still has
  a `cloudlab.pkl` — just a mostly-empty one (only `packages`/`flakes`
  have Pkl-level defaults; everything else needing a value comes from
  the merge).
- **`template` is an open `String`, not a closed union type.** The
  actual template catalog (python, docker, and future ones — tailscale,
  coding-agent-harness templates, a notebook/uv template, etc.) is a
  separate, later sub-project covering real Nix/home-manager
  provisioning content. This schema only needs to reference a template
  by name without ever needing to change when a new one is added.
- **No `PklProject` manifest.** Codegen invokes the full package URL
  form directly (`pkl run package://pkg.pkl-lang.org/pkl-go/pkl.golang@0.13.2#/gen.pkl
  pkl/Config.pkl`) rather than introducing a `PklProject` +
  `PklProject.deps.json` dependency-alias layer for a single
  dependency — one less file/lockfile pair to maintain for what a
  single `go:generate` line already does. Verified hands-on: the `@go/`
  alias form used by Apple's own examples only resolves inside a
  directory with a `PklProject` module ("Cannot import dependency
  because there is no project found") — the full `package://...#/go.pkl`
  URL is not a style preference, it's the only form that works without
  one.
- **pkl-go pinned to v0.13.2, not the latest v0.14.0.** Verified
  hands-on: `pkl.golang@0.14.0`'s `gen.pkl` requires Pkl CLI ≥ 0.32.0;
  the Pkl CLI actually available in this environment (via nix,
  `/Users/subramk/.nix-profile/bin/pkl`) is 0.31.1, and running codegen
  against it fails with `Module 'pkl.go.gen' requires Pkl version
  0.32.0 or higher`. v0.13.2 runs cleanly against 0.31.1 — confirmed by
  actually generating code and running it against a real `.pkl` fixture
  end-to-end. Revisit the pin once a Pkl CLI ≥ 0.32.0 is the
  established baseline.
- **Evaluating a project's `cloudlab.pkl` needs network access once,
  not just at codegen time.** Because the schema module itself contains
  `import "package://pkg.pkl-lang.org/pkl-go/pkl.golang@0.13.2#/go.pkl"`
  (needed for the `@go.Package` annotation), and every project file
  amends that schema, evaluating *any* project file — not just running
  codegen — requires Pkl to resolve that package. Verified hands-on:
  the first `LoadFromPath` call fetches and caches it under
  `~/.pkl/cache` (`pkl.EvaluatorOptions.CacheDir`'s default, confirmed
  in `pkl-go`'s source); every evaluation after that is fully offline.
  This is a one-time-per-machine cost, not a per-invocation one, but
  it's a real prerequisite worth stating plainly rather than
  discovering as a confusing first-run failure.
- **Testing: stdlib `testing` only, no testify/afero/uber-go/mock.**
  Matches every other package in the codebase (manual
  `if got != want { t.Errorf(...) }`, no assertion library). `afero`
  specifically cannot help here regardless of preference: the `pkl`
  subprocess reads files directly off the OS filesystem, never through
  Go's `os`/`io/fs` layer, so an in-memory fake filesystem can't
  intercept anything it reads. `uber-go/mock` is skipped for the same
  reason Provider layer skipped mocking `godo`: testing a mocked
  evaluator would only prove the mock returns what it's told, not that
  Pkl evaluation or the Go-side merge logic actually works — the real
  subprocess against real `.pkl` fixtures is the only test that means
  anything here.

## Architecture

```
pkl/
└── Config.pkl                # versioned schema source, with @go.Package annotation
internal/config/
├── generate.go                # //go:generate directive only
├── Config.pkl.go              # generated: Config struct + Load/LoadFromPath (DO NOT EDIT)
├── init.pkl.go                 # generated: pkl.RegisterStrictMapping (DO NOT EDIT)
├── config.go                   # hand-written: Load() wrapper — base resolution + merge
└── config_test.go
```

Verified directly against a real generated example
(`apple/pkl-go-examples`, `simple/` directory): a class defined
*within* the same `.pkl` module (like this schema's `Flake`) generates
into the *same* Go package, not a separate one — a separate package
only happens for classes imported from a different `.pkl` file. So
`Flake` lands as a plain struct in `internal/config`, not a
sub-package.

## Components

**Schema** (`pkl/Config.pkl`):

```pkl
@go.Package { name = "github.com/jskswamy/cloudlab/internal/config" }
module cloudlab.Config

import "package://pkg.pkl-lang.org/pkl-go/pkl.golang@0.13.2#/go.pkl"

/// Optional override for the personal base config's path (supports ~
/// expansion, resolved by cloudlab in Go, not by Pkl). Defaults to
/// $XDG_CONFIG_HOME/cloudlab/base.pkl (or ~/.config/cloudlab/base.pkl)
/// if unset.
basePath: String?

region: String?     // DO region slug, e.g. "nyc3"
size: String?        // DO droplet size slug, e.g. "s-1vcpu-1gb"
template: String?    // open-ended; validated against the template catalog at provisioning time, not here
sshKeys: Listing<String>?  // key IDs/fingerprints already registered with the provider
packages: Listing<String> = new Listing {}
flakes: Listing<Flake> = new Listing {}

class Flake {
  url: String
  packages: Listing<String>
}
```

`packages`/`flakes` mirror the existing `cloudlab.yaml` shape from
ADR-0005 — this spec changes the language and merge behavior, not the
underlying semantics of package/flake composition.

The `import "package://...#/go.pkl"` line and `@go.Package` annotation
are Pkl's own codegen wiring, and the full-URL import form (rather than
the `@go/` alias) is required without a `PklProject` — both verified by
actually running codegen against this exact schema, not assumed.

**Codegen** (`internal/config/generate.go`):

```go
package config

//go:generate pkl run package://pkg.pkl-lang.org/pkl-go/pkl.golang@0.13.2#/gen.pkl ../../pkl/Config.pkl
```

Running `go generate ./internal/config/...` produces `Config.pkl.go`
(the `Config` struct plus generated `Load`/`LoadFromPath` functions),
`Flake.pkl.go` (the inline `Flake` class, confirmed to land in the same
`config` package, in its own file), and `init.pkl.go` (a
`pkl.RegisterStrictMapping` call Pkl needs internally) — all three
committed, all marked `DO NOT EDIT` by the generator itself. Verified
by running this exact codegen command end-to-end: `Region`, `Size`,
`Template`, `BasePath`, and `SshKeys` (the `String?`/`Listing<String>?`
fields) generate as pointer types (`*string`, `*[]string`); `Packages`
and `Flakes` (non-nullable `Listing`s with defaults) generate as plain
`[]string`/`[]Flake`, defaulting to an empty (non-nil) slice when unset
rather than `nil`.

**Hand-written wrapper** (`internal/config/config.go`):

```go
// Load resolves the project's cloudlab.pkl at path, merges it with a
// personal base config if one is found (see basePath resolution
// below), and returns the merged Config. Scalar fields take the
// project's value if set, else the base's; list fields are additive
// (base's entries first, then the project's). Returns an error naming
// any of region/size/template still unset after merging.
func Load(ctx context.Context, path string) (Config, error)
```

Internally: evaluate `path` via the generated `LoadFromPath`; resolve
the base path (project's `BasePath` if set, expanding `~` via
`os.UserHomeDir()`, else `$XDG_CONFIG_HOME/cloudlab/base.pkl` /
`~/.config/cloudlab/base.pkl`); if that file exists, evaluate it too
and merge per the rules above; validate the merged result; return it.

A project's `cloudlab.pkl` must `amends` the schema module to evaluate
correctly through the generated `LoadFromPath`/`EvaluateModule` path —
verified hands-on: a "duck-typed" module that merely declares
same-named properties without amending the schema fails at evaluation
time with an opaque decode error (`expected array length 2 but got
4`), because `pkl-go`'s strict object mapping expects the module to
actually be (via `amends`) the registered `cloudlab.Config` type, not
just structurally similar to it. `amends "Config.pkl"` (relative to a
co-located schema file) was confirmed to work end-to-end, both through
`pkl eval` and through the generated Go `LoadFromPath`.

## Data Flow

1. A caller (a later phase) resolves the repo-root path to
   `cloudlab.pkl` (via `internal/identity`, unchanged) and calls
   `config.Load(ctx, path)`.
2. `Load` evaluates the project file via the generated `LoadFromPath`.
3. `Load` resolves and checks for a personal base file; if present,
   evaluates it too and merges it under the project's values.
4. `Load` validates that `region`/`size`/`template` are set after
   merging; returns a clear error naming any that aren't.
5. Out of scope here: turning the returned `Config` into a
   `provider.InstanceSpec` and a Nix package list for `up` to consume.

## Error Handling

- `pkl` CLI not installed → the evaluator's raw
  `exec: "pkl": executable not found` is wrapped into a clear,
  actionable message naming the install step, not surfaced as a bare
  Go exec error.
- Pkl evaluation/type errors (malformed file, wrong type) → Pkl's own
  descriptive error is wrapped with operation context
  (`"loading %s: %w"`), never swallowed.
- No implicit defaults if `cloudlab.pkl` is missing entirely — `Load`
  returns a clear "file not found" error; there is no fallback to
  reading the personal base directly.
- A required field (`region`/`size`/`template`) still unset after
  merging both files → `Load` returns an error naming exactly which
  field, not a generic "invalid config."

## Testing

`config_test.go` uses real temp-dir `.pkl` fixtures — each amending
`pkl/Config.pkl` via a relative path computed at test time (fixtures
are written into a temp dir alongside a copy of, or a path back to,
the real schema — settled in the implementation task, not here),
including a fixture standing in for the personal base, pointed at via
`basePath` rather than the real `~/.config/cloudlab/`, so no test ever
touches a real user's machine-local file — evaluated by the real `pkl`
binary. This is the first package in the codebase whose tests require
an external binary on `PATH` and, on a machine that has never run
`pkl` against this schema before, network access for the one-time
`pkl.golang` package fetch; the implementation plan documents both
prerequisites explicitly rather than letting them surface as a
surprising CI failure.

Covers: a fully self-contained project file (no base needed), a
project file merging with a base fixture (scalars override, lists
merge additively — both directions asserted), a project file missing a
required field with no base to supply it (clear error naming the
field), a missing `cloudlab.pkl` (clear not-found error), a malformed
`.pkl` file (clear evaluation error), a `basePath` override pointing
at a non-default fixture location, and — if feasible without
flakiness — a missing `pkl` binary path (skipped/marked if `pkl` isn't
on `PATH` in the test environment, rather than failing opaquely).

## Documentation

Schema code and a working loader aren't enough on their own — a user
staring at their first `cloudlab.pkl` needs a real reference, not just
inline Pkl doc-comments. This sub-project's deliverables include:

- **A foundational config-structure doc** (`docs/config.md` or
  similar), documenting every schema field, its type, whether it's
  required (post-merge) or has a default, how the base-merge and
  `basePath` override work, and how each field maps to what actually
  gets provisioned (e.g. `size` → the DO droplet size slug `Create`
  uses). Written for a user who has never seen Pkl before, not just
  for someone who already knows the schema.
- **At least two worked examples**: a minimal, self-contained
  `cloudlab.pkl` with no base needed, and a paired example showing the
  personal-base pattern (a base fixture plus a project file that
  merges with it and overrides one or two fields) — so the reuse
  mechanism this whole design exists for is demonstrated, not just
  described in prose. Each example's `cloudlab.pkl` amends the schema
  via a relative path (`amends "../../pkl/Config.pkl"` or similar,
  exact path settled in the implementation task) since the examples
  live inside cloudlab's own repo; the doc calls out explicitly that a
  project in an independent repo will instead need a stable URL once
  cloudlab has tagged releases (see Out of scope), rather than implying
  the relative form is what a real, separate project repo would use.

This lands alongside the schema and `Load()` implementation, not as a
follow-up — the plan's tasks should include writing it, not defer it
to whichever later sub-project wires `Load()` into `up`.

## Out of scope (deferred to later sub-projects)

- The template catalog itself: what `python`, `docker`, `tailscale`,
  coding-agent-harness templates, a notebook/uv template, etc. actually
  install/configure. This spec only needs `template` to be a valid,
  open-ended string.
- Wiring `config.Load`'s output into `up` (building
  `provider.InstanceSpec` and the Nix package list from a `Config`).
- SSH key sourcing (the paused SSH key management sub-project) — this
  schema's `sshKeys` field is just `Listing<String>` of already-resolved
  IDs/fingerprints, however they were obtained.
- Any Terraform-style multi-instance `apply` workflow — explicitly not
  wanted; one instance per project, imperative commands stay as they
  are.
- **How an arbitrary external repo's `cloudlab.pkl` references this
  schema at a stable location.** A project file must `amends` the
  schema module (see Components) — for a project living in cloudlab's
  own repo (this sub-project's fixtures/examples), a relative path
  (`amends "Config.pkl"`) works. A project in a *different*,
  independent repo can't use a relative path into cloudlab's source
  tree. Verified hands-on that `amends` over an `https://` URL
  mechanically works (fetched a real file from
  `raw.githubusercontent.com` during verification) — the likely answer
  is a tag-pinned raw URL, e.g. `amends
  "https://raw.githubusercontent.com/jskswamy/cloudlab/v0.1.0/pkl/Config.pkl"`,
  once cloudlab has real tagged releases to pin against. Not resolved
  here because it's a distribution/versioning concern, not a `Load()`
  concern — worked examples in this sub-project use the relative,
  co-located form and say so explicitly, rather than presenting an
  unverified URL pattern as settled.
