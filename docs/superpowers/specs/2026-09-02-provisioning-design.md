# Provisioning: templates, cloud-init, and render/validate

## Status

Proposed

## Context

Foundation, the Provider layer, and declarative config (`cloudlab.pkl`)
have all shipped, each as a standalone, testable package landed before
any CLI wiring — Provisioning follows the same pattern. ADR-0004
("cloud-init as a thin trigger") and ADR-0005 ("module-based package
composition") already establish the *shape* of provisioning; this spec
turns that shape into real, testable Go code and a real Nix template
catalog, without wiring anything into `up` yet.

**This sub-project produces:** an `internal/provisioning` package
(cloud-init payload, template-ref resolution, render-trigger decision,
per-instance flake rendering, and offline validation) plus a real
`templates/` Nix flake with working `python`/`docker` profiles — not
wired into any CLI command. Wiring this into `up`/`shell`/a new
`provision` command (already decided, see Out of Scope) is a later
sub-project, matching how the Provider layer and declarative config
landed without CLI wiring.

## Decisions

- **Cloud-init installs Nix, nothing else.** No per-template
  parameterization, no home-manager invocation baked into boot-time
  bash. The reason the original two-pass design (template-only switch
  right after boot, full switch after rsync) existed was that
  `cloudlab.yaml` was assumed to be read off the VM's own rsynced copy
  of the repo — home-manager couldn't see project config until the
  repo existed on the VM. That assumption no longer holds:
  `config.Resolve()` reads the project's `cloudlab.pkl` on the user's
  own machine, and reconciliation (a later sub-project) renders and
  ships the result over SSH — the VM's copy of the repo is never
  involved in resolving config. Once that's true, there's no reason to
  wait for rsync before a full reconcile, and cloud-init's job shrinks
  to the one thing it must do before an SSH connection is even
  possible: install Nix. The payload is a real embedded file
  (`internal/provisioning/cloud-init.sh`), not a Go string literal —
  consistent with how the Pkl schema is embedded in `internal/config`:

  ```bash
  #!/bin/bash
  set -euo pipefail

  curl --proto '=https' --tlsv1.2 -sSf -L https://install.determinate.systems/nix | sh -s -- install --no-confirm
  ```

  Verified against the `DeterminateSystems/nix-installer` repo directly
  (the authoritative source — a docs.determinate.systems page gave a
  slightly different, less precise flag set that omitted the `install`
  subcommand).

- **Templates live in this repo's own `templates/` directory, as a
  separate flake from the repo-root dev-shell `flake.nix`**, referenced
  by a floating git ref (`github:jskswamy/cloudlab?dir=templates#python`),
  not a version tag. Template content (a package added to a profile, a
  bug fix) changes on a completely different cadence than cloudlab's
  own CLI logic — coupling them would force a cloudlab release for
  every template tweak. A floating ref means a push to `templates/` is
  live on the next reconcile, with zero release process anywhere. The
  two flakes are unrelated: the root `flake.nix` is cloudlab's own dev
  toolchain (Go, Pkl), never a template.

- **`template` accepts a built-in name or a full flake ref.**
  `cloudlab.pkl`'s `template` field (already a plain `String?`) holds
  either a built-in name (`"python"`, `"docker"` — expanded to the
  default `templates/` ref) or a user's own flake ref
  (`"github:someuser/their-templates#custom"`), used as-is. The
  resolution rule is just: is this string a recognized built-in name,
  or not.

- **`templates/flake.nix` exposes both `homeManagerModules.<name>`
  and `homeConfigurations.<name>` per template.** `homeManagerModules`
  is the bare, composable module `Render` imports when building a
  per-instance wrapper flake. `homeConfigurations` is a thin, statically
  built convenience wrapper around that same module
  (`home-manager.lib.homeManagerConfiguration { modules = [ homeManagerModules.<name> ]; ... }`),
  so the common "nothing extra to add" case can `home-manager switch`
  directly against the static ref, with zero rendering.

- **`Flake.modules` is a `Boolean`, not a list of module names.** The
  confirmed use case is "still pick a template, add one module on top"
  — one module, from one flake, at its default output. A list-of-names
  (parallel to `packages`) would only earn its keep for cherry-picking
  among several distinctly-named modules from a single flake — a
  narrow, unconfirmed need. When `true`, pulls `homeManagerModules.default`
  from that flake; when `false` (default), that flake is only used for
  its `packages` output, exactly as `flakes[]` already works.

- **A new `arch` field, not a hardcoded target system.** DigitalOcean
  offers ARM droplets alongside the more common x86_64 ones, so the
  target system can't be assumed. `arch: String = "x86_64"` is a plain,
  user-facing value (`"x86_64"` or `"arm64"`) — not Nix's own system
  triple naming, which nobody using cloudlab should need to know.
  Translated internally via `NixSystem(arch) string`
  (`"x86_64"` → `"x86_64-linux"`, `"arm64"` → `"aarch64-linux"`; every
  cloudlab instance is Linux, only the CPU part varies). Defaults to
  `"x86_64"`, matching DigitalOcean's most common droplet type; can be
  omitted entirely for that default.

- **Render-trigger rule: render a per-instance wrapper flake only when
  `packages` or `flakes[]` is non-empty; otherwise reuse the static
  template ref for both the very first reconcile on a fresh instance
  and every one after.** `home-manager switch --flake <ref>#<name>`
  evaluates a fixed, self-contained `homeConfigurations.<name>` — there
  is no CLI flag to inject extra modules on top of it. The moment
  `cloudlab.pkl` contributes anything beyond the bare template, the
  only way to add those modules is to render a new wrapper flake that
  imports the template module and the synthetic packages/flakes
  modules together. If neither is set, there's nothing to add, and
  rendering would just reproduce what the static ref's own
  `homeConfigurations.<name>` already provides — wasted work. This rule
  applies uniformly regardless of *when* reconciliation happens: a
  fresh instance's very first reconcile naturally resolves to "no
  packages, no flakes yet, use the static ref" — it isn't a special
  case, it falls out of the same rule.

- **`Render` generates Nix syntax via Go's stdlib `text/template`.** No
  new dependency, no shelling out to `nix` to produce the wrapper. It
  doesn't produce a `flake.lock` — the target machine generates that
  itself on first evaluation, since it already needs network access to
  fetch the template/nixpkgs/home-manager/external-flake inputs
  regardless.

- **`Validate` proves flake outputs exist without building or
  switching anything**, via `nix eval <ref>#<attr> --apply 'x: "ok"'`.
  A bare `nix eval` tries to print whatever it finds at `<attr>`, which
  fails on non-representable values (a home-manager module, a
  function) even when the attribute genuinely exists. The `--apply`
  lambda never touches `x`, so Nix only needs to resolve the attribute
  path far enough to bind it — proving existence without caring about
  shape. Works identically for derivations (`packages.<system>.<name>`)
  and modules (`homeManagerModules.default`); no type-specific
  handling needed. Checks: the resolved template ref's
  `homeConfigurations.<name>` (if no render needed) or
  `homeManagerModules.<name>` (if render needed); each `flakes[]`
  entry's named `packages`; each `flakes[]` entry's
  `homeManagerModules.default` if `modules: true`. No secrets or
  provider credentials required — this is a syntax/existence check
  against public Nix infrastructure, not a live-instance check.

- **Validation errors extract Nix's own final error line, not its full
  trace, and name what was being checked.** A raw `nix eval` failure is
  a multi-line stack trace ending in a specific `error: <reason>` line
  — noisy and unfriendly to surface directly. `lastNixError` extracts
  just that final line; `Validate` wraps it with what was being checked
  (`template "python" does not expose homeConfigurations.python:
  attribute 'homeConfigurations' missing`), and collects every problem
  found into one error rather than stopping at the first — matching how
  `internal/config`'s own `validate()` already names every missing
  field at once.

## Architecture

```
templates/                      # separate flake, own inputs, own lock
├── flake.nix                   # outputs: homeManagerModules.{python,docker},
│                                #          homeConfigurations.{python,docker}
├── flake.lock
└── modules/
    ├── common.nix               # git, age, aide, Claude Code — shared
    ├── python.nix                # imports common.nix + python toolchain
    └── docker.nix                # imports common.nix + docker/minikube

internal/provisioning/
├── cloud_init.go                # //go:embed cloud-init.sh
├── cloud-init.sh                 # the entire cloud-init payload
├── template.go                   # ResolveTemplateRef, NixSystem, splitFlakeRef
├── render.go                      # NeedsRender, Render, the flake.nix.tmpl
└── validate.go                    # Validate, evalExists, lastNixError
```

## Components

**`ResolveTemplateRef`** (`internal/provisioning/template.go`):

```go
// ResolveTemplateRef expands a built-in template name ("python",
// "docker") to its default flake ref under templates/; any other
// string is assumed to already be a home-manager-style "url#name"
// flake ref and is returned as-is.
func ResolveTemplateRef(template string) string
```

**`NixSystem`**:

```go
// NixSystem maps a cloudlab.pkl arch value to the Nix system string
// used for per-system flake outputs. Every cloudlab instance is
// Linux; only the CPU part varies.
func NixSystem(arch string) string
```

**`NeedsRender` / `Render`** (`internal/provisioning/render.go`):

```go
// NeedsRender reports whether cfg's packages/flakes require a
// per-instance wrapper flake, rather than using the template ref
// as-is.
func NeedsRender(cfg config.Config) bool

// Render produces the per-instance wrapper flake.nix content for cfg,
// importing templateRef's homeManagerModules.<name> as the template
// module, plus a synthetic module for cfg.Packages and one per
// cfg.Flakes[] entry (its packages, and its homeManagerModules.default
// if Modules is true).
func Render(cfg config.Config, templateRef string) (string, error)
```

Illustrative shape of the rendered output (exact syntax settled during
implementation):

```nix
{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    home-manager.url = "github:nix-community/home-manager";
    template.url = "github:jskswamy/cloudlab?dir=templates";
    flake0.url = "github:someorg/custom-tool";
  };
  outputs = { self, nixpkgs, home-manager, template, flake0, ... }: {
    homeConfigurations."python" = home-manager.lib.homeManagerConfiguration {
      pkgs = nixpkgs.legacyPackages."x86_64-linux";
      modules = [
        template.homeManagerModules."python"
        { home.packages = [ pkgs.ripgrep pkgs.jq ]; }
        { home.packages = [ flake0.packages."x86_64-linux".cli ]; }
        flake0.homeManagerModules.default
      ];
    };
  };
}
```

**`Validate`** (`internal/provisioning/validate.go`):

```go
// Validate checks that cfg's template and flakes[] resolve to real,
// evaluable flake outputs, without building or switching anything.
// Returns an error naming every problem found, not just the first.
func Validate(ctx context.Context, cfg config.Config) error
```

## Data Flow

1. `CloudInitUserData` is a static constant — it has no runtime data
   flow of its own. A later sub-project's VM-creation step passes it
   as-is to `provider.InstanceSpec.UserData`; it never varies per
   template, per config, or per instance.
2. A caller (a later sub-project — `up`, `provision`, or `shell`)
   resolves the project's `cloudlab.pkl` via `config.Resolve` (already
   shipped) and has a `config.Config`.
3. `ResolveTemplateRef(cfg.Template)` produces the flake ref to use.
4. `NeedsRender(cfg)` decides whether a wrapper flake is needed.
5. If needed, `Render(cfg, templateRef)` produces the wrapper's
   `flake.nix` content as a string.
6. Out of scope here: shipping that content to a VM (SSH), and
   actually running `home-manager switch` against it — that's
   `Reconcile`, part of the later sub-project (see Out of Scope).
7. Separately, `Validate(ctx, cfg)` can run at any time (e.g. in CI, or
   before an `up`) to catch a bad template/flake reference without
   touching a live instance or spending provider credentials.

## Error Handling

- `Validate` collects every problem (bad template ref, missing flake
  package, missing flake module) into one error, each line naming what
  was checked and Nix's own specific reason — never a bare Nix stack
  trace, never just the first problem found.
- `Render`/`NeedsRender` operate on an already-validated `config.Config`
  — they don't re-validate; a caller that wants pre-flight checking
  calls `Validate` itself, separately.
- Nothing in this package depends on provider credentials or a live
  instance. `Validate` only shells out to `nix eval` — its network
  dependency is the nixpkgs binary cache and GitHub (for the template
  and any `flakes[].url` referenced), the same public infrastructure
  `nix develop` already needs locally. Unlike `internal/config`, this
  package never touches Pkl or `pkg.pkl-lang.org` — `Validate` and
  `Render` both operate on an already-resolved `config.Config`, not on
  raw `cloudlab.pkl` files.

## Testing

- `ResolveTemplateRef`: built-in names expand correctly; an arbitrary
  string (already a ref) passes through unchanged.
- `NixSystem`: `"x86_64"`/unset → `"x86_64-linux"`; `"arm64"` →
  `"aarch64-linux"`.
- `NeedsRender`: empty packages+flakes → `false`; either non-empty →
  `true`.
- `Render`: covers template-only, template+packages, and
  template+flakes(with and without `modules: true`) — asserting on the
  generated Nix syntax's structure, not just that it's non-empty.
- `Validate`: against small real test fixture flakes under `testdata/`
  (not mocked — matches this codebase's existing rule of testing
  against the real `nix` binary, never a fake, the same way
  `internal/config`'s tests use the real `pkl` binary), covering: a
  fully valid config (no errors); a template ref that doesn't expose
  the needed attribute; a `flakes[]` entry missing a named package; a
  `flakes[]` entry with `modules: true` where the flake has no
  `homeManagerModules.default`. Errors are asserted for both content
  (names the right template/flake/package) and the absence of raw Nix
  stack-trace noise.
- Real `python`/`docker` template content: a smoke test that
  `templates/flake.nix`'s `homeConfigurations.python` and
  `.docker` actually evaluate (`nix eval`), proving the checked-in
  templates aren't just structurally plausible but genuinely valid.

## Out of scope (deferred to later sub-projects)

- **Shipping and switching**: the `provision` command, `Reconcile`
  (resolve config → decide render → ship over SSH via stdin-piped
  `cat > path`, not the SCP protocol → run `home-manager switch`), and
  wiring `up`/`shell`/`provision` to call it. Already decided
  (`cloudlab-998`, `cloudlab-g42`) but not yet implemented.
- **Progress reporting** during `Reconcile` — phase-line + streamed
  output now, a `bubbletea`+`viewport` collapsible pane later. Already
  decided and tracked as an epic (`cloudlab-b90`).
- **The interactive preset wizard** (`cloudlab init` + `up`'s automatic
  fallback when no `cloudlab.pkl` exists) — its own sub-project,
  comparable in scope to Foundation/Provider layer, needing its own
  design pass before implementation. Already decided and tracked as an
  epic (`cloudlab-pwg`).
- **The full `python`/`docker` package lists** beyond what's needed to
  prove the mechanism works — the exact toolchain contents (uv version,
  Jupyter setup, Docker/minikube specifics) are an implementation
  detail settled task-by-task, not a design decision.
- **Provider-side validation** (checking `region`/`size` against live
  DigitalOcean data) — a different tier of check than `Validate`
  provides here, needing real credentials and network access to a
  specific provider; not "syntax and lint."
