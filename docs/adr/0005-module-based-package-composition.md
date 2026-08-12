# 0005: Module-based package/flake composition

## Status

Accepted

## Context

Beyond the two built-in templates, users need to add packages per-repo —
the devbox (`devbox add`) UX was the explicit reference: a plain package
list, no Nix authoring required. The wrinkle devbox itself doesn't have:
these packages apply to a *remote* VM, not a local shell, so "add a
package" can't just mean "edit a file, it appears" — something has to
re-trigger `home-manager switch` on an already-running instance (see
[ADR-0007](0007-command-surface.md) for when that happens).

A further requirement surfaced during design: support pulling packages from
*other flakes*, not just nixpkgs — and, looking further ahead, support full
home-manager module composition (a flake contributing actual config, not
just a binary), without a rewrite of the composition mechanism when that's
needed.

## Decision

`cloudlab.yaml` (repo root, optional):

```yaml
packages:
  - nodejs_22                         # nixpkgs attribute name

flakes:
  - url: github:someorg/custom-tool
    packages: [cli]                   # attrs to pull from that flake's packages output
```

Internally, reconciliation is built around a **list of home-manager
modules**, not a flat `home.packages` list:

- The template's own profile is one module.
- The `packages:` list becomes one synthetic module
  (`{ home.packages = [ pkgs.nodejs_22 ]; }`).
- Each `flakes[]` entry becomes another synthetic module the same way,
  built from that flake's `packages` output.
- home-manager merges the module list, same as it would merge any set of
  home-manager modules.

v1 only implements the packages-only path (`packages:` and
`flakes[].packages`). Full module-level composition — a future
`flakes[].modules: [...]` importing a flake's own
`homeManagerModules.default` directly, i.e. arbitrary home-manager config,
not just a package list — is *not* implemented yet, but the module-list
pipeline is designed to accept it as just another module-producer function,
with no restructuring required.

## Consequences

- More machinery up front than "append to one package list" would need for
  v1 alone — a deliberate trade against the alternative (build the simple
  version now, refactor to module composition later when it's needed) which
  the design explicitly rejected: "otherwise we will end up with a lot of
  refactoring."
- Nix package/flake names throughout, not devbox's own package registry —
  home-manager already resolves nixpkgs attributes directly, so there's no
  reason to add a second package-search layer to match devbox's naming UX.
- A per-instance flake gets rendered (not just the static, build-time
  embedded one) whenever `flakes:` is non-empty, since `flakes[].url`
  entries are inputs unknown at binary-build time.
