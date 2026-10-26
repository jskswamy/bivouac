package config

import (
	"context"
	"os"
	"os/exec"
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
		`sshKeys { "keys/base.pub" }`,
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
		`sshKeys { "keys/project.pub" }`,
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

	wantKeys := []string{"keys/base.pub", "keys/project.pub"}
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

	got := mergeConfig(base, project, nil).Agents
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

	got := mergeConfig(base, project, nil).HerdrTabs
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

func TestMergeConfig_SettingsBaseOnly(t *testing.T) {
	base := Config{Settings: map[string]any{"a.b": "base-value"}}
	project := Config{}
	got := mergeConfig(base, project, nil)
	if got.Settings["a.b"] != "base-value" {
		t.Errorf("Settings[a.b] = %v, want base-value", got.Settings["a.b"])
	}
}

func TestMergeConfig_SettingsProjectOnly(t *testing.T) {
	base := Config{}
	project := Config{Settings: map[string]any{"a.b": "project-value"}}
	got := mergeConfig(base, project, nil)
	if got.Settings["a.b"] != "project-value" {
		t.Errorf("Settings[a.b] = %v, want project-value", got.Settings["a.b"])
	}
}

func TestMergeConfig_SettingsProjectOverridesBaseKey(t *testing.T) {
	base := Config{Settings: map[string]any{"a": "base-value"}}
	project := Config{Settings: map[string]any{"a": "project-value"}}
	got := mergeConfig(base, project, nil)
	if got.Settings["a"] != "project-value" {
		t.Errorf("Settings[a] = %v, want project-value (project wins)", got.Settings["a"])
	}
}

func TestMergeConfig_SettingsProjectAddsNewKey(t *testing.T) {
	base := Config{Settings: map[string]any{"a": "base-value"}}
	project := Config{Settings: map[string]any{"b": "project-value"}}
	got := mergeConfig(base, project, nil)
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

const shellFixtureHead = `region = "blr1"
size = "s-2vcpu-4gb"
template = "python"
`

func TestResolve_ShellDefaultsToFish(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, path, shellFixtureHead)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

	cfg, err := Resolve(t.Context(), path)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Shell != "fish" {
		t.Errorf("Shell = %q, want %q", cfg.Shell, "fish")
	}
}

func TestResolve_ShellAcceptsZshAndBash(t *testing.T) {
	for _, want := range []string{"zsh", "bash"} {
		t.Run(want, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "bivouac.pkl")
			writeFixture(t, path, shellFixtureHead+`shell = "`+want+"\"\n")
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

			cfg, err := Resolve(t.Context(), path)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if cfg.Shell != want {
				t.Errorf("Shell = %q, want %q", cfg.Shell, want)
			}
		})
	}
}

// The value is interpolated into a root-run boot script, so an unknown
// shell has to be refused when the config is evaluated, not discovered on
// the instance.
func TestResolve_ShellRejectsAnUnknownShell(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, path, shellFixtureHead+`shell = "tcsh"`+"\n")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

	if _, err := Resolve(t.Context(), path); err == nil {
		t.Fatal("Resolve() error = nil, want a type error for a shell that is not fish, zsh or bash")
	}
}

// shell is a personal preference, so it can live in the base rather than
// being repeated in every project. It has no schema default -- fish is
// applied only once neither file sets it -- so an unset project value can
// be told from a set one, as with region and size.
func TestResolve_ShellComesFromTheBase(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, filepath.Join(dir, "base.pkl"), `shell = "zsh"`+"\n")
	path := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, path, `basePath = "./base.pkl"`+"\n"+shellFixtureHead)

	cfg, err := Resolve(t.Context(), path)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Shell != "zsh" {
		t.Errorf("Shell = %q, want %q (from the base)", cfg.Shell, "zsh")
	}
}

func TestResolve_AProjectShellOverridesTheBase(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, filepath.Join(dir, "base.pkl"), `shell = "zsh"`+"\n")
	path := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, path, `basePath = "./base.pkl"`+"\n"+shellFixtureHead+`shell = "bash"`+"\n")

	cfg, err := Resolve(t.Context(), path)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Shell != "bash" {
		t.Errorf("Shell = %q, want %q (the project wins)", cfg.Shell, "bash")
	}
}

// A base with other settings but no shell must still leave the default, or
// having a base at all would change which shell an instance gets.
func TestResolve_ABaseWithoutAShellStillDefaultsToFish(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, filepath.Join(dir, "base.pkl"), `packages { "ripgrep" }`+"\n")
	path := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, path, `basePath = "./base.pkl"`+"\n"+shellFixtureHead)

	cfg, err := Resolve(t.Context(), path)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Shell != "fish" {
		t.Errorf("Shell = %q, want %q", cfg.Shell, "fish")
	}
}

func TestResolve_ShellInTheBaseIsTypeChecked(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, filepath.Join(dir, "base.pkl"), `shell = "tcsh"`+"\n")
	path := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, path, `basePath = "./base.pkl"`+"\n"+shellFixtureHead)

	if _, err := Resolve(t.Context(), path); err == nil {
		t.Fatal("Resolve() error = nil, want a type error for a base shell that is not fish, zsh or bash")
	}
}

// A project that never mentions arch, image, tailscale or beads still
// loads with their schema defaults, so the merge has to ask the file
// whether it declared them. Without that, `image = "tag:my-dev"` in
// base.pkl would be silently replaced by every project. The explicit
// rows matter as much: a project writing the default out loud has
// chosen it, and must beat a base that says otherwise.
func TestResolve_InheritsUndeclaredScalarsFromBase(t *testing.T) {
	get := map[string]func(Resolved) any{
		"arch":      func(r Resolved) any { return r.Arch },
		"image":     func(r Resolved) any { return r.Image },
		"tailscale": func(r Resolved) any { return r.Tailscale },
		"beads":     func(r Resolved) any { return r.Beads },
	}
	tests := []struct {
		name    string
		field   string
		base    string
		project string
		want    any
	}{
		{"image omitted inherits base", "image", `image = "tag:my-dev"`, "", "tag:my-dev"},
		{"image explicit default wins", "image", `image = "tag:my-dev"`, `image = "ubuntu-24-04-x64"`, "ubuntu-24-04-x64"},
		{"image explicit other wins", "image", `image = "tag:my-dev"`, `image = "ubuntu-22-04-x64"`, "ubuntu-22-04-x64"},
		{"image neither declares", "image", "", "", "ubuntu-24-04-x64"},

		{"arch omitted inherits base", "arch", `arch = "arm64"`, "", "arm64"},
		{"arch explicit default wins", "arch", `arch = "arm64"`, `arch = "x86_64"`, "x86_64"},
		{"arch neither declares", "arch", "", "", "x86_64"},

		{"tailscale omitted inherits base", "tailscale", `tailscale = true`, "", true},
		{"tailscale explicit default wins", "tailscale", `tailscale = true`, `tailscale = false`, false},
		{"tailscale explicit other wins", "tailscale", `tailscale = false`, `tailscale = true`, true},
		{"tailscale neither declares", "tailscale", "", "", false},

		{"beads omitted inherits base", "beads", `beads = "off"`, "", "off"},
		{"beads explicit default wins", "beads", `beads = "off"`, `beads = "session"`, "session"},
		{"beads explicit other wins", "beads", `beads = "off"`, `beads = "dolthub"`, "dolthub"},
		{"beads neither declares", "beads", "", "", "session"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			cfgHome := filepath.Join(dir, "xdg")
			if err := os.MkdirAll(filepath.Join(cfgHome, "bivouac"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("XDG_CONFIG_HOME", cfgHome)
			writeFixture(t, filepath.Join(cfgHome, "bivouac", "base.pkl"),
				"region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\n"+tt.base+"\n")

			project := filepath.Join(dir, "bivouac.pkl")
			writeFixture(t, project, "template = \"python\"\n"+tt.project+"\n")

			cfg, err := Resolve(context.Background(), project)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got := get[tt.field](cfg); got != tt.want {
				t.Errorf("%s = %v, want %v", tt.field, got, tt.want)
			}
		})
	}
}

func TestMergeConfig_AllowUnfreePackagesAreAdditiveBaseFirst(t *testing.T) {
	base := Config{AllowUnfreePackages: []string{"terraform"}}
	project := Config{AllowUnfreePackages: []string{"vault"}}

	got := mergeConfig(base, project, nil).AllowUnfreePackages
	want := []string{"terraform", "vault"}
	if !equalStrings(got, want) {
		t.Fatalf("AllowUnfreePackages = %v, want %v", got, want)
	}
}

func TestResolve_GCloudProvider(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, project, strings.Join([]string{
		`provider = "gcloud"`,
		`template = "python"`,
		`gcloud {`,
		`  project = "my-gcp-project"`,
		`  zone = "us-central1-a"`,
		`  machineType = "e2-standard-4"`,
		`}`,
	}, "\n")+"\n")

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Provider != "gcloud" {
		t.Errorf("Provider = %q, want gcloud", cfg.Provider)
	}
	if cfg.GCloud == nil {
		t.Fatal("GCloud is nil, want non-nil")
	}
	if cfg.GCloud.Project != "my-gcp-project" {
		t.Errorf("GCloud.Project = %q, want my-gcp-project", cfg.GCloud.Project)
	}
	if cfg.GCloud.Zone != "us-central1-a" {
		t.Errorf("GCloud.Zone = %q, want us-central1-a", cfg.GCloud.Zone)
	}
	if cfg.GCloud.MachineType != "e2-standard-4" {
		t.Errorf("GCloud.MachineType = %q, want e2-standard-4", cfg.GCloud.MachineType)
	}
	if cfg.Region != "us-central1-a" {
		t.Errorf("Region = %q, want us-central1-a", cfg.Region)
	}
	if cfg.Size != "e2-standard-4" {
		t.Errorf("Size = %q, want e2-standard-4", cfg.Size)
	}
}

func TestResolve_GCloudProvider_WithBaseDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgHome := filepath.Join(dir, "config")
	baseDir := filepath.Join(cfgHome, "bivouac")
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(baseDir, "base.pkl"), strings.Join([]string{
		`region = "blr1"`,
		`size = "s-2vcpu-4gb"`,
		`template = "docker"`,
	}, "\n")+"\n")

	project := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, project, strings.Join([]string{
		`provider = "gcloud"`,
		`gcloud {`,
		`  project = "my-gcp-project"`,
		`  zone = "asia-south1-c"`,
		`  machineType = "e2-standard-4"`,
		`}`,
	}, "\n")+"\n")

	t.Setenv("XDG_CONFIG_HOME", cfgHome)

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Provider != "gcloud" {
		t.Errorf("Provider = %q, want gcloud", cfg.Provider)
	}
	if cfg.Region != "asia-south1-c" {
		t.Errorf("Region = %q, want asia-south1-c", cfg.Region)
	}
	if cfg.Size != "e2-standard-4" {
		t.Errorf("Size = %q, want e2-standard-4", cfg.Size)
	}
}

func TestResolve_DigitalOceanExplicitBlock(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, project, strings.Join([]string{
		`provider = "digitalocean"`,
		`template = "python"`,
		`digitalocean {`,
		`  region = "sfo3"`,
		`  size = "s-2vcpu-4gb"`,
		`}`,
	}, "\n")+"\n")

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Provider != "digitalocean" {
		t.Errorf("Provider = %q, want digitalocean", cfg.Provider)
	}
	if cfg.Region != "sfo3" {
		t.Errorf("Region = %q, want sfo3", cfg.Region)
	}
	if cfg.Size != "s-2vcpu-4gb" {
		t.Errorf("Size = %q, want s-2vcpu-4gb", cfg.Size)
	}
}

func TestResolve_PklStderrRedirectedToLog(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "cache", "bivouac", "logs")
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	project := filepath.Join(dir, "bivouac.pkl")
	if err := os.WriteFile(project, []byte(`
template = "minimal"
region = "nyc3"
size = "s-1vcpu-1gb"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Template != "minimal" {
		t.Errorf("Template = %q, want minimal", cfg.Template)
	}
	logPath := filepath.Join(logDir, "pkl.log")
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("expected log file at %s, got error: %v", logPath, err)
	}
}

func TestResolve_PklWrapperUsesPOSIXShSyntax(t *testing.T) {
	pklPath, err := exec.LookPath("pkl")
	if err != nil {
		t.Skip("pkl not installed")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(pklPath, filepath.Join(binDir, "pkl")); err != nil {
		t.Fatal(err)
	}
	// Simulate dash / strict POSIX sh on Ubuntu where /bin/sh rejects
	// bash-only parameter slicing such as "${@:3}".
	fakeSh := `#!/bin/sh
if [ "$1" = "-c" ]; then
  case "$2" in
    *'${@:'*|*'${*:'*)
      echo "sh: 1: Bad substitution" >&2
      exit 2
      ;;
  esac
fi
exec /bin/sh "$@"
`
	if err := os.WriteFile(filepath.Join(binDir, "sh"), []byte(fakeSh), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

	project := filepath.Join(dir, "bivouac.pkl")
	if err := os.WriteFile(project, []byte(`
template = "minimal"
region = "nyc3"
size = "s-1vcpu-1gb"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(context.Background(), project); err != nil {
		t.Fatalf("Resolve() under strict POSIX sh failed: %v", err)
	}
}
