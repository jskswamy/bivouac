package wizard

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/config"
)

func TestRoute(t *testing.T) {
	tests := []struct {
		field string
		want  Destination
	}{
		{config.FieldRegion, Personal},
		{config.FieldSize, Personal},
		{config.FieldSSHKeys, Personal},
		{config.FieldTemplate, Project},
		{config.FieldAgents, Project},
		{config.FieldPackages, Project},
		{config.FieldBeads, Project},
		{config.FieldTailscale, Project},
		{config.FieldArch, Project},
		{config.FieldImage, Project},
		{config.FieldFlakes, Project},
	}

	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			got, err := Route(tt.field)
			if err != nil {
				t.Fatalf("Route(%q) error = %v", tt.field, err)
			}
			if got != tt.want {
				t.Errorf("Route(%q) = %v, want %v", tt.field, got, tt.want)
			}
		})
	}
}

// basePath decides which file to merge; routing it into a file the
// wizard writes would let the wizard repoint its own base.
func TestRoute_RejectsBasePathAndUnknowns(t *testing.T) {
	for _, field := range []string{config.FieldBasePath, "nope"} {
		if _, err := Route(field); err == nil {
			t.Errorf("Route(%q) error = nil, want an error", field)
		}
	}
}

// Every field the schema has must route somewhere, or a field added to
// Config.pkl silently stops being written.
func TestRoute_CoversEverySchemaField(t *testing.T) {
	for _, field := range config.Fields() {
		if field == config.FieldBasePath {
			continue
		}
		if _, err := Route(field); err != nil {
			t.Errorf("Route(%q) error = %v; every schema field must route", field, err)
		}
	}
}

func TestSplit(t *testing.T) {
	personal, project, err := Split(config.Values{
		"region":    "nyc3",
		"size":      "s-1vcpu-1gb",
		"sshKeys":   []string{"AAAA...key"},
		"template":  "python",
		"agents":    []string{"claude"},
		"tailscale": true,
	})
	if err != nil {
		t.Fatalf("Split() error = %v", err)
	}

	wantPersonal := config.Values{"region": "nyc3", "size": "s-1vcpu-1gb", "sshKeys": []string{"AAAA...key"}}
	wantProject := config.Values{"template": "python", "agents": []string{"claude"}, "tailscale": true}
	if !reflect.DeepEqual(personal, wantPersonal) {
		t.Errorf("personal = %#v, want %#v", personal, wantPersonal)
	}
	if !reflect.DeepEqual(project, wantProject) {
		t.Errorf("project = %#v, want %#v", project, wantProject)
	}
}

func TestSplit_EmptyAnswersGiveEmptyHalves(t *testing.T) {
	personal, project, err := Split(config.Values{})
	if err != nil {
		t.Fatalf("Split() error = %v", err)
	}
	if len(personal) != 0 || len(project) != 0 {
		t.Errorf("Split({}) = %#v, %#v, want two empty sets", personal, project)
	}
}

func TestPersonalFields_AreExactlyTheRoutedOnes(t *testing.T) {
	got := append([]string{}, PersonalFields()...)
	sort.Strings(got)
	want := []string{config.FieldRegion, config.FieldSSHKeys, config.FieldSize, config.FieldInstructions}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PersonalFields() = %v, want %v", got, want)
	}
}

// instructions routes to base.pkl like the others but cannot be lifted
// into it: its value is a path read relative to the file that declares
// it, so the same line means two different files in the two places.
func TestMovablePersonalFields_ExcludeInstructions(t *testing.T) {
	got := append([]string{}, MovablePersonalFields()...)
	sort.Strings(got)
	want := []string{config.FieldRegion, config.FieldSSHKeys, config.FieldSize}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MovablePersonalFields() = %v, want %v", got, want)
	}
}

// Personal, so it is asked once when base.pkl is created and again only
// on [change] -- not on every init. A path has nothing to enumerate, so
// unlike sshKeys it is an ordinary question rather than its own step.
func TestPersonalQuestions_IncludesInstructions(t *testing.T) {
	var found *Question
	qs := PersonalQuestions(t.TempDir())
	for i := range qs {
		if qs[i].Field == config.FieldInstructions {
			found = &qs[i]
		}
	}
	if found == nil {
		t.Fatal("PersonalQuestions() has no instructions question")
	}
	if found.Kind != TextList {
		t.Errorf("Kind = %v, want TextList", found.Kind)
	}
	if found.Validate == nil {
		t.Error("the instructions question carries no validator")
	}
}

// Checked while the question is on screen, because the wizard can tell
// with what it already has and an answer that gets through fails every
// later up outright.
func TestPersonalQuestions_InstructionsValidatorChecksThePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := instructionsQuestion(t, dir).Validate

	if err := v("real.md"); err != nil {
		t.Errorf("Validate(%q) = %v, want nil", "real.md", err)
	}
	if err := v(""); err != nil {
		t.Errorf("Validate(\"\") = %v, want nil -- blank means none", err)
	}
	err := v("gone.md")
	if err == nil {
		t.Fatal("Validate(\"gone.md\") = nil, want an error")
	}
	if !strings.Contains(err.Error(), "gone.md") {
		t.Errorf("error %q does not name the path", err)
	}
}

// Every other question keeps the wizard's existing rule: region and size
// take typed slugs and are caught by the provider at up, not here.
func TestPersonalQuestions_OtherQuestionsAreNotValidated(t *testing.T) {
	for _, q := range PersonalQuestions(t.TempDir()) {
		if q.Field == config.FieldInstructions {
			continue
		}
		if q.Validate != nil {
			t.Errorf("question %q gained a validator", q.Field)
		}
	}
}

// Presets carry the project's shape. A path like docs/agent-workflow.md
// means nothing stamped into another repository, and a personal one
// belongs to the user rather than the shape -- the same reasoning that
// keeps sshKeys out.
func TestPresetShape_ExcludesInstructions(t *testing.T) {
	for _, f := range presetShape {
		if f == config.FieldInstructions {
			t.Error("a preset must not capture instructions")
		}
	}
}

func instructionsQuestion(t *testing.T, dir string) Question {
	t.Helper()
	for _, q := range PersonalQuestions(dir) {
		if q.Field == config.FieldInstructions {
			return q
		}
	}
	t.Fatal("no instructions question")
	return Question{}
}

func TestQuestions_AskOnlyFieldsThatRouteToTheirOwnHalf(t *testing.T) {
	for _, q := range PersonalQuestions(t.TempDir()) {
		if d, err := Route(q.Field); err != nil || d != Personal {
			t.Errorf("personal question %q routes to %v (err %v)", q.Field, d, err)
		}
	}
	for _, q := range ProjectQuestions() {
		if d, err := Route(q.Field); err != nil || d != Project {
			t.Errorf("project question %q routes to %v (err %v)", q.Field, d, err)
		}
	}
}

func TestQuestions_EveryQuestionIsAnswerable(t *testing.T) {
	for _, q := range append(PersonalQuestions(t.TempDir()), ProjectQuestions()...) {
		if q.Title == "" {
			t.Errorf("question %q has no title", q.Field)
		}
		if !config.KnownField(q.Field) {
			t.Errorf("question %q is not a schema field", q.Field)
		}
		switch q.Kind {
		case Select, MultiSelect:
			if len(q.Options) == 0 {
				t.Errorf("question %q is a %v with no options", q.Field, q.Kind)
			}
		case Text, TextList, Confirm:
			if len(q.Options) != 0 {
				t.Errorf("question %q is a %v but carries options", q.Field, q.Kind)
			}
		}
	}
}

// The option lists are copies of closed sets declared in Config.pkl.
// This is the guard that they are still the same sets: a value added to
// the schema and not here is a value the wizard can never produce.
func TestQuestions_OptionsMatchTheSchemasUnions(t *testing.T) {
	schema := readSchema(t)
	for _, tt := range []struct{ field string }{{config.FieldBeads}, {config.FieldAgents}} {
		want := unionMembers(t, schema, tt.field)
		got := optionsFor(t, tt.field)
		sort.Strings(want)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s options = %v, want %v (from Config.pkl)", tt.field, got, want)
		}
	}
}

func readSchema(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtimeCaller()
	path := filepath.Join(filepath.Dir(thisFile), "..", "config", "Config.pkl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading Config.pkl: %v", err)
	}
	return string(raw)
}

// unionMembers pulls the quoted members of `field: "a"|"b"|...` out of
// the schema text.
func unionMembers(t *testing.T, schema, field string) []string {
	t.Helper()
	line := regexp.MustCompile(`(?m)^` + field + `:.*$`).FindString(schema)
	if line == "" {
		t.Fatalf("no declaration of %q in Config.pkl", field)
	}
	// Everything after the first `=` is the field's default, not a
	// member of its union.
	if i := strings.Index(line, "="); i >= 0 {
		line = line[:i]
	}

	var members []string
	for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(line, -1) {
		members = append(members, m[1])
	}
	if len(members) == 0 {
		t.Fatalf("no union members on %q's declaration", field)
	}
	return members
}

func optionsFor(t *testing.T, field string) []string {
	t.Helper()
	for _, q := range append(PersonalQuestions(t.TempDir()), ProjectQuestions()...) {
		if q.Field == field {
			return append([]string{}, q.Options...)
		}
	}
	t.Fatalf("no question for field %q", field)
	return nil
}

func TestPresetValues(t *testing.T) {
	base := config.Values{"region": "nyc3", "size": "s-1vcpu-1gb"}

	tests := []struct {
		name    string
		answers config.Values
		want    config.Values
	}{
		{
			name:    "shape is always captured",
			answers: config.Values{"template": "python", "agents": []string{"claude"}, "packages": []string{"jq"}, "beads": "session", "tailscale": true},
			want:    config.Values{"template": "python", "agents": []string{"claude"}, "packages": []string{"jq"}, "beads": "session", "tailscale": true},
		},
		{
			// A k8s-shaped preset that needs headroom has to carry the
			// size, or picking it on a 1vcpu base fails later with
			// nothing having warned.
			name:    "sizing is captured only when the run overrode base",
			answers: config.Values{"template": "python", "size": "s-4vcpu-8gb", "region": "nyc3"},
			want:    config.Values{"template": "python", "size": "s-4vcpu-8gb"},
		},
		{
			name:    "sizing matching base is left to base",
			answers: config.Values{"template": "python", "size": "s-1vcpu-1gb", "region": "nyc3"},
			want:    config.Values{"template": "python"},
		},
		{
			// A preset is shared across projects; a fingerprint is not.
			name:    "ssh keys are never captured",
			answers: config.Values{"template": "python", "sshKeys": []string{"AAAA...key"}},
			want:    config.Values{"template": "python"},
		},
		{
			name:    "nothing to capture is an empty preset",
			answers: config.Values{"sshKeys": []string{"AAAA...key"}},
			want:    config.Values{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PresetValues(tt.answers, base)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("PresetValues() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestPresetValues_NoBaseMeansSizingIsAnOverride(t *testing.T) {
	got := PresetValues(config.Values{"template": "python", "size": "s-4vcpu-8gb"}, config.Values{})
	want := config.Values{"template": "python", "size": "s-4vcpu-8gb"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PresetValues() = %#v, want %#v", got, want)
	}
}
