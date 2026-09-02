# Provisioning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a real `templates/` Nix flake (`python`/`docker` home-manager profiles) and a standalone, testable `internal/provisioning` Go package (cloud-init payload, template-ref resolution, render-trigger decision, per-instance flake rendering, offline validation) — no CLI wiring.

**Architecture:** `templates/flake.nix` exposes flat `homeManagerModules.<name>` (composable modules) and `homeConfigurations.<name>-<system>` (statically-built, directly-switchable configurations, one per template per supported architecture). `internal/provisioning` embeds the cloud-init payload, resolves `template` values (built-in name or BYO ref) to a concrete flake reference, decides whether a per-instance wrapper flake is needed, renders one via Go's `text/template` when it is, and validates that a resolved config's template/flakes actually exist via `nix eval`.

**Tech Stack:** Go 1.23+ stdlib only (`embed`, `text/template`, `os/exec`, `context`) — no new Go dependencies. Nix flakes, `home-manager.lib.homeManagerConfiguration`. Pkl schema addition (`arch`, `Flake.modules`) via the existing `pkl-gen-go` codegen pipeline.

## Global Constraints

- Module path: `github.com/jskswamy/cloudlab`.
- **Toolchain prerequisite: run everything in this plan inside `nix develop`** (from the repo root) — same as every prior plan in this repo.
- **Real package names, verified, not guessed**: `git`, `age`, `python312`, `uv`, `docker`, `minikube`, `kubectl` are all confirmed real nixpkgs attributes (checked against the `unstable` channel directly, not assumed from memory). `aide` (`github.com/jskswamy/aide`) and Claude Code are **not** nixpkgs packages — integrating them is explicitly out of scope for this plan (per the design spec's own carve-out: "full python/docker package lists... an implementation detail settled task-by-task, not a design decision"). `common.nix` in this plan only includes `git` and `age`; expanding it is future work, not a placeholder to fill in "later" within this plan.
- **VM user is assumed to be `root`, home directory `/root`** — DigitalOcean's standard Ubuntu droplet images boot with `root` as the only user, no separate non-root user unless configured elsewhere (a decision that belongs to a later "up implementation" sub-project, not this one). `home.stateVersion = "25.11"` (current stable NixOS/home-manager release at time of writing). These three values are hardcoded in `templates/modules/common.nix` for now; if a later phase decides differently, that's a one-file change, not a redesign.
- **`homeConfigurations` output naming: `"<template>-<system>"`, not nested per-system.** Home-manager's own flake convention for `homeConfigurations` is a *flat* name (verified against `nix-community/home-manager`'s own README example: `homeConfigurations.jdoe = ...`, no system nesting) — but a single flat `homeConfigurations.python` can only ever be built against one hardcoded `nixpkgs.legacyPackages.${system}`, which would break on whichever architecture wasn't chosen. Since cloudlab's own code (never a human typing `--flake` by hand) constructs every `--flake <ref>#<name>` string, the multi-architecture answer is a flat name with the system baked in: `homeConfigurations."python-x86_64-linux"`, `homeConfigurations."python-aarch64-linux"`, etc. `homeManagerModules.<name>` stays flat with no system suffix — modules are plain functions, not tied to a system until built into a configuration.
- **`ResolveTemplateRef` takes `(template, arch string)`, not just `template`** — a refinement of the design spec's function signature (the spec explicitly left "exact syntax settled during implementation" open). Needs `arch` to construct the correctly-suffixed default ref for built-in names. A BYO ref (already a complete `"url#name"` string the user wrote themselves) is returned as-is, ignoring `arch`.
- No new Go dependencies. Testing: stdlib `testing` only, real `nix`/`pkl` binaries, no mocking — matching every other package in this codebase.
- `internal/provisioning` never touches Pkl directly — it only ever consumes an already-resolved `config.Config`.

---

### Task 1: Extend the `cloudlab.pkl` schema — `arch` and `Flake.modules`

**Files:**
- Modify: `internal/config/Config.pkl`
- Modify (regenerated): `internal/config/Config.pkl.go`, `internal/config/Flake.pkl.go`
- Modify: `internal/config/config_test.go`
- Modify: `docs/config.md`

**Interfaces:**
- Consumes: nothing new — extends the already-shipped `internal/config` package.
- Produces: `Config.Arch string` (defaults `"x86_64"`), `Flake.Modules bool` (defaults `false`) — consumed by Task 4/5/6 of this plan.

- [ ] **Step 1: Write the failing tests**

Add to `internal/config/config_test.go`:

```go
func TestLoad_ArchDefaultsToX86_64(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, project, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
	}, "\n")+"\n")

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Arch != "x86_64" {
		t.Errorf("Arch = %q, want %q", cfg.Arch, "x86_64")
	}
}

func TestLoad_ArchOverride(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, project, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`arch = "arm64"`,
	}, "\n")+"\n")

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Arch != "arm64" {
		t.Errorf("Arch = %q, want %q", cfg.Arch, "arm64")
	}
}

func TestLoad_FlakeModulesDefaultsFalse(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, project, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`flakes {`,
		`  new {`,
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
	if cfg.Flakes[0].Modules != false {
		t.Errorf("Flakes[0].Modules = %v, want false", cfg.Flakes[0].Modules)
	}
}

func TestLoad_FlakeModulesTrue(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, project, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`flakes {`,
		`  new {`,
		`    url = "github:someorg/custom-tool"`,
		`    packages { "cli" }`,
		`    modules = true`,
		`  }`,
		`}`,
	}, "\n")+"\n")

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(cfg.Flakes) != 1 || !cfg.Flakes[0].Modules {
		t.Errorf("Flakes = %v, want one entry with Modules = true", cfg.Flakes)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/... -run 'TestLoad_Arch|TestLoad_FlakeModules' -v`
Expected: FAIL — compile errors, `cfg.Arch undefined` / `f.Modules undefined` (the generated struct doesn't have these fields yet).

- [ ] **Step 3: Update the schema**

Edit `internal/config/Config.pkl` to:

```pkl
@go.Package { name = "github.com/jskswamy/cloudlab/internal/config" }
module cloudlab.Config

import "package://pkg.pkl-lang.org/pkl-go/pkl.golang@0.14.0#/go.pkl"

basePath: String?
region: String?
size: String?
template: String?
arch: String = "x86_64"
sshKeys: Listing<String>?
packages: Listing<String> = new Listing {}
flakes: Listing<Flake> = new Listing {}

class Flake {
  url: String
  packages: Listing<String>
  modules: Boolean = false
}
```

- [ ] **Step 4: Regenerate the Go bindings**

Run (inside `nix develop`): `go generate ./internal/config/...`

Confirm `internal/config/Config.pkl.go` now has `Arch string \`pkl:"arch"\`` (not a pointer — it has a Pkl-level default, same as `Packages`/`Flakes`) and `internal/config/Flake.pkl.go` now has `Modules bool \`pkl:"modules"\`` (also not a pointer, same reasoning). If either generates as a pointer type instead, stop and reconcile with a human before continuing — every later task in this plan assumes these exact shapes.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/config/... -v`
Expected: PASS — every test in the package, including the four new ones and everything that existed before.

- [ ] **Step 6: Update the docs**

In `docs/config.md`, add a row to the fields table (after `template`):

```markdown
| `arch` | `String` | No | `"x86_64"` | Instance CPU architecture: `"x86_64"` or `"arm64"`. Maps to the Nix system used for template/flake resolution. |
```

And update the `flakes` row's description to mention `modules`:

```markdown
| `flakes` | `Listing<Flake>` (`{url, packages, modules}`) | No | empty | Nix flakes to install, each with its own package list and an optional `modules` flag to also pull that flake's `homeManagerModules.default`. |
```

- [ ] **Step 7: Commit**

```bash
git add internal/config/Config.pkl internal/config/Config.pkl.go internal/config/Flake.pkl.go internal/config/config_test.go docs/config.md
git commit -m "Add arch and Flake.modules fields to cloudlab.pkl"
```

---

### Task 2: `templates/` Nix flake — common, python, docker profiles

**Files:**
- Create: `templates/flake.nix`
- Create: `templates/modules/common.nix`
- Create: `templates/modules/python.nix`
- Create: `templates/modules/docker.nix`

**Interfaces:**
- Consumes: nothing from other tasks — pure Nix content, no Go involved.
- Produces: `homeManagerModules.{python,docker}` and `homeConfigurations."{python,docker}-{x86_64-linux,aarch64-linux}"`, consumed by Task 4 (`ResolveTemplateRef`) and Task 6 (`Validate`'s integration test).

This task has no Go test — it's verified with real `nix eval` commands directly, the same way this repo's own `flake.nix` is verified.

- [ ] **Step 1: Write the common module**

Create `templates/modules/common.nix`:

```nix
{ pkgs, ... }:
{
  home.username = "root";
  home.homeDirectory = "/root";
  home.stateVersion = "25.11";

  home.packages = [
    pkgs.git
    pkgs.age
  ];
}
```

- [ ] **Step 2: Write the python module**

Create `templates/modules/python.nix`:

```nix
{ pkgs, ... }:
{
  imports = [ ./common.nix ];

  home.packages = [
    pkgs.python312
    pkgs.uv
  ];
}
```

- [ ] **Step 3: Write the docker module**

Create `templates/modules/docker.nix`:

```nix
{ pkgs, ... }:
{
  imports = [ ./common.nix ];

  home.packages = [
    pkgs.docker
    pkgs.minikube
    pkgs.kubectl
  ];
}
```

- [ ] **Step 4: Write the templates flake**

Create `templates/flake.nix`:

```nix
{
  description = "cloudlab template catalog";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    home-manager = {
      url = "github:nix-community/home-manager";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, home-manager }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      templateNames = [ "python" "docker" ];

      homeManagerModules = {
        python = import ./modules/python.nix;
        docker = import ./modules/docker.nix;
      };

      mkConfig = system: name:
        home-manager.lib.homeManagerConfiguration {
          pkgs = nixpkgs.legacyPackages.${system};
          modules = [ homeManagerModules.${name} ];
        };
    in
    {
      inherit homeManagerModules;

      homeConfigurations = nixpkgs.lib.listToAttrs (nixpkgs.lib.concatMap
        (system: map
          (name: {
            name = "${name}-${system}";
            value = mkConfig system name;
          })
          templateNames)
        systems);
    };
}
```

- [ ] **Step 5: Stage the new files**

Run: `git add templates/`

Nix flake evaluation only sees files that are at least staged in git
(tracked in the index) — an untracked file is invisible to `nix eval`
even if it's sitting right there on disk. Stage now, before verifying;
the actual commit still happens in Step 7.

- [ ] **Step 6: Verify the templates flake evaluates**

Run (inside `nix develop`, from the repo root):

```bash
nix eval ./templates#homeManagerModules.python --apply 'x: "ok"'
nix eval ./templates#homeManagerModules.docker --apply 'x: "ok"'
nix eval './templates#homeConfigurations."python-x86_64-linux"' --apply 'x: "ok"'
nix eval './templates#homeConfigurations."docker-x86_64-linux"' --apply 'x: "ok"'
```

Expected: each prints `"ok"`. If any fails, the error names the exact missing attribute or Nix syntax problem — fix it and rerun before continuing.

- [ ] **Step 7: Generate the lock file**

Run: `nix flake lock ./templates`

Expected: creates `templates/flake.lock`.

- [ ] **Step 8: Commit**

```bash
git add templates/
git commit -m "Add templates/ flake with python and docker profiles"
```

---

### Task 3: `internal/provisioning` — cloud-init payload

**Files:**
- Create: `internal/provisioning/cloud-init.sh`
- Create: `internal/provisioning/cloud_init.go`
- Test: `internal/provisioning/cloud_init_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `provisioning.CloudInitUserData string`, consumed by a later sub-project's VM-creation step (not this plan).

- [ ] **Step 1: Write the failing test**

Create `internal/provisioning/cloud_init_test.go`:

```go
package provisioning

import "testing"

func TestCloudInitUserData_InstallsNixNonInteractively(t *testing.T) {
	if CloudInitUserData == "" {
		t.Fatal("CloudInitUserData is empty")
	}
	if CloudInitUserData[:2] != "#!" {
		t.Errorf("CloudInitUserData = %q, want it to start with a shebang", CloudInitUserData[:2])
	}
	for _, want := range []string{
		"install.determinate.systems/nix",
		"install --no-confirm",
	} {
		if !containsString(CloudInitUserData, want) {
			t.Errorf("CloudInitUserData does not contain %q", want)
		}
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/provisioning/... -v`
Expected: FAIL — compile error, `undefined: CloudInitUserData` (package doesn't exist yet).

- [ ] **Step 3: Write the cloud-init script**

Create `internal/provisioning/cloud-init.sh`:

```bash
#!/bin/bash
set -euo pipefail

curl --proto '=https' --tlsv1.2 -sSf -L https://install.determinate.systems/nix | sh -s -- install --no-confirm
```

- [ ] **Step 4: Write the embed**

Create `internal/provisioning/cloud_init.go`:

```go
// Package provisioning produces the artifacts a later sub-project
// needs to bring a cloudlab instance's Nix/home-manager environment
// up to date: the cloud-init payload, template-ref resolution, the
// render-trigger decision, per-instance flake rendering, and offline
// validation. It never touches Pkl, SSH, or a live instance — it only
// ever consumes an already-resolved config.Config.
package provisioning

import _ "embed"

//go:embed cloud-init.sh
var CloudInitUserData string
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/provisioning/... -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/provisioning/cloud-init.sh internal/provisioning/cloud_init.go internal/provisioning/cloud_init_test.go
git commit -m "Add embedded cloud-init payload"
```

---

### Task 4: Template-ref resolution and Nix system mapping

**Files:**
- Create: `internal/provisioning/template.go`
- Test: `internal/provisioning/template_test.go`

**Interfaces:**
- Consumes: nothing from other tasks (pure string logic).
- Produces: `ResolveTemplateRef(template, arch string) string`, `NixSystem(arch string) string`, `splitFlakeRef(ref string) (url, name string)` — used by Task 5 (`Render`) and Task 6 (`Validate`).

- [ ] **Step 1: Write the failing tests**

Create `internal/provisioning/template_test.go`:

```go
package provisioning

import "testing"

func TestResolveTemplateRef_BuiltinExpandsWithArchSuffix(t *testing.T) {
	got := ResolveTemplateRef("python", "x86_64")
	want := "github:jskswamy/cloudlab?dir=templates#python-x86_64-linux"
	if got != want {
		t.Errorf("ResolveTemplateRef(python, x86_64) = %q, want %q", got, want)
	}
}

func TestResolveTemplateRef_BuiltinArm64(t *testing.T) {
	got := ResolveTemplateRef("docker", "arm64")
	want := "github:jskswamy/cloudlab?dir=templates#docker-aarch64-linux"
	if got != want {
		t.Errorf("ResolveTemplateRef(docker, arm64) = %q, want %q", got, want)
	}
}

func TestResolveTemplateRef_ByoRefPassesThroughUnchanged(t *testing.T) {
	byo := "github:someuser/their-templates#custom"
	got := ResolveTemplateRef(byo, "x86_64")
	if got != byo {
		t.Errorf("ResolveTemplateRef(%q) = %q, want unchanged", byo, got)
	}
}

func TestNixSystem_DefaultsToX86_64Linux(t *testing.T) {
	if got := NixSystem("x86_64"); got != "x86_64-linux" {
		t.Errorf("NixSystem(x86_64) = %q, want x86_64-linux", got)
	}
	if got := NixSystem(""); got != "x86_64-linux" {
		t.Errorf("NixSystem(\"\") = %q, want x86_64-linux", got)
	}
}

func TestNixSystem_Arm64(t *testing.T) {
	if got := NixSystem("arm64"); got != "aarch64-linux" {
		t.Errorf("NixSystem(arm64) = %q, want aarch64-linux", got)
	}
}

func TestSplitFlakeRef_SplitsOnLastHash(t *testing.T) {
	url, name := splitFlakeRef("github:jskswamy/cloudlab?dir=templates#python-x86_64-linux")
	if url != "github:jskswamy/cloudlab?dir=templates" {
		t.Errorf("url = %q, want %q", url, "github:jskswamy/cloudlab?dir=templates")
	}
	if name != "python-x86_64-linux" {
		t.Errorf("name = %q, want %q", name, "python-x86_64-linux")
	}
}

func TestSplitFlakeRef_NoHashReturnsEmptyName(t *testing.T) {
	url, name := splitFlakeRef("github:someuser/their-templates")
	if url != "github:someuser/their-templates" || name != "" {
		t.Errorf("splitFlakeRef(no-hash) = (%q, %q), want (unchanged, empty)", url, name)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/provisioning/... -run 'TestResolveTemplateRef|TestNixSystem|TestSplitFlakeRef' -v`
Expected: FAIL — compile errors, `undefined: ResolveTemplateRef` / `NixSystem` / `splitFlakeRef`.

- [ ] **Step 3: Write the minimal implementation**

Create `internal/provisioning/template.go`:

```go
package provisioning

import "strings"

// defaultTemplatesRef is this repo's own templates/ flake, floated on
// the default branch (not a version tag) — see the Provisioning
// design spec for why template fixes shouldn't need a cloudlab
// release.
const defaultTemplatesRef = "github:jskswamy/cloudlab?dir=templates"

// builtinTemplates is the set of template names ResolveTemplateRef
// expands against defaultTemplatesRef. Anything else is assumed to
// already be a full "url#name" flake ref and is returned as-is.
var builtinTemplates = map[string]bool{
	"python": true,
	"docker": true,
}

// ResolveTemplateRef expands a built-in template name ("python",
// "docker") to its default flake ref, with arch's Nix system appended
// to the output name (matching templates/flake.nix's
// "<name>-<system>" homeConfigurations naming). Any other template
// value is assumed to already be a complete flake ref and is returned
// unchanged, ignoring arch.
func ResolveTemplateRef(template, arch string) string {
	if !builtinTemplates[template] {
		return template
	}
	return defaultTemplatesRef + "#" + template + "-" + NixSystem(arch)
}

// NixSystem maps a cloudlab.pkl arch value to the Nix system string
// used for per-system flake outputs. Every cloudlab instance is
// Linux; only the CPU part varies. Empty or unrecognized values
// default to x86_64-linux, matching cloudlab.pkl's own arch default.
func NixSystem(arch string) string {
	if arch == "arm64" {
		return "aarch64-linux"
	}
	return "x86_64-linux"
}

// splitFlakeRef splits a home-manager-style "url#name" reference into
// its flake URL and output name. A ref with no "#" returns the whole
// string as url and an empty name.
func splitFlakeRef(ref string) (url, name string) {
	i := strings.LastIndex(ref, "#")
	if i < 0 {
		return ref, ""
	}
	return ref[:i], ref[i+1:]
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/provisioning/... -v`
Expected: PASS — every test in the package.

- [ ] **Step 5: Commit**

```bash
git add internal/provisioning/template.go internal/provisioning/template_test.go
git commit -m "Add template-ref resolution and Nix system mapping"
```

---

### Task 5: Render-trigger rule and per-instance flake rendering

**Files:**
- Create: `internal/provisioning/render.go`
- Test: `internal/provisioning/render_test.go`

**Interfaces:**
- Consumes: `config.Config`, `config.Flake` (already shipped; extended in Task 1), `NixSystem` (Task 4).
- Produces: `NeedsRender(cfg config.Config) bool`, `Render(cfg config.Config, templateRef string) (string, error)` — consumed by Task 6's integration test and by a later sub-project's `Reconcile`.

- [ ] **Step 1: Write the failing tests**

Create `internal/provisioning/render_test.go`:

```go
package provisioning

import (
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/config"
)

func TestNeedsRender_EmptyPackagesAndFlakes_False(t *testing.T) {
	cfg := config.Config{}
	if NeedsRender(cfg) {
		t.Error("NeedsRender() = true, want false for empty packages and flakes")
	}
}

func TestNeedsRender_NonEmptyPackages_True(t *testing.T) {
	cfg := config.Config{Packages: []string{"ripgrep"}}
	if !NeedsRender(cfg) {
		t.Error("NeedsRender() = false, want true when packages is non-empty")
	}
}

func TestNeedsRender_NonEmptyFlakes_True(t *testing.T) {
	cfg := config.Config{Flakes: []config.Flake{{Url: "github:someorg/custom-tool"}}}
	if !NeedsRender(cfg) {
		t.Error("NeedsRender() = false, want true when flakes is non-empty")
	}
}

func TestRender_TemplateOnly_ImportsTemplateModule(t *testing.T) {
	cfg := config.Config{Arch: "x86_64"}
	out, err := Render(cfg, "github:jskswamy/cloudlab?dir=templates#python-x86_64-linux")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(out, `template.url = "github:jskswamy/cloudlab?dir=templates"`) {
		t.Errorf("output does not reference the template flake url:\n%s", out)
	}
	if !strings.Contains(out, "template.homeManagerModules.\"python-x86_64-linux\"") {
		t.Errorf("output does not import the template's homeManagerModules:\n%s", out)
	}
}

func TestRender_WithPackages_AddsSyntheticModule(t *testing.T) {
	cfg := config.Config{Arch: "x86_64", Packages: []string{"ripgrep", "jq"}}
	out, err := Render(cfg, "github:jskswamy/cloudlab?dir=templates#python-x86_64-linux")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(out, `pkgs."ripgrep"`) || !strings.Contains(out, `pkgs."jq"`) {
		t.Errorf("output does not reference both packages:\n%s", out)
	}
}

func TestRender_WithFlakeModules_ImportsFlakeInputAndModule(t *testing.T) {
	cfg := config.Config{
		Arch: "x86_64",
		Flakes: []config.Flake{
			{Url: "github:someorg/custom-tool", Packages: []string{"cli"}, Modules: true},
		},
	}
	out, err := Render(cfg, "github:jskswamy/cloudlab?dir=templates#python-x86_64-linux")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(out, `flake0.url = "github:someorg/custom-tool"`) {
		t.Errorf("output does not declare the flake input:\n%s", out)
	}
	if !strings.Contains(out, `flake0.packages."x86_64-linux"."cli"`) {
		t.Errorf("output does not reference the flake's package:\n%s", out)
	}
	if !strings.Contains(out, "flake0.homeManagerModules.default") {
		t.Errorf("output does not reference the flake's default module:\n%s", out)
	}
}

func TestRender_FlakeWithoutModules_OmitsModuleReference(t *testing.T) {
	cfg := config.Config{
		Arch: "x86_64",
		Flakes: []config.Flake{
			{Url: "github:someorg/custom-tool", Packages: []string{"cli"}, Modules: false},
		},
	}
	out, err := Render(cfg, "github:jskswamy/cloudlab?dir=templates#python-x86_64-linux")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if strings.Contains(out, "homeManagerModules.default") {
		t.Errorf("output references homeManagerModules.default when Modules is false:\n%s", out)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/provisioning/... -run 'TestNeedsRender|TestRender' -v`
Expected: FAIL — compile errors, `undefined: NeedsRender` / `Render`.

- [ ] **Step 3: Write the minimal implementation**

Create `internal/provisioning/render.go`:

```go
package provisioning

import (
	"strings"
	"text/template"

	"github.com/jskswamy/cloudlab/internal/config"
)

// NeedsRender reports whether cfg's packages/flakes require a
// per-instance wrapper flake, rather than using the template ref
// as-is. See the Provisioning design spec's render-trigger rule.
func NeedsRender(cfg config.Config) bool {
	return len(cfg.Packages) > 0 || len(cfg.Flakes) > 0
}

var renderTmpl = template.Must(template.New("flake").Parse(`{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    home-manager.url = "github:nix-community/home-manager";
    template.url = "{{.TemplateURL}}";
{{range $i, $f := .Flakes}}    flake{{$i}}.url = "{{$f.Url}}";
{{end}}  };
  outputs = { self, nixpkgs, home-manager, template{{range $i, $f := .Flakes}}, flake{{$i}}{{end}}, ... }: {
    homeConfigurations.default = home-manager.lib.homeManagerConfiguration {
      pkgs = nixpkgs.legacyPackages."{{.System}}";
      modules = [
        template.homeManagerModules."{{.TemplateName}}"
{{if .Packages}}        { home.packages = [ {{range .Packages}}pkgs."{{.}}" {{end}}]; }
{{end}}{{range $i, $f := .Flakes}}{{if $f.Packages}}        { home.packages = [ {{range $f.Packages}}flake{{$i}}.packages."{{$.System}}"."{{.}}" {{end}}]; }
{{end}}{{if $f.Modules}}        flake{{$i}}.homeManagerModules.default
{{end}}{{end}}      ];
    };
  };
}
`))

type renderData struct {
	TemplateURL  string
	TemplateName string
	System       string
	Packages     []string
	Flakes       []config.Flake
}

// Render produces the per-instance wrapper flake.nix content for cfg,
// importing templateRef's homeManagerModules.<name> as the template
// module, plus a synthetic module for cfg.Packages and one per
// cfg.Flakes[] entry (its packages, and its homeManagerModules.default
// if Modules is true).
func Render(cfg config.Config, templateRef string) (string, error) {
	url, name := splitFlakeRef(templateRef)
	data := renderData{
		TemplateURL:  url,
		TemplateName: name,
		System:       NixSystem(cfg.Arch),
		Packages:     cfg.Packages,
		Flakes:       cfg.Flakes,
	}
	var out strings.Builder
	if err := renderTmpl.Execute(&out, data); err != nil {
		return "", err
	}
	return out.String(), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/provisioning/... -v`
Expected: PASS — every test in the package.

- [ ] **Step 5: Commit**

```bash
git add internal/provisioning/render.go internal/provisioning/render_test.go
git commit -m "Add render-trigger rule and per-instance flake rendering"
```

---

### Task 6: Offline validation, real fixtures, and the templates integration proof

**Files:**
- Create: `internal/provisioning/validate.go`
- Create: `internal/provisioning/testdata/good-flake/flake.nix`
- Create: `internal/provisioning/testdata/missing-package-flake/flake.nix`
- Create: `internal/provisioning/testdata/no-modules-flake/flake.nix`
- Test: `internal/provisioning/validate_test.go`

**Interfaces:**
- Consumes: `config.Config`, `ResolveTemplateRef`/`NixSystem`/`splitFlakeRef` (Task 4), `NeedsRender` (Task 5), the real `templates/` flake (Task 2).
- Produces: `Validate(ctx context.Context, cfg config.Config) error` — nothing else in this plan consumes it; it's this package's other public entry point alongside `Render`.

- [ ] **Step 1: Write the test fixture flakes**

Create `internal/provisioning/testdata/good-flake/flake.nix`:

```nix
{
  outputs = { self, nixpkgs }: {
    packages.x86_64-linux.cli = nixpkgs.legacyPackages.x86_64-linux.hello;
    homeManagerModules.default = { pkgs, ... }: { home.packages = [ pkgs.hello ]; };
  };
}
```

Create `internal/provisioning/testdata/missing-package-flake/flake.nix`:

```nix
{
  outputs = { self, nixpkgs }: {
    packages.x86_64-linux = { };
    homeManagerModules.default = { pkgs, ... }: { home.packages = [ pkgs.hello ]; };
  };
}
```

Create `internal/provisioning/testdata/no-modules-flake/flake.nix`:

```nix
{
  outputs = { self, nixpkgs }: {
    packages.x86_64-linux.cli = nixpkgs.legacyPackages.x86_64-linux.hello;
  };
}
```

- [ ] **Step 2: Write the failing tests**

Create `internal/provisioning/validate_test.go`:

```go
package provisioning

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/config"
)

func testdataFlake(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine this test file's own path")
	}
	return "path:" + filepath.Join(filepath.Dir(thisFile), "testdata", name)
}

// validTemplateFor points every flake-focused Validate test at a
// template ref that's guaranteed to resolve (good-flake exposes both
// homeManagerModules.default and packages.cli) — these tests are
// about a specific flakes[] entry, not about template resolution
// itself, and Validate always checks the template too. A zero-value
// cfg.Template would produce a spurious extra "template" problem
// alongside whatever these tests actually mean to exercise.
func validTemplateFor(t *testing.T) string {
	t.Helper()
	return testdataFlake(t, "good-flake") + "#default"
}

func TestValidate_GoodConfig_NoError(t *testing.T) {
	cfg := config.Config{
		Arch:     "x86_64",
		Template: validTemplateFor(t),
		Flakes: []config.Flake{
			{Url: testdataFlake(t, "good-flake"), Packages: []string{"cli"}, Modules: true},
		},
	}
	if err := Validate(context.Background(), cfg); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
}

func TestValidate_MissingFlakePackage_NamesItClearly(t *testing.T) {
	cfg := config.Config{
		Arch:     "x86_64",
		Template: validTemplateFor(t),
		Flakes: []config.Flake{
			{Url: testdataFlake(t, "missing-package-flake"), Packages: []string{"cli"}},
		},
	}
	err := Validate(context.Background(), cfg)
	if err == nil {
		t.Fatal("Validate() error = nil, want error naming the missing package")
	}
	if !strings.Contains(err.Error(), "cli") {
		t.Errorf("error %q does not mention the missing package %q", err.Error(), "cli")
	}
	if strings.Contains(err.Error(), "while calling") || strings.Contains(err.Error(), "at /") {
		t.Errorf("error %q looks like a raw Nix stack trace, not a user-friendly message", err.Error())
	}
}

func TestValidate_FlakeMissingModules_NamesItClearly(t *testing.T) {
	cfg := config.Config{
		Arch:     "x86_64",
		Template: validTemplateFor(t),
		Flakes: []config.Flake{
			{Url: testdataFlake(t, "no-modules-flake"), Packages: []string{"cli"}, Modules: true},
		},
	}
	err := Validate(context.Background(), cfg)
	if err == nil {
		t.Fatal("Validate() error = nil, want error naming the missing module")
	}
	if !strings.Contains(err.Error(), "homeManagerModules") {
		t.Errorf("error %q does not mention homeManagerModules", err.Error())
	}
}

func TestValidate_RealTemplates_NoError(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine this test file's own path")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	templatesRef := "path:" + filepath.Join(repoRoot, "templates")

	cfg := config.Config{Arch: "x86_64", Template: templatesRef + "#python-x86_64-linux"}
	if err := Validate(context.Background(), cfg); err != nil {
		t.Errorf("Validate() error = %v, want nil (real templates/ flake should validate cleanly)", err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/provisioning/... -run TestValidate -v`
Expected: FAIL — compile error, `undefined: Validate`.

- [ ] **Step 4: Write the minimal implementation**

Create `internal/provisioning/validate.go`:

```go
package provisioning

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/jskswamy/cloudlab/internal/config"
)

// Validate checks that cfg's template and flakes[] resolve to real,
// evaluable flake outputs, without building or switching anything.
// Returns an error naming every problem found, not just the first.
func Validate(ctx context.Context, cfg config.Config) error {
	url, name := splitFlakeRef(ResolveTemplateRef(cfg.Template, cfg.Arch))
	attr := "homeConfigurations." + name
	if NeedsRender(cfg) {
		attr = "homeManagerModules." + name
	}

	var problems []string
	if err := evalExists(ctx, url, attr); err != nil {
		problems = append(problems, fmt.Sprintf("template %q: %v", cfg.Template, err))
	}

	system := NixSystem(cfg.Arch)
	for _, f := range cfg.Flakes {
		for _, pkg := range f.Packages {
			if err := evalExists(ctx, f.Url, "packages."+system+"."+pkg); err != nil {
				problems = append(problems, fmt.Sprintf("flake %q package %q: %v", f.Url, pkg, err))
			}
		}
		if f.Modules {
			if err := evalExists(ctx, f.Url, "homeManagerModules.default"); err != nil {
				problems = append(problems, fmt.Sprintf("flake %q module: %v", f.Url, err))
			}
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("provisioning validation failed:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

// evalExists reports whether ref#attr resolves to anything at all.
// The --apply lambda never touches x, so Nix only needs to resolve
// the attribute path far enough to bind it — proving existence
// without trying to print a value that might not be representable
// (a home-manager module, a function).
func evalExists(ctx context.Context, ref, attr string) error {
	out, err := exec.CommandContext(ctx, "nix", "eval", ref+"#"+attr, "--apply", `x: "ok"`).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", lastNixError(string(out)))
	}
	return nil
}

// lastNixError extracts Nix's own final, specific error line from its
// often trace-heavy output, so validation errors stay short and
// actionable instead of dumping a full Nix stack trace at the user.
func lastNixError(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); strings.HasPrefix(line, "error:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "error:"))
		}
	}
	return strings.TrimSpace(output)
}
```

- [ ] **Step 5: Stage the new testdata fixture flakes**

Run: `git add internal/provisioning/testdata/`

Same gotcha as Task 2's `templates/` flake: `nix eval` (which `Validate`'s tests shell out to) only sees files at least staged in git, even for a fixture flake nested inside the main repo. Stage now, before running the tests — the actual commit still happens in Step 7.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/provisioning/... -v`
Expected: PASS — every test in the package, including `TestValidate_RealTemplates_NoError`, which proves Task 2's real `templates/` flake is genuinely valid, not just structurally plausible.

- [ ] **Step 7: Commit**

```bash
git add internal/provisioning/validate.go internal/provisioning/validate_test.go internal/provisioning/testdata/
git commit -m "Add offline validation with user-friendly error messages"
```

---

## Final check

Run the full package suite once more from the repo root (inside `nix develop`):

```bash
go build ./...
go vet ./...
go test ./... -v
nix flake check --print-build-logs
```

Expected: everything builds, every test across the repo passes (this plan's `internal/config` and `internal/provisioning` tests plus every untouched earlier phase's tests — aside from `TestLoadFromPath_MinimalFixture`, the known machine-specific exception documented throughout this project's history), and `nix flake check` is clean.
