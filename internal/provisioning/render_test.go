package provisioning

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	if !strings.Contains(out, "template.homeManagerModules.\"python\"") {
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

func TestRender_Output_EvaluatesInNix(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine this test file's own path")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	templatesRef := "path:" + filepath.Join(repoRoot, "templates")

	cfg := config.Config{Arch: "x86_64", Packages: []string{"hello"}}
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

	cmd := exec.Command("nix", "eval", "path:"+dir+"#homeConfigurations.default.activationPackage.drvPath")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rendered flake failed to evaluate:\n%s", out)
	}
}
