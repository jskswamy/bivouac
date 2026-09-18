# Curated Home-Manager Modules and Settings Overrides Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `bivouac.pkl`/`base.pkl` name specific home-manager module paths to pull from an external flake (not just the everything-catch-all `.default`), and override any home-manager option in the final merged config on a per-project basis.

**Architecture:** Two independent schema additions to `Config.pkl`. `Flake.modules` changes from `Boolean` to `Listing<String>` (dotted module paths) — this is a type change the Go compiler forces to land atomically across `render.go`, `validate.go`, and `write.go` in one task. A new top-level `settings: Mapping<String, String|Boolean|Int|Listing<String>>` field layers option overrides onto the final config, rendered as one more synthetic home-manager module, merged key-wise (project overrides base) rather than additively.

**Tech Stack:** Go 1.26, Pkl 0.32.1 + pkl-go 0.14.0 codegen, Nix flakes + home-manager, git-hooks.nix pre-commit.

**Spec:** [`docs/superpowers/specs/2026-09-18-flake-modules-and-settings-design.md`](../specs/2026-09-18-flake-modules-and-settings-design.md)

## Global Constraints

- **`go` is not on `$PATH` outside `nix develop`.** Run every Go/Pkl/lint/nix command as `nix develop --command <cmd>`.
- **Check formatting with `gofmt -l $(git ls-files '*.go')`, never `gofmt -l .`** — the bare-dot form walks the gitignored `.gocache/gopath/pkg/mod/` module cache and always reports third-party files as unformatted.
- **Regenerating `internal/config`'s bindings requires `go generate ./internal/config/...`** (see `internal/config/generate.go`'s directive) after every `Config.pkl` edit. Never hand-edit a `*.pkl.go` file — it's marked `DO NOT EDIT` and regeneration will silently discard hand edits.
- **`Flake.modules`'s type change is one atomic unit.** Go won't compile with a mismatched field type, so Task 1 touches `Config.pkl`, its generated bindings, `render.go`, `validate.go`, `write.go`, and every test asserting the old `Boolean` shape, all in one task — there is no way to land it more incrementally without an intermediate broken-build state.
- **`nix-modules`' own tools already expose `enable`, via `lib.mkDefault` internally** (confirmed in the design doc from that repo's own README: "drop it again with `tools.<name>.enable = false`") — this is why `settings["tools.tig.enable"] = false` works without a NixOS-module-system conflict: a plain assignment from `settings` overrides a `mkDefault`, and never collides with one, at the option-system level.
- **pkl-go decodes `Mapping<String, String|Boolean|Int|Listing<String>>` as `map[string]any`, with element runtime types `string`, `bool`, `int`, and `[]interface{}` — NOT `[]string`.** Verified via a throwaway codegen experiment during design; every list element inside a settings value is itself boxed in `any` and needs its own type assertion.
- **This repo's own convention for `nix eval`-backed tests is real evaluation, not mocks** (see `internal/provisioning/testdata/*`) — `Validate`'s package/module-existence tests run genuine `nix eval` against tiny fixture flakes checked into `testdata/`, and `TestRender_Output_EvaluatesInNix` genuinely builds the rendered flake. Follow this pattern for new tests rather than introducing a fake evaluator.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/config/Config.pkl` | Schema: `Flake.modules` type change, new `settings` field |
| `internal/config/*.pkl.go` | Regenerated bindings (never hand-edited) |
| `internal/config/config.go` | `mergeConfig`'s new `mergeSettings` key-wise merge |
| `internal/provisioning/render.go` | `nixPath`/`nixValue` helpers, template rendering for `modules` and `settings`, `NeedsRender`'s new settings-triggers-render case |
| `internal/provisioning/validate.go` | Per-entry existence checks for `flakes[].modules` |
| `internal/config/write.go` | `renderFlakes`'s `modules` output, new `renderSettings` for the wizard/preset pipeline |
| `internal/config/read.go` | `fieldValue`'s settings case, `fieldDefaults`/`IsDefault`'s map-shaped default |
| `docs/architecture.md`, `docs/config.md` | User-facing schema documentation |
| `internal/provisioning/testdata/good-flake/flake.nix` | Gains a nested `homeManagerModules.tools.git` for named-module test coverage |

---

### Task 1: `Flake.modules` — `Boolean` → `Listing<String>`, end to end

**Files:**
- Modify: `internal/config/Config.pkl:58`
- Modify: `internal/config/Flake.pkl.go` (regenerated, not hand-edited)
- Modify: `internal/provisioning/render.go:34-58` (template), `:60-74` (renderData), `:135-159` (Render), `:161-213` (validation helpers)
- Modify: `internal/provisioning/validate.go:88-98`
- Modify: `internal/config/write.go:185-200` (`renderFlakes`)
- Modify: `internal/config/config_test.go:358-388`
- Modify: `internal/config/write_test.go` (`TestRender_Flakes`)
- Modify: `internal/provisioning/render_test.go` (`TestRender_WithFlakeModules_ImportsFlakeInputAndModule`, `TestRender_FlakeWithoutModules_OmitsModuleReference`, `TestNeedsRender_NonEmptyFlakes_True`)
- Modify: `internal/provisioning/validate_test.go` (`TestValidate_GoodConfig_NoError`, `TestValidate_FlakeMissingModules_NamesItClearly`)
- Modify: `internal/provisioning/testdata/good-flake/flake.nix`
- Test: same files as above (this repo colocates tests with source)

**Interfaces:**
- Produces: `func nixPath(dotted string) string` in `internal/provisioning/render.go` — turns `"tools.git"` into `."tools"."git"`, `"git-tools"` into `."git-tools"`. Task 3 reuses this for `settings` keys.
- Produces: `Flake.Modules []string` (was `bool`) — every later task and any caller outside this plan that reads `Flake.Modules` must treat it as a slice.

- [ ] **Step 1: Change the schema**

Edit `internal/config/Config.pkl:58`:

```pkl
class Flake {
  url: String
  packages: Listing<String>
  modules: Listing<String> = new Listing {}
}
```

- [ ] **Step 2: Regenerate bindings**

Run: `nix develop --command go generate ./internal/config/...`

- [ ] **Step 3: Confirm the break**

Run: `nix develop --command go build ./...`
Expected: FAIL. `internal/provisioning/render.go` fails with something like `f.Modules (variable of type []string) used as value of type bool` at the `{{if $f.Modules}}` template execution point (this surfaces at template *execution*, not compile time, since Go templates aren't statically typed against the data — so the actual compile failure is in `internal/config/write.go:195`'s `fmt.Fprintf(b, "    modules = %t\n", f.Modules)`, where `%t` requires a `bool` and now gets `[]string`; `go vet` catches this as a build-blocking vet error under `go build`). Confirm the exact failure by running the command — do not assume which line fails first.

- [ ] **Step 4: Fix `write.go`'s `renderFlakes`**

Edit `internal/config/write.go:194-196`, replacing the boolean line with the same `renderStringListing` helper `packages` already uses on the line above it:

```go
		renderStringListing(b, "    ", "packages", f.Packages)
		renderStringListing(b, "    ", "modules", f.Modules)
```

Delete the old comment above `renderFlakes` that explains why `modules` was written even when false (`internal/config/write.go:191-194`, the "modules is written even when false..." paragraph) — that reasoning was specific to a `Boolean` with a schema default; `renderStringListing` already handles the empty-listing case identically to how `packages` does, so there is nothing special left to explain.

- [ ] **Step 5: Add `nixPath` and update `render.go`'s template**

Add this function to `internal/provisioning/render.go`, near `validateNixIdent` (around line 175):

```go
// nixPath turns a dot-separated string into Nix attribute-access syntax
// with every segment quoted. Nix source requires this: an unquoted
// hyphenated segment (`a.git-tools`) parses as subtraction, not nested
// attribute access, so every segment is quoted unconditionally rather
// than only when it would otherwise be ambiguous.
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

Register it in the template's FuncMap and update the template string. Change `internal/provisioning/render.go:34`:

```go
var renderTmpl = template.Must(template.New("flake").Funcs(template.FuncMap{
	"nixPath": nixPath,
}).Parse(`{
```

Change lines 53-54 (the `{{if $f.Modules}}` block) to a range:

```
{{end}}{{range $f.Modules}}        flake{{$i}}.homeManagerModules{{nixPath .}}
{{end}}{{end}}      ];
```

(This replaces exactly the two lines `{{if $f.Modules}}        flake{{$i}}.homeManagerModules.default` and `{{end}}{{end}}      ];` — the surrounding `{{range $i, $f := .Flakes}}{{if $f.Packages}}...{{end}}` on line 52 is unchanged.)

- [ ] **Step 6: Update `validateRenderData`**

`validNixIdent`'s charset (`^[A-Za-z0-9_+.-]+$`, `internal/provisioning/render.go:165`) already permits `.`, so a dotted module path validates as one string with no splitting needed — only `nixPath` (rendering) needs per-segment awareness, not validation. Add a loop in `validateRenderData` (`internal/provisioning/render.go`, inside the `for _, f := range data.Flakes` loop that already validates `f.Packages`, around line 206-210):

```go
		for _, m := range f.Modules {
			if err := validateNixIdent("flake module", m); err != nil {
				return err
			}
		}
```

- [ ] **Step 7: Fix `validate.go`'s existence check**

Replace `internal/provisioning/validate.go:94-98`:

```go
		for _, m := range f.Modules {
			if err := evalExists(ctx, r, f.Url, "homeManagerModules."+m); err != nil {
				problems = append(problems, fmt.Sprintf("flake %q module %q: %v", f.Url, m, err))
			}
		}
```

Note: this builds `"homeManagerModules." + m` as a **CLI attrpath argument** to `nix eval`, unquoted — do not use `nixPath` here. `nix eval url#homeManagerModules.tools.git` resolves nested/hyphenated attrs correctly as a command-line installable string; `nixPath`'s per-segment quoting is only needed for generated Nix *source*, a different syntax context with different rules (unquoted `a.b-c` in source parses as subtraction; the CLI's attrpath parser has no such ambiguity).

- [ ] **Step 8: Add a nested module to the `good-flake` test fixture**

Edit `internal/provisioning/testdata/good-flake/flake.nix`:

```nix
{
  outputs = { self, nixpkgs }: {
    packages.x86_64-linux.cli = nixpkgs.legacyPackages.x86_64-linux.hello;
    homeManagerModules.default = { pkgs, ... }: { home.packages = [ pkgs.hello ]; };
    homeManagerModules.tools.git = { pkgs, ... }: { home.packages = [ pkgs.hello ]; };
  };
}
```

- [ ] **Step 9: Update existing tests to the new shape**

`internal/config/config_test.go:358-388` — rename and rewrite the two tests:

```go
func TestLoad_FlakeModulesEmpty(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, project, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`flakes {`,
		`  new Flake {`,
		`    url = "github:someorg/custom-tool"`,
		`    packages { "cli" }`,
		`  }`,
		`}`,
	}, "\n")+"\n")

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(cfg.Flakes) != 1 {
		t.Fatalf("Flakes = %v, want 1 entry", cfg.Flakes)
	}
	if len(cfg.Flakes[0].Modules) != 0 {
		t.Errorf("Flakes[0].Modules = %v, want empty", cfg.Flakes[0].Modules)
	}
}

func TestLoad_FlakeModulesList(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, project, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`flakes {`,
		`  new Flake {`,
		`    url = "github:someorg/custom-tool"`,
		`    packages { "cli" }`,
		`    modules { "tools.git" }`,
		`  }`,
		`}`,
	}, "\n")+"\n")

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(cfg.Flakes) != 1 || len(cfg.Flakes[0].Modules) != 1 || cfg.Flakes[0].Modules[0] != "tools.git" {
		t.Errorf("Flakes = %v, want one entry with Modules = [tools.git]", cfg.Flakes)
	}
}
```

`internal/config/write_test.go`'s `TestRender_Flakes` — replace the `Modules: true`/`Modules: false` (bool) construction and the expected output:

```go
func TestRender_Flakes(t *testing.T) {
	got, err := Render(Values{"flakes": []Flake{
		{Url: "github:nix-community/fenix", Packages: []string{"default"}, Modules: []string{"tools.git"}},
		{Url: "github:foo/bar", Packages: []string{}},
	}})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	want := strings.Join([]string{
		`flakes {`,
		`  new Flake {`,
		`    url = "github:nix-community/fenix"`,
		`    packages {`,
		`      "default"`,
		`    }`,
		`    modules {`,
		`      "tools.git"`,
		`    }`,
		`  }`,
		`  new Flake {`,
		`    url = "github:foo/bar"`,
		`    packages {}`,
		`    modules {}`,
		`  }`,
		`}`,
		``,
	}, "\n")
	if got != want {
		t.Errorf("Render() =\n%s\nwant\n%s", got, want)
	}
}
```

`internal/provisioning/render_test.go` — replace `TestRender_WithFlakeModules_ImportsFlakeInputAndModule` and `TestRender_FlakeWithoutModules_OmitsModuleReference`:

```go
func TestRender_WithFlakeModules_ImportsFlakeInputAndModule(t *testing.T) {
	cfg := config.Config{
		Arch: "x86_64",
		Flakes: []config.Flake{
			{Url: "github:someorg/custom-tool", Packages: []string{"cli"}, Modules: []string{"tools.git", "git-tools"}},
		},
	}
	out, err := Render(cfg, "github:jskswamy/bivouac?dir=templates#python-x86_64-linux")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(out, `flake0.url = "github:someorg/custom-tool"`) {
		t.Errorf("output does not declare the flake input:\n%s", out)
	}
	if !strings.Contains(out, `flake0.packages."x86_64-linux"."cli"`) {
		t.Errorf("output does not reference the flake's package:\n%s", out)
	}
	if !strings.Contains(out, `flake0.homeManagerModules."tools"."git"`) {
		t.Errorf("output does not reference the dotted module path:\n%s", out)
	}
	if !strings.Contains(out, `flake0.homeManagerModules."git-tools"`) {
		t.Errorf("output does not reference the flat module name:\n%s", out)
	}
}

func TestRender_FlakeWithoutModules_OmitsModuleReference(t *testing.T) {
	cfg := config.Config{
		Arch: "x86_64",
		Flakes: []config.Flake{
			{Url: "github:someorg/custom-tool", Packages: []string{"cli"}},
		},
	}
	out, err := Render(cfg, "github:jskswamy/bivouac?dir=templates#python-x86_64-linux")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if strings.Contains(out, "homeManagerModules") {
		t.Errorf("output references homeManagerModules with no modules configured:\n%s", out)
	}
}
```

Every other `render_test.go` case constructing `config.Flake{...}` without a `Modules` field is unaffected — Go's zero value for `[]string` is `nil`, which ranges to nothing, matching the old `Modules: false` (default) behavior exactly. Grep to confirm none of them set `Modules: true` or `Modules: false` explicitly: `grep -n "Modules:" internal/provisioning/render_test.go` — fix any remaining hit the same way as above.

`internal/provisioning/validate_test.go` — update the two tests using `Modules: true`:

```go
func TestValidate_GoodConfig_NoError(t *testing.T) {
	tmpl := validTemplateFor(t)
	cfg := config.Resolved{
		Template: tmpl,
		Config: config.Config{
			Arch: "x86_64",
			Flakes: []config.Flake{
				{Url: testdataFlake(t, "good-flake"), Packages: []string{"cli"}, Modules: []string{"default", "tools.git"}},
			},
		},
	}
	if err := Validate(context.Background(), LocalRunner{}, cfg); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
}

func TestValidate_FlakeMissingModules_NamesItClearly(t *testing.T) {
	tmpl := validTemplateFor(t)
	cfg := config.Resolved{
		Template: tmpl,
		Config: config.Config{
			Arch: "x86_64",
			Flakes: []config.Flake{
				{Url: testdataFlake(t, "no-modules-flake"), Packages: []string{"cli"}, Modules: []string{"default"}},
			},
		},
	}
	err := Validate(context.Background(), LocalRunner{}, cfg)
	if err == nil {
		t.Fatal("Validate() error = nil, want error naming the missing module")
	}
	if !strings.Contains(err.Error(), "homeManagerModules") {
		t.Errorf("error %q does not mention homeManagerModules", err.Error())
	}
}
```

Add a new test proving a *named* (not just default) missing module is caught, right after `TestValidate_FlakeMissingModules_NamesItClearly`:

```go
func TestValidate_FlakeMissingNamedModule_NamesItClearly(t *testing.T) {
	tmpl := validTemplateFor(t)
	cfg := config.Resolved{
		Template: tmpl,
		Config: config.Config{
			Arch: "x86_64",
			Flakes: []config.Flake{
				{Url: testdataFlake(t, "good-flake"), Packages: []string{"cli"}, Modules: []string{"tools.nonexistent"}},
			},
		},
	}
	err := Validate(context.Background(), LocalRunner{}, cfg)
	if err == nil {
		t.Fatal("Validate() error = nil, want error naming the missing module")
	}
	if !strings.Contains(err.Error(), "tools.nonexistent") {
		t.Errorf("error %q does not name the missing module path", err.Error())
	}
}
```

- [ ] **Step 10: Run the affected package tests**

Run: `nix develop --command go test ./internal/config/... ./internal/provisioning/...`
Expected: PASS. `TestValidate_GoodConfig_NoError` and `TestValidate_FlakeMissingNamedModule_NamesItClearly` genuinely invoke `nix eval` (via `LocalRunner{}`) against the updated `testdata/good-flake` fixture — this needs `nix` on `PATH`, i.e. run inside `nix develop`.

- [ ] **Step 11: Full build and format check**

Run: `nix develop --command go build ./...` → exit 0.
Run: `nix develop --command gofmt -l $(git ls-files '*.go')` → empty output.

- [ ] **Step 12: Commit**

```bash
nix develop --command git add internal/config/Config.pkl internal/config/Flake.pkl.go \
  internal/config/config_test.go internal/config/write.go internal/config/write_test.go \
  internal/provisioning/render.go internal/provisioning/render_test.go \
  internal/provisioning/validate.go internal/provisioning/validate_test.go \
  internal/provisioning/testdata/good-flake/flake.nix
git commit -m "Let flakes[].modules name specific module paths"
```

**Done when:** `flakes[].modules` is a `Listing<String>` end to end — schema, bindings, rendering (with per-segment-quoted Nix source), validation (via unquoted CLI attrpaths), and the wizard/preset writer all agree, and every test above passes.

---

### Task 2: Schema and merge — top-level `settings` field

**Files:**
- Modify: `internal/config/Config.pkl` (add `settings` field)
- Modify: `internal/config/Config.pkl.go` (regenerated)
- Modify: `internal/config/config.go:118-132` (`mergeConfig`)
- Test: `internal/config/config_test.go` (new merge tests)

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces: `Config.Settings map[string]any` (generated field). `mergeSettings(base, project map[string]any) map[string]any` — Task 3 and Task 4 read `cfg.Settings` (the *merged* result, via `Config.Settings` on a `Resolved`/`Config` value), not this function directly.

- [ ] **Step 1: Add the field to the schema**

Edit `internal/config/Config.pkl`, adding after the `flakes` field (and after the `Flake` class, or before it — Pkl doesn't require declaration order between a field and a class it references, but keep it visually grouped with the other top-level fields, above the `class Flake { ... }` block):

```pkl
/// Overrides layered onto the final merged home-manager configuration,
/// keyed by a dot-separated path (e.g. "programs.git.userEmail",
/// "tools.tig.enable"). Not scoped to any flake or module: the option
/// being overridden may belong to the template, to the shared common
/// module, or to a flakes[].modules import.
///
/// Merges by key, not additively like the Listing fields above: the
/// project's value replaces the base's for a matching key, and adds
/// any key the base didn't have.
settings: Mapping<String, String|Boolean|Int|Listing<String>> = new Mapping {}
```

- [ ] **Step 2: Regenerate bindings**

Run: `nix develop --command go generate ./internal/config/...`

Confirm the generated `internal/config/Config.pkl.go` gained `Settings map[string]any` \`pkl:"settings"\`` on the `Config` struct.

- [ ] **Step 3: Write the failing merge tests**

Add to `internal/config/config_test.go`:

```go
func TestMergeConfig_SettingsBaseOnly(t *testing.T) {
	base := Config{Settings: map[string]any{"a.b": "base-value"}}
	project := Config{}
	got := mergeConfig(base, project)
	if got.Settings["a.b"] != "base-value" {
		t.Errorf("Settings[a.b] = %v, want base-value", got.Settings["a.b"])
	}
}

func TestMergeConfig_SettingsProjectOnly(t *testing.T) {
	base := Config{}
	project := Config{Settings: map[string]any{"a.b": "project-value"}}
	got := mergeConfig(base, project)
	if got.Settings["a.b"] != "project-value" {
		t.Errorf("Settings[a.b] = %v, want project-value", got.Settings["a.b"])
	}
}

func TestMergeConfig_SettingsProjectOverridesBaseKey(t *testing.T) {
	base := Config{Settings: map[string]any{"a": "base-value"}}
	project := Config{Settings: map[string]any{"a": "project-value"}}
	got := mergeConfig(base, project)
	if got.Settings["a"] != "project-value" {
		t.Errorf("Settings[a] = %v, want project-value (project wins)", got.Settings["a"])
	}
}

func TestMergeConfig_SettingsProjectAddsNewKey(t *testing.T) {
	base := Config{Settings: map[string]any{"a": "base-value"}}
	project := Config{Settings: map[string]any{"b": "project-value"}}
	got := mergeConfig(base, project)
	if got.Settings["a"] != "base-value" {
		t.Errorf("Settings[a] = %v, want base-value (untouched)", got.Settings["a"])
	}
	if got.Settings["b"] != "project-value" {
		t.Errorf("Settings[b] = %v, want project-value (added)", got.Settings["b"])
	}
	if len(got.Settings) != 2 {
		t.Errorf("Settings = %v, want exactly 2 keys", got.Settings)
	}
}
```

- [ ] **Step 4: Run to confirm failure**

Run: `nix develop --command go test ./internal/config/... -run TestMergeConfig_Settings`
Expected: FAIL — `mergeConfig` doesn't populate `Settings` at all yet (the returned `Config{}` literal has no `Settings:` field), so `got.Settings` is `nil` and every index into it returns the zero value `nil`, not the expected strings.

- [ ] **Step 5: Implement the merge**

Add to `internal/config/config.go`, near `mergeStringSlicePtrs` (after it, around line 148):

```go
// mergeSettings layers project over base by key: the project's value
// replaces any matching base key and adds any new one. Unlike every
// Listing field above, this is not additive -- a settings value is a
// single override, not an accumulating list, so keeping both a base and
// a project value for the same key would mean picking one arbitrarily
// at render time instead of deciding it here, once.
func mergeSettings(base, project map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(project))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range project {
		merged[k] = v
	}
	return merged
}
```

Add `Settings: mergeSettings(base.Settings, project.Settings),` to the `Config{...}` literal `mergeConfig` returns (`internal/config/config.go:120-131`), as a new line alongside `Flakes: append(...)`.

- [ ] **Step 6: Run to confirm the tests pass**

Run: `nix develop --command go test ./internal/config/... -run TestMergeConfig_Settings`
Expected: PASS.

- [ ] **Step 7: Run the full config package suite**

Run: `nix develop --command go test ./internal/config/...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/config/Config.pkl internal/config/Config.pkl.go internal/config/config.go internal/config/config_test.go
git commit -m "Merge settings by key, project overriding base"
```

**Done when:** `Config.Settings` exists, resolves through `Resolve()`, and merges key-wise with project winning ties — the four table-test cases above all pass.

---

### Task 3: Render `settings` as a synthetic module

**Files:**
- Modify: `internal/provisioning/render.go` (`NeedsRender`, `renderTmpl`, `renderData`, `Render`, `validateRenderData`, new `nixValue`)
- Test: `internal/provisioning/render_test.go`

**Interfaces:**
- Consumes: `nixPath(dotted string) string` (Task 1), `config.Config.Settings map[string]any` (Task 2).
- Produces: `func nixValue(v any) (string, error)`.

- [ ] **Step 1: Write the failing `nixValue` tests**

Add to `internal/provisioning/render_test.go`:

```go
func TestNixValue_String(t *testing.T) {
	got, err := nixValue("work@example.com")
	if err != nil {
		t.Fatalf("nixValue() error = %v", err)
	}
	if got != `"work@example.com"` {
		t.Errorf("nixValue() = %q, want %q", got, `"work@example.com"`)
	}
}

func TestNixValue_Bool(t *testing.T) {
	got, err := nixValue(false)
	if err != nil {
		t.Fatalf("nixValue() error = %v", err)
	}
	if got != "false" {
		t.Errorf("nixValue() = %q, want %q", got, "false")
	}
}

func TestNixValue_Int(t *testing.T) {
	got, err := nixValue(42)
	if err != nil {
		t.Fatalf("nixValue() error = %v", err)
	}
	if got != "42" {
		t.Errorf("nixValue() = %q, want %q", got, "42")
	}
}

func TestNixValue_StringList(t *testing.T) {
	got, err := nixValue([]interface{}{"x", "y"})
	if err != nil {
		t.Fatalf("nixValue() error = %v", err)
	}
	if got != `[ "x" "y" ]` {
		t.Errorf("nixValue() = %q, want %q", got, `[ "x" "y" ]`)
	}
}

func TestNixValue_NonStringListElement_Rejected(t *testing.T) {
	_, err := nixValue([]interface{}{"x", 42})
	if err == nil {
		t.Fatal("nixValue() error = nil, want error for a non-string list element")
	}
}

func TestNixValue_UnsupportedType_Rejected(t *testing.T) {
	_, err := nixValue(3.14)
	if err == nil {
		t.Fatal("nixValue() error = nil, want error for an unsupported type")
	}
}

func TestNixValue_StringWithEmbeddedQuote_Rejected(t *testing.T) {
	_, err := nixValue(`work"; malicious = true; "`)
	if err == nil {
		t.Fatal("nixValue() error = nil, want error for a string breaking out of its Nix literal")
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `nix develop --command go test ./internal/provisioning/... -run TestNixValue`
Expected: FAIL — `nixValue` is undefined (compile error).

- [ ] **Step 3: Implement `nixValue`**

Add to `internal/provisioning/render.go`, after `nixPath` (Task 1's addition):

```go
// nixValue renders a settings value (pkl-go's decoding of a
// String|Boolean|Int|Listing<String> union) as a Nix literal. Listing<String>
// decodes as []interface{}, not []string -- every element is itself
// boxed in `any` -- so the list case type-asserts each element on its
// own rather than a single slice-wide assertion.
func nixValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		if err := validateNixString("settings value", x); err != nil {
			return "", err
		}
		return `"` + x + `"`, nil
	case bool:
		return strconv.FormatBool(x), nil
	case int:
		return strconv.Itoa(x), nil
	case []interface{}:
		parts := make([]string, len(x))
		for i, e := range x {
			s, ok := e.(string)
			if !ok {
				return "", fmt.Errorf("settings value: list element %v is not a string", e)
			}
			if err := validateNixString("settings value", s); err != nil {
				return "", err
			}
			parts[i] = `"` + s + `"`
		}
		return "[ " + strings.Join(parts, " ") + " ]", nil
	default:
		return "", fmt.Errorf("settings value %v: unsupported type %T", v, v)
	}
}
```

Add `"strconv"` to `internal/provisioning/render.go`'s import block.

- [ ] **Step 4: Run to confirm `nixValue` tests pass**

Run: `nix develop --command go test ./internal/provisioning/... -run TestNixValue`
Expected: PASS.

- [ ] **Step 5: Write the failing render/template test**

Add to `internal/provisioning/render_test.go`:

```go
func TestRender_WithSettings_AddsSyntheticModule(t *testing.T) {
	cfg := config.Config{
		Arch:     "x86_64",
		Settings: map[string]any{"programs.git.userEmail": "work@example.com"},
	}
	out, err := Render(cfg, "github:jskswamy/bivouac?dir=templates#python-x86_64-linux")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(out, `."programs"."git"."userEmail" = "work@example.com";`) {
		t.Errorf("output does not set the settings override:\n%s", out)
	}
}

func TestRender_NoSettings_OmitsSettingsModule(t *testing.T) {
	cfg := config.Config{Arch: "x86_64"}
	out, err := Render(cfg, "github:jskswamy/bivouac?dir=templates#python-x86_64-linux")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	// The only "= " lines this cfg's output should ever contain are the
	// two the base template always emits (bivouac.tailscale, and the
	// input url/system.= assignments already covered by other tests) --
	// a settings module would add a distinctive third `({ ... }: { ... =`
	// shape. Simplest robust check: no dotted-quoted-attr assignment
	// pattern appears at all.
	if strings.Contains(out, `." = "`) || strings.Contains(out, `.".`) {
		t.Errorf("output contains a settings-shaped assignment with no settings configured:\n%s", out)
	}
}

func TestNeedsRender_OnlySettings_True(t *testing.T) {
	cfg := config.Config{Settings: map[string]any{"a.b": "x"}}
	if !NeedsRender(cfg) {
		t.Error("NeedsRender() = false, want true for a config with only settings set")
	}
}
```

- [ ] **Step 6: Run to confirm failure**

Run: `nix develop --command go test ./internal/provisioning/... -run "TestRender_WithSettings|TestRender_NoSettings|TestNeedsRender_OnlySettings"`
Expected: FAIL. `TestRender_WithSettings_AddsSyntheticModule` fails because nothing renders `settings` yet; `TestNeedsRender_OnlySettings_True` fails because `NeedsRender` doesn't check `Settings`.

- [ ] **Step 7: Wire `Settings` into `NeedsRender`, `renderData`, and `Render`**

Edit `internal/provisioning/render.go:23-25`:

```go
func NeedsRender(cfg config.Config) bool {
	return len(cfg.Packages) > 0 || len(cfg.Flakes) > 0 || cfg.Tailscale || len(cfg.Agents) > 0 || len(cfg.Settings) > 0
}
```

Add a field to `renderData` (`internal/provisioning/render.go:60-74`):

```go
	Flakes   []config.Flake
	Settings map[string]any
```

Populate it in `Render` (`internal/provisioning/render.go:141-150`), adding `Settings: cfg.Settings,` to the `renderData{...}` literal.

- [ ] **Step 8: Add the settings module to the template**

Edit the template (`internal/provisioning/render.go:52-54`), inserting a new block right after the flakes range and before the closing `];`:

```
{{end}}{{if $f.Modules}}{{range $f.Modules}}        flake{{$i}}.homeManagerModules{{nixPath .}}
{{end}}{{end}}{{end}}{{if .Settings}}        ({ ... }: { {{range $k, $v := .Settings}}{{nixPath $k}} = {{nixValue $v}}; {{end}} })
{{end}}      ];
```

(Note: after Task 1, the modules line no longer has a `{{if $f.Modules}}` guard — it's a bare `{{range $f.Modules}}`, which already emits nothing for an empty slice. Re-check the exact surrounding template text before editing; the snippet above shows the *target* end state, not a literal diff against Task 1's intermediate version, since template whitespace control (`{{end}}` placement) is sensitive to exact adjacency.)

Add `nixValue` to the FuncMap alongside `nixPath` (`internal/provisioning/render.go`'s `template.New("flake").Funcs(template.FuncMap{...})`):

```go
var renderTmpl = template.Must(template.New("flake").Funcs(template.FuncMap{
	"nixPath":  nixPath,
	"nixValue": nixValue,
}).Parse(`{
```

- [ ] **Step 9: Validate settings keys before rendering**

Add to `validateRenderData` (`internal/provisioning/render.go`, after the `Flakes` loop, before its closing `return nil`):

```go
	for k := range data.Settings {
		if err := validateNixIdent("settings key", k); err != nil {
			return err
		}
	}
```

- [ ] **Step 10: Run to confirm the tests pass**

Run: `nix develop --command go test ./internal/provisioning/...`
Expected: PASS, full package.

- [ ] **Step 11: Format and build check**

Run: `nix develop --command gofmt -l $(git ls-files '*.go')` → empty.
Run: `nix develop --command go build ./...` → exit 0.

- [ ] **Step 12: Commit**

```bash
git add internal/provisioning/render.go internal/provisioning/render_test.go
git commit -m "Render settings as a synthetic home-manager module"
```

**Done when:** a config with only `settings` set (no packages/agents/flakes/tailscale) renders a wrapper flake at all, and that flake's module list includes a synthetic module setting every settings key to its Nix-literal value.

---

### Task 4: Wizard/preset round-trip — `settings` in `write.go`

**Files:**
- Modify: `internal/config/write.go:22-88` (Values doc comment, field constants, `fieldOrder`, `fieldKinds`)
- Modify: `internal/config/write.go` (new `renderSettings`, `renderField`'s dispatch)
- Modify: `internal/config/read.go:76-77` (`fieldValue`'s `FieldSettings` case), `:100-109` (`fieldDefaults`), `:118-131` (`IsDefault`)
- Test: `internal/config/write_test.go`, `internal/config/read_test.go` (`TestReadValues_RoundTripsRender`, `TestIsDefault`)

**Interfaces:**
- Consumes: nothing from Tasks 1-3 directly (this is the write-side counterpart; it doesn't call `nixPath`/`nixValue`, which are Nix-source-generation helpers, not Pkl-source-generation helpers — this task writes *Pkl*, not Nix).
- Produces: `Values{"settings": map[string]any{...}}` becomes a legal `Render` input.

- [ ] **Step 1: Write the failing render test**

Add to `internal/config/write_test.go`:

```go
func TestRender_Settings(t *testing.T) {
	got, err := Render(Values{"settings": map[string]any{
		"programs.git.userEmail": "work@example.com",
		"tools.tig.enable":       false,
		"retries":                3,
		"tags":                   []string{"a", "b"},
	}})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	// Map iteration order is not guaranteed, so check for each line's
	// presence rather than the whole block's exact text.
	for _, want := range []string{
		`settings {`,
		`  ["programs.git.userEmail"] = "work@example.com"`,
		`  ["tools.tig.enable"] = false`,
		`  ["retries"] = 3`,
		`  ["tags"] = new Listing { "a"; "b" }`,
		`}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render() missing line %q in:\n%s", want, got)
		}
	}
}

func TestRender_EmptySettings(t *testing.T) {
	got, err := Render(Values{"settings": map[string]any{}})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(got, "settings {}\n") {
		t.Errorf("Render() = %q, want an empty settings block", got)
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `nix develop --command go test ./internal/config/... -run TestRender_Settings`
Expected: FAIL — `"settings"` is not a known field yet, so `Render` returns the error `rendering config: "settings" is not a field of bivouac's schema`.

- [ ] **Step 3: Register the field**

Edit `internal/config/write.go`:

Add to the `const (...)` block (`internal/config/write.go:25-38`):

```go
	FieldSettings     = "settings"
```

Add to `fieldOrder` (`internal/config/write.go:44-58`), after `FieldFlakes` (matching `Config.pkl`'s declaration order from Task 2):

```go
	FieldFlakes,
	FieldSettings,
}
```

Add a new `fieldKind`:

```go
const (
	kindString fieldKind = iota
	kindBool
	kindStringList
	kindFlakeList
	kindSettingsMap
)
```

Add to `fieldKinds` (`internal/config/write.go:72-85`):

```go
	FieldSettings:     kindSettingsMap,
```

Update the `Values` doc comment (`internal/config/write.go:9-19`, the "Permitted value types are string, bool, []string and []Flake" line) to add the new shape:

```go
// Permitted value types are string, bool, int, []string, []Flake, and
// map[string]any (for settings, whose own values are string, bool, int,
// or []string).
```

- [ ] **Step 4: Implement `renderSettings` and wire it into `renderField`**

Add a case to `renderField`'s switch (`internal/config/write.go:130-155`):

```go
	case kindSettingsMap:
		settings, ok := value.(map[string]any)
		if !ok {
			return typeError(name, "a map of settings", value)
		}
		return renderSettings(b, name, settings)
```

Every other case in `renderField`'s switch falls through to the function's shared `return nil` at the end (`renderStringListing`/`renderFlakes` are void — they can't fail). This case is the first one whose body can itself fail, so it returns directly instead of falling through; that's a normal, valid early return out of a switch case and needs no other change to the function.

Add `renderSettings` near `renderStringListing`:

```go
// renderSettings writes `name { ["key"] = value ... }`, sorting keys so
// re-rendering the same map produces byte-identical output regardless
// of map iteration order.
func renderSettings(b *strings.Builder, name string, settings map[string]any) error {
	if len(settings) == 0 {
		fmt.Fprintf(b, "%s {}\n", name)
		return nil
	}
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Fprintf(b, "%s {\n", name)
	for _, k := range keys {
		v, err := pklValue(settings[k])
		if err != nil {
			return fmt.Errorf("settings[%q]: %w", k, err)
		}
		fmt.Fprintf(b, "  [%s] = %s\n", quote(k), v)
	}
	b.WriteString("}\n")
	return nil
}

// pklValue renders a settings value as a Pkl literal -- the write-side
// counterpart to internal/provisioning's nixValue, but simpler: callers
// build a Values map themselves (the wizard, a preset load), so a list
// here is a genuine []string, not pkl-go's boxed []interface{} decoding
// (that shape only appears when *reading* a Mapping back through the
// generated bindings, which this function never does).
func pklValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return quote(x), nil
	case bool:
		return fmt.Sprintf("%t", x), nil
	case int:
		return fmt.Sprintf("%d", x), nil
	case []string:
		if len(x) == 0 {
			return "new Listing {}", nil
		}
		quoted := make([]string, len(x))
		for i, s := range x {
			quoted[i] = quote(s)
		}
		return "new Listing { " + strings.Join(quoted, "; ") + " }", nil
	default:
		return "", fmt.Errorf("unsupported settings value type %T", v)
	}
}
```

`write.go` already imports `"sort"` (used by `sortedKeys`, `Render`'s own helper) — no import change needed for `renderSettings`'s `sort.Strings(keys)` call.

- [ ] **Step 5: Handle `fieldValue`'s read side**

`internal/config/read.go`'s `fieldValue` function (the one `ReadValues` calls per declared field name) needs a `FieldSettings` case too, or `ReadValues` will silently drop a declared `settings` block when reading a file back. Add to `fieldValue`'s switch, after the `FieldFlakes` case (`internal/config/read.go:76-77`):

```go
	case FieldSettings:
		return c.Settings, len(c.Settings) > 0
```

- [ ] **Step 6: Fix `IsDefault` for a map-shaped default**

`internal/config/read.go`'s `fieldDefaults` map (`internal/config/read.go:100-109`) and `IsDefault` (`internal/config/read.go:118-131`) both need a `map[string]any` case, or two problems occur: `TestReadValues_EmptyConfig_MatchesSchemaDefaults` (`internal/config/read_test.go:227-238`, which loops over every field from an empty config) will call `IsDefault(FieldSettings, map[string]any{})`, and `IsDefault`'s `default: return def == value` branch **panics at runtime** comparing two `map[string]any` values with `==` — maps are not comparable in Go, so this is not just a wrong answer, it's a crash the moment `FieldSettings` is registered.

Add to `fieldDefaults` (`internal/config/read.go:100-109`):

```go
	FieldSettings:     map[string]any{},
```

Add a case to `IsDefault`'s switch (`internal/config/read.go:122-130`), before the `default:` branch:

```go
	case map[string]any:
		v, isMap := value.(map[string]any)
		return isMap && len(v) == len(d)
```

- [ ] **Step 7: Add a table-test row**

Add to `TestIsDefault`'s `tests` slice in `internal/config/read_test.go` (`internal/config/read_test.go:241-260`), alongside the `FieldFlakes` rows:

```go
		{FieldSettings, map[string]any{}, true},
		{FieldSettings, map[string]any{"a.b": "x"}, false},
```

- [ ] **Step 8: Run to confirm write tests pass**

Run: `nix develop --command go test ./internal/config/... -run "TestRender_Settings|TestRender_EmptySettings"`
Expected: PASS.

- [ ] **Step 9: Extend the full round-trip test**

Edit `internal/config/read_test.go`'s `TestReadValues_RoundTripsRender` (the `want := Values{...}` literal), adding a `"settings"` key and updating the `"flakes"` entry's `Modules` field to match Task 1's new type:

```go
	want := Values{
		"region":    "nyc3",
		"size":      "s-2vcpu-4gb",
		"template":  "python",
		"arch":      "arm64",
		"tailscale": true,
		"beads":     "off",
		"sshKeys":   []string{"AAAA...fingerprint"},
		"packages":  []string{"ripgrep", "jq"},
		"agents":    []string{"claude"},
		"flakes":    []Flake{{Url: "github:foo/bar", Packages: []string{"default"}, Modules: []string{"default"}}},
		"settings":  map[string]any{"programs.git.userEmail": "work@example.com"},
	}
```

- [ ] **Step 10: Run the full round-trip test**

Run: `nix develop --command go test ./internal/config/... -run TestReadValues_RoundTripsRender`
Expected: PASS — this test performs a *real* `Render` → file write → `ReadValues` (real pkl evaluation) → `reflect.DeepEqual` round trip, so it proves the schema, the writer, and the reader all agree on the settings shape end to end.

- [ ] **Step 11: Run the full config package suite and build**

Run: `nix develop --command go test ./internal/config/...` → PASS.
Run: `nix develop --command go build ./...` → exit 0.
Run: `nix develop --command gofmt -l $(git ls-files '*.go')` → empty.

- [ ] **Step 12: Commit**

```bash
git add internal/config/write.go internal/config/write_test.go internal/config/read.go internal/config/read_test.go
git commit -m "Round-trip settings through the wizard/preset writer"
```

**Done when:** a `Values` map containing a `"settings"` entry renders as valid Pkl, and reading that file back with `ReadValues` reproduces the identical map — proven by `TestReadValues_RoundTripsRender`'s real evaluation round trip, not just a string-comparison test.

---

### Task 5: Documentation

**Files:**
- Modify: `docs/architecture.md:220-248` (the `modules = true` example and surrounding prose)
- Modify: `docs/config.md` (field reference)

**Interfaces:** None — this task changes no code.

- [ ] **Step 1: Read the current section**

Run: `sed -n '220,250p' docs/architecture.md` to see the exact current text before editing (this plan was written against the pre-Task-1 state; confirm line numbers still match after Tasks 1-4 land, since none of those tasks touch this file, but re-verify rather than assuming).

- [ ] **Step 2: Replace the `modules = true` example**

Replace the fenced Pkl example (currently ending in `modules = true`) with:

```pkl
agents { "claude" }

packages { "nodejs_22" }

flakes {
  new {
    url = "github:someorg/custom-tool"
    packages { "cli" }
    modules { "tools.git" }
  }
}

settings {
  ["programs.git.userEmail"] = "work@example.com"
}
```

- [ ] **Step 3: Update the surrounding prose**

Replace the sentence "each `flakes[]` entry contributes its packages, and its own `homeManagerModules.default` when `modules = true`" with something naming the new shape, e.g.: "each `flakes[]` entry contributes its packages, and one home-manager module per dotted path named in `modules` (a flat name like `git-tools` for a top-level group, a dotted one like `tools.git` for an individual tool) — and `settings` contributes one more synthetic module layering option overrides onto everything else, keyed by a dotted path into the final merged configuration rather than into any one flake's output."

Update the closing sentence ("`flakes[].modules` is implemented") similarly if it still reads as describing a boolean.

- [ ] **Step 4: Add a `settings` field entry to `docs/config.md`**

Find `docs/config.md`'s field reference (likely a table or list of `Config.pkl` fields — grep for `packages` or `flakes` to find the right section) and add a `settings` entry documenting: what it's for (per-project overrides of any home-manager option, not tied to a specific flake), the value union (`String|Boolean|Int|Listing<String>`), and the key-wise merge rule (project's value wins on a matching key, unlike every other listing field's pure concatenation).

- [ ] **Step 5: Verify no other stale references remain**

Run: `grep -rn "modules = true\|modules = false" docs/architecture.md docs/config.md README.md`
Expected: no matches.

- [ ] **Step 6: Commit**

```bash
git add docs/architecture.md docs/config.md
git commit -m "Document modules paths and settings overrides"
```

**Done when:** a reader of `docs/architecture.md` and `docs/config.md` alone (no design doc, no plan) can correctly write both a `modules` listing and a `settings` override.

---

### Task 6: Whole-tree verification

**Files:** None modified — this task only runs checks.

**Interfaces:** None.

- [ ] **Step 1: Build**

Run: `nix develop --command go build ./...`
Expected: exit 0.

- [ ] **Step 2: Full test suite**

Run: `nix develop --command go test ./...`
Expected: every package passes. **Known exception:** `internal/config`'s `TestLoadFromPath_MinimalFixture` can fail with a `pkg.pkl-lang.org` network timeout on a sandboxed/offline environment — this is a pre-existing environment flake unrelated to this work (confirmed earlier by checking it fails identically on `main` before any of this plan's changes). If it's the *only* failure and the error mentions `HTTP connect timed out`, that's expected; anything else is a real regression and must be fixed before continuing.

- [ ] **Step 3: Format check**

Run: `nix develop --command gofmt -l $(git ls-files '*.go')`
Expected: empty output. (Not `gofmt -l .` — that walks the gitignored module cache and always reports third-party files.)

- [ ] **Step 4: Confirm no stray debug output or TODOs**

Run: `grep -rn "TODO\|FIXME\|XXX" internal/config/write.go internal/config/config.go internal/provisioning/render.go internal/provisioning/validate.go`
Expected: no matches introduced by this plan (pre-existing hits elsewhere in the tree are out of scope).

- [ ] **Step 5: Commit if this task's own verification produced any fixups**

If any of the above steps required a fix, commit it separately with a message describing what verification caught. If everything was already green, this task produces no commit — that's a valid outcome, not a failure.

**Done when:** the whole tree builds, the full test suite is green (module network-flake exception noted above), and formatting is clean.
