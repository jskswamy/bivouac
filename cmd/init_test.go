package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/config"
	"github.com/jskswamy/cloudlab/internal/preset"
	"github.com/jskswamy/cloudlab/internal/wizard"
)

// scriptedPrompter stands in for the huh forms. The forms are the one
// part of the flow a test cannot drive, so everything else is decided
// against this: the four entry conditions are all reachable here.
type scriptedPrompter struct {
	// answers are keyed by schema field name.
	answers map[string]any
	// decisions are keyed by meta prompt key.
	decisions map[string]any
	// asked records the fields put to the user, in order.
	asked []string
	// seen records the value each field was pre-filled with.
	seen map[string]any
	// keyChoice is what AskKeys returns, by fingerprint.
	keyChoice []string
	// keyOffer records the offer AskKeys was given.
	keyOffer wizard.KeyOffer
	// keysAsked records whether the SSH key question was reached.
	keysAsked bool
}

func newScript() *scriptedPrompter {
	return &scriptedPrompter{
		answers:   map[string]any{},
		decisions: map[string]any{},
		seen:      map[string]any{},
	}
}

func (s *scriptedPrompter) Ask(q wizard.Question, current any) (any, error) {
	s.asked = append(s.asked, q.Field)
	s.seen[q.Field] = current
	if v, ok := s.answers[q.Field]; ok {
		return v, nil
	}
	return current, nil // unscripted: keep whatever was pre-filled
}

func (s *scriptedPrompter) AskKeys(offer wizard.KeyOffer) ([]string, error) {
	s.keysAsked = true
	s.keyOffer = offer
	if s.keyChoice != nil {
		return s.keyChoice, nil
	}
	// Unscripted: take the offer's own defaults, as a user pressing
	// straight through would.
	var chosen []string
	for _, c := range offer.Choices {
		if c.Selected {
			chosen = append(chosen, c.Fingerprint)
		}
	}
	return chosen, nil
}

func (s *scriptedPrompter) Choose(m meta, options []string, current string) (string, error) {
	v, ok := s.decisions[m.Key]
	if !ok {
		return "", fmt.Errorf("test script has no answer for choice %q (options %v)", m.Key, options)
	}
	return v.(string), nil
}

func (s *scriptedPrompter) Confirm(m meta, current bool) (bool, error) {
	v, ok := s.decisions[m.Key]
	if !ok {
		return false, fmt.Errorf("test script has no answer for confirm %q", m.Key)
	}
	return v.(bool), nil
}

func (s *scriptedPrompter) Input(m meta, current string) (string, error) {
	v, ok := s.decisions[m.Key]
	if !ok {
		return "", fmt.Errorf("test script has no answer for input %q", m.Key)
	}
	return v.(string), nil
}

// initFixture is a temp XDG home plus a temp git repository to run the
// flow in.
type initFixture struct {
	xdg      string
	root     string
	basePath string
	project  string
	out      *bytes.Buffer
}

func newInitFixture(t *testing.T) *initFixture {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	// The SSH key question reads both of these. A developer's own agent
	// or token must not change what these tests see.
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("DIGITALOCEAN_TOKEN", "")

	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	// macOS puts TempDir under a symlinked /var; RepoRoot resolves it,
	// so the fixture has to compare against the resolved path too.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}

	return &initFixture{
		xdg:      xdg,
		root:     resolved,
		basePath: filepath.Join(xdg, "cloudlab", "base.pkl"),
		project:  filepath.Join(resolved, "cloudlab.pkl"),
		out:      &bytes.Buffer{},
	}
}

func (f *initFixture) run(t *testing.T, s *scriptedPrompter) error {
	t.Helper()
	return runInitFlow(context.Background(), f.out, s, f.root)
}

func (f *initFixture) write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func (f *initFixture) values(t *testing.T, path string) config.Values {
	t.Helper()
	v, err := config.ReadValues(context.Background(), path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return v
}

func (f *initFixture) text(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(raw)
}

// Entry condition 1: nothing exists yet. Personal questions are asked
// once, and the two files land in their own places.
func TestInitFlow_FirstRunEver(t *testing.T) {
	f := newInitFixture(t)
	s := newScript()
	s.answers = map[string]any{
		"region":    "nyc3",
		"size":      "s-1vcpu-1gb",
		"template":  "python",
		"agents":    []string{"claude"},
		"packages":  []string{"jq"},
		"beads":     "session",
		"tailscale": false,
	}
	s.decisions = map[string]any{promptSavePreset: false}
	// No agent and no ~/.ssh in the fixture, so the key offer is empty
	// and this stands for the free-text entry it always carries.
	s.keyChoice = []string{"aa:bb:cc"}

	if err := f.run(t, s); err != nil {
		t.Fatalf("runInitFlow() error = %v\n%s", err, f.out)
	}

	wantBase := config.Values{"region": "nyc3", "size": "s-1vcpu-1gb", "sshKeys": []string{"aa:bb:cc"}}
	if got := f.values(t, f.basePath); !reflect.DeepEqual(got, wantBase) {
		t.Errorf("base.pkl = %#v, want %#v", got, wantBase)
	}
	// beads and tailscale were answered at their defaults, so they are
	// not written: the file says what was chosen, not what the schema
	// would have given anyway.
	wantProject := config.Values{"template": "python", "agents": []string{"claude"}, "packages": []string{"jq"}}
	if got := f.values(t, f.project); !reflect.DeepEqual(got, wantProject) {
		t.Errorf("cloudlab.pkl = %#v, want %#v", got, wantProject)
	}
}

// The committed file must never carry the author's key: that is the
// whole reason answers route by nature.
func TestInitFlow_ProjectFileNeverGetsTheSSHKey(t *testing.T) {
	f := newInitFixture(t)
	s := newScript()
	s.answers = map[string]any{
		"region": "nyc3", "size": "s-1vcpu-1gb",
		"template": "python",
	}
	s.decisions = map[string]any{promptSavePreset: false}
	s.keyChoice = []string{"aa:bb:cc"}

	if err := f.run(t, s); err != nil {
		t.Fatalf("runInitFlow() error = %v", err)
	}
	if text := f.text(t, f.project); strings.Contains(text, "aa:bb:cc") {
		t.Errorf("cloudlab.pkl contains the SSH key:\n%s", text)
	}
}

// Entry condition 2, "use": an existing base is summarised and left
// alone, and its questions are not asked again.
func TestInitFlow_ExistingBaseKept(t *testing.T) {
	f := newInitFixture(t)
	f.write(t, f.basePath, "region = \"ams3\"\nsize = \"s-2vcpu-4gb\"\n")
	before := f.text(t, f.basePath)

	s := newScript()
	s.answers = map[string]any{"template": "docker"}
	s.decisions = map[string]any{
		promptPersonalAction: personalUse,
		promptSavePreset:     false,
	}

	if err := f.run(t, s); err != nil {
		t.Fatalf("runInitFlow() error = %v", err)
	}
	if after := f.text(t, f.basePath); after != before {
		t.Errorf("base.pkl was rewritten:\n%s\nwant unchanged:\n%s", after, before)
	}
	for _, field := range s.asked {
		if field == "region" || field == "size" || field == "sshKeys" {
			t.Errorf("personal question %q was asked again", field)
		}
	}
	if !strings.Contains(f.out.String(), "ams3") {
		t.Errorf("summary does not mention the existing region:\n%s", f.out)
	}
}

// Entry condition 2, "change": the personal questions come back
// pre-filled, and fields the flow never asks about survive.
func TestInitFlow_ExistingBaseChanged(t *testing.T) {
	f := newInitFixture(t)
	f.write(t, f.basePath, "region = \"ams3\"\nsize = \"s-2vcpu-4gb\"\npackages {\n  \"git\"\n}\n")

	s := newScript()
	s.answers = map[string]any{"region": "nyc3", "template": "python"}
	s.decisions = map[string]any{
		promptPersonalAction: personalChange,
		promptSavePreset:     false,
	}

	if err := f.run(t, s); err != nil {
		t.Fatalf("runInitFlow() error = %v", err)
	}
	got := f.values(t, f.basePath)
	if got["region"] != "nyc3" {
		t.Errorf("region = %v, want the changed nyc3", got["region"])
	}
	if got["size"] != "s-2vcpu-4gb" {
		t.Errorf("size = %v, want s-2vcpu-4gb kept from the file", got["size"])
	}
	if !reflect.DeepEqual(got["packages"], []string{"git"}) {
		t.Errorf("packages = %v, want base's own packages preserved", got["packages"])
	}
	if s.seen["size"] != "s-2vcpu-4gb" {
		t.Errorf("size was pre-filled with %v, want the file's value", s.seen["size"])
	}
}

// Entry condition 3 with a preset: the preset's fields are stamped into
// the new project file and pre-fill the questions.
func TestInitFlow_StampsAPreset(t *testing.T) {
	f := newInitFixture(t)
	f.write(t, f.basePath, "region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\n")
	if err := preset.Save("k8s", config.Values{
		"template": "docker",
		"agents":   []string{"claude", "codex"},
		"size":     "s-4vcpu-8gb",
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	s := newScript()
	s.decisions = map[string]any{
		promptPersonalAction: personalUse,
		promptPresetChoice:   "k8s",
		promptSavePreset:     false,
	}

	if err := f.run(t, s); err != nil {
		t.Fatalf("runInitFlow() error = %v\n%s", err, f.out)
	}
	got := f.values(t, f.project)
	if got["template"] != "docker" {
		t.Errorf("template = %v, want docker from the preset", got["template"])
	}
	if !reflect.DeepEqual(got["agents"], []string{"claude", "codex"}) {
		t.Errorf("agents = %v, want the preset's", got["agents"])
	}
	// A preset that needs headroom carries its sizing, and stamping it
	// into the project file is how the override reaches provisioning.
	if got["size"] != "s-4vcpu-8gb" {
		t.Errorf("size = %v, want the preset's override stamped in", got["size"])
	}
	if s.seen["template"] != "docker" {
		t.Errorf("template was pre-filled with %v, want the preset's", s.seen["template"])
	}
}

// A preset is stamped, never linked: nothing in the written file may
// point back at the preset store.
func TestInitFlow_StampedPresetIsNotLinked(t *testing.T) {
	f := newInitFixture(t)
	f.write(t, f.basePath, "region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\n")
	if err := preset.Save("shape", config.Values{"template": "docker"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	s := newScript()
	s.decisions = map[string]any{
		promptPersonalAction: personalUse,
		promptPresetChoice:   "shape",
		promptSavePreset:     false,
	}
	if err := f.run(t, s); err != nil {
		t.Fatalf("runInitFlow() error = %v", err)
	}
	text := f.text(t, f.project)
	if strings.Contains(text, "basePath") || strings.Contains(text, "presets") {
		t.Errorf("cloudlab.pkl links to the preset store:\n%s", text)
	}
}

// Entry condition 3 without a preset: "start fresh" asks the project
// questions with nothing pre-filled.
func TestInitFlow_StartFresh(t *testing.T) {
	f := newInitFixture(t)
	f.write(t, f.basePath, "region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\n")
	if err := preset.Save("k8s", config.Values{"template": "docker"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	s := newScript()
	s.answers = map[string]any{"template": "python"}
	s.decisions = map[string]any{
		promptPersonalAction: personalUse,
		promptPresetChoice:   presetStartFresh,
		promptSavePreset:     false,
	}

	if err := f.run(t, s); err != nil {
		t.Fatalf("runInitFlow() error = %v", err)
	}
	if got := f.values(t, f.project); got["template"] != "python" {
		t.Errorf("template = %v, want python", got["template"])
	}
}

// With no presets saved there is nothing to offer, so the choice is not
// put to the user at all.
func TestInitFlow_NoPresetsMeansNoChoice(t *testing.T) {
	f := newInitFixture(t)
	f.write(t, f.basePath, "region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\n")

	s := newScript()
	s.answers = map[string]any{"template": "python"}
	s.decisions = map[string]any{
		promptPersonalAction: personalUse,
		promptSavePreset:     false,
	}
	// promptPresetChoice is deliberately unscripted: reaching it fails.
	if err := f.run(t, s); err != nil {
		t.Fatalf("runInitFlow() error = %v", err)
	}
}

// Entry condition 4: an existing project file is edited, pre-filled,
// and fields the flow never asks about survive untouched.
func TestInitFlow_EditsAnExistingProjectFile(t *testing.T) {
	f := newInitFixture(t)
	f.write(t, f.basePath, "region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\n")
	f.write(t, f.project, strings.Join([]string{
		`template = "python"`,
		`packages {`,
		`  "jq"`,
		`}`,
		`flakes {`,
		`  new Flake {`,
		`    url = "github:foo/bar"`,
		`    packages {`,
		`      "default"`,
		`    }`,
		`    modules = true`,
		`  }`,
		`}`,
	}, "\n")+"\n")

	s := newScript()
	s.answers = map[string]any{"packages": []string{"jq", "ripgrep"}}
	s.decisions = map[string]any{
		promptPersonalAction: personalUse,
		promptSavePreset:     false,
	}

	if err := f.run(t, s); err != nil {
		t.Fatalf("runInitFlow() error = %v\n%s", err, f.out)
	}
	got := f.values(t, f.project)
	if !reflect.DeepEqual(got["packages"], []string{"jq", "ripgrep"}) {
		t.Errorf("packages = %v, want the edited list", got["packages"])
	}
	if got["template"] != "python" {
		t.Errorf("template = %v, want python pre-filled and kept", got["template"])
	}
	if flakes, ok := got["flakes"].([]config.Flake); !ok || len(flakes) != 1 || flakes[0].Url != "github:foo/bar" {
		t.Errorf("flakes = %#v, want the file's own flake preserved", got["flakes"])
	}
	if s.seen["packages"] == nil {
		t.Error("packages was not pre-filled from the file")
	}
}

// A file that predates the split holds personal fields. Accepting the
// move lifts them into base.pkl and takes them out of the committed
// file, which resolves to the same config either way.
func TestInitFlow_MovesPersonalFieldsOnAccept(t *testing.T) {
	f := newInitFixture(t)
	f.write(t, f.project, "region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\ntemplate = \"python\"\nsshKeys {\n  \"aa:bb:cc\"\n}\n")

	s := newScript()
	s.decisions = map[string]any{
		promptMovePersonal: true,
		promptSavePreset:   false,
	}

	if err := f.run(t, s); err != nil {
		t.Fatalf("runInitFlow() error = %v\n%s", err, f.out)
	}

	project := f.values(t, f.project)
	for _, field := range wizard.PersonalFields() {
		if _, ok := project[field]; ok {
			t.Errorf("cloudlab.pkl still declares %q after the move", field)
		}
	}
	base := f.values(t, f.basePath)
	if base["region"] != "nyc3" || base["size"] != "s-1vcpu-1gb" {
		t.Errorf("base.pkl = %#v, want the lifted region and size", base)
	}
	if !reflect.DeepEqual(base["sshKeys"], []string{"aa:bb:cc"}) {
		t.Errorf("base.pkl sshKeys = %v, want the lifted key", base["sshKeys"])
	}
}

// Declining changes nothing about where the fields live: restructuring
// a committed file as a side effect of editing one setting is the thing
// the design rejects.
func TestInitFlow_DecliningTheMoveKeepsTheFieldsInPlace(t *testing.T) {
	f := newInitFixture(t)
	f.write(t, f.project, "region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\ntemplate = \"python\"\nsshKeys {\n  \"aa:bb:cc\"\n}\n")

	s := newScript()
	s.answers = map[string]any{"template": "docker"}
	s.decisions = map[string]any{
		promptMovePersonal: false,
		promptSavePreset:   false,
	}

	if err := f.run(t, s); err != nil {
		t.Fatalf("runInitFlow() error = %v\n%s", err, f.out)
	}
	project := f.values(t, f.project)
	if project["region"] != "nyc3" || project["size"] != "s-1vcpu-1gb" {
		t.Errorf("cloudlab.pkl = %#v, want region and size left where they were", project)
	}
	if !reflect.DeepEqual(project["sshKeys"], []string{"aa:bb:cc"}) {
		t.Errorf("sshKeys = %v, want it left in place", project["sshKeys"])
	}
	if project["template"] != "docker" {
		t.Errorf("template = %v, want the edit applied", project["template"])
	}
	if _, err := os.Stat(f.basePath); !os.IsNotExist(err) {
		t.Errorf("base.pkl was created despite declining the move")
	}
}

// A committed fingerprint is the one the report has to name outright.
func TestInitFlow_NamesACommittedSSHKey(t *testing.T) {
	f := newInitFixture(t)
	f.write(t, f.project, "template = \"python\"\nsshKeys {\n  \"aa:bb:cc\"\n}\n")

	s := newScript()
	s.answers = map[string]any{"region": "nyc3", "size": "s-1vcpu-1gb"}
	s.decisions = map[string]any{promptMovePersonal: false, promptSavePreset: false}
	if err := f.run(t, s); err != nil {
		t.Fatalf("runInitFlow() error = %v", err)
	}
	if !strings.Contains(f.out.String(), "sshKeys") {
		t.Errorf("report does not name sshKeys:\n%s", f.out)
	}
}

// A project file with no personal fields in it has nothing to move, so
// the question is not asked.
func TestInitFlow_NoPersonalFieldsMeansNoMoveQuestion(t *testing.T) {
	f := newInitFixture(t)
	f.write(t, f.basePath, "region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\n")
	f.write(t, f.project, "template = \"python\"\n")

	s := newScript()
	s.decisions = map[string]any{
		promptPersonalAction: personalUse,
		promptSavePreset:     false,
	}
	// promptMovePersonal is deliberately unscripted.
	if err := f.run(t, s); err != nil {
		t.Fatalf("runInitFlow() error = %v", err)
	}
}

func TestInitFlow_SavesAPreset(t *testing.T) {
	f := newInitFixture(t)
	s := newScript()
	s.answers = map[string]any{
		"region": "nyc3", "size": "s-1vcpu-1gb",
		"template": "python", "agents": []string{"claude"}, "beads": "session",
	}
	s.decisions = map[string]any{
		promptSavePreset: true,
		promptPresetName: "go-api",
	}

	if err := f.run(t, s); err != nil {
		t.Fatalf("runInitFlow() error = %v\n%s", err, f.out)
	}
	saved, err := preset.Load(context.Background(), "go-api")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if saved["template"] != "python" {
		t.Errorf("preset template = %v, want python", saved["template"])
	}
	if _, ok := saved["sshKeys"]; ok {
		t.Error("preset captured sshKeys")
	}
	if _, ok := saved["size"]; ok {
		t.Error("preset captured a size that matches base")
	}
}

// The read-back is the only thing that proves the flow did not write
// pkl that will not parse.
func TestInitFlow_ReadsBackWhatItWrote(t *testing.T) {
	f := newInitFixture(t)
	s := newScript()
	s.answers = map[string]any{
		"region": "nyc3", "size": "s-1vcpu-1gb",
		// A quote and a backslash: if the renderer did not escape them
		// the file would not parse, and only the read-back would say so.
		"template": `weird"\ref`,
	}
	s.decisions = map[string]any{promptSavePreset: false}

	if err := f.run(t, s); err != nil {
		t.Fatalf("runInitFlow() error = %v\n%s", err, f.out)
	}
	cfg, err := config.Resolve(context.Background(), f.project)
	if err != nil {
		t.Fatalf("written config does not resolve: %v", err)
	}
	if cfg.Template != `weird"\ref` {
		t.Errorf("Template = %v, want the answer round-tripped", cfg.Template)
	}
}

// Without a terminal the wizard must not open. init and up are both
// covered, and the message has to name the command that fixes it.
func TestRequireTerminal_NamesInitAndOpensNoForm(t *testing.T) {
	err := requireTerminal(false)
	if err == nil {
		t.Fatal("requireTerminal(false) error = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "cloudlab init") {
		t.Errorf("error = %q, want it to name `cloudlab init`", err)
	}
	if err := requireTerminal(true); err != nil {
		t.Errorf("requireTerminal(true) error = %v, want nil", err)
	}
}

func TestInitCmd_RefusesWithoutATerminal(t *testing.T) {
	f := newInitFixture(t)
	cmd := newRootCmd()
	cmd.SetArgs([]string{"init", "--repo", f.root})
	cmd.SetOut(f.out)
	cmd.SetErr(f.out)
	// cobra's default stdin here is os.Stdin, which under `go test` is
	// not a character device -- exactly the non-interactive case.
	err := cmd.Execute()
	if err == nil {
		t.Fatal("cloudlab init error = nil without a terminal, want a refusal")
	}
	if !strings.Contains(err.Error(), "cloudlab init") {
		t.Errorf("error = %q, want it to name `cloudlab init`", err)
	}
	if _, statErr := os.Stat(f.project); !os.IsNotExist(statErr) {
		t.Error("cloudlab.pkl was written despite refusing to run")
	}
}

func TestUpCmd_RefusesWithoutATerminalWhenConfigIsMissing(t *testing.T) {
	f := newInitFixture(t)
	cmd := newRootCmd()
	cmd.SetArgs([]string{"up", "--repo", f.root})
	cmd.SetOut(f.out)
	cmd.SetErr(f.out)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("cloudlab up error = nil with no config and no terminal, want a refusal")
	}
	if !strings.Contains(err.Error(), "cloudlab init") {
		t.Errorf("error = %q, want it to name `cloudlab init`", err)
	}
}

func TestErrNoConfig_IsRecognisedByUp(t *testing.T) {
	dir := t.TempDir()
	_, err := config.Resolve(context.Background(), filepath.Join(dir, "cloudlab.pkl"))
	if err == nil {
		t.Fatal("Resolve() of a missing file error = nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Resolve() of a missing file = %v, want it to wrap os.ErrNotExist so up can tell it apart", err)
	}
}

// A config carries what its author chose. Answers that match the
// schema's own defaults add a line that changes nothing, so they are
// left out.
func TestInitFlow_AcceptedDefaultsAreNotWritten(t *testing.T) {
	f := newInitFixture(t)
	s := newScript()
	s.answers = map[string]any{
		"region": "nyc3", "size": "s-1vcpu-1gb",
		"template":  "python",
		"beads":     "session", // the schema default
		"tailscale": false,     // the schema default
		"agents":    []string{},
		"packages":  []string{},
	}
	s.decisions = map[string]any{promptSavePreset: false}

	if err := f.run(t, s); err != nil {
		t.Fatalf("runInitFlow() error = %v\n%s", err, f.out)
	}
	if got := f.values(t, f.project); !reflect.DeepEqual(got, config.Values{"template": "python"}) {
		t.Errorf("cloudlab.pkl = %#v, want only the template that was chosen", got)
	}
	if text := f.text(t, f.project); strings.Contains(text, "beads") || strings.Contains(text, "tailscale") {
		t.Errorf("cloudlab.pkl restates the schema's defaults:\n%s", text)
	}
}

func TestInitFlow_NonDefaultAnswersAreWritten(t *testing.T) {
	f := newInitFixture(t)
	s := newScript()
	s.answers = map[string]any{
		"region": "nyc3", "size": "s-1vcpu-1gb",
		"template":  "python",
		"beads":     "off",
		"tailscale": true,
	}
	s.decisions = map[string]any{promptSavePreset: false}

	if err := f.run(t, s); err != nil {
		t.Fatalf("runInitFlow() error = %v\n%s", err, f.out)
	}
	got := f.values(t, f.project)
	if got["beads"] != "off" || got["tailscale"] != true {
		t.Errorf("cloudlab.pkl = %#v, want the chosen beads and tailscale", got)
	}
}

// Deleting a line because its value happens to match a default would be
// the same unasked-for restructuring the flow refuses elsewhere.
func TestInitFlow_ADeclaredFieldKeepsItsLineAtTheDefault(t *testing.T) {
	f := newInitFixture(t)
	f.write(t, f.basePath, "region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\n")
	f.write(t, f.project, "template = \"python\"\nbeads = \"off\"\n")

	s := newScript()
	s.answers = map[string]any{"beads": "session"}
	s.decisions = map[string]any{
		promptPersonalAction: personalUse,
		promptSavePreset:     false,
	}

	if err := f.run(t, s); err != nil {
		t.Fatalf("runInitFlow() error = %v\n%s", err, f.out)
	}
	got := f.values(t, f.project)
	if got["beads"] != "session" {
		t.Errorf("beads = %v, want the edit written to the line the file already had", got["beads"])
	}
}
