# Composable Presets Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the scalar `template` field with a composable `presets` list, so an instance can be python *and* k8s, and so docker starts a daemon only where one was asked for.

**Architecture:** `presets` is a typed Pkl `Listing` of curated names. Each name maps to a home-manager module in `templates/flake.nix`'s `homeManagerModules`. `Render` emits one module line per preset onto `common`, which every instance gets. `template` keeps only its bring-your-own-flake-ref job.

**Tech Stack:** Go 1.x, Pkl (via `pkl-go` codegen), Nix flakes, home-manager.

**Spec:** `docs/superpowers/specs/2026-09-07-presets-and-connect-design.md`

## Global Constraints

- Preset names are exactly `python`, `docker`, `k8s`. The Pkl `Listing` type enforces this; no Go-side validation is added.
- `k8s.nix` imports `docker.nix`. Selecting `k8s` starts `dockerd`. This is a transitive dependency, not a convenience.
- `python.nix` keeps **both** `python312` and `uv`. `python312` is the base interpreter; `uv python install` supplies pinned versions.
- No deprecation path. `template = "docker"` becomes invalid outright — the project has no released users.
- Nix system strings are `x86_64-linux` and `aarch64-linux` only. `templates/flake.nix` builds no darwin systems.
- Go tests run via `nix develop --command go test ./...`. Never run bare `go test` — the toolchain comes from the flake.
- Commit style is classic: imperative subject, ≤50 chars, capitalised, no trailing period, no `type:` prefix. Never add an AI co-author trailer.

---

### Task 1: Add `presets` to the config schema

**Files:**
- Modify: `internal/config/Config.pkl`
- Modify: `internal/config/config.go:117-124` (the merge function)
- Regenerate: `internal/config/Config.pkl.go` (do not hand-edit — it says DO NOT EDIT)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `config.Config.Presets []string` with pkl tag `presets`. Merged additively, base entries before project entries, exactly like `Packages`/`Agents`/`Flakes`.

- [ ] **Step 1: Write the failing test**

Add to `internal/config/config_test.go`:

```go
func TestMerge_Presets_BaseBeforeProject(t *testing.T) {
	base := Config{Presets: []string{"docker"}}
	project := Config{Presets: []string{"python"}}

	got := merge(project, base)

	want := []string{"docker", "python"}
	if len(got.Presets) != len(want) {
		t.Fatalf("Presets = %v, want %v", got.Presets, want)
	}
	for i := range want {
		if got.Presets[i] != want[i] {
			t.Errorf("Presets[%d] = %q, want %q", i, got.Presets[i], want[i])
		}
	}
}
```

Check the existing merge test in this file for the exact name and argument
order of the merge function before running — if it is not `merge(project,
base)`, match whatever the neighbouring `Packages` test does.

- [ ] **Step 2: Run test to verify it fails**

Run: `nix develop --command go test ./internal/config/ -run TestMerge_Presets -v`
Expected: FAIL — `got.Presets` undefined (field does not exist).

- [ ] **Step 3: Add the field to the Pkl schema**

In `internal/config/Config.pkl`, after the `agents` block:

```pkl
/// Curated module sets to compose onto the baseline. Unlike `packages`,
/// a preset may carry a systemd unit or activation script, and encodes
/// which packages belong together -- `k8s` installs `kind` and
/// `kubectl` rather than `minikube`, and pulls in `docker`, which
/// `kind` needs to run nodes at all.
presets: Listing<"python"|"docker"|"k8s"> = new Listing {}
```

- [ ] **Step 4: Regenerate the Go bindings**

Run: `nix develop --command go generate ./internal/config/`

Verify `internal/config/Config.pkl.go` now contains `Presets []string` with
tag `pkl:"presets"`. If codegen fails on a cold cache it needs network once
to fetch `pkl.golang` — see `docs/config.md`.

- [ ] **Step 5: Merge presets additively**

In `internal/config/config.go`, in the merge function beside the existing
list fields:

```go
Presets:   append(append([]string{}, base.Presets...), project.Presets...),
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `nix develop --command go test ./internal/config/ -v`
Expected: PASS, including the pre-existing tests.

- [ ] **Step 7: Commit**

```bash
git add internal/config/
git commit -m "Add a presets field to the config schema"
```

---

### Task 2: Remove the built-in template names

**Files:**
- Modify: `internal/provisioning/template.go:11-30`
- Test: `internal/provisioning/template_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `ResolveTemplateRef(template, arch string) string` returns `template` unchanged for every input. `builtinTemplates` and `templateModuleName` are deleted.

`templateModuleName` exists only to strip the `-<system>` suffix that
`ResolveTemplateRef` appended for built-ins. With built-ins gone, nothing
appends it, so nothing needs to strip it.

- [ ] **Step 1: Write the failing test**

Replace the built-in expansion cases in `internal/provisioning/template_test.go`
with:

```go
func TestResolveTemplateRef_BuiltinNameIsNotExpanded(t *testing.T) {
	got := ResolveTemplateRef("docker", "x86_64")
	if got != "docker" {
		t.Errorf("ResolveTemplateRef(\"docker\") = %q, want %q unchanged — built-ins were removed in favour of presets", got, "docker")
	}
}

func TestResolveTemplateRef_FlakeRefPassesThrough(t *testing.T) {
	ref := "github:someorg/custom#thing-x86_64-linux"
	if got := ResolveTemplateRef(ref, "x86_64"); got != ref {
		t.Errorf("ResolveTemplateRef(%q) = %q, want it unchanged", ref, got)
	}
}
```

Delete any existing test asserting `"python"` or `"docker"` expands to a
`github:jskswamy/cloudlab?dir=templates#...` ref, and any test of
`templateModuleName`.

- [ ] **Step 2: Run test to verify it fails**

Run: `nix develop --command go test ./internal/provisioning/ -run TestResolveTemplateRef -v`
Expected: FAIL — `ResolveTemplateRef("docker")` returns the expanded
`github:...#docker-x86_64-linux`.

- [ ] **Step 3: Delete the expansion**

In `internal/provisioning/template.go`, delete the `builtinTemplates` map and
the `templateModuleName` function, and reduce `ResolveTemplateRef` to:

```go
// ResolveTemplateRef returns template unchanged. A template value is
// always a complete flake ref now: the built-in names ("python",
// "docker") became presets, which name home-manager modules directly
// rather than flake outputs, so there is nothing left to expand.
//
// Kept as a function rather than inlined at the call site because a
// bring-your-own ref is the one thing that may still need
// arch-dependent handling if per-system BYO outputs are ever supported.
func ResolveTemplateRef(template, _ string) string {
	return template
}
```

Keep `defaultTemplatesRef` — Task 3 uses it. Keep `NixSystem` and
`splitFlakeRef`; both are still used by `Render`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `nix develop --command go test ./internal/provisioning/ -v`
Expected: compile errors in `render.go` if `templateModuleName` is still
referenced there. Task 3 replaces that call; if the package will not build,
temporarily change `TemplateName: templateModuleName(name, cfg.Arch)` to
`TemplateName: name` to get a green build, and Task 3 will replace the line
properly.

- [ ] **Step 5: Commit**

```bash
git add internal/provisioning/template.go internal/provisioning/template_test.go
git commit -m "Remove the built-in template names"
```

---

### Task 3: Render one module line per preset

**Files:**
- Modify: `internal/provisioning/render.go:23-25` (`NeedsRender`), `:27-51` (`renderTmpl`), `:53-66` (`renderData`), `:128-151` (`Render`)
- Test: `internal/provisioning/render_test.go`

**Interfaces:**
- Consumes: `config.Config.Presets` from Task 1; `defaultTemplatesRef` from Task 2.
- Produces: a rendered flake whose `modules` list is
  `template.homeManagerModules."common"` followed by one
  `template.homeManagerModules."<preset>"` per entry, in config order.

Two behaviours change together and cannot be split: `NeedsRender` must return
true when presets are set, and `Render` must supply a `TemplateURL` when
`template` is unset. Without the first, presets render nothing; without the
second, an unset `template` renders a flake with an empty input URL.

- [ ] **Step 1: Write the failing tests**

Add to `internal/provisioning/render_test.go`:

```go
func TestNeedsRender_PresetsOnly_True(t *testing.T) {
	cfg := config.Config{Presets: []string{"docker"}}
	if !NeedsRender(cfg) {
		t.Error("NeedsRender() = false, want true when presets is non-empty — otherwise presets silently do nothing")
	}
}

func TestNeedsRender_NoTemplate_True(t *testing.T) {
	cfg := config.Config{}
	if !NeedsRender(cfg) {
		t.Error("NeedsRender() = false, want true when template is unset — common must still be composed")
	}
}

func TestRender_AlwaysImportsCommon(t *testing.T) {
	cfg := config.Config{Arch: "x86_64"}
	out, err := Render(cfg, "")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(out, `template.homeManagerModules."common"`) {
		t.Errorf("Render() did not import common:\n%s", out)
	}
}

func TestRender_EmitsOneModulePerPreset(t *testing.T) {
	cfg := config.Config{Arch: "x86_64", Presets: []string{"docker", "k8s"}}
	out, err := Render(cfg, "")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	for _, want := range []string{
		`template.homeManagerModules."common"`,
		`template.homeManagerModules."docker"`,
		`template.homeManagerModules."k8s"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Render() missing %s:\n%s", want, out)
		}
	}
}

func TestRender_NoTemplate_UsesDefaultTemplatesRef(t *testing.T) {
	cfg := config.Config{Arch: "x86_64", Presets: []string{"python"}}
	out, err := Render(cfg, "")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(out, `template.url = "`+defaultTemplatesRef+`"`) {
		t.Errorf("Render() did not default template.url to %q:\n%s", defaultTemplatesRef, out)
	}
}

func TestRender_ByoTemplateRef_UsesItsURL(t *testing.T) {
	cfg := config.Config{Arch: "x86_64"}
	out, err := Render(cfg, "github:someorg/custom#thing")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(out, `template.url = "github:someorg/custom"`) {
		t.Errorf("Render() did not use the bring-your-own URL:\n%s", out)
	}
}

func TestRender_RejectsPresetWithBadNixIdent(t *testing.T) {
	cfg := config.Config{Arch: "x86_64", Presets: []string{`bad"name`}}
	if _, err := Render(cfg, ""); err == nil {
		t.Error("Render() error = nil, want an error for a preset name that breaks out of the Nix string literal")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `nix develop --command go test ./internal/provisioning/ -run 'TestNeedsRender_Presets|TestNeedsRender_NoTemplate|TestRender_Always|TestRender_EmitsOne|TestRender_NoTemplate|TestRender_Byo|TestRender_RejectsPreset' -v`
Expected: FAIL — `Presets` not used by `NeedsRender`, and no `common` line in output.

- [ ] **Step 3: Extend `NeedsRender`**

```go
// Presets and an unset template both force a render: the preset module
// list exists only in the rendered flake, and with no bring-your-own
// ref there is no other flake to point home-manager at. Left out, a
// config whose only content is "presets { \"docker\" }" would render
// nothing and install nothing -- the same silent no-op the tailscale
// clause above exists to prevent.
func NeedsRender(cfg config.Config) bool {
	return len(cfg.Packages) > 0 || len(cfg.Flakes) > 0 || cfg.Tailscale ||
		len(cfg.Agents) > 0 || len(cfg.Presets) > 0 || cfg.Template == nil
}
```

- [ ] **Step 4: Emit the module lines**

In `renderTmpl`, replace the single template module line with:

```
      modules = [
        template.homeManagerModules."common"
{{range .Presets}}        template.homeManagerModules."{{.}}"
{{end}}        { cloudlab.tailscale = {{.Tailscale}}; }
```

Leave the rest of the `modules` list (packages, agents, flakes) unchanged.

In `renderData`, replace `TemplateName string` with:

```go
	// Presets are module names in the templates flake's
	// homeManagerModules, emitted in config order after common.
	Presets []string
```

In `Render`, replace the `TemplateName` assignment and default the URL:

```go
	url, _ := splitFlakeRef(templateRef)
	if url == "" {
		url = defaultTemplatesRef
	}
	for _, p := range cfg.Presets {
		if err := validateNixIdent("preset", p); err != nil {
			return "", err
		}
	}
```

and set `TemplateURL: url, Presets: cfg.Presets` in the `renderData` literal,
dropping `TemplateName`. Remove `TemplateName` from `validateRenderData` if
it is referenced there.

- [ ] **Step 5: Run tests to verify they pass**

Run: `nix develop --command go test ./internal/provisioning/ -v`
Expected: PASS. Pre-existing `Render` tests that asserted a
`homeManagerModules."python"` line from a `#python-x86_64-linux` ref will
fail — update them to pass `Presets` instead, since that is the new way to
select a module.

- [ ] **Step 6: Commit**

```bash
git add internal/provisioning/
git commit -m "Render one module line per preset"
```

---

### Task 4: Restructure the templates flake

**Files:**
- Modify: `templates/modules/common.nix:~30` (the two `getEnv` assignments)
- Modify: `templates/modules/docker.nix`, `templates/modules/python.nix`
- Create: `templates/modules/k8s.nix`
- Modify: `templates/flake.nix`

**Interfaces:**
- Consumes: the module names `Render` emits in Task 3 — `common`, `python`, `docker`, `k8s`.
- Produces: `homeManagerModules.{common,python,docker,k8s}`, plus a `checks.<system>.<name>` output building each preset's `activationPackage`.

`common.nix` needs `lib.mkDefault` on its identity fields before any of this
can be built in CI. Under `nix flake check`'s pure evaluation
`builtins.getEnv "USER"` returns `""`, and home-manager fails on an empty
`home.username`. `mkDefault` lets the checks supply a value without
disturbing the `--impure` path reconcile uses.

- [ ] **Step 1: Make common.nix's identity overridable**

In `templates/modules/common.nix`, change the two assignments:

```nix
  config.home.username = lib.mkDefault username;
  config.home.homeDirectory = lib.mkDefault (builtins.getEnv "HOME");
```

`lib` is already a module argument in that file.

- [ ] **Step 2: Stop the presets importing common, and fix their contents**

`templates/modules/python.nix` — drop the import, keep both packages:

```nix
{ pkgs, ... }:
{
  # python312 is the base interpreter, always present; `uv python
  # install` supplies whatever version a project actually pins.
  home.packages = [
    pkgs.python312
    pkgs.uv
  ];
}
```

`templates/modules/docker.nix` — drop the `imports` line and `pkgs.minikube`,
keeping `pkgs.docker`, the `dockerGroup` activation script and the
`systemd.user.services.docker` unit exactly as they are. Delete the comment
above `pkgs.minikube` explaining the kubectl collision; with minikube gone
there is no collision to explain.

Create `templates/modules/k8s.nix`:

```nix
{ pkgs, ... }:
{
  # kind runs Kubernetes nodes as docker containers, so k8s without a
  # docker daemon is broken rather than merely reduced -- a transitive
  # dependency, expressed the way the module system expresses them.
  # Importing is safe even when docker is also selected explicitly:
  # the module system deduplicates by path.
  imports = [ ./docker.nix ];

  # kubectl explicitly, which minikube's bundled bin/kubectl used to
  # make impossible (same path, different derivation, buildEnv
  # collision). kind ships none.
  home.packages = [
    pkgs.kind
    pkgs.kubectl
  ];
}
```

- [ ] **Step 3: Rewrite templates/flake.nix outputs**

Replace the `let` bindings and `outputs` attrset body with:

```nix
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      presetNames = [
        "python"
        "docker"
        "k8s"
      ];

      homeManagerModules = {
        common = import ./modules/common.nix;
        python = import ./modules/python.nix;
        docker = import ./modules/docker.nix;
        k8s = import ./modules/k8s.nix;
      };

      # common is always present; a preset composes onto it. Identity is
      # forced here because common.nix reads $USER/$HOME via getEnv,
      # which is empty under `nix flake check`'s pure evaluation.
      mkConfig =
        system: names:
        home-manager.lib.homeManagerConfiguration {
          pkgs = nixpkgs.legacyPackages.${system};
          modules = [
            homeManagerModules.common
            {
              home.username = "cloudlab";
              home.homeDirectory = "/home/cloudlab";
              home.stateVersion = "25.11";
            }
          ] ++ map (n: homeManagerModules.${n}) names;
        };
    in
    {
      inherit homeManagerModules;

      homeConfigurations = nixpkgs.lib.listToAttrs (
        nixpkgs.lib.concatMap (
          system:
          map (name: {
            name = "${name}-${system}";
            value = mkConfig system [ name ];
          }) presetNames
          ++ [{
            name = "common-${system}";
            value = mkConfig system [ ];
          }]
        ) systems
      );

      # What actually gets built by `nix flake check ./templates`.
      # homeConfigurations is not an output type flake check recognises,
      # so without this every preset would stay unbuilt until a user
      # provisioned one.
      checks = nixpkgs.lib.genAttrs systems (
        system:
        nixpkgs.lib.genAttrs presetNames (
          name: (mkConfig system [ name ]).activationPackage
        )
        // {
          common = (mkConfig system [ ]).activationPackage;
        }
      );
    };
```

- [ ] **Step 4: Build every preset**

Run: `nix flake check ./templates --print-build-logs`
Expected: PASS, building `common`, `python`, `docker` and `k8s` for both
systems. A failure here is a real broken preset, not a test artefact.

If `aarch64-linux` cannot be built on the local machine, run
`nix flake check ./templates --system x86_64-linux` locally and rely on CI
(Task 5) for full coverage.

- [ ] **Step 5: Commit**

```bash
git add templates/
git commit -m "Compose presets onto a common baseline module"
```

---

### Task 5: Wire CI, migrate the config, update the docs

**Files:**
- Modify: `.github/workflows/ci.yml:29`
- Modify: `cloudlab.pkl` (repo root — local-only, in `.git/info/exclude`, so it will not appear in `git status`)
- Modify: `docs/config.md`, `README.md`

**Interfaces:**
- Consumes: the `checks` output from Task 4.
- Produces: nothing consumed by later tasks.

- [ ] **Step 1: Add the templates check to CI**

In `.github/workflows/ci.yml`, after the existing `nix flake check` line:

```yaml
      - run: nix flake check ./templates --print-build-logs
```

- [ ] **Step 2: Migrate this repository's own config**

`cloudlab.pkl` currently sets `template = "docker"`, which Task 2 made
invalid. Replace it:

```pkl
region = "blr1"
size = "s-2vcpu-2gb"
tailscale = true
presets {
  "docker"
}
sshKeys {
  "ab:d5:8c:b1:79:ae:ae:a6:85:8c:5c:7f:16:d9:b9:03"
}
agents {
  "claude"
}
packages {
  "tig"
  "lazygit"
}
```

Note `template` is gone entirely — it is optional now, and this instance does
not bring its own flake.

- [ ] **Step 3: Verify the config still evaluates**

Run: `nix develop --command go test ./internal/config/ -v`
Then: `nix develop --command go run . status`
Expected: config resolves without error. A Pkl error naming `template` means
Step 2 was missed somewhere.

- [ ] **Step 4: Update the docs**

In `docs/config.md`, replace the `template` row of the fields table and the
`### Templates` section:

- `template`: "A complete flake reference (`\"<url>#<output>\"`) whose
  `homeManagerModules` output replaces the built-in baseline. Optional; omit
  it to use cloudlab's own templates flake."
- New `presets` row: "Curated module sets composed onto the baseline:
  `python`, `docker`, `k8s`. Additive and composable — unlike the old
  `template` field, an instance can be several at once."
- Add a `### Presets` section documenting each name's contents, and stating
  plainly that `k8s` pulls in `docker` because `kind` needs a daemon.

In `README.md`, update the Templates table to a Presets table with the same
three entries, and correct the "Both build on a shared `common` module"
sentence, which now describes every instance rather than two templates.

- [ ] **Step 5: Run the full suite**

Run: `nix develop --command bash -c 'go build ./... && go vet ./... && go test ./...'`
Then: `nix develop --command pre-commit run --all-files`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add .github/workflows/ci.yml docs/config.md README.md
git commit -m "Build every preset in CI and document them"
```

`cloudlab.pkl` is excluded from git and is deliberately not staged.

---

## Verification

After all five tasks:

```bash
nix develop --command bash -c 'go build ./... && go vet ./... && go test ./...'
nix flake check --print-build-logs
nix flake check ./templates --print-build-logs
```

Then against a real instance, which is the only thing that exercises
`Render` end to end:

```bash
cloudlab provision
ssh <instance> 'command -v docker kind kubectl'
```

`kind` and `kubectl` should be absent until `presets { "k8s" }` is added, and
present after another `cloudlab provision`.
