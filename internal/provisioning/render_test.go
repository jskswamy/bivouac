package provisioning

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/config"
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
	out, err := Render(cfg, "github:jskswamy/bivouac?dir=templates#python-x86_64-linux")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(out, `template.url = "github:jskswamy/bivouac?dir=templates"`) {
		t.Errorf("output does not reference the template flake url:\n%s", out)
	}
	if !strings.Contains(out, "template.homeManagerModules.\"python\"") {
		t.Errorf("output does not import the template's homeManagerModules:\n%s", out)
	}
}

func TestRender_WithPackages_AddsSyntheticModule(t *testing.T) {
	cfg := config.Config{Arch: "x86_64", Packages: []string{"ripgrep", "jq"}}
	out, err := Render(cfg, "github:jskswamy/bivouac?dir=templates#python-x86_64-linux")
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
	if strings.Contains(out, "flake0.homeManagerModules") {
		t.Errorf("output references flake0.homeManagerModules with no modules configured:\n%s", out)
	}
}

func TestRender_PackageWithEmbeddedQuote_Rejected(t *testing.T) {
	cfg := config.Config{Arch: "x86_64", Packages: []string{`ripgrep"; malicious = true; "`}}
	_, err := Render(cfg, "github:jskswamy/bivouac?dir=templates#python-x86_64-linux")
	if err == nil {
		t.Fatal("Render() error = nil, want error for package name with embedded quote")
	}
}

func TestRender_PackageWithNixInterpolation_Rejected(t *testing.T) {
	cfg := config.Config{Arch: "x86_64", Packages: []string{"${builtins.trace \"pwned\" null}"}}
	_, err := Render(cfg, "github:jskswamy/bivouac?dir=templates#python-x86_64-linux")
	if err == nil {
		t.Fatal("Render() error = nil, want error for package name with Nix string interpolation")
	}
}

func TestRender_FlakeURLWithEmbeddedQuote_Rejected(t *testing.T) {
	cfg := config.Config{
		Arch:   "x86_64",
		Flakes: []config.Flake{{Url: `github:someorg/tool"; malicious = true; "`}},
	}
	_, err := Render(cfg, "github:jskswamy/bivouac?dir=templates#python-x86_64-linux")
	if err == nil {
		t.Fatal("Render() error = nil, want error for flake url with embedded quote")
	}
}

func TestRender_FlakePackageWithEmbeddedQuote_Rejected(t *testing.T) {
	cfg := config.Config{
		Arch: "x86_64",
		Flakes: []config.Flake{
			{Url: "github:someorg/custom-tool", Packages: []string{`cli"; malicious = true; "`}},
		},
	}
	_, err := Render(cfg, "github:jskswamy/bivouac?dir=templates#python-x86_64-linux")
	if err == nil {
		t.Fatal("Render() error = nil, want error for flake package name with embedded quote")
	}
}

func TestRender_TemplateNameWithEmbeddedQuote_Rejected(t *testing.T) {
	cfg := config.Config{Arch: "x86_64"}
	_, err := Render(cfg, `github:jskswamy/bivouac?dir=templates#python"; malicious = true; "`)
	if err == nil {
		t.Fatal("Render() error = nil, want error for template name with embedded quote")
	}
}

func TestRender_ValidValues_StillRender(t *testing.T) {
	cfg := config.Config{
		Arch:     "x86_64",
		Packages: []string{"ripgrep", "python3.11", "gcc-arm-embedded", "nodejs_22"},
		Flakes: []config.Flake{
			{Url: "github:someorg/custom-tool?ref=main", Packages: []string{"cli"}, Modules: []string{"tools.git"}},
		},
	}
	if _, err := Render(cfg, "github:jskswamy/bivouac?dir=templates#python-x86_64-linux"); err != nil {
		t.Fatalf("Render() error = %v, want nil for legitimate values", err)
	}
}

func TestRender_Output_EvaluatesInNix(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine this test file's own path")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	templatesRef := "path:" + filepath.Join(repoRoot, "templates")

	// Settings covers every value type the schema's union allows, each
	// aimed at a real home-manager option, so the synthetic module has
	// to both parse and type-check against the actual option set. Lists
	// are []interface{} because that is what pkl-go decodes them to.
	cfg := config.Config{
		Arch:     "x86_64",
		Packages: []string{"hello"},
		Settings: map[string]any{
			"home.sessionVariables.BIVOUAC_STR": "x",
			"home.sessionVariables.BIVOUAC_INT": 42,
			"programs.jq.enable":                true,
			"programs.git.ignores":              []interface{}{"*.swp"},
		},
	}
	out, err := Render(cfg, templatesRef+"#python-x86_64-linux")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	// nix only evaluates git-staged/tracked files, even for a flake
	// nested in a fresh temp dir — same gotcha as templates/testdata.
	for _, args := range [][]string{{"init"}, {"add", "."}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// --impure: common.nix reads $USER/$HOME via builtins.getEnv for
	// home.username/homeDirectory, since the actual instance user
	// varies per instance (see internal/reconcile/reconcile.go for the
	// same flag on the real switch invocation). Left at the ambient
	// environment's real values -- Nix's own tooling needs a real,
	// existing $HOME for its own git/cache lookups while fetching
	// flake inputs, so this can't be overridden to a fake path.
	cmd := exec.Command("nix", "eval", "--impure", "path:"+dir+"#homeConfigurations.default.activationPackage.drvPath")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rendered flake failed to evaluate:\n%s", out)
	}
}

// TestTemplates_AllBuildCleanly actually *builds* every built-in
// template's own homeConfiguration directly (not through Render's
// wrapper flake, and not just `nix eval ...drvPath`, which only
// resolves the derivation path without realizing it). Catches
// package-set problems -- like two packages providing the same file
// path (a buildEnv conflict) -- that only surface when the derivation
// is actually built, which only the *template's* own package list can
// have, independent of any user packages/flakes.
func TestTemplates_AllBuildCleanly(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("templates target x86_64-linux; building one requires a Linux nix builder")
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine this test file's own path")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	templatesDir := filepath.Join(repoRoot, "templates")

	// --impure: common.nix reads $USER/$HOME via builtins.getEnv for
	// home.username/homeDirectory, since the actual instance user
	// varies per instance. Left at the ambient environment's real
	// values -- see TestRender_Output_EvaluatesInNix.
	for name := range builtinTemplates {
		t.Run(name, func(t *testing.T) {
			attr := name + "-" + NixSystem("x86_64")
			cmd := exec.Command("nix", "build", "--impure", "--no-link",
				"path:"+templatesDir+"#homeConfigurations."+attr+".activationPackage")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("template %q failed to build:\n%s", name, out)
			}
		})
	}
}

// bivouac.tailscale is set only by the rendered wrapper flake, and the
// shared template defaults it to false. A config that asks for Tailscale
// but has no packages and no flakes must therefore still render, or the
// flag silently does nothing and the tailscaled unit is never installed.
func TestNeedsRender_TrueForTailscaleAlone(t *testing.T) {
	cfg := config.Config{Tailscale: true}
	if !NeedsRender(cfg) {
		t.Error("NeedsRender(tailscale-only config) = false, want true — otherwise bivouac.tailscale is never set")
	}
}

// The rendered wrapper flake is the only place this option is ever set
// (see render.go's renderTmpl and NeedsRender doc comment) -- a
// half-renamed option here would either fail to evaluate on the instance
// or, worse, silently do nothing because the shared module's own default
// stands. This pins the rendered output to the new namespace.
func TestRender_SetsTailscaleOptionUnderBivouacNamespace(t *testing.T) {
	cfg := config.Config{Tailscale: true}
	out, err := Render(cfg, "github:jskswamy/bivouac?dir=templates#python-x86_64-linux")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(out, "bivouac.tailscale = true;") {
		t.Errorf("rendered flake does not set bivouac.tailscale:\n%s", out)
	}
	if strings.Contains(out, "cloudlab.tailscale") {
		t.Errorf("rendered flake still sets cloudlab.tailscale:\n%s", out)
	}
}

func TestNeedsRender_TrueForAgentsAlone(t *testing.T) {
	if !NeedsRender(config.Config{Agents: []string{"codex"}}) {
		t.Error("NeedsRender(agents-only config) = false, want true")
	}
}

// The nixpkgs attribute is not the tool's name: pi ships as
// pi-coding-agent and claude as claude-code, which is the whole reason
// agents is a curated list rather than free-form packages entries.
func TestRender_Agents_MapToNixpkgsAttributeNames(t *testing.T) {
	cfg := config.Config{Arch: "x86_64", Agents: []string{"claude", "codex", "copilot", "cursor", "opencode", "pi"}}
	out, err := Render(cfg, "github:jskswamy/bivouac?dir=templates#docker-x86_64-linux")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	for _, want := range []string{`pkgs."claude-code"`, `pkgs."codex"`, `pkgs."github-copilot-cli"`, `pkgs."cursor-cli"`, `pkgs."opencode"`, `pkgs."pi-coding-agent"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, `pkgs."pi"`) || strings.Contains(out, `pkgs."claude"`) || strings.Contains(out, `pkgs."cursor"`) || strings.Contains(out, `pkgs."copilot"`) {
		t.Errorf("output references a short name rather than the nixpkgs attribute:\n%s", out)
	}
}

// Selecting an unfree agent must permit exactly that package. Switching
// allowUnfree on wholesale would also quietly permit anything the user
// happened to list in packages, which they never asked for.
func TestRender_UnfreeAgent_PermitsOnlyThatPackage(t *testing.T) {
	cfg := config.Config{Arch: "x86_64", Agents: []string{"claude", "codex"}}
	out, err := Render(cfg, "github:jskswamy/bivouac?dir=templates#docker-x86_64-linux")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(out, `allowUnfreePredicate = pkg: builtins.elem (nixpkgs.lib.getName pkg) [ "claude-code" ]`) {
		t.Errorf("output does not permit exactly claude-code:\n%s", out)
	}
	if strings.Contains(out, "allowUnfree = true") {
		t.Errorf("output allows unfree wholesale rather than per-package:\n%s", out)
	}
}

func TestRender_NoUnfreeAgent_PermitsNothingUnfree(t *testing.T) {
	cfg := config.Config{Arch: "x86_64", Agents: []string{"codex", "opencode"}}
	out, err := Render(cfg, "github:jskswamy/bivouac?dir=templates#docker-x86_64-linux")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(out, `getName pkg) [ ]`) {
		t.Errorf("output should permit an empty set of unfree packages:\n%s", out)
	}
}

// Pkl's union type rejects unknown names first, so reaching Render with
// one means the schema and the mapping have drifted -- louder is better
// than installing nothing while reporting success.
func TestRender_UnknownAgent_Rejected(t *testing.T) {
	cfg := config.Config{Arch: "x86_64", Agents: []string{"definitely-not-an-agent"}}
	if _, err := Render(cfg, "github:jskswamy/bivouac?dir=templates#docker-x86_64-linux"); err == nil {
		t.Fatal("Render() error = nil, want an error for an unknown agent")
	}
}

// Several agents are unfree, and every one selected must be named in the
// predicate -- a missing entry fails the build only once that package is
// reached, long after render looked fine.
func TestRender_MultipleUnfreeAgents_AllPermitted(t *testing.T) {
	cfg := config.Config{Arch: "x86_64", Agents: []string{"claude", "cursor", "copilot", "codex"}}
	out, err := Render(cfg, "github:jskswamy/bivouac?dir=templates#docker-x86_64-linux")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	for _, want := range []string{`"claude-code"`, `"cursor-cli"`, `"github-copilot-cli"`} {
		if !strings.Contains(out, "getName pkg) [ ") || !strings.Contains(out, want) {
			t.Errorf("unfree predicate does not permit %s:\n%s", want, out)
		}
	}
	// codex is free and must not be listed as needing permission.
	predicate := out[strings.Index(out, "allowUnfreePredicate"):]
	predicate = predicate[:strings.Index(predicate, "\n")]
	if strings.Contains(predicate, "codex") {
		t.Errorf("free package listed in the unfree predicate: %s", predicate)
	}
}

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

func TestRender_WithSettings_AddsSyntheticModule(t *testing.T) {
	cfg := config.Config{
		Arch:     "x86_64",
		Settings: map[string]any{"programs.git.userEmail": "work@example.com"},
	}
	out, err := Render(cfg, "github:jskswamy/bivouac?dir=templates#python-x86_64-linux")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(out, `{ "programs"."git"."userEmail" = "work@example.com"; }`) {
		t.Errorf("output does not set the settings override:\n%s", out)
	}
}

func TestRender_NoSettings_OmitsSettingsModule(t *testing.T) {
	cfg := config.Config{Arch: "x86_64"}
	out, err := Render(cfg, "github:jskswamy/bivouac?dir=templates#python-x86_64-linux")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
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

// A trailing backslash escapes the closing quote Render adds, so the
// value runs on into the rest of the module -- as much a way out of the
// literal as an embedded '"'.
func TestNixValue_StringWithTrailingBackslash_Rejected(t *testing.T) {
	if _, err := nixValue(`C:\`); err == nil {
		t.Fatal("nixValue() error = nil, want error for a backslash escaping the closing quote")
	}
}

func TestNixValue_ListElementWithBackslash_Rejected(t *testing.T) {
	if _, err := nixValue([]interface{}{`a\`}); err == nil {
		t.Fatal("nixValue() error = nil, want error for a backslash in a list element")
	}
}

// The charset alone admits '.', so a path with an empty segment would
// slip through and render as a quoted empty attribute name ("a".""."b").
func TestRender_DottedPathWithEmptySegment_Rejected(t *testing.T) {
	for _, path := range []string{"", ".git", "git.", "tools..git"} {
		settings := config.Config{Arch: "x86_64", Settings: map[string]any{path: true}}
		if _, err := Render(settings, "github:jskswamy/bivouac?dir=templates#python-x86_64-linux"); err == nil {
			t.Errorf("Render() with settings key %q: error = nil, want error", path)
		}
		modules := config.Config{Arch: "x86_64", Flakes: []config.Flake{{Url: "github:o/r", Modules: []string{path}}}}
		if _, err := Render(modules, "github:jskswamy/bivouac?dir=templates#python-x86_64-linux"); err == nil {
			t.Errorf("Render() with module path %q: error = nil, want error", path)
		}
	}
}

// A Config built in Go rather than decoded by pkl-go carries a list
// setting as []string; the config writer already accepts that shape,
// so Render must too or the two disagree on what a valid setting is.
func TestNixValue_PlainStringSlice(t *testing.T) {
	got, err := nixValue([]string{"x", "y"})
	if err != nil {
		t.Fatalf("nixValue() error = %v", err)
	}
	if got != `[ "x" "y" ]` {
		t.Errorf("nixValue() = %q, want %q", got, `[ "x" "y" ]`)
	}
}
