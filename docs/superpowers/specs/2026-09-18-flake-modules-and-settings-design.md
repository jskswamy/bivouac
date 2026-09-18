# Curated home-manager modules and per-project option overrides

Status: designed
Date: 2026-09-18
Builds on: [ADR-0005](../../adr/0005-module-based-package-composition.md), which
anticipated `flakes[].modules` importing a flake's own home-manager config, not
just a package list, and is now superseded by this design.

## The problem

The user maintains a personal collection of home-manager modules at
[`jskswamy/nix-modules`](https://github.com/jskswamy/nix-modules) — real
config for `git`, `lazygit`, `tig`, `hunk`, `herdr`, `ccstatusline`, `fish`,
`starship`, `tmux`, and more, each its own module under `modules/tools/<name>`,
some bundled into groups (`git-tools`, `agent-tools`, ...). The goal: declare
this once in a personal `~/.config/bivouac/base.pkl` so every instance gets
the same setup, without hand-authoring Nix.

Two gaps stand in the way:

1. **`flakes[].modules` is a `Boolean`.** It can only pull a flake's
   `homeManagerModules.default` — every tool at once. `nix-modules` exposes
   named groups and individual tools specifically so a consumer can pick a
   subset; today's schema can't express "just these".

2. **No way to override anything.** `nix-modules`' own `git` module doesn't
   even declare `user.name`/`user.email` — its own doc comment calls them
   "per-person and per-employer, and belong in the consumer's own
   configuration layered on top of this." Without a way to layer that on
   top, a shared module can't be identity-bearing at all. The need
   generalizes past git: any project may want a different value for
   anything a shared module (or the template) set a default for.

## Design

### 1. `flakes[].modules` becomes `Listing<String>`

```pkl
class Flake {
  url: String
  packages: Listing<String>
  modules: Listing<String> = new Listing {}
}
```

Each entry names a path into that flake's `homeManagerModules`, dot-separated
for nesting:

```pkl
flakes {
  new {
    url = "github:jskswamy/nix-modules"
    modules {
      "tools.git"
      "tools.lazygit"
      "tools.tig"
      "tools.hunk"
      "tools.herdr"
      "tools.ccstatusline"
      "tools.fish"
      "tools.starship"
      "tools.tmux"
    }
  }
}
```

A flat name (`"git-tools"`) addresses a top-level group; a dotted one
(`"tools.git"`) addresses one individual tool. The old `modules = true`
becomes an explicit `modules { "default" }` — no magic boolean, no special
case: `"default"` is just an attribute name that happens to mean "everything"
in a flake that defines it that way, exactly like `nix-modules` does.

Breaking change to an existing field, deliberately: consistent with this
project's hard-cutover conventions elsewhere, and the field's only current
consumers are this repo's own tests, docs, and the `init` wizard.

### 2. A new top-level `settings` field for option overrides

```pkl
settings: Mapping<String, String|Boolean|Int|Listing<String>> = new Mapping {}
```

```pkl
settings {
  ["programs.git.userEmail"] = "work@example.com"
  ["programs.git.userName"] = "K Subramanian (Work)"
  ["tools.tig.enable"] = false
}
```

Each key is a dot-separated path into the final merged home-manager
configuration — not scoped to any one flake or module, since the option
being overridden might belong to the template, to `common.nix`, or to an
imported module. This is deliberately general: git identity is the
motivating example, not the scope. The same mechanism is also how a project
turns off one module from a base-declared set (`tools.tig.enable = false`)
without a separate exclusion mechanism — `nix-modules`' own tools already
expose `enable` for exactly this, wired through `lib.mkDefault` internally
(confirmed by its own README: "drop it again with `tools.<name>.enable =
false`"), so an ordinary override wins without a NixOS-module-system
conflict.

Value types are a closed union — `String`, `Boolean`, `Int`,
`Listing<String>` — covering identity strings, enable flags, and simple
lists, without trying to mirror Nix's full value model in Pkl. Verified via
a throwaway codegen experiment (`pkl-go` 0.14.0) that pkl-go decodes
`Mapping<String, String|Boolean|Int|Listing<String>>` as `map[string]any`,
with element runtime types `string`, `bool`, `int`, and `[]interface{}`
(**not** `[]string` — every element of the listing case is itself boxed in
`any`). The Nix-value serializer must type-switch accordingly.

### 3. Rendering

Both features need to turn a dotted string into safe Nix attribute syntax.
Nix's own attrset literals already support dotted-key sugar (`a.b.c = 1;` ≡
`a = { b = { c = 1; }; };`), but only when every segment is a valid bare
identifier — a hyphenated segment (`git-tools`) must be quoted
(`a."git-tools"`), and unquoted `a.b-c` would parse as subtraction. One
shared template helper handles both `modules` and `settings`:

```go
func nixPath(dotted string) string {
    var b strings.Builder
    for _, seg := range strings.Split(dotted, ".") {
        b.WriteString(`."`)
        b.WriteString(seg)
        b.WriteString(`"`)
    }
    return b.String()
}
```

`flakes[].modules` entries render as one line per entry:

```
flake{{$i}}.homeManagerModules{{nixPath .}}
```

replacing today's hardcoded `flake{{$i}}.homeManagerModules.default`.

`settings` renders as one more synthetic module, appended after everything
else in the module list:

```
({ ... }: { {{range $k, $v := .Settings}}{{nixPath $k}} = {{nixValue $v}}; {{end}} })
```

`nixValue` type-switches on the verified Go shapes:

```go
func nixValue(v any) (string, error) {
    switch x := v.(type) {
    case string:
        return quotedNixString(x), nil   // reuses validateNixString's charset check
    case bool:
        return strconv.FormatBool(x), nil
    case int:
        return strconv.Itoa(x), nil
    case []any:
        parts := make([]string, len(x))
        for i, e := range x {
            s, ok := e.(string)
            if !ok {
                return "", fmt.Errorf("settings value: list element %v is not a string", e)
            }
            parts[i] = quotedNixString(s)
        }
        return "[ " + strings.Join(parts, " ") + " ]", nil
    default:
        return "", fmt.Errorf("settings value %v: unsupported type %T", v, v)
    }
}
```

Both `modules` entries and `settings` keys get the same `validateNixIdent`-
style per-segment charset check `Packages`/`AgentPackages` already get,
applied per path segment rather than to the whole dotted string (a segment
is checked in isolation so a legitimate `.` separator is never mistaken for
part of an identifier that needs escaping).

### 4. Merge semantics

Every existing `Listing` field (`packages`, `agents`, `instructions`,
`flakes`) merges by concatenation: base's entries, then the project's.
`flakes[].modules` follows the same rule unchanged — it's still a `Listing`.

`settings` is a `Mapping`, and needs a **different** rule: key-wise override,
not concatenation. Base's key/value pairs apply first; the project's pairs
then replace any matching key and add any new one. This mirrors how scalar
fields (`region`, `size`, `template`) already resolve — project's value wins,
base's is the fallback — just applied per-key instead of to one field. This
is a new merge *pattern* in `config.Resolve()` (today's merge logic only
implements "take project's if set, else base's" for scalars and "concatenate"
for listings), not a reuse of either existing one.

Because the merge fully resolves in Go before rendering, there is exactly
one final value per settings key by the time a per-instance flake is
generated — never two competing plain assignments for the same option, so
there's no risk of a NixOS-module-system "conflicting definitions" error
from `settings` itself. A collision between a `settings` override and a
module's own **non-default** (not `mkDefault`) assignment of the same option
is still possible and is a real Nix build error — same as it would be for
any hand-written home-manager config today, not something this design tries
to prevent.

### 5. Validation

`internal/provisioning/validate.go` already checks `flakes[].packages`
entries exist via `nix eval --impure <url>#packages.<system>.<pkg>`.
`flakes[].modules` gets the equivalent check, one `evalExists` call per
entry: `homeManagerModules.<path>` — and here the *unquoted* dotted string
is exactly what `nix eval`'s CLI attrpath syntax expects
(`nix eval url#homeManagerModules.tools.git` resolves nested attrs on dots,
including hyphenated segments, without needing the quoting `nixPath` applies
for generated source: CLI installable syntax and Nix-language source syntax
parse attrpaths differently). So the render-time quoting helper and the
validation-time attrpath are two different strings built from the same
input, not one shared value.

`settings` gets **no existence validation**. Checking whether an arbitrary
dotted path is a real home-manager option would mean fully evaluating the
option tree (`options.<path>`), which is a materially heavier check than
existence-testing a `packages` or `homeManagerModules` attribute, and this
design doesn't add it. A typo'd path surfaces as an ordinary home-manager
build failure ("The option `...` does not exist") during `provision` — later
than the packages-existence checks catch things, but not silent.

## Testing

- `Flake.Modules` round-trips through Pkl read/write (`internal/config`),
  same shape as the existing `Listing<String>` tests for `packages`.
- `settings` merge: table tests over `config.Resolve()` covering
  base-only, project-only, override (project replaces a base key), and
  addition (project adds a new key) — the four cases a key-wise merge needs.
- `nixPath`: table tests covering a bare segment, a hyphenated segment, and
  a multi-segment dotted path.
- `nixValue`: table tests over all four Go shapes (`string`, `bool`, `int`,
  `[]any` of strings) plus the error case (an unsupported type, and a
  non-string list element).
- `render.go` template output: extend the existing `TestRender_*` suite with
  cases for a `modules` entry (plain name, dotted name) and a non-empty
  `settings` map, following the same "render then `nix eval`/parse the
  result" pattern `TestRender_Output_EvaluatesInNix` already uses.
- `validate.go`: a fake-runner test asserting `flakes[].modules` entries are
  checked via `homeManagerModules.<path>`, replacing the old
  hardcoded-`.default` assertion.

## Scope boundaries

- **`flakes` merge stays pure concatenation, not deduped by `url`.** If base
  and a project both declare a `Flake` entry for the same URL, the rendered
  flake gets two separate `flakeN` inputs pointing at the same URL — harmless
  (each independently exposes its own `packages`/`modules`) but redundant.
  Fixing this would be a real, separate design question (how do two
  `Flake` entries for the same URL combine their `packages`/`modules`
  listings?) and isn't needed to unblock this feature.
- **No generic "exclude a listing entry" mechanism for `packages` /
  `agents` / `instructions`.** `settings`'s `tools.<name>.enable = false`
  pattern solves exclusion for `flakes[].modules` specifically, because
  `nix-modules`' own tools already expose that hook. It does not generalize
  to excluding a `packages` entry (a plain nixpkgs package has no `enable`
  option to override) — out of scope here.

## Rejected alternatives

**Keep `modules` as `Boolean`, add a separate `String?` field for a named
module.** Rejected: two fields for one concept (which modules to pull) is
more surface area than one `Listing<String>`, and doesn't compose (can't ask
for two named modules without importing `.default` and eating everything).

**Model `settings` values as raw Nix expression strings** (e.g.
`["programs.git.userEmail"] = "\"work@example.com\""`, with the value
spliced into the template unquoted). Rejected: that's arbitrary Nix
authoring through the back door, exactly what `packages`/`agents`/`flakes`
were designed to keep out of `bivouac.pkl`. The closed value union keeps
`settings` declarative like everything else in the schema.

**Validate `settings` keys against the option tree before rendering.**
Rejected for now: the check is materially more expensive than the existing
package/module existence checks (needs a full option-tree evaluation, not
a single attribute-existence probe), and an unvalidated typo already fails
loudly at `provision` time. Worth reconsidering if silent/late failures on
`settings` turn out to be a real problem in practice.
