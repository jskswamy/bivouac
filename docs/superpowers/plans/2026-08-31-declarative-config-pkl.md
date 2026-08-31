# Declarative Config: cloudlab.pkl Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a standalone, testable `internal/config` Go package that loads `cloudlab.pkl` (Pkl) declarative config files, merges a project file with an optional personal base config, and validates the result — plus a foundational config-structure doc and two worked examples. No CLI wiring.

**Architecture:** `pkl/Config.pkl` is the versioned schema, codegen'd via `pkl-gen-go` into `internal/config/{Config,Flake,init}.pkl.go` (committed, `DO NOT EDIT`). A hand-written `internal/config/config.go` wraps the generated `LoadFromPath` with base-path resolution, a Go-side merge (scalars: project-overrides-base; lists: additive), and post-merge required-field validation. `docs/config.md` plus two examples under `docs/examples/` document the schema and the base-merge pattern for humans.

**Tech Stack:** Go 1.23+, `github.com/apple/pkl-go` v0.14.0, Pkl CLI 0.32.1 (via this repo's `flake.nix` — `nix develop`), Go standard library otherwise (no testify/afero/uber-go-mock).

## Global Constraints

- Module path: `github.com/jskswamy/cloudlab`.
- **Toolchain prerequisite: run everything in this plan inside `nix develop`** (from the repo root). It provides `go` and Pkl 0.32.1 — the Pkl CLI pkl-go 0.14.0's codegen requires (verified: pkl-go 0.14.0 needs Pkl >= 0.32.0; a bare `pkl` on `PATH` outside the flake may be older and will fail codegen with `Module 'pkl.go.gen' requires Pkl version 0.32.0 or higher`).
- **Sandboxed shells and `~/.pkl`:** the first `pkl` invocation on a machine writes to `~/.pkl/cache` (fetching and caching the `pkl.golang` package used by the schema's `@go.Package` annotation — a one-time, then-fully-offline cost). If a sandboxed Bash tool blocks writes to the home directory (`FileSystemException: /Users/.../.pkl: Operation not permitted`), that is an environment restriction, not a code bug — rerun the specific blocked command with the sandbox disabled for that one invocation.
- **Schema** (`pkl/Config.pkl`) field set, exact and final for this plan — do not add/rename/remove fields:
  ```pkl
  @go.Package { name = "github.com/jskswamy/cloudlab/internal/config" }
  module cloudlab.Config

  import "package://pkg.pkl-lang.org/pkl-go/pkl.golang@0.14.0#/go.pkl"

  basePath: String?
  region: String?
  size: String?
  template: String?
  sshKeys: Listing<String>?
  packages: Listing<String> = new Listing {}
  flakes: Listing<Flake> = new Listing {}

  class Flake {
    url: String
    packages: Listing<String>
  }
  ```
- **Generated Go types are fixed** (verified by actually running codegen against this exact schema — do not deviate):
  ```go
  type Config struct {
      BasePath *string  `pkl:"basePath"`
      Region   *string  `pkl:"region"`
      Size     *string  `pkl:"size"`
      Template *string  `pkl:"template"`
      SshKeys  *[]string `pkl:"sshKeys"`
      Packages []string  `pkl:"packages"`
      Flakes   []Flake   `pkl:"flakes"`
  }

  type Flake struct {
      Url      string   `pkl:"url"`
      Packages []string `pkl:"packages"`
  }
  ```
  `Packages`/`Flakes` default to an empty (non-nil) slice when unset in Pkl, never `nil`. Every other field is a pointer, `nil` when unset in Pkl.
- **A `.pkl` file must `amends` the schema to decode correctly.** Verified: a "duck-typed" module that only declares same-named properties (no `amends`) fails at evaluation with an opaque `expected array length N but got M` decode error, because `pkl-go`'s strict object mapping requires the module to actually extend the registered `cloudlab.Config` type. Every fixture and example in this plan amends `pkl/Config.pkl` (or a copy/reference to it) — never a bare, un-amended module.
- **A bare absolute filesystem path works directly in `amends "..."`** (verified — no `file://` prefix needed): `amends "/abs/path/to/Config.pkl"` resolves correctly regardless of the amending file's own location. This is how test fixtures (written to `t.TempDir()`, unrelated to the schema's location) reference the schema.
- **Merge semantics** (already locked by the design spec, do not change): scalars (`Region`/`Size`/`Template`) — project's value if set, else base's. Lists (`SshKeys`/`Packages`/`Flakes`) — additive, base's entries first, then project's.
- **`basePath` resolution order**: (1) project's `BasePath` if set — `~`/`~/...` expands via `os.UserHomeDir()`; an absolute path is used as-is; anything else resolves relative to the **project file's own directory** (`filepath.Dir(path)`), never the process's CWD. (2) Else `$XDG_CONFIG_HOME/cloudlab/base.pkl`. (3) Else `~/.config/cloudlab/base.pkl` (mirrors `internal/state.Open`'s XDG pattern — Linux-shaped on every OS, resolved by hand).
- **Base file existence is optional, not an error.** If the resolved base path doesn't exist (`os.IsNotExist`), the project file is used standalone — no error. Any other `os.Stat` error (e.g. permission denied) is a real error.
- Testing: stdlib `testing` only. No testify/afero/uber-go-mock anywhere in this plan.
- No CLI wiring. Nothing in `cmd/` changes in this plan.

---

### Task 1: Schema and codegen

**Files:**
- Create: `pkl/Config.pkl`
- Create: `internal/config/generate.go`
- Create (generated, do not hand-edit their contents): `internal/config/Config.pkl.go`, `internal/config/Flake.pkl.go`, `internal/config/init.pkl.go`
- Test: `internal/config/generate_test.go`

**Interfaces:**
- Produces: `config.Config` struct, `config.Flake` struct, `config.LoadFromPath(ctx context.Context, path string) (Config, error)` — all generated, exact shapes given in Global Constraints.

This task proves the codegen pipeline end-to-end: schema in, working generated Go out, loadable against a real fixture.

- [ ] **Step 1: Write the schema**

Create `pkl/Config.pkl` with exactly the content from Global Constraints above (copy it verbatim, including the `@go.Package` annotation and the full `package://` import — not the `@go/` alias, which requires a `PklProject` this repo deliberately doesn't have).

- [ ] **Step 2: Write the codegen directive**

Create `internal/config/generate.go`:

```go
// Package config loads and merges cloudlab.pkl declarative config files.
package config

//go:generate pkl run package://pkg.pkl-lang.org/pkl-go/pkl.golang@0.14.0#/gen.pkl ../../pkl/Config.pkl
```

- [ ] **Step 3: Write the failing smoke test**

Create `internal/config/generate_test.go`:

```go
package config

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// schemaPath returns the absolute path to pkl/Config.pkl regardless of
// the test binary's working directory.
func schemaPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine this test file's own path")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "pkl", "Config.pkl")
}

func TestLoadFromPath_MinimalFixture(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "cloudlab.pkl")
	content := "amends " + quotePklString(schemaPath(t)) + "\n\n" +
		"region = \"nyc3\"\n" +
		"packages {\n  \"ripgrep\"\n}\n"
	if err := os.WriteFile(fixture, []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	cfg, err := LoadFromPath(context.Background(), fixture)
	if err != nil {
		t.Fatalf("LoadFromPath() error = %v", err)
	}

	if cfg.Region == nil || *cfg.Region != "nyc3" {
		t.Errorf("Region = %v, want \"nyc3\"", cfg.Region)
	}
	if cfg.Size != nil {
		t.Errorf("Size = %v, want nil", cfg.Size)
	}
	if len(cfg.Packages) != 1 || cfg.Packages[0] != "ripgrep" {
		t.Errorf("Packages = %v, want [ripgrep]", cfg.Packages)
	}
	if cfg.Flakes == nil || len(cfg.Flakes) != 0 {
		t.Errorf("Flakes = %v, want empty non-nil slice", cfg.Flakes)
	}
}

// quotePklString renders s as a Pkl double-quoted string literal. Test
// fixture paths are plain filesystem paths (no quotes/backslashes to
// escape), so a bare wrap is sufficient here.
func quotePklString(s string) string {
	return "\"" + s + "\""
}
```

- [ ] **Step 4: Run test to verify it fails**

Run (inside `nix develop`): `go test ./internal/config/... -run TestLoadFromPath_MinimalFixture -v`
Expected: FAIL — compile error, `undefined: LoadFromPath` (no generated code exists yet).

- [ ] **Step 5: Run codegen**

Run (inside `nix develop`, from the repo root): `go generate ./internal/config/...`

This produces `internal/config/Config.pkl.go`, `internal/config/Flake.pkl.go`, and `internal/config/init.pkl.go`. Open each and confirm the struct shapes match Global Constraints exactly (field names, `pkl:"..."` tags, pointer vs. value types). If they don't match — e.g. a future Pkl/pkl-go version changes codegen output — stop and reconcile with a human before continuing; every later task's code assumes these exact shapes.

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/config/... -run TestLoadFromPath_MinimalFixture -v`
Expected: PASS. (First run on a machine that has never invoked `pkl` before will also fetch the `pkl.golang` package to `~/.pkl/cache` — needs network access once; every run after is offline. If this fails with `~/.pkl: Operation not permitted` in a sandboxed shell, see Global Constraints.)

- [ ] **Step 7: Commit**

```bash
git add pkl/Config.pkl internal/config/generate.go internal/config/Config.pkl.go internal/config/Flake.pkl.go internal/config/init.pkl.go internal/config/generate_test.go
git commit -m "Add cloudlab.pkl schema and generated Go bindings"
```

---

### Task 2: Base-path resolution

**Files:**
- Create: `internal/config/basepath.go`
- Test: `internal/config/basepath_test.go`

**Interfaces:**
- Consumes: nothing from Task 1 (pure Go, no Pkl evaluation).
- Produces: `resolveBasePath(projectPath string, override *string) (string, error)` — used by `Resolve` in Task 3.

This task has no dependency on `pkl` at all — pure `os`/`path/filepath` logic, fast to test.

- [ ] **Step 1: Write the failing tests**

Create `internal/config/basepath_test.go`:

```go
package config

import (
	"path/filepath"
	"testing"
)

func TestResolveBasePath_NoOverride_UsesXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/config")

	got, err := resolveBasePath("/repo/cloudlab.pkl", nil)
	if err != nil {
		t.Fatalf("resolveBasePath() error = %v", err)
	}

	want := filepath.Join("/xdg/config", "cloudlab", "base.pkl")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveBasePath_NoOverride_NoXDG_UsesHomeConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/alice")

	got, err := resolveBasePath("/repo/cloudlab.pkl", nil)
	if err != nil {
		t.Fatalf("resolveBasePath() error = %v", err)
	}

	want := filepath.Join("/home/alice", ".config", "cloudlab", "base.pkl")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveBasePath_OverrideAbsolute_UsedAsIs(t *testing.T) {
	override := "/custom/base.pkl"

	got, err := resolveBasePath("/repo/cloudlab.pkl", &override)
	if err != nil {
		t.Fatalf("resolveBasePath() error = %v", err)
	}

	if got != override {
		t.Errorf("got %q, want %q", got, override)
	}
}

func TestResolveBasePath_OverrideHomeRelative_Expands(t *testing.T) {
	t.Setenv("HOME", "/home/alice")
	override := "~/work-base.pkl"

	got, err := resolveBasePath("/repo/cloudlab.pkl", &override)
	if err != nil {
		t.Fatalf("resolveBasePath() error = %v", err)
	}

	want := filepath.Join("/home/alice", "work-base.pkl")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveBasePath_OverrideRelative_ResolvesAgainstProjectDir(t *testing.T) {
	override := "./base.pkl"

	got, err := resolveBasePath("/repo/nested/cloudlab.pkl", &override)
	if err != nil {
		t.Fatalf("resolveBasePath() error = %v", err)
	}

	want := filepath.Join("/repo/nested", "base.pkl")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveBasePath_OverrideEmptyString_TreatedAsUnset(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/config")
	override := ""

	got, err := resolveBasePath("/repo/cloudlab.pkl", &override)
	if err != nil {
		t.Fatalf("resolveBasePath() error = %v", err)
	}

	want := filepath.Join("/xdg/config", "cloudlab", "base.pkl")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/... -run TestResolveBasePath -v`
Expected: FAIL — compile error, `undefined: resolveBasePath`.

- [ ] **Step 3: Write the minimal implementation**

Create `internal/config/basepath.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
)

// resolveBasePath determines the personal base config's path for a
// project file at projectPath. Resolution order: override (if set and
// non-empty) — ~-expanded if home-relative, used as-is if absolute,
// else resolved relative to projectPath's own directory; else
// $XDG_CONFIG_HOME/cloudlab/base.pkl; else ~/.config/cloudlab/base.pkl.
func resolveBasePath(projectPath string, override *string) (string, error) {
	if override != nil && *override != "" {
		return resolveOverridePath(filepath.Dir(projectPath), *override)
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "cloudlab", "base.pkl"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "cloudlab", "base.pkl"), nil
}

func resolveOverridePath(projectDir, override string) (string, error) {
	if override == "~" || strings.HasPrefix(override, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(override, "~")), nil
	}
	if filepath.IsAbs(override) {
		return override, nil
	}
	return filepath.Join(projectDir, override), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/... -run TestResolveBasePath -v`
Expected: PASS (all six subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/config/basepath.go internal/config/basepath_test.go
git commit -m "Add personal base config path resolution"
```

---

### Task 3: Resolve — merge and validation

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `config.LoadFromPath` (Task 1), `resolveBasePath` (Task 2), and two test-only helpers already defined in `internal/config/generate_test.go` from Task 1 — `schemaPath(t *testing.T) string` and `quotePklString(s string) string`. `config_test.go`'s `writeFixture` helper (Step 1 below) calls both directly; do not redefine them.
- Produces: `config.Resolve(ctx context.Context, path string) (Config, error)` — the package's public entry point.

- [ ] **Step 1: Write the failing tests**

Create `internal/config/config_test.go`:

```go
package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("amends "+quotePklString(schemaPath(t))+"\n\n"+body), 0o644); err != nil {
		t.Fatalf("writing fixture %s: %v", path, err)
	}
}

func TestLoad_SelfContainedProjectFile(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, project, "region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\ntemplate = \"python\"\n")

	// No base file at the XDG default location for this test's HOME.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Region == nil || *cfg.Region != "nyc3" {
		t.Errorf("Region = %v, want nyc3", cfg.Region)
	}
	if cfg.Size == nil || *cfg.Size != "s-1vcpu-1gb" {
		t.Errorf("Size = %v, want s-1vcpu-1gb", cfg.Size)
	}
	if cfg.Template == nil || *cfg.Template != "python" {
		t.Errorf("Template = %v, want python", cfg.Template)
	}
}

func TestLoad_MergesWithBase_ScalarsOverrideListsAdditive(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.pkl")
	writeFixture(t, base, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`sshKeys { "base-key" }`,
		`packages { "git" }`,
	}, "\n")+"\n")

	project := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, project, strings.Join([]string{
		`basePath = "./base.pkl"`,
		`size = "s-2vcpu-4gb"`, // overrides base
		`template = "python"`,
		`sshKeys { "project-key" }`,
		`packages { "ripgrep" }`,
	}, "\n")+"\n")

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if cfg.Region == nil || *cfg.Region != "nyc3" {
		t.Errorf("Region = %v, want nyc3 (from base)", cfg.Region)
	}
	if cfg.Size == nil || *cfg.Size != "s-2vcpu-4gb" {
		t.Errorf("Size = %v, want s-2vcpu-4gb (project overrides base)", cfg.Size)
	}
	if cfg.Template == nil || *cfg.Template != "python" {
		t.Errorf("Template = %v, want python", cfg.Template)
	}

	wantKeys := []string{"base-key", "project-key"}
	if cfg.SshKeys == nil || !equalStrings(*cfg.SshKeys, wantKeys) {
		t.Errorf("SshKeys = %v, want %v (base then project)", cfg.SshKeys, wantKeys)
	}

	wantPackages := []string{"git", "ripgrep"}
	if !equalStrings(cfg.Packages, wantPackages) {
		t.Errorf("Packages = %v, want %v (base then project)", cfg.Packages, wantPackages)
	}
}

func TestLoad_MissingBaseFile_ProjectStandalone(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, project, strings.Join([]string{
		`basePath = "./does-not-exist.pkl"`,
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
	}, "\n")+"\n")

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Region == nil || *cfg.Region != "nyc3" {
		t.Errorf("Region = %v, want nyc3", cfg.Region)
	}
}

func TestLoad_MissingRequiredFieldAfterMerge_ReturnsClearError(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, project, `region = "nyc3"`+"\n") // size, template never set

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

	_, err := Resolve(context.Background(), project)
	if err == nil {
		t.Fatal("Resolve() error = nil, want error naming missing fields")
	}
	for _, want := range []string{"size", "template"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention missing field %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "region") {
		t.Errorf("error %q mentions region, which was set", err.Error())
	}
}

func TestLoad_MissingProjectFile_ReturnsNotFoundError(t *testing.T) {
	dir := t.TempDir()
	_, err := Resolve(context.Background(), filepath.Join(dir, "no-such-file.pkl"))
	if err == nil {
		t.Fatal("Resolve() error = nil, want error")
	}
}

func TestLoad_MalformedProjectFile_ReturnsEvaluationError(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "cloudlab.pkl")
	if err := os.WriteFile(project, []byte("this is not valid pkl {{{"), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	_, err := Resolve(context.Background(), project)
	if err == nil {
		t.Fatal("Resolve() error = nil, want evaluation error")
	}
}

func TestLoad_BasePathOverride_PointsAtNonDefaultLocation(t *testing.T) {
	dir := t.TempDir()
	custom := filepath.Join(dir, "custom-base.pkl")
	writeFixture(t, custom, `region = "sfo3"`+"\n")

	project := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, project, strings.Join([]string{
		`basePath = ` + quotePklString(custom),
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
	}, "\n")+"\n")

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Region == nil || *cfg.Region != "sfo3" {
		t.Errorf("Region = %v, want sfo3 (from custom base)", cfg.Region)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/... -run TestLoad_ -v`
Expected: FAIL — compile error, `undefined: Load`.

- [ ] **Step 3: Write the minimal implementation**

Create `internal/config/config.go`:

```go
package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Resolve loads the project's cloudlab.pkl at path, merges it with a
// personal base config if one is found (see resolveBasePath), and
// returns the merged Config. Scalar fields take the project's value if
// set, else the base's; list fields are additive (base's entries
// first, then the project's). Returns an error naming any of
// region/size/template still unset after merging.
//
// Named Resolve, not Load, because the generated Config.pkl.go already
// defines a lower-level Load(ctx, evaluator, source) — this function
// builds on LoadFromPath (also generated) instead.
func Resolve(ctx context.Context, path string) (Config, error) {
	project, err := LoadFromPath(ctx, path)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return Config{}, fmt.Errorf("pkl CLI not found on PATH (run inside `nix develop`, or install it: https://pkl-lang.org/main/current/pkl-cli/index.html#installation): %w", err)
		}
		return Config{}, fmt.Errorf("loading %s: %w", path, err)
	}

	basePath, err := resolveBasePath(path, project.BasePath)
	if err != nil {
		return Config{}, fmt.Errorf("resolving base config path: %w", err)
	}

	merged := project
	if _, statErr := os.Stat(basePath); statErr == nil {
		base, err := LoadFromPath(ctx, basePath)
		if err != nil {
			return Config{}, fmt.Errorf("loading base config %s: %w", basePath, err)
		}
		merged = mergeConfig(base, project)
	} else if !os.IsNotExist(statErr) {
		return Config{}, fmt.Errorf("checking base config %s: %w", basePath, statErr)
	}

	if err := validate(merged); err != nil {
		return Config{}, err
	}
	return merged, nil
}

// mergeConfig layers project over base: scalars use project's value if
// set, else base's; lists are additive, base's entries first.
func mergeConfig(base, project Config) Config {
	return Config{
		Region:   coalesce(project.Region, base.Region),
		Size:     coalesce(project.Size, base.Size),
		Template: coalesce(project.Template, base.Template),
		SshKeys:  mergeStringSlicePtrs(base.SshKeys, project.SshKeys),
		Packages: append(append([]string{}, base.Packages...), project.Packages...),
		Flakes:   append(append([]Flake{}, base.Flakes...), project.Flakes...),
	}
}

func coalesce(project, base *string) *string {
	if project != nil {
		return project
	}
	return base
}

func mergeStringSlicePtrs(base, project *[]string) *[]string {
	if base == nil && project == nil {
		return nil
	}
	merged := []string{}
	if base != nil {
		merged = append(merged, *base...)
	}
	if project != nil {
		merged = append(merged, *project...)
	}
	return &merged
}

func validate(c Config) error {
	var missing []string
	if c.Region == nil {
		missing = append(missing, "region")
	}
	if c.Size == nil {
		missing = append(missing, "size")
	}
	if c.Template == nil {
		missing = append(missing, "template")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required field(s) after merging project and base config: %s", strings.Join(missing, ", "))
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/... -v`
Expected: PASS — every test in the package, including Task 1's and Task 2's.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "Add Resolve: merge project config with personal base"
```

---

### Task 4: Documentation and worked examples

**Files:**
- Create: `docs/config.md`
- Create: `docs/examples/minimal/cloudlab.pkl`
- Create: `docs/examples/with-base/base.pkl`
- Create: `docs/examples/with-base/cloudlab.pkl`
- Test: `internal/config/examples_test.go`

**Interfaces:**
- Consumes: `config.Resolve` (Task 3), and the test-only helper `equalStrings(got, want []string) bool` already defined in `internal/config/config_test.go` from Task 3 — `examples_test.go`'s second test calls it directly; do not redefine it.
- Produces: nothing new for other packages — this task's deliverable is documentation, validated by a test that actually loads it.

- [ ] **Step 1: Write the minimal example**

Create `docs/examples/minimal/cloudlab.pkl`:

```pkl
amends "../../../pkl/Config.pkl"

region = "nyc3"
size = "s-1vcpu-1gb"
template = "python"
sshKeys {
  "AAAA...my-key-fingerprint"
}
packages {
  "ripgrep"
  "jq"
}
```

- [ ] **Step 2: Write the base+override pair**

Create `docs/examples/with-base/base.pkl`:

```pkl
amends "../../../pkl/Config.pkl"

// A personal base config — not committed to any individual project's
// repo. Lives at $XDG_CONFIG_HOME/cloudlab/base.pkl (or
// ~/.config/cloudlab/base.pkl) on a real machine; this copy exists
// only to demonstrate the merge pattern.
size = "s-1vcpu-1gb"
sshKeys {
  "AAAA...my-key-fingerprint"
}
packages {
  "git"
  "ripgrep"
}
```

Create `docs/examples/with-base/cloudlab.pkl`:

```pkl
amends "../../../pkl/Config.pkl"

// Points at the sibling base.pkl instead of the real XDG default, so
// this example is self-contained and doesn't depend on anything
// outside this directory. A real project typically omits basePath
// entirely and relies on the XDG default.
basePath = "./base.pkl"

region = "nyc3"
template = "python"
// size comes from base.pkl (not overridden here).
// packages merges additively: base's [git, ripgrep] + this file's [jq].
packages {
  "jq"
}
```

- [ ] **Step 3: Write the failing test verifying both examples load correctly**

Create `internal/config/examples_test.go`:

```go
package config

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine this test file's own path")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

func TestExamples_Minimal_LoadsCleanly(t *testing.T) {
	path := filepath.Join(repoRoot(t), "docs", "examples", "minimal", "cloudlab.pkl")

	cfg, err := Resolve(context.Background(), path)
	if err != nil {
		t.Fatalf("Resolve(%s) error = %v", path, err)
	}
	if cfg.Region == nil || *cfg.Region != "nyc3" {
		t.Errorf("Region = %v, want nyc3", cfg.Region)
	}
	if cfg.Template == nil || *cfg.Template != "python" {
		t.Errorf("Template = %v, want python", cfg.Template)
	}
}

func TestExamples_WithBase_MergesCleanly(t *testing.T) {
	path := filepath.Join(repoRoot(t), "docs", "examples", "with-base", "cloudlab.pkl")

	cfg, err := Resolve(context.Background(), path)
	if err != nil {
		t.Fatalf("Resolve(%s) error = %v", path, err)
	}
	if cfg.Region == nil || *cfg.Region != "nyc3" {
		t.Errorf("Region = %v, want nyc3 (from project)", cfg.Region)
	}
	if cfg.Size == nil || *cfg.Size != "s-1vcpu-1gb" {
		t.Errorf("Size = %v, want s-1vcpu-1gb (from base)", cfg.Size)
	}
	wantPackages := []string{"git", "ripgrep", "jq"}
	if !equalStrings(cfg.Packages, wantPackages) {
		t.Errorf("Packages = %v, want %v (base then project)", cfg.Packages, wantPackages)
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `go test ./internal/config/... -run TestExamples -v`
Expected: FAIL — `no such file or directory` for `docs/examples/minimal/cloudlab.pkl` (not yet created if Steps 1-2 were skipped) or a Pkl relative-import error if the `amends` path is wrong. Confirm the failure is specifically about the file/path, not a logic bug, before moving on.

If Steps 1-2 were already done (as written above), this step instead confirms the examples are *correct*: run it once before Step 5 to catch a wrong `amends` path or a typo'd field name immediately, rather than assuming the hand-written `.pkl` content is right.

- [ ] **Step 5: Fix any path or content errors surfaced by Step 4, then run again**

Run: `go test ./internal/config/... -run TestExamples -v`
Expected: PASS.

- [ ] **Step 6: Write the foundational config doc**

Create `docs/config.md`:

```markdown
# Configuring cloudlab: `cloudlab.pkl`

cloudlab reads a declarative config file, `cloudlab.pkl`, written in
[Pkl](https://pkl-lang.org) — a typed, validated alternative to YAML.
This doc explains every field, how personal defaults are reused across
projects, and what happens when something's missing or wrong.

If you've never used Pkl before: it looks like a typed config file
(`key = value`), not a programming language. You don't need to learn
Pkl deeply to write a `cloudlab.pkl` — the two worked examples in
`docs/examples/` cover the common cases.

## Fields

| Field | Type | Required? | Default | Meaning |
|---|---|---|---|---|
| `region` | `String?` | Yes, after merge | none | DigitalOcean region slug, e.g. `"nyc3"`. Maps directly to the `Region` field `Provider.Create` sends. |
| `size` | `String?` | Yes, after merge | none | DigitalOcean droplet size slug, e.g. `"s-1vcpu-1gb"`. Maps directly to `Provider.Create`'s `Size`. |
| `template` | `String?` | Yes, after merge | none | Provisioning template name. The template catalog itself (what each name actually installs) is a separate, later feature — for now this is just a name cloudlab passes through. |
| `sshKeys` | `Listing<String>?` | No | none | SSH key IDs/fingerprints already registered with your provider. |
| `packages` | `Listing<String>` | No | empty | Nix packages to install on the instance. |
| `flakes` | `Listing<Flake>` (`{url, packages}`) | No | empty | Nix flakes to install, each with its own package list. |
| `basePath` | `String?` | No | none | Overrides where cloudlab looks for your personal base config (see below). |

"Required, after merge" means: `region`/`size`/`template` don't have to
be set in your project's `cloudlab.pkl` itself, as long as your
personal base config supplies them (or vice versa) — see below. If
neither file sets one, `Load` fails with an error naming exactly which
field is still missing.

## Personal base config and reuse across projects

Most of your `cloudlab.pkl` settings — your SSH key, your usual droplet
size, packages you always want — don't change per-project. Instead of
repeating them in every repo, put them once in a personal base config:

- Default location: `$XDG_CONFIG_HOME/cloudlab/base.pkl`, or
  `~/.config/cloudlab/base.pkl` if `XDG_CONFIG_HOME` isn't set.
- Not committed anywhere — it's yours, local to your machine.
- Written exactly like a project `cloudlab.pkl` (same schema, same
  `amends` line).

When cloudlab loads a project's `cloudlab.pkl`, it also checks for your
base config. If one exists, the two are merged:

- **Scalars** (`region`, `size`, `template`): the project's value wins
  if it set one; otherwise the base's value is used.
- **Lists** (`sshKeys`, `packages`, `flakes`): additive — your base's
  entries first, then the project's. Nothing is dropped from either
  side.

If your base config doesn't exist yet, this isn't an error — your
project's `cloudlab.pkl` is used on its own, and any field it doesn't
set is simply missing (which fails validation if it's one of the three
required ones).

### Pointing at a different base file

Set `basePath` in your project's `cloudlab.pkl` to use something other
than the default location:

```pkl
basePath = "~/.config/cloudlab/work-base.pkl"  // a different personal file
basePath = "./team-base.pkl"                    // a file checked into this repo, next to cloudlab.pkl
```

A `~`-prefixed path expands to your home directory. An absolute path is
used as-is. Anything else is resolved relative to the project file's
own directory — not wherever you happen to run `cloudlab` from — so a
relative `basePath` always means "the file next to this one."

## Writing your `cloudlab.pkl`

Every `cloudlab.pkl` must start with an `amends` line pointing at
cloudlab's schema — this is what gives you typed fields and lets
cloudlab validate the file. See `docs/examples/minimal/cloudlab.pkl`
for the simplest possible file, and `docs/examples/with-base/` for the
base-merge pattern (a `base.pkl` and a `cloudlab.pkl` that merges with
it).

> **A note on the `amends` path in these examples:** they point at
> `pkl/Config.pkl` inside *this* repo (cloudlab's own source), because
> that's where the examples live. A `cloudlab.pkl` in your own,
> separate project repo can't use that same relative path — it needs a
> stable reference to cloudlab's schema instead (for example, a
> version-pinned URL once cloudlab has tagged releases). That
> distribution mechanism isn't settled yet; treat the `amends` path in
> your own repo's `cloudlab.pkl` as the one detail these examples don't
> demonstrate correctly for you to copy verbatim.

## Errors you might see

- **"missing required field(s) after merging project and base config: size, template"** — `region`/`size`/`template` weren't set in either your project file or your base config. Set them in one or the other.
- **"pkl CLI not found on PATH"** — install Pkl, or run inside this repo's `nix develop` shell if you're working on cloudlab itself.
- A Pkl evaluation error (malformed file, wrong type for a field) is passed through with the file path it came from — Pkl's own error message names the exact line and problem.
```

- [ ] **Step 7: Commit**

```bash
git add docs/config.md docs/examples/minimal/cloudlab.pkl docs/examples/with-base/base.pkl docs/examples/with-base/cloudlab.pkl internal/config/examples_test.go
git commit -m "Add config.md and worked examples for cloudlab.pkl"
```

---

## Final check

Run the full package suite once more from the repo root (inside `nix develop`):

```bash
go build ./...
go test ./... -v
```

Expected: everything builds, every test across the repo passes (this plan's `internal/config` tests plus the untouched Foundation/Provider-layer tests).
