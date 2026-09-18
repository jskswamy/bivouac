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
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing fixture %s: %v", path, err)
	}
}

func TestLoad_SelfContainedProjectFile(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, project, "region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\ntemplate = \"python\"\n")

	// No base file at the XDG default location for this test's HOME.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Region != "nyc3" {
		t.Errorf("Region = %v, want nyc3", cfg.Region)
	}
	if cfg.Size != "s-1vcpu-1gb" {
		t.Errorf("Size = %v, want s-1vcpu-1gb", cfg.Size)
	}
	if cfg.Template != "python" {
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

	project := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, project, strings.Join([]string{
		`basePath = "./base.pkl"`,
		`size = "s-2vcpu-4gb"`, // overrides base
		`template = "python"`,
		`arch = "arm64"`,
		`tailscale = true`,
		`beads = "dolthub"`,
		`sshKeys { "project-key" }`,
		`packages { "ripgrep" }`,
	}, "\n")+"\n")

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if cfg.Region != "nyc3" {
		t.Errorf("Region = %v, want nyc3 (from base)", cfg.Region)
	}
	if cfg.Size != "s-2vcpu-4gb" {
		t.Errorf("Size = %v, want s-2vcpu-4gb (project overrides base)", cfg.Size)
	}
	if cfg.Template != "python" {
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

	if cfg.Arch != "arm64" {
		t.Errorf("Arch = %q, want %q (project overrides base)", cfg.Arch, "arm64")
	}
	if !cfg.Tailscale {
		t.Error("Tailscale = false, want true (project overrides base)")
	}
	if cfg.Beads != "dolthub" {
		t.Errorf("Beads = %q, want %q (project overrides base)", cfg.Beads, "dolthub")
	}
}

func TestLoad_MissingBaseFile_ProjectStandalone(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "bivouac.pkl")
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
	if cfg.Region != "nyc3" {
		t.Errorf("Region = %v, want nyc3", cfg.Region)
	}
}

func TestLoad_MissingRequiredFieldAfterMerge_ReturnsClearError(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "bivouac.pkl")
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

func TestLoad_PklNotOnPATH_ReturnsClearError(t *testing.T) {
	dir := t.TempDir()
	// An empty temp dir has no pkl binary in it, so the PATH lookup
	// fails before the project file is even read — it doesn't need to
	// exist.
	t.Setenv("PATH", t.TempDir())

	_, err := Resolve(context.Background(), filepath.Join(dir, "bivouac.pkl"))
	if err == nil {
		t.Fatal("Resolve() error = nil, want error naming pkl CLI not found")
	}
	if !strings.Contains(err.Error(), "pkl CLI not found on PATH") {
		t.Errorf("error %q does not mention pkl CLI not found on PATH", err.Error())
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
	project := filepath.Join(dir, "bivouac.pkl")
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

	project := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, project, strings.Join([]string{
		`basePath = ` + quote(custom),
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
	}, "\n")+"\n")

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Region != "sfo3" {
		t.Errorf("Region = %v, want sfo3 (from custom base)", cfg.Region)
	}
}

func TestLoad_ProjectFileDeclaresOwnAmends_ReturnsClearError(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "bivouac.pkl")
	if err := os.WriteFile(project, []byte(`amends "whatever.pkl"`+"\n\n"+`region = "nyc3"`+"\n"), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	_, err := Resolve(context.Background(), project)
	if err == nil {
		t.Fatal("Resolve() error = nil, want error naming the amends line")
	}
	if !strings.Contains(err.Error(), "amends") {
		t.Errorf("error %q does not mention amends", err.Error())
	}
}

func TestLoad_ArchDefaultsToX86_64(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "bivouac.pkl")
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
	project := filepath.Join(dir, "bivouac.pkl")
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

func TestLoad_ArchInvalid_ReturnsClearError(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, project, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`arch = "sparc64"`,
	}, "\n")+"\n")

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

	if _, err := Resolve(context.Background(), project); err == nil {
		t.Error("Resolve() error = nil, want error for invalid arch value")
	}
}

func TestLoad_ImageDefaultsToUbuntu2404(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "bivouac.pkl")
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
	if cfg.Image != "ubuntu-24-04-x64" {
		t.Errorf("Image = %q, want %q", cfg.Image, "ubuntu-24-04-x64")
	}
}

func TestLoad_ImageOverride(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, project, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`image = "ubuntu-22-04-x64"`,
	}, "\n")+"\n")

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Image != "ubuntu-22-04-x64" {
		t.Errorf("Image = %q, want %q", cfg.Image, "ubuntu-22-04-x64")
	}
}

func TestLoad_ImageSurvivesBaseMerge(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.pkl")
	writeFixture(t, base, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
	}, "\n")+"\n")

	project := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, project, strings.Join([]string{
		`basePath = "./base.pkl"`,
		`template = "python"`,
		`image = "ubuntu-22-04-x64"`,
	}, "\n")+"\n")

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Image != "ubuntu-22-04-x64" {
		t.Errorf("Image = %q, want %q (project overrides base)", cfg.Image, "ubuntu-22-04-x64")
	}
}

func TestLoad_FlakeModulesDefaultsFalse(t *testing.T) {
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
	if cfg.Flakes[0].Modules != false {
		t.Errorf("Flakes[0].Modules = %v, want false", cfg.Flakes[0].Modules)
	}
}

func TestLoad_FlakeModulesTrue(t *testing.T) {
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

func TestMergeConfig_AgentsAreAdditiveBaseFirst(t *testing.T) {
	base := Config{Agents: []string{"claude"}}
	project := Config{Agents: []string{"codex"}}

	got := mergeConfig(base, project).Agents
	want := []string{"claude", "codex"}
	if len(got) != len(want) {
		t.Fatalf("Agents = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Agents[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolve_BeadsDefaultsToSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bivouac.pkl")
	if err := os.WriteFile(path, []byte(`region = "blr1"
size = "s-2vcpu-4gb"
template = "python"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Resolve(t.Context(), path)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	// The default has to be inert on a repository with no .beads/, so an
	// existing config needs no change at all -- see beads.Detect, which
	// returns ModeAbsent and skips every step.
	if cfg.Beads != "session" {
		t.Errorf("Beads = %q, want %q", cfg.Beads, "session")
	}
}

func TestResolve_BeadsAcceptsDolthubAndOff(t *testing.T) {
	for _, want := range []string{"dolthub", "off"} {
		t.Run(want, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "bivouac.pkl")
			body := `region = "blr1"
size = "s-2vcpu-4gb"
template = "python"
beads = "` + want + `"
`
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Resolve(t.Context(), path)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if cfg.Beads != want {
				t.Errorf("Beads = %q, want %q", cfg.Beads, want)
			}
		})
	}
}

func TestMergeConfig_HerdrTabsAreAdditiveBaseFirst(t *testing.T) {
	base := Config{HerdrTabs: []HerdrTab{{Label: "agent"}}}
	project := Config{HerdrTabs: []HerdrTab{{Label: "run"}}}

	got := mergeConfig(base, project).HerdrTabs
	if len(got) != 2 || got[0].Label != "agent" || got[1].Label != "run" {
		t.Errorf("HerdrTabs = %+v, want [agent run] -- base's tabs are created first", got)
	}
}

// Through pkl, not just mergeConfig: the classes have to be reachable from
// an amending file by their bare names, and the optional pane fields have
// to come back as nil rather than "".
func TestResolve_HerdrTabsMergeAcrossBaseAndProject(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.pkl")
	writeFixture(t, base, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`herdrTabs {`,
		`  new HerdrTab {`,
		`    label = "agent"`,
		`  }`,
		`}`,
	}, "\n")+"\n")
	project := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, project, strings.Join([]string{
		`basePath = "./base.pkl"`,
		`template = "python"`,
		`herdrTabs {`,
		`  new HerdrTab {`,
		`    label = "run"`,
		`    panes {`,
		`      new HerdrPane {`,
		`        label = "server"`,
		`        command = "npm run dev"`,
		`      }`,
		`      new HerdrPane {`,
		`        label = "logs"`,
		`        cwd = "logs"`,
		`      }`,
		`    }`,
		`  }`,
		`}`,
	}, "\n")+"\n")

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(cfg.HerdrTabs) != 2 || cfg.HerdrTabs[0].Label != "agent" || cfg.HerdrTabs[1].Label != "run" {
		t.Fatalf("HerdrTabs = %+v, want [agent run]", cfg.HerdrTabs)
	}
	panes := cfg.HerdrTabs[1].Panes
	if len(panes) != 2 {
		t.Fatalf("run panes = %+v, want 2", panes)
	}
	if panes[0].Command == nil || *panes[0].Command != "npm run dev" || panes[0].Cwd != nil {
		t.Errorf("server pane = %+v, want command set and cwd nil", panes[0])
	}
	if panes[1].Cwd == nil || *panes[1].Cwd != "logs" || panes[1].Command != nil {
		t.Errorf("logs pane = %+v, want cwd set and command nil", panes[1])
	}
}

func TestResolve_HerdrTabsDefaultToEmpty(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, project, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s"`,
		`template = "python"`,
		`basePath = "./does-not-exist.pkl"`,
	}, "\n")+"\n")

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(cfg.HerdrTabs) != 0 {
		t.Errorf("HerdrTabs = %+v, want none -- layout is opt-in", cfg.HerdrTabs)
	}
}

// An empty label matches herdr's unlabelled root pane (a pane nobody has
// named has no label key at all, which decodes to ""), so its command would
// never run -- reject it at config time instead of silently no-op'ing.
func TestResolve_HerdrTabLabelMustNotBeEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, path, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`herdrTabs {`,
		`  new HerdrTab {`,
		`    label = ""`,
		`  }`,
		`}`,
	}, "\n")+"\n")

	if _, err := Resolve(t.Context(), path); err == nil {
		t.Fatal("Resolve() error = nil, want an empty herdrTabs tab label rejected")
	}
}

func TestResolve_HerdrPaneLabelMustNotBeEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, path, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`herdrTabs {`,
		`  new HerdrTab {`,
		`    label = "run"`,
		`    panes {`,
		`      new HerdrPane {`,
		`        label = ""`,
		`      }`,
		`    }`,
		`  }`,
		`}`,
	}, "\n")+"\n")

	if _, err := Resolve(t.Context(), path); err == nil {
		t.Fatal("Resolve() error = nil, want an empty herdrTabs pane label rejected")
	}
}

func TestResolve_BeadsRejectsAnUnknownMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bivouac.pkl")
	if err := os.WriteFile(path, []byte(`region = "blr1"
size = "s-2vcpu-4gb"
template = "python"
beads = "dolthubb"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(t.Context(), path); err == nil {
		t.Fatal("Resolve() error = nil, want a type error naming the allowed modes")
	}
}
