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
	tmpl := validTemplateFor(t)
	cfg := config.Config{
		Arch:     "x86_64",
		Template: &tmpl,
		Flakes: []config.Flake{
			{Url: testdataFlake(t, "good-flake"), Packages: []string{"cli"}, Modules: true},
		},
	}
	if err := Validate(context.Background(), cfg); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
}

func TestValidate_MissingFlakePackage_NamesItClearly(t *testing.T) {
	tmpl := validTemplateFor(t)
	cfg := config.Config{
		Arch:     "x86_64",
		Template: &tmpl,
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
	tmpl := validTemplateFor(t)
	cfg := config.Config{
		Arch:     "x86_64",
		Template: &tmpl,
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

	tmpl := templatesRef + "#python-x86_64-linux"
	cfg := config.Config{Arch: "x86_64", Template: &tmpl}
	if err := Validate(context.Background(), cfg); err != nil {
		t.Errorf("Validate() error = %v, want nil (real templates/ flake should validate cleanly)", err)
	}
}
