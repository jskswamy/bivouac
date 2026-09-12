package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/cloudlab/internal/config"
	"github.com/jskswamy/cloudlab/internal/preset"
)

func presetFixture(t *testing.T) string {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	return xdg
}

func runPresetCmd(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"preset"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestPresetList_Empty(t *testing.T) {
	presetFixture(t)
	out, err := runPresetCmd(t, "", "list")
	if err != nil {
		t.Fatalf("preset list error = %v", err)
	}
	if !strings.Contains(out, "no presets") {
		t.Errorf("output = %q, want it to say there are none", out)
	}
}

func TestPresetList_NameWhatItSetsAndWhenItChanged(t *testing.T) {
	presetFixture(t)
	if err := preset.Save("go-api", config.Values{
		"template": "python",
		"agents":   []string{"claude"},
		"packages": []string{"jq"},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	path, _ := preset.Path("go-api")
	changed := time.Date(2026, 3, 4, 15, 4, 0, 0, time.Local)
	if err := os.Chtimes(path, changed, changed); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	out, err := runPresetCmd(t, "", "list")
	if err != nil {
		t.Fatalf("preset list error = %v", err)
	}
	if !strings.Contains(out, "go-api") {
		t.Errorf("output does not name the preset:\n%s", out)
	}
	// Schema order, not map order: two runs must print the same line.
	if !strings.Contains(out, "template, packages, agents") {
		t.Errorf("output does not list what it sets, in schema order:\n%s", out)
	}
	if !strings.Contains(out, "2026-03-04 15:04") {
		t.Errorf("output does not show when it changed:\n%s", out)
	}
}

func TestPresetShow_PrintsThePkl(t *testing.T) {
	presetFixture(t)
	if err := preset.Save("go-api", config.Values{"template": "python", "packages": []string{"jq"}}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	out, err := runPresetCmd(t, "", "show", "go-api")
	if err != nil {
		t.Fatalf("preset show error = %v", err)
	}
	want := "template = \"python\"\npackages {\n  \"jq\"\n}\n"
	if !strings.Contains(out, want) {
		t.Errorf("output =\n%s\nwant it to contain\n%s", out, want)
	}
}

func TestPresetShow_UnknownName(t *testing.T) {
	presetFixture(t)
	if _, err := runPresetCmd(t, "", "show", "nope"); err == nil {
		t.Fatal("preset show of an unsaved name error = nil, want an error")
	}
}

func TestPresetDelete_RemovesAfterConfirming(t *testing.T) {
	presetFixture(t)
	if err := preset.Save("gone", config.Values{"template": "python"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if _, err := runPresetCmd(t, "y\n", "delete", "gone"); err != nil {
		t.Fatalf("preset delete error = %v", err)
	}
	path, _ := preset.Path("gone")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("preset file still there after a confirmed delete")
	}
}

func TestPresetDelete_DecliningKeepsIt(t *testing.T) {
	presetFixture(t)
	if err := preset.Save("keep", config.Values{"template": "python"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	out, err := runPresetCmd(t, "n\n", "delete", "keep")
	if err != nil {
		t.Fatalf("preset delete error = %v", err)
	}
	path, _ := preset.Path("keep")
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("preset was deleted despite declining: %v", statErr)
	}
	if !strings.Contains(out, "Aborted") {
		t.Errorf("output = %q, want it to say nothing happened", out)
	}
}

// Nothing can depend on a preset, because presets are stamped into the
// projects made from them rather than linked. Deleting one must not
// disturb a project built from it.
func TestPresetDelete_BreaksNoProject(t *testing.T) {
	xdg := presetFixture(t)
	if err := preset.Save("shape", config.Values{"template": "python"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	project := filepath.Join(t.TempDir(), "cloudlab.pkl")
	if err := os.WriteFile(project, []byte("template = \"python\"\nregion = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\n"), 0o644); err != nil {
		t.Fatalf("writing project: %v", err)
	}
	_ = xdg

	if _, err := runPresetCmd(t, "y\n", "delete", "shape"); err != nil {
		t.Fatalf("preset delete error = %v", err)
	}
	if _, err := config.Resolve(context.Background(), project); err != nil {
		t.Errorf("project stopped resolving after its preset was deleted: %v", err)
	}
}

func TestPresetDelete_UnknownName(t *testing.T) {
	presetFixture(t)
	if _, err := runPresetCmd(t, "y\n", "delete", "nope"); err == nil {
		t.Fatal("preset delete of an unsaved name error = nil, want an error")
	}
}

// edit shares the init flow's question definitions rather than
// duplicating them, so the questions cannot drift between creating a
// preset and editing one.
func TestPresetEdit_AsksTheSameProjectQuestions(t *testing.T) {
	presetFixture(t)
	if err := preset.Save("go-api", config.Values{
		"template": "python",
		"agents":   []string{"claude"},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	s := newScript()
	s.answers = map[string]any{"template": "docker"}
	var out bytes.Buffer
	if err := runPresetEdit(context.Background(), &out, s, "go-api"); err != nil {
		t.Fatalf("runPresetEdit() error = %v", err)
	}

	asked := projectQuestionFields()
	if !reflect.DeepEqual(s.asked, asked) {
		t.Errorf("asked %v, want the project questions %v", s.asked, asked)
	}
	if s.seen["template"] != "python" {
		t.Errorf("template pre-filled with %v, want the preset's value", s.seen["template"])
	}

	saved, err := preset.Load(context.Background(), "go-api")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if saved["template"] != "docker" {
		t.Errorf("template = %v, want the edit applied", saved["template"])
	}
	if !reflect.DeepEqual(saved["agents"], []string{"claude"}) {
		t.Errorf("agents = %v, want the untouched value kept", saved["agents"])
	}
}

// A preset may carry a size override, which the project questions do not
// cover. Editing one must not drop it.
func TestPresetEdit_KeepsFieldsTheQuestionsDoNotCover(t *testing.T) {
	presetFixture(t)
	if err := preset.Save("k8s", config.Values{"template": "docker", "size": "s-4vcpu-8gb"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	s := newScript()
	var out bytes.Buffer
	if err := runPresetEdit(context.Background(), &out, s, "k8s"); err != nil {
		t.Fatalf("runPresetEdit() error = %v", err)
	}
	saved, err := preset.Load(context.Background(), "k8s")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if saved["size"] != "s-4vcpu-8gb" {
		t.Errorf("size = %v, want the override kept", saved["size"])
	}
}

func TestPresetEdit_UnknownName(t *testing.T) {
	presetFixture(t)
	s := newScript()
	var out bytes.Buffer
	if err := runPresetEdit(context.Background(), &out, s, "nope"); err == nil {
		t.Fatal("runPresetEdit() of an unsaved name error = nil, want an error")
	}
}

func TestPresetEdit_RefusesWithoutATerminal(t *testing.T) {
	presetFixture(t)
	if err := preset.Save("go-api", config.Values{"template": "python"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	_, err := runPresetCmd(t, "", "edit", "go-api")
	if err == nil {
		t.Fatal("preset edit error = nil without a terminal, want a refusal")
	}
	if !strings.Contains(err.Error(), "cloudlab init") {
		t.Errorf("error = %q, want it to name `cloudlab init`", err)
	}
}
