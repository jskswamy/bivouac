package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// selectFixture gives each test an isolated XDG config home and project
// directory, and returns where base.pkl and the project bivouac.pkl
// would live. Neither file is written; each test writes what it needs.
func selectFixture(t *testing.T) (basePath, projectDir string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	if err := os.MkdirAll(filepath.Join(dir, "xdg", "bivouac"), 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	projectDir = filepath.Join(dir, "repo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("creating project dir: %v", err)
	}
	return filepath.Join(dir, "xdg", "bivouac", "base.pkl"), projectDir
}

const completeConfig = `region = "nyc3"` + "\n" + `size = "s-1vcpu-1gb"` + "\n" + `template = "python"` + "\n"

func TestLoadSelected_ExplicitWins(t *testing.T) {
	base, projectDir := selectFixture(t)
	writeFixture(t, base, `region = "blr1"`+"\n"+`size = "s-1vcpu-1gb"`+"\n"+`template = "base"`+"\n")
	writeFixture(t, filepath.Join(projectDir, "bivouac.pkl"), completeConfig)
	explicit := filepath.Join(t.TempDir(), "custom.pkl")
	writeFixture(t, explicit, `template = "explicit"`+"\n")

	got, err := LoadSelected(context.Background(), explicit, projectDir)
	if err != nil {
		t.Fatalf("LoadSelected() error = %v", err)
	}
	if got.Kind != SelectionExplicit || got.Path != explicit {
		t.Errorf("selected %s %q, want explicit %q", got.Kind, got.Path, explicit)
	}
	if got.Config.Template != "explicit" {
		t.Errorf("Template = %q, want explicit", got.Config.Template)
	}
	// An explicit file is a project file, so it keeps base inheritance.
	if got.Config.Region != "blr1" {
		t.Errorf("Region = %q, want blr1 inherited from base", got.Config.Region)
	}
}

func TestLoadSelected_ExplicitMissingNoFallback(t *testing.T) {
	base, projectDir := selectFixture(t)
	writeFixture(t, base, completeConfig)
	writeFixture(t, filepath.Join(projectDir, "bivouac.pkl"), completeConfig)
	explicit := filepath.Join(t.TempDir(), "missing.pkl")

	_, err := LoadSelected(context.Background(), explicit, projectDir)
	if err == nil {
		t.Fatal("LoadSelected() error = nil, want error for missing explicit file")
	}
	if !strings.Contains(err.Error(), explicit) {
		t.Errorf("error %q does not name %s", err, explicit)
	}
}

func TestLoadSelected_ExplicitInvalidNoFallback(t *testing.T) {
	base, projectDir := selectFixture(t)
	writeFixture(t, base, completeConfig)
	writeFixture(t, filepath.Join(projectDir, "bivouac.pkl"), completeConfig)
	explicit := filepath.Join(t.TempDir(), "broken.pkl")
	writeFixture(t, explicit, "this is not valid pkl {{{")

	if _, err := LoadSelected(context.Background(), explicit, projectDir); err == nil {
		t.Fatal("LoadSelected() error = nil, want error for invalid explicit file")
	}
}

func TestLoadSelected_InvalidProjectIsErrorNotBase(t *testing.T) {
	base, projectDir := selectFixture(t)
	writeFixture(t, base, completeConfig)
	project := filepath.Join(projectDir, "bivouac.pkl")
	writeFixture(t, project, "this is not valid pkl {{{")

	_, err := LoadSelected(context.Background(), "", projectDir)
	if err == nil {
		t.Fatal("LoadSelected() error = nil, want the project file's error, not base")
	}
	if !strings.Contains(err.Error(), project) {
		t.Errorf("error %q does not name %s", err, project)
	}
}

func TestLoadSelected_ProjectInheritsBase(t *testing.T) {
	base, projectDir := selectFixture(t)
	writeFixture(t, base, `region = "blr1"`+"\n"+`packages { "a" }`+"\n")
	project := filepath.Join(projectDir, "bivouac.pkl")
	writeFixture(t, project, `size = "s-1vcpu-1gb"`+"\n"+`template = "python"`+"\n"+`packages { "b" }`+"\n")

	got, err := LoadSelected(context.Background(), "", projectDir)
	if err != nil {
		t.Fatalf("LoadSelected() error = %v", err)
	}
	if got.Kind != SelectionProject || got.Path != project {
		t.Errorf("selected %s %q, want project %q", got.Kind, got.Path, project)
	}
	if got.Config.Region != "blr1" {
		t.Errorf("Region = %q, want blr1 from base", got.Config.Region)
	}
	if !equalStrings(got.Config.Packages, []string{"a", "b"}) {
		t.Errorf("Packages = %v, want [a b]", got.Config.Packages)
	}
}

func TestLoadSelected_BaseOnlyLoadsOnce(t *testing.T) {
	base, projectDir := selectFixture(t)
	writeFixture(t, base, completeConfig+`packages { "a" }`+"\n")

	got, err := LoadSelected(context.Background(), "", projectDir)
	if err != nil {
		t.Fatalf("LoadSelected() error = %v", err)
	}
	if got.Kind != SelectionBase || got.Path != base {
		t.Errorf("selected %s %q, want base %q", got.Kind, got.Path, base)
	}
	if len(got.Config.Packages) != 1 {
		t.Errorf("Packages = %v, want exactly [a]: base merged with itself", got.Config.Packages)
	}
}

func TestLoadSelected_NoProjectDir(t *testing.T) {
	base, _ := selectFixture(t)
	writeFixture(t, base, completeConfig)

	got, err := LoadSelected(context.Background(), "", "")
	if err != nil {
		t.Fatalf("LoadSelected() error = %v", err)
	}
	if got.Kind != SelectionBase {
		t.Errorf("Kind = %s, want base", got.Kind)
	}
}

func TestLoadSelected_ExplicitPathEqualToBaseLoadsOnce(t *testing.T) {
	base, projectDir := selectFixture(t)
	writeFixture(t, base, completeConfig+`packages { "a" }`+"\n")
	// Reach the same file through a symlink, so a plain string compare
	// would not catch it.
	link := filepath.Join(t.TempDir(), "link.pkl")
	if err := os.Symlink(base, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := LoadSelected(context.Background(), link, projectDir)
	if err != nil {
		t.Fatalf("LoadSelected() error = %v", err)
	}
	if got.Kind != SelectionExplicit {
		t.Errorf("Kind = %s, want explicit", got.Kind)
	}
	if len(got.Config.Packages) != 1 {
		t.Errorf("Packages = %v, want exactly [a]: base merged with itself", got.Config.Packages)
	}
}

func TestLoadSelected_NoneFound(t *testing.T) {
	base, projectDir := selectFixture(t)

	_, err := LoadSelected(context.Background(), "", projectDir)
	if err == nil {
		t.Fatal("LoadSelected() error = nil, want error naming where it looked")
	}
	for _, want := range []string{"--config", filepath.Join(projectDir, "bivouac.pkl"), base} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

func TestLoadSelected_IgnoresMissingSSHKeyFiles(t *testing.T) {
	_, projectDir := selectFixture(t)
	writeFixture(t, filepath.Join(projectDir, "bivouac.pkl"), completeConfig+`sshKeys { "~/.ssh/nope.pub" }`+"\n")

	got, err := LoadSelected(context.Background(), "", projectDir)
	if err != nil {
		t.Fatalf("LoadSelected() error = %v", err)
	}
	if len(got.Config.SSHKeyFiles) != 0 {
		t.Errorf("SSHKeyFiles = %v, want none: image builds use temporary keys", got.Config.SSHKeyFiles)
	}
}

func TestLoadSelected_RequiresRegionSizeTemplate(t *testing.T) {
	_, projectDir := selectFixture(t)
	writeFixture(t, filepath.Join(projectDir, "bivouac.pkl"), `packages { "a" }`+"\n")

	_, err := LoadSelected(context.Background(), "", projectDir)
	if err == nil {
		t.Fatal("LoadSelected() error = nil, want missing-field error")
	}
	for _, want := range []string{"region", "size", "template"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}
